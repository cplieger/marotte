package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

func newCompactionReapFixture(t *testing.T) (*Runtime, string, *turnlog.Turn, context.Context) {
	t.Helper()
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	pctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(nil) })
	turnID, log := h.stagePromptTurnCalling(t, "c1", cancel)
	t.Cleanup(func() { h.ReleaseTurn("c1", turnID) })
	return h, turnID, log, pctx
}

// inside a synctest bubble its stamp is the fake clock's.
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
		h.coord.CompactionFailed("c1", turnID, "compaction failed")
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
	h, turnID, _, pctx := newCompactionReapFixture(t)
	gen := h.coord.turns.attachForward("c1").gen
	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", turnID, "compaction failed")
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
	h, turnID, log, pctx := newCompactionReapFixture(t)

	synctest.Test(t, func(t *testing.T) {
		// Inside the bubble and before the arm: synctest's clock starts at 2000-01-01, so an outside stamp reads as newer.
		startTool(t, log, "tool")
		time.Sleep(time.Millisecond)

		h.coord.CompactionFailed("c1", turnID, "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
	})
	if pctx.Err() == nil {
		t.Error("an in-flight status left behind before the budget kept the turn alive")
	}
}

func TestCompactionReap_KeepsAToolThatStartedInsideTheBudgetAlive(t *testing.T) {
	h, turnID, log, pctx := newCompactionReapFixture(t)

	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", turnID, "compaction failed")
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

		turn, ok := h.coord.turns.claimTurn(t.Context(), "c1", turnID, nil)
		if !ok {
			t.Fatal("normal closer could not claim the tool-running turn")
		}
		h.coord.turns.finish(turn, marotte.TurnResult{})
	})
}

func TestCompactionReap_WithNoOpenTurnArmsNothing(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.CompactionFailed("c1", "t-gone", "compaction failed")
	if _, ok := h.coord.turns.lookup("c1"); ok {
		t.Error("CompactionFailed with no open turn created turn state")
	}
}

// A failure recorded in a turn that is no longer the chat's own arms nothing on the turn that is.
func TestCompactionReap_ATurnNoLongerOwnArmsNothing(t *testing.T) {
	h, turnID, _, _ := newCompactionReapFixture(t)
	h.coord.CompactionFailed("c1", "t-closed-earlier", "compaction failed")
	lc, _ := h.coord.turns.lookup("c1")
	lc.mu.Lock()
	armed := lc.own != nil && lc.own.ID == turnID && lc.own.reapTimer != nil
	lc.mu.Unlock()
	if armed {
		t.Error("a failure named for another turn armed the reap on the open turn")
	}
}

func TestCompactionReap_StoppedWhenTheTurnClosesNormally(t *testing.T) {
	h, turnID, _, _ := newCompactionReapFixture(t)
	h.coord.CompactionFailed("c1", turnID, "compaction failed")
	turn, ok := h.coord.turns.claimTurn(t.Context(), "c1", turnID, nil)
	if !ok {
		t.Fatal("normal closer could not claim the armed turn")
	}
	if turn.reapTimer != nil {
		t.Error("claimed turn kept its compaction reap timer")
	}
	h.coord.turns.finish(turn, marotte.TurnResult{})
}
