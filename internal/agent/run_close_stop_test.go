package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// setStopTiming shortens the stop's window and backoff for one test.
func setStopTiming(t *testing.T, window, first time.Duration) {
	t.Helper()
	w, f, m := runStopWindow, runStopFirstRetry, runStopMaxRetry
	runStopWindow, runStopFirstRetry, runStopMaxRetry = window, first, first
	t.Cleanup(func() { runStopWindow, runStopFirstRetry, runStopMaxRetry = w, f, m })
}

// newStopRig is a hub whose chat c1 holds sess_owner and launched wf_1, running, with its keep mark written by its
// run_start. Inspect answers wf_1 running until the test changes it.
func newStopRig(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "r", "status": "running", "parentSessionId": "sess_owner",
		}),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowDelete:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusRunning, ""),
	}
	cs.seed(t, "c1", func(c *marotte.Chat) { c.RecordSession("sess_owner") })
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "r", "parentSessionId": "sess_owner",
	}))
	if !holdsKey(t, cs, "c1", runKey) {
		t.Fatal("Setup: the run's start wrote no keep mark on its launcher")
	}
	return h, cs, br
}

// TestStopChatRuns_ReadsTheRunInventoryOnce pins one `workflow/list` for a tab close, not N+1. The bridge is inserted
// so openBridge's rehydrate hook does not read the inventory.
func TestStopChatRuns_ReadsTheRunInventoryOnce(t *testing.T) {
	setStopTiming(t, time.Hour, time.Hour)
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t,
			map[string]any{"workflowId": "wf_1", "name": "publish", "status": "running", "parentSessionId": "sess_owner"},
			map[string]any{"workflowId": "wf_2", "name": "publish", "status": "paused", "parentSessionId": "sess_owner"},
		),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}
	cs.seed(t, "c1", func(c *marotte.Chat) { c.RecordSession("sess_owner") })
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, "wf_1")
	leased(t, h.runs, "wf_2")

	h.runs.StopChatRuns(t.Context(), "c1")

	if got := callsOf(br, methodKiroWorkflowCancel); got != 2 {
		t.Errorf("the cancel went out %d times for 2 live runs, want 2", got)
	}
	if got := callsOf(br, methodKiroWorkflowList); got != 1 {
		t.Errorf("`workflow/list` went out %d times to stop 2 runs, want 1", got)
	}
}

// The first cancel rides the launching chat's resident session, before the close tears it down, so the run's
// lifecycle frames land on the lane they are routed to.
func TestStopChatRuns_TheFirstCancelRidesTheChatsResidentSession(t *testing.T) {
	setStopTiming(t, time.Hour, time.Hour)
	rec := &doorRecorder{results: map[string]json.RawMessage{
		methodKiroWorkflowList:    agentRunList(t, "running"),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}}
	cs := newTestChatStore()
	lane, _ := rec.factory().(*fakeBridge)
	h := newRuntimeOn(t.Context(), "/tmp/work", fakeEngineOf(rec.factory, lane), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	owner, ok := rec.factory().(*fakeBridge)
	if !ok {
		t.Fatal("Setup: the recorder handed back something other than a fake")
	}
	owner.sessionID = "sess_owner"
	cs.seed(t, "c1", func(c *marotte.Chat) { c.RecordSession("sess_owner") })
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: owner, state: bridgeIdle})
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", ParentSessionID: "sess_owner", Origin: runlease.OriginAgent})

	h.runs.StopChatRuns(t.Context(), "c1")

	if !slices.Contains(owner.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel did not ride the launching chat's resident session")
	}
	if fakes, calls := rec.sawCall(methodKiroWorkflowCancel); fakes != 1 || calls != 1 {
		t.Errorf("the cancel reached %d lane(s) in %d call(s), want exactly 1 and 1", fakes, calls)
	}
}

// A cancel the run reads back terminal is the stop confirmed: the mark goes and nothing is deleted.
func TestStopChatRuns_ACancelReadBackTerminalReleasesTheMark(t *testing.T) {
	setStopTiming(t, time.Hour, 10*time.Millisecond)
	h, cs, br := newStopRig(t)

	h.runs.StopChatRuns(t.Context(), "c1")

	if !holdsKey(t, cs, "c1", runKey) {
		t.Error("the mark went before the stop was confirmed")
	}
	if callsOf(br, methodKiroWorkflowCancel) == 0 {
		t.Fatal("no cancel went out for the closed chat's run")
	}
	br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusCancelled, ""))
	waitUntil(t, "the confirmed stop's release", func() bool { return !holdsKey(t, cs, "c1", runKey) })
	if callsOf(br, methodKiroWorkflowDelete) != 0 {
		t.Error("a run whose cancel was confirmed was deleted")
	}
}

// A cancel that never takes falls back to the delete once the window is spent, and the delete releases the mark.
func TestStopChatRuns_ARefusedCancelFallsBackToDeleteAfterTheWindow(t *testing.T) {
	const window = 150 * time.Millisecond
	setStopTiming(t, window, 20*time.Millisecond)
	h, cs, br := newStopRig(t)
	br.setCallErr(methodKiroWorkflowCancel, errors.New("owner pid 979344 is live"))

	start := time.Now()
	h.runs.StopChatRuns(t.Context(), "c1")

	waitUntil(t, "the delete fallback", func() bool { return callsOf(br, methodKiroWorkflowDelete) > 0 })
	if waited := time.Since(start); waited < window {
		t.Errorf("the delete went out %v after the close, before the %v window", waited, window)
	}
	if n := callsOf(br, methodKiroWorkflowCancel); n < 2 {
		t.Errorf("the cancel went out %d times in the window, want it retried", n)
	}
	waitUntil(t, "the deleted run's release", func() bool { return !holdsKey(t, cs, "c1", runKey) })
}

// A close no person made (an API caller's) stops nothing: the runs go on, and their marks keep the chat by time.
func TestCloseChatState_WithoutAPersonStopsNothing(t *testing.T) {
	setStopTiming(t, time.Hour, time.Hour)
	h, cs, br := newStopRig(t)

	h.CloseChatState(t.Context(), "c1", false)

	if n := callsOf(br, methodKiroWorkflowCancel) + callsOf(br, methodKiroWorkflowDelete); n != 0 {
		t.Errorf("a close no person made sent %d run stops, want 0", n)
	}
	if !holdsKey(t, cs, "c1", runKey) {
		t.Error("a close no person made released the run's mark")
	}
}

// A run none of whose launching chats has an open tab is not retried: a person's close stopped it.
func TestRefuseClosedLauncher_RefusesARunWhoseLaunchingChatIsClosed(t *testing.T) {
	h, _, _ := newTestHub()
	open := map[marotte.ChatID]bool{"c-open": true}
	h.coord.chatHasTab = func(id marotte.ChatID) bool { return open[id] }
	cases := []struct {
		name    string
		holders []marotte.ChatID
		refused bool
	}{
		{name: "closed_launcher", holders: []marotte.ChatID{"c-closed"}, refused: true},
		{name: "one_launcher_open", holders: []marotte.ChatID{"c-closed", "c-open"}},
		{name: "parentless_run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := h.runs.refuseClosedLauncher("wf_1", tc.holders)
			if tc.refused != (err != nil) {
				t.Fatalf("refuseClosedLauncher(%v) = %v, want refused %v", tc.holders, err, tc.refused)
			}
			if tc.refused {
				if code, _, _ := command.StatusOf(err); code != http.StatusConflict {
					t.Errorf("status = %d, want 409", code)
				}
			}
		})
	}
}
