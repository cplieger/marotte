package forges

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/forgeapi"
)

func TestConnectionRecords_RoundTripAtMode0600(t *testing.T) {
	dir := t.TempDir()
	gitea := specFor(KindGitea, "127.0.0.1:3000")
	gitea.WebBaseURL, gitea.APIBaseURL = "http://127.0.0.1:3000", "http://127.0.0.1:3000/api/v1"
	gitea.CAPEM, gitea.ClientCertPEM, gitea.ClientKeyPEM, gitea.Proxy = "ca", "cert", "key", "http://proxy:3128"
	gitea.PrivateAddresses, gitea.PlaintextHTTP, gitea.OAuthClientID = true, true, "client"
	gitea.RotationCursor, gitea.OwnerScopes, gitea.Slot = "cursor", []string{"acme", "group/sub"}, 1
	want := []connectionRecord{
		{connectionSpec: githubRecord()},
		{connectionSpec: gitea, Standing: standingRefused},
	}
	writeRecords(t, dir, want...)

	got, err := newRecordsFile(dir).load()
	if err != nil || !got.verified {
		t.Fatalf("load() = %+v, %v; want a verified file", got, err)
	}
	if !reflect.DeepEqual(got.recs, want) {
		t.Errorf("load() = %+v, want %+v", got.recs, want)
	}
	info, err := os.Stat(filepath.Join(dir, connectionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("record file mode = %#o, want 0600", mode)
	}
}

func TestConnectionRecords_MalformedFileIsMovedAsideNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	f := newRecordsFile(dir)
	garbage := []byte(`{"connections": [`)
	if err := os.WriteFile(f.path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := f.load()
	if err != nil || len(got.recs) != 0 || got.verified {
		t.Fatalf("load() over a malformed file = %+v, %v; want no records, no error and no verdict to sweep by", got, err)
	}
	aside, err := filepath.Glob(filepath.Join(dir, connectionsFileName+".corrupt.*"))
	if err != nil || len(aside) != 1 {
		t.Fatalf("corrupt copies = %v (%v), want exactly one", aside, err)
	}

	writeRecords(t, dir, connectionRecord{connectionSpec: githubRecord()})
	kept, err := os.ReadFile(aside[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(garbage) {
		t.Errorf("corrupt copy = %q after a write, want the original %q", kept, garbage)
	}
	if recs, err := f.load(); err != nil || len(recs.recs) != 1 {
		t.Errorf("load() after the write = %+v, %v; want the one new record", recs, err)
	}
}

func TestConnectionRecords_WidenedModeFailsTheLoadWithAReason(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, connectionRecord{connectionSpec: githubRecord()})
	orig := enforceFileMode
	t.Cleanup(func() { enforceFileMode = orig })
	enforceFileMode = func(string, os.FileMode) (os.FileMode, error) {
		return 0o660, atomicfile.ErrModeNotStored
	}

	got, err := newRecordsFile(dir).load()
	if err == nil {
		t.Fatal("load() over an unverifiable mode returned no error")
	}
	for _, want := range []string{connectionsFileName, "0600"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("load() error %q does not name %q", err, want)
		}
	}
	if len(got.recs) != 1 || got.recs[0].ID != "github:github.com" || got.verified {
		t.Errorf("load() = %+v, want the one record listed and the file unverified", got)
	}
}

func TestConnectionRecords_SymlinkAtTheNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	f := newRecordsFile(dir)
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.load(); err == nil {
		t.Error("load() through a symlink at the name returned no error")
	}
}

// Only the standing persists of a status: the login, email and check time are
// memory, learnt again at the next check.
func TestConnectionRecords_OnlyTheStandingOfAStatusPersists(t *testing.T) {
	isolateGit(t)
	cfg := t.TempDir()
	seedConnections(t, cfg, "alice", githubRecord())
	m, _ := loadedManager(t, cfg, &whoamiCore{err: &forgeapi.Error{Kind: forgeapi.KindForbidden, Message: "an IP allowlist"}})
	if _, err := m.check(t.Context(), "github:github.com", checkBudget); err != nil {
		t.Fatalf("check() = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(cfg, connectionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Connections []map[string]json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Connections) != 1 || string(doc.Connections[0]["standing"]) != `"refused"` {
		t.Fatalf("record file = %s, want the one record standing refused", raw)
	}
	for _, key := range []string{"email", "checked_at", "error", "status", "username", "login"} {
		if _, ok := doc.Connections[0][key]; ok {
			t.Errorf("record file carries %q, which is memory: %s", key, raw)
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
	spec := specFor(KindCodeberg, "codeberg.org")
	if got := spec.webBase(); got != "https://codeberg.org" {
		t.Errorf("webBase() with no stored base = %q, want https://codeberg.org", got)
	}
}

// A record is admitted at boot by the rules that admitted it at sign-in; the
// first of two records with one id wins.
func TestConnectionRecords_InvalidRecordsAreDropped(t *testing.T) {
	dir := t.TempDir()
	f := newRecordsFile(dir)
	cases := map[string]string{
		"the id another host makes": `{"id":"github:evil.test","kind":"github","host":"github.com","sign_in":"s","slot":0}`,
		"an unknown kind":           `{"id":"bitbucket:bitbucket.org","kind":"bitbucket","host":"bitbucket.org","sign_in":"s","slot":0}`,
		"no sign-in id":             `{"id":"gitlab:gitlab.com","kind":"gitlab","host":"gitlab.com","slot":0}`,
		"a third slot":              `{"id":"gitlab:gitlab.com","kind":"gitlab","host":"gitlab.com","sign_in":"s","slot":2}`,
		"an unknown standing":       `{"id":"gitlab:gitlab.com","kind":"gitlab","host":"gitlab.com","sign_in":"s","slot":0,"standing":"maybe"}`,
		"a host with a path":        `{"id":"gitea:x.test/p","kind":"gitea","host":"x.test/p","sign_in":"s","slot":0}`,
		"a web base on another host": `{"id":"gitlab:gitlab.com","kind":"gitlab","host":"gitlab.com","sign_in":"s","slot":0,` +
			`"web_base_url":"https://evil.test"}`,
		"a web base not in its stored form": `{"id":"gitlab:gitlab.com","kind":"gitlab","host":"gitlab.com","sign_in":"s",` +
			`"slot":0,"web_base_url":"https://GITLAB.com/"}`,
		"a sign-in id over its bound": `{"id":"gitlab:gitlab.com","kind":"gitlab","host":"gitlab.com","slot":0,"sign_in":"` +
			strings.Repeat("s", maxSignInBytes+1) + `"}`,
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"connections":[` +
				`{"id":"github:github.com","kind":"github","host":"github.com","sign_in":"first","slot":0},` + bad + `,` +
				`{"id":"github:github.com","kind":"github","host":"github.com","sign_in":"second","slot":1}]}`
			if err := os.WriteFile(f.path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := f.load()
			if err != nil {
				t.Fatal(err)
			}
			if len(got.recs) != 1 || got.recs[0].ID != "github:github.com" || got.recs[0].SignIn != "first" {
				t.Errorf("load() = %+v, want only the one well-formed, first-seen record", got.recs)
			}
		})
	}
}
