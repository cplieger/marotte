package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// sessionOf is the session signInHub reports name's frames in, one per server so each frame leaves the others' standing.
func sessionOf(name string) marotte.SessionID { return marotte.SessionID("sess-" + name) }

// awaitingSignIn is name waiting for sign-in as its session reports it.
func awaitingSignIn(name string, src marotte.MCPSource) translate.MCPServerReport {
	return translate.MCPServerReport{
		Name: name, Source: src, State: translate.MCPReportNeedsAuth,
		AuthorizationURL: "https://provider.example/passive",
	}
}

// signInHub is a runtime with a ready engine whose store holds names, each waiting for sign-in.
func signInHub(t *testing.T, names ...string) (*Runtime, *fakeBridge) {
	t.Helper()
	h := newHubWithMCPConfig(t, enabledConfig(names...))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	for _, n := range names {
		reportMCP(t, h, sessionOf(n), awaitingSignIn(n, marotte.MCPSource{}))
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

// standingSignIn is the servers whose sign-ins stand in any view, sorted and comma-joined, "" when none does.
func standingSignIn(h *Runtime) string {
	h.mcpRegistry.mu.RLock()
	defer h.mcpRegistry.mu.RUnlock()
	var out []string
	for _, v := range h.mcpRegistry.views {
		for _, s := range v.signIns {
			out = append(out, s.server)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
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
	if p[keyServerName] != "linear" || p[keyStartOAuth] != true || p[marotte.KeySessionID] != sessionOf("linear") {
		t.Errorf("reset params = %v, want serverName linear, startOAuth true and linear's session", p)
	}
	if got := standingSignIn(h); got != "linear" {
		t.Errorf("standing sign-in = %q after the reset answered, want it kept until the server reports", got)
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
		record func(t *testing.T, h *Runtime)
		want   string
	}{
		"connected": {
			record: func(t *testing.T, h *Runtime) {
				reportMCP(t, h, sessionOf("linear"), connectedMCP("linear", marotte.MCPSource{}))
			},
			want: errSignInConnected.Error(),
		},
		"failed for another reason": {
			record: func(t *testing.T, h *Runtime) {
				reportMCP(t, h, sessionOf("linear"), failedMCP("linear", marotte.MCPSource{}, "boom"))
			},
			want: errSignInNotNeeded.Error(),
		},
		"not reported at all": {
			record: func(*testing.T, *Runtime) {},
			want:   errSignInNotNeeded.Error(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("linear"))
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			tc.record(t, h)

			code, body := postSignIn(h, "linear")

			if code != http.StatusConflict || !strings.Contains(body, tc.want) {
				t.Errorf("sign-in = %d %s, want 409 %q", code, body, tc.want)
			}
			joinInflight(t, h)
			if n := countCalls(fakeSourceOf(t, h).lane, methodV3MCPResetServer); n != 0 {
				t.Errorf("resets sent = %d, want none", n)
			}
			if got := standingSignIn(h); got != "" {
				t.Errorf("standing sign-in = %q after a refusal, want none", got)
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
	h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://provider.example/authorize?state=s", Server: "linear"}, false)

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

// openExternalUrl names its server, so two servers sign in at once even in one session, and each consent URL lands on
// its own server's row.
func TestSignIn_TwoServersSignInAtOnce(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("linear", "github"))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	lane := fakeSourceOf(t, h).lane
	reportMCP(t, h, "sess-both", awaitingSignIn("linear", marotte.MCPSource{}), awaitingSignIn("github", marotte.MCPSource{}))

	for _, server := range []string{"linear", "github"} {
		if code, body := postSignIn(h, server); code != http.StatusAccepted {
			t.Fatalf("%s's sign-in = %d %s, want 202", server, code, body)
		}
	}
	awaitResets(t, lane, 2)
	joinInflight(t, h)

	if got := standingSignIn(h); got != "github,linear" {
		t.Errorf("standing sign-ins = %q, want both", got)
	}
	for _, server := range []string{"github", "linear"} {
		req := consentRequest{URL: "https://p.example/a?state=st-" + server, Server: server}
		if shown := h.mcpRegistry.anchorConsentURL(t.Context(), req, false); shown.how != consentOnRow || shown.server != server {
			t.Errorf("%s's consent URL = %+v, want on its own row", server, shown)
		}
	}
	if code, body := postSignIn(h, "linear"); code != http.StatusConflict || !strings.Contains(body, errSignInInProgress.Error()) {
		t.Errorf("a second sign-in for linear = %d %s, want 409 in progress", code, body)
	}
}

// A consent URL KAS names another server for is never taken by the standing Sign in: it is a banner naming that
// server, and the standing row keeps waiting for its own.
func TestSignIn_ConsentURLForAnotherServerIsNotTakenByTheStandingSignIn(t *testing.T) {
	h, lane := signInHub(t, "linear")
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	req := consentRequest{URL: "https://p.example/a?state=s", Server: "github", Completion: "notification", FlowID: "f1"}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), req, true); shown.how != consentBanner {
		t.Errorf("github's consent URL = %+v, want a banner", shown)
	}
	if row := rowOf(t, h, "linear"); row.OAuthURL != "" || standingSignIn(h) != "linear" {
		t.Errorf("linear's row = %+v, want its Sign in standing with no consent URL", row)
	}
}

// fakeClock is a settable clock for the standing sign-in's deadline.
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

// A standing sign-in whose server never reported is taken over once KAS's own connect ceiling has passed.
func TestSignIn_TheStandingSignInExpiresAtItsDeadline(t *testing.T) {
	h, lane := signInHub(t, "linear", "github")
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("first sign-in = %d, want 202", code)
	}

	clock.advance(signInTTL - time.Second)
	if code, _ := postSignIn(h, "linear"); code != http.StatusConflict {
		t.Fatalf("sign-in a second before the deadline = %d, want 409", code)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://p.example/a?state=s", Server: "linear"}, false); shown.how != consentOnRow || shown.server != "linear" {
		t.Errorf("a consent URL before the deadline = %+v, want on linear's row", shown)
	}
	reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("re-sign-in = %d, want 202", code)
	}
	clock.advance(signInTTL)
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://p.example/a?state=s", Server: "linear"}, false); shown.how != consentRefused {
		t.Errorf("a consent URL after the deadline = %+v, want refused, not taken by an expired sign-in", shown)
	}

	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Errorf("sign-in after the deadline = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 3)
	if got := standingSignIn(h); got != "linear" {
		t.Errorf("standing sign-in = %q, want linear's new one", got)
	}
}

// A reset the engine refuses ends the sign-in at once; the server still waits, so the row says why and offers Sign in
// again.
func TestSignIn_EngineErrorEndsTheStandingSignIn(t *testing.T) {
	h, lane := signInHub(t, "linear")
	lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "MCP governance is disabled by your organization"})

	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	if got := standingSignIn(h); got != "" {
		t.Errorf("standing sign-in = %q after a refused reset, want none", got)
	}
	row := rowOf(t, h, "linear")
	if row.State != mcpStateOAuth || !strings.Contains(row.Error, "MCP governance is disabled") {
		t.Errorf("row = %+v, want needs_auth with the engine's reason", row)
	}
	needed := eventsOfType(h, marotte.EventMCPOAuthNeeded)
	if len(needed) == 0 || !strings.Contains(string(needed[len(needed)-1].Payload), "MCP governance is disabled") {
		t.Errorf("mcp_oauth_needed = %+v, want the last one carrying the engine's reason", needed)
	}
	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Errorf("retry = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 2)
	joinInflight(t, h)
}

// A refused Sign in in a waiting session leaves the row on that session even while another has the server
// connected: the row keeps the reason and a retry goes back to the waiting session.
func TestSignIn_ARefusedSignInLeavesTheWaitingSessionRetryable(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("linear"))
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	lane := fakeSourceOf(t, h).lane
	reportMCP(t, h, "sess-a", connectedMCP("linear", marotte.MCPSource{}))
	reportMCP(t, h, "sess-b", awaitingSignIn("linear", marotte.MCPSource{}))
	lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "listener busy"})
	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || !strings.Contains(row.Error, "listener busy") {
		t.Errorf("row = %+v, want needs_auth with the engine's reason", row)
	}
	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("retry = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 2)
	joinInflight(t, h)
	if p := lane.paramsFor(methodV3MCPResetServer); p[marotte.KeySessionID] != marotte.SessionID("sess-b") || p[keyStartOAuth] != true {
		t.Errorf("retry params = %v, want startOAuth in sess-b", p)
	}
}

// A refusal's reason stands until the waiting session reports something else or a new Sign in starts.
func TestSignIn_ARefusalsReasonEndsWithTheNextStatusOrClaim(t *testing.T) {
	refuse := func(t *testing.T) (*Runtime, *fakeBridge) {
		t.Helper()
		h, lane := signInHub(t, "linear")
		lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "refused"})
		if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
			t.Fatalf("sign-in = %d, want 202", code)
		}
		awaitResets(t, lane, 1)
		joinInflight(t, h)
		if row := rowOf(t, h, "linear"); row.Error == "" {
			t.Fatalf("row = %+v, want the refusal's reason", row)
		}
		return h, lane
	}
	t.Run("the same wait reported again keeps it", func(t *testing.T) {
		h, _ := refuse(t)
		reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
		if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.Error == "" {
			t.Errorf("row = %+v, want needs_auth still with the reason", row)
		}
	})
	t.Run("a connect clears it", func(t *testing.T) {
		h, _ := refuse(t)
		reportMCP(t, h, sessionOf("linear"), connectedMCP("linear", marotte.MCPSource{}))
		reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
		if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.Error != "" {
			t.Errorf("row = %+v, want needs_auth with no reason", row)
		}
	})
	t.Run("a new sign-in clears it", func(t *testing.T) {
		h, _ := refuse(t)
		if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
			t.Fatalf("claimSignIn = %v", err)
		}
		if row := rowOf(t, h, "linear"); row.Error != "" {
			t.Errorf("row = %+v after a new claim, want no reason", row)
		}
		h.mcpRegistry.dropSession(t.Context(), sessionOf("linear"))
	})
}

// A foreign row keeps its provenance through a refused sign-in.
func TestSignIn_EngineErrorKeepsAForeignRowsOrigin(t *testing.T) {
	h, lane := signInHub(t, "mine")
	reportMCP(t, h, sessionOf("theirs"), awaitingSignIn("theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"}))
	lane.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "no such server"})

	if code, _ := postSignIn(h, "theirs"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)

	if row := rowOf(t, h, "theirs"); row.State != mcpStateOAuth || row.Error == "" || row.Origin != marotte.OriginWorkspace || row.OriginRoot != "/w" {
		t.Errorf("row = %+v, want needs_auth with the reason and still the workspace's", row)
	}
}

func TestSignIn_WithNoEngineIsUnavailable(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.startGate = make(chan struct{})
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, WithMCPConfig(enabledConfig("linear")))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))

	if code, _ := postSignIn(h, "linear"); code != http.StatusServiceUnavailable {
		t.Errorf("sign-in = %d, want 503", code)
	}
	if got := standingSignIn(h); got != "" {
		t.Errorf("standing sign-in = %q with no engine, want none", got)
	}
}

// A sign-in ends with the next status its session reports for its server, whatever it is but connecting, and with
// its session or the engine.
func TestSignIn_TerminalStatusEndsTheStandingSignIn(t *testing.T) {
	cases := map[string]func(t *testing.T, h *Runtime, name string){
		"connected": func(t *testing.T, h *Runtime, name string) {
			reportMCP(t, h, sessionOf(name), connectedMCP(name, marotte.MCPSource{}))
		},
		"waiting for sign-in again": func(t *testing.T, h *Runtime, name string) {
			reportMCP(t, h, sessionOf(name), awaitingSignIn(name, marotte.MCPSource{}))
		},
		"failed": func(t *testing.T, h *Runtime, name string) {
			reportMCP(t, h, sessionOf(name), failedMCP(name, marotte.MCPSource{}, "timed out"))
		},
		"disabled, marotte's own": func(t *testing.T, h *Runtime, name string) {
			reportMCP(t, h, sessionOf(name), disabledMCP(name, marotte.MCPSource{}))
		},
		"disabled, a workspace's": func(t *testing.T, h *Runtime, name string) {
			reportMCP(t, h, sessionOf(name), disabledMCP(name, marotte.MCPSource{Origin: "workspace", Root: "/w"}))
		},
		"left out of its session's frame": func(t *testing.T, h *Runtime, name string) {
			reportMCP(t, h, sessionOf(name))
		},
		"its session ended": func(t *testing.T, h *Runtime, name string) {
			h.mcpRegistry.dropSession(t.Context(), sessionOf(name))
		},
		"the engine ended": func(t *testing.T, h *Runtime, _ string) {
			h.mcpRegistry.clearAll(t.Context())
		},
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := signInHub(t, "linear", "github")
			if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
				t.Fatalf("claimSignIn = %v", err)
			}
			if name != "the engine ended" {
				report(t, h, "github")
				if got := standingSignIn(h); got != "linear" {
					t.Fatalf("another session's status ended the sign-in (standing sign-in = %q)", got)
				}
			}

			report(t, h, "linear")

			if got := standingSignIn(h); got != "" {
				t.Errorf("standing sign-in = %q after the server reported, want none", got)
			}
		})
	}
}

// A connecting report is the sign-in's own reconnect, so the sign-in and its record stand through it.
func TestSignIn_AConnectingReportKeepsTheSignIn(t *testing.T) {
	h, _ := signInHub(t, "linear")
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
		t.Fatalf("claimSignIn = %v", err)
	}
	const consent = "https://provider.example/authorize?state=s"
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: consent, Server: "linear"}, false); shown.how != consentOnRow {
		t.Fatalf("anchorConsentURL = %+v, want on the standing sign-in's row", shown)
	}

	reportMCP(t, h, sessionOf("linear"), translate.MCPServerReport{Name: "linear", State: translate.MCPReportConnecting})

	if got := standingSignIn(h); got != "linear" {
		t.Errorf("standing sign-in = %q after a connecting report, want linear's", got)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateSigningIn || row.OAuthURL != consent {
		t.Errorf("row = %+v, want signing_in on the consent URL", row)
	}
}

// A sign-in is filed under the session that binds its server, so a later session for the same server neither
// carries nor ends it, and the sign-in leaves no engine-wide trace once its session ends.
func TestSignIn_LivesOnTheBindingSessionsView(t *testing.T) {
	h, lane := signInHub(t, "linear")
	reportMCP(t, h, "sess-later", awaitingSignIn("linear", marotte.MCPSource{}))
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	joinInflight(t, h)
	if p := lane.paramsFor(methodV3MCPResetServer); p[marotte.KeySessionID] != sessionOf("linear") {
		t.Errorf("reset session = %v, want the oldest binding session %s", p[marotte.KeySessionID], sessionOf("linear"))
	}

	reportMCP(t, h, "sess-later", connectedMCP("linear", marotte.MCPSource{}))
	if got := standingSignIn(h); got != "linear" {
		t.Fatalf("a later session's frame ended the sign-in (standing sign-in = %q)", got)
	}

	h.mcpRegistry.dropSession(t.Context(), sessionOf("linear"))
	if got := standingSignIn(h); got != "" {
		t.Errorf("standing sign-in = %q after its session ended, want none", got)
	}
	if code, body := postSignIn(h, "linear"); code != http.StatusConflict || !strings.Contains(body, errSignInConnected.Error()) {
		t.Errorf("sign-in after the later session connected = %d %s, want 409 connected", code, body)
	}
}

// KAS keeps one instance per declaration: a session holding another declaration of the name stays waiting while
// the other is connected, and only a sign-in in that session connects it. The row offers that sign-in whichever
// session is older, and the reset goes to the waiting session.
func TestSignIn_GoesToTheSessionWaitingWhileAnotherIsConnected(t *testing.T) {
	cases := map[string]struct{ older, newer translate.MCPServerReport }{
		"the older session waits": {
			older: awaitingSignIn("linear", marotte.MCPSource{}), newer: connectedMCP("linear", marotte.MCPSource{}),
		},
		"the newer session waits": {
			older: connectedMCP("linear", marotte.MCPSource{}), newer: awaitingSignIn("linear", marotte.MCPSource{}),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("linear"))
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			lane := fakeSourceOf(t, h).lane
			reportMCP(t, h, "sess-older", tc.older)
			reportMCP(t, h, "sess-newer", tc.newer)
			waiting := marotte.SessionID("sess-older")
			if tc.newer.State == translate.MCPReportNeedsAuth {
				waiting = "sess-newer"
			}
			if got := rowOf(t, h, "linear").State; got != mcpStateOAuth {
				t.Errorf("row state = %q, want %q so the row offers Sign in", got, mcpStateOAuth)
			}

			if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
				t.Fatalf("sign-in = %d %s, want 202", code, body)
			}
			awaitResets(t, lane, 1)
			joinInflight(t, h)

			if p := lane.paramsFor(methodV3MCPResetServer); p[marotte.KeySessionID] != waiting || p[keyStartOAuth] != true {
				t.Errorf("reset params = %v, want startOAuth in the waiting session %s", p, waiting)
			}
		})
	}
}

// A sign-in whose session never reported again leaves signing_in once its deadline passes: the row offers Sign in
// again and a paste for the dead consent URL is not relayed.
func TestSignIn_AnExpiredSignInReturnsItsServerToNeedsAuth(t *testing.T) {
	h, lane := signInHub(t, "linear")
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	awaitResets(t, lane, 1)
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://provider.example/authorize?state=s", Server: "linear"}, false); shown.how != consentOnRow {
		t.Fatalf("anchorConsentURL = %+v, want on the standing sign-in's row", shown)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateSigningIn {
		t.Fatalf("row = %+v, want signing_in before the deadline", row)
	}

	clock.advance(signInTTL)

	if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), "s"); !errors.Is(err, errRelayNoFlow) {
		t.Errorf("relay after the deadline = %v, want errRelayNoFlow", err)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.OAuthURL != "" {
		t.Errorf("row = %+v, want needs_auth with no consent URL", row)
	}
	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Errorf("sign-in after the deadline = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 2)
	joinInflight(t, h)
}

// The deadline ends a sign-in by itself, with no frame and no request, and the clients hear the server needs a
// sign-in again. The bubble's clock moves only once every goroutine waits, so the URL is claimed before the deadline
// however slow the runner is.
func TestSignIn_TheDeadlineEndsTheSignInWithNoFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, lane := signInHub(t, "linear")
		if code, _ := postSignIn(h, "linear"); code != http.StatusAccepted {
			t.Fatalf("sign-in = %d, want 202", code)
		}
		awaitResets(t, lane, 1)
		if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://provider.example/authorize?state=s", Server: "linear"}, false); shown.how != consentOnRow {
			t.Fatalf("anchorConsentURL = %+v, want on the standing sign-in's row", shown)
		}
		before := h.bus.fanout.Position().Head

		time.Sleep(signInTTL)
		synctest.Wait()

		if !slices.Contains(extractTypes(t, bufferedSince(h, before)), string(marotte.EventMCPOAuthNeeded)) {
			t.Errorf("no mcp_oauth_needed past the deadline; row = %+v", rowOf(t, h, "linear"))
		}
		if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.OAuthURL != "" {
			t.Errorf("row = %+v past the deadline, want needs_auth with no consent URL", row)
		}
		if got := standingSignIn(h); got != "" {
			t.Errorf("standing sign-in = %q past the deadline, want none", got)
		}
		joinInflight(t, h)
	})
}

// The consent URL KAS opens while a sign-in stands belongs to that server's row, and no chat banner is raised.
func TestOpenExternalURL_ClaimedBySignInRecordsTheAnchor(t *testing.T) {
	const consent = "https://provider.example/authorize?state=s1&redirect_uri=http%3A%2F%2F127.0.0.1%3A4000%2Foauth%2Fcallback"
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	reportMCP(t, h, sessionOf("theirs"), awaitingSignIn("theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"}))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "theirs"); err != nil {
		t.Fatalf("claimSignIn = %v", err)
	}
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 21, "theirs", consent))

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

// A status ending the Sign in ends its row's signing_in, not its consent URL: KAS's listener may still be waiting, so
// the URL stays relayable, single use, until the Sign in's own deadline.
func TestSignIn_AnAnchorOutlivesTheSignInThatTookIt(t *testing.T) {
	lA := newCallbackListener(t, http.StatusOK)
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
		t.Fatalf("claimSignIn(linear) = %v", err)
	}
	if err := openURL(t, h, lane, 1, authURLFor(t, lA.srv.URL, "st-a")); err != nil {
		t.Fatalf("A's answer = %v, want success", err)
	}

	reportMCP(t, h, sessionOf("linear"), failedMCP("linear", marotte.MCPSource{}, "timed out"))

	if got := standingSignIn(h); got != "" {
		t.Fatalf("standing sign-in = %q after the failed status, want none", got)
	}
	pasted := pastedFor(t, lA.srv.URL, "ca", "st-a")
	if rec := postRelay(t, h, "linear", pasted); rec.Code != http.StatusOK || lA.gotCode != "ca" {
		t.Errorf("A's paste = %d %s, listener saw %q; want 200 delivering ca", rec.Code, rec.Body.String(), lA.gotCode)
	}
	if rec := postRelay(t, h, "linear", pasted); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errRelayAlreadyDone.Error()) {
		t.Errorf("the second paste = %d %s, want 409 already delivered", rec.Code, rec.Body.String())
	}
	clock.advance(signInTTL)
	if rec := postRelay(t, h, "linear", pastedFor(t, lA.srv.URL, "fresh", "st-a")); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), errRelayNoFlow.Error()) {
		t.Errorf("a paste past A's deadline = %d %s, want 409 press Sign in first", rec.Code, rec.Body.String())
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

// A sign-in whose reset is still unanswered when the server reports and a second Sign in then stands ends with that
// status: its late failure leaves the second sign-in and its row alone.
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
	reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
	if code, body := postSignIn(h, "linear"); code != http.StatusAccepted {
		t.Fatalf("second sign-in = %d %s, want 202", code, body)
	}
	awaitResets(t, lane, 2)
	close(late)
	joinInflight(t, h)

	if got := standingSignIn(h); got != "linear" {
		t.Errorf("standing sign-in = %q after the first attempt failed late, want the second attempt's kept", got)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.Error != "" {
		t.Errorf("row = %+v, want it waiting for the second sign-in, not failed", row)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://provider.example/authorize?state=s2", Server: "linear"}, false); shown.how != consentOnRow || shown.server != "linear" {
		t.Errorf("the second attempt's consent URL = %+v, want on linear's row", shown)
	}
}
