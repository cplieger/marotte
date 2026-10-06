package agent

// Reads what a reload reads: a terminal's output in the chat's entry log on disk.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// hubWithRealStore builds a runtime over an on-disk chat store seeded with chat c1.
func hubWithRealStore(t *testing.T) (*Runtime, *testChatStore) {
	t.Helper()
	cs := newTestChatStore()
	br := newRecordingTermBridge()
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return br }, cs)
	cs.wire(h)
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	return h, cs
}

// seedTerminal registers a live agent terminal holding raw bytes, as the pump would.
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

// storedToolResult re-reads chat c1's log from disk and returns its one tool_result.
func storedToolResult(t *testing.T, cs *testChatStore) marotte.EntryToolResult {
	t.Helper()
	results := toolResultsOf(t, logOf(t, cs, "c1"))
	if len(results) != 1 {
		t.Fatalf("persisted tool_result entries = %d, want 1", len(results))
	}
	return results[0]
}

// runTerminalTurn drives a terminal-backed tool call to a settled turn end, which seals the result into the log.
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

// TestPersistedToolCall_CarriesTerminalOutputAndSpans is the reload test: KAS releases
// before reporting the result (~3ms, measured), and the log must carry text and spans.
func TestPersistedToolCall_CarriesTerminalOutputAndSpans(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "\x1b[31mred\x1b[0m output\n")

	turnID, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", sessionUpdate(t,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending"}`))
	// KAS releases before it reports the result; the test reproduces that order.
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
	// The offsets must address the text beside them; "red" is the styled run.
	s := tr.OutputSpans[0]
	if s.Start != 0 || s.End != 3 {
		t.Errorf("first span = [%d,%d), want [0,3) covering %q", s.Start, s.End, "red")
	}
	if s.End > len(tr.Output) {
		t.Errorf("span end %d exceeds persisted text length %d", s.End, len(tr.Output))
	}
}

// TestPersistedToolCall_AdoptsWhenLinkAndCompletionShareAFrame pins the fold order: the
// terminal link is folded before `completed`, or adoption finds no id.
func TestPersistedToolCall_AdoptsWhenLinkAndCompletionShareAFrame(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "one frame\n")

	runTerminalTurn(t, h, completedWithTerminal)

	if got := storedToolResult(t, cs).Output; got != "one frame\n" {
		t.Errorf("persisted Output = %q, want %q (the link must be adopted before the status fold)",
			got, "one frame\n")
	}
}

// TestPersistedToolCall_TerminalOutputBeatsAContentFragment pins that the terminal's full
// output wins over an earlier content fragment.
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

// TestPersistedToolCall_SurvivesTheTurnBoundary pins that adoption has until the turn's closer evicts the record.
func TestPersistedToolCall_SurvivesTheTurnBoundary(t *testing.T) {
	h, cs := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "kept\n")
	// Without the release nothing enters `retired` and the eviction assertion is vacuous.
	if _, ok := h.agentTerms.release("term-1"); !ok {
		t.Fatal("Setup: release found no terminal, so nothing is retired to evict")
	}

	runTerminalTurn(t, h, completedWithTerminal)

	if got := storedToolResult(t, cs).Output; got != "kept\n" {
		t.Errorf("persisted Output = %q, want %q", got, "kept\n")
	}
	// Gone afterwards, so nothing adopts twice and the map stays bounded.
	h.agentTerms.mu.Lock()
	remaining := len(h.agentTerms.retired)
	h.agentTerms.mu.Unlock()
	if remaining != 0 {
		t.Errorf("retired records after turn end = %d, want 0 (the finalizer must evict)", remaining)
	}
}
