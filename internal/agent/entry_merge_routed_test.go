package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

const routeNotice = "Found a better model for this task. Switched to it."

func routedRow(id string) entryRow {
	return entryRow{kind: marotte.EntryKindModelRouted, id: id, payload: marotte.EntryModelRouted{Message: routeNotice}}
}

func TestMergeEntries_ModelRoutedPairsByOrdinalWithinTheTurn(t *testing.T) {
	record := []recordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "first"),
		routedRow("T1:e1"),
		textRow("S2", "", "second"),
		routedRow("T1:e2"),
		liveClose("T1:e3", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "first"),
		routedRow("m7"),
		textRow("S2", "", "second"),
		routedRow("m9"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := mergeEntries(record, projected, "sid-1")
	if len(merged) != 1 {
		t.Fatalf("merged %d turns, want 1:\n%s", len(merged), dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindModelRouted); got != 2 {
		t.Errorf("merged turn holds %d model_routed, want the record's 2:\n%s", got, dumpMerged(merged))
	}
	if changed {
		t.Errorf("changed = true, want false: both routes pair with the record's:\n%s", dumpMerged(merged))
	}
}

func TestMergeEntries_AReplayOnlyModelRoutedIsInserted(t *testing.T) {
	record := []recordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "first"),
		routedRow("T1:e1"),
		liveClose("T1:e2", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "first"),
		routedRow("m7"),
		routedRow("m8"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := mergeEntries(record, projected, "sid-1")
	if got := countKind(merged[0], marotte.EntryKindModelRouted); got != 2 {
		t.Errorf("merged turn holds %d model_routed, want the record's one plus the replay's second:\n%s", got, dumpMerged(merged))
	}
	if !changed {
		t.Error("changed = false, want true: the replay held a route the record missed")
	}
}
