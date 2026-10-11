package forges

// The production PRSource's three rules: one ListMyPRs per connection per sweep
// whatever the repository count, the rows kept to the repositories a workspace
// clone tracks, and the clone join that decides which repository a clone is.

import (
	"context"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
)

// repoIDOf is the canonical repository id of selector, spelled out here rather
// than through RepoRef.Encode so the join is checked against the rule itself.
func repoIDOf(selector string) string {
	return forgeapi.RepoIDPrefix + hex.EncodeToString([]byte(strings.ToLower(selector)))
}

func prIn(family forgeapi.Family, selector string, number int, check forgeapi.CheckState) forgeapi.PullRequest {
	return forgeapi.PullRequest{
		Ref:    forgeapi.PRRef{Number: number},
		Repo:   forgeapi.RepoRef{Family: family, Selector: selector, DisplayPath: selector},
		Title:  "PR " + selector,
		State:  forgeapi.PRStateOpen,
		Action: forgeapi.ActionState{Checks: check},
	}
}

// myPRsCore serves ListMyPRs from pages keyed by the cursor asked for and counts
// every other read, which the poller must not make.
type myPRsCore struct {
	forgeapi.Core
	pages  map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]
	afters []forgeapi.Cursor
	others int
}

func (c *myPRsCore) ListMyPRs(_ context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	set, err := forgeapi.ResolveList(opts...)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	c.afters = append(c.afters, set.After)
	return c.pages[set.After], nil
}

func (*myPRsCore) BudgetState() forgeapi.BudgetState {
	return forgeapi.BudgetState{Remaining: forgeapi.BudgetRemainingUnknown}
}

func (c *myPRsCore) Whoami(context.Context) (forgeapi.Account, error) {
	c.others++
	return forgeapi.Account{Login: "bob"}, nil
}

func (c *myPRsCore) ListPRs(context.Context, forgeapi.RepoRef, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	c.others++
	return forgeapi.Page[forgeapi.PullRequest]{}, nil
}

func sourceManager(t *testing.T, cores map[string]forgeapi.Core, recs ...connectionRecord) *Manager {
	t.Helper()
	// The boot Refresh registers a helper for every record in the global git
	// config, which a package-wide config would accumulate test after test.
	isolateGit(t)
	stubPath(t)
	cfg := t.TempDir()
	for i := range recs {
		seedStoreRecord(t, cfg, recs[i].ID, "bob")
	}
	m := NewManager(cfg)
	saveRecords(t, m.conns, recs...)
	m.clients.newCore = func(rec *connectionRecord, _ []forgeapi.Option) (forgeapi.Core, error) {
		if c, ok := cores[rec.ID]; ok {
			return c, nil
		}
		return nil, errNoClient
	}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	return m
}

func gitlabRecord() connectionRecord {
	return connectionRecord{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com"}
}

func localGiteaRecord() connectionRecord {
	return connectionRecord{
		ID: "gitea:127.0.0.1:3000", Kind: KindGitea, Host: "127.0.0.1:3000",
		WebBaseURL: "http://127.0.0.1:3000", PlaintextHTTP: true, PrivateAddresses: true,
	}
}

func fixedOrigins(origins ...RepoOrigin) func(context.Context) []RepoOrigin {
	return func(context.Context) []RepoOrigin { return origins }
}

var errNoClient = errors.New("no client for this connection")

// firstPage is the cursor of a walk that has not started.
func firstPage(PRConnection, Scope) forgeapi.Cursor { return "" }

// trackedVerdicts sweeps a push-only poller over src once and answers the
// verdict it holds per subject key.
func trackedVerdicts(t *testing.T, src PRSource) map[string]string {
	t.Helper()
	p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{push: true}).Open)
	p.sweep(t.Context())
	got := make(map[string]string, len(p.seen))
	for key, tr := range p.seen {
		got[key] = tr.check
	}
	return got
}

func subjectKey(forgeID, selector string, number int) string {
	return marotte.PRSubject(forgeID, repoIDOf(selector), number).Key
}

func TestCloneRepos_AnswersDirForgeAndCanonicalRepoID(t *testing.T) {
	rows := []ConfiguredForge{
		{ID: "github:github.com", Kind: KindGitHub, Host: "github.com", webBase: "https://github.com", Connected: true},
		{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com", webBase: "https://gitlab.com", Connected: true},
		{ID: "gitea:127.0.0.1:3000", Kind: KindGitea, Host: "127.0.0.1:3000", webBase: "http://127.0.0.1:3000"},
	}
	origins := []RepoOrigin{
		{Dir: "sandbox", WebBase: "https://gitlab.com", Slug: "forgeapi-live/nested/sandbox"},
		{Dir: "marotte-ssh", WebBase: "", Slug: "cplieger/marotte"},
		{Dir: "upper", WebBase: "https://GitHub.com:443", Slug: "Bob/App"},
		{Dir: "local", WebBase: "http://127.0.0.1:3000", Slug: "alice/app"},
		{Dir: "other-instance", WebBase: "http://127.0.0.1:4000", Slug: "alice/app"},
		{Dir: "elsewhere", WebBase: "https://codeberg.org", Slug: "a/b"},
		{Dir: "plaintext-github", WebBase: "http://github.com", Slug: "bob/app"},
		{Dir: "too-deep", WebBase: "https://github.com", Slug: "a/b/c"},
	}
	want := []CloneRepo{
		{Dir: "sandbox", ForgeID: "gitlab:gitlab.com", RepoID: repoIDOf("forgeapi-live/nested/sandbox")},
		{Dir: "upper", ForgeID: "github:github.com", RepoID: repoIDOf("bob/app")},
		{Dir: "local", ForgeID: "gitea:127.0.0.1:3000", RepoID: repoIDOf("alice/app")},
	}
	if got := cloneRepos(rows, origins); !slices.Equal(got, want) {
		t.Errorf("CloneRepos() =\n%+v\nwant\n%+v\n(an ssh remote, another port, another forge's origin, another scheme and a path the family refuses join nothing)",
			got, want)
	}
}

func TestSource_KeepsOnlyRowsInATrackedClone(t *testing.T) {
	core := &myPRsCore{pages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {Items: []forgeapi.PullRequest{
		prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPassing),
		prIn(forgeapi.FamilyGitHub, "bob/elsewhere", 2, forgeapi.CheckFailing),
		prIn(forgeapi.FamilyGitHub, "carol/app", 3, forgeapi.CheckFailing),
	}}}}
	m := sourceManager(t, map[string]forgeapi.Core{"github:github.com": core}, githubRecord())
	src := NewManagerPRSource(m, fixedOrigins(
		RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"},
		RepoOrigin{Dir: "ssh-clone", WebBase: "", Slug: "carol/app"},
	))

	reads := src.Read(t.Context(), false, firstPage)
	wantConn := PRConnection{ID: "github:github.com", Account: "bob", WebBase: "https://github.com"}
	if len(reads) != 1 || reads[0].Conn != wantConn {
		t.Fatalf("Read() = %+v, want one read of %+v", reads, wantConn)
	}
	got := trackedVerdicts(t, src)
	want := map[string]string{subjectKey("github:github.com", "bob/app", 1): checkPassing}
	if !maps.Equal(got, want) {
		t.Errorf("tracked verdicts = %v, want %v: only the tracked repository's, and an ssh clone tracks none", got, want)
	}
}

func TestSource_MatchesAMixedCaseRemoteToTheCanonicalRepoID(t *testing.T) {
	core := &myPRsCore{pages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {Items: []forgeapi.PullRequest{
		prIn(forgeapi.FamilyGitHub, "Bob/App", 1, forgeapi.CheckPending),
		prIn(forgeapi.FamilyGitHub, "carol/tool", 2, forgeapi.CheckPending),
	}}}}
	m := sourceManager(t, map[string]forgeapi.Core{"github:github.com": core}, githubRecord())
	src := NewManagerPRSource(m, fixedOrigins(
		RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"},
		RepoOrigin{Dir: "tool", WebBase: "https://github.com", Slug: "Carol/Tool"},
	))

	got := trackedVerdicts(t, src)
	want := map[string]string{
		subjectKey("github:github.com", "bob/app", 1):    checkPending,
		subjectKey("github:github.com", "carol/tool", 2): checkPending,
	}
	if !maps.Equal(got, want) {
		t.Errorf("tracked verdicts = %v, want %v: a remote and the forge spelling one repository differently are one repository",
			got, want)
	}
}

func TestSource_OneListMyPRsPerConnectionPerSweep(t *testing.T) {
	github := &myPRsCore{pages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{
		"":   {Items: []forgeapi.PullRequest{prIn(forgeapi.FamilyGitHub, "bob/one", 1, forgeapi.CheckPending)}, Next: "n2"},
		"n2": {Items: []forgeapi.PullRequest{prIn(forgeapi.FamilyGitHub, "bob/two", 2, forgeapi.CheckPending)}},
	}}
	gitlab := &myPRsCore{pages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {}}}
	gitea := &myPRsCore{pages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {}}}
	codeberg := &myPRsCore{pages: map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": {}}}
	codebergRec := connectionRecord{ID: "codeberg:codeberg.org", Kind: KindCodeberg, Host: "codeberg.org"}
	m := sourceManager(t, map[string]forgeapi.Core{
		"github:github.com": github, "gitlab:gitlab.com": gitlab, "gitea:127.0.0.1:3000": gitea,
		"codeberg:codeberg.org": codeberg,
	}, githubRecord(), gitlabRecord(), localGiteaRecord(), codebergRec)
	// Codeberg keeps its record and loses its credential, so its row is not connected.
	if err := m.store.Delete(codebergRec.ID); err != nil {
		t.Fatalf("Setup: delete the codeberg credential: %v", err)
	}
	m.invalidate()
	src := NewManagerPRSource(m, fixedOrigins(
		RepoOrigin{Dir: "one", WebBase: "https://github.com", Slug: "bob/one"},
		RepoOrigin{Dir: "two", WebBase: "https://github.com", Slug: "bob/two"},
		RepoOrigin{Dir: "three", WebBase: "https://github.com", Slug: "bob/three"},
		RepoOrigin{Dir: "group-a", WebBase: "https://gitlab.com", Slug: "group/a"},
		RepoOrigin{Dir: "group-b", WebBase: "https://gitlab.com", Slug: "group/sub/b"},
		RepoOrigin{Dir: "berg", WebBase: "https://codeberg.org", Slug: "bob/berg"},
	))
	p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{push: true}).Open)

	p.sweep(t.Context())
	p.sweep(t.Context())

	if !slices.Equal(github.afters, []forgeapi.Cursor{"", "n2"}) {
		t.Errorf("GitHub ListMyPRs cursors over two sweeps = %q, want one call a sweep, the second from the first's Next",
			github.afters)
	}
	if len(gitlab.afters) != 2 {
		t.Errorf("GitLab ListMyPRs calls over two sweeps = %d, want 2, one a sweep whatever its two tracked repositories",
			len(gitlab.afters))
	}
	if len(gitea.afters) != 0 {
		t.Errorf("Gitea ListMyPRs calls = %d, want 0: no clone tracks a repository there", len(gitea.afters))
	}
	if len(codeberg.afters) != 0 {
		t.Errorf("Codeberg ListMyPRs calls = %d, want 0: a connection with no credential is not read", len(codeberg.afters))
	}
	if n := github.others + gitlab.others + gitea.others; n != 0 {
		t.Errorf("the sweeps made %d identity or per-repository reads, want none", n)
	}
}

func TestSource_APresentCycleHealsATemporaryProbeFailure(t *testing.T) {
	core := &whoamiCore{err: &forgeapi.Error{Op: "Whoami", Kind: forgeapi.KindTransient, Message: "no route to host"}}
	m := sourceManager(t, map[string]forgeapi.Core{"github:github.com": core}, githubRecord())
	if err := m.probeConnection(t.Context(), "github:github.com"); err == nil {
		t.Fatal("Setup: Probe() over a dead network = nil, want the failure")
	}
	core.err = nil
	p := newTestPoller(NewManagerPRSource(m, fixedOrigins()), &fakeNotifier{}, &fakeGate{present: true})

	p.sweep(t.Context())

	if f := m.get("github:github.com"); f == nil || !f.Connected || f.LastError != "" {
		t.Errorf("row after a present cycle once the network is back = %+v, want connected with the error cleared", f)
	}
}

func TestSource_NothingConnectedReadsNoOrigins(t *testing.T) {
	stubPath(t)
	m := NewManager(t.TempDir())
	saveRecords(t, m.conns, githubRecord())
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatalf("Setup: Refresh() = %v", err)
	}
	read := 0
	src := NewManagerPRSource(m, func(context.Context) []RepoOrigin {
		read++
		return []RepoOrigin{{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}}
	})

	for _, present := range []bool{false, true} {
		if reads := src.Read(t.Context(), present, firstPage); len(reads) != 0 {
			t.Errorf("Read(present %v) with no stored credential = %+v, want no read", present, reads)
		}
	}
	if read != 0 {
		t.Errorf("the clones' origins were read %d times with nothing connected, want 0", read)
	}
}
