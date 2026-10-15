package chat

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestReadPosition_ARunningTurnIsNotCounted(t *testing.T) {
	f := newLogFixture(t)
	a := f.prompt("a")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.prompt("b")

	if got := f.log.ReadPosition(); got != 1 {
		t.Errorf("ReadPosition() with b running = %d, want 1 (a's index)", got)
	}
}

func TestReadPosition_EmptyLogAnswersZero(t *testing.T) {
	f := newLogFixture(t)
	if got := f.log.ReadPosition(); got != 0 {
		t.Errorf("ReadPosition() on an empty log = %d, want 0", got)
	}
}

func TestReadPosition_SkipsCarriers(t *testing.T) {
	t.Run("a revert's minted carrier", func(t *testing.T) {
		f := newLogFixture(t)
		a := f.prompt("a")
		f.closeTurn(a, marotte.TurnOutcomeCompleted)
		_, minted, err := f.log.Revert(t.Context(), a, marotte.TurnRevertCauseRewind, "")
		if err != nil || len(minted) == 0 {
			t.Fatalf("Setup: Revert(a) = (minted %d, %v), want a minted carrier", len(minted), err)
		}
		if got := f.log.ReadPosition(); got != 0 {
			t.Errorf("ReadPosition() after reverting the only turn = %d, want 0", got)
		}
	})
	t.Run("a lane-less entry's event carrier", func(t *testing.T) {
		f := newLogFixture(t)
		e := entryOf("", "", "evt-1", marotte.EntryKindModeSwitched, marotte.EntryModeSwitched{})
		minted, err := f.log.appendBetweenTurns(t.Context(), e)
		if err != nil || len(minted) == 0 {
			t.Fatalf("Setup: appendBetweenTurns on an empty log = (minted %d, %v), want a carrier", len(minted), err)
		}
		if got := f.log.ReadPosition(); got != 0 {
			t.Errorf("ReadPosition() with only an event carrier = %d, want 0", got)
		}
	})
}

func TestReadPosition_RewindLowersThenTheNextTurnPassesIt(t *testing.T) {
	f := newLogFixture(t)
	a, b := f.prompt("a"), f.prompt("b")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	before := f.log.ReadPosition()
	if _, _, err := f.log.Revert(t.Context(), b, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("Setup: Revert(b): %v", err)
	}
	if got := f.log.ReadPosition(); got != 1 {
		t.Errorf("ReadPosition() after reverting b = %d, want 1 (a's index)", got)
	}

	c := f.prompt("c")
	f.closeTurn(c, marotte.TurnOutcomeCompleted)
	if got := f.log.ReadPosition(); got <= before {
		t.Errorf("ReadPosition() after c closes = %d, want past the pre-rewind %d, or the turn after a rewind never lights", got, before)
	}
}

func TestReadPosition_FollowsFileOrderNotTimestamps(t *testing.T) {
	cases := []struct {
		desc string
		ts   func(i int) int64
	}{
		{desc: "equal timestamps", ts: func(int) int64 { return 1000 }},
		{desc: "descending timestamps", ts: func(i int) int64 { return int64(10_000 - i) }},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			f := newLogFixture(t)
			for _, text := range []string{"a", "b", "c"} {
				f.closeTurn(f.prompt(text), marotte.TurnOutcomeCompleted)
			}
			all := mustAll(t, f.log)
			for i := range all {
				all[i].Ts = tc.ts(i)
			}
			if err := f.log.Rewrite(t.Context(), all); err != nil {
				t.Fatalf("Setup: Rewrite(): %v", err)
			}
			if got := f.log.ReadPosition(); got != 3 {
				t.Errorf("ReadPosition() = %d, want 3, the newest turn in file order", got)
			}
		})
	}
}

func TestTurnHighWater_CountsRevertedTurnsAndCarriers(t *testing.T) {
	f := newLogFixture(t)
	a := f.prompt("a")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	if _, _, err := f.log.Revert(t.Context(), a, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("Setup: Revert(a): %v", err)
	}
	if got := f.log.TurnHighWater(); got != 2 {
		t.Errorf("TurnHighWater() = %d, want 2: the reverted turn and its carrier", got)
	}
}
