package filebrowse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"unicode"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/parallel"
	"github.com/cplieger/marotte/internal/textsearch"
)

const (
	// maxSearchFiles bounds how many files one contents search opens. A source tree is many small
	// files, so the file budget is larger and the per-file budget smaller than the cross-chat
	// search's.
	maxSearchFiles = 5000
	// maxFileMatches caps ONE file's contribution, so a generated or minified file cannot spend
	// the whole reply before the reader's own code is reached.
	maxFileMatches = 20
	// searchReadBudget bounds the bytes the read fan-out can hold at once, so searchWorkers full
	// reads never exceed it.
	searchReadBudget = 4 << 20
	// maxSearchFileSize is how much of ONE file is read; past it the head is still searched and
	// Tally.Truncated says so. A hit straddling the cut is lost.
	maxSearchFileSize = searchReadBudget / searchWorkers
	// searchExcerptRadius is how much of a long line surrounds the match. A minified bundle is
	// one line, so the excerpt has to be windowed even though the unit is a line.
	searchExcerptRadius = 80
)

// searchWorkers matches chat.searchWorkers: the bound is disk, not CPU.
const searchWorkers = 8

// errSearchBinary marks a candidate rejected by the binary sniff. It is not a failure and never
// logged: a match inside a binary is noise the reader cannot act on.
var errSearchBinary = errors.New("filebrowse: binary file")

// searchCandidate is one file the walk accepted, named RELATIVE to the directory handle it was
// found in: a name is all the read needs and all it is allowed to use, because the handle is what
// confines it.
type searchCandidate struct {
	name string
	abs  string
}

// contentVisitor reads in the walker's exact path order, flushing before every descent, so the
// reply cut at maxSearchMatches is a true prefix of the path-ordered answer.
type contentVisitor struct {
	ctx     context.Context
	rows    []FileMatch
	pending []searchCandidate
	needle  textsearch.Needle
	// files is Tally.Scanned, in files read.
	files int
	// accepted is what maxSearchFiles counts: a candidate at its admission, before its read can
	// refuse it.
	accepted int
	// matched is Tally.Matched, in matching lines.
	matched   int
	truncated bool
}

// newContentVisitor uses the transcript search's exact case rule, so the two toggles agree.
func newContentVisitor(ctx context.Context, query string, caseSensitive bool) *contentVisitor {
	return &contentVisitor{ctx: ctx, needle: textsearch.NewNeedle(query, caseSensitive)}
}

func (v *contentVisitor) file(d *walkDir, e *walkEntry) bool {
	if !e.inFilter || !e.typ.IsRegular() {
		return true
	}
	if v.accepted >= maxSearchFiles {
		// Checking BEFORE the accept is what makes `truncated` exact: it is set only when there
		// really was one more file to read.
		v.truncated = true
		v.flush(d)
		return false
	}
	v.accepted++
	v.pending = append(v.pending, searchCandidate{name: e.name, abs: e.abs})
	if len(v.pending) >= searchReadDirChunk {
		v.flush(d)
	}
	return true
}

func (v *contentVisitor) dir(d *walkDir, _ *walkEntry) bool {
	v.flush(d)
	return true
}

func (v *contentVisitor) leave(d *walkDir) { v.flush(d) }

func (v *contentVisitor) full() bool { return len(v.rows) >= maxSearchMatches }

// rootFile searches a root that turned out to be a file, from the descriptor openPinnedRoot
// already holds, so the reopen this walk exists to avoid does not sneak back in at the root.
func (v *contentVisitor) rootFile(f *os.File, abs string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return true
	}
	v.accepted++
	v.fold(v.readHits(f, abs, info.Size()))
	return true
}

func (v *contentVisitor) result(truncated bool) FileSearchResult {
	rows := v.rows
	if len(rows) > maxSearchMatches {
		rows = rows[:maxSearchMatches]
	}
	res := FileSearchResult{Matches: matchesOrEmpty(rows)}
	res.Scanned, res.Matched, res.Truncated = v.files, v.matched, truncated || v.truncated
	return res
}

// flush folds in admission order, and every open uses d's descriptor, so it runs before the walk
// descends past d or closes it.
func (v *contentVisitor) flush(d *walkDir) {
	if len(v.pending) == 0 {
		return
	}
	per := make([]candidateRead, len(v.pending))
	// A slot the dead context never dispatched keeps this mark; its zero value would fold as a
	// file read.
	for i := range per {
		per[i].unread = true
	}
	parallel.Bounded(v.ctx, v.pending, searchWorkers, func(i int, c searchCandidate) {
		per[i] = v.readCandidate(d.f, c)
	})
	v.pending = v.pending[:0]
	for i := range per {
		v.fold(per[i])
	}
}

// candidateRead is one candidate's outcome, carried back by index rather than folded in by the
// worker: the tally is the walk goroutine's to write, which keeps the fan-out free of a mutex.
type candidateRead struct {
	hits []FileMatch
	// matched is every matching line, counted past the per-file cap.
	matched int
	// unread says the file was left UNREAD for a reason the walk did not choose, so the answer
	// has a hole; partial says it was read only to its ceiling.
	unread  bool
	partial bool
}

// An unread file was never scanned; a partial one was, and both leave the answer incomplete.
func (v *contentVisitor) fold(r candidateRead) {
	v.rows = append(v.rows, r.hits...)
	v.matched += r.matched
	if r.unread {
		v.truncated = true
		return
	}
	v.files++
	if r.partial {
		v.truncated = true
	}
}

// readCandidate takes the file's shape off the descriptor it opened, so a name swapped since the
// listing is refused there rather than trusted.
func (v *contentVisitor) readCandidate(dir *os.File, c searchCandidate) candidateRead {
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
	return v.readHits(f, c.abs, info.Size())
}

// A binary is not a loss (it holds no lines to report), a file read to its ceiling is partial, and
// a file the dead context left unread is a hole the walk's own stop already reports.
func (v *contentVisitor) readHits(f *os.File, abs string, size int64) candidateRead {
	data, partial, err := readSearchFile(v.ctx, f, size)
	switch {
	case errors.Is(err, errSearchBinary):
		return candidateRead{}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return candidateRead{unread: true}
	case err != nil:
		return candidateRead{unread: logSearchReadError(abs, err)}
	}
	hits, matched := v.matchLines(abs, data)
	return candidateRead{hits: hits, matched: matched, partial: partial}
}

// readSearchFile reads at most maxSearchFileSize, and partial reports that more was there. A binary
// costs the sniff window, not the per-file ceiling.
func readSearchFile(ctx context.Context, f *os.File, size int64) (data string, partial bool, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	sniff := make([]byte, min(int64(binarySniffN), size))
	n, err := io.ReadFull(f, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false, err
	}
	// Sliced to what was actually read: ReadFull leaves the tail of a short read zeroed, and a
	// zero byte is exactly what looksBinary looks for.
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

// matchLines finds every matching line in one file's text: the rows, cut at maxFileMatches, and
// the count they were cut from.
func (v *contentVisitor) matchLines(abs, data string) (hits []FileMatch, matched int) {
	line := 0
	for rest := data; rest != ""; {
		seg := rest
		if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
			seg, rest = rest[:idx], rest[idx+1:]
		} else {
			rest = ""
		}
		line++
		hit, ok := firstHit(v.needle, seg)
		if !ok {
			continue
		}
		matched++
		if len(hits) < maxFileMatches {
			excerpt, ranges := excerptLine(v.needle, seg, hit.Rune)
			hits = append(hits, FileMatch{
				Path: abs, Excerpt: excerpt, Kind: MatchKindContent, Line: line, Ranges: ranges,
			})
		}
	}
	return hits, matched
}

func firstHit(needle textsearch.Needle, text string) (textsearch.Hit, bool) {
	for hit := range needle.Occurrences(text) {
		return hit, true
	}
	return textsearch.Hit{}, false
}

// excerptLine renders one matching line with the UTF-16 ranges of every occurrence it holds whole.
// A CRLF's CR is dropped, and a long line is windowed around the hit at its RUNE index, so a fold
// cannot move the window off it, and the ellipsis is U+2026. Edge whitespace is trimmed only
// outside every occurrence, since a needle may begin, end or consist of it.
func excerptLine(needle textsearch.Needle, seg string, hitRune int) (excerpt string, ranges []MatchRange) {
	runes := []rune(strings.TrimRight(seg, "\r"))
	start := max(hitRune-searchExcerptRadius, 0)
	end := min(hitRune+needle.RuneLen()+searchExcerptRadius, len(runes))
	window := string(runes[start:end])
	var hits []int
	lo := len(window) - len(strings.TrimLeftFunc(window, unicode.IsSpace))
	hi := len(strings.TrimRightFunc(window, unicode.IsSpace))
	for hit := range needle.Occurrences(window) {
		hits = append(hits, hit.Byte)
		lo = min(lo, hit.Byte)
		hi = max(hi, advanceRunes(window, hit.Byte, needle.RuneLen()))
	}
	body := window[lo:max(lo, hi)]
	var b strings.Builder
	if start > 0 {
		b.WriteString("\u2026")
	}
	shift := utf16Len(b.String())
	b.WriteString(body)
	if end < len(runes) {
		b.WriteString("\u2026")
	}
	ranges = make([]MatchRange, 0, len(hits))
	for _, at := range hits {
		r := utf16Range(body, textsearch.Hit{Byte: at - lo}, needle.RuneLen())
		ranges = append(ranges, MatchRange{Start: shift + r.Start, End: shift + r.End})
	}
	return b.String(), ranges
}
