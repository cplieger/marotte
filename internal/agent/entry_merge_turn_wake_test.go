package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// KAS's replay holds no turn_wake (it is marotte's record of what woke the turn), so the merge after a restart
// keeps the record's, in its place at the head of the wake turn.
func TestMergeEntries_AWakeTurnKeepsItsTurnWake(t *testing.T) {
	wake := marotte.EntryTurnWake{
		Reason: marotte.WakeReasonRunFinished, Run: "wf_28486344b9bdedb0", RunLabel: "release-impact",
		RunStatus: marotte.RunStatusCompleted,
	}
	record := []recordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		entryRow{kind: marotte.EntryKindTurnWake, id: "T1:e1", payload: wake},
		textRow("S", "", "The release-impact run finished; reading its results."),
		placeholderClose("T1:e3"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "The release-impact run finished; reading its results. All green."),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, _ := mergeEntries(record, projected, "sid-1")
	if len(merged) != 1 {
		t.Fatalf("merged %d turns, want the one wake turn:\n%s", len(merged), dumpMerged(merged))
	}
	if n := countKind(merged[0], marotte.EntryKindTurnWake); n != 1 {
		t.Fatalf("the merged turn holds %d turn_wake entries, want 1:\n%s", n, dumpMerged(merged))
	}
	if e := merged[0].Entries[1]; e.Kind != marotte.EntryKindTurnWake {
		t.Errorf("entry 1 is %s, want the turn_wake right after the turn_open:\n%s", e.Kind, dumpMerged(merged))
	}
	var got marotte.EntryTurnWake
	decodePayload(t, entryIn(t, merged[0], marotte.EntryKindTurnWake), &got)
	if got.Run != wake.Run || got.RunStatus != wake.RunStatus || got.RunLabel != wake.RunLabel {
		t.Errorf("turn_wake = %+v, want the record's %+v", got, wake)
	}
}
