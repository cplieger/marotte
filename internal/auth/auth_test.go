package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/runesafe/v3"
)

// fakeBackend is a device-flow backend whose Poll blocks until the test approves or fails it.
type fakeBackend struct {
	outcomes chan pollOutcome
}

type pollOutcome struct {
	err     error
	account string
}

var _ signin.Backend = (*fakeBackend)(nil)

func newFakeBackend() *fakeBackend { return &fakeBackend{outcomes: make(chan pollOutcome, 4)} }

func (*fakeBackend) Declaration() signin.Declaration {
	return signin.Declaration{Backend: "fake", HostCallback: "_test/auth/token", Flows: []signin.FlowKind{signin.FlowDevice}}
}

func (*fakeBackend) Begin(context.Context, signin.FlowParams) (signin.DeviceStart, error) {
	return signin.DeviceStart{
		VerificationURL: "https://device.example/activate", UserCode: "ABCD-EFGH", ExpiresIn: 10 * time.Minute,
	}, nil
}

func (f *fakeBackend) Poll(ctx context.Context, _ signin.DeviceStart) (signin.Credential, error) {
	select {
	case o := <-f.outcomes:
		if o.err != nil {
			return signin.Credential{}, o.err
		}
		data, _ := json.Marshal(map[string]string{"account": o.account})
		return signin.Credential{Data: data}, nil
	case <-ctx.Done():
		return signin.Credential{}, ctx.Err()
	}
}

func (*fakeBackend) Refresh(_ context.Context, c signin.Credential) (signin.Credential, error) {
	return c, nil
}
func (*fakeBackend) Validate(signin.Credential) error { return nil }

func (*fakeBackend) Label(c signin.Credential) signin.Identity {
	var r struct {
		Account string `json:"account"`
	}
	_ = json.Unmarshal(c.Data, &r)
	return signin.Identity{Method: "Logged in with Builder ID", AccountType: "BuilderId", Email: r.Account}
}

func (*fakeBackend) LiveSwitchable(signin.Credential, signin.Credential) bool { return true }
func (*fakeBackend) Fresh(signin.Credential, time.Time) bool                  { return true }
func (*fakeBackend) Servable(signin.Credential, time.Time) bool               { return true }

func (*fakeBackend) Result(signin.Credential, time.Time) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (*fakeBackend) Refusal(signin.Refusal) *marotte.RPCError {
	return &marotte.RPCError{Code: -32000, Message: "not signed in"}
}

// harness is a vault over a temp config dir and the handler over it.
type harness struct {
	t       *testing.T
	backend *fakeBackend
	vault   *signin.Vault
	h       *Handler
	dir     string
}

func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	b := newFakeBackend()
	v := signin.Open(ctx, dir, b)
	t.Cleanup(func() {
		cancel()
		v.Wait()
	})
	h := NewHandler(v, "fake", opts...)
	h.managedSettingsPath = filepath.Join(dir, "no-managed-settings.json")
	return &harness{t: t, backend: b, vault: v, h: h, dir: dir}
}

func (hs *harness) do(method, path, body string) *httptest.ResponseRecorder {
	hs.t.Helper()
	mux := http.NewServeMux()
	hs.h.RegisterRoutes(mux)
	req := httptest.NewRequestWithContext(hs.t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (hs *harness) whoami() WhoamiResponse {
	hs.t.Helper()
	rec := hs.do(http.MethodGet, "/api/whoami", "")
	var w WhoamiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
		hs.t.Fatalf("whoami body %q: %v", rec.Body.String(), err)
	}
	return w
}

// login starts a sign-in and returns its attempt.
func (hs *harness) login() LoginResponse {
	hs.t.Helper()
	rec := hs.do(http.MethodPost, "/api/login", "")
	if rec.Code != http.StatusOK {
		hs.t.Fatalf("POST /api/login = %d %s", rec.Code, rec.Body.String())
	}
	var lr LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		hs.t.Fatalf("login body: %v", err)
	}
	return lr
}

// awaitAttempt polls whoami until attempt left pending, the client's own stop rule.
func (hs *harness) awaitAttempt(attempt uint64) WhoamiResponse {
	hs.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := hs.whoami()
		if w.LoginAttempt == attempt && w.LoginState != string(signin.AttemptPending) {
			return w
		}
		time.Sleep(10 * time.Millisecond)
	}
	hs.t.Fatalf("attempt %d never left pending", attempt)
	return WhoamiResponse{}
}

func (hs *harness) signIn(account string) {
	hs.t.Helper()
	lr := hs.login()
	hs.backend.outcomes <- pollOutcome{account: account}
	if w := hs.awaitAttempt(lr.Attempt); w.State != WhoamiSignedIn || w.Email != account {
		hs.t.Fatalf("after signing in %s: whoami = %+v", account, w)
	}
}

func TestLogin_AnswersTheURLAndCodeShape(t *testing.T) {
	hs := newHarness(t)
	lr := hs.login()
	if lr.URL != "https://device.example/activate" || lr.Code != "ABCD-EFGH" || lr.Attempt == 0 {
		t.Errorf("login = %+v, want the device URL, the code and a numbered attempt", lr)
	}
	if rec := hs.do(http.MethodPost, "/api/login", ""); rec.Code != http.StatusConflict {
		t.Errorf("a second login while one polls = %d, want 409", rec.Code)
	}
}

func TestLogin_ClientDisconnectAfterTheResponseStillCompletes(t *testing.T) {
	hs := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	mux := http.NewServeMux()
	hs.h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/login", nil))
	var lr LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatalf("login: %v (%s)", err, rec.Body.String())
	}
	cancel()
	hs.backend.outcomes <- pollOutcome{account: "a@example.com"}
	if w := hs.awaitAttempt(lr.Attempt); w.State != WhoamiSignedIn {
		t.Errorf("after the request ended, the approval = %+v, want signed in", w)
	}
}

func TestWhoami_FromTheVault(t *testing.T) {
	hs := newHarness(t)
	if w := hs.whoami(); w.State != WhoamiSignedOut {
		t.Errorf("an empty vault = %+v, want signed_out", w)
	}
	hs.signIn("a@example.com")
	w := hs.whoami()
	if w.Auth != "Logged in with Builder ID" || w.AccountType != "BuilderId" {
		t.Errorf("whoami labels = %+v, want the backend's", w)
	}
}

func TestLogout_ClearsTheVault(t *testing.T) {
	hs := newHarness(t)
	hs.signIn("a@example.com")
	if rec := hs.do(http.MethodPost, "/api/logout", ""); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if w := hs.whoami(); w.State != WhoamiSignedOut {
		t.Errorf("after logout whoami = %+v, want signed_out", w)
	}
}

func TestLogout_RemoveFailureIs500AndKeepsTheAccount(t *testing.T) {
	hs := newHarness(t)
	hs.signIn("a@example.com")
	// A non-empty directory where the file was: the removal fails with ENOTEMPTY, root or not.
	path := filepath.Join(hs.dir, "signin.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(path, "held"), 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	rec := hs.do(http.MethodPost, "/api/logout", "")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "still signed in") {
		t.Errorf("logout over an unremovable file = %d %s, want 500 naming the kept sign-in", rec.Code, rec.Body.String())
	}
	if w := hs.whoami(); w.State != WhoamiSignedIn {
		t.Errorf("after a failed logout whoami = %+v, want still signed in", w)
	}
}

func TestLoginControls_StillGateTheDoors(t *testing.T) {
	hs := newHarness(t)
	hs.h.managedSettingsPath = managedFile(t, `{"rules":[{"capability":"signin_method","effect":"deny","match":["builder_id","google","github"]}]}`)
	if rec := hs.do(http.MethodPost, "/api/login", ""); rec.Code != http.StatusForbidden {
		t.Errorf("a denied Builder ID login = %d, want 403", rec.Code)
	}
	body := `{"provider":"https://d-1234.awsapps.com/start","region":"eu-west-1"}`
	if rec := hs.do(http.MethodPost, "/api/login", body); rec.Code != http.StatusOK {
		t.Errorf("a permitted organization login = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestWhoami_CarriesLoginErrorAfterAFailedSignIn(t *testing.T) {
	failure := &signin.FailureError{Text: "This account has no Kiro profile marotte can use"}
	t.Run("signed out", func(t *testing.T) {
		hs := newHarness(t)
		lr := hs.login()
		hs.backend.outcomes <- pollOutcome{err: failure}
		w := hs.awaitAttempt(lr.Attempt)
		if w.State != WhoamiSignedOut || w.LoginState != string(signin.AttemptFailed) || w.LoginError != failure.Text {
			t.Errorf("a failed first sign-in = %+v, want signed_out with the failure", w)
		}
	})
	t.Run("signed in after a failed switch", func(t *testing.T) {
		hs := newHarness(t)
		hs.signIn("a@example.com")
		lr := hs.login()
		// The old account stays signed in while the switch polls: a poll must not read that as the switch's success.
		if w := hs.whoami(); w.State != WhoamiSignedIn || w.LoginAttempt != lr.Attempt || w.LoginState != string(signin.AttemptPending) {
			t.Fatalf("during the switch whoami = %+v, want the old account and this attempt pending", w)
		}
		hs.backend.outcomes <- pollOutcome{err: failure}
		w := hs.awaitAttempt(lr.Attempt)
		if w.State != WhoamiSignedIn || w.Email != "a@example.com" || w.LoginError != failure.Text {
			t.Errorf("a failed switch = %+v, want the old account kept and the failure named", w)
		}
	})
	t.Run("a switch that commits is told from the old account", func(t *testing.T) {
		hs := newHarness(t)
		hs.signIn("a@example.com")
		lr := hs.login()
		hs.backend.outcomes <- pollOutcome{account: "b@example.com"}
		if w := hs.awaitAttempt(lr.Attempt); w.LoginState != string(signin.AttemptSignedIn) || w.Email != "b@example.com" {
			t.Errorf("a committed switch = %+v, want the new account", w)
		}
	})
}

func TestWhoami_LoginErrorClearsOnTheNextStart(t *testing.T) {
	hs := newHarness(t)
	lr := hs.login()
	hs.backend.outcomes <- pollOutcome{err: &signin.FailureError{Text: "denied"}}
	hs.awaitAttempt(lr.Attempt)
	next := hs.login()
	w := hs.whoami()
	if w.LoginError != "" || w.LoginAttempt != next.Attempt || w.LoginState != string(signin.AttemptPending) {
		t.Errorf("after a new start whoami = %+v, want no error and the new attempt pending", w)
	}
}

func TestAPIKey_RefusesLoginAndLogoutAndKeepsTheStoredAccount(t *testing.T) {
	hs := newHarness(t)
	hs.signIn("a@example.com")
	keyed := NewHandler(hs.vault, "fake", WithAPIKey(true))
	keyed.managedSettingsPath = hs.h.managedSettingsPath
	hk := &harness{t: t, backend: hs.backend, vault: hs.vault, h: keyed, dir: hs.dir}
	if w := hk.whoami(); w.State != WhoamiSignedIn || w.Auth != textAPIKeyAuth || !w.APIKey {
		t.Errorf("with the key whoami = %+v, want the API key named and flagged", w)
	}
	for _, path := range []string{"/api/login", "/api/logout"} {
		if rec := hk.do(http.MethodPost, path, ""); rec.Code != http.StatusConflict {
			t.Errorf("POST %s with the key = %d, want 409", path, rec.Code)
		}
	}
	// Removing the key (a handler without it) reveals the account the vault kept.
	if w := hs.whoami(); w.State != WhoamiSignedIn || w.Email != "a@example.com" || w.APIKey {
		t.Errorf("without the key whoami = %+v, want the stored account, not flagged", w)
	}
}

func TestWhoami_RejectsNonGET(t *testing.T) {
	hs := newHarness(t)
	if rec := hs.do(http.MethodPost, "/api/whoami", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/whoami = %d, want 405", rec.Code)
	}
}

func TestHandleLogin_RejectsBadBodies(t *testing.T) {
	hs := newHarness(t)
	for name, body := range map[string]string{
		"malformed":     "{",
		"http provider": `{"provider":"http://evil.com"}`,
		"flag region":   `{"region":"--help"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if rec := hs.do(http.MethodPost, "/api/login", body); rec.Code != http.StatusBadRequest {
				t.Errorf("login with %s = %d, want 400", name, rec.Code)
			}
		})
	}
	big := `{"provider":"` + strings.Repeat("a", 2<<20) + `"}`
	if rec := hs.do(http.MethodPost, "/api/login", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversize body = %d, want 413", rec.Code)
	}
}

func TestSignInErrorText_UsesTheBackendsWords(t *testing.T) {
	if got := signInErrorText(&signin.FailureError{Text: "no profile"}); got != "no profile" {
		t.Errorf("a backend failure = %q, want its text", got)
	}
	if got := signInErrorText(errors.New("dial tcp: refused")); strings.Contains(got, "dial") {
		t.Errorf("an internal error leaked into the user text: %q", got)
	}
}

func TestValidateProvider(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is allowed", "", false},
		{"valid https URL", "https://view.awsapps.com/start", false},
		{"http rejected", "http://evil.com", true},
		{"bare host rejected", "evil.com", true},
		{"flag smuggling rejected", "--identity-provider", true},
		{"over length rejected", "https://example.com/" + strings.Repeat("a", maxProviderLen), true},
		{"userinfo rejected", "https://u:[email protected]/", true},
		{"empty-user userinfo rejected", "https://@evil.com/", true},
		{"boundary length exactly max accepted", "https://example.com/" + strings.Repeat("a", maxProviderLen-len("https://example.com/")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProvider(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateProvider(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateProvider(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestValidateRegion(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is allowed", "", false},
		{"us-east-1", "us-east-1", false},
		{"eu-west-2", "eu-west-2", false},
		{"ap-southeast-1", "ap-southeast-1", false},
		{"cn-north-1 (China)", "cn-north-1", false},
		{"us-gov-west-1 (GovCloud)", "us-gov-west-1", false},
		{"us-iso-east-1 (ISO)", "us-iso-east-1", false},
		{"us-isob-east-1 (ISO-B)", "us-isob-east-1", false},
		{"eu-isoe-west-1 (ISO-E)", "eu-isoe-west-1", false},
		{"flag smuggling rejected", "--help", true},
		{"uppercase rejected", "US-EAST-1", true},
		{"over length rejected", strings.Repeat("a", maxRegionLen+1), true},
		{"empty interior segment rejected", "us--east-1", true},
		{"boundary length exactly max accepted", "aa-" + strings.Repeat("b", maxRegionLen-5) + "-1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRegion(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateRegion(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateRegion(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

// The account row's strings are upstream text (the identity provider's, through the backend's label); these pin
// that whoami bounds each to one line.
func TestWhoamiFrom_SanitizesAndBoundsEveryIdentityString(t *testing.T) {
	const rlo = "\u202e" // RIGHT-TO-LEFT OVERRIDE

	// A 60 KiB email and a direction override in every field, which the output cap alone allowed.
	huge := strings.Repeat("a", 60*1024) + "@example.com"
	got := whoamiFrom(&signin.Status{Kind: signin.StatusSignedIn, Label: signin.Identity{
		Email:       huge,
		Method:      "Logged in with " + rlo + "detsurt",
		AccountType: "SomethingNew" + rlo + "detsurt",
		StartURL:    "https://" + rlo + "moc.live" + "/start",
		Region:      "us-east-1" + rlo,
	}})

	for name, v := range map[string]string{
		"Email":       got.Email,
		"Auth":        got.Auth,
		"AccountType": got.AccountType,
		"StartURL":    got.StartURL,
		"Region":      got.Region,
	} {
		if len(v) > maxIdentityFieldBytes+len("...") {
			t.Errorf("%s = %d bytes, want <= %d", name, len(v), maxIdentityFieldBytes+3)
		}
		for _, r := range v {
			if runesafe.IsBidiControl(r) {
				t.Errorf("%s = %q still carries U+%04X", name, v, r)
			}
		}
	}
	// Bounded, not emptied: the field still identifies the account.
	if !strings.HasPrefix(got.Email, "aaaa") || !strings.HasSuffix(got.Email, "...") {
		t.Errorf("Email = %q..., want a truncated prefix ending in the cut marker", got.Email[:min(20, len(got.Email))])
	}
}

// TestWhoamiFrom_LeavesOrdinaryIdentityFieldsByteIdentical pins that real emails, region ids and start URLs are untouched.
func TestWhoamiFrom_LeavesOrdinaryIdentityFieldsByteIdentical(t *testing.T) {
	got := whoamiFrom(&signin.Status{Kind: signin.StatusSignedIn, Label: signin.Identity{
		Email: "jean-rené@société.example", Method: "Logged in with Builder ID", AccountType: "BuilderId",
		StartURL: "https://d-9067f19e2b.awsapps.com/start", Region: "eu-west-3",
	}})
	for name, pair := range map[string][2]string{
		"Email":       {got.Email, "jean-rené@société.example"},
		"AccountType": {got.AccountType, "BuilderId"},
		"StartURL":    {got.StartURL, "https://d-9067f19e2b.awsapps.com/start"},
		"Region":      {got.Region, "eu-west-3"},
		"Auth":        {got.Auth, "Logged in with Builder ID"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want byte-identical %q", name, pair[0], pair[1])
		}
	}
}
