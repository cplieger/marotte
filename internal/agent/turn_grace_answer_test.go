package agent

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

const cancelledRequestID = "req-cancelled-execution"

// graceAbandonedPrompt runs a bound prompt turn that the reader stops and whose cancel grace expires unanswered, then
// queues a follow-up under the KAS hold and delivers KAS's idle report: the window where the cancelled execution's
// trailing frames are still to come. It returns the store, the fake, the gate the prompts block on, and the
// session/prompt calls so far.
func graceAbandonedPrompt(t *testing.T) (*testChatStore, *fakeBridge, chan struct{}, func() []string) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: gate}
	prompts := promptCalls(br)
	if rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("Setup: prompt = %d %s", rec.Code, rec.Body.String())
	}
	synctest.Wait()
	br.deliver(br.promptTurnStartMsg())
	br.deliver(newActivityMsg(true))
	synctest.Wait()
	if rec := postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("Setup: cancel = %d, want 200", rec.Code)
	}
	time.Sleep(command.CancelGrace)
	synctest.Wait()
	if closes := closesOf(t, logOf(t, cs, "c1")); len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeCancelled {
		t.Fatalf("Setup: turn_close entries = %+v, want the stopped turn closed cancelled by the grace", closes)
	}
	queueHeldRow(t, h)
	br.deliver(newActivityMsg(false))
	synctest.Wait()
	return cs, br, gate, prompts
}

// newCancelledCompletionMsg is the cancelled execution's turn_completion, delivered after the idle report: the order
// the wait must survive, since a grace-expired execution's frame order is not captured.
func newCancelledCompletionMsg() *marotte.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":                "turn_completion",
		"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.5}},
		"elapsedTime":         float64(9000),
		"requestIds":          []string{cancelledRequestID},
	})
}

// The prompt's answer is the last frame of a cancelled execution, so the follow-up waits for it: sent at the idle
// report, it would own the chat and take a trailing turn_completion's credits and request ids.
func TestGraceClosedPromptHoldsNextSendUntilAbandonedAnswerFolded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setCancelGrace(t, 10*time.Second)
		cs, br, gate, prompts := graceAbandonedPrompt(t)
		if n := len(prompts()); n != 1 {
			t.Fatalf("session/prompt calls = %d after KAS's idle report, want 1: the follow-up went out before the "+
				"cancelled prompt's answer", n)
		}

		br.deliver(newCancelledCompletionMsg())
		synctest.Wait()
		br.answerAbandonedPrompt()
		close(gate)
		synctest.Wait()

		if n := len(prompts()); n != 2 {
			t.Fatalf("session/prompt calls = %d once the cancelled prompt was answered, want 2", n)
		}
		if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-1", "m-q1"}) {
			t.Fatalf("turn_open prompt ids = %v, want [m-1 m-q1]", ids)
		}
		closes := closesOf(t, logOf(t, cs, "c1"))
		if len(closes) != 2 {
			t.Fatalf("turn_close entries = %d, want 2", len(closes))
		}
		if slices.Contains(closes[1].RequestIDs, cancelledRequestID) {
			t.Errorf("the follow-up's turn_close request ids = %v, carrying the cancelled execution's %q",
				closes[1].RequestIDs, cancelledRequestID)
		}
	})
}

// KAS owes no answer to a cancelled prompt, so the follow-up goes out one cancel grace after the idle report rather
// than never.
func TestGraceHoldReleasesAfterCancelGraceWithNoAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setCancelGrace(t, 10*time.Second)
		_, _, gate, prompts := graceAbandonedPrompt(t)
		defer close(gate)

		time.Sleep(command.CancelGrace - time.Second)
		synctest.Wait()
		if n := len(prompts()); n != 1 {
			t.Fatalf("session/prompt calls = %d before the cancel grace from the idle report passed, want 1", n)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if n := len(prompts()); n != 2 {
			t.Errorf("session/prompt calls = %d one cancel grace after the idle report with no answer, want 2", n)
		}
	})
}
