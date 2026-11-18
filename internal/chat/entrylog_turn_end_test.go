package chat

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// closeTs is the ts the log stamped on turn's turn_close.
func closeTs(t *testing.T, f *logFixture, turn string) int64 {
	t.Helper()
	for _, e := range mustRange(t, f, turn) {
		if e.Kind == marotte.EntryKindTurnClose {
			return e.Ts
		}
	}
	t.Fatalf("turn %s has no turn_close", turn)
	return 0
}

// A Bot's idle trigger counts from its last turn's end, which the header caches beside the turn count, written by the
// same counters write: the newest closed turn's close, never an open turn's and never a carrier's.
func TestEntryLog_TheHeaderCachesTheLastTurnEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLogFixture(t)
		first := f.prompt("one")
		time.Sleep(time.Minute)
		f.closeTurn(first, marotte.TurnOutcomeCompleted)
		if err := f.log.writeCounters(t.Context()); err != nil {
			t.Fatalf("write counters: %v", err)
		}
		ended := closeTs(t, f, first)
		if h := f.readHeader(); h.LastTurnEndedAt != ended || h.TurnCount != 1 {
			t.Errorf("after the first close the header is (n %d, ended %d), want (1, %d)", h.TurnCount, h.LastTurnEndedAt, ended)
		}

		time.Sleep(time.Minute)
		second := f.prompt("two")
		if err := f.log.writeCounters(t.Context()); err != nil {
			t.Fatalf("write counters: %v", err)
		}
		if h := f.readHeader(); h.LastTurnEndedAt != ended || h.TurnCount != 2 {
			t.Errorf("with the second turn open the header is (n %d, ended %d), want (2, %d): an open turn has not ended",
				h.TurnCount, h.LastTurnEndedAt, ended)
		}

		time.Sleep(time.Minute)
		f.closeTurn(second, marotte.TurnOutcomeFailed)
		if err := f.log.writeCounters(t.Context()); err != nil {
			t.Fatalf("write counters: %v", err)
		}
		later := closeTs(t, f, second)
		if later <= ended {
			t.Fatalf("Setup: the second close's ts %d is not after the first's %d", later, ended)
		}
		if h := f.readHeader(); h.LastTurnEndedAt != later {
			t.Errorf("after the second close last_turn_ended_at = %d, want %d", h.LastTurnEndedAt, later)
		}

		if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool { c.LastTurnEndedAt = 1; return true }); err != nil {
			t.Fatalf("plant a stale header: %v", err)
		}
		f.reopen()
		if h := f.readHeader(); h.LastTurnEndedAt != later {
			t.Errorf("after a reopen last_turn_ended_at = %d, want the log's %d", h.LastTurnEndedAt, later)
		}
	})
}

// A carrier turn is marotte's own bookkeeping, not a turn the agent ran, so it is no turn end.
func TestEntryLog_ACarrierTurnIsNoTurnEnd(t *testing.T) {
	f := newLogFixture(t)
	e := entryOf("", "", "switch-1", marotte.EntryKindModelSwitched, marotte.EntryModelSwitched{From: "a", To: "b"})
	if _, err := f.log.appendBetweenTurns(t.Context(), e); err != nil {
		t.Fatalf("between-turns append: %v", err)
	}
	f.reopen()
	if h := f.readHeader(); h.LastTurnEndedAt != 0 {
		t.Errorf("a chat whose only turn is a carrier has last_turn_ended_at %d, want 0", h.LastTurnEndedAt)
	}
}
