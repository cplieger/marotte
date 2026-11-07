package chat

import (
	"testing"

	"github.com/cplieger/marotte/internal/textsearch"
)

// searchQueryFixture is testdata/search_query.json: the chat search's query parse, produced from parseSearchQuery
// and consumed by static-src/chat-query.node.test.ts through parseChatQuery, so the DOM pass marks the words the
// server matched.
type searchQueryFixture struct {
	Comment []string         `json:"_comment"`
	Rows    []searchQueryRow `json:"rows"`
}

type searchQueryRow struct {
	Raw     string             `json:"raw"`
	Terms   []textsearch.Term  `json:"terms"`
	Filters searchQueryFilters `json:"filters"`
}

// searchQueryFilters are the scoped filters as the scan applies them; turn is -1 for none.
type searchQueryFilters struct {
	File string `json:"file"`
	Tool string `json:"tool"`
	Turn int    `json:"turn"`
}

var searchQueryFixtureComment = []string{
	"The chat search's query parse, as parseSearchQuery (internal/chat/search.go) reports it.",
	"",
	"Go PRODUCES this file (TestSearchQueryFixture) and static-src/chat-query.node.test.ts CONSUMES it",
	"through parseChatQuery. file and tool are lowercased, turn is -1 when absent, the last filter of a",
	"name wins, and anything the rule refuses stays a term.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchQueryFixture",
	"then, from static-src/: npx vitest --run --project node chat-query.node.test.ts",
}

func TestSearchQueryFixture(t *testing.T) {
	raws := []string{
		"deadset security failure",
		`"security failure" deadset`,
		"file:Main.go retry",
		"tool:ReadFile turn:3",
		"turn:0",
		"turn:abc",
		"turn:-1",
		"turn:+2",
		"turn:99999999999999999999",
		"http://x",
		`file:"a b"`,
		"file:a.go file:b.go",
		"FILE:A.go Tool:Edit",
		`"file:a.go"`,
		"role:user",
		"file:",
		"-v --force",
	}
	fx := searchQueryFixture{Comment: searchQueryFixtureComment}
	for _, raw := range raws {
		q := parseSearchQuery(raw, false)
		terms := q.terms
		if terms == nil {
			terms = []textsearch.Term{}
		}
		fx.Rows = append(fx.Rows, searchQueryRow{Raw: raw, Terms: terms, Filters: searchQueryFilters{File: q.file, Tool: q.tool, Turn: q.turn}})
	}
	pinGolden(t, "testdata/search_query.json", fx, "TestSearchQueryFixture", "chat-query.node.test.ts")
}
