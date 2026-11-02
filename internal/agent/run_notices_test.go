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

// KAS queues a notice only into a busy parent's buffer, so only a live launching chat records, in finish order.
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

	notice := func(label string) string { return `A workflow you launched ("` + label + `") completed.` }
	if wf, _, ok := h.runs.RunNotice("c1", notice("wf_2")); !ok || wf != "wf_2" {
		t.Errorf("RunNotice(c1, wf_2's words) = %q ok=%v, want wf_2: the words name the run, not the order", wf, ok)
	}
	wf, ts, ok := h.runs.RunNotice("c1", notice("wf_1"))
	if !ok || wf != "wf_1" || ts < before {
		t.Errorf("RunNotice(c1, wf_1's words) = %q at %d ok=%v, want wf_1 at or after %d", wf, ts, ok, before)
	}
	if wf, _, ok = h.runs.RunNotice("c1", notice("wf_p")); ok {
		t.Errorf("RunNotice(c1, wf_p's words) = %q, want none: a paused run_complete queues no notice", wf)
	}
	if wf, _, ok = h.runs.RunNotice("c2", notice("wf_3")); ok {
		t.Errorf("RunNotice(c2) = %q, want none: KAS prompts an idle parent instead of queueing", wf)
	}
	if wf, _, ok = h.runs.RunNotice(runChatID("wf_4"), notice("wf_4")); ok {
		t.Errorf("RunNotice(run chat) = %q, want none: a parentless run has no chat buffer", wf)
	}
}

// A clear carries no words, so it forgets as many notices as it names, and a notice no label matches names no run.
func TestRunNotice_AClearForgetsAndUnmatchedWordsNameNoRun(t *testing.T) {
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	h.stagePromptTurn(t, "c1")
	for _, wf := range []string{"wf_1", "wf_2", "wf_3"} {
		h.runs.observeComplete(t.Context(), "c1", runComplete(wf, "completed"))
	}

	if wf, _, ok := h.runs.RunNotice("c1", `A workflow you launched ("nightly") completed.`); ok {
		t.Errorf("RunNotice(unmatched words) = %q, want none", wf)
	}
	h.runs.ForgetRunNotices("c1", 2)
	if !h.runs.holdsNotice("c1") {
		t.Fatal("forgetting two of three notices left none")
	}
	h.runs.ForgetRunNotices("c1", 5)
	if h.runs.holdsNotice("c1") {
		t.Error("forgetting more notices than recorded left some")
	}
}

// Two launches of one workflow quote one label, so the words cannot tell their notices apart: reversed delivery
// must stamp neither notice with the other run's provenance.
func TestRunNotice_ASharedLabelNamesNoRun(t *testing.T) {
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	h.stagePromptTurn(t, "c1")
	for _, wf := range []string{"wf_1", "wf_2"} {
		h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": wf, "status": "completed", "finalState": map[string]any{"workflowName": "nightly"},
		}))
	}
	notice := `A workflow you launched ("nightly") completed.`

	for i := range 2 {
		if wf, _, ok := h.runs.RunNotice("c1", notice); ok {
			t.Errorf("RunNotice #%d (shared label) = %q, want no run: the words fit both", i+1, wf)
		}
	}
	if h.runs.holdsNotice("c1") {
		t.Error("the ambiguous notices are still queued, holding clearStaleNotices open")
	}
}

// After one of two same-label notices is read, KAS clearing the other must not take a third run's notice with it.
func TestRunNotice_AClearAfterAnAmbiguousReadSparesAnotherRunsNotice(t *testing.T) {
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	h.stagePromptTurn(t, "c1")
	h.runs.observeComplete(t.Context(), "c1", runComplete("wf_3", "completed"))
	for _, wf := range []string{"wf_1", "wf_2"} {
		h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
			"workflowId": wf, "status": "completed", "finalState": map[string]any{"workflowName": "nightly"},
		}))
	}

	if wf, _, ok := h.runs.RunNotice("c1", `A workflow you launched ("nightly") completed.`); ok {
		t.Fatalf("RunNotice(shared label) = %q, want no run", wf)
	}
	h.runs.ForgetRunNotices("c1", 1)

	if wf, _, ok := h.runs.RunNotice("c1", `A workflow you launched ("wf_3") completed.`); !ok || wf != "wf_3" {
		t.Errorf("RunNotice(wf_3's words) = %q ok=%v, want wf_3: the clear named the other nightly notice", wf, ok)
	}
	if h.runs.holdsNotice("c1") {
		t.Error("a notice is still queued after every one was read or cleared")
	}
}

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

// KAS skips its turn-end clear on an abnormal end, so marotte clears a settled chat's buffer itself while a notice is held.
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
		t.Errorf("clear sessionId = %v, want the chat session's own %q", got, br.SessionID())
	}
}

// Shutdown stops the bridge itself, so a close once it began sends no clear.
func TestTurnClosed_OnceShutdownBeganSendsNoClear(t *testing.T) {
	h, _, br := newTestHub()
	defer shutdownHub(t, h)
	br.callResults = map[string]json.RawMessage{
		marotte.MethodSessionSteerClear: json.RawMessage(`{"cleared":true,"messageIds":["notify-wf-1"]}`),
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.runs.observeComplete(t.Context(), "c1", runComplete("wf_1", "completed"))
	beginDraining(h)

	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerWireEnd, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})
	joinInflight(t, h)

	if slices.Contains(br.callLog(), marotte.MethodSessionSteerClear) {
		t.Errorf("_session/steer/clear went out once Shutdown began: %v", br.callLog())
	}
}

// No held notice, no clear.
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
