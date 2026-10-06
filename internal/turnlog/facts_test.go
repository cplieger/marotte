package turnlog

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

var approvalOptions = []AskOption{
	{ID: "accept", Kind: "allow_once", Name: "Yes"},
	{ID: "always", Kind: "allow_always", Name: "Always"},
	{ID: "reject", Kind: "reject_once", Name: "No"},
}

// resultInteraction is the interaction the tool_result at entry k carries.
func resultInteraction(t *testing.T, rec *recorder, k int) *marotte.ToolInteraction {
	t.Helper()
	var res marotte.EntryToolResult
	if err := json.Unmarshal(rec.entries[k].Payload, &res); err != nil {
		t.Fatalf("parse tool_result %d: %v", k, err)
	}
	return res.Interaction
}

// A settled call's result says how its ask was answered: by option kind, by label or typed
// text, or by nothing when unanswered.
func TestToolResultCarriesHowTheAskWasAnswered(t *testing.T) {
	tests := []struct {
		want     *marotte.ToolInteraction
		name     string
		kind     string
		outcome  string
		selected string
		options  []AskOption
		asked    bool
	}{
		{
			name: "an approval records the chosen option's kind", kind: "tool_approval",
			options: approvalOptions, asked: true, outcome: "selected", selected: "always",
			want: &marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "allow_always"},
		},
		{
			name: "a question answered by option records its label", kind: "user_input",
			options: []AskOption{{ID: "o1", Kind: "allow_once", Name: "Use Postgres"}},
			asked:   true, outcome: "selected", selected: "o1",
			want: &marotte.ToolInteraction{Type: "user_input", Outcome: "selected", Choice: "Use Postgres"},
		},
		{
			name: "a question answered by text records the text", kind: "user_input",
			asked: true, outcome: "answered", selected: "ship it on Friday",
			want: &marotte.ToolInteraction{Type: "user_input", Outcome: "answered", Choice: "ship it on Friday"},
		},
		{
			name: "a cancelled ask records nothing", kind: "tool_approval",
			options: approvalOptions, asked: true, outcome: "cancelled",
		},
		{
			name:    "an answer to an ask the turn never saw records nothing",
			outcome: "selected", selected: "always",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			turn, rec := open(t)
			rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "c1"}))
			if tt.asked {
				turn.NoteAsk("c1", tt.kind, tt.options)
			}
			turn.NoteAnswer("c1", tt.outcome, tt.selected)
			rec.seal(turn.ToolResult(ctx, "", "c1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}))

			got := resultInteraction(t, rec, 1)
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("tool_result.interaction = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A call the turn closed before it settled still records the answer it got: an
// approved command that was then cut short was approved.
func TestAbortedResultCarriesTheAnswer(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.seal(turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "c1"}))
	turn.NoteAsk("c1", "tool_approval", approvalOptions)
	turn.NoteAnswer("c1", "selected", "reject")
	rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCancelled}))

	want := marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "reject_once"}
	if got := resultInteraction(t, rec, 1); got == nil || *got != want {
		t.Errorf("aborted tool_result.interaction = %+v, want %+v", got, want)
	}
}

// The turn_close carries every completion frame's facts: request ids and
// recoveries once each, tokens and streaming time summed, steering once each.
func TestCloseCarriesTheCompletionFacts(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	turn.NoteTurnCompletion([]string{"r1", "r2"}, []string{"empty"},
		&marotte.TurnThroughput{EstimatedTokens: 100, ActiveStreamingMs: 1000})
	turn.NoteTurnCompletion([]string{"r2", "r3", ""}, []string{"empty", "truncation"},
		&marotte.TurnThroughput{EstimatedTokens: 50, ActiveStreamingMs: 500})
	turn.NoteTurnCompletion(nil, nil, nil)
	turn.NoteSteering([]string{"file:///w/.kiro/steering/a.md", "file:///w/.kiro/steering/a.md"})
	turn.NoteSteering([]string{"file:///w/.kiro/steering/b.md"})
	rec.seal(turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))

	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(rec.entries[0].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	if want := []string{"r1", "r2", "r3"}; !slices.Equal(footer.RequestIDs, want) {
		t.Errorf("request_ids = %v, want %v", footer.RequestIDs, want)
	}
	if want := []string{"empty", "truncation"}; !slices.Equal(footer.Recoveries, want) {
		t.Errorf("recoveries = %v, want %v", footer.Recoveries, want)
	}
	if want := (marotte.TurnThroughput{EstimatedTokens: 150, ActiveStreamingMs: 1500}); footer.Throughput == nil || *footer.Throughput != want {
		t.Errorf("throughput = %+v, want %+v", footer.Throughput, want)
	}
	if want := []string{"file:///w/.kiro/steering/a.md", "file:///w/.kiro/steering/b.md"}; !slices.Equal(footer.Steering, want) {
		t.Errorf("steering = %v, want %v", footer.Steering, want)
	}
}

// A turn with no completion facts closes with none, so the footer shows no rows.
func TestCloseWithoutCompletionFactsCarriesNone(t *testing.T) {
	turn, rec := open(t)
	rec.seal(turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}))
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(rec.entries[0].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	if footer.Throughput != nil || footer.RequestIDs != nil || footer.Recoveries != nil || footer.Steering != nil {
		t.Errorf("an empty turn's footer carries %+v, want no completion facts", footer)
	}
}

// The engine's class reaches the footer only for a broken turn and only when it
// is a class marotte knows; a minified name or a completed turn shows none.
func TestCloseNamesAKnownEngineClassOnABrokenTurn(t *testing.T) {
	tests := []struct {
		name      string
		errorType string
		want      string
		outcome   marotte.TurnOutcome
	}{
		{name: "a known class on a failed turn", errorType: "ModelThrottleError", outcome: marotte.TurnOutcomeFailed, want: "ModelThrottleError"},
		{name: "a minified class on a failed turn", errorType: "ge", outcome: marotte.TurnOutcomeFailed},
		{name: "a known class on a completed turn", errorType: "ModelThrottleError", outcome: marotte.TurnOutcomeCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn, rec := open(t)
			turn.SetEngineError(marotte.EngineError{Message: "boom", ErrorType: tt.errorType})
			rec.seal(turn.Close(t.Context(), marotte.TurnConclusion{Outcome: tt.outcome}))
			var footer marotte.EntryTurnClose
			if err := json.Unmarshal(rec.entries[0].Payload, &footer); err != nil {
				t.Fatalf("parse turn_close: %v", err)
			}
			if footer.EngineErrorClass != tt.want {
				t.Errorf("engine_error_class = %q, want %q", footer.EngineErrorClass, tt.want)
			}
		})
	}
}
