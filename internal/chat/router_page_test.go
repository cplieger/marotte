package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// chatPage is the transcript GET as the client decodes it, spelled by hand so a renamed json tag fails here.
type chatPage struct {
	Entries     []marotte.Entry        `json:"entries"`
	OpenEntries []marotte.OpenEntry    `json:"open_entries"`
	Subject     []marotte.SubjectStamp `json:"subject"`
	HasMore     bool                   `json:"has_more"`
	Live        bool                   `json:"live"`
}

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
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/%s%s = %d, want 200; body = %s", id, query, rec.Code, rec.Body.String())
	}
	var page chatPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return page
}

// seedMidTurn leaves what an in-flight turn leaves on disk: a header and one turn_open.
func seedMidTurn(t *testing.T, s *Store, id marotte.ChatID) {
	t.Helper()
	openPromptTurn(t, s, id, "m-"+string(id))
}

func getChat(t *testing.T, s *Store, id marotte.ChatID) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id), nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return envelope
}

// The subject is a list with the chat stamp first.
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

// `live` is the registry's answer: an open turn on disk under a dead process is false, a registry-held turn true.
func TestChatGet_LiveIsTheRegistrysAnswer(t *testing.T) {
	for _, live := range []bool{true, false} {
		s := pageStore(t, live, nil)
		openPromptTurn(t, s, "c1", "m-1")
		if page := getPage(t, s, "c1", ""); page.Live != live {
			t.Errorf("live = %v with the registry answering %v", page.Live, live)
		}
	}
}

// With no registry nothing is live, whatever turn_open the log holds.
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

// The newest page carries every open tail of a turn in the window and one live_turn stamp per open turn beside the
// chat stamp; tails outside the window are not served.
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

// An older page carries the chat stamp alone, with no tail or live_turn stamp the client could not refresh.
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

func railRows(t *testing.T, s *Store, id marotte.ChatID) []marotte.TurnSummary {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/turns", nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
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

// A rail row's outcome is written: `running` until the turn_close, then the close's outcome.
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

// turnsOfPage renders each page entry as "<turn>:<kind>".
func turnsOfPage(entries []marotte.Entry) string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		out = append(out, entries[i].Turn+":"+string(entries[i].Kind))
	}
	return strings.Join(out, " ")
}

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

// A page is N whole turns, newest by n: `?limit=` counts turns, `has_more` flags an older one, `?before=<turn_id>`
// pages older with each turn's entries in file order.
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

// A `before` naming a turn this log lacks (a rewind can cause it) or not an id is the caller's error.
func TestChatGet_ABadBeforeCursorIs400(t *testing.T) {
	s, _ := newTestStore(t)
	fourClosedTurns(t, s, "c1")
	for _, query := range []string{"?before=t-nosuchturn", "?before=not%20an%20id"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+query, nil)
		rec := httptest.NewRecorder()
		newRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/chats/c1%s = %d, want 400; body = %s", query, rec.Code, rec.Body.String())
		}
	}
}

// The generated decoder rejects `null`, so a header with no log answers `"entries":[]`.
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
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("body does not carry `\"entries\":[]`: %s", rec.Body.String())
	}
}

// A promptless turn (a wire turn_start) is served whole and counted by `?limit=`.
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

// `?limit=` counts turns over 1..200; anything else takes the default rather than clamping.
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
