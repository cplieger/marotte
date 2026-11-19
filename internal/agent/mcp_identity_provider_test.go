package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/translate"
)

// signedIn is KAS's organization status for Acme, signed in as account.
func signedIn(account string) *marotte.MCPIdentityProviderStatus {
	return &marotte.MCPIdentityProviderStatus{
		State: marotte.MCPIdentityProviderSignedIn, Issuer: "https://login.acme.example", DisplayName: "Acme", Account: account,
	}
}

// servedIDP is the organization status the registry serves now.
func servedIDP(h *Runtime) marotte.MCPIdentityProviderPayload {
	h.mcpRegistry.mu.RLock()
	defer h.mcpRegistry.mu.RUnlock()
	return h.mcpRegistry.idp.served
}

// idpFramesSince decodes the mcp_identity_provider events since before.
func idpFramesSince(t *testing.T, h *Runtime, before uint64) []marotte.MCPIdentityProviderPayload {
	t.Helper()
	var out []marotte.MCPIdentityProviderPayload
	for _, e := range eventsSince(h, before, marotte.EventMCPIdentityProvider) {
		var evt struct {
			Payload marotte.MCPIdentityProviderPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &evt); err != nil {
			t.Fatalf("decoding the event: %v", err)
		}
		out = append(out, evt.Payload)
	}
	return out
}

// organizationStatusFrame is a _kiro/mcp/status notification for session carrying block as its identityProvider.
func organizationStatusFrame(t *testing.T, session string, block map[string]any) *marotte.RPCResponse {
	t.Helper()
	params := map[string]any{"sessionId": session, "servers": []map[string]any{}}
	if block != nil {
		params["identityProvider"] = block
	}
	return &marotte.RPCResponse{Method: methodV3MCPStatus, Params: mustJSON(t, params)}
}

// KAS keeps one provider connection per engine and re-sends it to every session: the newest frame is the engine's,
// whichever session sent it, and the engine's end makes it unknown.
func TestMCPRegistry_IdentityProviderFollowsTheNewestFrame(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := h.bus.fanout.Position().Head

	h.mcpRegistry.RecordSessionStatus(t.Context(), "sess_a", translate.MCPSessionStatus{IdentityProvider: signedIn("ada@acme.example")})
	h.mcpRegistry.RecordSessionStatus(t.Context(), "sess_b", translate.MCPSessionStatus{IdentityProvider: &marotte.MCPIdentityProviderStatus{
		State: marotte.MCPIdentityProviderSessionExpired, Issuer: "https://login.acme.example", DisplayName: "Acme",
	}})

	got := servedIDP(h)
	if !got.Known || got.Status == nil || got.Status.State != marotte.MCPIdentityProviderSessionExpired {
		t.Errorf("served = %+v, want sess_b's newer session-expired", got)
	}
	frames := idpFramesSince(t, h, before)
	if len(frames) != 2 || frames[1].Status.State != marotte.MCPIdentityProviderSessionExpired {
		t.Fatalf("mcp_identity_provider frames = %+v, want one per change, the last session-expired", frames)
	}
	h.mcpRegistry.RecordSessionStatus(t.Context(), "sess_a", translate.MCPSessionStatus{})
	if got := servedIDP(h); !got.Known || got.Status != nil {
		t.Errorf("served after a frame with no provider = %+v, want known with none", got)
	}

	h.mcpRegistry.clearAll()

	if got := servedIDP(h); got.Known || got.Status != nil {
		t.Errorf("served after the engine ended = %+v, want unknown", got)
	}
	if frames := idpFramesSince(t, h, before); len(frames) != 4 || frames[3].Known {
		t.Errorf("mcp_identity_provider frames = %+v, want the engine's end announced as unknown", frames)
	}
}

// An observer pass's session binds servers no chat uses: its status feeds only the organization sign-in.
func TestObserverForward_RecordsTheIdentityProvider(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	held := &heldStreamSession{fakeBridge: newFakeBridge(), stream: make(chan marotte.Notification, 1)}
	held.noGovernance, held.noDoorSeeds = true, true
	fakeSourceOf(t, h).setObserver(func() engineSession { return held })
	if err := h.observe(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
		t.Fatalf("observer pass: %v", err)
	}
	held.stream <- marotte.Notification{Msg: &marotte.RPCResponse{Method: methodV3MCPStatus, Params: mustJSON(t, map[string]any{
		"sessionId": "observer-1",
		"servers":   []map[string]any{{"name": "github", "status": "connected"}},
		"identityProvider": map[string]any{
			"state": "signed-out", "issuer": "https://login.acme.example", "displayName": "Acme",
		},
	})}}
	close(held.stream)
	joinInflight(h)

	if got := servedIDP(h); !got.Known || got.Status == nil || got.Status.State != marotte.MCPIdentityProviderSignedOut {
		t.Errorf("served = %+v, want the pass's signed-out", got)
	}
	h.mcpRegistry.mu.RLock()
	views := len(h.mcpRegistry.views)
	h.mcpRegistry.mu.RUnlock()
	if views != 0 {
		t.Errorf("views = %d, want none for an observer pass", views)
	}
}

// postIDP presses one of the card's buttons.
func postIDP(handler http.HandlerFunc) int {
	return postJSON(handler, "/api/mcp/identity-provider", `{}`).Code
}

// The Sign in is engine-wide and blocks for KAS's whole flow, so it runs in the background under KAS's own bound.
func TestMCPIdentityProviderSignIn_CallsTheEngineAndAnswers202(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	joinInflight(h)
	lane := fakeSourceOf(t, h).lane
	gate := make(chan struct{})
	lane.setBlockOn(methodKiroMCPIdPSignIn, gate)
	started := time.Now()

	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignIn); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202 while the call is still open", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for countCalls(lane, methodKiroMCPIdPSignIn) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the engine never got signIn")
		}
		time.Sleep(time.Millisecond)
	}
	if p := lane.paramsFor(methodKiroMCPIdPSignIn); len(p) != 0 {
		t.Errorf("signIn params = %v, want {} with no sessionId", p)
	}
	if d := lane.deadlineOf(methodKiroMCPIdPSignIn); d.Sub(started) < 330*time.Second {
		t.Errorf("signIn deadline = %v after the press, want at least KAS's 300 s callback wait plus discovery and exchange", d.Sub(started))
	}
	if got := servedIDP(h); !got.SigningIn {
		t.Errorf("served while signing in = %+v, want signing_in", got)
	}
	close(gate)
	joinInflight(h)
	if got := servedIDP(h); got.SigningIn || got.SignInError != "" {
		t.Errorf("served once signIn answered = %+v, want the sign-in ended with no error", got)
	}
}

func TestMCPIdentityProviderSignIn_RefusesASecondWhileOneStands(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	joinInflight(h)
	lane := fakeSourceOf(t, h).lane
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	lane.setBlockOn(methodKiroMCPIdPSignIn, gate)

	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignIn); code != http.StatusAccepted {
		t.Fatalf("first sign-in = %d, want 202", code)
	}
	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignIn); code != http.StatusConflict {
		t.Errorf("second sign-in = %d, want 409 while the first stands", code)
	}
}

// KAS's refusal ends the Sign in and stays on the card in KAS's words until the next press.
func TestMCPIdentityProviderSignIn_ARefusalKeepsKiroCLIsReason(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	joinInflight(h)
	lane := fakeSourceOf(t, h).lane
	lane.setCallRPCErr(methodKiroMCPIdPSignIn, &marotte.RPCError{Code: -32603, Message: "No identity provider is configured."})

	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignIn); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}
	joinInflight(h)
	got := servedIDP(h)
	if got.SigningIn || got.SignInError == "" {
		t.Errorf("served = %+v, want the sign-in ended with kiro-cli's reason", got)
	}
	lane.setCallRPCErr(methodKiroMCPIdPSignIn, nil)
}

func TestMCPIdentityProviderSignIn_WithNoEngineIsUnavailable(t *testing.T) {
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	src.startGate = make(chan struct{})
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })

	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignIn); code != http.StatusServiceUnavailable {
		t.Errorf("sign-in = %d, want 503", code)
	}
	if got := servedIDP(h); got.SigningIn {
		t.Errorf("served = %+v, want no sign-in standing", got)
	}
	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignOut); code != http.StatusServiceUnavailable {
		t.Errorf("sign-out = %d, want 503", code)
	}
}

// Sign out is also the card's Cancel: KAS discards a sign-in in progress, so the standing one ends with it.
func TestMCPIdentityProviderSignOut(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	joinInflight(h)
	lane := fakeSourceOf(t, h).lane
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	lane.setBlockOn(methodKiroMCPIdPSignIn, gate)
	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignIn); code != http.StatusAccepted {
		t.Fatalf("sign-in = %d, want 202", code)
	}

	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignOut); code != http.StatusNoContent {
		t.Fatalf("sign-out = %d, want 204", code)
	}
	if n := countCalls(lane, methodKiroMCPIdPSignOut); n != 1 {
		t.Errorf("signOut calls = %d, want 1", n)
	}
	if p := lane.paramsFor(methodKiroMCPIdPSignOut); len(p) != 0 {
		t.Errorf("signOut params = %v, want {} with no sessionId", p)
	}
	if got := servedIDP(h); got.SigningIn {
		t.Errorf("served after sign-out = %+v, want the standing sign-in ended", got)
	}

	lane.setCallRPCErr(methodKiroMCPIdPSignOut, &marotte.RPCError{Code: -32603, Message: "boom"})
	if code := postIDP(h.mcpRegistry.handleIdentityProviderSignOut); code != http.StatusBadGateway {
		t.Errorf("refused sign-out = %d, want 502", code)
	}
}

// identityProviderPage is KAS's openExternalUrl for the organization's page, the flag its two callers set.
func identityProviderPage(t *testing.T, id int64, url string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{Method: methodKiroOpenExternalURL, ID: &id, Params: mustJSON(t, map[string]any{
		"url": url, "identityProviderSignIn": true,
	})}
}

// The page KAS opens for the user's own Sign in is the card's, not a banner, and lives as long as KAS's wait.
func TestEngineLane_IdentityProviderURLGoesToTheStandingSignIn(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	joinInflight(h)
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	fakeSourceOf(t, h).lane.setBlockOn(methodKiroMCPIdPSignIn, gate)
	if err := h.mcpRegistry.startOrgSignIn(); err != nil {
		t.Fatalf("startOrgSignIn = %v", err)
	}
	connectPage(t, h)
	before := h.bus.fanout.Position().Head
	page := authURLFor(t, l.srv.URL, "st-org")

	h.engineFrame(fakeSourceOf(t, h).current(), identityProviderPage(t, 61, page))
	if a := awaitLaneAnswer(t, lane, 61); a.err != nil {
		t.Fatalf("answer = %v, want success so KAS keeps waiting", a.err)
	}
	// Not joinInflight: the signIn call stays open on inflight until the gate closes.
	for deadline := time.Now().Add(5 * time.Second); servedIDP(h).OAuthURL != page; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("served = %+v, want signing in on the page", servedIDP(h))
		}
	}

	if n := len(eventsSince(h, before, marotte.EventOpenExternalURL)); n != 0 {
		t.Errorf("open_external_url events = %d, want none: the card shows it", n)
	}
	if got := servedIDP(h); !got.SigningIn {
		t.Errorf("served = %+v, want signing in", got)
	}
	h.mcpRegistry.mu.RLock()
	var expires time.Time
	for _, a := range h.mcpRegistry.anchors {
		if a.state == "st-org" {
			expires = a.expires
		}
	}
	h.mcpRegistry.mu.RUnlock()
	if want := clock.now().Add(300 * time.Second); !expires.Equal(want) {
		t.Errorf("anchor expires %v, want %v (KAS's 300 s callback wait)", expires, want)
	}
	if rec := postRelay(t, h, "", pastedFor(t, l.srv.URL, "c", "st-org")); rec.Code != http.StatusOK {
		t.Fatalf("paste = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := servedIDP(h); !got.Relayed {
		t.Errorf("served after the paste = %+v, want relayed", got)
	}
	clock.advance(301 * time.Second)
	if got := h.mcpRegistry.identityProviderLocked(); got.OAuthURL != "" {
		t.Errorf("projection past KAS's wait = %+v, want the page gone", got)
	}
}

// KAS's own automatic sign-in for a refused tool call has no Sign in to ride: every page shows it, named for the
// organization.
func TestEngineLane_IdentityProviderURLWithNoSignInIsAnOrganizationBanner(t *testing.T) {
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	h.mcpRegistry.RecordSessionStatus(t.Context(), "sess_a", translate.MCPSessionStatus{IdentityProvider: signedIn("")})
	connectPage(t, h)
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), identityProviderPage(t, 62, "https://login.acme.example/authorize?state=st-auto"))
	if a := awaitLaneAnswer(t, lane, 62); a.err != nil {
		t.Fatalf("answer = %v, want success", a.err)
	}
	joinInflight(h)

	p := bannerOf(t, h, before)
	if !p.IdentityProvider || p.IdentityProviderName != "Acme" || p.Server != "" {
		t.Errorf("banner = %+v, want the organization's, named Acme", p)
	}
	if want := clock.now().Add(300 * time.Second); !p.ExpiresAt.Equal(want) {
		t.Errorf("banner expires_at = %v, want %v (KAS's 300 s callback wait)", p.ExpiresAt, want)
	}
}

// A cold GET with a provider configured reads the status through one observer pass, and only once per provider.
func TestMCPIdentityProviderStatus_ColdWarmsOnce(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	joinInflight(h)
	passes := 0
	fakeSourceOf(t, h).setObserver(func() engineSession {
		passes++
		held := &heldStreamSession{fakeBridge: newFakeBridge(), stream: make(chan marotte.Notification, 1)}
		held.noGovernance, held.noDoorSeeds = true, true
		held.stream <- marotte.Notification{Msg: organizationStatusFrame(t, "observer", map[string]any{
			"state": "signed-out", "issuer": "https://login.acme.example", "displayName": "Acme",
		})}
		close(held.stream)
		return held
	})
	cfg := &mcp.IdentityProvider{Issuer: "https://login.acme.example", ClientID: "kiro-client"}

	got, stamp := h.MCPIdentityProviderStatus(t.Context(), cfg)
	if !got.Known || got.Status == nil || got.Status.State != marotte.MCPIdentityProviderSignedOut {
		t.Errorf("cold GET = %+v, want the pass's signed-out", got)
	}
	if stamp == nil || stamp.Kind != "mcp_identity_provider" || stamp.Version == "0" {
		t.Errorf("stamp = %+v, want the mcp_identity_provider version the status was committed at", stamp)
	}
	h.MCPIdentityProviderStatus(t.Context(), cfg)
	if passes != 1 {
		t.Errorf("observer passes = %d, want 1: the status already shows the provider", passes)
	}
	if _, _ = h.MCPIdentityProviderStatus(t.Context(), nil); passes != 1 {
		t.Errorf("observer passes = %d, want none for no provider", passes)
	}
	// A provider the status never shows (kiro-cli still reports the old one) is warmed once, not at every GET.
	other := &mcp.IdentityProvider{Issuer: "https://other.example", ClientID: "o"}
	h.MCPIdentityProviderStatus(t.Context(), other)
	h.MCPIdentityProviderStatus(t.Context(), other)
	if passes != 2 {
		t.Errorf("observer passes = %d, want one more for the new provider", passes)
	}
}

// A server signing in through the organization keeps its reason on its row.
func TestMCPRegistry_RowCarriesTheEnterpriseReason(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("docs"))
	reportMCP(t, h, "sess_a", translate.MCPServerReport{
		Name: "docs", State: translate.MCPReportFailed, Error: "Sign in to your organization to use this server.",
		Enterprise: &marotte.MCPEnterpriseAuth{Reason: "idp-sign-in-required"},
	})
	if row := rowOf(t, h, "docs"); row.Enterprise == nil || row.Enterprise.Reason != "idp-sign-in-required" {
		t.Errorf("row = %+v, want the enterprise reason", row)
	}
	before := h.bus.fanout.Position().Head
	reportMCP(t, h, "sess_a", translate.MCPServerReport{
		Name: "docs", State: translate.MCPReportFailed, Error: "Sign in to your organization to use this server.",
		Enterprise: &marotte.MCPEnterpriseAuth{Reason: "idp-session-expired"},
	})
	if n := len(eventsSince(h, before, marotte.EventMCPStatus)); n != 1 {
		t.Errorf("mcp_status frames = %d, want one for a changed reason", n)
	}
}
