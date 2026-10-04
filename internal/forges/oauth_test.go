package forges

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

const testDeviceCode = "example-device-code"

// grantEndpoint is a GitHub appliance on a real loopback socket, so the
// library's own dialer and connection pool are the ones exercised: the OAuth
// endpoint pair, and the identity read a completed grant is verified with.
type grantEndpoint struct {
	srv *httptest.Server
	// hold, when set, keeps the first token request waiting until it is
	// closed; held is closed once that request is waiting.
	hold chan struct{}
	held chan struct{}
	// onUser, when set, runs as the identity read arrives.
	onUser      func()
	codeForm    url.Values
	tokenBody   string
	expiresIn   string
	userAuth    string
	deviceCodes []string
	// tokenStatus is the token answer's status, 200 when unset.
	tokenStatus int
	codes       atomic.Int32
	tokens      atomic.Int32
	closed      atomic.Int32
	mu          sync.Mutex
}

func newGrantEndpoint(t *testing.T, expiresIn, tokenBody string) *grantEndpoint {
	t.Helper()
	e := &grantEndpoint{expiresIn: expiresIn, tokenBody: tokenBody}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.serve(t, w, r)
	}))
	e.srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			e.closed.Add(1)
		}
	}
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	return e
}

func (e *grantEndpoint) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		t.Errorf("Setup: the request body is not a form: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/login/device/code":
		e.codes.Add(1)
		e.mu.Lock()
		e.codeForm = r.PostForm
		e.mu.Unlock()
		_, _ = io.WriteString(w, `{"device_code":"`+testDeviceCode+`","user_code":"ABCD-1234",`+
			`"verification_uri":"https://github.com/login/device","expires_in":`+e.expiresIn+`,"interval":5}`)
	case "/login/oauth/access_token":
		n := e.tokens.Add(1)
		e.mu.Lock()
		e.deviceCodes = append(e.deviceCodes, r.PostForm.Get("device_code"))
		hold, held := e.hold, e.held
		e.mu.Unlock()
		if n == 1 && hold != nil {
			close(held)
			<-hold
		}
		if e.tokenStatus != 0 {
			w.WriteHeader(e.tokenStatus)
		}
		_, _ = io.WriteString(w, e.tokenBody)
	case "/api/v3/user":
		e.mu.Lock()
		e.userAuth = r.Header.Get("Authorization")
		onUser := e.onUser
		e.mu.Unlock()
		if onUser != nil {
			onUser()
		}
		w.Header().Set("X-OAuth-Scopes", "repo, read:org, workflow")
		_, _ = io.WriteString(w, `{"login":"alice"}`)
	default:
		http.NotFound(w, r)
	}
}

// holdFirstToken makes the first token request wait until release is called.
func (e *grantEndpoint) holdFirstToken(t *testing.T) (held <-chan struct{}, release func()) {
	t.Helper()
	hold := make(chan struct{})
	e.mu.Lock()
	e.hold, e.held = hold, make(chan struct{})
	held = e.held
	e.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return held, release
}

func (e *grantEndpoint) record() connectionRecord {
	host := strings.TrimPrefix(e.srv.URL, "http://")
	return connectionRecord{
		ID: MakeID(KindGitHub, host), Kind: KindGitHub, Host: host, WebBaseURL: e.srv.URL,
		PrivateAddresses: true, PlaintextHTTP: true,
	}
}

const pendingAnswer = `{"error":"authorization_pending"}`

// slowDownAnswer makes every poll write the grant's interval, so under -race
// two polls of one grant that are not serialized are reported.
const slowDownAnswer = `{"error":"slow_down"}`

func startTestGrant(t *testing.T, r *grantRegistry, e *grantEndpoint) startedGrant {
	t.Helper()
	req, ok := marotteApp(forgeapi.FamilyGitHub)
	if !ok {
		t.Fatal("Setup: marotteApp(github) has no application")
	}
	rec := e.record()
	started, err := r.start(t.Context(), &rec, req)
	if err != nil {
		t.Fatalf("Setup: start() = %v", err)
	}
	return started
}

func (r *grantRegistry) held() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.grants)
}

// waitFor polls cond until it holds or two seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// skewedRegistry is a registry whose clock runs skew ahead of the real one,
// which the library's own expiry check still reads.
func skewedRegistry(skew *atomic.Int64) *grantRegistry {
	r := newGrantRegistry()
	r.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	return r
}

var grantIDShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

// A cancel pressed while a poll is in flight answers at once and wins over the
// approval that poll brings back, so the account is not connected.
func TestGrantRegistry_CancelDuringAPollWinsOverItsApproval(t *testing.T) {
	e := newGrantEndpoint(t, "900", approvedAnswer)
	r := newGrantRegistry()
	started := startTestGrant(t, r, e)
	held, release := e.holdFirstToken(t)
	errs := make(chan error, 1)
	go func() {
		_, err := r.poll(t.Context(), started.GrantID)
		errs <- err
	}()
	<-held
	cancelled := make(chan bool, 1)
	go func() { cancelled <- r.cancel(started.GrantID) }()
	select {
	case ok := <-cancelled:
		if !ok {
			t.Errorf("cancel() of a grant being polled = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Error("cancel() waited behind the in-flight poll")
	}
	release()
	if err := <-errs; !errors.Is(err, errGrantNotFound) {
		t.Errorf("poll() answered after a cancel = %v, want errGrantNotFound", err)
	}
}

// A start past the ceiling is refused before its device-code request, so a
// caller cannot grow the registry or the upstream traffic without bound.
func TestGrantRegistry_StartPastTheCeilingSendsNothing(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	r := newGrantRegistry()
	for range maxHeldGrants {
		startTestGrant(t, r, e)
	}
	req, _ := marotteApp(forgeapi.FamilyGitHub)
	rec := e.record()
	if _, err := r.start(t.Context(), &rec, req); !errors.Is(err, errTooManyGrants) {
		t.Errorf("start() with %d grants held = %v, want errTooManyGrants", maxHeldGrants, err)
	}
	if got := e.codes.Load(); got != maxHeldGrants {
		t.Errorf("device-code requests = %d, want %d: the refused start sent one", got, maxHeldGrants)
	}
	if got := r.held(); got != maxHeldGrants {
		t.Errorf("held() after a refused start = %d, want %d", got, maxHeldGrants)
	}
}

func TestGrantRegistry_StartAnswersAGrantIDAndKeepsTheDeviceCode(t *testing.T) {
	e := newGrantEndpoint(t, "900", pendingAnswer)
	r := newGrantRegistry()

	first := startTestGrant(t, r, e)
	second := startTestGrant(t, r, e)

	if !grantIDShape.MatchString(first.GrantID) || first.GrantID == second.GrantID {
		t.Errorf("start() grant ids = %q, %q; want two distinct 16-byte hex ids", first.GrantID, second.GrantID)
	}
	if first.UserCode != "ABCD-1234" || first.VerificationURI != "https://github.com/login/device" ||
		first.Interval != 5*time.Second || time.Until(first.Expires) < 14*time.Minute {
		t.Errorf("start() = %+v, want the code answer's user code, URI, interval and expiry", first)
	}
	if shown := fmt.Sprintf("%+v", first); strings.Contains(shown, testDeviceCode) {
		t.Errorf("start() = %s, which carries the device code", shown)
	}
	e.mu.Lock()
	form := e.codeForm
	e.mu.Unlock()
	if form.Get("client_id") != "Ov23li9ja8ak5hoZACXH" || form.Get("scope") != "repo read:org workflow" {
		t.Errorf("the device-code request named client %q scopes %q, want Marotte's application and repo read:org workflow",
			form.Get("client_id"), form.Get("scope"))
	}

	if _, err := r.poll(t.Context(), first.GrantID); err != nil {
		t.Fatalf("poll(%s) = %v, want pending", first.GrantID, err)
	}
	e.mu.Lock()
	sent := e.deviceCodes
	e.mu.Unlock()
	if len(sent) != 1 || sent[0] != testDeviceCode {
		t.Errorf("poll by grant id sent device codes %q, want the one the server held", sent)
	}
}

// secondPollEntered makes r's clock signal the second poll from now on: a
// poll reads the clock in its sweep, before it takes the grant's lock.
func secondPollEntered(r *grantRegistry) <-chan struct{} {
	var calls atomic.Int32
	entered := make(chan struct{})
	r.now = func() time.Time {
		if calls.Add(1) == 2 {
			close(entered)
		}
		return time.Now()
	}
	return entered
}

func TestGrantRegistry_ConcurrentPollsAreSerialized(t *testing.T) {
	e := newGrantEndpoint(t, "900", slowDownAnswer)
	r := newGrantRegistry()
	g := startTestGrant(t, r, e)
	held, release := e.holdFirstToken(t)
	entered := secondPollEntered(r)

	errs := make(chan error, 2)
	go func() { _, err := r.poll(t.Context(), g.GrantID); errs <- err }()
	<-held
	go func() { _, err := r.poll(t.Context(), g.GrantID); errs <- err }()
	<-entered
	release()
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("poll() = %v, want pending", err)
		}
	}

	if got := e.tokens.Load(); got != 2 {
		t.Errorf("two polls sent %d token requests, want 2", got)
	}
}

// The sweep closes an expired grant while a poll of it is in flight: Close
// waits for that poll and sends nothing of its own (ADR-0099).
func TestGrantRegistry_SweepOverlappingAPollWaitsForIt(t *testing.T) {
	e := newGrantEndpoint(t, "900", slowDownAnswer)
	var skew atomic.Int64
	r := skewedRegistry(&skew)
	g := startTestGrant(t, r, e)
	held, release := e.holdFirstToken(t)

	errs := make(chan error, 1)
	go func() { _, err := r.poll(t.Context(), g.GrantID); errs <- err }()
	<-held
	// The registry now reads the grant as past its expiry and grace while the
	// library does not, so the sweep takes a grant whose poll is in flight.
	skew.Store(int64(time.Hour))
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		if _, err := r.poll(t.Context(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, errGrantNotFound) {
			t.Errorf("poll(unknown) = %v, want errGrantNotFound", err)
		}
	}()
	waitFor(t, "the sweep to take the grant", func() bool { return r.held() == 0 })
	release()
	if err := <-errs; err != nil {
		t.Errorf("poll() = %v, want pending", err)
	}
	<-swept

	if got := e.tokens.Load(); got != 1 {
		t.Errorf("the poll and the sweep sent %d token requests, want 1 (the sweep's Close sends nothing)", got)
	}
	waitFor(t, "the swept grant's endpoint to be released", func() bool { return e.closed.Load() > 0 })
}

func TestGrantRegistry_ExpiredGrantIsSweptAndEnded(t *testing.T) {
	t.Parallel()
	e := newGrantEndpoint(t, "1", pendingAnswer)
	var skew atomic.Int64
	r := skewedRegistry(&skew)
	g := startTestGrant(t, r, e)
	time.Sleep(time.Until(g.Expires) + 20*time.Millisecond)

	skew.Store(int64(grantSweepGrace + time.Second))
	if _, err := r.poll(t.Context(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, errGrantNotFound) {
		t.Fatalf("poll(unknown) = %v, want errGrantNotFound", err)
	}

	if r.held() != 0 {
		t.Errorf("the registry holds %d grants after the sweep, want 0", r.held())
	}
	waitFor(t, "the swept grant's endpoint to be released", func() bool { return e.closed.Load() > 0 })
	if got := e.tokens.Load(); got != 0 {
		t.Errorf("sweeping an expired grant sent %d token requests, want none", got)
	}
	if _, err := r.poll(t.Context(), g.GrantID); !errors.Is(err, errGrantNotFound) {
		t.Errorf("poll(swept) = %v, want errGrantNotFound", err)
	}
}

func TestGrantRegistry_PollWithinTheGraceAnswersTheExpiry(t *testing.T) {
	t.Parallel()
	e := newGrantEndpoint(t, "1", pendingAnswer)
	r := newGrantRegistry()
	g := startTestGrant(t, r, e)
	time.Sleep(time.Until(g.Expires) + 20*time.Millisecond)

	_, err := r.poll(t.Context(), g.GrantID)
	if ferr, ok := errors.AsType[*forgeapi.Error](err); !ok || ferr.Code != forgeapi.CodeGrantExpired {
		t.Errorf("poll() just past the expiry = %v, want %q", err, forgeapi.CodeGrantExpired)
	}
	if got := e.tokens.Load(); got != 0 {
		t.Errorf("polling an expired grant sent %d token requests, want none", got)
	}
}

func TestGrantRegistry_UnknownGrantIDIsNotFound(t *testing.T) {
	r := newGrantRegistry()

	if _, err := r.poll(t.Context(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, errGrantNotFound) {
		t.Errorf("poll(unknown) = %v, want errGrantNotFound", err)
	}
	if r.cancel("0123456789abcdef0123456789abcdef") {
		t.Error("cancel(unknown) = true, want false")
	}
}

func TestGrantRegistry_ApprovedGrantIsAnsweredOnce(t *testing.T) {
	e := newGrantEndpoint(t, "900", `{"access_token":"token-new","token_type":"bearer","scope":"repo,read:org,workflow",`+
		`"expires_in":28800,"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`)
	r := newGrantRegistry()
	g := startTestGrant(t, r, e)

	got, err := r.poll(t.Context(), g.GrantID)
	if err != nil || !got.Done {
		t.Fatalf("poll() of an approved grant = %+v, %v; want done", got, err)
	}
	if got.Record.Token != "token-new" || got.Record.Kind != forgeapi.CredKindRotatingOAuth ||
		got.Record.ClientID != "Ov23li9ja8ak5hoZACXH" {
		t.Errorf("poll() record = %+v, want the rotating credential for Marotte's application", got.Record)
	}
	if want := e.record(); !reflect.DeepEqual(got.Conn, want) {
		t.Errorf("poll() connection = %+v, want the one the grant was started for %+v", got.Conn, want)
	}
	if _, err := r.poll(t.Context(), g.GrantID); !errors.Is(err, errGrantNotFound) {
		t.Errorf("a second poll() of an answered grant = %v, want errGrantNotFound", err)
	}
	if n := e.tokens.Load(); n != 1 {
		t.Errorf("the two polls sent %d token requests, want 1", n)
	}
}
