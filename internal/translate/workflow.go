package translate

// v3 workflow-run lifecycle handlers: KAS's nine notifications become three SSE events
// (seven invalidations share one handler). They arrive on the LAUNCHING CHAT's bridge.

import (
	"cmp"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runOriginAgent labels a run KAS parented on a chat session, the population with no
// supervisor in this tier — see logAgentRun.
const runOriginAgent = "agent"

// kasRunStart mirrors _kiro/workflow/run_start. `nodeTree` and `inputs` are not decoded:
// the client refetches `inspect`, whose tree is fresher. `parentSessionId` is not decoded
// either (see logAgentRun).
type kasRunStart struct {
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
}

// kasRunNode is the shape shared by the seven progress frames. Every field is optional across
// the set: `paused` carries only the workflow id, and `loop_iteration` names its node in
// `loopId` rather than `nodeId`.
type kasRunNode struct {
	WorkflowID string   `json:"workflowId"`
	NodeID     string   `json:"nodeId"`
	LoopID     string   `json:"loopId"`
	SessionID  string   `json:"sessionId"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	NodePath   []string `json:"nodePath"`
}

// kasRunComplete mirrors _kiro/workflow/run_complete. `finalState` is read only for
// `workflowName`, the one lifecycle frame with no top-level name; the client refetches.
type kasRunComplete struct {
	WorkflowID string `json:"workflowId"`
	Status     string `json:"status"`
	FinalState struct {
		WorkflowName string `json:"workflowName"`
		RunLabel     string `json:"runLabel"`
	} `json:"finalState"`
}

// logAgentRun writes the one durable trace an agent-launched run gets: two lines correlated
// by workflow_id, since such a run has no record or supervisor. Silent for a run marotte
// launched. The discriminator is the DELIVERY ADDRESS, not `parentSessionId` (which
// _kiro/workflow/new requires): (*Runtime).dispatch hands a run-bridge frame an EMPTY chat
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
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunStarted, chatID, marotte.RunStartedPayload{
		WorkflowID: p.WorkflowID,
		Name:       cmp.Or(t.runOrigin.RunLabel(p.WorkflowID), p.WorkflowName),
		// Keyed on the workflow id: this frame's chat id is empty for exactly these runs.
		Scheduled: t.runOrigin.IsScheduled(p.WorkflowID),
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
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunFinished, chatID, marotte.RunFinishedPayload{
		WorkflowID: p.WorkflowID,
		Status:     p.Status,
		Name:       cmp.Or(p.FinalState.RunLabel, p.FinalState.WorkflowName),
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
			path := runNodePathOf(&p, node)
			// The ONE frame announcing a step's session id; recorded before the broadcast so a racing
			// permission ask still classifies.
			if p.SessionID != "" {
				t.steps.record(p.SessionID, p.WorkflowID, node, path)
			}
			// The run turn's opening bracket; a path already open is a no-op on the log.
			t.runs.RunNodeStart(ctx, p.WorkflowID, path, p.SessionID, chatID)
		case marotte.RunProgressNodeComplete:
			t.runs.RunNodeComplete(ctx, p.WorkflowID, runNodePathOf(&p, node), p.Status, p.Reason)
		}
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunProgress, chatID,
			runProgress(kind, node, &p, time.Now())))
	}
}

// runProgress builds the frame for one progress kind. An empty node path is the signal:
// a client applies a named node and refetches otherwise.
func runProgress(
	kind marotte.RunProgressKind, node string, p *kasRunNode, at time.Time,
) marotte.RunProgressPayload {
	out := marotte.RunProgressPayload{WorkflowID: p.WorkflowID, NodeID: node, Kind: kind}
	stamp := at.UTC().Format(time.RFC3339Nano)
	switch kind {
	case marotte.RunProgressNodeStart:
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusRunning
		out.StartedAt = stamp
	case marotte.RunProgressNodeComplete:
		out.NodePath = runNodePathOf(p, node)
		// KAS's word is already the client tree's NodeState vocabulary.
		out.Status = p.Status
		out.EndedAt = stamp
		out.FailureReason = p.Reason
	case marotte.RunProgressNodePaused:
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusPaused
	case marotte.RunProgressWatchPoll:
		// A poll re-states `running`: a frame stating nothing cannot be applied.
		out.NodePath = runNodePathOf(p, node)
		out.Status = runNodeStatusRunning
	case marotte.RunProgressLoopIteration, marotte.RunProgressPaused, marotte.RunProgressStepsQueued:
	}
	return out
}

// The two KAS NodeState words this translator asserts rather than forwards.
const (
	runNodeStatusRunning = "running"
	runNodeStatusPaused  = "paused"
)

// runNodePathOf joins the frame's node path, falling back to the node id: an
// empty path would silently mean "refetch" (see runProgress).
func runNodePathOf(p *kasRunNode, node string) string {
	if len(p.NodePath) > 0 {
		return strings.Join(p.NodePath, "/")
	}
	return node
}
