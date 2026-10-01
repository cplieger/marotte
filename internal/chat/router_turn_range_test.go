package chat

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// getTurnRange drives GET /api/chats/{id}/turns/{turn} through the router's own
// dispatch, so the test reaches the handler the way a client does.
func getTurnRange(t *testing.T, s *Store, id marotte.ChatID, turn string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/turns/"+turn, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	return rec
}

// The range read's TWO refusals are two different statuses, and the split is the whole
// point: a turn the log holds nothing for is the one outcome a CALLER can cause, so it
// is a 404, and a log this server cannot read is a 500. Answering 404 for both told a
// client its turn does not exist whenever a disk or decode fault answered the read, so
// it stopped asking about a turn that is really there.
func TestTurnRangeRoute_404sAnUnknownTurnAnd500sAnUnreadableLog(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: NewStore: %v", err)
	}
	turn := openPromptTurn(t, s, "c1", "m-1")
	closeTurn(t, s, "c1", turn, marotte.TurnOutcomeCompleted)

	if code := getTurnRange(t, s, "c1", turn).Code; code != http.StatusOK {
		t.Fatalf("Setup: the turn's own range read = %d, want 200", code)
	}
	if code := getTurnRange(t, s, "c1", "t-nosuchturn").Code; code != http.StatusNotFound {
		t.Errorf("unknown turn = %d, want 404", code)
	}
	if code := getTurnRange(t, s, "c-nosuchchat", turn).Code; code != http.StatusNotFound {
		t.Errorf("unknown chat = %d, want 404", code)
	}

	// Overwrite the log's bytes in place at the SAME length, so the store's offset index
	// still points inside the file and it is the DECODE that fails: the shape a torn or
	// corrupt log takes for a range read.
	path := filepath.Join(dir, "c1", entriesFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read the log: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(raw)), 0o600); err != nil {
		t.Fatalf("Setup: corrupt the log: %v", err)
	}
	if code := getTurnRange(t, s, "c1", turn).Code; code != http.StatusInternalServerError {
		t.Errorf("undecodable log = %d, want 500", code)
	}
}
