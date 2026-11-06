package translate

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
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

func hookCardCase(t *testing.T, frame map[string]any, attr FrameAttribution) (*[]marotte.ServerEvent, []marotte.EntryToolCall) {
	t.Helper()
	events, entries := hookEntriesCase(t, nil, frame, attr)
	return events, toolCallsOf(t, entries)
}

func hookEntriesCase(t *testing.T, triggers map[string]string, frame map[string]any, attr FrameAttribution) (*[]marotte.ServerEvent, []marotte.Entry) {
	t.Helper()
	base, events := newEventCaptureDeps()
	tr := New(rolesOf(&hookTriggerDeps{baseDeps: base, triggers: triggers}))
	chatID := marotte.ChatID("c1")
	tr.HandleSessionInfoUpdate(t.Context(), chatID, mustJSON(t, frame), attr)
	return events, base.chatEntries(chatID)
}

// hookBlock is the frame's `_meta.kiro.hook` object, for a case to add fields to.
func hookBlock(frame map[string]any) map[string]any {
	return frame["_meta"].(map[string]any)["kiro"].(map[string]any)["hook"].(map[string]any)
}

func TestHandleSessionInfoUpdate_HookUpdateCard(t *testing.T) {
	t.Run("Recorded", func(t *testing.T) {
		events, calls := hookCardCase(t, hookUpdateFrame(t, "probe-save", hookStatusCompleted), FrameAttribution{})
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
				_, calls := hookCardCase(t, hookUpdateFrame(t, "probe", status), FrameAttribution{})
				if len(calls) != 1 {
					t.Fatalf("buffered tool calls = %d, want 1", len(calls))
				}
				if calls[0].Status != marotte.ToolFailed {
					t.Errorf("hookToolStatus(%q) = %q, want %q", status, calls[0].Status, marotte.ToolFailed)
				}
			})
		}
	})

	t.Run("NothingWhenBlockNestedAtWrongLevel", func(t *testing.T) {
		hook := map[string]any{
			"hookId": "h1", "operationId": "op-1", "name": "probe", "status": hookStatusCompleted, "actionType": "runCommand",
		}
		frames := map[string]map[string]any{
			// Block under params._meta, so the update object carries no kiro block.
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
				events, calls := hookCardCase(t, frame, FrameAttribution{})
				if hasEntryAppended(events, marotte.EntryKindToolCall) || len(calls) != 0 {
					t.Errorf("a hook block outside update._meta.kiro produced events=%v calls=%d; want nothing",
						hasEntryAppended(events, marotte.EntryKindToolCall), len(calls))
				}
			})
		}
	})

	t.Run("TitleIsSingleLineAndBounded", func(t *testing.T) {
		name := "a\nb\x1b[31mc" + strings.Repeat("x", 2000)
		_, calls := hookCardCase(t, hookUpdateFrame(t, name, hookStatusCompleted), FrameAttribution{})
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
		// The bound plus textsafe's three-byte truncation marker.
		if maxLen := len("Hook fired: ") + maxDisplayTextBytes + len("..."); len(title) > maxLen {
			t.Errorf("len(Title) = %d, want <= %d", len(title), maxLen)
		}
	})
}

// A step's hook goes to its run turn by both doors a step's session_info_update takes: the
// launching chat's and a parentless run's host session.
func TestStepHookUpdate_RecordedInTheStepsRunTurn(t *testing.T) {
	doors := map[string]func(tr *Translator, t *testing.T, raw []byte, attr FrameAttribution){
		"chat_hosted": func(tr *Translator, t *testing.T, raw []byte, attr FrameAttribution) {
			tr.HandleSessionInfoUpdate(t.Context(), "c1", raw, attr)
		},
		"run_hosted": func(tr *Translator, t *testing.T, raw []byte, attr FrameAttribution) {
			tr.HandleStepInfoUpdate(t.Context(), "run:wf_1", raw, attr)
		},
	}
	for name, feed := range doors {
		t.Run(name, func(t *testing.T) {
			base, events := newEventCaptureDeps()
			tr := New(rolesOf(&hookTriggerDeps{baseDeps: base, triggers: map[string]string{"h1": "PreToolUse"}}))
			attr := FrameAttribution{Step: true, RunID: "wf_1", SessionID: "sess_step", NodePath: workflow.PathKey([]string{"wf_1", "build"})}
			passing := hookUpdateFrame(t, "guard", hookStatusCompleted)
			passing["_meta"].(map[string]any)["kiro"].(map[string]any)["agentSubtaskId"] = "sub-1"
			failing := hookUpdateFrame(t, "lint", hookStatusCompleted)
			hookBlock(failing)["operationId"] = "op-2"
			hookBlock(failing)["output"] = map[string]any{"result": map[string]any{"output": "2 errors\n", "exitCode": 1}}

			feed(tr, t, mustJSON(t, passing), attr)
			feed(tr, t, mustJSON(t, failing), attr)

			entries := base.runEntries("wf_1", attr.NodePath)
			calls := toolCallsOf(t, entries)
			want := []marotte.EntryToolCall{
				{ID: "hook-op-1", Title: "Hook fired: guard", Kind: marotte.ToolKindHook, Status: marotte.ToolCompleted, HookTrigger: "PreToolUse"},
				{ID: "hook-op-2", Title: "Hook fired: lint", Kind: marotte.ToolKindHook, Status: marotte.ToolFailed, Output: "2 errors\n", HookTrigger: "PreToolUse"},
			}
			if len(calls) != len(want) {
				t.Fatalf("run turn tool calls = %+v, want the step's two hook cards", calls)
			}
			for i := range want {
				got := calls[i]
				got.Ts = 0
				if !reflect.DeepEqual(got, want[i]) {
					t.Errorf("run turn hook card %d = %+v, want %+v", i, got, want[i])
				}
			}
			var lanes []string
			for i := range entries {
				if entries[i].Kind == marotte.EntryKindToolCall {
					lanes = append(lanes, entries[i].Lane)
				}
			}
			if !slices.Equal(lanes, []string{"sub-1", ""}) {
				t.Errorf("hook tool_call lanes = %q, want [sub-1 \"\"]", lanes)
			}
			if got := toolCallsOf(t, base.chatEntries("c1")); len(got) != 0 {
				t.Errorf("chat tool calls after a step's hooks = %+v, want none", got)
			}
			if n := runToolCallsAppended(events, "wf_1"); n != 2 {
				t.Errorf("entry_appended{tool_call} for run wf_1 = %d, want 2", n)
			}
		})
	}
}

func runToolCallsAppended(events *[]marotte.ServerEvent, runID string) int {
	n := 0
	for _, e := range *events {
		p, ok := e.Payload.(marotte.EntryAppendedPayload)
		if e.Type == marotte.EventEntryAppended && ok && p.Entry.Kind == marotte.EntryKindToolCall && p.WorkflowID == runID && e.ChatID == "" {
			n++
		}
	}
	return n
}

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
			hookBlock(frame)["hookId"] = tc.hookID
			base, _ := newEventCaptureDeps()
			roles := rolesOf(&hookTriggerDeps{baseDeps: base})
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

// A consumed kind listed in the ignored table would log a decode miss as a known drop.
func TestKnownSessionInfoKinds_HookUpdateIsConsumed(t *testing.T) {
	if _, ok := knownSessionInfoKinds["hook_update"]; ok {
		t.Fatal("knownSessionInfoKinds lists hook_update, which is consumed by handleHookUpdate")
	}
}

func TestHandleSessionInfoUpdate_HookTriggerStamped(t *testing.T) {
	triggers := map[string]string{"h1": "PreToolUse"}
	t.Run("Resolved", func(t *testing.T) {
		_, entries := hookEntriesCase(t, triggers, hookUpdateFrame(t, "guard", hookStatusCompleted), FrameAttribution{})
		calls := toolCallsOf(t, entries)
		if len(calls) != 1 || calls[0].HookTrigger != "PreToolUse" {
			t.Fatalf("calls = %+v, want one card stamped PreToolUse", calls)
		}
	})
	t.Run("MissStampsNothing", func(t *testing.T) {
		frame := hookUpdateFrame(t, "guard", hookStatusCompleted)
		hookBlock(frame)["hookId"] = "unknown#hook-0"
		_, entries := hookEntriesCase(t, triggers, frame, FrameAttribution{})
		calls := toolCallsOf(t, entries)
		if len(calls) != 1 || calls[0].HookTrigger != "" {
			t.Fatalf("calls = %+v, want one card with no trigger", calls)
		}
	})
}

func TestHandleSessionInfoUpdate_HookOutcome(t *testing.T) {
	cases := map[string]struct {
		output     any
		wantStatus marotte.ToolStatus
		wantOutput string
	}{
		"exit_code_fails":     {map[string]any{"command": "guard.sh", "result": map[string]any{"exitCode": 3}}, marotte.ToolFailed, ""},
		"exit_zero_completes": {map[string]any{"command": "guard.sh", "result": map[string]any{"exitCode": 0}}, marotte.ToolCompleted, ""},
		"output_recorded":     {map[string]any{"result": map[string]any{"output": "lint: \x1b[31m2 warnings\x1b[0m\n", "exitCode": 0}}, marotte.ToolCompleted, "lint: 2 warnings\n"},
		"no_result":           {map[string]any{"command": "guard.sh"}, marotte.ToolCompleted, ""},
		"malformed_output":    {"not an object", marotte.ToolCompleted, ""},
		"malformed_exit_code": {map[string]any{"result": map[string]any{"exitCode": "3"}}, marotte.ToolCompleted, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			frame := hookUpdateFrame(t, "guard", hookStatusCompleted)
			hookBlock(frame)["output"] = tc.output
			_, calls := hookCardCase(t, frame, FrameAttribution{})
			if len(calls) != 1 {
				t.Fatalf("buffered tool calls = %d, want 1: a hook card is never dropped for its output", len(calls))
			}
			if calls[0].Status != tc.wantStatus || calls[0].Output != tc.wantOutput {
				t.Errorf("status %q output %q, want %q %q", calls[0].Status, calls[0].Output, tc.wantStatus, tc.wantOutput)
			}
		})
	}
}

func TestHandleSessionInfoUpdate_SubagentHookInDelegateLane(t *testing.T) {
	frame := hookUpdateFrame(t, "guard", hookStatusCompleted)
	frame["_meta"].(map[string]any)["kiro"].(map[string]any)["agentSubtaskId"] = "sub-1"
	_, entries := hookEntriesCase(t, nil, frame, FrameAttribution{})
	var lanes []string
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindToolCall {
			lanes = append(lanes, entries[i].Lane)
		}
	}
	if len(lanes) != 1 || lanes[0] != "sub-1" {
		t.Fatalf("hook tool_call lanes = %q, want [sub-1]", lanes)
	}
}
