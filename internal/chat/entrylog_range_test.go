package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The lower bound is INCLUSIVE, which is what lets one function answer both reads the
// two roots used to take through a bool: from == 0 is the whole turn, and a tail is the
// wire's own exclusive `after` plus one. A bound read as EXCLUSIVE serves the entry the
// caller already holds back to it, which the client's store answers as a hole.
func TestEntryLog_TurnRangeTakesAnInclusiveLowerBound(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})
	f.append(turn, "", "say-2", marotte.EntryKindText, marotte.EntryText{Text: "two"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	whole, err := f.log.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("Setup: TurnRange(%q, 0): %v", turn, err)
	}
	wantShapes(t, whole, []string{"0:/turn_open", "1:/text", "2:/text", "3:/turn_close"},
		"TurnRange(turn, 0)")

	tail, err := f.log.TurnRange(turn, 2)
	if err != nil {
		t.Fatalf("TurnRange(%q, 2): %v", turn, err)
	}
	wantShapes(t, tail, []string{"2:/text", "3:/turn_close"}, "TurnRange(turn, 2)")

	last, err := f.log.TurnRange(turn, 3)
	if err != nil {
		t.Fatalf("TurnRange(%q, 3): %v", turn, err)
	}
	wantShapes(t, last, []string{"3:/turn_close"}, "TurnRange(turn, 3) — the bound's own entry")

	past, err := f.log.TurnRange(turn, 4)
	if err != nil {
		t.Fatalf("TurnRange(%q, 4): %v", turn, err)
	}
	if len(past) != 0 {
		t.Errorf("TurnRange(turn, 4) = %v, want no entries: nothing at or above the bound", shapes(past))
	}
}

// TurnPage answers the seq the PAGE speaks for, never the seq the LOG holds, and the two
// part exactly where it matters: an EMPTY page answers `from - 1`, the exclusive cursor
// the caller already held. Reading the log's own newest instead lets a seal landing
// between the two reads stamp a version one ahead of the page, which a reconnect never
// fetches, so the client holds a stamp it can never satisfy.
func TestEntryLog_TurnPageStampsThePageRatherThanTheLog(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})

	newest, ok := f.log.NewestSeq(turn)
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

	// The ordinary empty tail: the caller is level with the log, so from - 1 and the
	// log's own newest agree and this arm alone cannot tell them apart.
	if entries, served, err = f.log.TurnPage(turn, newest+1); err != nil {
		t.Fatalf("TurnPage(%q, %d): %v", turn, newest+1, err)
	}
	if len(entries) != 0 || served != newest {
		t.Errorf("TurnPage(turn, %d) = %v, newestSeq %d; want no entries and %d",
			newest+1, shapes(entries), served, newest)
	}

	// A caller AHEAD of the log is what separates the two rules: the page speaks for the
	// cursor it was asked from, and the log's newest is a different number.
	if entries, served, err = f.log.TurnPage(turn, newest+4); err != nil {
		t.Fatalf("TurnPage(%q, %d): %v", turn, newest+4, err)
	}
	if len(entries) != 0 || served != newest+3 {
		t.Errorf("TurnPage(turn, %d) = %v, newestSeq %d; want no entries and %d, the caller's own cursor",
			newest+4, shapes(entries), served, newest+3)
	}
}

// turnRangePage is the range route's body, the three keys a client reads.
type turnRangePage struct {
	Entries     []marotte.Entry        `json:"entries"`
	OpenEntries []marotte.OpenEntry    `json:"open_entries"`
	Subject     []marotte.SubjectStamp `json:"subject"`
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

// getTurnRangeQuery drives the route with a query string, so the test reads the ?after=
// translation the way a client writes it.
func getTurnRangeQuery(t *testing.T, s *Store, id marotte.ChatID, turn, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/chats/" + string(id) + "/turns/" + turn
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	return rec
}

// The WIRE spelling does not move with the log's signature: ?after= stays EXCLUSIVE and
// absent stays the whole turn, and the door is where the translation happens. A door
// that passes `after` straight through as the inclusive bound serves the entry the
// client already holds, and one that translates an absent cursor to 1 drops the
// turn_open, which is the entry the client needs to place the card at all.
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
