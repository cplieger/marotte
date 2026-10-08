package agent

// The run host: launching a parentless workflow run and the process that holds it. An agent-launched
// run is parented on its chat's session by KAS; a manual or scheduled one gets one bridge under the
// synthetic chat id `run:<workflowId>`, which has no chat file.

import (
	"cmp"
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

// runChatPrefix namespaces run bridges' synthetic chat ids; real ids are `c-*` uuids.
const runChatPrefix = "run:"

// runChatID is the bridge-manager key for a run's bridge.
func runChatID(workflowID string) marotte.ChatID {
	return marotte.ChatID(runChatPrefix + workflowID)
}

// isRunChat reports whether a chat id names a run bridge.
func isRunChat(chatID marotte.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

// workflowIDOf recovers a run bridge's workflow id from its chat id, or "".
func workflowIDOf(chatID marotte.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch handshake; a first launch may unpack a ~240 MB KAS runtime.
const launchTimeout = 120 * time.Second

// errRecipeBusy is the single-run rule: one live run per recipe, globally.
var errRecipeBusy = errors.New("this recipe already has a live run")

// Launch starts one parentless attended run of the recipe and returns its workflow id and name.
func (rs *Runs) Launch(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return rs.launch(ctx, source, inputs, manualLaunch())
}

// LaunchScheduled launches an unattended run for a schedule. slotAt is its next slot; zero leaves the idle window and backstop.
func (rs *Runs) LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error) {
	return rs.launch(ctx, source, nil, scheduledLaunch(scheduleID, slotAt))
}

// launch is the shared body of both public launch verbs.
func (rs *Runs) launch(ctx context.Context, source string, inputs map[string]string, o launchOrigin) (id, name string, err error) {
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

	// Started outside the manager: the map key is the workflow id `new` returns. Replies ride readLoop, so Forward can attach after.
	bridge := rs.bridges.factory()
	if sErr := bridge.Start(cctx, &marotte.StartOpts{
		Lifetime: rs.lifecycle.shutdownCtx,
		// Named explicitly, so a buildACPArgs default change cannot start a legacy-engine run bridge.
		AgentEngine:      resolveAgentEngine(),
		EnableHooks:      true,
		Presets:          securityPresets(cctx, rs.lifecycle.configDir),
		IgnoreFiles:      func(c context.Context) []string { return spawnIgnoreFiles(c, rs.lifecycle.configDir) },
		TerminalTimeout:  func(c context.Context) int { return terminalCommandTimeoutMs(c, rs.lifecycle.configDir) },
		ToolSearch:       toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:        knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:           memoryPreference(cctx, rs.lifecycle.configDir),
		Features:         agentFeatures(cctx, rs.lifecycle.configDir, currentLocks(rs.locks)),
		DisableTelemetry: rs.lifecycle.telemetryDisabled(rs.locks),
		// A run's step sessions share the process, so they inherit it.
		ContentCollection: contentCollectionResolver(rs.lifecycle.configDir, rs.locks),
		// Nothing renders a step session's title.
		DisableSessionTitles: true,
	}); sErr != nil {
		return "", "", fmt.Errorf("run bridge start: %w", sErr)
	}

	// Refuse before the RPC: a started bridge with no session is a broken handshake.
	parent := bridge.SessionID()
	if parent == "" {
		bridge.Stop()
		return "", "", errors.New("run bridge started with no ACP session; cannot launch a workflow")
	}

	wfID, err := rs.workflowNew(cctx, bridge, recipe.Source, inputs, parent, o.runLabel(recipe.Name))
	if err != nil {
		bridge.Stop()
		return "", "", err
	}

	// Register before invoke: the first lifecycle frame follows immediately.
	launched := &sharedBridge{bridge: bridge, state: bridgeIdle, live: startedLive(bridge)}
	if !rs.bridges.insert(runChatID(wfID), launched) {
		bridge.Stop()
		return "", "", fmt.Errorf("workflow %s already has a run bridge", wfID)
	}
	// A settings write between Start and the insert found no bridge to push to.
	launched.syncLive(cctx, runChatID(wfID), rs.readLiveSettings)
	// The run's envelope, before anything can execute (runlease.Lease).
	rs.grantLease(cctx, wfID, recipe.Name, o)
	rs.coord.goForward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// Created but never started.
		rs.releaseLease(cctx, wfID)
		rs.coord.CloseBridge(cctx, runChatID(wfID), marotte.TurnOutcomeInterrupted)
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

// userStop attributes a stop to the user; why is set only when the cause is not the stop button.
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

// Cancel stops a run on the user's behalf. A lost termination claim returns nil; a refused RPC is
// returned, and retryTermination retries it.
func (rs *Runs) Cancel(ctx context.Context, workflowID string) error {
	return rs.cancelOn(ctx, workflowID, userStop(""), nil)
}

// cancelOn is Cancel with the carrier already resolved, so stopping N runs costs one inventory read; nil resolves it.
func (rs *Runs) cancelOn(ctx context.Context, workflowID string, stop runStop, carrier *sharedBridge) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	return rs.finishTermination(ctx, workflowID, "", stop, carrier)
}

// cancelRPC issues the cancel verb only. The bridge stays open: the owner must live to the node
// boundary to certify the cancel, and `run_complete` closes it.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string, stop runStop, carrier *sharedBridge) error {
	return rs.control(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call", stop, carrier)
}

// Delete removes a run and everything kept about it, on the user's behalf; unrecoverable, and a failure
// leaves everything. No termination claim (KAS's delete cancels a live run itself), and the bridge is closed.
func (rs *Runs) Delete(ctx context.Context, workflowID string) error {
	return rs.deleteOn(ctx, workflowID, userStop(""), nil)
}

// deleteOn is Delete on a carrier the caller already resolved; nil resolves it.
func (rs *Runs) deleteOn(ctx context.Context, workflowID string, stop runStop, carrier *sharedBridge) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if err := rs.control(ctx, workflowID, methodKiroWorkflowDelete, "workflow delete call", stop, carrier); err != nil {
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

// Pause asks a run to stop at its next node boundary; the reply confirms the ask. A re-hosted pause is
// refused: KAS throws for a run its process forgot.
func (rs *Runs) Pause(ctx context.Context, workflowID string) error {
	return rs.hostedControl(ctx, workflowID, methodKiroWorkflowPause, userStop(""))
}

// Resume re-drives a paused run with a fresh executing budget.
func (rs *Runs) Resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume, runStop{})
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// retryTimeout bounds the whole retry handshake. Below clientRequestBudget, from which it is derived:
// a browser abort first would tear down a fresh bridge and release the lease. A var for tests.
var retryTimeout = clientRequestBudget - 5*time.Second

// clientRequestBudget is the timeout @cplieger/fetch hard-wires into every apiAction.
const clientRequestBudget = 30 * time.Second

// errRetryEngineSlow reports a retry not handed off within retryTimeout; the REST layer answers 503.
var errRetryEngineSlow = errors.New(
	"the run's engine did not start in time, so nothing was retried. Try again",
)

// errRetryOutcomeUnreadable reports a retry KAS accepted whose report was unreadable. The run may be
// executing, so the remedy is refreshing, not retrying or killing the bridge.
var errRetryOutcomeUnreadable = errors.New(
	"the retry was accepted but its report could not be read, so which steps it reset " +
		"is unknown. Refresh the run to see where it is",
)

// kasRetryOutcome decodes `_kiro/workflow/retry`'s reply. RetriedNodeIDs tells a five-node retry from one that reset nothing.
type kasRetryOutcome struct {
	WorkflowID     string   `json:"workflowId"`
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retriedNodeIds"`
}

// Retry resets a failed or aborted run's failed work and reports what it reset, hosted from the gate's own read of the run's origin.
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
		// KAS took the retry, so the run may be re-driving in this carrier.
		if errors.Is(err, errRetryOutcomeUnreadable) {
			host.release(nil)
			return
		}
		host.release(err)
	}()
	// Threaded from the gate: nothing here can learn it once the run re-drives.
	recipe := aff.origin.recipe
	// The lease before the verb: retry's `run_start` can beat the reply.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(cctx, workflowID, recipe, manualLaunch())
		minted = true
	}
	out, err = rs.retryCall(cctx, host.sb.bridge, workflowID)
	if errors.Is(err, errRetryOutcomeUnreadable) {
		// Only the report is lost; tearing down would kill the work mid-node.
		rs.rearmRetried(cctx, workflowID, recipe)
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
	rs.rearmRetried(cctx, workflowID, recipe)
	slog.Info("workflow run retried",
		"workflow_id", workflowID, "recipe", recipe,
		"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
	return out, nil
}

// retryCall issues the verb and decodes its report, through runCallErr so an `error` member is not read as success.
func (rs *Runs) retryCall(
	ctx context.Context, bridge acpCaller, workflowID string,
) (marotte.RunRetriedResponse, error) {
	none := marotte.RunRetriedResponse{}
	resp, err := bridge.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
	if cErr := runCallErr(resp, err); cErr != nil {
		return none, fmt.Errorf("workflow retry: %w", rs.retryDeadlineErr(ctx, cErr))
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

// retryStartErr labels a failed bridge start: this call's deadline, or the start
// failure itself.
func (rs *Runs) retryStartErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return fmt.Errorf("retry bridge start: %w", err)
}

// retryDeadlineErr maps a failure on this call's budget to errRetryEngineSlow.
func (rs *Runs) retryDeadlineErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return err
}

// errStepStatusRefused means KAS declined the update (terminal run, or no running or paused step); the REST layer answers 409.
var errStepStatusRefused = errors.New("this run's step status was not changed")

// updateStatusAction is `_kiro/workflow/update`'s step-status action, sent explicitly because the tool schema requires it.
const updateStatusAction = "update_status"

// errStepStatusMistargeted means the named node is not the one KAS would mark, so the write was withheld. Wraps errStepStatusRefused.
var errStepStatusMistargeted = fmt.Errorf(
	"%w: this run is not waiting at the step you asked to mark", errStepStatusRefused,
)

// errStepStatusUnreadable means the pre-send read failed, so the write was withheld: a `completed` on the
// wrong node publishes its capture irreversibly.
var errStepStatusUnreadable = fmt.Errorf(
	"%w: this run's state could not be read just now, so try again in a moment",
	errStepStatusRefused,
)

// SetStepStatus marks a step completed, failed or running so a wedged run advances. KAS targets
// positionally, so the tree is read first and the write withheld unless the targets agree.
func (rs *Runs) SetStepStatus(ctx context.Context, workflowID, nodeID, status string) (err error) {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if !slices.Contains(runStepStatuses, status) {
		return fmt.Errorf("step status must be one of %v", runStepStatuses)
	}
	// Before hostRun: `inspect` runs on the utility session. The position lock spans read and write, since a chat open can heal this run.
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
	// A decline releases with a cause too: no run_complete follows.
	defer func() { host.release(err) }()
	resp, callErr := rs.callReclaiming(ctx, host.sb.bridge, workflowID, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"action":      updateStatusAction,
		"status":      status,
	})
	if callErr != nil {
		return callErr
	}
	// Both declines answer {updated:false} with 200, so the reply is read.
	if refusal := stepStatusRefusal(resp); refusal != "" {
		return fmt.Errorf("%w: %s", errStepStatusRefused, refusal)
	}
	if status == runStepRunning {
		// Re-driven without the user's words; SettledByUser because this is the reader's decision.
		rs.settleAskForNode(ctx, workflowID, nodeID, marotte.SettledByUser)
	}
	return nil
}

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

// stepStatusAddress reports whether the named node is the one KAS would mark, refusing when unsure: a wrong guess writes irreversibly.
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

// stepNodeType is the state tree's `type` for a step, the only kind KAS's resolver considers.
const stepNodeType = "step"

// statusUpdateTarget mirrors KAS's resolver: the first running step node in pre-order, else the first paused one.
func statusUpdateTarget(root *askNode) *askNode {
	running, paused := stepTargets(root)
	if running != nil {
		return running
	}
	return paused
}

// stepTargets is statusUpdateTarget's one pre-order pass, judging each node before its children, returning at the first running step.
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

// The step statuses a human may set. `running` is continue-without-answering, which plain Resume cannot substitute.
const (
	runStepCompleted = "completed"
	runStepFailed    = "failed"
	runStepRunning   = "running"
)

// runStepStatuses is the allowlist, in the order the refusal names them.
var runStepStatuses = []string{runStepCompleted, runStepFailed, runStepRunning}

// errAskAlreadySettled means the named ask is no longer open; the REST layer answers 409.
var errAskAlreadySettled = errors.New(
	"that question has already been answered, or the step it belonged to has moved on",
)

// AnswerInput answers one parked step with the user's words, as a `session/prompt` to the paused step's
// own session. Order is the contract: carrier, claim, address, send; each guards a window the next
// would open. A failed send restores the claim.
func (rs *Runs) AnswerInput(ctx context.Context, workflowID, askID, text string) (err error) {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("an answer cannot be empty")
	}
	// Before the claim, so the registry is never empty with no answer in flight.
	rs.asks.beginAnswer(workflowID)
	defer rs.asks.endAnswer(workflowID)
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	// Held from here, not from the Call: the address read is a round trip, and a bound coming due inside
	// it would close this carrier.
	defer func() { host.release(err) }()
	a, ok := rs.asks.TakeIfPresent(workflowID, askID)
	if !ok {
		return errAskAlreadySettled
	}
	session, verdict := rs.answerAddress(ctx, workflowID, a)
	if verdict == answerMoot {
		// Moot rather than restored: KAS has stopped waiting on this question.
		rs.announceSettled(ctx, a, marotte.SettledByMoot)
		return errAskAlreadySettled
	}
	if verdict == answerBusy {
		// Early, not stale: the run is between steps, so the card goes back and the reader retries.
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
	// A fresh budget, as Resume gives.
	rs.armDeadline(ctx, workflowID)
	rs.announceSettled(ctx, a, marotte.SettledByUser)
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_id", a.payload.NodeID, "ask_id", askID)
	return nil
}

// errRunNotParked means the run is executing, with no parked step to reroute into yet. Retryable; the card survives.
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

// answerAddress resolves where an ask's answer goes and grades the run's state. The fresh read leads,
// since a prompt KAS does not reroute runs as an ordinary turn; an unreadable run falls back to the
// ask's own address rather than refusing.
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

// askedStep finds the parked step an ask belongs to by its node id, or nil, so a parallel run's second
// parked branch is answerable. An empty id (older asks) matches any parked step; repeat iterations share an id.
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

// hostedControl issues a verb on a process holding the run, never the utility bridge, which denies
// permissions and fs/terminal calls.
func (rs *Runs) hostedControl(ctx context.Context, workflowID, method string, stop runStop) error {
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
	_, callErr := rs.callReclaiming(ctx, host.sb.bridge, workflowID, method, params)
	host.release(callErr)
	return callErr
}

// callReclaiming sends a verb that loads the run into b's process. After a crash the utility session's
// KAS stamps every run it reconciles as its own (acp-server.js sweepStaleRuns), refusing other
// processes for ~135 s; on that refusal the utility is stopped, voiding the stamp, and the verb resent.
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

// reclaimGrace bounds resendWhileOwned: KAS probes liveness with `process.kill(pid, 0)`, and the stopped
// process exits ~25 ms after Stop (kiro-cli 2.27.0).
var reclaimGrace = 2 * time.Second

const reclaimPoll = 50 * time.Millisecond

// resendWhileOwned resends until KAS stops refusing for another owner, the grace or ctx ends, and answers the last reply.
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

// errRunHostStart marks a failure to start a carrier: a server fault, answered 500 without echoing an internal path.
var errRunHostStart = errors.New("a process for this run could not be started")

// errLaunchSessionUnavailable means no carrier can hold the run's launch session; driving it elsewhere
// would drop the profile's presets from its steps. Answered 409.
var errLaunchSessionUnavailable = errors.New("the run's launching session cannot be opened here")

// launchSessionUnavailableText is errLaunchSessionUnavailable as the reader sees it.
const launchSessionUnavailableText = "This run's launching session cannot be opened on this server. " +
	"Cancel and delete still work."

// errRunNotListed means KAS's inventory does not list the run. Answered 404.
var errRunNotListed = errors.New("run not found")

// runParent is a run's launching session and the chat owning it; "" for parentless.
type runParent struct {
	chat    marotte.ChatID
	session string
}

// runOrigin is a run's launch facts from one inventory read and the chat store; err says why parent cannot route a verb.
type runOrigin struct {
	err      error
	parent   runParent
	chatName string
	recipe   string
	// pauseKind and pauseNodeID are kasWorkflowRun's, off the same read.
	pauseKind    string
	pauseNodeID  string
	pausePending bool
}

// originOf reads where a run was launched from. An ownerless parent is trusted only after a complete
// chat scan: loading a chat's session onto a run bridge replays it into a carrier.
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
	o.parent.chat, o.chatName, complete = rs.chatForSession(ctx, o.parent.session)
	if o.parent.chat == "" && !complete {
		o.err = fmt.Errorf("%w: a chat file could not be read, so which chat launched the run is unknown",
			errRunHostStart)
	}
	return o
}

// runHost is one verb's hold on a run's carrier, counted in carrierUse before the host lock is
// released so no verb can end the carrier uncounted. Released exactly once.
type runHost struct {
	rs         *Runs
	sb         *sharedBridge
	chatID     marotte.ChatID
	workflowID string
	// started is whether this verb started the carrier, so release alone decides its end.
	started bool
}

// hostRun is acquireHost for a verb with no inventory read of its own.
func (rs *Runs) hostRun(ctx context.Context, workflowID string) (*runHost, error) {
	return rs.acquireHost(ctx, workflowID, rs.originOf)
}

// acquireHost resolves one verb's carrier under the run's host lock (a waiter gives up with its ctx): the
// run's own bridge, else its launching session made live with the profile's presets, never another
// session. origin is read only when the run's own bridge is not live.
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

// errVerbNeverSent is the release cause for a host acquired for a verb that had already given up.
var errVerbNeverSent = errors.New("the verb gave up before anything was sent")

// acquireDurably runs acquire on a durable context in a tracked goroutine that owns unlock: a cancelled
// spawn would detach a chat from its session. The lock never spans the verb's RPC; a host for a
// verb that gave up is released under the lock.
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
			// The host is the verb's now: it fails on its own ctx and releases it.
			mu.Unlock()
			r := <-done
			return r.h, r.err
		}
		abandoned = true
		mu.Unlock()
		return nil, ctx.Err()
	}
}

// release ends this verb's hold; cause is nil when it landed or KAS took it. A carrier this verb started
// and failed is ended here only, under the host lock. On a context error KAS may have taken the verb,
// so the carrier is kept under the kept-carrier bound.
func (h *runHost) release(cause error) {
	rs := h.rs
	if !h.started || cause == nil {
		rs.carriers.leave(h.sb)
		return
	}
	unlock, err := rs.hosts.acquire(rs.lifecycle.shutdownCtx, h.workflowID)
	if err != nil {
		// Shutting down stops every carrier.
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

// rehostOnChat reaches a chat-parented run through the chat's own bridge, never a second resident on
// its session. That bridge must hold the run's launching session: OpenBridge can fall back to a new one.
func (rs *Runs) rehostOnChat(
	ctx context.Context, workflowID string, parent runParent,
) (*sharedBridge, error) {
	held, err := rs.coord.OpenBridge(ctx, parent.chat, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunHostStart, err)
	}
	// OpenBridge can return a bridge another caller is still starting.
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

// loadRunCarrier makes a parentless run's launching session live on a run bridge under its synthetic id,
// owning unlock. Registered and forwarded before the load: session/load sends host requests.
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

// startRunCarrier loads session on sb's bridge; on failure sb leaves the map and its bridge stops.
func (rs *Runs) startRunCarrier(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, session string,
) error {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	rs.coord.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(cctx, &marotte.StartOpts{
		Lifetime:         rs.lifecycle.shutdownCtx,
		SessionID:        session,
		AgentEngine:      resolveAgentEngine(),
		EnableHooks:      true,
		Presets:          securityPresets(cctx, rs.lifecycle.configDir),
		IgnoreFiles:      func(c context.Context) []string { return spawnIgnoreFiles(c, rs.lifecycle.configDir) },
		TerminalTimeout:  func(c context.Context) int { return terminalCommandTimeoutMs(c, rs.lifecycle.configDir) },
		ToolSearch:       toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:        knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:           memoryPreference(cctx, rs.lifecycle.configDir),
		Features:         agentFeatures(cctx, rs.lifecycle.configDir, currentLocks(rs.locks)),
		DisableTelemetry: rs.lifecycle.telemetryDisabled(rs.locks),
		// A run's step sessions share the process, so they inherit it.
		ContentCollection: contentCollectionResolver(rs.lifecycle.configDir, rs.locks),
		// Nothing renders a step session's title.
		DisableSessionTitles: true,
	}); err != nil {
		rs.bridges.removeIfSame(chatID, sb)
		sb.bridge.Stop()
		return err
	}
	sb.adoptSpawn()
	sb.setIdle()
	// A settings write during the spawn skipped this bridge (syncLive).
	sb.syncLive(cctx, chatID, rs.readLiveSettings)
	return nil
}

func (rs *Runs) readLiveSettings(ctx context.Context) liveSettings {
	return readLiveSettings(ctx, rs.lifecycle.configDir, readLiveFields)
}

// carrierUse counts the run verbs holding each carrier, keyed by carrier since one holds several verbs in turn.
type carrierUse struct {
	held map[*sharedBridge]int
	// onIdle is a close a lifecycle frame deferred while a verb held the carrier (whenIdle).
	onIdle map[*sharedBridge]func()
	mu     sync.Mutex
}

// enter records a verb using sb, paired with leave over the whole span from resolve to return:
// AnswerInput's span includes an `inspect` round trip.
func (c *carrierUse) enter(sb *sharedBridge) {
	sb.runVerbs.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil {
		c.held = make(map[*sharedBridge]int)
	}
	c.held[sb]++
}

// leave records a verb done with sb and runs any deferred close outside the lock. The entry is deleted
// at zero, since its key pins the bridge.
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

// whenIdle runs closeFn once no verb holds sb, at once if none does. It ends on the caller's context or
// KAS answering. A repeat frame replaces the pending closer: one carrier, one close.
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

// keptCarrierGrace is how long a carrier kept on an unknown outcome waits before its run is re-read;
// longer than launchTimeout. carrierUse is the safety argument. A var for tests.
var keptCarrierGrace = 10 * time.Minute

// carrierVerdict is what one firing of the kept-carrier bound decided.
type carrierVerdict int

const (
	// carrierClosed: the carrier was stopped.
	carrierClosed carrierVerdict = iota
	// carrierSpared pins that not this bound's to close, or its run still executes; the bound ends.
	carrierSpared
	// carrierBusy pins that a verb still holds it; ask again.
	carrierBusy
)

// boundKeptCarrier gives a carrier kept on a context error an end, since nothing else would close it.
// Untracked, like healPaused's AfterFunc: the guards make a late firing one inspect. A busy verdict
// re-arms until the holding verb ends or shutdown.
func (rs *Runs) boundKeptCarrier(chatID marotte.ChatID, workflowID string, kept *sharedBridge) {
	time.AfterFunc(keptCarrierGrace, func() {
		if rs.closeKeptCarrier(chatID, workflowID, kept) == carrierBusy {
			rs.boundKeptCarrier(chatID, workflowID, kept)
		}
	})
}

// closeKeptCarrier is the bound's decision under the run's host lock. Identity leads, so a stale bound
// neither closes a later carrier nor loops; use comes next, a local read.
func (rs *Runs) closeKeptCarrier(
	chatID marotte.ChatID, workflowID string, kept *sharedBridge,
) carrierVerdict {
	unlock, err := rs.hosts.acquire(rs.lifecycle.shutdownCtx, workflowID)
	if err != nil {
		// Shutting down stops every carrier.
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

// isCtxErr reports a cancellation or deadline at any depth, the one condition under which a failed verb keeps its carrier.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// hostBridgeChat resolves the bridge holding the run's registry entry and its chat id, or "": the run's
// own `run:<id>` bridge, or the launching chat's. The id keys a synthesised ask (askChatID). One
// `workflow/list` on the second path only.
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

// runOwnBridge is the process holding the run under its own `run:<id>`, or nil (also while loading).
// The one owner of the preference: a re-hosted run is registered only in that process.
func (rs *Runs) runOwnBridge(workflowID string) *sharedBridge {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil && sb.startedPastSpawn() {
		return sb
	}
	return nil
}

// control issues a verb safe on either connection, preferring the process holding the run: only cancel
// and delete, which only write state. KAS refuses them on the utility session while the owner lives.
func (rs *Runs) control(
	ctx context.Context, workflowID, method, logLabel string, stop runStop, carrier *sharedBridge,
) error {
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
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

// dispatch is translateACPEvent's branch for run-bridge frames. Host requests and the three ask kinds
// reuse the chat handlers; lifecycle frames go workspace-global with an empty chat id; session/update
// goes to the run's own log (`runs/<workflowId>/entries.jsonl`) as run-scoped entry events.
func (rt *Runtime) dispatch(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, msg)
		return
	}
	// Before the prefix test. It keeps the run bridge's own chat id: `run:<id>` is the ask's dock key.
	if msg.Method == methodKiroSessionNotify {
		rt.runs.handleSessionNotify(ctx, chatID, msg)
		return
	}
	// Keeps the run's chat id, so the toast names the run.
	if msg.Method == methodV3SystemNotify {
		rt.translator.HandleSystemNotify(ctx, chatID, msg)
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

// dispatchRequest answers an A→C request on a run bridge; an unmatched one is refused, since silence wedges the step.
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

// closeStoppedBridge closes a run bridge once its run stopped executing (terminal or paused); hostRun
// re-hosts on demand. Run bridges only; no lease released; an unrecognised status kept. A goroutine,
// since this runs on the forward loop Stop closes; it defers until the carrier is idle.
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
	// Only the goroutine takes the host lock: whenIdle can run this from release, which holds it.
	rt.runs.carriers.whenIdle(sb, func() { go rt.runs.closeStoppedCarrier(chatID, workflowID, sb) })
}

// closeStoppedCarrier ends sb under the host lock; a verb that entered since may be re-driving, so sb
// goes to the kept-carrier bound instead.
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

// kasRecipe is one listRecipes entry; `plan` rides through as raw JSON (marotte.Recipe).
type kasRecipe struct {
	Inputs      map[string]string `json:"inputs"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"`
	Plan        json.RawMessage   `json:"plan"`
	BuiltIn     bool              `json:"builtIn"`
}

// listRecipes fetches the bundled and workspace recipes through the utility session.
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

// workflowNew creates the run on bridge and returns its id. parent is the run bridge's own session, so
// frames route back to the executing process. workspacePaths stays for pre-0.63.3 engines.
func (rs *Runs) workflowNew(
	ctx context.Context, bridge acpCaller, source string, inputs map[string]string, parent marotte.SessionID, label string,
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

// runCallErr folds a Call's two failure channels into one error classified by workflow.Classify.
func runCallErr(resp *marotte.RPCResponse, err error) error {
	if err == nil && resp != nil && resp.Error != nil {
		err = resp.Error
	}
	return workflow.Classify(err)
}

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON for a run whose owner died. A literal: only this reason licenses a cancel.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// Pause reasons for a stop nobody chose, licensing a resume only. The network one is matched by prefix
// since KAS interpolates the error code.
const (
	interruptedPauseReason  = "Step interrupted (agent shutdown or connection reset); will resume."
	modelServicePauseReason = "Transient model service error (service 5xx/throttling); will resume."
	networkPausePrefix      = "Transient connection error ("
)

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
	switch reason {
	case stalePauseReason, interruptedPauseReason, modelServicePauseReason:
		return true
	}
	return strings.HasPrefix(reason, networkPausePrefix)
}

// resumeInterruptedRuns resumes the runs this chat's sessions launched that stopped for a cause nobody
// chose; never resumeAll, which would undo deliberate pauses.
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
		// Untracked: it re-reads and does nothing unless still parked.
		time.AfterFunc(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the three fields the heal reads off `_kiro/workflow/paused`, decoded separately from
// lifecycleFrame. Pointer first for govet's fieldalignment.
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

// pauseCodeOf is pauseClassOf's twin, making `code` a field this path reads.
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

// healProgress refills a run's per-stall budgets when a node completes and retires that node's ask
// (node-scoped: a sibling branch may still wait), including one answered from the TUI.
func (rs *Runs) healProgress(
	next func(context.Context, marotte.ChatID, *marotte.RPCResponse),
) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// The cancel ladder too: a refusal speaks only for its moment (maxCancelRetries).
			rs.clearCancelRetries(f.WorkflowID)
			// And the idle window. Both run populations reach this: the run bridge reuses chatHandlers for every `_kiro/workflow/*` method.
			rs.refillDeadline(ctx, f.WorkflowID)
			// Moot: the frame says only the node moved on.
			rs.settleAskForNode(ctx, f.WorkflowID, f.NodeID, marotte.SettledByMoot)
		}
		next(ctx, chatID, msg)
	}
}

// nodeFrame is the two fields the node-scoped ask clear reads off `_kiro/workflow/node_complete`. Strict:
// settleAskForNode needs both ids, so a partial frame would settle the wrong node.
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

// resumeIfInterrupted resumes one paused run whose pause was involuntary, on the chat's bridge, which then owns it.
func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID marotte.ChatID, workflowID string) {
	release, err := rs.positions.acquire(ctx, workflowID)
	if err != nil {
		return
	}
	defer release()
	// The wider involuntary set, because this resumes; the orphan sweep's narrower `restartPaused` cancels.
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
	// Arm here: this path calls the verb directly, so only the `run_start` frame would otherwise cover it.
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

// CancelForChat cancels every live run this chat's sessions launched: killing the chat's process only
// pauses them. Starts with a record read, so a deleted chat no-ops.
func (rs *Runs) CancelForChat(ctx context.Context, chatID marotte.ChatID, stop runStop) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	rs.CancelForSessions(ctx, chatID, chat.SessionChain(), stop)
}

// CancelForSessions cancels every live run these sessions launched, from a captured chain with no record
// read, best-effort so a tab close never waits. One inventory read; the carrier comes from what the loop knows.
func (rs *Runs) CancelForSessions(ctx context.Context, chatID marotte.ChatID, sessionChain []string, stop runStop) {
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
			// This chat's bridge by id: the loop filtered on its own chain.
			carrier = rs.bridges.get(chatID)
		}
		if cErr := rs.cancelOn(ctx, r.WorkflowID, stop, carrier); cErr != nil {
			slog.Warn("close: run cancel failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", cErr)
			continue
		}
		slog.Info("close: cancelled chat's run", "workflow_id", r.WorkflowID, "chat_id", chatID)
	}
}

// DeleteForSessions deletes every run these sessions launched, whatever its status: KAS refuses a session
// delete while the session owns a live run. Routing as CancelForSessions.
func (rs *Runs) DeleteForSessions(ctx context.Context, chatID marotte.ChatID, sessionChain []string, stop runStop) {
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
		carrier := rs.runOwnBridge(r.WorkflowID)
		if carrier == nil {
			carrier = rs.bridges.get(chatID)
		}
		if dErr := rs.deleteOn(ctx, r.WorkflowID, stop, carrier); dErr != nil {
			slog.Warn("delete: run delete failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", dErr)
		}
	}
}
