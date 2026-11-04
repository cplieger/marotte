package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
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
// run_start. Inspect answers wf_1 running until the test changes it. No chat has a tab. The hub shuts down before
// setStopTiming's restore, so no stop step reads the timing vars as they are put back.
func newStopRig(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.coord.chatHasTab = func(marotte.ChatID) bool { return false }
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

	h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

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

	h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

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

	h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

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
	h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

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

	h.CloseChatState(t.Context(), "c1", nil)

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

// A chat that holds a run's mark only because the run's owner scan could not read it did not launch the run, so its
// close stops nothing.
func TestStopChatRuns_ADoubtHoldersCloseStopsNothing(t *testing.T) {
	setStopTiming(t, 50*time.Millisecond, 10*time.Millisecond)
	h, cs, br := newTestHub()
	h.coord.chatHasTab = func(marotte.ChatID) bool { return false }
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "r", "status": "running", "parentSessionId": "sess_launcher",
		}),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusRunning, ""),
	}
	cs.seed(t, "c-launcher", func(c *marotte.Chat) { c.RecordSession("sess_launcher") })
	cs.seed(t, "c-bystander", func(c *marotte.Chat) { c.RecordSession("sess_bystander") })
	leaseRun(t, h, runlease.Lease{
		WorkflowID: "wf_1", ChatID: "c-launcher", ParentSessionID: "sess_launcher", Origin: runlease.OriginAgent,
		KeepChats: []string{"c-launcher", "c-bystander"},
	})

	h.runs.stopChatRuns(t.Context(), "c-bystander", &chat.Claim{})
	time.Sleep(100 * time.Millisecond)

	if n := callsOf(br, methodKiroWorkflowCancel) + callsOf(br, methodKiroWorkflowDelete); n != 0 {
		t.Errorf("closing a chat that only held the run's mark in doubt sent %d stops for a run c-launcher launched", n)
	}
}

// A leased run KAS already lists as terminal is not stopped: no cancel, no delete, and its mark ages out. A list
// that cannot be read, and a status that is not terminal (empty, or one KAS's enum lacks), still stop it.
func TestStopChatRuns_ALeasedRunListedFinishedIsNotStopped(t *testing.T) {
	cases := []struct {
		name    string
		list    func(t *testing.T, br *fakeBridge)
		stopped bool
	}{
		{name: "listed_completed", list: func(t *testing.T, br *fakeBridge) {
			br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
				"workflowId": "wf_1", "name": "r", "status": "completed", "parentSessionId": "sess_owner",
			}))
		}},
		{name: "list_unreadable", stopped: true, list: func(t *testing.T, br *fakeBridge) {
			br.setCallErr(methodKiroWorkflowList, errors.New("kas gone"))
		}},
		{name: "listed_status_empty", stopped: true, list: func(t *testing.T, br *fakeBridge) {
			br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
				"workflowId": "wf_1", "name": "r", "status": "", "parentSessionId": "sess_owner",
			}))
		}},
		{name: "listed_status_unknown", stopped: true, list: func(t *testing.T, br *fakeBridge) {
			br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
				"workflowId": "wf_1", "name": "r", "status": "queued", "parentSessionId": "sess_owner",
			}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const window = 80 * time.Millisecond
			setStopTiming(t, window, 10*time.Millisecond)
			h, cs, br := newStopRig(t)
			leaseRun(t, h, runlease.Lease{
				WorkflowID: "wf_1", ChatID: "c1", ParentSessionID: "sess_owner", Origin: runlease.OriginAgent,
				KeepChats: []string{"c1"},
			})
			tc.list(t, br)

			h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

			if tc.stopped {
				waitUntil(t, "the leased run's delete fallback", func() bool { return callsOf(br, methodKiroWorkflowDelete) > 0 })
				return
			}
			time.Sleep(2 * window)
			if n := callsOf(br, methodKiroWorkflowCancel) + callsOf(br, methodKiroWorkflowDelete); n != 0 {
				t.Errorf("a leased run KAS lists as completed got %d stops, want 0", n)
			}
			if !holdsKey(t, cs, "c1", runKey) {
				t.Error("a finished run's mark was released; it ages out on its own")
			}
		})
	}
}

// A close whose chat record cannot be read stops the runs whose leases name the chat: here the ctx of a nested
// close whose budget is spent, so the first cancel fails and the lifetime's retry sends it.
func TestStopChatRuns_AnUnreadableRecordStopsTheLeasedRuns(t *testing.T) {
	setStopTiming(t, time.Hour, 10*time.Millisecond)
	logs := captureLogs(t)
	h, _, br := newStopRig(t)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", Origin: runlease.OriginAgent})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	h.runs.stopChatRuns(ctx, "c1", &chat.Claim{})

	waitUntil(t, "the leased run's cancel", func() bool { return callsOf(br, methodKiroWorkflowCancel) > 0 })
	if want := "close: the chat could not be read; stopping only the runs its leases name"; !strings.Contains(logs.String(), want) {
		t.Errorf("the unreadable record was not reported; want %q in %s", want, logs.String())
	}
}

// A chat reopened inside the stop window drops the stop: no further cancel and no delete.
func TestStopChatRuns_AReopenedChatDropsTheStop(t *testing.T) {
	const window = 150 * time.Millisecond
	setStopTiming(t, window, 40*time.Millisecond)
	logs := captureLogs(t)
	h, _, br := newStopRig(t)
	var reopened atomic.Bool
	h.coord.chatHasTab = func(marotte.ChatID) bool { return reopened.Load() }
	br.setCallErr(methodKiroWorkflowCancel, errors.New("owner pid 979344 is live"))

	h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})
	reopened.Store(true)
	waitUntil(t, "the dropped stop", func() bool { return strings.Contains(logs.String(), "its run's stop is dropped") })
	time.Sleep(2 * window)

	if n := callsOf(br, methodKiroWorkflowDelete); n != 0 {
		t.Errorf("a reopened chat's run was deleted %d times", n)
	}
	if n := callsOf(br, methodKiroWorkflowCancel); n != 1 {
		t.Errorf("the cancel went out %d times after the chat was reopened, want only the close's first", n)
	}
}

// An inspect whose pauseDetail drifted in type still reads a terminal status as the stop confirmed.
func TestRunReadTerminal_KeepsTheStatusWhenAFieldDrifts(t *testing.T) {
	h, _, br := newTestHub()
	const drifted = `{"workflowId":"wf_1","state":{"workflowId":"wf_1","pauseDetail":"transient-error","status":"cancelled"}}`
	br.setCallResult(methodKiroWorkflowInspect, json.RawMessage(drifted))

	if !h.runs.runReadTerminal(t.Context(), "wf_1") {
		t.Error("a cancelled run whose pauseDetail is a string was read as not terminal")
	}
}

// A retry is refused when the run's launcher is closed, even while a chat its lease keeps only in doubt has a tab.
func TestRetryHolders_ADoubtHolderIsNotTheLauncher(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.chatHasTab = func(id marotte.ChatID) bool { return id == "c-doubt" }
	leaseRun(t, h, runlease.Lease{
		WorkflowID: "wf_1", ChatID: "c-closed", Origin: runlease.OriginAgent, KeepChats: []string{"c-closed", "c-doubt"},
	})

	holders, launchers, err := h.runs.retryHolders(t.Context(), "wf_1", &runOrigin{})
	if err != nil {
		t.Fatalf("retryHolders = %v", err)
	}
	if !slices.Contains(holders, "c-doubt") {
		t.Errorf("holders = %v: the doubt chat's mark is no longer written", holders)
	}
	err = h.runs.refuseClosedLauncher("wf_1", launchers)
	if code, _, _ := command.StatusOf(err); code != http.StatusConflict {
		t.Errorf("retry with the launcher closed and a doubt holder open = %v, want 409", err)
	}
}

// A leased run KAS's complete search no longer finds is stopped: no delete, no ERROR, and its mark is released.
func TestStopChatRuns_ALeasedRunKASNoLongerKnowsIsStopped(t *testing.T) {
	setStopTiming(t, 80*time.Millisecond, 10*time.Millisecond)
	logs := captureLogs(t)
	h, cs, br := newStopRig(t)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", Origin: runlease.OriginAgent, KeepChats: []string{"c1"}})
	br.setCallResult(methodKiroWorkflowList, kasRuns(t))
	br.setCallErr(methodKiroWorkflowInspect, workflowNotFoundErr())

	h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

	waitUntil(t, "the gone run's release", func() bool { return !holdsKey(t, cs, "c1", runKey) })
	time.Sleep(160 * time.Millisecond)
	if n := callsOf(br, methodKiroWorkflowDelete); n != 0 {
		t.Errorf("a run KAS no longer knows was deleted %d times", n)
	}
	if strings.Contains(logs.String(), "could not be stopped") {
		t.Errorf("a run KAS no longer knows was reported unstoppable: %s", logs.String())
	}
}

// holdRig is newStopRig with c2, an idle chat that launched nothing, a reopen switch for c1, and inspect replies by
// run once arrangeTwoRuns installs them.
type holdRig struct {
	h           *Runtime
	cs          *testChatStore
	br          *fakeBridge
	inspect     map[string]json.RawMessage
	mu          sync.Mutex
	wf2Inspects atomic.Int64
	reopened    atomic.Bool
}

// The close's stop hold keeps the chat until the last of its stops ends, whichever way that stop ends, and a close
// with nothing to stop ends it at once.
func TestStopChatRuns_TheStopHoldEndsWithTheLastStop(t *testing.T) {
	refuseCancel := func(_ *testing.T, r *holdRig) {
		r.br.setCallErr(methodKiroWorkflowCancel, errors.New("owner pid 979344 is live"))
	}
	cases := []struct {
		arrange func(t *testing.T, r *holdRig)
		// end drives the close's stops to their end; nil leaves them to end on their own.
		end    func(t *testing.T, r *holdRig)
		check  func(t *testing.T, r *holdRig)
		name   string
		chat   marotte.ChatID
		window time.Duration
		atOnce bool
	}{
		{name: "no_candidates", chat: "c2", atOnce: true},
		{name: "one_confirmed", chat: "c1", end: func(t *testing.T, r *holdRig) {
			r.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", marotte.RunStatusCancelled, ""))
		}},
		{name: "two_runs_one_confirmed", chat: "c1", arrange: arrangeTwoRuns, end: endTwoRunsInTurn},
		{
			name: "delete_fallback", chat: "c1", window: 200 * time.Millisecond, arrange: refuseCancel,
			check: func(t *testing.T, r *holdRig) {
				if holdsKey(t, r.cs, "c1", runKey) {
					t.Error("the deleted run's mark was not released")
				}
			},
		},
		{
			name: "delete_failed", chat: "c1", window: 200 * time.Millisecond,
			arrange: func(t *testing.T, r *holdRig) {
				refuseCancel(t, r)
				r.br.setCallErr(methodKiroWorkflowDelete, errors.New("kas gone"))
			},
			check: func(t *testing.T, r *holdRig) {
				if !holdsKey(t, r.cs, "c1", runKey) {
					t.Error("a run whose delete failed lost the mark that keeps its chat by time")
				}
			},
		},
		{name: "reopen_drop", chat: "c1", arrange: refuseCancel, end: func(_ *testing.T, r *holdRig) {
			r.reopened.Store(true)
		}, check: func(t *testing.T, r *holdRig) {
			if n := callsOf(r.br, methodKiroWorkflowDelete); n != 0 {
				t.Errorf("a reopened chat's run was deleted %d times", n)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window := tc.window
			if window == 0 {
				window = time.Hour
			}
			setStopTiming(t, window, 10*time.Millisecond)
			r := &holdRig{}
			r.h, r.cs, r.br = newStopRig(t)
			r.h.coord.chatHasTab = func(id marotte.ChatID) bool { return id == "c1" && r.reopened.Load() }
			r.cs.seed(t, "c2", func(c *marotte.Chat) { c.RecordSession("sess_idle") })
			if tc.arrange != nil {
				tc.arrange(t, r)
			}
			hold, _ := r.cs.SwapTabForStop(t.Context(), tc.chat)

			r.h.runs.stopChatRuns(t.Context(), tc.chat, &hold)

			if tc.atOnce {
				if holdsKey(t, r.cs, tc.chat, marotte.KeyStop) {
					t.Error("a close that stopped nothing kept its stop hold")
				}
				return
			}
			if !holdsKey(t, r.cs, tc.chat, marotte.KeyStop) {
				t.Fatal("the stop hold went before the close's stops ended")
			}
			if tc.end != nil {
				tc.end(t, r)
			}
			waitUntil(t, "the stop hold's release", func() bool { return !holdsKey(t, r.cs, tc.chat, marotte.KeyStop) })
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

// arrangeTwoRuns gives c1 a second running run, wf_2, and answers each run's inspect from wf_1's and wf_2's own
// status, both running.
func arrangeTwoRuns(t *testing.T, r *holdRig) {
	t.Helper()
	r.br.setCallResult(methodKiroWorkflowList, kasRuns(t,
		map[string]any{"workflowId": "wf_1", "name": "r", "status": "running", "parentSessionId": "sess_owner"},
		map[string]any{"workflowId": "wf_2", "name": "r", "status": "running", "parentSessionId": "sess_owner"},
	))
	r.h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_2", "workflowName": "r", "parentSessionId": "sess_owner",
	}))
	r.answerInspect(t, "wf_1", marotte.RunStatusRunning)
	r.answerInspect(t, "wf_2", marotte.RunStatusRunning)
	r.br.setOnCall(func(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, nil, false
		}
		id, _ := p[keyWorkflowID].(string)
		if id == "wf_2" {
			r.wf2Inspects.Add(1)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		reply, ok := r.inspect[id]
		return reply, nil, ok
	})
}

// endTwoRunsInTurn confirms wf_1's stop, checks the hold outlives it while wf_2 is still being stopped, then
// confirms wf_2's.
func endTwoRunsInTurn(t *testing.T, r *holdRig) {
	t.Helper()
	r.answerInspect(t, "wf_1", marotte.RunStatusCancelled)
	waitUntil(t, "wf_1's confirmed stop", func() bool { return !holdsKey(t, r.cs, "c1", runKey) })
	seen := r.wf2Inspects.Load()
	waitUntil(t, "two more of wf_2's read-backs", func() bool { return r.wf2Inspects.Load() >= seen+2 })
	if !holdsKey(t, r.cs, "c1", marotte.KeyStop) {
		t.Fatal("the stop hold went with the first of the close's two stops")
	}
	r.answerInspect(t, "wf_2", marotte.RunStatusCancelled)
}

func (r *holdRig) answerInspect(t *testing.T, workflowID string, status marotte.RunStatus) {
	t.Helper()
	reply := inspectReply(t, workflowID, status, "")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inspect == nil {
		r.inspect = map[string]json.RawMessage{}
	}
	r.inspect[workflowID] = reply
}
