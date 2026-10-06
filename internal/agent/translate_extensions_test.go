package agent

// Tests for the v3 `_kiro/*` handlers through translateACPEvent: mcp/status, customAgent/*, error/rate_limit,
// system/notify (translate/init_errors.go, v3_notifications.go) and session/update sub-kinds (v3_updates.go).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestTranslateMCPStatus(t *testing.T) {
	tests := []struct {
		name       string
		params     any
		wantSnap   func(t *testing.T, snap []mcpServerRuntime)
		wantEvents []string
	}{
		{
			name:   "ConnectedRecordsServer",
			params: map[string]any{"servers": []map[string]any{{"name": "github", "status": "connected"}}},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].Name != "github" {
					t.Fatalf("registry snapshot = %+v", snap)
				}
				if snap[0].State != mcpStateConnected {
					t.Errorf("state = %q, want %q", snap[0].State, mcpStateConnected)
				}
			},
		},
		{
			name:   "FailedRecordsError",
			params: map[string]any{"servers": []map[string]any{{"name": "broken", "status": "failed", "errorMessage": "connection refused"}}},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].State != mcpStateFailed {
					t.Fatalf("snapshot = %+v", snap)
				}
				if snap[0].Error != "connection refused" {
					t.Errorf("error = %q", snap[0].Error)
				}
			},
			wantEvents: []string{"mcp_failed"},
		},
		{
			name:   "FailedWithAuthURLEmitsOAuth",
			params: map[string]any{"servers": []map[string]any{{"name": "linear", "status": "failed", "authorizationUrl": "https://oauth.example/authorize"}}},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].OAuthURL != "https://oauth.example/authorize" {
					t.Errorf("snapshot = %+v", snap)
				}
			},
			wantEvents: []string{"mcp_oauth_needed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			before := h.bus.fanout.Position().Head
			msg := &marotte.RPCResponse{Method: "_kiro/mcp/status", Params: mustJSON(t, tc.params)}
			h.translateACPEvent("", msg)

			if tc.wantSnap != nil {
				tc.wantSnap(t, h.mcpRegistry.Snapshot())
			}
			if len(tc.wantEvents) > 0 {
				types := extractTypes(t, bufferedSince(h, before))
				if missing := missingEvents(types, tc.wantEvents...); len(missing) > 0 {
					t.Errorf("missing events %v; got %v", missing, types)
				}
			}
		})
	}
}

// availableCommandsFrame is a chat's catalog frame: one prompt and one skill.
func availableCommandsFrame(t *testing.T) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": "available_commands_update",
		"availableCommands": []map[string]any{
			{"name": "review", "description": "(file prompt)", "input": map[string]any{"hint": "[args]"}, "_meta": map[string]any{"kiro": map[string]any{"type": "prompt"}}},
			{"name": "some-skill", "description": "a skill", "_meta": map[string]any{"kiro": map[string]any{"type": "skill"}}},
		},
	}
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: mustJSON(t, map[string]any{"update": update})}
}

// A chat's frame fills GET /api/slash-commands' catalog, announced once; a repeat changes nothing.
func TestTranslateV3_AvailableCommandsUpdateFeedsTheCatalog(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.bus.fanout.Position().Head

	h.translateACPEvent("c1", availableCommandsFrame(t))
	h.translateACPEvent("c1", availableCommandsFrame(t))

	types := extractTypes(t, bufferedSince(h, before))
	if !slices.Equal(types, []string{string(marotte.EventSlashCommandsChanged)}) {
		t.Errorf("events = %v, want one slash_commands_changed", types)
	}
	rec := httptest.NewRecorder()
	h.handleSlashCommands(rec, httptest.NewRequest(http.MethodGet, "/api/slash-commands", nil))
	var got marotte.SlashCommandsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET /api/slash-commands: %v", err)
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "review" || got.Commands[0].Hint != "[args]" {
		t.Errorf("GET /api/slash-commands = %+v, want only the review prompt", got.Commands)
	}
}

// documents_changed fills the issues map keyed by KiroDoc.Path, announced once;
// a failed frame changes nothing.
func TestSteeringDocumentsChanged_ProducesIssuesKeyedByDocsPath(t *testing.T) {
	h, _, _ := newTestHub()
	before := h.bus.fanout.Position().Head
	frame := func(status string) *marotte.RPCResponse {
		return &marotte.RPCResponse{Method: methodV3SteeringDocs, Params: mustJSON(t, map[string]any{
			"sessionId": "s1", "status": status,
			"documents": []map[string]any{{
				"name": "a", "uri": "file:///workspace/.kiro/steering/a.md", "inclusion": "always", "content": "body",
				"_meta": map[string]any{"kiro": map[string]any{"configIssues": []map[string]any{{
					"code": "contextReferenceUnresolved", "reference": "#[[file:x]]", "reason": "notFound", "remediation": "Fix it.",
				}}}},
			}},
		})}
	}

	h.translateACPEvent("c1", frame("success"))
	h.translateACPEvent("c1", frame("failed"))

	types := extractTypes(t, bufferedSince(h, before))
	if !slices.Equal(types, []string{string(marotte.EventSteeringIssuesChanged)}) {
		t.Errorf("events = %v, want one steering_issues_changed", types)
	}
	got := h.steeringIssues.Snapshot()
	issues := got["workspace/.kiro/steering/a.md"]
	if len(got) != 1 || len(issues) != 1 || issues[0].Code != "contextReferenceUnresolved" || issues[0].Remediation != "Fix it." {
		t.Errorf("issues = %+v, want one contextReferenceUnresolved keyed workspace/.kiro/steering/a.md", got)
	}
}

// A chat's catalog carries MCP prompts the utility session cannot see, so the
// utility list seeds an empty catalog and never replaces a chat's.
func TestSlashCatalog_UtilityNeverOverwritesChat(t *testing.T) {
	var c slashCatalog
	utility := []marotte.SlashCommand{{Name: "u", Kind: marotte.SlashKindPrompt}}
	chat := []marotte.SlashCommand{{Name: "c", Kind: marotte.SlashKindPrompt}}

	if !c.SetFromUtility(utility) {
		t.Fatal("SetFromUtility on an empty catalog = false, want true")
	}
	if !c.SetFromChat(chat) {
		t.Fatal("SetFromChat over a utility list = false, want true")
	}
	if c.SetFromUtility(utility) {
		t.Error("SetFromUtility after a chat frame = true, want false")
	}
	if got, _ := c.Snapshot(); len(got) != 1 || got[0].Name != "c" {
		t.Errorf("Snapshot() = %+v, want the chat's list", got)
	}
}

// Not ready until KAS sends a catalog; an empty one still counts, letting the client release unknown names.
func TestSlashCatalog_ReadyOnlyAfterAKASCatalog(t *testing.T) {
	var c slashCatalog
	if cmds, ready := c.Snapshot(); ready || len(cmds) != 0 {
		t.Fatalf("Snapshot() before any frame = (%+v, %v), want ([], false)", cmds, ready)
	}
	if !c.SetFromChat([]marotte.SlashCommand{}) {
		t.Error("SetFromChat(empty) on an unready catalog = false, want true so clients refetch")
	}
	if cmds, ready := c.Snapshot(); !ready || len(cmds) != 0 {
		t.Errorf("Snapshot() after an empty catalog = (%+v, %v), want ([], true)", cmds, ready)
	}
	if c.SetFromChat([]marotte.SlashCommand{}) {
		t.Error("SetFromChat(empty) again = true, want false: nothing changed")
	}
}

// The utility seed fills the menu but never lets the client release an unknown name.
func TestSlashCatalog_UtilitySeedIsNotReady(t *testing.T) {
	var c slashCatalog
	seed := []marotte.SlashCommand{{Name: "review", Kind: marotte.SlashKindPrompt}}
	if !c.SetFromUtility(seed) {
		t.Fatal("SetFromUtility(seed) on an empty catalog = false, want true so clients refetch")
	}
	if cmds, ready := c.Snapshot(); ready || len(cmds) != 1 {
		t.Errorf("Snapshot() after a utility seed = (%+v, %v), want one command, not ready", cmds, ready)
	}
	if !c.SetFromChat(seed) {
		t.Error("SetFromChat(same list) after a utility seed = false, want true: readiness changed")
	}
	if _, ready := c.Snapshot(); !ready {
		t.Error("Snapshot() after a chat frame: ready = false, want true")
	}
}

func TestHandleSlashCommands_ReportsReadiness(t *testing.T) {
	h, _, _ := newTestHub()
	get := func() marotte.SlashCommandsResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		h.handleSlashCommands(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/slash-commands", http.NoBody))
		var resp marotte.SlashCommandsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return resp
	}
	if resp := get(); resp.Ready || len(resp.Commands) != 0 {
		t.Errorf("before any frame: %+v, want {commands:[] ready:false}", resp)
	}
	h.slash.SetFromChat([]marotte.SlashCommand{{Name: "review", Kind: marotte.SlashKindPrompt}})
	if resp := get(); !resp.Ready || len(resp.Commands) != 1 {
		t.Errorf("after a catalog: %+v, want ready with one command", resp)
	}
}

func TestTranslateV3_SummarizationRunningEmitsTransient(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.bus.fanout.Position().Head

	msg := &marotte.RPCResponse{
		Method: marotte.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"kiro": map[string]any{"summarization": map[string]any{"status": "running"}}},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "compaction_started"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
}

func TestTranslateV3_SummarizationSuccessPersistsEvent(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	summary := "summary text"

	msg := &marotte.RPCResponse{
		Method: marotte.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta": map[string]any{"kiro": map[string]any{"summarization": map[string]any{
					"status":  "success",
					"summary": map[string]any{"conversationSummary": summary},
				}}},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	// Between turns on an empty log, the compaction opens the event turn it joins.
	entries := logOf(t, cs, "c1")
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (an event turn_open, then the compaction): %+v", len(entries), entries)
	}
	if opens := opensOf(t, entries); len(opens) != 1 || opens[0].Source != marotte.TurnOpenNameEvent {
		t.Errorf("turn_opens = %+v, want one event turn", opens)
	}
	compaction := entries[1]
	if compaction.Kind != marotte.EntryKindCompaction {
		t.Fatalf("kind = %q, want %q", compaction.Kind, marotte.EntryKindCompaction)
	}
	var c marotte.EntryCompaction
	if err := json.Unmarshal(compaction.Payload, &c); err != nil {
		t.Fatalf("decode compaction: %v", err)
	}
	if c.Summary != summary {
		t.Errorf("summary = %q, want %q", c.Summary, summary)
	}
	chat, _ := cs.Get(t.Context(), "c1")
	if chat.CompactionWatermark != compaction.ID {
		t.Errorf("watermark = %q, want the compaction entry's id %q", chat.CompactionWatermark, compaction.ID)
	}
}

func TestTranslateV3_UsageUpdatePersistsContextPct(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	msg := &marotte.RPCResponse{
		Method: marotte.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "usage_update",
				"size":          1000,
				"used":          250,
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	chat, _ := cs.Get(t.Context(), "c1")
	if chat.Usage.ContextPct != 25 {
		t.Errorf("context_pct = %v, want 25", chat.Usage.ContextPct)
	}
}

func TestTranslateInitErrors_AgentNotFoundPersistsFallback(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.CurrentModeID = "nonexistent"
		return true
	})
	before := h.bus.fanout.Position().Head
	msg := &marotte.RPCResponse{
		Method: "_kiro/customAgent/not_found",
		Params: mustJSON(t, map[string]any{
			"requestedAgent": "nonexistent",
			"fallbackAgent":  "vibe",
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(t.Context(), "c1")
	if c.CurrentModeID != "vibe" {
		t.Errorf("current_mode_id = %q, want vibe", c.CurrentModeID)
	}
	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "error"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
}

func TestTranslateInitErrors_AgentConfigErrorEmitsError(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.bus.fanout.Position().Head
	msg := &marotte.RPCResponse{
		Method: "_kiro/customAgent/config_error",
		Params: mustJSON(t, map[string]any{
			"path":  "/home/user/.kiro/agents/broken.md",
			"error": "invalid YAML frontmatter",
		}),
	}
	h.translateACPEvent("c1", msg)
	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "error"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
}

func TestTranslateInitErrors_RateLimitEmitsError(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.bus.fanout.Position().Head
	msg := &marotte.RPCResponse{
		Method: "_kiro/error/rate_limit",
		Params: mustJSON(t, map[string]any{
			"message": "Rate limit exceeded, try again in 30s",
		}),
	}
	h.translateACPEvent("c1", msg)
	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "error"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
}

func TestTranslateKnowledgeIndexing_ReachesTheTranslator(t *testing.T) {
	for _, method := range []string{"_kiro/knowledge/indexingStarted", "_kiro/knowledge/indexingCompleted"} {
		t.Run(method, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			before := h.bus.fanout.Position().Head
			h.translateACPEvent("c1", &marotte.RPCResponse{
				Method: method,
				Params: mustJSON(t, map[string]any{"sessionId": "sess-1", "name": "docs", "fileCount": 2, "status": "success"}),
			})
			types := extractTypes(t, bufferedSince(h, before))
			if missing := missingEvents(types, "knowledge_indexing"); len(missing) > 0 {
				t.Errorf("translateACPEvent(%s): missing %v; got %v", method, missing, types)
			}
		})
	}
}

func systemNotifyMsg(t *testing.T) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		Method: "_kiro/system/notify",
		Params: mustJSON(t, map[string]any{
			"level":   "warning",
			"message": "The selected model is experiencing high load.",
		}),
	}
}

func TestTranslateSystemNotify_EmitsASystemNoticeNotAnError(t *testing.T) {
	h, _, _ := newTestHub()
	before := h.bus.fanout.Position().Head
	h.translateACPEvent("c1", systemNotifyMsg(t))
	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "system_notice"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
	if slices.Contains(types, "error") {
		t.Errorf("a system notice raised an error event: %v", types)
	}
}

func TestTranslateSystemNotify_ARunBridgeNamesItsRun(t *testing.T) {
	h, _, _ := newTestHub()
	before := h.bus.fanout.Position().Head
	h.translateACPEvent(runChatID("wf_1"), systemNotifyMsg(t))
	var got []marotte.ChatID
	for _, e := range bufferedSince(h, before) {
		var msg marotte.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == marotte.EventSystemNotice {
			got = append(got, msg.ChatID)
		}
	}
	if len(got) != 1 || got[0] != runChatID("wf_1") {
		t.Errorf("system_notice chat ids = %v, want one on %q", got, runChatID("wf_1"))
	}
}

func TestUtilitySession_ForwardsASystemNotify(t *testing.T) {
	var seen []string
	us := &utilitySession{hooks: utilitySessionHooks{
		onSystemNotify: func(msg *marotte.RPCResponse) { seen = append(seen, msg.Method) },
	}}
	if !us.dispatchNotification(systemNotifyMsg(t)) || len(seen) != 1 {
		t.Errorf("dispatchNotification took %v, want the notify handed to its hook", seen)
	}
}
