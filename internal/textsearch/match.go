package textsearch

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Matcher is a query's terms prepared once for scanning any number of texts.
type Matcher struct {
	terms         [][]string
	caseSensitive bool
}

// NewMatcher prepares terms. Case-insensitive matching folds case and accents on both sides (FoldText);
// case-sensitive matching is literal.
func NewMatcher(terms []Term, caseSensitive bool) Matcher {
	m := Matcher{terms: make([][]string, 0, len(terms)), caseSensitive: caseSensitive}
	for _, t := range terms {
		words := make([]string, 0, len(t.Words))
		for _, w := range t.Words {
			if !caseSensitive {
				w = FoldText(w)
			}
			if w != "" {
				words = append(words, w)
			}
		}
		if len(words) > 0 {
			m.terms = append(m.terms, words)
		}
	}
	return m
}

// Terms is how many terms must match; 0 means the query has no free text.
func (m Matcher) Terms() int { return len(m.terms) }

// Span is one term's match, addressed into the ORIGINAL text: Rune where it starts, Runes its length.
type Span struct{ Term, Rune, Runes int }

// Spans lists every term's matches in text order, ties by term index. One term's spans never overlap (scanned
// left to right); spans of different terms may. A phrase matches its first word as a substring, then each next
// word after one or more whitespace runes; a failed extension resumes one rune after its start.
func (m Matcher) Spans(text string) []Span {
	if len(m.terms) == 0 {
		return nil
	}
	hay := text
	if !m.caseSensitive {
		hay = FoldText(text)
	}
	var out []Span
	for i, words := range m.terms {
		out = appendTermSpans(out, hay, i, words)
	}
	if len(out) == 0 {
		return nil
	}
	// The fold preserves rune count, so a rune offset into hay is one into text.
	slices.SortStableFunc(out, func(a, b Span) int { return cmp.Or(cmp.Compare(a.Rune, b.Rune), cmp.Compare(a.Term, b.Term)) })
	return out
}

// appendTermSpans scans hay for one term.
func appendTermSpans(out []Span, hay string, term int, words []string) []Span {
	hb, hr := 0, 0
	for from := 0; from < len(hay); {
		at := strings.Index(hay[from:], words[0])
		if at < 0 {
			break
		}
		at += from
		hr += utf8.RuneCountInString(hay[hb:at])
		hb = at
		end, ok := phraseEnd(hay, at+len(words[0]), words[1:])
		if !ok {
			_, w := utf8.DecodeRuneInString(hay[at:])
			from = at + w
			continue
		}
		out = append(out, Span{Term: term, Rune: hr, Runes: utf8.RuneCountInString(hay[at:end])})
		from = end
	}
	return out
}

// phraseEnd extends a phrase match from pos over rest, each word preceded by at least one whitespace rune.
func phraseEnd(hay string, pos int, rest []string) (int, bool) {
	for _, w := range rest {
		start := pos
		for pos < len(hay) {
			r, n := utf8.DecodeRuneInString(hay[pos:])
			if !unicode.IsSpace(r) {
				break
			}
			pos += n
		}
		if pos == start || !strings.HasPrefix(hay[pos:], w) {
			return 0, false
		}
		pos += len(w)
	}
	return pos, true
}
