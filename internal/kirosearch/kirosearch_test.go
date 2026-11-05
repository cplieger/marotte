package kirosearch

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
)

// fakeRG writes an executable sh script standing in for rg. The script runs with an empty
// environment, so it names every binary by absolute path.
func fakeRG(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rg")
	syscall.ForkLock.RLock()
	err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func realRG(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("rg")
	switch {
	case err == nil:
		return p
	case os.Getenv("KIROSEARCH_REQUIRE_RG") != "":
		t.Fatal("rg is not installed, and KIROSEARCH_REQUIRE_RG makes these tests mandatory")
	default:
		t.Skip("rg is not installed")
	}
	return ""
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// matchLine is one `rg --json` match event for path (relative to rg's cwd, as rg prints it).
func matchLine(path, text string, line int) string {
	return `{"type":"match","data":{"path":{"text":"` + path + `"},"lines":{"text":"` + text +
		`\n"},"line_number":` + strconv.Itoa(line) + `,"absolute_offset":0,"submatches":[]}}`
}

// A rooted include glob makes rg walk only the subtree it names, so a sibling repo is never listed.
func TestText_RootedIncludeSearchesOnlyThatSubtree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"repo/internal/a.go": "x", "other/b.go": "x"})
	argsFile := filepath.Join(t.TempDir(), "args")
	bin := fakeRG(t, `printf '%s\n' "$@" > `+argsFile)

	Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "x", IncludePattern: "repo/internal/**/*.go"})

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("rg was not run: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if got := args[len(args)-1]; got != filepath.Join("repo", "internal") {
		t.Errorf("search path = %q, want repo/internal", got)
	}
	if !slices.Contains(args, "repo/internal/**/*.go") {
		t.Errorf("args %q do not pass the include glob to rg", args)
	}
	if slices.Contains(args, "--follow") || slices.Contains(args, "-L") {
		t.Errorf("args %q follow symlinks", args)
	}
}

func TestText_RealRGRootedIncludeFindsOnlyThatSubtree(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"repo/internal/deeper/a.go": "func Run() {}\n",
		"repo/other/b.go":           "func Run() {}\n",
		"other/internal/c.go":       "func Run() {}\n",
	})

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "func Run", IncludePattern: "repo/internal/**/*.go"})

	want := []Match{{FilePath: "repo/internal/deeper/a.go", LineNumber: 1, LineText: "func Run() {}"}}
	if !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want %+v", got, want)
	}
}

func TestText_IncludeUnderAMissingDirectoryRunsNoSearch(t *testing.T) {
	root := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	bin := fakeRG(t, `printf '%s\n' "$@" > `+argsFile)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "x", IncludePattern: "absent/**/*.go"})

	if got == nil || len(got) != 0 {
		t.Errorf("Text = %#v, want an empty non-nil slice", got)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Errorf("rg ran for a subtree that does not exist")
	}
}

// With a real rg: the rooted pattern finds the file under repo and not the same name elsewhere.
func TestFiles_RealRGFindsOnlyUnderTheRootedPrefix(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"repo/internal/build.go": "", "other/internal/build.go": ""})

	got := Searcher{Bin: bin}.Files(t.Context(), []string{root}, FilesQuery{Pattern: "repo/internal/**/*build.go*", MaxResults: 11})

	if want := []string{"repo/internal/build.go"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}

// rg's exit 2 means some paths were unreadable; what it printed is still the answer.
func TestText_ExitTwoKeepsTheMatches(t *testing.T) {
	root := t.TempDir()
	bin := fakeRG(t, `printf '%s\n' '`+matchLine("./a.go", "func Run()", 3)+`'
echo 'rg: ./broken: No such file or directory (os error 2)' >&2
exit 2`)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "func Run"})

	want := []Match{{FilePath: "a.go", LineNumber: 3, LineText: "func Run()"}}
	if !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want %+v", got, want)
	}
}

func TestFiles_ExitTwoKeepsTheFiles(t *testing.T) {
	root := t.TempDir()
	bin := fakeRG(t, `echo ./a.go
echo 'rg: ./loop: File system loop found' >&2
exit 2`)

	got := Searcher{Bin: bin}.Files(t.Context(), []string{root}, FilesQuery{Pattern: "**/*a.go*", MaxResults: 11})

	if want := []string{"a.go"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}

// slowRG prints one result, then runs a child that would outlive the search, writing its pid to pidFile.
func slowRG(t *testing.T, firstLine, pidFile string) string {
	t.Helper()
	return fakeRG(t, `printf '%s\n' '`+firstLine+`'
/bin/sleep 60 &
echo $! > `+pidFile+`
wait`)
}

func alive(t *testing.T, pidFile string) bool {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the fake rg never started its child: %v", err)
	}
	stat, err := os.ReadFile("/proc/" + strings.TrimSpace(string(raw)) + "/stat")
	if err != nil {
		return false
	}
	_, rest, _ := strings.Cut(string(stat), ") ")
	return !strings.HasPrefix(rest, "Z")
}

func awaitGone(t *testing.T, pidFile string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for alive(t, pidFile) {
		if time.Now().After(deadline) {
			t.Fatal("rg's child outlived the search")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestText_DeadlineReturnsThePartialResultAndKillsTheGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := slowRG(t, matchLine("./a.go", "hit", 1), pidFile)
	const deadline = 300 * time.Millisecond
	prev := searchDeadline
	searchDeadline = deadline
	t.Cleanup(func() { searchDeadline = prev })

	start := time.Now()
	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit"})
	took := time.Since(start)

	if took > deadline+killWait {
		t.Errorf("Text took %v, want at most the deadline %v plus %v", took, deadline, killWait)
	}
	want := []Match{{FilePath: "a.go", LineNumber: 1, LineText: "hit"}}
	if !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want the partial result %+v", got, want)
	}
	awaitGone(t, pidFile)
}

func TestFiles_CancelReturnsThePartialResultAndKillsTheGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := slowRG(t, "./a.go", pidFile)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)

	start := time.Now()
	got := Searcher{Bin: bin}.Files(ctx, []string{root}, FilesQuery{Pattern: "**/*a.go*", MaxResults: 11})

	if took := time.Since(start); took > 300*time.Millisecond+killWait {
		t.Errorf("Files took %v after a cancel at 300ms", took)
	}
	if want := []string{"a.go"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want the partial result %q", got, want)
	}
	awaitGone(t, pidFile)
}

func TestSearch_NoBinaryAnswersEmpty(t *testing.T) {
	root := t.TempDir()
	if got := (Searcher{}).Text(t.Context(), []string{root}, TextQuery{Pattern: "x"}); got == nil || len(got) != 0 {
		t.Errorf("Text = %#v, want an empty non-nil slice", got)
	}
	if got := (Searcher{}).Files(t.Context(), []string{root}, FilesQuery{Pattern: "x"}); got == nil || len(got) != 0 {
		t.Errorf("Files = %#v, want an empty non-nil slice", got)
	}
}

func TestFiles_StopsAtMaxResults(t *testing.T) {
	root := t.TempDir()
	bin := fakeRG(t, `for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do echo ./f$i.go; done`)

	got := Searcher{Bin: bin}.Files(t.Context(), []string{root}, FilesQuery{Pattern: "**/*", MaxResults: 11})

	if len(got) != 11 {
		t.Errorf("len(Files) = %d, want maxResults 11", len(got))
	}
}

func warnings(logs *capture.Recorder) []string {
	var out []string
	for _, r := range logs.Records() {
		if r.Level >= slog.LevelWarn {
			out = append(out, r.Message)
		}
	}
	return out
}

// rg refuses a pattern its regex engines cannot compile (exit 2, no output); KAS's own search then
// looks for the literal text.
func TestText_UncompilablePatternSearchesTheLiteral(t *testing.T) {
	root := t.TempDir()
	bin := fakeRG(t, `case " $* " in
*" --fixed-strings "*) printf '%s\n' '`+matchLine("./a.go", "func Run() {}", 4)+`' ;;
*) echo 'rg: regex parse error:' >&2; exit 2 ;;
esac`)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "func Run("})

	want := []Match{{FilePath: "a.go", LineNumber: 4, LineText: "func Run() {}"}}
	if !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want %+v", got, want)
	}
}

func TestText_RealRGFindsAnUncompilablePatternAsText(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n\nfunc Run() {}\n", "b.go": "package b\n"})

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "func Run(", CaseSensitive: true})

	if !slices.ContainsFunc(got, func(m Match) bool { return m.FilePath == "a.go" && m.LineNumber == 3 && !m.IsContext }) {
		t.Errorf("Text = %+v, want the match a.go:3", got)
	}
}

// rg exiting 2 having searched nothing for any other reason is a failure worth a warning.
func TestText_RefusedSearchWarns(t *testing.T) {
	logs := capture.Default(t)
	bin := fakeRG(t, `echo "rg: error parsing glob '{a': unclosed alternate group" >&2
exit 2`)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{t.TempDir()}, TextQuery{Pattern: "x", IncludePattern: "{a"})

	if got == nil || len(got) != 0 {
		t.Errorf("Text = %#v, want an empty non-nil slice", got)
	}
	if len(warnings(logs)) != 1 {
		t.Errorf("warnings = %q, want one for the refused search", warnings(logs))
	}
}

// A --files walk that finds no file writes nothing, so its exit 2 with no output is a refusal only
// when rg names a bad glob; otherwise it met unreadable paths. The stderr lines are rg 14.1.1's.
func TestFiles_ExitTwoWithNoOutputWarnsOnlyForARefusal(t *testing.T) {
	tests := map[string]struct {
		stderr string
		want   int
	}{
		"unreadable path": {"rg: gone: IO error for operation on gone: No such file or directory (os error 2)", 0},
		"bad glob":        {"rg: error parsing glob '{a': unclosed alternate group; missing '}'", 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			logs := capture.Default(t)
			bin := fakeRG(t, `echo "`+tc.stderr+`" >&2
exit 2`)

			got := Searcher{Bin: bin}.Files(t.Context(), []string{t.TempDir()}, FilesQuery{Pattern: "**/*.go", MaxResults: 11})

			if got == nil || len(got) != 0 {
				t.Errorf("Files = %#v, want an empty non-nil slice", got)
			}
			if w := warnings(logs); len(w) != tc.want {
				t.Errorf("warnings = %q, want %d", w, tc.want)
			}
		})
	}
}

// rg closing its output and then exiting 0 is a clean search, not a failure.
func TestFiles_CleanExitAfterTheOutputClosesLogsNoWarning(t *testing.T) {
	logs := capture.Default(t)
	bin := fakeRG(t, `echo ./a.go
exec 1>&-
/bin/sleep 0.2`)

	got := Searcher{Bin: bin}.Files(t.Context(), []string{t.TempDir()}, FilesQuery{Pattern: "**/*", MaxResults: 11})

	if want := []string{"a.go"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings = %q, want none", w)
	}
}

func TestText_OutputCapLogsNoWarning(t *testing.T) {
	logs := capture.Default(t)
	bin := fakeRG(t, `/usr/bin/yes '`+strings.Repeat("x", 4000)+`' | /usr/bin/head -c `+strconv.Itoa(maxOutputBytes+1<<20)+`
/bin/sleep 60`)

	start := time.Now()
	Searcher{Bin: bin}.Text(t.Context(), []string{t.TempDir()}, TextQuery{Pattern: "x"})

	if took := time.Since(start); took > killWait+time.Second {
		t.Errorf("Text took %v, want it to stop at the output cap", took)
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings = %q, want none: the cap is a limit, not a failure", w)
	}
}

func TestText_EndedContextLogsNoWarning(t *testing.T) {
	logs := capture.Default(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	Searcher{Bin: fakeRG(t, "exit 0")}.Text(ctx, []string{t.TempDir()}, TextQuery{Pattern: "x"})

	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings = %q, want none for a search whose caller left", w)
	}
}

// One rg JSON line past maxLineBytes is skipped, and the lines after it are still read.
func TestText_OverLongLineIsSkipped(t *testing.T) {
	root := t.TempDir()
	bin := fakeRG(t, `/usr/bin/head -c `+strconv.Itoa(maxLineBytes+1)+` /dev/zero
echo
printf '%s\n' '`+matchLine("./b.go", "later", 2)+`'`)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "later"})

	want := []Match{{FilePath: "b.go", LineNumber: 2, LineText: "later"}}
	if !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want %+v", got, want)
	}
}

// A directory named like a flag is still a path to rg.
func TestFiles_RealRGReadsAFlagShapedDirectoryAsAPath(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"-h/a.go": "", "b.go": ""})

	got := Searcher{Bin: bin}.Files(t.Context(), []string{root}, FilesQuery{Pattern: "-h/**/*.go", MaxResults: 11})

	if want := []string{"-h/a.go"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}

// rg does not follow a symlink, so no file under a linked prefix can match.
func TestText_IncludeThroughASymlinkRunsNoSearch(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/a.go": "x"})
	if err := os.Symlink("real", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args")
	bin := fakeRG(t, `printf '%s\n' "$@" > `+argsFile)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "x", IncludePattern: "link/**/*.go"})

	if got == nil || len(got) != 0 {
		t.Errorf("Text = %#v, want an empty non-nil slice", got)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Errorf("rg walked the root for a prefix it cannot enter")
	}
}

// A workspace that is not a git repository still keeps its .gitignore'd files out, as KAS's rg does.
func TestSearch_RealRGHonoursGitignoreOutsideARepository(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{".gitignore": "ignored/\n", "ignored/a.go": "hit\n", "kept/a.go": "hit\n"})
	s := Searcher{Bin: bin}

	t.Run("files", func(t *testing.T) {
		got := s.Files(t.Context(), []string{root}, FilesQuery{Pattern: "**/*.go", MaxResults: 11})
		if want := []string{"kept/a.go"}; !slices.Equal(got, want) {
			t.Errorf("Files = %q, want %q", got, want)
		}
	})
	t.Run("text", func(t *testing.T) {
		got := s.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit"})
		want := []Match{{FilePath: "kept/a.go", LineNumber: 1, LineText: "hit"}}
		if !slices.Equal(got, want) {
			t.Errorf("Text = %+v, want %+v", got, want)
		}
	})
}

// Answer paths are written against Shown, the session's roots, when it is set.
func TestFiles_ShownRootsWriteThePaths(t *testing.T) {
	work := filepath.Join(t.TempDir(), "w")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := fakeRG(t, `echo ./notes/x.md`)

	got := Searcher{Bin: bin, Shown: []string{work, "/d/notes"}}.Files(t.Context(), []string{work}, FilesQuery{Pattern: "**/*", MaxResults: 11})

	if want := []string{"w/notes/x.md"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}

// Expected values follow KAS 3.0.0's Rar and MJ (testdata kas-probe search-text.json pathForm).
func TestDisplayPath(t *testing.T) {
	roots := []string{"/w", "/d/notes"}
	tests := map[string]struct {
		abs  string
		want string
	}{
		"under the first root":                    {"/w/repo/a.go", "repo/a.go"},
		"under a later root":                      {"/d/notes/x.md", "notes/x.md"},
		"first root name clash with a later root": {"/w/notes/x.md", "w/notes/x.md"},
		"under no root":                           {"/elsewhere/a.go", "/elsewhere/a.go"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := displayPath(tc.abs, roots); got != tc.want {
				t.Errorf("displayPath(%q) = %q, want %q", tc.abs, got, tc.want)
			}
		})
	}
	if got := displayPath("/w/repo/a.go", []string{"/w"}); got != "repo/a.go" {
		t.Errorf("one root: displayPath = %q, want repo/a.go", got)
	}
}
