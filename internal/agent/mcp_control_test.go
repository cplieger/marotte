package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// insertLiveBridge inserts a live bridge for chatID and returns its fake.
func insertLiveBridge(t *testing.T, h *Runtime, chatID marotte.ChatID) *fakeBridge {
	t.Helper()
	sb, _ := h.bridge.mgr.orInsert(chatID)
	fb, ok := sb.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("bridge is %T, want *fakeBridge", sb.bridge)
	}
	return fb
}

// bridgeCalled reports whether the fake bridge received a call to method.
func bridgeCalled(b *fakeBridge, method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.calls, method)
}

// enabledConfig stages names as the user's own enabled servers, in both sets as a real store nests them.
func enabledConfig(names ...string) *fakeMCPConfig {
	return &fakeMCPConfig{enabled: nameSet(names...), configured: nameSet(names...)}
}

func nameSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

func TestReconnectMCPServer_FansOutToAllLiveBridges(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b1 := insertLiveBridge(t, h, "c1")
	b2 := insertLiveBridge(t, h, "c2")

	n := h.mcpRegistry.reconnectServer(t.Context(), "everything")
	if n != 2 {
		t.Fatalf("targeted = %d, want 2", n)
	}
	for i, b := range []*fakeBridge{b1, b2} {
		if !bridgeCalled(b, methodV3MCPResetServer) {
			t.Errorf("bridge %d did not receive %s", i, methodV3MCPResetServer)
		}
	}
}

func TestReconnectMCPServer_NoBridgesIsNoOp(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	if n := h.mcpRegistry.reconnectServer(t.Context(), "everything"); n != 0 {
		t.Fatalf("targeted = %d, want 0", n)
	}
}

func TestGetMCPPrompt_CallsBridgeAndReturnsResult(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b := insertLiveBridge(t, h, "c1")

	res, err := h.mcpRegistry.promptFor(t.Context(), "everything", "simple-prompt", nil)
	if err != nil {
		t.Fatalf("promptFor: %v", err)
	}
	if !bridgeCalled(b, methodV3MCPGetPrompt) {
		t.Errorf("bridge did not receive %s", methodV3MCPGetPrompt)
	}
	if len(res) == 0 {
		t.Error("empty result")
	}
}

func TestGetMCPResource_CallsBridge(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b := insertLiveBridge(t, h, "c1")

	if _, err := h.mcpRegistry.resourceFor(t.Context(), "everything", "demo://x"); err != nil {
		t.Fatalf("resourceFor: %v", err)
	}
	if !bridgeCalled(b, methodV3MCPGetResource) {
		t.Errorf("bridge did not receive %s", methodV3MCPGetResource)
	}
}

func TestMCPFetch_NoLiveBridgeErrors(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	_, err := h.mcpRegistry.promptFor(t.Context(), "everything", "p", nil)
	if !errors.Is(err, errNoLiveBridge) {
		t.Fatalf("err = %v, want errNoLiveBridge", err)
	}
}

func TestHandleMCPReconnect_MethodNotAllowed(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/reconnect", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleReconnect(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestHandleMCPReconnect_UnknownServer(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestHandleMCPReconnect_OK(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	insertLiveBridge(t, h, "c1")
	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Reconnected int `json:"reconnected"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Reconnected != 1 {
		t.Errorf("reconnected = %d, want 1", body.Reconnected)
	}
}

// TestHandleMCPReconnect_ReachesAServerKASReportsLive pins Reconnect for a live-reported workspace
// server, and not for one KAS reports disabled.
func TestHandleMCPReconnect_ReachesAServerKASReportsLive(t *testing.T) {
	cases := map[string]struct {
		record func(ctx context.Context, h *Runtime)
		want   int
	}{
		"a workspace server reported connected": {
			record: func(ctx context.Context, h *Runtime) {
				h.mcpRegistry.RecordConnected(ctx, "theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"}, nil, nil, nil, nil)
			},
			want: http.StatusOK,
		},
		"a power's server reported failed": {
			record: func(ctx context.Context, h *Runtime) {
				h.mcpRegistry.RecordInitFailure(ctx, "theirs", marotte.MCPSource{Origin: "power", Power: "p"}, "boom")
			},
			want: http.StatusOK,
		},
		"a workspace server reported disabled": {
			record: func(ctx context.Context, h *Runtime) {
				h.mcpRegistry.RecordDisabled(ctx, "theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"})
			},
			want: http.StatusNotFound,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(enabledConfig("mine"))
			insertLiveBridge(t, h, "c1")
			tc.record(t.Context(), h)
			rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"theirs"}`)
			if rec.Code != tc.want {
				t.Errorf("handleReconnect(theirs) code = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandleMCPGetPrompt_MissingPrompt(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.mcpRegistry.handlePrompt, "/api/mcp/prompt", `{"server":"everything"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleMCPGetPrompt_NoLiveBridgeConflict(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.mcpRegistry.handlePrompt, "/api/mcp/prompt", `{"server":"everything","prompt":"simple-prompt"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleMCPGetResource_OK(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	insertLiveBridge(t, h, "c1")
	rec := postJSON(h.mcpRegistry.handleResource, "/api/mcp/resource", `{"server":"everything","uri":"demo://x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestMCPRegistry_RecordConnectedStoresDiscovery pins connect-time prompts, resources and templates in /api/mcp/status.
func TestMCPRegistry_RecordConnectedStoresDiscovery(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	prompts := []marotte.MCPPromptInfo{{Name: "Simple Prompt", PromptName: "simple-prompt", Description: "no args"}}
	resources := []marotte.MCPResourceInfo{{Name: "doc", URI: "demo://doc", MimeType: "text/markdown"}}
	templates := []marotte.MCPResourceTemplateInfo{{Name: "issue", URITemplate: "gh://issues/{number}"}}
	h.mcpRegistry.RecordConnected(t.Context(), "everything", marotte.MCPSource{}, nil, prompts, resources, templates)

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap[0].Prompts) != 1 || snap[0].Prompts[0].PromptName != "simple-prompt" {
		t.Errorf("prompts = %+v", snap[0].Prompts)
	}
	if len(snap[0].Resources) != 1 || snap[0].Resources[0].URI != "demo://doc" {
		t.Errorf("resources = %+v", snap[0].Resources)
	}
	if len(snap[0].ResourceTemplates) != 1 || snap[0].ResourceTemplates[0].URITemplate != "gh://issues/{number}" {
		t.Errorf("resource templates = %+v", snap[0].ResourceTemplates)
	}
}

// postJSON posts a JSON body to an http.HandlerFunc.
func postJSON(handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// TestReconnectMCPServer_ReportsABridgeThatRefusedTheReset pins that the count is bridges targeted, so the
// log line is the only record of a refusal, and must not fire on success.
func TestReconnectMCPServer_ReportsABridgeThatRefusedTheReset(t *testing.T) {
	const wantLine = "mcp reconnect: bridge call failed"

	t.Run("a bridge that refused is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h := newHubWithMCPConfig(enabledConfig("everything"))
		b := insertLiveBridge(t, h, "c1")
		b.callErrs = map[string]error{methodV3MCPResetServer: errors.New("bridge wedged")}

		if n := h.mcpRegistry.reconnectServer(t.Context(), "everything"); n != 1 {
			t.Fatalf("targeted = %d, want 1", n)
		}
		out := logs.String()
		if !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a bridge that refused the reset said nothing, while the reply counts it as "+
				"reconnected; want a line reading %q. Got: %s", wantLine, out)
		}
		if !strings.Contains(out, `"server":"everything"`) {
			t.Errorf("the failure line does not name the server it is about: %s", out)
		}
	})

	t.Run("an ordinary reconnect is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := newHubWithMCPConfig(enabledConfig("everything"))
		insertLiveBridge(t, h, "c1")

		if n := h.mcpRegistry.reconnectServer(t.Context(), "everything"); n != 1 {
			t.Fatalf("targeted = %d, want 1", n)
		}
		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a reconnect every bridge accepted was reported as failed: %s", out)
		}
	})
}

// TestGetMCPPrompt_SendsAnArgumentsObjectEitherWay pins that `null` fails server validation where `{}`
// passes, and supplied arguments must not be replaced.
func TestGetMCPPrompt_SendsAnArgumentsObjectEitherWay(t *testing.T) {
	cases := []struct {
		args map[string]any
		name string
		want string
	}{
		{
			name: "no arguments become an empty object, never null",
			args: nil,
			want: `{}`,
		},
		{
			name: "the caller's arguments travel unchanged",
			args: map[string]any{"repo": "marotte"},
			want: `{"repo":"marotte"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHubWithMCPConfig(enabledConfig("everything"))
			b := insertLiveBridge(t, h, "c1")

			if _, err := h.mcpRegistry.promptFor(t.Context(), "everything", "p", c.args); err != nil {
				t.Fatalf("promptFor: %v", err)
			}
			params := b.paramsFor(methodV3MCPGetPrompt)
			if params == nil {
				t.Fatal("no getPrompt call was issued, so there are no arguments to inspect")
			}
			sent, err := json.Marshal(params["arguments"])
			if err != nil {
				t.Fatalf("marshal the arguments the call carried: %v", err)
			}
			if string(sent) != c.want {
				t.Errorf("promptFor(args=%v) sent arguments %s, want %s", c.args, sent, c.want)
			}
		})
	}
}

// TestWriteMCPResult_AlwaysWritesADecodableObject pins the {} fallback for an empty result only.
func TestWriteMCPResult_AlwaysWritesADecodableObject(t *testing.T) {
	cases := []struct {
		name string
		res  json.RawMessage
		want string
	}{
		{name: "an absent result becomes an empty object", res: nil, want: `{}`},
		{name: "an empty result becomes an empty object", res: json.RawMessage(``), want: `{}`},
		{
			name: "a real result is relayed verbatim",
			res:  json.RawMessage(`{"messages":[{"role":"user"}]}`),
			want: `{"messages":[{"role":"user"}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeMCPResult(rec, c.res)
			if got := strings.TrimSpace(rec.Body.String()); got != c.want {
				t.Errorf("writeMCPResult(%q) wrote %s, want %s", c.res, got, c.want)
			}
		})
	}
}
