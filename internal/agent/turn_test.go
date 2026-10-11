package agent

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

func openTestTurn(t *testing.T, reg *turnRegistry, chatID marotte.ChatID) *activeTurn {
	t.Helper()
	id := "t-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	lc := reg.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !lc.slotFreeLocked(marotte.TurnSourcePrompt) {
		t.Fatalf("open(%q) was refused: the prompt slot is held", chatID)
	}
	return lc.openLocked(chatID, &marotte.Entry{ID: id}, marotte.TurnSourcePrompt, "", turnlog.Open(id, nil))
}

// TestTurnRegistry_InterruptIsFirstWinsPerTurn pins that first-wins because Cancel and kiro-cli's tool-use filter both write
// the cause; id-scoped because on the bridge turn A's cause once survived into turn B.
func TestTurnRegistry_InterruptIsFirstWinsPerTurn(t *testing.T) {
	reg := newTurnRegistry()
	turn := openTestTurn(t, reg, "c1")

	if !reg.interrupt("c1", turn.ID, "the filter stopped it") {
		t.Fatal("the first cause on an open turn must be taken")
	}
	if reg.interrupt("c1", turn.ID, "a later cause") {
		t.Error("a second cause was taken; the turn's cause is already decided")
	}
	if got := reg.interruptCause(turn); got != "the filter stopped it" {
		t.Errorf("cause = %q, want the FIRST one to win", got)
	}

	// The old id names a finished turn; its cause must not reach the new one.
	reg.finish(turn, marotte.TurnResult{})
	next := openTestTurn(t, reg, "c1")
	if reg.interrupt("c1", turn.ID, "stale cause") {
		t.Error("a cause for a finished turn was accepted")
	}
	if got := reg.interruptCause(next); got != "" {
		t.Errorf("the new turn carries %q; a previous turn's cause must not survive into it", got)
	}
}

// TestTurnRegistry_ClaimIsFirstWins pins that two closers persist and announce one turn once. The loser waits, observing the
// winner's unlocked persistence and broadcast rather than a half-finalized turn.
func TestTurnRegistry_ClaimIsFirstWins(t *testing.T) {
	reg := newTurnRegistry()
	openTestTurn(t, reg, "c1")

	turn, won := reg.claimOwn(t.Context(), "c1")
	if !won {
		t.Fatal("claiming an open turn must be taken")
	}

	second := make(chan bool, 1)
	go func() {
		_, ok := reg.claimOwn(context.WithoutCancel(t.Context()), "c1")
		second <- ok
	}()
	select {
	case <-second:
		t.Fatal("a second closer resolved while the first was still finalizing")
	case <-time.After(20 * time.Millisecond):
	}

	reg.finish(turn, marotte.TurnResult{})
	select {
	case ok := <-second:
		if ok {
			t.Error("a second closer claimed the same turn; its effects would run twice")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second closer never resolved after the finalize completed")
	}
}

// TestTurnRegistry_FinalizeWakesAParkedOpen pins that the waitable closes on every state change; armed only on folds, a
// finalize woke nobody and the next prompt parked forever.
func TestTurnRegistry_FinalizeWakesAParkedOpen(t *testing.T) {
	reg := newTurnRegistry()
	first := openTestTurn(t, reg, "c1")
	if _, won := reg.claimOwn(t.Context(), "c1"); !won {
		t.Fatal("claiming the open turn must be taken")
	}

	opened := make(chan *activeTurn, 1)
	go func() {
		var next *activeTurn
		err := reg.withLifecycle(context.WithoutCancel(t.Context()), "c1", func(lc *chatLifecycle) error {
			next = lc.openLocked("c1", &marotte.Entry{ID: "t-next"}, marotte.TurnSourcePrompt, "", turnlog.Open("t-next", nil))
			return nil
		})
		if err != nil {
			next = nil
		}
		opened <- next
	}()

	// The next turn stays unobservable until the previous one's persistence and broadcast complete.
	select {
	case <-opened:
		t.Fatal("an open proceeded while the chat was finalizing")
	case <-time.After(20 * time.Millisecond):
	}

	reg.finish(first, marotte.TurnResult{})
	select {
	case turn := <-opened:
		if turn == nil {
			t.Fatal("the woken open was refused")
		}
		if turn.Seq <= first.Seq {
			t.Errorf("the woken open took seq %d after seq %d; the open sequence must advance", turn.Seq, first.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a finalize did not wake the parked open; the next prompt never reaches KAS")
	}
}

// TestTurnRegistry_OpenIsCancellable pins that a channel, unlike sync.Cond, lets a waiter give up on a wedged chat.
func TestTurnRegistry_OpenIsCancellable(t *testing.T) {
	reg := newTurnRegistry()
	openTestTurn(t, reg, "c1")
	if _, won := reg.claimOwn(t.Context(), "c1"); !won {
		t.Fatal("claiming the open turn must be taken")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ran := false
	err := reg.withLifecycle(ctx, "c1", func(*chatLifecycle) error {
		ran = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("withLifecycle on a cancelled ctx = %v, want context.Canceled", err)
	}
	if ran {
		t.Error("the open body ran on a cancelled context while the chat was finalizing")
	}
}

// TestTurnRegistry_ForgetDropsTheChat pins that a deleted chat's lifecycle must not be retained for the process's life.
func TestTurnRegistry_ForgetDropsTheChat(t *testing.T) {
	reg := newTurnRegistry()
	openTestTurn(t, reg, "c1")

	reg.forget("c1")

	if reg.live("c1") {
		t.Error("a forgotten chat still reports a live turn")
	}
	reg.mu.Lock()
	n := len(reg.chats)
	reg.mu.Unlock()
	if n != 0 {
		t.Errorf("registry holds %d lifecycles after forget, want 0", n)
	}
}

// A real persist failure has no store-side seam.
type failingUsageStore struct {
	bridgeChatRecords
	err error
}

func (s failingUsageStore) Mutate(context.Context, marotte.ChatID, func(*marotte.Chat, bool) bool) (string, error) {
	return "", s.err
}

// TestMutateUsage_TombstonedRefusalIsNotAnError pins that ErrTombstoned means the write was declined for a deleted chat, and
// metering lands once per turn, so logging it as an error would fire on the mechanism working.
func TestMutateUsage_TombstonedRefusalIsNotAnError(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.seed(t, "c1", nil)
	if _, err := cs.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Delete(c1) = %v", err)
	}
	logs := captureLogs(t)

	h.coord.AccumulateSpend(t.Context(), "c1", 0.5)
	h.coord.StageConversationTurnSummary(t.Context(), "c1", 1200)

	if out := logs.String(); strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("a tombstoned metering write logged an error: %s", out)
	}
	if _, ok := cs.Get(t.Context(), "c1"); ok {
		t.Error("a metering write resurrected the deleted chat")
	}
}

// TestMutateUsage_OtherErrorsStillLog pins that matching the sentinel must not swallow a real persist failure.
func TestMutateUsage_OtherErrorsStillLog(t *testing.T) {
	bc := &bridgeCoordinator{chatStore: failingUsageStore{err: errors.New("disk full")}, turns: newTurnRegistry()}
	logs := captureLogs(t)

	bc.AccumulateSpend(t.Context(), "c1", 0.5)

	if out := logs.String(); !strings.Contains(out, "persist turn metering") {
		t.Errorf("a real metering write failure was swallowed: %s", out)
	}
}

// A finish after forget settles the turn's OWN lifecycle: re-resolving a fresh one would leave the original in
// turnFinalizing and park Forward forever.
func TestForget_DoesNotStrandAnInFlightFinalizeOnAnotherLifecycle(t *testing.T) {
	r := newTurnRegistry()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"

	lc := r.lifecycleFor(chatID)
	openTestTurn(t, r, chatID)
	claimed, won := r.claimOwn(ctx, chatID)
	if !won {
		t.Fatal("claimOwn lost the claim on a freshly opened turn")
	}

	r.forget(chatID)

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	woken := make(chan bool, 1)
	go func() {
		ok := lc.awaitNotFinalizing(waitCtx)
		if ok {
			lc.mu.Unlock()
		}
		woken <- ok
	}()

	r.finish(claimed, marotte.TurnResult{Stop: marotte.StopReasonEndTurn})

	if !<-woken {
		t.Fatal("the waiter on the forgotten lifecycle was never woken, so Forward is parked " +
			"on a channel nothing will close")
	}
	lc.mu.Lock()
	state, own := lc.state, lc.own
	lc.mu.Unlock()
	if state != turnIdle || own != nil {
		t.Errorf("the forgotten lifecycle is state %v with own %v, want idle and nil: the finalize "+
			"published somewhere else", state, own)
	}
}

// busyChatIDs covers an own turn, a pending turn owed its bracket and a held admission slot: the set a stale
// thinking retraction is withheld from, shared by the handshake and the transcript GET.
func TestBusyChatIDs_NamesOnlyTheChatsOwnTurns(t *testing.T) {
	busySet := func(t *testing.T, r *turnRegistry) map[marotte.ChatID]bool {
		t.Helper()
		out := map[marotte.ChatID]bool{}
		for _, id := range r.busyChatIDs() {
			out[id] = true
		}
		return out
	}

	t.Run("a prompt turn is busy", func(t *testing.T) {
		h, _, _ := newTestHub()
		id, _ := h.stagePromptTurn(t, "c1")
		t.Cleanup(func() { h.coord.ReleaseTurn("c1", id) })
		if !busySet(t, h.coord.turns)["c1"] {
			t.Error("a chat running its own prompt turn is absent from busy_chats, so a " +
				"reconnect would retract `thinking` under a live turn")
		}
	})

	t.Run("a prompt-class reservation with no Turn minted is busy", func(t *testing.T) {
		h, _, _ := newTestHub()
		if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
			t.Fatal("a fresh chat refused a prompt reservation")
		}
		t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
		if !busySet(t, h.coord.turns)["c1"] {
			t.Error("a chat whose prompt is admitted but whose Turn is not minted is absent " +
				"from busy_chats: the negative statement is incomplete for the admission window")
		}
	})

	// ReleaseTurn leaves the turn open, so it must be finalized for the chat to be idle.
	t.Run("a settled chat is not busy", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.stagePromptTurn(t, "c1")
		turn, won := h.coord.turns.claimOwn(t.Context(), "c1")
		if !won {
			t.Fatal("claimOwn lost the claim on a freshly opened turn")
		}
		h.coord.turns.finish(turn, marotte.TurnResult{})
		if busySet(t, h.coord.turns)["c1"] {
			t.Error("a settled chat is still named busy, so the client never retracts")
		}
	})
}

// A shell reservation reaches this set through the same predicate.
func TestBusyChatIDs_NamesAnAdmittedShellCommand(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourceLocalShell) {
		t.Fatal("a fresh chat refused a shell reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if !slices.Contains(h.coord.turns.busyChatIDs(), "c1") {
		t.Error("a chat holding a shell reservation is absent from busy_chats")
	}
}

// TestTurnRegistry_StoppedAfterIsKeyedOnTheOpenSequence pins that a stop belongs to every turn open when it arrived, and it
// outlives the finalize while a handle is held.
func TestTurnRegistry_StoppedAfterIsKeyedOnTheOpenSequence(t *testing.T) {
	h, _, _ := newTestHub()
	a, _ := h.stagePromptTurn(t, "c1")

	h.coord.RequestStop("c1")
	endTurn(t, h, "c1", a)

	if !h.coord.StopRequestedAfter("c1", a) {
		t.Error("StopRequestedAfter(a) = false after a stop and a finalize with its handle held, want true")
	}
	b, _ := h.stagePromptTurn(t, "c1")
	if h.coord.StopRequestedAfter("c1", b) {
		t.Error("StopRequestedAfter(b) = true for a turn opened after the stop, want false")
	}
	if !h.coord.StopRequestedAfter("c1", a) {
		t.Error("StopRequestedAfter(a) = false once a later turn opened, want true")
	}

	h.coord.RequestStop("c-never-opened")
	if _, ok := h.coord.turns.lookup("c-never-opened"); ok {
		t.Error("a stop on a chat with no turns minted a lifecycle for it")
	}
}
