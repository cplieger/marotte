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

// An absolute include under one root names nothing under another, which is not searched.
func TestText_RealRGAbsoluteIncludeSearchesOnlyItsRoot(t *testing.T) {
	bin := realRG(t)
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, map[string]string{"repo/a.go": "hit\n"})
	writeTree(t, second, map[string]string{"repo/a.go": "hit\n"})

	got := Searcher{Bin: bin}.Text(t.Context(), []string{first, second}, TextQuery{Pattern: "hit", IncludePattern: first + "/repo/*.go"})

	want := []Match{{FilePath: "repo/a.go", LineNumber: 1, LineText: "hit"}}
	if !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want %+v", got, want)
	}
}

func TestText_RealRGExcludeFormsLeaveOutTheFolderTheyName(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"repo/internal/a.go": "hit\n", "keep/b.go": "hit\n"})
	want := []Match{{FilePath: "keep/b.go", LineNumber: 1, LineText: "hit"}}
	for name, exclude := range map[string]string{
		"relative":               "repo/internal/**",
		"absolute":               root + "/repo/internal/**",
		"dot-led":                "./repo/internal/**",
		"trailing slash":         "repo/internal/",
		"bare folder":            "repo/internal",
		"dot segment":            "repo/./internal/**",
		"doubled slash":          "repo//internal/**",
		"doubled leading slash":  "//repo/internal/**",
		"led by slash-dot":       "/./repo/internal/**",
		"doubled trailing slash": "repo/internal//",
	} {
		t.Run(name, func(t *testing.T) {
			got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", ExcludePattern: exclude})
			if !slices.Equal(got, want) {
				t.Errorf("Text(exclude %q) = %+v, want %+v", exclude, got, want)
			}
		})
	}
}

// KAS turns a file_search query "^<path>" into the pattern "<path>*", absolute when the model wrote
// an absolute path.
func TestFiles_RealRGAbsoluteAndDotLedPatternsFindUnderTheRoot(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"repo/internal/build.go": "", "other/build.go": ""})
	for name, pattern := range map[string]string{
		"absolute": root + "/repo/internal/build*",
		"dot-led":  "./repo/internal/build*",
	} {
		t.Run(name, func(t *testing.T) {
			got := Searcher{Bin: bin}.Files(t.Context(), []string{root}, FilesQuery{Pattern: pattern, MaxResults: 11})
			if want := []string{"repo/internal/build.go"}; !slices.Equal(got, want) {
				t.Errorf("Files(%q) = %q, want %q", pattern, got, want)
			}
		})
	}
}

// A path written from the root ("./x.go", "<root>/x.go") names the root's own file, not every file
// of that name in the tree.
func TestText_RealRGRootFileFormsFindOnlyTheRootFile(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"x.go": "hit\n", "sub/x.go": "hit\n"})
	want := []Match{{FilePath: "x.go", LineNumber: 1, LineText: "hit"}}
	for name, include := range map[string]string{
		"root-anchored": "/x.go",
		"dot-led":       "./x.go",
		"absolute":      root + "/x.go",
	} {
		t.Run(name, func(t *testing.T) {
			got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", IncludePattern: include})
			if !slices.Equal(got, want) {
				t.Errorf("Text(include %q) = %+v, want %+v", include, got, want)
			}
		})
	}
}

// An include naming the root itself searches all of it.
func TestText_RealRGRootIncludeFormsSearchTheWholeRoot(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "hit\n"})
	want := []Match{{FilePath: "a.go", LineNumber: 1, LineText: "hit"}}
	for name, include := range map[string]string{"dot": ".", "dot slash": "./", "slash": "/", "absolute": root, "absolute, slash": root + "/"} {
		t.Run(name, func(t *testing.T) {
			got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", IncludePattern: include})
			if !slices.Equal(got, want) {
				t.Errorf("Text(include %q) = %+v, want %+v", include, got, want)
			}
		})
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

func TestText_DeadlineReturnsThePartialResultAndKillsTheSession(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := slowRG(t, matchLine("./a.go", "hit", 1), pidFile)
	const deadline = 300 * time.Millisecond
	shortDeadline(t, deadline)

	start := time.Now()
	got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit"})
	took := time.Since(start)

	if took > deadline+killWait {
		t.Errorf("Text took %v, want at most the deadline %v plus %v", took, deadline, killWait)
	}
	if len(got) == 0 || !isPartialNotice(got[0].FilePath) {
		t.Fatalf("Text = %+v, want it to open on a notice that the results are partial", got)
	}
	want := []Match{{FilePath: "a.go", LineNumber: 1, LineText: "hit"}}
	if !slices.Equal(got[1:], want) {
		t.Errorf("Text after the notice = %+v, want the partial result %+v", got[1:], want)
	}
	awaitGone(t, pidFile)
}

// isPartialNotice reports whether an answer entry is the notice of a search the deadline cut short.
func isPartialNotice(s string) bool {
	return strings.Contains(s, "partial") && strings.Contains(s, searchDeadline.String())
}

func shortDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	prev := searchDeadline
	searchDeadline = d
	t.Cleanup(func() { searchDeadline = prev })
}

func TestFiles_DeadlineAnswerSaysItIsPartial(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := slowRG(t, "./a.go", pidFile)
	const deadline = 300 * time.Millisecond
	shortDeadline(t, deadline)

	start := time.Now()
	got := Searcher{Bin: bin}.Files(t.Context(), []string{t.TempDir()}, FilesQuery{Pattern: "**/*a.go*", MaxResults: 11})

	if took := time.Since(start); took > deadline+killWait {
		t.Errorf("Files took %v, want at most the deadline %v plus %v", took, deadline, killWait)
	}
	if len(got) != 2 || !isPartialNotice(got[0]) || got[1] != "a.go" {
		t.Errorf("Files = %q, want the partial notice and then a.go", got)
	}
	awaitGone(t, pidFile)
}

// KAS reruns a search answered after its 30 s client wait on a walker with no deadline, and a loaded
// host spent up to 17.3 s outside marotte (searchDeadline's measurement).
func TestSearchDeadline_AnswersWithinKASsClientWait(t *testing.T) {
	const kasWait, outside = 30 * time.Second, 17300 * time.Millisecond
	if total := searchDeadline + killWait + outside; total >= kasWait {
		t.Errorf("deadline %v + kill wait %v + %v outside = %v, want under KAS's %v wait", searchDeadline, killWait, outside, total, kasWait)
	}
}

// A caller's own deadline ends the search like a cancel, with no notice: the notice names
// searchDeadline.
func TestText_CallerDeadlineAddsNoPartialNotice(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := slowRG(t, matchLine("./a.go", "hit", 1), pidFile)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	got := Searcher{Bin: bin}.Text(ctx, []string{t.TempDir()}, TextQuery{Pattern: "hit"})

	if want := []Match{{FilePath: "a.go", LineNumber: 1, LineText: "hit"}}; !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want the partial result %+v and no notice", got, want)
	}
	awaitGone(t, pidFile)
}

// A search that ends before its deadline is whole, and its answer carries no notice.
func TestText_WholeAnswerCarriesNoPartialNotice(t *testing.T) {
	bin := fakeRG(t, `printf '%s\n' '`+matchLine("./a.go", "hit", 1)+`'`)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{t.TempDir()}, TextQuery{Pattern: "hit"})

	if want := []Match{{FilePath: "a.go", LineNumber: 1, LineText: "hit"}}; !slices.Equal(got, want) {
		t.Errorf("Text = %+v, want %+v", got, want)
	}
}

// The answer is capped at maxTextEntries with the notice counted, so the notice is never the entry
// a cap drops.
func TestText_PartialNoticeCountsTowardTheEntryCap(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	var lines strings.Builder
	for i := range maxTextEntries + 5 {
		lines.WriteString(matchLine("./f"+strconv.Itoa(i)+".go", "hit", 1) + "\n")
	}
	bin := fakeRG(t, `printf '%s' '`+lines.String()+`'
/bin/sleep 60 &
echo $! > `+pidFile+`
wait`)
	shortDeadline(t, 300*time.Millisecond)

	got := Searcher{Bin: bin}.Text(t.Context(), []string{t.TempDir()}, TextQuery{Pattern: "hit"})

	if len(got) != maxTextEntries || !isPartialNotice(got[0].FilePath) {
		t.Errorf("Text gave %d entries opening on %q, want %d opening on the partial notice", len(got), got[0].FilePath, maxTextEntries)
	}
	awaitGone(t, pidFile)
}

// escapedRG prints one result, then leaves a child that called setsid holding rg's output, which the
// session kill does not reach. The child is killed by pid when the test ends.
func escapedRG(t *testing.T, firstLine string) string {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return fakeRG(t, `printf '%s\n' '`+firstLine+`'
/usr/bin/setsid /bin/sleep 30 &
echo $! > `+pidFile+`
wait`)
}

func TestText_DeadlineEndsTheReadWhenAChildOutsideTheSessionHoldsTheOutput(t *testing.T) {
	bin := escapedRG(t, matchLine("./a.go", "hit", 1))
	const deadline = 300 * time.Millisecond
	shortDeadline(t, deadline)

	start := time.Now()
	got := Searcher{Bin: bin}.Text(t.Context(), []string{t.TempDir()}, TextQuery{Pattern: "hit"})

	if took := time.Since(start); took > deadline+killWait+time.Second {
		t.Errorf("Text took %v, want the deadline %v plus at most %v", took, deadline, killWait+time.Second)
	}
	if len(got) != 2 || !isPartialNotice(got[0].FilePath) || got[1].FilePath != "a.go" {
		t.Errorf("Text = %+v, want the partial notice and then a.go", got)
	}
}

// twoRootRG lists n files in a root holding a "fast" file, and in any other root lists nothing until
// it is killed, leaving a child whose pid it writes to pidFile.
func twoRootRG(t *testing.T, n int, pidFile string) string {
	t.Helper()
	return fakeRG(t, `if [ -e fast ]; then
  i=0; while [ $i -lt `+strconv.Itoa(n)+` ]; do echo ./f$i.go; i=$((i+1)); done
else
  /bin/sleep 60 &
  echo $! > `+pidFile+`
  wait
fi`)
}

// Files shows the first root's files first, so once the first root alone fills maxResults a slow
// later root can add nothing: the answer comes without waiting for it, and it is whole.
func TestFiles_FirstRootFillingTheLimitEndsTheSearchWhole(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, map[string]string{"fast": ""})
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := twoRootRG(t, 20, pidFile)
	const deadline = 5 * time.Second
	shortDeadline(t, deadline)

	start := time.Now()
	got := Searcher{Bin: bin}.Files(t.Context(), []string{first, second}, FilesQuery{Pattern: "**/*.go*", MaxResults: 11})

	if took := time.Since(start); took > deadline/2 {
		t.Errorf("Files took %v, want the answer well before the %v deadline", took, deadline)
	}
	if len(got) != 11 || slices.ContainsFunc(got, isPartialNotice) {
		t.Errorf("Files = %q, want 11 files and no partial notice", got)
	}
	if _, err := os.Stat(pidFile); err == nil {
		awaitGone(t, pidFile)
	}
}

// A later root the deadline cut still makes the answer partial while the earlier roots left room.
func TestFiles_LaterRootCutWithRoomLeftIsPartial(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, map[string]string{"fast": ""})
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := twoRootRG(t, 3, pidFile)
	shortDeadline(t, 300*time.Millisecond)

	got := Searcher{Bin: bin}.Files(t.Context(), []string{first, second}, FilesQuery{Pattern: "**/*.go*", MaxResults: 11})

	if len(got) != 4 || !isPartialNotice(got[0]) {
		t.Errorf("Files = %q, want the partial notice and the first root's 3 files", got)
	}
	awaitGone(t, pidFile)
}

// The first root's files come first, so the deadline cutting it leaves the answer partial even when
// a later root alone fills maxResults.
func TestFiles_FirstRootCutIsPartialWhenALaterRootFillsTheLimit(t *testing.T) {
	slow, fast := t.TempDir(), t.TempDir()
	writeTree(t, fast, map[string]string{"fast": ""})
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := twoRootRG(t, 20, pidFile)
	shortDeadline(t, 300*time.Millisecond)

	got := Searcher{Bin: bin}.Files(t.Context(), []string{slow, fast}, FilesQuery{Pattern: "**/*.go*", MaxResults: 11})

	if len(got) != 11 || !isPartialNotice(got[0]) {
		t.Errorf("Files = %q, want the partial notice and 10 of the later root's files", got)
	}
	awaitGone(t, pidFile)
}

// The notice counts toward maxResults, so the cap drops a file, never the notice.
func TestFiles_PartialNoticeCountsTowardMaxResults(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeTree(t, first, map[string]string{"fast": ""})
	pidFile := filepath.Join(t.TempDir(), "pid")
	bin := fakeRG(t, `if [ -e fast ]; then
  i=0; while [ $i -lt 5 ]; do echo ./f$i.go; i=$((i+1)); done
else
  i=0; while [ $i -lt 8 ]; do echo ./g$i.go; i=$((i+1)); done
  /bin/sleep 60 &
  echo $! > `+pidFile+`
  wait
fi`)
	shortDeadline(t, 300*time.Millisecond)

	got := Searcher{Bin: bin}.Files(t.Context(), []string{first, second}, FilesQuery{Pattern: "**/*.go*", MaxResults: 11})

	if len(got) != 11 || !isPartialNotice(got[0]) || got[5] != "f4.go" || got[10] != filepath.Base(second)+"/g4.go" {
		t.Errorf("Files = %q, want the partial notice, the first root's 5 files and 5 of the second's", got)
	}
	awaitGone(t, pidFile)
}

func TestFiles_CancelReturnsThePartialResultAndKillsTheSession(t *testing.T) {
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

// A linked prefix is never rg's start path: rg follows a start path that is a symlink, outside the
// root included.
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

func TestText_RealRGRootEntryIncludeFindsOnlyTheRootsOwnEntries(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"top.md": "hit\n", "sub/b.md": "hit\n", "sub/top.md": "hit\n"})
	want := []Match{{FilePath: "top.md", LineNumber: 1, LineText: "hit"}}
	for _, include := range []string{"/*.md", "/top.md", root + "/top.md"} {
		t.Run(include, func(t *testing.T) {
			got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", IncludePattern: include})
			if !slices.Equal(got, want) {
				t.Errorf("Text(include %q) = %+v, want %+v", include, got, want)
			}
		})
	}
}

// KAS turns a plain file_search query "kiro" into "**/*kiro*", which its own rg matches against file
// names: a folder whose name holds the query does not list its files.
func TestFiles_RealRGNameQueryListsOnlyTheNamesItMatches(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/b/kiro.go": "", "kirosearch/kirosearch.go": "", "kirosearch/doc.go": "", "src/kiro-docs/one.md": "", "top.go": "",
	})

	got := Searcher{Bin: bin}.Files(t.Context(), []string{root}, FilesQuery{Pattern: "**/*kiro*", MaxResults: 11})

	slices.Sort(got)
	if want := []string{"a/b/kiro.go", "kirosearch/kirosearch.go"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}

// An include whose last segment is a name glob selects files by name, as in rg: a folder whose name
// it matches does not add its files.
func TestText_RealRGNameIncludesSearchOnlyTheNamesTheyMatch(t *testing.T) {
	bin := realRG(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": "hit\n", "pkg/a_test.go": "hit\n", "dir.go/nested/notgo.txt": "hit\n", "testdata/x.txt": "hit\n",
	})
	match := func(path string) Match { return Match{FilePath: path, LineNumber: 1, LineText: "hit"} }
	for include, want := range map[string][]Match{
		"*.go":       {match("a.go"), match("pkg/a_test.go")},
		"**/*.go":    {match("a.go"), match("pkg/a_test.go")},
		"**/*test*":  {match("pkg/a_test.go")},
		"pkg/*test*": {match("pkg/a_test.go")},
	} {
		t.Run(include, func(t *testing.T) {
			got := Searcher{Bin: bin}.Text(t.Context(), []string{root}, TextQuery{Pattern: "hit", IncludePattern: include})
			if !slices.Equal(got, want) {
				t.Errorf("Text(include %q) = %+v, want %+v", include, got, want)
			}
		})
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
