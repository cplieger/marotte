package agent

// Is the chat RUNNING, and what is still coalescing in its open turns — the two facts
// the chat store's HTTP surface reads off the registry (chat.WithLiveTurn,
// chat.WithOpenTurns) because a log alone cannot state them: an open turn's tail is
// sealed per lane, so the entries a reader is watching stream are on no disk yet.

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestTurnLive_FalseOnAnIdleChat(t *testing.T) {
	h, _, _ := newTestHub()
	if h.TurnLive("c1") {
		t.Error("an idle chat reports a live turn; the record is final and should read as such")
	}
}

func TestTurnLive_TrueWhileATurnIsOpen(t *testing.T) {
	h, _, _ := newTestHub()
	id, _ := h.stagePromptTurn(t, "c1")
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", id) })

	if !h.TurnLive("c1") {
		t.Error("an open turn reports the record final, which is what makes the client " +
			"derive a terminal outcome for a turn that is running")
	}
	// Scoped to the chat asked about, not to "any chat is busy".
	if h.TurnLive("c2") {
		t.Error("an unrelated chat reports a live turn")
	}
}

// `turnFinalizing` counts as LIVE: the closer's turn_close and broadcast have not
// landed, so the record is still provisional at the moment a refetch is most likely
// to race it.
func TestTurnLive_TrueWhileFinalizing(t *testing.T) {
	h, _, _ := newTestHub()
	h.stagePromptTurn(t, "c1")

	// Claiming without finishing IS the window between a closer claiming and its effects landing.
	turn, won := h.coord.turns.claimOwn(t.Context(), "c1")
	if !won {
		t.Fatal("claimOwn lost the claim on a freshly opened turn")
	}
	if !h.TurnLive("c1") {
		t.Error("a finalizing turn reports the record final, so a refetch inside the " +
			"close window derives a verdict from a turn_close that has not landed")
	}
	h.coord.turns.finish(turn, marotte.TurnResult{})
	if h.TurnLive("c1") {
		t.Error("a finished turn still reports live")
	}
}

// What makes the read safe to call from HTTP: the GET answers for every chat a reader
// merely OPENS, and `lifecycleFor` creates a lifecycle on first use that only a bridge
// teardown or delete removes — so asking through it leaks an entry per chat read.
func TestTurnLive_RecordsNothingAboutTheChatItWasAskedAbout(t *testing.T) {
	h, _, _ := newTestHub()
	reg := h.coord.turns

	reg.mu.Lock()
	before := len(reg.chats)
	reg.mu.Unlock()

	for range 3 {
		if h.TurnLive("never-had-a-turn") {
			t.Fatal("a chat that never had a turn reports one live")
		}
		if tails := h.OpenTurns("never-had-a-turn"); len(tails) != 0 {
			t.Fatalf("a chat that never had a turn reports %d open turns", len(tails))
		}
	}

	reg.mu.Lock()
	after := len(reg.chats)
	_, minted := reg.chats["never-had-a-turn"]
	reg.mu.Unlock()

	if minted {
		t.Error("reading the state minted a lifecycle for the chat it was asked about; " +
			"an HTTP read path would then leave one per chat opened, dropped only by forget")
	}
	if after != before {
		t.Errorf("the registry grew from %d to %d entries across three reads", before, after)
	}
}

// An ADMITTED prompt is a turn in flight from every client's point of view: the prompt
// is persisted and broadcast and `thinking` is latched, so answering false made
// `live: false` mean two different things.
func TestTurnLive_TrueForAnAdmittedPromptWithNoTurnMinted(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Fatal("a fresh chat refused a prompt reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if !h.TurnLive("c1") {
		t.Error("a chat whose prompt is admitted but whose Turn is not minted reports no " +
			"live turn, so the client's heal clears `thinking` under a live prompt")
	}
}

// A shell reservation is held across the `!cmd` turn's open, and a shell turn emits
// no deltas, so the client's one-chunk recovery cannot reach it.
func TestTurnLive_TrueForAnAdmittedShellCommand(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourceLocalShell) {
		t.Fatal("a fresh chat refused a shell reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if !h.TurnLive("c1") {
		t.Error("a chat holding a shell reservation reports no live turn")
	}
}

// OpenTurns is the GET's open_entries: the own turn by id, carrying the text its
// lane is still coalescing, so a reader landing mid-reply sees the reply so far.
func TestOpenTurns_CarriesTheCoalescingTail(t *testing.T) {
	h, _, _ := newTestHub()
	id, log := streamingPromptTurn(t, h, "c1", "the reply so far")
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", id) })

	tails := h.OpenTurns("c1")
	if len(tails) != 1 || tails[0].ID != id {
		t.Fatalf("OpenTurns(c1) = %+v, want one tail for turn %q", tails, id)
	}
	texts := make([]string, 0, len(tails[0].Entries))
	for _, e := range tails[0].Entries {
		texts = append(texts, e.Text)
	}
	if !slices.Equal(texts, []string{"the reply so far"}) {
		t.Errorf("open entries = %q, want the unsealed text", texts)
	}
	if !log.Emitted() {
		t.Error("the accumulator reports nothing emitted after a delta folded into it")
	}
}

// The connect handshake's busy set and the GET's `live` answer ONE client question
// through two channels, so they are one predicate. Two spellings is how they come to
// disagree.
func TestTurnLive_AgreesWithTheConnectBusySet(t *testing.T) {
	cases := []struct {
		name  string
		setUp func(t *testing.T, h *Runtime)
	}{
		{"idle", func(*testing.T, *Runtime) {}},
		{"prompt turn", func(t *testing.T, h *Runtime) {
			id, _ := h.stagePromptTurn(t, "c1")
			t.Cleanup(func() { h.coord.ReleaseTurn("c1", id) })
		}},
		{"admitted prompt", func(t *testing.T, h *Runtime) {
			if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
				t.Fatal("a fresh chat refused a prompt reservation")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
		}},
		{"admitted shell command", func(t *testing.T, h *Runtime) {
			if !h.coord.TryReserveTurn("c1", marotte.TurnSourceLocalShell) {
				t.Fatal("a fresh chat refused a shell reservation")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			tc.setUp(t, h)

			busy := slices.Contains(h.coord.turns.busyChatIDs(), "c1")
			if got := h.TurnLive("c1"); got != busy {
				t.Errorf("TurnLive(c1) = %v, but busy_chats says %v; the handshake and "+
					"the transcript GET disagree about whether this chat is running", got, busy)
			}
		})
	}
}
