package agent

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

// toolCallsOf decodes every tool_call entry in entries, in file order.
func toolCallsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryToolCall {
	t.Helper()
	var out []marotte.EntryToolCall
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindToolCall {
			continue
		}
		var c marotte.EntryToolCall
		if err := json.Unmarshal(entries[i].Payload, &c); err != nil {
			t.Fatalf("decode tool_call %q: %v", entries[i].ID, err)
		}
		out = append(out, c)
	}
	return out
}

// toolResultsOf decodes every tool_result entry in entries, in file order.
func toolResultsOf(t *testing.T, entries []marotte.Entry) []marotte.EntryToolResult {
	t.Helper()
	var out []marotte.EntryToolResult
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindToolResult {
			continue
		}
		var r marotte.EntryToolResult
		if err := json.Unmarshal(entries[i].Payload, &r); err != nil {
			t.Fatalf("decode tool_result %q: %v", entries[i].ID, err)
		}
		out = append(out, r)
	}
	return out
}

// openTextEntry is the chat's one open text entry, failing on none or several.
func openTextEntry(t *testing.T, h *Runtime, chatID marotte.ChatID) marotte.OpenEntry {
	t.Helper()
	turn := h.liveTurn(chatID)
	if turn == nil {
		t.Fatalf("liveTurn(%q) = nil, want an open turn", chatID)
	}
	open := turn.OpenEntries()
	if len(open) != 1 || open[0].Kind != marotte.EntryKindText {
		t.Fatalf("OpenEntries(%q) = %+v, want one open text entry", chatID, open)
	}
	return open[0]
}

// The first chunk of a turn marotte never prompted opens a wire turn and a text
// entry in one frame each.
func TestTranslateACPEvent_AssistantChunk(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	before := h.bus.fanout.Position().Head
	h.translateACPEvent("c1", newChunkMsg("hello "))

	gotTypes := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(gotTypes, "turn_opened", "entry_opened"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, gotTypes)
	}

	open := openTextEntry(t, h, "c1")
	if open.Text != "hello " || open.N != 1 {
		t.Errorf("open text entry = %+v, want text %q at n=1", open, "hello ")
	}
}

// A second chunk extends the open entry rather than opening another: same id, the
// text coalesced, and the frame is entry_delta, never a second entry_opened.
func TestTranslateACPEvent_ASecondChunkExtendsTheOpenEntry(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	h.translateACPEvent("c1", newChunkMsg("one"))
	first := openTextEntry(t, h, "c1")

	before := h.bus.fanout.Position().Head
	h.translateACPEvent("c1", newChunkMsg("two"))
	second := openTextEntry(t, h, "c1")

	if first.ID != second.ID {
		t.Errorf("entry id changed between chunks: %q → %q", first.ID, second.ID)
	}
	if second.Text != "onetwo" || second.N != 2 {
		t.Errorf("open text entry = %+v, want text %q at n=2", second, "onetwo")
	}
	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "entry_delta"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
	for _, typ := range types {
		if typ == "entry_opened" {
			t.Errorf("a second chunk re-opened the entry; got %v", types)
		}
	}
}

// A tool_call frame is held open until its terminal update seals the tool_result; the create's locations and
// diffs ride the entry, the update's the result.
func TestTranslateACPEvent_ToolCalls(t *testing.T) {
	cases := []struct {
		assert func(*testing.T, []marotte.Entry, *turnlog.Turn)
		name   string
		events []json.RawMessage
	}{
		{
			name: "tool_call_is_held_open_until_its_result",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"readFile","kind":"read","status":"pending"}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, turn *turnlog.Turn) {
				calls := toolCallsOf(t, entries)
				if len(calls) != 1 || calls[0].ID != "tc-1" || calls[0].Status != marotte.ToolPending {
					t.Errorf("tool_call entries = %+v, want one pending tc-1", calls)
				}
				if results := toolResultsOf(t, entries); len(results) != 0 {
					t.Errorf("tool_result entries = %+v, want none before the update", results)
				}
				if _, open := turn.OpenCallFor("tc-1"); !open {
					t.Error("OpenCallFor(tc-1) = settled, want unsettled until the terminal update")
				}
			},
		},
		{
			name: "terminal_update_seals_the_result_and_settles_the_call",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"readFile","kind":"read","status":"pending"}`),
				json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","content":[{"type":"content","content":{"text":"file contents"}}]}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, turn *turnlog.Turn) {
				results := toolResultsOf(t, entries)
				if len(results) != 1 {
					t.Fatalf("tool_result entries = %+v, want one", results)
				}
				if results[0].Status != marotte.ToolCompleted {
					t.Errorf("status = %q, want completed", results[0].Status)
				}
				if !strings.Contains(results[0].Output, "file contents") {
					t.Errorf("output = %q, want the update's content", results[0].Output)
				}
				if _, open := turn.OpenCallFor("tc-1"); open {
					t.Error("OpenCallFor(tc-1) = unsettled after its terminal update")
				}
			},
		},
		{
			// v3: subagents are ordinary tool calls, with no noise-title filter.
			name: "tool_call_passes_through_without_noise_filter",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-noise","title":"Summarizing","kind":"read","status":"pending"}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, _ *turnlog.Turn) {
				calls := toolCallsOf(t, entries)
				if len(calls) != 1 || calls[0].ID != "tc-noise" {
					t.Errorf("tool call not passed through: %+v", calls)
				}
			},
		},
		{
			name: "tool_call_with_locations",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-loc","title":"Reading main.go","kind":"read","status":"pending","locations":[{"path":"main.go","line":42}]}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, _ *turnlog.Turn) {
				calls := toolCallsOf(t, entries)
				if len(calls) != 1 {
					t.Fatalf("tool_call entries = %+v, want one", calls)
				}
				if loc := calls[0].Locations; len(loc) != 1 || loc[0].Path != "main.go" || loc[0].Line != 42 {
					t.Errorf("locations = %+v, want main.go:42", loc)
				}
			},
		},
		{
			name: "tool_call_with_diffs",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-diff","title":"Editing main.go","kind":"edit","status":"pending","locations":[{"path":"main.go","line":1}],"content":[{"type":"diff","path":"/abs/main.go","oldText":"hello","newText":"world"}]}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, _ *turnlog.Turn) {
				calls := toolCallsOf(t, entries)
				if len(calls) != 1 {
					t.Fatalf("tool_call entries = %+v, want one", calls)
				}
				if d := calls[0].Diffs; len(d) != 1 || d[0].OldText != "hello" || d[0].NewText != "world" {
					t.Errorf("diffs = %+v, want hello→world", d)
				}
			},
		},
		{
			name: "tool_call_update_with_locations",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"readFile","kind":"read","status":"pending"}`),
				json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","locations":[{"path":"config.go"}]}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, _ *turnlog.Turn) {
				results := toolResultsOf(t, entries)
				if len(results) != 1 {
					t.Fatalf("tool_result entries = %+v, want one", results)
				}
				if loc := results[0].Locations; len(loc) != 1 || loc[0].Path != "config.go" {
					t.Errorf("locations = %+v, want config.go", loc)
				}
			},
		},
		{
			name: "tool_call_update_with_diffs",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"editFile","kind":"edit","status":"pending"}`),
				json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","content":[{"type":"diff","path":"/abs/file.go","oldText":"old","newText":"new"}]}`),
			},
			assert: func(t *testing.T, entries []marotte.Entry, _ *turnlog.Turn) {
				results := toolResultsOf(t, entries)
				if len(results) != 1 {
					t.Fatalf("tool_result entries = %+v, want one", results)
				}
				if d := results[0].Diffs; len(d) != 1 || d[0].Path != "/abs/file.go" {
					t.Errorf("diffs = %+v, want /abs/file.go", d)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			for _, raw := range tc.events {
				h.translateACPEvent("c1", &marotte.RPCResponse{
					Method: "session/update",
					Params: mustJSON(t, map[string]any{"update": raw}),
				})
			}
			turn := h.liveTurn("c1")
			if turn == nil {
				t.Fatal("liveTurn(c1) = nil, want the wire turn the tool_call opened")
			}
			tc.assert(t, logOf(t, cs, "c1"), turn)
		})
	}
}

// A plan frame is one plan entry in the turn it arrived in.
func TestTranslateACPEvent_PlanIsOneEntry(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	raw := json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step 1","priority":"high","status":"pending"}]}`)
	h.translateACPEvent("c1", &marotte.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	})

	turns, plans := plansOf(t, logOf(t, cs, "c1"))
	if len(plans) != 1 {
		t.Fatalf("plan entries = %+v, want one", plans)
	}
	if len(plans[0].Entries) != 1 || plans[0].Entries[0].Content != "step 1" {
		t.Errorf("plan = %+v, want the frame's one entry", plans[0])
	}
	if turn := h.liveTurn("c1"); turn == nil || turns[0] != turn.ID() {
		t.Errorf("plan landed in turn %q, want the open turn", turns[0])
	}
}

func TestTranslateACPEvent_PermissionRequestEmitsAndPushes(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	before := h.bus.fanout.Position().Head
	// v3: the correlation id is the envelope's msg.ID, the params are flat ({sessionId, toolCall, options}), and the option id is `optionId`.
	permID := int64(42)
	msg := &marotte.RPCResponse{
		ID:     &permID,
		Method: "session/request_permission",
		Params: mustJSON(t, map[string]any{
			"sessionId": "c1-sess",
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "writeFile", "kind": "edit"},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "permission_needed"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
}

func TestTranslateACPEvent_MalformedJSONIgnored(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	// Each must be a silent no-op, not a panic.
	bad := []*marotte.RPCResponse{
		{Method: "session/update", Params: json.RawMessage(`{bad`)},
		{Method: "session/update", Params: json.RawMessage(`{"params":{"update":null}}`)},
		{Method: "session/update", Params: nil},
		{Method: "_kiro/mcp/status", Params: json.RawMessage(`not json`)},
		{Method: "session/request_permission", Params: json.RawMessage(`{`)},
		{Method: "unknown_method", Params: json.RawMessage(`{}`)},
	}
	for _, m := range bad {
		h.translateACPEvent("c1", m)
	}
}

// BenchmarkTranslateACPEvent exercises the session/update dispatch hot path with representative payloads.
func BenchmarkTranslateACPEvent(b *testing.B) {
	payloads := []struct {
		msg  *marotte.RPCResponse
		name string
	}{
		{
			name: "agent_message_chunk",
			msg: &marotte.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello, world! This is a representative chunk of assistant output."}}}`),
			},
		},
		{
			name: "tool_call",
			msg: &marotte.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"tool_call","toolCallId":"tc-bench-1","title":"readFile","kind":"read","status":"pending","locations":[{"path":"main.go","line":10}]}}`),
			},
		},
		{
			name: "tool_call_update",
			msg: &marotte.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-bench-1","status":"completed","content":[{"type":"content","content":{"text":"package main\nfunc main() {}\n"}}]}}`),
			},
		},
	}

	for _, p := range payloads {
		b.Run(p.name, func(b *testing.B) {
			h, cs, _ := newTestHub()
			_, _ = cs.Mutate(b.Context(), "bench", func(c *marotte.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			// Pre-seed an unsettled call for tool_call_update; once settled, later frames fold nothing.
			if p.name == "tool_call_update" {
				_, turn := h.stagePromptTurn(b, "bench")
				call := marotte.EntryToolCall{ID: "tc-bench-1", Status: marotte.ToolPending}
				if _, err := turn.ToolCall(b.Context(), "", &call); err != nil {
					b.Fatalf("pre-seed tool_call: %v", err)
				}
			}
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				h.translateACPEvent("bench", p.msg)
			}
		})
	}
}

// FuzzTranslateInitErrors fuzzes the v3 init-error and notice parsing (rate_limit, customAgent/*, system/notify):
// no panics and a non-decreasing replay buffer.
func FuzzTranslateInitErrors(f *testing.F) {
	seeds := []string{
		`{"message":"rate limited, retry in 30s"}`,
		`{"requestedAgent":"planner","fallbackAgent":"vibe"}`,
		`{"path":"/config/agents/x.json","error":"parse error"}`,
		`{"level":"warning","message":"model under high load"}`,
		`{}`,
		`{"message":null}`,
		`{"requestedAgent":"` + strings.Repeat("あ", 100) + `","fallbackAgent":""}`,
		`not json at all`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(f.Context(), "fuzz", func(c *marotte.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	methods := []string{
		"_kiro/error/rate_limit",
		"_kiro/customAgent/not_found",
		"_kiro/customAgent/config_error",
		"_kiro/system/notify",
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		before := h.bus.fanout.Position().Head
		var idx int
		if len(data) > 0 {
			idx = int(data[0]) % len(methods)
		}
		msg := &marotte.RPCResponse{
			Method: methods[idx],
			Params: data,
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
		if head := h.bus.fanout.Position().Head; head < before {
			t.Errorf("event head went backwards from %d", before)
		}
	})
}

// FuzzTranslateMCP fuzzes `_kiro/mcp/status` (HandleMCPStatus): no panics and a consistent mcpRegistry.
func FuzzTranslateMCP(f *testing.F) {
	seeds := []string{
		`{"servers":[{"name":"my-server","status":"connected","tools":[{"name":"t1"}]}]}`,
		`{"servers":[{"name":"oauth-srv","status":"failed","authorizationUrl":"https://example.com/auth"}]}`,
		`{"servers":[{"name":"fail-srv","status":"failed","errorMessage":"connection refused"}]}`,
		`{"servers":[{"name":"","status":"connecting"}]}`,
		`{"servers":[]}`,
		`{}`,
		`{"servers":null}`,
		`{"servers":[{"name":"` + strings.Repeat("x", 1000) + `","status":"connected","tools":null}]}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(f.Context(), "fuzz", func(c *marotte.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &marotte.RPCResponse{
			Method: "_kiro/mcp/status",
			Params: data,
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
		// Snapshot must not panic (concurrent-safe read).
		_ = h.mcpRegistry.Snapshot()
	})
}

// FuzzHandleSessionUpdate fuzzes the session/update dispatcher: malformed JSON and unknown subtypes are silent no-ops.
func FuzzHandleSessionUpdate(f *testing.F) {
	seeds := []string{
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","content":[{"type":"content","content":{"text":"out"}}]}`,
		`{"sessionUpdate":"plan","entries":[{"content":"step","priority":"high","status":"pending"}]}`,
		`{"sessionUpdate":"current_mode_update","modeId":"code"}`,
		`{}`,
		`{"sessionUpdate":"unknown_future_subtype","data":123}`,
		`not json at all`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(f.Context(), "fuzz", func(c *marotte.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &marotte.RPCResponse{
			Method: "session/update",
			Params: json.RawMessage(`{"update":` + string(data) + `}`),
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
	})
}

// An fs/* request is routed to the FS handler, which answers through the bridge.
func TestTranslateACPEvent_RoutesFSRequest(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "r.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(7106)
	msg := &marotte.RPCResponse{
		ID:     &id,
		Method: marotte.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "r.txt"}),
	}
	h.translateACPEvent("c1", msg)
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fs/read request was not routed to the FS handler")
	}
}

// A terminal/* request is routed to the terminal handler, which answers through the bridge.
func TestTranslateACPEvent_RoutesTerminalRequest(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	// A terminal owned by c1, so output resolves and answers through its bridge.
	h.agentTerms.mu.Lock()
	h.agentTerms.terms["term-1"] = newAgentTerminal(nil, "c1", 64)
	h.agentTerms.mu.Unlock()

	id := int64(7110)
	msg := &marotte.RPCResponse{
		ID:     &id,
		Method: methodTermOutput,
		Params: mustJSON(t, map[string]any{"terminalId": "term-1"}),
	}
	h.translateACPEvent("c1", msg)
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal/output request was not routed to the terminal handler")
	}
}

// registerParentSession registers a bridge for chatID whose SessionID
// is parentSession, so h.parentACPSession(chatID) returns it.
func registerParentSession(t *testing.T, h *Runtime, chatID marotte.ChatID, parentSession string) {
	t.Helper()
	sb, _ := h.bridge.mgr.orInsert(chatID)
	br := newFakeBridge()
	br.sessionID = parentSession
	sb.bridge = br
	sb.state = bridgeIdle
}

// captureSubSession installs a capturing agent_message_chunk handler, drives handleSessionUpdate with sessionID,
// and returns the computed subSessionID and whether the handler ran.
func captureSubSession(t *testing.T, h *Runtime, chatID marotte.ChatID, sessionID string) (got string, called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[marotte.ACPUpdateKind]sessionUpdateHandler{
		marotte.ACPUpdateAgentChunk: func(_ context.Context, _ marotte.ChatID, _ json.RawMessage, attr translate.FrameAttribution) {
			if attr.Subagent {
				got = attr.SessionID
			}
			called = true
		},
	}
	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateAgentChunk),
		"content":       map[string]any{"type": "text", "text": "x"},
	})
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    update,
	})
	msg := &marotte.RPCResponse{Method: "session/update", Params: params}
	h.handleSessionUpdate(t.Context(), chatID, msg)
	return got, called
}

// subSessionID is the frame's sessionId only when non-empty, a parent exists, and they differ; otherwise "".
func TestHandleSessionUpdate_SubSessionAttribution(t *testing.T) {
	cases := []struct {
		name       string
		chatID     marotte.ChatID
		registerPS string // parent session to register; "" => no bridge (parent == "")
		sessionID  string
		want       string
	}{
		{
			name:       "subagent_when_session_nonempty_parent_set_and_differs",
			chatID:     "chat-sub",
			registerPS: "parent-A",
			sessionID:  "sub-B",
			want:       "sub-B",
		},
		{
			name:       "parent_when_session_equals_parent",
			chatID:     "chat-match",
			registerPS: "same",
			sessionID:  "same",
			want:       "",
		},
		{
			name:       "parent_when_no_parent_bridge",
			chatID:     "chat-noparent",
			registerPS: "",
			sessionID:  "sub-C",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			defer shutdownHub(t, h)
			if tc.registerPS != "" {
				registerParentSession(t, h, tc.chatID, tc.registerPS)
			}
			got, called := captureSubSession(t, h, tc.chatID, tc.sessionID)
			if !called {
				t.Fatalf("handleSessionUpdate did not invoke the sub-handler (sub-dispatch returned early)")
			}
			if got != tc.want {
				t.Errorf("handleSessionUpdate subSessionID = %q, want %q (parent=%q, sessionID=%q)",
					got, tc.want, tc.registerPS, tc.sessionID)
			}
		})
	}
}

// captureAttribution is captureSubSession's sibling for the whole attribution, for the step case a string cannot express.
func captureAttribution(t *testing.T, h *Runtime, chatID marotte.ChatID, sessionID string) (got translate.FrameAttribution, called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[marotte.ACPUpdateKind]sessionUpdateHandler{
		marotte.ACPUpdateSessionInfo: func(_ context.Context, _ marotte.ChatID, _ json.RawMessage, attr translate.FrameAttribution) {
			got = attr
			called = true
		},
	}
	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateSessionInfo),
		"_meta":         map[string]any{"kiro": map[string]any{"kind": "turn_completion"}},
	})
	params := mustJSON(t, map[string]any{"sessionId": sessionID, "update": update})
	h.handleSessionUpdate(t.Context(), chatID, &marotte.RPCResponse{Params: params})
	return got, called
}

// TestHandleSessionUpdate_StepFrameIsAttributedWithoutAMetaBlock pins that KAS's session_info_update carries no
// `_meta.kiro.workflow` (buildSessionInfoUpdate merges no promptMeta), so the step fact must come from the session.
func TestHandleSessionUpdate_StepFrameIsAttributedWithoutAMetaBlock(t *testing.T) {
	const (
		chatID  = marotte.ChatID("chat-step")
		stepSID = "step-session-1"
	)
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, "wf_1", "build", "wf_1/build")

	got, called := captureAttribution(t, h, chatID, stepSID)
	if !called {
		t.Fatal("handleSessionUpdate did not invoke the sub-handler (sub-dispatch returned early)")
	}
	want := translate.FrameAttribution{SessionID: stepSID, RunID: "wf_1", NodePath: "wf_1/build", NodeID: "build", Step: true}
	if got != want {
		t.Errorf("handleSessionUpdate(step session %q, no _meta.kiro.workflow) attribution = %+v, want %+v",
			stepSID, got, want)
	}
}

// A step's config_option_update keeps its attribution through the dispatch table, or the step's model becomes the chat's.
func TestHandleSessionUpdate_AStepsConfigFrameLeavesTheChatsModelAlone(t *testing.T) {
	const (
		chatID  = marotte.ChatID("chat-step")
		stepSID = "step-session-1"
	)
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, "wf_1", "build", "wf_1/build")
	cs.seed(t, chatID, func(c *marotte.Chat) { c.Model = "opus" })

	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []map[string]any{{
			"id":           "model",
			"type":         "select",
			"currentValue": "fable",
			"options":      []map[string]any{{"value": "opus", "name": "Opus"}, {"value": "fable", "name": "Fable"}},
		}},
	})
	params := mustJSON(t, map[string]any{"sessionId": stepSID, "update": update})
	h.handleSessionUpdate(t.Context(), chatID, &marotte.RPCResponse{Params: params})

	c, _ := cs.Get(t.Context(), chatID)
	if c.Model != "opus" {
		t.Errorf("after a step's config frame Model = %q, want opus", c.Model)
	}
	if !slices.Equal(c.ServedModelIDs, []string{"opus", "fable"}) {
		t.Errorf("ServedModelIDs = %v, want [opus fable] (the catalog half still applies)", c.ServedModelIDs)
	}
}

// dispatchUpdate drives handleSessionUpdate with an update of `kind` plus extra fields and reports whether the live handler ran.
func dispatchUpdate(t *testing.T, h *Runtime, kind marotte.ACPUpdateKind, extra map[string]any) (called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[marotte.ACPUpdateKind]sessionUpdateHandler{
		kind: func(_ context.Context, _ marotte.ChatID, _ json.RawMessage, _ translate.FrameAttribution) {
			called = true
		},
	}
	update := map[string]any{"sessionUpdate": string(kind)}
	maps.Copy(update, extra)
	params := mustJSON(t, map[string]any{"sessionId": "", "update": mustJSON(t, update)})
	h.handleSessionUpdate(t.Context(), "c1",
		&marotte.RPCResponse{Method: "session/update", Params: params})
	return called
}

// TestHandleSessionUpdate_DropsReplayedFrames pins that session/load replays the transcript as session/update frames
// (kiro-cli 2.16.0: 9 frames, 6 tagged), and ungated they open a phantom turn. The flag is at
// `update._meta.kiro.replay`, not `params._meta`.
func TestHandleSessionUpdate_DropsReplayedFrames(t *testing.T) {
	replayMeta := map[string]any{"kiro": map[string]any{"replay": true}}

	tests := []struct {
		name       string
		extra      map[string]any
		wantCalled bool
	}{
		{
			name:       "a live frame reaches its handler",
			extra:      map[string]any{"content": map[string]any{"type": "text", "text": "x"}},
			wantCalled: true,
		},
		{
			name: "a replay-tagged frame is dropped",
			extra: map[string]any{
				"content": map[string]any{"type": "text", "text": "x"},
				"_meta":   replayMeta,
			},
			wantCalled: false,
		},
		{
			name: "replay:false is not a replay",
			extra: map[string]any{
				"content": map[string]any{"type": "text", "text": "x"},
				"_meta":   map[string]any{"kiro": map[string]any{"replay": false}},
			},
			wantCalled: true,
		},
		{
			name: "the flag one level UP does not gate — it rides `update`, not `params`",
			extra: map[string]any{
				"content": map[string]any{"type": "text", "text": "x"},
				"_meta":   map[string]any{"replay": true}, // missing the kiro nesting
			},
			wantCalled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			if got := dispatchUpdate(t, h, marotte.ACPUpdateAgentChunk, tt.extra); got != tt.wantCalled {
				t.Errorf("live handler called = %v, want %v", got, tt.wantCalled)
			}
		})
	}
}

// TestHandleSessionUpdate_CatalogFrameSurvivesALoad pins that the gate is per frame. config_option_update is untagged
// current state; gating on the load would empty the mode pill on every resume.
func TestHandleSessionUpdate_CatalogFrameSurvivesALoad(t *testing.T) {
	h, _, _ := newTestHub()
	if !dispatchUpdate(t, h, marotte.ACPUpdateConfigOption, nil) {
		t.Error("config_option_update was dropped; it is untagged by KAS and carries current session state")
	}
}
