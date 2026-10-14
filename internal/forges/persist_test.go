package forges

// What a connection is lives in the records file and the credential store,
// written through one door per change, so a restart and git's helper read what
// the live rows show.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
)

// loginCore signs in as the token it was built for.
type loginCore struct {
	whoCore
	login string
}

func (c *loginCore) Whoami(ctx context.Context) (forgeapi.Account, error) {
	if _, err := c.whoCore.Whoami(ctx); err != nil {
		return forgeapi.Account{}, err
	}
	return forgeapi.Account{Login: c.login}, nil
}

func loginCores(_ *connectionSpec, opts []forgeapi.Option) (forgeapi.Core, error) {
	token, err := forgeapi.Resolve(opts...).Credential.Token(context.Background())
	if err != nil {
		return nil, err
	}
	return &loginCore{login: token}, nil
}

func signInNow(t *testing.T, m *Manager, spec connectionSpec, token string) {
	t.Helper()
	if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &spec, new(patFor(spec.webBase(), token))); err != nil {
		t.Fatalf("signIn(%s) = %v", token, err)
	}
}

func setOwnersNow(t *testing.T, m *Manager, id string, owners ...string) {
	t.Helper()
	if _, err := m.setOwners(t.Context(), m.deadlines.from(time.Now()), id, owners); err != nil {
		t.Fatalf("setOwners(%q) = %v", owners, err)
	}
}

func TestReader_CannotRefreshTheCredential(t *testing.T) {
	ts := newTokenServer(t, rotatedAnswer)
	spec := ts.record()
	cred := rotatingCredential(ts.srv.URL, time.Minute)
	m, _, _ := keeperManager(t, &spec, &cred)
	credFile := filepath.Join(m.configDir, credentialStoreDir, "credentials.json")
	before := fileBytes(t, credFile)

	if rec := getRoute(t, repoRoutes(m), "/api/forges/"+spec.ID+"/repos?refresh=1"); rec.Code != http.StatusOK {
		t.Fatalf("GET repos = %d %s", rec.Code, rec.Body)
	}
	if fileBytes(t, credFile) != before || ts.refreshes.Load() != 0 {
		t.Errorf("a listing read through a refresh-due credential sent %d refreshes and rewrote the store, want neither",
			ts.refreshes.Load())
	}
	NewKeeper(m).pass(t.Context())
	if fileBytes(t, credFile) == before || ts.refreshes.Load() != 1 {
		t.Errorf("a keeper pass sent %d refreshes, want the one that rotates the credential", ts.refreshes.Load())
	}
}

func TestManager_WritesThroughAndARestartLoadsTheSameState(t *testing.T) {
	isolateGit(t)
	m, _ := newManager(t, t.TempDir(), nil)
	m.newCore = loginCores
	m.Load(t.Context())
	spec := githubRecord()
	signInNow(t, m, spec, "alice")
	setOwnersNow(t, m, spec.ID, "acme", "beta")
	if got := readRecords(t, m.configDir); len(got) != 1 || !slices.Equal(got[0].OwnerScopes, []string{"acme", "beta"}) {
		t.Errorf("records after setting owners = %+v, want [acme beta] written before any later write", got)
	}
	cur, _, _ := m.current(spec.ID)
	cur.conn.(*loginCore).answer(errForbidden)
	if _, err := m.check(t.Context(), spec.ID, checkBudget); err != nil {
		t.Fatal(err)
	}

	again := restarted(t, m)
	was, _, _ := m.current(spec.ID)
	now, _, _ := again.current(spec.ID)
	if !reflect.DeepEqual(now.durable, was.durable) || now.durable.Standing != standingRefused || now.durable.Slot != 0 {
		t.Errorf("record after a restart = %+v, want the one written %+v, standing refused at slot 0", now.durable, was.durable)
	}
	if v, w := viewFor(t, again, spec.ID), viewFor(t, m, spec.ID); v.Status != w.Status || v.Account != w.Account ||
		!slices.Equal(v.OwnerScopes, w.OwnerScopes) || v.Username != "alice" {
		t.Errorf("row after a restart = %+v, want the live row's status, account and owners %+v", v, w)
	}

	cur, _, _ = m.current(spec.ID)
	if err := m.commit(t.Context(), spec.ID, withCursor(cur, "repo-3:1")); err != nil {
		t.Fatal(err)
	}
	signInNow(t, m, spec, "alice")
	got := readRecords(t, m.configDir)[0]
	if got.SignIn == was.durable.SignIn || !slices.Equal(got.OwnerScopes, []string{"acme", "beta"}) ||
		got.RotationCursor != "repo-3:1" || got.Slot != 1 {
		t.Errorf("record after a new sign-in = %+v, want a new sign-in id at slot 1 with the owners and cursor kept", got)
	}

	t.Run("an account the owners named", func(t *testing.T) {
		setOwnersNow(t, m, spec.ID, "acme", "carol")
		signInNow(t, m, spec, "carol")
		if got := viewFor(t, m, spec.ID).OwnerScopes; !slices.Equal(got, []string{"acme"}) {
			t.Errorf("owners after signing in as carol = %q, want carol dropped: the owner scope reads her already", got)
		}
		h, err := m.reader(spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		owners := 0
		for _, sc := range cycleScopes(&h, true) {
			if strings.EqualFold(sc.Owner, "carol") {
				owners++
			}
		}
		if owners != 1 {
			t.Errorf("a present cycle reads carol in %d scopes, want one", owners)
		}
	})
}

func withCursor(c *connection, cursor string) *connection {
	next := *c
	next.spec.RotationCursor = cursor
	return &next
}

func TestManager_TwoConnectionsWritingAtOnceKeepBothRecords_Concurrent(t *testing.T) {
	isolateGit(t)
	cfg := t.TempDir()
	gh, gl := githubRecord(), gitlabRecord()
	seedConnections(t, cfg, "bob", gh, gl)
	m, _ := loadedManager(t, cfg, &whoCore{})

	// The first write holds until a second write arrives (or 300ms pass), and
	// the second lands after the first: two writers that both snapshot before
	// either installs leave the second's stale copy of the first's record.
	m.persist.Lock()
	real := m.records.writeFile
	var mu sync.Mutex
	calls := 0
	second, firstDone := make(chan struct{}), make(chan struct{})
	m.records.writeFile = func(ctx context.Context, path string, data []byte) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			select {
			case <-second:
			case <-time.After(300 * time.Millisecond):
			}
			defer close(firstDone)
			return real(ctx, path, data)
		}
		if n == 2 {
			close(second)
		}
		<-firstDone
		return real(ctx, path, data)
	}
	m.persist.Unlock()

	var wg sync.WaitGroup
	for i, id := range []string{gh.ID, gl.ID} {
		wg.Go(func() {
			if _, err := m.setOwners(t.Context(), m.deadlines.from(time.Now()), id, []string{fmt.Sprintf("org-%d", i)}); err != nil {
				t.Errorf("setOwners(%s) = %v", id, err)
			}
		})
	}
	wg.Wait()

	recs := readRecords(t, cfg)
	got := map[string][]string{}
	for _, r := range recs {
		got[r.ID] = r.OwnerScopes
	}
	if len(recs) != 2 || !slices.Equal(got[gh.ID], []string{"org-0"}) || !slices.Equal(got[gl.ID], []string{"org-1"}) {
		t.Errorf("records after two connections' writes = %+v, want %s on [org-0] and %s on [org-1]", recs, gh.ID, gl.ID)
	}
}

func TestManager_SignOutThenSignInOnOneIDEndsSignedIn(t *testing.T) {
	isolateGit(t)
	m, _ := loadedManager(t, t.TempDir(), &whoCore{})
	spec := githubRecord()
	signInNow(t, m, spec, "token-a")
	s := logStore(t, m)
	s.onDelete = func(string) error {
		time.Sleep(100 * time.Millisecond) // a slow disk, the fixture
		return nil
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := m.signOut(t.Context(), m.deadlines.from(time.Now()), spec.ID); err != nil {
			t.Errorf("signOut() = %v", err)
		}
	})
	waitFor(t, "the sign-out's first delete", func() bool { return len(s.log()) > 0 })
	wg.Go(func() { signInNow(t, m, spec, "token-b") })
	wg.Wait()

	k0, k1 := slotKey(spec.ID, 0), slotKey(spec.ID, 1)
	if got, want := s.log(), []string{"delete " + k0, "delete " + k1, "save " + k0, "delete " + k1}; !slices.Equal(got, want) {
		t.Errorf("store calls = %q, want %q: the sign-in saves only after both of the sign-out's deletes", got, want)
	}
	cred, ok, err := openTestStore(t, m.configDir).Load(k0)
	if err != nil || !ok || cred.Token != "token-b" {
		t.Errorf("credential after the sign-out then the sign-in = %+v (%v, %v), want token-b", cred, ok, err)
	}
	if _, ok := m.view(spec.ID); !ok {
		t.Error("no row after the sign-in, want it signed in")
	}
}

func TestSignOut_AFailedRecordWriteKeepsEverything(t *testing.T) {
	h := newConnectHarness(t, aliceWire())
	id := h.signedIn(t, KindGitHub, "", "ghp_secret", "")
	failingWrites(t, h.m)

	if rec := h.do(t, http.MethodDelete, "/api/forges/"+id, ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("DELETE with the records write failing = %d %s, want 500", rec.Code, rec.Body)
	}
	if keys := h.storedKeys(t); !slices.Equal(keys, []string{slotKey(id, 0)}) {
		t.Errorf("store keys = %q, want the active slot kept", keys)
	}
	if _, err := h.m.reader(id); err != nil {
		t.Errorf("reader() = %v, want the connection still serving", err)
	}
	if out := helperAnswer(t, h.cfgDir, "github.com"); !strings.Contains(out, "password=ghp_secret") {
		t.Errorf("the git helper answered %q, want the credential still handed to git", out)
	}
}

func TestCredentialHelper_FollowsTheDurableSlot(t *testing.T) {
	t.Run("the switch is the records write", func(t *testing.T) {
		isolateGit(t)
		m, _ := loadedManager(t, t.TempDir(), &whoCore{})
		spec := githubRecord()
		signInNow(t, m, spec, "token-a")
		s := logStore(t, m)
		m.persist.Lock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			signInNow(t, m, spec, "token-b")
		}()
		waitFor(t, "the new slot's save", func() bool { return slices.Contains(s.log(), "save "+slotKey(spec.ID, 1)) })
		if out := helperAnswer(t, m.configDir, "github.com"); !strings.Contains(out, "password=token-a") {
			t.Errorf("the helper answered %q before the records write, want A's token", out)
		}
		m.persist.Unlock()
		<-done
		if out := helperAnswer(t, m.configDir, "github.com"); !strings.Contains(out, "password=token-b") {
			t.Errorf("the helper answered %q after the records write, want B's token", out)
		}
	})
	t.Run("a failed records write", func(t *testing.T) {
		isolateGit(t)
		m, _ := loadedManager(t, t.TempDir(), &whoCore{})
		spec := githubRecord()
		signInNow(t, m, spec, "token-a")
		before := viewFor(t, m, spec.ID)
		failingWrites(t, m)

		if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &spec, new(patFor(spec.webBase(), "token-b"))); err == nil {
			t.Fatal("signIn() with the records write failing = nil, want the failure")
		}
		if got := viewFor(t, m, spec.ID); got.Account != before.Account {
			t.Errorf("row after the failed sign-in = %+v, want A's %+v", got, before)
		}
		store := openTestStore(t, m.configDir)
		if keys, _ := store.Keys(); !slices.Equal(keys, []string{slotKey(spec.ID, 0)}) {
			t.Errorf("store keys = %q, want A's slot kept and the new slot removed", keys)
		}
		if out := helperAnswer(t, m.configDir, "github.com"); !strings.Contains(out, "password=token-a") {
			t.Errorf("the helper answered %q, want A's token", out)
		}
	})
}

func TestLoad_SweepsCredentialsNoRecordOwns(t *testing.T) {
	seed := func(t *testing.T) (cfg, kept string, swept []string) {
		t.Helper()
		cfg = t.TempDir()
		spec := githubRecord()
		seedConnections(t, cfg, "bob", spec)
		store := openTestStore(t, cfg)
		gone := MakeID(KindGitLab, "gitlab.com")
		swept = []string{slotKey(spec.ID, 1), slotKey(gone, 0), slotKey(gone, 1), spec.ID}
		for _, k := range swept {
			if err := store.Save(k, staticCredential(&spec, "bob", "stray")); err != nil {
				t.Fatal(err)
			}
		}
		return cfg, slotKey(spec.ID, 0), swept
	}
	keysOf := func(t *testing.T, cfg string) []string {
		t.Helper()
		keys, err := openTestStore(t, cfg).Keys()
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(keys)
		return keys
	}

	t.Run("a verified file", func(t *testing.T) {
		isolateGit(t)
		cfg, kept, _ := seed(t)
		loadedManager(t, cfg, &whoCore{})
		if got := keysOf(t, cfg); !slices.Equal(got, []string{kept}) {
			t.Errorf("keys after a verified Load = %q, want only the active slot %q", got, kept)
		}
	})
	for name, prepare := range map[string]func(t *testing.T, cfg string){
		"an unverified file": func(t *testing.T, _ string) {
			orig := enforceFileMode
			t.Cleanup(func() { enforceFileMode = orig })
			enforceFileMode = func(string, os.FileMode) (os.FileMode, error) { return 0o660, atomicfile.ErrModeNotStored }
		},
		"a missing file": func(t *testing.T, cfg string) {
			if err := os.Remove(filepath.Join(cfg, connectionsFileName)); err != nil {
				t.Fatal(err)
			}
		},
		"a file moved aside": func(t *testing.T, cfg string) {
			if err := os.WriteFile(filepath.Join(cfg, connectionsFileName), []byte(`{"connections":[`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolateGit(t)
			cfg, kept, swept := seed(t)
			prepare(t, cfg)
			loadedManager(t, cfg, &whoCore{})
			want := append([]string{kept}, swept...)
			slices.Sort(want)
			if got := keysOf(t, cfg); !slices.Equal(got, want) {
				t.Errorf("keys after a Load over %s = %q, want every key kept %q", name, got, want)
			}
		})
	}
}
