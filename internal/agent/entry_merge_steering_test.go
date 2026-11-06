package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

func steeringRow(id, doc string) entryRow {
	return entryRow{kind: marotte.EntryKindToolCall, id: id, payload: marotte.EntryToolCall{
		ID: id, Title: "Steering loaded: " + doc, Kind: marotte.ToolKindSteering, Status: marotte.ToolCompleted,
		SteeringDocs: []string{"file:///w/.kiro/steering/" + doc},
	}}
}

// Neither side's frame carries an id, so the n-th steering box of a paired turn is the n-th on
// the other side, and a reload draws each once.
func TestMergeEntries_SteeringBoxesPairByOrdinalWithinTheTurn(t *testing.T) {
	record := []recordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		steeringRow("steering-T1:e1", "go.md"),
		textRow("S", "", "first"),
		steeringRow("steering-T1:e2", "testing.md"),
		liveClose("T1:e3", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		steeringRow("steering-m7", "go.md"),
		textRow("S", "", "first"),
		steeringRow("steering-m9", "testing.md"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := mergeEntries(record, projected, "sid-1")
	if len(merged) != 1 {
		t.Fatalf("merged %d turns, want 1:\n%s", len(merged), dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindToolCall); got != 2 {
		t.Errorf("merged turn holds %d steering boxes, want the record's 2:\n%s", got, dumpMerged(merged))
	}
	if changed {
		t.Errorf("changed = true, want false: both boxes pair with the record's:\n%s", dumpMerged(merged))
	}
}

func TestMergeEntries_AReplayOnlySteeringBoxIsInserted(t *testing.T) {
	record := []recordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "first"),
		liveClose("T1:e2", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "first"),
		steeringRow("steering-m7", "go.md"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := mergeEntries(record, projected, "sid-1")
	if got := countKind(merged[0], marotte.EntryKindToolCall); got != 1 {
		t.Errorf("merged turn holds %d steering boxes, want the replay's one:\n%s", got, dumpMerged(merged))
	}
	if !changed {
		t.Error("changed = false, want true: the replay held a box the record missed")
	}
}
