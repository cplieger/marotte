package agent

// The listener is a real httptest server on 127.0.0.1, the shape of KAS's ephemeral listener,
// so relayClientFor's loopback policy is exercised for real.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/ssrf"
	"github.com/cplieger/marotte/internal/marotte"
)

// authURLFor builds the consent URL KAS opens for a sign-in, the relay's trust anchor, the way KAS builds it.
func authURLFor(t *testing.T, listenerURL, state string) string {
	t.Helper()
	lu, err := url.Parse(listenerURL)
	if err != nil {
		t.Fatalf("parse listener URL %q: %v", listenerURL, err)
	}
	redirect := "http://127.0.0.1:" + lu.Port() + "/oauth/callback"
	q := url.Values{
		"client_id":     {"marotte-test"},
		"response_type": {"code"},
		"redirect_uri":  {redirect},
		"state":         {state},
	}
	return "https://provider.example/authorize?" + q.Encode()
}

func pastedFor(t *testing.T, listenerURL, code, state string) string {
	t.Helper()
	lu, err := url.Parse(listenerURL)
	if err != nil {
		t.Fatalf("parse listener URL %q: %v", listenerURL, err)
	}
	q := url.Values{"code": {code}, "state": {state}}
	return "http://127.0.0.1:" + lu.Port() + "/oauth/callback?" + q.Encode()
}

func postRelay(t *testing.T, h *Runtime, server, pasted string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"server":` + strconv.Quote(server) + `,"redirect_url":` + strconv.Quote(pasted) + `}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/mcp/oauth-relay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleOAuthRelay(rec, req)
	return rec
}

// callbackListener stands in for KAS's redirect listener, recording the query so a test proves the code arrived verbatim.
type callbackListener struct {
	srv     *httptest.Server
	status  int
	gotCode string
	gotHits int
}

func newCallbackListener(t *testing.T, status int) *callbackListener {
	t.Helper()
	l := &callbackListener{status: status}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.gotHits++
		l.gotCode = r.URL.Query().Get("code")
		w.WriteHeader(l.status)
		_, _ = w.Write([]byte("you can close this window"))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

// stageFlow runs one sign-in attempt up to its consent page: the server waits for sign-in in a session of its own (so
// staging another server leaves it standing), the user presses Sign in, and KAS opens the consent URL.
func stageFlow(t *testing.T, h *Runtime, server, listenerURL, state string) {
	t.Helper()
	reportMCP(t, h, marotte.SessionID("sess-"+server), oauthMCP(server, "https://provider.example/passive"))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), server); err != nil {
		t.Fatalf("claimSignIn(%s) = %v", server, err)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, listenerURL, state), Server: server}, false); shown.how != consentOnRow || shown.server != server {
		t.Fatalf("anchorConsentURL = %+v, want on %s's row", shown, server)
	}
}

func relayState(t *testing.T, h *Runtime, server string) (relayed, pending bool) {
	t.Helper()
	for _, s := range h.mcpRegistry.snapshot() {
		if s.Name == server {
			return s.Relayed, s.State == mcpStateSigningIn
		}
	}
	t.Fatalf("server %q is not in the registry snapshot", server)
	return false, false
}

func TestOAuthRelay_DeliversAStrandedCallback(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "the-code", "st-abc"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if l.gotHits != 1 {
		t.Errorf("listener hits = %d, want exactly 1", l.gotHits)
	}
	// Verbatim, or the token exchange fails.
	if l.gotCode != "the-code" {
		t.Errorf("listener saw code %q, want %q", l.gotCode, "the-code")
	}
}

// The relay is single use per attempt: the first delivery spends the code.
func TestOAuthRelay_IsSingleUsePerAttempt(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")
	pasted := pastedFor(t, l.srv.URL, "the-code", "st-abc")

	if rec := postRelay(t, h, "linear", pasted); rec.Code != http.StatusOK {
		t.Fatalf("first relay status = %d, want 200", rec.Code)
	}
	rec := postRelay(t, h, "linear", pasted)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second relay status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already delivered") {
		t.Errorf("second relay body = %s, want it to name the already-delivered case", rec.Body.String())
	}
	if l.gotHits != 1 {
		t.Errorf("listener hits = %d, want 1: the second paste must not reach it", l.gotHits)
	}
}

// blockingListener parks the first request until released, so a test acts while a relay is in
// flight; later requests answer at once and are counted.
type blockingListener struct {
	srv     *httptest.Server
	codes   chan string
	release chan struct{}
	once    sync.Once
	hits    atomic.Int64
}

func newBlockingListener(t *testing.T, status int) *blockingListener {
	t.Helper()
	l := &blockingListener{
		codes:   make(chan string, 8),
		release: make(chan struct{}),
	}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := l.hits.Add(1)
		l.codes <- r.URL.Query().Get("code")
		if n == 1 {
			<-l.release
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("you can close this window"))
	}))
	t.Cleanup(func() {
		l.releaseAll()
		l.srv.Close()
	})
	return l
}

// waitForCallback blocks until the next callback arrives: a sleep could miss the race window.
func (l *blockingListener) waitForCallback(t *testing.T) string {
	t.Helper()
	select {
	case code := <-l.codes:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("no callback reached the loopback listener")
		return ""
	}
}

// releaseAll lets every parked request answer. Idempotent.
func (l *blockingListener) releaseAll() {
	l.once.Do(func() { close(l.release) })
}

// The single-use rule must be atomic: the listener holds the first relay open while a second paste tries.
func TestOAuthRelay_ConcurrentPastesDeliverOnce(t *testing.T) {
	l := newBlockingListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")
	pasted := pastedFor(t, l.srv.URL, "the-code", "st-abc")

	firstStatus := make(chan int, 1)
	go func() { firstStatus <- postRelay(t, h, "linear", pasted).Code }()
	if code := l.waitForCallback(t); code != "the-code" {
		t.Fatalf("listener saw code %q, want %q", code, "the-code")
	}

	// In flight now; this paste must be refused.
	second := postRelay(t, h, "linear", pasted)
	l.releaseAll()

	if code := <-firstStatus; code != http.StatusOK {
		t.Fatalf("first relay status = %d, want 200", code)
	}
	if second.Code != http.StatusConflict {
		t.Fatalf("concurrent second relay status = %d, want 409: the reservation must be taken before the network call, not after it; body %s",
			second.Code, second.Body.String())
	}
	if got := l.hits.Load(); got != 1 {
		t.Errorf("listener hits = %d, want exactly 1: the same authorization code was replayed twice", got)
	}
}

// An old relay's completion must not latch a newer attempt, or the new one's paste box is withheld.
func TestOAuthRelay_AnOldRelayCannotLatchANewAttempt(t *testing.T) {
	l := newBlockingListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-one")
	pastedA := pastedFor(t, l.srv.URL, "c1", "st-one")

	firstStatus := make(chan int, 1)
	go func() { firstStatus <- postRelay(t, h, "linear", pastedA).Code }()
	if code := l.waitForCallback(t); code != "c1" {
		t.Fatalf("listener saw code %q, want the first attempt's %q", code, "c1")
	}

	// The attempt ended and the user signed in again: a new consent URL replaces the record.
	stageFlow(t, h, "linear", l.srv.URL, "st-two")
	l.releaseAll()
	if code := <-firstStatus; code != http.StatusOK {
		t.Fatalf("the in-flight relay ended %d, want 200", code)
	}

	if relayed, pending := relayState(t, h, "linear"); relayed || !pending {
		t.Fatalf("relayed = %v, pending = %v; the old relay's completion latched the new attempt, whose own callback was never delivered",
			relayed, pending)
	}
	// The new attempt stays relayable.
	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "c2", "st-two"))
	if rec.Code != http.StatusOK {
		t.Fatalf("relaying the new attempt = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if code := l.waitForCallback(t); code != "c2" {
		t.Errorf("listener saw code %q, want the second attempt's %q", code, "c2")
	}
}

// A fresh attempt's consent URL is a new anchor with its own latch.
func TestOAuthRelay_ANewAttemptClearsTheLatch(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-one")
	if rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "c1", "st-one")); rec.Code != http.StatusOK {
		t.Fatalf("first relay status = %d, want 200", rec.Code)
	}

	stageFlow(t, h, "linear", l.srv.URL, "st-two")
	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "c2", "st-two"))

	if rec.Code != http.StatusOK {
		t.Fatalf("relay after a new attempt status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if l.gotCode != "c2" {
		t.Errorf("listener saw code %q, want the second attempt's %q", l.gotCode, "c2")
	}
}

// A server waiting for sign-in with no Sign in pressed has no live listener, so a paste is refused unsent.
func TestOAuthRelay_RefusesThePassiveURL(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	msg := &marotte.RPCResponse{Method: "_kiro/mcp/status", Params: mustJSON(t, map[string]any{"servers": []map[string]any{{
		"name": "linear", "status": "failed", "failedAuthorization": true, "authorizationUrl": authURLFor(t, l.srv.URL, "st-abc"),
	}}})}
	h.translateACPEvent("", h.originOf(""), msg)

	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "the-code", "st-abc"))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "press Sign in first") {
		t.Fatalf("status = %d %s, want 409 asking for a Sign in", rec.Code, rec.Body.String())
	}
	if l.gotHits != 0 {
		t.Errorf("listener hits = %d, want 0: the status entry's URL is no anchor", l.gotHits)
	}
}

// No flow in flight means no relay: nothing to check a paste against.
func TestOAuthRelay_RefusesWithNoFlowInFlight(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	for name, server := range map[string]string{
		"a server with no authorization pending": "linear",
		"a server that does not exist at all":    "never-heard-of-it",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, nil)
			if server == "linear" {
				// Connected, not awaiting authorization.
				reportMCP(t, h, marotte.SessionID("sess-"+server), connectedMCP(server, marotte.MCPSource{}))
			}
			rec := postRelay(t, h, server, pastedFor(t, l.srv.URL, "c", "st"))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
			}
			if l.gotHits != 0 {
				t.Errorf("listener hits = %d, want 0", l.gotHits)
			}
		})
	}
}

// A mid-session re-auth's consent URL names no server, so its pasted callback is relayed on the anchor its state
// picks, single use, whatever server the paste names.
func TestOAuthRelay_ALazySignInIsRelayedByItsState(t *testing.T) {
	for name, server := range map[string]string{
		"no server named":                       "",
		"a connected server, the re-auth's row": "linear",
	} {
		t.Run(name, func(t *testing.T) {
			l := newCallbackListener(t, http.StatusOK)
			h := newHubWithMCPConfig(t, nil)
			reportMCP(t, h, "s1", connectedMCP("linear", marotte.MCPSource{}))
			h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, l.srv.URL, "st-lazy")}, true)
			pasted := pastedFor(t, l.srv.URL, "the-code", "st-lazy")

			if rec := postRelay(t, h, server, pasted); rec.Code != http.StatusOK {
				t.Fatalf("relay status = %d, want 200; body %s", rec.Code, rec.Body.String())
			}
			if l.gotHits != 1 || l.gotCode != "the-code" {
				t.Errorf("listener hits %d code %q, want the code delivered once", l.gotHits, l.gotCode)
			}
			rec := postRelay(t, h, server, pasted)
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errRelayAlreadyDone.Error()) {
				t.Errorf("second paste = %d %s, want 409 already delivered", rec.Code, rec.Body.String())
			}
			if l.gotHits != 1 {
				t.Errorf("listener hits = %d after the second paste, want 1", l.gotHits)
			}
		})
	}
}

func TestOAuthRelay_ALazySignInRefusesAnotherState(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, l.srv.URL, "st-lazy")}, true)

	rec := postRelay(t, h, "", pastedFor(t, l.srv.URL, "the-code", "st-other"))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errRelayStateDrift.Error()) {
		t.Errorf("status = %d %s, want 409 naming a different sign-in", rec.Code, rec.Body.String())
	}
	if l.gotHits != 0 {
		t.Errorf("listener hits = %d, want 0", l.gotHits)
	}
}

// The paste is checked before any anchor is chosen, so a malformed one is named as such.
func TestOAuthRelay_ALazySignInRefusesAMalformedPaste(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, l.srv.URL, "st-lazy")}, true)

	rec := postRelay(t, h, "", "https://evil.example/oauth/callback?code=c&state=st-lazy")

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errRelayNotHTTP.Error()) {
		t.Errorf("status = %d %s, want 400 naming the scheme", rec.Code, rec.Body.String())
	}
	if rec := postRelay(t, h, "", pastedFor(t, l.srv.URL, "c", "st-lazy")); rec.Code != http.StatusOK {
		t.Errorf("the corrected paste = %d %s, want 200: a refused paste spends nothing", rec.Code, rec.Body.String())
	}
}

// KAS's lazy listener closes after LAZY_AUTH_TIMEOUT_MS, the engine's end drops every anchor, and the oldest goes
// past the cap.
func TestOAuthRelay_ALazySignInEnds(t *testing.T) {
	cases := map[string]func(t *testing.T, h *Runtime, clock *fakeClock, listenerURL string){
		"at KAS's timeout": func(_ *testing.T, _ *Runtime, clock *fakeClock, _ string) {
			clock.advance(lazySignInTTL)
		},
		"at the engine's end": func(t *testing.T, h *Runtime, _ *fakeClock, _ string) {
			h.mcpRegistry.clearAll(t.Context())
		},
		"past the cap": func(t *testing.T, h *Runtime, _ *fakeClock, listenerURL string) {
			for i := range maxConsentAnchors {
				h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, listenerURL, "st-later-"+strconv.Itoa(i))}, true)
			}
		},
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			l := newCallbackListener(t, http.StatusOK)
			h := newHubWithMCPConfig(t, nil)
			clock := &fakeClock{t: time.Unix(1_000_000, 0)}
			h.mcpRegistry.mu.Lock()
			h.mcpRegistry.now = clock.now
			h.mcpRegistry.mu.Unlock()
			h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, l.srv.URL, "st-lazy")}, true)
			clock.advance(lazySignInTTL - time.Second)

			end(t, h, clock, l.srv.URL)

			rec := postRelay(t, h, "", pastedFor(t, l.srv.URL, "c", "st-lazy"))
			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d %s, want 409", rec.Code, rec.Body.String())
			}
			if l.gotHits != 0 {
				t.Errorf("listener hits = %d, want 0", l.gotHits)
			}
		})
	}
}

// A listener's refusal gives the anchor back, as it does a row's reservation.
func TestOAuthRelay_ALazySignInStaysRetryableAfterARefusal(t *testing.T) {
	l := newCallbackListener(t, http.StatusBadRequest)
	h := newHubWithMCPConfig(t, nil)
	h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, l.srv.URL, "st-lazy")}, true)
	pasted := pastedFor(t, l.srv.URL, "c", "st-lazy")

	if rec := postRelay(t, h, "", pasted); rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d %s, want 502", rec.Code, rec.Body.String())
	}
	l.status = http.StatusOK
	if rec := postRelay(t, h, "", pasted); rec.Code != http.StatusOK {
		t.Errorf("retry = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// A URL with no state could never match a paste, so it anchors nothing; KAS still waits its 300 s for it.
func TestAnchorConsentURL_KeepsNoURLWithoutAState(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	h.mcpRegistry.mu.Lock()
	h.mcpRegistry.now = clock.now
	h.mcpRegistry.mu.Unlock()
	before := h.bus.fanout.Position().Head
	shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://provider.example/authorize?client_id=x"}, true)
	if shown.how != consentBanner {
		t.Errorf("shown = %v, want consentBanner", shown.how)
	}
	h.mcpRegistry.mu.RLock()
	n := len(h.mcpRegistry.anchors)
	h.mcpRegistry.mu.RUnlock()
	if n != 0 {
		t.Errorf("anchors = %d, want 0", n)
	}
	banners := eventsSince(h, before, marotte.EventOpenExternalURL)
	if len(banners) != 1 {
		t.Fatalf("open_external_url events = %d, want 1", len(banners))
	}
	var evt struct {
		Payload marotte.OpenExternalURLPayload `json:"payload"`
	}
	if err := json.Unmarshal(banners[0].Event.Data, &evt); err != nil {
		t.Fatalf("decoding the event: %v", err)
	}
	if want := clock.now().Add(lazySignInTTL); !evt.Payload.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", evt.Payload.ExpiresAt, want)
	}
}

// Two overlapping sign-ins both complete: a later consent URL leaves the standing Sign in's anchor and row alone, and
// each paste reaches the listener its own URL named.
func TestOAuthRelay_ALaterConsentURLLeavesTheStandingSignInsAnchor(t *testing.T) {
	lA := newCallbackListener(t, http.StatusOK)
	lB := newCallbackListener(t, http.StatusOK)
	h, lane := signingInOnLane(t)
	connectPage(t, h)
	urlA := authURLFor(t, lA.srv.URL, "st-a")
	if err := openURL(t, h, lane, 1, urlA); err != nil {
		t.Fatalf("A's answer = %v, want success", err)
	}
	if err := openURL(t, h, lane, 2, authURLFor(t, lB.srv.URL, "st-b")); err != nil {
		t.Fatalf("B's answer = %v, want success", err)
	}

	if row := rowOf(t, h, "linear"); row.OAuthURL != urlA {
		t.Errorf("row link = %q, want A %q", row.OAuthURL, urlA)
	}
	if rec := postRelay(t, h, "linear", pastedFor(t, lA.srv.URL, "ca", "st-a")); rec.Code != http.StatusOK || lA.gotCode != "ca" {
		t.Errorf("A's paste = %d %s, listener A saw %q; want 200 delivering ca", rec.Code, rec.Body.String(), lA.gotCode)
	}
	if rec := postRelay(t, h, "", pastedFor(t, lB.srv.URL, "cb", "st-b")); rec.Code != http.StatusOK || lB.gotCode != "cb" {
		t.Errorf("B's paste = %d %s, listener B saw %q; want 200 delivering cb", rec.Code, rec.Body.String(), lB.gotCode)
	}
}

// The paste's state picks its anchor; the server it was pasted under picks nothing.
func TestOAuthRelay_APasteIsRelayedByItsStateWhicheverServerItNames(t *testing.T) {
	lL := newCallbackListener(t, http.StatusOK)
	lA := newCallbackListener(t, http.StatusOK)
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	connectPage(t, h)
	if err := openURL(t, h, lane, 1, authURLFor(t, lL.srv.URL, "st-l")); err != nil {
		t.Fatalf("L's answer = %v, want success", err)
	}
	reportMCP(t, h, sessionOf("linear"), awaitingSignIn("linear", marotte.MCPSource{}))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
		t.Fatalf("claimSignIn(linear) = %v", err)
	}
	if err := openURL(t, h, lane, 2, authURLFor(t, lA.srv.URL, "st-a")); err != nil {
		t.Fatalf("A's answer = %v, want success", err)
	}

	if rec := postRelay(t, h, "linear", pastedFor(t, lL.srv.URL, "cl", "st-l")); rec.Code != http.StatusOK || lL.gotCode != "cl" {
		t.Errorf("L's paste under linear = %d %s, listener L saw %q; want 200 delivering cl", rec.Code, rec.Body.String(), lL.gotCode)
	}
	if rec := postRelay(t, h, "linear", pastedFor(t, lA.srv.URL, "ca", "st-a")); rec.Code != http.StatusOK || lA.gotCode != "ca" {
		t.Errorf("A's paste under linear = %d %s, listener A saw %q; want 200 delivering ca", rec.Code, rec.Body.String(), lA.gotCode)
	}
}

// The anchor cap evicts the oldest anchor, never the standing Sign in's.
func TestOAuthRelay_LaterURLsNeverEvictTheStandingSignInsAnchor(t *testing.T) {
	lA := newCallbackListener(t, http.StatusOK)
	h, lane := signingInOnLane(t)
	connectPage(t, h)
	urlA := authURLFor(t, lA.srv.URL, "st-a")
	if err := openURL(t, h, lane, 1, urlA); err != nil {
		t.Fatalf("A's answer = %v, want success", err)
	}
	for i := range maxConsentAnchors {
		if err := openURL(t, h, lane, int64(2+i), authURLFor(t, lA.srv.URL, "st-later-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("later URL %d's answer = %v, want success", i, err)
		}
	}

	if row := rowOf(t, h, "linear"); row.OAuthURL != urlA {
		t.Errorf("row link = %q, want A %q", row.OAuthURL, urlA)
	}
	if rec := postRelay(t, h, "linear", pastedFor(t, lA.srv.URL, "ca", "st-a")); rec.Code != http.StatusOK || lA.gotCode != "ca" {
		t.Errorf("A's paste = %d %s, listener saw %q; want 200 delivering ca", rec.Code, rec.Body.String(), lA.gotCode)
	}
}

// A listener refusing the callback leaves the attempt retryable: a 4xx is usually KAS's own state check.
func TestOAuthRelay_ARefusedCallbackStaysRetryable(t *testing.T) {
	l := newCallbackListener(t, http.StatusBadRequest)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "the-code", "st-abc"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	if relayed, pending := relayState(t, h, "linear"); relayed || !pending {
		t.Errorf("relayed = %v, pending = %v; want a still-unrelayed pending flow", relayed, pending)
	}
}

// A gone listener is a gateway failure, not a success.
func TestOAuthRelay_ADeadListenerIsAGatewayFailure(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	dead := l.srv.URL
	l.srv.Close() // frees the port; nothing is listening now
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", dead, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, dead, "the-code", "st-abc"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthRelay_RejectsNonPOST(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/mcp/oauth-relay", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleOAuthRelay(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// The authorization code must never reach the log: it is a bearer credential during the exchange.
func TestOAuthRelay_NeverLogsTheCode(t *testing.T) {
	const secret = "super-secret-authorization-code"
	logs := captureLogs(t)
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(t, nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	// The third path is the one that leaked: *url.Error's message opens with the full request URL.
	postRelay(t, h, "linear", pastedFor(t, l.srv.URL, secret, "st-abc"))

	stageFlow(t, h, "linear", l.srv.URL, "st-two")
	l.status = http.StatusInternalServerError
	postRelay(t, h, "linear", pastedFor(t, l.srv.URL, secret, "st-two"))

	dead := l.srv.URL
	l.srv.Close()
	stageFlow(t, h, "linear", dead, "st-three")
	postRelay(t, h, "linear", pastedFor(t, dead, secret, "st-three"))

	if got := logs.String(); strings.Contains(got, secret) {
		t.Errorf("the authorization code reached the log:\n%s", got)
	}
}

func TestValidateRelayAddress(t *testing.T) {
	const (
		host  = "127.0.0.1:41234"
		state = "st-abc"
	)
	authURL := "https://provider.example/authorize?" + url.Values{
		"redirect_uri": {"http://" + host + "/oauth/callback"},
		"state":        {state},
	}.Encode()

	cases := map[string]struct {
		pasted string
		auth   string
		want   error
	}{
		"the ordinary stranded callback": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			want:   nil,
		},
		"clipboard whitespace is trimmed, not rejected": {
			pasted: "  http://" + host + "/oauth/callback?code=abc&state=" + state + "\n",
			want:   nil,
		},
		// `localhost` and `127.0.0.1` are both loopback; the spelling is never a refusal.
		"a localhost paste against a 127.0.0.1 advertisement": {
			pasted: "http://localhost:41234/oauth/callback?code=abc&state=" + state,
			want:   nil,
		},
		"a 127.0.0.1 paste against a localhost advertisement": {
			pasted: "http://127.0.0.1:41234/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://localhost:41234/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: nil,
		},
		"the RFC 9207 issuer parameter rides along": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state + "&iss=https%3A%2F%2Fp.example",
			want:   nil,
		},
		// relayMinPort and 65535 are both accepted: KAS's ephemeral port can land on the range's edges.
		"the lowest unprivileged port is accepted": {
			pasted: "http://127.0.0.1:1024/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://127.0.0.1:1024/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: nil,
		},
		"the highest port is accepted": {
			pasted: "http://127.0.0.1:65535/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://127.0.0.1:65535/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: nil,
		},
		"a port above the 16-bit range is refused": {
			pasted: "http://127.0.0.1:65536/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://127.0.0.1:65536/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: errRelayBadPort,
		},
		// DEL is excluded like CR and LF.
		"a DEL byte is refused": {
			pasted: "http://" + host + "/oauth/callback?code=a\x7f&state=" + state,
			want:   errRelayBadBytes,
		},
		"a remote host is refused": {
			pasted: "http://evil.example:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotLoopback,
		},
		"a host that merely starts with the literal is refused": {
			pasted: "http://127.0.0.1.evil.example:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotLoopback,
		},
		"a link-local address is not loopback": {
			pasted: "http://169.254.169.254:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotLoopback,
		},
		// The byte gate must run before isLoopbackHost, whose ToLower on an allow-list would fail open.
		// (Measured on go1.27.0: only U+0130 and U+212A lower into ASCII, neither matches.) This
		// case's error identity keeps the order.
		"a non-ASCII host never reaches the loopback allow-list": {
			pasted: "http://\u212Aocalhost:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayBadBytes,
		},
		"https is a different address, not a nicer one": {
			pasted: "https://" + host + "/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotHTTP,
		},
		"a non-http scheme is refused": {
			pasted: "file:///etc/passwd?code=abc&state=" + state,
			want:   errRelayNotHTTP,
		},
		"userinfo is refused": {
			pasted: "http://user:pw@127.0.0.1:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayHasCreds,
		},
		"a fragment means this is not the address the browser was sent to": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state + "#frag",
			want:   errRelayHasFragment,
		},
		"a privileged port is refused": {
			pasted: "http://127.0.0.1:80/oauth/callback?code=abc&state=" + state,
			want:   errRelayBadPort,
		},
		"no port at all is refused": {
			pasted: "http://127.0.0.1/oauth/callback?code=abc&state=" + state,
			want:   errRelayBadPort,
		},
		"no code means the sign-in did not complete": {
			pasted: "http://" + host + "/oauth/callback?state=" + state,
			want:   errRelayNoCode,
		},
		"an empty code is no code": {
			pasted: "http://" + host + "/oauth/callback?code=&state=" + state,
			want:   errRelayNoCode,
		},
		"an error redirect carries nothing to relay": {
			pasted: "http://" + host + "/oauth/callback?error=access_denied&state=" + state,
			want:   errRelayBadQuery,
		},
		"an unexpected query parameter is refused, not forwarded": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state + "&surprise=1",
			want:   errRelayBadQuery,
		},
		"a CRLF injection attempt cannot reach the request line": {
			pasted: "http://" + host + "/oauth/callback?code=a\r\nX-Evil: 1&state=" + state,
			want:   errRelayBadBytes,
		},
		"a raw space is refused": {
			pasted: "http://" + host + "/oauth/callback?code=a b&state=" + state,
			want:   errRelayBadBytes,
		},
		"a NUL byte is refused": {
			pasted: "http://" + host + "/oauth/callback?code=a\x00b&state=" + state,
			want:   errRelayBadBytes,
		},
		"non-ASCII is refused": {
			pasted: "http://" + host + "/oauth/callback?code=café&state=" + state,
			want:   errRelayBadBytes,
		},
		"an oversize paste is refused before it is parsed": {
			pasted: "http://" + host + "/oauth/callback?code=" + strings.Repeat("a", relayURLCap) + "&state=" + state,
			want:   errRelayTooLong,
		},
		"a non-URL is refused": {
			pasted: "http://[not-an-address/oauth/callback?code=abc",
			want:   errRelayUnparsable,
		},
		"a loopback port KAS never advertised is refused": {
			pasted: "http://127.0.0.1:9999/oauth/callback?code=abc&state=" + state,
			want:   errRelayTargetDrift,
		},
		"a path KAS never advertised is refused": {
			pasted: "http://" + host + "/somewhere/else?code=abc&state=" + state,
			want:   errRelayTargetDrift,
		},
		"an authorization URL with no redirect_uri has nothing to relay to": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth:   "https://provider.example/authorize?state=" + state,
			want:   errRelayNoRedirect,
		},
		// The advertisement is dialed, so a non-loopback stored value is refused.
		"an advertised redirect off-box is refused": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://169.254.169.254:41234/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: errRelayNoRedirect,
		},
		"an advertised redirect on a privileged port is refused": {
			pasted: "http://127.0.0.1:22/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://127.0.0.1:22/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: errRelayBadPort,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			auth := tc.auth
			if auth == "" {
				auth = authURL
			}
			got, err := validateRelayAddress(tc.pasted, auth)
			if err != tc.want {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want == nil && got == nil {
				t.Fatal("accepted the address but returned no URL to replay")
			}
			if tc.want != nil && got != nil {
				t.Error("refused the address but still returned a URL to replay")
			}
		})
	}
}

// The reservation binds a paste by its state: anything but the anchor's own state is refused and leaves the anchor
// unlatched, and with no anchor there is nothing to relay to.
func TestReserveRelay_BindsThePasteByItsState(t *testing.T) {
	cases := map[string]string{
		"a mismatched state belongs to a different sign-in": "st-other",
		"a missing state cannot be matched":                 "",
		"a state that is a prefix of the real one":          "st-ab",
	}
	for name, pasted := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, nil)
			h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: authURLFor(t, "http://127.0.0.1:41234", "st-abc")}, true)
			if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), pasted); !errors.Is(err, errRelayStateDrift) {
				t.Errorf("reserve %q = %v, want errRelayStateDrift", pasted, err)
			}
			if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), "st-abc"); err != nil {
				t.Errorf("reserve st-abc after the refusal = %v, want the anchor still unlatched", err)
			}
		})
	}
	t.Run("no anchor at all", func(t *testing.T) {
		h := newHubWithMCPConfig(t, nil)
		if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), "st-abc"); !errors.Is(err, errRelayNoFlow) {
			t.Errorf("reserve with no anchor = %v, want errRelayNoFlow", err)
		}
	})
}

// FuzzValidateRelayAddress asserts the acceptance invariant: anything accepted is safe to dial
// and forward without the handler re-checking.
func FuzzValidateRelayAddress(f *testing.F) {
	const authURL = "https://p.example/authorize?" +
		"redirect_uri=http%3A%2F%2F127.0.0.1%3A41234%2Foauth%2Fcallback&state=st-abc"

	f.Add("http://127.0.0.1:41234/oauth/callback?code=abc&state=st-abc", authURL)
	f.Add("http://localhost:41234/oauth/callback?code=abc&state=st-abc", authURL)
	f.Add("http://127.0.0.1:41234/oauth/callback?code=abc&state=wrong", authURL)
	f.Add("http://evil.example:41234/oauth/callback?code=abc&state=st-abc", authURL)
	f.Add("http://127.0.0.1:41234/oauth/callback?code=a\r\nX: 1&state=st-abc", authURL)
	f.Add("http://user:pw@127.0.0.1:41234/x?code=a&state=st-abc", authURL)
	f.Add("://", "")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, pasted, auth string) {
		got, err := validateRelayAddress(pasted, auth)
		if err != nil {
			if got != nil {
				t.Fatalf("refused (%v) but returned a URL to replay: %q", err, got.String())
			}
			return
		}
		if got == nil {
			t.Fatal("accepted but returned no URL to replay")
		}

		// Plain http to a loopback name on an unprivileged port.
		if !strings.EqualFold(got.Scheme, "http") {
			t.Errorf("accepted scheme %q, want http", got.Scheme)
		}
		if !isLoopbackHost(got.Hostname()) {
			t.Errorf("accepted non-loopback host %q", got.Hostname())
		}
		port, perr := strconv.Atoi(got.Port())
		if perr != nil || port < relayMinPort || port > 65535 {
			t.Errorf("accepted port %q, want %d..65535", got.Port(), relayMinPort)
		}

		// No credentials, no fragment.
		if got.User != nil {
			t.Error("accepted a URL carrying userinfo")
		}
		if got.Fragment != "" {
			t.Errorf("accepted a URL carrying fragment %q", got.Fragment)
		}

		// Only printable ASCII, so the request line cannot be split.
		if !isPrintableASCII(got.String()) {
			t.Errorf("accepted a URL that is not printable ASCII: %q", got.String())
		}

		// A code, and only callback keys.
		q := got.Query()
		if q.Get("code") == "" {
			t.Error("accepted a URL with no authorization code")
		}
		for k := range q {
			if _, ok := relayQueryKeys[k]; !ok {
				t.Errorf("accepted a URL carrying unexpected query key %q", k)
			}
		}

		// The dial target is KAS's and the query the paste's; nothing crosses over.
		aq, aerr := url.Parse(auth)
		if aerr != nil {
			t.Fatalf("accepted a paste against an unparsable authorization URL %q", auth)
		}
		want, werr := url.Parse(aq.Query().Get("redirect_uri"))
		if werr != nil {
			t.Fatalf("accepted a paste against an unparsable redirect_uri")
		}
		if got.Scheme != want.Scheme || got.Host != want.Host ||
			got.EscapedPath() != want.EscapedPath() {
			t.Errorf("dial target %q is not the advertised callback %q verbatim",
				got.Scheme+"://"+got.Host+got.EscapedPath(),
				want.Scheme+"://"+want.Host+want.EscapedPath())
		}
		if got.User != nil || got.Fragment != "" {
			t.Error("the advertised callback contributed userinfo or a fragment")
		}
		// Re-parsed from the input, so a rewrite would show.
		pu, puerr := url.Parse(strings.TrimSpace(pasted))
		if puerr != nil {
			t.Fatalf("accepted a paste that does not parse: %q", pasted)
		}
		if got.RawQuery != pu.RawQuery {
			t.Errorf("query was rewritten: %q, want the pasted %q", got.RawQuery, pu.RawQuery)
		}
	})
}

// TestRelayClientForPinsTheOnePort pins the client to the one port it was built for.
func TestRelayClientForPinsTheOnePort(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL + "/callback?code=abc&state=xyz")
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", srv.URL, err)
	}
	client, err := relayClientFor(target)
	if err != nil {
		t.Fatalf("relayClientFor(%q) error = %v, want a client", target, err)
	}

	resp, err := client.Get(target.String())
	if err != nil {
		t.Fatalf("GET the pinned port error = %v, want it allowed", err)
	}
	_ = resp.Body.Close()

	// A different loopback port is refused before any dial.
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	if err != nil {
		t.Fatalf("ParseUint(%q) error = %v", target.Port(), err)
	}
	other := max(uint16(port)+1, relayMinPort)
	//nolint:bodyclose // the request is expected to fail before a response exists
	_, err = client.Get("http://127.0.0.1:" + strconv.Itoa(int(other)) + "/callback")
	if err == nil {
		t.Fatalf("GET port %d with a client pinned to %d succeeded, want it refused", other, port)
	}
	// Assert the reason: a connection-refused error would pass a widened allowlist.
	var se *ssrf.Error
	if !errors.As(err, &se) || se.Kind != ssrf.KindBadPort {
		t.Errorf("GET port %d error = %v, want an ssrf KindBadPort refusal (the pin, not a dial failure)", other, err)
	}
}

// A port parseLoopbackCallback rejects is rejected here too.
func TestRelayClientForRefusesBadPorts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"privileged", "http://127.0.0.1:80/callback"},
		{"port zero", "http://127.0.0.1:0/callback"},
		{"no port", "http://127.0.0.1/callback"},
		// No unparseable port here: url.Parse rejects it first, so the case asserts nothing at this layer.
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v; every case in this table must reach relayClientFor", tc.raw, err)
			}
			if _, err := relayClientFor(u); err == nil {
				t.Errorf("relayClientFor(%q) = nil error, want the port refused", tc.raw)
			}
		})
	}
}

// TestParseLoopbackCallback_LengthCap pins relayURLCap as the longest accepted address.
func TestParseLoopbackCallback_LengthCap(t *testing.T) {
	t.Parallel()

	const prefix = "http://127.0.0.1:41234/oauth/callback?code="
	atCap := prefix + strings.Repeat("a", relayURLCap-len(prefix))
	// The fixture is only meaningful at the exact boundary.
	if len(atCap) != relayURLCap {
		t.Fatalf("fixture is %d bytes, want exactly relayURLCap (%d)", len(atCap), relayURLCap)
	}

	if _, err := parseLoopbackCallback(atCap); err != nil {
		t.Errorf("parseLoopbackCallback(%d-byte address) error = %v, want it accepted at the cap", len(atCap), err)
	}
	overCap := atCap + "a"
	if _, err := parseLoopbackCallback(overCap); !errors.Is(err, errRelayTooLong) {
		t.Errorf("parseLoopbackCallback(%d-byte address) error = %v, want %v", len(overCap), err, errRelayTooLong)
	}
}

// The port floor is inclusive.
func TestRelayClientForAcceptsTheLowestUnprivilegedPort(t *testing.T) {
	t.Parallel()

	raw := "http://127.0.0.1:" + strconv.Itoa(relayMinPort) + "/callback"
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	if _, err := relayClientFor(u); err != nil {
		t.Errorf("relayClientFor(%q) error = %v, want a client at the port floor", raw, err)
	}
}
