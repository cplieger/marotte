package agent

// The admission slot against the real registry and coordinator.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// tryReserve takes the slot iff free and the life gate admits stamp, never waiting: a probe of who holds the slot.
func (r *turnRegistry) tryReserve(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	lc, ok := r.tryLifecycleAt(chatID, stamp)
	if !ok {
		return command.Reservation{}, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	a := r.reserveLocked(lc, source, stamp, "")
	return a.res, a.res.Token != 0
}

// Not parallel-safe.
func shrinkAdmissionWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := command.AdmissionWait
	command.AdmissionWait = d
	t.Cleanup(func() { command.AdmissionWait = prev })
}

// setCancelGrace sets the cancel grace short to drive expiry or long to keep it from firing. Not parallel-safe.
func setCancelGrace(t *testing.T, d time.Duration) {
	t.Helper()
	prev := command.CancelGrace
	command.CancelGrace = d
	t.Cleanup(func() { command.CancelGrace = prev })
}

func seedChat(t *testing.T, cs *testChatStore, id marotte.ChatID) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

// A prompt against a revert-held chat answers within the budget as queued behind it, and writes nothing: the
// press never waits out the hold.
func TestPrompt_BehindARevertHeldSlotAnswersQueuedAndWritesNothing(t *testing.T) {
	shrinkAdmissionWait(t, 60*time.Millisecond)
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if !h.coord.tryReserveNow("c1", marotte.TurnSourceRevert) {
		t.Fatal("setup: the admission slot was already held")
	}
	t.Cleanup(func() { h.coord.releaseHeld("c1") })

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		As string `json:"as"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if body.As != "queued" {
		t.Errorf("as = %q, want queued behind the held slot (body %s)", body.As, rec.Body.String())
	}
	if entries := logOf(t, cs, "c1"); len(entries) != 0 {
		t.Errorf("log = %d entries, want none: a queued prompt opens no turn", len(entries))
	}
}

// gateSessionOpen parks every session door on the returned gate, installed through the public setter so the wiring is
// pinned too.
func gateSessionOpen(h *Runtime) (entered, gate chan struct{}) {
	entered = make(chan struct{})
	gate = make(chan struct{})
	var once sync.Once
	h.SetPreSessionOpen(func(ctx context.Context) {
		once.Do(func() { close(entered) })
		select {
		case <-gate:
		case <-ctx.Done():
		}
	})
	return entered, gate
}

// waitForTurnClosed polls until exactly want turn_closed frames exist and returns their turn_close entries; a surplus is a defect.
func waitForTurnClosed(t *testing.T, h *Runtime, want int) []marotte.EntryTurnClose {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := closedBroadcasts(t, h)
		if len(got) > want {
			t.Fatalf("turn_closed frames = %d, want exactly %d: a turn announced its close more than once", len(got), want)
		}
		if len(got) == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn_closed frames = %d, want %d", len(got), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// The model is stamped at StartTurn with the bridge live; credits are the turn's own frames, so session-open-window spend never reaches its close.
func TestPromptTurn_MeteringAndModelStampAtStartTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1") // cold: no model on the record yet
	entered, gate := gateSessionOpen(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered
	// Spend lands before StartTurn.
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Usage.Credits = 5
		c.Usage.HasRealData = true
		return true
	}); err != nil {
		t.Fatalf("stage spend: %v", err)
	}
	close(gate)

	closed := waitForTurnClosed(t, h, 1)
	if got := closed[0].Credits; got != 0 {
		t.Errorf("turn_close credits = %v, want 0: spend during the session-open window is not the turn's", got)
	}
	if closed[0].Model == "" {
		t.Error("turn_close model is empty on a cold chat; StartTurn runs after the session open persisted the session's model")
	}
}

// The recovery arbitrates on the captured result: an empty wire-ended turn closes `empty` and re-prompts on a fresh
// session. ReleaseTurn before AwaitTurn would blind it.
func TestPromptTurn_EmptyWireEndedTurnRecoversThroughTheRealRegistry(t *testing.T) {
	cs := newTestChatStore()
	gate := make(chan struct{})
	var mu sync.Mutex
	var minted []*fakeBridge
	factory := func() engineSession {
		mu.Lock()
		defer mu.Unlock()
		br := newFakeBridge()
		if len(minted) == 0 {
			// Only the first session's prompt is held, so turn_end lands mid-call.
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
		}
		minted = append(minted, br)
		return br
	}
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(factory, nil), cs)
	cs.wire(h)
	seedChat(t, cs, "c1")

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	// The wire closes the turn empty while the response is pending.
	first := func() *fakeBridge {
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			var br *fakeBridge
			if len(minted) > 0 {
				br = minted[0]
			}
			mu.Unlock()
			if br != nil {
				return br
			}
			if time.Now().After(deadline) {
				t.Fatal("the prompt goroutine never opened a session")
			}
			time.Sleep(time.Millisecond)
		}
	}()
	waitForCall(t, first, marotte.MethodPrompt)
	first.deliver(first.promptTurnStartMsg())
	first.deliver(newTurnEndMsg("end_turn"))
	close(gate)

	// The recovery re-prompts on a fresh session.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		reopened := len(minted) >= 2
		var second *fakeBridge
		if reopened {
			second = minted[1]
		}
		mu.Unlock()
		if reopened && slices.Contains(second.callLog(), marotte.MethodPrompt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the empty wire-ended turn was never retried: the captured result did not reach the recovery")
		}
		time.Sleep(time.Millisecond)
	}
	// The log records the empty close and the retry as a turn of its own.
	entries := logOf(t, cs, "c1")
	closes := closesOf(t, entries)
	if len(closes) == 0 || closes[0].Outcome != marotte.TurnOutcomeEmpty {
		t.Errorf("turn_close outcomes = %+v, want the first turn closed %q", closes, marotte.TurnOutcomeEmpty)
	}
	opens := opensOf(t, entries)
	if len(opens) != 2 || opens[1].Source != marotte.TurnOpenNameEmptyRetry {
		t.Errorf("turn_open sources = %+v, want [prompt empty_retry]", opens)
	}
}

// Shutdown mid-Call: the prompt unwinds, the turn finalizes, inflight drains and Shutdown returns.
func TestPromptTurn_ShutdownMidCallDrainsTheTurn(t *testing.T) {
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the in-flight prompt goroutine did not drain", err)
	}
}

// Shutdown right after the ack: the in-flight registration precedes the ack, so Shutdown waits for the turn.
func TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)
	defer close(gate)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the pre-goroutine window is registered in-flight before the ack", err)
	}
	// The goroutine reached a terminal signal under shutdown.
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(marotte.EventTurnClosed)); missing != nil {
		if missingErr := missingEvents(types, string(marotte.EventError)); missingErr != nil {
			t.Errorf("events = %v, want a terminal turn_closed or error for the in-flight prompt", types)
		}
	}
}

// A cancel while the admitted prompt's session opens answers 200 and keeps the prompt unsent: its turn closes
// cancelled with no session/prompt, and the chat accepts the next prompt.
func TestPromptTurn_CancelInTheSessionOpenKeepsThePromptUnsent(t *testing.T) {
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // parked in the session open: nothing sent yet

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel in the session-open window = %d, want 200", rec.Code)
	}
	close(gate)
	if closed := waitForTurnClosed(t, h, 1); closed[0].Outcome != marotte.TurnOutcomeCancelled || closed[0].FailureReason != "" {
		t.Errorf("turn_closed = %+v, want cancelled with no failure_reason", closed[0])
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("calls = %v, want no session/prompt for a prompt stopped before its send", br.callLog())
	}

	// The slots released, so a fresh prompt is admitted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := postCmd(t, h, marotte.ClientCommand{
			Type: "prompt", ChatID: "c1",
			Payload: json.RawMessage(`{"text":"again","message_id":"m-2"}`),
		})
		if rec.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a follow-up prompt never got in: last answer %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForTurnClosed(t, h, 2)
}

// When KAS never answers after an unacked session/cancel, the grace expiry must conclude `cancelled`, on both
// surfaces, closed once.
func TestPromptTurn_UnackedCancelConcludesCancelled(t *testing.T) {
	setCancelGrace(t, 20*time.Millisecond)
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	closed := waitForTurnClosed(t, h, 1)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one: %+v", len(closes), closes)
	}
	if closes[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_close outcome = %q, want %q: the reader pressed Stop, so nothing failed",
			closes[0].Outcome, marotte.TurnOutcomeCancelled)
	}
	if closes[0].FailureReason != "" {
		t.Errorf("turn_close failure_reason = %q, want empty: a cancel has no account to give",
			closes[0].FailureReason)
	}
	if closed[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Errorf("turn_closed outcome = %q, want %q: the live surface must agree with the "+
			"persisted one", closed[0].Outcome, marotte.TurnOutcomeCancelled)
	}
}

// Shutdown's cause changes the sentence, never the stop, so a turn without the grace sentinel stays `interrupted`.
// The grace is armed long and Shutdown runs after StartTurn, so the two contend for one close.
func TestPromptTurn_ShutdownDuringTheGraceStaysInterrupted(t *testing.T) {
	setCancelGrace(t, time.Minute)
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)

	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	lc, ok := h.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("the prompt opened no lifecycle")
	}
	lc.mu.Lock()
	armed := lc.call != nil && lc.call.graceTimer != nil
	lc.mu.Unlock()
	if !armed {
		t.Fatal("the cancel armed no grace, so the two cancellers were never contended")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the in-flight prompt goroutine did not drain", err)
	}

	closed := waitForTurnClosed(t, h, 1)
	if closed[0].Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("turn_closed outcome = %q, want %q: a shutdown carries no grace sentinel, "+
			"so it is a fault rather than a stop the reader asked for",
			closed[0].Outcome, marotte.TurnOutcomeInterrupted)
	}
}

func shutdownMidPrompt(t *testing.T, arm func(h *Runtime)) *testChatStore {
	t.Helper()
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, marotte.MethodPrompt)
	if arm != nil {
		arm(h)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	return cs
}

// TestShutdown_TheCutTurnNamesTheRestartAsItsCause: the reader cancelled nothing, so
// the footer must not say they did.
func TestShutdown_TheCutTurnNamesTheRestartAsItsCause(t *testing.T) {
	cs := shutdownMidPrompt(t, nil)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted || closes[0].FailureReason != string(shutdownInterruptCause) {
		t.Errorf("turn_close = outcome %q reason %q, want interrupted with %q",
			closes[0].Outcome, closes[0].FailureReason, shutdownInterruptCause)
	}
}

// TestShutdown_AUserCancelCauseOutranksTheShutdownCause: the cause is first-wins, so
// one already claimed keeps its word.
func TestShutdown_AUserCancelCauseOutranksTheShutdownCause(t *testing.T) {
	const earlier marotte.InterruptCause = "an earlier cause"
	cs := shutdownMidPrompt(t, func(h *Runtime) {
		own, ok := ownID(h, "c1")
		if !ok {
			t.Fatal("no own turn to claim a cause on")
		}
		if !h.coord.turns.interrupt("c1", own, earlier) {
			t.Fatal("the earlier cause was not claimed")
		}
	})

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].FailureReason != string(earlier) {
		t.Errorf("turn_close = %+v, want the earlier cause kept", closes)
	}
}

// A reserve refuses beside a turn KAS bracketed, since each one sends a session/prompt or opens a turn; beside a
// fold-opened wire turn only the idle reserve, which reads every holder, refuses.
func TestTryReserve_BesideAWireTurn(t *testing.T) {
	cases := []struct {
		name     string
		open     func(t *testing.T, h *Runtime)
		wantBare bool
	}{
		{
			name: "a bracketed wire turn",
			open: func(_ *testing.T, h *Runtime) { h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg()) },
		},
		{
			name: "a fold-opened wire turn",
			open: func(t *testing.T, h *Runtime) {
				if h.stageWireTurn(t, "c1") == nil {
					t.Fatal("setup: the fold opened no wire turn")
				}
			},
			wantBare: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			seedChat(t, cs, "c1")
			tc.open(t, h)
			if _, open := ownID(h, "c1"); !open {
				t.Fatal("setup: no wire turn is open")
			}

			if h.coord.tryReserveIdleNow("c1", marotte.TurnSourceRevert) {
				t.Error("TryReserveIdleTurn reserved beside an open wire turn, want refused")
				h.coord.releaseHeld("c1")
			}
			got := h.coord.tryReserveNow("c1", marotte.TurnSourcePrompt)
			if got {
				h.coord.releaseHeld("c1")
			}
			if got != tc.wantBare {
				t.Errorf("tryReserve = %t, want %t", got, tc.wantBare)
			}
		})
	}
}

// wakeTurnHub is a chat with a live bridge and a turn KAS opened itself, nothing streamed into it yet.
func wakeTurnHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	s, cs, br := steerOpsHub(t)
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnStartMsg())
	if _, open := ownID(s.h, "c1"); !open {
		t.Fatal("setup: a turn_start with nothing pending opened no wire turn")
	}
	return s.h, cs, br
}

// The composer's Send beside an agent-initiated turn is steered into it: no turn of its own opens and no
// session/prompt reaches KAS, which would abort that turn.
func TestCmdPrompt_BesideAWireTurnIsSteeredAndOpensNoTurn(t *testing.T) {
	h, cs, br := wakeTurnHub(t)
	opens := len(opensOf(t, logOf(t, cs, "c1")))

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	})
	joinInflight(t, h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		As string `json:"as"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if body.As != "steered" {
		t.Errorf("as = %q, want steered into the running turn", body.As)
	}
	if got := len(opensOf(t, logOf(t, cs, "c1"))); got != opens {
		t.Errorf("turn_open entries = %d, want %d: a steered prompt opens no turn", got, opens)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("bridge calls = %v, want no session/prompt beside the running turn", br.callLog())
	}
}

// A prompt steered into the running turn is a dock row under the id KAS stamps on it, `steer-<messageId>`
// (handleSessionSteer), so KAS's steering_queued for it updates that row instead of adding a second one.
func TestCmdPrompt_ASteeredPromptsRowIsKeyedByTheIDKASStampsOnIt(t *testing.T) {
	s, _, _ := steerOpsHub(t)
	s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnStartMsg())

	rec := postCmd(t, s.h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	})
	joinInflight(t, s.h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if _, ok := s.state("steer-m-1"); !ok {
		t.Errorf("no dock row under steer-m-1, the id KAS's frames name for this steer")
	}
	if _, ok := s.state("m-1"); ok {
		t.Errorf("a dock row is keyed by the bare message id m-1, which no KAS frame names, so it never leaves")
	}
}

// A Send bound for the running turn as a steer while the unread steers already fill kiro-cli's buffer budget is refused
// 409 full, so the client keeps the text: it is neither steered nor queued as a row.
func TestCmdPrompt_ASteerPastTheSteerBudgetIsRefusedFull(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		// unread is how many steers fill the budget before the refused one.
		unread int
	}{
		{name: "past the joined text's size", text: strings.Repeat("x", command.MaxPromptBytes/2+1), unread: 1},
		{name: "past the row count", text: "more", unread: maxSteerRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, br := steerOpsHub(t)
			s.h.translateACPEvent("c1", s.h.originOf("c1"), newTurnStartMsg())
			send := func(id string) *httptest.ResponseRecorder {
				payload, _ := json.Marshal(map[string]string{"text": tc.text, "message_id": id})
				return postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: "c1", Payload: payload})
			}
			for i := range tc.unread {
				if got := ackAs(t, send(fmt.Sprintf("m-%d", i))); got != "steered" {
					t.Fatalf("Setup: send %d answered %q, want steered", i, got)
				}
			}

			rec := send("m-over")
			joinInflight(t, s.h)

			var body struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != http.StatusConflict || err != nil || body.Reason != "full" {
				t.Errorf("the send past the budget = %d %s, want 409 full", rec.Code, rec.Body.String())
			}
			if n := countCalls(br, marotte.MethodSessionSteer); n != tc.unread {
				t.Errorf("session/steer calls = %d, want %d: the refused send is not steered", n, tc.unread)
			}
			if got := stateOf(s.h, "c1", "m-over"); got != "" {
				t.Errorf("the refused send stands on the list as %q, want it gone", got)
			}
		})
	}
}

// A prompt that reads as a KAS notification is never steered: KAS would file it under a notify- id no row
// carries. It waits for the running turn's end instead.
func TestCmdPrompt_NotificationShapedTextBesideATurnIsQueuedNotSteered(t *testing.T) {
	h, _, br := wakeTurnHub(t)

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"[notification/info] hi","message_id":"m-1"}`),
	})
	joinInflight(t, h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		As string `json:"as"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if body.As != "queued" {
		t.Errorf("as = %q, want queued behind the running turn", body.As)
	}
	if slices.Contains(br.callLog(), marotte.MethodSessionSteer) {
		t.Errorf("bridge calls = %v, want no session/steer for notification-shaped text", br.callLog())
	}
}

// mergeHub is a hub whose engine lists the tangent "c-tangent" (session s-fork) as forked from the parent "c1"
// (s-parent) and answers a prompt with a summary.
func mergeHub(t *testing.T) (*Runtime, *testChatStore, func(method string) int) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		marotte.MethodSessionList: sessionRowsJSON(t, map[string]string{"s-fork": "s-parent", "s-parent": ""}),
	}
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"Found it."}}
	seedChain(t, cs, "c1", "s-parent")
	seedChain(t, cs, "c-tangent", "s-fork")
	return h, cs, func(method string) int { return countCalls(br, method) }
}

func mergeCmd(tangent marotte.ChatID) marotte.ClientCommand {
	return marotte.ClientCommand{Type: marotte.CmdMergeTangent, ChatID: tangent, Payload: json.RawMessage(`{"op_id":"op-1"}`)}
}

// Merging a tangent while KAS runs a turn of its own there is refused as busy, like any running turn: the summary
// prompt would abort it.
func TestMergeTangent_ATangentRunningAWakeTurnIsRefusedBusy(t *testing.T) {
	h, _, calls := mergeHub(t)
	h.translateACPEvent("c-tangent", h.originOf("c-tangent"), newTurnStartMsg())

	rec := postCmd(t, h, mergeCmd("c-tangent"))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"busy"`) {
		t.Errorf("merge_tangent = %d %s, want 409 busy", rec.Code, rec.Body.String())
	}
	if n := calls(marotte.MethodPrompt); n != 0 {
		t.Errorf("session/prompt calls = %d, want none beside the running turn", n)
	}
}

// The findings for a parent running a turn KAS started are queued behind it, never prompted into it.
func TestMergeTangent_QueuesTheFindingsBehindAParentsWakeTurn(t *testing.T) {
	h, cs, calls := mergeHub(t)
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())

	if rec := postCmd(t, h, mergeCmd("c-tangent")); rec.Code != http.StatusOK {
		t.Fatalf("merge_tangent = %d %s, want 200", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool {
		c, _ := cs.Get(t.Context(), "c-tangent")
		return len(c.TangentMerges) == 1 && c.TangentMerges[0].State != marotte.TangentMergeRunning
	})

	if c, _ := cs.Get(t.Context(), "c-tangent"); c.TangentMerges[0].State != marotte.TangentMergeSucceeded {
		t.Errorf("merge state = %q, want succeeded", c.TangentMerges[0].State)
	}
	rows := queuedRows(t, cs, "c1")
	if len(rows) != 1 || !strings.Contains(rows[0].Row.Text, "Found it.") {
		t.Errorf("parent's queue = %+v, want the findings queued", rows)
	}
	if n := calls(marotte.MethodPrompt); n != 1 {
		t.Errorf("session/prompt calls = %d, want the tangent's summary only", n)
	}
	if ids := promptIDs(t, cs, "c1"); len(ids) != 0 {
		t.Errorf("parent prompt turns = %v, want none beside the running turn", ids)
	}
}

// A steer while the chat's session door is still opening has no running turn to join: it is refused no_turn, which
// the client answers by sending the words as a prompt, and nothing is left on the list for a later turn.
func TestPromptTurn_SteerInTheSessionDoorWindowIsRefusedNoTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSessionOpen(h)

	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdSteer, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"also this","message_id":"m-2"}`),
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"reason":"no_turn"`) {
		t.Errorf("steer in the door window = %d %s, want 409 no_turn", rec.Code, rec.Body.String())
	}
	close(gate)
	waitForTurnClosed(t, h, 1)
	opens := opensOf(t, logOf(t, cs, "c1"))
	if len(opens) != 1 {
		t.Errorf("turn_open entries = %+v, want the prompt's turn alone", opens)
	}
}
