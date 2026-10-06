package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// searchFixture is testdata/search_hits.json: one real search reply per query. Go produces it from a real scan
// (UPDATE_GOLDEN=1); chat-search.node.test.ts decodes it through decodeSearchResult.
type searchFixture struct {
	Comment []string          `json:"_comment"`
	Queries []searchQueryCase `json:"queries"`
}

// searchQueryCase is one query's pinned reply.
type searchQueryCase struct {
	Name          string       `json:"name"`
	Query         string       `json:"query"`
	CaseSensitive bool         `json:"case_sensitive"`
	Result        SearchResult `json:"result"`
}

var searchFixtureComment = []string{
	"GET /api/chats/{id}/search replies, produced by a real scan over one two-turn entry log.",
	"",
	"chat.SearchResult and chat.Hit are wiregen-registered, so the TypeScript types and",
	"decoders are generated from them; this file is what keeps the ENCODER honest against",
	"that decoder. TestSearchWireContract (Go) asserts the scan marshals to exactly these",
	"bytes, and chat-search.node.test.ts (TypeScript) decodes every reply through the",
	"generated decodeSearchResult and pins the field-level invariants (segment-relative",
	"RUNE offsets, the entry-kind zero contract, the tally beside the hits).",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchWireContract",
	"then re-run the TS half: npx vitest --run chat-search.node.test.ts (from static-src/).",
}

// searchContractEntries is the fixture's log: turn 1 covers every free-text segment kind (the prompt twice, the second
// after a multibyte word, its attachment name, thinking, text, a delegate lane, one tool pair per settled kind, steer,
// plan, failed close); turn 2 is `turn:2`'s target, with entry hits and no prose behind one. Every declared kind must
// occur here.
func searchContractEntries() ([]marotte.Entry, map[string]struct{}) {
	return chatOf(
		openTurn("t-1", 1, &marotte.EntryPrompt{
			ID:   "m-1",
			Text: "Where does the retry backoff live? The naïve loop calls retry twice.",
			// The path carries the needle and adds no hit: names only.
			Attachments: []marotte.Attachment{{Path: "docs/retry/backoff.md", Name: "retry-notes.md"}},
		}).
			thinking("th1", "The retry semantics differ per client.").
			text("a1", "The **retry** helper lives in fetch.go; wrap the call in retry(ctx).").
			tool(marotte.EntryToolCall{ID: "tc1", Title: "Read retry.go", Kind: marotte.ToolKindRead},
				&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "func retry(ctx context.Context) error"}).
			laneText("sub-1", "a2", "The delegate traced the retry path end to end.").
			// The one tool_input hit: its 28-byte leaf is under inputLeafDedupeMin (40), so it is kept though it repeats in
			// new_text. old_text carries the needle too and adds nothing.
			tool(marotte.EntryToolCall{
				ID: "tc3", Title: "Replace in File", Kind: marotte.ToolKindEdit,
				Input: json.RawMessage(`{"path":"fetch.go","newStr":"return retry(ctx, fetchOnce)"}`),
			},
				&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{
					Path:    "fetch.go",
					OldText: "func fetch(ctx context.Context) error { return retryOnce(ctx) }",
					NewText: "func fetch(ctx context.Context) error {\n\treturn retry(ctx, fetchOnce)\n}",
				}}}).
			// The disclosed claim replaces the title; the URI's needle adds nothing.
			tool(marotte.EntryToolCall{ID: "tc4", Title: "Disclose Context", Kind: marotte.ToolKindOther},
				&marotte.EntryToolResult{Status: marotte.ToolCompleted, Disclosed: &marotte.ToolDisclosed{
					Type: "skill", DisplayName: "retry-budget", URI: "file:///workspace/.kiro/skills/retry/SKILL.md",
				}}).
			// The denial's resource is the reader-facing string; capability and patterns add nothing.
			tool(marotte.EntryToolCall{ID: "tc5", Title: "Run Command", Kind: marotte.ToolKindExecute},
				&marotte.EntryToolResult{Status: marotte.ToolFailed, Denial: &marotte.ToolDenial{
					Capability: "shell_retry", Resource: "rm -rf /config/retry", Scope: "user", Source: "permissions.yaml",
					Rule: &marotte.ToolDenialRule{Capability: "shell", Effect: "deny", Match: []string{"rm -rf /config/retry*"}},
				}}).
			add("", "steer-1", marotte.EntryKindSteer, marotte.EntrySteer{
				Text: "also cap the retry count", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
			}).
			add("", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
				{Content: "Trace the retry path", Status: marotte.PlanCompleted},
			}}).
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: "the retry budget ran out"}),
		openTurn("t-2", 2, prompt("m-2", "Anything left?")).
			tool(marotte.EntryToolCall{ID: "tc2", Title: "List files", Kind: marotte.ToolKindRead},
				&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "a.go b.go"}).
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}),
	)
}

// TestSearchWireContract pins the in-chat search reply's shape to testdata/search_hits.json, read by
// chat-search.node.test.ts.
func TestSearchWireContract(t *testing.T) {
	entries, drawn := searchContractEntries()
	fx := searchFixture{
		Comment: searchFixtureComment,
		Queries: []searchQueryCase{
			{Name: "free text hits every segment kind", Query: "retry"},
			{Name: "filter only lists matching entries", Query: "turn:2"},
		},
	}
	kinds := make(map[SegmentKind]int)
	for i := range fx.Queries {
		q := &fx.Queries[i]
		q.Result = Search(entries, drawn, q.Query, q.CaseSensitive)
		if len(q.Result.Matches) == 0 {
			t.Fatalf("Search(%q) found nothing; an empty fixture would pin nothing", q.Query)
		}
		for _, h := range q.Result.Matches {
			kinds[h.SegmentKind]++
		}
	}
	// segmentKinds, not a second literal.
	for _, want := range segmentKinds {
		if kinds[want] == 0 {
			t.Errorf("fixture carries no %q hit; the TS side cannot pin a kind that never occurs", want)
		}
	}

	pinGolden(t, "testdata/search_hits.json", fx, "TestSearchWireContract", "chat-search.node.test.ts")
}

// searchAllFixture is testdata/search_all.json: one real GET /api/chats/search reply, decoded by
// actions/chat-search.node.test.ts through decodeSearchAllResult.
type searchAllFixture struct {
	Comment []string        `json:"_comment"`
	Query   string          `json:"query"`
	Result  SearchAllResult `json:"result"`
}

var searchAllFixtureComment = []string{
	"A GET /api/chats/search reply, produced by a real SearchAll over a seeded store.",
	"",
	"chat.SearchAllResult and chat.Match are wiregen-registered; TestSearchAllWireContract",
	"(Go) asserts the store's reply marshals to exactly these bytes, and",
	"actions/chat-search.node.test.ts (TypeScript) decodes it through the generated",
	"decodeSearchAllResult. A title-only match carries no `best`.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchAllWireContract",
	"then re-run the TS half: npx vitest --run actions/chat-search.node.test.ts (from static-src/).",
}

// TestSearchAllWireContract pins the cross-chat reply's shape with three rows: title and body, body-only with
// several hits (a multibyte word keeps rune offsets honest), and title-only. Directories are written directly to fix
// UpdatedAt and mtime order.
func TestSearchAllWireContract(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := func(i int, id, name string, turn *turnFixture) {
		entries, _ := chatOf(turn)
		seedChatDir(t, s, &marotte.Chat{ID: id, Name: name, UpdatedAt: int64(1000 * (i + 1)), TurnCount: 1},
			entries, base.Add(time.Duration(i)*time.Minute))
	}
	seed(0, "chat-001", "Redis migration", openTurn("chat-001-t1", 1, prompt("m-1", "we moved the cache to redis today")).
		text("a1", "Redis is up; the naïve redis client was replaced.").
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}))
	seed(1, "chat-002", "Grocery list", openTurn("chat-002-t1", 1, prompt("m-1", "nothing relevant here at all")).
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}))
	seed(2, "chat-003", "Why redis over memcached", openTurn("chat-003-t1", 1, prompt("m-1", "compare the two caches for us")).
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}))

	fx := searchAllFixture{Comment: searchAllFixtureComment, Query: "redis"}
	fx.Result = s.SearchAll(t.Context(), fx.Query)
	if len(fx.Result.Matches) != 2 {
		t.Fatalf("SearchAll(%q) matched %d chats, want 2: %+v", fx.Query, len(fx.Result.Matches), fx.Result.Matches)
	}
	var withBest, titleOnly int
	for _, m := range fx.Result.Matches {
		if m.Best != nil {
			withBest++
		} else {
			titleOnly++
		}
	}
	if withBest == 0 || titleOnly == 0 {
		t.Fatalf("fixture needs one match with a best hit and one without, got %d and %d", withBest, titleOnly)
	}

	pinGolden(t, "testdata/search_all.json", fx, "TestSearchAllWireContract", "actions/chat-search.node.test.ts")
}

// pinGolden marshals v, rewrites path under UPDATE_GOLDEN=1 and compares bytes. The failure names the regeneration
// command and the TypeScript consumer: a cross-language fixture changes atomically.
func pinGolden(t *testing.T, path string, v any, regen, consumer string) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/chat/ -run %s): %v", path, regen, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/chat/ -run %s, "+
			"then re-run the TS half: npx vitest --run %s (from static-src/).",
			path, want, got, regen, consumer)
	}
}
