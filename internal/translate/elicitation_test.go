package translate

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// elicitationHarness runs one _kiro/mcp/elicitation frame and records what it answered, filed
// and broadcast.
type elicitationHarness struct {
	deps   *pendingCaptureDeps
	events []marotte.ServerEvent
}

func runElicitation(t *testing.T, elicitation map[string]any) *elicitationHarness {
	t.Helper()
	h, tr := newElicitationHarness()
	h.ask(t, tr, "s", elicitation)
	return h
}

func newElicitationHarness() (*elicitationHarness, *Translator) {
	h := &elicitationHarness{deps: &pendingCaptureDeps{baseDeps: newBaseDeps()}}
	h.deps.onBroadcast = func(_ context.Context, evt marotte.ServerEvent) { h.events = append(h.events, evt) }
	return h, New(rolesOf(h.deps))
}

// ask sends one elicitation naming tool call tc-1 from sessionID.
func (h *elicitationHarness) ask(t *testing.T, tr *Translator, sessionID string, elicitation map[string]any) {
	t.Helper()
	id := int64(31)
	tr.HandleElicitationCreate(t.Context(), "c1", h.deps.origin(), &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
		"sessionId": sessionID, "toolCallId": "tc-1", "elicitation": elicitation,
	})})
}

func (h *elicitationHarness) needed(t *testing.T) (marotte.ElicitationNeededPayload, bool) {
	t.Helper()
	for _, e := range h.events {
		if e.Type != marotte.EventElicitationNeeded {
			continue
		}
		p, ok := e.Payload.(marotte.ElicitationNeededPayload)
		if !ok {
			t.Fatalf("elicitation_needed payload = %T", e.Payload)
		}
		return p, true
	}
	return marotte.ElicitationNeededPayload{}, false
}

// MCP 2025-11-25 "Safe URL Handling": a client opens only a web link. KAS's own gate admits
// any scheme on a loopback host, so the refusal is marotte's, answered as KAS answers its own.
func TestHandleElicitationCreate_DeclinesALinkThatIsNotAWebAddress(t *testing.T) {
	refused := []string{
		"vscode://localhost/open",
		"javascript://localhost/%0Aalert(1)",
		"file://127.0.0.1/etc/passwd",
		"smb://127.0.0.1/share",
		"https:foo",
		"",
	}
	for _, url := range refused {
		t.Run("refused "+url, func(t *testing.T) {
			h := runElicitation(t, map[string]any{"mode": "url", "message": "Connect", "url": url})

			if len(h.deps.asked) != 1 {
				t.Fatalf("got %d answers, want 1", len(h.deps.asked))
			}
			got := h.deps.asked[0]
			if got.rpcErr != nil {
				t.Errorf("answered with rpcErr %v, want a result", got.rpcErr)
			}
			if gotJSON, wantJSON := string(mustJSON(t, got.result)), `{"action":"decline"}`; gotJSON != wantJSON {
				t.Errorf("answer = %s, want %s", gotJSON, wantJSON)
			}
			if _, shown := h.needed(t); shown || len(h.deps.pendingAdds) != 0 {
				t.Errorf("shown = %v, pending adds = %v, want neither", shown, h.deps.pendingAdds)
			}
		})
	}
	for _, url := range []string{"https://example.com/connect", "http://localhost:8080/cb"} {
		t.Run("shown "+url, func(t *testing.T) {
			h := runElicitation(t, map[string]any{"mode": "url", "message": "Connect", "url": url})

			if len(h.deps.asked) != 0 {
				t.Errorf("answered %d times, want none: the user answers a web link", len(h.deps.asked))
			}
			if p, shown := h.needed(t); !shown || p.URL != url {
				t.Errorf("elicitation_needed = %+v, %v, want it shown with url %q", p, shown, url)
			}
		})
	}
}

// Every MCP 2025-11-25 primitive variant reaches the card: a dropped field renders a text box
// where the server asked for a picker, and the server-side check could not see the rule.
func TestHandleElicitationCreate_KeepsEveryMCPSchemaVariant(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"size":   map[string]any{"type": "string", "title": "Size", "oneOf": []any{map[string]any{"const": "s", "title": "Small"}}},
			"legacy": map[string]any{"type": "string", "title": "Legacy", "enum": []any{"r"}, "enumNames": []any{"Red"}},
			"tags": map[string]any{
				"type": "array", "title": "Tags", "minItems": 1.0, "maxItems": 2.0, "default": []any{"a"},
				"items": map[string]any{"type": "string", "enum": []any{"a", "b"}},
			},
			"titled": map[string]any{"type": "array", "title": "Titled", "items": map[string]any{"anyOf": []any{map[string]any{"const": "x", "title": "X"}}}},
			"score":  map[string]any{"type": "number", "title": "Score", "minimum": 1.5, "maximum": 9.0},
		},
		"required": []any{"size"},
	}
	h := runElicitation(t, map[string]any{"mode": "form", "message": "Pick", "requestedSchema": schema})

	p, shown := h.needed(t)
	if !shown {
		t.Fatal("no elicitation_needed broadcast")
	}
	var got, want any
	if err := json.Unmarshal(mustJSON(t, p.RequestedSchema), &got); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if err := json.Unmarshal(mustJSON(t, schema), &want); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotJSON, wantJSON := string(mustJSON(t, got)), string(mustJSON(t, want)); gotJSON != wantJSON {
		t.Errorf("requested_schema = %s\nwant %s", gotJSON, wantJSON)
	}
}

// A field label is as much the thing the user consents to as the message is, so the schema's
// display strings get the message's treatment while every value the answer carries stays as sent.
func TestHandleElicitationCreate_NeutralizesTheSchemasDisplayText(t *testing.T) {
	deceptive := "Share " + rlo + "gnihton" + pdf
	schema := map[string]any{
		"type": "object", "title": deceptive, "description": deceptive,
		"properties": map[string]any{
			"secret": map[string]any{"type": "string", "title": deceptive, "description": deceptive},
			"size": map[string]any{"type": "string", "title": "Size", "oneOf": []any{
				map[string]any{"const": "s" + rlo, "title": deceptiveOption},
			}},
			"legacy": map[string]any{"type": "string", "title": "Legacy", "enum": []any{"r"}, "enumNames": []any{deceptiveOption}},
			"tags": map[string]any{"type": "array", "title": "Tags", "items": map[string]any{"anyOf": []any{
				map[string]any{"const": "x", "title": deceptiveOption},
			}}},
		},
	}
	h := runElicitation(t, map[string]any{"mode": "form", "message": "Pick", "requestedSchema": schema})

	p, shown := h.needed(t)
	if !shown {
		t.Fatal("no elicitation_needed broadcast")
	}
	got := p.RequestedSchema
	if want := displayText(deceptive); got.Properties["secret"].Title != want {
		t.Errorf("secret title = %q, want %q: the reversed span stays present", got.Properties["secret"].Title, want)
	}
	if c := got.Properties["size"].OneOf[0].Const; c != "s"+rlo {
		t.Errorf("const = %q, want the server's value: the answer must match it", c)
	}
	got.Properties["size"].OneOf[0].Const = "s"
	assertNeutralizedOnTheWire(t, "requested_schema", got)
}

func TestHandleElicitationCreate_LabelsAnUntitledFieldByItsNeutralizedName(t *testing.T) {
	name := "token" + rlo + "eman"
	h := runElicitation(t, map[string]any{"mode": "form", "message": "Pick", "requestedSchema": map[string]any{
		"type": "object", "properties": map[string]any{name: map[string]any{"type": "string"}},
	}})

	p, shown := h.needed(t)
	if !shown {
		t.Fatal("no elicitation_needed broadcast")
	}
	prop, ok := p.RequestedSchema.Properties[name]
	if !ok {
		t.Fatalf("properties = %v, want the server's key %q kept: the answer is keyed by it", p.RequestedSchema.Properties, name)
	}
	if want := displayText(name); prop.Title != want {
		t.Errorf("title = %q, want %q", prop.Title, want)
	}
}

// MCP 2025-11-25 Security Considerations: the client MUST show which server requests the
// input. KAS's elicitation names only the tool call, so the server comes from that call's stamp.
func TestHandleElicitationCreate_NamesTheServerOfTheCallItInterrupts(t *testing.T) {
	mcpCall := map[string]any{
		"toolCallId": "tc-1", "title": "@probe/echo_text", "kind": "other", "status": "pending",
		"_meta": map[string]any{"kiro": map[string]any{"serverName": "probe"}},
	}
	builtin := map[string]any{"toolCallId": "tc-1", "title": "readFile", "kind": "read", "status": "pending"}
	step := FrameAttribution{Step: true, SessionID: "s-step", RunID: "wf_1", NodePath: "n1"}
	probe := &marotte.MCPToolIdentity{ServerName: "probe", ToolName: "echo_text"}
	cases := map[string]struct {
		call    map[string]any
		attr    FrameAttribution
		session string
		want    *marotte.MCPToolIdentity
	}{
		"the chat's own MCP call":         {mcpCall, FrameAttribution{}, "s", probe},
		"a workflow step's MCP call":      {mcpCall, step, "s-step", probe},
		"a built-in tool's call":          {builtin, FrameAttribution{}, "s", nil},
		"no open call with that id":       {nil, FrameAttribution{}, "s", nil},
		"a step's call asked by the chat": {mcpCall, step, "s", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, tr := newElicitationHarness()
			tr.RecordStepSession("s-step", "wf_1", "n1", "n1")
			if c.call != nil {
				tr.HandleToolCall(t.Context(), "c1", mustJSON(t, c.call), c.attr)
			}
			h.ask(t, tr, c.session, map[string]any{"mode": "form", "message": "Pick"})

			p, shown := h.needed(t)
			if !shown {
				t.Fatal("no elicitation_needed broadcast")
			}
			if !reflect.DeepEqual(p.MCPTool, c.want) {
				t.Errorf("mcp_tool = %+v, want %+v", p.MCPTool, c.want)
			}
		})
	}
}

// Reading the asking call never opens a run turn for a step that has none.
func TestHandleElicitationCreate_OpensNoTurnForAStepWithNone(t *testing.T) {
	h, tr := newElicitationHarness()
	tr.RecordStepSession("s-step", "wf_1", "n1", "n1")

	h.ask(t, tr, "s-step", map[string]any{"mode": "form", "message": "Pick"})

	if len(h.deps.turns.runs) != 0 {
		t.Errorf("run turns = %v, want none opened", h.deps.turns.runs)
	}
	if p, shown := h.needed(t); !shown || p.MCPTool != nil {
		t.Errorf("shown = %v, mcp_tool = %+v, want shown with none", shown, p.MCPTool)
	}
}
