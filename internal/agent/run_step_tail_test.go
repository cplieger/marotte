package agent

import (
	"encoding/json"
	"strconv"
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

// servedStart is a run read's step_starts row for path, with the streaming entry it carries.
type servedStart struct {
	Open *marotte.OpenEntry `json:"open"`
	Turn string             `json:"turn"`
	Tail string             `json:"tail"`
}

func servedRow(t *testing.T, h *Runtime, query, path string) servedStart {
	t.Helper()
	_, body := getRunAt(t, h, query)
	var got struct {
		StepStarts map[string]servedStart `json:"step_starts"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	row, ok := got.StepStarts[path]
	if !ok {
		t.Fatalf("?%s: step_starts = %+v, want a row for %s", query, got.StepStarts, path)
	}
	return row
}

// A client that opens the run tab mid-entry never saw the entry's entry_opened, so every read's row for an in-flight
// step carries the streaming root-lane entry as it stands when the read is written: its id and delta count, which the
// next entry_delta follows, and text from which the dock line reads.
func TestHandleRun_AnInFlightStepsRowCarriesItsStreamingEntry(t *testing.T) {
	h, _ := hostedLeasedRun(t, repeatInspect(t, 1))
	h.runs.log = newRunLog(t.TempDir())
	path := workflow.PathKey([]string{"wf_1", "lane", "iter-0", "build"})
	turn, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	rev := revOf(t, h)
	for _, d := range []string{"## Plan\n- first point\nsec", "ond"} {
		if _, err := turn.TextDelta(t.Context(), "", "say-1", d); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{"view=glance", "", "since=" + strconv.FormatUint(rev, 10)}
	for _, query := range queries {
		row := servedRow(t, h, query, path)
		if row.Open == nil {
			t.Fatalf("?%s: the row carries no open entry: %+v", query, row)
		}
		if row.Turn != turn.ID() {
			t.Errorf("?%s: turn = %q, want the attempt's newest turn %s", query, row.Turn, turn.ID())
		}
		if row.Open.Turn != turn.ID() || row.Open.Lane != "" || row.Open.N != 2 {
			t.Errorf("?%s: open = %+v, want turn %s's root-lane entry after 2 deltas", query, row.Open, turn.ID())
		}
		if got := lastProseLine(row.Open.Text); got != "second" {
			t.Errorf("?%s: the open entry's text reads %q, want %q", query, got, "second")
		}
	}

	if _, err := turn.TextDelta(t.Context(), "", "say-1", "\n```"); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		row := servedRow(t, h, query, path)
		if row.Open == nil || row.Open.N != 3 || lastProseLine(row.Open.Text) != "second" {
			t.Errorf("?%s after a third delta: open = %+v, want 3 deltas still reading %q", query, row.Open, "second")
		}
	}

	if _, err := turn.SealOpen(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		if row := servedRow(t, h, query, path); row.Open != nil || row.Tail != "second" {
			t.Errorf("?%s after the seal: row = %+v, want no open entry and the sealed line", query, row)
		}
	}
}

// A step's turn prints between revisions with no revision of its own, so a read's in-flight row carries the line
// as it stands when the read is written.
func TestHandleRun_AnInFlightStepsRowCarriesTheLinePrintedSinceTheRevision(t *testing.T) {
	h, _ := hostedLeasedRun(t, repeatInspect(t, 1))
	h.runs.log = newRunLog(t.TempDir())
	path := workflow.PathKey([]string{"wf_1", "lane", "iter-0", "build"})
	turn, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	rev := revOf(t, h)
	if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "call-1", Title: "rg heavyRun", Kind: marotte.ToolKindOther}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"view=glance", "", "since=" + strconv.FormatUint(rev, 10)} {
		if row := servedRow(t, h, query, path); row.Tail != "rg heavyRun" {
			t.Errorf("?%s: tail = %q, want the tool call printed since the revision", query, row.Tail)
		}
	}
}
