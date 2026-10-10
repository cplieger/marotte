package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A cursor naming a reverted turn gets the absent-id 400. A 200 would prepend a newest page in applyPage's older
// branch; 500 is the turn-range route's mapping for a different failure.
func TestRevert_PageRefusesARevertedCursorWith400(t *testing.T) {
	s := pageStore(t, false, nil)
	const id marotte.ChatID = "c-abcdef01"
	a := openPromptTurn(t, s, id, "m-a")
	closeTurn(t, s, id, a, marotte.TurnOutcomeCompleted)
	b := openPromptTurn(t, s, id, "m-b")
	closeTurn(t, s, id, b, marotte.TurnOutcomeCompleted)

	record := entryOf(a, "", b+":revert", marotte.EntryKindTurnRevert, marotte.EntryTurnRevert{
		From: b, FromN: 2, Through: b, Cause: marotte.TurnRevertCauseRewind,
	})
	if err := s.Append(t.Context(), id, record); err != nil {
		t.Fatalf("append turn_revert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"?before="+b, nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET ?before=<a reverted turn> = %d, want %d: a reverted cursor is a caller-caused refusal, not a page and not a server error; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// The same door still serves the surviving page: the refusal is about the cursor.
	if page := getPage(t, s, id, ""); len(page.Entries) == 0 {
		t.Errorf("the newest page is empty after a revert, want the surviving turn's entries")
	}
}
