package agent

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestGetBridge_ReturnsNilForUnknown(t *testing.T) {
	h, _, _ := newTestHub()
	if sb := h.coord.bridgeFor("no-such-chat"); sb != nil {
		t.Errorf("getBridge returned %+v for missing chat", sb)
	}
}

func TestOpenBridge_ReusesExisting(t *testing.T) {
	h, cs, _ := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	sb1, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("first create error = %v", err)
	}
	sb2, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("second create error = %v", err)
	}
	if sb1 != sb2 {
		t.Errorf("second call returned a different session entry")
	}
}

func TestOpenBridge_MissingChatIsError(t *testing.T) {
	h, _, _ := newTestHub()
	_, err := h.coord.openBridge(t.Context(), "no-chat", "")
	if err == nil {
		t.Fatal("expected error for missing chat")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

func TestCloseBridge_RemovesAndClosesTheSession(t *testing.T) {
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	sb, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)

	h.coord.closeBridge(t.Context(), "c1", marotte.TurnOutcomeCancelled)
	// The close runs off the caller's path; the session's forward ends with it.
	joinInflight(t, h)

	if h.coord.bridgeFor("c1") != nil {
		t.Error("session still registered after closeBridge")
	}
	if !fb.isClosed() {
		t.Error("the session was never closed")
	}
}
