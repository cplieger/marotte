package agent

// The notify route's answer against a chat whose delivery is slow: the steer reply, the turn open or a close held.
// The answer comes once the wake is on the chat's run list, whatever the delivery is doing.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notify"
)

const (
	notifyAnswerToken   = "notify-answer-token-0123456789-abcdefghij" // gitleaks:allow
	notifyAnswerSession = "sess-notify-c1"
	// notifyAnswerCeiling is far past the route's own bound and far under any wait on kiro-cli, a turn or a close.
	notifyAnswerCeiling = time.Second
)

// notifyRoute serves the notify route over h's chats, with c1 holding notifyAnswerSession.
func notifyRoute(t *testing.T, h *Runtime, cs *testChatStore) http.Handler {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession(notifyAnswerSession)
		return true
	}); err != nil {
		t.Fatalf("Setup: record c1's session: %v", err)
	}
	gate, err := notify.ParseToken(notifyAnswerToken)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(notify.Pattern, notify.NewHandler(gate, h.Notifier()))
	return mux
}

// postWake posts text from source under key to c1's session on its own goroutine.
func postWake(route http.Handler, source, key, text string) <-chan *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"text": text, "source": source, "dedupe_key": key})
	got := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+notifyAnswerSession+"/notify", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+notifyAnswerToken)
		w := httptest.NewRecorder()
		route.ServeHTTP(w, r)
		got <- w
	}()
	return got
}

// routeAnswerWithin is the route's answer, failing the test when it has none within notifyAnswerCeiling.
func routeAnswerWithin(t *testing.T, got <-chan *httptest.ResponseRecorder, what string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-got:
		return w
	case <-time.After(notifyAnswerCeiling):
		t.Fatalf("%s: no answer within %v of the POST: the route waited on the delivery", what, notifyAnswerCeiling)
	}
	return nil
}

// requireAccepted fails the test unless w is 200 {"delivered":"accepted"}.
func requireAccepted(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	var body struct {
		Delivered string `json:"delivered"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusOK || err != nil || body.Delivered != "accepted" {
		t.Fatalf("%s answered %d %s, want 200 {\"delivered\":\"accepted\"}", what, w.Code, w.Body.String())
	}
}

// entryFrom is the id and state of chatID's first entry whose row came from source, "" for both when none does.
func entryFrom(h *Runtime, chatID marotte.ChatID, source string) (id string, state marotte.RunEntryState) {
	for _, e := range listOf(h, chatID) {
		if e.Row != nil && e.Row.Caller != nil && e.Row.Caller.Label == source {
			return e.ID, e.State
		}
	}
	return "", ""
}

// rowsFrom is the text of every entry on chatID's list whose row came from source, in list order.
func rowsFrom(h *Runtime, chatID marotte.ChatID, source string) []string {
	var out []string
	for _, e := range listOf(h, chatID) {
		if e.Row != nil && e.Row.Caller != nil && e.Row.Caller.Label == source {
			out = append(out, e.Row.Text)
		}
	}
	return out
}

// idleChat stages one turn on c1 and closes it, so c1 has a live, idle life and one turn open in its log.
func idleChat(t *testing.T, s *steerHarness) {
	t.Helper()
	id, _ := s.h.stagePromptTurn(t, "c1")
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnEndMsg("end_turn"))
	s.h.coord.releaseTurn("c1", id)
	idleAfter(t, s.h, "c1", id)
}

// holdFirstLaunch parks c1's first take before its turn opens; parked closes once it is parked, and release (also
// run at cleanup) lets it go on.
func holdFirstLaunch(t *testing.T, h *Runtime) (parked <-chan struct{}, release func()) {
	t.Helper()
	in, gate := make(chan struct{}), make(chan struct{})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	launch := h.coord.launchEntry
	var first sync.Once
	h.coord.launchEntry = func(ctx context.Context, chatID marotte.ChatID, l *command.EntryLaunch) (string, error) {
		first.Do(func() {
			close(in)
			<-gate
		})
		return launch(ctx, chatID, l)
	}
	return in, release
}

func TestNotify_AnswerComesWhileTheDeliveryIsHeld(t *testing.T) {
	t.Run("the steer reply held", func(t *testing.T) {
		s, cs, br := steerOpsHub(t)
		id, _ := s.h.stagePromptTurn(t, "c1")
		s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
		route := notifyRoute(t, s.h, cs)
		release := holdCall(t, br, marotte.MethodSessionSteer)

		requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k1", grelotWake), "the wake"), "the wake")

		wake, state := entryFrom(s.h, "c1", "grelot")
		if state != marotte.RunOffered {
			t.Fatalf("wake entry %q is %q while its steer is unanswered, want offered", wake, state)
		}
		release()
		if !reachesState(s.h, "c1", wake, marotte.RunInKAS) {
			t.Errorf("wake entry = %q after kiro-cli answered its steer, want in_kas", stateOf(s.h, "c1", wake))
		}
	})

	t.Run("the turn open held", func(t *testing.T) {
		s, cs, _ := steerOpsHub(t)
		idleChat(t, s)
		route := notifyRoute(t, s.h, cs)
		parked, release := holdFirstLaunch(t, s.h)

		got := postWake(route, "grelot", "k1", grelotWake)
		<-parked
		requireAccepted(t, routeAnswerWithin(t, got, "the wake"), "the wake")

		if _, state := entryFrom(s.h, "c1", "grelot"); state != marotte.RunStarting {
			t.Fatalf("wake entry is %q while its turn open is held, want starting", state)
		}
		release()
		if next := nextPrompt(t, cs, "c1", 1); next.Text != grelotWake || !fromGrelot(next.Caller) {
			t.Errorf("turn after the release = %q (caller %+v), want the wake from grelot", next.Text, next.Caller)
		}
	})

	t.Run("a close held", func(t *testing.T) {
		s, cs, _ := steerOpsHub(t)
		idleChat(t, s)
		route := notifyRoute(t, s.h, cs)
		hold := s.h.coord.turns.gates.hold("c1")
		t.Cleanup(hold.release)

		w := routeAnswerWithin(t, postWake(route, "grelot", "k1", grelotWake), "the wake")

		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
			t.Errorf("the wake under a held close answered %d (Retry-After %q) %s, want 503 with Retry-After 5",
				w.Code, w.Header().Get("Retry-After"), w.Body.String())
		}
		if got := rowsFrom(s.h, "c1", "grelot"); len(got) != 0 {
			t.Errorf("grelot rows = %q after the refusal, want none", got)
		}
	})
}

func TestNotify_BackToBackWakesAnswerInOrderWhileTheFirstDeliveryIsHeld(t *testing.T) {
	t.Run("two sources each answer and go in POST order", func(t *testing.T) {
		s, cs, _ := steerOpsHub(t)
		idleChat(t, s)
		route := notifyRoute(t, s.h, cs)
		parked, release := holdFirstLaunch(t, s.h)

		first := postWake(route, "grelot", "k1", grelotWake)
		<-parked
		requireAccepted(t, routeAnswerWithin(t, first, "wake A"), "wake A")
		requireAccepted(t, routeAnswerWithin(t, postWake(route, "mail", "k2", "new mail"), "wake B"), "wake B")

		a, aState := entryFrom(s.h, "c1", "grelot")
		b, bState := entryFrom(s.h, "c1", "mail")
		if aState != marotte.RunStarting || bState != marotte.RunPending {
			t.Fatalf("list = A %q %q, B %q %q, want A starting and B pending", a, aState, b, bState)
		}
		release()
		if next := nextPrompt(t, cs, "c1", 1); next.Text != grelotWake || !fromGrelot(next.Caller) {
			t.Fatalf("turn after the release = %q (caller %+v), want wake A from grelot", next.Text, next.Caller)
		}
		if next := nextPrompt(t, cs, "c1", 2); next.Text != "new mail" || next.Caller == nil || next.Caller.Label != "mail" {
			t.Errorf("turn after A's = %q (caller %+v), want wake B from mail", next.Text, next.Caller)
		}
	})

	t.Run("later wakes from the same source behind a held take keep one waiting entry", func(t *testing.T) {
		s, cs, _ := steerOpsHub(t)
		idleChat(t, s)
		route := notifyRoute(t, s.h, cs)
		parked, release := holdFirstLaunch(t, s.h)

		first := postWake(route, "grelot", "k1", "wake 1")
		<-parked
		requireAccepted(t, routeAnswerWithin(t, first, "wake 1"), "wake 1")
		requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k2", "wake 2"), "wake 2"), "wake 2")
		requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k3", "wake 3"), "wake 3"), "wake 3")

		if got, want := rowsFrom(s.h, "c1", "grelot"), []string{"wake 1", "wake 3"}; !slices.Equal(got, want) {
			t.Fatalf("grelot rows = %q, want %q: the held take, then one waiting entry holding the latest text", got, want)
		}
		release()
		if next := nextPrompt(t, cs, "c1", 2); next.Text != "wake 3" || !fromGrelot(next.Caller) {
			t.Errorf("turn after the held take's = %q (caller %+v), want the latest wake", next.Text, next.Caller)
		}
	})
}
