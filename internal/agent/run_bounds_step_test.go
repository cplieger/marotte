package agent

// An open step turn holds the idle window: KAS has the step in flight, and a step inside a KAS-side
// tool or a model reasoning silently sends no frame for longer than the window.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

func openStep(t *testing.T, h *Runtime, workflowID string, path []string, sessionID string) {
	t.Helper()
	step := &translate.RunStep{RunID: workflowID, NodePath: workflow.PathKey(path), NodeID: path[len(path)-1], SessionID: sessionID}
	if _, _, err := h.runs.log.open(t.Context(), step, runChatID(workflowID)); err != nil {
		t.Fatalf("open step %q on %s: %v", path, sessionID, err)
	}
}

// stepFixture is a manual run on its own carrier with a run log, whose cancel KAS would take.
func stepFixture(t *testing.T, id string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	return h, br
}

func wantWindowHeld(t *testing.T, h *Runtime, br *fakeBridge, id string) {
	t.Helper()
	if got := h.runs.endReason(id); got != "" {
		t.Errorf("endReason = %q, want the window refilled for a run whose step is in flight", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a run whose step is in flight: %v", br.callLog())
	}
	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("the refill left %v of budget, want a full idle window %v", budget.Round(time.Second), runIdleWindow)
	}
}

// A model reasoning past the window sends nothing: GPT-class steps stream no thinking frames.
func TestCancelExpired_AStepReasoningSilentlyPastTheWindowIsNotStopped(t *testing.T) {
	const id = "wf_1"
	h, br := stepFixture(t, id)
	openStep(t, h, id, []string{"root", "a"}, "step-session-a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantWindowHeld(t, h, br, id)
}

// The wire's own sequence: KAS sends node_start twice (only the second names the session), then a tool
// it runs itself, which emits nothing until it returns.
func TestCancelExpired_AStepInsideALongKASToolIsNotStopped(t *testing.T) {
	const id = "wf_1"
	h, br := stepFixture(t, id)
	carrier := runChatID(id)
	start := map[string]any{"workflowId": id, "nodeId": "review", "nodePath": []string{id, "review"}, "type": stepNodeType}
	h.dispatch(t.Context(), carrier, h.originOf(carrier), runNotif(methodWFNodeStart, start))
	start["sessionId"] = "step-session-a"
	h.dispatch(t.Context(), carrier, h.originOf(carrier), runNotif(methodWFNodeStart, start))
	h.dispatch(t.Context(), carrier, h.originOf(carrier), stepToolCall(t, "step-session-a", map[string]any{
		"workflowId": id, "nodeId": "review", "nodePath": []string{id, "review"},
	}))
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantWindowHeld(t, h, br, id)
}

// agent-mail-watch's shape: a repeat around a command watch that polls and answers idle. KAS sends a
// watch_poll per idle poll and nothing else, so only the open watch turn says the run is working.
func TestCancelExpired_ACommandWatchPollingIdleIsNotStopped(t *testing.T) {
	const id = "wf_1"
	h, br := stepFixture(t, id)
	carrier := runChatID(id)
	watch := []string{id, "watching", "iter-0", "mail"}
	h.dispatch(t.Context(), carrier, h.originOf(carrier), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": id, "nodeId": "watching", "nodePath": []string{id, "watching"}, "type": "repeat",
	}))
	h.dispatch(t.Context(), carrier, h.originOf(carrier), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": id, "nodeId": "mail", "nodePath": watch, "type": "watch",
	}))
	for range 3 {
		h.dispatch(t.Context(), carrier, h.originOf(carrier), runNotif(methodWFWatchPoll, map[string]any{
			"workflowId": id, "nodeId": "mail", "nodePath": watch, "outcome": "idle",
		}))
	}
	if h.runs.log.turn(id, workflow.PathKey(watch)) == nil {
		t.Fatal("Setup: node_start opened no turn for the watch")
	}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantWindowHeld(t, h, br, id)
}

// A path over keyenc.MaxComponentBytes keys its turn by a hash, and the open turn still holds the window.
func TestCancelExpired_AStepWithAHashedPathKeyHoldsTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := stepFixture(t, id)
	long := strings.Repeat("x", 9<<10)
	h.dispatch(t.Context(), runChatID(id), h.originOf(runChatID(id)), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": id, "nodeId": long, "nodePath": []string{id, long}, "type": stepNodeType, "sessionId": "step-session-a",
	}))
	if h.runs.log.turn(id, workflow.PathKey([]string{id, long})) == nil {
		t.Fatal("Setup: node_start opened no turn for the step")
	}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantWindowHeld(t, h, br, id)
}

// After a restart the step registry is cold, so the content frame opening a resumed step's turn names the
// node only in its workflow _meta; that turn holds the window too.
func TestCancelExpired_AStepOpenedByItsMetaHoldsTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := stepFixture(t, id)
	h.dispatch(t.Context(), runChatID(id), h.originOf(runChatID(id)), stepMetaChunk(t, "step-session-a", map[string]any{
		"workflowId": id, "nodeId": "a", "nodePath": []string{id, "a"},
	}))
	if h.runs.log.turn(id, workflow.PathKey([]string{id, "a"})) == nil {
		t.Fatal("Setup: the step's content frame opened no turn")
	}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantWindowHeld(t, h, br, id)
}

// stepMetaChunk is a step session's agent_message_chunk carrying its _meta.kiro.workflow block.
func stepMetaChunk(t *testing.T, sessionID string, wf map[string]any) *marotte.RPCResponse {
	t.Helper()
	return stepUpdate(t, sessionID, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "working"},
		"_meta":         map[string]any{"kiro": map[string]any{"workflow": wf}},
	})
}

// stepToolCall is a step session's pending KAS-side search call, as the aborted reviews' last frame was.
func stepToolCall(t *testing.T, sessionID string, wf map[string]any) *marotte.RPCResponse {
	t.Helper()
	return stepUpdate(t, sessionID, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "tc-search-1",
		"title":         "File Search",
		"kind":          "search",
		"status":        "pending",
		"_meta":         map[string]any{"kiro": map[string]any{"workflow": wf}},
	})
}

func stepUpdate(t *testing.T, sessionID string, update map[string]any) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: mustJSON(t, map[string]any{
		"sessionId": sessionID, "update": mustJSON(t, update),
	})}
}
