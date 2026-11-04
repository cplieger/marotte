package agent

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// reserveNow takes c1's prompt reservation with a stamp taken now, failing the test unless it is acquired.
func reserveNow(t *testing.T, h *Runtime) command.Reservation {
	t.Helper()
	out, res := h.coord.ReserveTurnForPrompt(t.Context(), "c1", time.Second, h.coord.LifeStamp(), "")
	if out != command.AdmissionAcquired || res.Token == 0 {
		t.Fatalf("reserve = %v %+v, want acquired", out, res)
	}
	return res
}

// An old life's reservation released after a reopen leaves the reopened life's reservation held.
func TestCloseTab_AnEndedPromptsReleaseLeavesTheReopenedPromptsSlot(t *testing.T) {
	h, _, _, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	old := reserveNow(t, h)
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	reopenTab(t, h, "op-reopen")
	current := reserveNow(t, h)

	h.coord.ReleaseTurnReservation(old)
	if _, ok := h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp()); ok {
		t.Error("the old life's release freed the reopened life's slot")
	}
	h.coord.ReleaseTurnReservation(current)
	if _, ok := h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp()); !ok {
		t.Error("the reopened life's own release left its slot held")
	}
}

// A prompt that reaches its admission while a close is being decided waits; once the close commits it answers
// 409 chat_closed and opens no door.
func TestCloseTab_AReservationAfterTheHoldsWaitsAndRunsInTheReopenedChat(t *testing.T) {
	h, _, br, _ := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	doors := doorCounter(br)
	hold := h.HoldChat("c1")
	type reply struct {
		reason string
		code   int
	}
	prompted := make(chan reply, 1)
	go func() {
		code, reason := postPromptReply(t, h, "hi")
		prompted <- reply{code: code, reason: reason}
	}()
	select {
	case r := <-prompted:
		hold.Release()
		t.Fatalf("prompt = %d %q during the hold, want it waiting", r.code, r.reason)
	case <-time.After(200 * time.Millisecond):
	}
	hold.Commit()
	r := <-prompted
	hold.Release()

	if r.code != http.StatusConflict || r.reason != "chat_closed" {
		t.Errorf("prompt = %d %q, want 409 chat_closed", r.code, r.reason)
	}
	joinInflight(t, h)
	if n := doors.Load(); n != 0 {
		t.Errorf("doors = %d, want none for the closed chat", n)
	}
	if h.coord.turns.live("c1") {
		t.Error("a turn runs in the closed chat")
	}
}

// A close refused at its check commits nothing, so the prompt it parked runs in the same life.
func TestCloseTab_ARefusedCloseLetsItsParkedPromptRunInTheSameLife(t *testing.T) {
	h, _, br, _ := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	before := h.coord.turns.testLifecycle("c1")
	hold := h.HoldChat("c1")
	prompted := make(chan int, 1)
	go func() { prompted <- postPrompt(t, h, "hi") }()
	select {
	case code := <-prompted:
		hold.Release()
		t.Fatalf("prompt = %d during the hold, want it waiting", code)
	case <-time.After(200 * time.Millisecond):
	}
	hold.Release()
	if code := <-prompted; code != http.StatusOK {
		t.Fatalf("prompt = %d after the refused close, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)
	if after, ok := h.coord.turns.lookup("c1"); !ok || after != before {
		t.Error("the prompt ran in another life than the one the refused close left standing")
	}
	if before.endedNow() {
		t.Error("a refused close ended the life")
	}
}

// A close's drain after the teardown forgot the chat refuses and mints no lifecycle.
func TestDrain_AfterTheForgetMintsNoLifecycle(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	lc := h.coord.turns.testLifecycle("c1")
	lc.mu.Lock()
	fence := command.TurnFence{Epoch: lc.epoch, Seq: lc.nextSeq}
	lc.mu.Unlock()
	hold := h.HoldChat("c1")
	hold.Commit()
	h.coord.turns.forget("c1")
	hold.Release()

	if h.coord.tryReserveFencedNow("c1", marotte.TurnSourcePrompt, fence) {
		t.Error("the drain reserved in a forgotten chat")
	}
	if _, ok := h.coord.turns.lookup("c1"); ok {
		t.Error("the drain's reserve left a lifecycle for the forgotten chat")
	}
}

func TestReserveTurnForPrompt_AClosedSinceTheStampAnswersClosed(t *testing.T) {
	t.Run("a close committed since the stamp", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		stamp := h.coord.LifeStamp()
		hold := h.HoldChat("c1")
		hold.Commit()
		hold.Release()
		start := time.Now()
		out, res := h.coord.ReserveTurnForPrompt(t.Context(), "c1", time.Second, stamp, "")
		if out != command.AdmissionClosed || res.Token != 0 {
			t.Errorf("reserve = %v %+v, want AdmissionClosed with no reservation", out, res)
		}
		if took := time.Since(start); took > 500*time.Millisecond {
			t.Errorf("reserve answered after %v, want at once", took)
		}
	})
	t.Run("a hold standing past the wait", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		hold := h.HoldChat("c1")
		t.Cleanup(hold.Release)
		const wait = 50 * time.Millisecond
		start := time.Now()
		out, res := h.coord.ReserveTurnForPrompt(t.Context(), "c1", wait, h.coord.LifeStamp(), "")
		if out != command.AdmissionStarting || res.Token != 0 {
			t.Errorf("reserve = %v %+v, want AdmissionStarting with no reservation", out, res)
		}
		if took := time.Since(start); took < wait {
			t.Errorf("reserve answered after %v, want it to wait out its %v", took, wait)
		}
	})
}

// A try reservation while a close is held refuses at once instead of waiting for the close's job.
func TestTryReserve_DuringACloseRefusesAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		life bool
	}{
		{name: "a chat with a life", life: true},
		{name: "a chat with no lifecycle yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			if tc.life {
				h.coord.turns.testLifecycle("c1")
			}
			hold := h.HoldChat("c1")
			t.Cleanup(hold.Release)
			stamp := h.coord.LifeStamp()
			start := time.Now()
			_, idle := h.coord.TryReserveIdleTurn("c1", marotte.TurnSourceRevert, stamp)
			_, try := h.coord.turns.tryReserve("c1", marotte.TurnSourceEmptyRetry, stamp)
			if took := time.Since(start); took > 100*time.Millisecond {
				t.Errorf("the try reservations answered after %v, want at once", took)
			}
			if idle || try {
				t.Errorf("idle = %v, try = %v during a held close, want both refused", idle, try)
			}
			if _, ok := h.coord.turns.lookup("c1"); ok != tc.life {
				t.Errorf("lifecycle present = %v, want %v: a refused try mints none", ok, tc.life)
			}
		})
	}
}

// A drained prompt whose door opens with its reservation's stamp after the close is refused, whether the close's job
// still runs or the chat was reopened since, and opens no session.
func TestDrain_APromptParkedBeforeItsDoorAcrossTheCloseOpensNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reopen bool
	}{
		{name: "across the commit, the job still running"},
		{name: "across the job and a reopen", reopen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br, tab := closeTabHub(t)
			t.Cleanup(func() { shutdownHub(t, h) })
			doors := doorCounter(br)
			res := reserveNow(t, h)
			reached, release := parkTeardownAt(t, h, "begun")
			t.Cleanup(release)
			closed := make(chan int, 1)
			go func() { closed <- postCloseTab(t, h, tab) }()
			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("the close's teardown never ran")
			}
			if tc.reopen {
				release()
				awaitTeardown(t, h, "c1")
				reopenTab(t, h, "op-reopen")
			}

			_, err := h.coord.openBridgeAt(t.Context(), "c1", "", res.Stamp)
			if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
				t.Errorf("open at the old stamp = %v, want ErrChatClosedDuringCall", err)
			}
			if n := doors.Load(); n != 0 {
				t.Errorf("doors = %d, want none for the closed life", n)
			}
			release()
			if code := <-closed; code != http.StatusOK {
				t.Fatalf("close_tab = %d, want 200", code)
			}
			if tc.reopen {
				if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
					t.Errorf("the reopened chat's own open = %v, want it to open", err)
				}
			}
		})
	}
}

// A turn closing after the close committed and before its job's bridge close starts no tail: the parked model pick
// stays parked and no model apply reaches the session.
func TestCloseTab_ATurnClosingBeforeTheJobStartsNoTail(t *testing.T) {
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	holdPrompt(t, br)
	cancelGate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-cancelGate:
		default:
			close(cancelGate)
		}
	})
	br.mu.Lock()
	br.blockNotifyOn = map[string]chan struct{}{marotte.MethodCancel: cancelGate}
	br.mu.Unlock()
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	awaitWire(t, br, marotte.MethodPrompt)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.PendingModel = "next-model"
		return true
	}); err != nil {
		t.Fatalf("Setup: park a pick: %v", err)
	}

	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	for deadline := time.Now().Add(5 * time.Second); len(closedBroadcasts(t, h)) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the prompt's turn never closed after the commit cut its call")
		}
		time.Sleep(time.Millisecond)
	}
	if !h.coord.turns.gates.held("c1") {
		t.Fatal("Setup: the close's job returned before the turn closed")
	}
	time.Sleep(50 * time.Millisecond)
	if slices.Contains(br.callLog(), marotte.MethodSetConfigOption) {
		t.Errorf("calls = %v, want no model apply from a turn the close ended", br.callLog())
	}
	if rec, _ := cs.Get(t.Context(), "c1"); rec.PendingModel != "next-model" {
		t.Errorf("pending model = %q, want the pick still parked", rec.PendingModel)
	}
	close(cancelGate)
	awaitTeardown(t, h, "c1")
}

// A close held after a caller found its lifecycle still stops that caller's reservation: reserveLocked checks the
// gate under the lifecycle's lock, so a hold taken between the lookup and the reserve is not missed.
func TestReserveLocked_AHoldAfterTheLookupParksTheReservation(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	turns := h.coord.turns
	lc := turns.testLifecycle("c1")
	stamp := turns.gates.stamp()
	hold := h.HoldChat("c1")
	lc.mu.Lock()
	a := turns.reserveLocked(lc, marotte.TurnSourcePrompt, stamp, "")
	lc.mu.Unlock()
	if a.res.Token != 0 || a.verdict != gateWait || a.idle == nil {
		t.Errorf("reserve during a hold = %+v %v (idle %v), want no reservation and gateWait", a.res, a.verdict, a.idle != nil)
	}
	hold.Commit()
	turns.forget("c1")
	hold.Release()
	lc2 := turns.testLifecycle("c1")
	lc2.mu.Lock()
	a = turns.reserveLocked(lc2, marotte.TurnSourcePrompt, stamp, "")
	lc2.mu.Unlock()
	if a.res.Token != 0 || a.verdict != gateClosedSince {
		t.Errorf("reserve at a stamp from before the commit = %+v %v, want gateClosedSince", a.res, a.verdict)
	}
}
