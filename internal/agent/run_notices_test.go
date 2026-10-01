package agent

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func runComplete(workflowID, status string) *marotte.RPCResponse {
	return runNotif(methodWFRunComplete, map[string]any{"workflowId": workflowID, "status": status})
}

// KAS queues a completion notice only into a BUSY parent's buffer (an idle one is
// prompted), so the queue mirrors that: a live launching chat records, an idle chat
// and a run's own synthetic chat do not, and the notices pair in finish order.
func TestObserveComplete_QueuesANoticeForABusyLaunchingChatOnly(t *testing.T) {
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	h.stagePromptTurn(t, "c1")
	before := time.Now().UnixMilli()

	h.runs.observeComplete(t.Context(), "c1", runComplete("wf_1", "completed"))
	h.runs.observeComplete(t.Context(), "c1", runComplete("wf_2", "failed"))
	h.runs.observeComplete(t.Context(), "c1", runComplete("wf_p", "paused"))
	h.runs.observeComplete(t.Context(), "c2", runComplete("wf_3", "completed"))
	h.runs.observeComplete(t.Context(), runChatID("wf_4"), runComplete("wf_4", "completed"))

	wf, ts, ok := h.runs.RunNotice("c1")
	if !ok || wf != "wf_1" || ts < before {
		t.Errorf("RunNotice(c1) #1 = %q at %d ok=%v, want wf_1 at or after %d", wf, ts, ok, before)
	}
	if wf, _, ok = h.runs.RunNotice("c1"); !ok || wf != "wf_2" {
		t.Errorf("RunNotice(c1) #2 = %q ok=%v, want wf_2 (finish order)", wf, ok)
	}
	if wf, _, ok = h.runs.RunNotice("c1"); ok {
		t.Errorf("RunNotice(c1) #3 = %q, want none: a paused run_complete queues no notice", wf)
	}
	if wf, _, ok = h.runs.RunNotice("c2"); ok {
		t.Errorf("RunNotice(c2) = %q, want none: KAS prompts an idle parent instead of queueing", wf)
	}
	if wf, _, ok = h.runs.RunNotice(runChatID("wf_4")); ok {
		t.Errorf("RunNotice(run chat) = %q, want none: a parentless run has no chat buffer", wf)
	}
}

// awaitCall polls the fake's call log for method, failing closed at the deadline.
func awaitCall(t *testing.T, br *fakeBridge, method string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(br.callLog(), method) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// ADDENDUM 8 (b): KAS skips its own turn-end clear when a turn ends abnormally, so a
// notice queued during that turn ambushes the reader's next prompt. When the chat's
// turn closes with a finished run still held, marotte clears the buffer itself, on
// the chat's own session; while the turn is live nothing is cleared.
func TestTurnClosed_ClearsTheBufferOfASettledChatHoldingANotice(t *testing.T) {
	h, _, br := newTestHub()
	defer shutdownHub(t, h)
	br.callResults = map[string]json.RawMessage{
		marotte.MethodSessionSteerClear: json.RawMessage(`{"cleared":true,"messageIds":["notify-wf-1"]}`),
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.runs.observeComplete(t.Context(), "c1", runComplete("wf_1", "completed"))

	h.runs.clearStaleNotices(t.Context(), "c1")
	if slices.Contains(br.callLog(), marotte.MethodSessionSteerClear) {
		t.Fatal("cleared while the turn was still live: KAS would drop a notice the turn's own drain was about to read")
	}

	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerWireEnd, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})

	if !awaitCall(t, br, marotte.MethodSessionSteerClear) {
		t.Fatalf("no _session/steer/clear after the close of a chat holding a run notice; calls: %v", br.callLog())
	}
	if got := br.paramsFor(marotte.MethodSessionSteerClear)[marotte.KeySessionID]; got != br.SessionID() {
		t.Errorf("clear sessionId = %v, want the chat bridge's own %q", got, br.SessionID())
	}
}

// The clear is gated on a held notice, not on the close: a chat whose turn ended
// with nothing queued is left to KAS's own boundary rule.
func TestTurnClosed_LeavesAChatHoldingNoNoticeAlone(t *testing.T) {
	h, _, br := newTestHub()
	defer shutdownHub(t, h)
	br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteerClear: json.RawMessage(`{"cleared":true}`)}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	turnID, _ := h.stagePromptTurn(t, "c1")

	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerWireEnd, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})

	if awaitCall(t, br, marotte.MethodSessionSteerClear) {
		t.Errorf("_session/steer/clear went out for a chat holding no run notice: %v", br.callLog())
	}
}
