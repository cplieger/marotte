package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
)

// chatParentedHub hosts an agent-launched run the way the user's own chat does: its launching chat's session on the
// session the lease records. The run is read on the engine, whose lane answers inspect.
func chatParentedHub(t *testing.T, l runlease.Lease) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, engine := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	chat := newFakeBridge()
	chat.sessionID = "sess_parent"
	engine.setCallResult(methodKiroWorkflowInspect, recordedReply(t, "parked"))
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: chat, state: bridgeIdle})
	l.Origin = runlease.OriginAgent
	leaseRun(t, h, l)
	return h, engine
}

// The user's case: the park raises the card on the launching chat, and the parent's send_message plus the step's
// resumed turn take it down.
func TestRunAsks_AChatParentedRunsAskIsRaisedAndClearedOnItsChat(t *testing.T) {
	h, engine := chatParentedHub(t, runlease.Lease{WorkflowID: recordedRun, ChatID: "c1", ParentSessionID: "sess_parent"})

	h.chatHandlers[methodWFNodePaused](t.Context(), "c1", runNotif(methodWFNodePaused, map[string]any{
		"workflowId": recordedRun, "nodeId": "ask", "reason": "waiting",
	}))
	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 1 })

	engine.setCallResult(methodKiroWorkflowInspect, recordedReply(t, "running-after-parent-send"))
	h.chatHandlers[methodKiroSessionNotify](t.Context(), "c1", runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": recordedSession, "callerSessionId": "sess_parent", "message": "Use main.",
		"severity": "info", "sender": "parent", "workflowId": recordedRun,
	}))
	h.runs.askTurnEdge(recordedRun, recordedSession, translate.TurnEdgeStart)
	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 0 })
}

// A lease minted before its run named a parent session learns it from run_start, which ensureResident reads.
func TestRunAsks_RunStartRecordsTheParentSessionOnTheLease(t *testing.T) {
	h, _ := chatParentedHub(t, runlease.Lease{WorkflowID: recordedRun, ChatID: "c1"})

	h.chatHandlers[methodWFRunStart](t.Context(), "c1", runNotif(methodWFRunStart, map[string]any{
		"workflowId": recordedRun, "workflowName": "asker", "parentSessionId": "sess_parent",
	}))

	if l, _ := h.runs.lease(recordedRun); l.ParentSessionID != "sess_parent" {
		t.Errorf("the lease's parent session = %q, want the run_start's", l.ParentSessionID)
	}
	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 1 })
}

// A paused run keeps its question across an engine restart: the new generation's read finds the same wait under the
// same id, and no published snapshot between the two drops or replaces the card.
func TestRunAsks_AnEngineRestartKeepsTheRunsQuestion(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	fakeSourceOf(t, h).setFactory(func() engineSession { return newFakeBridge() })
	leaseParentless(t, h, "wf_1")
	if _, err := h.runs.ensureResident(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Setup: ensureResident = %v", err)
	}
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeEnd)
	waitFor(t, func() bool { return len(lastAsksOf(t, h, "wf_1").Asks) == 1 })
	askID := readNow(t, h, "wf_1").asks[0].ID

	fakeSourceOf(t, h).current().crash()
	waitReady(t, h.engine, 2)
	waitFor(t, func() bool { return h.coord.bridgeFor(runChatID("wf_1")) == nil })
	br.forgetCalls()

	got, body := getRun(t, h, "wf_1")

	if len(got.OpenAsks) != 1 || got.OpenAsks[0].AskID != askID || !got.AsksReachable {
		t.Errorf("open_asks = %+v reachable %v, want the same wait %s: %s", got.OpenAsks, got.AsksReachable, askID, body)
	}
	if countCalls(br, methodKiroWorkflowInspect) == 0 {
		t.Error("the answer was not read from the restarted engine")
	}
	if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || p.Asks[0].AskID != askID {
		t.Errorf("the published snapshot = %+v, want the same wait", p.Asks)
	}
	frames := runAsksFrames(t, h, "wf_1")
	first := slices.IndexFunc(frames, func(f marotte.RunAsksPayload) bool { return len(f.Asks) > 0 })
	for _, f := range frames[max(first, 0):] {
		if len(f.Asks) != 1 || f.Asks[0].AskID != askID {
			t.Errorf("seq %d = %+v: the restart churned the card", f.Seq, f.Asks)
		}
	}
}

// One run's wedged inspect blocks nothing on another run on the same engine: its read, publish and verbs go through.
func TestRunAsks_AWedgedRunBlocksNoOtherRun(t *testing.T) {
	h, wedged, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	other := newFakeBridge()
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_2", ParentSessionID: testLaunchSession})
	h.bridge.mgr.insert(runChatID("wf_2"), &sharedBridge{bridge: other, state: bridgeIdle})
	askID := readNow(t, h, "wf_1").asks[0].ID
	stuck := make(chan struct{})
	wf2 := inspectAsk(t, "wf_2", marotte.RunStatusRunning, parkedStep("review", "sess_two"))
	wedged.mu.Lock()
	wedged.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, nil, false
		}
		if params[keyWorkflowID] == "wf_2" {
			return wf2, nil, true
		}
		<-stuck
		return nil, nil, false
	}
	wedged.mu.Unlock()
	answered := make(chan error, 1)
	go func() { answered <- h.runs.answerInput(context.Background(), "wf_1", askID, "teal") }()
	t.Cleanup(func() { close(stuck); <-answered })
	waitFor(t, func() bool {
		return len(slices.DeleteFunc(wedged.callLog(), func(m string) bool { return m != methodKiroWorkflowInspect })) >= 2
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r, err := h.runs.readAsks(ctx, h.runs.cellFor("wf_2"))
	if err != nil || len(r.asks) != 1 {
		t.Fatalf("the other run's read = %d asks, %v; want its question", len(r.asks), err)
	}
	if p := lastAsksOf(t, h, "wf_2"); !p.Reachable || len(p.Asks) != 1 {
		t.Errorf("the other run's snapshot = %+v, want its question published", p)
	}
	if err := h.runs.pause(ctx, "wf_2"); err != nil {
		t.Errorf("a pause of the other run = %v, want it sent", err)
	}
	if !slices.Contains(append(other.callLog(), wedged.callLog()...), methodKiroWorkflowPause) {
		t.Error("the other run's pause never reached KAS")
	}
}
