package agent

// The notify route's answer against a chat whose delivery is slow: the steer reply, the turn open or a close held.
// The answer comes once the wake is on the chat's run list, whatever the delivery is doing.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
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

// sentAfterTurn reports whether text reached kiro-cli after chatID's turn open n: steered (k saw it) or as the prompt
// of a later turn. Which of the two the run order picks depends on whether turn n still runs.
func sentAfterTurn(t *testing.T, k *kas320Steering, cs *testChatStore, chatID marotte.ChatID, n int, text string) bool {
	t.Helper()
	if slices.Contains(k.steered(), text) {
		return true
	}
	opens := opensOf(t, logOf(t, cs, chatID))
	return len(opens) > n+1 && slices.ContainsFunc(opens[n+1:], func(o marotte.EntryTurnOpen) bool {
		return o.Prompt != nil && o.Prompt.Text == text
	})
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
		s, cs, br := steerOpsHub(t)
		k := kas320On(s.h, "c1", br)
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
		waitFor(t, func() bool { return sentAfterTurn(t, k, cs, "c1", 1, "new mail") })
	})

	t.Run("later wakes from the same source behind a held take keep one waiting entry", func(t *testing.T) {
		s, cs, br := steerOpsHub(t)
		k := kas320On(s.h, "c1", br)
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
		if next := nextPrompt(t, cs, "c1", 1); next.Text != "wake 1" || !fromGrelot(next.Caller) {
			t.Fatalf("turn after the release = %q (caller %+v), want wake 1 from grelot", next.Text, next.Caller)
		}
		waitFor(t, func() bool { return sentAfterTurn(t, k, cs, "c1", 1, "wake 3") })
		joinInflight(t, s.h)
		if sentAfterTurn(t, k, cs, "c1", 0, "wake 2") {
			t.Errorf("wake 2 reached kiro-cli, want its text replaced by wake 3")
		}
	})
}

// A wake taken at once whose turn then fails to open was already answered accepted: it waits paused, never lost and
// never retried in a loop, and the caller's next send is the retry, which opens the turn with the later text.
func TestNotify_AWakeWhoseLaunchFailsAfterItsAnswerIsRetriedByTheNextWake(t *testing.T) {
	s, cs, _ := steerOpsHub(t)
	idleChat(t, s)
	route := notifyRoute(t, s.h, cs)
	launch := s.h.coord.launchEntry
	var first sync.Once
	s.h.coord.launchEntry = func(ctx context.Context, chatID marotte.ChatID, l *command.EntryLaunch) (string, error) {
		refused := false
		first.Do(func() { refused = true })
		if refused {
			s.h.coord.ReleaseTurnReservation(l.Res)
			return "", errors.New("kiro-cli refused the turn")
		}
		return launch(ctx, chatID, l)
	}

	requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k1", "wake 1"), "wake 1"), "wake 1")
	joinInflight(t, s.h)
	if _, state := entryFrom(s.h, "c1", "grelot"); state != marotte.RunPaused {
		t.Fatalf("wake entry is %q after its failed launch, want paused", state)
	}
	if n := len(opensOf(t, logOf(t, cs, "c1"))); n != 1 {
		t.Errorf("turns opened = %d after the failed launch, want none", n-1)
	}

	requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k2", "wake 2"), "wake 2"), "wake 2")
	if next := nextPrompt(t, cs, "c1", 1); next.Text != "wake 2" || !fromGrelot(next.Caller) {
		t.Errorf("turn after the resend = %q (caller %+v), want wake 2 from grelot", next.Text, next.Caller)
	}
}

// The accept line names the run entry carrying the wake, and the line that reports its delivery names the same id, so
// one wake's answer and delivery join in the log.
func TestNotify_TheAcceptLineJoinsTheDeliveryLine(t *testing.T) {
	t.Run("steered", func(t *testing.T) {
		logs := capture.Default(t)
		s, cs, _ := steerOpsHub(t)
		id, _ := s.h.stagePromptTurn(t, "c1")
		s.h.translateACPEvent("c1", s.h.originOf("c1"), newPromptTurnStartMsg(id))
		route := notifyRoute(t, s.h, cs)

		requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k1", grelotWake), "the wake"), "the wake")
		wake, _ := entryFrom(s.h, "c1", "grelot")
		if !reachesState(s.h, "c1", wake, marotte.RunInKAS) {
			t.Fatalf("wake entry = %q, want in_kas", stateOf(s.h, "c1", wake))
		}
		joinInflight(t, s.h)
		if got, _ := logs.AttrValueExact("notify accepted", "message_id"); got != wake {
			t.Errorf("accept line message_id = %q, want the wake entry %q", got, wake)
		}
		if got, _ := logs.AttrValueExact("queued message steered", "message_id"); got != wake {
			t.Errorf("steer line message_id = %q, want the wake entry %q", got, wake)
		}
	})
	t.Run("a turn of its own", func(t *testing.T) {
		logs := capture.Default(t)
		s, cs, _ := steerOpsHub(t)
		idleChat(t, s)
		route := notifyRoute(t, s.h, cs)

		requireAccepted(t, routeAnswerWithin(t, postWake(route, "grelot", "k1", grelotWake), "the wake"), "the wake")
		nextPrompt(t, cs, "c1", 1)
		joinInflight(t, s.h)
		accepted, _ := logs.AttrValueExact("notify accepted", "message_id")
		if accepted == "" || !slices.Contains(logs.AttrValuesExact("queued prompt dispatched", "message_id"), accepted) {
			t.Errorf("accept line message_id = %q, dispatch lines %q: want the accepted id among them",
				accepted, logs.AttrValuesExact("queued prompt dispatched", "message_id"))
		}
	})
}
