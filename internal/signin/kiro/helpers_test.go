package kiro

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/signin"
)

// call is one request a fakeAWS served; host is the one pointAt named, path the rest.
type call struct {
	query  url.Values
	host   string
	path   string
	auth   string
	target string
	body   string
}

type scripted struct {
	body   string
	status int
}

// fakeAWS serves SSO-OIDC (/client/register, /device_authorization, /token) under hostOIDC and the
// Kiro control plane (POST / for profiles, GET /Get-Usage-Limits for the email) under
// hostManagement, and answers 404 to a route asked of the other host.
type fakeAWS struct {
	srv *httptest.Server
	// deviceAnswers is consumed one per device-grant poll; when empty the grant succeeds.
	deviceAnswers []scripted
	refresh       scripted
	profiles      scripted
	email         scripted
	emailGate     chan struct{}
	emailGone     chan struct{}
	account       string
	ttl           string
	calls         []call
	mu            sync.Mutex
}

func newFakeAWS(t *testing.T, account string) *fakeAWS {
	t.Helper()
	f := &fakeAWS{
		account:  account,
		profiles: scripted{status: 200, body: `{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:111:profile/` + account + `","profileName":"` + account + `-profile"}]}`},
		email:    scripted{status: 200, body: `{"userInfo":{"email":"` + account + `@example.com"}}`},
		refresh:  scripted{status: 200, body: `{"accessToken":"` + account + `-access-2","expiresIn":3600}`},
		ttl:      "3600",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAWS) set(fn func(f *fakeAWS)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAWS) served() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeAWS) servedPath(path string) []call {
	var out []call
	for _, c := range f.served() {
		if c.path == path {
			out = append(out, c)
		}
	}
	return out
}

// The two hosts pointAt maps a region to, as path prefixes on one fakeAWS.
const (
	hostOIDC       = "oidc"
	hostManagement = "management"
)

func (f *fakeAWS) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	host, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	path = "/" + path
	f.mu.Lock()
	f.calls = append(f.calls, call{host: host, path: path, query: r.URL.Query(), auth: r.Header.Get("Authorization"), target: r.Header.Get("X-Amz-Target"), body: string(body)})
	var answer scripted
	gate, gone := f.emailGate, f.emailGone
	oidc, management := host == hostOIDC, host == hostManagement
	switch {
	case oidc && path == "/client/register":
		answer = scripted{status: 200, body: `{"clientId":"client-1","clientSecret":"client-secret-1","clientSecretExpiresAt":4102444800}`}
	case oidc && path == "/device_authorization":
		answer = scripted{status: 200, body: `{"deviceCode":"device-1","userCode":"ABCD-EFGH","verificationUri":"https://device.sso.example/","verificationUriComplete":"https://device.sso.example/?user_code=ABCD-EFGH","expiresIn":600,"interval":1}`}
	case oidc && path == "/token" && strings.Contains(string(body), refreshGrant):
		answer = f.refresh
	case oidc && path == "/token":
		answer = scripted{status: 200, body: `{"accessToken":"` + f.account + `-access","refreshToken":"` + f.account + `-refresh","expiresIn":` + f.ttl + `}`}
		if len(f.deviceAnswers) > 0 {
			answer, f.deviceAnswers = f.deviceAnswers[0], f.deviceAnswers[1:]
		}
	case management && path == "/" && r.Method == http.MethodPost:
		answer = f.profiles
	case management && path == "/Get-Usage-Limits":
		answer = f.email
	default:
		answer = scripted{status: 404}
	}
	f.mu.Unlock()
	if management && path == "/Get-Usage-Limits" && gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			if gone != nil {
				close(gone)
			}
			return
		}
	}
	w.WriteHeader(answer.status)
	_, _ = io.WriteString(w, answer.body)
}

// testClock is a settable time source for the backend.
type testClock struct {
	t  time.Time
	mu sync.Mutex
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func backendFor(f *fakeAWS) *Backend {
	b := New()
	pointAt(b, f.srv.URL)
	b.pollUnit = time.Millisecond
	b.emailTimeout = 2 * time.Second
	return b
}

// pointAt sends every request b makes to base, under hostOIDC or hostManagement, once the
// partition table has accepted the region.
func pointAt(b *Backend, base string) {
	b.resolve = func(region string) (endpoints, error) {
		if _, err := endpointsFor(region); err != nil {
			return endpoints{}, err
		}
		return endpoints{oidc: base + "/" + hostOIDC, management: base + "/" + hostManagement}, nil
	}
}

// signInDirect runs Begin and Poll, as the vault does.
func signInDirect(t *testing.T, b *Backend, p signin.FlowParams) (record, error) {
	t.Helper()
	d, err := b.Begin(t.Context(), p)
	if err != nil {
		return record{}, err
	}
	c, err := b.Poll(t.Context(), d)
	if err != nil {
		return record{}, err
	}
	return decodeRecord(c)
}

var idcParams = signin.FlowParams{Flow: signin.FlowDevice, StartURL: "https://d-1234.awsapps.com/start", Region: "eu-west-1"}

func testRecord(p provider, arn string) record {
	return record{
		StartURL: builderIDStartURL, Region: "us-east-1", ClientID: "client", ClientSecret: "client-secret",
		RefreshToken: "refresh-1", AccessToken: "access-1", ExpiresAt: time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC),
		ProfileArn: arn, Provider: p,
	}
}

func credOf(t *testing.T, r record) signin.Credential {
	t.Helper()
	c, err := r.credential()
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	return c
}

// vaultHarness is a real vault over the kiro backend and a fakeAWS per account.
type vaultHarness struct {
	t      *testing.T
	v      *signin.Vault
	b      *Backend
	aws    *fakeAWS
	cancel context.CancelFunc
}

func newVaultHarness(t *testing.T, account string) *vaultHarness {
	t.Helper()
	h := &vaultHarness{t: t, aws: newFakeAWS(t, account)}
	h.b = backendFor(h.aws)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.v = signin.Open(ctx, t.TempDir(), h.b)
	t.Cleanup(func() {
		cancel()
		h.v.Wait()
	})
	return h
}

// signIn signs in as account through p, the fakeAWS issuing that account's tokens and profile.
func (h *vaultHarness) signIn(account string, p signin.FlowParams, profiles scripted) signin.Status {
	h.t.Helper()
	h.aws.set(func(f *fakeAWS) {
		f.account = account
		f.profiles = profiles
	})
	if _, err := h.v.StartSignIn(h.t.Context(), ID, p); err != nil {
		h.t.Fatalf("StartSignIn: %v", err)
	}
	return h.settle()
}

// settle waits until the sign-in attempt is no longer pending.
func (h *vaultHarness) settle() signin.Status {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.v.Status(ID); st.SignIn.State != signin.AttemptPending {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatal("the sign-in never settled")
	return signin.Status{}
}

func profilesOf(account string) scripted {
	return scripted{status: 200, body: `{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:111:profile/` + account + `","profileName":"p"}]}`}
}

var noProfiles = scripted{status: 200, body: `{"profiles":[]}`}

func (h *vaultHarness) answer() (answer, bool) {
	h.t.Helper()
	res, rerr := h.v.Answer(h.t.Context(), ID)
	if rerr != nil {
		return answer{}, false
	}
	var a answer
	if err := json.Unmarshal(res, &a); err != nil {
		h.t.Fatalf("answer shape: %v", err)
	}
	return a, true
}

func builderID() signin.FlowParams { return signin.FlowParams{Flow: signin.FlowDevice} }
