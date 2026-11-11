package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// startTail is the turn and tail a step's step_starts row carries, read off its wire form.
func startTail(t *testing.T, r *runLog, path string) (turn, tail string) {
	t.Helper()
	got, err := r.stepStarts(t.Context(), "wf1")
	if err != nil {
		t.Fatalf("StepStarts: %v", err)
	}
	row, ok := got[path]
	if !ok {
		t.Fatalf("StepStarts = %+v, want a row for %s", got, path)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Turn string `json:"turn"`
		Tail string `json:"tail"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Turn, wire.Tail
}

// TestRunLog_StepStartsCarryAnInFlightStepsTurnAndLine pins the row a dock line is seeded from: an in-flight step's
// newest turn and the last line its own lane printed, by the dock line's rule (run-step-tail.ts lastPrintedLine).
func TestRunLog_StepStartsCarryAnInFlightStepsTurnAndLine(t *testing.T) {
	step := workflow.PathKey([]string{"wf1", "build"})
	t.Run("a sealed text's last prose line, its markdown markers dropped", func(t *testing.T) {
		r := newRunLog(t.TempDir())
		turn, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: step}, "c-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := turn.TextDelta(t.Context(), "", "say-1", "## Plan\n- reading the diff\n```\n\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := turn.SealOpen(t.Context()); err != nil {
			t.Fatal(err)
		}
		gotTurn, gotTail := startTail(t, r, step)
		if gotTurn != turn.ID() {
			t.Errorf("turn = %q, want %q", gotTurn, turn.ID())
		}
		if gotTail != "reading the diff" {
			t.Errorf("tail = %q, want %q", gotTail, "reading the diff")
		}
	})
	t.Run("a tool call reads as its title", func(t *testing.T) {
		r := newRunLog(t.TempDir())
		turn, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: step}, "c-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := turn.TextDelta(t.Context(), "", "say-1", "Reading."); err != nil {
			t.Fatal(err)
		}
		if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "call-1", Title: "rg heavyRun", Kind: marotte.ToolKindOther}); err != nil {
			t.Fatal(err)
		}
		if _, tail := startTail(t, r, step); tail != "rg heavyRun" {
			t.Errorf("tail = %q, want %q", tail, "rg heavyRun")
		}
	})
	t.Run("the turn after an interruption keeps the line until it prints", func(t *testing.T) {
		r := newRunLog(t.TempDir())
		turn, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: step}, "c-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := turn.TextDelta(t.Context(), "", "say-1", "before the restart"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.closeHost(t.Context(), "c-1", marotte.ConcludeStopReason(marotte.StopReasonInterrupted)); err != nil {
			t.Fatal(err)
		}
		if _, tail := startTail(t, r, step); tail != "before the restart" {
			t.Errorf("after the interruption, tail = %q, want the line standing", tail)
		}
		resumed, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: step}, "c-1")
		if err != nil {
			t.Fatal(err)
		}
		gotTurn, gotTail := startTail(t, r, step)
		if gotTurn != resumed.ID() || gotTail != "before the restart" {
			t.Errorf("resumed row = %q %q, want %q with the line standing", gotTurn, gotTail, resumed.ID())
		}
	})
	t.Run("an ended attempt carries neither", func(t *testing.T) {
		r := newRunLog(t.TempDir())
		turn, _, err := r.open(t.Context(), &translate.RunStep{RunID: "wf1", NodePath: step}, "c-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := turn.TextDelta(t.Context(), "", "say-1", "done"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.closeNode(t.Context(), "wf1", step, "completed", "", nil); err != nil {
			t.Fatal(err)
		}
		if gotTurn, gotTail := startTail(t, r, step); gotTurn != "" || gotTail != "" {
			t.Errorf("ended row = %q %q, want neither", gotTurn, gotTail)
		}
	})
}
