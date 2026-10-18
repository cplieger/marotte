package agent

import (
	"context"
	"errors"
	"net/http"
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
