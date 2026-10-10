package forges

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
)

func testConnectionStore(t *testing.T) (*connectionStore, string) {
	t.Helper()
	dir := t.TempDir()
	return &connectionStore{path: filepath.Join(dir, connectionsFileName)}, dir
}

func saveRecords(t *testing.T, s *connectionStore, recs ...connectionRecord) {
	t.Helper()
	if err := s.update(t.Context(), func([]connectionRecord) []connectionRecord { return recs }); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func githubRecord() connectionRecord {
	return connectionRecord{ID: "github:github.com", Kind: KindGitHub, Host: "github.com", WebBaseURL: "https://github.com"}
}

func TestConnectionRecords_RoundTripAtMode0600(t *testing.T) {
	s, _ := testConnectionStore(t)
	want := []connectionRecord{
		githubRecord(),
		{
			ID: "gitea:127.0.0.1:3000", Kind: KindGitea, Host: "127.0.0.1:3000",
			WebBaseURL: "http://127.0.0.1:3000", APIBaseURL: "http://127.0.0.1:3000/api/v1",
			CAPEM: "ca", ClientCertPEM: "cert", ClientKeyPEM: "key", Proxy: "http://proxy:3128",
			PrivateAddresses: true, PlaintextHTTP: true, OAuthClientID: "client",
			HelperValue: "/'bin/marotte' git-credential", RotationCursor: "cursor",
			OwnerScopes: []string{"acme", "group/sub"},
		},
	}
	saveRecords(t, s, want...)

	got, err := s.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("load() = %+v, want %+v", got, want)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("record file mode = %#o, want 0600", mode)
	}
}

func TestConnectionRecords_MalformedFileIsMovedAsideNotOverwritten(t *testing.T) {
	s, dir := testConnectionStore(t)
	garbage := []byte(`{"connections": [`)
	if err := os.WriteFile(s.path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.load()
	if err != nil || len(got) != 0 {
		t.Fatalf("load() over a malformed file = %+v, %v; want no records and no error", got, err)
	}
	aside, err := filepath.Glob(filepath.Join(dir, connectionsFileName+".corrupt.*"))
	if err != nil || len(aside) != 1 {
		t.Fatalf("corrupt copies = %v (%v), want exactly one", aside, err)
	}

	saveRecords(t, s, githubRecord())
	kept, err := os.ReadFile(aside[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(garbage) {
		t.Errorf("corrupt copy = %q after a write, want the original %q", kept, garbage)
	}
	if recs, err := s.load(); err != nil || len(recs) != 1 {
		t.Errorf("load() after the write = %+v, %v; want the one new record", recs, err)
	}
}

// failRecordWritesAfterOneRead lets one record-file read through, the check a
// connect or disconnect makes first, and refuses every later one, so the
// write after it fails.
func failRecordWritesAfterOneRead(t *testing.T) {
	t.Helper()
	orig := enforceFileMode
	t.Cleanup(func() { enforceFileMode = orig })
	reads := 0
	enforceFileMode = func(path string, mode os.FileMode) (os.FileMode, error) {
		reads++
		if reads > 1 {
			return 0o660, atomicfile.ErrModeNotStored
		}
		return orig(path, mode)
	}
}

// The record is the commit point: a reconnect whose record write fails keeps
// the credential that worked.
func TestManagerConnect_AFailedRecordWriteKeepsTheWorkingCredential(t *testing.T) {
	isolateGit(t)
	m := recordManager(t, &whoamiCore{})
	old, ok, err := m.store.Load("github:github.com")
	if err != nil || !ok {
		t.Fatalf("Setup: Load() = %v, %v, want the seeded credential", ok, err)
	}
	failRecordWritesAfterOneRead(t)

	rec := githubRecord()
	if err := m.connect(t.Context(), &rec, "token-new"); err == nil {
		t.Fatal("connect() with the record write refused = nil, want its error")
	}
	if got, ok, err := m.store.Load("github:github.com"); err != nil || !ok || got.Token != old.Token {
		t.Errorf("credential after the failed reconnect = %q, %v, %v; want the previous %q", got.Token, ok, err, old.Token)
	}
}

// A disconnect whose record write fails leaves the record with the credential
// it authenticates with.
func TestManagerDisconnect_AFailedRecordWriteKeepsTheConnection(t *testing.T) {
	isolateGit(t)
	m := recordManager(t, &whoamiCore{})
	failRecordWritesAfterOneRead(t)

	if err := m.disconnect(t.Context(), m.get("github:github.com")); err == nil {
		t.Fatal("disconnect() with the record write refused = nil, want its error")
	}
	if _, ok, err := m.store.Load("github:github.com"); err != nil || !ok {
		t.Errorf("credential after the failed disconnect = %v, %v; want it kept beside the record", ok, err)
	}
}

func TestConnectionRecords_WidenedModeFailsTheLoadWithAReason(t *testing.T) {
	s, _ := testConnectionStore(t)
	saveRecords(t, s, githubRecord())
	orig := enforceFileMode
	t.Cleanup(func() { enforceFileMode = orig })
	enforceFileMode = func(string, os.FileMode) (os.FileMode, error) {
		return 0o660, atomicfile.ErrModeNotStored
	}

	recs, err := s.load()
	if err == nil {
		t.Fatal("load() over an unverifiable mode returned no error")
	}
	for _, want := range []string{connectionsFileName, "0600"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("load() error %q does not name %q", err, want)
		}
	}
	if len(recs) != 1 || recs[0].ID != "github:github.com" {
		t.Errorf("load() records = %+v, want the one record listed for its disconnected row", recs)
	}
	if uErr := s.update(t.Context(), func([]connectionRecord) []connectionRecord { return nil }); uErr == nil {
		t.Error("update() over an unverifiable mode succeeded; the file must not be written")
	}
	if _, statErr := os.Stat(s.path); statErr != nil {
		t.Errorf("record file gone after a refused load: %v", statErr)
	}
}

func TestConnectionRecords_SymlinkAtTheNameIsRefused(t *testing.T) {
	s, dir := testConnectionStore(t)
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load(); err == nil {
		t.Error("load() through a symlink at the name returned no error")
	}
}

func TestConnectionRecords_OneWriterLosesNoUpdate(t *testing.T) {
	s, _ := testConnectionStore(t)
	saveRecords(t, s, githubRecord())

	const n = 25
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range n {
			host := fmt.Sprintf("gitea%d.test", i)
			rec := connectionRecord{ID: MakeID(KindGitea, host), Kind: KindGitea, Host: host}
			if err := s.update(t.Context(), func(recs []connectionRecord) []connectionRecord {
				return append(recs, rec)
			}); err != nil {
				t.Errorf("connect %d: %v", i, err)
			}
		}
	})
	wg.Go(func() {
		for i := range n {
			if err := s.update(t.Context(), func(recs []connectionRecord) []connectionRecord {
				for j := range recs {
					if recs[j].ID == "github:github.com" {
						recs[j].RotationCursor = fmt.Sprint(i)
					}
				}
				return recs
			}); err != nil {
				t.Errorf("cursor %d: %v", i, err)
			}
		}
	})
	wg.Wait()

	recs, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != n+1 {
		t.Errorf("load() = %d records, want %d: a connect was lost", len(recs), n+1)
	}
	if i := slices.IndexFunc(recs, func(r connectionRecord) bool { return r.ID == "github:github.com" }); i < 0 ||
		recs[i].RotationCursor != fmt.Sprint(n-1) {
		t.Errorf("github record = %+v, want rotation cursor %d: a cursor write-back was lost", recs, n-1)
	}
}

func TestConnectionRecords_ProbeStateIsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	seedStoreRecord(t, dir, "github:github.com", "alice")
	m := NewManager(dir)
	saveRecords(t, m.conns, githubRecord())
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	row := m.forges["github:github.com"]
	row.Email, row.LastProbed, row.LastError = "alice@example.test", 42, "probe failed"
	m.mu.Unlock()
	if err := m.conns.update(t.Context(), func(recs []connectionRecord) []connectionRecord { return recs }); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(m.conns.path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Connections []map[string]json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, rec := range doc.Connections {
		for _, key := range []string{"email", "last_probed", "last_error", "connected", "username"} {
			if _, ok := rec[key]; ok {
				t.Errorf("record file carries %q, which is probe state: %s", key, raw)
			}
		}
	}
}

func TestConnectionRecords_KindFamilyTable(t *testing.T) {
	cases := []struct {
		kind   Kind
		family forgeapi.Family
		host   string
	}{
		{KindGitHub, forgeapi.FamilyGitHub, "github.com"},
		{KindGitLab, forgeapi.FamilyGitLab, "gitlab.com"},
		{KindGitea, forgeapi.FamilyGitea, ""},
		{KindCodeberg, forgeapi.FamilyGitea, "codeberg.org"},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			if got := c.kind.family(); got != c.family {
				t.Errorf("%s.family() = %v, want %v", c.kind, got, c.family)
			}
			if got := c.kind.defaultHost(); got != c.host {
				t.Errorf("%s.DefaultHost() = %q, want %q", c.kind, got, c.host)
			}
		})
	}
	if got := Kind("bitbucket").family(); got != forgeapi.FamilyUnknown {
		t.Errorf("an unknown kind's family = %v, want FamilyUnknown", got)
	}
	rec := connectionRecord{ID: "codeberg:codeberg.org", Kind: KindCodeberg, Host: "codeberg.org"}
	if got := rec.webBase(); got != "https://codeberg.org" {
		t.Errorf("webBase() with no stored base = %q, want https://codeberg.org", got)
	}
}

func TestConnectionRecords_InvalidRecordsAreDropped(t *testing.T) {
	s, _ := testConnectionStore(t)
	body := `{"connections":[
		{"id":"github:github.com","kind":"github","host":"github.com"},
		{"id":"github:evil.test","kind":"github","host":"github.com"},
		{"id":"bitbucket:bitbucket.org","kind":"bitbucket","host":"bitbucket.org"},
		{"id":"github:github.com","kind":"github","host":"github.com"}
	]}`
	if err := os.WriteFile(s.path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "github:github.com" {
		t.Errorf("load() = %+v, want only the one well-formed, first-seen record", recs)
	}
}
