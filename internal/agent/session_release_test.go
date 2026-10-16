package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
)

// releaseFixture is a runtime with a tab set whose chat c1 has an open, idle session and no tab.
func releaseFixture(t *testing.T) (*Runtime, *tabs.Store, *sharedBridge) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tab store: %v", err)
	}
	h := reopenFixtureIn(t, t.TempDir(), nil, WithTabs(st))
	settleBoot(t, h)
	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: OpenBridge(c1): %v", err)
	}
	return h, st, sb
}

// released reports whether the release check took c1's session, waiting up to 5 s for the close it started to drop
// the entry. The claim is synchronous, so a check that left the session alone reads unclaimed at once.
func released(t *testing.T, h *Runtime, sb *sharedBridge) bool {
	t.Helper()
	sb.mu.Lock()
	claimed := sb.closeClaim != nil
	sb.mu.Unlock()
	if !claimed && h.bridge.mgr.get("c1") == sb {
		return false
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if sessionClosed(fakeOf(sb)) && h.bridge.mgr.get("c1") == nil {
			return true
		}
	}
	t.Fatal("the release check claimed c1's session and did not close it within 5s")
	return false
}

func TestReleaseIfUnheld_ClosesOnlyChatsWithNoTabTurnOrLiveRun(t *testing.T) {
	for _, tc := range []struct {
		hold func(t *testing.T, h *Runtime, st *tabs.Store)
		name string
		want bool
	}{
		{name: "nothing holds it", want: true},
		{name: "an open tab", hold: func(t *testing.T, _ *Runtime, st *tabs.Store) {
			if _, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c1"}); err != nil {
				t.Fatalf("Setup: open tab: %v", err)
			}
		}},
		{name: "an admitted turn", hold: func(t *testing.T, h *Runtime, _ *tabs.Store) {
			if !h.coord.turns.tryReserveIdle("c1", marotte.TurnSourcePrompt) {
				t.Fatal("Setup: the admission slot was not free")
			}
		}},
		{name: "a live run it launched", hold: func(t *testing.T, h *Runtime, _ *tabs.Store) {
			h.runs.grantLease(t.Context(), "wf_1", "fix", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
		}},
		{name: "a run step it hosts", hold: func(_ *testing.T, h *Runtime, _ *tabs.Store) {
			h.bridge.mgr.hostsLiveRun = func(chatID marotte.ChatID) bool { return chatID == "c1" }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st, sb := releaseFixture(t)
			if tc.hold != nil {
				tc.hold(t, h, st)
			}

			h.coord.releaseIfUnheld("c1")

			if got := released(t, h, sb); got != tc.want {
				t.Errorf("released = %v, want %v", got, tc.want)
			}
		})
	}
}

// The release check runs where a hold ends: the chat's last turn or admission, and a run it launched ending.
func TestReleaseIfUnheld_RunsWhenTheTurnEndsAndWhenTheRunEnds(t *testing.T) {
	t.Run("the admission is released", func(t *testing.T) {
		h, _, sb := releaseFixture(t)
		if !h.coord.turns.tryReserveIdle("c1", marotte.TurnSourcePrompt) {
			t.Fatal("Setup: the admission slot was not free")
		}
		h.coord.releaseIfUnheld("c1")
		if released(t, h, sb) {
			t.Fatal("Setup: the session closed under an admitted turn")
		}

		h.coord.turns.releaseReservation("c1")

		if !released(t, h, sb) {
			t.Error("the chat's last admission ended and its unheld session stayed resident")
		}
	})
	t.Run("the run it launched ends", func(t *testing.T) {
		h, _, sb := releaseFixture(t)
		h.runs.grantLease(t.Context(), "wf_1", "fix", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())

		h.runs.releaseLease(t.Context(), "wf_1")

		if !released(t, h, sb) {
			t.Error("the chat's run ended and its unheld session stayed resident")
		}
	})
}

// A turn admitted after the release's turn check, while the claimed session is still open, waits for the close and
// takes a fresh generation instead of prompting on the session being closed.
func TestReleaseIfUnheld_AnOpenDuringTheClaimWaitsAndTakesAFreshGeneration(t *testing.T) {
	h, _, sb := releaseFixture(t)
	old := fakeOf(sb)
	gate := make(chan struct{})
	old.mu.Lock()
	old.stopGate = gate
	old.mu.Unlock()

	h.coord.releaseIfUnheld("c1")
	got := make(chan *sharedBridge, 1)
	go func() {
		next, err := h.coord.openBridge(context.Background(), "c1", "")
		if err != nil {
			t.Errorf("OpenBridge(c1) during the release: %v", err)
		}
		got <- next
	}()
	select {
	case next := <-got:
		t.Fatalf("an open answered while the release's close was held (same entry: %v)", next == sb)
	case <-time.After(300 * time.Millisecond):
	}
	close(gate)

	var next *sharedBridge
	select {
	case next = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the open did not answer within 5s of the release's close landing")
	}
	if next == nil || next == sb {
		t.Fatal("the open handed out the entry the release closed")
	}
	if !sessionClosed(old) {
		t.Error("the released session was not closed")
	}
	if s := fakeOf(next); s == nil || s == old || !s.Open() {
		t.Error("the open's generation holds no fresh open session")
	}
}

// A chat-parented run's step sessions are KAS's: the release check closes only the chat's own session.
func TestReleaseIfUnheld_NeverClosesAStepSession(t *testing.T) {
	h, _, sb := releaseFixture(t)
	h.runs.grantLease(t.Context(), "wf_1", "fix", launchOrigin{origin: runlease.OriginAgent, chatID: "c1"}, hostSec())
	h.runs.recordParentSession(t.Context(), "wf_1", string(sb.SessionID()))
	h.runs.router.registerOwner("wf_1", sb.SessionID())
	h.runs.router.seedSteps("wf_1", []string{"sess_step"})

	h.runs.releaseLease(t.Context(), "wf_1")

	if !released(t, h, sb) {
		t.Fatal("Setup: the chat's own session was not released")
	}
	if stepCloseRequested(fakeSourceOf(t, h).lane, "sess_step") {
		t.Error("the release check closed the run's step session")
	}
	if fakeOf(sb).SessionID() == "sess_step" {
		t.Error("the chat's handle names the step session")
	}
}
