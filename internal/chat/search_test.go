package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// turnFixture accumulates one turn's entries in file order, seq assigned as the
// appender would.
type turnFixture struct {
	id      string
	entries []marotte.Entry
}

// openTurn starts a turn whose turn_open carries the prompt and the ordinal n.
func openTurn(id string, n uint64, prompt *marotte.EntryPrompt) *turnFixture {
	tf := &turnFixture{id: id}
	return tf.add("", id, marotte.EntryKindTurnOpen, marotte.EntryTurnOpen{Prompt: prompt, Source: marotte.TurnOpenNamePrompt, N: n})
}

// prompt is a turn_open prompt with no attachments.
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

// tool appends the tool_call and, when res is non-nil, its tool_result.
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

// chatOf joins turns in file order with every turn drawn.
func chatOf(turns ...*turnFixture) ([]marotte.Entry, map[string]struct{}) {
	var entries []marotte.Entry
	drawn := make(map[string]struct{}, len(turns))
	for _, tf := range turns {
		entries = append(entries, tf.entries...)
		drawn[tf.id] = struct{}{}
	}
	return entries, drawn
}

// oneText is a one-turn chat holding a single text entry.
func oneText(s string) ([]marotte.Entry, map[string]struct{}) {
	return chatOf(openTurn("t-1", 1, nil).text("a1", s))
}

// oneTool is a one-turn chat holding one tool_call and its tool_result.
func oneTool(call marotte.EntryToolCall, res *marotte.EntryToolResult) ([]marotte.Entry, map[string]struct{}) {
	return chatOf(openTurn("t-1", 1, nil).tool(call, res))
}

// search runs Search over a fixture with the case flag off.
func search(entries []marotte.Entry, drawn map[string]struct{}, q string) []Hit {
	return Search(entries, drawn, q, false).Matches
}

// transcript: turn 1 asks about the retry, turn 2 about the composer.
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
	hits := search(entries, drawn, "composer")
	if len(hits) != 2 {
		t.Fatalf("Search(%q) = %d hits, want 2 (the prompt and the reply)", "composer", len(hits))
	}
	if hits[0].TurnID != "t-2" || hits[0].EntryID != "t-2" || hits[0].Turn != 2 || hits[0].SegmentKind != SegmentPrompt {
		t.Errorf("hit 0 = %+v, want the prompt of t-2 in turn 2", hits[0])
	}
	if hits[1].TurnID != "t-2" || hits[1].EntryID != "a2" || hits[1].Turn != 2 {
		t.Errorf("hit 1 = %+v, want a2 in turn t-2 (ordinal 2)", hits[1])
	}
}

// The match-case flag governs the FREE TEXT only. The scoped filters stay
// case-insensitive whatever the reader asked for: a path is typed from memory,
// and a filter that suddenly cared about case would be a behaviour change nobody
// requested by ticking a box labelled "match case".
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
			if got := len(Search(tc.entries, tc.drawn, tc.query, tc.caseSensitive).Matches); got != tc.want {
				t.Errorf("Search(%q, case=%v) = %d hits, want %d", tc.query, tc.caseSensitive, got, tc.want)
			}
		})
	}
}

// The rune-offset arithmetic reads a prefix of the HAYSTACK, which is the folded
// string in insensitive mode and the original in sensitive mode; both must land
// on the same rune index for the client to highlight the right occurrence.
func TestSearch_OffsetsAreRuneIndicesInBothCaseModes(t *testing.T) {
	entries, drawn := oneText("héllo wörld Needle")
	for _, cs := range []bool{false, true} {
		hits := Search(entries, drawn, "Needle", cs).Matches
		if len(hits) != 1 {
			t.Fatalf("Search(case=%v) = %d hits, want 1", cs, len(hits))
		}
		if want := len([]rune("héllo wörld ")); hits[0].Offset != want {
			t.Errorf("Search(case=%v) offset = %d, want %d (a rune index, not a byte)", cs, hits[0].Offset, want)
		}
	}
}

// Offsets are relative to the matched ENTRY's own segment, never to the turn,
// so a tool call between two text entries does not shift the second one.
func TestSearch_EveryOccurrenceIsAHitAtItsOwnOffset(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "intro paragraph").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "shell"}, nil).
		text("a2", "retry retry retry"))
	hits := search(entries, drawn, "retry")
	if len(hits) != 3 {
		t.Fatalf("Search(%q) = %d hits, want 3", "retry", len(hits))
	}
	for i, want := range []int{0, 6, 12} {
		if hits[i].EntryID != "a2" || hits[i].Offset != want || hits[i].SegmentLen != 17 {
			t.Errorf("hit %d = %+v, want a2 at offset %d of 17", i, hits[i], want)
		}
	}
}

func TestSearch_EmptyQueryFindsNothing(t *testing.T) {
	entries, drawn := transcript()
	for _, q := range []string{"", "   "} {
		if got := search(entries, drawn, q); len(got) != 0 {
			t.Errorf("Search(%q) = %d hits, want 0", q, len(got))
		}
	}
}

// An empty result must marshal as [] rather than null, so the client has one
// empty case instead of two.
func TestSearch_NeverReturnsNil(t *testing.T) {
	entries, drawn := transcript()
	if search(entries, drawn, "") == nil {
		t.Error("an empty query returned a nil slice")
	}
	if Search(nil, nil, "anything", false).Matches == nil {
		t.Error("an empty log returned a nil slice")
	}
}

// "Which turn printed that error" is asked more often than "which turn
// mentioned it", so a tool_result's output is searched, and so is the thinking.
func TestSearch_SearchesThinkingAndToolOutput(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		thinking("th1", "considering a mutex here").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "shell"},
			&marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "permission denied"}))
	if hits := search(entries, drawn, "permission denied"); len(hits) != 1 || hits[0].SegmentKind != SegmentToolOutput || hits[0].EntryID != "tc1:result" {
		t.Errorf("Search(%q) = %+v, want one tool_output hit on tc1:result", "permission denied", hits)
	}
	if hits := search(entries, drawn, "mutex"); len(hits) != 1 || hits[0].SegmentKind != SegmentReasoning {
		t.Errorf("Search(%q) = %+v, want one reasoning hit", "mutex", hits)
	}
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
		hits := search(entries, drawn, "turn:2")
		if len(hits) == 0 {
			t.Fatal("turn:2 matched nothing")
		}
		for _, h := range hits {
			if h.Turn != 2 || h.TurnID != "t-2" {
				t.Errorf("turn:2 returned a hit in turn %d (%s)", h.Turn, h.TurnID)
			}
		}
	})

	// A file only READ has a location and no diff, and "the turn where you
	// looked at auth.go" is a real question, so locations count too; a WRITE
	// is found through its result's diff path.
	t.Run("file matches a read as well as a write", func(t *testing.T) {
		got := search(entries, drawn, "file:token.go")
		if len(got) != 2 || got[0].EntryID != "tc1" || got[1].EntryID != "tc1:result" {
			t.Errorf("file:token.go = %+v, want the reading call and its result", got)
		}
		got = search(entries, drawn, "file:composer.ts")
		if len(got) != 1 || got[0].EntryID != "tc2:result" {
			t.Errorf("file:composer.ts = %+v, want the writing result alone", got)
		}
	})

	t.Run("tool matches title or kind", func(t *testing.T) {
		if got := search(entries, drawn, "tool:readFile"); len(got) != 2 {
			t.Errorf("tool:readFile = %d hits, want 2 (the call and its result)", len(got))
		}
		if got := search(entries, drawn, "tool:edit"); len(got) != 2 {
			t.Errorf("tool:edit = %d hits by kind, want 2", len(got))
		}
	})

	t.Run("filters combine", func(t *testing.T) {
		if got := search(entries, drawn, "tool:read turn:1"); len(got) != 2 {
			t.Errorf("tool:read turn:1 = %+v, want the two tc1 entries", got)
		}
		// A filter that excludes everything returns nothing rather than
		// ignoring itself.
		if got := search(entries, drawn, "tool:read turn:99"); len(got) != 0 {
			t.Errorf("an impossible combination returned %d hits", len(got))
		}
	})

	t.Run("filter plus free text", func(t *testing.T) {
		if got := search(entries, drawn, "turn:1 auth"); len(got) != 2 || got[0].EntryID != "t-1" || got[1].EntryID != "tc1:result" {
			t.Errorf("turn:1 auth = %+v, want the prompt and the output of turn 1", got)
		}
	})
}

// A colon-bearing term that names no filter stays text: a URL is meant
// literally, `turn:abc` and `turn:0` name no turn (turns count from 1), and
// `role:` is not a filter over entries.
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
			hits := search(entries, drawn, tc.query)
			if len(hits) != 1 || hits[0].SegmentKind != SegmentContent {
				t.Errorf("Search(%q) = %+v, want one content hit: the term is free text", tc.query, hits)
			}
		})
	}
}

func TestSearch_ExcerptCarriesContextAndCollapsesWhitespace(t *testing.T) {
	long := strings.Repeat("a ", 100) + "needle " + strings.Repeat("b ", 100)
	entries, drawn := oneText(long)
	hits := search(entries, drawn, "needle")
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

// An excerpt marks a cut with an ellipsis and carries a fixed radius of context
// around the match. A mark on an uncut side claims text was dropped when none
// was, and a short radius silently loses context the reader needs.
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
			hits := search(entries, drawn, "needle")
			if len(hits) != 1 {
				t.Fatalf("Search(%q) = %d hits, want 1", tc.content, len(hits))
			}
			if got := hits[0].Excerpt; got != tc.want {
				t.Errorf("Search(%q) excerpt = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// The hit LIST is cut at maxSearchHits and the COUNT is not: a reader shown 200
// hits out of 340 is told 340, so the cap reads as a floor rather than a total.
// Scanned is every entry of a drawn turn, and Truncated stays false because
// everything was read; the cut is stated by Matched exceeding the list.
func TestSearch_MatchedCountsPastTheHitCap(t *testing.T) {
	cases := []struct {
		name        string
		occurrences int
		wantHits    int
	}{
		{name: "under the cap", occurrences: maxSearchHits - 1, wantHits: maxSearchHits - 1},
		{name: "exactly at the cap", occurrences: maxSearchHits, wantHits: maxSearchHits},
		{name: "one past the cap", occurrences: maxSearchHits + 1, wantHits: maxSearchHits},
		{name: "far past the cap", occurrences: 3 * maxSearchHits, wantHits: maxSearchHits},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, drawn := chatOf(openTurn("t-1", 1, nil).
				text("a1", strings.Repeat("hit ", tc.occurrences)).
				text("a2", "nothing here"))
			res := Search(entries, drawn, "hit", false)
			if len(res.Matches) != tc.wantHits {
				t.Errorf("%d occurrences: got %d hits, want %d", tc.occurrences, len(res.Matches), tc.wantHits)
			}
			if res.Matched != tc.occurrences {
				t.Errorf("%d occurrences: Matched = %d, want %d", tc.occurrences, res.Matched, tc.occurrences)
			}
			if res.Scanned != len(entries) {
				t.Errorf("%d occurrences: Scanned = %d, want %d (every entry of a drawn turn is read)", tc.occurrences, res.Scanned, len(entries))
			}
			if res.Truncated {
				t.Errorf("%d occurrences: Truncated = true, want false: the cut is Matched > len(Matches)", tc.occurrences)
			}
		})
	}
}

// searchEntries' second result is the byte volume of exactly the segments the
// scan read: every span every entry exposes, and nothing from an entry a filter
// excluded. Cross-chat ranking divides its occurrence count by this, so the two
// must cover one span set.
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
			if _, got := searchEntries(entries, drawn, tc.query, false); got != tc.want {
				t.Errorf("searchEntries(%q) chars = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

// A turn the rail does not draw has no card and no rail row, so its segments
// are skipped: a hit there would name an n nothing shows and an entry the
// client cannot find. Its entries are not scanned either.
func TestSearch_AnUndrawnTurnIsSkipped(t *testing.T) {
	entries, drawn := chatOf(
		openTurn("t-1", 1, prompt("m-1", "find the needle")).text("a1", "one needle").
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}),
		openTurn("t-2", 2, nil).
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: "the needle retry came back empty"}),
	)
	delete(drawn, "t-2")
	res := Search(entries, drawn, "needle", false)
	for _, h := range res.Matches {
		if h.TurnID == "t-2" {
			t.Errorf("hit %+v names the undrawn turn", h)
		}
	}
	if res.Matched != 2 || res.Scanned != 3 {
		t.Errorf("Matched = %d, Scanned = %d, want 2 and 3: the undrawn turn's two entries are neither matched nor scanned", res.Matched, res.Scanned)
	}
}

// seedSearchChat writes one closed prompt turn into a store: the header named
// name, the prompt, one text entry per reply, and the close. It answers the turn id.
func seedSearchChat(t *testing.T, s *Store, id marotte.ChatID, name, promptText string, replies ...string) string {
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
	return opened.Turn
}

// searchVia drives GET /api/chats/{id}/search and decodes the reply.
func searchVia(t *testing.T, s *Store, id marotte.ChatID, query string) SearchResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/search"+query, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search%s = %d, body = %s", query, rec.Code, rec.Body.String())
	}
	var body SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return body
}

// The turn ordinal a hit reports and the one the rail draws are both the
// turn_open's n, read off the same log, so they cannot disagree.
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

// Both halves of the in-chat search have to agree on the match-case toggle: the
// client highlights in the DOM while this enumerates session-wide, so the flag
// rides the request rather than being a default either side could get wrong.
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

// The handler writes the scan's own reply, so the tally reaches the wire: a cut
// list arrives beside the count that says it was cut.
func TestHandleSearch_ReportsTheTally(t *testing.T) {
	s, _ := newTestStore(t)
	seedSearchChat(t, s, "c1", "One", "count these", strings.Repeat("hit ", maxSearchHits+1), "one lonely miss")

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
			// turn_open, two text entries, turn_close.
			if body.Scanned != 4 {
				t.Errorf("q=%s: scanned = %d, want 4", tc.query, body.Scanned)
			}
			if body.Truncated {
				t.Errorf("q=%s: truncated = true, want false", tc.query)
			}
		})
	}
}

// The chat's TITLE is not part of the in-chat search, and the handler is the
// layer that can say so: Search sees entries and never a name. It is a DECISION,
// not an omission: SearchAll already answers "which conversation", and a title
// hit names no position inside a transcript for the client to navigate to.
func TestHandleSearch_ChatNameIsNotSearched(t *testing.T) {
	s, _ := newTestStore(t)
	seedSearchChat(t, s, "c1", "The needle investigation", "what now", "nothing to report")
	body := searchVia(t, s, "c1", "?q=needle")
	if len(body.Matches) != 0 || body.Matched != 0 {
		t.Errorf("the chat NAME matched %d times (matched=%d), want 0: %+v", len(body.Matches), body.Matched, body.Matches)
	}
}

// wantHit is the segment-addressing half of an expected hit; assertHits checks it
// field by field so a failure names the exact coordinate that broke.
type wantHit struct {
	entry      string
	lane       string
	kind       SegmentKind
	offset     int
	segmentLen int
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
		if h.SegmentKind != w.kind {
			t.Errorf("hit %d SegmentKind = %q, want %q", i, h.SegmentKind, w.kind)
		}
		if h.Lane != w.lane {
			t.Errorf("hit %d Lane = %q, want %q", i, h.Lane, w.lane)
		}
		if h.Offset != w.offset {
			t.Errorf("hit %d Offset = %d, want %d", i, h.Offset, w.offset)
		}
		if h.SegmentLen != w.segmentLen {
			t.Errorf("hit %d SegmentLen = %d, want %d", i, h.SegmentLen, w.segmentLen)
		}
	}
}

// The same text in the agent's lane and a delegate's lane is two different
// places: each hit names its own entry and lane, and both offsets are relative
// to their OWN segment, so the two identical prefixes yield identical offsets.
func TestSearch_DistinguishesParentAndDelegateLanes(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "the needle in the parent").
		laneText("sub-1", "a2", "the needle in the delegate"))
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "a1", kind: SegmentContent, offset: 4, segmentLen: 24},
		{entry: "a2", lane: "sub-1", kind: SegmentContent, offset: 4, segmentLen: 26},
	})
}

// A tool call's title is a segment of its tool_call entry and its output a
// segment of the tool_result: two entries, two hits, each offset relative to
// its own span, both in the call's lane.
func TestSearch_ToolTitleAndOutputAreSegmentsOfTheCallAndTheResult(t *testing.T) {
	tf := openTurn("t-1", 1, nil).text("a1", "running the search now")
	tf.add("sub-9", "tc1", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "tc1", Title: "grep needle"})
	tf.add("sub-9", "tc1:result", marotte.EntryKindToolResult, marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "found a needle here"})
	entries, drawn := chatOf(tf)
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", lane: "sub-9", kind: SegmentToolTitle, offset: 5, segmentLen: 11},
		{entry: "tc1:result", lane: "sub-9", kind: SegmentToolOutput, offset: 8, segmentLen: 19},
	})
}

// A diff-bearing result's new_text is a segment of its own carrying a RUNE
// offset. The diff's PATH is not searched: it is reachable through `file:` and
// through the title, so a needle in the path yields nothing.
func TestSearch_DiffNewTextIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{Path: "needle.go", NewText: "åß needle"}}},
	)
	// "åß " is 3 runes (5 bytes); the whole segment is 9 runes (11 bytes).
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1:result", kind: SegmentToolDiff, offset: 3, segmentLen: 9},
	})
}

// new_text only: 97.4% of old_text's lines are also in new_text, so searching
// both would mint a second hit for one rendered line. The stated loss is exactly
// this: a line the edit REMOVED is not findable through the diff.
func TestSearch_DiffOldTextIsNotSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{
			OldText: "the needle used to live here", NewText: "and now it does not",
		}}},
	)
	if hits := search(entries, drawn, "needle"); len(hits) != 0 {
		t.Errorf("Search found %d hits in a diff's old_text, want 0: %+v", len(hits), hits)
	}
}

// A file DELETE has an empty new_text, so it contributes no segment at all: the
// title is the only span, so the one hit is a title hit.
func TestSearch_DiffWithEmptyNewTextContributesNoSegment(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Delete needle.go"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{{OldText: "the needle used to live here"}}},
	)
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: SegmentToolTitle, offset: 7, segmentLen: 16},
	})
}

// Only Diffs[0] is searched, because nothing renders or fetches a second diff:
// a hit past the first would be a counted match with no destination.
func TestSearch_OnlyTheFirstDiffIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Replace in File"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Diffs: []marotte.ToolDiff{
			{Path: "a.go", NewText: "first needle"},
			{Path: "b.go", NewText: "second needle"},
		}},
	)
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1:result", kind: SegmentToolDiff, offset: 6, segmentLen: 12},
	})
}

// inputCall builds a tool_call whose only searchable span is its input.
func inputCall(input string) marotte.EntryToolCall {
	return marotte.EntryToolCall{ID: "tc1", Title: "Write File", Input: json.RawMessage(input)}
}

// Only the string LEAF VALUES of an input are searched: a needle in a KEY is not
// text a reader searches for, a number or a bool is not either, and every string
// at any depth is covered so a nested array element is reachable.
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
		want:  []wantHit{{entry: "tc1", kind: SegmentToolInput, offset: 5, segmentLen: 16}},
	}, {
		// The query is the bool's own spelling: `true` would match every call
		// carrying a flag.
		name:  "a bool leaf is not searched",
		query: "true",
		input: `{"recurse":true}`,
	}, {
		name:  "a number leaf is not searched",
		query: "42",
		input: `{"limit":42}`,
	}, {
		// The leaves are "outer", "deep needle" and "tail": one segment of
		// "outer\ndeep needle\ntail", so the hit sits 11 runes in.
		name:  "a string nested in an array of objects is covered",
		input: `{"a":"outer","edits":[{"newStr":"deep needle"}],"z":"tail"}`,
		want:  []wantHit{{entry: "tc1", kind: SegmentToolInput, offset: 11, segmentLen: 22}},
	}, {
		// An array ELEMENT is a value, never a key, so a walk that alternates
		// key/value inside an array would drop every second path.
		name:  "every element of a string array is covered",
		input: `{"paths":["a.go","needle.go"]}`,
		want:  []wantHit{{entry: "tc1", kind: SegmentToolInput, offset: 5, segmentLen: 14}},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			if query == "" {
				query = "needle"
			}
			entries, drawn := oneTool(inputCall(tc.input), nil)
			hits := search(entries, drawn, query)
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

// Leaves are joined in DOCUMENT order, which is the order the card prints them,
// so two occurrences come back at the offsets that order gives them. The two
// leaves are different lengths, so the expected offsets (0, 24) belong to this
// order alone: swapped, the same two leaves yield 11 and 18; the keys are
// reverse-alphabetical so a sorted walk cannot produce document order by accident.
func TestSearch_ToolInputLeafOrderIsDocumentOrder(t *testing.T) {
	entries, drawn := oneTool(inputCall(`{"z":"needle first","a":"and then a needle"}`), nil)
	// "needle first\nand then a needle" is one 30-rune segment.
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: SegmentToolInput, offset: 0, segmentLen: 30},
		{entry: "tc1", kind: SegmentToolInput, offset: 24, segmentLen: 30},
	})
}

func TestSearch_ToolInputOffsetIsARuneIndex(t *testing.T) {
	entries, drawn := oneTool(inputCall(`{"a":"åß","b":"ü needle"}`), nil)
	// "åß\nü " is 5 runes (8 bytes); the whole segment is 11 runes (14 bytes).
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: SegmentToolInput, offset: 5, segmentLen: 11},
	})
}

// An edit call sends its payload twice, as the input's newStr on the tool_call
// and as the diff on the tool_result, and that is ONE rendered write, so it is
// one hit, on the entry whose element holds the text.
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
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1:result", kind: SegmentToolDiff, offset: 47, segmentLen: 60},
	})
}

// A missing input is a NORMAL value: the literal `null` the bound writes for an
// input it could neither parse nor shorten, and an absent input, both yield no
// segment, with no error to the caller. These are the two shapes a log line can
// hold; malformed bytes cannot reach a persisted payload and are the walker's own case.
func TestSearch_MissingInputYieldsNoHit(t *testing.T) {
	for _, input := range []string{`null`, ``} {
		entries, drawn := oneTool(inputCall(input), nil)
		if hits := search(entries, drawn, "needle"); len(hits) != 0 {
			t.Errorf("Search found %d hits in input %q, want 0: %+v", len(hits), input, hits)
		}
	}
}

// The leaf walker answers "" for bytes it cannot parse rather than a partial
// segment or an error: a truncated document is malformed like any other.
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

// A plan entry renders as a card, so each of its entries is a searchable span
// of its own: one segment per plan entry, all on the plan entry's id.
func TestSearch_PlanIsSearched(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "starting now").
		add("", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
			{Content: "Read the needle", Status: marotte.PlanCompleted},
			{Content: "Fix the needle", Status: marotte.PlanPending},
		}}))
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "plan-1", kind: SegmentPlan, offset: 9, segmentLen: 15},
		{entry: "plan-1", kind: SegmentPlan, offset: 8, segmentLen: 14},
	})
}

// A denial's RESOURCE is the one reader-facing string in the policy verdict, the
// command or path that was refused, so it is the one field searched.
func TestSearch_DenialResourceIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Run Command"},
		&marotte.EntryToolResult{Status: marotte.ToolFailed, Denial: &marotte.ToolDenial{
			Capability: "shell", Resource: "rm -rf needle", Scope: "user", Source: "permissions.yaml",
		}},
	)
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1:result", kind: SegmentToolDenial, offset: 7, segmentLen: 13},
	})
}

// A turn that ended badly persists WHY on its turn_close, and the card's footer
// renders it, so the reason is a searchable span of that entry.
func TestSearch_TurnFailureReasonIsSearched(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "partial answer").
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeFailed, FailureReason: "the needle budget ran out"}))
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "t-1:close", kind: SegmentTurnFailure, offset: 4, segmentLen: 25},
	})
}

// A steer entry's text is the note the transcript renders for it, so it is a
// `steer` segment; an agent-origin note is searched the same way.
func TestSearch_SteerTextIsSearched(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "working on it").
		add("", "steer-1", marotte.EntryKindSteer, marotte.EntrySteer{
			Text: "also check the needle", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
		}))
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "steer-1", kind: SegmentSteer, offset: 15, segmentLen: 21},
	})
}

// A compaction failure's reason and a safety block's properties both render as
// text in the turn, so each is a content segment of its own entry.
func TestSearch_CompactionFailedAndSafetyBlockedAreContentSegments(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		add("", "cf-1", marotte.EntryKindCompactionFailed, marotte.EntryCompactionFailed{Reason: "needle too large"}).
		add("", "sb-1", marotte.EntryKindSafetyBlocked, marotte.EntrySafetyBlocked{Properties: []string{"deletes data", "moves the needle"}}))
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "cf-1", kind: SegmentContent, offset: 0, segmentLen: 16},
		{entry: "sb-1", kind: SegmentContent, offset: 23, segmentLen: 29},
	})
}

// An attachment is searched by the NAME the pill renders and NOT by the path,
// which lives in a `title` attribute the client's DOM walker cannot mark. Both
// directions, because either half alone would pass while the other broke.
func TestSearch_AttachmentNameIsSearchedAndPathIsNot(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, &marotte.EntryPrompt{
		ID: "m-1", Text: "have a look",
		Attachments: []marotte.Attachment{{Path: "docs/haystack/notes.md", Name: "needle-notes.md"}},
	}))
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "t-1", kind: SegmentAttachment, offset: 0, segmentLen: 15},
	})
	if hits := search(entries, drawn, "haystack"); len(hits) != 0 {
		t.Errorf("the attachment PATH matched %d times, want 0: %+v", len(hits), hits)
	}
}

// A `disclose_context` result's display name is what the card SHOWS, replacing
// the title, so a reader who can see a skill name must be able to find it.
// DisplayName only: URI is not rendered and Type is a class name.
func TestSearch_DisclosedDisplayNameIsSearched(t *testing.T) {
	entries, drawn := oneTool(
		marotte.EntryToolCall{ID: "tc1", Title: "Disclose Context"},
		&marotte.EntryToolResult{Status: marotte.ToolCompleted, Disclosed: &marotte.ToolDisclosed{
			Type: "skill", DisplayName: "needle-review", URI: "file:///workspace/.kiro/skills/haystack/SKILL.md",
		}},
	)
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1:result", kind: SegmentToolDisclosed, offset: 0, segmentLen: 13},
	})
	if hits := search(entries, drawn, "haystack"); len(hits) != 0 {
		t.Errorf("the disclosed URI matched %d times, want 0: %+v", len(hits), hits)
	}
}

// The fields deliberately left unsearched, each for a stated reason, and each
// asserted where a needle in it would otherwise be indistinguishable from a gap.
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
			if hits := search(entries, drawn, "needle"); len(hits) != 0 {
				t.Errorf("%s is searched (%d hits) but %s: %+v", tc.name, len(hits), tc.why, hits)
			}
		})
	}
}

// A tool call's segments come out in the order the CARD renders them: the
// create half (title, input) on the tool_call, then the settled half
// (disclosed, diff, denial, output) on the tool_result. Stepping a card walks it
// the way a reader reads it, and bestHit's list-position tie-break is pinned to
// a stated order rather than to an accident of declaration.
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
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "tc1", kind: SegmentToolTitle, offset: 5, segmentLen: 11},
		{entry: "tc1", kind: SegmentToolInput, offset: 2, segmentLen: 21},
		{entry: "tc1:result", kind: SegmentToolDisclosed, offset: 4, segmentLen: 16},
		{entry: "tc1:result", kind: SegmentToolDiff, offset: 2, segmentLen: 20},
		{entry: "tc1:result", kind: SegmentToolDenial, offset: 2, segmentLen: 16},
		{entry: "tc1:result", kind: SegmentToolOutput, offset: 8, segmentLen: 19},
	})
}

// Hits arrive in FILE order across entries and across interleaved turns: the
// log's position is the only order, so a prompt, a plan and a close are found
// where they sit rather than in a per-kind tail.
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
	for _, h := range search(entries, drawn, "needle") {
		got = append(got, h.EntryID+"/"+string(h.SegmentKind))
	}
	want := []string{"t-1/prompt", "a1/content", "b1/content", "t-2:close/turn_failure", "plan-1/plan"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("hit order = %v, want %v", got, want)
	}
}

// Every kind segmentKinds declares has a producer that can reach it, read off
// the SAME slice the golden's kind loop reads. Two queries: SegmentEntry is
// produced by a FILTER-ONLY query alone.
func TestSearch_SegmentKindsAreExhaustive(t *testing.T) {
	entries, drawn := searchContractEntries()
	seen := make(map[SegmentKind]int)
	for _, q := range []string{"retry", "turn:2"} {
		hits := search(entries, drawn, q)
		if len(hits) == 0 {
			t.Fatalf("Search(%q) found nothing; it can vouch for no kind at all", q)
		}
		for _, h := range hits {
			seen[h.SegmentKind]++
		}
	}
	for _, kind := range segmentKinds {
		if seen[kind] == 0 {
			t.Errorf("segment kind %q has no producer: a declared kind the client can never be handed", kind)
		}
	}
}

// Segment offsets and lengths count RUNES, not bytes, and are relative to the
// matched entry even when an earlier entry holds multi-byte text.
func TestSearch_SegmentOffsetsAreRuneIndices(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "héllo wörld").
		thinking("th1", "åß needle"))
	// "åß " is 3 runes (5 bytes); the whole segment is 9 runes (11 bytes).
	assertHits(t, search(entries, drawn, "needle"), []wantHit{
		{entry: "th1", kind: SegmentReasoning, offset: 3, segmentLen: 9},
	})
}

// A filter-only query yields one synthetic hit per matching entry, carried as
// segment_kind "entry": offset 0, zero segment length, locating the entry rather
// than a span inside it. A tool_call with no text is still listed, with an
// excerpt from its first span.
func TestSearch_FilterOnlyHitsAreEntryKind(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "prose here").
		tool(marotte.EntryToolCall{ID: "tc1", Title: "shell"}, nil))
	hits := search(entries, drawn, "turn:1")
	assertHits(t, hits, []wantHit{
		{entry: "t-1", kind: SegmentEntry},
		{entry: "a1", kind: SegmentEntry},
		{entry: "tc1", kind: SegmentEntry},
	})
	if hits[2].Excerpt != "shell" {
		t.Errorf("tool-only entry excerpt = %q, want its title", hits[2].Excerpt)
	}
}

// lane is OPTIONAL on the generated type, and the encoder's half of that is
// omitting it when unset rather than writing an empty string.
func TestHit_WireShape(t *testing.T) {
	full, err := json.Marshal(Hit{
		TurnID: "t-1", EntryID: "tc1:result", SegmentKind: SegmentToolOutput, Lane: "sub-1", Turn: 3, Offset: 8, SegmentLen: 19,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"turn_id":"t-1"`, `"entry_id":"tc1:result"`, `"segment_kind":"tool_output"`, `"lane":"sub-1"`, `"turn":3`, `"offset":8`, `"segment_len":19`} {
		if !strings.Contains(string(full), key) {
			t.Errorf("marshalled hit %s lacks %s", full, key)
		}
	}
	minimal, err := json.Marshal(Hit{TurnID: "t-1", EntryID: "a1", SegmentKind: SegmentEntry})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(minimal), "lane") {
		t.Errorf("marshalled agent-lane hit %s carries lane, want it omitted", minimal)
	}
	for _, key := range []string{"message_id", "block_index", "agent_subtask_id"} {
		if strings.Contains(string(full), key) {
			t.Errorf("marshalled hit %s carries the deleted %s", full, key)
		}
	}
}
