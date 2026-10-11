package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
)

func promptCount(br *fakeBridge) int {
	n := 0
	for _, m := range br.callLog() {
		if m == marotte.MethodPrompt {
			n++
		}
	}
	return n
}

// The reader goes away after KAS received the prompt and before it replied: the words KAS took stay in
// the step's record, whether they join its open turn or head the turn KAS reopens.
func TestMessageStep_AReaderLeavingAfterTheWriteKeepsTheWordsKASTook(t *testing.T) {
	for _, turn := range []string{"open_turn", "closed_turn"} {
		t.Run(turn, func(t *testing.T) {
			h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
			if turn == "closed_turn" {
				h.coord.runs = h.runs.log
				h.coord.closeHostedRuns(t.Context(), runChatID("wf_1"), marotte.StopReasonInterrupted)
			}
			reply := make(chan struct{})
			br.mu.Lock()
			br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: reply}
			br.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := h.runs.messageStep(ctx, "wf_1", reviewPath, "carry on", "m-20")
				done <- err
			}()
			waitForCall(t, br, marotte.MethodPrompt)
			cancel()
			close(reply)
			if err := <-done; err != nil {
				t.Fatalf("MessageStep cancelled after the write = %v, want nil: KAS took the words", err)
			}

			if turn == "closed_turn" {
				h.runs.RunNodeStart(t.Context(), reviewStep("sess_step"), "")
				if _, prompts := runTurnsAt(t, h, reviewPath); len(prompts) != 2 || prompts[1] == nil || prompts[1].Text != "carry on" {
					t.Errorf("root/review prompts = %+v, want the reopened turn headed by the words", prompts)
				}
				return
			}
			want := []marotte.EntrySteer{{Text: "carry on", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
			if got := stepSteersIn(t, h); !reflect.DeepEqual(got, want) {
				t.Errorf("run log steers = %+v, want %+v in the step's open turn", got, want)
			}
		})
	}
}

// A carrier that dies between the write and the reply leaves the words recorded once, and a retry under
// the same message id answers the first verb without a second session/prompt.
func TestMessageStep_ACarrierDyingBeforeItsReplyRecordsTheWordsOnceAndARetrySendsNothing(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	br.setCallErr(marotte.MethodPrompt, &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true})

	_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-21")
	if err == nil {
		t.Fatal("MessageStep whose carrier died before replying = nil, want an error the composer keeps the id for")
	}
	out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-21")
	if err != nil || out.Verb != marotte.RunStepMessagePrompt {
		t.Fatalf("retried MessageStep = (%+v, %v), want verb prompt from the first send", out, err)
	}

	if n := promptCount(br); n != 1 {
		t.Errorf("KAS got %d session/prompt, want 1: a retry must not deliver the words again", n)
	}
	want := []marotte.EntrySteer{{Text: "carry on", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
	if got := stepSteersIn(t, h); !reflect.DeepEqual(got, want) {
		t.Errorf("run log steers = %+v, want %+v recorded once", got, want)
	}
}

// A Delete arriving while an unconfirmed send holds the run waits for it, then clears its retry record:
// a retry after the deletion is not answered as delivered.
func TestDelete_AfterAnUnconfirmedSendClearsItsRetryRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
		t.Cleanup(func() { shutdownHub(t, h) })
		s := newStepScript(t, br, "paused")
		br.setCallErr(marotte.MethodPrompt, &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true})
		// The second inspect is the one read under the run's gate.
		gatedRead, releaseRead := s.hold(methodKiroWorkflowInspect, 2)

		sent := sendAsync(t.Context(), h, "carry on", "m-30")
		<-gatedRead
		deleted := make(chan struct{})
		go func() {
			if err := h.runs.delete(t.Context(), "wf_1"); err != nil {
				t.Errorf("Delete = %v, want nil", err)
			}
			close(deleted)
		}()
		synctest.Wait()
		select {
		case <-deleted:
			t.Error("Delete returned while a send held the run, want it to wait for the send")
		default:
		}
		releaseRead()
		if r := <-sent; !errors.Is(r.err, errStepUnconfirmed) {
			t.Fatalf("MessageStep whose carrier died before replying = %v, want errStepUnconfirmed", r.err)
		}
		<-deleted
		s.set("deleted")

		out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-30")
		if err == nil {
			t.Errorf("retried MessageStep after Delete = (%+v, nil), want an error: the run is gone", out)
		}
		if n := promptCount(br); n != 1 {
			t.Errorf("KAS got %d session/prompt, want 1", n)
		}
	})
}

// Delete winning over a retry: the retry record of a send made before it goes with the run, so the
// retry is not answered as delivered.
func TestMessageStep_ARetryAfterDeleteIsNotAnsweredFromTheDeletedRun(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	s := newStepScript(t, br, "paused")
	br.setCallErr(marotte.MethodPrompt, &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true})
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-31"); !errors.Is(err, errStepUnconfirmed) {
		t.Fatalf("Setup: MessageStep whose carrier died before replying = %v, want errStepUnconfirmed", err)
	}

	if err := h.runs.delete(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Setup: Delete = %v, want nil", err)
	}
	s.set("deleted")

	out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-31")
	if err == nil {
		t.Errorf("retried MessageStep after Delete = (%+v, nil), want an error: the run is gone", out)
	}
	if n := promptCount(br); n != 1 {
		t.Errorf("KAS got %d session/prompt, want 1", n)
	}
}

// The answer route's twin: the words are recorded and the question settled, so a second answer finds
// nothing to answer instead of reaching KAS again.
func TestAnswerInput_ACarrierDyingBeforeItsReplySettlesTheAsk(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: marotte.RunInputNeededPayload{
		WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
	}})
	br.setCallErr(marotte.MethodPrompt, &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true})

	if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "teal"); err == nil {
		t.Fatal("AnswerInput whose carrier died before replying = nil, want the unconfirmed error")
	}
	if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "teal"); !errors.Is(err, errAskAlreadySettled) {
		t.Errorf("second AnswerInput = %v, want errAskAlreadySettled", err)
	}

	if n := promptCount(br); n != 1 {
		t.Errorf("KAS got %d session/prompt, want 1", n)
	}
	if got := stepSteersIn(t, h); len(got) != 1 || got[0].Text != "teal" {
		t.Errorf("run log steers = %+v, want the answer recorded once", got)
	}
	if h.runs.asks.hasRun("wf_1") {
		t.Error("wf_1 still has an open ask, want it settled by the recorded answer")
	}
}

func callIndex(br *fakeBridge, method string) int {
	return slices.Index(br.callLog(), method)
}

// A retry of an unconfirmed send while a Delete is with KAS is refused rather than answered from the
// record the deletion is taking.
func TestMessageStep_ARetryDuringADeleteIsNotAnsweredFromTheRecord(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	br.setCallErr(marotte.MethodPrompt, &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true})
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-42"); !errors.Is(err, errStepUnconfirmed) {
		t.Fatalf("Setup: MessageStep whose carrier died before replying = %v, want errStepUnconfirmed", err)
	}
	held := make(chan struct{})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowDelete: held}
	br.mu.Unlock()
	deleted := make(chan error, 1)
	go func() { deleted <- h.runs.delete(t.Context(), "wf_1") }()
	waitForCall(t, br, methodKiroWorkflowDelete)

	out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-42")
	close(held)
	if dErr := <-deleted; dErr != nil {
		t.Fatalf("Delete = %v, want nil", dErr)
	}

	if code := refusalCode(t, h, err); code != string(marotte.RunStepBusy) {
		t.Errorf("retried MessageStep during Delete = (%+v, %v), want reason %q", out, err, marotte.RunStepBusy)
	}
}
