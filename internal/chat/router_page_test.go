package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// chatPage is the transcript GET as the CLIENT decodes it, spelled by hand so a renamed
// json tag fails here instead of passing an assertion against itself.
type chatPage struct {
	Chat        map[string]any         `json:"chat"`
	Entries     []marotte.Entry        `json:"entries"`
	OpenEntries []marotte.OpenEntry    `json:"open_entries"`
	Subject     []marotte.SubjectStamp `json:"subject"`
	HasMore     bool                   `json:"has_more"`
	Live        bool                   `json:"live"`
}

// pageStore is a store whose registry answers `live` and `tails` for every chat.
func pageStore(t *testing.T, live bool, tails []OpenTurnTail) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(),
		WithLiveTurn(func(marotte.ChatID) bool { return live }),
		WithOpenTurns(func(marotte.ChatID) []OpenTurnTail { return tails }))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// openPromptTurn opens one prompt-class turn and answers its id.
func openPromptTurn(t *testing.T, s *Store, id marotte.ChatID, promptID string) string {
	t.Helper()
	opened, err := s.OpenTurn(t.Context(), id, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: promptID, Text: "do the thing"},
	}, func(c *marotte.Chat) { c.Name = string(id) })
	if err != nil {
		t.Fatalf("OpenTurn(%s): %v", promptID, err)
	}
	return opened.Turn
}

func closeTurn(t *testing.T, s *Store, id marotte.ChatID, turn string, outcome marotte.TurnOutcome) {
	t.Helper()
	e := entryOf(turn, "", turn+":close", marotte.EntryKindTurnClose, marotte.EntryTurnClose{Outcome: outcome})
	if err := s.Append(t.Context(), id, e); err != nil {
		t.Fatalf("Append(turn_close): %v", err)
	}
}

func getPage(t *testing.T, s *Store, id marotte.ChatID, query string) chatPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+query, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/%s%s = %d, want 200; body = %s", id, query, rec.Code, rec.Body.String())
	}
	var page chatPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return page
}

// seedMidTurn leaves the record a turn in flight leaves on disk: a header and one
// turn_open with nothing after it.
func seedMidTurn(t *testing.T, s *Store, id marotte.ChatID) {
	t.Helper()
	openPromptTurn(t, s, id, "m-"+string(id))
}

// getChat drives the transcript GET and answers the raw envelope.
func getChat(t *testing.T, s *Store, id marotte.ChatID) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id), nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return envelope
}

// chatStampOf is the page's `chat` stamp: the subject is a LIST, the chat stamp first.
func chatStampOf(t *testing.T, envelope map[string]any) any {
	t.Helper()
	stamps, ok := envelope["subject"].([]any)
	if !ok || len(stamps) == 0 {
		t.Fatalf("subject = %v, want a non-empty list of stamps", envelope["subject"])
	}
	return stamps[0]
}

func stampKinds(stamps []marotte.SubjectStamp) []string {
	out := make([]string, 0, len(stamps))
	for _, st := range stamps {
		out = append(out, st.Kind)
	}
	return out
}

// `live` is the registry's answer, never the log's: a turn open on disk under a dead
// process reads false, and a turn the registry holds reads true whatever the log says.
func TestChatGet_LiveIsTheRegistrysAnswer(t *testing.T) {
	for _, live := range []bool{true, false} {
		s := pageStore(t, live, nil)
		openPromptTurn(t, s, "c1", "m-1")
		if page := getPage(t, s, "c1", ""); page.Live != live {
			t.Errorf("live = %v with the registry answering %v", page.Live, live)
		}
	}
}

// With no registry injected nothing is live: a store serving a chat the process holds no
// turn for must not read an open turn_open as a live turn.
func TestChatGet_LiveIsFalseWithNoRegistryInjected(t *testing.T) {
	s, _ := newTestStore(t)
	openPromptTurn(t, s, "c1", "m-1")
	page := getPage(t, s, "c1", "")
	if page.Live {
		t.Error("live = true with no registry injected")
	}
	if got := stampKinds(page.Subject); len(got) != 1 || got[0] != "chat" {
		t.Errorf("subject kinds = %v, want [chat]", got)
	}
}

// The newest page carries every open tail of a turn in the window and one live_turn
// stamp per open turn beside the chat stamp, so a reader can tell exactly what the page
// certifies; a tail for a turn outside the window is not served.
func TestChatGet_CarriesOpenTailsAndOneLiveTurnStampPerOpenTurn(t *testing.T) {
	var tails []OpenTurnTail
	s := pageStore(t, true, nil)
	s.openTurns = func(marotte.ChatID) []OpenTurnTail { return tails }
	turn := openPromptTurn(t, s, "c1", "m-1")
	tails = []OpenTurnTail{
		{ID: turn, Entries: []marotte.OpenEntry{{Turn: turn, ID: "say-1", Kind: marotte.EntryKindText, Text: "hel", N: 1}}},
		{ID: "t-elsewhere", Entries: []marotte.OpenEntry{{Turn: "t-elsewhere", ID: "say-9", Kind: marotte.EntryKindText, Text: "x", N: 1}}},
	}

	page := getPage(t, s, "c1", "")
	if len(page.OpenEntries) != 1 || page.OpenEntries[0].ID != "say-1" || page.OpenEntries[0].Text != "hel" {
		t.Errorf("open_entries = %+v, want the one tail of the turn in the window", page.OpenEntries)
	}
	if got := stampKinds(page.Subject); len(got) != 2 || got[0] != "chat" || got[1] != "live_turn" {
		t.Fatalf("subject kinds = %v, want [chat live_turn]", got)
	}
	if page.Subject[1].Ref != turn {
		t.Errorf("live_turn stamp ref = %q, want the open turn %q", page.Subject[1].Ref, turn)
	}
}

// An older page carries the chat stamp alone and no open tail: no open turn is in an
// older page, and a client paging back must not adopt a live_turn stamp it cannot refresh.
func TestChatGet_OlderPageCarriesTheChatStampAlone(t *testing.T) {
	var tails []OpenTurnTail
	s := pageStore(t, true, nil)
	s.openTurns = func(marotte.ChatID) []OpenTurnTail { return tails }
	first := openPromptTurn(t, s, "c1", "m-1")
	closeTurn(t, s, "c1", first, marotte.TurnOutcomeCompleted)
	second := openPromptTurn(t, s, "c1", "m-2")
	tails = []OpenTurnTail{{ID: second, Entries: []marotte.OpenEntry{{Turn: second, ID: "say-2", Kind: marotte.EntryKindText, Text: "x", N: 1}}}}

	page := getPage(t, s, "c1", "?before="+second)
	if len(page.OpenEntries) != 0 {
		t.Errorf("older page open_entries = %+v, want none", page.OpenEntries)
	}
	if got := stampKinds(page.Subject); len(got) != 1 || got[0] != "chat" {
		t.Errorf("older page subject kinds = %v, want [chat]", got)
	}
	if len(page.Entries) == 0 || page.Entries[0].Turn != first {
		t.Errorf("older page entries = %+v, want the first turn's", page.Entries)
	}
}

// railRows drives GET /api/chats/{id}/turns and answers the rows as decoded.
func railRows(t *testing.T, s *Store, id marotte.ChatID) []marotte.TurnSummary {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/turns", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET turns = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Turns []marotte.TurnSummary `json:"turns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return envelope.Turns
}

// A rail row's outcome is WRITTEN: `running` exactly while the turn has no turn_close,
// and the close's own outcome once one is appended. Nothing derives it from the body.
func TestTurnsIndex_OutcomeIsRunningUntilTheCloseIsWritten(t *testing.T) {
	s, _ := newTestStore(t)
	turn := openPromptTurn(t, s, "c1", "m-1")

	rows := railRows(t, s, "c1")
	if len(rows) != 1 || rows[0].ID != turn || rows[0].Outcome != marotte.TurnOutcomeRunning {
		t.Fatalf("rows before the close = %+v, want one %s row for %s", rows, marotte.TurnOutcomeRunning, turn)
	}
	closeTurn(t, s, "c1", turn, marotte.TurnOutcomeFailed)
	rows = railRows(t, s, "c1")
	if len(rows) != 1 || rows[0].Outcome != marotte.TurnOutcomeFailed {
		t.Errorf("rows after the close = %+v, want one %s row", rows, marotte.TurnOutcomeFailed)
	}
}

// turnsOfPage is each page entry as "<turn>:<kind>", the shape the paging rules are about.
func turnsOfPage(entries []marotte.Entry) string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		out = append(out, entries[i].Turn+":"+string(entries[i].Kind))
	}
	return strings.Join(out, " ")
}

// fourClosedTurns seeds four prompt turns, each a turn_open, one text entry and a
// close, and answers their ids in order.
func fourClosedTurns(t *testing.T, s *Store, id marotte.ChatID) []string {
	t.Helper()
	turns := make([]string, 0, 4)
	for i := range 4 {
		turn := openPromptTurn(t, s, id, "m-"+string(rune('a'+i)))
		e := entryOf(turn, "", turn+"-say", marotte.EntryKindText, marotte.EntryText{Text: "reply"})
		if err := s.Append(t.Context(), id, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
		closeTurn(t, s, id, turn, marotte.TurnOutcomeCompleted)
		turns = append(turns, turn)
	}
	return turns
}

// The page is N WHOLE turns, newest first by n, never split: `?limit=` counts turns,
// `has_more` says an older turn exists past the edge, and `?before=<turn_id>` pages
// older with every entry of each turn in file order.
func TestChatGet_PagesWholeTurnsByLimitAndBefore(t *testing.T) {
	s, _ := newTestStore(t)
	turns := fourClosedTurns(t, s, "c1")
	whole := func(turn string) string {
		return turn + ":turn_open " + turn + ":text " + turn + ":turn_close"
	}

	newest := getPage(t, s, "c1", "?limit=2")
	if got, want := turnsOfPage(newest.Entries), whole(turns[2])+" "+whole(turns[3]); got != want {
		t.Errorf("?limit=2 entries = %q, want %q", got, want)
	}
	if !newest.HasMore {
		t.Error("?limit=2 has_more = false with two older turns on disk")
	}

	older := getPage(t, s, "c1", "?limit=2&before="+turns[2])
	if got, want := turnsOfPage(older.Entries), whole(turns[0])+" "+whole(turns[1]); got != want {
		t.Errorf("?before=%s entries = %q, want %q", turns[2], got, want)
	}
	if older.HasMore {
		t.Error("the oldest page has_more = true with nothing older on disk")
	}

	all := getPage(t, s, "c1", "?limit=10")
	if len(all.Entries) != 12 || all.HasMore {
		t.Errorf("?limit=10 = %d entries, has_more %v; want all 12 and false", len(all.Entries), all.HasMore)
	}
}

// A `before` cursor naming a turn this log does not hold is the caller's error
// (a rewind can produce one under a reader mid-scroll), as is one that is not an id.
func TestChatGet_ABadBeforeCursorIs400(t *testing.T) {
	s, _ := newTestStore(t)
	fourClosedTurns(t, s, "c1")
	for _, query := range []string{"?before=t-nosuchturn", "?before=not%20an%20id"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+query, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/chats/c1%s = %d, want 400; body = %s", query, rec.Code, rec.Body.String())
		}
	}
}

// The generated decoder rejects `null` for an array, so a chat with a header and no
// log answers `"entries":[]`.
func TestChatGet_EmptyPageIsAnArrayNotNull(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Fresh"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("body does not carry `\"entries\":[]`: %s", rec.Body.String())
	}
}

// A turn nothing prompted (a wire turn_start with no prompt on its turn_open) is a
// turn like any other: served whole on its page and counted by `?limit=`.
func TestChatGet_APromptlessTurnIsServedWhole(t *testing.T) {
	s, _ := newTestStore(t)
	first := openPromptTurn(t, s, "c1", "m-1")
	closeTurn(t, s, "c1", first, marotte.TurnOutcomeCompleted)
	opened, err := s.OpenTurn(t.Context(), "c1", &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, nil)
	if err != nil {
		t.Fatalf("OpenTurn(wire): %v", err)
	}
	for _, say := range []string{"h1", "h2", "h3"} {
		e := entryOf(opened.Turn, "", say, marotte.EntryKindText, marotte.EntryText{Text: "agent-initiated"})
		if err := s.Append(t.Context(), "c1", e); err != nil {
			t.Fatalf("Append(%s): %v", say, err)
		}
	}
	closeTurn(t, s, "c1", opened.Turn, marotte.TurnOutcomeCompleted)

	page := getPage(t, s, "c1", "?limit=1")
	want := opened.Turn + ":turn_open " + opened.Turn + ":text " + opened.Turn + ":text " + opened.Turn + ":text " + opened.Turn + ":turn_close"
	if got := turnsOfPage(page.Entries); got != want {
		t.Errorf("?limit=1 entries = %q, want the whole promptless turn %q", got, want)
	}
	if !page.HasMore {
		t.Error("has_more = false with the prompt turn older on disk")
	}
}

// `?limit=` is a count of TURNS honoured over 1..200 inclusive; anything else
// falls back to the default rather than clamping, so a caller asking for the
// unserveable cannot keep believing the number it sent.
func TestParseLimitParam_HonoursTheInclusiveRange(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent", query: "", want: defaultPageTurns},
		{name: "smallest_accepted", query: "?limit=1", want: 1},
		{name: "largest_accepted", query: "?limit=200", want: 200},
		{name: "one_past_the_largest", query: "?limit=201", want: defaultPageTurns},
		{name: "zero", query: "?limit=0", want: defaultPageTurns},
		{name: "negative", query: "?limit=-1", want: defaultPageTurns},
		{name: "not_a_number", query: "?limit=lots", want: defaultPageTurns},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			if got := parseLimitParam(r); got != tc.want {
				t.Errorf("parseLimitParam(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}
