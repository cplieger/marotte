package applogin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
)

const (
	testOrigin   = "https://marotte.example"
	testUsername = "owner"
	testPassword = "correct horse battery staple 42"
)

// eventLog records OnEvent's events.
type eventLog struct {
	evs []account.Event
	mu  sync.Mutex
}

func (l *eventLog) add(e account.Event) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

func (l *eventLog) of(kind account.EventKind) []account.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []account.Event
	for _, e := range l.evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// harness is the production constructor chain over a test disk.
type harness struct {
	t       testing.TB
	attempt *Attempt
	store   *Store
	disk    *testDisk
	svc     *account.Service
	authn   *access.Authenticator
	origin  *Origin
	events  *eventLog
	agent   access.CredentialVerifier
	agentKy string
	edits   []func(*Options)
}

// newHarness builds over a fresh test disk; edit changes the options before Build.
func newHarness(t testing.TB, edit ...func(*Options)) *harness {
	t.Helper()
	return buildHarness(t, newTestDisk(t), edit...)
}

func buildHarness(t testing.TB, d *testDisk, edit ...func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, disk: d, events: &eventLog{}, edits: edit}
	agent, err := NewAgentKeyVerifier(func(_, v string) error { h.agentKy = v; return nil })
	if err != nil {
		t.Fatalf("NewAgentKeyVerifier: %v", err)
	}
	h.agent = agent
	opts := Options{
		Disk: d, Path: d.path, Proxies: ParseProxies(""), Origin: parseOrigin(testOrigin, nil), Agent: agent,
		OnEvent: h.events.add, MemoryLimit: 1 << 30,
	}
	for _, e := range edit {
		e(&opts)
	}
	h.origin = opts.Origin
	a, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		a.stop()
		<-a.Stopped()
	})
	h.attempt, h.store, h.svc, h.authn = a, a.Store(), a.Service(), a.Authenticator()
	return h
}

// reopen stops the harness's attempt and builds a new one over the same file, with the same
// option edits and then extra.
func (h *harness) reopen(extra ...func(*Options)) *harness {
	h.t.Helper()
	h.attempt.stop()
	<-h.attempt.Stopped()
	return buildHarness(h.t, h.disk, append(slices.Clone(h.edits), extra...)...)
}

// crashCopy is an abrupt restart: a new attempt over a copy of the file as it is now, the old
// attempt never stopped.
func (h *harness) crashCopy() *harness {
	h.t.Helper()
	data, err := os.ReadFile(h.disk.path)
	if err != nil {
		h.t.Fatalf("crash copy: %v", err)
	}
	d := newTestDisk(h.t)
	if err := os.WriteFile(d.path, data, 0o600); err != nil {
		h.t.Fatalf("crash copy: %v", err)
	}
	return buildHarness(h.t, d, h.edits...)
}

// withOrigin builds over the canonical origin raw.
func withOrigin(raw string) func(*Options) {
	return func(o *Options) { o.Origin = parseOrigin(raw, nil) }
}

// fixture writes fn's changes straight onto the store, bypassing the SPI's rules and the guard.
func (h *harness) fixture(fn func(tx *txView) error) {
	h.t.Helper()
	if err := h.store.write(context.Background(), fn); err != nil {
		h.t.Fatalf("fixture write: %v", err)
	}
}

// setPolicy is a fixture write of the policy record, its generation moved by one.
func (h *harness) setPolicy(edit func(p *storePolicy)) {
	h.t.Helper()
	h.fixture(func(tx *txView) error {
		p := tx.s.policy.clone()
		edit(&p)
		p.Generation = tx.s.policy.Generation + 1
		tx.setPolicy(&p)
		return nil
	})
}

// owner is the stored owner.
func (h *harness) owner() *access.User {
	h.t.Helper()
	u, found, err := h.store.UserByUsername(context.Background(), testUsername)
	if err != nil || !found {
		h.t.Fatalf("the owner: found %t, %v", found, err)
	}
	return u
}

func plainCtx(ctx context.Context) context.Context {
	return withFacts(ctx, requestFacts{plain: true})
}

func httpsCtx(ctx context.Context) context.Context {
	return withFacts(ctx, requestFacts{plain: false})
}

var testClient = account.Client{
	UserAgent: "test-browser",
	IP:        netip.MustParseAddr("192.0.2.10"), Peer: netip.MustParseAddr("192.0.2.10"),
}

// setupOwner completes setup by password over a plain request and returns the owner's session.
func (h *harness) setupOwner(ctx context.Context) account.SignedIn {
	h.t.Helper()
	tok, err := h.svc.IssueSetupToken(ctx, account.OutOfBand("boot"), time.Hour)
	if err != nil {
		h.t.Fatalf("IssueSetupToken: %v", err)
	}
	si, err := h.svc.SetupWithPassword(plainCtx(ctx), testClient, account.SetupPassword{
		Token: tok, Password: access.NewSecret(testPassword), Username: testUsername,
	})
	if err != nil {
		h.t.Fatalf("SetupWithPassword: %v", err)
	}
	return si
}

// principal authenticates a plain request carrying token's session cookie.
func (h *harness) principal(token access.Secret) access.Principal {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token.Reveal()})
	p, err := h.authn.Authenticate(r)
	if err != nil {
		h.t.Fatalf("Authenticate: %v", err)
	}
	return p
}

// recoveryPrincipal authenticates a recovery session's cookie on a route that admits it.
func (h *harness) recoveryPrincipal(token access.Secret) access.Principal {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(access.AllowRecoverySession(r.Context()))
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token.Reveal()})
	p, err := h.authn.Authenticate(r)
	if err != nil {
		h.t.Fatalf("Authenticate: %v", err)
	}
	return p
}

// signIn signs the owner in by password over a plain request.
func (h *harness) signIn(ctx context.Context) account.SignedIn {
	h.t.Helper()
	si, err := h.svc.PasswordLogin(plainCtx(ctx), testClient, account.Credentials{
		Username: testUsername, Password: access.NewSecret(testPassword),
	})
	if err != nil {
		h.t.Fatalf("PasswordLogin: %v", err)
	}
	return si
}

// newTestSession is a fresh session record for userID, created at at.
func newTestSession(userID int64, at time.Time) *access.Session {
	_, hash := access.GenerateSessionToken()
	return &access.Session{
		ID: randomID(), TokenHash: hash, UserID: userID,
		CreatedAt: at, LastActivity: at, AuthTime: at, AuthMethod: access.MethodPassword, Kind: access.SessionFull,
	}
}

var idSeq struct {
	sync.Mutex
	n int
}

func randomID() string {
	idSeq.Lock()
	defer idSeq.Unlock()
	idSeq.n++
	return "sess-" + time.Now().Format("150405.000000000") + "-" + itoa(idSeq.n)
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{digits[n%10]}, b...)
		n /= 10
	}
	return string(b)
}

// newSeamIdP starts a test IdP at the https issuer url and routes the OIDC client's dials for its
// host to it, for the test's life.
func newSeamIdP(t *testing.T, url string, opts accesstest.IdPOptions) *accesstest.IdP {
	t.Helper()
	opts.URL = url
	idp := accesstest.NewIdP(t, opts)
	routeSeam(t, originHost(url), &oidcDialSeam{addr: idp.Addr().String(), roots: idp.RootCAs()})
	return idp
}

// routeSeam sends the OIDC client's dials for host through seam, for the test's life.
func routeSeam(t *testing.T, host string, seam *oidcDialSeam) {
	t.Helper()
	host = canonicalHost(host)
	oidcSeams.Store(host, seam)
	t.Cleanup(func() { oidcSeams.Delete(host) })
}

// providerOf is the saved record of idp, its four origins its issuer's.
func providerOf(idp *accesstest.IdP) storeProvider {
	o := strings.TrimSuffix(idp.Issuer(), "/")
	return storeProvider{
		Issuer: idp.Issuer(), ClientID: idp.ClientID(), ClientSecret: access.NewSecret(idp.ClientSecret()),
		Origins: ProviderOrigins{Issuer: o, Authorization: o, Token: o, JWKS: o}, DisplayName: "Test IdP",
	}
}

// builtProvider builds rec's provider through marotte's own client.
func builtProvider(t *testing.T, rec storeProvider, origin *Origin) *oidc.Provider {
	t.Helper()
	p, err := oidc.NewProvider(t.Context(), ProviderConfig(rec, origin, access.NewEphemeralSealer()))
	if err != nil {
		t.Fatalf("oidc.NewProvider(%s): %v", rec.Issuer, err)
	}
	return p
}

// accountTestActor is the out-of-band actor own writes take in store-level tests.
var accountTestActor = account.OutOfBand("test")
