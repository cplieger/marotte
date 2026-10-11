package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

type stepScript struct {
	inspect map[string]json.RawMessage
	holds   map[string]chan struct{}
	entered map[string]chan struct{}
	calls   map[string]int
	state   string
	mu      sync.Mutex
}

func newStepScript(t *testing.T, br *fakeBridge, state string) *stepScript {
	t.Helper()
	s := &stepScript{
		state: state,
		inspect: map[string]json.RawMessage{
			"paused":    oneStepInspect(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused, "sess_step"),
			"running":   oneStepInspect(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning, "sess_step"),
			"completed": oneStepInspect(t, marotte.RunStatusRunning, marotte.RunNodeStatusCompleted, "sess_step"),
		},
		holds:   map[string]chan struct{}{},
		entered: map[string]chan struct{}{},
		calls:   map[string]int{},
	}
	br.mu.Lock()
	br.onCall = s.answer
	br.mu.Unlock()
	return s
}

// hold parks the n-th call (1-based) of method, after it read the state, until release is called.
func (s *stepScript) hold(method string, n int) (entered <-chan struct{}, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := method + "#" + strconv.Itoa(n)
	h, e := make(chan struct{}), make(chan struct{})
	s.holds[key], s.entered[key] = h, e
	return e, func() { close(h) }
}

func (s *stepScript) set(state string) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

func (s *stepScript) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *stepScript) answer(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
	var reply json.RawMessage
	switch method {
	case methodKiroWorkflowInspect:
	case marotte.MethodPrompt:
		reply = json.RawMessage(`{"stopReason":"end_turn"}`)
	case marotte.MethodSessionSteer:
		reply = json.RawMessage(`{"queued":true}`)
	default:
		return nil, nil, false
	}
	s.mu.Lock()
	s.calls[method]++
	key := method + "#" + strconv.Itoa(s.calls[method])
	if method == methodKiroWorkflowInspect {
		reply = s.inspect[s.state]
	}
	hold, entered := s.holds[key], s.entered[key]
	s.mu.Unlock()
	if hold != nil {
		close(entered)
		<-hold
		if method == marotte.MethodPrompt {
			s.set("running")
		}
	}
	return reply, nil, true
}

type stepSend struct {
	err error
	out marotte.RunStepMessageResponse
}

func sendAsync(ctx context.Context, h *Runtime, text, messageID string) <-chan stepSend {
	done := make(chan stepSend, 1)
	go func() {
		out, err := h.runs.messageStep(ctx, "wf_1", reviewPath, text, messageID)
		done <- stepSend{out: out, err: err}
	}()
	return done
}

func refusalCode(t *testing.T, h *Runtime, err error) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.runRoutes.writeStepErr(rec, "message", "wf_1", reviewPath, err)
	return reasonOf(t, rec.Body.Bytes())
}

// While the first message's prompt is resuming a paused step, a second message waits for it and then
// decides again: it steers the now-running step instead of prompting it a second time.
func TestMessageStep_ASecondSendToAPausedStepDecidesAfterTheFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
		t.Cleanup(func() { shutdownHub(t, h) })
		h.runs.RunNodePaused(t.Context(), "wf_1", reviewSegments)
		s := newStepScript(t, br, "paused")
		promptIn, releasePrompt := s.hold(marotte.MethodPrompt, 1)

		first := sendAsync(t.Context(), h, "use main", "m-a")
		<-promptIn
		second := sendAsync(t.Context(), h, "and the fixture", "m-b")
		// Every goroutine is parked: the first in its prompt, the second waiting on the run.
		synctest.Wait()
		if n := s.count(marotte.MethodPrompt); n != 1 {
			t.Errorf("%d session/prompt while the first was in flight, want 1", n)
		}
		releasePrompt()
		r1, r2 := <-first, <-second

		if r1.err != nil || r1.out.Verb != marotte.RunStepMessagePrompt {
			t.Errorf("first MessageStep = (%+v, %v), want verb prompt", r1.out, r1.err)
		}
		if r2.err != nil || r2.out.Verb != marotte.RunStepMessageSteer {
			t.Errorf("second MessageStep = (%+v, %v), want verb steer: the step was running by then", r2.out, r2.err)
		}
		if p, st := s.count(marotte.MethodPrompt), s.count(marotte.MethodSessionSteer); p != 1 || st != 1 {
			t.Errorf("KAS got %d session/prompt and %d _session/steer, want one of each", p, st)
		}
	})
}

// A step that completes while its steer is in flight waits for the send: the end then finds the row in
// KAS and records it "not read", and no send follows the end.
func TestMessageStep_ACompletionWaitsForAnInFlightSteer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
		t.Cleanup(func() { shutdownHub(t, h) })
		s := newStepScript(t, br, "running")
		steerIn, releaseSteer := s.hold(marotte.MethodSessionSteer, 1)

		sent := sendAsync(t.Context(), h, "check the logs", "m-f")
		<-steerIn
		completed := make(chan struct{})
		go func() {
			h.runs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
			close(completed)
		}()
		synctest.Wait()
		select {
		case <-completed:
			t.Error("RunNodeComplete returned while the step's steer was still being sent, want it to wait")
		default:
		}
		releaseSteer()
		r := <-sent
		<-completed

		if r.err != nil || r.out.Verb != marotte.RunStepMessageSteer {
			t.Errorf("MessageStep = (%+v, %v), want verb steer", r.out, r.err)
		}
		got := stepSteersIn(t, h)
		if len(got) != 1 || got[0].Reason != marotte.SteerReasonBoundary {
			t.Errorf("run log steers = %+v, want the sent row recorded not read at the end", got)
		}
	})
}

// A decision read before the step completed is made again once the send holds the run: the message is
// refused as finished and nothing reaches the finished step.
func TestMessageStep_AStepThatCompletesDuringTheReadIsRefusedFinished(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	s := newStepScript(t, br, "running")
	readIn, releaseRead := s.hold(methodKiroWorkflowInspect, 1)

	sent := sendAsync(t.Context(), h, "too late", "m-c")
	<-readIn
	h.runs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
	s.set("completed")
	releaseRead()
	r := <-sent

	if code := refusalCode(t, h, r.err); code != string(marotte.RunStepFinished) {
		t.Errorf("MessageStep refusal = %q (%v), want finished", code, r.err)
	}
	if n := s.count(marotte.MethodSessionSteer); n != 0 {
		t.Errorf("KAS got %d _session/steer, want none after the step completed", n)
	}
}

// A step that pauses after the decision but before the steer is routed takes no row: the send is refused
// and the dock keeps nothing for a step whose execution stopped.
func TestMessageStep_AStepThatPausesBeforeTheRouteKeepsNoRow(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	s := newStepScript(t, br, "running")
	unlockOps, err := h.runs.steers.queue.LockSteerOps(t.Context(), marotte.StepSteerKey("sess_step"))
	if err != nil {
		t.Fatalf("Setup: LockSteerOps = %v", err)
	}

	sent := sendAsync(t.Context(), h, "check the logs", "m-d")
	// Both reads done: the decision is steer, and the send now waits on the step's steer lock.
	waitFor(t, func() bool { return s.count(methodKiroWorkflowInspect) >= 2 })
	h.runs.RunNodePaused(t.Context(), "wf_1", reviewSegments)
	unlockOps()
	r := <-sent

	if code := refusalCode(t, h, r.err); code != string(marotte.RunStepBusy) {
		t.Errorf("MessageStep refusal = %q (%v), want busy", code, r.err)
	}
	if n := s.count(marotte.MethodSessionSteer); n != 0 {
		t.Errorf("KAS got %d _session/steer, want none into a paused step", n)
	}
	if replay := h.bus.steers.list(""); len(replay) != 0 {
		t.Errorf("connect replay = %d frames, want no dock row left for the paused step", len(replay))
	}
}

// stepEndOnRecord completes the step the moment a steer is recorded for it: after the route, before the
// call, the last point a send can be stopped.
type stepEndOnRecord struct {
	command.SteerRecorder
	end  func()
	once sync.Once
}

func (r *stepEndOnRecord) RecordUserSteer(chatID marotte.ChatID, steerID string) {
	r.SteerRecorder.RecordUserSteer(chatID, steerID)
	r.once.Do(r.end)
}

// A step that completes after its steer was routed and before it was sent: the send does not reach KAS,
// and the row is recorded "not read" in the step's turn.
func TestMessageStep_AStepThatCompletesBeforeTheSendIsNotSteered(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	h.runs.steers.ledger = &stepEndOnRecord{SteerRecorder: h.runs.steers.ledger, end: func() {
		h.runs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
	}}

	_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-e")

	if code := refusalCode(t, h, err); code != string(marotte.RunStepBusy) {
		t.Errorf("MessageStep refusal = %q (%v), want busy", code, err)
	}
	if calls := br.callLog(); slices.Contains(calls, marotte.MethodSessionSteer) {
		t.Errorf("calls = %v, want no _session/steer after the step completed", calls)
	}
	got := stepSteersIn(t, h)
	if len(got) != 1 || got[0].Reason != marotte.SteerReasonBoundary {
		t.Errorf("run log steers = %+v, want the routed row recorded not read", got)
	}
}
