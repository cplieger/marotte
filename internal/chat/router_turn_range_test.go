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

func getTurnRange(t *testing.T, s *Store, id marotte.ChatID, turn string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/turns/"+turn, nil)
	rec := httptest.NewRecorder()
	newRouter(s).handleOne(rec, req)
	return rec
}

// A missing turn is the caller's 404; an unreadable log the server's 500. One 404 for both told clients real turns
// did not exist.
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

	// Same-length overwrite, so the offset index stays valid and the decode fails, as for a corrupt log.
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
