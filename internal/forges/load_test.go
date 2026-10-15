package forges

// Load reads the records file and the credential store, and each failure it
// meets has its own scope: a malformed record drops only itself, a setting the
// library refuses only its row, and a store read every row.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/slogx/capture"
)

// countingCores builds a closable fake per loaded sign-in and keeps them.
type countingCores struct {
	built []*whoCore
	mu    sync.Mutex
}

func (c *countingCores) newCore(*connectionSpec, []forgeapi.Option) (forgeapi.Core, error) {
	core := &whoCore{}
	c.mu.Lock()
	c.built = append(c.built, core)
	c.mu.Unlock()
	return core, nil
}

// failingLoads is a store whose Load of key fails on the calls fail names,
// counting from one.
type failingLoads struct {
	*creds.FileStore
	fail  map[int]bool
	key   string
	calls int
	mu    sync.Mutex
}

func (s *failingLoads) Load(key string) (creds.Record, bool, error) {
	if key == s.key {
		s.mu.Lock()
		s.calls++
		n := s.calls
		s.mu.Unlock()
		if s.fail[n] {
			return creds.Record{}, false, errors.New("the credential file could not be read")
		}
	}
	return s.FileStore.Load(key)
}

func (s *failingLoads) loads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *failingLoads) resetLoads() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = 0
}

func TestLoad_EachFailureHasItsScope(t *testing.T) {
	isolateGit(t)
	cfg := t.TempDir()
	gh := githubRecord()
	owned := specFor(KindGitHub, "ghe.test")
	owned.OwnerScopes = []string{"bob"}
	for i := range 20 {
		owned.OwnerScopes = append(owned.OwnerScopes, fmt.Sprintf("org-%02d", i))
	}
	owned.OwnerScopes = append(owned.OwnerScopes, "ORG-00")
	proxied := specFor(KindGitea, "gitea.test")
	proxied.Proxy = "ftp://proxy"
	bare := specFor(KindGitea, "plain.test")
	seedConnections(t, cfg, "bob", gh, owned, proxied, bare)
	pathHost := specFor(KindGitea, "x.test/p")
	bigHost := specFor(KindGitea, strings.Repeat("a", 9<<10))
	store := openTestStore(t, cfg)
	devKey := MakeID(KindGitLab, "gitlab.com")
	for _, k := range []string{slotKey(pathHost.ID, 0), slotKey(bigHost.ID, 0), devKey} {
		if err := store.Save(k, staticCredential(&gh, "bob", "stray")); err != nil {
			t.Fatal(err)
		}
	}
	recs := readRecords(t, cfg)
	recs = append(recs, connectionRecord{connectionSpec: pathHost}, connectionRecord{connectionSpec: bigHost},
		connectionRecord{ID: devKey, Kind: KindGitLab, Host: "gitlab.com"})
	writeRecords(t, cfg, recs...)
	logs := capture.Default(t)
	cores := &countingCores{}
	m, _ := newManager(t, cfg, nil)
	m.newCore = cores.newCore
	m.Load(t.Context())

	if got := viewFor(t, m, gh.ID); got.Status != ForgeConnected {
		t.Errorf("the valid record = %+v, want connected", got)
	}
	if got := viewFor(t, m, owned.ID); got.Status != ForgeConnected || len(got.OwnerScopes) != 20 ||
		slices.Contains(got.OwnerScopes, "ORG-00") || slices.Contains(got.OwnerScopes, "bob") {
		t.Errorf("the owners record = %+v, want connected with its 20 owners, the repeat and the account's own dropped", got)
	}
	if !logs.Contains("forges: a connection's stored owners were normalised") {
		t.Error("no Warn line names the normalised owners")
	}
	ids := []string{}
	for _, f := range m.List() {
		ids = append(ids, f.ID)
	}
	for _, gone := range []string{pathHost.ID, bigHost.ID, devKey} {
		if slices.Contains(ids, gone) {
			t.Errorf("List() holds %s, want the malformed record left out", gone)
		}
	}
	if n := strings.Count(string(rendered(t, logs)), "dropping an invalid connection record"); n != 3 {
		t.Errorf("Load logged %d dropped records, want 3", n)
	}
	keys, err := store.Keys()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(keys)
	want := []string{slotKey(gh.ID, 0), slotKey(owned.ID, 0), slotKey(proxied.ID, 0), slotKey(bare.ID, 0)}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("store keys after Load = %q, want the admitted records' active slots %q", keys, want)
	}
	if got := viewFor(t, m, proxied.ID); got.Status != ForgeReconnect || got.Error == nil || !strings.Contains(got.Error.Message, "proxy") {
		t.Errorf("the record with a refused proxy = %+v, want reconnect naming the setting", got)
	}
	if _, err := m.reader(proxied.ID); err == nil {
		t.Error("reader() of the refused-settings row = a handle, want none")
	}
	if got := helperValuesFor(t, "https://gitea.test"); len(got) != 0 {
		t.Errorf("credential.https://gitea.test.helper = %q, want no registration for an unusable row", got)
	}

	before := fileBytes(t, m.records.path)
	if _, err := m.check(t.Context(), bare.ID, checkBudget); err != nil {
		t.Fatal(err)
	}
	if fileBytes(t, m.records.path) != before {
		t.Error("a check of a record with no owners rewrote the file, want it unchanged")
	}
	if _, err := m.check(t.Context(), owned.ID, checkBudget); err != nil {
		t.Fatal(err)
	}
	for _, r := range readRecords(t, cfg) {
		if r.ID == owned.ID && len(r.OwnerScopes) != 20 {
			t.Errorf("the owners record after its first check holds %d owners, want the kept 20 written", len(r.OwnerScopes))
		}
	}

	for name, fail := range map[string]map[int]bool{
		"the store read":              {1: true},
		"the credential state's read": {2: true},
	} {
		t.Run(name, func(t *testing.T) {
			stray := slotKey(gh.ID, 1)
			if err := store.Save(stray, staticCredential(&gh, "bob", "stray")); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			m.store = &failingLoads{FileStore: store, key: slotKey(bare.ID, 0), fail: fail}
			m.mu.Unlock()
			built := len(cores.built)
			changes := &changeLog{}
			m.notify = changes.notify
			m.Load(t.Context())

			for _, f := range m.List() {
				if f.Status != ForgeUnusable || f.Error == nil || !strings.Contains(f.Error.Message, "the forge credential store could not be read") {
					t.Errorf("row %s = %+v, want unusable naming the store read", f.ID, f)
				}
			}
			cores.mu.Lock()
			for _, c := range cores.built[built:] {
				if c.closed.Load() == 0 {
					t.Error("a client built for the failed Load was left open")
				}
			}
			cores.mu.Unlock()
			if k, _ := store.Keys(); !slices.Contains(k, stray) {
				t.Errorf("store keys = %q, want nothing swept by a failed Load", k)
			}
			NewKeeper(m).pass(t.Context())
			if got := viewFor(t, m, gh.ID); got.Status != ForgeConnected {
				t.Errorf("row after the next pass = %+v, want connected", got)
			}
			if got := changes.all(); len(got) == 0 || got[0] != (Change{}) {
				t.Errorf("the heal notified %+v, want one change of every row first", got)
			}
		})
	}
}
