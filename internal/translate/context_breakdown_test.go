package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func kasBreakdown() map[string]any {
	return map[string]any{
		"schema": 1, "modelCalls": 2, "totalChars": 9000,
		"window":    map[string]any{"usagePercentage": 12.5, "sizeTokens": 200000},
		"compacted": true,
		"categories": map[string]any{
			"steering": map[string]any{
				"chars": 3000, "percent": 33,
				"items": []any{
					map[string]any{"name": "go\u202e.md", "uri": "file:///w/.kiro/steering/go.md", "chars": 2000, "percent": 22, "inclusion": "fileMatch"},
					map[string]any{"name": "remote", "uri": "https://example.com/x", "chars": 1000, "percent": 11, "inclusion": "always"},
				},
				"omitted": map[string]any{"count": 4, "chars": 120},
			},
			"history": map[string]any{
				"chars": 5000, "percent": 56, "userChars": 1000, "assistantChars": 3500, "thinkingChars": 500, "compactionSummaryChars": 0,
			},
			"futureCategory": map[string]any{"chars": 1000, "percent": 11},
			"mcpTools":       map[string]any{"chars": 0, "percent": 0},
		},
		"media": map[string]any{"images": 1, "imageBytes": 2048, "documents": 0, "documentBytes": 0},
	}
}

func breakdownOf(t *testing.T, v any) *marotte.ContextBreakdown {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return contextBreakdownFrom(raw)
}

func TestContextBreakdownFrom_ReadsKASsDetailedShape(t *testing.T) {
	b := breakdownOf(t, kasBreakdown())
	if b == nil {
		t.Fatal("contextBreakdownFrom = nil, want the breakdown")
	}
	if b.TotalChars != 9000 || b.ModelCalls != 2 || !b.Compacted {
		t.Errorf("totals = %+v, want 9000 chars over 2 calls, compacted", b)
	}
	keys := make([]string, 0, len(b.Categories))
	for _, c := range b.Categories {
		keys = append(keys, c.Key)
	}
	if got := strings.Join(keys, ","); got != "history,steering,futureCategory" {
		t.Errorf("categories = %s, want history,steering,futureCategory: by size, the unknown key kept, the empty one dropped", got)
	}
	history, steering := b.Categories[0], b.Categories[1]
	if len(history.Parts) != 3 || history.Parts[0] != (marotte.ContextPart{Key: "assistant", Chars: 3500}) {
		t.Errorf("history parts = %+v, want assistant 3500 first, then user and thinking", history.Parts)
	}
	if len(steering.Items) != 2 {
		t.Fatalf("steering items = %+v, want 2", steering.Items)
	}
	if got := steering.Items[0]; got.Name != "go .md" || got.URI != "file:///w/.kiro/steering/go.md" || got.Inclusion != "fileMatch" {
		t.Errorf("first steering item = %+v, want the bidi control neutralised and the file: link kept", got)
	}
	if steering.Items[1].URI != "" {
		t.Errorf("second steering item uri = %q, want a non-file: uri dropped", steering.Items[1].URI)
	}
	if steering.OmittedCount != 4 || steering.OmittedChars != 120 {
		t.Errorf("omitted = %d/%d, want 4 items, 120 chars", steering.OmittedCount, steering.OmittedChars)
	}
	if b.Media == nil || b.Media.Images != 1 || b.Media.ImageBytes != 2048 {
		t.Errorf("media = %+v, want one 2048-byte image", b.Media)
	}
}

func TestContextBreakdownFrom_RefusesAnotherSchema(t *testing.T) {
	v := kasBreakdown()
	v["schema"] = 2
	if b := breakdownOf(t, v); b != nil {
		t.Errorf("contextBreakdownFrom(schema 2) = %+v, want nil", b)
	}
	if b := contextBreakdownFrom(nil); b != nil {
		t.Errorf("contextBreakdownFrom(absent) = %+v, want nil", b)
	}
}

func TestContextBreakdownFrom_HoldsItsOwnBounds(t *testing.T) {
	cats := map[string]any{}
	for i := range 40 {
		cats[fmt.Sprintf("c%02d", i)] = map[string]any{"chars": 100 + i, "percent": 1}
	}
	items := make([]any, 0, 50)
	for i := range 50 {
		items = append(items, map[string]any{"name": fmt.Sprintf("t%d", i), "chars": 10, "percent": 1})
	}
	cats["toolIO"] = map[string]any{"chars": 5000, "percent": 50, "items": items}
	b := breakdownOf(t, map[string]any{"schema": 1, "categories": cats})
	if len(b.Categories) != maxBreakdownCategories {
		t.Errorf("categories = %d, want the %d cap", len(b.Categories), maxBreakdownCategories)
	}
	if b.Categories[0].Key != "toolIO" || len(b.Categories[0].Items) != maxBreakdownItems {
		t.Errorf("first category = %s with %d items, want toolIO capped at %d", b.Categories[0].Key, len(b.Categories[0].Items), maxBreakdownItems)
	}
}

func TestHandleSessionInfoUpdate_ContextBreakdownReachesTheTurnClose(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	ctx := t.Context()
	turn := startedTurn(deps, "c1")

	tr.HandleSessionInfoUpdate(ctx, "c1", liveInfoFrame(t, map[string]any{
		"kind":                "turn_completion",
		"promptTurnSummaries": []any{},
		"contextBreakdown":    kasBreakdown(),
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
	if footer.ContextBreakdown == nil || footer.ContextBreakdown.TotalChars != 9000 {
		t.Errorf("context_breakdown = %+v, want the frame's 9000-char breakdown", footer.ContextBreakdown)
	}
}

func TestEntryProjection_ReplaysTheContextBreakdownOntoTheCloser(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "working"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_completion", map[string]any{
			"promptTurnSummaries": []any{},
			"contextBreakdown":    kasBreakdown(),
		})),
		turnEndFrame(t, "end_turn"),
	})
	var payload marotte.EntryTurnClose
	payloadOf(t, entryOfKind(t, turns[0], marotte.EntryKindTurnClose), &payload)
	if payload.ContextBreakdown == nil || len(payload.ContextBreakdown.Categories) != 3 {
		t.Errorf("replayed context_breakdown = %+v, want the three non-empty categories", payload.ContextBreakdown)
	}
}
