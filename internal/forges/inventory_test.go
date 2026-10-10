package forges

// The inventory the poller fills: every scope read once a present cycle, the
// authored call alone otherwise, and each entry carrying what the cycle read.

import (
	"cmp"
	"context"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// scopeCore serves ListMyPRs per owner ("" is the authored list), from walks
// keyed by cursor where the owner has one and from pages otherwise, and counts
// the account reads and every list it was asked for.
type scopeCore struct {
	forgeapi.Core
	pages   map[string]forgeapi.Page[forgeapi.PullRequest]
	walks   map[string]map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]
	errs    map[string]error
	afters  map[string][]forgeapi.Cursor
	login   string
	asked   []string
	budget  forgeapi.BudgetState
	whoamis int
}

func (c *scopeCore) Whoami(context.Context) (forgeapi.Account, error) {
	c.whoamis++
	return forgeapi.Account{Login: c.login}, nil
}

func (c *scopeCore) ListMyPRs(_ context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	set, err := forgeapi.ResolveList(opts...)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	c.asked = append(c.asked, set.Owner)
	c.afters[set.Owner] = append(c.afters[set.Owner], set.After)
	if err := c.errs[set.Owner]; err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if walk, ok := c.walks[set.Owner]; ok {
		return walk[set.After], nil
	}
	return c.pages[set.Owner], nil
}

func (c *scopeCore) BudgetState() forgeapi.BudgetState { return c.budget }

func newScopeCore() *scopeCore {
	return &scopeCore{
		login: "bob", pages: map[string]forgeapi.Page[forgeapi.PullRequest]{}, errs: map[string]error{},
		walks: map[string]map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{}, afters: map[string][]forgeapi.Cursor{},
	}
}

func inventoryPoller(t *testing.T, cores map[string]forgeapi.Core, origins []RepoOrigin, recs ...connectionRecord,
) (*PRStatusPoller, *fakeGate, *fakeNotifier, *Manager) {
	t.Helper()
	m := sourceManager(t, cores, recs...)
	g := &fakeGate{present: true}
	n := &fakeNotifier{}
	return NewPRStatusPoller(NewManagerPRSource(m, fixedOrigins(origins...)), n, g.Open), g, n, m
}

func entriesOf(p *PRStatusPoller) []InventoryEntry {
	p.inv.mu.Lock()
	defer p.inv.mu.Unlock()
	out := slices.Collect(maps.Values(p.inv.entries))
	slices.SortFunc(out, func(a, b InventoryEntry) int { return cmp.Compare(a.ForgeID, b.ForgeID) })
	return out
}

func entryFor(t *testing.T, p *PRStatusPoller, id string) InventoryEntry {
	t.Helper()
	for _, e := range entriesOf(p) {
		if e.ForgeID == id {
			return e
		}
	}
	t.Fatalf("no inventory entry for %s in %+v", id, entriesOf(p))
	return InventoryEntry{}
}

func scopesOf(e *InventoryEntry) []Scope {
	out := make([]Scope, 0, len(e.Scopes))
	for i := range e.Scopes {
		out = append(out, Scope{Kind: e.Scopes[i].Scope, Owner: e.Scopes[i].Owner})
	}
	return out
}

func rowNumbers(s *InventoryScope) []int {
	out := make([]int, 0, len(s.Rows))
	for i := range s.Rows {
		out = append(out, s.Rows[i].Number)
	}
	slices.Sort(out)
	return out
}

func page(prs ...forgeapi.PullRequest) forgeapi.Page[forgeapi.PullRequest] {
	return forgeapi.Page[forgeapi.PullRequest]{Items: prs}
}

func TestInventory_PresentCycleRunsEveryScope(t *testing.T) {
	core := newScopeCore()
	core.pages["bob"] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	core.pages[""] = page(prIn(forgeapi.FamilyGitHub, "carol/lib", 2, forgeapi.CheckPending))
	core.pages["acme"] = page(prIn(forgeapi.FamilyGitHub, "acme/site", 3, forgeapi.CheckPending))
	rec := githubRecord()
	rec.OwnerScopes = []string{"acme"}
	p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)

	p.sweep(t.Context())

	if core.whoamis != 1 || !slices.Equal(core.asked, []string{"bob", "", "acme"}) {
		t.Errorf("a present cycle made %d account reads and listed owners %q, want 1 and [bob, (authored), acme]",
			core.whoamis, core.asked)
	}
	e := entryFor(t, p, rec.ID)
	want := []Scope{{Kind: scopeOwner, Owner: "bob"}, authoredScope, {Kind: scopeAdded, Owner: "acme"}}
	if got := scopesOf(&e); !slices.Equal(got, want) {
		t.Errorf("entry scopes = %+v, want %+v", got, want)
	}
	for i, n := range []int{1, 2, 3} {
		if got := rowNumbers(&e.Scopes[i]); !slices.Equal(got, []int{n}) {
			t.Errorf("scope %+v rows = %v, want [%d]", want[i], got, n)
		}
	}
	if e.State != inventoryReady || e.CycleID != "1" || e.FetchedAt <= 0 || e.Error != nil {
		t.Errorf("entry state %q, cycle %q, fetched_at %d, error %+v; want ready from cycle 1, stamped, no error",
			e.State, e.CycleID, e.FetchedAt, e.Error)
	}
	p.sweep(t.Context())
	if core.whoamis != 2 || len(core.asked) != 6 || entryFor(t, p, rec.ID).CycleID != "2" {
		t.Errorf("after a second present cycle: %d account reads, %d lists, entry cycle %q; want 2, 6 and 2",
			core.whoamis, len(core.asked), entryFor(t, p, rec.ID).CycleID)
	}
}

func TestInventory_PushOnlyCycleRunsTheAuthoredCallAlone(t *testing.T) {
	core := newScopeCore()
	core.pages[""] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	rec := githubRecord()
	rec.OwnerScopes = []string{"acme"}
	clone := RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}
	p, g, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{clone}, rec)
	g.present, g.push = false, true

	p.sweep(t.Context())
	if core.whoamis != 0 || !slices.Equal(core.asked, []string{""}) {
		t.Errorf("a push-only cycle made %d account reads and listed owners %q, want none and the authored call alone",
			core.whoamis, core.asked)
	}
	if got := entriesOf(p); len(got) != 0 {
		t.Errorf("a push-only cycle wrote the inventory: %+v", got)
	}

	g.present = true
	p.sweep(t.Context())
	g.present = false
	p.sweep(t.Context())
	if e := entryFor(t, p, rec.ID); e.CycleID != "2" {
		t.Errorf("the entry after a present cycle then a push-only one is from cycle %q, want 2: push-only writes nothing", e.CycleID)
	}
}

func TestInventory_GitLabHasNoOwnerScopeForTheLogin(t *testing.T) {
	core := newScopeCore()
	rec := gitlabRecord()
	rec.OwnerScopes = []string{"group/sub"}
	p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)

	p.sweep(t.Context())

	if !slices.Equal(core.asked, []string{"", "group/sub"}) {
		t.Errorf("a GitLab present cycle listed owners %q, want the authored call and the added group only", core.asked)
	}
	e := entryFor(t, p, rec.ID)
	if got, want := scopesOf(&e), []Scope{authoredScope, {Kind: scopeAdded, Owner: "group/sub"}}; !slices.Equal(got, want) {
		t.Errorf("GitLab entry scopes = %+v, want %+v", got, want)
	}
}

func TestInventory_RowsCarryNoBody(t *testing.T) {
	withBody := func(pr forgeapi.PullRequest) forgeapi.PullRequest {
		pr.Body = "the description, read on demand by the detail"
		return pr
	}
	core := newScopeCore()
	core.pages["bob"] = page(withBody(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending)))
	core.pages[""] = page(withBody(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending)))
	rec := githubRecord()
	p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)

	p.sweep(t.Context())

	e := entryFor(t, p, rec.ID)
	for i := range e.Scopes {
		for _, row := range e.Scopes[i].Rows {
			if row.Body != "" || row.Title != "PR bob/app" {
				t.Errorf("%s row = body %q, title %q; want no body and the row's title", e.Scopes[i].Scope, row.Body, row.Title)
			}
		}
	}
}

func TestInventory_FailedScopeKeepsItsRowsAndCarriesTheError(t *testing.T) {
	core := newScopeCore()
	core.pages["bob"] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	core.pages[""] = page(prIn(forgeapi.FamilyGitHub, "carol/lib", 2, forgeapi.CheckPending))
	rec := githubRecord()
	p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	p.sweep(t.Context())

	core.errs["bob"] = &forgeapi.Error{Kind: forgeapi.KindNotFound, Code: forgeapi.CodeOwnerUnresolved, DiagID: "diag-owner"}
	p.sweep(t.Context())
	e := entryFor(t, p, rec.ID)
	wantErr := InventoryError{Code: forgeapi.CodeOwnerUnresolved, Kind: "not_found", DiagID: "diag-owner"}
	if e.Error == nil || *e.Error != wantErr || e.State != inventoryPartial {
		t.Errorf("entry with its owner scope failing: error %+v, state %q; want %+v and partial", e.Error, e.State, wantErr)
	}
	if got := rowNumbers(&e.Scopes[0]); !slices.Equal(got, []int{1}) {
		t.Errorf("the failed owner scope's rows = %v, want the previous cycle's [1]", got)
	}

	limited := &forgeapi.Error{Kind: forgeapi.KindRateLimited, RetryAfter: 90 * time.Second}
	core.errs["bob"], core.errs[""] = limited, limited
	p.sweep(t.Context())
	e = entryFor(t, p, rec.ID)
	wantErr = InventoryError{Kind: "rate_limited", RetryAfterS: 90}
	if e.Error == nil || *e.Error != wantErr || e.State != inventoryFailed {
		t.Errorf("entry with every scope failing: error %+v, state %q; want %+v and failed", e.Error, e.State, wantErr)
	}
	if got := [][]int{rowNumbers(&e.Scopes[0]), rowNumbers(&e.Scopes[1])}; !reflect.DeepEqual(got, [][]int{{1}, {2}}) {
		t.Errorf("rows of the failed scopes = %v, want the last read [[1] [2]]", got)
	}

	clear(core.errs)
	p.sweep(t.Context())
	if e = entryFor(t, p, rec.ID); e.Error != nil || e.State != inventoryReady {
		t.Errorf("entry after a clean cycle: error %+v, state %q; want none and ready", e.Error, e.State)
	}
}

func TestInventory_NoticesComeFromTheAuthoredRowsOnly(t *testing.T) {
	authored := func(check forgeapi.CheckState) map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest] {
		first := page(prIn(forgeapi.FamilyGitHub, "bob/app", 9, check))
		first.Next = "c2"
		return map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": first, "c2": page()}
	}
	core := newScopeCore()
	core.pages["bob"] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 7, forgeapi.CheckPending))
	core.walks[""] = authored(forgeapi.CheckPending)
	rec := githubRecord()
	clone := RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}
	p, _, n, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{clone}, rec)
	p.sweep(t.Context())

	// #7 flips while the authored walk is still on its way, so nothing prunes it.
	core.pages["bob"] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 7, forgeapi.CheckFailing))
	p.sweep(t.Context())
	core.walks[""] = authored(forgeapi.CheckPassing)
	p.sweep(t.Context())

	if len(n.sent) != 1 || n.sent[0].subject.Key != subjectKey(rec.ID, "bob/app", 9) {
		t.Errorf("sent %+v, want one notice for authored #9: owner rows in a tracked clone notify nothing", n.sent)
	}
	if _, ok := p.seen[subjectKey(rec.ID, "bob/app", 7)]; ok || len(p.seen) != 1 {
		t.Errorf("tracked subjects %v, want #9 alone", p.seen)
	}
}

func TestInventory_DisconnectDropsTheEntry(t *testing.T) {
	t.Run("Disconnected", func(t *testing.T) {
		rec := githubRecord()
		p, _, _, m := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: newScopeCore()}, nil, rec)
		p.sweep(t.Context())
		entryFor(t, p, rec.ID)

		if err := m.store.Delete(rec.ID); err != nil {
			t.Fatalf("Setup: delete the credential: %v", err)
		}
		m.invalidate()
		p.sweep(t.Context())
		if got := entriesOf(p); len(got) != 0 {
			t.Errorf("entries after the connection lost its credential = %+v, want none", got)
		}
	})
	t.Run("GateClosed", func(t *testing.T) {
		rec := githubRecord()
		p, g, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: newScopeCore()}, nil, rec)
		p.sweep(t.Context())
		entryFor(t, p, rec.ID)

		g.present = false
		p.sweep(t.Context())
		if got := entriesOf(p); len(got) != 0 {
			t.Errorf("entries after the gate closed = %+v, want none", got)
		}
	})
	t.Run("Replaced", func(t *testing.T) {
		rec := githubRecord()
		clone := RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}
		p, g, _, m := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: newScopeCore()}, []RepoOrigin{clone}, rec)
		p.sweep(t.Context())
		entryFor(t, p, rec.ID)

		seedStoreRecord(t, m.configDir, rec.ID, "carol")
		m.invalidate()
		g.present, g.push = false, true
		p.sweep(t.Context())
		if got := entriesOf(p); len(got) != 0 {
			t.Errorf("entries after the connection was signed in as another account = %+v, want bob's dropped", got)
		}
	})
}

func TestInventory_ScopeRowsAreTheWalksAcrossCycles(t *testing.T) {
	more := forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 1}
	first := page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	first.Next, first.Partial = "c2", &more
	second := page(prIn(forgeapi.FamilyGitHub, "bob/app", 2, forgeapi.CheckPending))
	core := newScopeCore()
	core.walks[""] = map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": first, "c2": second}
	rec := githubRecord()
	p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	authored := func() InventoryScope {
		e := entryFor(t, p, rec.ID)
		return e.Scopes[1]
	}

	p.sweep(t.Context())
	if s, e := authored(), entryFor(t, p, rec.ID); !slices.Equal(rowNumbers(&s), []int{1}) || s.Next != "c2" ||
		s.Partial == nil || s.Partial.Reason != more.Reason.String() || e.State != inventoryPartial {
		t.Errorf("after the walk's first page: rows %v, next %q, partial %+v, state %q; want [1], c2, the page's own and partial",
			rowNumbers(&s), s.Next, s.Partial, e.State)
	}
	p.sweep(t.Context())
	if s, e := authored(), entryFor(t, p, rec.ID); !slices.Equal(rowNumbers(&s), []int{1, 2}) || s.Next != "" ||
		s.Partial != nil || e.State != inventoryReady {
		t.Errorf("after the walk's last page: rows %v, next %q, partial %+v, state %q; want [1 2], none, none and ready",
			rowNumbers(&s), s.Next, s.Partial, e.State)
	}

	gone := page(prIn(forgeapi.FamilyGitHub, "bob/app", 2, forgeapi.CheckPending))
	gone.Next = "c2"
	core.walks[""] = map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": gone, "c2": page()}
	p.sweep(t.Context())
	if s := authored(); !slices.Equal(rowNumbers(&s), []int{1, 2}) {
		t.Errorf("mid-walk without #1: rows %v, want [1 2]: only the page that ends a walk drops a row", rowNumbers(&s))
	}
	p.sweep(t.Context())
	if s := authored(); !slices.Equal(rowNumbers(&s), []int{2}) {
		t.Errorf("after a walk that never read #1: rows %v, want [2]", rowNumbers(&s))
	}
	if want := []forgeapi.Cursor{"", "c2", "", "c2"}; !slices.Equal(core.afters[""], want) {
		t.Errorf("authored cursors over four cycles = %q, want %q", core.afters[""], want)
	}
}

func TestInventory_RemovedOwnerScopeForgetsItsWalk(t *testing.T) {
	acme := page(prIn(forgeapi.FamilyGitHub, "acme/site", 3, forgeapi.CheckPending))
	acme.Next = "a2"
	core := newScopeCore()
	core.walks["acme"] = map[forgeapi.Cursor]forgeapi.Page[forgeapi.PullRequest]{"": acme, "a2": page()}
	rec := githubRecord()
	rec.OwnerScopes = []string{"acme"}
	p, _, _, m := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	setOwners := func(owners ...string) {
		t.Helper()
		r := githubRecord()
		r.OwnerScopes = owners
		saveRecords(t, m.conns, r)
		m.invalidate()
	}

	p.sweep(t.Context())
	setOwners()
	p.sweep(t.Context())
	if e := entryFor(t, p, rec.ID); len(e.Scopes) != 2 {
		t.Errorf("entry scopes after the owner was removed = %+v, want the owner and authored scopes alone", scopesOf(&e))
	}
	setOwners("acme")
	p.sweep(t.Context())

	if want := []forgeapi.Cursor{"", ""}; !slices.Equal(core.afters["acme"], want) {
		t.Errorf("acme cursors = %q, want %q: an owner added again starts its walk again", core.afters["acme"], want)
	}
}

func TestInventory_AddedOwnerIsReadNextCycle(t *testing.T) {
	core := newScopeCore()
	core.pages["acme"] = page(prIn(forgeapi.FamilyGitHub, "acme/site", 3, forgeapi.CheckPending))
	rec := githubRecord()
	p, _, _, m := inventoryPoller(t, map[string]forgeapi.Core{rec.ID: core}, nil, rec)
	mux := http.NewServeMux()
	NewHTTPHandler(m, nil).RegisterRoutes(mux)

	p.sweep(t.Context())
	answeredOwners(t, putOwners(t, mux, rec.ID, ownersBodyOf("acme")))
	p.sweep(t.Context())

	if want := []string{"bob", "", "bob", "", "acme"}; !slices.Equal(core.asked, want) {
		t.Errorf("owners listed over a cycle, an owner added, then a cycle = %q, want %q", core.asked, want)
	}
	e := entryFor(t, p, rec.ID)
	want := []Scope{{Kind: scopeOwner, Owner: "bob"}, authoredScope, {Kind: scopeAdded, Owner: "acme"}}
	if got := scopesOf(&e); !slices.Equal(got, want) || !slices.Equal(rowNumbers(&e.Scopes[2]), []int{3}) {
		t.Errorf("entry scopes after the owner was added = %+v, want %+v with acme's row", got, want)
	}

	answeredOwners(t, putOwners(t, mux, rec.ID, ownersBodyOf()))
	p.sweep(t.Context())
	if got := core.asked[len(core.asked)-2:]; !slices.Equal(got, []string{"bob", ""}) {
		t.Errorf("owners listed by the cycle after the owners were cleared = %q, want [bob, (authored)]", got)
	}
}

func TestInventory_ClonesAreTheCloneJoin(t *testing.T) {
	origins := []RepoOrigin{
		{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"},
		{Dir: "tool", WebBase: "https://GitHub.com", Slug: "Bob/Tool"},
		{Dir: "ssh", WebBase: "", Slug: "bob/ssh"},
		{Dir: "group", WebBase: "https://gitlab.com", Slug: "group/sub/repo"},
	}
	gh, gl := githubRecord(), gitlabRecord()
	p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{gh.ID: newScopeCore(), gl.ID: newScopeCore()}, origins, gh, gl)

	p.sweep(t.Context())

	wantGH := []CloneRepo{
		{Dir: "app", ForgeID: gh.ID, RepoID: repoIDOf("bob/app")},
		{Dir: "tool", ForgeID: gh.ID, RepoID: repoIDOf("bob/tool")},
	}
	if got := entryFor(t, p, gh.ID).Clones; !slices.Equal(got, wantGH) {
		t.Errorf("GitHub entry clones = %+v, want %+v", got, wantGH)
	}
	wantGL := []CloneRepo{{Dir: "group", ForgeID: gl.ID, RepoID: repoIDOf("group/sub/repo")}}
	if got := entryFor(t, p, gl.ID).Clones; !slices.Equal(got, wantGL) {
		t.Errorf("GitLab entry clones = %+v, want %+v", got, wantGL)
	}
}

func TestInventory_BudgetAndCredentialStateAreCarried(t *testing.T) {
	reset := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	gh, gl := newScopeCore(), newScopeCore()
	gh.budget = forgeapi.BudgetState{Remaining: 4990, Reset: reset, LastCost: 1}
	gl.budget = forgeapi.BudgetState{Remaining: forgeapi.BudgetRemainingUnknown}
	ghRec, glRec := githubRecord(), gitlabRecord()
	p, _, _, m := inventoryPoller(t, map[string]forgeapi.Core{ghRec.ID: gh, glRec.ID: gl}, nil, ghRec, glRec)
	if err := m.store.Save(ghRec.ID, creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://github.com", Kind: forgeapi.CredKindStaticPAT,
		Token: "test-token", Issued: time.Now(), Account: "bob", Usability: creds.UsabilityReconnectRequired,
	}); err != nil {
		t.Fatalf("Setup: mark the GitHub credential: %v", err)
	}

	p.sweep(t.Context())

	e := entryFor(t, p, ghRec.ID)
	if want := (InventoryBudget{Remaining: 4990, Reset: reset.UnixMilli(), LastCost: 1}); e.Budget == nil || *e.Budget != want ||
		e.Credential != "reconnect_required" {
		t.Errorf("GitHub entry budget %+v, credential %q; want %+v and reconnect_required", e.Budget, e.Credential, want)
	}
	e = entryFor(t, p, glRec.ID)
	if want := (InventoryBudget{Remaining: -1}); e.Budget == nil || *e.Budget != want || e.Credential != "valid" {
		t.Errorf("GitLab entry budget %+v, credential %q; want %+v and valid", e.Budget, e.Credential, want)
	}

	t.Run("NoClient", func(t *testing.T) {
		rec := githubRecord()
		p, _, _, _ := inventoryPoller(t, map[string]forgeapi.Core{}, nil, rec)
		p.sweep(t.Context())

		e := entryFor(t, p, rec.ID)
		if e.State != inventoryFailed || e.Error == nil || e.Error.Kind != "unknown" || e.Budget != nil || e.Credential != "unknown" {
			t.Errorf("entry of a connection whose client cannot be built: state %q, error %+v, budget %+v, credential %q; "+
				"want failed, kind unknown, no budget and unknown", e.State, e.Error, e.Budget, e.Credential)
		}
	})
}
