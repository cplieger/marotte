package forges

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

const rotatedAnswer = `{"access_token":"token-new","token_type":"bearer","scope":"repo","expires_in":28800,` +
	`"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`

// tokenServer is a TLS stand-in for github.com: its OAuth token endpoint answers
// body to every refresh, and it counts every request it receives on any path.
type tokenServer struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newTokenServer(t *testing.T, body string) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.hits.Add(1)
		if r.URL.Path != "/login/oauth/access_token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

// record is a GitHub connection record addressing the server through its own CA.
func (ts *tokenServer) record() connectionRecord {
	host := strings.TrimPrefix(ts.srv.URL, "https://")
	return connectionRecord{
		ID: MakeID(KindGitHub, host), Kind: KindGitHub, Host: host, WebBaseURL: ts.srv.URL,
		CAPEM:            string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.srv.Certificate().Raw})),
		PrivateAddresses: true,
	}
}

// rotatingCredential is an eight-hour GitHub token issued for webBase that
// expires in expiresIn, with a month left on its refresh token.
func rotatingCredential(webBase string, expiresIn time.Duration) creds.Record {
	now := time.Now().Truncate(time.Second)
	return creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: webBase, Kind: forgeapi.CredKindRotatingOAuth,
		Token: "token-old", Issued: now.Add(-8 * time.Hour), Expiry: now.Add(expiresIn),
		RefreshToken: "refresh-old", RefreshExpiry: now.Add(30 * 24 * time.Hour), ClientID: "client", Account: "alice",
	}
}

// keeperManager is a manager over a fresh config directory holding rec and its
// stored credential cred, with the real family clients.
func keeperManager(t *testing.T, rec *connectionRecord, cred *creds.Record) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	if m.store == nil {
		t.Fatalf("Setup: open the credential store: %s", m.storeReason)
	}
	if err := m.store.Save(rec.ID, *cred); err != nil {
		t.Fatalf("Setup: save the credential: %v", err)
	}
	saveRecords(t, m.conns, *rec)
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	return m
}

func TestKeeper_RefreshesARotatingRecordBeforeExpiry(t *testing.T) {
	ts := newTokenServer(t, rotatedAnswer)
	rec := ts.record()
	cred := rotatingCredential(ts.srv.URL, time.Minute)
	m := keeperManager(t, &rec, &cred)

	NewKeeper(m, nil).pass(t.Context())

	got, ok, err := m.store.Load(rec.ID)
	if err != nil || !ok || got.Token != "token-new" || got.RefreshToken != "refresh-new" {
		t.Errorf("stored credential after a keeper pass = token %q, refresh %q (ok %v, err %v); want the rotated pair token-new, refresh-new",
			got.Token, got.RefreshToken, ok, err)
	}
	if n := ts.hits.Load(); n != 1 {
		t.Errorf("the forge saw %d requests during the pass, want the one refresh", n)
	}
	if row := m.Get(rec.ID); row.ReconnectRequired || !row.Connected {
		t.Errorf("row after a successful refresh = %+v, want connected and not reconnect-required", row)
	}
}

func TestKeeper_LeavesAStaticRecordAlone(t *testing.T) {
	ts := newTokenServer(t, `{"error":"bad_refresh_token"}`)
	rec := ts.record()
	cred := creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: ts.srv.URL, Kind: forgeapi.CredKindStaticPAT,
		Token: "pat", Issued: time.Now().Truncate(time.Second), Account: "alice",
	}
	m := keeperManager(t, &rec, &cred)
	changes := 0

	NewKeeper(m, func(context.Context) { changes++ }).pass(t.Context())

	if n := ts.hits.Load(); n != 0 {
		t.Errorf("a pass over a token connection sent %d requests to the forge, want none", n)
	}
	if got, _, err := m.store.Load(rec.ID); err != nil || got.Token != "pat" || got.Usability != creds.UsabilityUnmarked {
		t.Errorf("stored credential after the pass = token %q, usability %v (err %v); want pat, unmarked", got.Token, got.Usability, err)
	}
	if row := m.Get(rec.ID); row.ReconnectRequired || !row.Connected || changes != 0 {
		t.Errorf("row after the pass = %+v with %d changes announced, want it untouched and nothing announced", row, changes)
	}
}

// A record whose credential the store lacks reads reconnect-required from the
// rebuild itself, so a keeper pass after a refresh announces no change.
func TestKeeper_ARecordWithNoCredentialIsNotReannounced(t *testing.T) {
	ts := newTokenServer(t, `{"error":"bad_refresh_token"}`)
	rec := ts.record()
	cred := creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: ts.srv.URL, Kind: forgeapi.CredKindStaticPAT,
		Token: "pat", Issued: time.Now().Truncate(time.Second), Account: "alice",
	}
	m := keeperManager(t, &rec, &cred)
	if err := m.store.Delete(rec.ID); err != nil {
		t.Fatalf("Setup: Delete() = %v", err)
	}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	if row := m.Get(rec.ID); !row.ReconnectRequired || row.ErrorCode != forgeapi.CodeReconnectRequired {
		t.Errorf("row with no stored credential = %+v, want reconnect-required", row)
	}
	changes := 0
	k := NewKeeper(m, func(context.Context) { changes++ })
	k.pass(t.Context())
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	k.pass(t.Context())
	if changes != 0 {
		t.Errorf("two keeper passes around a Refresh announced %d changes, want none: the row already said it", changes)
	}
}

func TestKeeper_TerminalRefreshMarksTheRowReconnectRequired(t *testing.T) {
	ts := newTokenServer(t, `{"error":"bad_refresh_token","error_description":"The refresh token passed is incorrect or expired."}`)
	rec := ts.record()
	cred := rotatingCredential(ts.srv.URL, time.Minute)
	m := keeperManager(t, &rec, &cred)
	changes := 0
	k := NewKeeper(m, func(context.Context) { changes++ })

	k.pass(t.Context())

	row := m.Get(rec.ID)
	if !row.ReconnectRequired || row.Connected || row.LastError == "" || row.ErrorCode != forgeapi.CodeReconnectRequired {
		t.Errorf("row after a refused refresh = %+v, want reconnect-required, disconnected, with the reason and its code", row)
	}
	if changes != 1 {
		t.Errorf("a refused refresh announced %d changes, want 1", changes)
	}
	k.pass(t.Context())
	if changes != 1 {
		t.Errorf("a second pass over the already-marked row announced %d changes in total, want still 1", changes)
	}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}
	if row := m.Get(rec.ID); !row.ReconnectRequired || row.Connected {
		t.Errorf("row after a refresh of the list = %+v, want the reconnect state kept from the marked record", row)
	}
}

func TestKeeper_FailedRefreshOfAnExpiredTokenLeavesTheRowUnmarked(t *testing.T) {
	ts := newTokenServer(t, `{"error":"temporarily_unavailable"}`)
	rec := ts.record()
	cred := rotatingCredential(ts.srv.URL, -time.Minute)
	m := keeperManager(t, &rec, &cred)
	changes := 0

	NewKeeper(m, func(context.Context) { changes++ }).pass(t.Context())

	if n := ts.hits.Load(); n == 0 {
		t.Fatal("the pass sent no refresh for an expired token; the fixture is not exercising a failed refresh")
	}
	if row := m.Get(rec.ID); row.ReconnectRequired || changes != 0 {
		t.Errorf("row after a refresh the forge failed without refusing = %+v with %d changes announced, "+
			"want it not reconnect-required and nothing announced: the next pass refreshes again", row, changes)
	}
}

// cursorCore is a fake client whose budget state reports the rotation cursor
// set last.
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

func giteaCursorRecord() connectionRecord {
	return connectionRecord{
		ID: "gitea:git.example.com", Kind: KindGitea, Host: "git.example.com", WebBaseURL: "https://git.example.com",
		HelperValue: "/'usr/bin/marotte' git-credential --config-dir '/config'", RotationCursor: "repo-1:4",
		OwnerScopes: []string{"acme"},
	}
}

func TestKeeper_PersistsAMovedRotationCursor(t *testing.T) {
	rec := giteaCursorRecord()
	core := &cursorCore{cursor: "repo-1:4"}
	m := recordManagerFor(t, &rec, core)
	k := NewKeeper(m, nil)
	pre := storedRecord(t, m, rec.ID)

	core.set("repo-7:12")
	k.pass(t.Context())

	got := storedRecord(t, m, rec.ID)
	if got.RotationCursor != "repo-7:12" {
		t.Errorf("stored rotation cursor after the client moved it = %q, want repo-7:12", got.RotationCursor)
	}
	if got.HelperValue != pre.HelperValue || len(got.OwnerScopes) != 1 || got.OwnerScopes[0] != "acme" {
		t.Errorf("the cursor write-back changed the record's other fields: %+v, was %+v", got, pre)
	}

	path := m.conns.path
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
	rec := giteaCursorRecord()
	core := &cursorCore{cursor: "repo-1:4"}
	m := recordManagerFor(t, &rec, core)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go NewKeeper(m, nil).Run(ctx)
		synctest.Wait()

		core.set("repo-2:9")
		time.Sleep(keeperInterval)
		synctest.Wait()
		if got := storedRecord(t, m, rec.ID).RotationCursor; got != "repo-2:9" {
			t.Errorf("stored cursor one interval after it moved = %q, want repo-2:9", got)
		}
	})
}

func TestKeeper_StopsOnContextCancel(t *testing.T) {
	m := recordManager(t, &fakeCore{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		NewKeeper(m, nil).Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of its context ending")
	}
}

// whoamiCore is a fake client whose identity read answers err.
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

// heldWhoamiCore answers Whoami once release closes, or its context ends.
type heldWhoamiCore struct {
	fakeCore
	release chan struct{}
}

func (c *heldWhoamiCore) Whoami(ctx context.Context) (forgeapi.Account, error) {
	select {
	case <-c.release:
		return c.fakeCore.Whoami(ctx)
	case <-ctx.Done():
		return forgeapi.Account{}, ctx.Err()
	}
}

// A probe caller leaving does not fail another waiting on the same probe, and
// the verdict still lands on the row.
func TestManagerProbe_ASharedProbeSurvivesTheFirstCallerLeaving(t *testing.T) {
	core := &heldWhoamiCore{}
	m := recordManager(t, core)
	synctest.Test(t, func(t *testing.T) {
		core.release = make(chan struct{})
		first, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() { _ = m.Probe(first, "github:github.com") })
		synctest.Wait()
		var err error
		wg.Go(func() { err = m.Probe(t.Context(), "github:github.com") })
		synctest.Wait()
		cancel()
		synctest.Wait()
		close(core.release)
		wg.Wait()

		if err != nil {
			t.Errorf("Probe() of a caller still waiting after the first left = %v, want nil", err)
		}
		if f := m.Get("github:github.com"); f == nil || !f.Connected || f.LastError != "" {
			t.Errorf("row after the shared probe = %+v, want connected", f)
		}
	})
}

func TestManagerProbe_ReconnectRefusalMarksTheRow(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{
		Code: forgeapi.CodeReconnectRequired, Kind: forgeapi.KindUnauthorized, Message: "reconnect the forge",
	}}
	m := recordManager(t, core)

	if err := m.Probe(t.Context(), "github:github.com"); err == nil {
		t.Fatal("Probe() over a reconnect refusal = nil, want the refusal")
	}
	if row := m.Get("github:github.com"); !row.ReconnectRequired || row.Connected || row.ErrorCode != forgeapi.CodeReconnectRequired {
		t.Errorf("row after a reconnect refusal = %+v, want reconnect-required, disconnected and coded so", row)
	}

	core.err = &forgeapi.Error{Kind: forgeapi.KindTransient, Message: "upstream timed out"}
	_ = m.Probe(t.Context(), "github:github.com")
	if row := m.Get("github:github.com"); !row.ReconnectRequired {
		t.Errorf("row after a transient probe failure = %+v, want the reconnect state kept", row)
	}

	core.err = nil
	if err := m.Probe(t.Context(), "github:github.com"); err != nil {
		t.Fatalf("Probe() after a new sign-in = %v", err)
	}
	if row := m.Get("github:github.com"); row.ReconnectRequired || !row.Connected {
		t.Errorf("row after a successful probe = %+v, want the reconnect state cleared", row)
	}
}

func TestManagerList_MarkedCredentialReadsReconnectRequired(t *testing.T) {
	rec := githubRecord()
	cred := creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: rec.webBase(), Kind: forgeapi.CredKindStaticPAT,
		Token: "pat", Issued: time.Now(), Account: "alice", Usability: creds.UsabilityReconnectRequired,
	}
	m := keeperManager(t, &rec, &cred)

	row := m.Get(rec.ID)
	if !row.ReconnectRequired || row.Connected || row.LastError == "" || row.ErrorCode != forgeapi.CodeReconnectRequired {
		t.Errorf("row of a record marked reconnect-required = %+v, want reconnect-required, disconnected, with the reason and its code", row)
	}
}

func TestConfiguredForge_AlwaysStatesReconnectRequired(t *testing.T) {
	data, err := json.Marshal(ConfiguredForge{ID: "github:github.com", Kind: KindGitHub, Host: "github.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reconnect_required":false`) {
		t.Errorf("json.Marshal(a usable row) = %s, want reconnect_required stated as false", data)
	}
}
