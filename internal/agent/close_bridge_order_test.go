package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// awaitOpenTurns waits until c1 has want open turns.
func awaitOpenTurns(t *testing.T, h *Runtime, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); len(h.coord.turns.openTurnIDs("c1")) != want; {
		if time.Now().After(deadline) {
			t.Fatalf("open turns = %d, want %d", len(h.coord.turns.openTurnIDs("c1")), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// Frames the session queued before its release are folded before the teardown closes the chat's turns: a chunk
// lands in the open turn, which then closes once, and a turn KAS started at the release is closed too.
func TestCloseBridge_ReleasesThenWaitsForTheForwardThenClosesTheTurns(t *testing.T) {
	t.Run("a chunk queued at the release folds into the open turn", func(t *testing.T) {
		h, cs, br, tab := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open session: %v", err)
		}
		br.deliver(newTurnStartMsg())
		awaitOpenTurns(t, h, 1)
		openAtRelease := -1
		br.mu.Lock()
		br.beforeRelease = func() {
			openAtRelease = len(h.coord.turns.openTurnIDs("c1"))
			br.deliver(newChunkMsg("words queued at the release"))
		}
		br.mu.Unlock()

		if code := postCloseTab(t, h, tab); code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		awaitTeardown(t, h, "c1")
		joinInflight(t, h)

		if openAtRelease != 1 {
			t.Errorf("open turns at the release = %d, want 1: the release precedes every turn close", openAtRelease)
		}
		if !cs.hasText(t, "c1", "words queued at the release") {
			t.Error("the chunk queued at the release never reached the chat's log")
		}
		entries := logOf(t, cs, "c1")
		if opens, closes := len(opensOf(t, entries)), len(closesOf(t, entries)); opens != 1 || closes != 1 {
			t.Errorf("turn_open = %d, turn_close = %d, want one of each", opens, closes)
		}
	})
	t.Run("a turn started at the release is closed before the forgets", func(t *testing.T) {
		h, cs, br, tab := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open session: %v", err)
		}
		br.mu.Lock()
		br.beforeRelease = func() { br.deliver(newTurnStartMsg()) }
		br.mu.Unlock()

		if code := postCloseTab(t, h, tab); code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		awaitTeardown(t, h, "c1")
		joinInflight(t, h)

		entries := logOf(t, cs, "c1")
		if opens, closes := len(opensOf(t, entries)), len(closesOf(t, entries)); opens != closes {
			t.Errorf("turn_open = %d, turn_close = %d, want every opened turn closed", opens, closes)
		}
		if _, ok := h.coord.turns.lookup("c1"); ok {
			t.Error("the closed chat still has a lifecycle after its teardown")
		}
	})
}

// withSessionID is msg with params.sessionId set, as KAS names the session a frame belongs to.
func withSessionID(t *testing.T, msg *marotte.RPCResponse, sessionID string) *marotte.RPCResponse {
	t.Helper()
	var params map[string]json.RawMessage
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	params["sessionId"], _ = json.Marshal(sessionID)
	out := *msg
	out.Params, _ = json.Marshal(params)
	return &out
}

// Once the teardown released the old session, the frames KAS still sends for it reach the engine lane and none
// lands in the chat: no lifecycle is minted, the log does not grow, an ask is answered cancelled on the engine lane
// with nothing pending for the chat, and the reopened chat's turn starts with KAS's activity unread.
func TestCloseTab_TheOldSessionsFramesNeverReachTheReopenedChat(t *testing.T) {
	h, cs, br, tab := closeTabHub(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open session: %v", err)
	}
	old := recordSessionID(t, cs)
	gate := make(chan struct{})
	br.mu.Lock()
	br.stopGate = gate
	br.mu.Unlock()
	if code := postCloseTab(t, h, tab); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	awaitTeardown(t, h, "c1")
	before := len(logOf(t, cs, "c1"))

	src := fakeSourceOf(t, h)
	h.engineFrame(src.current(), withSessionID(t, newTurnStartMsg(), old))
	h.engineFrame(src.current(), withSessionID(t, newChunkMsg("words for nobody"), old))
	h.engineFrame(src.current(), withSessionID(t, newActivityMsg(false), old))
	h.engineFrame(src.current(), withSessionID(t, newTurnEndMsg("end_turn"), old))
	id := int64(77)
	params, _ := json.Marshal(map[string]any{"sessionId": old, "toolCall": map[string]any{"toolCallId": "t-old"}})
	h.engineFrame(src.current(), &marotte.RPCResponse{Method: marotte.MethodRequestPermission, ID: &id, Params: params})
	joinInflight(t, h)

	if _, ok := h.coord.turns.lookup("c1"); ok {
		t.Error("the released session's frames minted a lifecycle for the closed chat")
	}
	if got := len(logOf(t, cs, "c1")); got != before {
		t.Errorf("the chat's log grew from %d to %d entries on the released session's frames", before, got)
	}
	if r, ok := src.lane.responseTo(id); !ok || !isCancelledOutcome(r.result) {
		t.Errorf("the released session's ask = %+v (answered %v), want answered cancelled on the engine lane", r, ok)
	}
	if ids := h.bus.pendingPerms.idsForChat("c1"); len(ids) != 0 {
		t.Errorf("pending asks for the chat = %v, want none", ids)
	}

	close(gate)
	awaitSessionClosed(t, br)
	next := newFakeBridge()
	src.setFactory(func() engineSession { return next })
	reopenTab(t, h, "op-reopen")
	release := holdPrompt(t, next)
	if code := postPrompt(t, h, "again"); code != http.StatusOK {
		t.Fatalf("prompt after the reopen = %d, want its ack", code)
	}
	awaitWire(t, next, marotte.MethodPrompt)
	turn, ok := h.coord.PromptTurn("c1")
	if !ok {
		t.Fatal("the reopened chat's prompt has no turn")
	}
	if live, ok := h.coord.turns.turnByID("c1", turn.ID()); !ok || live.kasIdle {
		t.Errorf("the reopened chat's turn (open %v) carries an activity report it never read", ok)
	}
	release()
}

// isCancelledOutcome reports whether a permission answer is the cancelled outcome.
func isCancelledOutcome(result any) bool {
	got, _ := json.Marshal(result)
	want, _ := json.Marshal(marotte.PermissionOutcomeCancelled())
	return string(got) == string(want)
}

// A turn KAS starts while a reload's session/close waits for its answer is closed once the session's stream ends,
// and the prompt whose open asked for the reload, not yet sent, stays open for the reloaded session.
func TestReloadIfMarked_AWireTurnOpenedDuringItsCloseIsClosed(t *testing.T) {
	h, _ := reopenFixture(t)
	first, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("Setup: OpenBridge: %v", err)
	}
	res, ok := h.coord.turns.tryReserve("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp())
	if !ok {
		t.Fatal("Setup: the prompt's reservation was refused on an idle chat")
	}
	t.Cleanup(func() { h.coord.turns.releaseReservation(res) })
	prompt, err := h.coord.openTurnNow(t.Context(), "c1", command.TurnOpen{
		Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "prompt"},
	})
	if err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}
	old := fakeOf(first)
	gate := make(chan struct{})
	old.mu.Lock()
	old.stopGate = gate
	old.mu.Unlock()
	h.markSessionsForReload("test")

	opened := make(chan error, 1)
	go func() {
		_, err := h.coord.openAtReleased(context.Background(), "c1", "", res)
		opened <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if first.life.closingSession(first) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the reload never claimed the marked session")
		}
	}
	old.deliver(newTurnStartMsg())
	close(gate)
	if err := <-opened; err != nil {
		t.Fatalf("OpenBridge after the reload: %v", err)
	}

	open := h.coord.turns.openTurnIDs("c1")
	if len(open) != 1 || open[0].ID != prompt {
		ids := make([]string, 0, len(open))
		for _, o := range open {
			ids = append(ids, o.ID)
		}
		t.Errorf("open turns after the reload = %v, want only the unsent prompt %s", ids, prompt)
	}
	if closed := closedBroadcasts(t, h); len(closed) != 1 || closed[0].Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("turn_closed = %+v, want the wire turn closed interrupted once", closed)
	}
}

// A turn KAS starts while the release check's session/close waits for its answer is closed once the session's
// stream ends.
func TestReleaseCheck_AWireTurnOpenedDuringItsCloseIsClosed(t *testing.T) {
	h, _, sb := releaseFixture(t)
	t.Cleanup(func() { shutdownHub(t, h) })
	br := fakeOf(sb)
	gate := make(chan struct{})
	closeAt := make(chan struct{})
	var entered sync.Once
	br.mu.Lock()
	br.stopGate = gate
	br.onCloseStart = func() { entered.Do(func() { close(closeAt) }) }
	br.mu.Unlock()

	h.coord.releaseIfUnheld("c1")
	select {
	case <-closeAt:
	case <-time.After(5 * time.Second):
		t.Fatal("the release check's session/close never started")
	}
	br.deliver(newTurnStartMsg())
	close(gate)
	if !released(t, h, sb) {
		t.Fatal("the release check left the session open")
	}
	joinInflight(t, h)

	if open := h.coord.turns.openTurnIDs("c1"); len(open) != 0 {
		t.Errorf("open turns after the release check's close = %d, want 0", len(open))
	}
	if closed := closedBroadcasts(t, h); len(closed) != 1 || closed[0].Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("turn_closed = %+v, want the wire turn closed interrupted once", closed)
	}
}
