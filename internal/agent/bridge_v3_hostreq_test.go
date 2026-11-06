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
	"time"

	"github.com/cplieger/go/sse"
	"github.com/cplieger/marotte/internal/marotte"
)

func openExternalURLMsg(t *testing.T, id int64, url string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		Method: methodKiroOpenExternalURL,
		ID:     &id,
		Params: mustJSON(t, map[string]any{"url": url}),
	}
}

// openURL is the engine opening url on its lane, answered once the answer's broadcasts are buffered.
func openURL(t *testing.T, h *Runtime, lane *fakeBridge, id int64, url string) error {
	t.Helper()
	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, id, url))
	answer := awaitLaneAnswer(t, lane, id)
	joinInflight(t, h)
	return answer.err
}

// eventsSince is the buffered events of type typ after the buffer position since.
func eventsSince(h *Runtime, since uint64, typ marotte.EventType) []sse.ReplayEvent {
	var out []sse.ReplayEvent
	for _, e := range bufferedSince(h, since) {
		if strings.Contains(string(e.Event.Data), `"type":"`+string(typ)+`"`) {
			out = append(out, e)
		}
	}
	return out
}

// connectPage holds one live SSE connection, a page that can show a URL, until the test ends or disconnect is
// called; disconnect returns once no page is connected.
func connectPage(t *testing.T, h *Runtime) (disconnect func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
		req.Header.Set(wireHeader, "1")
		h.handleSSE(httptest.NewRecorder(), req)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	deadline := time.Now().Add(5 * time.Second)
	for h.bus.fanout.ClientCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the page's SSE connection never subscribed")
		}
		time.Sleep(time.Millisecond)
	}
	return func() {
		cancel()
		<-served
		deadline := time.Now().Add(5 * time.Second)
		for h.bus.fanout.ClientCount() != 0 {
			if time.Now().After(deadline) {
				t.Fatal("the page's SSE connection never unsubscribed")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// signingInOnLane is a runtime whose linear waits for sign-in with Sign in pressed, and the engine lane that opens
// its consent URLs.
func signingInOnLane(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
		t.Fatalf("claimSignIn(linear) = %v", err)
	}
	return h, lane
}

// A later consent URL, another server's re-auth, takes no standing Sign in's slot: it is shown on every page.
func TestEngineLane_AConsentURLAfterTheSignInsOwnIsShownEverywhere(t *testing.T) {
	lA := newCallbackListener(t, http.StatusOK)
	lB := newCallbackListener(t, http.StatusOK)
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
	connectPage(t, h)
	if err := openURL(t, h, lane, 1, authURLFor(t, lA.srv.URL, "st-a")); err != nil {
		t.Fatalf("A's answer = %v, want success", err)
	}
	before := h.bus.fanout.Position().Head
	urlB := authURLFor(t, lB.srv.URL, "st-b")

	if err := openURL(t, h, lane, 2, urlB); err != nil {
		t.Fatalf("B's answer = %v, want success", err)
	}

	shown := eventsSince(h, before, marotte.EventOpenExternalURL)
	if len(shown) != 1 {
		t.Fatalf("open_external_url events since A = %d, want 1 for B", len(shown))
	}
	var evt struct {
		Payload marotte.OpenExternalURLPayload `json:"payload"`
	}
	if err := json.Unmarshal(shown[0].Event.Data, &evt); err != nil {
		t.Fatalf("decoding the event: %v", err)
	}
	if evt.Payload.URL != urlB {
		t.Errorf("banner url = %q, want B %q", evt.Payload.URL, urlB)
	}
	// KAS's LAZY_AUTH_TIMEOUT_MS: B is not the standing Sign in's, so it gets the lazy wait.
	if want := clock.now().Add(lazySignInTTL); !evt.Payload.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", evt.Payload.ExpiresAt, want)
	}
	if n := len(eventsSince(h, before, marotte.EventMCPSignInOpen)); n != 0 {
		t.Errorf("mcp_sign_in_open events since A = %d, want none: B is not linear's", n)
	}
}

// A URL with no state could never be relayed, so it must not take the standing Sign in's slot from the real one.
func TestEngineLane_AConsentURLWithoutAStateLeavesTheSignInWaiting(t *testing.T) {
	lA := newCallbackListener(t, http.StatusOK)
	h, lane := signingInOnLane(t)
	connectPage(t, h)
	before := h.bus.fanout.Position().Head

	if err := openURL(t, h, lane, 1, "https://provider.example/authorize?client_id=x"); err != nil {
		t.Fatalf("the stateless URL's answer = %v, want success", err)
	}
	if n := len(eventsSince(h, before, marotte.EventOpenExternalURL)); n != 1 {
		t.Errorf("open_external_url events = %d, want the stateless URL shown on every page", n)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateOAuth || row.OAuthURL != "" {
		t.Errorf("row after the stateless URL = %+v, want needs_auth with no consent URL", row)
	}
	urlA := authURLFor(t, lA.srv.URL, "st-a")
	if err := openURL(t, h, lane, 2, urlA); err != nil {
		t.Fatalf("A's answer = %v, want success", err)
	}
	if row := rowOf(t, h, "linear"); row.State != mcpStateSigningIn || row.OAuthURL != urlA {
		t.Errorf("row after A = %+v, want signing_in on A", row)
	}
}

// A state already anchored is answered as seen, even with no page left to show it: KAS's attempt keeps waiting.
func TestEngineLane_AnAnchoredURLSeenAgainWithNoPageIsAnswered(t *testing.T) {
	lL := newCallbackListener(t, http.StatusOK)
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	disconnect := connectPage(t, h)
	urlL := authURLFor(t, lL.srv.URL, "st-l")
	if err := openURL(t, h, lane, 1, urlL); err != nil {
		t.Fatalf("first answer = %v, want success", err)
	}
	disconnect()

	if err := openURL(t, h, lane, 2, urlL); err != nil {
		t.Errorf("second answer = %v, want success: its state is already anchored", err)
	}

	h.mcpRegistry.mu.RLock()
	n := len(h.mcpRegistry.anchors)
	h.mcpRegistry.mu.RUnlock()
	if n != 1 {
		t.Errorf("anchors = %d, want the one for st-l", n)
	}
	if rec := postRelay(t, h, "", pastedFor(t, lL.srv.URL, "cl", "st-l")); rec.Code != http.StatusOK {
		t.Errorf("L's paste = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// A consent URL seen again keeps its attribution and shows nothing new: no second sign-in-open resets the row's
// relayed state on the pages.
func TestEngineLane_AConsentURLSeenAgainKeepsItsAttribution(t *testing.T) {
	lA := newCallbackListener(t, http.StatusOK)
	h, lane := signingInOnLane(t)
	connectPage(t, h)
	urlA := authURLFor(t, lA.srv.URL, "st-a")
	if err := openURL(t, h, lane, 1, urlA); err != nil {
		t.Fatalf("first answer = %v, want success", err)
	}
	before := h.bus.fanout.Position().Head

	if err := openURL(t, h, lane, 2, urlA); err != nil {
		t.Errorf("second answer = %v, want success", err)
	}

	if row := rowOf(t, h, "linear"); row.State != mcpStateSigningIn || row.OAuthURL != urlA {
		t.Errorf("row = %+v, want signing_in on A", row)
	}
	if n := len(eventsSince(h, before, marotte.EventOpenExternalURL)); n != 0 {
		t.Errorf("open_external_url events since the first open = %d, want none", n)
	}
	if n := len(eventsSince(h, before, marotte.EventMCPSignInOpen)); n != 0 {
		t.Errorf("mcp_sign_in_open events since the first open = %d, want none", n)
	}
}

// An unclaimed URL is shown on every page and anchored for the relay by its state.
func TestEngineLane_OpenExternalURLWithAPageConnectedIsShownEverywhere(t *testing.T) {
	const consent = "https://auth.example.com/authorize?state=s1&redirect_uri=http%3A%2F%2F127.0.0.1%3A4000%2Foauth%2Fcallback"
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	connectPage(t, h)
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 12, consent))

	answer := awaitLaneAnswer(t, lane, 12)
	joinInflight(t, h)
	if answer.err != nil {
		t.Fatalf("answer error = %v, want success so KAS keeps its listener waiting", answer.err)
	}
	var shown []sse.ReplayEvent
	for _, e := range bufferedSince(h, before) {
		if strings.Contains(string(e.Event.Data), `"type":"`+string(marotte.EventOpenExternalURL)+`"`) {
			shown = append(shown, e)
		}
	}
	if len(shown) != 1 {
		t.Fatalf("open_external_url events = %d, want 1", len(shown))
	}
	if shown[0].Event.Topic != "" || !strings.Contains(string(shown[0].Event.Data), `"url":`) {
		t.Errorf("event = topic %q data %s, want every page's topic carrying the URL", shown[0].Event.Topic, shown[0].Event.Data)
	}
	h.mcpRegistry.mu.RLock()
	anchors := slices.Clone(h.mcpRegistry.anchors)
	h.mcpRegistry.mu.RUnlock()
	if len(anchors) != 1 || anchors[0].state != "s1" || anchors[0].authURL != consent {
		t.Fatalf("anchors = %+v, want one on the URL keyed by its state s1", anchors)
	}
	var evt struct {
		Payload marotte.OpenExternalURLPayload `json:"payload"`
	}
	if err := json.Unmarshal(shown[0].Event.Data, &evt); err != nil {
		t.Fatalf("decoding the event: %v", err)
	}
	// A page replaying the event later must see when KAS stopped waiting, not a fresh 300 s.
	if !evt.Payload.ExpiresAt.Equal(anchors[0].expires) {
		t.Errorf("expires_at = %v, want the anchor's deadline %v", evt.Payload.ExpiresAt, anchors[0].expires)
	}
}

// With no page connected nobody can open the URL, so KAS is told at once rather than waiting 300 s.
func TestEngineLane_OpenExternalURLWithNoPageConnectedIsRefused(t *testing.T) {
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 11, "https://auth.example.com/authorize?state=s1"))

	answer := awaitLaneAnswer(t, lane, 11)
	// The answer runs on inflight and may broadcast after it responds, so the buffer is read only once it returns.
	joinInflight(t, h)
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](answer.err)
	if !isRPC || rpcErr.Code != -32000 {
		t.Errorf("answer = (result %v, error %v), want RPC error -32000 so KAS ends the attempt at once", answer.result, answer.err)
	}
	for _, ty := range extractTypes(t, bufferedSince(h, before)) {
		if ty == string(marotte.EventOpenExternalURL) {
			t.Errorf("a URL no page can show was broadcast as open_external_url")
		}
	}
	h.mcpRegistry.mu.RLock()
	n := len(h.mcpRegistry.anchors)
	h.mcpRegistry.mu.RUnlock()
	if n != 0 {
		t.Errorf("anchors = %d, want none for a refused URL", n)
	}
}

func TestEngineLane_OpenExternalURLUnsafeSchemeIsRefusedWithInvalidParams(t *testing.T) {
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	connectPage(t, h)
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 9, "javascript:alert(1)"))

	answer := awaitLaneAnswer(t, lane, 9)
	joinInflight(t, h)
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](answer.err)
	if !isRPC || rpcErr.Code != -32602 {
		t.Errorf("answer = (result %v, error %v), want RPC error -32602", answer.result, answer.err)
	}
	for _, ty := range extractTypes(t, bufferedSince(h, before)) {
		if ty == string(marotte.EventOpenExternalURL) {
			t.Errorf("an unsafe URL was broadcast as open_external_url")
		}
	}
}

func TestHandleKiroClientRequest_DoesNotClaimGetAccessToken(t *testing.T) {
	h, _, _ := newTestHub()
	id := int64(3)
	tests := map[string]struct {
		msg  *marotte.RPCResponse
		want bool
	}{
		"relay_owned_auth": {
			msg:  &marotte.RPCResponse{ID: &id, Method: "_kiro/auth/get" + "AccessToken"},
			want: false,
		},
		"shell_type": {
			msg:  &marotte.RPCResponse{ID: &id, Method: methodKiroShellType},
			want: true,
		},
		"open_external_url_is_the_engine_lanes": {
			msg:  openExternalURLMsg(t, id, "https://x.example"),
			want: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := h.inbound.handleKiroClientRequest(t.Context(), "c1", h.originOf("c1"), tc.msg); got != tc.want {
				t.Errorf("handleKiroClientRequest(%q) = %v, want %v", tc.msg.Method, got, tc.want)
			}
		})
	}
}
