package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A cursor naming a REVERTED turn is refused by the door the server already has:
// `Window` returns the error it returns for an absent id and the chat page renders it
// as 400 (router_handlers.go's own arm, whose comment names the rewind as its reason).
// Never 200 — a newest page answering a `?before=` read reaches applyPage's PREPENDING
// older branch and leaves turn_order newest-then-oldest — and never 500, which is the
// turn-range route's mapping for a different failure.
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
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET ?before=<a reverted turn> = %d, want %d: a reverted cursor is a caller-caused refusal, not a page and not a server error; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// The same door still serves the surviving page, so the refusal is about the
	// cursor rather than about the chat.
	if page := getPage(t, s, id, ""); len(page.Entries) == 0 {
		t.Errorf("the newest page is empty after a revert, want the surviving turn's entries")
	}
}
