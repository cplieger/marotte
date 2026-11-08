package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// runActor is who asks to change a run's pause state.
type runActor int

const (
	// byPerson is the Run tab's pause, resume, a repeat's extension and finish, and a paused step's end.
	byPerson runActor = iota + 1
	// byGate is the sign-in gate's closing sweep and its finder of the pauses it persisted (findHolds).
	byGate
	// byHeal is every automatic resume: the heal timer, the rehydrate and chain sweeps, and the gate's release.
	byHeal
	// byOrphanSweep cancels a run a dead process left paused.
	byOrphanSweep
	// byUnattended ends a scheduled run's pause that nobody resumed within its budget (watchUnattendedPause).
	byUnattended
)

// runPhase is the state an actor expects its run in; with the actor it names the request.
type runPhase int

const (
	phaseRunning runPhase = iota + 1
	phasePaused
	// phaseAtCap is a repeat paused at its cap, which a person's finish ends.
	phaseAtCap
	// phaseStepPaused is a paused step, which a person's end finishes whether or not its siblings still run.
	phaseStepPaused
)

type runWant struct {
	actor runActor
	from  runPhase
}

var (
	personPause  = runWant{actor: byPerson, from: phaseRunning}
	personResume = runWant{actor: byPerson, from: phasePaused}
	personFinish = runWant{actor: byPerson, from: phaseAtCap}
	personEnd    = runWant{actor: byPerson, from: phaseStepPaused}
	gatePause    = runWant{actor: byGate, from: phaseRunning}
	gateFind     = runWant{actor: byGate, from: phasePaused}
	healResume   = runWant{actor: byHeal, from: phasePaused}
	orphanCancel = runWant{actor: byOrphanSweep, from: phasePaused}
	pauseExpiry  = runWant{actor: byUnattended, from: phasePaused}
)

// runSeen is a run's state as one inspect under its resume lock reads it.
type runSeen int

const (
	// seenUnreadable is a read that failed, did not decode, or answered about another run.
	seenUnreadable runSeen = iota
	seenGone
	seenRunning
	// seenPausing is running with a pause asked, while KAS cancels the in-flight turn.
	seenPausing
	// seenGatePaused is the gate's own landed pause.
	seenGatePaused
	// seenPersonPaused is a person's pause, pending or landed.
	seenPersonPaused
	// seenRestartPaused is paused because the run's owning process died.
	seenRestartPaused
	// seenAskPaused is paused, by nobody marotte names, on steps that each wait on a person (workflow.PausedOnAsks),
	// which KAS's resume parks again at once.
	seenAskPaused
	// seenThrottledAsk is seenAskPaused where the run log records each asking step's own turn failing on a transient
	// model error since it asked (Runs.throttledAsks): the question is stale, and only an answer continues the step.
	seenThrottledAsk
	// seenInvoluntary is paused for another cause marotte resumes unasked (resumablePause).
	seenInvoluntary
	// seenAutoStopped is a pause an automatic resume would take (healResume's row) on a run marotte stopped resuming
	// on its own (Runs.stopAutoResume).
	seenAutoStopped
	// seenOtherPause is any other pause, such as a repeat at its cap.
	seenOtherPause
	// seenEnded is any other status.
	seenEnded
)

// runAction is a cell of runTransitions; the zero value, every cell the table leaves out, changes nothing.
type runAction int

const (
	_ runAction = iota
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
	// actEndUnattended ends a scheduled run paused with nobody to resume it (endUnattended).
	actEndUnattended
	// actEndStep is a person's end of the paused step the read holds at the request's path (endStepAsked).
	actEndStep
)

// runTransitions is the one table of the pause and resume verbs marotte writes of a run: a row per request, a cell
// per state its read finds, and a cell left out changes nothing (transitionRefusal says why). A step's answer to an
// ask moves its run on outside it. The gate pauses only a running run, and an automatic resume takes only the
// gate's own pause or one nobody chose, so a person's pause, which replaces the gate's, outlives the gate's release.
var runTransitions = map[runWant]map[runSeen]runAction{
	personPause: {
		seenRunning: actPause, seenPausing: actPause, seenGatePaused: actPause,
		seenRestartPaused: actPause, seenThrottledAsk: actPause, seenInvoluntary: actPause, seenAutoStopped: actPause,
	},
	personResume: {
		seenGatePaused: actResume, seenPersonPaused: actResume, seenRestartPaused: actResume,
		seenThrottledAsk: actResume, seenInvoluntary: actResume, seenAutoStopped: actResume, seenOtherPause: actResume,
	},
	personFinish: {
		seenGatePaused: actFinish, seenPersonPaused: actFinish, seenRestartPaused: actFinish,
		seenAskPaused: actFinish, seenThrottledAsk: actFinish, seenInvoluntary: actFinish, seenAutoStopped: actFinish,
		seenOtherPause: actFinish,
	},
	personEnd: {
		seenRunning: actEndStep, seenPausing: actEndStep, seenGatePaused: actEndStep, seenPersonPaused: actEndStep,
		seenRestartPaused: actEndStep, seenAskPaused: actEndStep, seenThrottledAsk: actEndStep, seenInvoluntary: actEndStep,
		seenAutoStopped: actEndStep, seenOtherPause: actEndStep,
	},
	gatePause: {seenRunning: actHoldPause},
	gateFind:  {seenGatePaused: actKeepHold},
	healResume: {
		seenGatePaused: actHeal, seenRestartPaused: actHeal, seenThrottledAsk: actHeal, seenInvoluntary: actHeal,
	},
	orphanCancel: {seenRestartPaused: actCancelOrphan},
	pauseExpiry: {
		seenRestartPaused: actEndUnattended, seenThrottledAsk: actEndUnattended, seenInvoluntary: actEndUnattended,
		seenAutoStopped: actEndUnattended, seenOtherPause: actEndUnattended,
	},
}

// The refusals a person's request gets when the table changes nothing.
var (
	errRunAlreadyRunning = command.StatusErrorReason(http.StatusConflict, "already_running",
		errors.New("this run is already running, so nothing was resumed"))
	errRunAlreadyPaused = command.StatusErrorReason(http.StatusConflict, "already_paused",
		errors.New("this run is already paused, so nothing was paused"))
	errRunStateUnread = command.StatusError(http.StatusServiceUnavailable,
		errors.New("this run's state could not be read, so nothing was changed. Try again"))
	errRunGone           = command.StatusError(http.StatusNotFound, errors.New("run not found"))
	errRunAwaitingAnswer = command.StatusErrorReason(http.StatusConflict, "awaiting_answer",
		errors.New("this run is waiting for an answer to a step's question; resuming would only park it again, so nothing was resumed"))
)

// runRequest is one request to the table, with what its action needs.
type runRequest struct {
	// resume is a person's resume's params beside the run's id: a repeat's extension.
	resume map[string]any
	// verb names a person's request in its refusal.
	verb string
	// node is a finish's repeat node.
	node string
	// path is an end's step, one workflow.PathKey, and status the status the end writes.
	path   string
	status string
	// chatID is a heal's launching chat, "" when unknown.
	chatID marotte.ChatID
	want   runWant
	// charged says an earlier try's heal still stands, so a heal claims none.
	charged bool
	// caller is who sent a person's request.
	caller controlCaller
}

// resumeParams is a person's resume's params beside the run's id. Only an API caller's is attributed: the page's
// resume names no initiator, so KAS's `run_start` carries none and the client words no resume.
func (req *runRequest) resumeParams() map[string]any {
	if req.caller != callerAPI {
		return req.resume
	}
	params := map[string]any{}
	req.caller.stop().apply(params)
	maps.Copy(params, req.resume)
	return params
}

// runChange is the cell one transition carried out.
type runChange struct {
	act runAction
	// outcome is a heal's; charged whether its heal stands.
	outcome resumeOutcome
	charged bool
}

// transitionHook, when set, runs between a transition's read and its action, under the run's resume lock; an error
// it returns fails the read, as a failed inspect does. A test seam.
var transitionHook atomic.Pointer[func(rs *Runs, workflowID string, want runWant, seen runSeen) error]

// transition is every pause and resume marotte writes of a run, a repeat's extension and finish, a paused step's
// end, the orphan sweep's cancel, the gate's finding of its own pause, and the drop of a heal's hold: it reads the run under the
// run's resume lock, which each of them takes here, and carries out runTransitions' cell for the request and what it
// read.
func (rs *Runs) transition(ctx context.Context, workflowID string, req *runRequest) (runChange, error) {
	c := runChange{outcome: resumeFailed, charged: req.charged}
	release, err := rs.resumes.Lock(ctx, workflowID)
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
		req.caller.stop().apply(params)
		if err := rs.writePause(ctx, workflowID, params); err != nil {
			return err
		}
		if rs.endAutoResumeStop(ctx, workflowID) {
			// The run stays paused, and KAS's frame for this pause can reach the run view before the clear.
			rs.announcePaused(ctx, workflowID)
		}
		return nil
	case actResume:
		if len(r.throttled) > 0 {
			_, err := rs.answerThrottled(ctx, workflowID, r.throttled, req.caller.words())
			return rs.endAutoResumeStopAfter(ctx, workflowID, err)
		}
		return rs.endAutoResumeStopAfter(ctx, workflowID, rs.resumeAsked(ctx, workflowID, req.resumeParams()))
	case actHoldPause:
		return rs.holdPause(ctx, workflowID)
	case actHeal:
		c.outcome, c.charged = rs.heal(ctx, workflowID, req, r)
		return nil
	case actCancelOrphan:
		rs.disarmDeadline(ctx, workflowID)
		return rs.cancelRPC(ctx, workflowID, runStop{})
	case actFinish:
		return rs.finishAsked(ctx, workflowID, req.node)
	case actKeepHold:
		rs.keepHold(workflowID)
		return nil
	case actEndUnattended:
		return rs.endUnattended(ctx, workflowID, req, r)
	case actEndStep:
		return rs.endStepAsked(ctx, workflowID, req, r)
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
	case seenAskPaused:
		if req.want == personResume {
			return errRunAwaitingAnswer
		}
		return errRunAlreadyPaused
	default:
		return errRunAlreadyPaused
	}
}

// pauseRead is one read of a run, classified, and its tree as workflow.DecodeInspect reads it.
type pauseRead struct {
	err  error
	res  inspectRunState
	tree workflow.Inspect
	// transientCode is the first asking step's error class of a seenThrottledAsk read, and throttled the asking
	// steps' paths; both are kept when seenAutoStopped overrides it.
	transientCode string
	throttled     []string
	seen          runSeen
}

func (rs *Runs) readPauseState(ctx context.Context, workflowID string) pauseRead {
	res, raw, err := rs.readInspect(ctx, workflowID)
	switch {
	case workflowNotFound(err):
		return pauseRead{err: err, seen: seenGone}
	case err != nil:
		return pauseRead{err: err, seen: seenUnreadable}
	case !res.names(workflowID):
		named := cmp.Or(res.WorkflowID, res.State.WorkflowID)
		return pauseRead{err: fmt.Errorf("the reply names run %q, not %q", named, workflowID), seen: seenUnreadable}
	}
	tree, dErr := workflow.DecodeInspect(workflowID, raw)
	r := pauseRead{tree: tree, res: res, seen: res.seen(asksOf(tree))}
	if r.seen == seenAskPaused {
		if r.throttled, r.transientCode = rs.throttledAsks(workflowID, tree); r.throttled != nil {
			r.seen = seenThrottledAsk
		}
	}
	if _, heals := runTransitions[healResume][r.seen]; heals && rs.autoResumeStopped(workflowID) {
		r.seen = seenAutoStopped
	}
	if r.seen == seenUnreadable {
		r.err = dErr
	}
	return r
}

// throttledAsks answers the paths of tree's asking steps, and the first one's error class, when the run log records
// each one's turn failing on a transient model error after its question was answered in that turn; nil otherwise.
// KAS 3.1.0 pauses such a failure on the answered question, with no pauseDetail and none of its in-step retries
// (acp-server.js executeNode, withTransientRetry). An unanswered question stays the step's to have answered.
func (rs *Runs) throttledAsks(workflowID string, tree workflow.Inspect) (paths []string, code string) {
	if rs.log == nil {
		return nil, ""
	}
	for _, st := range workflow.ParkedSteps(tree) {
		path := workflow.PathKey(st.Path)
		c, ok := rs.log.transientFailure(workflowID, path)
		if !ok {
			return nil, ""
		}
		if !rs.log.answeredAsk(workflowID, path) {
			slog.Info("a step's turn failed on a model error before its question was answered, so the run waits for the answer",
				"workflow_id", scrubLog(workflowID), "node_path", scrubLog(path), "failed_code", scrubLog(c))
			return nil, ""
		}
		paths = append(paths, path)
		code = cmp.Or(code, c)
	}
	return paths, code
}

// seen classifies the read; asks is the ask state of the same reply, consulted only for a pause that is not the
// gate's, a person's or a restart's.
func (r *inspectRunState) seen(asks askState) runSeen {
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
	case asks == asksUnread:
		return seenUnreadable
	case asks == asksAll:
		return seenAskPaused
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

// sendResume writes every resume of a run, on its launching session, under the admission a. A resume whose answer
// could not be read may have re-driven the run, so it arms the deadline as a resume that took; a caller arms on nil.
func (rs *Runs) sendResume(ctx context.Context, a runAdmission, host *drivenHost, workflowID string, extra map[string]any) error {
	params := map[string]any{keyWorkflowID: workflowID}
	maps.Copy(params, extra)
	err := rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		_, err := rs.write(cctx, a, host.sb, workflowID, methodKiroWorkflowResume, params, mutationDrive)
		return err
	})
	if marotte.ReplyUnread(err) {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// resumeAsked is a person's resume, refused while the gate is closed.
func (rs *Runs) resumeAsked(ctx context.Context, workflowID string, extra map[string]any) error {
	a, err := rs.admitRunWork(refuseClosed, workflowID, "")
	if err != nil {
		return err
	}
	defer a.settle(workflowID)
	host, release, err := rs.ensureResident(ctx, workflowID)
	defer release()
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
	host, release, err := rs.ensureResident(ctx, workflowID)
	defer release()
	if err != nil {
		return err
	}
	return rs.updateNodeStatus(ctx, host, workflowID, nodeID, runStepCompleted)
}

// holdPause writes the gate's reasoned pause of a running run, whose reason KAS keeps as the run's stopReason;
// errGateOpened when the gate opened first.
func (rs *Runs) holdPause(ctx context.Context, workflowID string) error {
	open, m := rs.gate.now()
	rs.applyGateMove(m)
	if open {
		return errGateOpened
	}
	// "user" though no person asked: KAS's pause takes no other initiator (kirodotdev/Kiro#12274), so the reason
	// names marotte.
	params := map[string]any{keyWorkflowID: workflowID, "initiator": initiatorUser, "reason": holdReasonFor(m.to)}
	if err := rs.writePause(ctx, workflowID, params); err != nil {
		return err
	}
	slog.Info("sign-in gate: paused a running run until the agent's sign-in can be answered",
		"workflow_id", workflowID, "answerability", m.to.String())
	return nil
}

// heal resumes a run under the gate's admission and the run's heal budget, holding it while the gate is closed and,
// once the budget is spent, stopping its automatic resumes or ending a scheduled run, which has nobody to resume it.
// It reports whether a heal stands after it.
func (rs *Runs) heal(ctx context.Context, workflowID string, req *runRequest, r *pauseRead) (resumeOutcome, bool) {
	charged := req.charged
	a, aErr := rs.admitRunWork(holdClosed, workflowID, req.chatID)
	if aErr != nil {
		return resumeHeld, charged
	}
	claimed := false
	if !charged {
		attempts, ok := rs.claimHeal(workflowID)
		if !ok {
			return rs.healSpent(ctx, workflowID, req, attempts, r), false
		}
		claimed = true
	}
	var wrote bool
	var wErr error
	if len(r.throttled) > 0 {
		wrote, wErr = rs.writeHealAnswer(ctx, a, req.chatID, workflowID, r.throttled)
	} else {
		wrote, wErr = rs.writeHealResume(ctx, a, req.chatID, workflowID)
	}
	if claimed && !wrote {
		rs.refundHeal(workflowID)
		claimed = false
	}
	if claimed {
		// A claim another verb held past its TTL is that verb's write, and an unread answer may have taken: neither is
		// a failed one of marotte's.
		rs.noteHealWrite(workflowID, wErr != nil && !errors.Is(wErr, workflow.ErrJustClaimed) && !marotte.ReplyUnread(wErr))
	}
	charged = charged || claimed
	if wErr != nil {
		return resumeFailed, charged
	}
	return resumeWritten, charged
}

// healWritesSpentLog is healSpent's ERROR when marotte's own failed resume writes spent the budget.
const healWritesSpentLog = "marotte's resumes of a paused run failed until its heal budget was spent; " +
	"it stopped resuming it, so it waits for a resume by hand"

// healSpent is heal past the run's budget: a scheduled run is ended, since nobody would resume it, and any other is
// left paused under marotte's own record that it stopped resuming it, with no pause written: KAS would attribute one
// to the user. A run left paused stays so, since KAS resumes a paused run only when asked.
func (rs *Runs) healSpent(
	ctx context.Context, workflowID string, req *runRequest, attempts int, r *pauseRead,
) resumeOutcome {
	if l, held := rs.lease(workflowID); held && l.Unattended {
		if err := rs.endUnattended(ctx, workflowID, req, r); err != nil {
			return resumeFailed
		}
		return resumeSkipped
	}
	if rs.healWritesFailed(workflowID) {
		slog.Error(healWritesSpentLog, "workflow_id", workflowID, "chat_id", req.chatID, "attempts", attempts)
	} else {
		slog.Warn("a run keeps pausing for a cause nobody chose; marotte stopped resuming it, so it waits for a resume by hand",
			"workflow_id", workflowID, "chat_id", req.chatID, "attempts", attempts)
	}
	throttled := throttledPause(r.res.State.PauseDetail) || strings.Contains(strings.ToLower(r.transientCode), "throttl")
	if err := rs.stopAutoResume(ctx, workflowID, throttled); err != nil {
		// On a failed write the store keeps the stamp in memory, so only a restart loses it. With no lease, the spent
		// budget alone stops this process's resumes.
		slog.Warn("a run marotte stopped resuming could not be recorded as such",
			"workflow_id", workflowID, "chat_id", req.chatID, "error", err)
	}
	return resumeSkipped
}

// writeHealResume writes the resume a admitted and settles a, reporting whether it may have reached KAS and its
// failure, which is logged here.
func (rs *Runs) writeHealResume(ctx context.Context, a runAdmission, chatID marotte.ChatID, workflowID string) (wrote bool, err error) {
	defer a.settle(workflowID)
	host, release, err := rs.ensureResident(ctx, workflowID)
	defer release()
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
	case marotte.ReplyUnread(cErr):
		slog.Warn("rehydrate: the resume was sent but its answer could not be read",
			"workflow_id", workflowID, "chat_id", chatID, "error", cErr)
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

// healContinueText is KAS's own continuation of a step whose turn was interrupted (acp-server.js zlr, KAS 3.1.0),
// sent as the answer that continues a step paused on a stale question.
const healContinueText = `Continue - your previous turn was interrupted before it finished. Pick up where you left off. ` +
	`When the step's assigned work is complete, call send_message with sessionId "parent" and severity "success"; ` +
	`if you need user input, "warning"; if the work has failed, "error".`

// healCaller is who an automatic answer's words are from, in the step's transcript.
var healCaller = &marotte.Caller{Label: "marotte"}

// writeHealAnswer is writeHealResume for a seenThrottledAsk read: KAS continues a step paused on a question only when
// a message resumes it (acp-server.js executeNode, KAS 3.1.0), and a plain resume parks it again. It answers each
// question at paths and settles a, reporting whether an answer may have reached KAS and the failure, logged here.
func (rs *Runs) writeHealAnswer(
	ctx context.Context, a runAdmission, chatID marotte.ChatID, workflowID string, paths []string,
) (wrote bool, err error) {
	defer a.settle(workflowID)
	wrote, err = rs.answerThrottled(ctx, workflowID, paths, healCaller)
	if err != nil {
		slog.Warn("rehydrate: the question of a step a model error stopped could not be answered",
			"workflow_id", workflowID, "chat_id", chatID, "error", err)
		return wrote, err
	}
	slog.Info("rehydrate: answered the stale question of a step a model error stopped, which continues it",
		"workflow_id", workflowID, "chat_id", chatID)
	return true, nil
}

// answerThrottled answers the question at each path with healContinueText, from from (nil for the user's own), through
// the step message path; wrote is whether any answer may have reached KAS.
func (rs *Runs) answerThrottled(ctx context.Context, workflowID string, paths []string, from *marotte.Caller) (wrote bool, err error) {
	var errs []error
	for _, path := range paths {
		_, sErr := rs.messageStep(ctx, workflowID, path, healContinueText, ids.NewMessageID(), "", from)
		wrote = wrote || sErr == nil || errors.Is(sErr, errStepUnconfirmed)
		if sErr != nil {
			errs = append(errs, sErr)
		}
	}
	return wrote, errors.Join(errs...)
}
