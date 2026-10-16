package agent

// Stop and the tab teardowns answer each open permission ask `cancelled` before session/cancel (ACP: a client
// cancelling a turn MUST answer its pending session/request_permission requests), through the real ask tracker.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: OpenBridge: %v", err)
	}
	return h, sess
}

// addPermission files one permission ask on chat c1 as KAS's request acpID, a step's when runID is set.
func addPermission(h *Runtime, origin acpResponder, acpID int64, runID string) int64 {
	evt := h.bus.PendingPermsAdd(acpID, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{ToolCallID: fmt.Sprintf("tool-%d", acpID), RunID: runID}), origin)
	return evt.Payload.(marotte.PermissionNeededPayload).RequestID
}

func pendingAskIDs(h *Runtime) []int64 {
	var ids []int64
	for _, evt := range h.bus.pendingPerms.list("c1") {
		if p, ok := evt.Payload.(marotte.PermissionNeededPayload); ok {
			ids = append(ids, p.RequestID)
		}
	}
	return ids
}

func postStop(t *testing.T, h *Runtime) int {
	t.Helper()
	return postCmd(t, h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1"}).Code
}

func TestCancelPendingPermissions_ChatScopeLeavesStepAsksPending(t *testing.T) {
	h, _, _ := newTestHub()
	origin := newOrderedSession()
	addPermission(h, origin, 1, "")
	step := addPermission(h, origin, 2, "wf_1")

	if err := h.bus.CancelPendingPermissions(t.Context(), "c1", command.ScopeChatSession); err != nil {
		t.Fatalf("CancelPendingPermissions: %v", err)
	}
	if got := origin.written(); !slices.Equal(got, []string{"respond:1"}) {
		t.Errorf("answers written = %v, want only the chat's own ask [respond:1]", got)
	}
	if got := pendingAskIDs(h); !slices.Equal(got, []int64{step}) {
		t.Errorf("asks still pending = %v, want the step's [%d]", got, step)
	}
}

func TestCancelPendingPermissions_ChatAndRunsScopeTakesBoth(t *testing.T) {
	h, _, _ := newTestHub()
	origin := newOrderedSession()
	addPermission(h, origin, 1, "")
	addPermission(h, origin, 2, "wf_1")

	if err := h.bus.CancelPendingPermissions(t.Context(), "c1", command.ScopeChatAndRuns); err != nil {
		t.Fatalf("CancelPendingPermissions: %v", err)
	}
	if got := origin.written(); !slices.Equal(got, []string{"respond:1", "respond:2"}) {
		t.Errorf("answers written = %v, want both asks [respond:1 respond:2]", got)
	}
	if got := pendingAskIDs(h); len(got) != 0 {
		t.Errorf("asks still pending = %v, want none", got)
	}
}

func TestCancel_AnswersOpenPermissionsCancelledBeforeSessionCancel(t *testing.T) {
	h, sess := cancelFixture(t)
	addPermission(h, sess, 7, "")
	addPermission(h, sess, 8, "")

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200", code)
	}
	want := []string{"respond:7", "respond:8", "notify:" + marotte.MethodCancel}
	if got := sess.written(); !slices.Equal(got, want) {
		t.Errorf("frames written = %v, want %v: KAS must read the cancelled answers before the cancel", got, want)
	}
}

func TestCancel_LeavesAChatParentedStepsPermissionPending(t *testing.T) {
	h, sess := cancelFixture(t)
	addPermission(h, sess, 7, "")
	step := addPermission(h, sess, 8, "wf_1")

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200", code)
	}
	want := []string{"respond:7", "notify:" + marotte.MethodCancel}
	if got := sess.written(); !slices.Equal(got, want) {
		t.Errorf("frames written = %v, want %v: a Stop does not stop the chat's run, so its step's card stays", got, want)
	}
	if got := pendingAskIDs(h); !slices.Equal(got, []int64{step}) {
		t.Errorf("asks still pending = %v, want the step's [%d]", got, step)
	}
}

func TestCancel_ACardAnswerClaimedFirstWins(t *testing.T) {
	h, sess := cancelFixture(t)
	id := addPermission(h, sess, 7, "")
	reply, ok := h.bus.TakePendingPerm("c1", id, marotte.DecisionKindPermission, marotte.SettledByUser)
	if !ok {
		t.Fatal("Setup: the card's take lost on a fresh ask")
	}

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200", code)
	}
	if got := sess.written(); !slices.Equal(got, []string{"notify:" + marotte.MethodCancel}) {
		t.Errorf("frames written = %v, want only session/cancel: the card's take decided the ask", got)
	}
	if _, err := reply.Respond(t.Context(), map[string]any{"outcome": "selected"}); err != nil {
		t.Fatalf("the card's answer after Stop: %v", err)
	}
	if got := sess.written(); !slices.Equal(got, []string{"notify:" + marotte.MethodCancel, "respond:7"}) {
		t.Errorf("frames written = %v, want the card's own answer once", got)
	}
}

func TestCancel_CardAnswerAfterTheCancelsTakeIsWithdrawn(t *testing.T) {
	h, sess := cancelFixture(t)
	id := addPermission(h, sess, 7, "")

	if code := postStop(t, h); code != http.StatusOK {
		t.Fatalf("Stop = %d, want 200", code)
	}
	if _, ok := h.bus.TakePendingPerm("c1", id, marotte.DecisionKindPermission, marotte.SettledByUser); ok {
		t.Error("a card answer after Stop took the ask; the cancel's take decided it, so the card must be withdrawn")
	}
	want := []string{"respond:7", "notify:" + marotte.MethodCancel}
	if got := sess.written(); !slices.Equal(got, want) {
		t.Errorf("frames written = %v, want %v", got, want)
	}
}

func TestCancel_AReopenedAnswerIsRetriedThenStopsBeforeSessionCancel(t *testing.T) {
	h, sess := cancelFixture(t)
	first := addPermission(h, sess, 7, "")
	second := addPermission(h, sess, 8, "")
	sess.mu.Lock()
	sess.failRespond[7] = 3
	sess.mu.Unlock()

	if code := postStop(t, h); code != http.StatusBadGateway {
		t.Fatalf("Stop = %d, want 502: the reply goes on and the user must press Stop again", code)
	}
	if got := sess.written(); !slices.Equal(got, []string{"failed:7", "failed:7", "failed:7"}) {
		t.Errorf("frames written = %v, want three tries of the first answer and nothing after it", got)
	}
	if got := pendingAskIDs(h); !slices.Equal(got, []int64{first, second}) {
		t.Errorf("asks pending = %v, want both cards back [%d %d]", got, first, second)
	}
}

func TestCancel_EngineEndDuringTheAnswersStopsWithNoSessionCancel(t *testing.T) {
	h, sess := cancelFixture(t)
	addPermission(h, sess, 7, "")
	addPermission(h, sess, 8, "")
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
