package agent

// The notify route against a chat the user closed: 410 session_closed, nothing placed, the chat left closed.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestNotifyRoute_AChatTheUserClosedAnswers410AndStaysClosed(t *testing.T) {
	h, cs, _, tab := closeTabHub(t)
	route := notifyRoute(t, h, cs)
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("Setup: close_tab answered %d", code)
	}

	w := routeAnswerWithin(t, postWake(route, "grelot", "k1", grelotWake), "the wake")

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("answer %d %q is not JSON: %v", w.Code, w.Body.String(), err)
	}
	if w.Code != http.StatusGone || body.Code != "session_closed" || body.Error != "this chat was closed by the user" {
		t.Errorf("answer = %d %+v, want 410 code session_closed", w.Code, body)
	}
	if got := rowsFrom(h, "c1", "grelot"); len(got) != 0 {
		t.Errorf("grelot rows = %q after the refusal, want none", got)
	}
	open, _ := h.tabs.List()
	for _, s := range open {
		if s.Kind == marotte.TabKindChat && s.Ref == "c1" {
			t.Fatalf("c1 has tab %s after the notify, want it left closed", s.ID)
		}
	}
}

func TestNotifyRoute_AChatWithItsTabOpenIsAccepted(t *testing.T) {
	h, cs, _, _ := closeTabHub(t)
	route := notifyRoute(t, h, cs)

	requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k1", grelotWake), "the wake"), "the wake")
}
