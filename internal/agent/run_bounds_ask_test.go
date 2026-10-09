package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/workflow"
)

func askFixture(t *testing.T, id string, carrier marotte.ChatID) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(carrier, &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, id, []string{"root", "a"}, "step-session-a")
	return h, br
}

func stepAsk(h *Runtime, carrier marotte.ChatID, reqID int64, runID, nodeID string) {
	h.bus.PendingPermsAdd(reqID, marotte.NewEvent(marotte.EventPermissionNeeded, carrier,
		marotte.PermissionNeededPayload{RequestID: reqID, RunID: runID, NodeID: nodeID}))
}

func wantHeld(t *testing.T, h *Runtime, br *fakeBridge, id string) {
	t.Helper()
	if got := h.runs.endReason(id); got != "" {
		t.Errorf("endReason = %q, want the window refilled for a run a person owes an answer", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a run waiting on an answer: %v", br.callLog())
	}
	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("the refill left %v of budget, want a full idle window %v", budget.Round(time.Second), runIdleWindow)
	}
}

func wantStalled(t *testing.T, h *Runtime, br *fakeBridge, id string) {
	t.Helper()
	if got := h.runs.endReason(id); got != runEndStalled {
		t.Errorf("endReason = %q, want %q", got, runEndStalled)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for the stalled run: %v", br.callLog())
	}
}

func TestCancelExpired_AnOpenAskOnAManualRunHoldsTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	stepAsk(h, runChatID(id), 7, id, "a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantHeld(t, h, br, id)
}

// An agent-launched run's ask is filed under the launching chat, so the arm keys on the payload's run.
func TestCancelExpired_AnOpenAskOnAnAgentRunHoldsTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, "c1")
	stepAsk(h, "c1", 7, id, "a")
	deadline := stagedExpiry(t, h.runs, id, launchOrigin{origin: runlease.OriginAgent, chatID: "c1"})

	h.runs.cancelExpired(id, deadline)

	wantHeld(t, h, br, id)
}

func TestCancelExpired_AnAnsweredAskDoesNotHoldTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	stepAsk(h, runChatID(id), 7, id, "a")
	if !h.bus.TakePendingPerm(runChatID(id), 7, marotte.SettledByUser) {
		t.Fatal("the ask was not pending")
	}
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantStalled(t, h, br, id)
}

// An ask from a closed step must not hold the window: the engine abandons such asks.
func TestCancelExpired_AnAskFromAClosedStepDoesNotHoldTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	stepAsk(h, runChatID(id), 7, id, "b")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantStalled(t, h, br, id)
}

// A path over keyenc.MaxComponentBytes keys its turn by a hash, and its step's ask still holds the window.
func TestCancelExpired_AnAskFromAStepWithAHashedPathKeyHoldsTheWindow(t *testing.T) {
	const id = "wf_1"
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	long := strings.Repeat("x", 9<<10)
	h.dispatch(t.Context(), runChatID(id), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": id, "nodeId": long, "nodePath": []string{id, long}, "type": stepNodeType, "sessionId": "step-session-a",
	}))
	if h.runs.log.Turn(id, workflow.PathKey([]string{id, long})) == nil {
		t.Fatal("Setup: node_start opened no turn for the step")
	}
	stepAsk(h, runChatID(id), 7, id, long)
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantHeld(t, h, br, id)
}

// After a restart the step registry is cold, so the content frame opening a resumed step's turn names the
// node only in its workflow _meta; the ask is stamped once a run read has seeded the registry.
func TestCancelExpired_AnAskFromAStepOpenedByItsMetaHoldsTheWindow(t *testing.T) {
	const id = "wf_1"
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.log = newRunLog(t.TempDir())
	h.dispatch(t.Context(), runChatID(id), stepMetaChunk(t, "step-session-a", map[string]any{
		"workflowId": id, "nodeId": "a", "nodePath": []string{id, "a"},
	}))
	if h.runs.log.Turn(id, workflow.PathKey([]string{id, "a"})) == nil {
		t.Fatal("Setup: the step's content frame opened no turn")
	}
	stepAsk(h, runChatID(id), 7, id, "a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantHeld(t, h, br, id)
}

// stepMetaChunk is a step session's agent_message_chunk carrying its _meta.kiro.workflow block.
func stepMetaChunk(t *testing.T, sessionID string, wf map[string]any) *marotte.RPCResponse {
	t.Helper()
	update := mustJSON(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "working"},
		"_meta":         map[string]any{"kiro": map[string]any{"workflow": wf}},
	})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: mustJSON(t, map[string]any{
		"sessionId": sessionID, "update": update,
	})}
}

func TestCancelExpired_AnotherRunsAskDoesNotHoldTheWindow(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	stepAsk(h, runChatID("wf_2"), 7, "wf_2", "a")
	stepAsk(h, runChatID(id), 8, "", "a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantStalled(t, h, br, id)
}

// The slot and backstop still bound a run with an open ask.
func TestCancelExpired_ASlotOutranksAnOpenAsk(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	stepAsk(h, runChatID(id), 7, id, "a")
	deadline := stagedExpiry(t, h.runs, id, scheduledLaunch("sched-1", time.Now().Add(-2*time.Second)))

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q: the slot outranks an open ask", got, runEndOverran)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out at the slot: %v", br.callLog())
	}
}

func TestCancelExpired_ASpentBackstopOutranksAnOpenAsk(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	stepAsk(h, runChatID(id), 7, id, "a")
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())
	// The open stretch began a whole backstop ago.
	h.runs.mu.Lock()
	h.runs.bounds.armedAt[id] = time.Now().Add(-runBackstop)
	h.runs.mu.Unlock()

	h.runs.cancelExpired(id, deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q: the backstop outranks an open ask", got, runEndOverran)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out at the backstop: %v", br.callLog())
	}
}

func TestCancelExpired_ElicitationAndUserInputAsksHoldTheWindow(t *testing.T) {
	const id = "wf_1"
	for _, tc := range []struct {
		name string
		evt  marotte.ServerEvent
	}{
		{"elicitation", marotte.NewEvent(marotte.EventElicitationNeeded, runChatID(id),
			marotte.ElicitationNeededPayload{RequestID: 7, RunID: id, NodeID: "a"})},
		{"user input", marotte.NewEvent(marotte.EventUserInputNeeded, runChatID(id),
			marotte.UserInputNeededPayload{RequestID: 7, RunID: id, NodeID: "a"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := askFixture(t, id, runChatID(id))
			h.bus.PendingPermsAdd(7, tc.evt)
			deadline := stagedExpiry(t, h.runs, id, manualLaunch())

			h.runs.cancelExpired(id, deadline)

			wantHeld(t, h, br, id)
		})
	}
}
