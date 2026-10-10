package filebrowse

import (
	"cmp"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

type nameRow struct {
	match  FileMatch
	tier   int
	depth  int
	length int
}

func compareNameRows(a, b *nameRow) int {
	return cmp.Or(
		cmp.Compare(a.tier, b.tier),
		cmp.Compare(a.depth, b.depth),
		cmp.Compare(a.length, b.length),
		strings.Compare(a.match.Path, b.match.Path),
	)
}

// worstFirst is a max-heap of the best rows seen so far: its root is the row the next better
// candidate evicts, so keeping the top K costs O(log K) per candidate.
type worstFirst []nameRow

func (h worstFirst) worse(i, j int) bool { return compareNameRows(&h[i], &h[j]) > 0 }

func (h *worstFirst) push(row *nameRow) {
	*h = append(*h, *row)
	s := *h
	for i := len(s) - 1; i > 0; {
		parent := (i - 1) / 2
		if !s.worse(i, parent) {
			return
		}
		s[i], s[parent] = s[parent], s[i]
		i = parent
	}
}

func (h worstFirst) replaceRoot(row *nameRow) {
	h[0] = *row
	for i := 0; ; {
		worst, l, r := i, 2*i+1, 2*i+2
		if l < len(h) && h.worse(l, worst) {
			worst = l
		}
		if r < len(h) && h.worse(r, worst) {
			worst = r
		}
		if worst == i {
			return
		}
		h[i], h[worst] = h[worst], h[i]
		i = worst
	}
}

// nameVisitor ranks matching names over the WHOLE walk and keeps the best maxSearchMatches, so no
// cut is ever taken in walk order. It opens no file.
type nameVisitor struct {
	query   nameQuery
	top     worstFirst
	matched int
	scanned int
}

func newNameVisitor(q nameQuery) *nameVisitor {
	return &nameVisitor{query: q}
}

func (v *nameVisitor) file(_ *walkDir, e *walkEntry) bool {
	v.scanned++
	if e.inFilter {
		v.consider(e, MatchKindName)
	}
	return true
}

// dir offers a directory as a row only beside a query: a bare filter lists files, as `fd -t f`
// does.
func (v *nameVisitor) dir(_ *walkDir, e *walkEntry) bool {
	v.scanned++
	if e.inFilter && !v.query.empty() {
		v.consider(e, MatchKindDir)
	}
	return true
}

func (v *nameVisitor) consider(e *walkEntry, kind FileMatchKind) {
	tier, ranges, ok := v.query.match(e.sub)
	if !ok {
		return
	}
	v.matched++
	row := nameRow{
		match: FileMatch{Path: e.abs, Kind: kind, Ranges: rangesOrEmpty(ranges)},
		tier:  tier,
		depth: strings.Count(e.srel, "/") + 1,
	}
	if !v.query.empty() {
		row.length = utf8.RuneCountInString(e.name)
	}
	if len(v.top) < maxSearchMatches {
		v.top.push(&row)
		return
	}
	if compareNameRows(&row, &v.top[0]) < 0 {
		v.top.replaceRoot(&row)
	}
}

func (*nameVisitor) leave(*walkDir) {}

func (*nameVisitor) full() bool { return false }

// rootFile answers nothing: a root is the frame of a search, never a result inside it.
func (*nameVisitor) rootFile(*os.File, string, os.FileInfo) bool { return true }

func (v *nameVisitor) result(truncated bool) FileSearchResult {
	rows := slices.Clone(v.top)
	slices.SortFunc(rows, func(a, b nameRow) int { return compareNameRows(&a, &b) })
	matches := make([]FileMatch, 0, len(rows))
	for i := range rows {
		matches = append(matches, rows[i].match)
	}
	res := FileSearchResult{Matches: matches}
	res.Scanned, res.Matched, res.Truncated = v.scanned, v.matched, truncated
	return res
}
