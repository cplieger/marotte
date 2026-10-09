package translate

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// hookUpdateFrame builds the `update` object of a hook_update session_info_update with
// the hook block where KAS puts it, under `_meta.kiro`.
func hookUpdateFrame(t *testing.T, name, status string) map[string]any {
	t.Helper()
	return map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"kiro": map[string]any{
			"kind": "hook_update",
			"hook": map[string]any{
				"hookId":      "h1",
				"operationId": "op-1",
				"name":        name,
				"status":      status,
				"actionType":  "runCommand",
			},
		}},
	}
}

// hookCardCase drives one hook_update through the translator and returns the events it
// broadcast and the tool_call entries the chat's turn sealed.
func hookCardCase(t *testing.T, enabled bool, frame map[string]any, attr FrameAttribution) (*[]marotte.ServerEvent, []marotte.EntryToolCall) {
	t.Helper()
	base, events := newEventCaptureDeps()
	deps := &hookStatusDeps{baseDeps: base, enabled: enabled}
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	tr.HandleSessionInfoUpdate(t.Context(), chatID, mustJSON(t, frame), attr)
	return events, toolCallsOf(t, base.chatEntries(chatID))
}

// TestHandleSessionInfoUpdate_HookUpdateCard pins the `Hook fired` card: one settled
// tool call per hook_update frame, gated on hooks.showStatus, naming the hook and
// carrying no outcome text.
func TestHandleSessionInfoUpdate_HookUpdateCard(t *testing.T) {
	t.Run("ShownWhenEnabled", func(t *testing.T) {
		events, calls := hookCardCase(t, true, hookUpdateFrame(t, "probe-save", hookStatusCompleted), FrameAttribution{})
		if !hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Fatal("hook_update broadcast no entry_appended{tool_call}; want one Hook fired card")
		}
		if len(calls) != 1 {
			t.Fatalf("buffered tool calls = %d, want 1", len(calls))
		}
		got := calls[0]
		if got.ID != "hook-op-1" {
			t.Errorf("ID = %q, want %q", got.ID, "hook-op-1")
		}
		if got.Kind != marotte.ToolKindHook {
			t.Errorf("Kind = %q, want %q", got.Kind, marotte.ToolKindHook)
		}
		if got.Title != "Hook fired: probe-save" {
			t.Errorf("Title = %q, want %q", got.Title, "Hook fired: probe-save")
		}
		if got.Status != marotte.ToolCompleted {
			t.Errorf("Status = %q, want %q", got.Status, marotte.ToolCompleted)
		}
		if got.Output != "" || got.Input != nil {
			t.Errorf("Output = %q, Input = %s; want no outcome text on the card", got.Output, got.Input)
		}
		for _, word := range []string{"status", "exit", hookStatusCompleted} {
			if strings.Contains(strings.ToLower(got.Title), word) {
				t.Errorf("Title %q carries %q; the card must show no outcome text", got.Title, word)
			}
		}
	})

	t.Run("FailsOnAnyOtherStatus", func(t *testing.T) {
		for _, status := range []string{"", "running", "failed", "canceled", "awaiting_approval", "Success"} {
			t.Run("status_"+status, func(t *testing.T) {
				_, calls := hookCardCase(t, true, hookUpdateFrame(t, "probe", status), FrameAttribution{})
				if len(calls) != 1 {
					t.Fatalf("buffered tool calls = %d, want 1", len(calls))
				}
				if calls[0].Status != marotte.ToolFailed {
					t.Errorf("hookToolStatus(%q) = %q, want %q", status, calls[0].Status, marotte.ToolFailed)
				}
			})
		}
	})

	t.Run("NothingWhenDisabled", func(t *testing.T) {
		events, calls := hookCardCase(t, false, hookUpdateFrame(t, "probe-save", hookStatusCompleted), FrameAttribution{})
		if hasEntryAppended(events, marotte.EntryKindToolCall) {
			t.Error("hook_update broadcast a tool_call event with hooks.showStatus off; want nothing")
		}
		if len(calls) != 0 {
			t.Errorf("buffered tool calls = %d, want 0", len(calls))
		}
	})

	t.Run("NothingWhenBlockNestedAtWrongLevel", func(t *testing.T) {
		hook := map[string]any{
			"hookId": "h1", "operationId": "op-1", "name": "probe", "status": hookStatusCompleted, "actionType": "runCommand",
		}
		frames := map[string]map[string]any{
			// The block under params._meta rather than params.update._meta: the
			// update object the handler receives then carries no kiro block at all.
			"no_meta_on_update": {"sessionUpdate": "session_info_update"},
			// One level too shallow: a decoder reading update._meta.hook.
			"under_meta_not_kiro": {
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"hook": hook, "kiro": map[string]any{"kind": "hook_update"}},
			},
			// Two levels too shallow: a decoder reading update.hook.
			"under_update_root": {
				"sessionUpdate": "session_info_update",
				"hook":          hook,
				"_meta":         map[string]any{"kiro": map[string]any{"kind": "hook_update"}},
			},
		}
		for name, frame := range frames {
			t.Run(name, func(t *testing.T) {
				events, calls := hookCardCase(t, true, frame, FrameAttribution{})
				if hasEntryAppended(events, marotte.EntryKindToolCall) || len(calls) != 0 {
					t.Errorf("a hook block outside update._meta.kiro produced events=%v calls=%d; want nothing",
						hasEntryAppended(events, marotte.EntryKindToolCall), len(calls))
				}
			})
		}
	})

	t.Run("TitleIsSingleLineAndBounded", func(t *testing.T) {
		name := "a\nb\x1b[31mc" + strings.Repeat("x", 2000)
		_, calls := hookCardCase(t, true, hookUpdateFrame(t, name, hookStatusCompleted), FrameAttribution{})
		if len(calls) != 1 {
			t.Fatalf("buffered tool calls = %d, want 1", len(calls))
		}
		title := calls[0].Title
		if !strings.HasPrefix(title, "Hook fired: ") {
			t.Errorf("Title = %q, want the Hook fired: prefix", title)
		}
		if strings.ContainsAny(title, "\n\x1b") {
			t.Errorf("Title = %q carries a newline or ESC; want single-line", title)
		}
		// The bound plus runesafe's three-byte truncation marker.
		if maxLen := len("Hook fired: ") + maxDisplayTextBytes + len("..."); len(title) > maxLen {
			t.Errorf("len(Title) = %d, want <= %d", len(title), maxLen)
		}
	})

	t.Run("DroppedForSubagentAndStep", func(t *testing.T) {
		for _, attr := range []FrameAttribution{{Subagent: true, SessionID: "sub"}, {Step: true}} {
			events, calls := hookCardCase(t, true, hookUpdateFrame(t, "probe", hookStatusCompleted), attr)
			if hasEntryAppended(events, marotte.EntryKindToolCall) || len(calls) != 0 {
				t.Errorf("attribution %+v produced events=%v calls=%d; want nothing", attr, hasEntryAppended(events, marotte.EntryKindToolCall), len(calls))
			}
		}
	})
}

// TestHandleSessionInfoUpdate_HookCardSourcePath pins the hook file a Hook fired card
// opens: the workspace-relative file KAS's hook id names, and "" whenever the id names
// no file inside the workspace.
func TestHandleSessionInfoUpdate_HookCardSourcePath(t *testing.T) {
	cases := map[string]struct {
		workDir, hookID, want string
	}{
		"workspace_file":    {"/workspace", "/workspace/.kiro/hooks/lint.kiro.hook#hook-0", ".kiro/hooks/lint.kiro.hook"},
		"second_hook":       {"/workspace", "/workspace/.kiro/hooks/a.json#hook-12", ".kiro/hooks/a.json"},
		"global_file":       {"/workspace", "/config/home/.kiro/hooks/a.json#hook-0", ""},
		"agent_profile":     {"/workspace", "kiro_default#hook-1", ""},
		"index_not_numeric": {"/workspace", "/workspace/.kiro/hooks/a.json#hook-x", ""},
		"no_index":          {"/workspace", "/workspace/.kiro/hooks/a.json#hook-", ""},
		"workspace_root":    {"/workspace", "/workspace#hook-0", ""},
		"parent_escape":     {"/workspace", "/workspace/../etc/passwd#hook-0", ""},
		"no_work_dir":       {"", "/workspace/.kiro/hooks/a.json#hook-0", ""},
		"bare_id":           {"/workspace", "h1", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			frame := hookUpdateFrame(t, "probe", hookStatusCompleted)
			frame["_meta"].(map[string]any)["kiro"].(map[string]any)["hook"].(map[string]any)["hookId"] = tc.hookID
			base, _ := newEventCaptureDeps()
			roles := rolesOf(&hookStatusDeps{baseDeps: base, enabled: true})
			roles.WorkDir = tc.workDir
			New(roles).HandleSessionInfoUpdate(t.Context(), "c1", mustJSON(t, frame), FrameAttribution{})
			calls := toolCallsOf(t, base.chatEntries("c1"))
			if len(calls) != 1 {
				t.Fatalf("buffered tool calls = %d, want 1", len(calls))
			}
			if got := calls[0].SourcePath; got != tc.want {
				t.Errorf("hookId %q in %q: SourcePath = %q, want %q", tc.hookID, tc.workDir, got, tc.want)
			}
		})
	}
}

// TestKnownSessionInfoKinds_HookUpdateIsConsumed pins hook_update out of the
// deliberately-ignored table: a consumed kind listed there would log a decode miss as a
// known drop.
func TestKnownSessionInfoKinds_HookUpdateIsConsumed(t *testing.T) {
	if _, ok := knownSessionInfoKinds["hook_update"]; ok {
		t.Fatal("knownSessionInfoKinds lists hook_update, which is consumed by handleHookUpdate")
	}
}
