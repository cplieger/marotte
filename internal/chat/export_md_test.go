package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// stampedAt sets every entry's ts to ms, which the export reads for the heading timestamp.
func stampedAt(entries []marotte.Entry, ms int64) []marotte.Entry {
	for i := range entries {
		entries[i].Ts = ms
	}
	return entries
}

func TestRenderChatMarkdown_FullTranscript(t *testing.T) {
	c := &marotte.Chat{
		ID:            "abc123",
		Name:          "My Chat",
		Model:         "claude-x",
		CurrentModeID: "vibe",
		CreatedAt:     1_700_000_000_000,
		UpdatedAt:     1_700_000_100_000,
	}
	entries, _ := chatOf(openTurn("t-1", 1, prompt("m-1", "hello there")).
		thinking("th1", "let me think about this").
		text("a1", "hi back").
		add("", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
			{Content: "step one", Status: marotte.PlanCompleted},
			{Content: "step two", Status: marotte.PlanInProgress},
			{Content: "step three", Status: marotte.PlanPending},
		}}).
		tool(marotte.EntryToolCall{ID: "tc1", Title: "read file", Kind: marotte.ToolKindRead, Input: json.RawMessage(`{"path":"a.go"}`)},
			&marotte.EntryToolResult{
				Status: marotte.ToolCompleted, Output: "file contents here",
				Locations: []marotte.ToolLocation{{Path: "a.go", Line: 3}}, DurationMs: 1234,
			}).
		close(marotte.EntryTurnClose{
			Outcome: marotte.TurnOutcomeInterrupted, FailureReason: "interrupted by restart",
			ElapsedMs: 4200, Credits: 0.5, Model: "claude-x",
		}))

	md := renderChatMarkdown(c, stampedAt(entries, 1_700_000_050_000))

	wants := []string{
		"# My Chat",
		"**Chat ID:** `abc123`",
		"**Model:** claude-x",
		"**Mode:** vibe",
		"**Turns:** 1",
		"**Created:** 2023-11-14 22:13:20 UTC",
		"**Updated:** 2023-11-14 22:15:00 UTC",
		"## Turn 1",
		"_2023-11-14 22:14:10 UTC_", // the turn_open's own timestamp
		"**User**\n\nhello there",
		"<summary>Reasoning</summary>",
		"let me think about this",
		"hi back",
		"**Plan**",
		"- [x] step one",
		"- [ ] step two _(in progress)_",
		"- [ ] step three",
		"<summary>Tool: read file — completed</summary>",
		"Duration: 1234ms",
		"`a.go:3`",
		"file contents here",
		"{\n  \"path\": \"a.go\"\n}", // the JSON input, indented rather than raw
		"_Outcome: interrupted · 4.2s · 0.50 credits · claude-x_",
		"_Reason: interrupted by restart_",
	}
	for _, want := range wants {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestRenderChatMarkdown_EmptyLog(t *testing.T) {
	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Empty"}, nil)
	if !strings.Contains(md, "# Empty") {
		t.Errorf("missing title: %q", md)
	}
	if !strings.Contains(md, "_No turns._") {
		t.Errorf("missing empty-state marker: %q", md)
	}
}

func TestRenderChatMarkdown_FallbackTitleAndOneLineName(t *testing.T) {
	if md := renderChatMarkdown(&marotte.Chat{ID: "c1"}, nil); !strings.Contains(md, "# Untitled chat") {
		t.Errorf("missing fallback title: %q", md)
	}
	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "line1\nline2"}, nil)
	if !strings.Contains(md, "# line1 line2") {
		t.Errorf("newline in name not collapsed: %q", md)
	}
}

// A hidden bidi codepoint in tool output is scrubbed by sanitize.Output.
func TestRenderChatMarkdown_SanitisesToolOutput(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, nil).
		tool(marotte.EntryToolCall{ID: "tc1", Title: "run"},
			&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "safe\u202etext"}))
	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "S"}, entries)
	if strings.ContainsRune(md, '\u202e') {
		t.Errorf("tool output not sanitised; bidi control leaked into export")
	}
	if !strings.Contains(md, "safe") {
		t.Errorf("expected sanitised output to keep visible text: %q", md)
	}
}

// A triple-backtick run gets a longer fence.
func TestFencedCode_ExpandsFenceForEmbeddedBackticks(t *testing.T) {
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

// An absent plan, zero duration or line-less location renders nothing rather than invented sections.
func TestRenderChatMarkdown_OmitsWhatTheTurnDoesNotCarry(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, nil).
		text("a1", "done").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "grep"},
			&marotte.EntryToolResult{Status: marotte.ToolCompleted, Locations: []marotte.ToolLocation{{Path: "whole/file.go"}}}))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Sparse"}, entries)

	unwanted := []string{
		"**Plan**",   // the turn carries no plan
		"Duration:",  // the tool call was never timed
		"file.go:0`", // a location with no line is a file, not line zero
	}
	for _, bad := range unwanted {
		if strings.Contains(md, bad) {
			t.Errorf("markdown contains %q for a turn that carries none\n---\n%s", bad, md)
		}
	}
	if !strings.Contains(md, "`whole/file.go`") {
		t.Errorf("markdown missing the bare location path\n---\n%s", md)
	}
}

// A read user steer, a dropped one, an agent note and the main agent's message to a step each get their own heading,
// and every steer's text survives.
func TestRenderChatMarkdown_DistinguishesASteerAndItsDeliveryState(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, prompt("m-1", "go")).
		add("", "steer-1", marotte.EntryKindSteer, marotte.EntrySteer{Text: "use tabs", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}).
		add("", "steer-2", marotte.EntryKindSteer, marotte.EntrySteer{Text: "actually target main", Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped}).
		add("", "notify-1", marotte.EntryKindSteer, marotte.EntrySteer{Text: "the run finished", Origin: marotte.SteerOriginAgent, State: marotte.SteerStateRead, Severity: "info"}).
		add("", "notify-2", marotte.EntryKindSteer, marotte.EntrySteer{Text: "weigh this input", Origin: marotte.SteerOriginParent, State: marotte.SteerStateRead, Severity: "info"}).
		add("", "notify-3", marotte.EntryKindSteer, marotte.EntrySteer{Text: "and this one", Origin: marotte.SteerOriginParent, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonRestart}))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Steered"}, entries)

	for _, want := range []string{
		"**User**\n\ngo",
		"**User (mid-turn)**\n\nuse tabs",
		"**User (mid-turn, not delivered)**\n\nactually target main",
		"**Agent note**\n\nthe run finished",
		"**Main agent (mid-turn)**\n\nweigh this input",
		"**Main agent (mid-turn, not delivered)**\n\nand this one",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

// A wordless ack adds nothing; asserted as equality with the same turn without it.
func TestRenderChatMarkdown_AWordlessSteerAckAddsNothing(t *testing.T) {
	steer := marotte.EntrySteer{Text: "use tabs", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}

	withAck, _ := chatOf(openTurn("t-1", 1, prompt("m-1", "go")).
		add("", "steer-1", marotte.EntryKindSteer, steer).
		add("", "steer-1:ack", marotte.EntryKindSteerAck, marotte.EntrySteerAck{SteerID: "steer-1"}).
		text("say-1", "done"))
	without, _ := chatOf(openTurn("t-1", 1, prompt("m-1", "go")).
		add("", "steer-1", marotte.EntryKindSteer, steer).
		text("say-1", "done"))

	chat := &marotte.Chat{ID: "c1", Name: "Steered"}
	got := renderChatMarkdown(chat, stampedAt(withAck, 0))
	want := renderChatMarkdown(chat, stampedAt(without, 0))
	if got != want {
		t.Errorf("renderChatMarkdown with a steer_ack = %q, want the same markdown as without it %q", got, want)
	}
	if !strings.Contains(got, "**User (mid-turn)**\n\nuse tabs") {
		t.Errorf("markdown missing the steer the ack acknowledges\n---\n%s", got)
	}
}

// An ack's words are the agent's, rendered as prose at the ack's position.
func TestRenderChatMarkdown_RendersASteerAcksWordsAsAgentProse(t *testing.T) {
	steer := marotte.EntrySteer{Text: "use tabs", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}
	ack := marotte.EntrySteerAck{SteerID: "steer-1", Text: "switching to tabs now"}
	b, _ := chatOf(openTurn("t-1", 1, prompt("m-1", "go")).
		add("", "steer-1", marotte.EntryKindSteer, steer).
		add("", "steer-1:ack", marotte.EntryKindSteerAck, ack).
		text("say-1", "done"))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Steered"}, stampedAt(b, 0))
	want := "**User (mid-turn)**\n\nuse tabs\n\nswitching to tabs now\n\ndone"
	if !strings.Contains(md, want) {
		t.Errorf("renderChatMarkdown with an acknowledged steer = %q, want it to contain %q", md, want)
	}
}

// A revert's rule and words render at the record's position in its carrier; asserted on the boundary, not the payload.
func TestRenderChatMarkdown_StatesTheCutARevertRecorded(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, prompt("m-1", "go")).
		text("say-1", "done").
		add("", "t-2:revert", marotte.EntryKindTurnRevert, marotte.EntryTurnRevert{
			From: "t-2", FromN: 2, Through: "t-2", KASMessageID: "kas-2",
			Cause: marotte.TurnRevertCauseRewind,
		}))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Rewound"}, stampedAt(entries, 0))

	if !strings.Contains(md, "---\n\n**Rewound to here**\n\n") {
		t.Errorf("renderChatMarkdown over a turn_revert = %q, want the boundary rule plus \"Rewound to here\"", md)
	}
	if strings.Index(md, "**Rewound to here**") < strings.Index(md, "done") {
		t.Errorf("renderChatMarkdown put the boundary above the text it follows:\n---\n%s", md)
	}
}

// One kind, two triggers: a model move, an effort-only change, or the context reset an empty target means, mirroring
// the transcript row.
func TestRenderChatMarkdown_SaysWhichModelSwitchedTriggerFired(t *testing.T) {
	cases := []struct {
		name     string
		payload  marotte.EntryModelSwitched
		want     string
		unwanted []string
	}{
		{
			name:    "model_and_tier",
			payload: marotte.EntryModelSwitched{From: "sonnet-5", To: "opus-5", Effort: "high"},
			want:    "**Event: model_switched** sonnet-5 → opus-5 (high)",
		},
		{
			name:     "tier_only",
			payload:  marotte.EntryModelSwitched{From: "opus-5", To: "opus-5", Effort: "high"},
			want:     "**Event: model_switched** reasoning effort: high",
			unwanted: []string{"→"},
		},
		{
			// `effort` is optional on the wire, so an older chat file renders as before.
			name:     "no_tier",
			payload:  marotte.EntryModelSwitched{From: "sonnet-5", To: "opus-5"},
			want:     "**Event: model_switched** sonnet-5 → opus-5",
			unwanted: []string{"(", "switched by KAS"},
		},
		{
			name:    "repinned_by_kas",
			payload: marotte.EntryModelSwitched{From: "opus-5", To: "sonnet-5", Reason: marotte.ModelSwitchReasonUnavailable},
			want:    "**Event: model_switched** opus-5 → sonnet-5, switched by KAS: unavailable",
		},
		{
			name:     "context_reset",
			payload:  marotte.EntryModelSwitched{From: "sonnet-5", To: "", Effort: "high"},
			want:     "**Event: model_switched** context reset",
			unwanted: []string{"→", "high"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, _ := chatOf(openTurn("t-1", 1, nil).
				add("", "switch-1", marotte.EntryKindModelSwitched, tc.payload))

			md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Switched"}, entries)

			if !strings.Contains(md, tc.want) {
				t.Errorf("renderChatMarkdown(%+v) missing %q\n---\n%s", tc.payload, tc.want, md)
			}
			for _, bad := range tc.unwanted {
				if strings.Contains(md, bad) {
					t.Errorf("renderChatMarkdown(%+v) contains %q\n---\n%s", tc.payload, bad, md)
				}
			}
		})
	}
}

// A tool_result folds into its call's block with the settled status and output; an orphaned result renders alone.
func TestRenderChatMarkdown_FoldsAResultIntoItsCallAndRendersAnOrphanAlone(t *testing.T) {
	tf := openTurn("t-1", 1, nil).
		tool(marotte.EntryToolCall{ID: "tc1", Title: "build", Status: marotte.ToolPending},
			&marotte.EntryToolResult{Status: marotte.ToolFailed, Output: "exit 2"})
	tf.add("", "tc9:result", marotte.EntryKindToolResult, marotte.EntryToolResult{Title: "lost call", Status: marotte.ToolCompleted, Output: "orphan output"})
	entries, _ := chatOf(tf)

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Folded"}, entries)

	if n := strings.Count(md, "<summary>Tool: build"); n != 1 {
		t.Errorf("the build call renders %d blocks, want 1 (the result folds into the call)", n)
	}
	for _, want := range []string{
		"<summary>Tool: build — failed</summary>",
		"exit 2",
		"<summary>Tool result: lost call — completed</summary>",
		"orphan output",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, "Tool: build — pending") {
		t.Errorf("the call's own pending status outranked the settled result\n---\n%s", md)
	}
}

// A plan renders once, at the first plan's position, in its newest state.
func TestRenderChatMarkdown_RendersThePlanOnceInItsNewestState(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, nil).
		add("", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "step one", Status: marotte.PlanPending}}}).
		text("a1", "working").
		add("", "plan-2", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "step one", Status: marotte.PlanCompleted}}}))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Planned"}, entries)

	if n := strings.Count(md, "**Plan**"); n != 1 {
		t.Errorf("plan rendered %d times, want 1", n)
	}
	planAt, textAt := strings.Index(md, "**Plan**"), strings.Index(md, "working")
	if planAt < 0 || textAt < 0 || planAt > textAt {
		t.Errorf("plan at %d, text at %d: the plan renders at the FIRST plan entry's position", planAt, textAt)
	}
	if !strings.Contains(md, "- [x] step one") || strings.Contains(md, "- [ ] step one") {
		t.Errorf("plan is not in its newest state\n---\n%s", md)
	}
}

// A delegate's entries say whose words they are; lane "" gets no note.
func TestRenderChatMarkdown_MarksADelegatesLane(t *testing.T) {
	entries, _ := chatOf(openTurn("t-1", 1, nil).
		text("a1", "parent prose").
		laneText("sub-1", "a2", "delegate prose"))

	md := renderChatMarkdown(&marotte.Chat{ID: "c1", Name: "Lanes"}, entries)

	if !strings.Contains(md, "_Delegate `sub-1`_\n\ndelegate prose") {
		t.Errorf("markdown missing the delegate note before its prose\n---\n%s", md)
	}
	if n := strings.Count(md, "_Delegate"); n != 1 {
		t.Errorf("delegate notes = %d, want 1: the parent's prose carries none", n)
	}
}
