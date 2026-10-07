package filebrowse

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// walkIgnored is the walk's verdict for rel: ignored itself, or under a directory that is, since
// the walk never enters an ignored directory.
func walkIgnored(l *ignoreLevel, rel string, isDir bool) bool {
	segs := strings.Split(rel, "/")
	for i := 1; i < len(segs); i++ {
		if l.ignored(strings.Join(segs[:i], "/"), segs[i-1], true) {
			return true
		}
	}
	return l.ignored(rel, segs[len(segs)-1], isDir)
}

// gitIgnored asks real git which of paths a .gitignore holding lines excludes, in a fresh repo
// whose tree has those paths (a trailing "/" makes a directory).
func gitIgnored(t *testing.T, lines string, paths []string) map[string]bool {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		return cmd
	}
	if out, err := git("init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// A directory is asked WITHOUT its trailing slash: git lstats the path to learn its type, while
	// a literal trailing slash would let `dir/**` match the directory's own name.
	var in strings.Builder
	for _, p := range paths {
		in.WriteString(strings.TrimSuffix(p, "/") + "\x00")
	}
	cmd := git("check-ignore", "--no-index", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(in.String())
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("git check-ignore: %v", err)
		}
	}
	got := map[string]bool{}
	for p := range strings.SplitSeq(out.String(), "\x00") {
		if p != "" {
			got[p] = true
		}
	}
	return got
}

func ourIgnored(lines, p string) bool {
	rules := parseIgnoreRules([]byte(lines), "/test/.gitignore")
	l := &ignoreLevel{rules: rules}
	rel := strings.TrimSuffix(p, "/")
	return walkIgnored(l, rel, strings.HasSuffix(p, "/"))
}

func TestGitignore_AgreesWithGitCheckIgnore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; the oracle is real git")
	}
	cases := []struct {
		lines string
		paths []string
	}{
		{"*.log", []string{"a.log", "d/b.log", "a.txt", "a.log.txt"}},
		{"/a.log", []string{"a.log", "d/a.log"}},
		{"build/", []string{"build/", "d/build"}},
		{"build", []string{"build/", "d/build"}},
		{"d/build", []string{"d/build", "x/d/build", "build"}},
		{"d/*.c", []string{"d/a.c", "d/x/a.c", "a.c"}},
		{"**/foo", []string{"foo", "a/foo", "a/b/foo", "afoo"}},
		{"**/foo/bar", []string{"foo/bar", "a/foo/bar", "foo/x/bar"}},
		{"abc/**", []string{"abc/", "abc/x/", "abc/x/y", "x/abc/y"}},
		{"a/**/b", []string{"a/b", "a/x/b", "a/x/y/b", "a/xb"}},
		{"*.log\n!keep.log", []string{"a.log", "keep.log", "d/keep.log"}},
		{"!keep.log\n*.log", []string{"keep.log", "a.log"}},
		{"logs/\n!logs/keep.log", []string{"logs/keep.log", "logs/a.log"}},
		{"logs/**\n!logs/keep.log", []string{"logs/keep.log", "logs/a.log", "logs/"}},
		{"# comment\n*.tmp", []string{"# comment", "a.tmp"}},
		{`\#lit`, []string{"#lit", "lit"}},
		{`\!bang`, []string{"!bang", "bang"}},
		{"trail   ", []string{"trail", "trail   "}},
		{`space\ `, []string{"space ", "space"}},
		{"*.[oa]", []string{"x.o", "x.a", "x.c"}},
		{"*.[!oa]", []string{"x.o", "x.c"}},
		{"?.txt", []string{"a.txt", "ab.txt"}},
		{"a?b", []string{"axb", "a/b"}},
		{"*", []string{"a", "d/b"}},
		{"/*", []string{"a", "d/b"}},
		{"d/", []string{"d/x", "dd/x"}},
		{"Makefile", []string{"Makefile", "makefile"}},
		{"{a,b}", []string{"{a,b}", "a", "b"}},
		{"foo/*", []string{"foo/a/", "foo/a/b", "x/foo/a"}},
		{"/d/", []string{"d/x", "e/d/x"}},
		{"***.log", []string{"a.log", "d/a.log"}},
		{"a/**", []string{"a/b", "a/", "b/a/c"}},
		{"d/build/", []string{"d/build/", "d/build/x"}},
		{"**", []string{"a", "d/e"}},
		{"\n\n*.bak\n\n", []string{"x.bak", "x"}},
		{"*.log\r", []string{"a.log"}},
		{"node_modules/", []string{"web/node_modules/", "web/node_modules/x.js", "node_modules"}},
		{"/root-only\nsub/*.gen", []string{"root-only", "x/root-only", "sub/a.gen", "x/sub/a.gen"}},
		{"*.py[cod]", []string{"a.pyc", "a.pyo", "a.py"}},
		{"doc/**/*.pdf", []string{"doc/a.pdf", "doc/x/y/a.pdf", "a.pdf"}},
		{"[a-c]*", []string{"apple", "dog", "cat"}},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("case_%02d", i), func(t *testing.T) {
			want := gitIgnored(t, c.lines, c.paths)
			for _, p := range c.paths {
				clean := strings.TrimSuffix(p, "/")
				if got := ourIgnored(c.lines, p); got != want[clean] {
					t.Errorf(".gitignore %q: ignored(%q) = %v, git check-ignore says %v", c.lines, p, got, want[clean])
				}
			}
		})
	}
}

func TestGitignore_PropertyAgreesWithGitCheckIgnore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; the oracle is real git")
	}
	part := rapid.SampledFrom([]string{"a", "b", "*", "?", "**", "a*", "*b", "[ab]"})
	rapid.Check(t, func(rt *rapid.T) {
		var lines []string
		for range rapid.IntRange(1, 3).Draw(rt, "rules") {
			segs := rapid.SliceOfN(part, 1, 3).Draw(rt, "segments")
			line := strings.Join(segs, "/")
			if rapid.Bool().Draw(rt, "leadingSlash") {
				line = "/" + line
			}
			if rapid.Bool().Draw(rt, "trailingSlash") {
				line += "/"
			}
			if rapid.Bool().Draw(rt, "negate") {
				line = "!" + line
			}
			lines = append(lines, line)
		}
		files := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{
			"a", "b", "ab", "a/a", "a/b", "b/a", "a/b/a", "b/b/b", "ab/a", "a/ab/b",
		}), 1, 4, func(s string) string { return s }).Draw(rt, "files")
		if conflicting(files) {
			return
		}
		paths := withDirs(files)
		body := strings.Join(lines, "\n")
		want := gitIgnored(t, body, paths)
		for _, p := range paths {
			clean := strings.TrimSuffix(p, "/")
			if got := ourIgnored(body, p); got != want[clean] {
				rt.Fatalf(".gitignore %q: ignored(%q) = %v, git check-ignore says %v", body, p, got, want[clean])
			}
		}
	})
}

// conflicting reports whether one drawn file is another's directory.
func conflicting(files []string) bool {
	for _, a := range files {
		for _, b := range files {
			if strings.HasPrefix(b, a+"/") {
				return true
			}
		}
	}
	return false
}

// withDirs adds every parent directory, spelled with a trailing "/".
func withDirs(files []string) []string {
	out := slices.Clone(files)
	for _, f := range files {
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			if !slices.Contains(out, d+"/") {
				out = append(out, d+"/")
			}
		}
	}
	return out
}

// repoAt makes dir look like a repository root to the walk: a .git directory with info/exclude.
func repoAt(t *testing.T, dir, exclude string) {
	t.Helper()
	writeTree(t, dir, map[string]string{".git/info/exclude": exclude, ".git/HEAD": "ref: refs/heads/main\n"})
}

// listing runs a names search that lists every file the filter admits, and returns their paths
// relative to base.
func listing(t *testing.T, h *Handler, root, base string, extra map[string]string) []string {
	t.Helper()
	params := map[string]string{"path": root, "files": "*"}
	maps.Copy(params, extra)
	res := decodeSearch(t, searchReq(t, h, params))
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, strings.TrimPrefix(m.Path, base+"/"))
	}
	slices.Sort(out)
	return out
}

func TestSearch_RespectsNestedGitignoreAndNegation(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "excluded-by-info.txt\n")
	writeTree(t, dir, map[string]string{
		".gitignore":           "*.log\n!keep.log\nbuild/\n",
		"a.log":                "",
		"keep.log":             "",
		"ok.txt":               "",
		"excluded-by-info.txt": "",
		"build/out.txt":        "",
		"sub/.gitignore":       "secret.txt\n!a.log\n",
		"sub/secret.txt":       "",
		"sub/a.log":            "",
		"sub/fine.txt":         "",
	})

	got := listing(t, h, prefix, dir, nil)
	want := []string{".gitignore", "keep.log", "ok.txt", "sub/.gitignore", "sub/a.log", "sub/fine.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("listing = %v, want %v", got, want)
	}
}

func TestSearch_GitignoreAboveTheSearchRootApplies(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "")
	writeTree(t, dir, map[string]string{
		".gitignore":          "*.log\n/web/gen/\n",
		"web/.gitignore":      "*.tmp\n",
		"web/src/a.log":       "",
		"web/src/b.tmp":       "",
		"web/src/c.txt":       "",
		"web/gen/generated.c": "",
	})

	got := listing(t, h, filepath.Join(prefix, "web"), filepath.Join(dir, "web"), nil)
	if want := []string{".gitignore", "src/c.txt"}; !slices.Equal(got, want) {
		t.Errorf("listing under web = %v, want %v: the repo root's and web's rules both apply", got, want)
	}
}

func TestSearch_NestedRepoStartsAFreshRuleStack(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "")
	repoAt(t, filepath.Join(dir, "inner"), "")
	writeTree(t, dir, map[string]string{
		".gitignore":       "*.txt\n",
		"outer.txt":        "",
		"inner/inner.txt":  "",
		"inner/.gitignore": "*.md\n",
		"inner/x.md":       "",
		"other.md":         "",
	})

	got := listing(t, h, prefix, dir, nil)
	if want := []string{".gitignore", "inner/.gitignore", "inner/inner.txt", "other.md"}; !slices.Equal(got, want) {
		t.Errorf("listing = %v, want %v", got, want)
	}
}

func TestSearch_OutsideARepoGitignoreIsNotRead(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{".gitignore": "*.log\n", "a.log": ""})

	if got, want := listing(t, h, prefix, dir, nil), []string{".gitignore", "a.log"}; !slices.Equal(got, want) {
		t.Errorf("listing = %v, want %v: git applies .gitignore only inside a repository", got, want)
	}
}

func TestSearch_DotGitIsNeverWalked(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "")
	writeTree(t, dir, map[string]string{".git/config": "needle\n", "needle.txt": "needle\n"})

	for _, params := range []map[string]string{
		{"q": "needle"},
		{"q": "needle", "ignored": "1"},
		{"q": "needle", "mode": "contents"},
		{"q": "needle", "mode": "contents", "ignored": "1"},
		{"q": "config"},
	} {
		params["path"] = prefix
		for _, m := range decodeSearch(t, searchReq(t, h, params)).Matches {
			if strings.Contains(m.Path, "/.git/") || strings.HasSuffix(m.Path, "/.git") {
				t.Errorf("params %v returned %s from inside .git", params, m.Path)
			}
		}
	}
}

// Inside .git git applies no ignore rules, so the repository's own .gitignore must not hide the
// ref the reader went looking for.
func TestSearch_DotGitIsWalkedWhenTheRootIsInsideIt(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "")
	writeTree(t, dir, map[string]string{".gitignore": "main\n", ".git/refs/heads/main": "abc\n"})

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": filepath.Join(prefix, ".git"), "q": "main"}))
	if got := matchPaths(res); len(got) != 1 || got[0] != filepath.Join(dir, ".git/refs/heads/main") {
		t.Errorf("matches = %v, want the ref under the .git root the reader opened", got)
	}
}

func TestSearch_NodeModulesPrunedByDefault(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"node_modules/dep/index.js":     "",
		"web/node_modules/dep/index.js": "",
		"web/app.js":                    "",
	})

	if got, want := listing(t, h, prefix, dir, nil), []string{"web/app.js"}; !slices.Equal(got, want) {
		t.Errorf("listing = %v, want %v", got, want)
	}
}

func TestSearch_IncludeIgnoredLiftsEveryIgnoreRule(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "by-info.txt\n")
	writeTree(t, dir, map[string]string{
		".gitignore":          "*.log\n",
		"a.log":               "",
		"by-info.txt":         "",
		"node_modules/dep.js": "",
	})

	got := listing(t, h, prefix, dir, map[string]string{"ignored": "1"})
	if want := []string{".gitignore", "a.log", "by-info.txt", "node_modules/dep.js"}; !slices.Equal(got, want) {
		t.Errorf("listing with ignored=1 = %v, want %v", got, want)
	}
}

// The # mention menu's query: ignored files stay reachable while node_modules is pruned unread.
func TestSearch_MentionRequestReachesIgnoredFilesButNotNodeModules(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "")
	writeTree(t, dir, map[string]string{
		".gitignore":            "**/.agents/\n",
		".agents/tasks/plan.md": "",
		"node_modules/plan.md":  "",
		"src/plan.md":           "",
	})

	res := decodeSearch(t, searchReq(t, h, map[string]string{
		"path": prefix, "q": `"plan"`, "ignored": "1", "files": "!node_modules",
	}))
	got := relPaths(res, dir)
	slices.Sort(got)
	if want := []string{".agents/tasks/plan.md", "src/plan.md"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

func TestSearch_IgnoredFileRootIsHiddenUnlessAsked(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "by-info.txt\n")
	writeTree(t, dir, map[string]string{
		".gitignore":         "*.log\n/sub/d.txt\nlogs/\n",
		"sub/.gitignore":     "*.tmp\n",
		"a.log":              "needle\n",
		"by-info.txt":        "needle\n",
		"sub/b.tmp":          "needle\n",
		"sub/c.txt":          "needle\n",
		"sub/d.txt":          "needle\n",
		"logs/deep/e.txt":    "needle\n",
		"node_modules/f.txt": "needle\n",
		"src/node_modules":   "needle\n",
	})

	for _, tc := range []struct {
		file string
		want int
	}{
		{file: "a.log", want: 0},
		{file: "by-info.txt", want: 0},
		{file: "sub/b.tmp", want: 0},
		{file: "sub/d.txt", want: 0},
		{file: "logs/deep/e.txt", want: 0},
		{file: "node_modules/f.txt", want: 0},
		{file: "sub/c.txt", want: 1},
		{file: "src/node_modules", want: 1},
	} {
		root := filepath.Join(prefix, tc.file)
		hidden := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": root, "q": "needle"}))
		if len(hidden.Matches) != tc.want {
			t.Errorf("%s by default: matches = %d, want %d", tc.file, len(hidden.Matches), tc.want)
		}
		shown := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": root, "q": "needle", "ignored": "1"}))
		if len(shown.Matches) != 1 {
			t.Errorf("%s with ignored=1: matches = %d, want 1", tc.file, len(shown.Matches))
		}
	}
}

// A walk from the mount would never reach an ignored directory, so naming one as the root must not
// either, in either mode.
func TestSearch_IgnoredDirectoryRootIsHiddenUnlessAsked(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "by-info/\n")
	writeTree(t, dir, map[string]string{
		".gitignore":                      "build/\n/gen/\n",
		"by-info/needle.txt":              "needle\n",
		"gen/inner/needle.txt":            "needle\n",
		"node_modules/dep/needle.txt":     "needle\n",
		"web/node_modules/dep/needle.txt": "needle\n",
		"web/src/needle.txt":              "needle\n",
		"build/lib/.git/HEAD":             "ref: refs/heads/main\n",
		"build/lib/src/needle.txt":        "needle\n",
	})

	for _, tc := range []struct {
		root string
		want int
	}{
		{root: "build", want: 0},
		{root: "build/lib", want: 0},
		{root: "build/lib/src", want: 1},
		{root: "by-info", want: 0},
		{root: "gen/inner", want: 0},
		{root: "node_modules", want: 0},
		{root: "node_modules/dep", want: 0},
		{root: "web/node_modules/dep", want: 0},
		{root: "web/src", want: 1},
	} {
		for _, mode := range []string{"names", "contents"} {
			params := map[string]string{"mode": mode, "path": filepath.Join(prefix, tc.root), "q": "needle"}
			if got := len(decodeSearch(t, searchReq(t, h, params)).Matches); got != tc.want {
				t.Errorf("%s root %s by default: matches = %d, want %d", mode, tc.root, got, tc.want)
			}
			params["ignored"] = "1"
			if got := len(decodeSearch(t, searchReq(t, h, params)).Matches); got != 1 {
				t.Errorf("%s root %s with ignored=1: matches = %d, want 1", mode, tc.root, got)
			}
		}
	}
}

func TestGitignore_ReadIsBounded(t *testing.T) {
	t.Run("past_the_byte_cap", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		repoAt(t, dir, "")
		padding := strings.Repeat("# padding\n", maxGitignoreBytes/10+1)
		writeTree(t, dir, map[string]string{".gitignore": "*.tmp\n" + padding + "*.log\n", "a.log": "", "b.tmp": ""})

		if got, want := listing(t, h, prefix, dir, nil), []string{".gitignore", "a.log"}; !slices.Equal(got, want) {
			t.Errorf("listing = %v, want %v: the rule past the read cap is dropped, the one before it kept", got, want)
		}
	})
	t.Run("past_the_rule_cap", func(t *testing.T) {
		h, dir, prefix := testDir(t)
		repoAt(t, dir, "")
		var b strings.Builder
		b.WriteString("*.tmp\n")
		for i := range maxGitignoreRules - 1 {
			fmt.Fprintf(&b, "never-%d\n", i)
		}
		b.WriteString("*.log\n")
		writeTree(t, dir, map[string]string{".gitignore": b.String(), "a.log": "", "b.tmp": ""})

		if got, want := listing(t, h, prefix, dir, nil), []string{".gitignore", "a.log"}; !slices.Equal(got, want) {
			t.Errorf("listing = %v, want %v: the rule past the rule cap is dropped", got, want)
		}
	})
}

func TestGitignore_SymlinkedGitignoreIsNotFollowed(t *testing.T) {
	h, dir, prefix := testDir(t)
	repoAt(t, dir, "")
	writeTree(t, dir, map[string]string{"rules.txt": "*.log\n", "a.log": ""})
	if err := os.Symlink("rules.txt", filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatal(err)
	}

	if got, want := listing(t, h, prefix, dir, nil), []string{".gitignore", "a.log", "rules.txt"}; !slices.Equal(got, want) {
		t.Errorf("listing = %v, want %v: a symlinked .gitignore is not read", got, want)
	}
}
