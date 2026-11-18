package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// orgConsent is the page KAS opens for the organization sign-in: no serverName, its callback on one of KAS's fixed
// sign-in ports.
func orgConsent(state string) consentRequest {
	return consentRequest{
		URL:                    "https://idp.example.com/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A3128%2Foauth%2Fcallback&state=" + state,
		IdentityProviderSignIn: true,
	}
}

// orgHub is a runtime with a ready engine whose signIn call is held open until the returned release runs.
func orgHub(t *testing.T) (h *Runtime, lane *fakeBridge, release func()) {
	t.Helper()
	h = newHubWithMCPConfig(t, enabledConfig())
	lane = fakeSourceOf(t, h).lane
	gate := make(chan struct{})
	lane.setBlockOn(methodV3MCPIdentityProviderSignIn, gate)
	closed := false
	release = func() {
		if !closed {
			closed = true
			close(gate)
		}
	}
	t.Cleanup(release)
	return h, lane, release
}

func postOrgSignIn(t *testing.T, h *Runtime) {
	t.Helper()
	if rec := postJSON(h.mcpRegistry.handleOrgSignIn, "/api/mcp/identity-provider/sign-in", `{}`); rec.Code != http.StatusAccepted {
		t.Fatalf("organization sign-in = %d %s, want 202", rec.Code, rec.Body.String())
	}
	awaitCalls(t, fakeSourceOf(t, h).lane, methodV3MCPIdentityProviderSignIn, 1, "the sign-in was queued")
}

// servedOrg is GET /api/mcp/status's identity_provider, the zero value when the reply omits it.
func servedOrg(t *testing.T, h *Runtime) marotte.MCPIdentityProviderStatus {
	t.Helper()
	if st := servedMCPBody(t, h).IdentityProvider; st != nil {
		return *st
	}
	return marotte.MCPIdentityProviderStatus{}
}

// orgFrames is every mcp_identity_provider frame after sinceID.
func orgFrames(t *testing.T, h *Runtime, sinceID uint64) []marotte.MCPIdentityProviderStatus {
	t.Helper()
	var out []marotte.MCPIdentityProviderStatus
	for _, e := range eventsSince(h, sinceID, marotte.EventMCPIdentityProvider) {
		var f struct {
			Payload marotte.MCPIdentityProviderPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &f); err != nil {
			t.Fatalf("unmarshal mcp_identity_provider: %v", err)
		}
		out = append(out, f.Payload.Status)
	}
	return out
}

func reportOrg(t *testing.T, h *Runtime, sid marotte.SessionID, st *marotte.MCPIdentityProviderState) {
	t.Helper()
	h.mcpRegistry.RecordSessionStatus(t.Context(), sid, translate.MCPSessionStatus{IdentityProvider: st})
}

// The page KAS opens for the user's own organization sign-in belongs to the card, with the deadline KAS waits to,
// and raises no banner.
func TestAnchorConsentURL_IdentityProviderOnCard(t *testing.T) {
	h, _, _ := orgHub(t)
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	postOrgSignIn(t, h)
	before := h.bus.fanout.Position().Head

	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true); shown.how != consentOnCard {
		t.Fatalf("anchorConsentURL = %+v, want on the card", shown)
	}

	want := clock.now().Add(orgSignInTTL)
	got := servedOrg(t, h)
	if got.SignInURL != orgConsent("o1").URL || got.SignInDeadline == nil || !got.SignInDeadline.Equal(want) {
		t.Errorf("served identity_provider = %+v, want the page and a deadline of %v", got, want)
	}
	frames := orgFrames(t, h, before)
	if len(frames) != 1 || frames[0].SignInURL != orgConsent("o1").URL || frames[0].SignInDeadline == nil ||
		!frames[0].SignInDeadline.Equal(want) {
		t.Errorf("mcp_identity_provider frames = %+v, want one carrying the page and its deadline", frames)
	}
	if n := len(eventsSince(h, before, marotte.EventOpenExternalURL)); n != 0 {
		t.Errorf("open_external_url events = %d, want none for the card's own page", n)
	}
}

// A page KAS opens by itself (a tool call whose organization token was refused) is a banner for KAS's 300 s wait,
// marked as the organization's.
func TestAnchorConsentURL_IdentityProviderBanner(t *testing.T) {
	h, _, _ := orgHub(t)
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	before := h.bus.fanout.Position().Head

	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true); shown.how != consentBanner {
		t.Fatalf("anchorConsentURL = %+v, want a banner", shown)
	}

	events := eventsSince(h, before, marotte.EventOpenExternalURL)
	if len(events) != 1 {
		t.Fatalf("open_external_url events = %d, want one", len(events))
	}
	var f struct {
		Payload marotte.OpenExternalURLPayload `json:"payload"`
	}
	if err := json.Unmarshal(events[0].Event.Data, &f); err != nil {
		t.Fatal(err)
	}
	if !f.Payload.IdentityProviderSignIn || !f.Payload.ExpiresAt.Equal(clock.now().Add(orgSignInTTL)) {
		t.Errorf("payload = %+v, want identity_provider_sign_in and an expiry 300 s out", f.Payload)
	}
}

// KAS re-opens a running sign-in's page for each caller that joins it, so a banner raised before the user pressed
// Sign in moves to the card when KAS opens it again.
func TestAnchorConsentURL_IdentityProviderPromotedOnRejoin(t *testing.T) {
	h, _, _ := orgHub(t)
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true); shown.how != consentBanner {
		t.Fatalf("first open = %+v, want a banner", shown)
	}
	postOrgSignIn(t, h)

	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true); shown.how != consentOnCard {
		t.Fatalf("re-open = %+v, want promoted to the card", shown)
	}
	if got := servedOrg(t, h); got.SignInURL != orgConsent("o1").URL {
		t.Errorf("served identity_provider = %+v, want the page on the card", got)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true); shown.how != consentSeen {
		t.Errorf("a third open = %+v, want seen", shown)
	}
}

// signIn's budget must outlast KAS's own callback wait, or a late success reads as a failure.
func TestOrgSignIn_CallBudget(t *testing.T) {
	h, lane, _ := orgHub(t)
	start := time.Now()
	postOrgSignIn(t, h)

	if left := lane.deadlineOf(methodV3MCPIdentityProviderSignIn).Sub(start); left <= orgSignInTTL {
		t.Errorf("signIn budget = %v, want over KAS's %v callback wait", left, orgSignInTTL)
	}
}

// With no live session KAS sends no status, so signIn's reply is the verdict.
func TestOrgSignIn_ReplyIsVerdict(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h, _, release := orgHub(t)
		postOrgSignIn(t, h)
		release()
		joinInflight(h)

		got := servedOrg(t, h)
		if got.SignInDeadline != nil || got.Reported == nil || got.Reported.State != orgStateSignedIn || got.Error != "" {
			t.Errorf("served identity_provider = %+v, want signed-in with no sign-in standing", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		h := newHubWithMCPConfig(t, enabledConfig())
		lane := fakeSourceOf(t, h).lane
		lane.setCallRPCErr(methodV3MCPIdentityProviderSignIn, &marotte.RPCError{
			Code: -32603, Message: "No identity provider is set up for enterprise sign-in.",
		})
		postOrgSignIn(t, h)
		joinInflight(h)

		got := servedOrg(t, h)
		if got.SignInDeadline != nil || !strings.Contains(got.Error, "No identity provider is set up") {
			t.Errorf("served identity_provider = %+v, want no sign-in standing and kiro-cli's reason", got)
		}
		if rec := postJSON(h.mcpRegistry.handleOrgSignIn, "/api/mcp/identity-provider/sign-in", `{}`); rec.Code != http.StatusAccepted {
			t.Errorf("retry = %d %s, want 202", rec.Code, rec.Body.String())
		}
		joinInflight(h)
	})
	t.Run("one stands at a time", func(t *testing.T) {
		h, _, _ := orgHub(t)
		postOrgSignIn(t, h)
		if rec := postJSON(h.mcpRegistry.handleOrgSignIn, "/api/mcp/identity-provider/sign-in", `{}`); rec.Code != http.StatusConflict {
			t.Errorf("second sign-in = %d %s, want 409", rec.Code, rec.Body.String())
		}
	})
}

// A report saying signed-in ends the sign-in and its page; one leaving signing-in another way ends the page and
// leaves the reply to say why.
func TestOrgSignIn_StatusEndsAnchors(t *testing.T) {
	signingIn := &marotte.MCPIdentityProviderState{State: orgStateSigningIn, Issuer: "https://idp.example.com"}
	t.Run("signed in", func(t *testing.T) {
		h, _, _ := orgHub(t)
		postOrgSignIn(t, h)
		h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true)
		reportOrg(t, h, "s1", signingIn)

		reportOrg(t, h, "s1", &marotte.MCPIdentityProviderState{State: orgStateSignedIn, Issuer: "https://idp.example.com", Account: "ana"})

		got := servedOrg(t, h)
		if got.SignInDeadline != nil || got.SignInURL != "" || got.Reported == nil || got.Reported.Account != "ana" {
			t.Errorf("served identity_provider = %+v, want signed-in as ana with no sign-in standing", got)
		}
		if rec := postRelay(t, h, "", "http://localhost:3128/oauth/callback?code=c&state=o1"); rec.Code != http.StatusConflict ||
			!strings.Contains(rec.Body.String(), errRelayNoFlow.Error()) {
			t.Errorf("a paste after the sign-in = %d %s, want 409 no flow", rec.Code, rec.Body.String())
		}
	})
	t.Run("failed before its reply", func(t *testing.T) {
		h := newHubWithMCPConfig(t, enabledConfig())
		lane := fakeSourceOf(t, h).lane
		gate := make(chan struct{})
		lane.mu.Lock()
		lane.onCallFails = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
			if method != methodV3MCPIdentityProviderSignIn {
				return nil, nil, false, nil
			}
			<-gate
			return nil, nil, false, errors.New("the identity provider denied the sign-in")
		}
		lane.mu.Unlock()
		postOrgSignIn(t, h)
		h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true)
		reportOrg(t, h, "s1", signingIn)

		reportOrg(t, h, "s1", &marotte.MCPIdentityProviderState{State: orgStateSignedOut, Issuer: "https://idp.example.com"})
		if got := servedOrg(t, h); got.SignInURL != "" || got.SignInDeadline == nil {
			t.Errorf("served identity_provider = %+v, want the page gone and the sign-in awaiting its reply", got)
		}
		close(gate)
		joinInflight(h)
		if got := servedOrg(t, h); got.SignInDeadline != nil || !strings.Contains(got.Error, "denied the sign-in") {
			t.Errorf("served identity_provider = %+v, want the reply's reason", got)
		}
	})
	t.Run("an engine end clears it", func(t *testing.T) {
		h, _, _ := orgHub(t)
		postOrgSignIn(t, h)
		reportOrg(t, h, "s1", signingIn)

		h.mcpRegistry.clearAll()

		if got := servedMCPBody(t, h).IdentityProvider; got != nil {
			t.Errorf("served identity_provider = %+v, want it omitted after the engine ended", got)
		}
	})
}

// Sign out is also the card's Cancel: KAS discards the running attempt, and marotte ends the sign-in at once.
func TestOrgSignOut_CancelsStanding(t *testing.T) {
	h, lane, release := orgHub(t)
	postOrgSignIn(t, h)
	h.mcpRegistry.anchorConsentURL(t.Context(), orgConsent("o1"), true)

	if rec := postJSON(h.mcpRegistry.handleOrgSignOut, "/api/mcp/identity-provider/sign-out", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("sign-out = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if n := countCalls(lane, methodV3MCPIdentityProviderSignOut); n != 1 {
		t.Errorf("signOut calls = %d, want 1", n)
	}
	if got := servedOrg(t, h); got.SignInDeadline != nil || got.SignInURL != "" {
		t.Errorf("served identity_provider = %+v, want no sign-in standing", got)
	}

	release()
	joinInflight(h)
	if got := servedOrg(t, h); got.Reported != nil {
		t.Errorf("served identity_provider = %+v after the cancelled sign-in answered, want it left signed out", got)
	}
}
