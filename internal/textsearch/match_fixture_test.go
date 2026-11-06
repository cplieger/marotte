package textsearch

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// matchFixture is testdata/match.json: the query grammar and matcher's contract with the TypeScript twin in
// static-src/textsearch (query.ts), produced from real ParseQuery and Spans output.
type matchFixture struct {
	Comment []string   `json:"_comment"`
	Rows    []matchRow `json:"rows"`
}

type matchRow struct {
	Name          string        `json:"name"`
	Text          string        `json:"text"`
	Query         string        `json:"query"`
	CaseSensitive bool          `json:"case_sensitive"`
	Terms         []Term        `json:"terms"`
	Filters       []Filter      `json:"filters"`
	Spans         []fixtureSpan `json:"spans"`
	All           bool          `json:"all"`
}

// fixtureSpan is a Span plus its start and length in UTF-16 code units, the TypeScript side's unit.
type fixtureSpan struct {
	Term     int `json:"term"`
	Rune     int `json:"rune"`
	Byte     int `json:"byte"`
	UTF16    int `json:"utf16"`
	Runes    int `json:"runes"`
	UTF16Len int `json:"utf16_len"`
}

const (
	matchFixturePath = "testdata/match.json"
	matchFixtureCmd  = "UPDATE_GOLDEN=1 go test ./internal/textsearch/ -run TestMatchFixture"
)

var matchFixtureComment = []string{
	"Query grammar and spans, as the Go kernel reports them.",
	"",
	"Go PRODUCES this file from textsearch.ParseQuery and Matcher.Spans (TestMatchFixture) and the",
	"TypeScript twin in static-src/textsearch (query.ts) CONSUMES it, asserting each row's terms, filters,",
	"spans and whether every term matched. The filter rule here accepts the name `file` only.",
	"Positions index the ORIGINAL text: rune and byte as Go counts, utf16 and utf16_len in code units.",
	"",
	"Regenerate with: " + matchFixtureCmd,
	"then, from static-src/: npx vitest --run --project node textsearch/match.node.test.ts",
}

func matchRows() []matchRow {
	return []matchRow{
		{Name: "all_terms_in_another_order", Text: "the failure in deadset was a security bug", Query: "deadset security failure"},
		{Name: "two_of_three_terms", Text: "deadset security notes", Query: "deadset security failure"},
		{Name: "phrase_in_order", Text: "a security failure", Query: `"security failure"`},
		{Name: "phrase_out_of_order", Text: "failure security", Query: `"security failure"`},
		{Name: "phrase_across_a_newline", Text: "security\nfailure", Query: `"security failure"`},
		{Name: "phrase_across_two_spaces", Text: "security  failure", Query: `"security failure"`},
		{Name: "phrase_needs_whitespace", Text: "securityfailure", Query: `"security failure"`},
		{Name: "unclosed_quote_runs_to_the_end", Text: "deadset security failure", Query: `deadset "security fail`},
		{Name: "quoted_filter_stays_text", Text: "see file:a.go", Query: `"file:a.go"`},
		{Name: "unquoted_filter_is_lifted", Text: "a.go retry", Query: "file:a.go retry"},
		{Name: "refused_filter_stays_text", Text: "http://x", Query: "http://x"},
		{Name: "minus_is_text", Text: "go test -v ./...", Query: "-v"},
		{Name: "accent_in_the_text", Text: "La Sécurité", Query: "securite"},
		{Name: "accent_in_the_query", Text: "securite", Query: "Sécurité"},
		{Name: "uppercase_accent", Text: "ÉCOLE", Query: "ecole"},
		{Name: "decomposed_accent_in_the_text", Text: "e\u0301cole", Query: "école"},
		{Name: "astral_rune_before_the_match", Text: "😀😀 deadset", Query: "deadset"},
		{Name: "kelvin_sign", Text: "\u212Aelvin", Query: "kelvin"},
		{Name: "final_sigma", Text: "ΟΔΟΣ", Query: "οδος"},
		{Name: "case_sensitive_accent_is_literal", Text: "Sécurité", Query: "securite", CaseSensitive: true},
		{Name: "case_sensitive_match", Text: "Retry retry", Query: "retry", CaseSensitive: true},
		{Name: "overlapping_terms", Text: "deadset-go", Query: "deadset set-go"},
		{Name: "empty_query", Text: "anything", Query: `""`},
	}
}

func TestMatchFixture(t *testing.T) {
	fx := matchFixture{Comment: matchFixtureComment, Rows: matchRows()}
	matched := 0
	for i := range fx.Rows {
		row := &fx.Rows[i]
		q := ParseQuery(row.Query, acceptFile)
		row.Terms, row.Filters = q.Terms, q.Filters
		if row.Terms == nil {
			row.Terms = []Term{}
		}
		if row.Filters == nil {
			row.Filters = []Filter{}
		}
		m := NewMatcher(q.Terms, row.CaseSensitive)
		row.Spans = []fixtureSpan{}
		hit := make(map[int]bool)
		for _, s := range m.Spans(row.Text) {
			end := byteAfterRunes(row.Text, s.Byte, s.Runes)
			row.Spans = append(row.Spans, fixtureSpan{
				Term: s.Term, Rune: s.Rune, Byte: s.Byte, Runes: s.Runes,
				UTF16:    utf16Len(row.Text[:s.Byte]),
				UTF16Len: utf16Len(row.Text[s.Byte:end]),
			})
			hit[s.Term] = true
		}
		row.All = m.Terms() > 0 && len(hit) == m.Terms()
		if row.All {
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("no fixture row matches every term; the TS side would pin nothing")
	}
	got, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(matchFixturePath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(matchFixturePath)
	if err != nil {
		t.Fatalf("read golden %s (run %s): %v", matchFixturePath, matchFixtureCmd, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ParseQuery or Spans drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\nRegenerate with %s, then run static-src/textsearch/match.node.test.ts.",
			matchFixturePath, want, got, matchFixtureCmd)
	}
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func byteAfterRunes(s string, from, n int) int {
	for range n {
		_, w := utf8.DecodeRuneInString(s[from:])
		from += w
	}
	return from
}
