package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// newCompactionReapFixture stages a prompt turn whose bridge holds a cancellable
// prompt call, the state the reap's interrupt reaches into.
func newCompactionReapFixture(t *testing.T) (*Runtime, string, *turnlog.Turn, context.Context) {
	t.Helper()
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	turnID, log := h.stagePromptTurn(t, "c1")
	t.Cleanup(func() { h.ReleaseTurn("c1", turnID) })

	pctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(nil) })
	sb, _ := h.bridge.mgr.orInsert("c1")
	sb.mu.Lock()
	sb.state = bridgePrompting
	sb.promptCancel = cancel
	sb.mu.Unlock()
	return h, turnID, log, pctx
}

// startTool records an unsettled tool call on the turn. Inside a synctest bubble
// its stamp is the fake clock's, which is what the reap compares against.
func startTool(t *testing.T, log *turnlog.Turn, id string) {
	t.Helper()
	_, err := log.ToolCall(t.Context(), "", &marotte.EntryToolCall{
		ID: id, Title: id, Kind: marotte.ToolKindExecute, Status: marotte.ToolInProgress,
	})
	if err != nil {
		t.Fatalf("ToolCall(%q) failed: %v", id, err)
	}
}

func TestCompactionReap_TripsTheSilentTurn(t *testing.T) {
	h, turnID, _, pctx := newCompactionReapFixture(t)
	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
	})

	if pctx.Err() == nil {
		t.Error("silent failed-compaction turn kept its prompt context live")
	}
	lc := h.coord.turns.lifecycleFor("c1")
	lc.mu.Lock()
	turn := lc.own
	lc.mu.Unlock()
	if turn == nil || turn.ID != turnID {
		t.Fatalf("own turn after the reap = %v, want %q still open", turn, turnID)
	}
	if got := h.coord.turns.interruptCause(turn); got != "compaction failed" {
		t.Errorf("interrupt cause = %q, want %q", got, "compaction failed")
	}
}

func TestCompactionReap_ReArmsWhenTheBackendSpoke(t *testing.T) {
	h, _, _, pctx := newCompactionReapFixture(t)
	gen := h.coord.turns.attachForward("c1")
	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", "compaction failed")
		h.coord.turns.observe("c1", gen, 1)
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
		if pctx.Err() != nil {
			t.Error("backend activity did not restart the silence budget")
		}
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
	})
	if pctx.Err() == nil {
		t.Error("re-armed silence budget did not interrupt the turn")
	}
}

func TestCompactionReap_DoesNotRearmForAToolThatPredatesTheBudget(t *testing.T) {
	h, _, log, pctx := newCompactionReapFixture(t)

	synctest.Test(t, func(t *testing.T) {
		// Inside the bubble, and before the arm: synctest's clock starts at
		// 2000-01-01, so a start recorded outside it lands in the real present
		// and would read as newer than the arm rather than older.
		startTool(t, log, "tool")
		time.Sleep(time.Millisecond)

		h.coord.CompactionFailed("c1", "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
	})
	if pctx.Err() == nil {
		t.Error("an in-flight status left behind before the budget kept the turn alive")
	}
}

func TestCompactionReap_KeepsAToolThatStartedInsideTheBudgetAlive(t *testing.T) {
	h, _, log, pctx := newCompactionReapFixture(t)

	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", "compaction failed")
		time.Sleep(time.Millisecond)
		startTool(t, log, "tool")

		time.Sleep(compactionFailedTurnBudget - time.Millisecond)
		synctest.Wait()
		if pctx.Err() != nil {
			t.Error("a tool that started inside the budget did not keep the turn alive")
		}
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
		if pctx.Err() != nil {
			t.Error("a long-running tool was treated as a flat timeout")
		}

		turn, ok := h.coord.turns.claimOwn(t.Context(), "c1")
		if !ok {
			t.Fatal("normal closer could not claim the tool-running turn")
		}
		h.coord.turns.finish(turn, marotte.TurnResult{})
	})
}

func TestCompactionReap_WithNoOpenTurnArmsNothing(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.CompactionFailed("c1", "compaction failed")
	lc := h.coord.turns.lifecycleFor("c1")
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own != nil {
		t.Error("CompactionFailed with no open turn created turn state")
	}
}

func TestCompactionReap_StoppedWhenTheTurnClosesNormally(t *testing.T) {
	h, _, _, _ := newCompactionReapFixture(t)
	h.coord.CompactionFailed("c1", "compaction failed")
	turn, ok := h.coord.turns.claimOwn(t.Context(), "c1")
	if !ok {
		t.Fatal("normal closer could not claim the armed turn")
	}
	if turn.reapTimer != nil {
		t.Error("claimed turn kept its compaction reap timer")
	}
	h.coord.turns.finish(turn, marotte.TurnResult{})
}
