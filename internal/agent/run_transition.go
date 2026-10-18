package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// runActor is who asks to change a run's pause state.
type runActor int

const (
	// byPerson is the Run tab's pause, resume, and a repeat's extension and finish.
	byPerson runActor = iota + 1
	// byGate is the sign-in gate's closing sweep and its finder of the pauses it persisted (findHolds).
	byGate
	// byHeal is every automatic resume: the heal timer, the rehydrate and chain sweeps, and the gate's release.
	byHeal
	// byOrphanSweep cancels a run a dead process left paused.
	byOrphanSweep
)

// runPhase is the state an actor expects its run in; with the actor it names the request.
type runPhase int

const (
	phaseRunning runPhase = iota + 1
	phasePaused
	// phaseAtCap is a repeat paused at its cap, which a person's finish ends.
	phaseAtCap
)

type runWant struct {
	actor runActor
	from  runPhase
}

var (
	personPause  = runWant{actor: byPerson, from: phaseRunning}
	personResume = runWant{actor: byPerson, from: phasePaused}
	personFinish = runWant{actor: byPerson, from: phaseAtCap}
	gatePause    = runWant{actor: byGate, from: phaseRunning}
	gateFind     = runWant{actor: byGate, from: phasePaused}
	healResume   = runWant{actor: byHeal, from: phasePaused}
	orphanCancel = runWant{actor: byOrphanSweep, from: phasePaused}
)

// runSeen is a run's state as one inspect under its resume lock reads it.
type runSeen int

const (
	// seenUnreadable is a read that failed, did not decode, or answered about another run.
	seenUnreadable runSeen = iota
	seenGone
	seenRunning
	// seenPausing is running with a pause asked, which lands at the step's next boundary.
	seenPausing
	// seenGatePaused is the gate's own landed pause.
	seenGatePaused
	// seenPersonPaused is a person's pause, pending or landed, marotte's park for the user included.
	seenPersonPaused
	// seenRestartPaused is paused because the run's owning process died.
	seenRestartPaused
	// seenInvoluntary is paused for another cause marotte resumes unasked (resumablePause).
	seenInvoluntary
	// seenOtherPause is any other pause, such as a repeat at its cap or a step's ask.
	seenOtherPause
	// seenEnded is any other status.
	seenEnded
)

// runAction is a cell of runTransitions; actNone changes nothing.
type runAction int

const (
	actNone runAction = iota
	// actPause is a person's pause, which replaces any pause the run holds.
	actPause
	// actResume is a person's resume or repeat extension.
	actResume
	// actHoldPause is the gate's reasoned pause, which KAS keeps as the hold.
	actHoldPause
	// actHeal is an automatic resume under the gate's admission and the run's heal budget.
	actHeal
	actCancelOrphan
	// actFinish is a person's finish of a repeat at its cap; KAS refuses one of a node not paused there.
	actFinish
	// actKeepHold acts on the gate's own pause a finder read (keepHold).
	actKeepHold
)

// runTransitions is the one table of the pause and resume verbs marotte writes of a run: a row per request, a cell
// per state its read finds, and a cell left out changes nothing (transitionRefusal says why). A step's answer to an
// ask moves its run on outside it. The gate pauses only a running run, and an automatic resume takes only the
// gate's own pause or one nobody chose, so a person's pause, which replaces the gate's, outlives the gate's release.
var runTransitions = map[runWant]map[runSeen]runAction{
	personPause: {
		seenRunning: actPause, seenPausing: actPause, seenGatePaused: actPause,
		seenRestartPaused: actPause, seenInvoluntary: actPause,
	},
	personResume: {
		seenGatePaused: actResume, seenPersonPaused: actResume, seenRestartPaused: actResume,
		seenInvoluntary: actResume, seenOtherPause: actResume,
	},
	personFinish: {
		seenGatePaused: actFinish, seenPersonPaused: actFinish, seenRestartPaused: actFinish,
		seenInvoluntary: actFinish, seenOtherPause: actFinish,
	},
	gatePause:    {seenRunning: actHoldPause},
	gateFind:     {seenGatePaused: actKeepHold},
	healResume:   {seenGatePaused: actHeal, seenRestartPaused: actHeal, seenInvoluntary: actHeal},
	orphanCancel: {seenRestartPaused: actCancelOrphan},
}

// The refusals a person's request gets when the table changes nothing.
var (
	errRunAlreadyRunning = command.StatusErrorReason(http.StatusConflict, "already_running",
		errors.New("this run is already running, so nothing was resumed"))
	errRunAlreadyPaused = command.StatusErrorReason(http.StatusConflict, "already_paused",
		errors.New("this run is already paused, so nothing was paused"))
	errRunStateUnread = command.StatusError(http.StatusServiceUnavailable,
		errors.New("this run's state could not be read, so nothing was changed. Try again"))
	errRunGone = command.StatusError(http.StatusNotFound, errors.New("run not found"))
)

// runRequest is one request to the table, with what its action needs.
type runRequest struct {
	// resume is a person's resume's params beside the run's id: a repeat's extension.
	resume map[string]any
	// verb names a person's request in its refusal.
	verb string
	// node is a finish's repeat node.
	node string
	// chatID is a heal's launching chat, "" when unknown.
	chatID marotte.ChatID
	want   runWant
	// charged says an earlier try's heal still stands, so a heal claims none.
	charged bool
}

// runChange is what one transition read and the cell it carried out.
type runChange struct {
	seen runSeen
	act  runAction
	// outcome is a heal's; charged whether its heal stands.
	outcome resumeOutcome
	charged bool
}

// transitionHook, when set, runs between a transition's read and its action, under the run's resume lock; an error
// it returns fails the read, as a failed inspect does. A test seam.
var transitionHook atomic.Pointer[func(rs *Runs, workflowID string, want runWant, seen runSeen) error]

// transition is every pause and resume marotte writes of a run, a repeat's extension and finish, the orphan
// sweep's cancel, the gate's finding of its own pause, and the drop of a heal's hold: it reads the run under the
// run's resume lock, which each of them takes here, and carries out runTransitions' cell for the request and what it
// read.
func (rs *Runs) transition(ctx context.Context, workflowID string, req *runRequest) (runChange, error) {
	c := runChange{outcome: resumeFailed, charged: req.charged}
	release, err := rs.resumes.acquire(ctx, workflowID)
	if err != nil {
		return c, err
	}
	defer release()
	r := rs.readPauseState(ctx, workflowID)
	if hook := transitionHook.Load(); hook != nil {
		if hErr := (*hook)(rs, workflowID, req.want, r.seen); hErr != nil {
			r = pauseRead{err: hErr, seen: seenUnreadable}
		}
	}
	c.seen = r.seen
	c.act = runTransitions[req.want][r.seen]
	cErr := rs.carryOut(ctx, workflowID, req, &r, &c)
	if req.want == healResume && (c.outcome == resumeWritten || c.outcome == resumeSkipped) {
		// runGate's doc owns when a heal's hold ends.
		rs.gate.dropHold(workflowID)
	}
	return c, cErr
}

// carryOut is transition's action for the cell c names, under the run's resume lock.
func (rs *Runs) carryOut(ctx context.Context, workflowID string, req *runRequest, r *pauseRead, c *runChange) error {
	switch c.act {
	case actPause:
		params := map[string]any{keyWorkflowID: workflowID}
		userStop("").apply(params)
		return rs.writePause(ctx, workflowID, params)
	case actResume:
		return rs.resumeAsked(ctx, workflowID, req.resume)
	case actHoldPause:
		return rs.holdPause(ctx, workflowID)
	case actHeal:
		c.outcome, c.charged = rs.heal(ctx, workflowID, req)
		return nil
	case actCancelOrphan:
		rs.disarmDeadline(ctx, workflowID)
		return rs.cancelRPC(ctx, workflowID, runStop{})
	case actFinish:
		return rs.finishAsked(ctx, workflowID, req.node)
	case actKeepHold:
		rs.keepHold(workflowID)
		return nil
	}
	if r.seen != seenUnreadable {
		c.outcome = resumeSkipped
	}
	return transitionRefusal(workflowID, req, r)
}

// transitionRefusal says why the table changed nothing: a person's request is answered, the gate retries an
// unreadable run, and the other requests need no answer.
func transitionRefusal(workflowID string, req *runRequest, r *pauseRead) error {
	switch {
	case req.want.actor == byGate && r.seen == seenUnreadable:
		return fmt.Errorf("reading the run: %w", r.err)
	case req.want.actor != byPerson:
		return nil
	}
	switch r.seen {
	case seenUnreadable:
		slog.Warn("a run's state could not be read, so a person's "+req.verb+" changed nothing",
			"workflow_id", workflowID, "error", r.err)
		return errRunStateUnread
	case seenGone:
		return errRunGone
	case seenEnded:
		return command.StatusError(http.StatusConflict, errors.New(verbUnavailableText(req.verb, string(r.res.State.Status))))
	case seenRunning, seenPausing:
		return errRunAlreadyRunning
	default:
		return errRunAlreadyPaused
	}
}

// pauseRead is one read of a run, classified.
type pauseRead struct {
	err  error
	res  inspectRunState
	seen runSeen
}

func (rs *Runs) readPauseState(ctx context.Context, workflowID string) pauseRead {
	res, err := rs.readInspect(ctx, workflowID)
	switch {
	case workflowNotFound(err):
		return pauseRead{err: err, seen: seenGone}
	case err != nil:
		return pauseRead{err: err, seen: seenUnreadable}
	case !res.names(workflowID):
		named := cmp.Or(res.WorkflowID, res.State.WorkflowID)
		return pauseRead{err: fmt.Errorf("the reply names run %q, not %q", named, workflowID), seen: seenUnreadable}
	}
	return pauseRead{res: res, seen: res.seen()}
}

func (r *inspectRunState) seen() runSeen {
	st := &r.State
	switch {
	case st.Status == marotte.RunStatusRunning && st.PausePending != nil:
		return seenPausing
	case st.Status == marotte.RunStatusRunning:
		return seenRunning
	case st.Status != marotte.RunStatusPaused:
		return seenEnded
	case r.heldForSignIn():
		return seenGatePaused
	case r.userParked():
		return seenPersonPaused
	case st.PauseReason == stalePauseReason:
		return seenRestartPaused
	case resumablePause(st.PauseReason, st.PauseDetail):
		return seenInvoluntary
	default:
		return seenOtherPause
	}
}

// writePause writes every pause of a run, positioned as stopVerb positions one.
func (rs *Runs) writePause(ctx context.Context, workflowID string, params map[string]any) error {
	return rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		return rs.stopVerb(cctx, workflowID, methodKiroWorkflowPause, params)
	})
}

// sendResume writes every resume of a run, on its launching session, under the admission a.
func (rs *Runs) sendResume(ctx context.Context, a runAdmission, host *drivenHost, workflowID string, extra map[string]any) error {
	params := map[string]any{keyWorkflowID: workflowID}
	maps.Copy(params, extra)
	return rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		_, err := rs.write(cctx, a, host.sb, workflowID, methodKiroWorkflowResume, params, mutationDrive)
		return err
	})
}

// resumeAsked is a person's resume, refused while the gate is closed.
func (rs *Runs) resumeAsked(ctx context.Context, workflowID string, extra map[string]any) error {
	a, err := rs.admitRunWork(refuseClosed, workflowID, "")
	if err != nil {
		return err
	}
	defer a.settle(workflowID)
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		return err
	}
	if err := rs.sendResume(ctx, a, host, workflowID, extra); err != nil {
		return err
	}
	rs.armDeadline(ctx, workflowID)
	return nil
}

// finishAsked is a person's finish of the repeat at nodeID, refused while the gate is closed (drive admits it).
func (rs *Runs) finishAsked(ctx context.Context, workflowID, nodeID string) error {
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		return err
	}
	resp, err := host.call(ctx, rs, workflowID, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"action":      updateStatusAction,
		"status":      runStepCompleted,
		"nodeId":      nodeID,
	})
	if err != nil {
		return err
	}
	if refusal := stepStatusRefusal(resp); refusal != "" {
		return fmt.Errorf("%w: %s", errStepStatusRefused, refusal)
	}
	rs.armDeadline(ctx, workflowID)
	return nil
}

// holdPause writes the gate's reasoned pause of a running run, whose reason KAS keeps as the run's stopReason;
// errGateOpened when the gate opened first.
func (rs *Runs) holdPause(ctx context.Context, workflowID string) error {
	open, m := rs.gate.now()
	rs.applyGateMove(m)
	if open {
		return errGateOpened
	}
	params := map[string]any{keyWorkflowID: workflowID, "initiator": initiatorUser, "reason": holdReasonFor(m.to)}
	if err := rs.writePause(ctx, workflowID, params); err != nil {
		return err
	}
	slog.Info("sign-in gate: paused a running run until the agent's sign-in can be answered",
		"workflow_id", workflowID, "answerability", m.to.String())
	return nil
}

// heal resumes a run under the gate's admission and the run's heal budget, holding it while the gate is closed and
// parking it for the user once the budget is spent. It reports whether a heal stands after it.
func (rs *Runs) heal(ctx context.Context, workflowID string, req *runRequest) (resumeOutcome, bool) {
	charged := req.charged
	a, aErr := rs.admitRunWork(holdClosed, workflowID, req.chatID)
	if aErr != nil {
		return resumeHeld, charged
	}
	claimed := false
	if !charged {
		attempts, ok := rs.claimHeal(workflowID)
		if !ok {
			slog.Warn("a run keeps pausing for a cause nobody chose; leaving it paused for a resume by hand",
				"workflow_id", workflowID, "chat_id", req.chatID, "attempts", attempts)
			if err := rs.parkForUser(ctx, workflowID); err != nil {
				slog.Warn("a run marotte no longer resumes still reads as resuming automatically",
					"workflow_id", workflowID, "chat_id", req.chatID, "error", err)
				return resumeFailed, false
			}
			return resumeSkipped, false
		}
		claimed = true
	}
	wrote, wErr := rs.writeHealResume(ctx, a, req.chatID, workflowID)
	if claimed && !wrote {
		rs.refundHeal(workflowID)
		claimed = false
	}
	charged = charged || claimed
	if wErr != nil {
		return resumeFailed, charged
	}
	return resumeWritten, charged
}

// parkForUser stamps a paused run marotte no longer resumes as the user's to resume: KAS keeps a pause asked of a
// paused run that is not executing as its stopReason and starts nothing, so the run reads manualResumeReason instead
// of a promise of an automatic resume, and reads as a person's pause from then on.
func (rs *Runs) parkForUser(ctx context.Context, workflowID string) error {
	return rs.writePause(ctx, workflowID,
		map[string]any{keyWorkflowID: workflowID, "initiator": initiatorUser, "reason": manualResumeReason})
}

// writeHealResume writes the resume a admitted and settles a, reporting whether it may have reached KAS and its
// failure, which is logged here.
func (rs *Runs) writeHealResume(ctx context.Context, a runAdmission, chatID marotte.ChatID, workflowID string) (wrote bool, err error) {
	defer a.settle(workflowID)
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		slog.Warn("rehydrate: the run's launching session could not be made resident, so it was not resumed",
			"workflow_id", workflowID, "chat_id", chatID, "error", err)
		return false, err
	}
	cErr := rs.sendResume(ctx, a, host, workflowID, nil)
	switch {
	case cErr == nil:
	case isStatusRefusal(cErr):
		slog.Info("rehydrate: marotte has a send to this run in flight, so the resume waits for the next sweep",
			"workflow_id", workflowID, "chat_id", chatID, "error", cErr)
		return false, cErr
	case errors.Is(cErr, workflow.ErrJustClaimed):
		// KAS queues a second claim on one engine and refuses only after the first overstayed its claim TTL.
		slog.Info("rehydrate: another verb held this run's claim past its TTL, so the heal left it to that one",
			"workflow_id", workflowID, "chat_id", chatID)
		return true, cErr
	default:
		slog.Warn("rehydrate: resume failed", "workflow_id", workflowID, "chat_id", chatID, "error", cErr)
		return true, cErr
	}
	// Arm here: this path calls the verb directly, so only the `run_start` frame would otherwise cover it.
	rs.armDeadline(ctx, workflowID)
	slog.Info("rehydrate: resumed restart-paused run", "workflow_id", workflowID, "chat_id", chatID)
	return true, nil
}
