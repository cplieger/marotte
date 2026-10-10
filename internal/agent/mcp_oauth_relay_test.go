package agent

// The listener is a real httptest server on 127.0.0.1, the shape of KAS's ephemeral listener,
// so relayClientFor's loopback policy is exercised for real.

import (
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

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/ssrf/v4"
)

// authURLFor builds the authorization URL KAS would store, the relay's trust anchor, the way KAS builds it.
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

// stageFlow puts a server into waiting-for-authorization with KAS's advertised URL.
func stageFlow(t *testing.T, h *Runtime, server, listenerURL, state string) {
	t.Helper()
	h.mcpRegistry.RecordOAuth(t.Context(), server, marotte.MCPSource{}, authURLFor(t, listenerURL, state))
}

func relayState(t *testing.T, h *Runtime, server string) (relayed, pending bool) {
	t.Helper()
	for _, s := range h.mcpRegistry.snapshot() {
		if s.Name == server {
			return s.Relayed, s.State == mcpStateOAuth
		}
	}
	t.Fatalf("server %q is not in the registry snapshot", server)
	return false, false
}

func TestOAuthRelay_DeliversAStrandedCallback(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
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
	h := newHubWithMCPConfig(nil)
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
	h := newHubWithMCPConfig(nil)
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
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-one")
	pastedA := pastedFor(t, l.srv.URL, "c1", "st-one")

	firstStatus := make(chan int, 1)
	go func() { firstStatus <- postRelay(t, h, "linear", pastedA).Code }()
	if code := l.waitForCallback(t); code != "c1" {
		t.Fatalf("listener saw code %q, want the first attempt's %q", code, "c1")
	}

	// The user restarted: KAS advertises a new URL and recordOAuth replaces the record.
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

// A fresh attempt clears the latch through recordOAuth's whole-record rewrite.
func TestOAuthRelay_ANewAttemptClearsTheLatch(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
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

// No flow in flight means no relay: nothing to check a paste against.
func TestOAuthRelay_RefusesWithNoFlowInFlight(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	for name, server := range map[string]string{
		"a server with no authorization pending": "linear",
		"a server that does not exist at all":    "never-heard-of-it",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(nil)
			if server == "linear" {
				// Connected, not awaiting authorization.
				h.mcpRegistry.RecordConnected(t.Context(), server, marotte.MCPSource{}, nil, nil, nil, nil)
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

// A listener refusing the callback leaves the attempt retryable: a 4xx is usually KAS's own state check.
func TestOAuthRelay_ARefusedCallbackStaysRetryable(t *testing.T) {
	l := newCallbackListener(t, http.StatusBadRequest)
	h := newHubWithMCPConfig(nil)
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
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", dead, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, dead, "the-code", "st-abc"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthRelay_RejectsNonPOST(t *testing.T) {
	h := newHubWithMCPConfig(nil)
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
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	// The third path is the one that leaked: *url.Error's message opens with the full request URL.
	postRelay(t, h, "linear", pastedFor(t, l.srv.URL, secret, "st-abc"))

	stageFlow(t, h, "linear", l.srv.URL, "st-abc")
	l.status = http.StatusInternalServerError
	postRelay(t, h, "linear", pastedFor(t, l.srv.URL, secret, "st-abc"))

	dead := l.srv.URL
	l.srv.Close()
	stageFlow(t, h, "linear", dead, "st-abc")
	postRelay(t, h, "linear", pastedFor(t, dead, secret, "st-abc"))

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
		"a mismatched state belongs to a different sign-in": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=st-other",
			want:   errRelayStateDrift,
		},
		"a missing state cannot be verified": {
			pasted: "http://" + host + "/oauth/callback?code=abc",
			want:   errRelayStateDrift,
		},
		"a state that is a prefix of the real one is refused": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=st-ab",
			want:   errRelayStateDrift,
		},
		"an authorization URL with no redirect_uri has nothing to relay to": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth:   "https://provider.example/authorize?state=" + state,
			want:   errRelayNoRedirect,
		},
		"an authorization URL with no state cannot verify a paste": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth:   "https://provider.example/authorize?redirect_uri=http%3A%2F%2F" + host + "%2Foauth%2Fcallback",
			want:   errRelayNoState,
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
		if s := aq.Query().Get("state"); s == "" || s != q.Get("state") {
			t.Errorf("accepted state %q against advertised state %q", q.Get("state"), s)
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
