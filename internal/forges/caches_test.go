package forges

// Every cached answer is keyed by the account it was read through, and a
// sign-in or a sign-out drops the connection's entries in every family at once,
// so no surface serves the previous account's data.

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"golang.org/x/sync/semaphore"
)

// accountCore answers every read with rows named after the token it was built
// for, so a reply says which account read it.
type accountCore struct {
	fakeCore
	token string
}

func (c *accountCore) ListRepos(context.Context, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	return forgeapi.Page[forgeapi.Repository]{Items: []forgeapi.Repository{{Ref: forgeapi.RepoRef{DisplayPath: c.token + "/app"}}}}, nil
}

func (c *accountCore) RepoAffordances(context.Context, forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	return forgeapi.RepoAffordances{DefaultBranch: c.token}, nil
}

func (*accountCore) ConnectionCaps(context.Context) (forgeapi.ConnectionCaps, error) {
	return forgeapi.ConnectionCaps{}, nil
}

func (*accountCore) GrantCaps(context.Context) (forgeapi.GrantCaps, error) {
	return forgeapi.GrantCaps{}, nil
}

func accountCores(spec *connectionSpec, opts []forgeapi.Option) (forgeapi.Core, error) {
	token, err := forgeapi.Resolve(opts...).Credential.Token(context.Background())
	if err != nil {
		return nil, err
	}
	return &accountCore{prs: []forgeapi.PullRequest{prIn(spec.Kind.family(), "o/r", 1, forgeapi.CheckPending)}, token: token}, nil
}

func heldFor[T any](c *listCache[T], id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, e := range c.entries {
		if k.conn == id && !e.at.IsZero() {
			n++
		}
	}
	return n
}

// heldFamilies is how many of the four cache families hold an entry for id.
func heldFamilies(m *Manager, id string) [4]int {
	return [4]int{heldFor(m.lists.repos, id), heldFor(m.lists.prs, id), heldFor(m.lists.affordances, id), heldFor(m.lists.caps, id)}
}

func TestCaches_SignInAndSignOutDropEveryCacheFamilyAndInventory(t *testing.T) {
	for _, wired := range []bool{true, false} {
		name := "the inventory dropped on notify"
		if !wired {
			name = "a held entry of the old account"
		}
		t.Run(name, func(t *testing.T) { cachesDropped(t, wired) })
	}
}

func cachesDropped(t *testing.T, dropOnNotify bool) {
	isolateGit(t)
	m, _ := newManager(t, t.TempDir(), nil)
	m.newCore = accountCores
	m.Load(t.Context())
	x, y := githubRecord(), gitlabRecord()
	for _, s := range []connectionSpec{x, y} {
		if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &s, new(patFor(s.webBase(), "a-"+s.Host))); err != nil {
			t.Fatal(err)
		}
	}
	src := NewManagerPRSource(m, fixedOrigins())
	p := NewPRStatusPoller(src, &fakeNotifier{}, (&fakeGate{present: true}).Open)
	if dropOnNotify {
		m.notify = func(_ context.Context, c Change) {
			if c.AccountChanged {
				p.DropInventory(c.ID)
			}
		}
	}
	mux := inventoryRoutes(m, p)
	fill := func() {
		t.Helper()
		for _, id := range []string{x.ID, y.ID} {
			for _, path := range []string{"/repos", "/repos/v1.6f2f72/prs", "/repos/v1.6f2f72/affordances", "/capabilities"} {
				if rec := getRoute(t, mux, "/api/forges/"+id+path); rec.Code != http.StatusOK {
					t.Fatalf("Setup: GET %s%s = %d %s", id, path, rec.Code, rec.Body)
				}
			}
		}
		p.sweep(t.Context())
		for _, id := range []string{x.ID, y.ID} {
			if got := heldFamilies(m, id); slices.Contains(got[:], 0) {
				t.Fatalf("Setup: %s holds %v entries per family, want one in each", id, got)
			}
		}
		entryFor(t, p, x.ID)
	}
	inventoryOfX := func() (state string, held bool) {
		for _, e := range readInventory(t, mux) {
			if e.ForgeID == x.ID {
				return e.State, true
			}
		}
		return "", false
	}

	fill()
	account := viewFor(t, m, x.ID).Account
	stale, err := m.reader(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.signIn(t.Context(), m.deadlines.from(time.Now()), &x, new(patFor(x.webBase(), "b-github"))); err != nil {
		t.Fatal(err)
	}
	// A reader holding A's handle across the sign-in completes one read
	// through A's client after the drop; B never reads it.
	req, err := resolveList()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.repoPage(t.Context(), &stale, &req, false); err != nil {
		t.Fatal(err)
	}
	if got := heldFamilies(m, x.ID); got != [4]int{1, 0, 0, 0} {
		t.Errorf("after a sign-in to %s and one stale read it holds %v entries per family, want only the stale page", x.ID, got)
	}
	if got := heldFamilies(m, y.ID); got != [4]int{1, 1, 1, 1} {
		t.Errorf("after a sign-in to %s, %s holds %v entries per family, want all four kept", x.ID, y.ID, got)
	}
	if dropOnNotify {
		p.inv.mu.Lock()
		_, held := p.inv.entries[x.ID]
		p.inv.mu.Unlock()
		if held {
			t.Errorf("the inventory still holds %s's entry after the sign-in", x.ID)
		}
	}
	if state, _ := inventoryOfX(); state != inventoryLoading {
		t.Errorf("the inventory answers %s as %q after the sign-in, want loading", x.ID, state)
	}
	if now := viewFor(t, m, x.ID).Account; now == account {
		t.Fatalf("Setup: the sign-in kept account %q", now)
	}
	var list RepoList
	decodeJSON(t, getRoute(t, mux, "/api/forges/"+x.ID+"/repos"), &list)
	var aff RepoAffordances
	decodeJSON(t, getRoute(t, mux, "/api/forges/"+x.ID+"/repos/v1.6f2f72/affordances"), &aff)
	var caps Capabilities
	decodeJSON(t, getRoute(t, mux, "/api/forges/"+x.ID+"/capabilities"), &caps)
	b := viewFor(t, m, x.ID).Account
	if list.Account != b || len(list.Repos) != 1 || list.Repos[0].FullName != "b-github/app" || aff.Account != b ||
		aff.DefaultBranch != "b-github" || caps.Account != b {
		t.Errorf("B's replies = %+v, %+v, %+v; want B's rows tagged %q", list, aff, caps, b)
	}

	fill()
	if err := m.signOut(t.Context(), m.deadlines.from(time.Now()), x.ID); err != nil {
		t.Fatal(err)
	}
	if got := heldFamilies(m, x.ID); got != [4]int{} {
		t.Errorf("after a sign-out of %s it holds %v entries per family, want none", x.ID, got)
	}
	if got := heldFamilies(m, y.ID); got != [4]int{1, 1, 1, 1} {
		t.Errorf("after a sign-out of %s, %s holds %v entries per family, want all four kept", x.ID, y.ID, got)
	}
	if _, held := inventoryOfX(); held {
		t.Errorf("the inventory lists %s after its sign-out", x.ID)
	}
}

func TestListCache_DropRemovesOneConnectionsEntries(t *testing.T) {
	c := newListCaches()
	fill := func(id string) {
		t.Helper()
		ctx := t.Context()
		if _, err := c.repos.get(ctx, listKey(id, testSignIn, "", "repos", ""), false,
			func(context.Context) (RepoList, error) { return RepoList{}, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := c.prs.get(ctx, listKey(id, testSignIn, "v1.61", "prs", "open"), false,
			func(context.Context) (PRList, error) { return PRList{}, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := c.affordances.get(ctx, cacheKey{conn: id, account: testSignIn, scope: repoScope(id, "v1.61"), page: "affordances"},
			false, func(context.Context) (RepoAffordances, error) { return RepoAffordances{}, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := c.caps.get(ctx, cacheKey{conn: id, account: testSignIn, scope: id, page: "capabilities"}, false,
			func(context.Context) (Capabilities, error) { return Capabilities{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	held := func(id string) [4]int {
		return [4]int{heldFor(c.repos, id), heldFor(c.prs, id), heldFor(c.affordances, id), heldFor(c.caps, id)}
	}
	fill("x")
	fill("y")

	c.drop("x")

	if got := held("x"); got != [4]int{} {
		t.Errorf("x holds %v entries per family after drop(x), want none", got)
	}
	if got := held("y"); got != [4]int{1, 1, 1, 1} {
		t.Errorf("y holds %v entries per family after drop(x), want all four", got)
	}
}

func TestListCache_AFillInFlightAcrossADropStoresNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		held := newHeldFill()
		got := getAsync(t, c, testKey("k"), held.fill)
		synctest.Wait()

		// A sign-in landing here is the real case: the fill already read the forge
		// as the previous account, so its answer reaches the caller who asked and
		// goes no further.
		c.drop(testForge)
		close(held.release)
		<-got

		if _, ok := c.entries[testKey("k")]; ok {
			t.Error("an entry a fill begun before the drop stored, want none")
		}
	})

	t.Run("two accounts' cold fills of one key", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := newListCache[[]string](time.Hour, semaphore.NewWeighted(maxListFills))
			a, b := listKey(testForge, "sign-in-a", "", "repos", ""), listKey(testForge, "sign-in-b", "", "repos", "")
			release := make(chan struct{})
			fill := func(who string) func(context.Context) ([]string, error) {
				return func(context.Context) ([]string, error) {
					<-release
					return []string{who}, nil
				}
			}
			gotA := getAsync(t, c, a, fill("a"))
			gotB := getAsync(t, c, b, fill("b"))
			synctest.Wait()
			close(release)
			if va, vb := <-gotA, <-gotB; len(va) != 1 || va[0] != "a" || len(vb) != 1 || vb[0] != "b" {
				t.Errorf("the two accounts' fills answered %v and %v, want each its own", va, vb)
			}
		})
	})
}
