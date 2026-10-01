package translate

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// newEventCaptureDeps returns a baseDeps that captures broadcast events into
// the returned slice pointer. Tests read *events after exercising the
// translator.
func newEventCaptureDeps() (*baseDeps, *[]marotte.ServerEvent) {
	events := &[]marotte.ServerEvent{}
	deps := newBaseDeps()
	deps.onBroadcast = func(_ context.Context, evt marotte.ServerEvent) {
		*events = append(*events, evt)
	}
	return deps, events
}

// --- Event sequence tests ---

// The first delta of a stream OPENS an entry and every later one is a delta onto
// it: one frame each, so a client that missed neither holds exactly what the turn
// holds.
func TestSequence_AssistantChunk_OpensAnEntryThenDeltas(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello"},
	}), false, FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != marotte.EventEntryOpened {
		t.Fatalf("first chunk: events = %v, want one entry_opened", eventTypes(*events))
	}
	opened, ok := (*events)[0].Payload.(marotte.EntryOpenedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if opened.Open.Kind != marotte.EntryKindText || opened.Open.Text != "Hello" || opened.Open.N != 1 {
		t.Errorf("entry_opened = %+v, want a text entry holding %q at n=1", opened.Open, "Hello")
	}

	*events = nil
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": " world"},
	}), false, FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != marotte.EventEntryDelta {
		t.Fatalf("second chunk: events = %v, want one entry_delta", eventTypes(*events))
	}
	delta, ok := (*events)[0].Payload.(marotte.EntryDeltaPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if delta.EntryID != opened.Open.ID || delta.Delta != " world" || delta.N != 2 {
		t.Errorf("entry_delta = %+v, want %q at n=2 onto entry %q", delta, " world", opened.Open.ID)
	}
	open := deps.turns.chats[chatID].OpenEntries()
	if len(open) != 1 || open[0].Text != "Hello world" {
		t.Errorf("open after two deltas = %+v, want the one text entry %q", open, "Hello world")
	}
}

// A tool call is born sealed in the lane of the text it interrupts: the open text
// seals first, then the call lands as its own entry, and both reach the wire in
// that order.
func TestSequence_ToolCall_SealsTheTextThenAppendsTheCall(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Let me check..."},
	}), false, FrameAttribution{})
	*events = nil

	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "in_progress",
	}), FrameAttribution{})

	types := eventTypes(*events)
	if len(types) < 2 || types[0] != string(marotte.EventEntrySealed) || types[1] != string(marotte.EventEntryAppended) {
		t.Fatalf("events = %v, want entry_sealed then entry_appended", types)
	}
	sealed, ok := (*events)[0].Payload.(marotte.EntrySealedPayload)
	if !ok || sealed.N != 1 {
		t.Errorf("entry_sealed = %+v (%T), want the one-delta text entry sealed", (*events)[0].Payload, (*events)[0].Payload)
	}
	entries := deps.chatEntries(chatID)
	if kinds := entryKinds(entries); !equalKinds(kinds, []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindToolCall}) {
		t.Fatalf("sealed kinds = %v, want [text tool_call]", kinds)
	}
	calls := toolCallsOf(t, entries)
	if len(calls) != 1 || calls[0].ID != "tc1" || calls[0].Title != "readFile" {
		t.Errorf("tool_call entries = %+v, want tc1 readFile", calls)
	}
	if open := deps.turns.chats[chatID].OpenEntries(); len(open) != 0 {
		t.Errorf("open after the call = %+v, want none", open)
	}
}

// A non-terminal update is a tool_progress delta onto the call the turn still
// holds; the terminal one settles it as a tool_result entry and the turn lets the
// call go.
func TestSequence_ToolCallUpdate_ProgressThenResult(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")

	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "x"},
	}), false, FrameAttribution{})
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc1",
		"title":      "readFile",
		"kind":       "read",
		"status":     "pending",
	}), FrameAttribution{})
	*events = nil

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc1",
		"status":     "in_progress",
	}), FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != marotte.EventToolProgress {
		t.Fatalf("in_progress update: events = %v, want one tool_progress", eventTypes(*events))
	}
	progress, ok := (*events)[0].Payload.(marotte.ToolProgressPayload)
	if !ok || progress.ToolCallID != "tc1" || progress.Status != marotte.ToolInProgress {
		t.Errorf("tool_progress = %+v (%T), want tc1 in_progress", (*events)[0].Payload, (*events)[0].Payload)
	}
	if _, held := deps.turns.chats[chatID].OpenCallFor("tc1"); !held {
		t.Errorf("the turn let tc1 go on a non-terminal update")
	}

	*events = nil
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc1",
		"status":     "completed",
	}), FrameAttribution{})

	if !hasEntryAppended(events, marotte.EntryKindToolResult) || hasEventType(*events, marotte.EventToolProgress) {
		t.Fatalf("completed update: events = %v, want entry_appended{tool_result} and no tool_progress", eventTypes(*events))
	}
	results := toolResultsOf(t, deps.chatEntries(chatID))
	if len(results) != 1 || results[0].Status != marotte.ToolCompleted {
		t.Errorf("tool_result entries = %+v, want tc1 completed", results)
	}
	if _, held := deps.turns.chats[chatID].OpenCallFor("tc1"); held {
		t.Errorf("the turn still holds tc1 after its terminal update")
	}
}

func TestSequence_MCPStatus_RecordsConnection(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var recorded string
	wrapper := &mcpCaptureDeps{baseDeps: deps, connected: &recorded}
	tr := New(rolesOf(wrapper))

	// v3 consolidated MCP status: a "connected" server records a connection.
	tr.HandleMCPStatus(t.Context(), "", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"servers": []map[string]any{
				{"name": "github", "status": "connected"},
			},
		}),
	})

	if recorded != "github" {
		t.Errorf("RecordConnected called with %q, want %q", recorded, "github")
	}
}

// TestSequence_MCPStatus_RoutesDisabledToTheRecorder pins the amendment: the
// "disabled" status reaches the recorder instead of falling into the default arm
// that discarded it. Whether it produces a row is the recorder's call (only an
// unconfigured server gets one) — but the frame has to arrive for that call to
// be possible at all.
func TestSequence_MCPStatus_RoutesDisabledToTheRecorder(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var disabled []string
	tr := New(rolesOf(&mcpCaptureDeps{baseDeps: deps, disabled: &disabled}))

	tr.HandleMCPStatus(t.Context(), "", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"servers": []map[string]any{
				{"name": "off-server", "status": "disabled"},
				// "connecting" is transient, not terminal: it must stay discarded,
				// or a row would be painted that the next frame replaces.
				{"name": "starting-server", "status": "connecting"},
				// A nameless entry is unaddressable and skipped before the switch.
				{"name": "", "status": "disabled"},
			},
		}),
	})

	if len(disabled) != 1 || disabled[0] != "off-server" {
		t.Errorf("RecordDisabled calls = %v, want just [off-server]", disabled)
	}
}

func TestSequence_MCPStatus_CapturesToolsPromptsAndResources(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var connected string
	var tools []string
	var prompts []marotte.MCPPromptInfo
	var resources []marotte.MCPResourceInfo
	wrapper := &mcpCaptureDeps{baseDeps: deps, connected: &connected, tools: &tools, prompts: &prompts, resources: &resources}
	tr := New(rolesOf(wrapper))

	tr.HandleMCPStatus(t.Context(), "", &marotte.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"servers": []map[string]any{
				{
					"name":   "everything",
					"status": "connected",
					"tools": []map[string]any{
						{"name": "search"},
						{"name": "fetch"},
						{"name": ""}, // dropped: unaddressable
					},
					"prompts": []map[string]any{
						{"name": "Simple Prompt", "promptName": "simple-prompt", "description": "no args"},
						{"name": "Args Prompt", "promptName": "args-prompt", "arguments": []map[string]any{
							{"name": "city", "required": true},
						}},
						{"name": "no id", "promptName": ""}, // dropped: no machine name
					},
					"resources": []map[string]any{
						{"name": "doc", "uri": "demo://doc", "mimeType": "text/markdown"},
						{"name": "no uri", "uri": ""}, // dropped: unaddressable
					},
				},
			},
		}),
	})

	if connected != "everything" {
		t.Fatalf("connected = %q", connected)
	}
	// The tool names are what the MCP page lists the server's capabilities
	// from, so losing them leaves a connected server looking capability-free.
	if !slices.Equal(tools, []string{"search", "fetch"}) {
		t.Errorf("tools = %v, want [search fetch] (empty name dropped)", tools)
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %+v, want 2 (empty promptName dropped)", prompts)
	}
	if prompts[0].PromptName != "simple-prompt" || prompts[1].PromptName != "args-prompt" {
		t.Errorf("prompt names = %+v", prompts)
	}
	if len(prompts[1].Arguments) != 1 || !prompts[1].Arguments[0].Required {
		t.Errorf("args = %+v", prompts[1].Arguments)
	}
	if len(resources) != 1 || resources[0].URI != "demo://doc" {
		t.Errorf("resources = %+v, want 1 (empty uri dropped)", resources)
	}
}

type mcpCaptureDeps struct {
	*baseDeps
	connected *string
	tools     *[]string
	prompts   *[]marotte.MCPPromptInfo
	resources *[]marotte.MCPResourceInfo
	disabled  *[]string
	failures  *[]mcpFailure
}

func (d *mcpCaptureDeps) MCPRecorder() MCPRecorder {
	return &captureMCPRecorder{
		connected: d.connected, tools: d.tools, prompts: d.prompts,
		resources: d.resources, disabled: d.disabled, failures: d.failures,
	}
}

// mcpFailure is one RecordInitFailure call, so a test can read the reason the
// handler chose rather than only that a failure was recorded.
type mcpFailure struct {
	name   string
	reason string
}

type captureMCPRecorder struct {
	connected *string
	tools     *[]string
	prompts   *[]marotte.MCPPromptInfo
	resources *[]marotte.MCPResourceInfo
	disabled  *[]string
	failures  *[]mcpFailure
}

func (r *captureMCPRecorder) RecordConnected(_ context.Context, name string, tools []string, prompts []marotte.MCPPromptInfo, resources []marotte.MCPResourceInfo) {
	if r.connected != nil {
		*r.connected = name
	}
	if r.tools != nil {
		*r.tools = tools
	}
	if r.prompts != nil {
		*r.prompts = prompts
	}
	if r.resources != nil {
		*r.resources = resources
	}
}
func (*captureMCPRecorder) RecordOAuth(context.Context, string, string) {}
func (*captureMCPRecorder) SignalReady()                                {}

func (r *captureMCPRecorder) RecordInitFailure(_ context.Context, name, reason string) {
	if r.failures != nil {
		*r.failures = append(*r.failures, mcpFailure{name: name, reason: reason})
	}
}

func (r *captureMCPRecorder) RecordDisabled(_ context.Context, name string) {
	if r.disabled != nil {
		*r.disabled = append(*r.disabled, name)
	}
}

func TestSequence_ReasoningChunk_RoutesToReasoningBuilder(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c-reason")

	// Send a reasoning chunk (isReasoning=true)
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "thinking..."},
	}), true, FrameAttribution{})

	// The turn holds one open THINKING entry and nothing of kind text.
	open := deps.turns.chats[chatID].OpenEntries()
	if len(open) != 1 || open[0].Kind != marotte.EntryKindThinking || open[0].Text != "thinking..." {
		t.Errorf("open entries = %+v, want one thinking entry %q", open, "thinking...")
	}
	if !hasEventType(*events, marotte.EventEntryOpened) {
		t.Fatal("no entry_opened event emitted")
	}

	// Send a regular text chunk (isReasoning=false): the kind change seals the
	// thinking and opens a text entry in the same lane.
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "answer"},
	}), false, FrameAttribution{})

	open = deps.turns.chats[chatID].OpenEntries()
	if len(open) != 1 || open[0].Kind != marotte.EntryKindText || open[0].Text != "answer" {
		t.Errorf("open entries = %+v, want one text entry %q", open, "answer")
	}
	sealed := deps.chatEntries(chatID)
	if len(sealed) != 1 || sealed[0].Kind != marotte.EntryKindThinking {
		t.Errorf("sealed entries = %+v, want the thinking sealed by the kind change", sealed)
	}
}

// hasEventType reports whether events carries a frame of type et.
func hasEventType(events []marotte.ServerEvent, et marotte.EventType) bool {
	for _, e := range events {
		if e.Type == et {
			return true
		}
	}
	return false
}

// --- Helpers ---

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}

func eventTypes(events []marotte.ServerEvent) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = string(e.Type)
	}
	return types
}
