package textsearch

import (
	"slices"
	"testing"
)

func acceptFile(name, _ string) bool { return name == "file" }

func TestParseQuery(t *testing.T) {
	word := func(w string) Term { return Term{Words: []string{w}} }
	phrase := func(ws ...string) Term { return Term{Words: ws} }
	cases := []struct {
		name    string
		raw     string
		terms   []Term
		filters []filter
	}{
		{"words in input order", "deadset security failure", []Term{word("deadset"), word("security"), word("failure")}, nil},
		{"any run of whitespace separates", " a\t\tb\n c\u00a0d ", []Term{word("a"), word("b"), word("c"), word("d")}, nil},
		{"a quoted phrase", `"security failure" deadset`, []Term{phrase("security", "failure"), word("deadset")}, nil},
		{"a quote ends the word before it", `ab"cd ef"gh`, []Term{word("ab"), phrase("cd", "ef"), word("gh")}, nil},
		{"an unclosed quote runs to the end", `deadset "security fail`, []Term{word("deadset"), phrase("security", "fail")}, nil},
		{"an empty phrase adds nothing", `"" "  " x`, []Term{word("x")}, nil},
		{"a minus is text", "-v --force", []Term{word("-v"), word("--force")}, nil},
		{"an accepted filter", "file:Main.go retry", []Term{word("retry")}, []filter{{Name: "file", Value: "Main.go"}}},
		{"the filter name is lowercased, the value kept", "FILE:A.go", nil, []filter{{Name: "file", Value: "A.go"}}},
		{"a refused filter stays a word", "http://x", []Term{word("http://x")}, nil},
		{"an empty value stays a word", "file:", []Term{word("file:")}, nil},
		{"quoted text is never a filter", `"file:a.go"`, []Term{phrase("file:a.go")}, nil},
		{"terms are not deduplicated", "a a", []Term{word("a"), word("a")}, nil},
		{"invalid UTF-8 becomes U+FFFD", "a\xffb", []Term{word("a\uFFFDb")}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := ParseQuery(c.raw, acceptFile)
			if !slices.EqualFunc(q.Terms, c.terms, func(a, b Term) bool { return slices.Equal(a.Words, b.Words) }) {
				t.Errorf("terms = %+v, want %+v", q.Terms, c.terms)
			}
			if !slices.Equal(q.Filters, c.filters) {
				t.Errorf("filters = %+v, want %+v", q.Filters, c.filters)
			}
		})
	}
}

func TestParseQuery_NilFilterRuleKeepsEveryWord(t *testing.T) {
	q := ParseQuery("file:a.go", nil)
	if len(q.Filters) != 0 || len(q.Terms) != 1 || q.Terms[0].Words[0] != "file:a.go" {
		t.Errorf("ParseQuery(nil rule) = %+v, want one word", q)
	}
}
