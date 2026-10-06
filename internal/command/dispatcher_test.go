package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

func TestDispatcher_MethodNotAllowed(t *testing.T) {
	d := New()
	req := httptest.NewRequest(http.MethodGet, "/api/command", nil)
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}

func TestDispatcher_InvalidJSON(t *testing.T) {
	d := New()
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader("{invalid"))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	if got := w.Body.String(); !strings.Contains(got, "invalid json") {
		t.Errorf("body = %q, want it to contain %q", got, "invalid json")
	}
}

func TestDispatcher_InvalidRequestID(t *testing.T) {
	d := New()
	body := `{"type":"test","request_id":"../../etc/passwd"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestDispatcher_InvalidChatID(t *testing.T) {
	d := New()
	body := `{"type":"test","chat_id":"has spaces"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	if got := w.Body.String(); !strings.Contains(got, ids.ErrMsgInvalidChatID) {
		t.Errorf("body = %q, want it to contain %q", got, ids.ErrMsgInvalidChatID)
	}
}

func TestDispatcher_UnknownCommand(t *testing.T) {
	d := New()
	body := `{"type":"nonexistent_command","chat_id":"abc123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestDispatcher_BodyTooLarge(t *testing.T) {
	d := New()
	bigBody := bytes.Repeat([]byte("x"), 2*1024*1024)
	req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(bigBody))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 413 or 400", w.Code)
	}
}

// TestStatusError_UnwrapsToTheCause pins an Unwrap no code names directly: rpcerr.Text's errors.As
// needs it to reach KAS's `error.data`, else a -32603 renders as the literal "Internal error".
// Red-check: delete (*statusError).Unwrap.
func TestStatusError_UnwrapsToTheCause(t *testing.T) {
	cause := &marotte.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data:    json.RawMessage(`{"details":"the model refused the tool call"}`),
	}
	wrapped := StatusError(http.StatusBadGateway, cause)

	if got := statusOf(wrapped); got != http.StatusBadGateway {
		t.Errorf("statusOf = %d, want %d", got, http.StatusBadGateway)
	}
	if got := rpcerr.Text(wrapped); got != "the model refused the tool call" {
		t.Errorf("rpcerr.Text(wrapped) = %q, want the error.data details — the status "+
			"wrapper is hiding the cause from errors.As", got)
	}
	if !errors.Is(StatusError(http.StatusNotFound, ErrChatNotFound), ErrChatNotFound) {
		t.Error("errors.Is cannot see ErrChatNotFound through the status wrapper")
	}
}
