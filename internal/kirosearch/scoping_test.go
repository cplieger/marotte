package kirosearch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// scopeCase is a pattern as a model writes it and glob, the form rg run in the root is given: the
// pattern itself, or for a pattern spelled from the root (absolute, "./"-led, "." or empty
// segments) the same path written relative to the root. start is the folder the expected rg run
// starts from: "" for the whole root, or a literal folder an ignore rule names, which a search
// naming it explicitly enters as `rg ... <folder>` does.
type scopeCase struct {
	pattern string
	glob    string
	start   string
}

func same(p string) scopeCase         { return scopeCase{pattern: p, glob: p} }
func form(p, glob string) scopeCase   { return scopeCase{pattern: p, glob: glob} }
func named(p, start string) scopeCase { return scopeCase{pattern: p, glob: p, start: start} }

// scopeCases holds every pattern shape the scoping must not change, keyed by name; "<root>" stands
// for the searched root's path as given.
func scopeCases() map[string]scopeCase {
	long := strings.Repeat("a", 300)
	return map[string]scopeCase{
		"doublestar name":               same("**/x.go"),
		"folder globstar":               same("a/**"),
		"root-anchored globstar":        same("/**"),
		"dot-led globstar":              form("./**", "/**"),
		"absolute globstar":             form("<root>/**", "/**"),
		"folder name glob":              same("a/*.go"),
		"folder dot segment glob":       form("a/./*.go", "a/*.go"),
		"nested globstar":               same("a/b/**/*.ts"),
		"start-anchored name":           same("name*"),
		"start-anchored folder":         same("repo/internal*"),
		"start-anchored folder slash":   same("repo/internal/*"),
		"start-anchored readme":         same("READ*"),
		"start-anchored missing":        same("absent*"),
		"dot":                           form(".", ""),
		"dot slash":                     form("./", ""),
		"slash":                         form("/", ""),
		"doubled slash only":            form("//", ""),
		"absolute root":                 form("<root>", ""),
		"absolute root slash":           form("<root>/", ""),
		"doubled slash":                 form("repo//internal/**", "repo/internal/**"),
		"dot segment":                   form("repo/./internal/**", "repo/internal/**"),
		"doubled leading slash":         form("//repo/internal/**/*.go", "/repo/internal/**/*.go"),
		"led by slash-dot":              form("/./repo/internal/**/*.go", "/repo/internal/**/*.go"),
		"doubled trailing slash":        form("repo/internal//", "repo/internal/"),
		"trailing slash":                same("repo/internal/"),
		"bare folder":                   same("repo/internal"),
		"absolute bare folder":          form("<root>/repo/internal", "/repo/internal"),
		"root-anchored bare folder":     same("/repo/internal"),
		"top folder":                    same("repo"),
		"root-anchored top folder":      same("/repo"),
		"relative":                      same("repo/internal/**/*.go"),
		"absolute":                      form("<root>/repo/internal/**/*.go", "/repo/internal/**/*.go"),
		"dot-led":                       form("./repo/internal/**/*.go", "/repo/internal/**/*.go"),
		"root-anchored":                 same("/repo/internal/**/*.go"),
		"alternation below a folder":    same("repo/{internal,other}/**"),
		"name query":                    same("**/*kiro*"),
		"extension":                     same("*.go"),
		"doublestar extension":          same("**/*.go"),
		"doublestar test name":          same("**/*test*"),
		"folder test name":              same("pkg/*test*"),
		"root-anchored md":              same("/*.md"),
		"dot-led md":                    form("./*.md", "/*.md"),
		"absolute md":                   form("<root>/*.md", "/*.md"),
		"root file":                     same("/top.md"),
		"root-anchored x.go":            same("/x.go"),
		"dot-led x.go":                  form("./x.go", "/x.go"),
		"absolute x.go":                 form("<root>/x.go", "/x.go"),
		"literal name":                  same("go.mod"),
		"root-anchored file":            same("/go.mod"),
		"root-anchored name glob":       same("/re*"),
		"folder file glob":              same("repo/internal/*.go"),
		"folder literal file":           same("repo/internal/a.go"),
		"build name below a folder":     same("repo/internal/**/*build.go*"),
		"flag-shaped folder":            same("-h/**/*.go"),
		"hidden folder":                 same(".hidden/**"),
		"folder named like a glob":      same("dir.go/**"),
		"root-anchored NUL":             same("/a\x00b"),
		"NUL in the prefix":             same("a\x00b/x"),
		"root-anchored symlink loop":    same("/loop"),
		"prefix through a loop":         same("loop/x/*.go"),
		"root-anchored symlink":         same("/link"),
		"prefix through a symlink":      same("link/**/*.go"),
		"symlink out of the root":       same("out/**"),
		"root-anchored missing":         same("/missing"),
		"missing folder":                same("absent/**/*.go"),
		"name over 255 bytes":           same(long),
		"segment over 255 bytes":        same(long + "/x.go"),
		"prefix over 4096 bytes":        same("deep/" + strings.Repeat("a/", 2200) + "x.go"),
		"prefix through a file":         same("go.mod/x/*.go"),
		"file as the folder":            same("go.mod/*.go"),
		"led by ..":                     same("../x/*.go"),
		".. after a folder":             same("repo/../x/*.go"),
		".. after a glob":               same("**/../x/*.go"),
		"unclosed alternate":            same("{a"),
		"gitignored folder named":       named("ignored/**", "ignored"),
		"below a gitignored folder":     named("ignored/sub/**", filepath.Join("ignored", "sub")),
		"folder a parent ignores":       named("parentignored/**", "parentignored"),
		"git folder":                    same(".git/**"),
		"git segment below a folder":    same("repo/.git/**"),
		"start below the depth cap":     same("deep/a/**"),
		"below an anchored ignore rule": same("repo/**/*.go"),
		"gitignored folder with a !":    named("a!b/**", "a!b"),
		"gitignored folder with a ]":    named("foo]/**", "foo]"),
		"gitignored !-folder below one": named("src/!foo/**", filepath.Join("src", "!foo")),
		"negated folder":                same("!keep/**"),
		"comment-shaped folder":         same("#dir/**"),
		"negated .. pattern":            same("!../x/*.go"),
	}
}

// excludeCases are the exclude spellings, each searched with no include.
func excludeCases() map[string]scopeCase {
	return map[string]scopeCase{
		"relative":               same("repo/internal/**"),
		"absolute":               form("<root>/repo/internal/**", "/repo/internal/**"),
		"dot-led":                form("./repo/internal/**", "/repo/internal/**"),
		"trailing slash":         same("repo/internal/"),
		"bare folder":            same("repo/internal"),
		"dot segment":            form("repo/./internal/**", "repo/internal/**"),
		"doubled slash":          form("repo//internal/**", "repo/internal/**"),
		"doubled leading slash":  form("//repo/internal/**", "/repo/internal/**"),
		"led by slash-dot":       form("/./repo/internal/**", "/repo/internal/**"),
		"doubled trailing slash": form("repo/internal//", "repo/internal/"),
		"extension":              same("*.go"),
	}
}

// includeExcludeCases pair an include with an exclude naming its start folder, an ancestor or a
// folder below it; any exclude keeps the whole-root walk, an ignored folder's included.
func includeExcludeCases() map[string][2]scopeCase {
	return map[string][2]scopeCase{
		"exclude the start folder":            {same("repo/internal/**"), same("repo/internal")},
		"exclude an ancestor":                 {same("repo/internal/**"), same("repo")},
		"exclude a folder below":              {same("repo/**"), same("repo/internal")},
		"exclude the start, slashed":          {same("repo/internal/**"), same("repo/internal/")},
		"exclude with a named ignored folder": {same("ignored/**"), same("*.md")},
	}
}

// scopeTree writes every file under root that a scopeCases shape can name, each holding "hit". Its
// .gitignore holds an anchored rule two levels below repo, which a search started at repo must
// still apply.
func scopeTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{".gitignore": "ignored/\n/repo/internal/vendor/\na!b/\nfoo]/\nsrc/!foo/\n"}
	for _, name := range []string{
		"top.go", "top.md", "go.mod", "README.md", "x.go", "names.txt", "re.txt",
		"repo/internal/a.go", "repo/internal/b.ts", "repo/internal/build.go", "repo/internal/deeper/c.go",
		"repo/internal/deeper/x/y.ts", "repo/internal/deeper/build_test.go", "repo/other/d.go",
		"other/internal/e.go", "a/x.go", "a/b/f.ts", "a/b/c/g.ts", "a/b/c/d/h.ts", "a/b/kiro.go",
		"kirosearch/kirosearch.go", "src/kiro-docs/one.md", "dir.go/nested/notgo.txt", "pkg/a_test.go",
		"READMEdir/x.go", "docs/README.md", "name/n.go", "sub/name/m.go", "sub/namefile.go", "sub/x.go",
		"sub/top.md", "-h/a.go", ".hidden/h.go", "x/k.go", "deep/a/a/x.go", "ignored/z.go", "ignored/sub/w.go",
		"parentignored/p.go", ".git/config", "repo/.git/HEAD", "deep/" + strings.Repeat("a/", 48) + "at50.go",
		"deep/" + strings.Repeat("a/", 49) + "at51.go", "repo/internal/vendor/v.go", "repo/internal/sub/s.go",
		"a!b/x.go", "foo]/x.go", "src/!foo/x.go", "keep/k.go", "#dir/h.go",
	} {
		files[name] = "hit\n"
	}
	writeTree(t, root, files)
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"o.go": "hit\n"})
	for link, target := range map[string]string{"link": "repo", "loop": "loop", "out": outside} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
}

// rgList runs rg in root with args and returns its output lines sorted. A NUL in an argument,
// which exec refuses, is a search that finds nothing.
func rgList(t *testing.T, bin, root string, args ...string) []string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	cmd.Env = []string{}
	out, err := cmd.Output()
	if _, exited := errors.AsType[*exec.ExitError](err); err != nil && !exited {
		if errors.Is(err, syscall.EINVAL) {
			return nil
		}
		t.Fatalf("rg %q: %v", args, err)
	}
	lines := slices.DeleteFunc(strings.Split(string(out), "\n"), func(s string) bool { return s == "" })
	slices.Sort(lines)
	return lines
}

func globArgs(include, exclude string) []string {
	var args []string
	if include != "" {
		args = append(args, "--glob", include)
	}
	if exclude != "" {
		args = append(args, "--glob", "!"+exclude)
	}
	return args
}

// wholeRootText is the files rg run in root, from start ("" for the whole root), lists for Text's
// search of "hit".
func wholeRootText(t *testing.T, bin, root, include, exclude, start string) []string {
	t.Helper()
	args := []string{"-l", "--max-filesize", "1M", "--hidden", "--no-require-git", "--glob", "!.git", "--ignore-case"}
	args = append(append(args, globArgs(include, exclude)...), "--", "hit")
	if start != "" {
		args = append(args, start)
	}
	return rgList(t, bin, root, args...)
}

// wholeRootFiles is the files rg run in root, from start ("" for the whole root), lists for a
// find_files glob, with KAS 3.0.0's own arguments (testdata kas-probe search-files.json
// kasOwnRgArgs): 50 levels below the root, and no --glob for "**/*".
func wholeRootFiles(t *testing.T, bin, root, glob, exclude, start string) []string {
	t.Helper()
	depth := 50
	if start != "" {
		depth -= len(strings.Split(start, string(filepath.Separator)))
	}
	if glob == "**/*" {
		glob = ""
	}
	args := []string{"--files", "--max-depth", strconv.Itoa(depth), "--hidden", "--no-require-git", "--glob", "!.git"}
	args = append(args, globArgs(glob, exclude)...)
	if start != "" {
		args = append(args, "--", start)
	}
	return rgList(t, bin, root, args...)
}

func textFiles(got []Match) []string {
	var files []string
	for _, m := range got {
		if !m.IsContext && !slices.Contains(files, m.FilePath) {
			files = append(files, m.FilePath)
		}
	}
	slices.Sort(files)
	return files
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// scopeRoots are the trees every case is searched in: a real folder, and a symlink naming one,
// each below a folder whose .gitignore names one of the tree's folders, and another anchored below
// repo/internal, which a search started there must still apply.
func scopeRoots(t *testing.T) map[string]string {
	t.Helper()
	tree := func() string {
		parent := t.TempDir()
		writeTree(t, parent, map[string]string{".gitignore": "parentignored/\n/root/repo/internal/sub/\n"})
		root := filepath.Join(parent, "root")
		scopeTree(t, root)
		return root
	}
	plain := tree()
	linked := tree()
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(linked, link); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"real root": plain, "symlinked root": link}
}

// The scoping chooses only where rg starts: every pattern finds exactly what an unscoped rg run in
// the whole root finds with the same glob, except that a literal folder an ignore rule names is
// entered when the search names it and sets no exclude.
func TestSearch_RealRGScopingMatchesAWholeRootSearch(t *testing.T) {
	bin := realRG(t)
	s := Searcher{Bin: bin}
	for rootName, root := range scopeRoots(t) {
		at := func(p string) string { return strings.ReplaceAll(p, "<root>", root) }
		for name, tc := range scopeCases() {
			t.Run(rootName+", "+name+", text", func(t *testing.T) {
				t.Parallel()
				got := textFiles(s.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", IncludePattern: at(tc.pattern)}))
				if want := wholeRootText(t, bin, root, tc.glob, "", tc.start); !slices.Equal(got, want) {
					t.Errorf("Text(include %.80q) found %q, want %q (rg --glob %.80q)", tc.pattern, got, want, tc.glob)
				}
			})
			t.Run(rootName+", "+name+", files", func(t *testing.T) {
				t.Parallel()
				got := sorted(s.Files(t.Context(), []string{root}, FilesQuery{Pattern: at(tc.pattern), MaxResults: maxFileResults}))
				if want := wholeRootFiles(t, bin, root, tc.glob, "", tc.start); !slices.Equal(got, want) {
					t.Errorf("Files(%.80q) = %q, want %q (rg --glob %.80q)", tc.pattern, got, want, tc.glob)
				}
			})
		}
		for name, tc := range includeExcludeCases() {
			include, exclude := tc[0], tc[1]
			t.Run(rootName+", "+name+", text", func(t *testing.T) {
				t.Parallel()
				got := textFiles(s.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", IncludePattern: include.pattern, ExcludePattern: exclude.pattern}))
				if want := wholeRootText(t, bin, root, include.glob, exclude.glob, ""); !slices.Equal(got, want) {
					t.Errorf("Text(include %q, exclude %q) found %q, want %q", include.pattern, exclude.pattern, got, want)
				}
			})
			t.Run(rootName+", "+name+", files", func(t *testing.T) {
				t.Parallel()
				got := sorted(s.Files(t.Context(), []string{root}, FilesQuery{Pattern: include.pattern, Exclude: exclude.pattern, MaxResults: maxFileResults}))
				if want := wholeRootFiles(t, bin, root, include.glob, exclude.glob, ""); !slices.Equal(got, want) {
					t.Errorf("Files(%q, exclude %q) = %q, want %q", include.pattern, exclude.pattern, got, want)
				}
			})
		}
		for name, tc := range excludeCases() {
			t.Run(rootName+", exclude "+name+", text", func(t *testing.T) {
				t.Parallel()
				got := textFiles(s.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", ExcludePattern: at(tc.pattern)}))
				if want := wholeRootText(t, bin, root, "", tc.glob, ""); !slices.Equal(got, want) {
					t.Errorf("Text(exclude %q) found %q, want %q", tc.pattern, got, want)
				}
			})
			t.Run(rootName+", exclude "+name+", files", func(t *testing.T) {
				t.Parallel()
				got := sorted(s.Files(t.Context(), []string{root}, FilesQuery{Pattern: "**/*", Exclude: at(tc.pattern), MaxResults: maxFileResults}))
				if want := wholeRootFiles(t, bin, root, "**/*", tc.glob, ""); !slices.Equal(got, want) {
					t.Errorf("Files(exclude %q) = %q, want %q", tc.pattern, got, want)
				}
			})
		}
	}
}

// An absolute pattern names paths under its own root only; every other root is searched with the
// pattern as written, which names nothing there.
func TestSearch_RealRGAbsolutePatternMatchesEachRootsWholeSearch(t *testing.T) {
	bin := realRG(t)
	first, second := t.TempDir(), t.TempDir()
	scopeTree(t, first)
	writeTree(t, second, map[string]string{"repo/internal/z.go": "hit\n", "top.go": "hit\n"})
	roots := []string{first, second}
	s := Searcher{Bin: bin}
	for name, tc := range map[string]scopeCase{
		"folder under the first root": form(first+"/repo/internal/**/*.go", "/repo/internal/**/*.go"),
		"all of the second root":      form(second+"/**", "/**"),
		"first root's own entries":    form(first+"/*.md", "/*.md"),
	} {
		t.Run(name, func(t *testing.T) {
			var want []string
			for _, root := range roots {
				glob := tc.pattern
				if strings.HasPrefix(tc.pattern, root+"/") {
					glob = tc.glob
				}
				for _, rel := range wholeRootFiles(t, bin, root, glob, "", "") {
					want = append(want, displayPath(filepath.Join(root, rel), roots))
				}
			}
			slices.Sort(want)
			if got := sorted(s.Files(t.Context(), roots, FilesQuery{Pattern: tc.pattern, MaxResults: maxFileResults})); !slices.Equal(got, want) {
				t.Errorf("Files(%q) = %q, want %q", tc.pattern, got, want)
			}
		})
	}
}

// rgCall is the start path and --max-depth ("" for none) of one rg command line a fake rg recorded,
// or ran false when the search ran no rg.
type rgCall struct {
	start, maxDepth string
	ran             bool
}

func recordedCall(t *testing.T, argsFile string) rgCall {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		return rgCall{}
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	call := rgCall{start: args[len(args)-1], ran: true}
	if i := slices.Index(args, "--max-depth"); i >= 0 && i+1 < len(args) {
		call.maxDepth = args[i+1]
	}
	return call
}

// rg starts at the pattern's literal leading folders, below which every match lies, and passes no
// --max-depth read from the pattern; find_files keeps KAS's 50 levels counted from the root.
func TestSearch_StartsAtTheLiteralLeadingFolder(t *testing.T) {
	root := t.TempDir()
	scopeTree(t, root)
	long := strings.Repeat("a", 300)
	capped := "deep/" + strings.Repeat("a/", 49) + "**"
	repoInternal := filepath.Join("repo", "internal")
	run := func(start, depth string) rgCall { return rgCall{start: start, maxDepth: depth, ran: true} }
	tests := map[string]struct {
		pattern, exclude string
		text, files      rgCall
	}{
		"folder globstar":           {pattern: "repo/internal/**/*.go", text: run(repoInternal, ""), files: run(repoInternal, "48")},
		"root-anchored folder":      {pattern: "/repo/internal/*.go", text: run(repoInternal, ""), files: run(repoInternal, "48")},
		"absolute folder":           {pattern: root + "/repo/internal/**", text: run(repoInternal, ""), files: run(repoInternal, "48")},
		"dot segment":               {pattern: "repo/./internal/**", text: run(repoInternal, ""), files: run(repoInternal, "48")},
		"bare folder":               {pattern: "repo/internal", text: run("repo", ""), files: run("repo", "49")},
		"trailing slash":            {pattern: "repo/internal/", text: run("repo", ""), files: run("repo", "49")},
		"folder up to a glob":       {pattern: "repo/inter*/a.go", text: run("repo", ""), files: run("repo", "49")},
		"glob-led":                  {pattern: "**/internal/*.go", text: run(".", ""), files: run(".", "50")},
		"one segment":               {pattern: "*.go", text: run(".", ""), files: run(".", "50")},
		"one folder segment":        {pattern: "repo/", text: run(".", ""), files: run(".", "50")},
		"root-anchored one segment": {pattern: "/*.md", text: run(".", ""), files: run(".", "50")},
		"root-anchored globstar":    {pattern: "/**", text: run(".", ""), files: run(".", "50")},
		"name over 255 bytes":       {pattern: long, text: run(".", ""), files: run(".", "50")},
		"any exclude":               {pattern: "repo/internal/**", exclude: "*.md", text: run(".", ""), files: run(".", "50")},
		"named ignored folder":      {pattern: "ignored/**", text: run("ignored", ""), files: run("ignored", "49")},
		"start at the depth cap":    {pattern: capped, text: run(filepath.Dir(capped), ""), files: rgCall{}},
		"git segment":               {pattern: "repo/.git/**"},
		"git segment, any exclude":  {pattern: "repo/.git/**", exclude: "*.md"},
		"through a symlink":         {pattern: "link/**"},
		"symlink, any exclude":      {pattern: "link/**", exclude: "*.md"},
		"folder with a !":           {pattern: "a!b/**", text: run("a!b", ""), files: run("a!b", "49")},
		"folder with a ]":           {pattern: "foo]/*.go", text: run("foo]", ""), files: run("foo]", "49")},
		"negated pattern":           {pattern: "!keep/**", text: run(".", ""), files: run(".", "50")},
		"comment-shaped pattern":    {pattern: "#dir/**", text: run(".", ""), files: run(".", "50")},
		"root-anchored # folder":    {pattern: "/#dir/**", text: run("#dir", ""), files: run("#dir", "49")},
		"symlink out of the root":   {pattern: "out/**"},
		"NUL in one segment":        {pattern: "/a\x00b"},
		".. segment":                {pattern: "repo/../x/*.go"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			textArgs := filepath.Join(t.TempDir(), "args")
			Searcher{Bin: fakeRG(t, `printf '%s\n' "$@" > `+textArgs)}.Text(t.Context(), []string{root},
				TextQuery{Pattern: "hit", IncludePattern: tc.pattern, ExcludePattern: tc.exclude})
			if got := recordedCall(t, textArgs); got != tc.text {
				t.Errorf("Text(include %.60q) ran rg %+v, want %+v", tc.pattern, got, tc.text)
			}
			filesArgs := filepath.Join(t.TempDir(), "args")
			Searcher{Bin: fakeRG(t, `printf '%s\n' "$@" > `+filesArgs)}.Files(t.Context(), []string{root},
				FilesQuery{Pattern: tc.pattern, Exclude: tc.exclude, MaxResults: 11})
			if got := recordedCall(t, filesArgs); got != tc.files {
				t.Errorf("Files(%.60q) ran rg %+v, want %+v", tc.pattern, got, tc.files)
			}
		})
	}
}

// withoutDACOverride runs f on an OS thread without CAP_DAC_OVERRIDE and CAP_DAC_READ_SEARCH, so a
// mode-0 folder refuses root as it refuses any other user. Capabilities are per thread.
func withoutDACOverride(t *testing.T, f func()) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		// Never unlocked: the goroutine's exit then ends the thread with its reduced capabilities.
		runtime.LockOSThread()
		hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		var data [2]unix.CapUserData
		if err := unix.Capget(&hdr, &data[0]); err != nil {
			done <- err
			return
		}
		data[0].Effective &^= 1<<unix.CAP_DAC_OVERRIDE | 1<<unix.CAP_DAC_READ_SEARCH
		if err := unix.Capset(&hdr, &data[0]); err != nil {
			done <- err
			return
		}
		f()
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// skipCase is a literal leading folder whose stat fails with errno under the root setup prepares.
type skipCase struct {
	setup  func(t *testing.T, root string)
	folder string
	errno  syscall.Errno
	// peer is whether another root can hold the folder, and so must still be searched.
	peer, noDAC bool
}

// A literal leading folder that stat or EvalSymlinks refuses skips the root it is under, never
// widening to that root's whole walk, while every other root is still searched; an exclude, which
// starts rg at the root, skips it too.
func TestSearch_UnresolvableLeadingFolderSkipsOnlyItsRoot(t *testing.T) {
	long := strings.Repeat("a", 300)
	tests := map[string]skipCase{
		"missing (ENOENT)": {folder: "absent/x", errno: syscall.ENOENT, peer: true},
		"through a file (ENOTDIR)": {folder: "go.mod/x", errno: syscall.ENOTDIR, peer: true, setup: func(t *testing.T, root string) {
			writeTree(t, root, map[string]string{"go.mod": ""})
		}},
		"segment over 255 bytes (ENAMETOOLONG)": {folder: long + "/x", errno: syscall.ENAMETOOLONG},
		"prefix over 4096 bytes (ENAMETOOLONG)": {folder: "deep/" + strings.Repeat("a/", 2200) + "x", errno: syscall.ENAMETOOLONG},
		"NUL (EINVAL)":                          {folder: "a\x00b/x", errno: syscall.EINVAL},
		"through a loop (ELOOP)": {folder: "loop/x", errno: syscall.ELOOP, peer: true, setup: func(t *testing.T, root string) {
			if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
				t.Fatal(err)
			}
		}},
		"unsearchable (EACCES)": {folder: "locked/x", errno: syscall.EACCES, peer: true, noDAC: true, setup: func(t *testing.T, root string) {
			writeTree(t, root, map[string]string{"locked/x/a.go": "hit\n"})
			locked := filepath.Join(root, "locked")
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		}},
	}
	for name, tc := range tests {
		for variant, exclude := range map[string]string{"no exclude": "", "any exclude": "*.md"} {
			t.Run(name+", "+variant, func(t *testing.T) { checkSkipsOnlyItsRoot(t, tc, exclude) })
		}
	}
}

func checkSkipsOnlyItsRoot(t *testing.T, tc skipCase, exclude string) {
	t.Helper()
	bad, good := t.TempDir(), t.TempDir()
	if tc.setup != nil {
		tc.setup(t, bad)
	}
	roots, want := []string{bad}, []string(nil)
	if tc.peer {
		writeTree(t, good, map[string]string{tc.folder + "/a.go": "hit\n"})
		realGood, err := filepath.EvalSymlinks(good)
		if err != nil {
			t.Fatal(err)
		}
		roots, want = []string{bad, good}, []string{realGood}
	}
	logs := t.TempDir()
	textRG := fakeRG(t, `/bin/pwd -P >> '`+filepath.Join(logs, "text")+`'`)
	filesRG := fakeRG(t, `/bin/pwd -P >> '`+filepath.Join(logs, "files")+`'`)
	var statErr error
	search := func() {
		_, statErr = os.Stat(filepath.Join(bad, tc.folder))
		pattern := tc.folder + "/*.go"
		Searcher{Bin: textRG}.Text(t.Context(), roots, TextQuery{Pattern: "hit", IncludePattern: pattern, ExcludePattern: exclude})
		Searcher{Bin: filesRG}.Files(t.Context(), roots, FilesQuery{Pattern: pattern, Exclude: exclude, MaxResults: 11})
	}
	if tc.noDAC {
		withoutDACOverride(t, search)
	} else {
		search()
	}
	if !errors.Is(statErr, tc.errno) {
		t.Fatalf("the fixture's stat error is %v, want %v", statErr, tc.errno)
	}
	ranIn := func(file string) []string {
		raw, _ := os.ReadFile(filepath.Join(logs, file))
		return slices.DeleteFunc(strings.Split(string(raw), "\n"), func(s string) bool { return s == "" })
	}
	if got := ranIn("text"); !slices.Equal(got, want) {
		t.Errorf("Text ran rg in %q, want %q", got, want)
	}
	if got := ranIn("files"); !slices.Equal(got, want) {
		t.Errorf("Files ran rg in %q, want %q", got, want)
	}
}
