package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

type turnFixture struct {
	id      string
	entries []marotte.Entry
}

// segmentKinds is every SegmentKind the const block declares.
var segmentKinds = []SegmentKind{
	segmentContent, segmentReasoning, segmentToolTitle, segmentToolDisclosed,
	segmentToolDiff, segmentToolDenial, segmentToolInput, segmentToolOutput,
	segmentSteer, segmentPrompt, segmentPlan, segmentAttachment, segmentTurnFailure,
	segmentWake,
}

// itemKinds is every ItemKind the const block declares.
var itemKinds = []ItemKind{
	itemHeader, itemMessage, itemReasoning, itemTool, itemSteer, itemPlan, itemNotice, itemTurnFailure, itemTurn,
}

func openTurn(id string, n uint64, prompt *marotte.EntryPrompt) *turnFixture {
	tf := &turnFixture{id: id}
	return tf.add("", id, marotte.EntryKindTurnOpen, marotte.EntryTurnOpen{Prompt: prompt, Source: marotte.TurnOpenNamePrompt, N: n})
}

func prompt(id, text string) *marotte.EntryPrompt {
	return &marotte.EntryPrompt{ID: id, Text: text}
}

func (tf *turnFixture) add(lane, id string, kind marotte.EntryKind, payload any) *turnFixture {
	e := entryOf(tf.id, lane, id, kind, payload)
	e.Seq = uint64(len(tf.entries))
	tf.entries = append(tf.entries, *e)
	return tf
}

func (tf *turnFixture) text(id, s string) *turnFixture {
	return tf.add("", id, marotte.EntryKindText, marotte.EntryText{Text: s})
}

func (tf *turnFixture) laneText(lane, id, s string) *turnFixture {
	return tf.add(lane, id, marotte.EntryKindText, marotte.EntryText{Text: s})
}

func (tf *turnFixture) thinking(id, s string) *turnFixture {
	return tf.add("", id, marotte.EntryKindThinking, marotte.EntryThinking{Text: s})
}

func (tf *turnFixture) tool(call marotte.EntryToolCall, res *marotte.EntryToolResult) *turnFixture {
	tf.add("", call.ID, marotte.EntryKindToolCall, call)
	if res != nil {
		tf.add("", marotte.ToolResultID(call.ID), marotte.EntryKindToolResult, *res)
	}
	return tf
}

func (tf *turnFixture) close(p marotte.EntryTurnClose) *turnFixture {
	return tf.add("", tf.id+":close", marotte.EntryKindTurnClose, p)
}

func chatOf(turns ...*turnFixture) ([]marotte.Entry, map[string]struct{}) {
	var entries []marotte.Entry
	drawn := make(map[string]struct{}, len(turns))
	for _, tf := range turns {
		entries = append(entries, tf.entries...)
		drawn[tf.id] = struct{}{}
	}
	return entries, drawn
}

func oneText(s string) ([]marotte.Entry, map[string]struct{}) {
	return chatOf(openTurn("t-1", 1, nil).text("a1", s))
}

func oneTool(call marotte.EntryToolCall, res *marotte.EntryToolResult) ([]marotte.Entry, map[string]struct{}) {
	return chatOf(openTurn("t-1", 1, nil).tool(call, res))
}

func searchHits(entries []marotte.Entry, drawn map[string]struct{}, q string) []Hit {
	return search(entries, drawn, q, false).Matches
}

func transcript() ([]marotte.Entry, map[string]struct{}) {
	return chatOf(
		openTurn("t-1", 1, prompt("m-1", "how does the retry work")).
			text("a1", "The retry uses exponential backoff.").
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}),
		openTurn("t-2", 2, prompt("m-2", "now fix the composer")).
			text("a2", "Done, the composer grows upward.").
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}),
	)
}

func TestSearch_FindsTextAndNamesItsTurn(t *testing.T) {
	entries, drawn := transcript()
	hits := searchHits(entries, drawn, "composer")
	assertHits(t, hits, []wantHit{
		{entry: "t-2", kind: itemHeader, segs: []SegmentKind{segmentPrompt}},
		{entry: "a2", kind: itemMessage, segs: []SegmentKind{segmentContent}},
	})
	for _, h := range hits {
		if h.TurnID != "t-2" || h.Turn != 2 {
			t.Errorf("hit %+v, want turn t-2 (ordinal 2)", h)
		}
	}
}

// Match-case governs the free text only; scoped filters stay case-insensitive, since paths are typed from memory.
func TestSearch_CaseSensitivity(t *testing.T) {
	plain, plainDrawn := chatOf(openTurn("t-1", 1, prompt("m-1", "now fix the composer")).
		text("a1", "Done, the Composer grows upward."))
	filtered, filteredDrawn := chatOf(openTurn("t-1", 1, prompt("m-1", "look at the Composer")).
		tool(marotte.EntryToolCall{
			ID: "tc1", Title: "ReadFile", Kind: marotte.ToolKindRead,
			Locations: []marotte.ToolLocation{{Path: "static-src/Composer.ts"}},
		}, nil))

	cases := []struct {
		name          string
		entries       []marotte.Entry
		drawn         map[string]struct{}
		query         string
		caseSensitive bool
		want          int
	}{
		{name: "insensitive finds both spellings", entries: plain, drawn: plainDrawn, query: "composer", want: 2},
		{name: "insensitive from an upper-case query", entries: plain, drawn: plainDrawn, query: "COMPOSER", want: 2},
		{name: "sensitive finds only the exact spelling", entries: plain, drawn: plainDrawn, query: "composer", caseSensitive: true, want: 1},
		{name: "sensitive finds the capitalised spelling", entries: plain, drawn: plainDrawn, query: "Composer", caseSensitive: true, want: 1},
		{name: "sensitive finds nothing when nothing matches exactly", entries: plain, drawn: plainDrawn, query: "COMPOSER", caseSensitive: true, want: 0},
		{name: "a file filter stays case-insensitive under match-case", entries: filtered, drawn: filteredDrawn, query: "file:composer.ts", caseSensitive: true, want: 1},
		{name: "a tool filter stays case-insensitive under match-case", entries: filtered, drawn: filteredDrawn, query: "tool:readfile", caseSensitive: true, want: 1},
		{name: "the free text half still respects match-case", entries: filtered, drawn: filteredDrawn, query: "turn:1 composer", caseSensitive: true, want: 0},
		{name: "the free text half matches at its own casing", entries: filtered, drawn: filteredDrawn, query: "turn:1 Composer", caseSensitive: true, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(search(tc.entries, tc.drawn, tc.query, tc.caseSensitive).Matches); got != tc.want {
				t.Errorf("Search(%q, case=%v) = %d hits, want %d", tc.query, tc.caseSensitive, got, tc.want)
			}
		})
	}
}

// One item is one hit however often it holds the words, so the counter counts what the reader steps through.
func TestSearch_AnItemIsOneHitHoweverManyOccurrences(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "intro paragraph").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "shell"}, nil).
		text("a2", "retry retry retry"))
	res := search(entries, drawn, "retry", false)
	assertHits(t, res.Matches, []wantHit{{entry: "a2", kind: itemMessage, segs: []SegmentKind{segmentContent}}})
	if res.Matched != 1 {
		t.Errorf("Matched = %d, want 1 item", res.Matched)
	}
}

func TestSearch_EmptyQueryFindsNothing(t *testing.T) {
	entries, drawn := transcript()
	for _, q := range []string{"", "   "} {
		if got := searchHits(entries, drawn, q); len(got) != 0 {
			t.Errorf("Search(%q) = %d hits, want 0", q, len(got))
		}
	}
}

// An empty result marshals as [], not null.
func TestSearch_NeverReturnsNil(t *testing.T) {
	entries, drawn := transcript()
	if searchHits(entries, drawn, "") == nil {
		t.Error("an empty query returned a nil slice")
	}
	if search(nil, nil, "anything", false).Matches == nil {
		t.Error("an empty log returned a nil slice")
	}
}

// Tool output and thinking are searched: "which turn printed that error" is the commoner question.
func TestSearch_SearchesThinkingAndToolOutput(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		thinking("th1", "considering a mutex here").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "shell"},
			&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "permission denied"}))
	assertHits(t, searchHits(entries, drawn, "permission denied"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolOutput}},
	})
	assertHits(t, searchHits(entries, drawn, "mutex"), []wantHit{
		{entry: "th1", kind: itemReasoning, segs: []SegmentKind{segmentReasoning}},
	})
}

func TestSearch_ScopedFilters(t *testing.T) {
	entries, drawn := chatOf(
		openTurn("t-1", 1, prompt("m-1", "look at auth")).
			text("a1", "reading it").
			tool(marotte.EntryToolCall{
				ID: "tc1", Title: "readFile", Kind: marotte.ToolKindRead,
				Locations: []marotte.ToolLocation{{Path: "internal/auth/token.go"}},
			},
				&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "package auth"}),
		openTurn("t-2", 2, prompt("m-2", "and the composer")).
			text("a2", "editing it").
			tool(marotte.EntryToolCall{ID: "tc2", Title: "Replace in File", Kind: marotte.ToolKindEdit},
				&marotte.EntryToolResult{
					Status: marotte.ToolCompleted,
					Diffs:  []marotte.ToolDiff{{Path: "static-src/composer.ts", NewText: "grow()"}},
				}),
	)

	t.Run("turn", func(t *testing.T) {
		hits := searchHits(entries, drawn, "turn:2")
		if len(hits) == 0 {
			t.Fatal("turn:2 matched nothing")
		}
		for _, h := range hits {
			if h.Turn != 2 || h.TurnID != "t-2" {
				t.Errorf("turn:2 returned a hit in turn %d (%s)", h.Turn, h.TurnID)
			}
		}
	})

	// A read has a location and no diff, so locations count; a write is found through its diff path.
	t.Run("file matches a read as well as a write", func(t *testing.T) {
		assertHits(t, searchHits(entries, drawn, "file:token.go"), []wantHit{{entry: "tc1", kind: itemTool}})
		assertHits(t, searchHits(entries, drawn, "file:composer.ts"), []wantHit{{entry: "tc2", kind: itemTool}})
	})

	t.Run("tool matches title or kind", func(t *testing.T) {
		assertHits(t, searchHits(entries, drawn, "tool:readFile"), []wantHit{{entry: "tc1", kind: itemTool}})
		assertHits(t, searchHits(entries, drawn, "tool:edit"), []wantHit{{entry: "tc2", kind: itemTool}})
	})

	t.Run("filters combine", func(t *testing.T) {
		assertHits(t, searchHits(entries, drawn, "tool:read turn:1"), []wantHit{{entry: "tc1", kind: itemTool}})
		// An all-excluding filter returns nothing rather than ignoring itself.
		if got := searchHits(entries, drawn, "tool:read turn:99"); len(got) != 0 {
			t.Errorf("an impossible combination returned %d hits", len(got))
		}
	})

	t.Run("filter plus free text", func(t *testing.T) {
		assertHits(t, searchHits(entries, drawn, "turn:1 auth"), []wantHit{
			{entry: "t-1", kind: itemHeader, segs: []SegmentKind{segmentPrompt}},
			{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolOutput}},
		})
	})
}

// A colon term naming no filter stays text: URLs are literal, `turn:abc` and `turn:0` name no turn, `role:` is not a
// filter.
func TestSearch_UnknownOrUnparseablePrefixStaysFreeText(t *testing.T) {
	cases := []struct {
		name, content, query string
	}{
		{name: "a URL", content: "see https://example.com for more", query: "https://example.com"},
		{name: "a non-numeric turn", content: "the turn:abc marker", query: "turn:abc"},
		{name: "turn zero", content: "see turn:0 for the trace", query: "turn:0"},
		{name: "role", content: "the role:user marker", query: "role:user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, drawn := oneText(tc.content)
			hits := searchHits(entries, drawn, tc.query)
			if len(hits) != 1 || hits[0].Kind != itemMessage {
				t.Errorf("Search(%q) = %+v, want one message hit: the term is free text", tc.query, hits)
			}
		})
	}
}

func TestSearch_ExcerptCarriesContextAndCollapsesWhitespace(t *testing.T) {
	long := strings.Repeat("a ", 100) + "needle " + strings.Repeat("b ", 100)
	entries, drawn := oneText(long)
	hits := searchHits(entries, drawn, "needle")
	if len(hits) != 1 {
		t.Fatalf("Search(%q) = %d hits, want 1", "needle", len(hits))
	}
	ex := hits[0].Excerpt
	if !strings.Contains(ex, "needle") {
		t.Errorf("excerpt %q does not contain the match", ex)
	}
	if !strings.HasPrefix(ex, "\u2026") || !strings.HasSuffix(ex, "\u2026") {
		t.Errorf("excerpt %q is not marked as cut on both sides", ex)
	}
	if strings.Contains(ex, "  ") {
		t.Errorf("excerpt %q has an uncollapsed whitespace run", ex)
	}
}

// An ellipsis marks only a side actually cut, and the context radius is fixed.
func TestSearch_ExcerptMarksOnlyTheSidesItActuallyCut(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "match fills the whole text", content: "needle tail", want: "needle tail"},
		{name: "text continues past the radius", content: "needle" + strings.Repeat(" x", 100), want: "needle" + strings.Repeat(" x", 30) + "\u2026"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, drawn := oneText(tc.content)
			hits := searchHits(entries, drawn, "needle")
			if len(hits) != 1 {
				t.Fatalf("Search(%q) = %d hits, want 1", tc.content, len(hits))
			}
			if got := hits[0].Excerpt; got != tc.want {
				t.Errorf("Search(%q) excerpt = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// The hit list caps at maxSearchHits, the count does not (200 shown of 340 reads 340). Scanned is every entry of a
// drawn turn; Truncated stays false.
func TestSearch_MatchedCountsPastTheHitCap(t *testing.T) {
	cases := []struct {
		name     string
		items    int
		wantHits int
	}{
		{name: "under the cap", items: maxSearchHits - 1, wantHits: maxSearchHits - 1},
		{name: "exactly at the cap", items: maxSearchHits, wantHits: maxSearchHits},
		{name: "one past the cap", items: maxSearchHits + 1, wantHits: maxSearchHits},
		{name: "far past the cap", items: 3 * maxSearchHits, wantHits: maxSearchHits},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tf := openTurn("t-1", 1, nil)
			for i := range tc.items {
				tf.text("say-"+strconv.Itoa(i), "hit")
			}
			entries, drawn := chatOf(tf.text("a2", "nothing here"))
			res := search(entries, drawn, "hit", false)
			if len(res.Matches) != tc.wantHits {
				t.Errorf("%d items: got %d hits, want %d", tc.items, len(res.Matches), tc.wantHits)
			}
			if res.Matched != tc.items {
				t.Errorf("%d items: Matched = %d, want %d", tc.items, res.Matched, tc.items)
			}
			if res.Scanned != len(entries) {
				t.Errorf("%d items: Scanned = %d, want %d (every entry of a drawn turn is read)", tc.items, res.Scanned, len(entries))
			}
			if res.Truncated {
				t.Errorf("%d items: Truncated = true, want false: the cut is Matched > len(Matches)", tc.items)
			}
		})
	}
}

// searchEntries' byte volume covers exactly the segments read, none from filtered-out entries, matching the
// occurrence count's span set.
func TestSearchEntries_CharsAreTheSpansTheScanRead(t *testing.T) {
	entries, drawn := chatOf(
		openTurn("t-1", 1, prompt("m-1", "abc")).
			text("a1", "defg").
			thinking("th1", "hijkl").
			tool(marotte.EntryToolCall{ID: "tc1", Title: "run"},
				&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "mnopqrs"}),
		openTurn("t-2", 2, prompt("m-2", "tu")).text("a2", "vwxyz"),
	)
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "every span of every entry", query: "zzz", want: len("abc") + len("defg") + len("hijkl") + len("run") + len("mnopqrs") + len("tu") + len("vwxyz")},
		{name: "an entry a filter excludes is not read", query: "zzz turn:2", want: len("tu") + len("vwxyz")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchEntries(entries, drawn, tc.query, false).chars; got != tc.want {
				t.Errorf("searchEntries(%q) chars = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

// An undrawn turn has no card or rail row, so its entries are not scanned.
func TestSearch_AnUndrawnTurnIsSkipped(t *testing.T) {
	entries, drawn := chatOf(
		openTurn("t-1", 1, prompt("m-1", "find the needle")).text("a1", "one needle").
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}),
		openTurn("t-2", 2, nil).
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: "the needle retry came back empty"}),
	)
	delete(drawn, "t-2")
	res := search(entries, drawn, "needle", false)
	for _, h := range res.Matches {
		if h.TurnID == "t-2" {
			t.Errorf("hit %+v names the undrawn turn", h)
		}
	}
	if res.Matched != 2 || res.Scanned != 3 {
		t.Errorf("Matched = %d, Scanned = %d, want 2 and 3: the undrawn turn's two entries are neither matched nor scanned", res.Matched, res.Scanned)
	}
}

func seedSearchChat(t *testing.T, s *Store, id marotte.ChatID, name, promptText string, replies ...string) {
	t.Helper()
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + string(id), Text: promptText},
	}, func(c *marotte.Chat) { c.Name = name })
	if err != nil {
		t.Fatalf("OpenTurn(%s): %v", id, err)
	}
	for i, reply := range replies {
		e := entryOf(opened.Turn, "", opened.Turn+"-say-"+strconv.Itoa(i), marotte.EntryKindText, marotte.EntryText{Text: reply})
		if err := s.Append(t.Context(), id, e); err != nil {
			t.Fatalf("Append(text %d): %v", i, err)
		}
	}
	closeTurn(t, s, id, opened.Turn, marotte.TurnOutcomeCompleted)
}

func searchVia(t *testing.T, s *Store, id marotte.ChatID, query string) SearchResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/search"+query, nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search%s = %d, body = %s", query, rec.Code, rec.Body.String())
	}
	var body SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return body
}

// A hit's ordinal and the rail's are both turn_open's n from the same log.
func TestSearch_TurnNumbersMatchTheRailRows(t *testing.T) {
	s, _ := newTestStore(t)
	seedSearchChat(t, s, "c1", "One", "how does the retry work", "The retry uses exponential backoff.")
	seedSearchChat(t, s, "c1", "One", "now fix the composer", "Done, the composer grows upward.")
	byTurn := make(map[string]int)
	for _, row := range railRows(t, s, "c1") {
		byTurn[row.ID] = row.N
	}
	hits := searchVia(t, s, "c1", "?q=the").Matches
	if len(hits) == 0 {
		t.Fatal("the seeded chat matched nothing")
	}
	for _, h := range hits {
		if byTurn[h.TurnID] != h.Turn {
			t.Errorf("hit %+v disagrees with the rail (n=%d)", h, byTurn[h.TurnID])
		}
	}
}

// The client highlights in the DOM while this enumerates the session, so the case flag rides the request.
func TestHandleSearch_CaseParam(t *testing.T) {
	s, _ := newTestStore(t)
	seedSearchChat(t, s, "c1", "One", "now fix the composer", "Done, the Composer grows upward.")

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent is insensitive", query: "?q=composer", want: 2},
		{name: "case=1 is sensitive", query: "?q=composer&case=1", want: 1},
		{name: "case=0 is insensitive", query: "?q=composer&case=0", want: 2},
		{name: "an unrecognised value is insensitive", query: "?q=composer&case=yes", want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(searchVia(t, s, "c1", tc.query).Matches); got != tc.want {
				t.Errorf("%s: %d hits, want %d", tc.query, got, tc.want)
			}
		})
	}
}

// The handler writes the scan's reply, so a capped list arrives with its count.
func TestHandleSearch_ReportsTheTally(t *testing.T) {
	s, _ := newTestStore(t)
	replies := make([]string, maxSearchHits+1, maxSearchHits+2)
	for i := range replies {
		replies[i] = "hit"
	}
	seedSearchChat(t, s, "c1", "One", "count these", append(replies, "one lonely miss")...)

	cases := []struct {
		name        string
		query       string
		wantHits    int
		wantMatched int
	}{
		{name: "past the cap", query: "hit", wantHits: maxSearchHits, wantMatched: maxSearchHits + 1},
		{name: "under the cap", query: "lonely", wantHits: 1, wantMatched: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := searchVia(t, s, "c1", "?q="+tc.query)
			if len(body.Matches) != tc.wantHits {
				t.Errorf("q=%s: %d hits, want %d", tc.query, len(body.Matches), tc.wantHits)
			}
			if body.Matched != tc.wantMatched {
				t.Errorf("q=%s: matched = %d, want %d", tc.query, body.Matched, tc.wantMatched)
			}
			// turn_open, every reply, turn_close.
			if want := maxSearchHits + 4; body.Scanned != want {
				t.Errorf("q=%s: scanned = %d, want %d", tc.query, body.Scanned, want)
			}
			if body.Truncated {
				t.Errorf("q=%s: truncated = true, want false", tc.query)
			}
		})
	}
}

// The in-chat search deliberately skips the chat's title: SearchAll answers "which conversation", and a title hit has
// no transcript position.
func TestHandleSearch_ChatNameIsNotSearched(t *testing.T) {
	s, _ := newTestStore(t)
	seedSearchChat(t, s, "c1", "The needle investigation", "what now", "nothing to report")
	body := searchVia(t, s, "c1", "?q=needle")
	if len(body.Matches) != 0 || body.Matched != 0 {
		t.Errorf("the chat NAME matched %d times (matched=%d), want 0: %+v", len(body.Matches), body.Matched, body.Matches)
	}
}

// assertHits compares field by field to name the broken coordinate.
type wantHit struct {
	entry string
	lane  string
	kind  ItemKind
	segs  []SegmentKind
}

func assertHits(t *testing.T, hits []Hit, want []wantHit) {
	t.Helper()
	if len(hits) != len(want) {
		t.Fatalf("got %d hits, want %d: %+v", len(hits), len(want), hits)
	}
	for i, w := range want {
		h := hits[i]
		if h.EntryID != w.entry {
			t.Errorf("hit %d EntryID = %q, want %q", i, h.EntryID, w.entry)
		}
		if h.Kind != w.kind {
			t.Errorf("hit %d Kind = %q, want %q", i, h.Kind, w.kind)
		}
		if h.Lane != w.lane {
			t.Errorf("hit %d Lane = %q, want %q", i, h.Lane, w.lane)
		}
		if w.segs == nil {
			w.segs = []SegmentKind{}
		}
		if !slices.Equal(h.Segments, w.segs) {
			t.Errorf("hit %d Segments = %v, want %v", i, h.Segments, w.segs)
		}
	}
}

// The same text in the agent's and a delegate's lane is two hits, each with its own entry and lane.
func TestSearch_DistinguishesParentAndDelegateLanes(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "the needle in the parent").
		laneText("sub-1", "a2", "the needle in the delegate"))
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "a1", kind: itemMessage, segs: []SegmentKind{segmentContent}},
		{entry: "a2", lane: "sub-1", kind: itemMessage, segs: []SegmentKind{segmentContent}},
	})
}

// A tool's title and its result's output are two segments of one card: one hit, anchored on the call, in its lane.
func TestSearch_ToolTitleAndOutputAreSegmentsOfTheCallAndTheResult(t *testing.T) {
	tf := openTurn("t-1", 1, nil).text("a1", "running the search now")
	tf.add("sub-9", "tc1", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "tc1", Title: "grep needle"})
	tf.add("sub-9", "tc1:result", marotte.EntryKindToolResult, marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "found a needle here"})
	entries, drawn := chatOf(tf)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", lane: "sub-9", kind: itemTool, segs: []SegmentKind{segmentToolTitle, segmentToolOutput}},
	})
}

// A steering box draws its document names in its title and never its ids, so a name is a title hit and a path
// segment of an id finds nothing.
func TestSearch_SteeringBoxFindsItsDocumentNamesInTheTitle(t *testing.T) {
	tf := openTurn("t-1", 1, nil).text("a1", "reading the module")
	tf.add("sub-9", "g1", marotte.EntryKindToolCall, marotte.EntryToolCall{
		ID:     "steering-1",
		Title:  "Steering loaded: git-commits.md, testing.md",
		Kind:   marotte.ToolKindSteering,
		Status: marotte.ToolCompleted,
		SteeringDocs: []string{
			"file:///w/.kiro/steering/git-commits.md",
			"file:///w/.kiro/steering/testing.md",
		},
	})
	entries, drawn := chatOf(tf)
	assertHits(t, searchHits(entries, drawn, "testing.md"), []wantHit{
		{entry: "g1", lane: "sub-9", kind: itemTool, segs: []SegmentKind{segmentToolTitle}},
	})
	if hits := searchHits(entries, drawn, ".kiro/steering"); len(hits) != 0 {
		t.Errorf("Search found %d hits in a steering box's ids, want 0: %+v", len(hits), hits)
	}
}

// A result's new_text is its own segment; the diff path is not searched (`file:` and the title reach it).
func TestSearch_DiffNewTextIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{Path: "needle.go", NewText: "åß needle"}}},
	)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolDiff}},
	})
}

// new_text only: 97.4% of old_text's lines repeat there. A removed line is not findable through the diff.
func TestSearch_DiffOldTextIsNotSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{
			OldText: "the needle used to live here", NewText: "and now it does not",
		}}},
	)
	if hits := searchHits(entries, drawn, "needle"); len(hits) != 0 {
		t.Errorf("Search found %d hits in a diff's old_text, want 0: %+v", len(hits), hits)
	}
}

// A delete's empty new_text adds no segment; the one hit is the title.
func TestSearch_DiffWithEmptyNewTextContributesNoSegment(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Delete needle.go"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{OldText: "the needle used to live here"}}},
	)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolTitle}},
	})
}

// Only Diffs[0]: nothing renders a second diff, so a hit there would have no destination.
func TestSearch_OnlyTheFirstDiffIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{
			{Path: "a.go", NewText: "first needle"},
			{Path: "b.go", NewText: "second needle"},
		}},
	)
	hits := searchHits(entries, drawn, "needle")
	assertHits(t, hits, []wantHit{{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolDiff}}})
	if hits[0].Excerpt != "first needle" {
		t.Errorf("excerpt = %q, want the first diff's text", hits[0].Excerpt)
	}
}

// inputCall builds a tool_call whose only searchable span is its input.
func inputCall(input string) marotte.EntryToolCall {
	return marotte.EntryToolCall{ID: "tc1", Title: "Write File", Input: json.RawMessage(input)}
}

// Only string leaf values are searched, at any depth; keys, numbers and bools are not.
func TestSearch_ToolInputSearchesStringLeavesOnly(t *testing.T) {
	tests := []struct {
		name  string
		query string
		input string
		want  []wantHit
	}{{
		name:  "a needle in a key yields nothing",
		input: `{"needle":"a value"}`,
	}, {
		name:  "a needle in a string value is a tool_input hit",
		input: `{"cmd":"grep needle here"}`,
		want:  []wantHit{{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolInput}}},
	}, {
		// The query is the bool's spelling; `true` would match every flag.
		name:  "a bool leaf is not searched",
		query: "true",
		input: `{"recurse":true}`,
	}, {
		name:  "a number leaf is not searched",
		query: "42",
		input: `{"limit":42}`,
	}, {
		name:  "a string nested in an array of objects is covered",
		input: `{"a":"outer","edits":[{"newStr":"deep needle"}],"z":"tail"}`,
		want:  []wantHit{{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolInput}}},
	}, {
		// Array elements are values, so alternating key/value in an array would drop every second path.
		name:  "every element of a string array is covered",
		input: `{"paths":["a.go","needle.go"]}`,
		want:  []wantHit{{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolInput}}},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			if query == "" {
				query = "needle"
			}
			entries, drawn := oneTool(inputCall(tc.input), nil)
			hits := searchHits(entries, drawn, query)
			if tc.want == nil {
				if len(hits) != 0 {
					t.Errorf("Search(%q) found %d hits, want 0: %+v", query, len(hits), hits)
				}
				return
			}
			assertHits(t, hits, tc.want)
		})
	}
}

// Leaves join in document order, the card's order; reverse-alphabetical keys rule out a sorted walk.
func TestSearch_ToolInputLeafOrderIsDocumentOrder(t *testing.T) {
	entries, drawn := oneTool(inputCall(`{"z":"needle first","a":"and then a needle"}`), nil)
	hits := searchHits(entries, drawn, "needle")
	assertHits(t, hits, []wantHit{{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolInput}}})
	if hits[0].Excerpt != "needle first and then a needle" {
		t.Errorf("excerpt = %q, want the leaves in document order", hits[0].Excerpt)
	}
}

// An edit's payload appears as the input's newStr and as the result's diff, one rendered write, so only the diff
// segment holds it.
func TestSearch_InputLeafDoesNotDoubleCountItsDiff(t *testing.T) {
	const payload = "func fetch(ctx context.Context) error { return needle(ctx) }"
	if len(payload) < inputLeafDedupeMin {
		t.Fatalf("payload is %d bytes, under inputLeafDedupeMin (%d): the skip would not apply", len(payload), inputLeafDedupeMin)
	}
	entries, drawn := oneTool(
		marotte.EntryToolCall{
			ID: "tc1", Title: "Replace in File",
			Input: json.RawMessage(`{"path":"fetch.go","newStr":` + strconv.Quote(payload) + `}`),
		},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{Path: "fetch.go", NewText: payload}}},
	)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolDiff}},
	})
}

// A `null` input (unparseable and unshortenable) and an absent one yield no segment and no error; malformed bytes
// cannot reach a persisted payload.
func TestSearch_MissingInputYieldsNoHit(t *testing.T) {
	for _, input := range []string{`null`, ``} {
		entries, drawn := oneTool(inputCall(input), nil)
		if hits := searchHits(entries, drawn, "needle"); len(hits) != 0 {
			t.Errorf("Search found %d hits in input %q, want 0: %+v", len(hits), input, hits)
		}
	}
}

// The leaf walker returns "" for unparseable bytes, a truncated document included.
func TestInputLeafText_MalformedBytesYieldNoText(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "truncated object", input: `{"cmd":"needle"`},
		{name: "unterminated object", input: `{`},
		{name: "bare word", input: `needle`},
		{name: "trailing garbage past a valid object", input: `{"cmd":"needle"} needle`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputLeafText(json.RawMessage(tc.input), ""); got != "" {
				t.Errorf("inputLeafText(%q) = %q, want empty", tc.input, got)
			}
		})
	}
}

// A plan renders as one card, each of its entries a segment.
func TestSearch_PlanIsSearched(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "starting now").
		add("", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
			{Content: "Read the needle", Status: marotte.PlanCompleted},
			{Content: "Fix the needle", Status: marotte.PlanPending},
		}}))
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "plan-1", kind: itemPlan, segs: []SegmentKind{segmentPlan}},
	})
}

// A denial's resource (the refused command or path) is its one reader-facing, searched field.
func TestSearch_DenialResourceIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Run Command"},
		&marotte.EntryToolResult{Status: marotte.ToolFailed, Denial: &marotte.ToolDenial{
			Capability: "shell", Resource: "rm -rf needle", Scope: "user", Source: "permissions.yaml",
		}},
	)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolDenial}},
	})
}

// A turn_close's failure reason renders in the footer, so it is searched.
func TestSearch_TurnFailureReasonIsSearched(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "partial answer").
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: "the needle budget ran out"}))
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "t-1:close", kind: itemTurnFailure, segs: []SegmentKind{segmentTurnFailure}},
	})
}

// A steer's text is a steer item, agent-origin notes included.
func TestSearch_SteerTextIsSearched(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "working on it").
		add("", "steer-1", marotte.EntryKindSteer, marotte.EntrySteer{
			Text: "also check the needle", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
		}))
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "steer-1", kind: itemSteer, segs: []SegmentKind{segmentSteer}},
	})
}

// A compaction failure's reason and a safety block's properties render as text, so each is a notice.
func TestSearch_CompactionFailedAndSafetyBlockedAreNotices(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		add("", "cf-1", marotte.EntryKindCompactionFailed, marotte.EntryCompactionFailed{Reason: "needle too large"}).
		add("", "sb-1", marotte.EntryKindSafetyBlocked, marotte.EntrySafetyBlocked{Properties: []string{"deletes data", "moves the needle"}}))
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "cf-1", kind: itemNotice, segs: []SegmentKind{segmentContent}},
		{entry: "sb-1", kind: itemNotice, segs: []SegmentKind{segmentContent}},
	})
}

// A model fallback's models and KAS's codes are one notice, so find-in-chat lands on its row.
func TestSearch_ModelFallbackIsANotice(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		add("", "fb-1", marotte.EntryKindModelFallback, marotte.EntryModelFallback{From: "opus-5", Trigger: "availability", Reason: "chainExhausted"}))
	assertHits(t, searchHits(entries, drawn, "exhausted"), []wantHit{
		{entry: "fb-1", kind: itemNotice, segs: []SegmentKind{segmentContent}},
	})
}

// Attachments are searched by name, not by the path in the pill's `title` attribute; both directions asserted.
func TestSearch_AttachmentNameIsSearchedAndPathIsNot(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, &marotte.EntryPrompt{
		ID: "m-1", Text: "have a look",
		Attachments: []marotte.Attachment{{Path: "docs/haystack/notes.md", Name: "needle-notes.md"}},
	}))
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "t-1", kind: itemHeader, segs: []SegmentKind{segmentAttachment}},
	})
	if hits := searchHits(entries, drawn, "haystack"); len(hits) != 0 {
		t.Errorf("the attachment PATH matched %d times, want 0: %+v", len(hits), hits)
	}
}

// A `disclose_context` result's display name replaces the title on the card, so it is searched; URI and Type are
// not.
func TestSearch_DisclosedDisplayNameIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Disclose Context"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Disclosed: &marotte.ToolDisclosed{
			Type: "skill", DisplayName: "needle-review", URI: "file:///workspace/.kiro/skills/haystack/SKILL.md",
		}},
	)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{segmentToolDisclosed}},
	})
	if hits := searchHits(entries, drawn, "haystack"); len(hits) != 0 {
		t.Errorf("the disclosed URI matched %d times, want 0: %+v", len(hits), hits)
	}
}

// The deliberately unsearched fields, each asserted where a needle would otherwise look like a gap.
func TestSearch_UnsearchedFieldsStayUnsearched(t *testing.T) {
	tests := []struct {
		name string
		why  string
		turn func() *turnFixture
	}{
		{
			name: "ToolDiff.OldText",
			why:  "a line the edit REMOVED has no rendered surface in the card's mini-diff",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).tool(marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
					&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{Path: "f.go", OldText: "gone needle", NewText: "kept"}}})
			},
		},
		{
			name: "the second diff",
			why:  "nothing renders or fetches Diffs[1], so a hit there is counted-but-unreachable",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).tool(marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
					&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{
						{Path: "a.go", NewText: "first"}, {Path: "b.go", NewText: "second needle"},
					}})
			},
		},
		{
			name: "Attachment.Path",
			why:  "the path lives in a title ATTRIBUTE the DOM walker cannot mark",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, &marotte.EntryPrompt{
					ID: "m-1", Text: "look",
					Attachments: []marotte.Attachment{{Path: "needle/notes.md", Name: "notes.md"}},
				})
			},
		},
		{
			name: "Denial.Capability and the rule's patterns",
			why:  "a closed vocabulary and policy text editable through Settings, not reader-facing text",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).tool(marotte.EntryToolCall{ID: "tc1", Title: "Run Command"},
					&marotte.EntryToolResult{Status: marotte.ToolFailed, Denial: &marotte.ToolDenial{
						Capability: "needle", Resource: "rm -rf /",
						Rule: &marotte.ToolDenialRule{Capability: "shell", Effect: "deny", Match: []string{"needle*"}, Exclude: []string{"needle-safe"}},
					}})
			},
		},
		{
			name: "Disclosed.URI",
			why:  "the card renders the display name, never the uri",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).tool(marotte.EntryToolCall{ID: "tc1", Title: "Disclose Context"},
					&marotte.EntryToolResult{Status: marotte.ToolCompleted, Disclosed: &marotte.ToolDisclosed{
						Type: "skill", DisplayName: "review", URI: "file:///needle/SKILL.md",
					}})
			},
		},
		{
			name: "turn_close.code_references",
			why:  "attributions are TURN-scoped; KAS drops the span that would locate one",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).close(marotte.EntryTurnClose{
					Outcome:        marotte.TurnOutcomeCompleted,
					CodeReferences: []marotte.CodeReference{{LicenseName: "needle", Repository: "needle/repo"}},
				})
			},
		},
		{
			name: "ToolCall.Locations[].Path",
			why:  "already reachable through the `file:` filter and through the title",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).tool(marotte.EntryToolCall{
					ID: "tc1", Title: "Read File",
					Locations: []marotte.ToolLocation{{Path: "needle.go", Line: 12}},
				}, nil)
			},
		},
		{
			name: "the compaction summary",
			why:  "the model's own account of history already searchable at its source",
			turn: func() *turnFixture {
				return openTurn("t-1", 1, nil).add("", "compaction-1", marotte.EntryKindCompaction,
					marotte.EntryCompaction{Summary: "the needle was moved"})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, drawn := chatOf(tc.turn())
			if hits := searchHits(entries, drawn, "needle"); len(hits) != 0 {
				t.Errorf("%s is searched (%d hits) but %s: %+v", tc.name, len(hits), tc.why, hits)
			}
		})
	}
}

// A tool card's segments follow the card: title and input on the tool_call, then disclosed, diff, denial and output
// on the tool_result.
func TestSearch_SegmentOrderFollowsTheRenderedCard(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "grep needle", Input: json.RawMessage(`{"cmd":"a needle in the input"}`)},
		&marotte.EntryToolResult{
			Status:    marotte.ToolCompleted,
			Output:    "found a needle here",
			Disclosed: &marotte.ToolDisclosed{Type: "skill", DisplayName: "the needle skill"},
			Denial:    &marotte.ToolDenial{Capability: "shell", Resource: "a needle to deny"},
			Diffs:     []marotte.ToolDiff{{Path: "fetch.go", NewText: "a needle in the diff"}},
		},
	)
	assertHits(t, searchHits(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: itemTool, segs: []SegmentKind{
			segmentToolTitle, segmentToolInput, segmentToolDisclosed, segmentToolDiff, segmentToolDenial, segmentToolOutput,
		}},
	})
}

// Hits come in file order across entries and interleaved turns, not grouped by kind.
func TestSearch_HitsFollowFileOrder(t *testing.T) {
	a := openTurn("t-1", 1, prompt("m-1", "needle one")).text("a1", "needle two")
	b := openTurn("t-2", 2, nil).text("b1", "needle three").close(marotte.EntryTurnClose{
		Outcome: marotte.TurnOutcomeFailed, FailureReason: "needle four",
	})
	a.add("", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{{Content: "needle five"}}})
	// Interleave: t-1's entries, then t-2's, then t-1's late plan.
	entries := append(append([]marotte.Entry{}, a.entries[:2]...), b.entries...)
	entries = append(entries, a.entries[2])
	drawn := map[string]struct{}{"t-1": {}, "t-2": {}}
	var got []string
	for _, h := range searchHits(entries, drawn, "needle") {
		got = append(got, h.EntryID+"/"+string(h.Kind))
	}
	want := []string{"t-1/header", "a1/message", "b1/message", "t-2:close/turn_failure", "plan-1/plan"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("hit order = %v, want %v", got, want)
	}
}

// Every kind in segmentKinds has a reachable producer.
func TestSearch_SegmentKindsAreExhaustive(t *testing.T) {
	entries, drawn := searchContractEntries()
	seen := make(map[SegmentKind]int)
	hits := searchHits(entries, drawn, "retry")
	if len(hits) == 0 {
		t.Fatal("Search(retry) found nothing; it can vouch for no kind at all")
	}
	for _, h := range hits {
		for _, k := range h.Segments {
			seen[k]++
		}
	}
	for _, kind := range segmentKinds {
		if seen[kind] == 0 {
			t.Errorf("segment kind %q has no producer: a declared kind the client can never be handed", kind)
		}
	}
}

// A filter-only query yields one hit per kept item, with no segments; a textless call's excerpt is its title.
func TestSearch_FilterOnlyHitsAreItems(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "prose here").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "shell"}, nil))
	hits := searchHits(entries, drawn, "turn:1")
	assertHits(t, hits, []wantHit{
		{entry: "a1", kind: itemMessage},
		{entry: "tc1", kind: itemTool},
	})
	if hits[1].Excerpt != "shell" {
		t.Errorf("tool-only item excerpt = %q, want its title", hits[1].Excerpt)
	}
}

// lane is optional on the generated type, so the encoder omits it when unset; segments is always a list.
func TestHit_WireShape(t *testing.T) {
	full, err := json.Marshal(Hit{
		TurnID: "t-1", EntryID: "tc1", Kind: itemTool, Segments: []SegmentKind{segmentToolOutput}, Lane: "sub-1", Turn: 3,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"turn_id":"t-1"`, `"entry_id":"tc1"`, `"kind":"tool"`, `"segments":["tool_output"]`, `"lane":"sub-1"`, `"turn":3`} {
		if !strings.Contains(string(full), key) {
			t.Errorf("marshalled hit %s lacks %s", full, key)
		}
	}
	minimal, err := json.Marshal(Hit{TurnID: "t-1", EntryID: "a1", Kind: itemMessage, Segments: []SegmentKind{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(minimal), "lane") || !strings.Contains(string(minimal), `"segments":[]`) {
		t.Errorf("marshalled agent-lane hit %s: want lane omitted and segments an empty list", minimal)
	}
	for _, key := range []string{"offset", "segment_len", "segment_kind"} {
		if strings.Contains(string(full), key) {
			t.Errorf("marshalled hit %s carries the deleted %s", full, key)
		}
	}
}

// A hit in a prompt or steer an API caller sent names that caller; a hit in the user's words or the agent's names none.
func TestSearch_NamesTheAPICallerWhoseWordsMatched(t *testing.T) {
	sent := prompt("m-1", "relay: build failed")
	sent.Caller = &marotte.Caller{Label: "Agent Mail"}
	entries, drawn := chatOf(
		openTurn("t-1", 1, sent).
			add("", "steer-1", marotte.EntryKindSteer, marotte.EntrySteer{
				Text: "relay: build fixed", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead, Caller: &marotte.Caller{},
			}).
			text("a1", "the relay says the build is fine"),
		openTurn("t-2", 2, prompt("m-2", "what did the relay say")),
	)

	hits := searchHits(entries, drawn, "relay")
	if len(hits) != 4 {
		t.Fatalf("Search = %d hits, want 4: %+v", len(hits), hits)
	}
	want := map[string]*marotte.Caller{"t-1": {Label: "Agent Mail"}, "steer-1": {}, "a1": nil, "t-2": nil}
	for _, h := range hits {
		w := want[h.EntryID]
		if (h.Caller == nil) != (w == nil) || (w != nil && h.Caller.Label != w.Label) {
			t.Errorf("hit on %s caller = %+v, want %+v", h.EntryID, h.Caller, w)
		}
	}
}
