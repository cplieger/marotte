package agent

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// clearCauseHub is chat c1 with an open tab and session, mid-turn, holding one workflow message KAS buffered. Each
// session/cancel written runs onCancel with clear, which folds the steering_cleared naming that message, as
// cancelLocalTurn emits it.
func clearCauseHub(t *testing.T, onCancel func(clear func())) (*Runtime, *testChatStore, marotte.TabSubject) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tab store: %v", err)
	}
	h, cs, br := newTestHub(WithTabs(st))
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	opened, err := h.Membership().OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"}, "op-open")
	if err != nil {
		t.Fatalf("Setup: open tab: %v", err)
	}
	at := h.originOf("c1")
	id, _ := h.stagePromptTurn(t, "c1")
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", id) })
	h.translateACPEvent("c1", at, newTurnStartMsg())
	h.translateACPEvent("c1", at, runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": string(br.SessionID()), "message": `paused: repeat "fix-loop" reached its iteration limit`,
		"severity": "info", "sender": "step", "workflowId": "wf_1", "nodeId": "fix-loop", "notifyId": "notify-cap",
	}))
	joinInflight(t, h)
	br.mu.Lock()
	br.onNotify = func(method string) {
		if method == marotte.MethodCancel {
			onCancel(func() { h.translateACPEvent("c1", at, clearedMsg("notify-cap")) })
		}
	}
	br.mu.Unlock()
	return h, cs, opened.Subject
}

// clearCausesOf is the cause of every steer_cleared in c1's log.
func clearCausesOf(t *testing.T, cs *testChatStore) []marotte.SteerClearCause {
	t.Helper()
	var out []marotte.SteerClearCause
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind == marotte.EntryKindSteerCleared {
			var c marotte.EntrySteerCleared
			decodePayload(t, e, &c)
			out = append(out, c.Cause)
		}
	}
	return out
}

// clearedOnce reports one settlement, cleared.
func clearedOnce(d []marotte.EntrySteerDelivered) bool {
	return len(d) == 1 && d[0].Cleared
}

// settlementsOf is every steer_delivered in c1's log.
func settlementsOf(t *testing.T, cs *testChatStore) []marotte.EntrySteerDelivered {
	t.Helper()
	var out []marotte.EntrySteerDelivered
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind == marotte.EntryKindSteerDelivered {
			var d marotte.EntrySteerDelivered
			decodePayload(t, e, &d)
			out = append(out, d)
		}
	}
	return out
}

// The clear inside a stop's session/cancel records who stopped: the reader's Stop is a cancel, after which KAS
// re-prompts the message; a tab close's cancel is a boundary, since the session/close aborts that re-delivery. Neither
// frame says which message KAS re-delivered, so the message reads cleared either way.
func TestWorkflowMessage_AStopsClearRecordsWhoStopped(t *testing.T) {
	for _, tc := range []struct {
		stop      func(t *testing.T, h *Runtime, tab marotte.TabSubject)
		name      string
		wantCause marotte.SteerClearCause
	}{
		{name: "the reader's stop", wantCause: marotte.SteerClearCauseCancel, stop: func(t *testing.T, h *Runtime, _ marotte.TabSubject) {
			if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
				t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
			}
		}},
		{name: "a tab close", wantCause: marotte.SteerClearCauseBoundary, stop: func(t *testing.T, h *Runtime, tab marotte.TabSubject) {
			payload, _ := json.Marshal(marotte.CloseTabCommand{ID: tab.ID})
			if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCloseTab, Payload: payload}); rec.Code != http.StatusOK {
				t.Fatalf("close_tab = %d %s, want 200", rec.Code, rec.Body.String())
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, tab := clearCauseHub(t, func(clear func()) { clear() })
			if d := settlementsOf(t, cs); len(d) != 0 {
				t.Fatalf("Setup: settlements before the stop = %+v, want the message waiting", d)
			}

			tc.stop(t, h, tab)

			if c := clearCausesOf(t, cs); len(c) != 1 || c[0] != tc.wantCause {
				t.Errorf("clear causes = %v, want [%s]", c, tc.wantCause)
			}
			if d := settlementsOf(t, cs); !clearedOnce(d) {
				t.Errorf("settlements = %+v, want one cleared", d)
			}
		})
	}
}

// A reader's Stop landing between a tab close's session/cancel and the clear that cancel produced leaves the clear the
// teardown's: two clients can issue both at once, and the close still aborts KAS's re-delivery.
func TestWorkflowMessage_ATeardownsClearKeepsItsCauseWhenAReadersStopOverlaps(t *testing.T) {
	teardownCancelWritten := make(chan struct{})
	readerCancelWritten := make(chan struct{})
	var cancels atomic.Int32
	h, cs, tab := clearCauseHub(t, func(clear func()) {
		switch cancels.Add(1) {
		case 1:
			close(teardownCancelWritten)
			// Off the test goroutine, so no Fatal: a missing reader's cancel falls through to the
			// clear, and the cause assertion names what it recorded.
			select {
			case <-readerCancelWritten:
			case <-time.After(5 * time.Second):
			}
			clear()
		case 2:
			close(readerCancelWritten)
		}
	})

	closed := make(chan int, 1)
	go func() {
		payload, _ := json.Marshal(marotte.CloseTabCommand{ID: tab.ID})
		closed <- postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCloseTab, Payload: payload}).Code
	}()
	awaitClosed(t, teardownCancelWritten, "the tab close's session/cancel")
	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("reader's cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	select {
	case code := <-closed:
		if code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close_tab did not answer within 5s")
	}

	if c := clearCausesOf(t, cs); len(c) != 1 || c[0] != marotte.SteerClearCauseBoundary {
		t.Errorf("clear causes = %v, want [boundary]: the clear came from the tab close's cancel", c)
	}
}

// An error clear KAS emitted before the reader pressed Stop but folded after it records the reader's cancel, since
// the frame carries no cause; the message still reads cleared, so the recorded cause decides nothing shown.
func TestWorkflowMessage_AnErrorClearFoldedAfterTheReadersStopReadsCleared(t *testing.T) {
	h, cs, _ := clearCauseHub(t, func(func()) {})

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if d := settlementsOf(t, cs); len(d) != 0 {
		t.Fatalf("Setup: settlements before the clear = %+v, want none: the error clear is still queued", d)
	}
	h.translateACPEvent("c1", h.originOf("c1"), clearedMsg("notify-cap"))
	joinInflight(t, h)

	if d := settlementsOf(t, cs); !clearedOnce(d) {
		t.Errorf("settlements = %+v, want one cleared", d)
	}
}
