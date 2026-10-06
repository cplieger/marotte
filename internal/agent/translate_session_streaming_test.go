package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// plansOf decodes every plan entry in entries, in file order, with its turn.
func plansOf(t *testing.T, entries []marotte.Entry) (turns []string, plans []marotte.EntryPlan) {
	t.Helper()
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindPlan {
			continue
		}
		var p marotte.EntryPlan
		if err := json.Unmarshal(entries[i].Payload, &p); err != nil {
			t.Fatalf("decode plan %q: %v", entries[i].ID, err)
		}
		turns = append(turns, entries[i].Turn)
		plans = append(plans, p)
	}
	return turns, plans
}

func BenchmarkHandleAssistantChunk(b *testing.B) {
	shortChunk, _ := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello wor"},
	})
	longChunk, _ := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": strings.Repeat("x", 4096)},
	})
	reasoningChunk, _ := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": "Let me think about this carefully..."},
	})

	cases := []struct {
		name        string
		raw         json.RawMessage
		isReasoning bool
	}{
		{"short_chunk", shortChunk, false},
		{"long_chunk", longChunk, false},
		{"with_reasoning", reasoningChunk, true},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(b.Context(), "bench", func(c *marotte.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				h.translator.HandleAssistantChunk(b.Context(), "bench", tc.raw, tc.isReasoning, translate.FrameAttribution{})
			}
		})
	}
}

// A repeated plan frame seals nothing (ACP resends the whole array); a changed one appends. The client renders the newest.
func TestHandlePlan_ARepeatedFrameSealsNothingAndAChangedOneAppends(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	pending := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"pending"},{"content":"step 2","priority":"medium","status":"pending"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", pending, translate.FrameAttribution{})
	h.translator.HandlePlan(t.Context(), "c1", pending, translate.FrameAttribution{})

	_, plans := plansOf(t, logOf(t, cs, "c1"))
	if len(plans) != 1 || len(plans[0].Entries) != 2 {
		t.Fatalf("plan entries after two equal frames = %+v, want one plan holding 2 entries", plans)
	}

	done := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"completed"},{"content":"step 2","priority":"medium","status":"completed"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", done, translate.FrameAttribution{})

	_, plans = plansOf(t, logOf(t, cs, "c1"))
	if len(plans) != 2 {
		t.Fatalf("plan entries after a changed frame = %d, want 2: the new state appends", len(plans))
	}
	for i, e := range plans[1].Entries {
		if e.Status != marotte.PlanCompleted {
			t.Errorf("newest plan entry %d status = %q, want %q", i, e.Status, marotte.PlanCompleted)
		}
	}
}

// A plan folds into the chat's own open turn, so each turn keeps its own.
func TestHandlePlan_EachTurnKeepsItsOwnPlan(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	one, _ := h.stagePromptTurn(t, "c1")
	first := json.RawMessage(`{"entries":[{"content":"turn one","priority":"high","status":"pending"}]}`)
	h.translator.HandlePlan(ctx, "c1", first, translate.FrameAttribution{})
	resp := &marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", one, 0, resp)

	two, _ := h.stagePromptTurn(t, "c1")
	second := json.RawMessage(`{"entries":[{"content":"turn two","priority":"high","status":"pending"}]}`)
	h.translator.HandlePlan(ctx, "c1", second, translate.FrameAttribution{})

	turns, plans := plansOf(t, logOf(t, cs, "c1"))
	if len(plans) != 2 {
		t.Fatalf("plan entries = %d, want 2 (one per turn)", len(plans))
	}
	if turns[0] != one || plans[0].Entries[0].Content != "turn one" {
		t.Errorf("first plan = %q in turn %q, want %q in %q", plans[0].Entries[0].Content, turns[0], "turn one", one)
	}
	if turns[1] != two || plans[1].Entries[0].Content != "turn two" {
		t.Errorf("second plan = %q in turn %q, want %q in %q", plans[1].Entries[0].Content, turns[1], "turn two", two)
	}
}

func TestHandleModeUpdate_BroadcastsOnlyOnChange(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.CurrentModeID = "code"
		return true
	})

	before := h.bus.fanout.Position().Head

	// Same mode, no broadcast. current_mode_update keys on currentModeId; modeId is the outbound set_mode field.
	raw := json.RawMessage(`{"currentModeId":"code"}`)
	h.translator.HandleModeUpdate(t.Context(), "c1", raw)
	if head := h.bus.fanout.Position().Head; head != before {
		t.Errorf("expected no broadcast for same mode")
	}

	raw2 := json.RawMessage(`{"currentModeId":"chat"}`)
	h.translator.HandleModeUpdate(t.Context(), "c1", raw2)
	if head := h.bus.fanout.Position().Head; head == before {
		t.Error("expected broadcast for mode change, got none")
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.CurrentModeID != "chat" {
		t.Errorf("mode = %q, want chat", c.CurrentModeID)
	}
}
