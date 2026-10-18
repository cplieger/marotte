package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// A short grace for the call-grace rows; each runs in a bubble, so it is exact.
const stopGrace = 20 * time.Millisecond

// stagedCall is c1 with a sent prompt whose call ctx a stop's grace cancels.
func stagedCall(t *testing.T) (h *Runtime, turn command.TurnRef, callCtx context.Context) {
	t.Helper()
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	// Not t.Context(): cancel runs from t.Cleanup, after t.Context() is already done.
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	id, _ := h.stagePromptTurnCalling(t, "c1", cancel)
	t.Cleanup(func() { h.ReleaseTurn("c1", id) })
	return h, command.TurnRef{Chat: "c1", Turn: id}, ctx
}

// stopNow is a stop whose cancel went out: its targets captured and its send hold lifted.
func stopNow(h *Runtime) *command.TurnStop {
	stop := h.coord.RequestStop("c1")
	h.coord.ReleaseStop(stop)
	return stop
}

// KAS never answering a stopped prompt's call leaves marotte to cancel it at exactly the grace, with the cause that
// concludes `cancelled` rather than `interrupted`.
func TestArmStopGrace_CancelsAnUnansweredCallAtTheGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, turn, callCtx := stagedCall(t)
		stop := stopNow(h)
		if stop.Call != turn {
			t.Fatalf("the stop captured call %+v, want the sent prompt %+v", stop.Call, turn)
		}
		start := time.Now()
		if !h.coord.ArmStopGrace(stop, stopGrace) {
			t.Fatal("ArmStopGrace = false with a call in flight")
		}
		<-callCtx.Done()
		if elapsed := time.Since(start); elapsed != stopGrace {
			t.Errorf("the call was cancelled after %v, want exactly the grace %v", elapsed, stopGrace)
		}
		if !errors.Is(context.Cause(callCtx), command.ErrCancelGraceExpired) {
			t.Errorf("context.Cause = %v, want %v", context.Cause(callCtx), command.ErrCancelGraceExpired)
		}
		if !errors.Is(callCtx.Err(), context.Canceled) {
			t.Errorf("ctx.Err() = %v, want context.Canceled", callCtx.Err())
		}
	})
}

// A call that returned inside the grace keeps its context: EndPromptCall disarms the budget.
func TestArmStopGrace_AnAnsweredCallIsLeftAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, turn, callCtx := stagedCall(t)
		h.coord.ArmStopGrace(stopNow(h), stopGrace)
		h.coord.EndPromptCall(turn)

		time.Sleep(4 * stopGrace)
		if callCtx.Err() != nil {
			t.Errorf("the call was cancelled after it returned: %v", context.Cause(callCtx))
		}
	})
}

// A stop captured on one prompt's call never reaches the next prompt's, however late its grace is armed.
func TestArmStopGrace_ALaterPromptsCallIsNeverReached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, first, _ := stagedCall(t)
		stop := stopNow(h)
		h.coord.EndPromptCall(first)
		endTurn(t, h, "c1", first.Turn)
		nextCtx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		next, _ := h.stagePromptTurnCalling(t, "c1", cancel)
		defer h.ReleaseTurn("c1", next)

		if h.coord.ArmStopGrace(stop, stopGrace) {
			t.Error("ArmStopGrace = true for a stop whose call and turn are gone")
		}
		time.Sleep(4 * stopGrace)
		if nextCtx.Err() != nil {
			t.Errorf("the next prompt's call was cancelled by an earlier stop: %v", context.Cause(nextCtx))
		}
	})
}

// An open that replaces a turn is refused once a stop followed that turn, in the open's own section, and goes ahead
// when no stop did.
func TestOpenTurn_ReplacingAStoppedTurnIsRefused(t *testing.T) {
	retry := func(h *Runtime, replaces string) error {
		id, err := h.coord.OpenTurn(t.Context(), "c1", command.TurnOpen{
			Source: marotte.TurnSourceEmptyRetry, Prompt: &marotte.EntryPrompt{ID: "m-r", Text: "prompt"}, Replaces: replaces,
		})
		if err == nil {
			h.ReleaseTurn("c1", id)
		}
		return err
	}
	cases := []struct {
		name    string
		want    error
		stopped bool
	}{
		{name: "a stop followed the replaced turn", stopped: true, want: command.ErrTurnStopped},
		{name: "no stop followed it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			defer shutdownHub(t, h)
			seedChat(t, cs, "c1")
			empty, _ := h.stagePromptTurn(t, "c1")
			defer h.ReleaseTurn("c1", empty)
			if tc.stopped {
				stopNow(h)
			}
			endTurn(t, h, "c1", empty)
			if err := retry(h, empty); !errors.Is(err, tc.want) {
				t.Errorf("the replacing open = %v, want %v", err, tc.want)
			}
		})
	}
}

// A stop whose session/cancel is still being written holds every send of the chat: a prompt admitted after the
// stopped turn ended goes only once the cancel is out, so KAS's cancel cannot land on it, and the stop's grace then
// leaves it running with its words sent once.
func TestCmdCancel_AStopOutlivingItsTurnLeavesTheNextPromptAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br, _, answerFirst, prompted := interruptHub(t)
		cancelOut := make(chan struct{})
		br.mu.Lock()
		br.blockNotifyOn = map[string]chan struct{}{marotte.MethodCancel: cancelOut}
		br.mu.Unlock()
		stopped := make(chan int)
		go func() {
			stopped <- postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}).Code
		}()
		synctest.Wait()

		endFirstRun(br, answerFirst)
		second, answerSecond := sendSecondPrompt(t, h, br)
		assertPromptLive(t, h, cs, second, prompted(), 0)

		close(cancelOut)
		if code := <-stopped; code != http.StatusOK {
			t.Fatalf("cancel = %d, want 200", code)
		}
		synctest.Wait()
		assertPromptLive(t, h, cs, second, prompted(), 1)

		time.Sleep(2 * command.CancelGrace)
		synctest.Wait()
		assertPromptLive(t, h, cs, second, prompted(), 1)
		assertClosesClean(t, br, cs, second, answerSecond)
	})
}

// A sent prompt whose turn a second turn_start closed (its bracket lost) still holds its call, the bridge and the
// chat's admission: a stop with KAS's turn open beside it cancels that call at the grace too, so the chat takes
// the user's next prompt.
func TestCmdCancel_ASupersededPromptsUnansweredCallIsReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br, first, _, prompted := interruptHub(t)
		br.deliver(newTurnStartMsg())
		br.deliver(newTurnStartMsg())
		synctest.Wait()
		if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), first); !closed || c.Outcome == "" {
			t.Fatalf("Setup: the first prompt's turn_close = %+v (closed %v), want it closed by the second turn_start", c, closed)
		}

		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(command.CancelGrace)
		synctest.Wait()

		second, answerSecond := sendSecondPrompt(t, h, br)
		assertPromptLive(t, h, cs, second, prompted(), 1)
		assertClosesClean(t, br, cs, second, answerSecond)
	})
}

// A stop while the send gate closes a fold-opened re-wake to hand the chat to the user's waiting prompt finds
// nothing else running: that prompt is what the stop stops, so it is never sent and closes cancelled, quietly.
func TestCmdCancel_DuringTheHandoffCloseKeepsTheWaitingPromptUnsent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := newLogLineBlock(t, "closing the agent's unbracketed turn so an admitted prompt can be sent")
		swapDefaultLogger(t, block)
		h, cs, br, rewake := gateRewakeHub(t, nil)
		prompted := promptCalls(br)
		prompt, ok := h.coord.PromptTurn("c1")
		if !ok {
			t.Fatal("Setup: the admitted prompt has no turn")
		}

		kasIdleReport.apply(t, h, nil, nil)
		<-block.entered
		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
		}
		block.resume()
		synctest.Wait()

		if got := prompted(); len(got) != 0 {
			t.Errorf("session/prompt blocks = %v after the stop, want none", got)
		}
		if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), prompt.ID()); !closed || c.Outcome != marotte.TurnOutcomeCancelled || c.FailureReason != "" {
			t.Errorf("the prompt's turn_close = %+v (closed %v), want cancelled with no failure_reason", c, closed)
		}
		if failed := promptFailures(h); len(failed) != 0 {
			t.Errorf("prompt_failed = %q, want none for the reader's own stop", failed)
		}
		if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), rewake); !closed || c.FailureReason != promptHandoffReason {
			t.Errorf("the re-wake's turn_close = %+v (closed %v), want failure_reason %q", c, closed, promptHandoffReason)
		}
	})
}

// postPrompt posts the user's prompt m-1 and answers its HTTP status.
func postPrompt(t *testing.T, h *Runtime, text string) int {
	t.Helper()
	return postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"` + text + `","message_id":"m-` + text + `"}`),
	}).Code
}

// assertStoppedUnsent checks the chat's only turn closed cancelled with no failure_reason and no prompt_failed, and
// that no session/prompt went out.
func assertStoppedUnsent(t *testing.T, h *Runtime, br *fakeBridge) {
	t.Helper()
	if wire := br.wireLog(); slices.Contains(wire, marotte.MethodPrompt) {
		t.Errorf("wire = %v, want no session/prompt for the prompt the reader stopped", wire)
	}
	assertOnlyTurnCancelledQuietly(t, h)
}

// assertOnlyTurnCancelledQuietly checks the chat's only turn closed cancelled with no failure_reason and no
// prompt_failed.
func assertOnlyTurnCancelledQuietly(t *testing.T, h *Runtime) {
	t.Helper()
	if closed := closedBroadcasts(t, h); len(closed) != 1 || closed[0].Outcome != marotte.TurnOutcomeCancelled || closed[0].FailureReason != "" {
		t.Errorf("turn_closed = %+v, want the prompt's one turn cancelled with no failure_reason", closed)
	}
	if failed := promptFailures(h); len(failed) != 0 {
		t.Errorf("prompt_failed = %q, want none for the reader's own stop", failed)
	}
}

// assertNoPromptAfterTheCancel checks no session/prompt frame follows the stop's session/cancel on br's wire.
func assertNoPromptAfterTheCancel(t *testing.T, br *fakeBridge) {
	t.Helper()
	wire := br.wireLog()
	at := slices.Index(wire, marotte.MethodCancel)
	if at < 0 {
		t.Fatalf("wire = %v, want the stop's session/cancel", wire)
	}
	if slices.Contains(wire[at+1:], marotte.MethodPrompt) {
		t.Errorf("wire = %v, want no session/prompt after the stop's session/cancel", wire)
	}
}

// promptFrames counts the session/prompt frames on br's wire.
func promptFrames(br *fakeBridge) int {
	sent := 0
	for _, m := range br.wireLog() {
		if m == marotte.MethodPrompt {
			sent++
		}
	}
	return sent
}

// assertNextPromptSent checks the chat then takes the user's next prompt and writes it once.
func assertNextPromptSent(t *testing.T, h *Runtime, br *fakeBridge) {
	t.Helper()
	before := promptFrames(br)
	if code := postPrompt(t, h, "again"); code != http.StatusOK {
		t.Fatalf("the next prompt = %d, want admitted", code)
	}
	synctest.Wait()
	if sent := promptFrames(br) - before; sent != 1 {
		t.Errorf("session/prompt frames for the next prompt = %d, want 1", sent)
	}
}

// A stop between a prompt's admission and its turn's open finds nothing else running, so the admitted prompt is
// what it stops: the turn opens, is never sent and closes cancelled, quietly, and the chat takes the next prompt.
func TestCmdCancel_BetweenAdmissionAndTheOpenKeepsThePromptUnsent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		seedChat(t, cs, "c1")
		entered, resume := make(chan struct{}), make(chan struct{})
		var once sync.Once
		h.coord.turns.openHook = func() { once.Do(func() { close(entered); <-resume }) }
		acked := make(chan int)
		go func() { acked <- postPrompt(t, h, "hi") }()
		<-entered

		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
		}
		close(resume)
		if code := <-acked; code != http.StatusOK {
			t.Fatalf("prompt = %d, want its ack", code)
		}
		synctest.Wait()

		assertStoppedUnsent(t, h, br)
		assertNextPromptSent(t, h, br)
	})
}

// A stop after the send gate let a prompt through but before its frame reached the engine's write lock finds that
// prompt as its call: the prompt is never written, so the stop's session/cancel cannot precede it on the wire, and
// it closes cancelled, quietly.
func TestCmdCancel_BetweenTheSendGateAndTheWriteKeepsThePromptUnsent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		seedChat(t, cs, "c1")
		write := make(chan struct{})
		br.mu.Lock()
		br.blockWriteOn = map[string]chan struct{}{marotte.MethodPrompt: write}
		br.mu.Unlock()
		if code := postPrompt(t, h, "hi"); code != http.StatusOK {
			t.Fatalf("prompt = %d, want its ack", code)
		}
		synctest.Wait()
		lc, _ := h.coord.turns.lookup("c1")
		lc.mu.Lock()
		letThrough := lc.call != nil
		lc.mu.Unlock()
		if !letThrough {
			t.Fatal("Setup: the send gate has not let the prompt through")
		}

		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
		}
		br.mu.Lock()
		br.blockWriteOn = nil
		br.mu.Unlock()
		close(write)
		synctest.Wait()

		assertStoppedUnsent(t, h, br)
		assertNextPromptSent(t, h, br)
	})
}

// openAdmitted opens and starts the admitted prompt's turn and answers what its send gate says.
func openAdmitted(t *testing.T, h *Runtime) error {
	t.Helper()
	return sendGate(t, h, startAdmitted(t, h))
}

// sendGate answers what c1's send gate says to the turn's prompt.
func sendGate(t *testing.T, h *Runtime, id string) error {
	t.Helper()
	_, err := h.coord.AwaitPromptSend(t.Context(), "c1", id)
	return err
}

// startAdmitted opens and starts the admitted prompt's turn.
func startAdmitted(t *testing.T, h *Runtime) string {
	t.Helper()
	id, err := h.coord.OpenTurn(t.Context(), "c1", command.TurnOpen{
		Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "prompt"},
	})
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	t.Cleanup(func() { h.ReleaseTurn("c1", id) })
	if !h.coord.StartTurn(t.Context(), command.TurnRef{Chat: "c1", Turn: id}, nil) {
		t.Fatal("StartTurn refused the admitted prompt's turn")
	}
	return id
}

// reservePrompt takes c1's admission for a prompt.
func reservePrompt(t *testing.T, h *Runtime) {
	t.Helper()
	if got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", time.Second); got != command.AdmissionAcquired {
		t.Fatalf("ReserveTurnForPrompt = %v, want acquired", got)
	}
}

// A stop that finds a prompt admitted but not yet opened keeps the turn that admission opens unsent, whether the
// open lands before or after the stop's cancel went out, and reaches no other admission.
func TestArmStopGrace_KeepsAnAdmittedPromptUnsentWhereverItsOpenLands(t *testing.T) {
	cases := []struct {
		act  func(t *testing.T, h *Runtime) error
		want error
		name string
	}{
		{name: "opened after the cancel went out", want: command.ErrPromptStopped, act: func(t *testing.T, h *Runtime) error {
			reservePrompt(t, h)
			stop := h.coord.RequestStop("c1")
			if !h.coord.ArmStopGrace(stop, stopGrace) {
				t.Error("ArmStopGrace = false with an admitted prompt")
			}
			h.coord.ReleaseStop(stop)
			return openAdmitted(t, h)
		}},
		{name: "opened while the cancel was going out", want: command.ErrPromptStopped, act: func(t *testing.T, h *Runtime) error {
			reservePrompt(t, h)
			stop := h.coord.RequestStop("c1")
			id := startAdmitted(t, h)
			if !h.coord.ArmStopGrace(stop, stopGrace) {
				t.Error("ArmStopGrace = false with the admitted prompt's turn open")
			}
			h.coord.ReleaseStop(stop)
			return sendGate(t, h, id)
		}},
		{name: "a later admission is not the stop's", act: func(t *testing.T, h *Runtime) error {
			reservePrompt(t, h)
			stop := h.coord.RequestStop("c1")
			h.coord.ReleaseTurnReservation("c1")
			reservePrompt(t, h)
			if h.coord.ArmStopGrace(stop, stopGrace) {
				t.Error("ArmStopGrace = true for an admission taken after the stop")
			}
			h.coord.ReleaseStop(stop)
			return openAdmitted(t, h)
		}},
		{name: "a cancel that never went out", act: func(t *testing.T, h *Runtime) error {
			reservePrompt(t, h)
			h.coord.ReleaseStop(h.coord.RequestStop("c1"))
			return openAdmitted(t, h)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			defer shutdownHub(t, h)
			seedChat(t, cs, "c1")
			if err := tc.act(t, h); !errors.Is(err, tc.want) {
				t.Errorf("the admitted prompt's send gate = %v, want %v", err, tc.want)
			}
		})
	}
}

// A stop names an admission as its target only when nothing else runs and the admission is a prompt's.
func TestRequestStop_AnAdmissionIsItsTargetOnlyWhenNothingElseRuns(t *testing.T) {
	cases := []struct {
		admit func(t *testing.T, h *Runtime)
		name  string
		want  bool
	}{
		{name: "a prompt with nothing else running", want: true, admit: reservePrompt},
		{name: "a rewind's admission", admit: func(t *testing.T, h *Runtime) {
			if !h.coord.TryReserveIdleTurn("c1", marotte.TurnSourceRevert) {
				t.Fatal("Setup: the rewind's admission was refused")
			}
		}},
		{name: "while KAS reports a run", admit: func(t *testing.T, h *Runtime) {
			reservePrompt(t, h)
			h.coord.WireTurnActivity("c1", true)
		}},
		{name: "beside an open turn KAS started", admit: func(t *testing.T, h *Runtime) {
			reservePrompt(t, h)
			if h.coord.openWireTurn(t.Context(), "c1", false) == nil {
				t.Fatal("Setup: the agent's turn did not open")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			defer shutdownHub(t, h)
			seedChat(t, cs, "c1")
			tc.admit(t, h)
			stop := h.coord.RequestStop("c1")
			defer h.coord.ReleaseStop(stop)
			if got := stop.Admitted != 0; got != tc.want {
				t.Errorf("the stop targets the admission = %v (Admitted %d), want %v", got, stop.Admitted, tc.want)
			}
		})
	}
}

// busyReply is KAS's SessionBusyError answer to a session/prompt as the engine returns it (bridge's replyOutcome).
func busyReply() error {
	re := &marotte.RPCError{Code: marotte.RPCCodeSessionBusy, Message: "A prompt is already in-flight for session sess_x"}
	return fmt.Errorf("ACP error %d: %w: %w", re.Code, marotte.ErrSessionBusy, re)
}

// pastRetryDelay is longer than the prompt path's busy retry delay and shorter than command.CancelGrace.
const pastRetryDelay = 3 * time.Second

// busyFirstAttempt is c1 with a prompt whose first session/prompt KAS answered busy, its retry waiting out the delay.
func busyFirstAttempt(t *testing.T, br *fakeBridge, h *Runtime) {
	t.Helper()
	br.setCallErr(marotte.MethodPrompt, busyReply())
	if code := postPrompt(t, h, "hi"); code != http.StatusOK {
		t.Fatalf("prompt = %d, want its ack", code)
	}
	synctest.Wait()
	if n := promptFrames(br); n != 1 {
		t.Fatalf("Setup: session/prompt frames = %d, want the busy first attempt", n)
	}
	br.clearCallErr(marotte.MethodPrompt)
}

// A stop while a busy prompt's retry waits out its delay stops that prompt: the retry is never written after the
// stop's session/cancel, the turn closes cancelled, quietly, and the chat takes the next prompt.
func TestCmdCancel_DuringABusyRetrysDelayKeepsTheRetryUnwritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		seedChat(t, cs, "c1")
		busyFirstAttempt(t, br, h)

		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
		}
		time.Sleep(pastRetryDelay)
		synctest.Wait()

		assertNoPromptAfterTheCancel(t, br)
		assertOnlyTurnCancelledQuietly(t, h)
		assertNextPromptSent(t, h, br)
	})
}

// With no stop, the busy prompt's retry is written once the delay passes.
func TestPromptTurn_ABusyAnswerIsRetriedAfterTheDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		seedChat(t, cs, "c1")
		busyFirstAttempt(t, br, h)

		time.Sleep(pastRetryDelay)
		synctest.Wait()

		if n := promptFrames(br); n != 2 {
			t.Errorf("session/prompt frames = %d, want the first attempt and its retry", n)
		}
		if closed := closedBroadcasts(t, h); len(closed) != 1 || closed[0].Outcome == marotte.TurnOutcomeCancelled {
			t.Errorf("turn_closed = %+v, want the retried prompt's turn closed by its answer, not cancelled", closed)
		}
	})
}

// emptyRetryHub runs c1's first prompt to an empty end_turn, so the chat re-prompts on a fresh session, and answers
// that session's fake, which arm set up before the retry used it.
func emptyRetryHub(t *testing.T, arm func(*fakeBridge)) (h *Runtime, cs *testChatStore, retry *fakeBridge) {
	t.Helper()
	answerFirst := make(chan struct{})
	var (
		mu     sync.Mutex
		minted []*fakeBridge
	)
	factory := func() engineSession {
		mu.Lock()
		defer mu.Unlock()
		br := newFakeBridge()
		if len(minted) == 0 {
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answerFirst}
		} else {
			arm(br)
		}
		minted = append(minted, br)
		return br
	}
	cs = newTestChatStore()
	h = newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(factory, nil), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	seedChat(t, cs, "c1")
	if code := postPrompt(t, h, "tidy"); code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d", code)
	}
	synctest.Wait()
	mu.Lock()
	if len(minted) != 1 {
		mu.Unlock()
		t.Fatalf("Setup: sessions = %d, want the first prompt's", len(minted))
	}
	first := minted[0]
	mu.Unlock()
	first.deliver(newTurnStartMsg())
	first.deliver(newTurnEndMsg("end_turn"))
	close(answerFirst)
	synctest.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(minted) != 2 {
		t.Fatalf("Setup: sessions = %d, want the empty turn's retry on a fresh one", len(minted))
	}
	return h, cs, minted[1]
}

// assertRetryCancelledQuietly checks the empty turn's retry closed cancelled with no failure_reason and no
// prompt_failed.
func assertRetryCancelledQuietly(t *testing.T, h *Runtime, cs *testChatStore, retry string) {
	t.Helper()
	if c, closed := closeOfTurn(t, logOf(t, cs, "c1"), retry); !closed || c.Outcome != marotte.TurnOutcomeCancelled || c.FailureReason != "" {
		t.Errorf("the retry's turn_close = %+v (closed %v), want cancelled with no failure_reason", c, closed)
	}
	if failed := promptFailures(h); len(failed) != 0 {
		t.Errorf("prompt_failed = %q, want none for the reader's own stop", failed)
	}
}

// A stop after the send gate let the empty turn's retry through, before its frame reached the write lock, keeps the
// retry unwritten and closes it cancelled, quietly.
func TestCmdCancel_BetweenTheEmptyRetrysGateAndItsWriteKeepsItUnwritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		write := make(chan struct{})
		h, cs, br := emptyRetryHub(t, func(br *fakeBridge) {
			br.blockWriteOn = map[string]chan struct{}{marotte.MethodPrompt: write}
		})
		retry, ok := h.coord.PromptTurn("c1")
		lc, _ := h.coord.turns.lookup("c1")
		lc.mu.Lock()
		letThrough := ok && lc.call != nil && lc.call.ID == retry.ID()
		lc.mu.Unlock()
		if !letThrough {
			t.Fatal("Setup: the send gate has not let the retry through")
		}

		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
		}
		br.mu.Lock()
		br.blockWriteOn = nil
		br.mu.Unlock()
		close(write)
		synctest.Wait()

		if n := promptFrames(br); n != 0 {
			t.Errorf("the retry's session/prompt frames = %d, want none after the stop (wire %v)", n, br.wireLog())
		}
		assertRetryCancelledQuietly(t, h, cs, retry.ID())
	})
}

// A stop while the empty turn's retry waits out a busy answer's delay keeps its next attempt unwritten and closes
// it cancelled, quietly.
func TestCmdCancel_DuringTheEmptyRetrysBusyDelayKeepsItUnwritten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := emptyRetryHub(t, func(br *fakeBridge) {
			br.callErrs = map[string]error{marotte.MethodPrompt: busyReply()}
		})
		retry, ok := h.coord.PromptTurn("c1")
		if n := promptFrames(br); !ok || n != 1 {
			t.Fatalf("Setup: the retry's session/prompt frames = %d (turn %v), want its busy first attempt", n, ok)
		}
		br.clearCallErr(marotte.MethodPrompt)

		if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
		}
		time.Sleep(pastRetryDelay)
		synctest.Wait()

		assertNoPromptAfterTheCancel(t, br)
		assertRetryCancelledQuietly(t, h, cs, retry.ID())
	})
}

// The send gate judges every attempt of a turn against the stops since its first attempt was let through: a stop
// after it refuses a later attempt, and with none the later attempt goes.
func TestAwaitPromptSend_ALaterAttemptAnswersTheStopsSinceTheFirst(t *testing.T) {
	cases := []struct {
		want error
		name string
		stop bool
	}{
		{name: "a stop came between the attempts", stop: true, want: command.ErrPromptStopped},
		{name: "no stop came"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, cs, _ := newTestHub()
				defer shutdownHub(t, h)
				seedChat(t, cs, "c1")
				reservePrompt(t, h)
				id := startAdmitted(t, h)
				if err := sendGate(t, h, id); err != nil {
					t.Fatalf("Setup: the first attempt's gate = %v", err)
				}
				if tc.stop {
					stop := h.coord.RequestStop("c1")
					h.coord.ArmStopGrace(stop, stopGrace)
					h.coord.ReleaseStop(stop)
				}
				if err := sendGate(t, h, id); !errors.Is(err, tc.want) {
					t.Errorf("the later attempt's gate = %v, want %v", err, tc.want)
				}
			})
		})
	}
}
