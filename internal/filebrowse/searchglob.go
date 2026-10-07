// Every pattern here is untrusted, the gitignore rules of a cloned repository included, so the
// matcher is linear: one backtrack point per star, one per `**`, and braces expanded at compile
// time under a cap.

package filebrowse

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/textsearch"
)

const (
	// maxFilterItems bounds the patterns in a Files filter and the terms in a name query.
	maxFilterItems = 64
	// maxFilterItemLen bounds one pattern or term, in bytes.
	maxFilterItemLen = 1024
	// maxBraceAlternatives bounds what one pattern's braces expand to, so `{a,b}` repeated cannot
	// multiply into an exponential pattern list.
	maxBraceAlternatives = 64
	// maxBraceDepth bounds how deeply groups nest.
	maxBraceDepth = 4
	// maxBraceSteps bounds one expansion's recursion. A tree whose every group splits in two or
	// more has fewer inner nodes than leaves, so twice the alternatives admits every such pattern.
	maxBraceSteps = 2 * maxBraceAlternatives
)

// errBadPattern is every refusal this grammar makes; the handler answers it with a 400.
var errBadPattern = errors.New("filebrowse: malformed search pattern")

type globTokKind uint8

const (
	tokLit globTokKind = iota
	tokAny
	tokStar
	tokClass
)

type runeRange struct{ lo, hi rune }

type globTok struct {
	class *globClass
	// lit is compared as BYTES, as path.Match does, so an invalid byte only matches itself.
	lit  string
	kind globTokKind
}

type globClass struct {
	ranges []runeRange
	negate bool
}

func (c *globClass) matches(r rune) bool {
	in := false
	for _, rr := range c.ranges {
		if rr.lo <= r && r <= rr.hi {
			in = true
			break
		}
	}
	return in != c.negate
}

type segGlob []globTok

// match is the iterative wildcard match with one backtrack point at the last star, O(len(g) ×
// len(s)). A star retries one BYTE further on, as path.Match does; since `?` takes a whole rune,
// running out of name is then a reason to backtrack, not a verdict.
func (g segGlob) match(s string) bool {
	p, i := 0, 0
	starP, starI := -1, 0
	for {
		switch {
		case p < len(g) && g[p].kind == tokStar:
			starP, starI = p, i
			p++
			continue
		case p < len(g) && i < len(s):
			if n, ok := g[p].step(s[i:]); ok {
				p++
				i += n
				continue
			}
		case p == len(g) && i == len(s):
			return true
		}
		if starP < 0 || starI >= len(s) {
			return false
		}
		starI++
		i = starI
		p = starP + 1
	}
}

// step reports how many bytes of s a non-star token consumes, and false when it does not match.
func (t globTok) step(s string) (n int, ok bool) {
	switch t.kind {
	case tokAny:
		_, w := utf8.DecodeRuneInString(s)
		return w, true
	case tokClass:
		r, w := utf8.DecodeRuneInString(s)
		return w, t.class.matches(r)
	default:
		return len(t.lit), strings.HasPrefix(s, t.lit)
	}
}

// starRun says what a run of unescaped stars inside a segment means.
type starRun uint8

const (
	// starRunIsStar is gitignore(5)'s reading: "Other consecutive asterisks are considered
	// regular asterisks".
	starRunIsStar starRun = iota
	// starRunRefused is the search grammar's: `**` stands only as a whole segment.
	starRunRefused
)

// compileSegment compiles one segment with path.Match's class grammar: `[^…]` or `[!…]` negates,
// a range is `lo-hi`, `\` escapes, and an empty or unclosed class is malformed.
func compileSegment(s string, runs starRun) (segGlob, error) {
	var g segGlob
	for i := 0; i < len(s); {
		switch s[i] {
		case '*':
			switch {
			case len(g) == 0 || g[len(g)-1].kind != tokStar:
				g = append(g, globTok{kind: tokStar})
			case runs == starRunRefused && s != "**":
				return nil, errBadPattern
			}
			i++
		case '?':
			g = append(g, globTok{kind: tokAny})
			i++
		case '[':
			class, n, err := compileClass(s[i+1:])
			if err != nil {
				return nil, err
			}
			g = append(g, globTok{kind: tokClass, class: class})
			i += 1 + n
		case '\\':
			if i+1 >= len(s) {
				return nil, errBadPattern
			}
			_, w := utf8.DecodeRuneInString(s[i+1:])
			g = append(g, globTok{kind: tokLit, lit: s[i+1 : i+1+w]})
			i += 1 + w
		default:
			_, w := utf8.DecodeRuneInString(s[i:])
			g = append(g, globTok{kind: tokLit, lit: s[i : i+w]})
			i += w
		}
	}
	return g, nil
}

// compileClass's count includes the closing `]`.
func compileClass(s string) (*globClass, int, error) {
	c := &globClass{}
	i := 0
	if i < len(s) && (s[i] == '^' || s[i] == '!') {
		c.negate = true
		i++
	}
	for {
		if i < len(s) && s[i] == ']' && len(c.ranges) > 0 {
			return c, i + 1, nil
		}
		lo, n, err := classRune(s[i:])
		if err != nil {
			return nil, 0, err
		}
		i += n
		hi := lo
		if s[i] == '-' {
			hi, n, err = classRune(s[i+1:])
			if err != nil {
				return nil, 0, err
			}
			i += 1 + n
		}
		c.ranges = append(c.ranges, runeRange{lo: lo, hi: hi})
	}
}

// classRune is path.Match's getEsc: one possibly escaped rune, refusing a bare `-` or `]`, an
// invalid byte, and a class that ends without its `]`.
func classRune(s string) (r rune, n int, err error) {
	if s == "" || s[0] == '-' || s[0] == ']' {
		return 0, 0, errBadPattern
	}
	if s[0] == '\\' {
		n = 1
		if len(s) == 1 {
			return 0, 0, errBadPattern
		}
	}
	r, w := utf8.DecodeRuneInString(s[n:])
	if r == utf8.RuneError && w == 1 {
		return 0, 0, errBadPattern
	}
	n += w
	if n >= len(s) {
		return 0, 0, errBadPattern
	}
	return r, n, nil
}

type pathSeg struct {
	glob   segGlob
	double bool
}

// pathGlob matches a "/"-separated path. `**` stands only as a whole segment and matches zero or
// more segments, with the same single backtrack point the segment matcher uses.
type pathGlob []pathSeg

func (g pathGlob) match(segs []string) bool {
	p, i := 0, 0
	starP, starI := -1, 0
	for i < len(segs) {
		if p < len(g) && g[p].double {
			starP, starI = p, i
			p++
			continue
		}
		if p < len(g) && g[p].glob.match(segs[i]) {
			p++
			i++
			continue
		}
		if starP < 0 {
			return false
		}
		starI++
		i = starI
		p = starP + 1
	}
	for p < len(g) && g[p].double {
		p++
	}
	return p == len(g)
}

func compilePath(s string, runs starRun) (pathGlob, error) {
	parts := strings.Split(s, "/")
	g := make(pathGlob, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, errBadPattern
		}
		if part == "**" {
			if len(g) == 0 || !g[len(g)-1].double {
				g = append(g, pathSeg{double: true})
			}
			continue
		}
		seg, err := compileSegment(part, runs)
		if err != nil {
			return nil, err
		}
		g = append(g, pathSeg{glob: seg})
	}
	return g, nil
}

func expandBraces(p string) ([]string, error) {
	e := braceExpansion{out: []string{}}
	if err := e.expand(p); err != nil {
		return nil, err
	}
	return e.out, nil
}

type braceExpansion struct {
	out   []string
	steps int
}

func (e *braceExpansion) expand(p string) error {
	e.steps++
	if e.steps > maxBraceSteps {
		return errBadPattern
	}
	open := indexUnescaped(p, '{')
	if open < 0 {
		if len(e.out) >= maxBraceAlternatives {
			return errBadPattern
		}
		e.out = append(e.out, p)
		return nil
	}
	alts, end, err := braceAlternatives(p[open+1:])
	if err != nil {
		return err
	}
	prefix, suffix := p[:open], p[open+1+end:]
	for _, alt := range alts {
		if err := e.expand(prefix + alt + suffix); err != nil {
			return err
		}
	}
	return nil
}

// braceAlternatives's count includes the closing `}`.
func braceAlternatives(s string) (alts []string, n int, err error) {
	nest, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			nest++
			if nest >= maxBraceDepth {
				return nil, 0, errBadPattern
			}
		case '}':
			if nest == 0 {
				return append(alts, s[start:i]), i + 1, nil
			}
			nest--
		case ',':
			if nest == 0 {
				alts = append(alts, s[start:i])
				start = i + 1
			}
		}
	}
	return nil, 0, errBadPattern
}

func indexUnescaped(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case c:
			return i
		}
	}
	return -1
}

func splitTopLevelCommas(s string) []string {
	var parts []string
	nest, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			nest++
		case '}':
			if nest > 0 {
				nest--
			}
		case ',':
			if nest == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[{\\")
}

// entrySubject is one directory entry as the patterns see it. Under a case-insensitive search the
// FOLDED forms are compared, folded once per entry rather than once per pattern.
type entrySubject struct {
	name  string
	srel  string
	fname string
	fsrel string
	segs  []string
	isDir bool
}

func newEntrySubject(name, srel string, isDir, caseSensitive bool) *entrySubject {
	s := &entrySubject{name: name, srel: srel, fname: name, fsrel: srel, isDir: isDir}
	if !caseSensitive {
		s.fname = textsearch.Fold(name)
		s.fsrel = textsearch.Fold(srel)
	}
	return s
}

func (s *entrySubject) pathSegments() []string {
	if s.segs == nil {
		s.segs = strings.Split(s.fsrel, "/")
	}
	return s.segs
}

type globMatcher struct {
	base []segGlob
	path []pathGlob
}

func (m globMatcher) matches(s *entrySubject) bool {
	for _, g := range m.base {
		if g.match(s.fname) {
			return true
		}
	}
	if len(m.path) > 0 {
		segs := s.pathSegments()
		for _, g := range m.path {
			if g.match(segs) {
				return true
			}
		}
	}
	return false
}

// compileGlob takes p already folded when the search ignores case.
func compileGlob(p string) (globMatcher, error) {
	alts, err := expandBraces(p)
	if err != nil {
		return globMatcher{}, err
	}
	var m globMatcher
	for _, alt := range alts {
		if !strings.Contains(alt, "/") {
			g, err := compileSegment(alt, starRunRefused)
			if err != nil {
				return globMatcher{}, err
			}
			m.base = append(m.base, g)
			continue
		}
		switch {
		case strings.HasPrefix(alt, "./"):
			alt = alt[2:]
		case strings.HasPrefix(alt, "/"):
			alt = alt[1:]
		default:
			alt = "**/" + alt
		}
		g, err := compilePath(alt, starRunRefused)
		if err != nil {
			return globMatcher{}, err
		}
		m.path = append(m.path, g)
	}
	return m, nil
}

type filterPattern struct {
	// bare is a pattern with no glob character and no "/": the basename equals it, or, when it
	// starts with ".", ends with it.
	bare    string
	glob    globMatcher
	isBare  bool
	dirOnly bool
}

func (p *filterPattern) matches(s *entrySubject) bool {
	if p.dirOnly && !s.isDir {
		return false
	}
	if p.isBare {
		return s.fname == p.bare || (strings.HasPrefix(p.bare, ".") && strings.HasSuffix(s.fname, p.bare))
	}
	return p.glob.matches(s)
}

// filesFilter is the compiled Files field. An exclude wins over any include; with includes
// present, a file must match one of them or sit under a directory that did.
type filesFilter struct {
	include []filterPattern
	exclude []filterPattern
}

func (f filesFilter) hasIncludes() bool { return len(f.include) > 0 }

func (f filesFilter) empty() bool { return len(f.include) == 0 && len(f.exclude) == 0 }

func (f filesFilter) excludes(s *entrySubject) bool { return anyPattern(f.exclude, s) }

func (f filesFilter) includes(s *entrySubject) bool { return anyPattern(f.include, s) }

func anyPattern(ps []filterPattern, s *entrySubject) bool {
	for i := range ps {
		if ps[i].matches(s) {
			return true
		}
	}
	return false
}

func compileFilesFilter(raw string, caseSensitive bool) (filesFilter, error) {
	var f filesFilter
	n := 0
	for _, part := range splitTopLevelCommas(raw) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n++
		exclude := strings.HasPrefix(part, "!")
		if exclude {
			part = strings.TrimSpace(part[1:])
		}
		if n > maxFilterItems || len(part) > maxFilterItemLen {
			return filesFilter{}, errBadPattern
		}
		if !caseSensitive {
			part = textsearch.Fold(part)
		}
		p, err := compileFilterPattern(part)
		if err != nil {
			return filesFilter{}, err
		}
		if exclude {
			f.exclude = append(f.exclude, p)
		} else {
			f.include = append(f.include, p)
		}
	}
	return f, nil
}

func compileFilterPattern(part string) (filterPattern, error) {
	var p filterPattern
	if strings.HasSuffix(part, "/") {
		p.dirOnly = true
		part = strings.TrimRight(part, "/")
	}
	if part == "" || part == "." {
		return filterPattern{}, errBadPattern
	}
	if !hasGlobMeta(part) && !strings.Contains(part, "/") {
		p.isBare, p.bare = true, part
		return p, nil
	}
	g, err := compileGlob(part)
	if err != nil {
		return filterPattern{}, err
	}
	p.glob = g
	return p, nil
}

// Name-match tiers, best first: how a row ranks against the others before depth and length.
const (
	tierExact = iota
	tierPrefix
	tierSuffix
	tierWord
	tierInterior
	tierPath
)

// queryTerm is one name-query term: a substring (plain or quoted) or a glob, against the basename
// or, when it holds a "/", the path.
type queryTerm struct {
	glob   globMatcher
	needle textsearch.Needle
	isGlob bool
	onPath bool
}

// nameQuery is the compiled names-mode query: every term must match.
type nameQuery struct {
	terms []queryTerm
}

func (q nameQuery) empty() bool { return len(q.terms) == 0 }

// compileQuery splits a names-mode query into terms on whitespace. A `"…"` term is literal and may
// hold spaces; an unclosed quote runs to the end, so a query being typed is never refused.
func compileQuery(raw string, caseSensitive bool) (nameQuery, error) {
	var q nameQuery
	for _, rt := range tokenizeQuery(raw) {
		if len(q.terms) == maxFilterItems || len(rt.text) > maxFilterItemLen {
			return nameQuery{}, errBadPattern
		}
		t := queryTerm{onPath: strings.Contains(rt.text, "/")}
		if !rt.quoted && strings.ContainsAny(rt.text, "*?[{") {
			text := rt.text
			if !caseSensitive {
				text = textsearch.Fold(text)
			}
			g, err := compileGlob(text)
			if err != nil {
				return nameQuery{}, err
			}
			t.isGlob, t.glob, t.onPath = true, g, len(g.path) > 0
		} else {
			t.needle = textsearch.NewNeedle(rt.text, caseSensitive)
		}
		q.terms = append(q.terms, t)
	}
	return q, nil
}

type rawTerm struct {
	text   string
	quoted bool
}

func tokenizeQuery(raw string) []rawTerm {
	var out []rawTerm
	for rest := raw; ; {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			return out
		}
		if rest[0] == '"' {
			body, n := quotedTerm(rest[1:])
			if body != "" {
				out = append(out, rawTerm{text: body, quoted: true})
			}
			rest = rest[1+n:]
			continue
		}
		end := strings.IndexFunc(rest, unicode.IsSpace)
		if end < 0 {
			end = len(rest)
		}
		out = append(out, rawTerm{text: rest[:end]})
		rest = rest[end:]
	}
}

// quotedTerm reads a quoted term's body after its opening quote and returns it with the bytes
// consumed, closing quote included. `\"` and `\\` stand for the character escaped, so any literal
// can be quoted; every other `\` is itself.
func quotedTerm(s string) (body string, n int) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			return b.String(), i + 1
		case c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\'):
			i++
			b.WriteByte(s[i])
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), len(s)
}

// match reports whether every term matches the entry, the best tier any term reached, and the
// UTF-16 ranges of the basename the substring terms cover, merged.
func (q nameQuery) match(s *entrySubject) (tier int, ranges []MatchRange, ok bool) {
	tier = tierPath
	for i := range q.terms {
		t := &q.terms[i]
		tt, rs, hit := t.match(s)
		if !hit {
			return 0, nil, false
		}
		tier = min(tier, tt)
		ranges = append(ranges, rs...)
	}
	if len(q.terms) == 0 {
		tier = tierExact
	}
	return tier, mergeRanges(ranges), true
}

func (t *queryTerm) match(s *entrySubject) (tier int, ranges []MatchRange, ok bool) {
	if t.isGlob {
		if !t.glob.matches(s) {
			return 0, nil, false
		}
		if t.onPath {
			return tierPath, nil, true
		}
		return tierInterior, nil, true
	}
	if t.onPath {
		return t.matchPath(s)
	}
	tier = tierPath
	for hit := range t.needle.Occurrences(s.name) {
		ok = true
		tier = min(tier, occurrenceTier(s.name, hit, t.needle.RuneLen()))
		ranges = append(ranges, utf16Range(s.name, hit, t.needle.RuneLen()))
	}
	return tier, ranges, ok
}

// matchPath matches a substring term against the path under the search root; the part of an
// occurrence that falls inside the basename is still highlighted there.
func (t *queryTerm) matchPath(s *entrySubject) (tier int, ranges []MatchRange, ok bool) {
	baseByte := len(s.srel) - len(s.name)
	for hit := range t.needle.Occurrences(s.srel) {
		ok = true
		end := advanceRunes(s.srel, hit.Byte, t.needle.RuneLen())
		if end <= baseByte {
			continue
		}
		start := max(hit.Byte, baseByte) - baseByte
		ranges = append(ranges, MatchRange{
			Start: utf16Len(s.name[:start]),
			End:   utf16Len(s.name[:end-baseByte]),
		})
	}
	return tierPath, ranges, ok
}

func occurrenceTier(name string, hit textsearch.Hit, n int) int {
	end := advanceRunes(name, hit.Byte, n)
	switch {
	case hit.Byte == 0 && end == len(name):
		return tierExact
	case hit.Byte == 0:
		return tierPrefix
	case end == len(name):
		return tierSuffix
	}
	prev, _ := utf8.DecodeLastRuneInString(name[:hit.Byte])
	first, _ := utf8.DecodeRuneInString(name[hit.Byte:])
	if !isWordRune(prev) || !isWordRune(first) || (unicode.IsLower(prev) && unicode.IsUpper(first)) {
		return tierWord
	}
	return tierInterior
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func advanceRunes(s string, from, n int) int {
	for range n {
		_, w := utf8.DecodeRuneInString(s[from:])
		from += w
	}
	return from
}

func utf16Range(s string, hit textsearch.Hit, n int) MatchRange {
	start := utf16Len(s[:hit.Byte])
	return MatchRange{Start: start, End: start + utf16Len(s[hit.Byte:advanceRunes(s, hit.Byte, n)])}
}

// utf16Len counts s in UTF-16 code units, as the client's string offsets do. An invalid byte is
// one unit: encoding/json writes it as one U+FFFD.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// mergeRanges joins overlapping ranges, so the client never marks a character twice.
func mergeRanges(rs []MatchRange) []MatchRange {
	if len(rs) < 2 {
		return rs
	}
	slices.SortFunc(rs, func(a, b MatchRange) int { return cmp.Compare(a.Start, b.Start) })
	out := rs[:1]
	for _, r := range rs[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End {
			last.End = max(last.End, r.End)
			continue
		}
		out = append(out, r)
	}
	return out
}
