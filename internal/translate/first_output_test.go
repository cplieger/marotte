package translate

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

//go:fix inline
func stamp(v float64) *float64 { return new(v) }

func TestFirstOutputFrom_KeepsOnlyOrderedSpans(t *testing.T) {
	const start, sent, first, shown = 1_000_000.0, 1_000_900.4, 1_003_700.9, 1_005_200.7
	tests := []struct {
		in   *acpTurnTiming
		want *marotte.TurnFirstOutput
		name string
	}{
		{
			name: "all four stamps give three spans in whole ms",
			in:   &acpTurnTiming{stamp(start), stamp(sent), stamp(first), stamp(shown)},
			want: &marotte.TurnFirstOutput{TotalMs: 5200, SetupMs: 900, ModelMs: 2800},
		},
		{
			name: "no middle stamps give the total only",
			in:   &acpTurnTiming{PromptReceivedAt: stamp(start), FirstChunkEmittedAt: stamp(shown)},
			want: &marotte.TurnFirstOutput{TotalMs: 5200},
		},
		{
			name: "a request sent after the chunk withholds setup and model",
			in:   &acpTurnTiming{stamp(start), stamp(shown + 1), stamp(shown + 2), stamp(shown)},
			want: &marotte.TurnFirstOutput{TotalMs: 5200},
		},
		{
			name: "a first token before the request withholds model",
			in:   &acpTurnTiming{stamp(start), stamp(sent), stamp(sent - 1), stamp(shown)},
			want: &marotte.TurnFirstOutput{TotalMs: 5200, SetupMs: 900},
		},
		{
			name: "a request sent before the prompt withholds setup",
			in:   &acpTurnTiming{stamp(start), stamp(start - 5), stamp(first), stamp(shown)},
			want: &marotte.TurnFirstOutput{TotalMs: 5200, ModelMs: 3705},
		},
		{name: "a chunk before the prompt gives nothing", in: &acpTurnTiming{PromptReceivedAt: stamp(shown), FirstChunkEmittedAt: stamp(start)}},
		{name: "no prompt stamp gives nothing", in: &acpTurnTiming{FirstChunkEmittedAt: stamp(shown)}},
		{name: "no chunk stamp gives nothing", in: &acpTurnTiming{PromptReceivedAt: stamp(start)}},
		{name: "a zero stamp gives nothing", in: &acpTurnTiming{PromptReceivedAt: stamp(0), FirstChunkEmittedAt: stamp(shown)}},
		{name: "an infinite stamp gives nothing", in: &acpTurnTiming{PromptReceivedAt: stamp(start), FirstChunkEmittedAt: new(math.Inf(1))}},
		{name: "no block gives nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstOutputFrom(tc.in)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("firstOutputFrom = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func ttftBlock(start, sent, first, shown float64) map[string]any {
	return map[string]any{
		"turnId": "turn-1", "promptReceivedAt": start, "modelRequestSentAt": sent,
		"firstTokenReceivedAt": first, "firstChunkEmittedAt": shown,
		"agentOutput": map[string]any{"complete": true, "intervals": []any{}},
	}
}

func closedFirstOutput(t *testing.T, deps *baseDeps, turn *turnlog.Turn) *marotte.TurnFirstOutput {
	t.Helper()
	if _, err := turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
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
	return footer.FirstOutput
}

// A steer's continuation is a second KAS execution with its own stamp; the turn keeps the first.
func TestHandleAssistantChunk_FirstOutputReachesTheTurnClose(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := startedTurn(deps, "c1")
	chunk := func(text string, ttft map[string]any) json.RawMessage {
		return mustJSON(t, map[string]any{
			"content": map[string]any{"type": "text", "text": text},
			"_meta":   map[string]any{"kiro": map[string]any{"ttft": ttft}},
		})
	}
	tr.HandleAssistantChunk(t.Context(), "c1", chunk("Hello", ttftBlock(1000, 1900, 4700, 5200)), false, FrameAttribution{})
	tr.HandleAssistantChunk(t.Context(), "c1", chunk(" again", ttftBlock(9000, 9100, 9200, 9300)), false, FrameAttribution{})

	want := marotte.TurnFirstOutput{TotalMs: 4200, SetupMs: 900, ModelMs: 2800}
	if got := closedFirstOutput(t, deps, turn); got == nil || *got != want {
		t.Errorf("first_output = %+v, want the first chunk's %+v", got, want)
	}
}

// KAS sends turn_timing live only when persisting the chunk's stamp failed.
func TestHandleSessionInfoUpdate_TurnTimingFillsTheFirstOutput(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	turn := startedTurn(deps, "c1")
	tr.HandleSessionInfoUpdate(t.Context(), "c1", liveInfoFrame(t, map[string]any{
		"kind": "turn_timing", "turnId": "turn-1", "ttft": ttftBlock(1000, 1900, 4700, 5200),
	}), FrameAttribution{})

	want := marotte.TurnFirstOutput{TotalMs: 4200, SetupMs: 900, ModelMs: 2800}
	if got := closedFirstOutput(t, deps, turn); got == nil || *got != want {
		t.Errorf("first_output = %+v, want the turn_timing frame's %+v", got, want)
	}
}

func TestEntryProjection_ReplaysTheFirstOutputOntoTheCloser(t *testing.T) {
	timing := func(start float64) [2]any {
		return pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_timing", map[string]any{
			"ttft": ttftBlock(start, 1900, 4700, 5200),
		}))
	}
	turns := entryProject([][2]any{
		timing(500),
		turnStartFrame(t),
		agentChunkFrame(t, "working"),
		timing(1000),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_completion", map[string]any{
			"promptTurnSummaries": []any{},
		})),
		turnEndFrame(t, "end_turn"),
	})
	if len(turns) != 1 {
		t.Fatalf("projection gave %d turns, want 1: a turn_timing before any turn opens none", len(turns))
	}
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	want := marotte.TurnFirstOutput{TotalMs: 4200, SetupMs: 900, ModelMs: 2800}
	if payload.FirstOutput == nil || *payload.FirstOutput != want {
		t.Errorf("replayed first_output = %+v, want the turn's own %+v, never a stamp from before it", payload.FirstOutput, want)
	}
}
