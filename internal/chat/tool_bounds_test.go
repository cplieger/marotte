package chat

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// persistedTool appends one tool_call and its tool_result in a fresh turn and reads
// both back off DISK, so every assertion is about the RECORD and not about a value
// the test still holds.
func persistedTool(t *testing.T, call marotte.EntryToolCall, res marotte.EntryToolResult) (marotte.EntryToolCall, marotte.EntryToolResult) {
	t.Helper()
	s, _ := newTestStore(t)
	turn := openPromptTurn(t, s, "c1", "m-1")
	for _, e := range []*marotte.Entry{
		entryOf(turn, "", call.ID, marotte.EntryKindToolCall, call),
		entryOf(turn, "", call.ID+":result", marotte.EntryKindToolResult, res),
	} {
		if err := s.Append(t.Context(), "c1", e); err != nil {
			t.Fatalf("Setup: Append(%s): %v", e.Kind, err)
		}
	}
	page, err := s.TurnPage(t.Context(), "c1", turn, 0)
	if err != nil {
		t.Fatalf("Setup: TurnPage: %v", err)
	}
	entries := page.Entries
	if len(entries) != 3 {
		t.Fatalf("Setup: want turn_open, tool_call, tool_result; got %d entries", len(entries))
	}
	var gotCall marotte.EntryToolCall
	var gotRes marotte.EntryToolResult
	if err := json.Unmarshal(entries[1].Payload, &gotCall); err != nil {
		t.Fatalf("Setup: decode tool_call: %v", err)
	}
	if err := json.Unmarshal(entries[2].Payload, &gotRes); err != nil {
		t.Fatalf("Setup: decode tool_result: %v", err)
	}
	return gotCall, gotRes
}

// The output bound applies whatever the log's caps are: it is what keeps one call from
// adding an unbounded number of bytes to the record. The fixture must CONTAIN an output
// over persistBudget.outputBytes, and the assertion reads the value back off disk.
func TestStoreBound_OversizeOutputIsCutWithItsOriginalSizeRecorded(t *testing.T) {
	full := strings.Repeat("o", persistBudget.outputBytes*3)
	_, got := persistedTool(t,
		marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute, Status: marotte.ToolInProgress},
		marotte.EntryToolResult{
			Status: marotte.ToolCompleted, Output: full,
			OutputSpans: []marotte.TextSpan{{Start: 0, End: 4, Attrs: 1}},
		})

	if len(got.Output) >= len(full) {
		t.Errorf("persisted output is %d bytes, want fewer than the %d written", len(got.Output), len(full))
	}
	if len(got.Output) > persistBudget.outputBytes+1 {
		t.Errorf("persisted output is %d bytes, want at most %d", len(got.Output), persistBudget.outputBytes+1)
	}
	if got.Truncated == nil {
		t.Fatal("truncated = nil, want the marker: without it the reader is shown less than happened, silently")
	}
	if got.Truncated.OutputBytes != len(full) {
		t.Errorf("truncated.output_bytes = %d, want %d (the size before the cut)", got.Truncated.OutputBytes, len(full))
	}
	if len(got.OutputSpans) != 0 {
		t.Errorf("output_spans = %v, want none: the offsets index the whole output", got.OutputSpans)
	}
	if got.HasFull {
		t.Error("has_full = true on the persisted record; it is the transcript read path's field")
	}
}

// A ToolDiff is a before/after pair the client line-diffs, so a cut pair renders hunks
// describing an edit nobody made: a diff is kept WHOLE or dropped. The fixture holds one
// diff that fits and one that does not, or a test could pass by dropping everything.
func TestStoreBound_OversizeDiffIsDroppedNotTruncated(t *testing.T) {
	half := persistBudget.diffBytes
	diffs := []marotte.ToolDiff{
		{Path: "small.go", OldText: "a", NewText: "b"},
		{Path: "huge.go", OldText: strings.Repeat("o", half), NewText: strings.Repeat("n", half)},
	}
	_, got := persistedTool(t,
		marotte.EntryToolCall{ID: "tc1", Title: "Edit", Kind: marotte.ToolKindEdit, Status: marotte.ToolInProgress},
		marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: diffs})

	if len(got.Diffs) != 1 || got.Diffs[0].Path != "small.go" {
		t.Fatalf("persisted diffs = %v, want only the one that fit", got.Diffs)
	}
	if got.Diffs[0].OldText != "a" || got.Diffs[0].NewText != "b" {
		t.Errorf("kept diff = %+v, want it whole", got.Diffs[0])
	}
	if got.Truncated == nil {
		t.Fatal("truncated = nil, want the marker")
	}
	if got.Truncated.DiffCount != 2 {
		t.Errorf("truncated.diff_count = %d, want 2 (the count before the cut)", got.Truncated.DiffCount)
	}
	if want := diffsBytes(diffs); got.Truncated.DiffBytes != want {
		t.Errorf("truncated.diff_bytes = %d, want %d", got.Truncated.DiffBytes, want)
	}
}

// The claim line is built from an input's small members, so the bulk member goes and
// the small ones stay, with the whole input's size recorded.
func TestStoreBound_OversizeInputKeepsTheClaimLine(t *testing.T) {
	in, err := json.Marshal(map[string]any{
		"path": "internal/app/main.go",
		"text": strings.Repeat("L", persistBudget.inputMember*3),
	})
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	got, _ := persistedTool(t,
		marotte.EntryToolCall{ID: "tc1", Title: "Write", Kind: marotte.ToolKindWrite, Status: marotte.ToolInProgress, Input: in},
		marotte.EntryToolResult{Status: marotte.ToolCompleted})

	var kept map[string]any
	if uerr := json.Unmarshal(got.Input, &kept); uerr != nil {
		t.Fatalf("persisted input is not an object: %s", got.Input)
	}
	if kept["path"] != "internal/app/main.go" {
		t.Errorf("path = %v, want it kept", kept["path"])
	}
	if _, present := kept["text"]; present {
		t.Error("text survived, want the over-budget member dropped")
	}
	if got.Truncated == nil || got.Truncated.InputBytes != len(in) {
		t.Errorf("truncated = %+v, want InputBytes = %d", got.Truncated, len(in))
	}
}

func TestStoreBound_SmallCallIsPersistedWhole(t *testing.T) {
	call := marotte.EntryToolCall{
		ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute, Status: marotte.ToolInProgress,
		Input: json.RawMessage(`{"command":"ls"}`),
	}
	res := marotte.EntryToolResult{
		Status: marotte.ToolCompleted, Output: "all done\n",
		OutputSpans: []marotte.TextSpan{{Start: 0, End: 3, Attrs: 1}},
		Diffs:       []marotte.ToolDiff{{Path: "a.go", OldText: "x", NewText: "y"}},
	}
	gotCall, gotRes := persistedTool(t, call, res)

	if gotCall.Truncated != nil || gotRes.Truncated != nil {
		t.Errorf("truncated = %+v / %+v, want nil for a small call", gotCall.Truncated, gotRes.Truncated)
	}
	if gotRes.Output != res.Output || len(gotRes.OutputSpans) != 1 || len(gotRes.Diffs) != 1 {
		t.Errorf("persisted result = %+v, want it as written", gotRes)
	}
	var gotIn, wantIn map[string]any
	if err := json.Unmarshal(gotCall.Input, &gotIn); err != nil {
		t.Fatalf("persisted input is not an object: %s", gotCall.Input)
	}
	if err := json.Unmarshal(call.Input, &wantIn); err != nil {
		t.Fatalf("Setup: fixture input is not an object: %s", call.Input)
	}
	if !maps.Equal(gotIn, wantIn) {
		t.Errorf("input = %v, want %v", gotIn, wantIn)
	}
}

// The merge rewrite hands every entry back through the bound, so the marker must keep
// the FIRST measurement: a re-bounded already-cut output would otherwise report its own
// shortened length as the size before the cut.
func TestStoreBound_RewriteKeepsTheFirstMeasurement(t *testing.T) {
	full := strings.Repeat("o", persistBudget.outputBytes*3)
	s, _ := newTestStore(t)
	turn := openPromptTurn(t, s, "c1", "m-1")
	call := marotte.EntryToolCall{ID: "tc1", Title: "Execute", Kind: marotte.ToolKindExecute, Status: marotte.ToolInProgress}
	for _, e := range []*marotte.Entry{
		entryOf(turn, "", "tc1", marotte.EntryKindToolCall, call),
		entryOf(turn, "", "tc1:result", marotte.EntryKindToolResult, marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: full}),
	} {
		if err := s.Append(t.Context(), "c1", e); err != nil {
			t.Fatalf("Setup: Append(%s): %v", e.Kind, err)
		}
	}
	for range 3 {
		entries, err := s.All(t.Context(), "c1")
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		if _, _, err := s.Reconcile(t.Context(), "c1", func(l *EntryLog, _ EntryHeader) (bool, error) {
			return true, l.Rewrite(t.Context(), entries)
		}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}

	page, err := s.TurnPage(t.Context(), "c1", turn, 0)
	if err != nil {
		t.Fatalf("TurnPage: %v", err)
	}
	entries := page.Entries
	var res marotte.EntryToolResult
	if len(entries) != 3 || json.Unmarshal(entries[2].Payload, &res) != nil {
		t.Fatalf("want three entries with a decodable tool_result, got %d", len(entries))
	}
	if res.Truncated == nil {
		t.Fatal("truncated = nil after a rewrite, want the marker to survive")
	}
	if res.Truncated.OutputBytes != len(full) {
		t.Errorf("truncated.output_bytes = %d after 3 rewrites, want %d (the FIRST measurement)",
			res.Truncated.OutputBytes, len(full))
	}
}
