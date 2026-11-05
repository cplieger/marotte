package agent

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// switchFailures counts the switch_failed frames broadcast since before.
func switchFailures(t *testing.T, h *Runtime, before uint64) int {
	t.Helper()
	n := 0
	for _, p := range errorPayloadsSince(t, h, before) {
		if p.Code == marotte.ErrCodeSwitchFailed {
			n++
		}
	}
	return n
}

func postSwitchModel(t *testing.T, h *Runtime, model string) int {
	t.Helper()
	return postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSwitchModel, ChatID: "c1", Payload: json.RawMessage(`{"model":"` + model + `"}`),
	}).Code
}

// pendingModel is c1's parked pick.
func pendingModel(t *testing.T, cs *testChatStore) string {
	t.Helper()
	c, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("c1's record is gone")
	}
	return c.PendingModel
}

// reopenAndOpen reopens c1's tab and opens its session on a fresh fake, answering the record once the door ran.
func reopenAndOpen(t *testing.T, h *Runtime, cs *testChatStore) *marotte.Chat {
	t.Helper()
	next := newFakeBridge()
	fakeSourceOf(t, h).setFactory(func() engineSession { return next })
	reopenTab(t, h, "op-reopen")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open after the reopen: %v", err)
	}
	c, _ := cs.Get(t.Context(), "c1")
	return c
}

// A model pick whose apply meets the chat's ended life keeps the pick parked with no failure frame, and the
// reopened chat's door applies it; a swap kiro-cli refused while the life is live still clears it.
func TestSwitchModel_AnApplyOnAnEndedLifeKeepsThePick(t *testing.T) {
	t.Run("after the commit", func(t *testing.T) {
		h, cs, br, tab := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open session: %v", err)
		}
		reached, release := parkTeardownAt(t, h, "begun")
		t.Cleanup(release)
		closed := make(chan int, 1)
		go func() { closed <- postCloseTab(t, h, tab) }()
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			t.Fatal("the teardown never began")
		}
		before := h.bus.fanout.Position().Head
		br.forgetCalls()

		if code := postSwitchModel(t, h, "m-new"); code != http.StatusOK {
			t.Fatalf("switch_model = %d, want 200", code)
		}

		if calls := br.callLog(); countOf(calls, marotte.MethodSetConfigOption) != 0 {
			t.Errorf("calls = %v, want no set_config_option on the closing session", calls)
		}
		if got := pendingModel(t, cs); got != "m-new" {
			t.Errorf("pending_model = %q, want m-new parked", got)
		}
		if n := switchFailures(t, h, before); n != 0 {
			t.Errorf("switch_failed frames = %d, want none", n)
		}
		release()
		if code := <-closed; code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		awaitTeardown(t, h, "c1")
		if c := reopenAndOpen(t, h, cs); c.Model != "m-new" || c.PendingModel != "" {
			t.Errorf("after the reopen model = %q pending = %q, want m-new applied", c.Model, c.PendingModel)
		}
	})
	t.Run("during set_config_option", func(t *testing.T) {
		h, cs, br, tab := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open session: %v", err)
		}
		gate := make(chan struct{})
		t.Cleanup(func() { close(gate) })
		br.mu.Lock()
		br.setModelGate = gate
		br.mu.Unlock()
		br.forgetCalls()
		before := h.bus.fanout.Position().Head
		switched := make(chan int, 1)
		go func() { switched <- postSwitchModel(t, h, "m-new") }()
		for deadline := time.Now().Add(5 * time.Second); countOf(br.callLog(), marotte.MethodSetConfigOption) == 0; {
			if time.Now().After(deadline) {
				t.Fatal("the swap never reached set_config_option")
			}
			time.Sleep(time.Millisecond)
		}

		if code := postCloseTab(t, h, tab); code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		select {
		case code := <-switched:
			if code != http.StatusOK {
				t.Fatalf("switch_model = %d, want 200", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the close never ended the swap's set_config_option")
		}
		awaitTeardown(t, h, "c1")

		if got := pendingModel(t, cs); got != "m-new" {
			t.Errorf("pending_model = %q, want m-new kept for the next open", got)
		}
		if n := switchFailures(t, h, before); n != 0 {
			t.Errorf("switch_failed frames = %d, want none", n)
		}
		if c := reopenAndOpen(t, h, cs); c.Model != "m-new" || c.PendingModel != "" {
			t.Errorf("after the reopen model = %q pending = %q, want m-new applied", c.Model, c.PendingModel)
		}
	})
	t.Run("kiro-cli refused the swap", func(t *testing.T) {
		h, cs, br, _ := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open session: %v", err)
		}
		br.mu.Lock()
		br.setModelFailures = 1
		br.mu.Unlock()
		before := h.bus.fanout.Position().Head

		if code := postSwitchModel(t, h, "m-new"); code != http.StatusOK {
			t.Fatalf("switch_model = %d, want 200", code)
		}

		if got := pendingModel(t, cs); got != "" {
			t.Errorf("pending_model = %q, want the refused pick cleared", got)
		}
		if n := switchFailures(t, h, before); n != 1 {
			t.Errorf("switch_failed frames = %d, want one", n)
		}
	})
}

// A closed life's tail parked inside its model apply is cut by the close, and the reopened chat is the next life's
// alone: the pick stays parked with no failure frame, the reopen's door applies it, and the old tail sends nothing
// to the new session.
func TestCloseTab_AReopenWaitsForTheClosedLifesTail(t *testing.T) {
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	release := holdPrompt(t, br)
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel = "m-new"
		return true
	}); err != nil {
		t.Fatalf("Setup: park a pick: %v", err)
	}
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	br.mu.Lock()
	br.setModelGate = gate
	br.mu.Unlock()
	before := h.bus.fanout.Position().Head
	release()
	for deadline := time.Now().Add(5 * time.Second); countOf(br.callLog(), marotte.MethodSetConfigOption) == 0; {
		if time.Now().After(deadline) {
			t.Fatalf("calls = %v, want the turn's tail inside its model apply", br.callLog())
		}
		time.Sleep(time.Millisecond)
	}

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	if got := pendingModel(t, cs); got != "m-new" {
		t.Errorf("pending_model after the close = %q, want m-new kept", got)
	}
	next := newFakeBridge()
	fakeSourceOf(t, h).setFactory(func() engineSession { return next })
	reopenTab(t, h, "op-reopen")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open after the reopen: %v", err)
	}

	if c, _ := cs.Get(t.Context(), "c1"); c.Model != "m-new" || c.PendingModel != "" {
		t.Errorf("after the reopen model = %q pending = %q, want m-new applied by the door", c.Model, c.PendingModel)
	}
	if calls := next.callLog(); countOf(calls, marotte.MethodSetConfigOption) != 0 {
		t.Errorf("new session calls = %v, want no set_config_option from the old tail", calls)
	}
	if n := switchFailures(t, h, before); n != 0 {
		t.Errorf("switch_failed frames = %d, want none", n)
	}
}
