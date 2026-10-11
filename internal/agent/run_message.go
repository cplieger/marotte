package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// stepAddr is one step EXECUTION. A repeat's iterations share a node id, so the path and the session
// are what tell them apart, from the ask match to the record.
type stepAddr struct {
	workflowID string
	nodePath   string
	session    string
}

type stepNow struct {
	at     stepAddr
	status marotte.RunNodeStatus
	run    marotte.RunStatus
}

type runNow struct {
	workflowID string
	run        marotte.RunStatus
	steps      []workflow.StepSession
}

// errStepUnreadable wraps a failed inspect, which the route grades the way the run read does.
var errStepUnreadable = errors.New("the run's state could not be read")

func (rs *Runs) readRun(ctx context.Context, workflowID string) (runNow, error) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return runNow{}, fmt.Errorf("%w: %w", errStepUnreadable, err)
	}
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return runNow{}, errRunStateUndecodable
	}
	return runNow{workflowID: workflowID, run: marotte.RunStatus(res.State.Status), steps: workflow.Steps(res.State)}, nil
}

func (n runNow) addrOf(st workflow.StepSession) stepAddr {
	return stepAddr{workflowID: n.workflowID, nodePath: workflow.PathKey(st.Path), session: st.SessionID}
}

func (n runNow) step(nodePath string) (stepNow, bool) {
	for _, st := range n.steps {
		if at := n.addrOf(st); at.nodePath == nodePath {
			return stepNow{at: at, status: marotte.RunNodeStatus(st.Status), run: n.run}, true
		}
	}
	return stepNow{}, false
}

// askedStep resolves the execution an ask belongs to: the step running its session, else the one paused
// step its node id (or, naming none, the run) leaves. matches > 1 is an ask no execution can claim.
func (n runNow) askedStep(p *marotte.RunInputNeededPayload) (st workflow.StepSession, matches int) {
	if p.StepSessionID != "" {
		for _, s := range n.steps {
			if s.SessionID == p.StepSessionID {
				return s, 1
			}
		}
	}
	for _, s := range n.steps {
		if marotte.RunNodeStatus(s.Status) == marotte.RunNodeStatusPaused && (p.NodeID == "" || s.NodeID == p.NodeID) {
			st = s
			matches++
		}
	}
	return st, matches
}

func (n runNow) anyPaused() bool {
	for _, s := range n.steps {
		if marotte.RunNodeStatus(s.Status) == marotte.RunNodeStatusPaused {
			return true
		}
	}
	return false
}

// A paused step on a run whose loop still runs (a parallel sibling) is busy: KAS's reroute declines then and the prompt would run as a
// turn no run owns.
func stepMessageVerb(s *stepNow, asked bool) (marotte.RunStepMessageVerb, marotte.RunStepMessageRefusal) {
	switch {
	case s.run.Terminal():
		return "", marotte.RunStepFinished
	case s.status == marotte.RunNodeStatusCompleted, s.status == marotte.RunNodeStatusFailed,
		s.status == marotte.RunNodeStatusAborted, s.status == marotte.RunNodeStatusSkipped:
		return "", marotte.RunStepFinished
	case s.at.session == "", s.status == marotte.RunNodeStatusPending:
		if s.run == marotte.RunStatusPaused {
			return "", marotte.RunStepRunPaused
		}
		return "", marotte.RunStepNotStarted
	case s.status == marotte.RunNodeStatusRunning:
		return marotte.RunStepMessageSteer, ""
	case s.status == marotte.RunNodeStatusPaused && asked:
		return marotte.RunStepMessageAnswer, ""
	case s.status == marotte.RunNodeStatusPaused && s.run == marotte.RunStatusPaused:
		return marotte.RunStepMessagePrompt, ""
	}
	return "", marotte.RunStepBusy
}

var stepRefusalText = map[marotte.RunStepMessageRefusal]string{
	marotte.RunStepNotStarted: "this step has not started yet",
	marotte.RunStepFinished:   "this step has finished, so it cannot take a message",
	marotte.RunStepRunPaused:  "the run is paused; resume it to message this step",
	marotte.RunStepBusy:       "this step cannot take a message right now; try again in a moment",
	marotte.RunStepFull:       "too many unread messages are waiting for this step",
}

func stepRefusal(r marotte.RunStepMessageRefusal) error {
	return command.StatusErrorReason(http.StatusConflict, string(r), errors.New(stepRefusalText[r]))
}

type stepDecision struct {
	prompt *promptSend
	now    stepNow
	verb   marotte.RunStepMessageVerb
}

func (rs *Runs) readStep(ctx context.Context, workflowID, nodePath string) (runNow, stepNow, error) {
	n, err := rs.readRun(ctx, workflowID)
	if err != nil {
		return runNow{}, stepNow{}, err
	}
	now, ok := n.step(nodePath)
	if !ok {
		return runNow{}, stepNow{}, errStepUnknown
	}
	return n, now, nil
}

func (rs *Runs) preRefuse(ctx context.Context, workflowID, nodePath string) error {
	n, now, err := rs.readStep(ctx, workflowID, nodePath)
	if err != nil {
		return err
	}
	if _, refuse := stepMessageVerb(&now, rs.asks.askAt(n, now.at)); refuse != "" {
		return stepRefusal(refuse)
	}
	return nil
}

// messageStep delivers the user's words to the step at nodePath by the verb its live state calls for.
// A refusal is a StatusErrorReason carrying a RunStepMessageRefusal.
//
// The verb is decided from a fresh inspect under the run's message gate, so two sends to one paused
// step cannot both prompt it. A prompt or an answer is sent under the gate; a steer is routed after
// it, and its delivery re-checks under the gate that the step's turn is still open.
func (rs *Runs) messageStep(ctx context.Context, workflowID, nodePath, raw, messageID string) (out marotte.RunStepMessageResponse, err error) {
	text, steerID, err := command.ValidSteerText(raw, messageID)
	if err != nil {
		return out, err
	}
	if verb, ok, replayErr := rs.replayedVerb(workflowID, steerID); replayErr != nil || ok {
		return marotte.RunStepMessageResponse{Verb: verb}, replayErr
	}
	// A refusal no carrier can change is answered before one is hosted.
	if refused := rs.preRefuse(ctx, workflowID, nodePath); refused != nil {
		return out, refused
	}
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return out, err
	}
	defer func() { host.release(err) }()
	lease, err := rs.life.admit(workflowID)
	if err != nil {
		return out, stepRefusal(marotte.RunStepBusy)
	}
	defer lease.end()
	d, err := rs.deliverStepMessage(ctx, host, workflowID, nodePath, text, steerID)
	if d.prompt != nil {
		rs.settleHold(ctx, d.prompt)
	}
	if err != nil {
		return out, err
	}
	out.Verb = d.verb
	if d.verb != marotte.RunStepMessageSteer {
		return out, nil
	}
	out.SteerID = steerID
	return out, rs.steerStep(ctx, lease, host, d.now.at, steerID, text)
}

// The caller holds a lease on the run. A steer is left to the caller: its send takes the steer op
// lock, which comes before the gate. So is a prompt's settlement, which waits on the carrier's read
// loop, whose step ends take the gate.
func (rs *Runs) deliverStepMessage(ctx context.Context, host *runHost, workflowID, nodePath, text, steerID string) (stepDecision, error) {
	unlock, err := rs.steers.lock(ctx, workflowID)
	if err != nil {
		return stepDecision{}, err
	}
	defer unlock()
	n, now, err := rs.readStep(ctx, workflowID, nodePath)
	if err != nil {
		return stepDecision{}, err
	}
	adm, refuse := rs.asks.admitStep(n, &now, host.chatID)
	if refuse != "" {
		return stepDecision{}, stepRefusal(refuse)
	}
	d := stepDecision{now: now, verb: adm.verb, prompt: adm.prompt}
	switch d.verb {
	case marotte.RunStepMessageAnswer:
		defer rs.asks.endAnswer(workflowID)
		err = rs.sendAnswer(ctx, host, adm.ask, d.now.at, text, steerID)
	case marotte.RunStepMessagePrompt:
		err = rs.promptStep(ctx, host, d.prompt, d.now.at, text, steerID)
	case marotte.RunStepMessageSteer:
		if _, known := rs.steers.target(marotte.StepSteerKey(d.now.at.session)); !known {
			// This process never saw the step's node_start (a restart): bind it to its open turn now.
			rs.bindStepSteers(d.now.at, host.chatID)
		}
		return d, nil
	default:
		return stepDecision{}, stepRefusal(marotte.RunStepBusy)
	}
	// Under the lease, so a Delete's teardown runs after this and clears it.
	if errors.Is(err, errStepUnconfirmed) {
		rs.unconfirmed.note(workflowID, steerID, d.verb)
	}
	return d, err
}

// replayedVerb answers a retry of a recorded unconfirmed send. Under a lease: a Delete takes that
// record with the run.
func (rs *Runs) replayedVerb(workflowID, steerID string) (marotte.RunStepMessageVerb, bool, error) {
	lease, err := rs.life.admit(workflowID)
	if err != nil {
		return "", false, stepRefusal(marotte.RunStepBusy)
	}
	defer lease.end()
	verb, ok := rs.unconfirmed.verb(workflowID, steerID)
	return verb, ok, nil
}

func (rs *Runs) steerStep(ctx context.Context, lease runLease, host *runHost, at stepAddr, steerID, text string) error {
	key := marotte.StepSteerKey(at.session)
	unlock, err := rs.steers.queue.LockSteerOps(ctx, key)
	if err != nil {
		return command.StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	t := rs.steers.steerTarget(lease, host.sb.bridge, at.session)
	refuse, sendErr := command.SteerInto(ctx, t, command.SteerHolder{Held: true, Live: true}, steerID, text)
	switch {
	case refuse == command.SteerRefuseFull:
		return stepRefusal(marotte.RunStepFull)
	case refuse != "", errors.Is(sendErr, errStepSteerEnded):
		// The step's turn ended since the decision; a retry decides again.
		return stepRefusal(marotte.RunStepBusy)
	case sendErr != nil:
		return command.StatusError(http.StatusBadGateway, sendErr)
	}
	slog.Info("steered a running workflow step", "workflow_id", at.workflowID, "node_path", scrubLog(at.nodePath),
		"steer_id", steerID)
	return nil
}

// settleHold ends a prompt's hold once the carrier's read loop has folded every frame delivered
// before the call settled, which reaches Call ahead of them. The words answer the asks those frames
// carried; the rest are offered.
func (rs *Runs) settleHold(ctx context.Context, s *promptSend) {
	if s.sent.got != kasRefused {
		// It stops short only when that read loop ended or was replaced: none of its frames can then
		// reach the hold, so what it took is complete.
		rs.coord.turns.awaitDrained(durable.Context(ctx), s.carrier, s.sent.fence)
	}
	answered, unanswered := rs.asks.releaseHold(s)
	for _, a := range answered {
		slog.Info("a step's question was answered by the message sent while it asked",
			"workflow_id", scrubLog(a.payload.WorkflowID), "ask_id", scrubLog(a.payload.AskID))
		rs.announceSettled(ctx, a, marotte.SettledByUser)
	}
	for _, a := range unanswered {
		if rs.asks.add(a) {
			rs.bus.Broadcast(ctx, a.event())
		}
	}
}

func (rs *Runs) promptStep(ctx context.Context, host *runHost, s *promptSend, at stepAddr, text, entryID string) error {
	d, err := rs.sendStepWords(ctx, host, at, text, entryID)
	s.sent = d
	if err != nil {
		return err
	}
	slog.Info("resumed a paused workflow step with a message", "workflow_id", at.workflowID,
		"node_path", scrubLog(at.nodePath))
	return nil
}

// kasVerdict is what one call proves about KAS's state.
type kasVerdict int

const (
	// kasRefused: nothing changed, the call never reached KAS or KAS refused it.
	kasRefused kasVerdict = iota
	kasTook
	// kasUnconfirmed: written, with no answer read. Step words are recorded as taken, the way a chat
	// keeps a prompt it wrote, so a retry can never deliver them twice, and they answer what a reply
	// would have: an ask still owed is re-minted by the inspect reconcile (reconcileNeedInput).
	kasUnconfirmed
)

// stepDelivery is what one send of step words proved: the verdict, and the carrier's read-loop
// position the call settled at, a reply's or a failure's, on the attachment the words went out on.
type stepDelivery struct {
	fence drainPoint
	got   kasVerdict
}

// answers reports whether words KAS may hold answer an ask folded at `at`: its frame preceded the
// settlement on the same attachment, or it is no frame at all (gen 0).
func (d stepDelivery) answers(at drainPoint) bool {
	if d.got == kasRefused {
		return false
	}
	return at.gen == 0 || (at.gen == d.fence.gen && at.seq < d.fence.seq)
}

var errStepUnconfirmed = errors.New("the run's agent stopped before confirming the message; " +
	"the step's record keeps it, and sending it again only checks it arrived")

// sendStepWords places the words, then sends and settles them detached from the request: Bridge.CallAt
// writes before it waits, so a reader leaving after the write must not withdraw words KAS may hold.
func (rs *Runs) sendStepWords(ctx context.Context, host *runHost, at stepAddr, text, entryID string) (stepDelivery, error) {
	place, err := rs.placeStepMessage(ctx, at, entryID, text)
	if err != nil {
		return stepDelivery{}, err
	}
	ctx = durable.Context(ctx)
	d := stepDelivery{fence: drainPoint{gen: rs.coord.turns.folded(host.chatID).gen}}
	resp, seq, cErr := host.sb.bridge.CallAt(ctx, marotte.MethodPrompt, stepPromptParams(at.session, text))
	err = runCallErr(resp, cErr)
	d.got, d.fence.seq = verdictOf(err), seq
	if d.got == kasRefused {
		place.withdraw(rs)
		return d, err
	}
	// A fresh budget, as Resume gives.
	rs.armDeadline(ctx, at.workflowID)
	place.landed(ctx, rs, host.chatID)
	if d.got == kasUnconfirmed {
		slog.Warn("a workflow step's message was written but its reply was lost; recorded as sent",
			"workflow_id", at.workflowID, "node_path", scrubLog(at.nodePath), "error", err)
		return d, fmt.Errorf("%w: %w", errStepUnconfirmed, err)
	}
	return d, nil
}

// Bridge.CallAt reports a dead bridge or an oversize reply only for a written request, at its read-loop position.
func verdictOf(err error) kasVerdict {
	switch {
	case err == nil:
		return kasTook
	case errors.Is(err, marotte.ErrBridgeExited), errors.Is(err, marotte.ErrFrameTooLarge):
		return kasUnconfirmed
	}
	return kasRefused
}

// unconfirmedSends holds, per run, the message ids sendStepWords recorded as kasUnconfirmed and the
// verb each went under; a retry is answered from here instead of being sent.
type unconfirmedSends struct {
	byRun map[string]map[string]marotte.RunStepMessageVerb
	mu    sync.Mutex
}

func (u *unconfirmedSends) note(workflowID, messageID string, verb marotte.RunStepMessageVerb) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.byRun == nil {
		u.byRun = make(map[string]map[string]marotte.RunStepMessageVerb)
	}
	if u.byRun[workflowID] == nil {
		u.byRun[workflowID] = make(map[string]marotte.RunStepMessageVerb)
	}
	u.byRun[workflowID][messageID] = verb
}

func (u *unconfirmedSends) verb(workflowID, messageID string) (marotte.RunStepMessageVerb, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.byRun[workflowID][messageID]
	return v, ok
}

func (u *unconfirmedSends) forget(workflowID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.byRun, workflowID)
}

func stepPromptParams(session, text string) map[string]any {
	return map[string]any{
		marotte.KeySessionID: session,
		marotte.KeyPrompt:    []any{marotte.TextBlock(text)},
		"_meta":              map[string]any{"kiro": kascap.PromptMeta(&kascap.Prompt{StepMessage: text})},
	}
}

// stepPlacement is where the words that resume a paused step are recorded. KAS continues a step's open
// turn, so they join it as a read mid-turn entry; a turn that closed (a parentless run's carrier closes
// when it pauses) reopens on KAS's next node_start, which takes them as its prompt. Staged before the
// send: that node_start can arrive before the send returns.
type stepPlacement struct {
	at     stepAddr
	id     string
	text   string
	staged bool
}

// errStepUnrecorded refuses words the run's record cannot hold: nothing is sent that the step's
// transcript would not show.
var errStepUnrecorded = errors.New("the run's record could not be written, so the message was not sent")

func (rs *Runs) placeStepMessage(ctx context.Context, at stepAddr, entryID, text string) (stepPlacement, error) {
	p := stepPlacement{at: at, id: entryID, text: text}
	if rs.log == nil {
		return p, nil
	}
	if at.nodePath == "" {
		slog.Error("run log: the words for a step name no step path to record them at, so they were not sent",
			"run", at.workflowID, "session", scrubLog(at.session))
		return p, errStepUnrecorded
	}
	staged, err := rs.log.stagePrompt(ctx, at.workflowID, at.nodePath, &marotte.EntryPrompt{ID: entryID, Text: text})
	if err != nil {
		slog.Error("run log: the words for a step could not be placed in its record, so they were not sent",
			"run", at.workflowID, "node_path", scrubLog(at.nodePath), "error", err)
		return p, fmt.Errorf("%w: %w", errStepUnrecorded, err)
	}
	p.staged = staged
	return p, nil
}

func (p *stepPlacement) withdraw(rs *Runs) {
	if p.staged {
		rs.log.unstagePrompt(p.at.workflowID, p.at.nodePath, p.id)
	}
}

func (p *stepPlacement) landed(ctx context.Context, rs *Runs, host marotte.ChatID) {
	if p.staged || p.at.nodePath == "" {
		return
	}
	rs.RunSteer(ctx, p.at.workflowID, p.at.nodePath, p.id, &marotte.EntrySteer{
		Text: p.text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
	})
	rs.bindStepSteers(p.at, host)
}

func (rs *Runs) stepPathOf(workflowID, session string) string {
	if rs.log == nil {
		return ""
	}
	return rs.log.openPathOf(workflowID, session)
}

func (rs *Runs) bindStepSteers(at stepAddr, host marotte.ChatID) {
	if rs.log == nil || at.session == "" {
		return
	}
	if t := rs.log.turn(at.workflowID, at.nodePath); t != nil {
		rs.steers.bind(at.workflowID, at.nodePath, at.session, host, t.ID())
	}
}

// removeStepSteer deletes one unread row from a step's dock, as the chat's steer_remove does.
func (rs *Runs) removeStepSteer(ctx context.Context, workflowID, nodePath, steerID string) error {
	if steerID == "" {
		return command.StatusError(http.StatusBadRequest, command.ErrInvalidPayload)
	}
	return rs.withStepSteers(ctx, workflowID, nodePath, func(t command.SteerTarget) error {
		return command.RemoveSteerFrom(ctx, t, steerID, rs.endStepSteerOp(t.Key))
	})
}

// clearStepSteers discards every unread row of a step's dock, as the chat's steer_clear does.
func (rs *Runs) clearStepSteers(ctx context.Context, workflowID, nodePath string) error {
	return rs.withStepSteers(ctx, workflowID, nodePath, func(t command.SteerTarget) error {
		_, err := command.ClearSteersIn(ctx, t, rs.endStepSteerOp(t.Key))
		return err
	})
}

// withStepSteers runs op, clear and resubmit alike, under one lease. It never starts a carrier: a
// step with rows is running, and a dead carrier's rows were already retired.
func (rs *Runs) withStepSteers(ctx context.Context, workflowID, nodePath string, op func(command.SteerTarget) error) error {
	sessions := rs.steers.sessionsAt(workflowID, nodePath)
	if nodePath == "" || len(sessions) == 0 {
		return command.StatusErrorReason(http.StatusNotFound, command.SteerRefuseNotWaiting,
			errors.New("that message is no longer waiting"))
	}
	lease, err := rs.life.admit(workflowID)
	if err != nil {
		return command.StatusErrorReason(http.StatusConflict, string(marotte.RunStepBusy),
			errors.New("the run is being deleted, so its waiting messages cannot change now"))
	}
	defer lease.end()
	key := marotte.StepSteerKey(sessions[0])
	unlock, err := rs.steers.queue.LockSteerOps(ctx, key)
	if err != nil {
		return command.StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	var carrier acpSessionCaller
	if t, ok := rs.steers.target(key); ok {
		if sb := rs.bridges.get(t.host); sb != nil {
			carrier = sb.bridge
		}
	}
	return op(rs.steers.steerTarget(lease, carrier, sessions[0]))
}

// A step's rows are never rerouted: its turn end already retired them.
func (rs *Runs) endStepSteerOp(key marotte.ChatID) func(opID string) {
	return func(opID string) { rs.steers.queue.EndOp(key, opID) }
}

func (rt *Runtime) runStepSteerJob(job command.SteerJob) {
	rt.lifecycle.InflightAdd(1)
	go func() {
		defer rt.lifecycle.InflightDone()
		ctx, cancel := rt.lifecycle.TurnContext(context.Background())
		defer cancel()
		steers := rt.runs.steers
		t, ok := steers.target(job.Chat)
		if !ok {
			return
		}
		// A flush scheduled during a Delete waits for its outcome rather than dropping the rows.
		lease, err := rt.runs.life.admitWhenSettled(ctx, t.workflowID)
		if err != nil {
			return
		}
		defer lease.end()
		unlock, err := steers.queue.LockSteerOps(ctx, job.Chat)
		if err != nil {
			return
		}
		defer unlock()
		t, ok = steers.target(job.Chat)
		session, _ := job.Chat.StepSession()
		if !ok {
			return
		}
		var carrier acpSessionCaller
		if sb := rt.runs.bridges.get(t.host); sb != nil {
			carrier = sb.bridge
		}
		command.SendPlanned(ctx, steers.steerTarget(lease, carrier, session), steers.queue.PlanFlush(job.Chat))
	}()
}
