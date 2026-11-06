package translate

import (
	"fmt"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestHandleToolCall_InternalToolSuppression pins the internal-tool drop: the session-boot
// fetch_cloud_config call must reach neither a turn nor the wire, and its update must be
// dropped before the fold target can open a turn (the phantom "Agent-initiated turn").
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
		// No turn for the chat proves the update read the open turn rather than a fold target.
		if base.turns.chats[chatID] != nil {
			t.Error("the suppressed update opened a turn; want dropped, its create was never sealed")
		}
	})

	settle := func(t *testing.T, update map[string]any) []marotte.SystemNoticePayload {
		t.Helper()
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base))
		tr.HandleToolCall(t.Context(), "c1", mustJSON(t, cloudConfig), FrameAttribution{})
		update["toolCallId"] = "cc-1"
		tr.HandleToolCallUpdate(t.Context(), "c1", mustJSON(t, update), FrameAttribution{})
		var notices []marotte.SystemNoticePayload
		for _, e := range *events {
			n, ok := e.Payload.(marotte.SystemNoticePayload)
			if !ok {
				t.Errorf("the settle broadcast %s, want only a system notice", e.Type)
				continue
			}
			notices = append(notices, n)
		}
		if base.turns.chats["c1"] != nil {
			t.Error("the settle opened a turn; want none")
		}
		return notices
	}

	t.Run("FailedPullRaisesWarningNotice", func(t *testing.T) {
		const msg = "Couldn't fetch your cloud config. Sign in again to update it."
		got := settle(t, map[string]any{"status": "failed", "rawOutput": msg})
		if len(got) != 1 || got[0].Level != marotte.NoticeWarning || got[0].Message != msg {
			t.Errorf("notices = %+v, want one warning saying %q", got, msg)
		}
	})

	t.Run("SyncedPullRaisesInfoNotice", func(t *testing.T) {
		got := settle(t, map[string]any{"status": "completed", "rawOutput": map[string]any{"kind": "synced", "downloaded": 2, "deleted": 1}})
		if len(got) != 1 || got[0].Level != marotte.NoticeInfo || got[0].Message != "Cloud config synced: 3 files updated." {
			t.Errorf("notices = %+v, want one info notice counting 3 files", got)
		}
	})

	t.Run("UpToDatePullStaysSilent", func(t *testing.T) {
		if got := settle(t, map[string]any{"status": "completed", "rawOutput": map[string]any{"kind": "upToDate"}}); len(got) != 0 {
			t.Errorf("notices = %+v, want none for a pull that changed nothing", got)
		}
	})

	t.Run("NoticeFieldRaisesWarning", func(t *testing.T) {
		const msg = "Cloud config sync is off for your account. Your previously synced settings were removed."
		got := settle(t, map[string]any{"status": "completed", "rawOutput": map[string]any{"kind": "notEnabled", "retracted": true, "notice": msg}})
		if len(got) != 1 || got[0].Level != marotte.NoticeWarning || got[0].Message != msg {
			t.Errorf("notices = %+v, want one warning saying %q", got, msg)
		}
	})

	t.Run("UpdateDuringOpenTurnTouchesNoCall", func(t *testing.T) {
		tr, _, deps, events, chatID := primeToolCall(t)
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1", "title": "Fetching your cloud config", "kind": "other", "status": "in_progress",
			"_meta": map[string]any{"kiro": map[string]any{"toolId": "fetch_cloud_config"}},
		}), FrameAttribution{})
		*events = nil
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1", "status": "completed", "rawOutput": map[string]any{"kind": "upToDate"},
		}), FrameAttribution{})
		if len(*events) != 0 {
			t.Errorf("the internal settle broadcast %d events; want none", len(*events))
		}
		if _, open := deps.turns.chats[chatID].OpenCallFor("tc-1"); !open {
			t.Error("the internal settle closed the turn's own call tc-1; want it left open")
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

// A pull that never settles leaves its id behind; the oldest goes once the set is full.
func TestInternalCalls_EvictsTheOldestPastTheBound(t *testing.T) {
	var c internalCalls
	for i := range maxInternalCalls + 1 {
		c.add(fmt.Sprintf("cc-%d", i), "c1")
	}
	if c.holds("cc-0") {
		t.Error("the oldest id is still held past the bound")
	}
	if !c.holds(fmt.Sprintf("cc-%d", maxInternalCalls)) {
		t.Error("the newest id is not held")
	}
	c.take("cc-1")
	if c.holds("cc-1") {
		t.Error("a taken id is still held")
	}
}
