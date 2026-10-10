package forges

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type recordingWire struct {
	body string
	seen []*http.Request
	mu   sync.Mutex
}

func (w *recordingWire) transport() http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		w.mu.Lock()
		w.seen = append(w.seen, r.Clone(r.Context()))
		w.mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK, Request: r,
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   io.NopCloser(strings.NewReader(w.body)),
		}, nil
	})
}

func testStore(t *testing.T) *creds.FileStore {
	t.Helper()
	store, reason := openCredentialStore(t.TempDir())
	if store == nil {
		t.Fatalf("Setup: open store: %s", reason)
	}
	return store
}

func saveStatic(t *testing.T, store *creds.FileStore, rec *connectionRecord, token string) {
	t.Helper()
	if err := store.Save(rec.ID, creds.Record{
		Family: rec.Kind.family(), WebBaseURL: rec.webBase(), Kind: forgeapi.CredKindStaticPAT,
		Token: token, Issued: time.Now(), Account: "alice",
	}); err != nil {
		t.Fatalf("Setup: save credential: %v", err)
	}
}

func TestClientFor_CarriesTheRecordsConnectionAndOptions(t *testing.T) {
	store := testStore(t)
	rec := connectionRecord{
		ID: "github:ghe.example.com", Kind: KindGitHub, Host: "ghe.example.com", WebBaseURL: "https://ghe.example.com",
	}
	saveStatic(t, store, &rec, "ghe-token")
	wire := &recordingWire{body: `{"login":"alice"}`}
	f := newClientFactory()
	f.extra = []forgeapi.Option{forgeapi.WithWireTransport(wire.transport())}

	c, err := f.clientFor(store, &rec)
	if err != nil {
		t.Fatalf("clientFor(%+v) = %v", rec, err)
	}
	acct, err := c.core.Whoami(t.Context())
	if err != nil || acct.Login != "alice" {
		t.Fatalf("Whoami() = %+v, %v; want alice", acct, err)
	}
	if len(wire.seen) != 1 {
		t.Fatalf("the wire saw %d requests, want 1", len(wire.seen))
	}
	sent := wire.seen[0]
	if got := sent.URL.String(); got != "https://ghe.example.com/api/v3/user" {
		t.Errorf("request went to %q, want the record's web base %q", got, "https://ghe.example.com/api/v3/user")
	}
	if got := sent.Header.Get("Authorization"); got != "Bearer ghe-token" {
		t.Errorf("Authorization = %q, want the stored token", got)
	}
}

func TestClientFor_RebuildsOnlyWhenTheRecordChanges(t *testing.T) {
	store := testStore(t)
	rec := githubRecord()
	saveStatic(t, store, &rec, "tok")
	f := newClientFactory()
	built := 0
	f.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) {
		built++
		return &fakeCore{}, nil
	}

	first, _ := f.clientFor(store, &rec)
	bookkeeping := rec
	bookkeeping.HelperValue, bookkeeping.RotationCursor = "/bin/marotte git-credential", "cursor"
	bookkeeping.OwnerScopes = []string{"acme"}
	second, _ := f.clientFor(store, &bookkeeping)
	if built != 1 || first.core != second.core {
		t.Errorf("a record differing only in its helper value, rotation cursor and owner scopes built %d clients, want 1 reused",
			built)
	}
	moved := rec
	moved.WebBaseURL = "https://github.example.com"
	if _, err := f.clientFor(store, &moved); err != nil || built != 2 {
		t.Errorf("a re-addressed record built %d clients (err %v), want a second one", built, err)
	}
}

func TestSourceFor_CarriesTheRecordsPostures(t *testing.T) {
	var refreshes int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/oauth/access_token" {
			http.NotFound(w, r)
			return
		}
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"token-new","token_type":"bearer","scope":"repo","expires_in":28800,`+
			`"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	rec := connectionRecord{
		ID: MakeID(KindGitHub, host), Kind: KindGitHub, Host: host, WebBaseURL: srv.URL,
		CAPEM:            string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})),
		PrivateAddresses: true,
	}
	store := testStore(t)
	now := time.Now().Truncate(time.Second)
	if err := store.Save(rec.ID, creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: srv.URL, Kind: forgeapi.CredKindRotatingOAuth,
		Token: "token-old", Issued: now.Add(-8 * time.Hour), Expiry: now.Add(time.Minute),
		RefreshToken: "refresh-old", RefreshExpiry: now.Add(30 * 24 * time.Hour), ClientID: "client", Account: "alice",
	}); err != nil {
		t.Fatalf("Setup: save credential: %v", err)
	}

	src, err := newClientFactory().sourceFor(store, &rec)
	if err != nil {
		t.Fatalf("sourceFor(a private-range record) = %v", err)
	}
	token, err := src.Token(t.Context())
	if err != nil || token != "token-new" || refreshes != 1 {
		t.Errorf("Token() = %q, %v after %d refreshes; want token-new from one refresh through the record's CA and opt-in",
			token, err, refreshes)
	}

	_, err = creds.NewSource(store, rec.ID, connectionFor(&rec))
	var ferr *forgeapi.Error
	if !errors.As(err, &ferr) || ferr.Code != forgeapi.CodePrivateAddressRefused {
		t.Errorf("a source built without the record's postures = %v, want %q", err, forgeapi.CodePrivateAddressRefused)
	}
}

// The embedded Core is nil, so a method a test does not override panics rather than answering a
// zero value.
type fakeCore struct {
	forgeapi.Core
	repo      forgeapi.RepoRef
	repos     forgeapi.Page[forgeapi.Repository]
	prs       []forgeapi.PullRequest
	listCalls int
	closed    atomic.Int32
}

func (f *fakeCore) Close() { f.closed.Add(1) }

func (*fakeCore) BudgetState() forgeapi.BudgetState {
	return forgeapi.BudgetState{Remaining: forgeapi.BudgetRemainingUnknown}
}

func (f *fakeCore) ReadPR(_ context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	for _, p := range f.prs {
		if p.Ref.Number == pr.Number && strings.EqualFold(p.Repo.Selector, repo.Selector) {
			return p, nil
		}
	}
	return forgeapi.PullRequest{}, &forgeapi.Error{Kind: forgeapi.KindNotFound}
}

func (*fakeCore) Whoami(context.Context) (forgeapi.Account, error) {
	return forgeapi.Account{Login: "bob", Email: "bob@example.com"}, nil
}

func (f *fakeCore) ListRepos(context.Context, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	f.listCalls++
	return f.repos, nil
}

func (f *fakeCore) ListPRs(_ context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	f.listCalls++
	f.repo = repo
	if _, err := forgeapi.ResolveList(opts...); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return forgeapi.Page[forgeapi.PullRequest]{Items: f.prs}, nil
}

func (f *fakeCore) ListMyPRs(_ context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	f.listCalls++
	if _, err := forgeapi.ResolveList(opts...); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return forgeapi.Page[forgeapi.PullRequest]{Items: f.prs}, nil
}

func recordManager(t *testing.T, core forgeapi.Core) *Manager {
	t.Helper()
	rec := githubRecord()
	return recordManagerFor(t, &rec, core)
}

func recordManagerFor(t *testing.T, rec *connectionRecord, core forgeapi.Core) *Manager {
	t.Helper()
	stubPath(t)
	cfg := t.TempDir()
	seedStoreRecord(t, cfg, rec.ID, "bob")
	m := NewManager(cfg)
	saveRecords(t, m.conns, *rec)
	m.clients.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) { return core, nil }
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	return m
}

func TestManagerProbe_RecordRowAsksTheLibrary(t *testing.T) {
	m := recordManager(t, &fakeCore{})

	if err := m.probeConnection(t.Context(), "github:github.com"); err != nil {
		t.Fatalf("Probe() with no CLI on PATH = %v, want the library's Whoami", err)
	}
	if got := m.get("github:github.com"); got.Email != "bob@example.com" || !got.Connected || got.LastProbed == 0 {
		t.Errorf("row after Probe() = %+v, want connected with the library's email and a probe time", got)
	}
}

func TestManagerClient_DegradedRecordIsUnavailable(t *testing.T) {
	stubPath(t)
	m := NewManager(t.TempDir())
	saveRecords(t, m.conns, githubRecord())
	m.store, m.storeReason = nil, "the store is broken"
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err := m.client("github:github.com")
	if !errors.Is(err, errConnectionUnusable) || !strings.Contains(err.Error(), "the store is broken") {
		t.Errorf("client() over a degraded store = %v, want errConnectionUnusable naming the reason", err)
	}
}

func TestRepoNames_RecordRowListsThroughTheManager(t *testing.T) {
	core := &fakeCore{repos: forgeapi.Page[forgeapi.Repository]{Items: []forgeapi.Repository{
		{Ref: forgeapi.RepoRef{DisplayPath: "bob/one"}},
		{Ref: forgeapi.RepoRef{DisplayPath: "bob/two"}},
	}}}
	m := recordManager(t, core)

	names, err := m.RepoNames(t.Context(), "github:github.com")
	if err != nil {
		t.Fatalf("RepoNames() = %v", err)
	}
	if want := []string{"bob/one", "bob/two"}; !slices.Equal(names, want) {
		t.Errorf("RepoNames() = %v, want %v", names, want)
	}
}
