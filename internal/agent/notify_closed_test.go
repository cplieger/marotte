package agent

// A message from outside marotte's page into a chat the user closed, through the notify route or /api/command: 410
// session_closed, nothing placed or sent, the chat left closed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// postAPICmd posts cmd as a program does: no fetch metadata, no Origin.
func postAPICmd(t *testing.T, h *Runtime, cmd marotte.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	rec := httptest.NewRecorder()
	h.handleCommand(rec, httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body))))
	return rec
}

// A closed chat keeps its paused rows; an API caller's resume_queue or send_queued_now must not send them.
func TestQueueCommands_FromAnAPICallerIntoAChatTheUserClosedSendNothing(t *testing.T) {
	for _, cmd := range []marotte.ClientCommand{
		{Type: marotte.CmdResumeQueue, ChatID: "c1"},
		{Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"m-q1"}`)},
	} {
		t.Run(string(cmd.Type), func(t *testing.T) {
			h, cs, _, tab := closeTabHub(t)
			t.Cleanup(func() { shutdownHub(t, h) })
			seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "after the stop"})
			if code := postCloseTab(t, h, tab); code != http.StatusOK {
				t.Fatalf("Setup: close_tab answered %d", code)
			}
			if ids := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-q1"}) {
				t.Fatalf("Setup: stored rows after the close = %v, want [m-q1]", ids)
			}

			w := postAPICmd(t, h, cmd)

			var body struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("answer %d %q is not JSON: %v", w.Code, w.Body.String(), err)
			}
			if w.Code != http.StatusGone || body.Reason != "session_closed" || body.Error != "this chat was closed by the user" {
				t.Errorf("answer = %d %+v, want 410 reason session_closed", w.Code, body)
			}
			if opens := opensOf(t, logOf(t, cs, "c1")); len(opens) != 0 {
				t.Errorf("turn opens = %d after the refusal, want none", len(opens))
			}
			if ids := queuedIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-q1"}) {
				t.Errorf("stored rows = %v, want [m-q1] still waiting", ids)
			}
			open, _ := h.tabs.List()
			for _, s := range open {
				if s.Kind == marotte.TabKindChat && s.Ref == "c1" {
					t.Errorf("c1 has tab %s after the command, want it left closed", s.ID)
				}
			}
		})
	}
}
