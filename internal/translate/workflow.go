package translate

// v3 workflow-run lifecycle handlers: KAS's nine notifications become three SSE events
// (seven invalidations share one handler). They arrive on the LAUNCHING session's lane.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runOriginAgent labels a run KAS parented on a chat session, the population with no
// supervisor in this tier — see logAgentRun.
const runOriginAgent = "agent"

// `nodeTree` and `inputs` are not decoded: the client refetches `inspect`, whose tree is fresher.
// `parentSessionId` is not decoded either (see logAgentRun). Initiator and InitiatorReason are set
// on the run_start a resume emits.
type kasRunStart struct {
	WorkflowID      string `json:"workflowId"`
	WorkflowName    string `json:"workflowName"`
	Initiator       string `json:"initiator"`
	InitiatorReason string `json:"initiatorReason"`
}

// Every field is optional across the set: `paused` carries only the workflow id, and
// `loop_iteration` names its node in `loopId` rather than `nodeId`.
type kasRunNode struct {
	// Resolution settles a `steps_queued` revision; PendingSteps queues one.
	Resolution *struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	} `json:"resolution"`
	WorkflowID string `json:"workflowId"`
	NodeID     string `json:"nodeId"`
	LoopID     string `json:"loopId"`
	SessionID  string `json:"sessionId"`
	Status     string `json:"status"`
	// Reason is node_paused's text; node_complete names its own FailureReason.
	Reason        string `json:"reason"`
	FailureReason string `json:"failureReason"`
	// Kind is set on `node_paused` only, and only to retryWaitKind.
	Kind string `json:"kind"`
	// Initiator and InitiatorReason attribute a run-level `paused`.
	Initiator       string            `json:"initiator"`
	InitiatorReason string            `json:"initiatorReason"`
	PendingSteps    []json.RawMessage `json:"pendingSteps"`
	NodePath        []string          `json:"nodePath"`
	// Credits stays raw so a malformed member costs only itself, never the node_complete frame.
	Credits json.RawMessage `json:"credits"`
}

// retryWaitKind marks a `node_paused` KAS sends while a step waits out a transient failure: the
// step keeps running and KAS re-sends node_start after the wait, so it is no pause.
const retryWaitKind = "retry-wait"

func (p *kasRunNode) retryWait() bool { return p.Kind == retryWaitKind }

// `finalState` is read for the run's name, the one lifecycle frame with no top-level name, and for
// what a wake turn it starts says (autoWakeParentOnComplete reads the same fields); the client
// refetches the rest. `credits` is not read: the client sums the refetched tree's step credits.
type kasRunComplete struct {
	WorkflowID string `json:"workflowId"`
	Status     string `json:"status"`
	FinalState struct {
		WorkflowName  string `json:"workflowName"`
		RunLabel      string `json:"runLabel"`
		StopInitiator string `json:"stopInitiator"`
		StopReason    string `json:"stopReason"`
		Pause         struct {
			Kind          string `json:"kind"`
			NodeID        string `json:"nodeId"`
			MaxIterations int    `json:"maxIterations"`
		} `json:"pause"`
	} `json:"finalState"`
}

// logAgentRun writes the one durable trace an agent-launched run gets: two lines correlated
// by workflow_id, since such a run has no record or supervisor. Silent for a run marotte
// launched. The discriminator is the DELIVERY ADDRESS, not `parentSessionId` (which
// _kiro/workflow/new requires): (*Runtime).dispatch hands a run host session's frame an EMPTY chat
// id. Not the lease: observeComplete releases it BEFORE HandleRunComplete.
func logAgentRun(msg string, chatID marotte.ChatID, workflowID, recipe string, extra ...any) {
	if chatID == "" {
		return
	}
	slog.Info(msg,
		append([]any{
			"workflow_id", workflowID,
			"origin", runOriginAgent,
			"recipe", recipe,
		}, extra...)...)
}

// HandleRunStart translates _kiro/workflow/run_start into the run_started SSE. It fires
// again on every resume, so the client treats it as "exists and changed", not a create.
func (t *Translator) HandleRunStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[kasRunStart](msg, "workflow/run_start")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run started", chatID, p.WorkflowID, p.WorkflowName)
	t.askRunName(p.WorkflowID)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunStarted, chatID, marotte.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name: cmp.Or(t.RunName(p.WorkflowID), t.runOrigin.RunLabel(p.WorkflowID),
			marotte.RunDisplayName("", p.WorkflowName)),
		Initiator:       p.Initiator,
		InitiatorReason: p.InitiatorReason,
		// Keyed on the workflow id: this frame's chat id is empty for exactly these runs.
		Unattended: t.runOrigin.UnattendedOrigin(p.WorkflowID),
	}))
}

// HandleRunComplete translates _kiro/workflow/run_complete into the run_finished SSE.
// Terminal covers cancel, failure and a pause policy stop, so the status travels. It does
// NOT forget step sessions (`paused` arrives on a live run); agent.observeComplete does.
func (t *Translator) HandleRunComplete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[kasRunComplete](msg, "workflow/run_complete")
	if !ok || p.WorkflowID == "" {
		return
	}
	logAgentRun("agent-launched workflow run finished", chatID, p.WorkflowID,
		p.FinalState.WorkflowName, "status", p.Status)
	t.observeRunEnd(chatID, &p)
	name := marotte.RunDisplayName(p.FinalState.RunLabel, p.FinalState.WorkflowName)
	t.learnRunName(ctx, p.WorkflowID, name, chatID)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunFinished, chatID, marotte.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
		Name:       name,
	}))
}

// RunProgressHandler returns the handler for one of the seven progress kinds; the kind on
// the event tells the client how eagerly to refetch, never how to rebuild state.
func (t *Translator) RunProgressHandler(kind marotte.RunProgressKind) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		p, ok := unmarshalParams[kasRunNode](msg, "workflow/"+string(kind))
		if !ok || p.WorkflowID == "" {
			return
		}
		node := cmp.Or(p.NodeID, p.LoopID)
		switch kind {
		case marotte.RunProgressNodeStart:
			t.wakeMem.nodeRunning(p.WorkflowID, node)
			path := nodeKey(p.NodePath, node)
			// The ONE frame announcing a step's session id; recorded before the broadcast so a racing
			// permission ask still classifies.
			if p.SessionID != "" {
				t.steps.record(p.SessionID, p.WorkflowID, node, path)
				t.wakes.nodeStarted(p.SessionID)
			}
			// The run turn's opening bracket; a path already open is a no-op on the log.
			t.runs.RunNodeStart(ctx, &RunStep{RunID: p.WorkflowID, NodePath: path, SessionID: p.SessionID, Path: nodePathOf(p.NodePath, node)}, chatID)
		case marotte.RunProgressNodeComplete:
			t.wakeMem.nodeRunning(p.WorkflowID, node)
			t.runs.RunNodeComplete(ctx, p.WorkflowID, nodePathOf(p.NodePath, node), p.Status, p.FailureReason)
		case marotte.RunProgressNodePaused:
			if p.retryWait() {
				t.wakeMem.retryWaited(p.WorkflowID, node)
			} else {
				// A retry-wait is not one: the node stays running, so KAS buffers a parent's message.
				t.wakes.nodePaused(t.steps.sessionAt(p.WorkflowID, nodeKey(p.NodePath, node)))
				t.wakeMem.nodePaused(p.WorkflowID, node)
				t.runs.RunNodePaused(ctx, p.WorkflowID, nodePathOf(p.NodePath, node))
			}
		case marotte.RunProgressStepsQueued:
			t.runs.RunPlanUpdate(ctx, chatID, p.WorkflowID, planUpdateOf(&p))
		}
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunProgress, chatID,
			runProgress(kind, node, &p, time.Now())))
	}
}

// An empty node path is the signal: a client applies a named node and refetches otherwise.
func runProgress(
	kind marotte.RunProgressKind, node string, p *kasRunNode, at time.Time,
) marotte.RunProgressPayload {
	out := marotte.RunProgressPayload{WorkflowID: p.WorkflowID, NodeID: node, Kind: kind}
	stamp := at.UTC().Format(time.RFC3339Nano)
	switch kind {
	case marotte.RunProgressNodeStart:
		out.NodePath = nodePathOf(p.NodePath, node)
		out.Status = runNodeStatusRunning
		out.StartedAt = stamp
		out.SessionID = p.SessionID
	case marotte.RunProgressNodeComplete:
		out.NodePath = nodePathOf(p.NodePath, node)
		// KAS's word is already the client tree's NodeState vocabulary.
		out.Status = p.Status
		out.EndedAt = stamp
		out.FailureReason = p.FailureReason
		out.Credits = nodeCredits(p.Credits)
	case marotte.RunProgressNodePaused:
		out.NodePath = nodePathOf(p.NodePath, node)
		if p.retryWait() {
			out.Status = runNodeStatusRunning
			out.RetryReason = p.Reason
		} else {
			out.Status = runNodeStatusPaused
			out.PauseReason = p.Reason
		}
	case marotte.RunProgressWatchPoll:
		// A poll re-states `running`: a frame stating nothing cannot be applied.
		out.NodePath = nodePathOf(p.NodePath, node)
		out.Status = runNodeStatusRunning
	case marotte.RunProgressPaused:
		out.Initiator = p.Initiator
		out.InitiatorReason = p.InitiatorReason
	case marotte.RunProgressLoopIteration, marotte.RunProgressStepsQueued:
	}
	return out
}

// nodeCredits is a step's credit-unit total from node_complete's `credits`, the step session's
// whole spend; 0 for an absent or malformed member, which KAS's own schema drops too.
func nodeCredits(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var summaries []promptTurnSummary
	if err := json.Unmarshal(raw, &summaries); err != nil {
		slog.Debug("workflow node_complete: credits not decoded", "err", err)
		return 0
	}
	return creditsOf(summaries)
}

// planUpdateOf reads a `steps_queued`: steps without a resolution queue a revision, a resolution
// settles the queued one. An outcome outside KAS's three is kept as KAS's word.
func planUpdateOf(p *kasRunNode) marotte.RunPlanUpdate {
	if p.Resolution == nil {
		return marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: len(p.PendingSteps)}
	}
	return marotte.RunPlanUpdate{Outcome: marotte.RunPlanOutcome(p.Resolution.Outcome), Reason: p.Resolution.Reason}
}

// The two KAS NodeState words this translator asserts rather than forwards.
const (
	runNodeStatusRunning = "running"
	runNodeStatusPaused  = "paused"
)
