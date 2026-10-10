package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

func pausedReview() runNow {
	return runNow{workflowID: "wf_1", run: marotte.RunStatusPaused, steps: []workflow.StepSession{
		{Path: []string{"root", "review"}, NodeID: "review", SessionID: "sess_step", Status: string(marotte.RunNodeStatusPaused)},
	}}
}

func reviewAsk(askID string) *runAsk {
	a := askOf("run:wf_1", "wf_1", askID, "review")
	a.payload.StepSessionID = "sess_step"
	return a
}

func admitReviewPrompt(t *testing.T, r *pendingRunAsks) *promptSend {
	t.Helper()
	n := pausedReview()
	now, _ := n.step(reviewPath)
	adm, refuse := r.admitStep(n, &now, "run:wf_1")
	if refuse != "" || adm.verb != marotte.RunStepMessagePrompt || adm.prompt == nil {
		t.Fatalf("Setup: admitStep = (%+v, %q), want a prompt", adm, refuse)
	}
	return adm.prompt
}

// An ask held on a prompt in flight goes with its node: the prompt settling later offers nothing stale.
func TestPendingRunAsks_ANodeCompletingRetiresAnAskHeldOnAPrompt(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	s := admitReviewPrompt(t, r)
	ask := reviewAsk("a1")
	if got := r.offer(ask, drainPoint{gen: testFwdGen}); got != askHeld {
		t.Fatalf("Setup: offer during the prompt = %v, want held", got)
	}

	if got := r.takeNode("wf_1", "review"); len(got) != 1 || got[0] != ask {
		t.Errorf("TakeNode = %+v, want the held ask", got)
	}
	s.sent = stepDelivery{got: kasTook, fence: atFrame(1)}
	if answered, unanswered := r.releaseHold(s); len(answered)+len(unanswered) != 0 {
		t.Errorf("releaseHold after the node completed = (%+v, %+v), want nothing left to settle or offer", answered, unanswered)
	}
}

// The response came back at position 3, so a frame folded at position 2 preceded it and the words
// answer that ask, while one folded at 3 followed it: the step asked again after the words.
func TestPendingRunAsks_ADeliveredPromptAnswersOnlyAsksFoldedBeforeItsResponse(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	s := admitReviewPrompt(t, r)
	before, after := reviewAsk("a1"), reviewAsk("a2")
	if got := r.offer(before, atFrame(2)); got != askHeld {
		t.Fatalf("Setup: offer(the frame before the response) = %v, want held", got)
	}
	if got := r.offer(after, atFrame(3)); got != askHeld {
		t.Fatalf("Setup: offer(the frame after the response) = %v, want held", got)
	}

	s.sent = stepDelivery{got: kasTook, fence: atFrame(3)}
	answered, unanswered := r.releaseHold(s)
	if len(answered) != 1 || answered[0] != before {
		t.Errorf("releaseHold answered = %+v, want only the ask folded before the response", answered)
	}
	if len(unanswered) != 1 || unanswered[0] != after {
		t.Errorf("releaseHold unanswered = %+v, want the ask folded after the response", unanswered)
	}
}

// A replaced carrier restarts its positions at zero, so an ask folded on the new attachment is never
// read as preceding a response the old one delivered further along.
func TestPendingRunAsks_ADeliveredPromptAnswersNoAskFoldedOnAnotherAttachment(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	s := admitReviewPrompt(t, r)
	ask := reviewAsk("a1")
	if got := r.offer(ask, drainPoint{gen: testFwdGen + 1, seq: 1}); got != askHeld {
		t.Fatalf("Setup: offer on the new attachment = %v, want held", got)
	}

	s.sent = stepDelivery{got: kasTook, fence: atFrame(5)}
	answered, unanswered := r.releaseHold(s)
	if len(answered) != 0 || len(unanswered) != 1 || unanswered[0] != ask {
		t.Errorf("releaseHold = (%+v, %+v), want the new attachment's ask still owed an answer", answered, unanswered)
	}
}

// carrierHub is messageHub's paused step on a carrier of its own, whose queued frames the test folds
// by hand: the factory's bridge also serves the utility session, whose read loop drains its queue.
func carrierHub(t *testing.T) (h *Runtime, carrier *fakeBridge, gen uint64) {
	t.Helper()
	h, _ = messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	carrier = newFakeBridge()
	h.bridge.mgr.remove(runChatID("wf_1"))
	if !h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: carrier, state: bridgeIdle}) {
		t.Fatal("Setup: the run's own carrier could not be replaced")
	}
	return h, carrier, h.coord.turns.attachForward(runChatID("wf_1"))
}

func eventsOfType(h *Runtime, typ marotte.EventType) []bufferedEvent {
	var out []bufferedEvent
	for _, e := range bufferedEvents(h) {
		if e.Type == string(typ) {
			out = append(out, e)
		}
	}
	return out
}

// KAS writes the step's ask, then answers the words resuming it. The answer reaches the call first
// and the ask waits in the carrier's queue: the words still answer it, so it is never offered as a
// question nobody will answer.
func TestMessageStep_AnAskArrivingDuringAPausedStepsPromptIsSettledByIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br, gen := carrierHub(t)
		carrier := runChatID("wf_1")
		t.Cleanup(func() {
			// The test is this carrier's folder, so it reports the folder gone before shutdown waits on it.
			h.coord.turns.exitFor(carrier, gen)()
			shutdownHub(t, h)
		})
		br.mu.Lock()
		br.notifsOnCall = map[string][]*marotte.RPCResponse{
			marotte.MethodPrompt: {notifyAsk("wf_1", "review", "which branch?", "n1")},
		}
		br.mu.Unlock()

		sent := sendAsync(t.Context(), h, "the main branch", "m-20")
		synctest.Wait()
		select {
		case r := <-sent:
			t.Fatalf("MessageStep = (%+v, %v) with the ask still queued ahead of KAS's reply, want it waiting for the carrier", r.out, r.err)
		default:
		}
		h.coord.consumeFrame(carrier, br, gen, <-br.notifCh)
		r := <-sent

		if r.err != nil || r.out.Verb != marotte.RunStepMessagePrompt {
			t.Fatalf("MessageStep = (%+v, %v), want verb prompt", r.out, r.err)
		}
		if n := countCalls(br, marotte.MethodPrompt); n != 1 {
			t.Errorf("%d session/prompt, want 1", n)
		}
		if h.runs.asks.hasRun("wf_1") {
			t.Errorf("open asks = %+v, want the ask settled by the words that answered it", h.runs.asks.snapshotRun("wf_1"))
		}
		if got := eventsOfType(h, marotte.EventRunInputNeeded); len(got) != 0 {
			t.Errorf("run_input_needed frames = %+v, want none: the question was answered as it arrived", got)
		}
		settled := eventsOfType(h, marotte.EventRunInputSettled)
		if len(settled) != 1 || marshalPayload(t, settled[0].Payload)["settled_by"] != string(marotte.SettledByUser) {
			t.Errorf("run_input_settled frames = %+v, want one settled by the user", settled)
		}
	})
}

// The same ask, folded while the words were on their way, is offered once KAS refuses the words.
func TestMessageStep_AnAskArrivingDuringAFailedPromptIsOffered(t *testing.T) {
	h, br, gen := carrierHub(t)
	carrier := runChatID("wf_1")
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodPrompt {
			br.deliver(notifyAsk("wf_1", "review", "which branch?", "n1"))
			h.coord.consumeFrame(carrier, br, gen, <-br.notifCh)
		}
		return nil, nil, false
	}
	br.refuseAfterCall = map[string]*marotte.RPCError{marotte.MethodPrompt: {Code: -32602, Message: "step is not resumable"}}
	br.mu.Unlock()

	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "the main branch", "m-21"); err == nil {
		t.Fatal("MessageStep with the words refused = nil, want the refusal")
	}
	if open := h.runs.asks.snapshotRun("wf_1"); len(open) != 1 {
		t.Errorf("open asks = %+v, want the question offered once the words failed", open)
	}
	if got := eventsOfType(h, marotte.EventRunInputNeeded); len(got) != 1 {
		t.Errorf("run_input_needed frames = %+v, want the question published once", got)
	}
}

// KAS writes the step's ask, then the words' reply is lost after the write: the carrier dies, or the
// reply is too large to read. The words stay recorded once, so they answer that ask as a reply would,
// and a retry under the same id sends nothing.
func TestMessageStep_AnAskFoldedBeforeAnUnconfirmedPromptIsSettledByIt(t *testing.T) {
	for name, failure := range map[string]error{
		"carrier_exited":  &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true},
		"reply_too_large": &marotte.TransportError{Err: marotte.ErrFrameTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			h, br, gen := carrierHub(t)
			carrier := runChatID("wf_1")
			br.mu.Lock()
			br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
				if method == marotte.MethodPrompt {
					br.deliver(notifyAsk("wf_1", "review", "which branch?", "n1"))
					h.coord.consumeFrame(carrier, br, gen, <-br.notifCh)
				}
				return nil, nil, false
			}
			br.failAfterCall = map[string]error{marotte.MethodPrompt: failure}
			br.mu.Unlock()

			if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "the main branch", "m-22"); !errors.Is(err, errStepUnconfirmed) {
				t.Fatalf("MessageStep whose reply was lost = %v, want errStepUnconfirmed", err)
			}
			out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "the main branch", "m-22")
			if err != nil || out.Verb != marotte.RunStepMessagePrompt {
				t.Errorf("retried MessageStep = (%+v, %v), want verb prompt from the first send", out, err)
			}

			if n := promptCount(br); n != 1 {
				t.Errorf("KAS got %d session/prompt, want 1: a retry must not deliver the words again", n)
			}
			want := []marotte.EntrySteer{{Text: "the main branch", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
			if got := stepSteersIn(t, h); !reflect.DeepEqual(got, want) {
				t.Errorf("run log steers = %+v, want %+v recorded once", got, want)
			}
			if h.runs.asks.hasRun("wf_1") {
				t.Errorf("open asks = %+v, want the ask settled by the words recorded as sent", h.runs.asks.snapshotRun("wf_1"))
			}
			if got := eventsOfType(h, marotte.EventRunInputNeeded); len(got) != 0 {
				t.Errorf("run_input_needed frames = %+v, want none: the recorded words answered the question", got)
			}
			settled := eventsOfType(h, marotte.EventRunInputSettled)
			if len(settled) != 1 || marshalPayload(t, settled[0].Payload)["settled_by"] != string(marotte.SettledByUser) {
				t.Errorf("run_input_settled frames = %+v, want one settled by the user", settled)
			}
		})
	}
}

// KAS writes the step's ask, then the carrier shuts down before the words' reply is read, with the ask still queued
// for the folder. The failure settles at the ask's position, so the hold waits for that frame and the recorded
// words answer it once; a retry under the same id sends nothing.
func TestMessageStep_AnAskQueuedWhenTheCarrierExitsIsSettledByTheRecordedWords(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br, gen := carrierHub(t)
		carrier := runChatID("wf_1")
		t.Cleanup(func() {
			h.coord.turns.exitFor(carrier, gen)()
			shutdownHub(t, h)
		})
		br.mu.Lock()
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method == marotte.MethodPrompt {
				br.deliver(notifyAsk("wf_1", "review", "which branch?", "n1"))
				br.Stop()
			}
			return nil, nil, false
		}
		br.failAfterCall = map[string]error{
			marotte.MethodPrompt: &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true},
		}
		br.mu.Unlock()

		sent := sendAsync(t.Context(), h, "the main branch", "m-23")
		synctest.Wait()
		select {
		case r := <-sent:
			t.Fatalf("MessageStep = (%+v, %v) with the ask still queued at shutdown, want it waiting for the carrier", r.out, r.err)
		default:
		}
		h.coord.consumeFrame(carrier, br, gen, <-br.notifCh)
		if r := <-sent; !errors.Is(r.err, errStepUnconfirmed) {
			t.Fatalf("MessageStep whose carrier exited = %v, want errStepUnconfirmed", r.err)
		}
		out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "the main branch", "m-23")

		if err != nil || out.Verb != marotte.RunStepMessagePrompt {
			t.Errorf("retried MessageStep = (%+v, %v), want verb prompt from the first send", out, err)
		}
		if n := promptCount(br); n != 1 {
			t.Errorf("KAS got %d session/prompt, want 1: a retry must not deliver the words again", n)
		}
		want := []marotte.EntrySteer{{Text: "the main branch", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
		if got := stepSteersIn(t, h); !reflect.DeepEqual(got, want) {
			t.Errorf("run log steers = %+v, want %+v recorded once", got, want)
		}
		if h.runs.asks.hasRun("wf_1") {
			t.Errorf("open asks = %+v, want the queued ask settled by the recorded words", h.runs.asks.snapshotRun("wf_1"))
		}
		if got := eventsOfType(h, marotte.EventRunInputNeeded); len(got) != 0 {
			t.Errorf("run_input_needed frames = %+v, want none: the recorded words answered the question", got)
		}
		settled := eventsOfType(h, marotte.EventRunInputSettled)
		if len(settled) != 1 || marshalPayload(t, settled[0].Payload)["settled_by"] != string(marotte.SettledByUser) {
			t.Errorf("run_input_settled frames = %+v, want one settled by the user", settled)
		}
	})
}
