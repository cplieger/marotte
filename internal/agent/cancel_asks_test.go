package agent

// Stop and the tab teardowns answer each open ask with its kind's cancel answer before session/cancel (ACP: a client
// cancelling a turn MUST answer its pending session/request_permission requests; JSON-RPC 2.0 owes every request a
// response), and a run's end answers its own, through the real ask tracker.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// orderedSession records every answer and notification in the one order the shared pipe would carry them.
// failRespond fails that many writes of an ask's answer with a live session; endRespond fails them as an ended one.
type orderedSession struct {
	*fakeBridge
	failRespond map[int64]int
	endRespond  map[int64]bool
	frames      []string
	mu          sync.Mutex
}

func newOrderedSession() *orderedSession {
	return &orderedSession{fakeBridge: newFakeBridge(), failRespond: map[int64]int{}, endRespond: map[int64]bool{}}
}

func (s *orderedSession) Respond(ctx context.Context, id int64, result any, err error) error {
	s.mu.Lock()
	if s.endRespond[id] {
		s.mu.Unlock()
		return fmt.Errorf("%w: the engine ended", marotte.ErrBridgeExited)
	}
	if s.failRespond[id] > 0 {
		s.failRespond[id]--
		s.frames = append(s.frames, fmt.Sprintf("failed:%d", id))
		s.mu.Unlock()
		return errors.New("write refused")
	}
	s.frames = append(s.frames, fmt.Sprintf("respond:%d", id))
	s.mu.Unlock()
	return s.fakeBridge.Respond(ctx, id, result, err)
}

func (s *orderedSession) Notify(ctx context.Context, method string, params any) error {
	s.mu.Lock()
	s.frames = append(s.frames, "notify:"+method)
	s.mu.Unlock()
	return s.fakeBridge.Notify(ctx, method, params)
}

func (s *orderedSession) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.frames)
}

// cancelFixture is a runtime whose chat c1 holds sess as its resident session.
func cancelFixture(t *testing.T) (*Runtime, *orderedSession) {
	t.Helper()
	sess := newOrderedSession()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(func() engineSession { return sess }, nil), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: OpenBridge: %v", err)
	}
	return h, sess
}

// addAsk files one ask of the event's kind on chat c1 as KAS's request acpID, a step's when runID is set.
func addAsk(t *testing.T, h *Runtime, origin acpResponder, event marotte.EventType, acpID int64, runID string) int64 {
	t.Helper()
	toolCallID := fmt.Sprintf("tool-%d", acpID)
	var payload any
	switch event {
	case marotte.EventElicitationNeeded:
		payload = marotte.ElicitationNeededPayload{ToolCallID: toolCallID, RunID: runID}
	case marotte.EventUserInputNeeded:
		payload = marotte.UserInputNeededPayload{ToolCallID: toolCallID, RunID: runID}
	default:
		payload = marotte.PermissionNeededPayload{ToolCallID: toolCallID, RunID: runID}
	}
	return requestIDOf(t, h.bus.pendingPerms.add(acpID, marotte.NewEvent(event, "c1", payload), origin))
}

func addPermission(t *testing.T, h *Runtime, origin acpResponder, acpID int64, runID string) int64 {
	t.Helper()
	return addAsk(t, h, origin, marotte.EventPermissionNeeded, acpID, runID)
}

func pendingAskIDs(t *testing.T, h *Runtime) []int64 {
	t.Helper()
	var ids []int64
	for _, evt := range h.bus.pendingPerms.list("c1") {
		ids = append(ids, requestIDOf(t, evt))
	}
	return ids
}

func postStop(t *testing.T, h *Runtime) int {
	t.Helper()
	return postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}).Code
}

func decisionKindOf(event marotte.EventType) marotte.DecisionKind {
	kind, _ := marotte.DecisionKindForEvent(event)
	return kind
}

func TestCancel_AnswersEveryOpenAskWithItsKindsCancelBeforeSessionCancel(t *testing.T) {
	h, sess := cancelFixture(t)
	addAsk(t, h, sess, marotte.EventPermissionNeeded, 7, "")
	addAsk(t, h, sess, marotte.EventElicitationNeeded, 8, "")
	addAsk(t, h, sess, marotte.EventUserInputNeeded, 9, "")

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200", code)
	}
	want := []string{"respond:7", "respond:8", "respond:9", "notify:" + marotte.MethodCancel}
	if got := sess.written(); !slices.Equal(got, want) {
		t.Errorf("frames written = %v, want %v: KAS must read every ask's answer before the cancel", got, want)
	}
	for acpID, wantResult := range map[int64]any{
		7: marotte.PermissionOutcomeCancelled(),
		8: marotte.ElicitationResult{Action: marotte.ElicitationActionCancel},
		9: marotte.UserInputResult{Action: marotte.UserInputActionDismissed},
	} {
		got, ok := sess.responseTo(acpID)
		if !ok || got.err != nil || !reflect.DeepEqual(got.result, wantResult) {
			t.Errorf("answer to request %d = %+v (sent %v), want the result %+v", acpID, got, ok, wantResult)
		}
	}
	if got := pendingAskIDs(t, h); len(got) != 0 {
		t.Errorf("asks still pending = %v, want none", got)
	}
}

// A cancelled ask was answered for nobody's decision, so no surface may read it as someone else's answer.
func TestCancel_SettlesEachCancelledAskMoot(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, sess := cancelFixture(t)
			askID := addAsk(t, h, sess, tc.event, 7, "")
			head := h.bus.fanout.Position().Head

			if code := postStop(t, h); code != http.StatusOK {
				t.Fatalf("Stop = %d, want 200", code)
			}
			want := marotte.DecisionSettledPayload{RequestID: askID, Kind: decisionKindOf(tc.event), SettledBy: marotte.SettledByMoot}
			if got := settledEvents(t, bufferedSince(h, head)); len(got) != 1 || got[0] != want {
				t.Errorf("decision_settled = %+v, want exactly [%+v]", got, want)
			}
		})
	}
}

func TestCancelPendingAsks_ChatScopeLeavesStepAsksPending(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(askFamilies())) {
		tc := askFamilies()[name]
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			origin := newOrderedSession()
			addAsk(t, h, origin, tc.event, 1, "")
			step := addAsk(t, h, origin, tc.event, 2, "wf_1")

			if err := h.bus.CancelPendingAsks(t.Context(), "c1", command.ScopeChatSession); err != nil {
				t.Fatalf("CancelPendingAsks: %v", err)
			}
			if got := origin.written(); !slices.Equal(got, []string{"respond:1"}) {
				t.Errorf("answers written = %v, want only the chat's own ask [respond:1]", got)
			}
			if got := pendingAskIDs(t, h); !slices.Equal(got, []int64{step}) {
				t.Errorf("asks still pending = %v, want the step's [%d]", got, step)
			}
		})
	}
}

func TestCancelPendingAsks_ChatAndRunsScopeTakesBoth(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			origin := newOrderedSession()
			addAsk(t, h, origin, tc.event, 1, "")
			addAsk(t, h, origin, tc.event, 2, "wf_1")

			if err := h.bus.CancelPendingAsks(t.Context(), "c1", command.ScopeChatAndRuns); err != nil {
				t.Fatalf("CancelPendingAsks: %v", err)
			}
			if got := origin.written(); !slices.Equal(got, []string{"respond:1", "respond:2"}) {
				t.Errorf("answers written = %v, want both asks [respond:1 respond:2]", got)
			}
			if got := pendingAskIDs(t, h); len(got) != 0 {
				t.Errorf("asks still pending = %v, want none", got)
			}
		})
	}
}

func TestCancel_LeavesAChatParentedStepsAskPending(t *testing.T) {
	h, sess := cancelFixture(t)
	addPermission(t, h, sess, 7, "")
	step := addAsk(t, h, sess, marotte.EventElicitationNeeded, 8, "wf_1")

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200", code)
	}
	want := []string{"respond:7", "notify:" + marotte.MethodCancel}
	if got := sess.written(); !slices.Equal(got, want) {
		t.Errorf("frames written = %v, want %v: a Stop does not stop the chat's run, so its step's card stays", got, want)
	}
	if got := pendingAskIDs(t, h); !slices.Equal(got, []int64{step}) {
		t.Errorf("asks still pending = %v, want the step's [%d]", got, step)
	}
}

// A card answer whose take came first stands: the Stop writes no second answer for that id, and the card's own
// answer is delivered rather than withdrawn.
func TestCancel_ACardAnswerClaimedFirstWins(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, sess := cancelFixture(t)
			id := addAsk(t, h, sess, tc.event, 7, "")
			reply, ok := h.bus.TakePendingPerm("c1", id, decisionKindOf(tc.event), marotte.SettledByUser)
			if !ok {
				t.Fatal("Setup: the card's take lost on a fresh ask")
			}

			if code := postStop(t, h); code != http.StatusOK {
				t.Fatalf("Stop = %d, want 200", code)
			}
			if got := sess.written(); !slices.Equal(got, []string{"notify:" + marotte.MethodCancel}) {
				t.Errorf("frames written = %v, want only session/cancel: the card's take decided the ask", got)
			}
			outcome, err := reply.Respond(t.Context(), map[string]any{"answer": "card"})
			if outcome != command.AnswerDelivered || err != nil {
				t.Errorf("the card's answer after Stop = (%v, %v), want (AnswerDelivered, nil)", outcome, err)
			}
			if got := sess.written(); !slices.Equal(got, []string{"notify:" + marotte.MethodCancel, "respond:7"}) {
				t.Errorf("frames written = %v, want the card's own answer once", got)
			}
		})
	}
}

func TestCancel_CardAnswerAfterTheCancelsTakeIsWithdrawn(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, sess := cancelFixture(t)
			id := addAsk(t, h, sess, tc.event, 7, "")

			if code := postStop(t, h); code != http.StatusOK {
				t.Fatalf("Stop = %d, want 200", code)
			}
			if _, ok := h.bus.TakePendingPerm("c1", id, decisionKindOf(tc.event), marotte.SettledByUser); ok {
				t.Error("a card answer after Stop took the ask; the cancel's take decided it, so the card must be withdrawn")
			}
			want := []string{"respond:7", "notify:" + marotte.MethodCancel}
			if got := sess.written(); !slices.Equal(got, want) {
				t.Errorf("frames written = %v, want %v", got, want)
			}
		})
	}
}

func TestCancel_AReopenedAnswerIsRetriedThenStopsBeforeSessionCancel(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, sess := cancelFixture(t)
			first := addAsk(t, h, sess, tc.event, 7, "")
			second := addPermission(t, h, sess, 8, "")
			sess.mu.Lock()
			sess.failRespond[7] = cancelAttempts
			sess.mu.Unlock()

			if code := postStop(t, h); code != http.StatusBadGateway {
				t.Fatalf("Stop = %d, want 502: the reply goes on and the user must press Stop again", code)
			}
			if got := sess.written(); !slices.Equal(got, []string{"failed:7", "failed:7", "failed:7"}) {
				t.Errorf("frames written = %v, want three tries of the first answer and nothing after it", got)
			}
			if got := pendingAskIDs(t, h); !slices.Equal(got, []int64{first, second}) {
				t.Errorf("asks pending = %v, want both cards back [%d %d]", got, first, second)
			}
		})
	}
}

func TestCancel_EngineEndDuringTheAnswersStopsWithNoSessionCancel(t *testing.T) {
	h, sess := cancelFixture(t)
	addPermission(t, h, sess, 7, "")
	addAsk(t, h, sess, marotte.EventUserInputNeeded, 8, "")
	sess.mu.Lock()
	sess.endRespond[8] = true
	sess.mu.Unlock()

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200: the engine end already stopped the reply", code)
	}
	if got := sess.written(); !slices.Equal(got, []string{"respond:7"}) {
		t.Errorf("frames written = %v, want the first answer and nothing after the engine ended", got)
	}
}

// A finished run's open asks get their kind's cancel answer on the session they arrived on, with no
// decision_settled; a claimed one keeps its own answer, and the chat's own ask is not the run's to answer.
func TestClearPendingPermsForRun_AnswersEachOpenAskWithItsKindsCancel(t *testing.T) {
	h, _, _ := newTestHub()
	origin := newOrderedSession()
	addAsk(t, h, origin, marotte.EventPermissionNeeded, 1, "wf_1")
	addAsk(t, h, origin, marotte.EventElicitationNeeded, 2, "wf_1")
	addAsk(t, h, origin, marotte.EventUserInputNeeded, 3, "wf_1")
	claimed := addAsk(t, h, origin, marotte.EventUserInputNeeded, 4, "wf_1")
	own := addPermission(t, h, origin, 5, "")
	if _, ok := h.bus.TakePendingPerm("c1", claimed, marotte.DecisionKindUserInput, marotte.SettledByUser); !ok {
		t.Fatal("Setup: the card's take lost on a fresh ask")
	}
	head := h.bus.fanout.Position().Head

	h.bus.ClearPendingPermsForRun(t.Context(), "wf_1")

	if got := origin.written(); !slices.Equal(got, []string{"respond:1", "respond:2", "respond:3"}) {
		t.Errorf("answers written = %v, want the three open run asks in ask order [respond:1 respond:2 respond:3]", got)
	}
	for acpID, wantResult := range map[int64]any{
		1: marotte.PermissionOutcomeCancelled(),
		2: marotte.ElicitationResult{Action: marotte.ElicitationActionCancel},
		3: marotte.UserInputResult{Action: marotte.UserInputActionDismissed},
	} {
		got, ok := origin.responseTo(acpID)
		if !ok || got.err != nil || !reflect.DeepEqual(got.result, wantResult) {
			t.Errorf("answer to request %d = %+v (sent %v), want the result %+v", acpID, got, ok, wantResult)
		}
	}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 0 {
		t.Errorf("decision_settled = %+v, want none: a run's end retires its cards silently", got)
	}
	if got := pendingAskIDs(t, h); !slices.Equal(got, []int64{own}) {
		t.Errorf("asks still pending = %v, want only the chat's own [%d]", got, own)
	}
}

// An ask KAS sent before it read a Stop's session/cancel arrives after the Stop's answers: it gets its kind's cancel at
// once and no card, or a reconnect would re-offer it with nothing left to answer it.
func TestPendingPermsAdd_AnAskAfterAStopIsAnsweredWithItsKindsCancel(t *testing.T) {
	for name, tc := range askFamilies() {
		t.Run(name, func(t *testing.T) {
			h, sess := cancelFixture(t)
			turn, _ := h.stagePromptTurn(t, "c1")
			t.Cleanup(func() { h.ReleaseTurn("c1", turn) })
			if code := postStop(t, h); code != http.StatusOK {
				t.Fatalf("Stop = %d, want 200", code)
			}
			head := h.bus.fanout.Position().Head

			evt, filed := h.bus.PendingPermsAdd(t.Context(), 9, marotte.NewEvent(tc.event, "c1", tc.payload), sess)

			if filed {
				t.Error("PendingPermsAdd filed the ask, want it answered at once with no card")
			}
			kind := decisionKindOf(tc.event)
			if got, ok := sess.responseTo(9); !ok || got.err != nil || !reflect.DeepEqual(got.result, cancelAnswer(kind)) {
				t.Errorf("answer to request 9 = %+v (sent %v), want %+v", got, ok, cancelAnswer(kind))
			}
			if got := pendingAskIDs(t, h); len(got) != 0 {
				t.Errorf("asks still pending = %v, want none", got)
			}
			want := marotte.DecisionSettledPayload{RequestID: marotte.DecisionRequestID(evt.Payload), Kind: kind, SettledBy: marotte.SettledByMoot}
			if got := settledEvents(t, bufferedSince(h, head)); len(got) != 1 || got[0] != want {
				t.Errorf("decision_settled = %+v, want exactly [%+v]", got, want)
			}
		})
	}
}

// Only the stopped turn's own asks are answered: a chat-parented run's step goes on past a Stop, and a stop of an
// earlier turn says nothing about the one now open.
func TestPendingPermsAdd_FilesAnAskNoStopIsEnding(t *testing.T) {
	cases := []struct {
		name  string
		runID string
		stop  func(t *testing.T, h *Runtime, turn string) string
	}{
		{name: "no stop", stop: func(*testing.T, *Runtime, string) string { return "" }},
		{name: "a step's ask during a stopped turn", runID: "wf_1", stop: func(t *testing.T, h *Runtime, _ string) string {
			if code := postStop(t, h); code != http.StatusOK {
				t.Fatalf("Stop = %d, want 200", code)
			}
			return ""
		}},
		{name: "a stop of an earlier turn", stop: func(t *testing.T, h *Runtime, turn string) string {
			if code := postStop(t, h); code != http.StatusOK {
				t.Fatalf("Stop = %d, want 200", code)
			}
			endTurn(t, h, "c1", turn)
			next, _ := h.stagePromptTurn(t, "c1")
			return next
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, sess := cancelFixture(t)
			turn, _ := h.stagePromptTurn(t, "c1")
			t.Cleanup(func() { h.ReleaseTurn("c1", turn) })
			if next := tc.stop(t, h, turn); next != "" {
				t.Cleanup(func() { h.ReleaseTurn("c1", next) })
			}

			_, filed := h.bus.PendingPermsAdd(t.Context(), 9, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
				marotte.UserInputNeededPayload{RunID: tc.runID}), sess)

			if !filed {
				t.Error("PendingPermsAdd answered the ask, want it filed for its card")
			}
			if _, ok := sess.responseTo(9); ok {
				t.Error("request 9 was answered, want it left for the person")
			}
			if got := pendingAskIDs(t, h); len(got) != 1 {
				t.Errorf("asks pending = %v, want the one just filed", got)
			}
		})
	}
}
