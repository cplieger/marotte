package agent

// The run host: launching a parentless workflow run and the session that holds it. An agent-launched
// run is parented on its chat's session by KAS; a manual or scheduled one gets one host session under
// the synthetic chat id `run:<workflowId>`, which has no chat file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// Workflow RPC param keys, shared across the five verbs.
const (
	keyWorkflowID     = "workflowId"
	keyWorkspacePaths = "workspacePaths"
)

// Real ids are `c-*` uuids.
const runChatPrefix = "run:"

func runChatID(workflowID string) marotte.ChatID {
	return marotte.ChatID(runChatPrefix + workflowID)
}

func isRunChat(chatID marotte.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

func workflowIDOf(chatID marotte.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch: the host session's door, `workflow/new` and `invoke`.
const launchTimeout = 120 * time.Second

// errRecipeBusy is the single-run rule: one live run per recipe, globally.
var errRecipeBusy = errors.New("this recipe already has a live run")

// launch starts one parentless attended run of the recipe and returns its workflow id and name.
func (rs *Runs) launch(
	ctx context.Context, source string, inputs map[string]string, defaults marotte.RunDefaults,
) (id, name string, err error) {
	return rs.launchAs(ctx, source, inputs, defaults, manualLaunch())
}

// LaunchScheduled launches an unattended run for a schedule. slotAt is its next slot; zero leaves the idle window and backstop.
func (rs *Runs) LaunchScheduled(
	ctx context.Context, source, scheduleID string, defaults marotte.RunDefaults, slotAt time.Time,
) (id, name string, err error) {
	// The schedule runner outlives Shutdown, so a launch is held on inflight and refused once draining.
	release, ok := rs.lifecycle.enterUnlessDraining()
	if !ok {
		return "", "", errRuntimeDraining
	}
	defer release()
	return rs.launchAs(ctx, source, nil, defaults, scheduledLaunch(scheduleID, slotAt))
}

func (rs *Runs) launchAs(
	ctx context.Context, source string, inputs map[string]string, defaults marotte.RunDefaults, o launchOrigin,
) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := rs.launchRecipe(cctx, source, inputs)
	if err != nil {
		return "", "", err
	}
	if o.origin == runlease.OriginManual && !isAgentSource(source) {
		// A manual run yields to its recipe's next slot.
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	if bErr := rs.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// The host session's presets are resolved once and recorded on the lease: a reload after a restart applies
	// these, never the profile as it is then.
	presets := securityPresets(cctx, rs.lifecycle.configDir)
	sb := &sharedBridge{state: bridgeStarting, settled: newSessionSettle()}
	var relay *hostRelay
	if _, oErr := rs.engine.openSession(cctx, &marotte.SessionOpts{Role: marotte.SessionRoleHost, Presets: presets},
		func(s engineSession) {
			relay = newHostRelay(s, func(msg *marotte.RPCResponse) { rs.answerEarly(s, msg) })
			sb.attach(relay)
		}); oErr != nil {
		return "", "", fmt.Errorf("%w: %w", errRunHostStart, oErr)
	}
	host := sb.current()
	parent := host.SessionID()
	if parent == "" {
		rs.bridges.closeSession(host)
		return "", "", fmt.Errorf("%w: the host session's door answered no session id", errRunHostStart)
	}
	wfID, err := rs.workflowNew(cctx, host, recipe.Source, inputs, defaults, parent, o.runLabel(recipe.Name))
	if err != nil {
		rs.bridges.closeSession(host)
		return "", "", err
	}
	if !rs.bridges.insert(runChatID(wfID), sb) {
		rs.bridges.closeSession(host)
		return "", "", fmt.Errorf("workflow %s already has a host session", wfID)
	}
	sb.setIdle()
	sb.settled.settle(nil)
	rs.coord.goForward(runChatID(wfID), host)
	relay.begin()
	// The run's envelope, before anything can execute (runlease.Lease).
	rs.grantLease(cctx, wfID, recipe.Name, o, hostSecurityOf(presets))
	rs.recordParentSession(cctx, wfID, string(parent))
	// Visible to Route before invoke is written, so the run's first frame reaches the host's lane.
	rs.router.registerOwner(wfID, parent)
	if _, err := rs.drive(cctx, sb, wfID, methodKiroWorkflowInvoke,
		map[string]any{keyWorkflowID: wfID}, mutationDrive); err != nil {
		// Created but never started.
		rs.releaseLease(cctx, wfID)
		rs.coord.closeBridge(cctx, runChatID(wfID), marotte.TurnOutcomeInterrupted)
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	// After invoke, so a run that never started leaves no timer.
	rs.armDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// runStop is who a stop tells KAS asked. The zero value attributes nothing: an attributed stop tells the agent the user stopped it.
type runStop struct {
	why    string
	byUser bool
}

// KAS caps a reason at 500 characters and refuses one sent without an initiator.
const (
	initiatorUser      = "user"
	stopWhyTabClosed   = "tab closed"
	stopWhyChatDeleted = "chat deleted"
	stopWhyRewound     = "chat rewound"
)

// why is set only when the cause is not the stop button.
func userStop(why string) runStop { return runStop{byUser: true, why: why} }

func (s runStop) apply(params map[string]any) {
	if !s.byUser {
		return
	}
	params["initiator"] = initiatorUser
	if s.why != "" {
		params["reason"] = s.why
	}
}

// cancel stops a run on the user's behalf. A lost termination claim returns nil; a refused RPC is
// returned, and retryTermination retries it.
func (rs *Runs) cancel(ctx context.Context, workflowID string) error {
	return rs.cancelAs(ctx, workflowID, userStop(""))
}

// cancelAs is Cancel attributed to stop.
func (rs *Runs) cancelAs(ctx context.Context, workflowID string, stop runStop) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	return rs.finishTermination(ctx, workflowID, "", stop)
}

// cancelRPC issues the cancel verb only. The host session stays resident: the run certifies the cancel at its node
// boundary, and its terminal `run_complete` releases the host.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string, stop runStop) error {
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
	err := rs.stopVerb(ctx, workflowID, methodKiroWorkflowCancel, params)
	if err != nil {
		return fmt.Errorf("workflow cancel call: %w", err)
	}
	return nil
}

// delete removes a run and everything kept about it, on the user's behalf; unrecoverable, and a refused
// delete leaves everything. No termination claim (KAS's delete cancels a live run itself), and the bridge
// is closed. errRunDeleting refuses a run another delete holds; errDeleteUnconfirmed reports a delete
// KAS was sent whose outcome is still being read back.
func (rs *Runs) delete(ctx context.Context, workflowID string) error {
	return rs.deleteAs(ctx, workflowID, userStop(""))
}

var errDeleteUnconfirmed = errors.New("the delete reached the run's agent but its answer was lost; " +
	"the run takes no messages until marotte reads back whether it is gone")

// deleteReconcileRetry is how long an unconfirmed delete waits before its run is read again. A var for tests.
var deleteReconcileRetry = 30 * time.Second

// deleteAs is Delete attributed to stop.
func (rs *Runs) deleteAs(ctx context.Context, workflowID string, stop runStop) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	d, err := rs.life.beginDelete(ctx, workflowID)
	if err != nil {
		return err
	}
	var got kasVerdict
	rs.stopSerialized(ctx, workflowID, func(sctx context.Context) bool {
		got, err = rs.deleteCall(sctx, workflowID, stop)
		return got != kasRefused
	})
	switch got {
	case kasRefused:
		d.settle()
		return err
	case kasUnconfirmed:
		slog.Warn("a workflow delete was sent but its answer was lost; reading the run back",
			"workflow_id", workflowID, "error", err)
		return rs.reconcileDelete(ctx, d, err)
	case kasTook:
	}
	rs.teardownDeleted(ctx, workflowID)
	d.settle()
	return nil
}

// deleteCall sends the delete once the engine is ready, then waits for its answer detached from ctx: a delete KAS
// may have applied is waited out, never abandoned.
func (rs *Runs) deleteCall(ctx context.Context, workflowID string, stop runStop) (kasVerdict, error) {
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
	h, _, release, err := rs.engine.acquire(ctx)
	if err != nil {
		return kasRefused, err
	}
	defer release()
	resp, err := h.Call(durable.Context(ctx), methodKiroWorkflowDelete, params)
	err = runCallErr(resp, err)
	if err == nil {
		rs.router.forgetWorkflow(workflowID)
	}
	return verdictOf(err), err
}

// reconcileDelete settles a delete whose answer was lost by reading the run back: one KAS still has
// reopens, one KAS no longer knows is torn down here, and an unreadable answer keeps the run closed and
// asks again later.
func (rs *Runs) reconcileDelete(ctx context.Context, d *runDeletion, cause error) error {
	switch rs.readBack(ctx, d.workflowID) {
	case runKept:
		slog.Info("a run whose delete was unconfirmed is still there, so it takes messages again",
			"workflow_id", d.workflowID)
		rs.clearStopping(ctx, d.workflowID)
		d.settle()
		return cause
	case runGone:
		rs.teardownDeleted(ctx, d.workflowID)
		d.settle()
		return nil
	case runUnread:
	}
	rs.reconcileLater(d)
	return fmt.Errorf("%w: %w", errDeleteUnconfirmed, cause)
}

func (rs *Runs) reconcileLater(d *runDeletion) {
	rs.lifecycle.afterUnlessDraining(deleteReconcileRetry, func() {
		ctx := rs.lifecycle.shutdownCtx
		if ctx.Err() != nil {
			return
		}
		if rs.reconcileDelete(ctx, d, errDeleteUnconfirmed) == nil {
			slog.Info("an unconfirmed workflow delete was confirmed", "workflow_id", d.workflowID)
		}
	})
}

type readBackVerdict int

const (
	runUnread readBackVerdict = iota
	runKept
	runGone
)

func (rs *Runs) readBack(ctx context.Context, workflowID string) readBackVerdict {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		if workflowNotFound(err) {
			return runGone
		}
		slog.Warn("could not read back a run whose delete was unconfirmed", "workflow_id", workflowID, "error", err)
		return runUnread
	}
	if _, err := workflow.DecodeInspect(workflowID, raw); err != nil {
		return runUnread
	}
	return runKept
}

// teardownDeleted drops everything marotte keeps about a run KAS deleted.
func (rs *Runs) teardownDeleted(ctx context.Context, workflowID string) {
	rs.deleteRunLog(ctx, workflowID)
	rs.coord.closeBridge(ctx, runChatID(workflowID), marotte.TurnOutcomeInterrupted)
	rs.forgetBounds(ctx, workflowID)
	rs.clearEnd(workflowID)
	if rs.log != nil {
		if err := rs.log.removeDir(workflowID); err != nil {
			slog.Warn("run log: remove run directory", "workflow_id", workflowID, "error", err)
		}
	}
	slog.Info("workflow run deleted", "workflow_id", workflowID)
}

// pause asks a run to stop at its next node boundary; the reply confirms the ask.
func (rs *Runs) pause(ctx context.Context, workflowID string) error {
	params := map[string]any{keyWorkflowID: workflowID}
	userStop("").apply(params)
	return rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		err := rs.stopVerb(cctx, workflowID, methodKiroWorkflowPause, params)
		return err
	})
}

// resume re-drives a paused run with a fresh executing budget.
func (rs *Runs) resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume, runStop{})
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// Below clientRequestBudget, from which it is derived: the retry's own deadline, never a browser abort, decides
// whether it released a lease it minted. It bounds the host session's load and the call. A var for tests.
var retryTimeout = clientRequestBudget - 5*time.Second

// clientRequestBudget is the timeout @cplieger/fetch hard-wires into every apiAction.
const clientRequestBudget = 30 * time.Second

// errRetrySlow reports a retry not handed off within retryTimeout; the REST layer answers 503.
var errRetrySlow = errors.New(
	"the run's session did not load in time, so nothing was retried. Try again",
)

// errRetryOutcomeUnreadable reports a retry KAS accepted whose report was unreadable. The run may be
// executing, so the remedy is refreshing, not retrying.
var errRetryOutcomeUnreadable = errors.New(
	"the retry was accepted but its report could not be read, so which steps it reset " +
		"is unknown. Refresh the run to see where it is",
)

// RetriedNodeIDs tells a five-node retry from one that reset nothing.
type kasRetryOutcome struct {
	WorkflowID     string   `json:"workflowId"`
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retriedNodeIds"`
}

// retry resets a failed or aborted run's failed work and reports what it reset, hosted from the gate's own read of the run's origin.
func (rs *Runs) retry(
	ctx context.Context, workflowID string, aff *runAffordance,
) (out marotte.RunRetriedResponse, err error) {
	if workflowID == "" {
		return out, errors.New("missing workflow id")
	}
	cctx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()

	// Threaded from the gate: nothing here can learn it once the run re-drives.
	recipe := aff.origin.recipe
	// The lease before the verb, since retry's `run_start` can beat the reply, and before the host session's load,
	// which reads its presets from it.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantRetryLease(cctx, workflowID, &aff.origin)
		minted = true
	}
	host, err := rs.ensureResident(cctx, workflowID)
	if err != nil {
		if minted {
			rs.releaseLease(cctx, workflowID)
		}
		return out, rs.retryResidencyErr(cctx, err)
	}
	err = rs.controlSerialized(cctx, workflowID, func(sctx context.Context) error {
		var cErr error
		out, cErr = rs.retryCall(sctx, host, workflowID)
		return cErr
	})
	if errors.Is(err, errRetryOutcomeUnreadable) {
		// Only the report is lost; tearing down would kill the work mid-node.
		rs.rearmRetried(cctx, workflowID, &aff.origin)
		return out, err
	}
	if err != nil {
		// Return the minted lease, except on a context error, where KAS may have taken the retry and would run deadline-less.
		if minted && !isCtxErr(err) {
			rs.releaseLease(cctx, workflowID)
		}
		return out, err
	}
	// Only on success: a refused retry re-drove nothing.
	rs.rearmRetried(cctx, workflowID, &aff.origin)
	slog.Info("workflow run retried",
		"workflow_id", workflowID, "recipe", recipe,
		"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
	return out, nil
}

// retryCall issues the verb and decodes its report, through runCallErr so an `error` member is not read as success.
func (rs *Runs) retryCall(
	ctx context.Context, host *drivenHost, workflowID string,
) (marotte.RunRetriedResponse, error) {
	none := marotte.RunRetriedResponse{}
	resp, err := host.call(ctx, rs, workflowID, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
	if err != nil {
		return none, fmt.Errorf("workflow retry: %w", rs.retryDeadlineErr(ctx, err))
	}
	// Past here KAS accepted the verb: any failure is a lost report.
	if resp == nil || len(resp.Result) == 0 {
		return none, fmt.Errorf("%w: the reply carried no outcome", errRetryOutcomeUnreadable)
	}
	var out kasRetryOutcome
	if uErr := json.Unmarshal(resp.Result, &out); uErr != nil {
		return none, fmt.Errorf("%w: undecodable outcome: %w", errRetryOutcomeUnreadable, uErr)
	}
	// The reply must be about the run asked for.
	if out.WorkflowID != "" && out.WorkflowID != workflowID {
		return none, fmt.Errorf("%w: the reply names run %q, not %q",
			errRetryOutcomeUnreadable, out.WorkflowID, workflowID)
	}
	// Never nil on the wire.
	nodes := out.RetriedNodeIDs
	if nodes == nil {
		nodes = []string{}
	}
	return marotte.RunRetriedResponse{Status: out.Status, RetriedNodeIDs: nodes}, nil
}

// retryResidencyErr labels a failed load of the run's session: this call's deadline, or the load failure itself.
func (*Runs) retryResidencyErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetrySlow
	}
	return fmt.Errorf("retry: load the run's session: %w", err)
}

func (*Runs) retryDeadlineErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetrySlow
	}
	return err
}

// errStepStatusRefused means KAS declined the update (a terminal run, a step that is not paused, a caller
// that is not the owning parent, or a paused step given a status other than completed or failed); the REST
// layer answers 409.
var errStepStatusRefused = errors.New("this run's step status was not changed")

// updateStatusAction is `_kiro/workflow/update`'s step-status action, sent explicitly because the tool schema requires it.
const updateStatusAction = "update_status"

// stepStatusRefusal returns KAS's reason for a declined update, else "". `Updated` is a *bool so absent reads as taken.
func stepStatusRefusal(resp *marotte.RPCResponse) string {
	if resp == nil || len(resp.Result) == 0 {
		return ""
	}
	var reply struct {
		Updated *bool  `json:"updated"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Result, &reply) != nil {
		return ""
	}
	if reply.Updated == nil || *reply.Updated {
		return ""
	}
	if reply.Message == "" {
		return "KAS gave no reason"
	}
	return reply.Message
}

// runStepCompleted is the step status finishRepeat writes on a repeat paused at its cap.
const runStepCompleted = "completed"

// answerInput answers one parked step with the user's words, as a `session/prompt` to its own
// session. The ask is checked against a fresh read of the run's host under the run's serializer,
// after marotte's own pending send was registered; the serializer is released before the prompt,
// whose refused arm can last a whole ordinary turn.
func (rs *Runs) answerInput(ctx context.Context, workflowID, askID, text string) (err error) {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("an answer cannot be empty")
	}
	c := rs.cellFor(workflowID)
	if c == nil {
		return errAskGone
	}
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		return err
	}
	lease, err := rs.life.admit(workflowID)
	if err != nil {
		// A delete KAS keeps reopens the run, so this is not "gone".
		return errAskStopping
	}
	defer lease.end()
	defer rs.kickCell(c)
	if aErr := c.acquire(ctx); aErr != nil {
		return aErr
	}
	held := true
	release := func() {
		if held {
			held = false
			c.release()
		}
	}
	defer release()
	if refusal := c.holdRefusal(); refusal != nil {
		return refusal
	}
	p := rs.registerPending(ctx, c)
	r, err := rs.readAsks(ctx, c)
	if err != nil {
		rs.clearPending(ctx, c, p)
		slog.Warn("could not read a run before answering it", "workflow_id", workflowID, "error", err)
		return errAskUnreadable
	}
	ask, err := askByID(r.asks, askID)
	if err != nil {
		rs.clearPending(ctx, c, p)
		return err
	}
	at := stepAddr{workflowID: workflowID, nodePath: workflow.PathKey(ask.NodePath), session: ask.StepSessionID}
	unlock, err := rs.steers.lock(ctx, workflowID)
	if err != nil {
		rs.clearPending(ctx, c, p)
		return err
	}
	defer unlock()
	c.sendingPending(p, ask.StepSessionID)
	release()
	got, err := rs.sendStepWords(ctx, host, at, text, "answer-"+askID)
	if got == kasRefused {
		rs.clearPending(ctx, c, p)
		return err
	}
	rs.repliedPending(ctx, c, p)
	if err != nil {
		return err
	}
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_path", scrubLog(at.nodePath))
	return nil
}

// hostedControl issues a driving verb on the run's launching session, made resident first.
func (rs *Runs) hostedControl(ctx context.Context, workflowID, method string, stop runStop) error {
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		return err
	}
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
	return rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		_, err := host.call(cctx, rs, workflowID, method, params)
		return err
	})
}

// "" for parentless.
type runParent struct {
	chat    marotte.ChatID
	session string
}

// err says why parent cannot route a verb.
type runOrigin struct {
	err    error
	parent runParent
	recipe string
	// pauseKind and pauseNodeID are kasWorkflowRun's, off the same read.
	pauseKind    string
	pauseNodeID  string
	pausePending bool
}

// An ownerless parent is trusted only after a complete chat scan: loading a chat's session as a
// run host replays that chat's transcript into the run.
func (rs *Runs) originOf(ctx context.Context, workflowID string) runOrigin {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("could not read the run inventory, so a run's parent chat and recipe are unknown",
			"workflow_id", workflowID, "error", err)
		return runOrigin{err: fmt.Errorf("%w: reading the run inventory: %w", errRunHostStart, err)}
	}
	i := slices.IndexFunc(runs, func(r kasWorkflowRun) bool { return r.WorkflowID == workflowID })
	if i < 0 {
		return runOrigin{err: errRunNotListed}
	}
	// WorkflowName is the recipe the single-run rule compares; Name is `runLabel ?? workflowName`.
	o := runOrigin{
		recipe: runs[i].WorkflowName, parent: runParent{session: runs[i].ParentSessionID},
		pauseKind: runs[i].PauseKind, pauseNodeID: runs[i].PauseNodeID, pausePending: runs[i].PausePending,
	}
	if o.parent.session == "" {
		o.err = fmt.Errorf("%w: the run inventory names no launching session", errLaunchSessionUnavailable)
		return o
	}
	var complete bool
	o.parent.chat, complete = rs.chatForSession(ctx, o.parent.session)
	if o.parent.chat == "" && !complete {
		o.err = fmt.Errorf("%w: a chat file could not be read, so which chat launched the run is unknown",
			errRunHostStart)
	}
	return o
}

// runLocks is one ctx-abandonable lock per run (sync.Mutex cannot be). A ctx already ended when the lock frees loses.
type runLocks struct {
	held map[string]chan struct{}
	mu   sync.Mutex
}

func (p *runLocks) acquire(ctx context.Context, workflowID string) (release func(), err error) {
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p.mu.Lock()
		busy, taken := p.held[workflowID]
		if !taken {
			if p.held == nil {
				p.held = make(map[string]chan struct{})
			}
			done := make(chan struct{})
			p.held[workflowID] = done
			p.mu.Unlock()
			return func() {
				p.mu.Lock()
				delete(p.held, workflowID)
				p.mu.Unlock()
				close(done)
			}, nil
		}
		p.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// isCtxErr reports a cancellation or deadline at any depth: KAS may have taken a verb whose context ended.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Host requests and the three ask kinds reuse the chat handlers; lifecycle frames go
// workspace-global with an empty chat id; session/update goes to the run's own log
// (`runs/<workflowId>/entries.jsonl`) as run-scoped entry events.
func (rt *Runtime) dispatch(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, origin, msg)
		return
	}
	// Before the prefix test.
	if msg.Method == methodKiroSessionNotify {
		rt.runs.onNotify(ctx, chatID, msg)
		return
	}
	// Keeps the run's chat id, so a toast names the run.
	if rt.dispatchWorkspace(ctx, chatID, msg) {
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/") {
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		if msg.Method == methodWFRunComplete {
			rt.releaseEndedHost(chatID, msg)
		}
		return
	}
	if msg.Method == marotte.MethodSessionUpdate {
		rt.handleRunStepFrame(ctx, chatID, msg)
		return
	}
	slog.Debug("run host session: unhandled notification", "method", msg.Method, "chat_id", chatID)
}

// An unmatched one is refused, since silence wedges the step.
func (rt *Runtime) dispatchRequest(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	switch {
	case rt.inbound.handleFSRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroFSRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroClientRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroSecretRequest(ctx, chatID, origin, msg):
		return
	case strings.HasPrefix(msg.Method, methodTermPrefix):
		rt.handleTerminalRequest(ctx, chatID, origin, msg.Method, msg)
		return
	}
	if fn, ok := rt.askHandlers[msg.Method]; ok {
		fn(ctx, chatID, origin, msg)
		return
	}
	slog.Warn("run host session: refusing unexpected request", "method", msg.Method, "chat_id", chatID)
	_ = respondOn(ctx, rt.bridge.mgr, chatID, origin, msg, nil, &marotte.RPCError{
		Code:    marotte.RPCCodeMethodNotFound,
		Message: "unsupported on a run's host session: " + msg.Method,
	})
}

// releaseEndedHost closes a parentless run's host session once its run is terminal; a paused run keeps it resident,
// so resume, retry, Continue and answers need no reload. A goroutine, since this runs on the session's own forward.
func (rt *Runtime) releaseEndedHost(chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var p struct {
		Status marotte.RunStatus `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || !p.Status.Terminal() {
		return
	}
	if rt.bridge.mgr.get(chatID) == nil {
		return
	}
	slog.Info("a run ended, closing its host session", "chat_id", chatID, "status", p.Status)
	rt.lifecycle.goUnlessDraining(func() {
		rt.coord.closeBridge(rt.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
	})
}

const agentSourcePrefix = "agent://"

// agentNamePattern is KAS's own check on the name after agent://.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func isAgentSource(source string) bool { return strings.HasPrefix(source, agentSourcePrefix) }

// launchRecipe resolves a listed recipe or an agent:// source. An agent run needs a prompt, and its
// recipe name is KAS's `agent-<name>`, so one live run per agent. KAS refuses an unregistered agent.
func (rs *Runs) launchRecipe(ctx context.Context, source string, inputs map[string]string) (marotte.Recipe, error) {
	if !isAgentSource(source) {
		return rs.recipeBySource(ctx, source)
	}
	name := strings.TrimPrefix(source, agentSourcePrefix)
	if !agentNamePattern.MatchString(name) {
		return marotte.Recipe{}, fmt.Errorf("invalid agent name %q", name)
	}
	if strings.TrimSpace(inputs["prompt"]) == "" {
		return marotte.Recipe{}, errors.New("an agent run needs a prompt")
	}
	return marotte.Recipe{Name: "agent-" + name, Source: source}, nil
}

func (rs *Runs) recipeBySource(ctx context.Context, source string) (marotte.Recipe, error) {
	if source == "" {
		return marotte.Recipe{}, errors.New("missing recipe source")
	}
	recipes, err := rs.listRecipes(ctx)
	if err != nil {
		return marotte.Recipe{}, err
	}
	for _, r := range recipes {
		if r.Source == source {
			return r, nil
		}
	}
	return marotte.Recipe{}, fmt.Errorf("unknown recipe source %q", source)
}

// recipeIdle enforces the single-run rule against KAS's run list, the only source that sees runs
// marotte did not launch. The leases explain a blocking row as marotte's own orphan.
func (rs *Runs) recipeIdle(ctx context.Context, name string) error {
	runs, err := rs.list(ctx, nil)
	if err != nil {
		// Launching blind could allow a second live run.
		return fmt.Errorf("run list unavailable: %w", err)
	}
	status := make(map[string]marotte.RunStatus, len(runs))
	for i := range runs {
		status[runs[i].WorkflowID] = runs[i].Status
	}
	rs.reconcileLeasePresence(ctx, status, time.Now(), false)

	for i := range runs {
		// WorkflowName is the recipe; Name carries any stamped label, which made this guard fail open.
		if runs[i].WorkflowName != name || !runs[i].Status.Active() {
			continue
		}
		if rs.clearBlockingOrphan(ctx, runs[i].WorkflowID, runs[i].Status) {
			slog.Info("cleared a restart-orphaned run that was holding its recipe",
				"workflow_id", runs[i].WorkflowID, "recipe", name)
			continue
		}
		return errRecipeBusy
	}
	return nil
}

// `plan` rides through as raw JSON (marotte.Recipe).
type kasRecipe struct {
	Inputs          map[string]string `json:"inputs"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	ValidationError string            `json:"validationError"`
	Source          string            `json:"source"`
	Plan            json.RawMessage   `json:"plan"`
	BuiltIn         bool              `json:"builtIn"`
}

func (rs *Runs) listRecipes(ctx context.Context) ([]marotte.Recipe, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := rs.engine.rawCall(cctx, "workflow listRecipes call", methodKiroWorkflowListRecipes,
		map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}})
	if err != nil {
		return nil, err
	}
	var list struct {
		Recipes []kasRecipe `json:"recipes"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]marotte.Recipe, 0, len(list.Recipes))
	for _, r := range list.Recipes {
		if r.Name == "" || r.Source == "" {
			continue
		}
		docKey, _ := translate.DocKeyFromAbs(r.Source)
		out = append(out, marotte.Recipe{
			Name:            r.Name,
			Description:     r.Description,
			ValidationError: r.ValidationError,
			Source:          r.Source,
			DocKey:          docKey,
			Inputs:          r.Inputs,
			Plan:            r.Plan,
			BuiltIn:         r.BuiltIn,
		})
	}
	return out, nil
}

// workflowNew creates the run on bridge and returns its id. parent is the run host session's id, so
// frames route back to that session. workspacePaths stays for pre-0.63.3 engines.
func (rs *Runs) workflowNew(
	ctx context.Context, bridge acpCaller, source string, inputs map[string]string,
	defaults marotte.RunDefaults, parent marotte.SessionID, label string,
) (string, error) {
	// Always a map: KAS answers "inputs is not iterable" without one.
	in := map[string]any{}
	for k, v := range inputs {
		in[k] = v
	}
	params := map[string]any{
		"workflowPath":    source,
		keyWorkspacePaths: []string{rs.lifecycle.workDir},
		"inputs":          in,
		"parentSessionId": string(parent),
	}
	if label != "" {
		params["runLabel"] = label
	}
	if defaults.Model != "" {
		params["modelId"] = defaults.Model
	}
	if defaults.Effort != "" {
		params["effortLevel"] = defaults.Effort
	}
	resp, err := bridge.Call(ctx, methodKiroWorkflowNew, params)
	if cErr := runCallErr(resp, err); cErr != nil {
		return "", fmt.Errorf("workflow new: %w", cErr)
	}
	var res struct {
		WorkflowID string `json:"workflowId"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil || res.WorkflowID == "" {
		return "", errors.New("workflow new: reply carried no workflowId")
	}
	return res.WorkflowID, nil
}

func runCallErr(resp *marotte.RPCResponse, err error) error {
	if err == nil && resp != nil && resp.Error != nil {
		err = resp.Error
	}
	return workflow.Classify(err)
}

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON for a run whose owner died. A literal: only this reason licenses a cancel.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// resumableReasonPrefixes open the reasons KAS's pauseVerdict gives a stop nobody chose, licensing a resume
// only. Prefixes, because KAS rewords the tails between releases and interpolates the network code; the
// interruption arm carries no pauseDetail, so its prefix is its only arm. TestPauseReasonsMatchBundle
// (internal/kascap) checks them against the pinned agent server.
var resumableReasonPrefixes = []string{
	"Step interrupted (agent shutdown or connection reset);",
	"Model service error (service 5xx or throttling);",
	"Transient connection error (",
}

// transientErrorClass is the `pauseDetail.class` KAS stamps for every transient fault; it reaches a
// parallel branch's pause, whose prose KAS re-renders.
const transientErrorClass = "transient-error"

// pauseDetail is KAS's pause classification on the `paused` frame and in inspect, one declaration for
// the predicate path. `occurredAt` is deliberately absent: only run_ask.go reads it.
type pauseDetail struct {
	Class string `json:"class"`
	Code  string `json:"code"`
}

// resumablePause reports whether marotte should resume a pause unasked: reason OR detail, since detail-less
// pauses and re-rendered prose each need one arm. restartPaused (run_orphan.go) cancels and never reads a detail.
func resumablePause(reason string, detail *pauseDetail) bool {
	if detail != nil && detail.Class == transientErrorClass {
		return true
	}
	if reason == stalePauseReason {
		return true
	}
	return slices.ContainsFunc(resumableReasonPrefixes, func(p string) bool { return strings.HasPrefix(reason, p) })
}

// orphanResumeDelay is when the resume sweep re-reads a run KAS still lists running after an engine end:
// KAS judges a run's owner dead 135 s after its last beat (staleAfterMs, 4.5 beats of 30 s)
// and allows one beat of reader drift (kiro-cli 2.29.1). A var for tests.
var orphanResumeDelay = 165 * time.Second

// resumeInterruptedRuns resumes the restart-paused runs this chat's session chain launched, and
// sweeps once more after orphanResumeDelay when KAS has not yet reconciled one; never resumeAll,
// which would undo deliberate pauses.
func (rs *Runs) resumeInterruptedRuns(ctx context.Context, chatID marotte.ChatID) {
	if !rs.resumeChainRuns(ctx, chatID) {
		return
	}
	slog.Info("rehydrate: a run is still listed running; re-reading it once KAS reconciles it",
		"chat_id", chatID, "delay", orphanResumeDelay)
	rs.lifecycle.afterUnlessDraining(orphanResumeDelay, func() {
		hctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		rs.resumeChainRuns(hctx, chatID)
	})
}

// resumeChainRuns is one resume sweep, reporting whether a run of the chain was still listed running: after an
// engine end KAS reads its runs running until its staleness window passes.
func (rs *Runs) resumeChainRuns(ctx context.Context, chatID marotte.ChatID) (unreconciled bool) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return false
	}
	chain := make(map[string]bool, len(chat.PriorACPSessionIDs)+1)
	chain[chat.ACPSessionID] = true
	for _, id := range chat.PriorACPSessionIDs {
		chain[id] = true
	}

	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("rehydrate: run list unavailable, skipping resume sweep", "chat_id", chatID, "error", err)
		return false
	}
	for i := range runs {
		r := &runs[i]
		if !chain[r.ParentSessionID] {
			continue
		}
		switch marotte.RunStatus(r.Status) {
		case marotte.RunStatusPaused:
			rs.resumeIfInterrupted(ctx, chatID, r.WorkflowID)
		case marotte.RunStatusRunning:
			unreconciled = true
		}
	}
	// The chat's session is resident again, so every run it hosts is read again.
	rs.kickChat(chatID)
	return unreconciled
}

// maxAutoHeals bounds automatic resumes between two pieces of progress; a fourth try against a dead network says nothing.
const maxAutoHeals = 3

// healBaseDelay is the first automatic resume's delay, doubling (5s, 10s, 20s), so a deliberate cancel
// can land first. A var for tests.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS just parked for a reason nobody chose, off the `_kiro/workflow/paused`
// frame. After `next`, so the client renders the pause first.
func (rs *Runs) healPaused(
	next func(context.Context, marotte.ChatID, *marotte.RPCResponse),
) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		next(ctx, chatID, msg)
		f := decodePauseFrame(msg)
		if f.WorkflowID == "" || chatID == "" {
			return
		}
		if !resumablePause(f.PauseReason, f.PauseDetail) {
			// Without this a decline is silent; Debug because deliberate pauses land here too.
			slog.Debug("a paused run was left alone: its pause is not one marotte resumes unasked",
				"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
				"pause_reason", scrubLog(f.PauseReason),
				"pause_class", scrubLog(pauseClassOf(f.PauseDetail)),
				"pause_code", scrubLog(pauseCodeOf(f.PauseDetail)))
			return
		}
		attempt, ok := rs.claimHeal(f.WorkflowID)
		if !ok {
			slog.Warn("a run keeps pausing for a cause nobody chose; leaving it paused",
				"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
				"pause_reason", scrubLog(f.PauseReason), "attempts", attempt)
			return
		}
		delay := healBaseDelay * time.Duration(1<<(attempt-1))
		slog.Info("scheduling the automatic resume of an involuntarily paused run",
			"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
			"pause_reason", scrubLog(f.PauseReason), "delay", delay)
		rs.lifecycle.afterUnlessDraining(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the three fields the heal reads off `_kiro/workflow/paused`, decoded separately
// from lifecycleFrame. Pointer first for govet's fieldalignment.
type pauseFrame struct {
	PauseDetail *pauseDetail `json:"pauseDetail"`
	WorkflowID  string       `json:"workflowId"`
	PauseReason string       `json:"pauseReason"`
}

// pauseClassOf names a pause's class for a log line only; predicates compare the field directly.
func pauseClassOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Class
}

func pauseCodeOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Code
}

// unmarshalKeepingReadable decodes data into dst, keeping every field that decoded when one field's type
// drifted: `encoding/json` finishes the object on a type mismatch. A syntax error is not tolerated.
func unmarshalKeepingReadable(data []byte, dst any) bool {
	err := json.Unmarshal(data, dst)
	if err == nil {
		return true
	}
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func decodePauseFrame(msg *marotte.RPCResponse) pauseFrame {
	var f pauseFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if !unmarshalKeepingReadable(msg.Params, &f) {
		return pauseFrame{}
	}
	return f
}

// healProgress refills a run's per-stall budgets when a node completes.
func (rs *Runs) healProgress(
	next func(context.Context, marotte.ChatID, *marotte.RPCResponse),
) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// The cancel ladder too: a refusal speaks only for its moment (maxCancelRetries).
			rs.clearCancelRetries(f.WorkflowID)
			// And the idle window. Both run populations reach this: the run host session reuses chatHandlers for every `_kiro/workflow/*` method.
			rs.refillDeadline(ctx, f.WorkflowID)
		}
		next(ctx, chatID, msg)
	}
}

type nodeFrame struct {
	WorkflowID string `json:"workflowId"`
}

func decodeNodeFrame(msg *marotte.RPCResponse) nodeFrame {
	var f nodeFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return nodeFrame{}
	}
	return f
}

func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID marotte.ChatID, workflowID string) {
	// The wider involuntary set, because this resumes; the orphan sweep's narrower `restartPaused` cancels.
	if !rs.involuntarilyPaused(ctx, workflowID) {
		return
	}
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		slog.Warn("rehydrate: the run's launching session could not be made resident, so it was not resumed",
			"workflow_id", workflowID, "chat_id", chatID, "error", err)
		return
	}
	cErr := rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		_, err := host.call(cctx, rs, workflowID, methodKiroWorkflowResume, map[string]any{keyWorkflowID: workflowID})
		return err
	})
	if cErr != nil {
		if _, _, refused := command.StatusOf(cErr); refused {
			slog.Info("rehydrate: marotte has a send to this run in flight, so the resume waits for the next sweep",
				"workflow_id", workflowID, "chat_id", chatID, "error", cErr)
			return
		}
		if errors.Is(cErr, workflow.ErrJustClaimed) {
			// KAS queues a second claim on one engine and refuses only after the first overstayed its claim TTL.
			slog.Info("rehydrate: another verb held this run's claim past its TTL, so the heal left it to that one",
				"workflow_id", workflowID, "chat_id", chatID)
			return
		}
		slog.Warn("rehydrate: resume failed", "workflow_id", workflowID, "chat_id", chatID, "error", cErr)
		return
	}
	// Arm here: this path calls the verb directly, so only the `run_start` frame would otherwise cover it.
	rs.armDeadline(ctx, workflowID)
	slog.Info("rehydrate: resumed restart-paused run", "workflow_id", workflowID, "chat_id", chatID)
}

// listRaw lists runs with their raw parent session ids, for callers that scope by
// session chain rather than by resolved chat.
func (rs *Runs) listRaw(ctx context.Context) ([]kasWorkflowRun, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := rs.engine.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}})
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list.Runs, nil
}

// cancelForChat cancels every live run this chat's sessions launched: closing the chat's session leaves them
// running. Starts with a record read, so a deleted chat no-ops.
func (rs *Runs) cancelForChat(ctx context.Context, chatID marotte.ChatID, stop runStop) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	rs.cancelForSessions(ctx, chatID, chat.SessionChain(), stop)
}

// cancelForSessions cancels every live run these sessions launched, from a captured chain with no record
// read, best-effort so a tab close never waits. One inventory read.
func (rs *Runs) cancelForSessions(ctx context.Context, chatID marotte.ChatID, sessionChain []string, stop runStop) {
	if len(sessionChain) == 0 {
		return
	}
	chain := make(map[string]bool, len(sessionChain))
	for _, id := range sessionChain {
		chain[id] = true
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("close: run list unavailable, skipping run cancel", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if marotte.RunStatus(r.Status).Terminal() || !chain[r.ParentSessionID] {
			continue
		}
		if cErr := rs.cancelAs(ctx, r.WorkflowID, stop); cErr != nil {
			slog.Warn("close: run cancel failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", cErr)
			continue
		}
		slog.Info("close: cancelled chat's run", "workflow_id", r.WorkflowID, "chat_id", chatID)
	}
}

// deleteForSessions deletes every run these sessions launched, whatever its status: KAS refuses a session
// delete while the session owns a live run.
func (rs *Runs) deleteForSessions(ctx context.Context, chatID marotte.ChatID, sessionChain []string, stop runStop) {
	if len(sessionChain) == 0 {
		return
	}
	chain := make(map[string]bool, len(sessionChain))
	for _, id := range sessionChain {
		chain[id] = true
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("delete: run list unavailable, skipping run delete", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if !chain[r.ParentSessionID] {
			continue
		}
		if dErr := rs.deleteAs(ctx, r.WorkflowID, stop); dErr != nil {
			slog.Warn("delete: run delete failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", dErr)
		}
	}
}
