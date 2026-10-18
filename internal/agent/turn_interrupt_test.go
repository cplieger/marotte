package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

const (
	firstPrompt  = "Please tidy the settings page"
	secondPrompt = "Now rename the save button"
)

// interruptHub is c1 with its first prompt's session/prompt in flight; KAS answers it when answer closes.
func interruptHub(t *testing.T) (h *Runtime, cs *testChatStore, br *fakeBridge, first string, answer chan struct{}, prompted func() []string) {
	t.Helper()
	h, cs, br = newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	countDrains(h)
	prompted = promptCalls(br)
	answer = make(chan struct{})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
	br.mu.Unlock()
	admitPromptBehindSteerLock(t, h, cs, firstPrompt)()
	synctest.Wait()
	turn, ok := h.coord.PromptTurn("c1")
	if !ok || len(prompted()) != 1 {
		t.Fatalf("Setup: the first prompt is not in flight (turn %v, session/prompt %v)", ok, prompted())
	}
	return h, cs, br, turn.ID(), answer, prompted
}

// endFirstRun is KAS ending the first prompt's run as recorded: turn_start, its reply, turn_end, then its
// session/prompt answer.
func endFirstRun(br *fakeBridge, answer chan struct{}) {
	br.mu.Lock()
	session := br.sessionID
	br.mu.Unlock()
	br.deliver(newTurnStartMsg())
	br.deliver(newSessionChunkMsg(session, "Tidied."))
	br.deliver(newTurnEndMsg("end_turn"))
	close(answer)
	synctest.Wait()
}

// sendSecondPrompt posts the user's next prompt, whose session/prompt KAS answers only when the returned channel
// closes, and returns its turn.
func sendSecondPrompt(t *testing.T, h *Runtime, br *fakeBridge) (turn string, answer chan struct{}) {
	t.Helper()
	answer = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-answer:
		default:
			close(answer)
		}
	})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
	br.mu.Unlock()
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"` + secondPrompt + `","message_id":"m-3"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Setup: second prompt = %d %s, want admitted", rec.Code, rec.Body.String())
	}
	synctest.Wait()
	p, ok := h.coord.PromptTurn("c1")
	if !ok {
		t.Fatal("Setup: the second prompt has no turn")
	}
	return p.ID(), answer
}

// assertPromptLive checks the user's prompt turn is still open, unfailed and carries no interrupt cause, with its
// words sent to KAS want times.
func assertPromptLive(t *testing.T, h *Runtime, cs *testChatStore, turn string, prompted []string, want int) {
	t.Helper()
	if failed := promptFailures(h); len(failed) != 0 {
		t.Errorf("prompt_failed = %q, want none", failed)
	}
	if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), turn); closed {
		t.Errorf("the user's prompt turn closed (%+v), want it live", c)
	}
	if live, ok := h.coord.turns.turnByID("c1", turn); !ok {
		t.Error("the user's prompt turn left the registry, want it live")
	} else if cause := h.coord.turns.interruptCause(live); cause != "" {
		t.Errorf("the user's prompt turn carries interrupt cause %q, want none", cause)
	}
	sent := 0
	for _, p := range prompted {
		if strings.Contains(p, secondPrompt) {
			sent++
		}
	}
	if sent != want {
		t.Errorf("the user's prompt went to KAS %d times (session/prompt %v), want %d", sent, prompted, want)
	}
}

// assertClosesClean answers the user's prompt and checks its turn closes with no failure reason.
func assertClosesClean(t *testing.T, br *fakeBridge, cs *testChatStore, turn string, answer chan struct{}) {
	t.Helper()
	br.mu.Lock()
	session := br.sessionID
	br.mu.Unlock()
	br.deliver(newTurnStartMsg())
	br.deliver(newSessionChunkMsg(session, "Renamed."))
	br.deliver(newTurnEndMsg("end_turn"))
	close(answer)
	synctest.Wait()
	if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), turn); !closed || c.FailureReason != "" {
		t.Errorf("the user's prompt turn_close = %+v (closed %v), want closed with no failure_reason", c, closed)
	}
}

// A failed-compaction reap whose turn closes, and whose next prompt is sent, while the expiry is between its claim
// and its cancel leaves that prompt live, unstamped and sent once.
func TestCompactionReap_AnExpiryOutlivingItsTurnLeavesTheNextPromptAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := newLogLineBlock(t, "the turn stayed silent after a failed compaction; interrupting it")
		swapDefaultLogger(t, block)
		h, cs, br, first, answerFirst, prompted := interruptHub(t)

		h.coord.CompactionFailed("c1", first, "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		<-block.entered
		endFirstRun(br, answerFirst)
		second, answerSecond := sendSecondPrompt(t, h, br)
		block.resume()
		synctest.Wait()

		assertPromptLive(t, h, cs, second, prompted(), 1)
		assertClosesClean(t, br, cs, second, answerSecond)
	})
}

// A failed-compaction reap whose turn then ended empty, and was retried on a fresh session, cancels only that
// turn's own call when it resumes: the retry re-sending the user's words stays live and is sent once.
func TestCompactionReap_AnExpiryOutlivingAnEmptyTurnLeavesItsRetryAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := newLogLineBlock(t, "the turn stayed silent after a failed compaction; interrupting it")
		swapDefaultLogger(t, block)
		answerFirst, answerRetry := make(chan struct{}), make(chan struct{})
		t.Cleanup(func() { close(answerRetry) })
		var (
			mu           sync.Mutex
			minted       []*fakeBridge
			retryPrompts atomic.Int32
		)
		factory := func() engineSession {
			mu.Lock()
			defer mu.Unlock()
			br := newFakeBridge()
			gate := answerFirst
			if len(minted) > 0 {
				gate = answerRetry
				br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
					if method == marotte.MethodPrompt {
						retryPrompts.Add(1)
					}
					return nil, nil, false
				}
			}
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
			minted = append(minted, br)
			return br
		}
		firstBridge := func() *fakeBridge {
			mu.Lock()
			defer mu.Unlock()
			if len(minted) == 0 {
				return nil
			}
			return minted[0]
		}
		cs := newTestChatStore()
		h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(factory, nil), cs)
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		settleBoot(t, h)
		seedChat(t, cs, "c1")
		if rec := postCmd(t, h, marotte.ClientCommand{
			Type: marotte.CmdPrompt, ChatID: "c1", Payload: json.RawMessage(`{"text":"` + firstPrompt + `","message_id":"m-1"}`),
		}); rec.Code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d %s", rec.Code, rec.Body.String())
		}
		synctest.Wait()
		first, ok := h.coord.PromptTurn("c1")
		br := firstBridge()
		if !ok || br == nil {
			t.Fatal("Setup: the first prompt is not in flight")
		}

		h.coord.CompactionFailed("c1", first.ID(), "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		<-block.entered
		br.deliver(newTurnStartMsg())
		br.deliver(newTurnEndMsg("end_turn"))
		close(answerFirst)
		synctest.Wait()
		retry, ok := h.coord.PromptTurn("c1")
		if !ok || retry.ID() == first.ID() || retryPrompts.Load() != 1 {
			t.Fatalf("Setup: the empty turn's retry is not in flight (turn %v, its session/prompt %d)", ok, retryPrompts.Load())
		}

		block.resume()
		synctest.Wait()
		if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), retry.ID()); closed {
			t.Errorf("the retry's turn closed (%+v) when the reap resumed, want it live", c)
		}
		if _, live := h.coord.turns.turnByID("c1", retry.ID()); !live {
			t.Error("the retry's turn left the registry, want it live")
		}
		if n := retryPrompts.Load(); n != 1 {
			t.Errorf("the retry's session/prompt went out %d times, want once", n)
		}
	})
}

// kiro-cli's tool-use filter notice names the turn it ended: once that turn closed and the user's next prompt was
// sent, the interrupt reaches neither.
func TestInterruptTurn_ATurnThatClosedLeavesTheNextPromptAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br, first, answerFirst, prompted := interruptHub(t)
		endFirstRun(br, answerFirst)
		second, answerSecond := sendSecondPrompt(t, h, br)

		h.coord.InterruptTurn("c1", first, interruptReasonForTest)
		synctest.Wait()

		assertPromptLive(t, h, cs, second, prompted(), 1)
		assertClosesClean(t, br, cs, second, answerSecond)
	})
}

// interruptReasonForTest is the translator's filter reason, which this package cannot import.
const interruptReasonForTest = "Stopped by kiro-cli's tool-use security filter"

// The filter notice during a notification re-wake ends the re-wake's turn only: the user's prompt waiting behind it
// holds the bridge, and is sent once the re-wake's run ends.
func TestInterruptTurn_ARewakeTurnLeavesThePromptWaitingBehindIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		prompted := promptCalls(br)
		answer := make(chan struct{})
		defer close(answer)
		br.mu.Lock()
		br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
		br.mu.Unlock()
		release := admitPromptBehindSteerLock(t, h, cs, secondPrompt)
		prompt, ok := h.coord.PromptTurn("c1")
		if !ok {
			t.Fatal("Setup: the admitted prompt has no turn")
		}
		rewake := ""
		for _, s := range []sendGateStep{kasActive, kasTurnStart, gateRelease, kasReply} {
			s.apply(t, h, release, &rewake)
			synctest.Wait()
		}

		h.coord.InterruptTurn("c1", rewake, interruptReasonForTest)
		synctest.Wait()
		assertPromptLive(t, h, cs, prompt.ID(), prompted(), 0)
		if got := h.coord.turns.interruptCause(mustTurn(t, h, rewake)); got != interruptReasonForTest {
			t.Errorf("the re-wake's interrupt cause = %q, want %q", got, interruptReasonForTest)
		}

		for _, s := range []sendGateStep{kasIdleReport, kasTurnEnd} {
			s.apply(t, h, release, &rewake)
			synctest.Wait()
		}
		assertPromptLive(t, h, cs, prompt.ID(), prompted(), 1)
		if _, closed := closeOfTurn(t, logOf(t, cs, "c1"), rewake); !closed {
			t.Error("the re-wake's turn is still open after its turn_end")
		}
	})
}

func mustTurn(t *testing.T, h *Runtime, turn string) *activeTurn {
	t.Helper()
	live, ok := h.coord.turns.turnByID("c1", turn)
	if !ok {
		t.Fatalf("turn %q is not live", turn)
	}
	return live
}

// An interrupt trips the named turn's own prompt call with no cause, so it concludes `interrupted`; a turn with no
// prompt call, or one already interrupted, only keeps its first cause.
func TestInterruptTurn_TripsOnlyTheNamedTurnsOwnCall(t *testing.T) {
	t.Run("a prompt turn", func(t *testing.T) {
		h, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		pctx, cancel := context.WithCancelCause(context.Background())
		t.Cleanup(func() { cancel(nil) })
		id, _ := h.stagePromptTurnCalling(t, "c1", cancel)
		t.Cleanup(func() { h.ReleaseTurn("c1", id) })

		h.coord.InterruptTurn("c1", id, "first")
		if got := context.Cause(pctx); got != context.Canceled {
			t.Errorf("prompt context cause = %v, want context.Canceled, so the turn concludes interrupted", got)
		}
		h.coord.InterruptTurn("c1", id, "second")
		if got := h.coord.turns.interruptCause(mustTurn(t, h, id)); got != "first" {
			t.Errorf("interrupt cause = %q, want the first claim %q", got, "first")
		}
	})
	t.Run("a turn with no prompt call", func(t *testing.T) {
		h, cs, _ := newTestHub()
		seedChat(t, cs, "c1")
		wire := h.coord.openWireTurn(t.Context(), "c1", true)
		if wire == nil {
			t.Fatal("Setup: the agent's turn did not open")
		}
		id := wire.ID

		h.coord.InterruptTurn("c1", id, "first")
		if got := h.coord.turns.interruptCause(mustTurn(t, h, id)); got != "first" {
			t.Errorf("interrupt cause = %q, want %q", got, "first")
		}
	})
}

// A stop during a notification re-wake targets that turn: the user's prompt waiting behind it is sent once the
// re-wake ends and outlives the cancel grace.
func TestCmdCancel_DuringARewakeLeavesThePromptWaitingBehindIt(t *testing.T) {
	cases := []struct {
		name string
		// closing has a closer hold the re-wake's turn when the stop arrives.
		closing bool
	}{
		{name: "the re-wake is open"},
		{name: "the re-wake is closing", closing: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, cs, br := newTestHub()
				defer shutdownHub(t, h)
				countDrains(h)
				prompted := promptCalls(br)
				answer := make(chan struct{})
				defer close(answer)
				br.mu.Lock()
				br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answer}
				br.mu.Unlock()
				release := admitPromptBehindSteerLock(t, h, cs, secondPrompt)
				prompt, ok := h.coord.PromptTurn("c1")
				if !ok {
					t.Fatal("Setup: the admitted prompt has no turn")
				}
				rewake := ""
				for _, s := range []sendGateStep{kasActive, kasTurnStart, gateRelease, kasReply} {
					s.apply(t, h, release, &rewake)
					synctest.Wait()
				}
				var held *activeTurn
				if tc.closing {
					if held, ok = h.coord.turns.claimTurn(t.Context(), "c1", rewake, nil); !ok {
						t.Fatal("Setup: could not claim the re-wake turn")
					}
				}

				if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
					t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
				}
				if held != nil {
					h.coord.finalizeTurn(t.Context(), "c1", &turnClose{Closer: closerWireEnd, Stop: marotte.StopReasonCancelled, Turn: rewake, Claimed: held})
				}
				for _, s := range []sendGateStep{kasIdleReport, kasTurnEnd} {
					s.apply(t, h, release, &rewake)
					synctest.Wait()
				}
				assertPromptLive(t, h, cs, prompt.ID(), prompted(), 1)

				time.Sleep(2 * command.CancelGrace)
				synctest.Wait()
				assertPromptLive(t, h, cs, prompt.ID(), prompted(), 1)
			})
		})
	}
}
