package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestHandleToolCall_InternalToolSuppression pins the internal-tool drop: KAS
// announces its session-boot cloud-config fetch as a tool_call tagged
// _meta.kiro.toolId "fetch_cloud_config", its own TUI never renders it, and the
// frame arriving before the prompt's turn used to open a wire turn the prompt
// then displaced — the phantom "Agent-initiated turn". The frame must reach
// neither a turn nor the wire, and its follow-up update must be dropped BEFORE
// the fold target can open a turn for it.
func TestHandleToolCall_InternalToolSuppression(t *testing.T) {
	cloudConfig := map[string]any{
		"toolCallId": "cc-1",
		"title":      "Fetching your cloud config",
		"kind":       "other",
		"status":     "in_progress",
		"_meta": map[string]any{"kiro": map[string]any{
			"toolId": "fetch_cloud_config",
		}},
	}

	t.Run("CallIsDropped", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base))
		chatID := marotte.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, cloudConfig), FrameAttribution{})
		if hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("internal tool call appended a tool_call entry; want suppressed")
		}
		if _, folded := base.lastFold(); folded {
			t.Error("internal tool call asked for a fold target; want dropped before the turn is reached")
		}
		if base.turns.chats[chatID] != nil {
			t.Error("internal tool call opened the chat's turn; want no turn")
		}
	})

	t.Run("UpdateIsDroppedWithoutTouchingTheFoldTarget", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base))
		chatID := marotte.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, cloudConfig), FrameAttribution{})
		for range 2 {
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "cc-1",
				"status":     "completed",
			}), FrameAttribution{})
		}
		if len(*events) != 0 {
			t.Errorf("suppressed internal tool's update broadcast %d events; want none", len(*events))
		}
		// The load-bearing half: the update must not have opened a turn. The fold
		// target opens one per chat on first use, so a chat with no turn is the proof
		// the update read the open turn rather than asking for a fold target.
		if base.turns.chats[chatID] != nil {
			t.Error("the suppressed update opened a turn; want dropped, its create was never sealed")
		}
	})

	t.Run("OrdinaryOtherKindToolIsShown", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base))
		tr.HandleToolCall(t.Context(), marotte.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "ws-1",
			"title":      "web_search",
			"kind":       "other",
			"status":     "in_progress",
			"_meta": map[string]any{"kiro": map[string]any{
				"toolId": "web_search",
			}},
		}), FrameAttribution{})
		if !hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("ordinary kind:other tool call suppressed; only internal toolIds are gated")
		}
	})
}

func TestHandleToolCallUpdate_UnknownToolCallOpensNoTurn(t *testing.T) {
	base, events := newEventCaptureDeps()
	tr := New(rolesOf(base))

	tr.HandleToolCallUpdate(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": "unknown",
		"status":     "completed",
	}), FrameAttribution{})

	if base.turns.chats["c1"] != nil {
		t.Error("unknown tool update opened a turn")
	}
	if len(*events) != 0 {
		t.Errorf("unknown tool update emitted %d events, want 0", len(*events))
	}
}
