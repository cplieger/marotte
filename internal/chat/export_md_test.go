package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestRenderChatMarkdown_FullTranscript(t *testing.T) {
	c := &marotte.Chat{
		ID:            "abc123",
		Name:          "My Chat",
		Model:         "claude-x",
		CurrentModeID: "vibe",
		CreatedAt:     1_700_000_000_000,
		UpdatedAt:     1_700_000_100_000,
		Messages: []marotte.Message{
			{ID: "m1", Role: marotte.RoleUser, Content: "hello there", Ts: 1_700_000_000_000},
			{
				ID:        "m2",
				Role:      marotte.RoleAssistant,
				Content:   "hi back",
				Reasoning: "let me think about this",
				Plan: []marotte.PlanEntry{
					{Content: "step one", Status: marotte.PlanCompleted},
					{Content: "step two", Status: marotte.PlanInProgress},
					{Content: "step three", Status: marotte.PlanPending},
				},
				ToolCalls: []marotte.ToolCall{{
					ID:         "t1",
					Title:      "read file",
					Kind:       marotte.ToolKindRead,
					Status:     marotte.ToolCompleted,
					Output:     "file contents here",
					Input:      json.RawMessage(`{"path":"a.go"}`),
					Locations:  []marotte.ToolLocation{{Path: "a.go", Line: 3}},
					DurationMs: 1234,
				}},
				Ts: 1_700_000_050_000,
			},
			{ID: "m3", Role: marotte.RoleEvent, EventKind: marotte.EventInterrupted, Content: "interrupted by restart", Ts: 1_700_000_060_000},
		},
	}

	md := renderChatMarkdown(c)

	wants := []string{
		"# My Chat",
		"**Chat ID:** `abc123`",
		"**Model:** claude-x",
		"**Mode:** vibe",
		"**Messages:** 3",
		"**Created:** 2023-11-14 22:13:20 UTC",
		"**Updated:** 2023-11-14 22:15:00 UTC",
		"_2023-11-14 22:14:10 UTC_", // the assistant message's own timestamp
		"## User",
		"hello there",
		"## Assistant",
		"hi back",
		"<summary>Reasoning</summary>",
		"let me think about this",
		"**Plan**",
		"- [x] step one",
		"- [ ] step two _(in progress)_",
		"- [ ] step three",
		"<summary>Tool: read file — completed</summary>",
		"Duration: 1234ms",
		"`a.go:3`",
		"file contents here",
		"{\n  \"path\": \"a.go\"\n}", // the JSON input, indented rather than raw
		"## Event: interrupted",
		"interrupted by restart",
	}
	for _, want := range wants {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestRenderChatMarkdown_EmptyMessages(t *testing.T) {
	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Empty"})
	if !strings.Contains(md, "# Empty") {
		t.Errorf("missing title: %q", md)
	}
	if !strings.Contains(md, "_No messages._") {
		t.Errorf("missing empty-state marker: %q", md)
	}
}

func TestRenderChatMarkdown_FallbackTitleAndOneLineName(t *testing.T) {
	// Empty name → fallback title; CR/LF in a name must not break the heading.
	if md := renderChatMarkdown(&marotte.Chat{ID: "c1"}); !strings.Contains(md, "# Untitled chat") {
		t.Errorf("missing fallback title: %q", md)
	}
	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "line1\nline2"})
	if !strings.Contains(md, "# line1 line2") {
		t.Errorf("newline in name not collapsed: %q", md)
	}
}

func TestRenderChatMarkdown_SanitisesToolOutput(t *testing.T) {
	// A hidden bidi-control codepoint in tool output must be scrubbed by the
	// sanitize.Output pass the renderer applies.
	c := &marotte.Chat{
		ID: "c1", Name: "S",
		Messages: []marotte.Message{{
			ID: "m1", Role: marotte.RoleAssistant,
			ToolCalls: []marotte.ToolCall{{
				ID: "t1", Title: "run", Status: marotte.ToolCompleted,
				Output: "safe\u202etext", // U+202E RIGHT-TO-LEFT OVERRIDE
			}},
		}},
	}
	md := renderChatMarkdown(c)
	if strings.ContainsRune(md, '\u202e') {
		t.Errorf("tool output not sanitised; bidi control leaked into export")
	}
	if !strings.Contains(md, "safe") {
		t.Errorf("expected sanitised output to keep visible text: %q", md)
	}
}

func TestFencedCode_ExpandsFenceForEmbeddedBackticks(t *testing.T) {
	// Content containing a triple-backtick run must be wrapped in a longer
	// (4-backtick) fence so it can't close the block early.
	out := fencedCode("before ``` after", "")
	if !strings.HasPrefix(out, "````\n") {
		t.Errorf("want 4-backtick fence, got prefix %q", out[:min(8, len(out))])
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "````") {
		t.Errorf("closing fence not expanded: %q", out)
	}
}

func TestFencedCode_DefaultThreeBacktickFence(t *testing.T) {
	out := fencedCode("plain content", "json")
	if !strings.HasPrefix(out, "```json\n") {
		t.Errorf("want ```json fence, got %q", out[:min(10, len(out))])
	}
}

func TestMdTimestamp_ZeroIsEmpty(t *testing.T) {
	if got := mdTimestamp(0); got != "" {
		t.Errorf("mdTimestamp(0) = %q, want empty", got)
	}
	if got := mdTimestamp(1_700_000_000_000); !strings.HasSuffix(got, "UTC") {
		t.Errorf("mdTimestamp = %q, want UTC-suffixed datetime", got)
	}
}

// The export omits what a message does not carry. Rendering an absent plan, a
// zero duration or a line-less location produces sections and coordinates that
// were never in the transcript, which is worse than saying nothing.
func TestRenderChatMarkdown_OmitsWhatTheMessageDoesNotCarry(t *testing.T) {
	c := &marotte.Chat{
		ID:   "c1",
		Name: "Sparse",
		Messages: []marotte.Message{{
			ID:      "m1",
			Role:    marotte.RoleAssistant,
			Content: "done",
			ToolCalls: []marotte.ToolCall{{
				ID:        "t1",
				Title:     "grep",
				Status:    marotte.ToolCompleted,
				Locations: []marotte.ToolLocation{{Path: "whole/file.go"}},
			}},
		}},
	}

	md := renderChatMarkdown(c)

	unwanted := []string{
		"**Plan**",   // the message carries no plan
		"Duration:",  // the tool call was never timed
		"file.go:0`", // a location with no line is a file, not line zero
	}
	for _, bad := range unwanted {
		if strings.Contains(md, bad) {
			t.Errorf("markdown contains %q for a message that carries none\n---\n%s", bad, md)
		}
	}
	if !strings.Contains(md, "`whole/file.go`") {
		t.Errorf("markdown missing the bare location path\n---\n%s", md)
	}
}

// A steer is the reader's own words arriving mid-turn, and whether the agent read
// it is the fact they most want back. Rendered as `## User` the export said
// neither: a correction the agent never saw read exactly like the prompt above it.
func TestRenderChatMarkdown_DistinguishesASteerAndItsDeliveryState(t *testing.T) {
	c := &marotte.Chat{
		ID:   "c1",
		Name: "Steered",
		Messages: []marotte.Message{
			{ID: "u1", Role: marotte.RoleUser, Content: "go"},
			{
				ID: "steer-1", Role: marotte.RoleUser, Content: "use tabs",
				UserKind: marotte.UserKindSteer, SteerState: marotte.SteerStateRead,
			},
			{
				ID: "steer-2", Role: marotte.RoleUser, Content: "actually target main",
				UserKind: marotte.UserKindSteer, SteerState: marotte.SteerStateDropped,
			},
			// The whole legacy population, plus every row the replay projection
			// writes: the state is not known, so the heading claims neither.
			{
				ID: "steer-3", Role: marotte.RoleUser, Content: "and rename it",
				UserKind: marotte.UserKindSteer,
			},
		},
	}

	md := renderChatMarkdown(c)

	for _, want := range []string{
		"## User\n\ngo",
		"## User (mid-turn)\n\nuse tabs",
		"## User (mid-turn, not delivered)\n\nactually target main",
		"## User (mid-turn)\n\nand rename it",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
	// Every steer's text survives whatever its heading says: losing a word is
	// worse than an ambiguous label.
	if n := strings.Count(md, "## User"); n != 4 {
		t.Errorf("user headings = %d, want 4", n)
	}
}
