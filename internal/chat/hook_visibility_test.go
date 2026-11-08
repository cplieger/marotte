package chat

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// seedHookChat records one closed turn of calls in the chat's own lane, each with its settled result.
func seedHookChat(t *testing.T, s *Store, id marotte.ChatID, calls []marotte.EntryToolCall, results []marotte.EntryToolResult) {
	t.Helper()
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + string(id), Text: "run the build"},
	}, func(c *marotte.Chat) { c.Name = "Hooks" })
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	for i := range calls {
		if err := s.Append(t.Context(), id, entryOf(opened.Turn, "", calls[i].ID, marotte.EntryKindToolCall, calls[i])); err != nil {
			t.Fatalf("Append(%s): %v", calls[i].ID, err)
		}
		res := entryOf(opened.Turn, "", marotte.ToolResultID(calls[i].ID), marotte.EntryKindToolResult, results[i])
		if err := s.Append(t.Context(), id, res); err != nil {
			t.Fatalf("Append(%s result): %v", calls[i].ID, err)
		}
	}
	closeTurn(t, s, id, opened.Turn, marotte.TurnOutcomeCompleted)
}

func guard(id, trigger string) marotte.EntryToolCall {
	return marotte.EntryToolCall{
		ID: id, Title: "Hook fired: Destructive command guard", Kind: marotte.ToolKindHook,
		Status: marotte.ToolCompleted, HookTrigger: trigger,
	}
}

func command(id string) marotte.EntryToolCall {
	return marotte.EntryToolCall{ID: id, Title: "go test ./...", Kind: marotte.ToolKindExecute, Status: marotte.ToolInProgress}
}

func invocation(id string) marotte.EntryToolCall {
	return marotte.EntryToolCall{
		ID: id, Title: "invokeSubAgent", Kind: marotte.ToolKindOther, Status: marotte.ToolInProgress, AgentSubtaskID: "sub-1",
	}
}

func completed() marotte.EntryToolResult {
	return marotte.EntryToolResult{Status: marotte.ToolCompleted}
}

// With hooks.showStatus off, find counts a hook only where the transcript draws it as a row (static-src/hook-fold.ts).
func TestSearch_HookStatusOffSkipsTheHooksTheTranscriptHides(t *testing.T) {
	const q = "?q=Destructive"
	cases := []struct {
		name    string
		calls   []marotte.EntryToolCall
		results []marotte.EntryToolResult
		shown   bool
		want    int
	}{
		{
			name:    "switch on finds a passing guard",
			calls:   []marotte.EntryToolCall{guard("h1", "PreToolUse"), command("c1")},
			results: []marotte.EntryToolResult{completed(), completed()},
			shown:   true, want: 1,
		},
		{
			name:    "switch off skips a passing guard",
			calls:   []marotte.EntryToolCall{guard("h1", "PreToolUse"), command("c1")},
			results: []marotte.EntryToolResult{completed(), completed()},
			want:    0,
		},
		{
			name:    "switch off skips a passing prompt-submit hook",
			calls:   []marotte.EntryToolCall{guard("h1", "UserPromptSubmit")},
			results: []marotte.EntryToolResult{completed()},
			want:    0,
		},
		{
			name:  "switch off keeps the guards of a denied command",
			calls: []marotte.EntryToolCall{guard("h1", "PreToolUse"), command("c1")},
			results: []marotte.EntryToolResult{completed(), {
				Status: marotte.ToolFailed, Denial: &marotte.ToolDenial{Resource: "rm -rf /"},
			}},
			want: 1,
		},
		{
			name:    "switch off keeps the guards of a failed command",
			calls:   []marotte.EntryToolCall{guard("h1", "PreToolUse"), command("c1")},
			results: []marotte.EntryToolResult{completed(), {Status: marotte.ToolFailed}},
			want:    1,
		},
		{
			name:  "switch off keeps the guards of a command aborted after a rejected ask",
			calls: []marotte.EntryToolCall{guard("h1", "PreToolUse"), command("c1")},
			results: []marotte.EntryToolResult{completed(), {
				Status:      marotte.ToolAborted,
				Interaction: &marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "reject_once"},
			}},
			want: 1,
		},
		{
			name:    "switch off skips the guard of a delegate's invocation",
			calls:   []marotte.EntryToolCall{guard("h1", "PreToolUse"), invocation("i1")},
			results: []marotte.EntryToolResult{completed(), completed()},
			want:    0,
		},
		{
			name:    "switch off keeps the guard of a failed delegate's invocation",
			calls:   []marotte.EntryToolCall{guard("h1", "PreToolUse"), invocation("i1")},
			results: []marotte.EntryToolResult{completed(), {Status: marotte.ToolFailed}},
			want:    1,
		},
		{
			name:    "switch off keeps the after-hook of a failed delegate's invocation",
			calls:   []marotte.EntryToolCall{invocation("i1"), guard("h1", "PostToolUse")},
			results: []marotte.EntryToolResult{{Status: marotte.ToolFailed}, completed()},
			want:    1,
		},
		{
			name:    "switch off skips a failed delegate's guard that belongs to its next call",
			calls:   []marotte.EntryToolCall{invocation("i1"), guard("h1", "PreToolUse")},
			results: []marotte.EntryToolResult{{Status: marotte.ToolFailed}, completed()},
			want:    0,
		},
		{
			name:    "switch off keeps a hook that printed",
			calls:   []marotte.EntryToolCall{guard("h1", "PostToolUse"), command("c1")},
			results: []marotte.EntryToolResult{{Status: marotte.ToolCompleted, Output: "run the linter next"}, completed()},
			want:    1,
		},
		{
			name:    "switch off keeps a failed hook",
			calls:   []marotte.EntryToolCall{guard("h1", "PreToolUse"), command("c1")},
			results: []marotte.EntryToolResult{{Status: marotte.ToolFailed}, completed()},
			want:    1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			shown := tc.shown
			WithHookStatusShown(func() bool { return shown })(s)
			seedHookChat(t, s, "c1", tc.calls, tc.results)
			if got := searchVia(t, s, "c1", q).Matched; got != tc.want {
				t.Errorf("in-chat find matched %d, want %d", got, tc.want)
			}
			wantChats := min(tc.want, 1)
			if got := len(s.searchAll(t.Context(), "Destructive").Matches); got != wantChats {
				t.Errorf("History search found %d chats, want %d", got, wantChats)
			}
		})
	}
}

// A refused card shares a run only with the calls beside it: a guard in an earlier run, cut off by prose, still hides.
func TestSearch_HookStatusOffRefusalKeepsOnlyItsOwnRun(t *testing.T) {
	s, _ := newTestStore(t)
	WithHookStatusShown(func() bool { return false })(s)
	opened, err := s.OpenTurn(t.Context(), "c1", &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-c1", Text: "run the build"},
	}, func(c *marotte.Chat) { c.Name = "Hooks" })
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	turn := opened.Turn
	add := func(id string, kind marotte.EntryKind, payload any) {
		t.Helper()
		if err := s.Append(t.Context(), "c1", entryOf(turn, "", id, kind, payload)); err != nil {
			t.Fatalf("Append(%s): %v", id, err)
		}
	}
	add("h1", marotte.EntryKindToolCall, guard("h1", "PreToolUse"))
	add(marotte.ToolResultID("h1"), marotte.EntryKindToolResult, completed())
	add("c1", marotte.EntryKindToolCall, command("c1"))
	add(marotte.ToolResultID("c1"), marotte.EntryKindToolResult, completed())
	add("say", marotte.EntryKindText, marotte.EntryText{Text: "Now the risky one."})
	add("h2", marotte.EntryKindToolCall, guard("h2", "PreToolUse"))
	add(marotte.ToolResultID("h2"), marotte.EntryKindToolResult, completed())
	add("c2", marotte.EntryKindToolCall, command("c2"))
	add(marotte.ToolResultID("c2"), marotte.EntryKindToolResult, marotte.EntryToolResult{Status: marotte.ToolFailed})
	closeTurn(t, s, "c1", turn, marotte.TurnOutcomeCompleted)

	hits := searchVia(t, s, "c1", "?q=Destructive").Matches
	if len(hits) != 1 || hits[0].EntryID != "h2" {
		t.Errorf("hits are %+v, want one on h2, the guard beside the failed command", hits)
	}
}

// An entry between two halves of a run ends it only if the transcript draws it: a refusal after a row that draws
// nothing keeps the earlier guard as a row too (internal/chat/testdata/tool_runs.json pins the boundaries).
func TestSearch_HookStatusOffRefusalKeepsTheRunTheTranscriptDraws(t *testing.T) {
	type add struct {
		id      string
		kind    marotte.EntryKind
		payload any
	}
	steer := func(id string, origin marotte.SteerOrigin, state marotte.SteerState) add {
		return add{id, marotte.EntryKindSteer, marotte.EntrySteer{Text: "a note", Origin: origin, State: state}}
	}
	revert := func(id string) add {
		return add{id, marotte.EntryKindTurnRevert, marotte.EntryTurnRevert{From: "t-gone", FromN: 1, Through: "t-gone"}}
	}
	cases := []struct {
		name      string
		between   []add
		after     []add
		laterWake *marotte.EntryTurnWake
		want      []string
	}{
		{
			name:    "a steer the reader sees splits the run",
			between: []add{steer("s-1", marotte.SteerOriginUser, marotte.SteerStateRead)},
			want:    []string{"h2"},
		},
		{
			name:    "a step message a later wake draws does not split it",
			between: []add{steer("wfmsg-a", marotte.SteerOriginStep, marotte.SteerStateRead)},
			laterWake: &marotte.EntryTurnWake{
				Reason: marotte.WakeReasonStepMessage, Steers: []marotte.WakeSteer{{ID: "wfmsg-a", Text: "a note"}},
			},
			want: []string{"h1", "h2"},
		},
		{
			name:    "a dropped agent note the turn read does not split it",
			between: []add{steer("notify-a", marotte.SteerOriginAgent, marotte.SteerStateDropped)},
			after: []add{{"wfmsg-d:delivered", marotte.EntryKindSteerDelivered, marotte.EntrySteerDelivered{
				SteerID: "wfmsg-d", KASID: "notify-a", ReadTs: 2,
			}}},
			want: []string{"h1", "h2"},
		},
		{
			name:    "the newest rewind row splits the run",
			between: []add{revert("rv-1")},
			want:    []string{"h2"},
		},
		{
			name:    "an older rewind row does not split it",
			between: []add{revert("rv-1")},
			after:   []add{revert("rv-2")},
			want:    []string{"h1", "h2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			WithHookStatusShown(func() bool { return false })(s)
			opened, err := s.OpenTurn(t.Context(), "c1", &TurnSpec{
				Source: marotte.TurnOpenNamePrompt,
				Prompt: &marotte.EntryPrompt{ID: "m-c1", Text: "run the build"},
			}, func(c *marotte.Chat) { c.Name = "Hooks" })
			if err != nil {
				t.Fatalf("OpenTurn: %v", err)
			}
			entries := []add{
				{"h1", marotte.EntryKindToolCall, guard("h1", "PreToolUse")},
				{marotte.ToolResultID("h1"), marotte.EntryKindToolResult, completed()},
				{"c1", marotte.EntryKindToolCall, command("c1")},
				{marotte.ToolResultID("c1"), marotte.EntryKindToolResult, completed()},
			}
			entries = append(entries, tc.between...)
			entries = append(entries,
				add{"h2", marotte.EntryKindToolCall, guard("h2", "PreToolUse")},
				add{marotte.ToolResultID("h2"), marotte.EntryKindToolResult, completed()},
				add{"c2", marotte.EntryKindToolCall, command("c2")},
				add{marotte.ToolResultID("c2"), marotte.EntryKindToolResult, marotte.EntryToolResult{Status: marotte.ToolFailed}},
			)
			entries = append(entries, tc.after...)
			for _, a := range entries {
				if err := s.Append(t.Context(), "c1", entryOf(opened.Turn, "", a.id, a.kind, a.payload)); err != nil {
					t.Fatalf("Append(%s): %v", a.id, err)
				}
			}
			closeTurn(t, s, "c1", opened.Turn, marotte.TurnOutcomeCompleted)
			if tc.laterWake != nil {
				woke, err := s.OpenTurn(t.Context(), "c1", &TurnSpec{Source: marotte.TurnOpenNameEvent}, nil)
				if err != nil {
					t.Fatalf("OpenTurn(wake): %v", err)
				}
				if err := s.Append(t.Context(), "c1", entryOf(woke.Turn, "", "wake-1", marotte.EntryKindTurnWake, tc.laterWake)); err != nil {
					t.Fatalf("Append(turn_wake): %v", err)
				}
				closeTurn(t, s, "c1", woke.Turn, marotte.TurnOutcomeCompleted)
			}
			var got []string
			for _, h := range searchVia(t, s, "c1", "?q=Destructive").Matches {
				got = append(got, h.EntryID)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("hits on %v, want %v: find counts a guard exactly where the transcript draws it", got, tc.want)
			}
		})
	}
}
