package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route/routetest"
	"github.com/cplieger/marotte/internal/translate"
)

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

func TestHandleMCPReconnect_MethodNotAllowed(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("everything"))
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/reconnect", nil)
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.mcpRegistry.RegisterRoutes(routetest.On(mux))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestHandleMCPReconnect_UnknownServer(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("everything"))
	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// The reset addresses the oldest live session that binds the server, never every session holding the name.
func TestMCPReconnect_SendsTheBindingSessionID(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("everything"))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	reportMCP(t, h, "sess_a", connectedMCP("other", marotte.MCPSource{}))
	reportMCP(t, h, "sess_b", failedMCP("everything", marotte.MCPSource{}, "boom"))
	reportMCP(t, h, "sess_c", failedMCP("everything", marotte.MCPSource{}, "boom"))

	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	lane := fakeSourceOf(t, h).lane
	if n := countCalls(lane, methodV3MCPResetServer); n != 1 {
		t.Errorf("resets sent = %d, want 1 on the engine", n)
	}
	got := lane.paramsFor(methodV3MCPResetServer)
	if got[marotte.KeySessionID] != marotte.SessionID("sess_b") || got[keyServerName] != "everything" {
		t.Errorf("reset params = %v, want {sessionId: sess_b, serverName: everything}", got)
	}
}

// A server no live session binds has no instance to reset: the next session that binds it connects it.
func TestHandleMCPReconnect_AServerNoSessionBindsIsAConflict(t *testing.T) {
	for name, stage := range map[string]func(t *testing.T, h *Runtime){
		"no view lists it": func(*testing.T, *Runtime) {},
		"KAS says the session does not declare it": func(t *testing.T, h *Runtime) {
			reportMCP(t, h, "sess_a", failedMCP("everything", marotte.MCPSource{}, "boom"))
			fakeSourceOf(t, h).lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{
				Code: -32602, Message: "not declared",
				Data: json.RawMessage(`{"errorType":"McpServerNotDeclaredError","sessionId":"sess_a","serverName":"everything"}`),
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("everything"))
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			stage(t, h)

			rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)

			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "No open chat uses this server yet") {
				t.Errorf("reconnect = %d %s, want 409 naming why", rec.Code, rec.Body.String())
			}
		})
	}
}

// A reset the engine refuses is a failed reconnect, never a success.
func TestHandleMCPReconnect_ARefusedResetIsABadGateway(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("everything"))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	reportMCP(t, h, "sess_a", failedMCP("everything", marotte.MCPSource{}, "boom"))
	fakeSourceOf(t, h).lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "no such server"})
	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || rec.Code != http.StatusBadGateway || body.Error == "" {
		t.Errorf("a refused reset = %d %+v (%v), want 502 with an error", rec.Code, body, err)
	}
}

// With no engine running nothing can take the reset: the reconnect answers 503 and sends nothing.
func TestHandleMCPReconnect_WithNoEngineIsUnavailable(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.startGate = make(chan struct{})
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, WithMCPConfig(enabledConfig("everything")))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	reportMCP(t, h, "sess_a", failedMCP("everything", marotte.MCPSource{}, "boom"))

	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	if n := countCalls(src.lane, methodV3MCPResetServer); n != 0 {
		t.Errorf("resets sent = %d, want none", n)
	}
}

// A reset in flight is a door: a restart requested meanwhile stops the engine only after the reset answered.
func TestHandleMCPReconnect_ARestartWaitsForAResetInFlight(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("everything"))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	reportMCP(t, h, "sess_a", failedMCP("everything", marotte.MCPSource{}, "boom"))
	src := fakeSourceOf(t, h)
	inReset := make(chan struct{})
	answer := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once
	src.mu.Lock()
	src.callGate = func(_ context.Context, method string, resp *marotte.RPCResponse, err error) (*marotte.RPCResponse, error) {
		if method == methodV3MCPResetServer {
			close(inReset)
			<-answer
		}
		return resp, err
	}
	src.stopHook = func() { stopOnce.Do(func() { close(stopped) }) }
	src.mu.Unlock()
	codes := make(chan int, 1)
	go func() {
		codes <- postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`).Code
	}()
	<-inReset

	if !h.engine.requestRestart(restartCause{Kind: restartConfirmed}) {
		t.Fatal("the confirmed restart was refused")
	}
	select {
	case <-stopped:
		t.Fatal("the restart stopped the engine under a reset still in flight")
	case <-time.After(quietWindow):
	}
	close(answer)

	if code := <-codes; code != http.StatusNoContent {
		t.Errorf("the reset in flight = %d, want 204", code)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Error("the restart never stopped the engine after the reset answered")
	}
}

// TestHandleMCPReconnect_ReachesAServerKASReportsLive pins Reconnect for a live-reported workspace
// server, and not for one KAS reports disabled.
func TestHandleMCPReconnect_ReachesAServerKASReportsLive(t *testing.T) {
	cases := map[string]struct {
		report translate.MCPServerReport
		want   int
	}{
		"a workspace server reported connected": {
			report: connectedMCP("theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"}),
			want:   http.StatusNoContent,
		},
		"a power's server reported failed": {
			report: failedMCP("theirs", marotte.MCPSource{Origin: "power", Power: "p"}, "boom"),
			want:   http.StatusNoContent,
		},
		"a workspace server reported disabled": {
			report: disabledMCP("theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"}),
			want:   http.StatusNotFound,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("mine"))
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			reportMCP(t, h, "sess_a", tc.report)
			rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"theirs"}`)
			if rec.Code != tc.want {
				t.Errorf("handleReconnect(theirs) code = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestMCPRegistry_AConnectedFrameStoresDiscovery pins connect-time prompts, resources and templates in /api/mcp/status.
func TestMCPRegistry_AConnectedFrameStoresDiscovery(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	prompts := []marotte.MCPPromptInfo{{Name: "Simple Prompt", Description: "no args"}}
	resources := []marotte.MCPResourceInfo{{Name: "doc", URI: "demo://doc", MimeType: "text/markdown"}}
	templates := []marotte.MCPResourceTemplateInfo{{Name: "issue", URITemplate: "gh://issues/{number}"}}
	r := connectedMCP("everything", marotte.MCPSource{})
	r.Prompts, r.Resources, r.ResourceTemplates = prompts, resources, templates
	reportMCP(t, h, "s1", r)

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap[0].Prompts) != 1 || snap[0].Prompts[0].Name != "Simple Prompt" {
		t.Errorf("prompts = %+v", snap[0].Prompts)
	}
	if len(snap[0].Resources) != 1 || snap[0].Resources[0].URI != "demo://doc" {
		t.Errorf("resources = %+v", snap[0].Resources)
	}
	if len(snap[0].ResourceTemplates) != 1 || snap[0].ResourceTemplates[0].URITemplate != "gh://issues/{number}" {
		t.Errorf("resource templates = %+v", snap[0].ResourceTemplates)
	}
}

func postJSON(handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// Reconnect addresses the session the row shows: with one session connected and another waiting or failed, the row is
// the other one's, so its reset goes there and the connected session keeps its connection.
func TestReconnect_ResetsTheSessionTheRowShows(t *testing.T) {
	cases := map[string]struct {
		older, newer translate.MCPServerReport
		want         marotte.SessionID
	}{
		"the newer session waits": {
			older: connectedMCP("linear", marotte.MCPSource{}), newer: oauthMCP("linear", "https://p.example/passive"),
			want: "sess-newer",
		},
		"the newer session failed": {
			older: connectedMCP("linear", marotte.MCPSource{}), newer: failedMCP("linear", marotte.MCPSource{}, "boom"),
			want: "sess-newer",
		},
		"both connected": {
			older: connectedMCP("linear", marotte.MCPSource{}), newer: connectedMCP("linear", marotte.MCPSource{}),
			want: "sess-older",
		},
		"only the newer session lists it": {
			newer: failedMCP("linear", marotte.MCPSource{}, "boom"), want: "sess-newer",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("linear"))
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			lane := fakeSourceOf(t, h).lane
			if tc.older.Name != "" {
				reportMCP(t, h, "sess-older", tc.older)
			} else {
				reportMCP(t, h, "sess-older")
			}
			reportMCP(t, h, "sess-newer", tc.newer)

			if rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"linear"}`); rec.Code != http.StatusNoContent {
				t.Fatalf("reconnect = %d %s, want 204", rec.Code, rec.Body.String())
			}

			if p := lane.paramsFor(methodV3MCPResetServer); p[marotte.KeySessionID] != tc.want {
				t.Errorf("reset session = %v, want %s", p[marotte.KeySessionID], tc.want)
			}
		})
	}
}
