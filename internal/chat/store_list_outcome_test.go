package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The list endpoint carries the outcome the closer WROTE on the header, spelled the
// way the generated TypeScript reads it, and a chat whose newest turn has not closed
// carries no key rather than an empty one: the client's latch reads an absent field
// as "latch nothing", so `omitempty` is part of the contract.
func TestHandleList_CarriesLastTurnOutcomeOnTheWire(t *testing.T) {
	s, _ := newTestStore(t)
	seed := []struct {
		id      string
		outcome marotte.TurnOutcome
	}{
		{"c-done", marotte.TurnOutcomeCompleted},
		{"c-failed", marotte.TurnOutcomeFailed},
		{"c-open", ""},
	}
	for _, sc := range seed {
		if _, err := s.Mutate(t.Context(), marotte.ChatID(sc.id), func(c *marotte.Chat, _ bool) bool {
			c.Name = sc.id
			c.LastTurnOutcome = sc.outcome
			return true
		}); err != nil {
			t.Fatalf("seed %s: %v", sc.id, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"last_turn_outcome":"completed"`,
		`"last_turn_outcome":"failed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleList body does not carry %s; body = %s", want, body)
		}
	}

	var envelope struct {
		Chats []map[string]any `json:"chats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(envelope.Chats) != len(seed) {
		t.Fatalf("handleList rows = %d, want %d", len(envelope.Chats), len(seed))
	}
	for _, row := range envelope.Chats {
		id, _ := row["id"].(string)
		_, present := row["last_turn_outcome"]
		if id == "c-open" && present {
			t.Errorf("chat %s carries last_turn_outcome = %v, want the key absent", id, row["last_turn_outcome"])
		}
		if id != "c-open" && !present {
			t.Errorf("chat %s is missing last_turn_outcome", id)
		}
	}
}
