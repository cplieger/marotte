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

// openTestTurn records a prompt turn on reg's lifecycle for chatID, at the registry
// alone: no store append, because these tests are about the registry's own state
// machine. The id is minted per call so two opens on one chat never collide.
func openTestTurn(t *testing.T, reg *turnRegistry, chatID marotte.ChatID) *Turn {
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

// TestTurnRegistry_InterruptIsFirstWinsPerTurn pins both guards on the cause a turn
// ends with. FIRST-WINS because two writers reach one turn — a user pressing Cancel
// and kiro-cli's tool-use filter — and neither may relabel the other. ID-SCOPED
// because a cause offered for a finished turn must land nowhere; while it lived on
// the bridge, turn A's cause survived into turn B.
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

	// The turn ends and another opens. The old id names a turn that is over, so a
	// cause armed for it must not reach the new one.
	reg.finish(turn, marotte.TurnResult{})
	next := openTestTurn(t, reg, "c1")
	if reg.interrupt("c1", turn.ID, "stale cause") {
		t.Error("a cause for a finished turn was accepted")
	}
	if got := reg.interruptCause(next); got != "" {
		t.Errorf("the new turn carries %q; a previous turn's cause must not survive into it", got)
	}
}

// TestTurnRegistry_ClaimIsFirstWins pins the exclusion the finalizer rests on: two
// closers reaching one turn persist and announce it once. The loser WAITS rather than
// being refused — the winner's persistence and broadcast run with no lock held, so the
// loser observes the result of that work rather than a half-finalized turn.
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

// TestTurnRegistry_FinalizeWakesAParkedOpen is the normal-path deadlock guard, and the
// reason the waitable is a channel closed on EVERY state change: armed only where a
// frame folds, a finalize woke nobody and the user's next prompt parked in its open
// forever. withLifecycle is the one door an open goes through.
func TestTurnRegistry_FinalizeWakesAParkedOpen(t *testing.T) {
	reg := newTurnRegistry()
	first := openTestTurn(t, reg, "c1")
	if _, won := reg.claimOwn(t.Context(), "c1"); !won {
		t.Fatal("claiming the open turn must be taken")
	}

	opened := make(chan *Turn, 1)
	go func() {
		var next *Turn
		err := reg.withLifecycle(context.WithoutCancel(t.Context()), "c1", func(lc *chatLifecycle) error {
			next = lc.openLocked("c1", &marotte.Entry{ID: "t-next"}, marotte.TurnSourcePrompt, "", turnlog.Open("t-next", nil))
			return nil
		})
		if err != nil {
			next = nil
		}
		opened <- next
	}()

	// The parked open must not proceed while the chat is finalizing: that is what
	// keeps the next turn unobservable until the previous one's persistence and
	// broadcast have completed.
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

// TestTurnRegistry_OpenIsCancellable pins the other half of choosing a channel over a
// sync.Cond: a waiter can be given up on. Cond.Wait composes with no cancellation at
// all, so a chat wedged in finalizing held its caller for good.
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

// TestTurnRegistry_ForgetDropsTheChat: the registry must not outlive the chats it
// describes, or a deleted chat's lifecycle is retained for the process's life.
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

// failingUsageStore answers every Mutate with one chosen error and is asked nothing
// else, which is what lets the embedded interface stay nil: a real persist failure has
// no store-side seam, so the metering write's error handling is driven from here.
type failingUsageStore struct {
	bridgeChatRecords
	err error
}

func (s failingUsageStore) Mutate(context.Context, marotte.ChatID, func(*marotte.Chat, bool) bool) (string, error) {
	return "", s.err
}

// TestMutateUsage_TombstonedRefusalIsNotAnError pins the drop the tombstone was
// designed for. ErrTombstoned means the write was DECLINED for a chat id deleted inside
// the window, so nothing reached disk — and a metering frame lands once per turn on
// every chat, so surfacing it would put an ERROR line in the log for the mechanism
// working as intended. Driven through the REAL store's tombstone, not a stub's error.
func TestMutateUsage_TombstonedRefusalIsNotAnError(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.seed(t, "c1", nil)
	if err := cs.Delete(t.Context(), "c1"); err != nil {
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

// TestMutateUsage_OtherErrorsStillLog is the other half, and it is what keeps the drop
// narrow: matching the sentinel must not swallow a real persist failure — a full disk,
// a permission fault, a corrupt chat file.
func TestMutateUsage_OtherErrorsStillLog(t *testing.T) {
	bc := &BridgeCoordinator{chatStore: failingUsageStore{err: errors.New("disk full")}, turns: newTurnRegistry()}
	logs := captureLogs(t)

	bc.AccumulateSpend(t.Context(), "c1", 0.5)

	if out := logs.String(); !strings.Contains(out, "persist turn metering") {
		t.Errorf("a real metering write failure was swallowed: %s", out)
	}
}

// A chat forgotten mid-finalize still has its turn published on the lifecycle its
// waiters are parked on. Re-resolving from the chat id creates a FRESH lifecycle on a
// miss, so a finish in flight after a forget set that one idle and left the old one in
// turnFinalizing forever — parking Forward's own goroutine there for the life of the
// process, with no seal, no replay-projection settle and no exit tail. The turn carries
// its lifecycle now.
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

	// The chat goes away while its turn is finalizing.
	r.forget(chatID)

	// A fold arriving on Forward parks on the lifecycle it already resolved.
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

// busyChatIDs is live's population — an own turn, a pending turn owed its bracket, or a
// held admission slot — because it is the only set a client's stale-`thinking`
// retraction may be withheld from, and the handshake and the transcript GET must not
// disagree about it.
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

	// A SETTLED chat is the whole point of the negative statement: it is what the client
	// retracts against. ReleaseTurn drops the completion handle and leaves the turn open,
	// so the turn has to be finalized for the chat to be idle.
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

// A shell reservation reaches this door too, from the one predicate both doors read.
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
