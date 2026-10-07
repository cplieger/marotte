package filebrowse

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// relPaths is a result's paths relative to base, in reply order.
func relPaths(res FileSearchResult, base string) []string {
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, strings.TrimPrefix(m.Path, base+"/"))
	}
	return out
}

func namesSearch(t *testing.T, h *Handler, params map[string]string) FileSearchResult {
	t.Helper()
	return decodeSearch(t, searchReq(t, h, params))
}

// Markdown that MENTIONS .css sits in a dot-directory the walk reaches first,
// and hundreds of names that hold ".css" only as an interior word. Ranking over the whole walk
// still puts the two real stylesheets first.
func TestNameSearch_RanksAcrossTheWholeWalkNotWalkOrder(t *testing.T) {
	h, dir, prefix := testDir(t)
	tree := map[string]string{
		"web/static-src/css/01-tokens.css": ":root {}\n",
		"web/static-src/css/19-files.css":  ".fb {}\n",
	}
	for i := range 30 {
		tree[fmt.Sprintf(".kiro/steering/doc%02d.md", i)] = strings.Repeat("mentions .css here\n", 10)
	}
	for i := range 250 {
		tree[fmt.Sprintf(".agents/plan-%03d.css.md", i)] = "plan\n"
	}
	writeTree(t, dir, tree)

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": ".css"})

	if len(res.Matches) != maxSearchMatches || res.Matched != 252 {
		t.Fatalf("rows = %d matched = %d, want the cap and every one of the 252 matching names", len(res.Matches), res.Matched)
	}
	got := relPaths(res, dir)[:2]
	want := []string{"web/static-src/css/19-files.css", "web/static-src/css/01-tokens.css"}
	if !slices.Equal(got, want) {
		t.Errorf("first rows = %v, want the two stylesheets, the shorter name first: %v", got, want)
	}
	if res.Truncated {
		t.Error("truncated = true: the walk saw the whole tree, the reply was only cut")
	}
}

func TestNameSearch_ReadsNoFileContents(t *testing.T) {
	h, dir, prefix := testDir(t)
	payload := strings.Repeat("needle ", maxSearchFileSize/7)
	tree := map[string]string{"prose.txt": "the needle is in here\n"}
	for i := range searchWorkers {
		tree[fmt.Sprintf("big-%d.txt", i)] = payload
	}
	writeTree(t, dir, tree)

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "needle"})
	if len(res.Matches) != 0 {
		t.Errorf("matches = %v, want none: names mode never looks inside a file", matchPaths(res))
	}

	v := newNameVisitor(mustQuery(t, "needle"))
	walk := &searchWalk{ctx: t.Context(), v: v, maxDirs: maxNameSearchDirs, ignoreRules: true}
	grew := allocatedBy(func() { walk.addRoot(loc{m: &h.mounts[0], abs: dir}) })
	if bound := uint64(256 << 10); grew > bound {
		t.Errorf("a names walk over %d large files allocated %d bytes, want under %d: it read them", searchWorkers, grew, bound)
	}
}

func mustQuery(t *testing.T, raw string) nameQuery {
	t.Helper()
	q, err := compileQuery(raw, false)
	if err != nil {
		t.Fatalf("compileQuery(%q): %v", raw, err)
	}
	return q
}

func TestNameSearch_RanksExactPrefixSuffixWordInterior(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"atokb":      "",
		"a-tok-b":    "",
		"myTokx":     "",
		"notes.tok":  "",
		"tokens.css": "",
		"tok":        "",
	})

	got := relPaths(namesSearch(t, h, map[string]string{"path": prefix, "q": "tok"}), dir)
	want := []string{"tok", "tokens.css", "notes.tok", "myTokx", "a-tok-b", "atokb"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want exact, prefix, suffix, two word boundaries (shorter first), interior: %v", got, want)
	}
}

func TestNameSearch_FewerSegmentsRankAheadWithinATier(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"x/toka.go": "", "tokens-with-a-long-name.go": ""})

	got := relPaths(namesSearch(t, h, map[string]string{"path": prefix, "q": "tok"}), dir)
	if want := []string{"tokens-with-a-long-name.go", "x/toka.go"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestNameSearch_TermsAreANDed(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"tokens.css": "", "tokens.go": "", "files.css": ""})

	got := relPaths(namesSearch(t, h, map[string]string{"path": prefix, "q": "css  tok"}), dir)
	if want := []string{"tokens.css"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

func TestNameSearch_SlashTermMatchesThePath(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"static-src/css/01-tokens.css": "",
		"tokens.css":                   "",
		"static-src/tokens.ts":         "",
	})

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "css/ tokens"})
	if got, want := relPaths(res, dir), []string{"static-src/css/01-tokens.css"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if got, want := res.Matches[0].Ranges, []MatchRange{{Start: 3, End: 9}}; !slices.Equal(got, want) {
		t.Errorf("ranges = %v, want %v: only the basename term marks the basename", got, want)
	}
}

func TestNameSearch_GlobAndQuotedTerms(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"a.css":       "",
		"a.css.map":   "",
		"src/x/b.ts":  "",
		"lib/c.ts":    "",
		"a b.txt":     "",
		"a-b.txt":     "",
		"src/[x].txt": "",
		`a"[b.txt`:    "",
		`a"b.txt`:     "",
		`a\b.txt`:     "",
	})
	for _, tc := range []struct{ q, why string }{
		{q: "*.css", why: "a basename glob matches the whole name"},
		{q: "src/**/*.ts", why: "a slashed glob matches the path"},
		{q: `"a b"`, why: "a quoted term holds its space"},
		{q: `"[x]"`, why: "a quoted term has no glob characters"},
		{q: `"a\"[b"`, why: `\" is a quote inside the literal, not its end`},
		{q: `"a\"b"`, why: `\" keeps the literal one term`},
		{q: `"a\\b"`, why: `\\ is one backslash`},
		{q: `"a\b"`, why: "any other backslash is itself"},
	} {
		got := relPaths(namesSearch(t, h, map[string]string{"path": prefix, "q": tc.q}), dir)
		want := map[string][]string{
			"*.css":       {"a.css"},
			"src/**/*.ts": {"src/x/b.ts"},
			`"a b"`:       {"a b.txt"},
			`"[x]"`:       {"src/[x].txt"},
			`"a\"[b"`:     {`a"[b.txt`},
			`"a\"b"`:      {`a"b.txt`},
			`"a\\b"`:      {`a\b.txt`},
			`"a\b"`:       {`a\b.txt`},
		}[tc.q]
		if !slices.Equal(got, want) {
			t.Errorf("q=%s rows = %v, want %v: %s", tc.q, got, want, tc.why)
		}
	}
}

func TestNameSearch_EmptyQueryWithFilterListsFilesOnly(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"z.css":                "",
		"b.css":                "",
		"css/x.css":            "",
		"css/notes.md":         "",
		"a.css.map":            "",
		"styles/y.scss":        "",
		"theme.css/readme.txt": "",
	})

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "", "files": ".css"})
	want := []string{"b.css", "z.css", "css/x.css", "theme.css/readme.txt"}
	if got := relPaths(res, dir); !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v: files only, by depth then path", got, want)
	}
	for _, m := range res.Matches {
		if m.Kind != MatchKindName || len(m.Ranges) != 0 {
			t.Errorf("%s: kind %q ranges %v, want a plain name row", m.Path, m.Kind, m.Ranges)
		}
	}
	if res.Scanned != 10 {
		t.Errorf("scanned = %d, want 10: every entry the walk visited, the filter's refusals included", res.Scanned)
	}
}

func TestNameSearch_EmptyQueryAndFilterDoesNotWalk(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"a.txt": ""})

	for _, q := range []string{"", "   "} {
		rec := searchReq(t, h, map[string]string{"path": prefix, "q": q, "files": " , "})
		res := decodeSearch(t, rec)
		if len(res.Matches) != 0 || res.Scanned != 0 || res.Truncated {
			t.Errorf("q=%q result = %+v, want an empty reply with no walk", q, res)
		}
		if !strings.Contains(rec.Body.String(), `"matches":[]`) {
			t.Errorf("q=%q body must carry an empty array, not null", q)
		}
	}
}

// Driven at the counter, because the real budget is a million entries.
func TestNameSearch_EntryBudgetStopsAndSaysSo(t *testing.T) {
	h, dir, _ := testDir(t)
	writeTree(t, dir, map[string]string{"needle-a.txt": "", "needle-b.txt": ""})

	for _, tc := range []struct {
		spent     int
		wantRows  int
		truncated bool
	}{{spent: maxSearchEntries - 2, wantRows: 2, truncated: false}, {spent: maxSearchEntries - 1, wantRows: 0, truncated: true}} {
		v := newNameVisitor(mustQuery(t, "needle"))
		walk := &searchWalk{ctx: t.Context(), v: v, maxDirs: maxNameSearchDirs, ignoreRules: true, entries: tc.spent}
		walk.addRoot(loc{m: &h.mounts[0], abs: dir})
		res := v.result(walk.truncated)
		if len(res.Matches) != tc.wantRows || res.Truncated != tc.truncated {
			t.Errorf("with %d entries spent: rows = %d truncated = %v, want %d and %v",
				tc.spent, len(res.Matches), res.Truncated, tc.wantRows, tc.truncated)
		}
	}
}

func TestNameSearch_DeadlineStopsAndSaysSo(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"needle.txt": ""})
	saved := searchDeadline
	searchDeadline = 0
	t.Cleanup(func() { searchDeadline = saved })

	rec := searchReq(t, h, map[string]string{"path": prefix, "q": "needle"})
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("status = %d body = %q, want a reply: the search's own deadline is not a client cancel", rec.Code, rec.Body.String())
	}
	if res := decodeSearch(t, rec); !res.Truncated {
		t.Errorf("truncated = false after the deadline stopped the walk: %+v", res)
	}
}

func TestNameSearch_MatchedCountsBeyondTheReply(t *testing.T) {
	h, dir, prefix := testDir(t)
	const files = maxSearchMatches + 50
	for i := range files {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("needle-%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "needle"})
	if len(res.Matches) != maxSearchMatches || res.Matched != files || res.Truncated {
		t.Errorf("rows = %d matched = %d truncated = %v, want %d, %d and false",
			len(res.Matches), res.Matched, res.Truncated, maxSearchMatches, files)
	}
	if res.Scanned != files {
		t.Errorf("scanned = %d, want %d: every entry was visited", res.Scanned, files)
	}
}

func TestNameSearch_RangesAddressTheBasenameInUTF16(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"sub/\U0001F3AF needle Needle.txt": ""})

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "needle"})
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want one row", res.Matches)
	}
	want := []MatchRange{{Start: 3, End: 9}, {Start: 10, End: 16}}
	if got := res.Matches[0].Ranges; !slices.Equal(got, want) {
		t.Errorf("ranges = %v, want %v: offsets into the basename, the astral target counting two units", got, want)
	}
}

func TestNameSearch_FindsABinaryFileByName(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"cover-book.png": "\x89PNG\r\n\x1a\n\x00\x00not text at all",
		"notes.txt":      "prose that says nothing about it\n",
	})

	res := namesSearch(t, h, map[string]string{"path": prefix, "q": "book"})
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly one name hit", res.Matches)
	}
	m := res.Matches[0]
	if m.Path != filepath.Join(dir, "cover-book.png") || m.Kind != MatchKindName || m.Line != 0 || m.Excerpt != "" {
		t.Errorf("hit = %+v, want a name row for cover-book.png with no line and no excerpt", m)
	}
}

func TestNameSearch_SymlinkIsARowAndItsTargetIsNotRead(t *testing.T) {
	h, dir, prefix := testDir(t)
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"target.txt": "the needle is in the target\n"})
	link := filepath.Join(dir, "needle-link.txt")
	if err := os.Symlink(filepath.Join(outside, "target.txt"), link); err != nil {
		t.Fatal(err)
	}

	names := namesSearch(t, h, map[string]string{"path": prefix, "q": "needle"})
	if len(names.Matches) != 1 || names.Matches[0].Path != link || names.Matches[0].Kind != MatchKindName {
		t.Errorf("names matches = %+v, want one name row for the link", names.Matches)
	}
	contents := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))
	if len(contents.Matches) != 0 || contents.Scanned != 0 || contents.Truncated {
		t.Errorf("contents = %+v, want nothing read and nothing missing: a symlink's target is never opened", contents)
	}
}

func TestFilesFilter_ExcludeOutranksIncludeInAnyOrder(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{"a.ts": "", "a.test.ts": "", "b.go": ""})

	for _, files := range []string{"*.ts, !*.test.ts", "!*.test.ts, *.ts"} {
		got := relPaths(namesSearch(t, h, map[string]string{"path": prefix, "files": files}), dir)
		if want := []string{"a.ts"}; !slices.Equal(got, want) {
			t.Errorf("files=%q rows = %v, want %v", files, got, want)
		}
	}
}

func TestFilesFilter_IncludedDirectoryIncludesItsSubtree(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"src/a.go":       "",
		"src/x/b.md":     "",
		"lib/src/c.txt":  "",
		"lib/d.go":       "",
		"source/e.go":    "",
		"srcfile/f.json": "",
	})

	got := relPaths(namesSearch(t, h, map[string]string{"path": prefix, "files": "src"}), dir)
	slices.Sort(got)
	if want := []string{"lib/src/c.txt", "src/a.go", "src/x/b.md"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v: a bare name includes every folder of that name, at any depth", got, want)
	}
}

func TestFilesFilter_PathsAreRelativeToTheSearchedFolder(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"project/src/a.go":      "",
		"project/src/deep/x.go": "",
		"project/lib/src/y.go":  "",
	})
	for _, tc := range []struct {
		at, files string
		want      []string
	}{
		{at: "project/src", files: "deep/*.go", want: []string{"project/src/deep/x.go"}},
		{at: "project", files: "./src/*.go", want: []string{"project/src/a.go"}},
		{at: "project", files: "src/*.go", want: []string{"project/src/a.go", "project/lib/src/y.go"}},
	} {
		res := namesSearch(t, h, map[string]string{"path": filepath.Join(prefix, tc.at), "files": tc.files})
		if got := relPaths(res, dir); !slices.Equal(got, tc.want) {
			t.Errorf("files=%q at %s rows = %v, want %v", tc.files, tc.at, got, tc.want)
		}
	}
}

// Driven at the counter, because the real budget is 100 000 directories.
func TestNameSearch_DirectoryBudgetIsNamesModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dirs       int
		wantWalked bool
		wantRows   int
	}{
		{name: "past_the_contents_budget", dirs: maxSearchDirs, wantWalked: true, wantRows: 1},
		{name: "the_last_directory_of_the_budget", dirs: 99_999, wantWalked: true, wantRows: 1},
		{name: "one_directory_past_the_budget", dirs: 100_000, wantWalked: false, wantRows: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "needle.txt"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}

			v := newNameVisitor(mustQuery(t, "needle"))
			walk := &searchWalk{ctx: t.Context(), v: v, maxDirs: dirBudget(modeNames), ignoreRules: true, dirs: tc.dirs}
			if got := walk.walk(&walkDir{f: f, abs: dir, pruneGit: true}); got != tc.wantWalked {
				t.Errorf("names walk() with dirs=%d = %v, want %v", tc.dirs, got, tc.wantWalked)
			}
			res := v.result(walk.truncated)
			if len(res.Matches) != tc.wantRows || res.Truncated == tc.wantWalked {
				t.Errorf("names walk() with dirs=%d: %d rows, truncated %v; want %d rows, truncated %v", tc.dirs, len(res.Matches), res.Truncated, tc.wantRows, !tc.wantWalked)
			}
		})
	}
}
