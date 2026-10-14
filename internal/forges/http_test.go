package forges

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
)

const (
	githubRowPath = "/api/forges/github%3Agithub.com"
	githubOrigin  = "https://github.com"
	loginPath     = "/api/forges/login"
	testHelperBin = "/opt/marotte/marotte"
)

type pathWire struct {
	bodies map[string]string
	// key names the body a request is answered with, its URL path when nil.
	key  func(*http.Request) string
	seen []string
	mu   sync.Mutex
}

func (w *pathWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.seen = append(w.seen, r.Method+" "+r.URL.Path)
	w.mu.Unlock()
	key := r.URL.Path
	if w.key != nil {
		key = w.key(r)
	}
	status, body := http.StatusOK, w.bodies[key]
	if _, ok := w.bodies[key]; !ok {
		status, body = http.StatusNotFound, `{"message":"404 Not Found"}`
	}
	return &http.Response{
		StatusCode: status, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (w *pathWire) requests() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}

type userWire struct {
	body   string
	seen   []*http.Request
	status int
	mu     sync.Mutex
}

func (w *userWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.seen = append(w.seen, r.Clone(r.Context()))
	w.mu.Unlock()
	return &http.Response{
		StatusCode: w.status, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}, "X-Oauth-Scopes": {"repo, workflow"}},
		Body:   io.NopCloser(strings.NewReader(w.body)),
	}, nil
}

func (w *userWire) requests() []*http.Request {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}

func aliceWire() *userWire {
	return &userWire{status: http.StatusOK, body: `{"login":"alice"}`}
}

// connectHarness is the forge routes over a manager with no forge CLI on PATH,
// an isolated git config and HOME, a fixed helper binary, and the forge API
// answered by an injected wire.
type connectHarness struct {
	m       *Manager
	handler *HTTPHandler
	mux     *http.ServeMux
	home    string
	cfgDir  string
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func newConnectHarness(t *testing.T, wire http.RoundTripper) *connectHarness {
	t.Helper()
	gitcfg := isolateGit(t)
	stubPath(t)
	cfg := t.TempDir()
	m, _ := newManager(t, cfg, nil)
	if wire != nil {
		m.extraOpts = []forgeapi.Option{forgeapi.WithWireTransport(wire)}
	}
	m.Load(t.Context())
	mux := http.NewServeMux()
	handler := NewHTTPHandler(m)
	handler.RegisterRoutes(mux)
	return &connectHarness{m: m, handler: handler, mux: mux, home: filepath.Dir(gitcfg), cfgDir: cfg}
}

func (h *connectHarness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// login posts a token sign-in for kind on host, extra being more of the body's
// fields with their leading comma.
func (h *connectHarness) login(t *testing.T, kind Kind, host, token, extra string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"kind": string(kind), "host": host, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	return h.do(t, http.MethodPost, loginPath, strings.TrimSuffix(string(body), "}")+extra+"}")
}

// signedIn is a login that must answer 200 with the id the server minted.
func (h *connectHarness) signedIn(t *testing.T, kind Kind, host, token, extra string) string {
	t.Helper()
	rec := h.login(t, kind, host, token, extra)
	var got LoginResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ForgeID == "" {
		t.Fatalf("POST %s for %s on %q = %d %s, want 200 with the minted id", loginPath, kind, host, rec.Code, rec.Body)
	}
	return got.ForgeID
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body
}

func (h *connectHarness) helperValue(t *testing.T) string {
	t.Helper()
	return mustHelperValue(t, testHelperBin, h.cfgDir)
}

// storedKeys is every key the harness's credential store holds.
func (h *connectHarness) storedKeys(t *testing.T) []string {
	t.Helper()
	keys, err := openTestStore(t, h.cfgDir).Keys()
	if err != nil {
		t.Fatalf("read the store's keys: %v", err)
	}
	return keys
}

// assertNothingStored checks that no credential, no connection record and no
// helper registration exist after a refused sign-in.
func (h *connectHarness) assertNothingStored(t *testing.T, origin string) {
	t.Helper()
	if keys := h.storedKeys(t); len(keys) != 0 {
		t.Errorf("store holds %q after a refused sign-in, want nothing", keys)
	}
	if recs := readRecords(t, h.cfgDir); len(recs) != 0 {
		t.Errorf("connection records = %+v after a refused sign-in, want none", recs)
	}
	if got := helperValuesFor(t, origin); len(got) != 0 {
		t.Errorf("credential.%s.helper = %q after a refused sign-in, want nothing registered", origin, got)
	}
}

func TestPATLogin_SavesAStaticRecordAndRegistersTheHelper(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)

	if id := h.signedIn(t, KindGitHub, "", "ghp_secret", ""); id != "github:github.com" {
		t.Fatalf("minted id = %q, want github:github.com", id)
	}

	cred, ok, err := openTestStore(t, h.cfgDir).Load(slotKey("github:github.com", 0))
	if err != nil || !ok {
		t.Fatalf("store.Load(slot 0) = %v, %v; want the saved credential", ok, err)
	}
	if cred.Kind != forgeapi.CredKindStaticPAT || cred.Token != "ghp_secret" || cred.Account != "alice" ||
		cred.Family != forgeapi.FamilyGitHub || cred.WebBaseURL != githubOrigin {
		t.Errorf("stored credential = %+v, want a static PAT for alice on %s", cred, githubOrigin)
	}
	recs := readRecords(t, h.cfgDir)
	if len(recs) != 1 || recs[0].ID != "github:github.com" || recs[0].Kind != KindGitHub || recs[0].SignIn == "" {
		t.Fatalf("connection records = %+v; want the github.com record with its sign-in id", recs)
	}
	want := h.helperValue(t)
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, []string{"", want}) {
		t.Errorf("credential.%s.helper = %q, want the reset then %q", githubOrigin, got, want)
	}
	if seen := wire.requests(); len(seen) != 1 || seen[0].Header.Get("Authorization") != "Bearer ghp_secret" {
		t.Errorf("the forge saw %d requests, want the one identity read carrying the pasted token", len(seen))
	}
	if row := viewFor(t, h.m, "github:github.com"); row.Status != ForgeConnected || row.Username != "alice" ||
		row.Account != recs[0].SignIn {
		t.Errorf("row after the sign-in = %+v, want github.com connected as alice under the stored sign-in", row)
	}
}

func TestPATLogin_ScrubsTheHostsCleartextCredential(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	creds := filepath.Join(h.home, ".git-credentials")
	writeFixture(t, creds, "https://alice:old@github.com\nhttps://bob:tok@other.example/\n")

	h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatalf("read %s: %v", creds, err)
	}
	if strings.Contains(string(data), "github.com") || !strings.Contains(string(data), "other.example") {
		t.Errorf("~/.git-credentials after the sign-in = %q, want the github.com line gone and the other kept", data)
	}
}

func TestPATLogin_StoresNothingWhenWhoamiFails(t *testing.T) {
	wire := &userWire{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`}
	h := newConnectHarness(t, wire)

	rec := h.login(t, KindGitHub, "", "ghp_revoked", "")
	body := decodeBody(t, rec)
	if rec.Code != http.StatusUnauthorized || body["error"] == nil || body["kind"] != "unauthorized" {
		t.Errorf("POST %s with a refused token = %d %v, want 401 {error, kind: unauthorized}", loginPath, rec.Code, body)
	}
	h.assertNothingStored(t, githubOrigin)
	if _, ok := h.m.view("github:github.com"); ok {
		t.Error("a row exists after a refused sign-in, want none")
	}
}

func TestPATLogin_UnusablePersistenceAnswers503(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)
	h.signedIn(t, KindGitLab, "", "glpat", "")
	before := len(wire.requests())
	widenStore(t, h.m)

	rec := h.login(t, KindGitHub, "", "ghp_secret", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "credential store") {
		t.Errorf("POST %s over an unreadable store = %d %s, want 503 naming the store", loginPath, rec.Code, rec.Body)
	}
	if n := len(wire.requests()) - before; n != 0 {
		t.Errorf("the forge saw %d requests over an unusable store, want none", n)
	}
}

func TestPATLogin_WebBaseURLMustMatchTheHost(t *testing.T) {
	cases := []struct {
		name, base string
	}{
		{"another host", "https://evil.example"},
		{"another port", "https://github.com:8443"},
		{"a path", "https://github.com/elsewhere"},
		{"userinfo", "https://user:pw@github.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := aliceWire()
			h := newConnectHarness(t, wire)

			rec := h.login(t, KindGitHub, "", "ghp_secret", `,"web_base_url":"`+tc.base+`"`)
			body := decodeBody(t, rec)
			if rec.Code != http.StatusBadRequest || body["code"] != codeWebBaseInvalid {
				t.Errorf("sign-in to github.com at %q = %d %v, want 400 coded %q", tc.base, rec.Code, body, codeWebBaseInvalid)
			}
			if n := len(wire.requests()); n != 0 {
				t.Errorf("the token was sent in %d requests for a web base the host does not name, want none", n)
			}
			h.assertNothingStored(t, githubOrigin)
		})
	}

	t.Run("the host's own origin", func(t *testing.T) {
		h := newConnectHarness(t, aliceWire())
		h.signedIn(t, KindGitHub, "", "ghp_secret", `,"web_base_url":"https://GitHub.com/"`)
		if recs := readRecords(t, h.cfgDir); len(recs) != 1 || recs[0].WebBaseURL != "https://github.com" {
			t.Errorf("records = %+v, want the web base stored as scheme://host", recs)
		}
	})
}

// The host becomes the origin the token is sent to, so a host that is not a
// bare host[:port] is refused before any request: here userinfo would move the
// origin to evil.example.
func TestPATLogin_TheHostMustBeABareHost(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)

	rec := h.login(t, KindGitHub, "github.com:443@evil.example", "ghp_secret", "")
	if body := decodeBody(t, rec); rec.Code != http.StatusBadRequest || body["error"] != errHostInvalid.Error() {
		t.Errorf("sign-in to host github.com:443@evil.example = %d %v, want 400 with the host rule", rec.Code, body)
	}
	if n := len(wire.requests()); n != 0 {
		t.Errorf("the token was sent in %d requests, want none", n)
	}
	h.assertNothingStored(t, "https://evil.example")
}

// A plaintext web base reaches a real loopback GitHub API, so the library's own
// plaintext and private-address refusals are what decide it.
func TestPATLogin_HTTPWebBaseNeedsPlaintext(t *testing.T) {
	var reads int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		reads++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"login":"alice"}`)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	extra := func(more string) string { return `,"web_base_url":"` + srv.URL + `","private_addresses":true` + more }

	h := newConnectHarness(t, nil)
	rec := h.login(t, KindGitHub, host, "ghp_secret", extra(""))
	if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != forgeapi.CodePlaintextRefused {
		t.Errorf("sign-in over http without plaintext_http = %d %v, want 400 coded %q", rec.Code, got, forgeapi.CodePlaintextRefused)
	}
	h.assertNothingStored(t, srv.URL)

	h.signedIn(t, KindGitHub, host, "ghp_secret", extra(`,"plaintext_http":true`))
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Errorf("the instance answered %d identity reads, want 1 (none for the refused sign-in)", reads)
	}
	recs := readRecords(t, h.cfgDir)
	if len(recs) != 1 || recs[0].WebBaseURL != srv.URL || !recs[0].PlaintextHTTP || !recs[0].PrivateAddresses {
		t.Errorf("connection records = %+v; want the loopback record with both opt-ins", recs)
	}
}

// Gitea has no public instance to default to, so a body naming no host is
// refused before any request.
func TestPATLogin_GiteaRequiresAHost(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)

	rec := h.login(t, KindGitea, "", "gitea-secret", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errHostInvalid.Error()) {
		t.Errorf("sign-in to gitea with no host = %d %s, want 400 naming the host", rec.Code, rec.Body)
	}
	if n := len(wire.requests()); n != 0 {
		t.Errorf("the token was sent in %d requests, want none", n)
	}
	if recs := readRecords(t, h.cfgDir); len(recs) != 0 {
		t.Errorf("connection records = %+v after a refused sign-in, want none", recs)
	}
}

// A loopback Gitea over plain HTTP needs both opt-ins, and the library's own
// refusals name the one that is missing before the token is sent.
func TestPATLogin_LoopbackGiteaNeedsBothOptIns(t *testing.T) {
	var reads int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		reads++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"login":"alice"}`)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	extra := func(optIns string) string { return `,"web_base_url":"` + srv.URL + `"` + optIns }
	h := newConnectHarness(t, nil)

	refusals := []struct{ optIns, code string }{
		{`,"plaintext_http":true`, forgeapi.CodePrivateAddressRefused},
		{`,"private_addresses":true`, forgeapi.CodePlaintextRefused},
	}
	for _, tc := range refusals {
		rec := h.login(t, KindGitea, host, "gitea-secret", extra(tc.optIns))
		if got := decodeBody(t, rec); rec.Code != http.StatusBadRequest || got["code"] != tc.code {
			t.Errorf("sign-in to %s with only %s = %d %v, want 400 coded %q", srv.URL, tc.optIns, rec.Code, got, tc.code)
		}
		h.assertNothingStored(t, srv.URL)
	}

	h.signedIn(t, KindGitea, host, "gitea-secret", extra(`,"plaintext_http":true,"private_addresses":true`))
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Errorf("the instance answered %d identity reads, want 1 (none for the refused sign-ins)", reads)
	}
	recs := readRecords(t, h.cfgDir)
	if len(recs) != 1 || recs[0].WebBaseURL != srv.URL || !recs[0].PlaintextHTTP || !recs[0].PrivateAddresses {
		t.Errorf("connection records = %+v; want the loopback record with both opt-ins", recs)
	}
}

// A GitLab project can sit in a subgroup, so its path has more than two
// segments; the repository list keeps the whole namespace as the owner.
func TestGitLabPATLogin_NestedProjectIsListed(t *testing.T) {
	wire := &pathWire{bodies: map[string]string{
		"/api/v4/user": `{"id":1,"username":"alice"}`,
		"/api/v4/projects": `[{"path_with_namespace":"group/sub/project",` +
			`"web_url":"https://gitlab.com/group/sub/project",` +
			`"http_url_to_repo":"https://gitlab.com/group/sub/project.git",` +
			`"default_branch":"main","visibility":"private"}]`,
	}}
	h := newConnectHarness(t, wire)
	h.signedIn(t, KindGitLab, "", "glpat-secret", "")

	rec := h.do(t, http.MethodGet, "/api/forges/gitlab%3Agitlab.com/repos", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET repos = %d %s, want 200", rec.Code, rec.Body)
	}
	var got struct{ Repos []Repo }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("repos body %q: %v", rec.Body, err)
	}
	if len(got.Repos) != 1 {
		t.Fatalf("GET repos = %+v, want one row", got.Repos)
	}
	row := got.Repos[0]
	row.Affordances = RepoAffordances{}
	want := Repo{
		RepoID: "v1.67726f75702f7375622f70726f6a656374",
		Owner:  "group/sub", Name: "project", FullName: "group/sub/project", DefaultBranch: "main",
		URL: "https://gitlab.com/group/sub/project", CloneURL: "https://gitlab.com/group/sub/project.git",
		Private: true,
	}
	if !reflect.DeepEqual(row, want) {
		t.Errorf("GET repos row (affordances aside) = %+v, want %+v", row, want)
	}
	if seen := wire.requests(); !slices.Contains(seen, "GET /api/v4/projects") {
		t.Errorf("the forge saw %q, want the member-project list", seen)
	}
}

func TestDisconnect_DeletesRecordStoreEntryAndHelperPair(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	const foreign = "!/usr/bin/gh auth git-credential"
	writeGitConfig(t, os.Getenv("GIT_CONFIG_GLOBAL"),
		"[credential \""+githubOrigin+"\"]\n\thelper =\n\thelper = "+foreign+"\n")
	h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	if got := helperValuesFor(t, githubOrigin); len(got) != 4 {
		t.Fatalf("Setup: credential.%s.helper = %q, want the foreign pair then Marotte's", githubOrigin, got)
	}
	creds := filepath.Join(h.home, ".git-credentials")
	writeFixture(t, creds, "https://alice:old@github.com\nhttps://bob:tok@other.example/\n")

	rec := h.do(t, http.MethodDelete, githubRowPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE %s = %d %s", githubRowPath, rec.Code, rec.Body)
	}
	if keys := h.storedKeys(t); len(keys) != 0 {
		t.Errorf("store still holds %q after the sign-out", keys)
	}
	if recs := readRecords(t, h.cfgDir); len(recs) != 0 {
		t.Errorf("connection records after the sign-out = %+v; want none", recs)
	}
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, []string{"", foreign}) {
		t.Errorf("credential.%s.helper after the sign-out = %q, want only the foreign pair", githubOrigin, got)
	}
	if data, err := os.ReadFile(creds); err != nil || strings.Contains(string(data), "github.com") {
		t.Errorf("~/.git-credentials after the sign-out = %q, %v; want the github.com line scrubbed", data, err)
	}
	if _, ok := h.m.view("github:github.com"); ok {
		t.Error("a row exists after the sign-out, want none")
	}
}

func TestDisconnect_UnusablePersistenceAnswers503AndRemovesNothing(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	restore := widenStore(t, h.m)

	rec := h.do(t, http.MethodDelete, githubRowPath, "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "credential store") {
		t.Errorf("DELETE %s over an unreadable store = %d %s, want 503 naming the store", githubRowPath, rec.Code, rec.Body)
	}
	restore()
	if keys := h.storedKeys(t); len(keys) != 1 {
		t.Errorf("store keys after a refused sign-out = %q, want the credential kept", keys)
	}
	if recs := readRecords(t, h.cfgDir); len(recs) != 1 {
		t.Errorf("records after a refused sign-out = %+v; want the record kept", recs)
	}
	if got := helperValuesFor(t, githubOrigin); len(got) != 2 {
		t.Errorf("credential.%s.helper after a refused sign-out = %q, want the pair kept", githubOrigin, got)
	}
}

const githubCapsPath = "/api/forges/github%3Agithub.com/capabilities"

// The embedded Core is nil, so any other method panics.
type capsCore struct {
	forgeapi.Core
	connErr  error
	grantErr error
	conn     forgeapi.ConnectionCaps
	grant    forgeapi.GrantCaps
	reads    int
}

func (*capsCore) Close() {}

func (c *capsCore) ConnectionCaps(context.Context) (forgeapi.ConnectionCaps, error) {
	c.reads++
	return c.conn, c.connErr
}

func (c *capsCore) GrantCaps(context.Context) (forgeapi.GrantCaps, error) {
	c.reads++
	return c.grant, c.grantErr
}

func TestCapabilitiesRoute_CarriesEvidence(t *testing.T) {
	core := &capsCore{
		conn: forgeapi.ConnectionCaps{
			Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapRerunChecks: forgeapi.SupportYes, "pin_runs": forgeapi.SupportNo},
			Ev: map[forgeapi.Capability]forgeapi.Evidence{
				forgeapi.CapRerunChecks: {Source: forgeapi.EvidenceSwagger, Detail: "swagger declares rerun"},
				"pin_runs":              {Source: forgeapi.EvidenceProbe, Detail: "the probe answered 404"},
			},
		},
		grant: forgeapi.GrantCaps{
			Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapReadMergeState: forgeapi.SupportYes},
			Ev: map[forgeapi.Capability]forgeapi.Evidence{
				forgeapi.CapReadMergeState: {Source: forgeapi.EvidenceResponseHeader, Detail: "the scope header carries repo"},
			},
		},
	}
	rec := getRoute(t, repoRoutes(recordManager(t, core)), githubCapsPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", githubCapsPath, rec.Code, rec.Body)
	}
	var got Capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET %s body %q: %v", githubCapsPath, rec.Body, err)
	}
	want := Capabilities{
		Account: testSignIn,
		Connection: map[string]Affordance{
			"rerun_checks": {Support: "yes", Source: "swagger", Detail: "swagger declares rerun"},
			"pin_runs":     {Support: "no", Source: "probe", Detail: "the probe answered 404"},
		},
		Grant: map[string]Affordance{
			"read_merge_state": {Support: "yes", Source: "response-header", Detail: "the scope header carries repo"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GET %s = %+v, want %+v", githubCapsPath, got, want)
	}
}

func TestCapabilitiesRoute_UnknownIsItsOwnSpelling(t *testing.T) {
	core := &capsCore{
		conn: forgeapi.ConnectionCaps{
			Caps: map[forgeapi.Capability]forgeapi.Support{
				forgeapi.CapRerunChecks: forgeapi.SupportUnknown,
				"pin_runs":              forgeapi.SupportYes,
			},
			Ev: map[forgeapi.Capability]forgeapi.Evidence{
				forgeapi.CapRerunChecks: {Source: forgeapi.EvidenceResponseHeader, Detail: "the runner is enabled per repository"},
				"watch_runs":            {Source: forgeapi.EvidenceDefault, Detail: "nothing answered"},
			},
		},
	}
	rec := getRoute(t, repoRoutes(recordManager(t, core)), githubCapsPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", githubCapsPath, rec.Code, rec.Body)
	}
	want := map[string]any{
		"account": testSignIn,
		"connection": map[string]any{
			"rerun_checks": map[string]any{"support": "unknown", "source": "response-header", "detail": "the runner is enabled per repository"},
			"pin_runs":     map[string]any{"support": "yes", "source": "unknown", "detail": ""},
			"watch_runs":   map[string]any{"support": "unknown", "source": "default", "detail": "nothing answered"},
		},
		// The grant scope answered nothing, which the library reads as unknown.
		"grant": map[string]any{
			"read_merge_state": map[string]any{"support": "unknown", "source": "unknown", "detail": ""},
		},
	}
	if got := decodeBody(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("GET %s = %v, want %v", githubCapsPath, got, want)
	}
}

func TestCapabilitiesRoute_AFailedReadAnswersTheEnvelope(t *testing.T) {
	cases := map[string]struct {
		core   *capsCore
		code   string
		status int
	}{
		"the connection scope": {
			core:   &capsCore{connErr: &forgeapi.Error{Code: forgeapi.CodeFamilyUndetected, Kind: forgeapi.KindUnknown}},
			code:   forgeapi.CodeFamilyUndetected,
			status: http.StatusUnprocessableEntity,
		},
		"the grant scope": {
			core:   &capsCore{grantErr: &forgeapi.Error{Code: forgeapi.CodeReconnectRequired, Kind: forgeapi.KindUnauthorized}},
			code:   forgeapi.CodeReconnectRequired,
			status: http.StatusUnauthorized,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := getRoute(t, repoRoutes(recordManager(t, tc.core)), githubCapsPath)
			body := decodeBody(t, rec)
			if rec.Code != tc.status || body["code"] != tc.code {
				t.Errorf("GET %s with %s failing = %d %v, want %d %s", githubCapsPath, name, rec.Code, body, tc.status, tc.code)
			}
			if _, ok := body["connection"]; ok {
				t.Errorf("GET %s with %s failing carries a verdict: %v", githubCapsPath, name, body)
			}
		})
	}
}

func TestCapabilitiesRoute_UnusablePersistenceIs503(t *testing.T) {
	core := &capsCore{}
	m := recordManager(t, core)
	widenStore(t, m)

	rec := getRoute(t, repoRoutes(m), githubCapsPath)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "credential store") {
		t.Errorf("GET %s over unusable records = %d %s, want 503 naming the reason", githubCapsPath, rec.Code, rec.Body)
	}
	if core.reads != 0 {
		t.Errorf("the library answered %d capability reads over unusable records, want none", core.reads)
	}
}

func TestCapabilitiesRoute_OnlyGETReads(t *testing.T) {
	core := &capsCore{}
	mux := repoRoutes(recordManager(t, core))
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, githubCapsPath, nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s %s = %d Allow %q, want 405 Allow GET", method, githubCapsPath, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if core.reads != 0 {
		t.Errorf("the library answered %d capability reads for a non-GET, want none", core.reads)
	}
}
