package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// signInHub is a runtime with a ready engine whose store holds names, each waiting for sign-in.
func signInHub(t *testing.T, names ...string) (*Runtime, *fakeBridge) {
	t.Helper()
	h := newHubWithMCPConfig(t, enabledConfig(names...))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	for _, n := range names {
		h.mcpRegistry.RecordOAuth(t.Context(), n, marotte.MCPSource{})
	}
	return h, fakeSourceOf(t, h).lane
}

func postSignIn(h *Runtime, server string) (int, string) {
	rec := postJSON(h.mcpRegistry.handleSignIn, "/api/mcp/sign-in", `{"server":"`+server+`"}`)
	return rec.Code, rec.Body.String()
}

// awaitResets waits until the engine took n resets.
func awaitResets(t *testing.T, lane *fakeBridge, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for countCalls(lane, methodV3MCPResetServer) < n {
		if time.Now().After(deadline) {
			t.Fatalf("resets sent = %d, want %d", countCalls(lane, methodV3MCPResetServer), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func slotServer(h *Runtime) string {
	h.mcpRegistry.mu.RLock()
	defer h.mcpRegistry.mu.RUnlock()
	return h.mcpRegistry.signIn.server
}

func rowOf(t *testing.T, h *Runtime, name string) mcpServerRuntime {
	t.Helper()
	for _, s := range h.mcpRegistry.snapshot() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %q is not in the registry", name)
	return mcpServerRuntime{}
}

// A sign-in is KAS's explicit reauth: the one reset that keeps a redirect listener waiting.
func TestSignIn_SendsStartOAuthOnce(t *testing.T) {
	h, lane := signInHub(t, "linear")

	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	if n := countCalls(lane, methodV3MCPResetServer); n != 1 {
		t.Errorf("resets sent = %d, want exactly 1", n)
	}
	p := lane.paramsFor(methodV3MCPResetServer)
	if p[keyServerName] != "linear" || p[keyStartOAuth] != true {
		t.Errorf("reset params = %v, want serverName linear and startOAuth true", p)
	}
	if got := slotServer(h); got != "linear" {
		t.Errorf("slot = %q after the reset answered, want it held until the server reports", got)
	}
}

// Reconnect stays a passive retry: it never discards credentials.
func TestReconnect_SendsNoStartOAuth(t *testing.T) {
	h, lane := signInHub(t, "linear")
	if rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"linear"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("reconnect = %d, want 204", rec.Code)
	}
	if _, has := lane.paramsFor(methodV3MCPResetServer)[keyStartOAuth]; has {
		t.Errorf("reconnect params = %v, want no startOAuth", lane.paramsFor(methodV3MCPResetServer))
	}
}

// KAS's startOAuth discards every stored credential, so only a server waiting for sign-in may take one.
func TestSignIn_RefusesAConnectedServer(t *testing.T) {
	cases := map[string]struct {
		record func(ctx context.Context, h *Runtime)
		want   string
	}{
		"connected": {
			record: func(ctx context.Context, h *Runtime) {
				h.mcpRegistry.RecordConnected(ctx, "linear", marotte.MCPSource{}, nil, nil, nil, nil)
			},
			want: errSignInConnected.Error(),
		},
		"failed for another reason": {
			record: func(ctx context.Context, h *Runtime) {
				h.mcpRegistry.RecordInitFailure(ctx, "linear", marotte.MCPSource{}, "boom")
			},
			want: errSignInNotNeeded.Error(),
		},
		"not reported at all": {
			record: func(context.Context, *Runtime) {},
			want:   errSignInNotNeeded.Error(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("linear"))
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			tc.record(t.Context(), h)

			code, body := postSignIn(h, "linear")

			if code != http.StatusConflict || !strings.Contains(body, tc.want) {
				t.Errorf("sign-in = %d %s, want 409 %q", code, body, tc.want)
			}
			joinInflight(t, h)
			if n := countCalls(fakeSourceOf(t, h).lane, methodV3MCPResetServer); n != 0 {
				t.Errorf("resets sent = %d, want none", n)
			}
			if got := slotServer(h); got != "" {
				t.Errorf("slot = %q after a refusal, want free", got)
			}
		})
	}
}

func TestSignIn_RefusesAServerAlreadySigningIn(t *testing.T) {
	h, lane := signInHub(t, "linear")
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("first sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	h.mcpRegistry.claimSignInURL(t.Context(), "https://provider.example/authorize?state=s")

	if code, body := postSignIn(h, "linear"); code != http.StatusConflict || !strings.Contains(body, errSignInInProgress.Error()) {
		t.Errorf("second sign-in = %d %s, want 409 in progress", code, body)
	}
	joinInflight(t, h)
	if n := countCalls(lane, methodV3MCPResetServer); n != 1 {
		t.Errorf("resets sent = %d, want 1", n)
	}
}

func TestSignIn_RefusesAnUnknownServer(t *testing.T) {
	h, _ := signInHub(t, "linear")
	if code, _ := postSignIn(h, "nope"); code != http.StatusNotFound {
		t.Errorf("sign-in(nope) = %d, want 404", code)
	}
	if rec := postJSON(h.mcpRegistry.handleSignIn, "/api/mcp/sign-in", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("sign-in with no server = %d, want 404", rec.Code)
	}
}

// openExternalUrl names no server, so a second concurrent sign-in would make its consent page unattributable.
func TestSignIn_OneSlotPerEngine(t *testing.T) {
	h, lane := signInHub(t, "linear", "github")
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("first sign-in = %d, want 202", code)
	}

	code, body := postSignIn(h, "github")

	if code != http.StatusConflict || !strings.Contains(body, errSignInSlotTaken.Error()) {
		t.Errorf("second server's sign-in = %d %s, want 409 slot taken", code, body)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)
	if p := lane.paramsFor(methodV3MCPResetServer); p[keyServerName] != "linear" {
		t.Errorf("reset sent for %v, want only linear's", p[keyServerName])
	}
}

// fakeClock is a settable clock for the slot's deadline.
type fakeClock struct {
	t  time.Time
	mu sync.Mutex
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// A slot whose server never reported is taken over once KAS's own connect ceiling has passed.
func TestSignIn_SlotExpiresAtDeadline(t *testing.T) {
	h, lane := signInHub(t, "linear", "github")
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("first sign-in = %d, want 202", code)
	}

	clock.advance(signInSlotTTL - time.Second)
	if code, _ := postSignIn(h, "github"); code != http.StatusConflict {
		t.Fatalf("sign-in a second before the deadline = %d, want 409", code)
	}
	if _, ok := h.mcpRegistry.claimSignInURL(t.Context(), "https://p.example/a?state=s"); !ok {
		t.Error("a consent URL before the deadline was not claimed")
	}
	h.mcpRegistry.RecordOAuth(t.Context(), "linear", marotte.MCPSource{})
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("re-sign-in = %d, want 202", code)
	}
	clock.advance(signInSlotTTL)
	if _, ok := h.mcpRegistry.claimSignInURL(t.Context(), "https://p.example/a?state=s"); ok {
		t.Error("a consent URL after the deadline was claimed by an expired slot")
	}

	if code, body := postSignIn(h, "github"); code != http.StatusAccepted {
		t.Errorf("sign-in after the deadline = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 3)
	if got := slotServer(h); got != "github" {
		t.Errorf("slot = %q, want github's", got)
	}
}

// A reset the engine refuses ends the sign-in at once, and the row says why.
func TestSignIn_EngineErrorReleasesTheSlot(t *testing.T) {
	h, lane := signInHub(t, "linear")
	lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "MCP governance is disabled by your organization"})

	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	if got := slotServer(h); got != "" {
		t.Errorf("slot = %q after a refused reset, want free", got)
	}
	row := rowOf(t, h, "linear")
	if row.State != mcpStateFailed || !strings.Contains(row.Error, "MCP governance is disabled") {
		t.Errorf("row = %+v, want failed with the engine's reason", row)
	}
	if len(eventsOfType(h, marotte.EventMCPFailed)) == 0 {
		t.Error("no mcp_failed reached the clients")
	}
}

// A foreign row keeps its provenance through a refused sign-in.
func TestSignIn_EngineErrorKeepsAForeignRowsOrigin(t *testing.T) {
	h, lane := signInHub(t, "mine")
	h.mcpRegistry.RecordOAuth(t.Context(), "theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"})
	lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "no such server"})

	if code, _ := postSignIn(h, "theirs"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	if row := rowOf(t, h, "theirs"); row.State != mcpStateFailed || row.Origin != marotte.OriginWorkspace || row.OriginRoot != "/w" {
		t.Errorf("row = %+v, want failed and still the workspace's", row)
	}
}

func TestSignIn_WithNoEngineIsUnavailable(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.startGate = make(chan struct{})
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, WithMCPConfig(enabledConfig("linear")))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.mcpRegistry.RecordOAuth(t.Context(), "linear", marotte.MCPSource{})

	if code, _ := postSignIn(h, "linear"); code != http.StatusServiceUnavailable {
		t.Errorf("sign-in = %d, want 503", code)
	}
	if got := slotServer(h); got != "" {
		t.Errorf("slot = %q with no engine, want free", got)
	}
}

// The slot ends with the next status KAS reports for its server, whatever it is.
func TestSignIn_TerminalStatusReleasesTheSlot(t *testing.T) {
	cases := map[string]func(ctx context.Context, r *mcpRegistry, name string){
		"connected": func(ctx context.Context, r *mcpRegistry, name string) {
			r.RecordConnected(ctx, name, marotte.MCPSource{}, nil, nil, nil, nil)
		},
		"waiting for sign-in again": func(ctx context.Context, r *mcpRegistry, name string) {
			r.RecordOAuth(ctx, name, marotte.MCPSource{})
		},
		"failed": func(ctx context.Context, r *mcpRegistry, name string) {
			r.RecordInitFailure(ctx, name, marotte.MCPSource{}, "timed out")
		},
		"disabled, marotte's own": func(ctx context.Context, r *mcpRegistry, name string) {
			r.RecordDisabled(ctx, name, marotte.MCPSource{})
		},
		"disabled, a workspace's": func(ctx context.Context, r *mcpRegistry, name string) {
			r.RecordDisabled(ctx, name, marotte.MCPSource{Origin: "workspace", Root: "/w"})
		},
		"the engine ended": func(ctx context.Context, r *mcpRegistry, _ string) {
			r.clearAll(ctx)
		},
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := signInHub(t, "linear", "github")
			if _, err := h.mcpRegistry.claimSignIn("linear"); err != nil {
				t.Fatalf("claimSignIn = %v", err)
			}
			if name != "the engine ended" {
				report(t.Context(), h.mcpRegistry, "github")
				if got := slotServer(h); got != "linear" {
					t.Fatalf("another server's status freed the slot (slot = %q)", got)
				}
			}

			report(t.Context(), h.mcpRegistry, "linear")

			if got := slotServer(h); got != "" {
				t.Errorf("slot = %q after the server reported, want free", got)
			}
		})
	}
}

// The consent URL KAS opens under a held slot belongs to that server's row, and no chat banner is raised.
func TestOpenExternalURL_ClaimedBySignInRecordsTheAnchor(t *testing.T) {
	const consent = "https://provider.example/authorize?state=s1&redirect_uri=http%3A%2F%2F127.0.0.1%3A4000%2Foauth%2Fcallback"
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.mcpRegistry.RecordOAuth(t.Context(), "theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"})
	if _, err := h.mcpRegistry.claimSignIn("theirs"); err != nil {
		t.Fatalf("claimSignIn = %v", err)
	}
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 21, consent))

	answer := awaitLaneAnswer(t, lane, 21)
	joinInflight(t, h)
	if answer.err != nil {
		t.Fatalf("answer error = %v, want success so KAS keeps its listener waiting", answer.err)
	}
	row := rowOf(t, h, "theirs")
	if row.State != mcpStateSigningIn || row.OAuthURL != consent || row.Relayed {
		t.Errorf("row = %+v, want signing_in anchored on the consent URL", row)
	}
	if row.Origin != marotte.OriginWorkspace || row.OriginRoot != "/w" {
		t.Errorf("row provenance = %q %q, want the workspace's kept", row.Origin, row.OriginRoot)
	}
	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, string(marotte.EventMCPSignInOpen)); len(missing) > 0 {
		t.Errorf("missing %v; got %v", missing, types)
	}
	for _, ty := range types {
		if ty == string(marotte.EventOpenExternalURL) {
			t.Errorf("the claimed URL was also raised as a chat banner")
		}
	}
	opened := eventsOfType(h, marotte.EventMCPSignInOpen)
	if len(opened) != 1 || !strings.Contains(string(opened[0].Payload), `"server":"theirs"`) {
		t.Errorf("mcp_sign_in_open = %+v, want one naming theirs", opened)
	}
}

func awaitLaneAnswer(t *testing.T, lane *fakeBridge, id int64) fakeResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if a, ok := lane.responseTo(id); ok {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %d went unanswered", id)
		}
		time.Sleep(time.Millisecond)
	}
}

// A sign-in whose reset is still unanswered when the server reports and a second Sign in takes the slot ends with that
// status: its late failure leaves the second sign-in's slot and row alone.
func TestSignIn_AnEarlierAttemptsLateFailureLeavesTheNextOneAlone(t *testing.T) {
	h, lane := signInHub(t, "linear")
	late := make(chan struct{})
	resets := 0
	lane.mu.Lock()
	lane.onCallFails = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
		if method != methodV3MCPResetServer {
			return nil, nil, false, nil
		}
		lane.mu.Lock()
		resets++
		first := resets == 1
		lane.mu.Unlock()
		if !first {
			return nil, nil, false, nil
		}
		<-late
		return nil, nil, false, errors.New("the first attempt's reset timed out")
	}
	lane.mu.Unlock()

	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("first sign-in = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 1)
	h.mcpRegistry.RecordOAuth(t.Context(), "linear", marotte.MCPSource{})
	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("second sign-in = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 2)
	close(late)
	joinInflight(t, h)

	if got := slotServer(h); got != "linear" {
		t.Errorf("slot = %q after the first attempt failed late, want the second attempt's kept", got)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.Error != "" {
		t.Errorf("row = %+v, want it waiting for the second sign-in, not failed", row)
	}
	if _, ok := h.mcpRegistry.claimSignInURL(t.Context(), "https://provider.example/authorize?state=s2"); !ok {
		t.Error("the second attempt's consent URL was not claimed for linear")
	}
}
