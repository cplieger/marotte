package textsearch

import (
	"strings"
	"unicode"
)

// Query is a parsed search box value: the free-text terms, every one of which must match, and the scoped filters
// the caller's rule accepted.
type Query struct {
	Terms   []Term
	Filters []filter
}

// Term is one thing that must match: a word, or a quoted phrase whose words match in order with any run of
// whitespace between them.
type Term struct {
	Words []string `json:"words"`
}

// filter is an unquoted `name:value` word the caller's rule accepted. Name is lowercased; Value is as typed.
type filter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseQuery splits raw into terms and filters. Whitespace (unicode.IsSpace) separates words; `"` opens a phrase
// and the next `"` closes it, an unclosed phrase running to the end. An unquoted word `name:value` with a
// non-empty value is offered to isFilter (nil accepts none); a refused one stays a word. There is no OR or NOT:
// `-v` is a word. Terms keep input order and are not deduplicated.
func ParseQuery(raw string, isFilter func(name, value string) bool) Query {
	p := queryParser{isFilter: isFilter}
	for _, r := range strings.ToValidUTF8(raw, "\uFFFD") {
		switch {
		case r == '"':
			p.endWord()
			if p.inPhrase {
				p.endPhrase()
			} else {
				p.inPhrase = true
			}
		case unicode.IsSpace(r):
			p.endWord()
		default:
			p.word.WriteRune(r)
		}
	}
	p.endWord()
	p.endPhrase()
	return p.q
}

type queryParser struct {
	isFilter func(name, value string) bool
	q        Query
	phrase   []string
	word     strings.Builder
	inPhrase bool
}

func (p *queryParser) endWord() {
	if p.word.Len() == 0 {
		return
	}
	w := p.word.String()
	p.word.Reset()
	if p.inPhrase {
		p.phrase = append(p.phrase, w)
		return
	}
	if name, value, ok := strings.Cut(w, ":"); ok && value != "" && p.isFilter != nil && p.isFilter(strings.ToLower(name), value) {
		p.q.Filters = append(p.q.Filters, filter{Name: strings.ToLower(name), Value: value})
		return
	}
	p.q.Terms = append(p.q.Terms, Term{Words: []string{w}})
}

func (p *queryParser) endPhrase() {
	if len(p.phrase) > 0 {
		p.q.Terms = append(p.q.Terms, Term{Words: p.phrase})
	}
	p.phrase = nil
	p.inPhrase = false
}
