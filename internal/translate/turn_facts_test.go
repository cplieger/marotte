package translate

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// liveInfoFrame builds a session_info_update the way the live emitter does: the
// event's fields flat beside its kind, plus legacyFields' nested block when the
// kind has one.
func liveInfoFrame(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": fields}})
}

func pendingApprovalFrame(t *testing.T, callID string) json.RawMessage {
	t.Helper()
	ask := map[string]any{
		"toolCallId": callID, "interactionType": "tool_approval", "question": "Run rm?",
		"options": []map[string]any{
			{"optionId": "accept", "name": "Yes", "kind": "allow_once"},
			{"optionId": "always", "name": "Always", "kind": "allow_always"},
			{"optionId": "reject", "name": "No", "kind": "reject_once"},
		},
	}
	fields := map[string]any{"kind": "pending_interaction", "pendingInteraction": ask}
	maps.Copy(fields, ask)
	return liveInfoFrame(t, fields)
}

func resolvedFrame(t *testing.T, callID, outcome, selected string) json.RawMessage {
	t.Helper()
	r := map[string]any{"toolCallId": callID, "outcome": outcome, "selectedOption": selected}
	fields := map[string]any{"kind": "interaction_resolved", "interactionResolved": r}
	maps.Copy(fields, r)
	return liveInfoFrame(t, fields)
}

// toolResultInteraction is the interaction the turn's one tool_result carries.
func toolResultInteraction(t *testing.T, entries []marotte.Entry) *marotte.ToolInteraction {
	t.Helper()
	results := entriesOfKind(entries, marotte.EntryKindToolResult)
	if len(results) != 1 {
		t.Fatalf("turn sealed %d tool_result entries, want 1", len(results))
	}
	var res marotte.EntryToolResult
	if err := json.Unmarshal(results[0].Payload, &res); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	return res.Interaction
}

// An approval the user answered "always" is recorded on the call's result, read
// against the options the live pending_interaction frame named.
func TestHandleSessionInfoUpdate_AnsweredApprovalReachesTheToolResult(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	ctx := t.Context()
	turn := startedTurn(deps, "c1")

	tr.HandleToolCall(ctx, "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "title": "rm", "kind": "execute", "status": "pending",
	}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(ctx, "c1", pendingApprovalFrame(t, "tc-1"), FrameAttribution{})
	tr.HandleSessionInfoUpdate(ctx, "c1", resolvedFrame(t, "tc-1", "selected", "always"), FrameAttribution{})
	tr.HandleToolCallUpdate(ctx, "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	want := marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "allow_always"}
	if got := toolResultInteraction(t, deps.turns.entriesOf(turn)); got == nil || *got != want {
		t.Errorf("tool_result.interaction = %+v, want %+v", got, want)
	}
}

// A workflow step's ask is its run's, so it records nothing on the chat's turn.
func TestHandleSessionInfoUpdate_AStepsAskStaysOffTheChatsTurn(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	ctx := t.Context()
	turn := startedTurn(deps, "c1")
	step := FrameAttribution{Step: true, RunID: "wf_1"}

	tr.HandleToolCall(ctx, "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "title": "rm", "kind": "execute", "status": "pending",
	}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(ctx, "c1", pendingApprovalFrame(t, "tc-1"), step)
	tr.HandleSessionInfoUpdate(ctx, "c1", resolvedFrame(t, "tc-1", "selected", "always"), step)
	tr.HandleToolCallUpdate(ctx, "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	if got := toolResultInteraction(t, deps.turns.entriesOf(turn)); got != nil {
		t.Errorf("tool_result.interaction = %+v, want none from a step's frames", got)
	}
}

// The live turn_completion's request ids, throughput and recoveries, and the
// steering KAS added, reach the turn's turn_close.
func TestHandleSessionInfoUpdate_CompletionFactsReachTheTurnClose(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	ctx := t.Context()
	turn := startedTurn(deps, "c1")

	tr.HandleSessionInfoUpdate(ctx, "c1", liveInfoFrame(t, map[string]any{
		"kind": "steering_inclusion", "steeringDocuments": []string{"file:///workspace/.kiro/steering/go.md"},
	}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(ctx, "c1", liveInfoFrame(t, map[string]any{
		"kind":                "turn_completion",
		"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.5}},
		"elapsedTime":         1200,
		"status":              "success",
		"requestIds":          []string{"req-1", "req-2"},
		"recoveries":          []string{"empty"},
		"throughput":          map[string]any{"estimatedTokens": 420, "activeStreamingMs": 3000, "chunks": 9},
	}), FrameAttribution{})
	if _, err := turn.Close(ctx, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
		t.Fatalf("close: %v", err)
	}

	closes := entriesOfKind(deps.turns.entriesOf(turn), marotte.EntryKindTurnClose)
	if len(closes) != 1 {
		t.Fatalf("turn sealed %d turn_close entries, want 1", len(closes))
	}
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(closes[0].Payload, &footer); err != nil {
		t.Fatalf("parse turn_close: %v", err)
	}
	if want := []string{"req-1", "req-2"}; !slices.Equal(footer.RequestIDs, want) {
		t.Errorf("request_ids = %v, want %v", footer.RequestIDs, want)
	}
	if want := []string{"empty"}; !slices.Equal(footer.Recoveries, want) {
		t.Errorf("recoveries = %v, want %v", footer.Recoveries, want)
	}
	if want := (marotte.TurnThroughput{EstimatedTokens: 420, ActiveStreamingMs: 3000}); footer.Throughput == nil || *footer.Throughput != want {
		t.Errorf("throughput = %+v, want %+v", footer.Throughput, want)
	}
	if want := []string{"file:///workspace/.kiro/steering/go.md"}; !slices.Equal(footer.Steering, want) {
		t.Errorf("steering = %v, want %v", footer.Steering, want)
	}
	if footer.Credits != 0.5 {
		t.Errorf("credits = %v, want the completion's 0.5 still metered", footer.Credits)
	}
}

// A replayed session carries the same facts as the live one: the ask and its
// answer reach the call's result, and the completion facts and steering reach the
// turn_close, so a reloaded footer matches the live one.
func TestEntryProjection_ReplaysTheTurnFacts(t *testing.T) {
	info := func(sub string, extra map[string]any) [2]any {
		return pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", sub, extra))
	}
	turns := entryProject([][2]any{
		turnStartFrame(t),
		info(infoKindSteeringInclusion, map[string]any{"steeringDocuments": []string{"file:///w/.kiro/steering/a.md"}}),
		toolCallFrame(t, "call-1", nil),
		info(infoKindPendingInteraction, map[string]any{"pendingInteraction": map[string]any{
			"toolCallId": "call-1", "interactionType": "tool_approval",
			"options": []map[string]any{{"optionId": "reject", "name": "No", "kind": "reject_once"}},
		}}),
		info(infoKindInteractionResolved, map[string]any{"interactionResolved": map[string]any{
			"toolCallId": "call-1", "outcome": "selected", "selectedOption": "reject",
		}}),
		toolUpdateFrame(t, "call-1", "failed", nil),
		info(infoKindTurnCompletion, map[string]any{
			"promptTurnSummaries": []any{map[string]any{"unit": "credit", "usage": 0.25}},
			"requestIds":          []string{"req-9"},
			"recoveries":          []string{"streamError"},
			"throughput":          map[string]any{"estimatedTokens": 80, "activeStreamingMs": 400},
		}),
		turnEndFrame(t, "end_turn"),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	want := marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "reject_once"}
	if got := toolResultInteraction(t, turns[0].Entries); got == nil || *got != want {
		t.Errorf("tool_result.interaction = %+v, want %+v", got, want)
	}
	var footer marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &footer)
	if !slices.Equal(footer.RequestIDs, []string{"req-9"}) || !slices.Equal(footer.Recoveries, []string{"streamError"}) {
		t.Errorf("request_ids = %v, recoveries = %v, want the replayed completion's", footer.RequestIDs, footer.Recoveries)
	}
	if want := (marotte.TurnThroughput{EstimatedTokens: 80, ActiveStreamingMs: 400}); footer.Throughput == nil || *footer.Throughput != want {
		t.Errorf("throughput = %+v, want %+v", footer.Throughput, want)
	}
	if !slices.Equal(footer.Steering, []string{"file:///w/.kiro/steering/a.md"}) {
		t.Errorf("steering = %v, want the replayed inclusion", footer.Steering)
	}
}

// A replayed engine error names its class on the broken turn's footer, and only a
// class marotte knows.
func TestEntryProjection_NamesAKnownEngineClass(t *testing.T) {
	for _, tt := range []struct{ errorType, want string }{
		{errorType: "ModelThrottleError", want: "ModelThrottleError"},
		{errorType: "ye"},
	} {
		t.Run(tt.errorType, func(t *testing.T) {
			turns := entryProject([][2]any{
				turnStartFrame(t),
				pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindDisplayError, map[string]any{
					"displayError": map[string]any{"message": "The model is busy.", "errorType": tt.errorType},
				})),
				turnEndFrame(t, "error"),
			})
			var footer marotte.EntryTurnClose
			payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &footer)
			if footer.EngineErrorClass != tt.want {
				t.Errorf("engine_error_class = %q, want %q", footer.EngineErrorClass, tt.want)
			}
		})
	}
}
