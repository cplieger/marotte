package translate

// The terminal link on a tool call: its source, its adoption order, and a missing output.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestAdoptTerminalOutput_LinkAndCompletionInOneFrame pins the link set before adoption when
// one frame carries both the terminal block and `completed`.
func TestAdoptTerminalOutput_LinkAndCompletionInOneFrame(t *testing.T) {
	const termID = "term-1"
	tr, _, deps, events, chatID := primeToolCall(t)
	deps.terminals[termID] = termRendered{
		text:  "hello\n",
		spans: []marotte.TextSpan{{Start: 0, End: 5, FG: 1, BG: -1}},
	}

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"content": []map[string]any{
			{"type": "terminal", "terminalId": termID},
		},
	}), FrameAttribution{})

	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update was broadcast")
	}
	if tc.TerminalID != termID {
		t.Errorf("terminal_id = %q, want %q: the link was not adopted from the frame", tc.TerminalID, termID)
	}
	if tc.Output != "hello\n" {
		t.Errorf("output = %q, want %q: adoption ran before the link was set", tc.Output, "hello\n")
	}
	if len(tc.OutputSpans) != 1 || tc.OutputSpans[0].FG != 1 {
		t.Errorf("output_spans = %+v, want the terminal's one red span", tc.OutputSpans)
	}
}

// The ordinary sequence still works: the link arrives on an in_progress frame
// and the completion adopts against it.
func TestAdoptTerminalOutput_LinkOnAnEarlierFrame(t *testing.T) {
	const termID = "term-2"
	tr, _, deps, events, chatID := primeToolCall(t)
	deps.terminals[termID] = termRendered{text: "done\n"}

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{{"type": "terminal", "terminalId": termID}},
	}), FrameAttribution{})
	// No adoption while the command runs: the live stream owns those bytes.
	if tc, ok := lastToolCallUpdate(t, deps, events); ok && tc.Output != "" {
		t.Errorf("output = %q on an in_progress frame, want it empty until completion", tc.Output)
	}
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update was broadcast")
	}
	if tc.Output != "done\n" {
		t.Errorf("output = %q, want %q", tc.Output, "done\n")
	}
}

// TestAdoptTerminalOutput_SameFrameNoteStartsOnItsOwnLine pins the join after an adopted
// snapshot: kiro-cli 2.28's cancel frame carries a text block beside the terminal's output, and
// a command stopped mid-line must not run into it.
func TestAdoptTerminalOutput_SameFrameNoteStartsOnItsOwnLine(t *testing.T) {
	const (
		termID = "term-note"
		l6     = "This tool was interrupted before it reported a result, so it may or may not have taken effect."
	)
	spans := []marotte.TextSpan{{Start: 0, End: 5, FG: 2, BG: -1}}
	cases := []struct {
		name     string
		terminal string
		want     string
	}{
		{name: "partial", terminal: "building 42%", want: "building 42%\n" + l6 + "\n"},
		{name: "complete line", terminal: "done\n", want: "done\n" + l6 + "\n"},
		{name: "silent", terminal: "", want: l6 + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, _, deps, events, chatID := primeToolCall(t)
			deps.terminals[termID] = termRendered{text: c.terminal, spans: spans}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1", "status": "in_progress",
				"content": []map[string]any{{"type": "terminal", "terminalId": termID}},
			}), FrameAttribution{})
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "tc-1",
				"status":     "failed",
				"rawOutput":  l6,
				"content": []map[string]any{
					{"type": "content", "content": map[string]any{"type": "text", "text": l6}},
				},
			}), FrameAttribution{})

			tc, ok := lastToolCallUpdate(t, deps, events)
			if !ok {
				t.Fatal("no tool_call_update was broadcast")
			}
			if tc.Output != c.want {
				t.Errorf("output over terminal %q = %q, want %q", c.terminal, tc.Output, c.want)
			}
			if c.terminal != "" && (len(tc.OutputSpans) != 1 || tc.OutputSpans[0] != spans[0]) {
				t.Errorf("output_spans = %+v, want the terminal's own %+v", tc.OutputSpans, spans)
			}
		})
	}
}

// TestAdoptTerminalOutput_TerminalWinsOverAnEarlierFragment pins the terminal's full output over
// an earlier content fragment.
func TestAdoptTerminalOutput_TerminalWinsOverAnEarlierFragment(t *testing.T) {
	const termID = "term-3"
	tr, _, deps, events, chatID := primeToolCall(t)
	deps.terminals[termID] = termRendered{text: "line 1\nline 2\nline 3\n"}

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{
			{"type": "terminal", "terminalId": termID},
			{"type": "content", "content": map[string]any{"text": "line 1"}},
		},
	}), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	tc, _ := lastToolCallUpdate(t, deps, events)
	if tc.Output != "line 1\nline 2\nline 3\n" {
		t.Errorf("output = %q, want the terminal's full text rather than the fragment", tc.Output)
	}
}

// A type:"terminal" block's own text is never folded into the output delta: the
// bytes arrive on the terminal/* surface, so consuming both would double-render.
func TestParseToolUpdateContent_TerminalBlockContributesNoOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{
			{
				"type": "terminal", "terminalId": "term-4",
				"content": map[string]any{"text": "SHOULD NOT APPEAR"},
			},
		},
	}), FrameAttribution{})
	tc, _ := lastToolCallUpdate(t, deps, events)
	if strings.Contains(tc.Output, "SHOULD NOT APPEAR") {
		t.Errorf("output = %q, want the terminal block's text left to the stream", tc.Output)
	}
}

// TestAdoptTerminalOutput_MissIsLogged pins the log on a link resolving to no record (the only
// diagnostic), and silence in the benign cases. Not parallel: it swaps the slog default.
func TestAdoptTerminalOutput_MissIsLogged(t *testing.T) {
	const warning = "terminal output missing at completion"
	tests := []struct {
		name string
		// terminal, when non-nil, is registered under the id the tool call links.
		terminal *termRendered
		// priorOutput arrives on an earlier frame: a same-frame block folds AFTER adoption.
		priorOutput  string
		linkTerminal bool
		wantWarn     bool
		wantBytes    string
		reason       string
	}{{
		name: "GoneAndNothingToShow", linkTerminal: true, wantWarn: true, wantBytes: "output_bytes=0",
		reason: "the record is absent and the card will render empty: the actionable miss",
	}, {
		name: "GoneWithAFragmentAlreadyOnTheCall", linkTerminal: true,
		priorOutput: "partial", wantWarn: true, wantBytes: "output_bytes=8",
		reason: "still a miss (the full output is lost); the byte count is what tells a reader it is a fragment",
	}, {
		name: "Found", linkTerminal: true, terminal: &termRendered{text: "hi\n"}, wantWarn: false,
		reason: "adoption succeeded",
	}, {
		name: "FoundButEmpty", linkTerminal: true, terminal: &termRendered{text: ""}, wantWarn: false,
		reason: "a genuinely silent command is registered and empty, not missing —" +
			" and `mkdir -p` is common enough that warning here would drown the signal",
	}, {
		name: "NoTerminalAtAll", wantWarn: false,
		reason: "most tool calls have no terminal; warning here would drown the signal",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const termID = "term-1"
			var logs bytes.Buffer
			t.Cleanup(captureSlog(&logs))

			tr, _, deps, _, chatID := primeToolCall(t)
			if tt.terminal != nil {
				deps.terminals[termID] = *tt.terminal
			}
			if tt.priorOutput != "" {
				tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
					"toolCallId": "tc-1", "status": "in_progress",
					"content": []map[string]any{
						{"type": "content", "content": map[string]any{"text": tt.priorOutput}},
					},
				}), FrameAttribution{})
			}
			update := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			if tt.linkTerminal {
				update["content"] = []map[string]any{
					{"type": "terminal", "terminalId": termID},
				}
			}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, update), FrameAttribution{})

			if got := strings.Contains(logs.String(), warning); got != tt.wantWarn {
				t.Errorf("logged miss = %v, want %v (%s)\nlogs: %s",
					got, tt.wantWarn, tt.reason, logs.String())
			}
			if !tt.wantWarn {
				return
			}
			// The terminal id and byte count separate an empty card from a surviving fragment.
			for _, want := range []string{
				"terminal_id=" + termID, "tool_call_id=tc-1",
				"chat_id=" + string(chatID), tt.wantBytes,
			} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log line missing %q, want it for diagnosis\nlogs: %s", want, logs.String())
				}
			}
		})
	}
}

// The link is also taken from the INITIAL tool_call frame, so the card can
// subscribe to the live stream from its first paint rather than after an update.
func TestHandleToolCall_TakesTheTerminalLinkFromTheCreateFrame(t *testing.T) {
	deps, _, events := newLineCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleToolCall(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-9", "title": "run", "kind": "execute", "status": "in_progress",
		"content": []map[string]any{{"type": "terminal", "terminalId": "term-9"}},
	}), FrameAttribution{})

	for _, e := range *events {
		p, ok := e.Payload.(marotte.EntryAppendedPayload)
		if !ok || p.Entry.Kind != marotte.EntryKindToolCall {
			continue
		}
		call := decodePayload[marotte.EntryToolCall](t, &p.Entry)
		if call.TerminalID != "term-9" {
			t.Errorf("terminal_id = %q, want %q on the create's own entry", call.TerminalID, "term-9")
		}
		return
	}
	t.Fatalf("no entry_appended{tool_call} frame was broadcast: %v", eventTypes(*events))
}

// TestHandleToolCallUpdate_ASecondTerminalFrameForASettledCallAppendsNothing pins that the
// first terminal frame settles the call and a second appends nothing.
func TestHandleToolCallUpdate_ASecondTerminalFrameForASettledCallAppendsNothing(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)

	completed := mustJSON(t, map[string]any{"toolCallId": "tc-1", "status": "completed"})
	tr.HandleToolCallUpdate(t.Context(), chatID, completed, FrameAttribution{})

	first, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_result for the first terminal frame")
	}
	*events = nil

	tr.HandleToolCallUpdate(t.Context(), chatID, completed, FrameAttribution{})

	if len(*events) != 0 {
		t.Errorf("second terminal frame broadcast %v, want nothing: the call is settled", eventTypes(*events))
	}
	if results := toolResultsOf(t, deps.chatEntries(chatID)); len(results) != 1 || results[0].DurationMs != first.DurationMs {
		t.Errorf("tool_result entries = %+v, want the one settled result kept from the first frame", results)
	}
}
