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
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/marotte/internal/procout"
)

// searchDeadline bounds one search across every root; a var so tests can shorten it. Measured on a
// 1.18-million-file workspace (warm cache): a full file listing took 1.2 s and an unscoped text
// search 24.5 s, against KAS's own 30 s rg budget. 15 s returns every scoped search whole and an
// unscoped one partial.
var searchDeadline = 15 * time.Second

// KAS 3.0.0's own caps (kGo, DGo, OGo and UZu in acp-server.js), so an answer holds what KAS's
// own rg would have returned.
const (
	maxTextFiles   = 100
	maxTextEntries = 100
	maxCountArg    = "5"
	maxDepthArg    = "50"
	maxOutputBytes = 10 << 20
)

const (
	// maxLineBytes bounds one rg output line. A --json match event carries every submatch of its
	// line, so --max-filesize does not bound it; a longer line is skipped.
	maxLineBytes   = 4 << 20
	maxStderrBytes = 64 << 10
	// killWait bounds Wait after the group's SIGKILL, for a child still holding the pipes.
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

// Searcher runs the rg binary at Bin, each search within 15 s. An empty Bin finds nothing. Shown
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
// ends or the deadline passes is returned. The result is never nil.
func (s Searcher) Text(ctx context.Context, roots []string, q TextQuery) []Match {
	if s.Bin == "" || q.Pattern == "" || len(roots) == 0 {
		return []Match{}
	}
	ctx, cancel := context.WithTimeout(ctx, searchDeadline)
	defer cancel()
	args := []string{"--json", "--engine", "auto", "-C", "2", "--max-count", maxCountArg, "--max-filesize", "1M", "--hidden", "--no-require-git", "--glob", "!.git"}
	if !q.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if q.IncludePattern != "" {
		args = append(args, "--glob", q.IncludePattern)
	}
	if q.ExcludePattern != "" {
		args = append(args, "--glob", "!"+q.ExcludePattern)
	}
	args = append(args, "--", trimLoneQuote(q.Pattern))

	perRoot := make([][]*fileHits, len(roots))
	var wg sync.WaitGroup
	for i, root := range roots {
		dir, ok := scope(root, q.IncludePattern)
		if !ok {
			continue
		}
		wg.Go(func() {
			perRoot[i] = s.textRoot(ctx, root, append(slices.Clone(args), dir))
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

type fileHits struct {
	abs        string
	entries    []Match
	matchCount int
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

func (s Searcher) textRoot(ctx context.Context, root string, args []string) []*fileHits {
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
	refusal := s.run(ctx, root, args, onLine)
	if refusal != "" && regexRefused(refusal) {
		// KAS 3.0.0's own search escapes a pattern its regex engine rejects and finds the literal.
		refusal = s.run(ctx, root, append([]string{"--fixed-strings"}, args...), onLine)
	}
	logRefusal(root, refusal)
	return order
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
// first, with paths in KAS's form. .gitignore is honoured and symlinks are not followed. Whatever
// was found when ctx ends or the deadline passes is returned. The result is never nil.
func (s Searcher) Files(ctx context.Context, roots []string, q FilesQuery) []string {
	limit := q.MaxResults
	if limit <= 0 {
		limit = defaultMaxFiles
	}
	limit = min(limit, maxFileResults)
	if s.Bin == "" || len(roots) == 0 {
		return []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, searchDeadline)
	defer cancel()
	args := []string{"--files", "--max-depth", maxDepthArg, "--hidden", "--no-require-git", "--glob", "!.git"}
	if q.Pattern != "" && q.Pattern != "**/*" {
		args = append(args, "--glob", q.Pattern)
	}
	if q.Exclude != "" {
		args = append(args, "--glob", "!"+q.Exclude)
	}

	perRoot := make([][]string, len(roots))
	var wg sync.WaitGroup
	for i, root := range roots {
		dir, ok := scope(root, q.Pattern)
		if !ok {
			continue
		}
		wg.Go(func() {
			perRoot[i] = s.filesRoot(ctx, root, append(slices.Clone(args), "--", dir), limit)
		})
	}
	wg.Wait()

	out := make([]string, 0, limit)
	for _, found := range perRoot {
		for _, abs := range found {
			if len(out) == limit {
				return out
			}
			out = append(out, displayPath(abs, s.shown(roots)))
		}
	}
	return out
}

func (s Searcher) filesRoot(ctx context.Context, root string, args []string, limit int) []string {
	var found []string
	refusal := s.run(ctx, root, args, func(line []byte) bool {
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
	return found
}

// run streams rg's stdout lines, under root, to line until it returns false, ctx ends, or the
// output passes maxOutputBytes, then kills rg's process group. It returns rg's stderr when rg
// exited 2 having written no line; every other failure is logged here. The lines already read
// stay with the caller.
func (s Searcher) run(ctx context.Context, root string, args []string, line func([]byte) bool) (refusal string) {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(runCtx, s.Bin, args...) //nolint:gosec // G204: Bin is the image's rg (systembin); request fields are arguments after rg's own flags, never a shell
	cmd.Dir = root
	// An empty environment keeps RIPGREP_CONFIG_PATH, which an agent can set, from adding --follow
	// or other flags to the search.
	cmd.Env = []string{}
	procgroup.OwnGroup(cmd, killWait)
	stderr := procout.NewBuffer(maxStderrBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		if ctx.Err() != nil {
			slog.Debug("search: deadline or cancel reached before ripgrep started", "root", root, "reason", context.Cause(ctx))
		} else {
			slog.Warn("search: ripgrep could not be started", "error", err)
		}
		return ""
	}
	out := &io.LimitedReader{R: stdout, N: maxOutputBytes}
	read := readLines(out, line)
	read.capped = out.N <= 0
	if read.stopped || read.err != nil || read.capped {
		// A full pipe would block rg forever once the reads stop, so the group is killed before Wait.
		// After a natural EOF rg is exiting, and a kill then would turn a clean exit into a failure.
		stop()
	}
	err = cmd.Wait()
	if read.skipped > 0 {
		slog.Debug("search: skipped ripgrep output lines over the line cap", "root", root, "lines", read.skipped)
	}
	return exitRefusal(ctx, root, err, read, stderr.String())
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
		// KAS's own rg path rejects it, which is the walker fallback this package replaces.
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

// scope is the directory, relative to root, rg searches for glob: the glob's leading literal
// directories, so rg skips every other subtree (a --glob filters names but still descends every
// directory). ok is false when no file under root can match: the directory is absent, or reached
// through a symlink, which rg does not follow. A prefix it cannot resolve falls back to all of root.
func scope(root, glob string) (dir string, ok bool) {
	if !strings.Contains(glob, "/") || strings.HasPrefix(glob, "/") {
		return ".", true
	}
	segs := strings.Split(glob, "/")
	var lit []string
	for _, seg := range segs[:len(segs)-1] {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `*?[]{}\!`) {
			break
		}
		lit = append(lit, seg)
	}
	if len(lit) == 0 {
		return ".", true
	}
	rel := filepath.Join(lit...)
	want := filepath.Join(root, rel)
	info, err := os.Stat(want)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(want)
	realRoot, rootErr := filepath.EvalSymlinks(root)
	if err != nil || rootErr != nil {
		return ".", true
	}
	if resolved != filepath.Join(realRoot, rel) {
		return "", false
	}
	return rel, true
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
