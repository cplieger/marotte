// Recursive file search (GET /api/files/search), names ahead of contents, lexical and index-free on
// the textsearch kernel. Confinement is inherited from the resolved ROOT: every open is one name
// against the parent's descriptor with O_NOFOLLOW, Sensitive.Blocks runs on every entry, and
// resolvePath is NOT re-run per entry, since EvalSymlinks could re-root it.

package filebrowse

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/parallel"
	"github.com/cplieger/marotte/internal/textsearch"
	"github.com/cplieger/webhttp/v3"
)

const (
	// maxSearchFiles bounds how many files one search opens. A source tree is
	// many small files, so the file budget is larger and the per-file budget
	// smaller than the cross-chat search's.
	maxSearchFiles = 5000
	// maxSearchDirs bounds directories VISITED, which maxSearchFiles does not:
	// a tree of empty directories costs one ReadDir each and would otherwise
	// walk forever without ever filling the file budget.
	maxSearchDirs = 20_000
	// maxSearchDepth bounds how deep the walk descends: a directory handle
	// stays open for as long as the walk is inside it, so depth IS the number
	// of directory handles one search holds at once.
	maxSearchDepth = 128
	// maxSearchMatches caps the response: past this a search is a listing the
	// reader will refine rather than page through.
	maxSearchMatches = 200
	// maxFileMatches caps ONE file's contribution, so a generated or minified
	// file cannot spend the whole match budget before the reader's own code is
	// reached.
	maxFileMatches = 20
	// searchReadBudget bounds the bytes the read fan-out can hold at once, so
	// searchWorkers full reads never exceed it.
	searchReadBudget = 4 << 20
	// maxSearchFileSize is how much of ONE file is read; past it the head is still searched and
	// Tally.Truncated says so. A hit straddling the cut is lost.
	maxSearchFileSize = searchReadBudget / searchWorkers
	// searchExcerptRadius is how much of a long line surrounds the match. A
	// minified bundle is one line, so the excerpt has to be windowed even
	// though the unit is a line.
	searchExcerptRadius = 80
	// searchReadDirChunk bounds one ReadDir call's allocation and gives cancellation somewhere to
	// land; atomicfile.WalkDirInRoot's batch size.
	searchReadDirChunk = 256
)

// searchWorkers matches chat.searchWorkers: the bound is disk, not CPU.
const searchWorkers = 8

// errSearchBinary marks a candidate rejected by the binary sniff. It is not a
// failure and never logged: a match inside a binary is noise the reader cannot
// act on, so the file is opened, sniffed and dropped.
var errSearchBinary = errors.New("filebrowse: binary file")

// FileMatchKind is what matched. A defined type with constants rather than a
// bare string, because the client branches on it.
type FileMatchKind string

const (
	// MatchKindContent is a matching LINE: Line >= 1 and Excerpt is that line.
	MatchKindContent FileMatchKind = "content"
	// MatchKindName is a matching FILE name. Line is 0 and Excerpt is empty.
	MatchKindName FileMatchKind = "name"
	// MatchKindDir is a matching DIRECTORY name. Line 0, Excerpt empty.
	MatchKindDir FileMatchKind = "dir"
)

// FileMatch is one hit: a matching LINE, or an entry whose NAME matched. A line
// number rather than a byte offset, because the client opens a content result at the
// editor's `/file/{path}#L<line>`; a name hit carries Line 0, which matchLines never
// produces.
type FileMatch struct {
	// Path is the container-absolute path, the same namespace every other
	// /api/file* route speaks.
	Path    string        `json:"path"`
	Excerpt string        `json:"excerpt"`
	Kind    FileMatchKind `json:"kind"`
	Line    int           `json:"line"`
}

// FileSearchResult is GET /api/files/search's reply: the hits, cut at
// maxSearchMatches, beside the tally over the files the walk read.
type FileSearchResult struct {
	Matches []FileMatch `json:"matches"`
	textsearch.Tally
}

// --- /api/files/search (GET recursive content search) ---

func (h *Handler) handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	q := r.URL.Query()
	needle := q.Get("q")
	if strings.TrimSpace(needle) == "" {
		webhttp.WriteJSON(w, FileSearchResult{Matches: []FileMatch{}})
		return
	}
	include, err := parseGlobs(q["include"])
	if err != nil {
		httpreply.BadRequest(w, "invalid include pattern")
		return
	}
	exclude, err := parseGlobs(q["exclude"])
	if err != nil {
		httpreply.BadRequest(w, "invalid exclude pattern")
		return
	}
	roots, ok := h.searchRoots(w, q.Get("path"))
	if !ok {
		return
	}
	// The exact case rule the transcript search applies, so the two checkboxes agree.
	sc := newFileScan(r.Context(), needle, q.Get("case") == "1", include, exclude, h.sensitive)
	for _, root := range roots {
		if !sc.addRoot(root) {
			break
		}
	}
	res := sc.results()
	// A cancelled request gets no body: the client is gone, and a half-scan
	// reported as a whole one is the lie the caps exist to prevent.
	if r.Context().Err() != nil {
		return
	}
	webhttp.WriteJSON(w, res)
}

// searchRoots resolves the request's `path` into the locations to walk.
//
// "/" is not a real directory in the allow-list model, so it fans out over every
// granted mount: "search everything" is the honest reading of the root listing,
// and the caps are shared across the fan-out rather than multiplied by it.
func (h *Handler) searchRoots(w http.ResponseWriter, reqPath string) (roots []loc, ok bool) {
	if reqPath == "" || reqPath == "." || filepath.Clean("/"+reqPath) == "/" {
		roots = make([]loc, 0, len(h.mounts))
		for i := range h.mounts {
			m := &h.mounts[i]
			roots = append(roots, loc{m: m, abs: m.dir})
		}
		return roots, true
	}
	l, resolved := h.resolveOrForbid(w, reqPath)
	if !resolved {
		return nil, false
	}
	return []loc{l}, true
}

// parseGlobs flattens repeated and comma-separated pattern parameters and rejects a malformed one,
// because path.Match answers "no match" for a bad pattern and the search would silently find
// nothing.
func parseGlobs(raw []string) (patterns []string, err error) {
	for _, entry := range raw {
		for pat := range strings.SplitSeq(entry, ",") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if _, mErr := path.Match(pat, ""); mErr != nil {
				return nil, mErr
			}
			patterns = append(patterns, pat)
		}
	}
	return patterns, nil
}

// matchGlob applies ONE pattern: without "/" it matches the BASENAME (path.Match's `*` does not
// cross "/"), with "/" the path relative to the SEARCH ROOT. No `**`.
func matchGlob(pattern, rel string) bool {
	subject := rel
	if !strings.Contains(pattern, "/") {
		subject = path.Base(rel)
	}
	matched, err := path.Match(pattern, subject)
	return err == nil && matched
}

func matchAnyGlob(patterns []string, rel string) bool {
	for _, pat := range patterns {
		if matchGlob(pat, rel) {
			return true
		}
	}
	return false
}

// searchDir is one directory the walk is inside: the open handle that PINS it, against which every
// child is opened by name so no ancestor swap can redirect a read, plus its children's two
// coordinate spaces. One descriptor per level, bounded by maxSearchDepth.
type searchDir struct {
	f *os.File
	// abs is the container-absolute path, for the response and the denylist.
	abs string
	// srel is the path relative to the SEARCH ROOT ("" at the root), which is
	// what the globs are matched against.
	srel string
	// depth is 0 at the search root and counts the open handles held.
	depth int
}

// child names one entry of d in both coordinate spaces.
func (d searchDir) child(name string) (abs, srel string) {
	return filepath.Join(d.abs, name), path.Join(d.srel, name)
}

// searchCandidate is one file the walk accepted, named RELATIVE to the directory
// handle it was found in — a name is all the read needs and all it is allowed to
// use, because the handle is what confines it.
type searchCandidate struct {
	name string
	abs  string
}

// fileScan carries one search's accounting. Every field is owned by the WALK goroutine; the read
// fan-out collects into a pre-sized slice by index, so the scan needs no mutex or atomic.
type fileScan struct {
	ctx       context.Context
	include   []string
	exclude   []string
	matches   []FileMatch
	sensitive Sensitive
	// needle is the one kernel Needle both the name test and the line scan run,
	// so the two cannot disagree about the fold.
	needle textsearch.Needle
	// files is Tally.Scanned, in files.
	files int
	// accepted is what the maxSearchFiles budget counts: a candidate at its
	// admission, which is before its read can refuse it.
	accepted int
	dirs     int
	// matched is Tally.Matched, in matching lines and names.
	matched int
	// truncated is Tally.Truncated, set only where a unit went unread: a budget's
	// refusal, a failed read, or the entry a full reply stops the walk at.
	truncated bool
}

func newFileScan(ctx context.Context, needle string, caseSensitive bool, include, exclude []string, sensitive Sensitive) *fileScan {
	return &fileScan{
		ctx:       ctx,
		sensitive: sensitive,
		needle:    textsearch.NewNeedle(needle, caseSensitive),
		include:   include,
		exclude:   exclude,
	}
}

// nameMatches reports whether an entry's name contains the needle, under the scan's
// own case rule.
func (s *fileScan) nameMatches(name string) bool {
	return s.needle.Contains(name)
}

// nameFirst is the ranking class: a NAME or DIRECTORY hit sorts ahead of a CONTENT
// hit. Two classes rather than three, so a directory and a file share rank and then
// sort by path, which puts a directory ahead of its own children for free.
func nameFirst(k FileMatchKind) int {
	if k == MatchKindContent {
		return 1
	}
	return 0
}

// noteNameMatch records a NAME hit for an entry classify admitted, so excluded and sensitive paths
// never surface as name rows. The include gate is re-applied for a directory (classify skips it so
// includes cannot prune the walk), before the file-budget check, so a budget-refused candidate
// keeps its name row.
func (s *fileScan) noteNameMatch(d searchDir, name string, isDir bool) {
	if !s.nameMatches(name) {
		return
	}
	abs, srel := d.child(name)
	kind := MatchKindName
	if isDir {
		kind = MatchKindDir
		if len(s.include) > 0 && !matchAnyGlob(s.include, srel) {
			return
		}
	}
	s.collect([]FileMatch{{Path: abs, Kind: kind}}, 1)
}

// capped reports whether the walk stops at the entry in hand: the request was cancelled or the
// reply (which counts ROWS) is full, which marks the answer truncated.
func (s *fileScan) capped() bool {
	if s.ctx.Err() != nil {
		return true
	}
	if len(s.matches) >= maxSearchMatches {
		s.truncated = true
		return true
	}
	return false
}

// addRoot enqueues one search root and everything under it. Returns false to
// stop the whole scan.
func (s *fileScan) addRoot(l loc) bool {
	f, err := openPinnedRoot(l)
	if err != nil {
		// A root that cannot be opened contributes nothing, and on the "/" fan-out
		// the other mounts still answer — so the reply would otherwise present a
		// scan of some mounts as a scan of all of them.
		s.truncated = true
		slog.Warn("filebrowse: search open failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return true
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		s.truncated = true
		slog.Warn("filebrowse: search stat failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		return true
	}
	if !info.IsDir() {
		defer func() { _ = f.Close() }()
		return s.searchRootFile(f, l.abs, info)
	}
	return s.walkDir(searchDir{f: f, abs: l.abs})
}

// searchRootFile searches a root that turned out to be a file, from the
// descriptor openPinnedRoot already holds — so the reopen this walk exists to
// avoid does not sneak back in at the root.
func (s *fileScan) searchRootFile(f *os.File, abs string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return true
	}
	if s.accepted >= maxSearchFiles {
		s.truncated = true
		return false
	}
	s.accepted++
	s.fold(s.readHits(f, abs, info.Size()))
	return true
}

// walkDir consumes an already-open directory handle in chunks, reading each chunk's candidates and
// descending its subdirectories, and closes it after everything opened against it. A directory past
// the budget is refused before it is listed. Order is per chunk only, so which files a spent budget
// included is not stable on a large directory.
func (s *fileScan) walkDir(d searchDir) bool {
	defer func() { _ = d.f.Close() }()
	if s.dirs >= maxSearchDirs {
		s.truncated = true
		return false
	}
	s.dirs++
	for {
		if s.ctx.Err() != nil {
			return false
		}
		entries, err := d.f.ReadDir(searchReadDirChunk)
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		if !s.consumeChunk(d, entries) {
			return false
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// The chunk in hand was consumed, but the REST of this directory was
				// never enumerated, so entries the search would have matched are
				// unaccounted for. EOF is the ordinary end and says nothing.
				s.truncated = true
				slog.Warn("filebrowse: search readdir failed", "path", logsafe.Field(d.abs), "error", logsafe.Field(err.Error()))
			}
			return true
		}
		if len(entries) == 0 {
			return true
		}
	}
}

// consumeChunk gates one chunk of entries, then reads the accepted files before descending, so the
// fan-out does not compete with a descent for descriptors.
func (s *fileScan) consumeChunk(d searchDir, entries []fs.DirEntry) bool {
	var cands []searchCandidate
	var subdirs []string
	for _, e := range entries {
		if s.capped() {
			s.readChunk(d, cands)
			return false
		}
		verdict, name := s.classify(d, e)
		if verdict != entrySkip {
			s.noteNameMatch(d, name, verdict == entryDir)
		}
		switch verdict {
		case entryCandidate:
			if s.accepted >= maxSearchFiles {
				// Checking BEFORE the accept is what makes `truncated` exact: it
				// is set only when there really was one more file to read.
				s.truncated = true
				s.readChunk(d, cands)
				return false
			}
			s.accepted++
			abs, _ := d.child(name)
			cands = append(cands, searchCandidate{name: name, abs: abs})
		case entryDir:
			subdirs = append(subdirs, name)
		case entrySkip, entryName:
		}
	}
	s.readChunk(d, cands)
	for _, name := range subdirs {
		if !s.descend(d, name) {
			return false
		}
	}
	return true
}

// entryVerdict is what the gates decided about one directory entry.
type entryVerdict int

const (
	// entrySkip is an entry the search must not show: excluded, or sensitive.
	entrySkip entryVerdict = iota
	// entryCandidate is a regular file whose bytes are read.
	entryCandidate
	// entryDir is a directory the walk descends into.
	entryDir
	// entryName is an entry the listing shows but the search never opens — a
	// symlink, a FIFO, a device, a socket — so only its name can match.
	entryName
)

// classify applies every gate to one entry: sensitive-path denial, globs, then entry type. A
// symlink is never followed (a directory link would cycle, a file link duplicate a result) but its
// name still matches. Size is not a gate here: DirEntry.Info would lstat the PATHNAME;
// readSearchFile bounds the read off the descriptor.
func (s *fileScan) classify(d searchDir, e fs.DirEntry) (verdict entryVerdict, name string) {
	name = e.Name()
	abs, srel := d.child(name)
	if s.sensitive.Blocks(abs) {
		return entrySkip, name
	}
	if e.IsDir() {
		// An exclude prunes directories too (`exclude=node_modules`); an include stays files-only
		// or it would prune every directory.
		if matchAnyGlob(s.exclude, srel) {
			return entrySkip, name
		}
		return entryDir, name
	}
	if matchAnyGlob(s.exclude, srel) {
		return entrySkip, name
	}
	if len(s.include) > 0 && !matchAnyGlob(s.include, srel) {
		return entrySkip, name
	}
	if !e.Type().IsRegular() {
		return entryName, name
	}
	return entryCandidate, name
}

// descend opens one subdirectory against its parent's handle and walks it.
func (s *fileScan) descend(d searchDir, name string) bool {
	abs, srel := d.child(name)
	if d.depth+1 > maxSearchDepth {
		// Refusing to descend leaves files unread, which is what Truncated says.
		s.truncated = true
		slog.Debug("filebrowse: search depth cap reached", "path", logsafe.Field(abs))
		return true
	}
	f, err := openChild(d.f, name, abs, pinnedDirFlags)
	if err != nil {
		// A directory that vanished or was swapped under the walk is normal; the refusal is the
		// guarantee. Anything else lost the whole subtree, which Truncated reports.
		if !errors.Is(err, fs.ErrNotExist) && !isSwapRefusal(err) {
			s.truncated = true
			slog.Warn("filebrowse: search opendir failed", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
		}
		return true
	}
	return s.walkDir(searchDir{f: f, abs: abs, srel: srel, depth: d.depth + 1})
}

// readChunk reads one chunk's candidates in parallel (the bound is disk, as in chat.SearchAll),
// each worker into its own slot of a pre-sized slice. Every open uses d's descriptor, so the
// fan-out must finish before walkDir closes it.
func (s *fileScan) readChunk(d searchDir, cands []searchCandidate) {
	if len(cands) == 0 {
		return
	}
	per := make([]candidateRead, len(cands))
	parallel.Bounded(s.ctx, cands, searchWorkers, func(i int, c searchCandidate) {
		per[i] = s.readCandidate(d.f, c)
	})
	for i := range per {
		s.fold(per[i])
	}
}

// candidateRead is one candidate's outcome, carried back by index rather than
// folded in by the worker: the tally is the walk goroutine's to write — which is
// what keeps the fan-out free of a mutex.
type candidateRead struct {
	hits []FileMatch
	// matched is every matching line, counted past the per-file cap.
	matched int
	// unread says the file was left UNREAD for a reason the walk did not choose,
	// so the answer has a hole; partial says it was read only to its ceiling.
	unread  bool
	partial bool
}

// fold applies one read's outcome to the tally. An unread file was never
// scanned; a partial one was, and both leave the answer incomplete.
func (s *fileScan) fold(r candidateRead) {
	s.collect(r.hits, r.matched)
	if r.unread {
		s.truncated = true
		return
	}
	s.files++
	if r.partial {
		s.truncated = true
	}
}

// collect folds one entry's hits into the reply: a file's content hits with the
// count they were cut from, or a single NAME hit for a file or a directory.
func (s *fileScan) collect(hits []FileMatch, matched int) {
	s.matches = append(s.matches, hits...)
	s.matched += matched
}

// readCandidate opens one candidate against its directory's descriptor and takes the file's shape
// off THAT descriptor; a refusal there is the kernel keeping the walk's classification honest.
func (s *fileScan) readCandidate(dir *os.File, c searchCandidate) candidateRead {
	f, err := openChild(dir, c.name, c.abs, pinnedFileFlags)
	if err != nil {
		return candidateRead{unread: logSearchReadError(c.abs, err)}
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return candidateRead{unread: logSearchReadError(c.abs, err)}
	}
	if !info.Mode().IsRegular() {
		err = fmt.Errorf("%w: %s (type %s)", atomicfile.ErrNotRegular, c.abs, info.Mode().Type())
		return candidateRead{unread: logSearchReadError(c.abs, err)}
	}
	return s.readHits(f, c.abs, info.Size())
}

// readHits reads one open regular file and scans it. A binary is not a loss —
// it holds no lines to report, so the answer covers it — and neither is a file
// read to its ceiling, which is reported as partial rather than dropped.
func (s *fileScan) readHits(f *os.File, abs string, size int64) candidateRead {
	data, partial, err := readSearchFile(s.ctx, f, size)
	switch {
	case errors.Is(err, errSearchBinary):
		return candidateRead{}
	case err != nil:
		return candidateRead{unread: logSearchReadError(abs, err)}
	}
	hits, matched := s.matchLines(abs, data)
	return candidateRead{hits: hits, matched: matched, partial: partial}
}

// readSearchFile reads one candidate from an OPEN descriptor: the binary sniff prefix first, the
// rest only for text, at most maxSearchFileSize in all; partial reports that more was there. A
// binary costs the sniff window, not the per-file ceiling.
func readSearchFile(ctx context.Context, f *os.File, size int64) (data string, partial bool, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	sniff := make([]byte, min(int64(binarySniffN), size))
	n, err := io.ReadFull(f, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false, err
	}
	// Sliced to what was actually read: ReadFull leaves the tail of a short read
	// zeroed, and a zero byte is exactly what looksBinary looks for.
	sniff = sniff[:n]
	if looksBinary(sniff) {
		return "", false, errSearchBinary
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	// The rest from the same descriptor into one buffer; one byte past the ceiling says the file
	// was cut and is not scanned.
	var text strings.Builder
	text.Grow(int(min(size, maxSearchFileSize)) + 1)
	text.Write(sniff)
	if _, copyErr := io.Copy(&text, io.LimitReader(f, maxSearchFileSize-int64(n)+1)); copyErr != nil {
		return "", false, copyErr
	}
	data = text.String()
	if len(data) > maxSearchFileSize {
		return data[:maxSearchFileSize], true, nil
	}
	return data, false, nil
}

// results folds the walk's hits into the reply.
func (s *fileScan) results() FileSearchResult {
	flat := s.matches
	slices.SortFunc(flat, func(a, b FileMatch) int {
		return cmp.Or(
			// 0 for a name or directory hit, 1 for a content hit.
			cmp.Compare(nameFirst(a.Kind), nameFirst(b.Kind)),
			strings.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
		)
	})
	// Rank-aware: cutting the sorted tail can leave name rows only, and Matched > len(Matches) says
	// so; a quota would drop a higher-ranked row.
	if len(flat) > maxSearchMatches {
		flat = flat[:maxSearchMatches]
	}
	if flat == nil {
		// A nil slice serialises as JSON null, which the client must not have to
		// narrow.
		flat = []FileMatch{}
	}
	return FileSearchResult{
		Matches:   flat,
		Scanned:   s.files,
		Matched:   s.matched,
		Truncated: s.truncated,
	}
}

// logSearchReadError records why a candidate contributed nothing and reports whether that was a
// LOSS (Warn) rather than an expected skip (Debug): ErrNotRegular (FIFOs, devices, /proc specials),
// ELOOP and ENOTDIR from a swapped name, and a vanished file are skips.
func logSearchReadError(abs string, err error) (lost bool) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, atomicfile.ErrNotRegular), isSwapRefusal(err):
		slog.Debug("filebrowse: search skipped file", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
		return false
	default:
		slog.Warn("filebrowse: search read failed", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
		return true
	}
}

// matchLines finds every matching line in one file's text: the rows, cut at
// maxFileMatches, and the count they were cut from.
func (s *fileScan) matchLines(abs, data string) (hits []FileMatch, matched int) {
	line := 0
	for rest := data; rest != ""; {
		seg := rest
		if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
			seg, rest = rest[:idx], rest[idx+1:]
		} else {
			rest = ""
		}
		line++
		hit, ok := firstHit(s.needle, seg)
		if !ok {
			continue
		}
		matched++
		if len(hits) < maxFileMatches {
			hits = append(hits, FileMatch{
				Path: abs, Excerpt: excerptLine(seg, hit.Rune), Kind: MatchKindContent, Line: line,
			})
		}
	}
	return hits, matched
}

// firstHit is the first occurrence of needle in text, if any.
func firstHit(needle textsearch.Needle, text string) (textsearch.Hit, bool) {
	for hit := range needle.Occurrences(text) {
		return hit, true
	}
	return textsearch.Hit{}, false
}

// excerptLine renders one matching line: a CRLF's CR dropped, and a long line windowed around the
// match at its RUNE index so a fold cannot move it off the hit; the ellipsis is U+2026.
func excerptLine(seg string, hitRune int) string {
	runes := []rune(strings.TrimRight(seg, "\r"))
	start := max(hitRune-searchExcerptRadius, 0)
	end := min(hitRune+searchExcerptRadius, len(runes))
	var b strings.Builder
	if start > 0 {
		b.WriteString("\u2026")
	}
	b.WriteString(strings.TrimSpace(string(runes[start:end])))
	if end < len(runes) {
		b.WriteString("\u2026")
	}
	return b.String()
}
