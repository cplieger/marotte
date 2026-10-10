package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The lower bound is inclusive: from == 0 is the whole turn, a tail is the wire's exclusive `after` plus one. An
// exclusive reading re-serves an entry the client holds, which its store treats as a hole.
func TestEntryLog_TurnRangeTakesAnInclusiveLowerBound(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})
	f.append(turn, "", "say-2", marotte.EntryKindText, marotte.EntryText{Text: "two"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	whole, err := turnRange(f.log, turn, 0)
	if err != nil {
		t.Fatalf("Setup: TurnRange(%q, 0): %v", turn, err)
	}
	wantShapes(t, whole, []string{"0:/turn_open", "1:/text", "2:/text", "3:/turn_close"},
		"TurnRange(turn, 0)")

	tail, err := turnRange(f.log, turn, 2)
	if err != nil {
		t.Fatalf("TurnRange(%q, 2): %v", turn, err)
	}
	wantShapes(t, tail, []string{"2:/text", "3:/turn_close"}, "TurnRange(turn, 2)")

	last, err := turnRange(f.log, turn, 3)
	if err != nil {
		t.Fatalf("TurnRange(%q, 3): %v", turn, err)
	}
	wantShapes(t, last, []string{"3:/turn_close"}, "TurnRange(turn, 3) — the bound's own entry")

	past, err := turnRange(f.log, turn, 4)
	if err != nil {
		t.Fatalf("TurnRange(%q, 4): %v", turn, err)
	}
	if len(past) != 0 {
		t.Errorf("TurnRange(turn, 4) = %v, want no entries: nothing at or above the bound", shapes(past))
	}
}

// TurnPage returns the seq the page speaks for: an empty page answers `from - 1`. The log's newest could be a seal
// ahead of the page, a stamp the client could never satisfy.
func TestEntryLog_TurnPageStampsThePageRatherThanTheLog(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})

	newest, ok := f.log.newestSeq(turn)
	if !ok || newest != 1 {
		t.Fatalf("Setup: NewestSeq(%q) = %d, %v; want 1, true", turn, newest, ok)
	}

	entries, served, err := f.log.TurnPage(turn, 0)
	if err != nil {
		t.Fatalf("TurnPage(%q, 0): %v", turn, err)
	}
	if len(entries) != 2 || served != 1 {
		t.Fatalf("TurnPage(turn, 0) = %v, newestSeq %d; want the whole turn and 1",
			shapes(entries), served)
	}

	// Level with the log, the two rules agree.
	if entries, served, err = f.log.TurnPage(turn, newest+1); err != nil {
		t.Fatalf("TurnPage(%q, %d): %v", turn, newest+1, err)
	}
	if len(entries) != 0 || served != newest {
		t.Errorf("TurnPage(turn, %d) = %v, newestSeq %d; want no entries and %d",
			newest+1, shapes(entries), served, newest)
	}

	// A caller ahead of the log separates them.
	if entries, served, err = f.log.TurnPage(turn, newest+4); err != nil {
		t.Fatalf("TurnPage(%q, %d): %v", turn, newest+4, err)
	}
	if len(entries) != 0 || served != newest+3 {
		t.Errorf("TurnPage(turn, %d) = %v, newestSeq %d; want no entries and %d, the caller's own cursor",
			newest+4, shapes(entries), served, newest+3)
	}
}

// turnRangePage is the range route's body: the keys these tests read.
type turnRangePage struct {
	Entries []marotte.Entry        `json:"entries"`
	Subject []marotte.SubjectStamp `json:"subject"`
}

func decodeTurnRange(t *testing.T, rec *httptest.ResponseRecorder) turnRangePage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("range route = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var page turnRangePage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode the range body: %v; body = %s", err, rec.Body.String())
	}
	return page
}

// getTurnRangeQuery drives the route with a query string, as a client writes ?after=.
func getTurnRangeQuery(t *testing.T, s *Store, id marotte.ChatID, turn, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/chats/" + string(id) + "/turns/" + turn
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	return rec
}

// ?after= stays exclusive and absent stays the whole turn; the door translates. Passing it through re-serves a held
// entry, and mapping absent to 1 drops the turn_open the card needs.
func TestTurnRangeRoute_TranslatesTheExclusiveWireCursor(t *testing.T) {
	tail := []OpenTurnTail{{ID: "", Entries: nil}}
	s := pageStore(t, true, tail)
	turn := openPromptTurn(t, s, "c1", "m-1")
	if err := appendText(t, s, turn, "say-1", "one"); err != nil {
		t.Fatalf("Setup: append say-1: %v", err)
	}
	if err := appendText(t, s, turn, "say-2", "two"); err != nil {
		t.Fatalf("Setup: append say-2: %v", err)
	}
	tail[0].ID = turn
	tail[0].Entries = []marotte.OpenEntry{{
		Turn: turn, ID: "say-3", Kind: marotte.EntryKindText, Text: "three", N: 1,
	}}

	whole := decodeTurnRange(t, getTurnRangeQuery(t, s, "c1", turn, ""))
	wantShapes(t, whole.Entries, []string{"0:/turn_open", "1:/text", "2:/text"},
		"an absent ?after=")

	after := decodeTurnRange(t, getTurnRangeQuery(t, s, "c1", turn, "after=1"))
	wantShapes(t, after.Entries, []string{"2:/text"}, "?after=1")

	empty := decodeTurnRange(t, getTurnRangeQuery(t, s, "c1", turn, "after=2"))
	if len(empty.Entries) != 0 {
		t.Errorf("?after=2 serves %v, want an empty page", shapes(empty.Entries))
	}
	if len(empty.Subject) != 1 || empty.Subject[0].Version != turn+":2" {
		t.Errorf("?after=2 stamps %v, want one live_turn at %q: an empty page speaks for the cursor the caller held",
			empty.Subject, turn+":2")
	}
}
