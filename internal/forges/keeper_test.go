package forges

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

const rotatedAnswer = `{"access_token":"token-new","token_type":"bearer","scope":"repo","expires_in":28800,` +
	`"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`

// tokenServer is a TLS stand-in for github.com: its OAuth token endpoint
// answers body to every refresh, its identity read answers alice, and it counts
// each.
type tokenServer struct {
	srv       *httptest.Server
	refreshes atomic.Int32
	users     atomic.Int32
}

func newTokenServer(t *testing.T, body string) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			ts.refreshes.Add(1)
			_, _ = io.WriteString(w, body)
		case "/api/v3/user":
			ts.users.Add(1)
			_, _ = io.WriteString(w, `{"login":"alice"}`)
		case "/api/v3/user/repos":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *tokenServer) record() connectionSpec {
	host := strings.TrimPrefix(ts.srv.URL, "https://")
	s := specFor(KindGitHub, host)
	s.WebBaseURL = ts.srv.URL
	s.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.srv.Certificate().Raw}))
	s.PrivateAddresses = true
	return s
}

func rotatingCredential(webBase string, expiresIn time.Duration) creds.Record {
	now := time.Now().Truncate(time.Second)
	return creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: webBase, Kind: forgeapi.CredKindRotatingOAuth,
		Token: "token-old", Issued: now.Add(-8 * time.Hour), Expiry: now.Add(expiresIn),
		RefreshToken: "refresh-old", RefreshExpiry: now.Add(30 * 24 * time.Hour), ClientID: "client", Account: "alice",
	}
}

// keeperManager is a loaded manager over the real family clients holding spec
// with cred in its active slot, with the store it reads and its change log.
func keeperManager(t *testing.T, spec *connectionSpec, cred *creds.Record) (*Manager, *creds.FileStore, *changeLog) {
	t.Helper()
	isolateGit(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, cred.Account, *spec)
	store := openTestStore(t, cfg)
	if err := store.Save(slotKey(spec.ID, spec.Slot), *cred); err != nil {
		t.Fatalf("Setup: save the credential: %v", err)
	}
	m, log := loadedManager(t, cfg, nil)
	return m, store, log
}

func TestKeeper_RefreshesARotatingRecordBeforeExpiry(t *testing.T) {
	ts := newTokenServer(t, rotatedAnswer)
	rec := ts.record()
	cred := rotatingCredential(ts.srv.URL, time.Minute)
	m, store, _ := keeperManager(t, &rec, &cred)

	NewKeeper(m).pass(t.Context())

	got, ok, err := store.Load(slotKey(rec.ID, 0))
	if err != nil || !ok || got.Token != "token-new" || got.RefreshToken != "refresh-new" {
		t.Errorf("stored credential after a keeper pass = token %q, refresh %q (ok %v, err %v); want the rotated pair token-new, refresh-new",
			got.Token, got.RefreshToken, ok, err)
	}
	if n := ts.refreshes.Load(); n != 1 {
		t.Errorf("the forge saw %d refreshes during the pass, want 1", n)
	}
	if row := viewFor(t, m, rec.ID); row.Status != ForgeConnected {
		t.Errorf("row after a successful refresh = %+v, want connected", row)
	}
}

func TestKeeper_LeavesAStaticRecordAlone(t *testing.T) {
	ts := newTokenServer(t, `{"error":"bad_refresh_token"}`)
	rec := ts.record()
	cred := creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: ts.srv.URL, Kind: forgeapi.CredKindStaticPAT,
		Token: "pat", Issued: time.Now().Truncate(time.Second), Account: "alice",
	}
	m, store, changes := keeperManager(t, &rec, &cred)
	k := NewKeeper(m)

	k.pass(t.Context())
	k.pass(t.Context())

	if n := ts.refreshes.Load(); n != 0 {
		t.Errorf("two passes over a token connection sent %d refreshes, want none", n)
	}
	if n := ts.users.Load(); n != 2 {
		t.Errorf("two passes sent %d identity reads, want one a pass", n)
	}
	if got, _, err := store.Load(slotKey(rec.ID, 0)); err != nil || got.Token != "pat" || got.Usability != creds.UsabilityUnmarked {
		t.Errorf("stored credential after the passes = token %q, usability %v (err %v); want pat, unmarked", got.Token, got.Usability, err)
	}
	if row := viewFor(t, m, rec.ID); row.Status != ForgeConnected || len(changes.all()) != 0 {
		t.Errorf("row after the passes = %+v with %d changes announced, want it connected and nothing announced",
			row, len(changes.all()))
	}
}

// A refresh the forge failed without refusing leaves an expired token retrying,
// so the next pass refreshes again.
func TestKeeper_FailedRefreshOfAnExpiredTokenRetries(t *testing.T) {
	ts := newTokenServer(t, `{"error":"temporarily_unavailable"}`)
	rec := ts.record()
	cred := rotatingCredential(ts.srv.URL, -time.Minute)
	m, _, _ := keeperManager(t, &rec, &cred)
	k := NewKeeper(m)

	k.pass(t.Context())
	if n := ts.refreshes.Load(); n != 1 {
		t.Fatalf("the pass sent %d refreshes for an expired token, want 1; the fixture is not exercising a failed refresh", n)
	}
	if row := viewFor(t, m, rec.ID); row.Status != ForgeRetrying {
		t.Errorf("row after a refresh the forge failed without refusing = %+v, want retrying", row)
	}
	k.pass(t.Context())
	if n := ts.refreshes.Load(); n != 2 {
		t.Errorf("the second pass left the refresh count at %d, want 2: a retrying row is checked again", n)
	}
}

type cursorCore struct {
	fakeCore
	cursor forgeapi.RotationCursor
	mu     sync.Mutex
}

func (c *cursorCore) set(cursor forgeapi.RotationCursor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor = cursor
}

func (c *cursorCore) BudgetState() forgeapi.BudgetState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return forgeapi.BudgetState{Remaining: forgeapi.BudgetRemainingUnknown, RotationCursor: c.cursor}
}

func giteaCursorRecord() connectionSpec {
	s := specFor(KindGitea, "git.example.com")
	s.WebBaseURL, s.RotationCursor, s.OwnerScopes = "https://git.example.com", "repo-1:4", []string{"acme"}
	return s
}

func TestKeeper_PersistsAMovedRotationCursor(t *testing.T) {
	rec := giteaCursorRecord()
	core := &cursorCore{cursor: "repo-1:4"}
	isolateGit(t)
	m := recordManagerFor(t, &rec, core)
	k := NewKeeper(m)
	pre := storedRecord(t, m, rec.ID)

	core.set("repo-7:12")
	k.pass(t.Context())

	got := storedRecord(t, m, rec.ID)
	if got.RotationCursor != "repo-7:12" {
		t.Errorf("stored rotation cursor after the client moved it = %q, want repo-7:12", got.RotationCursor)
	}
	if got.SignIn != pre.SignIn || len(got.OwnerScopes) != 1 || got.OwnerScopes[0] != "acme" {
		t.Errorf("the cursor write-back changed the record's other fields: %+v, was %+v", got, pre)
	}

	path := m.records.path
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	k.pass(t.Context())
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A write publishes a new file by rename, so the inode tells a rewrite apart
	// even inside one tick of the filesystem's mtime clock.
	if !os.SameFile(before, after) {
		t.Errorf("a pass with the cursor unmoved rewrote %s, want no write", path)
	}
}

func TestKeeper_RunsAPassEveryInterval(t *testing.T) {
	isolateGit(t)
	rec := giteaCursorRecord()
	core := &cursorCore{cursor: "repo-1:4"}
	m := recordManagerFor(t, &rec, core)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	k := NewKeeper(m)
	k.every = 20 * time.Millisecond
	go k.Run(ctx)

	core.set("repo-2:9")
	deadline := time.Now().Add(10 * time.Second)
	for storedRecord(t, m, rec.ID).RotationCursor != "repo-2:9" {
		if time.Now().After(deadline) {
			t.Fatalf("stored cursor ten seconds after it moved = %q, want repo-2:9: no later pass ran",
				storedRecord(t, m, rec.ID).RotationCursor)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKeeper_StopsOnContextCancel(t *testing.T) {
	m := recordManager(t, &fakeCore{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		NewKeeper(m).Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of its context ending")
	}
}

type whoamiCore struct {
	fakeCore
	err error
}

func (c *whoamiCore) Whoami(ctx context.Context) (forgeapi.Account, error) {
	if c.err != nil {
		return forgeapi.Account{}, c.err
	}
	return c.fakeCore.Whoami(ctx)
}
