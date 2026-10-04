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
	githubPATPath = "/api/forges/github%3Agithub.com/login/pat"
	githubRowPath = "/api/forges/github%3Agithub.com"
	githubOrigin  = "https://github.com"
	gitlabPATPath = "/api/forges/gitlab%3Agitlab.com/login/pat"
	testHelperBin = "/opt/marotte/marotte"
)

// pathWire answers each request with the body registered for its URL path, a
// 404 for any other path, and records the method and path of every request.
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

// userWire answers GitHub's identity read with status and records every
// request it saw.
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

// writeFixture writes a file creating parents.
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
	m := NewManager(cfg)
	m.executable = func() (string, error) { return testHelperBin, nil }
	if wire != nil {
		m.clients.extra = []forgeapi.Option{forgeapi.WithWireTransport(wire)}
	}
	// The boot refresh, which spends the one startup reconcile of the helpers.
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	mux := http.NewServeMux()
	handler := NewHTTPHandler(m, nil)
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

// assertNothingStored checks that no credential, no connection record and no
// helper registration exist for id after a refused connect.
func (h *connectHarness) assertNothingStored(t *testing.T, id, origin string) {
	t.Helper()
	if h.m.store != nil {
		if _, ok, err := h.m.store.Load(id); ok || err != nil {
			t.Errorf("store holds a credential for %s (err %v) after a refused connect", id, err)
		}
	}
	recs, err := h.m.conns.load()
	if err != nil || len(recs) != 0 {
		t.Errorf("connection records = %+v, %v after a refused connect, want none", recs, err)
	}
	if got := helperValuesFor(t, origin); len(got) != 0 {
		t.Errorf("credential.%s.helper = %q after a refused connect, want nothing registered", origin, got)
	}
}

func TestPATLogin_SavesAStaticRecordAndRegistersTheHelper(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)

	rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`)
	if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != stateComplete {
		t.Fatalf("POST %s = %d %v, want 200 complete", githubPATPath, rec.Code, body)
	}

	cred, ok, err := h.m.store.Load("github:github.com")
	if err != nil || !ok {
		t.Fatalf("store.Load(github:github.com) = %v, %v; want the saved credential", ok, err)
	}
	if cred.Kind != forgeapi.CredKindStaticPAT || cred.Token != "ghp_secret" || cred.Account != "alice" ||
		cred.Family != forgeapi.FamilyGitHub || cred.WebBaseURL != githubOrigin {
		t.Errorf("stored credential = %+v, want a static PAT for alice on %s", cred, githubOrigin)
	}
	recs, err := h.m.conns.load()
	if err != nil || len(recs) != 1 || recs[0].ID != "github:github.com" || recs[0].Kind != KindGitHub {
		t.Fatalf("connection records = %+v, %v; want the github.com record", recs, err)
	}
	want := h.helperValue(t)
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, []string{"", want}) {
		t.Errorf("credential.%s.helper = %q, want the reset then %q", githubOrigin, got, want)
	}
	if recs[0].HelperValue != want {
		t.Errorf("record helper_value = %q, want the registered %q", recs[0].HelperValue, want)
	}
	if seen := wire.requests(); len(seen) != 1 || seen[0].Header.Get("Authorization") != "Bearer ghp_secret" {
		t.Errorf("the forge saw %d requests, want the one identity read carrying the pasted token", len(seen))
	}

	row := h.m.Get("github:github.com")
	if row == nil || !row.Connected || row.Username != "alice" {
		t.Errorf("row after connect = %+v, want github.com connected as alice with no CLI on PATH", row)
	}
}

func TestPATLogin_ScrubsTheHostsCleartextCredential(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	creds := filepath.Join(h.home, ".git-credentials")
	writeFixture(t, creds, "https://alice:old@github.com\nhttps://bob:tok@other.example/\n")

	if rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d %s", githubPATPath, rec.Code, rec.Body)
	}
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatalf("read %s: %v", creds, err)
	}
	if strings.Contains(string(data), "github.com") || !strings.Contains(string(data), "other.example") {
		t.Errorf("~/.git-credentials after connect = %q, want the github.com line gone and the other kept", data)
	}
}

func TestPATLogin_StoresNothingWhenWhoamiFails(t *testing.T) {
	wire := &userWire{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`}
	h := newConnectHarness(t, wire)

	rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_revoked"}`)
	body := decodeBody(t, rec)
	if _, coded := body["code"]; rec.Code != http.StatusOK || body["error"] == nil || !coded || body["kind"] != "unauthorized" {
		t.Errorf("POST %s with a refused token = %d %v, want a 2xx {error, code, kind: unauthorized}",
			githubPATPath, rec.Code, body)
	}
	if body["status"] == stateComplete {
		t.Errorf("a refused token answered complete: %v", body)
	}
	h.assertNothingStored(t, "github:github.com", githubOrigin)
	if row := h.m.Get("github:github.com"); row != nil {
		t.Errorf("row after a refused connect = %+v, want none", row)
	}
}

func TestPATLogin_DegradedStoreAnswers503(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)
	h.m.store, h.m.storeReason = nil, "the forge credential store /x cannot be used"

	rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "/x cannot be used") {
		t.Errorf("POST %s over a degraded store = %d %s, want 503 naming the store's reason",
			githubPATPath, rec.Code, rec.Body)
	}
	if n := len(wire.requests()); n != 0 {
		t.Errorf("the forge saw %d requests over a degraded store, want none", n)
	}
	h.assertNothingStored(t, "github:github.com", githubOrigin)
}

func TestPATLogin_WebBaseURLMustMatchTheIDHost(t *testing.T) {
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

			rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret","web_base_url":"`+tc.base+`"}`)
			body := decodeBody(t, rec)
			if rec.Code != http.StatusOK || body["code"] != codeWebBaseInvalid {
				t.Errorf("connect github:github.com at %q = %d %v, want a 2xx refusal coded %q",
					tc.base, rec.Code, body, codeWebBaseInvalid)
			}
			if n := len(wire.requests()); n != 0 {
				t.Errorf("the token was sent in %d requests for a web base the id does not name, want none", n)
			}
			h.assertNothingStored(t, "github:github.com", githubOrigin)
		})
	}

	t.Run("the id's own origin", func(t *testing.T) {
		h := newConnectHarness(t, aliceWire())
		rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret","web_base_url":"https://GitHub.com/"}`)
		if body := decodeBody(t, rec); body["status"] != stateComplete {
			t.Errorf("connect at the id's own origin = %d %v, want complete", rec.Code, body)
		}
	})
}

// The host comes from the request path and becomes the origin the token is
// sent to, so a host that is not a bare host[:port] is refused before any
// request: here userinfo would move the origin to evil.example.
func TestPATLogin_IDHostMustBeABareHost(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)

	rec := h.do(t, http.MethodPost, "/api/forges/github%3Agithub.com%3A443%40evil.example/login/pat",
		`{"token":"ghp_secret"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("connect to host github.com:443@evil.example = %d %s, want 400", rec.Code, rec.Body)
	}
	if n := len(wire.requests()); n != 0 {
		t.Errorf("the token was sent in %d requests, want none", n)
	}
	h.assertNothingStored(t, "github:github.com:443@evil.example", "https://evil.example")
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
	id := MakeID(KindGitHub, host)
	path := "/api/forges/" + strings.ReplaceAll(id, ":", "%3A") + "/login/pat"
	body := func(extra string) string {
		return `{"token":"ghp_secret","web_base_url":"` + srv.URL + `","private_addresses":true` + extra + `}`
	}

	h := newConnectHarness(t, nil)
	rec := h.do(t, http.MethodPost, path, body(""))
	if got := decodeBody(t, rec); rec.Code != http.StatusOK || got["code"] != forgeapi.CodePlaintextRefused {
		t.Errorf("connect over http without plaintext_http = %d %v, want a 2xx refusal coded %q",
			rec.Code, got, forgeapi.CodePlaintextRefused)
	}
	h.assertNothingStored(t, id, srv.URL)

	rec = h.do(t, http.MethodPost, path, body(`,"plaintext_http":true`))
	if got := decodeBody(t, rec); got["status"] != stateComplete {
		t.Fatalf("connect over http with plaintext_http = %d %v, want complete", rec.Code, got)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Errorf("the instance answered %d identity reads, want 1 (none for the refused connect)", reads)
	}
	recs, err := h.m.conns.load()
	if err != nil || len(recs) != 1 || recs[0].WebBaseURL != srv.URL || !recs[0].PlaintextHTTP || !recs[0].PrivateAddresses {
		t.Errorf("connection records = %+v, %v; want the loopback record with both opt-ins", recs, err)
	}
}

// Gitea has no public instance to default to, so an id naming no host is
// refused before any request.
func TestPATLogin_GiteaRequiresAHost(t *testing.T) {
	wire := aliceWire()
	h := newConnectHarness(t, wire)

	rec := h.do(t, http.MethodPost, patPath("gitea:"), `{"token":"gitea-secret"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("connect gitea with no host = %d %s, want 400", rec.Code, rec.Body)
	}
	if n := len(wire.requests()); n != 0 {
		t.Errorf("the token was sent in %d requests, want none", n)
	}
	if recs, err := h.m.conns.load(); err != nil || len(recs) != 0 {
		t.Errorf("connection records = %+v, %v after a refused connect, want none", recs, err)
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
	id := MakeID(KindGitea, strings.TrimPrefix(srv.URL, "http://"))
	body := func(optIns string) string {
		return `{"token":"gitea-secret","web_base_url":"` + srv.URL + `"` + optIns + `}`
	}
	h := newConnectHarness(t, nil)

	refusals := []struct{ optIns, code string }{
		{`,"plaintext_http":true`, forgeapi.CodePrivateAddressRefused},
		{`,"private_addresses":true`, forgeapi.CodePlaintextRefused},
	}
	for _, tc := range refusals {
		rec := h.do(t, http.MethodPost, patPath(id), body(tc.optIns))
		if got := decodeBody(t, rec); rec.Code != http.StatusOK || got["code"] != tc.code {
			t.Errorf("connect %s with only %s = %d %v, want a 2xx refusal coded %q", srv.URL, tc.optIns, rec.Code, got, tc.code)
		}
		h.assertNothingStored(t, id, srv.URL)
	}

	rec := h.do(t, http.MethodPost, patPath(id), body(`,"plaintext_http":true,"private_addresses":true`))
	if got := decodeBody(t, rec); got["status"] != stateComplete {
		t.Fatalf("connect %s with both opt-ins = %d %v, want complete", srv.URL, rec.Code, got)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Errorf("the instance answered %d identity reads, want 1 (none for the refused connects)", reads)
	}
	recs, err := h.m.conns.load()
	if err != nil || len(recs) != 1 || recs[0].WebBaseURL != srv.URL || !recs[0].PlaintextHTTP || !recs[0].PrivateAddresses {
		t.Errorf("connection records = %+v, %v; want the loopback record with both opt-ins", recs, err)
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
	connectGitLabByPAT(t, h)

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
	if rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`); rec.Code != http.StatusOK {
		t.Fatalf("Setup: connect = %d %s", rec.Code, rec.Body)
	}
	if got := helperValuesFor(t, githubOrigin); len(got) != 4 {
		t.Fatalf("Setup: credential.%s.helper = %q, want the foreign pair then Marotte's", githubOrigin, got)
	}
	creds := filepath.Join(h.home, ".git-credentials")
	writeFixture(t, creds, "https://alice:old@github.com\nhttps://bob:tok@other.example/\n")

	rec := h.do(t, http.MethodDelete, githubRowPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE %s = %d %s", githubRowPath, rec.Code, rec.Body)
	}
	if _, ok, err := h.m.store.Load("github:github.com"); ok || err != nil {
		t.Errorf("store still holds the credential after disconnect (err %v)", err)
	}
	if recs, err := h.m.conns.load(); err != nil || len(recs) != 0 {
		t.Errorf("connection records after disconnect = %+v, %v; want none", recs, err)
	}
	if got := helperValuesFor(t, githubOrigin); !slices.Equal(got, []string{"", foreign}) {
		t.Errorf("credential.%s.helper after disconnect = %q, want only the foreign pair", githubOrigin, got)
	}
	if data, err := os.ReadFile(creds); err != nil || strings.Contains(string(data), "github.com") {
		t.Errorf("~/.git-credentials after disconnect = %q, %v; want the github.com line scrubbed", data, err)
	}
	if row := h.m.Get("github:github.com"); row != nil {
		t.Errorf("row after disconnect = %+v, want none", row)
	}
}

func TestDisconnect_DegradedStoreAnswers503AndRemovesNothing(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	if rec := h.do(t, http.MethodPost, githubPATPath, `{"token":"ghp_secret"}`); rec.Code != http.StatusOK {
		t.Fatalf("Setup: connect = %d %s", rec.Code, rec.Body)
	}
	store := h.m.store
	h.m.store, h.m.storeReason = nil, "the forge credential store /x cannot be used"

	rec := h.do(t, http.MethodDelete, githubRowPath, "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "/x cannot be used") {
		t.Errorf("DELETE %s over a degraded store = %d %s, want 503 naming the reason", githubRowPath, rec.Code, rec.Body)
	}
	if _, ok, err := store.Load("github:github.com"); !ok || err != nil {
		t.Errorf("credential after a refused disconnect: present %v (err %v), want it kept", ok, err)
	}
	if recs, err := h.m.conns.load(); err != nil || len(recs) != 1 {
		t.Errorf("records after a refused disconnect = %+v, %v; want the record kept", recs, err)
	}
	if got := helperValuesFor(t, githubOrigin); len(got) != 2 {
		t.Errorf("credential.%s.helper after a refused disconnect = %q, want the pair kept", githubOrigin, got)
	}
}

const githubCapsPath = "/api/forges/github%3Agithub.com/capabilities"

// capsCore answers the two capability scopes with fixed values and counts the
// reads. The embedded Core is nil, so any other method panics.
type capsCore struct {
	forgeapi.Core
	connErr  error
	grantErr error
	conn     forgeapi.ConnectionCaps
	grant    forgeapi.GrantCaps
	reads    int
}

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
	var got map[string]map[string]Affordance
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET %s body %q: %v", githubCapsPath, rec.Body, err)
	}
	want := map[string]map[string]Affordance{
		"connection": {
			"rerun_checks": {Support: "yes", Source: "swagger", Detail: "swagger declares rerun"},
			"pin_runs":     {Support: "no", Source: "probe", Detail: "the probe answered 404"},
		},
		"grant": {
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

func TestCapabilitiesRoute_AnUnusableRecordFileIs503(t *testing.T) {
	core := &capsCore{}
	m := recordManager(t, core)
	m.mu.Lock()
	m.recordsReason = "the connection records /x cannot be used"
	m.mu.Unlock()

	rec := getRoute(t, repoRoutes(m), githubCapsPath)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "/x cannot be used") {
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
