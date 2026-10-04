package forges

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestManagerList_SortsByKindThenHost pins List's ordering contract:
// Kind first, Host as tiebreaker. Two GitHub hosts exercise the
// same-kind tiebreaker; a GitLab host that sorts before both proves the
// primary key is Kind.
func TestManagerList_SortsByKindThenHost(t *testing.T) {
	stubPath(t)
	cfg := t.TempDir()
	m := NewManager(cfg)
	var recs []connectionRecord
	for _, r := range []struct {
		kind Kind
		host string
	}{{KindGitHub, "ccc.example"}, {KindGitHub, "bbb.example"}, {KindGitLab, "aaa.example"}} {
		id := MakeID(r.kind, r.host)
		seedStoreRecord(t, cfg, id, "alice")
		recs = append(recs, connectionRecord{ID: id, Kind: r.kind, Host: r.host})
	}
	saveRecords(t, m.conns, recs...)

	list := m.List(t.Context())

	got := make([]string, len(list))
	for i, f := range list {
		got[i] = string(f.Kind) + "|" + f.Host
	}
	want := []string{
		"github|bbb.example", // Kind primary: all github before gitlab...
		"github|ccc.example", // ...Host secondary: bbb before ccc
		"gitlab|aaa.example", // gitlab last despite its host sorting first
	}
	if len(got) != len(want) {
		t.Fatalf("List() returned %d forges, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List()[%d] = %q, want %q (full order: %v)", i, got[i], want[i], got)
		}
	}
}

// The forge list is cached for a TTL, so a client that asks twice in quick
// succession reads the connection record file once.
func TestManagerList_ServesTheSecondCallFromCache(t *testing.T) {
	stubPath(t)
	cfg := t.TempDir()
	seedStoreRecord(t, cfg, "gitea:gitea.example", "bob")
	m := NewManager(cfg)
	saveRecords(t, m.conns, connectionRecord{ID: "gitea:gitea.example", Kind: KindGitea, Host: "gitea.example"})
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: the boot Refresh() = %v", err)
	}
	m.Invalidate()
	reads := 0
	real := enforceFileMode
	t.Cleanup(func() { enforceFileMode = real })
	enforceFileMode = func(path string, mode os.FileMode) (os.FileMode, error) {
		reads++
		return real(path, mode)
	}

	first := m.List(t.Context())
	second := m.List(t.Context())

	if len(first) != 1 || len(second) != 1 {
		t.Errorf("List returned %d then %d forges, want the one record row both times", len(first), len(second))
	}
	if reads != 1 {
		t.Errorf("the record file was read %d times across two List calls, want 1: the second is inside the cache TTL", reads)
	}
}

func TestManagerList_DegradedStoreRowsCarryTheReason(t *testing.T) {
	stubPath(t)
	cfg := t.TempDir()
	store := filepath.Join(cfg, credentialStoreDir)
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o750); err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg)
	saveRecords(t, m.conns, githubRecord())

	list := m.List(t.Context())
	if len(list) != 1 {
		t.Fatalf("List() = %+v, want the one record row", list)
	}
	if list[0].Connected {
		t.Errorf("row over a degraded store reads connected: %+v", list[0])
	}
	if !strings.Contains(list[0].LastError, store) || !strings.Contains(list[0].LastError, "0700") {
		t.Errorf("row last_error = %q, want the store directory and its required mode", list[0].LastError)
	}
	if list[0].ErrorCode != codeConnectionUnusable {
		t.Errorf("row error_code = %q, want %q: no probe, reconnect or sign-out can succeed until the mode is fixed",
			list[0].ErrorCode, codeConnectionUnusable)
	}
}

func TestManagerList_RecordRowsNeedNoCLI(t *testing.T) {
	stubPath(t)
	cfg := t.TempDir()
	seedStoreRecord(t, cfg, "github:github.com", "bob")
	m := NewManager(cfg)
	saveRecords(t, m.conns, githubRecord(),
		connectionRecord{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com"})

	byID := map[string]ConfiguredForge{}
	for _, f := range m.List(t.Context()) {
		byID[f.ID] = f
	}
	if got := byID["github:github.com"]; !got.Connected || got.Username != "bob" || got.LastError != "" {
		t.Errorf("github row with no CLI on PATH = %+v, want connected as bob", got)
	}
	if got := byID["gitlab:gitlab.com"]; got.Connected || got.LastError == "" {
		t.Errorf("a record with no stored credential = %+v, want disconnected with a reason", got)
	}
}

// connectedGitHub connects github.com through the manager's connect path with
// no forge CLI on PATH, the client being core.
func connectedGitHub(t *testing.T, core forgeapi.Core) (*Manager, string) {
	t.Helper()
	isolateGit(t)
	stubPath(t)
	cfg := t.TempDir()
	m := NewManager(cfg)
	m.executable = func() (string, error) { return testHelperBin, nil }
	m.clients.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) { return core, nil }
	rec := connectionRecord{ID: "github:github.com", Kind: KindGitHub, Host: "github.com"}
	if err := m.connect(t.Context(), &rec, "ghp_secret"); err != nil {
		t.Fatalf("Setup: connect(github.com) = %v", err)
	}
	return m, cfg
}

func TestGitHubConnection_SurvivesRefreshWithNoCLI(t *testing.T) {
	core := &fakeCore{}
	m, cfg := connectedGitHub(t, core)

	m.Invalidate()
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}
	if got := m.Get("github:github.com"); got == nil || !got.Connected || got.Username != "bob" {
		t.Errorf("row after a refresh with no CLI on PATH = %+v, want connected as bob", got)
	}

	restarted := NewManager(cfg)
	restarted.executable = m.executable
	byID := map[string]ConfiguredForge{}
	for _, f := range restarted.List(t.Context()) {
		byID[f.ID] = f
	}
	if got := byID["github:github.com"]; !got.Connected || got.Username != "bob" || got.LastError != "" {
		t.Errorf("row after a restart with no CLI on PATH = %+v, want connected as bob", got)
	}
}

// minePRs is a connection's ListMyPRs answer: #9 in the tracked bob/app and #10
// in a repository no clone tracks.
func minePRs(family forgeapi.Family, check forgeapi.CheckState) []forgeapi.PullRequest {
	mine := prIn(family, "bob/app", 9, check)
	mine.Title = "Mine"
	return []forgeapi.PullRequest{mine, prIn(family, "bob/elsewhere", 10, forgeapi.CheckFailing)}
}

// pollerView is the poller's read of webBase's authored pull requests through m,
// and the source the read came from.
func pollerView(ctx context.Context, m *Manager, webBase string) ([]ConnectionRead, PRSource) {
	src := NewManagerPRSource(m, fixedOrigins(RepoOrigin{Dir: "app", WebBase: webBase, Slug: "bob/app"}))
	return src.Read(ctx, false, firstPage), src
}

// mineRead answers how reads differ from one connection read by one authored
// page whose first row is #9 Mine in bob/app, or nil.
func mineRead(reads []ConnectionRead) error {
	if len(reads) != 1 || len(reads[0].Pages) != 1 || reads[0].Pages[0].Err != nil {
		return fmt.Errorf("reads = %+v, want one connection read by one authored page", reads)
	}
	rows := reads[0].Pages[0].Rows
	if len(rows) != 2 || rows[0].RepoID != repoIDOf("bob/app") || rows[0].Number != 9 || rows[0].Title != "Mine" {
		return fmt.Errorf("authored rows = %+v, want #9 Mine in bob/app first", rows)
	}
	return nil
}

func TestGitHubConnection_IsVisibleToThePollerWithNoCLI(t *testing.T) {
	m, _ := connectedGitHub(t, &fakeCore{prs: minePRs(forgeapi.FamilyGitHub, forgeapi.CheckPassing)})
	reads, src := pollerView(t.Context(), m, "https://github.com")
	if err := mineRead(reads); err != nil {
		t.Fatalf("Read() with no CLI on PATH: %v", err)
	}
	want := map[string]string{subjectKey("github:github.com", "bob/app", 9): checkPassing}
	if got := trackedVerdicts(t, src); !maps.Equal(got, want) {
		t.Errorf("tracked verdicts with no CLI on PATH = %v, want %v", got, want)
	}
}

// connectGitLabByPAT connects gitlab.com through the PAT route.
func connectGitLabByPAT(t *testing.T, h *connectHarness) {
	t.Helper()
	rec := h.do(t, http.MethodPost, gitlabPATPath, `{"token":"glpat-secret"}`)
	if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != stateComplete {
		t.Fatalf("POST %s = %d %v, want 200 complete", gitlabPATPath, rec.Code, body)
	}
}

func TestGitLabConnection_SurvivesRefreshWithNoCLI(t *testing.T) {
	h := newConnectHarness(t, &userWire{status: http.StatusOK, body: `{"id":1,"username":"alice"}`})
	connectGitLabByPAT(t, h)
	cred, ok, err := h.m.store.Load("gitlab:gitlab.com")
	if err != nil || !ok || cred.Family != forgeapi.FamilyGitLab || cred.Kind != forgeapi.CredKindStaticPAT ||
		cred.Account != "alice" || cred.WebBaseURL != "https://gitlab.com" {
		t.Errorf("stored credential = %+v (present %v, err %v), want a static GitLab PAT for alice", cred, ok, err)
	}
	if got := helperValuesFor(t, "https://gitlab.com"); len(got) != 2 || got[1] != h.helperValue(t) {
		t.Errorf("credential.https://gitlab.com.helper = %q, want the reset then Marotte's value", got)
	}

	h.m.Invalidate()
	if err := h.m.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}
	if got := h.m.Get("gitlab:gitlab.com"); got == nil || !got.Connected || got.Username != "alice" {
		t.Errorf("row after a refresh = %+v, want connected as alice", got)
	}

	restarted := NewManager(h.cfgDir)
	restarted.executable = h.m.executable
	byID := map[string]ConfiguredForge{}
	for _, f := range restarted.List(t.Context()) {
		byID[f.ID] = f
	}
	if got := byID["gitlab:gitlab.com"]; !got.Connected || got.Username != "alice" || got.LastError != "" {
		t.Errorf("row after a restart = %+v, want connected as alice", got)
	}
}

func TestGitLabConnection_IsVisibleToThePollerWithNoCLI(t *testing.T) {
	h := newConnectHarness(t, nil)
	h.m.clients.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) {
		return &fakeCore{prs: minePRs(forgeapi.FamilyGitLab, forgeapi.CheckFailing)}, nil
	}
	connectGitLabByPAT(t, h)
	reads, src := pollerView(t.Context(), h.m, "https://gitlab.com")
	if err := mineRead(reads); err != nil {
		t.Fatalf("Read() with no CLI on PATH: %v", err)
	}
	want := map[string]string{subjectKey("gitlab:gitlab.com", "bob/app", 9): checkFailing}
	if got := trackedVerdicts(t, src); !maps.Equal(got, want) {
		t.Errorf("tracked verdicts with no CLI on PATH = %v, want %v", got, want)
	}
}

// patPath is the PAT route of connection id.
func patPath(id string) string {
	return "/api/forges/" + strings.ReplaceAll(id, ":", "%3A") + "/login/pat"
}

// connectGiteaKindByPAT connects the Gitea-family connection id through the PAT
// route.
func connectGiteaKindByPAT(t *testing.T, h *connectHarness, id string) {
	t.Helper()
	rec := h.do(t, http.MethodPost, patPath(id), `{"token":"gitea-secret"}`)
	if body := decodeBody(t, rec); rec.Code != http.StatusOK || body["status"] != stateComplete {
		t.Fatalf("POST %s = %d %v, want 200 complete", patPath(id), rec.Code, body)
	}
}

func TestGiteaFamilyConnection_SurvivesRefreshWithNoCLI(t *testing.T) {
	for _, tc := range []struct{ id, origin string }{
		{id: "gitea:gitea.example", origin: "https://gitea.example"},
		{id: "codeberg:codeberg.org", origin: "https://codeberg.org"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			h := newConnectHarness(t, &userWire{status: http.StatusOK, body: `{"id":1,"login":"alice"}`})
			connectGiteaKindByPAT(t, h, tc.id)
			cred, ok, err := h.m.store.Load(tc.id)
			if err != nil || !ok || cred.Family != forgeapi.FamilyGitea || cred.Kind != forgeapi.CredKindStaticPAT ||
				cred.Account != "alice" || cred.WebBaseURL != tc.origin {
				t.Errorf("stored credential = %+v (present %v, err %v), want a static Gitea-family PAT for alice on %s",
					cred, ok, err, tc.origin)
			}
			if got := helperValuesFor(t, tc.origin); len(got) != 2 || got[1] != h.helperValue(t) {
				t.Errorf("credential.%s.helper = %q, want the reset then Marotte's value", tc.origin, got)
			}

			h.m.Invalidate()
			if err := h.m.Refresh(t.Context()); err != nil {
				t.Fatalf("Refresh() = %v", err)
			}
			if got := h.m.Get(tc.id); got == nil || !got.Connected || got.Username != "alice" {
				t.Errorf("row after a refresh = %+v, want connected as alice", got)
			}

			restarted := NewManager(h.cfgDir)
			restarted.executable = h.m.executable
			byID := map[string]ConfiguredForge{}
			for _, f := range restarted.List(t.Context()) {
				byID[f.ID] = f
			}
			if got := byID[tc.id]; !got.Connected || got.Username != "alice" || got.LastError != "" {
				t.Errorf("row after a restart = %+v, want connected as alice", got)
			}
		})
	}
}

func TestGiteaConnection_IsVisibleToThePollerWithNoCLI(t *testing.T) {
	h := newConnectHarness(t, nil)
	h.m.clients.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) {
		return &fakeCore{prs: minePRs(forgeapi.FamilyGitea, forgeapi.CheckFailing)}, nil
	}
	connectGiteaKindByPAT(t, h, "gitea:gitea.example")
	reads, src := pollerView(t.Context(), h.m, "https://gitea.example")
	if err := mineRead(reads); err != nil {
		t.Fatalf("Read() with no CLI on PATH: %v", err)
	}
	want := map[string]string{subjectKey("gitea:gitea.example", "bob/app", 9): checkFailing}
	if got := trackedVerdicts(t, src); !maps.Equal(got, want) {
		t.Errorf("tracked verdicts with no CLI on PATH = %v, want %v", got, want)
	}
}

func TestConnect_RecordWriteFailureLeavesNoCredential(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	m := NewManager(t.TempDir())
	m.clients.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) { return &fakeCore{}, nil }
	saveRecords(t, m.conns, connectionRecord{ID: "gitea:gitea.example", Kind: KindGitea, Host: "gitea.example"})
	loads := 0
	real := enforceFileMode
	t.Cleanup(func() { enforceFileMode = real })
	enforceFileMode = func(path string, mode os.FileMode) (os.FileMode, error) {
		if loads++; loads > 1 {
			return 0, errors.New("mode check failed")
		}
		return real(path, mode)
	}

	rec := connectionRecord{ID: "github:github.com", Kind: KindGitHub, Host: "github.com"}
	if err := m.connect(t.Context(), &rec, "ghp_secret"); err == nil {
		t.Fatal("connect() with an unwritable record file = nil, want the write failure")
	}
	if _, ok, err := m.store.Load("github:github.com"); ok || err != nil {
		t.Errorf("store holds the credential (err %v) after the record write failed, want it removed", err)
	}
	if got := helperValuesFor(t, githubOrigin); len(got) != 0 {
		t.Errorf("credential.%s.helper = %q after a failed connect, want nothing registered", githubOrigin, got)
	}
}

func TestConnect_ReconnectKeepsTheRecordsRotationCursor(t *testing.T) {
	m, _ := connectedGitHub(t, &fakeCore{})
	if err := m.conns.update(t.Context(), func(cur []connectionRecord) []connectionRecord {
		cur[0].RotationCursor = "cursor-7"
		return cur
	}); err != nil {
		t.Fatalf("Setup: write the cursor: %v", err)
	}

	rec := connectionRecord{ID: "github:github.com", Kind: KindGitHub, Host: "github.com"}
	if err := m.connect(t.Context(), &rec, "ghp_new"); err != nil {
		t.Fatalf("reconnect = %v", err)
	}
	recs, err := m.conns.load()
	if err != nil || len(recs) != 1 || recs[0].RotationCursor != "cursor-7" || recs[0].HelperValue == "" {
		t.Errorf("records after a reconnect = %+v, %v; want one record keeping cursor-7 and its helper value", recs, err)
	}
}

func TestConnect_ReAddressedReconnectMovesTheHelper(t *testing.T) {
	isolateGit(t)
	stubPath(t)
	m := NewManager(t.TempDir())
	m.executable = func() (string, error) { return testHelperBin, nil }
	m.clients.newCore = func(*connectionRecord, []forgeapi.Option) (forgeapi.Core, error) { return &fakeCore{}, nil }
	const host = "forge.test:8080"
	for _, base := range []string{"https://" + host, "http://" + host} {
		rec := connectionRecord{ID: MakeID(KindGitHub, host), Kind: KindGitHub, Host: host, WebBaseURL: base, PlaintextHTTP: true}
		if err := m.connect(t.Context(), &rec, "ghp_secret"); err != nil {
			t.Fatalf("connect(%s) = %v", base, err)
		}
	}
	if got := helperValuesFor(t, "https://"+host); len(got) != 0 {
		t.Errorf("credential.https://%s.helper = %q after re-addressing to http, want it removed", host, got)
	}
	if got := helperValuesFor(t, "http://"+host); len(got) != 2 {
		t.Errorf("credential.http://%s.helper = %q, want the reset and Marotte's value", host, got)
	}
}
