package agent

// The run host: launching a PARENTLESS workflow run and the process that holds it.
// An agent-launched run is parented on its chat's session by KAS and needs nothing
// here; a manual or scheduled one gets ONE bridge of its own under the synthetic chat
// id `run:<workflowId>`, which every response path already resolves by chat id. That
// id gets no chat file. A paused run keeps its lease after its process closes, so it
// stays reachable and hostRun re-hosts it on demand.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/workflow"
)

// Workflow RPC param keys, shared across the five verbs.
const (
	keyWorkflowID     = "workflowId"
	keyWorkspacePaths = "workspacePaths"
)

// runChatPrefix namespaces the synthetic chat ids run bridges register under. Real
// chat ids are client-generated `c-*` uuids, so the namespace cannot collide.
const runChatPrefix = "run:"

// runChatID is the bridge-manager key for a run's bridge.
func runChatID(workflowID string) marotte.ChatID {
	return marotte.ChatID(runChatPrefix + workflowID)
}

// isRunChat reports whether a chat id names a run bridge.
func isRunChat(chatID marotte.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

// workflowIDOf recovers the run a bridge hosts from its synthetic chat id, or "" for
// anything that is not one.
func workflowIDOf(chatID marotte.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch handshake. Generous because a first launch may
// unpack a ~240 MB KAS runtime tree before the process answers.
const launchTimeout = 120 * time.Second

// errRecipeBusy reports the single-run rule: one live run per recipe definition,
// globally. Concurrency is declared INSIDE a workflow.
var errRecipeBusy = errors.New("this recipe already has a live run")

// Launch starts one parentless ATTENDED run of the recipe with the given source key
// and returns its workflow id and name.
func (rs *Runs) Launch(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return rs.launch(ctx, source, inputs, manualLaunch())
}

// LaunchScheduled launches a run on behalf of a schedule, marking it UNATTENDED for
// the duration. slotAt is when this run's own next slot comes due; zero means the idle
// window and backstop alone bound it.
func (rs *Runs) LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error) {
	return rs.launch(ctx, source, nil, scheduledLaunch(scheduleID, slotAt))
}

// launch is the shared body of both public launch verbs.
func (rs *Runs) launch(ctx context.Context, source string, inputs map[string]string, o launchOrigin) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := rs.recipeBySource(cctx, source)
	if err != nil {
		return "", "", err
	}
	if o.origin == runlease.OriginManual {
		// A manual run yields to its recipe's next slot, known only once resolved.
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	if bErr := rs.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// Started OUTSIDE the manager: the map key is the workflow id, which only `new`'s
	// reply knows. Call replies ride the readLoop, so Forward can attach afterwards.
	bridge := rs.bridges.factory()
	if sErr := bridge.Start(cctx, &marotte.StartOpts{
		Lifetime: rs.lifecycle.shutdownCtx,
		// Named rather than inherited from buildACPArgs's default, so a change there
		// cannot launch a run bridge on the legacy engine. Same argv today.
		AgentEngine: resolveAgentEngine(),
		Presets:     securityPresets(cctx, rs.lifecycle.configDir),
		IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, rs.lifecycle.configDir) },
		ToolSearch:  toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:   knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:      memoryEnabled(cctx, rs.lifecycle.configDir),
	}); sErr != nil {
		return "", "", fmt.Errorf("run bridge start: %w", sErr)
	}

	// Refuse BEFORE the RPC rather than sending an empty value: a started bridge with
	// no session is a broken handshake, and letting KAS answer buys its param complaint
	// at the cost of a round trip and an error naming the wrong layer.
	parent := bridge.SessionID()
	if parent == "" {
		bridge.Stop()
		return "", "", errors.New("run bridge started with no ACP session; cannot launch a workflow")
	}

	wfID, err := rs.workflowNew(cctx, bridge, recipe.Source, inputs, parent)
	if err != nil {
		bridge.Stop()
		return "", "", err
	}

	// Register BEFORE invoke: the first lifecycle frame follows it immediately, and
	// a frame arriving before the map entry has no bridge to answer through.
	launched := &sharedBridge{bridge: bridge, state: bridgeIdle}
	if !rs.bridges.insert(runChatID(wfID), launched) {
		bridge.Stop()
		return "", "", fmt.Errorf("workflow %s already has a run bridge", wfID)
	}
	// The run's envelope, before anything can execute — see runlease.Lease.
	rs.grantLease(cctx, wfID, recipe.Name, o)
	rs.coord.goForward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// The run was created but never started, so nothing is executing.
		rs.releaseLease(cctx, wfID)
		rs.coord.CloseBridge(cctx, runChatID(wfID), marotte.TurnOutcomeInterrupted)
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	// After invoke, so a run that never started leaves no timer; idempotent with
	// the `run_start` frame's own arm.
	rs.armDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// Cancel asks a run to stop, on the USER's behalf. A LOST termination claim returns
// nil — something else got there first — and a refused RPC is handed back, with
// retryTermination re-attempting it on its own budget.
func (rs *Runs) Cancel(ctx context.Context, workflowID string) error {
	return rs.cancelOn(ctx, workflowID, nil)
}

// cancelOn is Cancel for a caller that has ALREADY resolved the run's carrier, so
// stopping N runs costs one inventory read instead of N+1 (CancelForSessions). A nil
// carrier means resolve it here, which is every other caller.
func (rs *Runs) cancelOn(ctx context.Context, workflowID string, carrier *sharedBridge) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	return rs.finishTermination(ctx, workflowID, "", carrier)
}

// cancelRPC issues the cancel VERB and nothing else — no claim, no record. The bridge
// is NOT closed here: the owning process must live to the node boundary to certify the
// cancelled state, so the terminal `run_complete` closes it.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string, carrier *sharedBridge) error {
	return rs.control(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call", carrier)
}

// Delete removes a run and everything either side keeps about it, on the USER's
// behalf. The only run verb that is not recoverable; a failed verb leaves everything
// in place, because the run still exists in KAS.
//
// Deliberately unlike cancel: no termination claim (KAS's delete cancels a
// non-terminal run itself) and the bridge IS closed.
func (rs *Runs) Delete(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if err := rs.control(ctx, workflowID, methodKiroWorkflowDelete, "workflow delete call", nil); err != nil {
		return err
	}
	rs.deleteRunLog(ctx, workflowID)
	rs.coord.CloseBridge(ctx, runChatID(workflowID), marotte.TurnOutcomeInterrupted)
	rs.forgetBounds(ctx, workflowID)
	rs.clearEnd(workflowID)
	if rs.log != nil {
		if err := rs.log.RemoveDir(workflowID); err != nil {
			slog.Warn("run log: remove run directory", "workflow_id", workflowID, "error", err)
		}
	}
	slog.Info("workflow run deleted", "workflow_id", workflowID)
	return nil
}

// Pause asks a running run to stop at its next node boundary, keeping its state
// resumable. The in-flight node runs to completion, so the reply confirms the ASK
// rather than a paused state; a re-hosted pause is expected to be REFUSED, because
// KAS throws for a run its own process forgot.
func (rs *Runs) Pause(ctx context.Context, workflowID string) error {
	return rs.hostedControl(ctx, workflowID, methodKiroWorkflowPause)
}

// Resume re-drives a paused run, re-arming the wall clock the pause parked: each
// arm bounds EXECUTING time, so a resumed run gets a FRESH budget.
func (rs *Runs) Resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume)
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// retryTimeout bounds the whole retry handshake, process start included.
//
// BELOW the browser's own request budget deliberately: on a longer deadline a
// slow engine start let the BROWSER abort first, and that cancellation tore down
// a freshly minted bridge, releasing the lease with nobody watching. DERIVED from
// clientRequestBudget so the relationship cannot be broken by editing one of the
// two. A `var` only so a test can drive the expiry in milliseconds.
var retryTimeout = clientRequestBudget - 5*time.Second

// clientRequestBudget is the ceiling retryTimeout fits under: the timeout
// @cplieger/fetch hard-wires into every apiAction, applied by the browser
// whatever the server thinks its own deadline is.
const clientRequestBudget = 30 * time.Second

// errRetryEngineSlow reports that the retry could not be handed off inside
// retryTimeout. Its own class so the REST layer can answer 503 "try again".
var errRetryEngineSlow = errors.New(
	"the run's engine did not start in time, so nothing was retried. Try again",
)

// errRetryOutcomeUnreadable reports that KAS ACCEPTED the retry and its report
// could not be read.
//
// Its own class because the remedy inverts: the run may be executing, so retrying
// would ask for the work twice and killing the bridge would kill it mid-node.
// Refreshing is what the reader can act on.
var errRetryOutcomeUnreadable = errors.New(
	"the retry was accepted but its report could not be read, so which steps it reset " +
		"is unknown. Refresh the run to see where it is",
)

// kasRetryOutcome is `_kiro/workflow/retry`'s reply in KAS's own spelling, the
// decode target only; the verb answers marotte.RunRetriedResponse.
//
// RetriedNodeIDs is why Retry returns a value at all: a retry that reset five
// nodes and one that reset none are otherwise indistinguishable, which is what
// "I pressed Retry and nothing happened" looks like from outside.
type kasRetryOutcome struct {
	WorkflowID     string   `json:"workflowId"`
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retriedNodeIds"`
}

// Retry resets a finished run's failed work and reports what it reset.
//
// Legal only from `failed` or `aborted`, the statuses `closeStoppedBridge` tears the
// bridge down on. It is hosted from the gate's own read of where the run came from
// (aff), so the verb acts on the answer the gate approved.
func (rs *Runs) Retry(
	ctx context.Context, workflowID string, aff *runAffordance,
) (out marotte.RunRetriedResponse, err error) {
	if workflowID == "" {
		return out, errors.New("missing workflow id")
	}
	cctx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()

	host, err := rs.acquireHost(cctx, workflowID, func(context.Context, string) runOrigin { return aff.origin })
	if err != nil {
		return out, rs.retryStartErr(cctx, err)
	}
	defer func() {
		// An unreadable report means KAS TOOK the retry, so the run may be re-driving
		// inside this carrier.
		if errors.Is(err, errRetryOutcomeUnreadable) {
			host.release(nil)
			return
		}
		host.release(err)
	}()
	// recipe is threaded from the gate because nothing here can learn it once the run
	// is re-driving.
	recipe := aff.origin.recipe
	// The lease before the verb, as a launch grants between `new` and `invoke`:
	// retry's own `run_start` can arrive before the call returns.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(cctx, workflowID, recipe, manualLaunch())
		minted = true
	}
	out, err = rs.retryCall(cctx, host.sb.bridge, workflowID)
	if errors.Is(err, errRetryOutcomeUnreadable) {
		// KAS ACCEPTED the retry, so the run may be re-driving inside this carrier:
		// only the report is lost, and tearing down would kill the work mid-node.
		rs.rearmRetried(cctx, workflowID, recipe)
		return out, err
	}
	if err != nil {
		// Nothing is executing, so the minted lease goes back, except on a CONTEXT
		// error: armDeadline returns when there is no lease, so a retry KAS did take
		// would execute with no deadline and nothing to arm one.
		if minted && !isCtxErr(err) {
			rs.releaseLease(cctx, workflowID)
		}
		return out, err
	}
	// Only on success: a refused retry re-drove nothing, so the run's previous
	// terminal reason is still the truth about it.
	rs.rearmRetried(cctx, workflowID, recipe)
	slog.Info("workflow run retried",
		"workflow_id", workflowID, "recipe", recipe,
		"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
	return out, nil
}

// retryCall issues the verb and decodes its outcome report. Folded through
// runCallErr, so a JSON-RPC refusal arriving as a well-formed response with an
// `error` member is not read as a success.
func (rs *Runs) retryCall(
	ctx context.Context, bridge acpCaller, workflowID string,
) (marotte.RunRetriedResponse, error) {
	none := marotte.RunRetriedResponse{}
	resp, err := bridge.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
	if cErr := runCallErr(resp, err); cErr != nil {
		return none, fmt.Errorf("workflow retry: %w", rs.retryDeadlineErr(ctx, cErr))
	}
	// Past this point KAS has ACCEPTED the verb, so every failure below is a lost
	// REPORT rather than a retry that did not happen.
	if resp == nil || len(resp.Result) == 0 {
		return none, fmt.Errorf("%w: the reply carried no outcome", errRetryOutcomeUnreadable)
	}
	var out kasRetryOutcome
	if uErr := json.Unmarshal(resp.Result, &out); uErr != nil {
		return none, fmt.Errorf("%w: undecodable outcome: %w", errRetryOutcomeUnreadable, uErr)
	}
	// The reply must be ABOUT the run that was asked for: one run's outcome under
	// another's name would be reported to the reader as theirs.
	if out.WorkflowID != "" && out.WorkflowID != workflowID {
		return none, fmt.Errorf("%w: the reply names run %q, not %q",
			errRetryOutcomeUnreadable, out.WorkflowID, workflowID)
	}
	// Never nil on the wire, so a counting caller need not tell "none" from "absent".
	nodes := out.RetriedNodeIDs
	if nodes == nil {
		nodes = []string{}
	}
	return marotte.RunRetriedResponse{Status: out.Status, RetriedNodeIDs: nodes}, nil
}

// retryStartErr labels a failed bridge start: this call's deadline, or the start
// failure itself.
func (rs *Runs) retryStartErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return fmt.Errorf("retry bridge start: %w", err)
}

// retryDeadlineErr rewrites a call that failed on THIS call's budget into
// errRetryEngineSlow, so the reader is told to try again.
func (rs *Runs) retryDeadlineErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return err
}

// errStepStatusRefused means KAS resolved the run and DECLINED the update instead of
// throwing: the run is terminal, or holds no running-or-paused step to mark. A state
// of the world rather than a fault, so the REST layer answers 409 with KAS's sentence.
var errStepStatusRefused = errors.New("this run's step status was not changed")

// updateStatusAction is `_kiro/workflow/update`'s action id for a step-status write.
// Sent EXPLICITLY although any non-`replace_remaining` value works: the tool schema
// declares `action` required, so an absent value is accepted only by accident.
const updateStatusAction = "update_status"

// errStepStatusMistargeted means the node the caller named is not the node KAS's
// resolver would mark, so the write was WITHHELD. Wraps errStepStatusRefused, so the
// REST layer's 409 arm covers it too.
var errStepStatusMistargeted = fmt.Errorf(
	"%w: this run is not waiting at the step you asked to mark", errStepStatusRefused,
)

// errStepStatusUnreadable means the pre-send read failed, so whether the caller's node
// is the one KAS would mark is UNKNOWN. Withheld rather than sent: a `completed` on the
// wrong node publishes its capture and stamps it finished, which nothing undoes.
var errStepStatusUnreadable = fmt.Errorf(
	"%w: this run's state could not be read just now, so try again in a moment",
	errStepStatusRefused,
)

// SetStepStatus marks a step completed, failed, or running so a wedged run can
// advance. The verb carries NO node id, so KAS resolves its target positionally and a
// client naming node X can have KAS mark node Y — the tree is READ first and the write
// withheld unless the two agree.
func (rs *Runs) SetStepStatus(ctx context.Context, workflowID, nodeID, status string) (err error) {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if !slices.Contains(runStepStatuses, status) {
		return fmt.Errorf("step status must be one of %v", runStepStatuses)
	}
	// BEFORE hostRun, unlike AnswerInput's read: `inspect` runs on the utility
	// session, so a withheld write spends no process start. The run's position lock
	// spans read and write: opening a chat starts its heal, which may resume this run.
	release, lockErr := rs.positions.acquire(ctx, workflowID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	if addrErr := rs.stepStatusAddress(ctx, workflowID, nodeID); addrErr != nil {
		return addrErr
	}
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	// A decline releases with a cause too: nothing was written, so no run_complete
	// follows and a re-host's process would outlive the verb.
	defer func() { host.release(err) }()
	resp, callErr := rs.callReclaiming(ctx, host.sb.bridge, workflowID, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"action":      updateStatusAction,
		"status":      status,
	})
	if callErr != nil {
		return callErr
	}
	// The REPLY is read, because a decline is not a throw: both declines answer
	// {updated:false} with a 200, which a caller ignoring it reports as landed.
	if refusal := stepStatusRefusal(resp); refusal != "" {
		return fmt.Errorf("%w: %s", errStepStatusRefused, refusal)
	}
	if status == runStepRunning {
		// Re-driven WITHOUT the user's words, so whatever it asked is no longer
		// answerable. SettledByUser because this IS the reader's decision.
		rs.settleAskForNode(ctx, workflowID, nodeID, marotte.SettledByUser)
	}
	return nil
}

// stepStatusRefusal returns KAS's reason when it declined the update, "" when it took
// it. `Updated` is a *bool so ABSENT reads as taken: an unstated field must not make a
// verb that worked report a refusal. A queued update was taken, so `queued` is unread.
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

// stepStatusAddress reports whether the node a caller named is the node KAS would
// mark, and REFUSES rather than falling back when it cannot tell: here a wrong answer
// costs a positional WRITE that publishes a capture and stamps endedAt, which nothing
// undoes.
func (rs *Runs) stepStatusAddress(ctx context.Context, workflowID, nodeID string) error {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a run's state, so its step status was left alone",
			"workflow_id", workflowID, "error", err)
		return errStepStatusUnreadable
	}
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		slog.Warn("a run's state did not decode, so its step status was left alone",
			"workflow_id", workflowID)
		return errStepStatusUnreadable
	}
	target := statusUpdateTarget(res.State.Root)
	if target == nil {
		slog.Info("a run holds no running or paused step, so nothing was marked",
			"workflow_id", workflowID, "node_id", scrubLog(nodeID),
			"status", scrubLog(string(res.State.Status)))
		return errStepStatusMistargeted
	}
	if target.NodeID != nodeID {
		slog.Info("a run's current step is not the one being marked, "+
			"so the status write was withheld", "workflow_id", workflowID,
			"asked_node_id", scrubLog(nodeID), "target_node_id", scrubLog(target.NodeID))
		return errStepStatusMistargeted
	}
	return nil
}

// stepNodeType is the state tree's `type` for a STEP node — the only kind KAS's
// resolver considers, which keeps a paused `parallel` container out of the answer.
const stepNodeType = "step"

// statusUpdateTarget resolves the node `_kiro/workflow/update` would mark, mirroring
// KAS's own resolver: the first RUNNING step node in pre-order, else the first PAUSED
// one. Not pausedLeaf, which answers where the run is WAITING, over leaves.
func statusUpdateTarget(root *askNode) *askNode {
	running, paused := stepTargets(root)
	if running != nil {
		return running
	}
	return paused
}

// stepTargets is statusUpdateTarget's ONE pre-order pass, returning the first RUNNING
// step node and the first PAUSED one. Both in one traversal because the resolver's own
// arms share one, and each node is judged BEFORE its children so "first" means the same
// thing here as there; a running step returns immediately and never descends.
func stepTargets(n *askNode) (running, paused *askNode) {
	if n == nil {
		return nil, nil
	}
	if n.Type == stepNodeType {
		if n.Status == marotte.RunNodeStatusRunning {
			return n, nil
		}
		if n.Status == marotte.RunNodeStatusPaused {
			paused = n
		}
	}
	for i := range n.Children {
		hitRunning, hitPaused := stepTargets(&n.Children[i])
		if hitRunning != nil {
			return hitRunning, nil
		}
		if paused == nil {
			paused = hitPaused
		}
	}
	return nil, paused
}

// The step statuses a human may set. `running` is the CONTINUE-WITHOUT-ANSWERING
// verb rather than a mark, and plain Resume cannot substitute for it.
const (
	runStepCompleted = "completed"
	runStepFailed    = "failed"
	runStepRunning   = "running"
)

// runStepStatuses is the allowlist, in the order the refusal names them.
var runStepStatuses = []string{runStepCompleted, runStepFailed, runStepRunning}

// errAskAlreadySettled means the ask an answer names is no longer open. Distinct
// from a KAS refusal so the REST layer answers 409 rather than 500.
var errAskAlreadySettled = errors.New(
	"that question has already been answered, or the step it belonged to has moved on",
)

// AnswerInput answers one parked step with the user's words, as a plain
// `session/prompt` addressed to the PAUSED STEP's own session.
//
// THE ORDER IS THE CONTRACT: carrier, then claim, then address, then send. Each
// step guards a window the next one would open. A failed send puts the claim back.
func (rs *Runs) AnswerInput(ctx context.Context, workflowID, askID, text string) (err error) {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("an answer cannot be empty")
	}
	// BEFORE the claim, so no instant has the registry empty AND nothing reporting
	// an answer in flight — see pendingRunAsks.beginAnswer.
	rs.asks.beginAnswer(workflowID)
	defer rs.asks.endAnswer(workflowID)
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	// Held from HERE, not from the Call: the address read below is a round trip, and a
	// bound coming due inside it would close the carrier this answer is about to use.
	defer func() { host.release(err) }()
	a, ok := rs.asks.TakeIfPresent(workflowID, askID)
	if !ok {
		return errAskAlreadySettled
	}
	session, verdict := rs.answerAddress(ctx, workflowID, a)
	if verdict == answerMoot {
		// Nobody has to answer this any more, so it is MOOT rather than restored:
		// re-offering a card for a step that has moved on asks the reader to
		// answer a question KAS has stopped waiting on.
		rs.announceSettled(ctx, a, marotte.SettledByMoot)
		return errAskAlreadySettled
	}
	if verdict == answerBusy {
		// EARLY, not stale: the run is between steps, so the words are still
		// wanted. The card goes back and the reader is told to retry — settling
		// here would discard what they typed and tell them the step had moved on.
		rs.restoreAsk(ctx, a)
		return errRunNotParked
	}
	if session == "" {
		rs.restoreAsk(ctx, a)
		return errors.New("the step that asked cannot be addressed on this server")
	}
	resp, cErr := host.sb.bridge.Call(ctx, marotte.MethodPrompt, map[string]any{
		marotte.KeySessionID: session,
		marotte.KeyPrompt:    []any{marotte.TextBlock(text)},
	})
	if callErr := runCallErr(resp, cErr); callErr != nil {
		rs.restoreAsk(ctx, a)
		return callErr
	}
	// A FRESH budget, the rule Resume follows: each arm bounds EXECUTING time.
	rs.armDeadline(ctx, workflowID)
	rs.announceSettled(ctx, a, marotte.SettledByUser)
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_id", a.payload.NodeID, "ask_id", askID)
	return nil
}

// errRunNotParked means the run is executing rather than waiting, so there is no
// parked step for KAS to reroute the answer into YET. Distinct from
// errAskAlreadySettled because it is retryable and the card survives it.
var errRunNotParked = errors.New(
	"this run is not waiting on an answer right now, so try again in a moment",
)

// answerVerdict is what a fresh read of the run says about the ask being answered.
type answerVerdict int

const (
	// answerSend: the ask's own step is parked, so KAS reroutes the prompt into it.
	answerSend answerVerdict = iota
	// answerMoot: nothing is waiting on this question any more.
	answerMoot
	// answerBusy: the run is BETWEEN steps, so the answer is early rather than stale.
	answerBusy
)

// answerAddress resolves where one ask's answer must be sent, and grades what the
// run's state says about the question.
//
// THE FRESH READ LEADS and the ask's own address is only the fallback, because a
// prompt KAS does not reroute runs as an ordinary turn on that session. An
// UNREADABLE run falls back rather than refusing: a failed read never destroys work.
func (rs *Runs) answerAddress(
	ctx context.Context, workflowID string, a *runAsk,
) (session string, verdict answerVerdict) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a parked run's state, so its ask answers to the address it carries",
			"workflow_id", workflowID, "error", err)
		return a.payload.StepSessionID, answerSend
	}
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return a.payload.StepSessionID, answerSend
	}
	if step := askedStep(res.State.Root, a.payload.NodeID); step != nil {
		return cmp.Or(step.SessionID, a.payload.StepSessionID), answerSend
	}
	if parked, _ := pausedLeaf(res.State.Root, nil); parked != nil {
		slog.Info("a run is parked at a different step than the one being answered, "+
			"so the answer was withheld", "workflow_id", workflowID,
			"asked_node_id", scrubLog(a.payload.NodeID), "parked_node_id", scrubLog(parked.NodeID))
		return "", answerMoot
	}
	if res.State.Status.Terminal() {
		return "", answerMoot
	}
	slog.Info("a run is not parked on any step right now, so its answer was held back "+
		"rather than discarded", "workflow_id", workflowID,
		"asked_node_id", scrubLog(a.payload.NodeID), "status", scrubLog(string(res.State.Status)))
	return "", answerBusy
}

// askedStep finds the PARKED step one ask belongs to, or nil when that step is not
// parked right now. Addressed by the ask's own NODE ID rather than by pausedLeaf's
// first depth-first match, which is what makes a parallel run's second parked branch
// answerable.
//
// An EMPTY id matches any parked step: such an ask was minted before that field
// existed, so it keeps pausedLeaf's older behaviour rather than being refused. A
// repeat's iterations SHARE an id, so any parked instance of it answers yes.
func askedStep(n *askNode, nodeID string) *askNode {
	if n == nil {
		return nil
	}
	if len(n.Children) == 0 {
		if n.Status == marotte.RunNodeStatusPaused && (nodeID == "" || n.NodeID == nodeID) {
			return n
		}
		return nil
	}
	for i := range n.Children {
		if hit := askedStep(&n.Children[i], nodeID); hit != nil {
			return hit
		}
	}
	return nil
}

// hostedControl issues a verb that must run on a process holding the run, never on
// the utility bridge: that session denies every permission request and errors every
// fs/terminal call, so a run resumed there would grind with no tools.
func (rs *Runs) hostedControl(ctx context.Context, workflowID, method string) error {
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	_, callErr := rs.callReclaiming(ctx, host.sb.bridge, workflowID, method, map[string]any{keyWorkflowID: workflowID})
	host.release(callErr)
	return callErr
}

// callReclaiming sends a verb that loads the run into b's process. The first KAS to
// start after a crash stamps each run it reconciles as its own (acp-server.js
// sweepStaleRuns), and that is the utility session, which every run read starts. The
// stamp refuses every other process until its owner exits or it goes stale, about
// 135 s later. The utility does no workflow work of its own, so on that refusal it is
// stopped, which voids the stamp, and the verb is sent again. A concurrent verb may have
// stopped it already, so the resend does not depend on this call finding it live.
func (rs *Runs) callReclaiming(
	ctx context.Context, b acpCaller, workflowID, method string, params map[string]any,
) (*marotte.RPCResponse, error) {
	resp, err := b.Call(ctx, method, params)
	callErr := runCallErr(resp, err)
	if !errors.Is(callErr, workflow.ErrOwnedElsewhere) {
		return resp, callErr
	}
	rs.utility().session.Stop()
	slog.Info("a run was held by another process, so the utility session was stopped "+
		"and the verb sent again", "workflow_id", workflowID, "method", method)
	return resendWhileOwned(ctx, b, method, params)
}

// reclaimGrace bounds resendWhileOwned. KAS's liveness probe is `process.kill(pid, 0)`,
// and the stopped session's KAS leaves the process table some 25 ms after Stop
// returns (measured on kiro-cli 2.27.0).
var reclaimGrace = 2 * time.Second

const reclaimPoll = 50 * time.Millisecond

// resendWhileOwned sends the verb until KAS stops refusing it for another owner, the
// grace ends or ctx does, and answers the last reply.
func resendWhileOwned(
	ctx context.Context, b acpCaller, method string, params map[string]any,
) (*marotte.RPCResponse, error) {
	deadline := time.Now().Add(reclaimGrace)
	for {
		resp, err := b.Call(ctx, method, params)
		callErr := runCallErr(resp, err)
		if !errors.Is(callErr, workflow.ErrOwnedElsewhere) || !time.Now().Before(deadline) {
			return resp, callErr
		}
		wait := time.NewTimer(reclaimPoll)
		select {
		case <-ctx.Done():
			wait.Stop()
			return resp, callErr
		case <-wait.C:
		}
	}
}

// errRunHostStart marks a failure to START a carrier for a run: a spawn fault on
// this server, not a statement about the run. The REST layer keys on it to answer
// 500 rather than echoing an internal path as though the caller had asked wrongly.
var errRunHostStart = errors.New("a process for this run could not be started")

// errLaunchSessionUnavailable means no carrier here can hold the session a run was
// launched from: the inventory names none, or the chat owning it is open on another
// session. KAS checks every step against that session, so driving the run anyway
// would leave the profile's presets out of its steps. A state of the run, answered 409
// with launchSessionUnavailableText.
var errLaunchSessionUnavailable = errors.New("the run's launching session cannot be opened here")

// launchSessionUnavailableText is errLaunchSessionUnavailable as the reader sees it.
const launchSessionUnavailableText = "This run's launching session cannot be opened on this server. " +
	"Cancel and delete still work."

// errRunNotListed means KAS's inventory does not list the run, so where it was
// launched from is unknown. Answered 404.
var errRunNotListed = errors.New("run not found")

// runParent is where a run was launched from: its launching session, and the chat
// owning it, "" for a parentless run.
type runParent struct {
	chat    marotte.ChatID
	session string
}

// runOrigin is a run's launch facts, read once off KAS's inventory and the chat
// store. err says why parent cannot route a verb, and is nil when it can.
type runOrigin struct {
	err      error
	parent   runParent
	chatName string
	recipe   string
}

// originOf reads where a run was launched from. A parent with no owning chat is
// trusted only after a COMPLETE chat scan: an unreadable chat file could own the
// session, and loading a chat's session onto a run bridge replays it into a carrier.
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
	// WorkflowName, not Name: the recipe is what the single-run rule compares a
	// re-armed lease against, and Name is `runLabel ?? workflowName`.
	o := runOrigin{recipe: runs[i].WorkflowName, parent: runParent{session: runs[i].ParentSessionID}}
	if o.parent.session == "" {
		o.err = fmt.Errorf("%w: the run inventory names no launching session", errLaunchSessionUnavailable)
		return o
	}
	var complete bool
	o.parent.chat, o.chatName, complete = rs.chatForSession(ctx, o.parent.session)
	if o.parent.chat == "" && !complete {
		o.err = fmt.Errorf("%w: a chat file could not be read, so which chat launched the run is unknown",
			errRunHostStart)
	}
	return o
}

// runHost is one verb's hold on the process carrying a run, counted in carrierUse
// before the run's host lock is released, so no other verb can end the carrier
// uncounted. Every verb releases it exactly once.
type runHost struct {
	rs         *Runs
	sb         *sharedBridge
	chatID     marotte.ChatID
	workflowID string
	// started is whether this verb started the carrier, which makes release the one
	// place its end is decided.
	started bool
}

// hostRun is acquireHost for a verb with no inventory read of its own.
func (rs *Runs) hostRun(ctx context.Context, workflowID string) (*runHost, error) {
	return rs.acquireHost(ctx, workflowID, rs.originOf)
}

// acquireHost resolves the carrier for one verb under the run's host lock, which a
// waiter gives up on with its own ctx: the run's own bridge, else its launching session
// made live with the profile's presets, never a carrier on any other session. KAS
// checks every step against that session, and rebuilds it from disk without presets
// when no process has it loaded. origin is read only when the run's own bridge is not
// live, so a hosted run costs no `workflow/list` trip.
func (rs *Runs) acquireHost(
	ctx context.Context, workflowID string, origin func(context.Context, string) runOrigin,
) (*runHost, error) {
	unlock, err := rs.hosts.acquire(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if sb := rs.runOwnBridge(workflowID); sb != nil {
		defer unlock()
		return rs.hold(workflowID, runChatID(workflowID), sb, false), nil
	}
	o := origin(ctx, workflowID)
	if o.err != nil {
		unlock()
		return nil, o.err
	}
	if o.parent.chat != "" {
		return rs.acquireDurably(ctx, unlock, func(dctx context.Context) (*runHost, error) {
			sb, err := rs.rehostOnChat(dctx, workflowID, o.parent)
			if err != nil {
				return nil, err
			}
			return rs.hold(workflowID, o.parent.chat, sb, false), nil
		})
	}
	return rs.loadRunCarrier(ctx, workflowID, o.parent.session, unlock)
}

// hold enters sb for one verb. The caller holds the run's host lock.
func (rs *Runs) hold(workflowID string, chatID marotte.ChatID, sb *sharedBridge, started bool) *runHost {
	rs.carriers.enter(sb)
	return &runHost{rs: rs, sb: sb, chatID: chatID, workflowID: workflowID, started: started}
}

// errVerbNeverSent is the release cause for a host acquired on behalf of a verb that
// had already given up, so KAS took nothing.
var errVerbNeverSent = errors.New("the verb gave up before anything was sent")

// acquireDurably runs acquire on a durable context in a tracked goroutine that owns
// unlock, so the run's host lock is held until acquisition ends even when the verb
// waits no longer than its own ctx. A cancelled spawn would leave a chat detached from
// its session for good (tryLoadSession's failure branch), and a half-loaded carrier
// visible to the next verb. The lock is released before the host is handed over, so it
// never spans the verb's RPC, and a host acquired for a verb that gave up is released
// with errVerbNeverSent while the lock is still held, so no queued verb can enter it.
func (rs *Runs) acquireDurably(
	ctx context.Context, unlock func(), acquire func(context.Context) (*runHost, error),
) (*runHost, error) {
	type acquired struct {
		h   *runHost
		err error
	}
	var (
		mu                   sync.Mutex
		abandoned, delivered bool
	)
	done := make(chan acquired, 1)
	dctx := durable.Context(ctx)
	rs.lifecycle.inflight.Go(func() {
		h, err := acquire(dctx)
		mu.Lock()
		if abandoned {
			mu.Unlock()
			if h != nil {
				h.releaseLocked(errVerbNeverSent)
			}
			unlock()
			return
		}
		delivered = true
		mu.Unlock()
		unlock()
		done <- acquired{h: h, err: err}
	})
	select {
	case r := <-done:
		return r.h, r.err
	case <-ctx.Done():
		mu.Lock()
		if delivered {
			// The host is already the verb's: it fails on its own ctx and releases it.
			mu.Unlock()
			r := <-done
			return r.h, r.err
		}
		abandoned = true
		mu.Unlock()
		return nil, ctx.Err()
	}
}

// release ends this verb's hold. cause is why the verb failed, nil when it landed or
// KAS took it. A carrier the verb started and that failed it is ended here and only
// here, under the run's host lock so no verb enters it meanwhile. A context error
// means KAS may have taken the verb, and a carrier another verb has held may be
// driving the run, so both are kept under the kept-carrier bound; otherwise it closes.
func (h *runHost) release(cause error) {
	rs := h.rs
	if !h.started || cause == nil {
		rs.carriers.leave(h.sb)
		return
	}
	unlock, err := rs.hosts.acquire(rs.lifecycle.shutdownCtx, h.workflowID)
	if err != nil {
		// Shutting down, which stops every carrier.
		rs.carriers.leave(h.sb)
		return
	}
	defer unlock()
	h.releaseLocked(cause)
}

// releaseLocked is release's decision for a carrier the verb started, made while the
// caller holds the run's host lock.
func (h *runHost) releaseLocked(cause error) {
	rs := h.rs
	rs.carriers.leave(h.sb)
	if !h.started || cause == nil {
		return
	}
	switch {
	case isCtxErr(cause):
		slog.Warn("a run verb ended with its context cancelled, so its carrier is kept: "+
			"whether KAS took the verb is unknown, and frames from a run it drove would go nowhere",
			"workflow_id", h.workflowID, "error", cause)
		rs.boundKeptCarrier(h.chatID, h.workflowID, h.sb)
	case h.sb.runVerbs.Load() > 1:
		rs.boundKeptCarrier(h.chatID, h.workflowID, h.sb)
	case rs.bridges.get(h.chatID) == h.sb:
		rs.coord.CloseBridge(rs.lifecycle.shutdownCtx, h.chatID, marotte.TurnOutcomeInterrupted)
	}
}

// rehostOnChat reaches a chat-parented run through the chat's own bridge, never a run
// bridge, which would be a second resident on the chat's session. That bridge must
// hold the run's own launching session: OpenBridge falls back to a fresh session when
// a load fails, and loads only the chat's CURRENT segment.
func (rs *Runs) rehostOnChat(
	ctx context.Context, workflowID string, parent runParent,
) (*sharedBridge, error) {
	held, err := rs.coord.OpenBridge(ctx, parent.chat, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunHostStart, err)
	}
	// OpenBridge hands back a bridge another caller is still starting.
	if !held.startedPastSpawn() {
		return nil, fmt.Errorf("%w: the launching chat is still opening", errRunHostStart)
	}
	if got := string(held.SessionID()); got != parent.session {
		slog.Warn("a run's launching chat is open on another session, so the run was not driven",
			"workflow_id", workflowID, "chat_id", parent.chat,
			"launch_session", parent.session, "chat_session", got)
		return nil, errLaunchSessionUnavailable
	}
	return held, nil
}

// loadRunCarrier makes a parentless run's launching session live on a run bridge under
// the run's synthetic chat id, and takes ownership of unlock. Registered and forwarded
// BEFORE the load, because session/load sends host requests Forward answers through
// the map (ACPBridge.NotifCh).
func (rs *Runs) loadRunCarrier(
	ctx context.Context, workflowID, session string, unlock func(),
) (*runHost, error) {
	chatID := runChatID(workflowID)
	sb, existed := rs.bridges.orInsert(chatID)
	if existed {
		unlock()
		return nil, fmt.Errorf("%w: a run bridge is already registered for this run", errRunHostStart)
	}
	return rs.acquireDurably(ctx, unlock, func(dctx context.Context) (*runHost, error) {
		if err := rs.startRunCarrier(dctx, chatID, sb, session); err != nil {
			return nil, fmt.Errorf("%w: %w", errRunHostStart, err)
		}
		slog.Info("re-hosted a run by loading its launching session", "workflow_id", workflowID,
			"session_id", session)
		return rs.hold(workflowID, chatID, sb, true), nil
	})
}

// startRunCarrier loads session on sb's bridge. On failure sb leaves the map and its
// bridge is stopped, which is what ends the forward loop attached before Start.
func (rs *Runs) startRunCarrier(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, session string,
) error {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	rs.coord.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(cctx, &marotte.StartOpts{
		Lifetime:    rs.lifecycle.shutdownCtx,
		SessionID:   session,
		AgentEngine: resolveAgentEngine(),
		Presets:     securityPresets(cctx, rs.lifecycle.configDir),
		IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, rs.lifecycle.configDir) },
		ToolSearch:  toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:   knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:      memoryEnabled(cctx, rs.lifecycle.configDir),
	}); err != nil {
		rs.bridges.removeIfSame(chatID, sb)
		sb.bridge.Stop()
		return err
	}
	sb.setIdle()
	return nil
}

// carrierUse counts the run verbs currently HOLDING each carrier, so the kept-carrier
// bound asks rather than inferring. The key is the CARRIER, because one carrier holds
// several verbs in turn.
type carrierUse struct {
	held map[*sharedBridge]int
	// onIdle is the close a lifecycle frame deferred because a verb was holding the
	// carrier. See whenIdle.
	onIdle map[*sharedBridge]func()
	mu     sync.Mutex
}

// enter records that a verb is using sb.
//
// Paired with leave over the WHOLE span from resolving the carrier to returning,
// never around the Call alone: AnswerInput's span contains an `inspect` round trip
// between taking the carrier and sending, so a Call-scoped count would leave the
// reader's carrier closable for the length of that read.
func (c *carrierUse) enter(sb *sharedBridge) {
	sb.runVerbs.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil {
		c.held = make(map[*sharedBridge]int)
	}
	c.held[sb]++
}

// leave records that a verb has finished with sb, and runs a close a lifecycle frame
// deferred. The entry is DELETED at zero rather than left holding 0: the key is a live
// pointer, so a retained entry would hold that bridge for the process's life.
//
// The deferred close runs OUTSIDE the lock — it tears a process down, so holding c.mu
// across it would block every other verb's enter and leave.
func (c *carrierUse) leave(sb *sharedBridge) {
	c.mu.Lock()
	if c.held[sb] > 1 {
		c.held[sb]--
		c.mu.Unlock()
		return
	}
	delete(c.held, sb)
	closeFn := c.onIdle[sb]
	delete(c.onIdle, sb)
	c.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
}

// busy reports whether any run verb is holding sb right now.
func (c *carrierUse) busy(sb *sharedBridge) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[sb] > 0
}

// whenIdle runs closeFn once no verb is holding sb — immediately when none is.
//
// It is what lets a LIFECYCLE frame ask the kept-carrier bound's question without a
// timer, and TWO INDEPENDENT terminators end the wait: the caller's context, and KAS
// answering — likely here, a terminal frame just arrived. Bridge exit is NOT a third,
// being the close being deferred. A repeat frame
// REPLACES the pending closer rather than queueing beside it — one carrier, one close,
// so exactly-once is the whole requirement.
func (c *carrierUse) whenIdle(sb *sharedBridge, closeFn func()) {
	c.mu.Lock()
	if c.held[sb] == 0 {
		c.mu.Unlock()
		closeFn()
		return
	}
	if c.onIdle == nil {
		c.onIdle = make(map[*sharedBridge]func())
	}
	c.onIdle[sb] = closeFn
	c.mu.Unlock()
}

// runLocks is one lock per run that a waiter can give up on with its ctx, which
// sync.Mutex cannot. A ctx already ended when a lock frees loses, so a verb that gave
// up never acquires.
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

// keptCarrierGrace is how long a carrier kept on an unknown outcome is given before
// its run is re-read. Still STRICTLY longer than launchTimeout, which covers Retry's
// own bounded call, but the grace is no longer the safety argument — carrierUse is.
// A `var` so a test drives the path in milliseconds; never reassigned in production.
var keptCarrierGrace = 10 * time.Minute

// carrierVerdict is what one firing of the kept-carrier bound decided.
type carrierVerdict int

const (
	// carrierClosed: the carrier was stopped.
	carrierClosed carrierVerdict = iota
	// carrierSpared: not this bound's to close, or its run is still executing.
	// Nothing further to wait for, so the bound ends here.
	carrierSpared
	// carrierBusy: a run verb is still holding it, so the answer is "ask again".
	carrierBusy
)

// boundKeptCarrier gives a carrier kept on a context error an END, because the reason
// it was kept is also the reason nothing else would ever close it.
//
// Untracked like healPaused's own AfterFunc and safe for the same reason: the guards
// below make a late firing cost one inspect instead of a wrong close. A BUSY verdict
// RE-ARMS, and that re-arm is NOT self-terminating — while a verb blocks on this
// carrier nothing here closes it, so the loop ends at that verb's own end or at
// shutdown.
func (rs *Runs) boundKeptCarrier(chatID marotte.ChatID, workflowID string, kept *sharedBridge) {
	time.AfterFunc(keptCarrierGrace, func() {
		if rs.closeKeptCarrier(chatID, workflowID, kept) == carrierBusy {
			rs.boundKeptCarrier(chatID, workflowID, kept)
		}
	})
}

// closeKeptCarrier is the bound's whole decision, split out so it is answerable
// without a timer. It decides under the run's host lock, so no verb can enter kept
// between the checks and the close.
//
// IDENTITY leads, so a stale bound neither closes a later re-host's carrier nor
// re-arms forever over one nothing will close; USE comes next because it is a local
// read and needs no RPC to decline.
func (rs *Runs) closeKeptCarrier(
	chatID marotte.ChatID, workflowID string, kept *sharedBridge,
) carrierVerdict {
	unlock, err := rs.hosts.acquire(rs.lifecycle.shutdownCtx, workflowID)
	if err != nil {
		// Shutting down, which stops every carrier.
		return carrierSpared
	}
	defer unlock()
	if rs.bridges.get(chatID) != kept {
		return carrierSpared
	}
	if rs.carriers.busy(kept) {
		slog.Info("a run verb is still holding a carrier its bound came due on, "+
			"so the carrier is kept and re-read later", "workflow_id", workflowID)
		return carrierBusy
	}
	res, ok := rs.inspect(rs.lifecycle.shutdownCtx, workflowID)
	if !ok || res.WorkflowID != workflowID {
		return carrierSpared
	}
	if !res.State.Status.Terminal() && res.State.Status != marotte.RunStatusPaused {
		return carrierSpared
	}
	slog.Info("closing a run carrier kept for a verb KAS never took",
		"workflow_id", workflowID, "status", res.State.Status)
	rs.coord.CloseBridge(rs.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
	return carrierClosed
}

// isCtxErr reports whether an error is a cancellation or a deadline, at any
// wrapping depth. It is the ONE condition under which a failed verb keeps the
// carrier it started (runHost.release).
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// hostBridgeChat resolves the bridge whose process holds the run's registry entry,
// plus the CHAT ID it belongs to, or "" when nothing here hosts the run: the run's
// own `run:<id>` bridge, or the LAUNCHING CHAT's, since KAS parents an agent-launched
// run on that session. The id is what a synthesised ask is keyed to (askChatID).
// Costs one `workflow/list` round trip on the second path only.
func (rs *Runs) hostBridgeChat(
	ctx context.Context, workflowID string,
) (marotte.ChatID, *sharedBridge) {
	if sb := rs.runOwnBridge(workflowID); sb != nil {
		return runChatID(workflowID), sb
	}
	o := rs.originOf(ctx, workflowID)
	if o.err != nil || o.parent.chat == "" {
		return "", nil
	}
	sb := rs.bridges.get(o.parent.chat)
	if sb == nil {
		return "", nil
	}
	return o.parent.chat, sb
}

// runOwnBridge is the process holding the run under its OWN `run:<id>` chat id, or
// nil when nothing re-hosted it there. A carrier still loading is nil too: it holds
// nothing yet, and a verb reaching it waits on the run's host lock.
//
// ONE owner for a three-line preference, because the preference is load-bearing and
// every host resolution composes it: a re-hosted run's registry entry lives in THAT
// process, so consulting the chat's bridge first would send the verb to a process
// which has forgotten the run and be refused.
func (rs *Runs) runOwnBridge(workflowID string) *sharedBridge {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil && sb.startedPastSpawn() {
		return sb
	}
	return nil
}

// control issues a verb that is safe on either connection, PREFERRING the process
// that holds the run. Only cancel and delete qualify: both only WRITE state, so a
// text-only session carries them; executing verbs use hostedControl.
//
// The utility session is REFUSED while the owner lives — KAS checks ownership on every
// branch but its own registry hit — so routing is what makes these two land. Resolving
// it needs no re-host (hostBridgeChat), so the fallback costs one `workflow/list` trip
// and a carrier the CALLER resolved (cancelOn) costs none.
func (rs *Runs) control(
	ctx context.Context, workflowID, method, logLabel string, carrier *sharedBridge,
) error {
	params := map[string]any{keyWorkflowID: workflowID}
	if carrier == nil {
		_, carrier = rs.hostBridgeChat(ctx, workflowID)
	}
	if carrier != nil {
		resp, err := carrier.bridge.Call(ctx, method, params)
		return runCallErr(resp, err)
	}
	_, err := rs.utility().session.rawCall(ctx, logLabel, method, callerParams(params))
	return err
}

// dispatch is translateACPEvent's branch for frames arriving on a RUN bridge.
//
// Host requests and the three ASK kinds reuse the chat handlers, keyed by the
// synthetic id. LIFECYCLE frames go out workspace-global with an EMPTY chat id,
// because a parentless run is owned by no chat. session/update is APPENDED to the
// RUN's own log (`runs/<workflowId>/entries.jsonl`) and goes out as the run-scoped
// entry events; it is never buffered into the synthetic chat id, which has no chat
// file to buffer into.
func (rt *Runtime) dispatch(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, msg)
		return
	}
	// BEFORE the prefix test, which this method is not under. It keeps the run
	// bridge's OWN chat id rather than going workspace-global: an ask is
	// answerable, so it must land on a surface, and `run:<id>` is that dock key.
	if msg.Method == methodKiroSessionNotify {
		rt.runs.handleSessionNotify(ctx, chatID, msg)
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/") {
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		if msg.Method == methodWFRunComplete {
			rt.closeStoppedBridge(chatID, msg)
		}
		return
	}
	if msg.Method == marotte.MethodSessionUpdate {
		rt.handleRunStepFrame(ctx, chatID, msg)
		return
	}
	slog.Debug("run bridge: unhandled notification", "method", msg.Method, "chat_id", chatID)
}

// dispatchRequest answers an A→C request on a run bridge, mirroring
// translateACPEvent's request half minus the chat-only concerns. An unmatched
// request is REFUSED rather than dropped: an unanswered one wedges the step's turn.
func (rt *Runtime) dispatchRequest(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	switch {
	case rt.inbound.handleFSRequest(ctx, chatID, msg),
		rt.inbound.handleKiroFSRequest(ctx, chatID, msg),
		rt.inbound.handleKiroClientRequest(ctx, chatID, msg),
		rt.inbound.handleKiroSecretRequest(ctx, chatID, msg):
		return
	case strings.HasPrefix(msg.Method, methodTermPrefix):
		rt.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return
	}
	if fn, ok := rt.chatHandlers[msg.Method]; ok &&
		(msg.Method == marotte.MethodRequestPermission ||
			msg.Method == marotte.MethodElicitationCreate ||
			msg.Method == marotte.MethodKiroUserInput) {
		fn(ctx, chatID, msg)
		return
	}
	slog.Warn("run bridge: refusing unexpected request", "method", msg.Method, "chat_id", chatID)
	_ = rt.BridgeRespond(ctx, chatID, *msg.ID, nil, &marotte.RPCError{
		Code:    marotte.RPCCodeMethodNotFound,
		Message: "unsupported on a run bridge: " + msg.Method,
	})
}

// closeStoppedBridge closes a run bridge once its run STOPPED EXECUTING, terminal or
// paused alike; hostRun re-hosts a parked one on demand. Run bridges only, no
// lease released, an unrecognised status kept.
//
// A goroutine because this runs FROM the forward loop, whose channel CloseBridge →
// Stop closes. It DEFERS until no verb holds the carrier; a verb that entered
// meanwhile hands it to the kept-carrier bound instead (closeStoppedCarrier).
func (rt *Runtime) closeStoppedBridge(chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var p struct {
		Status marotte.RunStatus `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return
	}
	if !p.Status.Terminal() && p.Status != marotte.RunStatusPaused {
		return
	}
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	slog.Info("run stopped executing, closing its bridge", "chat_id", chatID, "status", p.Status)
	workflowID := workflowIDOf(chatID)
	// Only the goroutine takes the host lock: whenIdle can run this closure from
	// carrierUse.leave inside runHost.release, which holds it.
	rt.runs.carriers.whenIdle(sb, func() { go rt.runs.closeStoppedCarrier(chatID, workflowID, sb) })
}

// closeStoppedCarrier ends sb once its run stopped executing, deciding under the run's
// host lock. A verb that entered sb since the frame may be re-driving the run, so sb
// then goes to the kept-carrier bound, which re-reads the run before closing.
func (rs *Runs) closeStoppedCarrier(chatID marotte.ChatID, workflowID string, sb *sharedBridge) {
	unlock, err := rs.hosts.acquire(rs.lifecycle.shutdownCtx, workflowID)
	if err != nil {
		return
	}
	defer unlock()
	if rs.bridges.get(chatID) != sb {
		return
	}
	if rs.carriers.busy(sb) {
		rs.boundKeptCarrier(chatID, workflowID, sb)
		return
	}
	rs.coord.CloseBridge(rs.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
}

// recipeBySource resolves a launch source against the CURRENT recipe list.
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

// recipeIdle enforces the single-run rule against the current run list.
//
// KAS's list is the source of truth, deliberately: it is the only thing that sees
// the runs marotte did not launch. The leases add the ability to EXPLAIN a blocking
// row — ask whether it is an orphan marotte itself left behind before refusing.
func (rs *Runs) recipeIdle(ctx context.Context, name string) error {
	runs, err := rs.list(ctx, nil)
	if err != nil {
		// Launching blind would let a second live run exist, which the
		// Run ⇄ Cancel row cannot represent.
		return fmt.Errorf("run list unavailable: %w", err)
	}
	status := make(map[string]marotte.RunStatus, len(runs))
	for i := range runs {
		status[runs[i].WorkflowID] = runs[i].Status
	}
	rs.reconcileLeasePresence(ctx, status, time.Now(), false)

	for i := range runs {
		// WorkflowName is the RECIPE; Name is the display name and carries a
		// label once anything stamps one, which made this guard fail OPEN —
		// a labelled run stopped blocking its own recipe's next slot.
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

// kasRecipe is one listRecipes entry as KAS reports it; `plan` rides through as
// raw JSON — see marotte.Recipe.
type kasRecipe struct {
	Inputs      map[string]string `json:"inputs"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"`
	Plan        json.RawMessage   `json:"plan"`
	BuiltIn     bool              `json:"builtIn"`
}

// listRecipes fetches the launchable recipe list (bundled + workspace) through
// the utility session — a pure read, safe on the shared connection.
func (rs *Runs) listRecipes(ctx context.Context) ([]marotte.Recipe, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow listRecipes call", methodKiroWorkflowListRecipes,
		callerParams(map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}}))
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
		out = append(out, marotte.Recipe{
			Name:        r.Name,
			Description: r.Description,
			Source:      r.Source,
			Inputs:      r.Inputs,
			Plan:        r.Plan,
			BuiltIn:     r.BuiltIn,
		})
	}
	return out, nil
}

// workflowNew creates the run on the given bridge and returns its id.
//
// parent is the ACP session the run is launched from, required by the engine since
// 0.63.3 (see methodKiroWorkflowNew). The caller passes the RUN BRIDGE'S own
// session, so the run's frames route back to the process executing it.
//
// workspacePaths STAYS: 0.63.3 resolves the roots from the parent session and only
// shape-validates the param, but a pre-0.63.3 engine reads it, and the value it
// yields either way is the same [workDir].
func (rs *Runs) workflowNew(ctx context.Context, bridge acpCaller, source string, inputs map[string]string, parent marotte.SessionID) (string, error) {
	// Always a map, never nil: KAS answers "inputs is not iterable" without it.
	in := map[string]any{}
	for k, v := range inputs {
		in[k] = v
	}
	resp, err := bridge.Call(ctx, methodKiroWorkflowNew, map[string]any{
		"workflowPath":    source,
		keyWorkspacePaths: []string{rs.lifecycle.workDir},
		"inputs":          in,
		"parentSessionId": string(parent),
	})
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

// runCallErr folds a bridge Call's two failure channels into one error, KAS's
// refusal typed by workflow.Classify in either: the real bridge returns the reply's
// RPCError wrapped in its own error as well as on the reply.
func runCallErr(resp *marotte.RPCResponse, err error) error {
	if err == nil && resp != nil && resp.Error != nil {
		err = resp.Error
	}
	return workflow.Classify(err)
}

// --- Restart recovery ---

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON, stamped by its read-path
// reconcile on a run whose owning process died. Matched as a LITERAL: several sites
// set pauseReason and only this one licenses a cancel.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// The pause reasons KAS records when a run stopped for a cause NOBODY CHOSE.
//
// THE RESUME SIDE ONLY: stalePauseReason licenses a CANCEL, this wider set only a
// resume. The network one is matched by PREFIX because KAS interpolates the error
// code into it, and that prefix cannot reach a reason that must be left alone.
const (
	interruptedPauseReason  = "Step interrupted (agent shutdown or connection reset); will resume."
	modelServicePauseReason = "Transient model service error (service 5xx/throttling); will resume."
	networkPausePrefix      = "Transient connection error ("
)

// transientErrorClass is the `pauseDetail.class` KAS stamps for every transient
// fault, the MACHINE-READABLE half of the reasons above. It reaches a pause a REASON
// cannot, because a parallel branch's prose is re-rendered around it.
const transientErrorClass = "transient-error"

// pauseDetail is KAS's machine-readable pause CLASSIFICATION, on both the
// run-level `paused` frame and `inspect`'s state. ONE declaration for the whole
// predicate path, so no second Go shape can disagree with the wire.
//
// `occurredAt` is on the wire and is deliberately NOT declared here: every field
// here is one a predicate decodes, and no predicate reads a timestamp. Its one
// reader is the need-input reconcile, off its own narrow shape (run_ask.go).
type pauseDetail struct {
	Class string `json:"class"`
	Code  string `json:"code"`
}

// resumablePause reports whether a pause means the run stopped for a cause nobody
// chose, and is therefore marotte's to resume unasked.
//
// REASON **OR** DETAIL, and both arms are load-bearing: the literals cover the
// pauses that carry no detail, the detail arm covers the ones whose prose is
// re-rendered. THE RESUME SIDE ONLY — `restartPaused` (run_orphan.go) cancels and
// never reads a detail, an asymmetry preserved by construction rather than comment.
func resumablePause(reason string, detail *pauseDetail) bool {
	if detail != nil && detail.Class == transientErrorClass {
		return true
	}
	switch reason {
	case stalePauseReason, interruptedPauseReason, modelServicePauseReason:
		return true
	}
	return strings.HasPrefix(reason, networkPausePrefix)
}

// resumeInterruptedRuns resumes the runs a chat's rehydrated bridge should pick
// back up: the ones ITS sessions launched that stopped for a cause nobody chose.
//
// Scoped twice: to this chat's session chain (never resumeAll, which would sweep
// runs another chat or the TUI paused on purpose), and to the involuntary reasons.
func (rs *Runs) resumeInterruptedRuns(ctx context.Context, chatID marotte.ChatID) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	chain := make(map[string]bool, len(chat.PriorACPSessionIDs)+1)
	chain[chat.ACPSessionID] = true
	for _, id := range chat.PriorACPSessionIDs {
		chain[id] = true
	}

	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("rehydrate: run list unavailable, skipping resume sweep", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if marotte.RunStatus(r.Status) != marotte.RunStatusPaused || !chain[r.ParentSessionID] {
			continue
		}
		rs.resumeIfInterrupted(ctx, chatID, r.WorkflowID)
	}
}

// maxAutoHeals bounds the automatic resumes one run may spend between two pieces
// of progress. Three, because a fourth attempt against a dead network says nothing.
const maxAutoHeals = 3

// healBaseDelay is the wait before the FIRST automatic resume, doubling per attempt
// (5s, 10s, 20s). Not zero, so a deliberate cancel can land first. A `var` so a test
// can drive the path in milliseconds; never reassigned in production.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS has just parked for a reason nobody chose, off the
// `_kiro/workflow/paused` frame the launching chat's bridge already receives — so
// the timing is exact with no polling and no timer per chat.
//
// Runs AFTER `next`, so the client renders the pause before anything undoes it.
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
			// Without this line the decline is silent and `observePaused` writes
			// nothing either, so the frame's arrival had to be inferred. Debug
			// because a run parked ON PURPOSE takes this branch too.
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
		// NOT tracked, unlike `bounds.timers`: a fired deadline CANCELS a run,
		// while this re-reads the state and does nothing unless still parked.
		time.AfterFunc(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the three fields the heal reads off `_kiro/workflow/paused`. Its
// own minimal decode rather than a share of `lifecycleFrame`, which carries the
// status the BOUNDS read and no pause reason at all. Read the DETAIL, not the
// prose. Pointer first for govet's fieldalignment; the tags carry the wire names.
type pauseFrame struct {
	PauseDetail *pauseDetail `json:"pauseDetail"`
	WorkflowID  string       `json:"workflowId"`
	PauseReason string       `json:"pauseReason"`
}

// pauseClassOf names a pause's class for a log line, or says there was none.
// LOG-ONLY: the predicates compare the field directly, because a sentinel string
// is a value KAS could theoretically send.
func pauseClassOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Class
}

// pauseCodeOf is pauseClassOf's twin, and it is what makes `code` a field this
// path READS rather than one it merely declares — see pauseDetail.
func pauseCodeOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Code
}

// unmarshalKeepingReadable decodes data into dst and reports whether the result is
// usable, KEEPING every field that decoded when one field's wire TYPE has drifted.
//
// `encoding/json` finishes the object on a type mismatch, so a drifted `pauseDetail`
// still leaves `workflowId` and `pauseReason` intact; a bare `err != nil` discarded
// them and blinded the heal silently. A SYNTAX error is NOT tolerated — there the
// bytes are not JSON, so nothing in dst is a partial answer.
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

// healProgress gives a run back both of its per-stall budgets when a node completes,
// and retires the ask that node was holding. Progress is the only honest evidence the
// fault cleared.
//
// The ASK clear is NODE-scoped, not run-scoped: a parallel branch's node can complete
// while a sibling's step still waits, and clearing the run would take that live ask
// with it. It also covers the step answered from the TUI, which no path here claimed.
// Both budgets are keyed on the RUN, so they are refilled per frame.
func (rs *Runs) healProgress(
	next func(context.Context, marotte.ChatID, *marotte.RPCResponse),
) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// The cancel ladder too: a refusal is evidence about a MOMENT, and a
			// run that has since completed a node makes our earlier refusals
			// stale. Without it, three refused Cancel presses leave the run
			// clock's own ladder spent hours later — see maxCancelRetries.
			rs.clearCancelRetries(f.WorkflowID)
			// And the idle window, which is the same evidence spent on the third
			// budget: a completed node is the strongest progress signal there is.
			// BOTH populations reach it — the run bridge reuses chatHandlers for
			// every `_kiro/workflow/*` method — so this is the one progress site a
			// parentless run and a chat-parented one share.
			rs.refillDeadline(ctx, f.WorkflowID)
			// SettledByMoot rather than SettledByUser: the frame says only that the node
			// moved on, and the answer path already settled anything marotte accepted.
			rs.settleAskForNode(ctx, f.WorkflowID, f.NodeID, marotte.SettledByMoot)
		}
		next(ctx, chatID, msg)
	}
}

// nodeFrame is the two fields the node-scoped ask clear reads off
// `_kiro/workflow/node_complete`, its own minimal decode for pauseFrame's reason.
//
// It keeps the STRICT decode its sibling dropped, and the asymmetry is the
// consumer's: `settleAskForNode` needs BOTH ids, so a frame missing either would
// settle the wrong node rather than degrade.
type nodeFrame struct {
	WorkflowID string `json:"workflowId"`
	NodeID     string `json:"nodeId"`
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

// resumeIfInterrupted inspects one paused run and resumes it when the pause was
// involuntary. Resumed on the CHAT's bridge, so that process owns the run again.
func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID marotte.ChatID, workflowID string) {
	release, err := rs.positions.acquire(ctx, workflowID)
	if err != nil {
		return
	}
	defer release()
	// The wider involuntary set, because this RESUMES; the orphan sweep's
	// narrower `restartPaused` cancels.
	if !rs.involuntarilyPaused(ctx, workflowID) {
		return
	}
	sb := rs.bridges.get(chatID)
	if sb == nil {
		return
	}
	if _, cErr := rs.callReclaiming(ctx, sb.bridge, workflowID, methodKiroWorkflowResume,
		map[string]any{keyWorkflowID: workflowID}); cErr != nil {
		if errors.Is(cErr, workflow.ErrJustClaimed) {
			slog.Info("rehydrate: another resume of this run is in flight, so the heal left it to that one",
				"workflow_id", workflowID, "chat_id", chatID)
			return
		}
		slog.Warn("rehydrate: resume failed", "workflow_id", workflowID, "chat_id", chatID, "error", cErr)
		return
	}
	// Resume's own arm, for its reason: each arm bounds EXECUTING time. Needed HERE
	// because this path calls the verb directly, so only the `run_start` frame covers
	// it — and a lost frame leaves the run executing unbounded with nothing noticing.
	rs.armDeadline(ctx, workflowID)
	slog.Info("rehydrate: resumed restart-paused run", "workflow_id", workflowID, "chat_id", chatID)
}

// listRaw lists runs with their raw parent session ids, for callers that scope by
// session chain rather than by resolved chat.
func (rs *Runs) listRaw(ctx context.Context) ([]kasWorkflowRun, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		callerParams(map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list.Runs, nil
}

// CancelForChat cancels every non-terminal run this chat's sessions launched. A run
// is durable state, so killing the chat's process only PAUSES it — each must be told
// to cancel while the owning bridge is alive to say it. Begins with a record read,
// so it no-ops on a deleted chat.
func (rs *Runs) CancelForChat(ctx context.Context, chatID marotte.ChatID) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	rs.CancelForSessions(ctx, chatID, chat.SessionChain())
}

// CancelForSessions cancels every non-terminal run launched by one of these
// sessions. The chain-shaped half of CancelForChat: it reads no chat record, so it
// works from a CAPTURED chain after the record is gone. Best-effort throughout —
// blocking a tab close on an RPC would invert the gesture's meaning.
//
// ONE inventory read, not N+1, and NO chat-record read: the carrier comes off the two
// things this loop already knows. A plain Cancel re-reads the inventory per run for
// the parent session, and the record-matching resolver cannot answer here at all.
func (rs *Runs) CancelForSessions(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
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
		carrier := rs.runOwnBridge(r.WorkflowID)
		if carrier == nil {
			// THIS chat's bridge, by id: the loop filtered on this chat's own
			// chain, so it is the launching chat for every run it reaches, and a
			// deleted record is no obstacle to a map lookup.
			carrier = rs.bridges.get(chatID)
		}
		if cErr := rs.cancelOn(ctx, r.WorkflowID, carrier); cErr != nil {
			slog.Warn("close: run cancel failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", cErr)
			continue
		}
		slog.Info("close: cancelled chat's run", "workflow_id", r.WorkflowID, "chat_id", chatID)
	}
}
