// Package kirosearch answers kiro-cli's grep_search and file_search (KAS's `_kiro/search/text_search`
// and `_kiro/search/find_files`) with ripgrep, inside a deadline, in the shapes KAS 3.0.0 reads.
//
// Every search returns a result, empty included, and never an error: KAS reruns a search the client
// failed with its own Node walker, which has no time limit and no cancel.
package kirosearch

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/marotte/internal/procout"
)

// searchDeadline bounds one search across every root; a var so tests can shorten it. KAS reruns a
// search answered after its 30 s client wait on a walker with no deadline, and on a loaded host
// 17.3 s passed outside marotte (KAS's slowest answer, 32.3 s, to a search cut at 15 s), so this plus
// killWait plus 17.3 s must stay under 30 s.
var searchDeadline = 10 * time.Second

// errSearchDeadline is the cause searchDeadline ends a search with, which tells it from a deadline
// the caller set.
var errSearchDeadline = errors.New("search deadline")

// KAS 3.0.0's own caps (kGo, DGo, OGo and UZu in acp-server.js), so an answer holds what KAS's
// own rg would have returned.
const (
	maxTextFiles   = 100
	maxTextEntries = 100
	maxCountArg    = "5"
	maxFileDepth   = 50
	maxOutputBytes = 10 << 20
)

const (
	// maxLineBytes bounds one rg output line. A --json match event carries every submatch of its
	// line, so --max-filesize does not bound it; a longer line is skipped.
	maxLineBytes   = 4 << 20
	maxStderrBytes = 64 << 10
	// killWait bounds Wait after the session's SIGKILL, for a child still holding the pipes.
	killWait = 2 * time.Second
	// defaultMaxFiles applies when find_files names no maxResults; KAS 3.0.0 always sends 11.
	defaultMaxFiles = 100
	maxFileResults  = 1000
)

// Match is one text_search entry, KAS's {filePath, lineNumber, lineText, isContext}.
type Match struct {
	FilePath   string `json:"filePath"`
	LineText   string `json:"lineText"`
	LineNumber int    `json:"lineNumber"`
	IsContext  bool   `json:"isContext"`
}

// TextQuery is a text_search request's search fields. The patterns are ripgrep globs, matched
// against paths relative to each root.
type TextQuery struct {
	Pattern        string
	IncludePattern string
	ExcludePattern string
	CaseSensitive  bool
}

// FilesQuery is a find_files request's search fields; MaxResults at or below 0 means 100.
type FilesQuery struct {
	Pattern    string
	Exclude    string
	MaxResults int
}

// Searcher runs the rg binary at Bin, each search within searchDeadline. An empty Bin finds nothing. Shown
// is the session's roots answer paths are written against (displayPath); nil means the searched roots.
type Searcher struct {
	Bin   string
	Shown []string
}

func (s Searcher) shown(searched []string) []string {
	if s.Shown != nil {
		return s.Shown
	}
	return searched
}

// Text searches roots for q.Pattern and returns the entries of the 100 files with the most matching
// lines, at most 100 entries, with paths in KAS's form (displayPath). Whatever was found when ctx
// ends or the deadline passes is returned, and a deadline answer opens on a notice entry saying it
// is partial. The result is never nil.
func (s Searcher) Text(ctx context.Context, roots []string, q TextQuery) []Match {
	if s.Bin == "" || q.Pattern == "" || len(roots) == 0 {
		return []Match{}
	}
	ctx, cancel := context.WithTimeoutCause(ctx, searchDeadline, errSearchDeadline)
	defer cancel()
	perRoot := make([][]*fileHits, len(roots))
	cut := make([]bool, len(roots))
	var wg sync.WaitGroup
	for i, root := range roots {
		args, ok := textArgs(root, q)
		if !ok {
			continue
		}
		wg.Go(func() {
			perRoot[i], cut[i] = s.textRoot(ctx, root, args)
		})
	}
	wg.Wait()

	var files []*fileHits
	for _, f := range perRoot {
		files = append(files, f...)
	}
	slices.SortStableFunc(files, func(a, b *fileHits) int {
		return cmp.Or(cmp.Compare(b.matchCount, a.matchCount), strings.Compare(a.abs, b.abs))
	})
	files = files[:min(len(files), maxTextFiles)]
	out := make([]Match, 0, maxTextEntries)
	if deadlineCut(ctx, slices.Contains(cut, true)) {
		out = append(out, Match{
			FilePath:  partialNotice(),
			LineText:  "Only part of the workspace was searched. Start includePattern with the folder to search, such as src/**/*.go, and set no excludePattern, to search it whole.",
			IsContext: true,
		})
	}
	for _, f := range files {
		slices.SortStableFunc(f.entries, func(a, b Match) int { return cmp.Compare(a.LineNumber, b.LineNumber) })
		shown := displayPath(f.abs, s.shown(roots))
		for _, e := range f.entries {
			if len(out) == maxTextEntries {
				return out
			}
			e.FilePath = shown
			out = append(out, e)
		}
	}
	return out
}

// textArgs is rg's command line searching root for q; ok is false when no file under root can match.
func textArgs(root string, q TextQuery) (args []string, ok bool) {
	include := rootGlob(root, q.IncludePattern)
	dir, _, ok := startDir(root, include, q.ExcludePattern != "")
	if !ok {
		return nil, false
	}
	args = []string{"--json", "--engine", "auto", "-C", "2", "--max-count", maxCountArg, "--max-filesize", "1M", "--hidden", "--no-require-git", "--glob", "!.git"}
	if !q.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	if exclude := rootGlob(root, q.ExcludePattern); exclude != "" {
		args = append(args, "--glob", "!"+exclude)
	}
	return runnable(append(args, "--", trimLoneQuote(q.Pattern), dir))
}

// runnable is args with ok false when one holds a NUL, which exec refuses, so no rg can run.
func runnable(args []string) (_ []string, ok bool) {
	if slices.ContainsFunc(args, func(a string) bool { return strings.ContainsRune(a, 0) }) {
		return nil, false
	}
	return args, true
}

type fileHits struct {
	abs        string
	entries    []Match
	matchCount int
}

// deadlineCut reports whether searchDeadline, not the caller, ended a search that cut says was
// stopped before rg finished a root whose results could be shown.
func deadlineCut(ctx context.Context, cut bool) bool {
	return cut && errors.Is(context.Cause(ctx), errSearchDeadline)
}

// partialNotice opens the answer of a search the deadline cut short: KAS renders every answer as
// whole (file_search's "complete results", grep_search's "No matches found."), and an answer entry
// is the only text of a client's that reaches the model.
func partialNotice() string {
	return fmt.Sprintf("[partial results: the search stopped at its %v deadline]", searchDeadline)
}

// rgEvent is the part of one `rg --json` line Text reads: KAS's qZu keeps a match or context line
// with a path, a non-empty text and a line number, and drops lines rg sent as bytes (non-UTF-8).
type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func (s Searcher) textRoot(ctx context.Context, root string, args []string) (found []*fileHits, cut bool) {
	byPath := map[string]*fileHits{}
	var order []*fileHits
	onLine := func(line []byte) bool {
		var ev rgEvent
		if json.Unmarshal(line, &ev) != nil || (ev.Type != "match" && ev.Type != "context") {
			return true
		}
		if ev.Data.Path.Text == "" || ev.Data.Lines.Text == "" || ev.Data.LineNumber <= 0 {
			return true
		}
		abs := filepath.Join(root, ev.Data.Path.Text)
		f := byPath[abs]
		if f == nil {
			f = &fileHits{abs: abs}
			byPath[abs] = f
			order = append(order, f)
		}
		f.entries = append(f.entries, Match{
			LineNumber: ev.Data.LineNumber,
			LineText:   strings.TrimSuffix(ev.Data.Lines.Text, "\n"),
			IsContext:  ev.Type == "context",
		})
		if ev.Type == "match" {
			f.matchCount++
		}
		return true
	}
	refusal, cut := s.run(ctx, root, args, onLine)
	if refusal != "" && regexRefused(refusal) {
		// KAS 3.0.0's own search escapes a pattern its regex engine rejects and finds the literal.
		refusal, cut = s.run(ctx, root, append([]string{"--fixed-strings"}, args...), onLine)
	}
	logRefusal(root, refusal)
	return order, cut
}

// regexRefused reports whether rg's stderr names a pattern no regex engine could compile.
func regexRefused(stderr string) bool {
	return strings.Contains(stderr, "regex parse error") || strings.Contains(stderr, "regex could not be compiled") ||
		strings.Contains(stderr, "compiled regex exceeds size limit")
}

func logRefusal(root, stderr string) {
	if stderr != "" {
		first, _, _ := strings.Cut(stderr, "\n")
		slog.Warn("search: ripgrep exited 2 having searched nothing", "root", root, "stderr", first)
	}
}

// Files lists the files under roots matching q.Pattern, at most q.MaxResults, the first root's
// first, with paths in KAS's form. .gitignore is honoured below the folder the pattern names
// (startDir) and symlinks are not followed. Whatever was found when ctx ends or the deadline
// passes is returned, and a deadline answer opens on a notice entry saying it is partial. The
// result is never nil.
func (s Searcher) Files(ctx context.Context, roots []string, q FilesQuery) []string {
	limit := q.MaxResults
	if limit <= 0 {
		limit = defaultMaxFiles
	}
	limit = min(limit, maxFileResults)
	if s.Bin == "" || len(roots) == 0 {
		return []string{}
	}
	ctx, cancel := context.WithTimeoutCause(ctx, searchDeadline, errSearchDeadline)
	defer cancel()
	f := &filesSearch{
		limit: limit,
		found: make([][]string, len(roots)),
		cut:   make([]bool, len(roots)),
		done:  make([]bool, len(roots)),
		stop:  make([]context.CancelFunc, len(roots)),
	}
	args := make([][]string, len(roots))
	rootCtx := make([]context.Context, len(roots))
	for i, root := range roots {
		var ok bool
		args[i], ok = filesArgs(root, q)
		f.done[i] = !ok
		rootCtx[i], f.stop[i] = context.WithCancel(ctx)
	}
	var wg sync.WaitGroup
	for i, root := range roots {
		if f.done[i] {
			continue
		}
		wg.Go(func() {
			found, cut := s.filesRoot(rootCtx[i], root, args[i], limit)
			f.finish(i, found, cut)
		})
	}
	wg.Wait()

	out := make([]string, 0, limit)
	if deadlineCut(ctx, f.partial()) {
		out = append(out, partialNotice()+": anchor the query to the folder to search, such as ^src/**/, and set no exclude, to search it whole")
	}
	for _, found := range f.found {
		for _, abs := range found {
			if len(out) == limit {
				return out
			}
			out = append(out, displayPath(abs, s.shown(roots)))
		}
	}
	return out
}

// filesSearch is one Files call's per-root state, shared by its root goroutines. Files shows the
// roots' files in root order, so once the finished leading roots hold limit files no later root's
// file can be shown.
type filesSearch struct {
	found [][]string
	cut   []bool
	done  []bool
	stop  []context.CancelFunc
	limit int
	mu    sync.Mutex
}

// finish records root i's result and stops every later root once the leading roots fill limit.
func (f *filesSearch) finish(i int, found []string, cut bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.found[i], f.cut[i], f.done[i] = found, cut, true
	n := 0
	for j, done := range f.done {
		if !done {
			return
		}
		if n += len(f.found[j]); n >= f.limit {
			for _, stop := range f.stop[j+1:] {
				stop()
			}
			return
		}
	}
}

// partial reports whether a root rg did not finish could have shown a file: the roots before it
// held fewer than limit files. Call it after every root finished.
func (f *filesSearch) partial() bool {
	n := 0
	for i, found := range f.found {
		if n >= f.limit {
			return false
		}
		if f.cut[i] {
			return true
		}
		n += len(found)
	}
	return false
}

// filesArgs is rg's command line listing root's files for q; ok is false when no file under root
// can match. KAS's own rg lists at most maxFileDepth levels below the root and passes no --glob
// for "**/*".
func filesArgs(root string, q FilesQuery) (args []string, ok bool) {
	glob := rootGlob(root, q.Pattern)
	dir, depth, ok := startDir(root, glob, q.Exclude != "")
	if !ok || depth >= maxFileDepth {
		return nil, false
	}
	args = []string{"--files", "--max-depth", strconv.Itoa(maxFileDepth - depth), "--hidden", "--no-require-git", "--glob", "!.git"}
	if glob != "" && glob != "**/*" {
		args = append(args, "--glob", glob)
	}
	if exclude := rootGlob(root, q.Exclude); exclude != "" {
		args = append(args, "--glob", "!"+exclude)
	}
	return runnable(append(args, "--", dir))
}

func (s Searcher) filesRoot(ctx context.Context, root string, args []string, limit int) (found []string, cut bool) {
	refusal, cut := s.run(ctx, root, args, func(line []byte) bool {
		if len(line) > 0 {
			found = append(found, filepath.Join(root, string(line)))
		}
		return len(found) < limit
	})
	switch {
	case strings.Contains(refusal, "error parsing glob"):
		logRefusal(root, refusal)
	case refusal != "":
		// A --files walk that finds no file writes nothing, so this exit 2 can be a walk that met
		// unreadable paths; --json always ends with a summary line, so Text never gets here.
		slog.Debug("search: ripgrep found no file and skipped unreadable paths", "root", root,
			"errors", strings.Count(refusal, "\n"))
	}
	return found, cut
}

// run streams rg's stdout lines, under root, to line until it returns false, ctx ends, or the
// output passes maxOutputBytes, then kills rg's session. It returns rg's stderr when rg
// exited 2 having written no line, and cut when ctx ended before rg finished; every other
// failure is logged here. The lines already read stay with the caller.
//
// ctx bounds the whole run: its end SIGKILLs rg's session, and cmd.WaitDelay (killWait) then closes
// the output pipe a process outside the session still holds, which is what ends readLines.
func (s Searcher) run(ctx context.Context, root string, args []string, line func([]byte) bool) (refusal string, cut bool) {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(runCtx, s.Bin, args...) //nolint:gosec // G204: Bin is the image's rg (systembin); request fields are arguments after rg's own flags, never a shell
	cmd.Dir = root
	// An empty environment keeps RIPGREP_CONFIG_PATH, which an agent can set, from adding --follow
	// or other flags to the search.
	cmd.Env = []string{}
	procgroup.OwnSession(cmd, killWait)
	stderr := procout.NewBuffer(maxStderrBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		if ctx.Err() != nil {
			slog.Debug("search: deadline or cancel reached before ripgrep started", "root", root, "reason", context.Cause(ctx))
			return "", true
		}
		slog.Warn("search: ripgrep could not be started", "error", err)
		return "", false
	}
	out := &io.LimitedReader{R: stdout, N: maxOutputBytes}
	read := readLines(out, line)
	read.capped = out.N <= 0
	if read.stopped || read.err != nil || read.capped {
		// A full pipe would block rg forever once the reads stop, so the session is killed before Wait.
		// After a natural EOF rg is exiting, and a kill then would turn a clean exit into a failure.
		stop()
	}
	err = cmd.Wait()
	if read.skipped > 0 {
		slog.Debug("search: skipped ripgrep output lines over the line cap", "root", root, "lines", read.skipped)
	}
	cut = err != nil && ctx.Err() != nil && !read.stopped && !read.capped
	return exitRefusal(ctx, root, err, read, stderr.String()), cut
}

// exitRefusal logs how rg's run ended, and returns its stderr when rg exited 2 having written no
// line.
func exitRefusal(ctx context.Context, root string, err error, read readResult, stderr string) string {
	code := -1
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	}
	switch {
	case err == nil:
	case ctx.Err() != nil:
		slog.Debug("search: deadline or cancel reached; returning partial results", "root", root, "reason", context.Cause(ctx))
	case read.stopped:
	case read.err != nil:
		slog.Debug("search: reading ripgrep's output failed; returning what it found", "root", root, "error", read.err)
	case read.capped:
		slog.Debug("search: ripgrep's output reached its cap; returning what it found", "root", root)
	case code == 1:
	case code == 2 && read.lines+read.skipped == 0:
		return cmp.Or(stderr, "(no stderr)")
	case code == 2:
		// rg's exit 2 after output is "some paths were unreadable", with every other result valid.
		// KAS's own text search rejects it and falls back to the walker this package replaces.
		slog.Debug("search: ripgrep skipped unreadable paths; results kept", "root", root,
			"errors", strings.Count(stderr, "\n"))
	default:
		slog.Warn("search: ripgrep failed; returning what it found", "root", root, "error", err)
	}
	return ""
}

type readResult struct {
	err     error
	lines   int
	skipped int
	stopped bool
	capped  bool
}

// readLines calls line with each line of r, without its '\n', until line returns false or r
// ends. A line over maxLineBytes is skipped and reading goes on, which bufio.Scanner cannot do.
func readLines(r io.Reader, line func([]byte) bool) readResult {
	br := bufio.NewReaderSize(r, 64<<10)
	var res readResult
	var buf []byte
	for {
		var tooLong bool
		var err error
		buf, tooLong, err = readLine(br, buf)
		switch {
		case tooLong:
			res.skipped++
		case len(buf) > 0:
			res.lines++
			if !line(bytes.TrimSuffix(buf, []byte("\n"))) {
				res.stopped = true
				return res
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				res.err = err
			}
			return res
		}
	}
}

// readLine reads one line of br into buf, reporting tooLong, with buf emptied, for a line over
// maxLineBytes.
func readLine(br *bufio.Reader, buf []byte) (out []byte, tooLong bool, err error) {
	buf = buf[:0]
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong && len(buf)+len(chunk) > maxLineBytes {
			tooLong, buf = true, buf[:0]
		} else if !tooLong {
			buf = append(buf, chunk...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return buf, tooLong, err
		}
	}
}

// rootGlob is glob in the form rg, run in root, matches it in: against the path relative to root,
// which holds no "." or empty segment. A path under root and a "./" or "/" lead name root's own
// entries, so they become root-anchored ("/rest"); a trailing "/" is kept. "" means all of root.
func rootGlob(root, glob string) string {
	root = filepath.Clean(root)
	if strings.TrimRight(glob, "/") == root {
		return ""
	}
	rest, anchored := strings.CutPrefix(glob, root+"/")
	if !anchored {
		rest = glob
	}
	segs := strings.Split(rest, "/")
	anchored = anchored || segs[0] == "" || segs[0] == "."
	segs = slices.DeleteFunc(segs, func(s string) bool { return s == "" || s == "." })
	if len(segs) == 0 {
		return ""
	}
	glob, rest = strings.Join(segs, "/"), glob
	if anchored {
		glob = "/" + glob
	}
	if strings.HasSuffix(rest, "/") {
		glob += "/"
	}
	return glob
}

// literal reports whether s holds no glob metacharacter.
func literal(s string) bool {
	return !strings.ContainsAny(s, `*?[]{}\!`)
}

// startDir is where rg, run in root, starts searching for glob (rootGlob's form): its literal
// leading folders, so rg skips every other subtree (a --glob filters names but still descends every
// folder), with their count; "." when it has none, when it has one segment and no "/" lead (rg
// matches it at any depth), or when excluded, since rg applies ignore rules and an exclude only
// below its start path, never to it or its ancestors. ok is false when no file under root can
// match: a ".." segment, a ".git" folder (the !.git glob), or a leading folder realDir refuses.
func startDir(root, glob string, excluded bool) (dir string, depth int, ok bool) {
	rel, anchored := strings.CutPrefix(strings.TrimSuffix(glob, "/"), "/")
	segs := strings.Split(rel, "/")
	if slices.Contains(segs, "..") {
		return "", 0, false
	}
	if excluded || !anchored && len(segs) == 1 {
		return ".", 0, true
	}
	for depth < len(segs)-1 && literal(segs[depth]) {
		depth++
	}
	if depth == 0 {
		return ".", 0, true
	}
	if slices.Contains(segs[:depth], ".git") {
		return "", 0, false
	}
	dir = filepath.Join(segs[:depth]...)
	if !realDir(root, dir) {
		return "", 0, false
	}
	return dir, depth, true
}

// realDir reports whether rel names a directory under root reached through no symlink: rg follows
// a start path that is a symlink, outside root included. Every stat error refuses it.
func realDir(root, rel string) bool {
	want := filepath.Join(root, rel)
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		return false
	}
	resolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	return err == nil && resolved == filepath.Join(realRoot, rel)
}

// trimLoneQuote drops a trailing double quote that is the pattern's only one, as KAS's zZu does
// for a model that closes a quote it never opened.
func trimLoneQuote(p string) string {
	if strings.HasSuffix(p, `"`) && strings.Count(p, `"`) == 1 {
		return p[:len(p)-1]
	}
	return p
}

// displayPath is KAS 3.0.0's Rar: abs relative to the first root holding it, prefixed with that
// root's base name when the bare relative path would resolve (resolveShown) under another root,
// else abs itself. KAS returns a client's answer unmapped, so the client must map it.
func displayPath(abs string, roots []string) string {
	for _, root := range roots {
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if rel == "." {
			return abs
		}
		for _, shown := range []string{rel, filepath.Base(root) + "/" + rel} {
			if resolveShown(shown, roots) == abs {
				return shown
			}
		}
		return abs
	}
	return abs
}

// resolveShown is KAS 3.0.0's MJ for a relative path: under the one root, else under the root
// whose base name is its first segment, else under the first root.
func resolveShown(rel string, roots []string) string {
	if len(roots) == 1 {
		return filepath.Join(roots[0], rel)
	}
	first, rest, _ := strings.Cut(filepath.Clean(rel), "/")
	for _, r := range roots {
		if filepath.Base(r) == first {
			return filepath.Join(r, rest)
		}
	}
	return filepath.Join(roots[0], rel)
}
