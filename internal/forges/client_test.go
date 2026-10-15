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

func TestOpenConn_CarriesTheConnectionAndTheStoredToken(t *testing.T) {
	cfg := t.TempDir()
	spec := specFor(KindGitHub, "ghe.example.com")
	spec.WebBaseURL = "https://ghe.example.com"
	store := openTestStore(t, cfg)
	if err := store.Save(slotKey(spec.ID, 0), staticCredential(&spec, "alice", "ghe-token")); err != nil {
		t.Fatal(err)
	}
	wire := &recordingWire{body: `{"login":"alice"}`}

	core, _, _, err := openConn(store, &spec, newFamilyClient, []forgeapi.Option{forgeapi.WithWireTransport(wire.transport())})
	if err != nil {
		t.Fatalf("openConn(%+v) = %v", spec, err)
	}
	acct, err := core.Whoami(t.Context())
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

func TestOpenConn_TheSourceCarriesTheConnectionsPostures(t *testing.T) {
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
	spec := specFor(KindGitHub, host)
	spec.WebBaseURL = srv.URL
	spec.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	spec.PrivateAddresses = true
	store := openTestStore(t, t.TempDir())
	now := time.Now().Truncate(time.Second)
	if err := store.Save(slotKey(spec.ID, 0), creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: srv.URL, Kind: forgeapi.CredKindRotatingOAuth,
		Token: "token-old", Issued: now.Add(-8 * time.Hour), Expiry: now.Add(time.Minute),
		RefreshToken: "refresh-old", RefreshExpiry: now.Add(30 * 24 * time.Hour), ClientID: "client", Account: "alice",
	}); err != nil {
		t.Fatalf("Setup: save credential: %v", err)
	}

	_, src, _, err := openConn(store, &spec, newFamilyClient, nil)
	if err != nil {
		t.Fatalf("openConn(a private-range record) = %v", err)
	}
	token, err := src.Token(t.Context())
	if err != nil || token != "token-new" || refreshes != 1 {
		t.Errorf("Token() = %q, %v after %d refreshes; want token-new from one refresh through the record's CA and opt-in",
			token, err, refreshes)
	}

	_, err = creds.NewSource(store, slotKey(spec.ID, 0), connectionFor(&spec))
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

// HeldRepoNames announces the page its read stored, then answers it for the
// account it was read through and nil for any other.
func TestHeldRepoNames_AnnouncesTheLandingAndAnswersOnlyItsAccount(t *testing.T) {
	core := &fakeCore{repos: forgeapi.Page[forgeapi.Repository]{Items: []forgeapi.Repository{
		{Ref: forgeapi.RepoRef{DisplayPath: "bob/one"}},
		{Ref: forgeapi.RepoRef{DisplayPath: "bob/two"}},
	}}}
	stubPath(t)
	cfg := t.TempDir()
	rec := githubRecord()
	seedConnections(t, cfg, "bob", rec)
	m, changes := loadedManager(t, cfg, core)
	listed := Change{ID: rec.ID, ReposListed: true}

	m.HeldRepoNames(t.Context(), rec.ID, testSignIn)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(changes.all(), listed) {
		if time.Now().After(deadline) {
			t.Fatalf("notified %+v after the read, want %+v", changes.all(), listed)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, want := m.HeldRepoNames(t.Context(), rec.ID, testSignIn), []string{"bob/one", "bob/two"}; !slices.Equal(got, want) {
		t.Errorf("HeldRepoNames(%q) = %v, want %v", testSignIn, got, want)
	}
	if got := m.HeldRepoNames(t.Context(), rec.ID, "someone-else"); got != nil {
		t.Errorf("HeldRepoNames(another account) = %v, want nil", got)
	}
}

// gatedRepos is fakeCore with a repository list that answers once release closes.
type gatedRepos struct {
	*fakeCore
	release chan struct{}
}

func (g *gatedRepos) ListRepos(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return forgeapi.Page[forgeapi.Repository]{}, ctx.Err()
	}
	return g.fakeCore.ListRepos(ctx, opts...)
}

// HeldRepoNames answers at once while the forge has not answered, and the
// names once the read it started lands.
func TestHeldRepoNames_NeverWaitsOnTheForge(t *testing.T) {
	core := &gatedRepos{release: make(chan struct{}), fakeCore: &fakeCore{repos: forgeapi.Page[forgeapi.Repository]{
		Items: []forgeapi.Repository{{Ref: forgeapi.RepoRef{DisplayPath: "bob/one"}}},
	}}}
	m := recordManager(t, core)

	done := make(chan []string, 1)
	go func() { done <- m.HeldRepoNames(t.Context(), "github:github.com", testSignIn) }()
	select {
	case names := <-done:
		if names != nil {
			t.Errorf("HeldRepoNames() before any read = %v, want nil", names)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HeldRepoNames() waited on the forge's repository list")
	}

	close(core.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := m.HeldRepoNames(t.Context(), "github:github.com", testSignIn)
		if slices.Equal(got, []string{"bob/one"}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("HeldRepoNames() after the read landed = %v, want [bob/one]", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
