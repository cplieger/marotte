package agent

// The one test that reads what a RELOAD reads: a terminal's output reaching the
// chat's entry log, which is the only thing a reload has. Every other test in this
// package asserts on in-memory state, so nothing else fails if `tc.OutputSpans =
// spans` were deleted outright.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// hubWithRealStore builds a runtime over an on-disk chat store seeded with chat c1.
// It mirrors hubWithBridge but returns the store, because the assertions read the
// log back through it.
func hubWithRealStore(t *testing.T) (*Runtime, *testChatStore) {
	t.Helper()
	cs := newTestChatStore()
	br := newRecordingTermBridge()
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return br }, cs)
	cs.wire(h)
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	return h, cs
}

// seedTerminal registers a live agent terminal holding raw bytes, the way the
// output pump would have.
func seedTerminal(h *Runtime, id string, chatID marotte.ChatID, raw string) {
	term := newAgentTerminal(nil, chatID, 1<<20)
	term.output.Write([]byte(raw))
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	h.agentTerms.terms[id] = term
	h.agentTerms.byChatID[chatID] = append(h.agentTerms.byChatID[chatID], id)
}

// sessionUpdate wraps one ACP session/update frame the way the bridge does.
func sessionUpdate(t *testing.T, raw string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": json.RawMessage(raw)}),
	}
}

// storedToolResult re-reads chat c1's log from DISK and returns its one tool_result.
func storedToolResult(t *testing.T, cs *testChatStore) marotte.EntryToolResult {
	t.Helper()
	results := toolResultsOf(t, logOf(t, cs, "c1"))
	if len(results) != 1 {
		t.Fatalf("persisted tool_result entries = %d, want 1", len(results))
	}
	return results[0]
}

// runTerminalTurn drives a complete terminal-backed tool call to turn end: the
// prompt turn opens, the tool call opens, the caller's frames run, and the turn is
// settled (which is what seals the result into the log).
func runTerminalTurn(t *testing.T, h *Runtime, frames ...string) {
	t.Helper()
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", sessionUpdate(t,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending"}`))
	for _, f := range frames {
		h.translateACPEvent("c1", sessionUpdate(t, f))
	}
	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, &marotte.RPCResponse{})
}

const completedWithTerminal = `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1",` +
	`"status":"completed","content":[{"type":"terminal","terminalId":"term-1"}]}`

// TestPersistedToolCall_CarriesTerminalOutputAndSpans is the reload test. A
// coloured command runs, KAS releases the terminal before reporting the result
// (the real ordering, measured at ~3ms on a live run), the turn ends, and the log
// must carry both the plain text and the style spans.
//
// The spans half is the part no other test covers: without it the card reloads
// as unstyled text, which looks plausible and would ship.
func TestPersistedToolCall_CarriesTerminalOutputAndSpans(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "\x1b[31mred\x1b[0m output\n")

	turnID, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", sessionUpdate(t,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending"}`))
	// KAS releases the terminal before it reports the result. That ordering is
	// the reason retired records exist, so the test has to reproduce it.
	if _, released := h.agentTerms.release("term-1"); !released {
		t.Fatal("release reported the terminal was not present")
	}
	h.translateACPEvent("c1", sessionUpdate(t, completedWithTerminal))
	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, &marotte.RPCResponse{})

	tr := storedToolResult(t, cs)
	if tr.Output != "red output\n" {
		t.Errorf("persisted Output = %q, want %q (a reload renders this and nothing else)",
			tr.Output, "red output\n")
	}
	if len(tr.OutputSpans) == 0 {
		t.Fatal("persisted OutputSpans is empty: the reloaded card renders unstyled, " +
			"and the escape sequences are already gone from the text so nothing can recover them")
	}
	// The offsets must address the text that shipped beside them, or the client
	// paints the wrong range. "red" is the styled run.
	s := tr.OutputSpans[0]
	if s.Start != 0 || s.End != 3 {
		t.Errorf("first span = [%d,%d), want [0,3) covering %q", s.Start, s.End, "red")
	}
	if s.End > len(tr.Output) {
		t.Errorf("span end %d exceeds persisted text length %d", s.End, len(tr.Output))
	}
}

// TestPersistedToolCall_AdoptsWhenLinkAndCompletionShareAFrame pins the fold
// ORDER against the log. One frame can carry both the terminal link and
// `completed`; with the status folded first, adoption looked up an id the tool
// call did not have yet and the output was lost for good.
func TestPersistedToolCall_AdoptsWhenLinkAndCompletionShareAFrame(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "one frame\n")

	runTerminalTurn(t, h, completedWithTerminal)

	if got := storedToolResult(t, cs).Output; got != "one frame\n" {
		t.Errorf("persisted Output = %q, want %q (the link must be adopted before the status fold)",
			got, "one frame\n")
	}
}

// TestPersistedToolCall_TerminalOutputBeatsAContentFragment pins which side
// wins when both have text. An earlier content block is a FRAGMENT of what the
// terminal holds in full, so preferring the tool call persists the fragment and
// silently truncates the record.
func TestPersistedToolCall_TerminalOutputBeatsAContentFragment(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "line 1\nline 2\nline 3\n")

	runTerminalTurn(t, h,
		`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress",`+
			`"content":[{"type":"terminal","terminalId":"term-1"},`+
			`{"type":"content","content":{"text":"line 1"}}]}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}`)

	if got := storedToolResult(t, cs).Output; got != "line 1\nline 2\nline 3\n" {
		t.Errorf("persisted Output = %q, want the terminal's full output; "+
			"a fragment here means the record was truncated to the progress line", got)
	}
}

// TestPersistedToolCall_SurvivesTheTurnBoundary pins the eviction timing. The
// retired record is dropped when the turn's own closer finalizes it, so adoption
// has exactly until turn end to happen.
func TestPersistedToolCall_SurvivesTheTurnBoundary(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "kept\n")
	// KAS's own release lands long before the completion that needs the bytes.
	// Without it nothing enters `retired` and the eviction assertion is vacuous.
	if _, ok := h.agentTerms.release("term-1"); !ok {
		t.Fatal("Setup: release found no terminal, so nothing is retired to evict")
	}

	runTerminalTurn(t, h, completedWithTerminal)

	if got := storedToolResult(t, cs).Output; got != "kept\n" {
		t.Errorf("persisted Output = %q, want %q", got, "kept\n")
	}
	// The record is gone afterwards: the turn is closed, so nothing can adopt it a
	// second time and the map does not grow without bound.
	h.agentTerms.mu.Lock()
	remaining := len(h.agentTerms.retired)
	h.agentTerms.mu.Unlock()
	if remaining != 0 {
		t.Errorf("retired records after turn end = %d, want 0 (the finalizer must evict)", remaining)
	}
}
