package textsearch

import (
	"slices"
	"testing"
)

func spansOf(raw, text string, caseSensitive bool) []Span {
	return NewMatcher(ParseQuery(raw, nil).Terms, caseSensitive).Spans(text)
}

func TestMatcher_Spans(t *testing.T) {
	cases := []struct {
		name          string
		raw, text     string
		caseSensitive bool
		want          []Span
	}{
		{
			"every term, merged in text order", "failure deadset", "deadset failure deadset",
			false,
			[]Span{{Term: 1, Rune: 0, Runes: 7}, {Term: 0, Rune: 8, Runes: 7}, {Term: 1, Rune: 16, Runes: 7}},
		},
		{"a phrase spans its whitespace", `"security failure"`, "a security\n  failure", false, []Span{{Term: 0, Rune: 2, Runes: 18}}},
		{"a phrase out of order has no span", `"security failure"`, "failure security", false, nil},
		{"a failed extension resumes one rune on", `"aa b"`, "aaa b", false, []Span{{Term: 0, Rune: 1, Runes: 4}}},
		{"a phrase's last word may continue", `"security fail"`, "security failure", false, []Span{{Term: 0, Rune: 0, Runes: 13}}},
		{"spans of one term never overlap", "aa", "aaa", false, []Span{{Term: 0, Rune: 0, Runes: 2}}},
		{"ties order by term", "ab a", "ab", false, []Span{{Term: 0, Rune: 0, Runes: 2}, {Term: 1, Rune: 0, Runes: 1}}},
		{"accents fold", "securite", "La Sécurité", false, []Span{{Term: 0, Rune: 3, Runes: 8}}},
		{"case-sensitive is literal", "securite", "La Sécurité", true, nil},
		{"rune offsets index the original", "x", "é😀x", false, []Span{{Term: 0, Rune: 2, Runes: 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spansOf(c.raw, c.text, c.caseSensitive); !slices.Equal(got, c.want) {
				t.Errorf("Spans = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestMatcher_Terms(t *testing.T) {
	if n := NewMatcher(nil, false).Terms(); n != 0 {
		t.Errorf("Terms() = %d with no terms", n)
	}
	if n := NewMatcher(ParseQuery(`a "b c"`, nil).Terms, false).Terms(); n != 2 {
		t.Errorf("Terms() = %d, want 2", n)
	}
	if got := NewMatcher(nil, false).Spans("anything"); len(got) != 0 {
		t.Errorf("an empty matcher yields %+v", got)
	}
}
