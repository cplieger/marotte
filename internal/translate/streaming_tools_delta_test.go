package translate

import (
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestToolCallDelta_SendsEachOutputChunkOnce pins that each output chunk travels on one
// frame only, not re-sent in full on every later frame.
func TestToolCallDelta_SendsEachOutputChunkOnce(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)

	for _, chunk := range []string{"first\n", "second\n", "third\n"} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"content": []map[string]any{
				{"type": "content", "content": map[string]any{"text": chunk}},
			},
		}), FrameAttribution{})
	}

	deltas := toolCallDeltas(t, events)
	if len(deltas) != 3 {
		t.Fatalf("got %d tool_progress frames, want 3", len(deltas))
	}
	// parseToolUpdateContent appends a newline per content block.
	for i, want := range []string{"first\n\n", "second\n\n", "third\n\n"} {
		if deltas[i].OutputDelta != want {
			t.Errorf("frame %d output_delta = %q, want %q — a delta must not restate what earlier frames delivered",
				i, deltas[i].OutputDelta, want)
		}
		if deltas[i].OutputReplace {
			t.Errorf("frame %d set output_replace on a plain append", i)
		}
	}
	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if got.Output != "first\n\nsecond\n\nthird\n\n" {
		t.Errorf("folded output = %q, want every chunk in order", got.Output)
	}
}

// TestToolCallDelta_SendsEachDiffOnce is the diff half of the same rule.
func TestToolCallDelta_SendsEachDiffOnce(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)

	for _, path := range []string{"a.go", "b.go"} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"content": []map[string]any{
				{"type": "diff", "path": path, "oldText": "x", "newText": "y"},
			},
		}), FrameAttribution{})
	}

	deltas := toolCallDeltas(t, events)
	if len(deltas) != 2 {
		t.Fatalf("got %d tool_progress frames, want 2", len(deltas))
	}
	for i, want := range []string{"a.go", "b.go"} {
		if len(deltas[i].DiffsAppended) != 1 || deltas[i].DiffsAppended[0].Path != want {
			t.Errorf("frame %d diffs_appended = %+v, want exactly the one new %q",
				i, deltas[i].DiffsAppended, want)
		}
	}
	got, _ := lastToolCallUpdate(t, deps, events)
	if len(got.Diffs) != 2 {
		t.Errorf("folded diffs = %d, want 2 — the card keeps every arrival", len(got.Diffs))
	}
}

// TestToolCallDelta_NeverCarriesTheInput pins that an update, which cannot change the
// input, has no field for it.
func TestToolCallDelta_NeverCarriesTheInput(t *testing.T) {
	fields := reflect.VisibleFields(reflect.TypeFor[marotte.ToolProgressPayload]())
	for _, f := range fields {
		if f.Name == "Input" {
			t.Error("ToolProgressPayload has an Input field; an update never changes it")
		}
	}
}

// TestToolCallDelta_TheTerminalOutputWinsOnTheResult pins the one rule a pure-append wire
// cannot express: at completion the terminal's full stream replaces the ACP fragments,
// so the settled call travels whole as tool_result, never as a tool_progress.
func TestToolCallDelta_TheTerminalOutputWinsOnTheResult(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	const termID = "term-1"
	deps.terminals[termID] = termRendered{text: "the terminal's whole stream\n"}

	// A fragment first, so there is something for the terminal's output to win over.
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"text": "a fragment"}},
		},
	}), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"content": []map[string]any{
			{"type": "terminal", "terminalId": termID},
		},
	}), FrameAttribution{})

	if deltas := toolCallDeltas(t, events); len(deltas) != 1 || deltas[0].OutputDelta != "a fragment\n" {
		t.Fatalf("tool_progress frames = %+v, want the fragment's alone; the completion is a tool_result", deltas)
	}
	results := toolResultsOf(t, deps.chatEntries(chatID))
	if len(results) != 1 {
		t.Fatalf("tool_result entries = %d, want 1", len(results))
	}
	if results[0].Output != "the terminal's whole stream\n" || results[0].TerminalID != termID {
		t.Errorf("tool_result = {output %q, terminal %q}, want the terminal's stream alone under %q",
			results[0].Output, results[0].TerminalID, termID)
	}
	got, _ := lastToolCallUpdate(t, deps, events)
	if got.Output != "the terminal's whole stream\n" {
		t.Errorf("folded output = %q, want the terminal's stream alone — the fragment would survive under it", got.Output)
	}
}

// TestToolCallDelta_AnUnchangedFieldIsAbsent pins "absent means unchanged": a status-only
// frame carries the status and the two addresses, nothing else.
func TestToolCallDelta_AnUnchangedFieldIsAbsent(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)

	// KAS sends title and kind nullish on most updates.
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
	}), FrameAttribution{})

	deltas := toolCallDeltas(t, events)
	if len(deltas) != 1 {
		t.Fatalf("got %d tool_progress frames, want 1", len(deltas))
	}
	want := marotte.ToolProgressPayload{
		Turn:       deps.turns.chats[chatID].ID(),
		ToolCallID: "tc-1",
		Status:     marotte.ToolInProgress,
	}
	if !reflect.DeepEqual(deltas[0], want) {
		t.Errorf("a status-only frame = %+v,\nwant exactly %+v", deltas[0], want)
	}
}

// TestToolCallDelta_SendsTheDuration pins that the frame that completes a call carries the duration.
func TestToolCallDelta_SendsTheDuration(t *testing.T) {
	before := marotte.ToolCall{ID: "tc-1", Status: marotte.ToolInProgress}
	after := before
	after.Status = marotte.ToolCompleted
	after.DurationMs = 1234

	d := toolProgress("t1", &before, &after)
	if d.DurationMs != 1234 {
		t.Errorf("duration_ms = %d, want 1234 — a completed card shows it", d.DurationMs)
	}
	// A frame that did not change it sends nothing.
	after.DurationMs = before.DurationMs
	if d2 := toolProgress("t1", &before, &after); d2.DurationMs != 0 {
		t.Errorf("duration_ms = %d on an unchanged duration, want 0", d2.DurationMs)
	}
}

func TestOutputDelta(t *testing.T) {
	tests := []struct {
		name        string
		before      string
		after       string
		wantDelta   string
		wantReplace bool
	}{
		{name: "unchanged", before: "abc", after: "abc"},
		{name: "appended", before: "abc", after: "abcdef", wantDelta: "def"},
		{name: "first write", before: "", after: "abc", wantDelta: "abc"},
		// Terminal adoption: a shorter or different value is not an extension, so it travels whole.
		{name: "shortened", before: "abcdef", after: "abc", wantDelta: "abc", wantReplace: true},
		{name: "rewritten", before: "abc", after: "xyz", wantDelta: "xyz", wantReplace: true},
		{name: "cleared", before: "abc", after: "", wantDelta: "", wantReplace: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delta, replace := outputDelta(tc.before, tc.after)
			if delta != tc.wantDelta || replace != tc.wantReplace {
				t.Errorf("outputDelta(%q, %q) = (%q, %t), want (%q, %t)",
					tc.before, tc.after, delta, replace, tc.wantDelta, tc.wantReplace)
			}
		})
	}
}

func toolCallDeltas(t *testing.T, events *[]marotte.ServerEvent) []marotte.ToolProgressPayload {
	t.Helper()
	var out []marotte.ToolProgressPayload
	for _, e := range *events {
		if e.Type != marotte.EventToolProgress {
			continue
		}
		p, ok := e.Payload.(marotte.ToolProgressPayload)
		if !ok {
			t.Fatalf("tool_progress payload type = %T, want marotte.ToolProgressPayload", e.Payload)
		}
		out = append(out, p)
	}
	return out
}
