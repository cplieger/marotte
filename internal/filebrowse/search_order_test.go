package filebrowse

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// secretTree holds a 0700 folder and a file whose names hold the query at the top level, a folder
// and a file holding it two levels down, and names that do not.
func secretTree(t *testing.T, dir string) {
	t.Helper()
	writeTree(t, dir, map[string]string{
		"secrets/key.txt":                "",
		"secret-notes.txt":               "",
		"readme.txt":                     "",
		"nested/deep/my-secret.md":       "",
		"nested/deep/secretdir/inner.go": "",
		"src/main.go":                    "",
	})
	if err := os.Chmod(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// bigSibling is a tree of files the query does not match, under a name that sorts before every
// other top-level entry, three levels down so the levels above it stay small.
func bigSibling(t *testing.T, dir string, files int) {
	t.Helper()
	tree := make(map[string]string, files)
	for i := range files {
		tree[fmt.Sprintf("_big/a/b/f%04d.txt", i)] = ""
	}
	writeTree(t, dir, tree)
}

type kindedPath struct {
	rel  string
	kind FileMatchKind
}

func kindedRows(res FileSearchResult, base string) []kindedPath {
	out := make([]kindedPath, 0, len(res.Matches))
	for _, m := range res.Matches {
		rel, _ := filepath.Rel(base, m.Path)
		out = append(out, kindedPath{rel: rel, kind: m.Kind})
	}
	slices.SortFunc(out, func(a, b kindedPath) int {
		switch {
		case a.rel < b.rel:
			return -1
		case a.rel > b.rel:
			return 1
		}
		return 0
	})
	return out
}

var secretRows = []kindedPath{
	{rel: "nested/deep/my-secret.md", kind: matchKindName},
	{rel: "nested/deep/secretdir", kind: matchKindDir},
	{rel: "secret-notes.txt", kind: matchKindName},
	{rel: "secrets", kind: matchKindDir},
}

func TestNameSearch_FolderAndFileMatchAtEveryDepth(t *testing.T) {
	h, dir, prefix := testDir(t)
	secretTree(t, dir)

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "secret"})

	if got := kindedRows(res, dir); !slices.Equal(got, secretRows) {
		t.Errorf("rows = %v, want the folders and the files whose names hold the query: %v", got, secretRows)
	}
	if res.Truncated {
		t.Error("truncated = true on a tree well under every budget")
	}
}

// A sibling tree larger than the entry budget sorts first, and the budget must run out inside
// it, the deepest level, never before the shallow matches are visited. Driven at the counter, as
// the budget's own tests are.
func TestNameSearch_ShallowMatchesSurviveASiblingTreeOverTheEntryBudget(t *testing.T) {
	h, dir, _ := testDir(t)
	secretTree(t, dir)
	bigSibling(t, dir, 200)

	v := newNameVisitor(mustQuery(t, "secret"))
	walk := newSearchWalk(t.Context(), v, budgetFor(modeNames, false))
	walk.ignoreRules = true
	walk.entries = maxNameSearchEntries - 100
	walk.walkRoots([]loc{{m: &h.mounts[0], abs: dir}})
	res := v.result(walk.truncated)

	if got := kindedRows(res, dir); !slices.Equal(got, secretRows) {
		t.Errorf("rows = %v, want every match above the level the budget ran out in: %v", got, secretRows)
	}
	if !res.Truncated {
		t.Error("truncated = false, want true: the big tree's files were never listed")
	}
	if !walk.spent {
		t.Error("spent = false, want true: the entry budget stopped the walk, so a deeper search reads more")
	}
}

// The same rule in contents mode, against the file budget: the top-level file is read before any
// file of the deeper sibling tree spends the budget.
func TestContentSearch_ShallowFileSurvivesASiblingTreeOverTheFileBudget(t *testing.T) {
	h, dir, _ := testDir(t)
	tree := map[string]string{"z-top.txt": "needle\n", "m/mid.txt": "needle\n"}
	for i := range 10 {
		tree[fmt.Sprintf("a/b/f%02d.txt", i)] = "needle\n"
	}
	writeTree(t, dir, tree)

	walk, v := contentWalk(t.Context(), "needle", false)
	v.accepted = maxSearchFiles - 4
	walk.walkRoots([]loc{{m: &h.mounts[0], abs: dir}})
	res := v.result(walk.truncated)

	want := []string{"z-top.txt", "m/mid.txt", "a/b/f00.txt", "a/b/f01.txt"}
	if got := relPaths(res, dir); !slices.Equal(got, want) {
		t.Errorf("rows = %v, want the shallowest files first, in level order: %v", got, want)
	}
	if !res.Truncated {
		t.Error("truncated = false, want true: the file budget refused a file")
	}
	if !v.spent() {
		t.Error("spent() = false, want true: the file budget stopped the walk, so a deeper search reads more")
	}
}

// A walk stopped by its own budget offers the deeper search; a deep search does not offer it
// again, and runs under the deep budget rather than the keystroke one.
func TestSearch_BudgetStopOffersADeeperSearch(t *testing.T) {
	h, dir, prefix := testDir(t)
	secretTree(t, dir)
	saved, savedDeep := nameSearchDeadline, deepNameSearchDeadline
	t.Cleanup(func() { nameSearchDeadline, deepNameSearchDeadline = saved, savedDeep })
	nameSearchDeadline = 0

	stopped := namesSearch(t, h, map[string]string{"path": prefix, "q": "secret"})
	if !stopped.Truncated || !stopped.CanSearchDeeper {
		t.Errorf("deadline-stopped search: truncated = %v can_search_deeper = %v, want both true", stopped.Truncated, stopped.CanSearchDeeper)
	}

	deep := namesSearch(t, h, map[string]string{"path": prefix, "q": "secret", "deep": "1"})
	if deep.Truncated || deep.CanSearchDeeper {
		t.Errorf("deep search: truncated = %v can_search_deeper = %v, want both false under the deep deadline", deep.Truncated, deep.CanSearchDeeper)
	}
	if got := kindedRows(deep, dir); !slices.Equal(got, secretRows) {
		t.Errorf("deep search rows = %v, want %v", got, secretRows)
	}

	deepNameSearchDeadline = 0
	deepStopped := namesSearch(t, h, map[string]string{"path": prefix, "q": "secret", "deep": "1"})
	if !deepStopped.Truncated || deepStopped.CanSearchDeeper {
		t.Errorf("deadline-stopped deep search: truncated = %v can_search_deeper = %v, want true and false", deepStopped.Truncated, deepStopped.CanSearchDeeper)
	}
}

// A hole no budget made, here the depth cap, is truncation a deeper search cannot fill.
func TestSearch_DepthCapOffersNoDeeperSearch(t *testing.T) {
	h, dir, prefix := testDir(t)
	deep := dir
	for range maxSearchDepth + 1 {
		deep = filepath.Join(deep, "d")
	}
	writeTree(t, deep, map[string]string{"needle.txt": ""})

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "needle"})
	if !res.Truncated || res.CanSearchDeeper {
		t.Errorf("truncated = %v can_search_deeper = %v, want true and false", res.Truncated, res.CanSearchDeeper)
	}
}

func TestBudgetFor_DeepScalesEveryCountAndTakesTheDeepDeadline(t *testing.T) {
	for _, m := range []searchMode{modeNames, modeContents} {
		base, deep := budgetFor(m, false), budgetFor(m, true)
		if deep.entries != deepFactor*base.entries || deep.dirs != deepFactor*base.dirs || deep.files != deepFactor*base.files {
			t.Errorf("mode %d: deep = %+v, want %d times %+v", m, deep, deepFactor, base)
		}
		if deep.deadline <= base.deadline {
			t.Errorf("mode %d: deep deadline %v, want longer than %v", m, deep.deadline, base.deadline)
		}
	}
	// A deep names search lists its entry budget at the slow end of the measured cost per entry
	// within its deadline, as TestNameSearch_EntryBudgetFitsTheDeadline holds for the keystroke one.
	const perEntry = 10 * time.Microsecond
	if b := budgetFor(modeNames, true); time.Duration(b.entries)*perEntry > b.deadline {
		t.Errorf("deep names budget %d entries at %v does not fit %v", b.entries, perEntry, b.deadline)
	}
	// static-src/api-client.ts gives up on a request after 30 seconds, before a longer walk answers.
	if d := budgetFor(modeContents, true).deadline; d >= 30*time.Second {
		t.Errorf("deep contents deadline %v, want under the client's 30s request timeout", d)
	}
}

// On a "/" fan-out the mounts share one walk: a mount whose tree outgrows the budget does not
// starve a shallow match in the mount after it.
func TestNameSearch_FanOutKeepsALaterMountsShallowMatches(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if first > second {
		first, second = second, first
	}
	bigSibling(t, first, 200)
	secretTree(t, second)
	h, err := New(Sensitive{}, []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	roots := []loc{{m: &h.mounts[0], abs: h.mounts[0].dir}, {m: &h.mounts[1], abs: h.mounts[1].dir}}
	if roots[0].abs != first {
		roots[0], roots[1] = roots[1], roots[0]
	}

	v := newNameVisitor(mustQuery(t, "secret"))
	walk := newSearchWalk(t.Context(), v, budgetFor(modeNames, false))
	walk.ignoreRules = true
	walk.entries = maxNameSearchEntries - 100
	walk.walkRoots(roots)
	res := v.result(walk.truncated)

	if got := kindedRows(res, second); !slices.Equal(got, secretRows) {
		t.Errorf("rows = %v, want the second mount's matches: %v", got, secretRows)
	}
	if !res.Truncated {
		t.Error("truncated = false, want true: the first mount's big tree was never listed")
	}
}

// A directory whose inode changed between two of the cursor's opens is refused.
func TestDirCursor_RefusesADirectoryReplacedSinceItsFirstOpen(t *testing.T) {
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{"pub/notes.txt": "", "chats/c1.json": ""})
	root := &walkDir{f: searchDirAt(t, h, "/config"), abs: "/config"}
	pub := &walkDir{parent: root, name: "pub", abs: "/config/pub", depth: 1}
	other := &walkDir{parent: root, name: "chats", abs: "/config/chats", depth: 1}

	c := &dirCursor{}
	t.Cleanup(c.close)
	if _, err := c.open(pub); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := c.open(other); err != nil {
		t.Fatalf("moving the cursor to a sibling: %v", err)
	}
	if pub.f != nil {
		t.Fatal("the cursor kept pub open after moving to its sibling; the reopen below would not be exercised")
	}
	if err := os.Rename(filepath.Join(backing, "pub"), filepath.Join(backing, "pub.moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(backing, "chats"), filepath.Join(backing, "pub")); err != nil {
		t.Fatal(err)
	}

	if _, err := c.open(pub); !errors.Is(err, errDirReplaced) {
		t.Errorf("reopen of a replaced directory: err = %v, want errDirReplaced", err)
	}
	if pub.f != nil {
		t.Error("the replaced directory's handle was kept")
	}
}

// A root holding more entries than the budget still offers the ones read within it, the part of
// the chunk that crossed the budget included, and offers a deeper search for the rest.
func TestNameSearch_RootOverTheEntryBudgetListsTheEntriesReadWithinIt(t *testing.T) {
	h, dir, _ := testDir(t)
	const files, budget = 401, 300
	tree := make(map[string]string, files)
	for i := range files {
		tree[fmt.Sprintf("needle-%03d.txt", i)] = ""
	}
	writeTree(t, dir, tree)

	v := newNameVisitor(mustQuery(t, "needle"))
	walk := newSearchWalk(t.Context(), v, budgetFor(modeNames, false))
	walk.ignoreRules = true
	walk.entries = maxNameSearchEntries - budget
	walk.walkRoots([]loc{{m: &h.mounts[0], abs: dir}})
	res := v.result(walk.truncated)

	if res.Matched != budget {
		t.Errorf("matched = %d, want %d: every entry read within the budget is offered", res.Matched, budget)
	}
	if len(res.Matches) != maxSearchMatches {
		t.Errorf("rows = %d, want the reply cap %d", len(res.Matches), maxSearchMatches)
	}
	if !res.Truncated || !walk.spent {
		t.Errorf("truncated = %v spent = %v, want both true: %d entries were never read", res.Truncated, walk.spent, files-budget)
	}
}

// An ancestor that failed to reopen is not opened again for its other queued children: here it
// comes back under its name, and its second child is still skipped.
func TestDirCursor_SkipsTheChildrenOfAnAncestorThatFailedToReopen(t *testing.T) {
	h, backing := searchHandlerAt(t, "/config")
	writeTree(t, backing, map[string]string{"p/d1/a.txt": "", "p/d2/b.txt": "", "q/c.txt": ""})
	root := &walkDir{f: searchDirAt(t, h, "/config"), abs: "/config"}
	p := &walkDir{parent: root, name: "p", abs: "/config/p", depth: 1}
	q := &walkDir{parent: root, name: "q", abs: "/config/q", depth: 1}
	d1 := &walkDir{parent: p, name: "d1", depth: 2}
	d2 := &walkDir{parent: p, name: "d2", depth: 2}

	c := &dirCursor{}
	t.Cleanup(c.close)
	if _, err := c.open(d1); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := c.open(q); err != nil {
		t.Fatalf("moving the cursor to p's sibling: %v", err)
	}
	moved := filepath.Join(backing, "p.moved")
	if err := os.Rename(filepath.Join(backing, "p"), moved); err != nil {
		t.Fatal(err)
	}
	if failed, err := c.open(d2); !errors.Is(err, os.ErrNotExist) || failed != "/config/p" {
		t.Fatalf("open with p gone: failed %q err %v, want /config/p and ErrNotExist", failed, err)
	}
	if err := os.Rename(moved, filepath.Join(backing, "p")); err != nil {
		t.Fatal(err)
	}

	if _, err := c.open(d2); !errors.Is(err, errAncestorLost) {
		t.Errorf("open below an ancestor that failed: err = %v, want errAncestorLost", err)
	}
	if d2.f != nil || p.f != nil {
		t.Error("a handle below the failed ancestor was opened")
	}
}

// A failed fstat is reported as itself, not as a replaced directory.
func TestDirIdentity_AFailedStatIsNotAReplacement(t *testing.T) {
	f, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dirIdentity(f); err == nil || errors.Is(err, errDirReplaced) {
		t.Errorf("dirIdentity(closed file) err = %v, want an error other than errDirReplaced", err)
	}
}

// ignoredNeedleRoot builds a repository root of needle files its own .gitignore excludes and
// answers it with the entry budget that cuts its raw listing where cut says. It retries in a fresh
// directory until the filesystem's listing order gives cut a needle before the cut.
func ignoredNeedleRoot(t *testing.T, cut func(order []string) (budget int, ok bool)) (dir string, budget int) {
	t.Helper()
	tree := map[string]string{".gitignore": "needle-*\n", ".git/HEAD": ""}
	for i := range 200 {
		tree[fmt.Sprintf("needle-%03d.txt", i)] = ""
	}
	for range 20 {
		dir = t.TempDir()
		writeTree(t, dir, tree)
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		order := make([]string, len(entries))
		for i, e := range entries {
			order[i] = e.Name()
		}
		if budget, ok := cut(order); ok {
			return dir, budget
		}
	}
	t.Fatal("no listing order put a needle before the cut in 20 directories")
	return "", 0
}

// cutBefore cuts the listing just before name, when a needle comes earlier and, for each of
// alsoBefore, that name does too.
func cutBefore(name string, alsoBefore ...string) func([]string) (int, bool) {
	return func(order []string) (int, bool) {
		at := slices.Index(order, name)
		if at < 0 || !slices.ContainsFunc(order[:at], func(n string) bool { return strings.HasPrefix(n, "needle-") }) {
			return 0, false
		}
		for _, n := range alsoBefore {
			if !slices.Contains(order[:at], n) {
				return 0, false
			}
		}
		return at, true
	}
}

// A listing the entry budget cuts still applies its directory's own rule files, whichever of them
// the cut dropped: the needles read before the cut are all ignored.
func TestNameSearch_ACutListingKeepsItsOwnIgnoreRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		cut  func([]string) (int, bool)
	}{
		{"cut before the gitignore", cutBefore(".gitignore")},
		{"cut before the git directory", cutBefore(".git", ".gitignore")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, budget := ignoredNeedleRoot(t, tc.cut)
			h, err := New(Sensitive{}, []string{dir})
			if err != nil {
				t.Fatal(err)
			}

			v := newNameVisitor(mustQuery(t, "needle"))
			walk := newSearchWalk(t.Context(), v, budgetFor(modeNames, false))
			walk.ignoreRules = true
			walk.entries = maxNameSearchEntries - budget
			walk.walkRoots([]loc{{m: &h.mounts[0], abs: h.mounts[0].dir}})
			res := v.result(walk.truncated)

			if res.Matched != 0 {
				t.Errorf("matched = %d, want 0: the root's .gitignore excludes every needle", res.Matched)
			}
			if !res.Truncated || !walk.spent {
				t.Errorf("truncated = %v spent = %v, want both true: the budget cut the listing", res.Truncated, walk.spent)
			}
		})
	}
}

// leaveHook runs after on leaving the directory at the path it names.
type leaveHook struct {
	*nameVisitor
	at    string
	after func()
}

func (v *leaveHook) leave(d *walkDir) {
	v.nameVisitor.leave(d)
	if d.abs == v.at {
		v.after()
	}
}

// A queued directory whose parent vanished after its listing is skipped, and so is its sibling,
// without a reopen or a log line: a vanished subtree is no hole in the answer.
func TestNameSearch_ADirectoryThatVanishedSkipsItsQueuedChildrenQuietly(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Sensitive{}, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, map[string]string{
		"a/x/needle-a.txt": "", "p/d1/needle-1.txt": "", "p/d2/needle-2.txt": "", "z/needle-z.txt": "",
	})
	logs := captureFilebrowseLogs(t)

	v := &leaveHook{nameVisitor: newNameVisitor(mustQuery(t, "needle")), at: filepath.Join(dir, "a", "x"), after: func() {
		if err := os.Rename(filepath.Join(dir, "p"), filepath.Join(dir, "p.moved")); err != nil {
			t.Error(err)
		}
	}}
	walk := newSearchWalk(t.Context(), v, budgetFor(modeNames, false))
	walk.walkRoots([]loc{{m: &h.mounts[0], abs: h.mounts[0].dir}})
	res := v.result(walk.truncated)

	if n := strings.Count(logs.String(), "search opendir failed"); n != 0 {
		t.Errorf("logged %d opendir failures, want 0:\n%s", n, logs)
	}
	if res.Truncated {
		t.Error("truncated = true, want false: the vanished subtree left no hole")
	}
	var got []string
	for _, m := range res.Matches {
		got = append(got, m.Path)
	}
	if want := []string{filepath.Join(dir, "z", "needle-z.txt"), filepath.Join(dir, "a", "x", "needle-a.txt")}; !slices.Equal(got, want) {
		t.Errorf("matches = %v, want %v", got, want)
	}
}
