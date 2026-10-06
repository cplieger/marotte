package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// promptIDs is every turn_open's prompt id in the chat's log, in file order.
func promptIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	var ids []string
	for _, o := range opensOf(t, logOf(t, cs, chatID)) {
		if o.Prompt != nil {
			ids = append(ids, o.Prompt.ID)
		}
	}
	return ids
}

func queuedRows(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []marotte.QueuedPrompt {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q has no record", chatID)
	}
	return c.QueuedPrompts
}

func seedQueue(t *testing.T, cs *testChatStore, chatID marotte.ChatID, rows ...marotte.QueuedPrompt) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.QueuedPrompts = rows
		return true
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
}

// drainGates holds each drainHub's post-close drain switch, installed once: a close's goroutine may still read it.
var drainGates sync.Map

// drainHub is a hub whose c1 has run one prompt to its close, with the post-close drain parked for the test to drive.
func drainHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	gate := &atomic.Bool{}
	drainGates.Store(h, gate)
	t.Cleanup(func() { drainGates.Delete(h) })
	h.coord.drainAfterClose = func(ctx context.Context, chatID marotte.ChatID, cl command.CloseFacts, ends command.EndFacts) {
		if gate.Load() {
			h.dispatcher.DrainAfterClose(ctx, chatID, cl, ends)
		}
	}
	seedQueue(t, cs, "c1")
	runPrompt(t, h, cs, "c1", "m-first")
	return h, cs, br
}

// armDrain turns a drainHub's post-close drain on or off.
func armDrain(h *Runtime, on bool) {
	if g, ok := drainGates.Load(h); ok {
		g.(*atomic.Bool).Store(on)
	}
}

// drainOnce runs the command-side drain for a close with the given outcome, unfenced.
func drainOnce(t *testing.T, h *Runtime, chatID marotte.ChatID, o marotte.TurnOutcome) {
	t.Helper()
	h.dispatcher.DrainAfterClose(t.Context(), chatID, command.CloseFacts{Outcome: o}, command.EndFacts{})
}

// runPrompt posts a prompt and waits for its turn to close.
func runPrompt(t *testing.T, h *Runtime, cs *testChatStore, chatID marotte.ChatID, messageID string) {
	t.Helper()
	before := len(closesOf(t, logOf(t, cs, chatID)))
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: chatID,
		Payload: json.RawMessage(`{"text":"hello","message_id":"` + messageID + `"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("prompt %s = %d %s", messageID, rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, chatID))) > before && !h.coord.turns.live(chatID) })
}

func TestDrain_OpensQueuedTurnAfterCleanClose(t *testing.T) {
	h, cs, br := newTestHub()
	br.chunksOnCall = map[string][]string{marotte.MethodPrompt: {"done"}}
	unblock := make(chan struct{})
	br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: unblock}

	postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"start","message_id":"m-1"}`),
	})
	waitForCall(t, br, marotte.MethodPrompt)

	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdQueuePrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"then run the tests","message_id":"m-q1"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("queue_prompt mid-turn = %d %s", rec.Code, rec.Body.String())
	}
	close(unblock)

	waitFor(t, func() bool {
		return slices.Equal(promptIDs(t, cs, "c1"), []string{"m-1", "m-q1"}) && len(queuedRows(t, cs, "c1")) == 0
	})
}

// The closing prompt releases its slot after the close, so the drain can find that reservation held.
func TestDrain_WaitsOutTheClosingPromptsReservation(t *testing.T) {
	h, cs, _ := drainHub(t)
	armDrain(h, true)
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "then run the tests"})
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Fatal("TryReserveTurn(c1) refused on an idle chat")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.coord.afterTurnClose(t.Context(), "c1", closeFacts{outcome: marotte.TurnOutcomeCompleted})
	}()
	select {
	case <-done:
		t.Fatal("afterTurnClose returned while the closing prompt still held the slot, so the row would wait for a close that never comes")
	case <-time.After(100 * time.Millisecond):
	}
	h.coord.ReleaseTurnReservation("c1")
	<-done

	waitFor(t, func() bool { return !h.coord.turns.live("c1") && len(closesOf(t, logOf(t, cs, "c1"))) == 2 })
	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first", "m-q1"}) {
		t.Errorf("turns opened = %v, want the queued row once the slot freed", ids)
	}
}

// A turn opening behind the reservation owns the next close.
func TestDrain_StandsDownWhenATurnOpensBehindTheReservation(t *testing.T) {
	h, cs, _ := drainHub(t)
	armDrain(h, true)
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "next"})
	if !h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Fatal("TryReserveTurn(c1) refused on an idle chat")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.coord.afterTurnClose(t.Context(), "c1", closeFacts{outcome: marotte.TurnOutcomeCompleted})
	}()
	turnID, _ := h.stagePromptTurn(t, "c1")
	<-done
	armDrain(h, false)
	endTurn(t, h, "c1", turnID)
	h.coord.ReleaseTurnReservation("c1")

	if ids := promptIDs(t, cs, "c1"); slices.Contains(ids, "m-q1") {
		t.Errorf("turns opened = %v: the drain sent the row beside a turn that opened", ids)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 {
		t.Errorf("queue = %+v, want the row left for the next close", rows)
	}
}

func TestDrain_UserRowsWaitAfterCancelAndFailure(t *testing.T) {
	h, cs, _ := drainHub(t)
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "after this turn"})

	for _, o := range []marotte.TurnOutcome{
		marotte.TurnOutcomeCancelled, marotte.TurnOutcomeFailed, marotte.TurnOutcomeInterrupted, marotte.TurnOutcomeEmpty,
	} {
		drainOnce(t, h, "c1", o)
	}

	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first"}) {
		t.Errorf("turns opened = %v, want none after a stop or a failure", ids)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 {
		t.Errorf("queue = %+v, want the row still waiting", rows)
	}
}

func TestDrain_OneUserRowPerCleanClose(t *testing.T) {
	h, cs, _ := drainHub(t)
	seedQueue(t, cs, "c1",
		marotte.QueuedPrompt{ID: "m-u1", Text: "first follow-up"},
		marotte.QueuedPrompt{ID: "m-u2", Text: "second follow-up"},
	)

	drainOnce(t, h, "c1", marotte.TurnOutcomeCompleted)
	waitFor(t, func() bool { return !h.coord.turns.live("c1") && len(closesOf(t, logOf(t, cs, "c1"))) == 2 })

	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first", "m-u1"}) {
		t.Errorf("turns opened = %v, want one user row per clean close", ids)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 || rows[0].ID != "m-u2" {
		t.Errorf("queue = %+v, want only m-u2 left", rows)
	}
}

func TestDrain_HeldRowsNeverAutoSent(t *testing.T) {
	h, cs, _ := drainHub(t)
	held := marotte.CarriedRow("m-held-carry", []string{"x"}, []string{"steer-1"})
	held.Held = true
	seedQueue(t, cs, "c1", held, marotte.QueuedPrompt{ID: "m-held", Text: "from before the restart", Held: true})

	drainOnce(t, h, "c1", marotte.TurnOutcomeCompleted)

	if ids := promptIDs(t, cs, "c1"); !slices.Equal(ids, []string{"m-first"}) {
		t.Errorf("turns opened = %v, want a held row never sent", ids)
	}
}

func TestDrain_NoOpWhileDrainingOrBridgeDeadOrTurnLive(t *testing.T) {
	h, cs, _ := drainHub(t)
	seedQueue(t, cs, "c1", marotte.QueuedPrompt{ID: "m-q1", Text: "next"})

	h.lifecycle.draining.Store(true)
	drainOnce(t, h, "c1", marotte.TurnOutcomeCompleted)
	h.lifecycle.draining.Store(false)

	turnID, _ := h.stagePromptTurn(t, "c1")
	drainOnce(t, h, "c1", marotte.TurnOutcomeCompleted)
	endTurn(t, h, "c1", turnID)

	if ids := promptIDs(t, cs, "c1"); slices.Contains(ids, "m-q1") {
		t.Errorf("turns opened = %v: a draining runtime or a live turn sent the row", ids)
	}

	// No bridge: the death closer's case.
	seedQueue(t, cs, "c2", marotte.QueuedPrompt{ID: "m-q2", Text: "next"})
	drainOnce(t, h, "c2", marotte.TurnOutcomeCompleted)
	if rows := queuedRows(t, cs, "c2"); len(rows) != 1 {
		t.Errorf("queue = %+v, want the row kept for a chat with no bridge", rows)
	}
}

// A queue_prompt racing a close either lands before it or answers no turn.
func TestAppendIfLive_IdleRefusesAndLiveAppends(t *testing.T) {
	h, cs, _ := drainHub(t)
	appendRow := func(c *marotte.Chat) error {
		c.QueuedPrompts = append(c.QueuedPrompts, marotte.QueuedPrompt{ID: "m-q1", Text: "x"})
		return nil
	}

	if live, err := h.coord.AppendIfLive(t.Context(), "c1", appendRow); err != nil || live {
		t.Fatalf("idle AppendIfLive = (%v, %v), want (false, nil)", live, err)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 0 {
		t.Fatalf("an idle chat stored %+v", rows)
	}

	turnID, _ := h.stagePromptTurn(t, "c1")
	if live, err := h.coord.AppendIfLive(t.Context(), "c1", appendRow); err != nil || !live {
		t.Fatalf("live AppendIfLive = (%v, %v), want (true, nil)", live, err)
	}
	endTurn(t, h, "c1", turnID)
	if rows := queuedRows(t, cs, "c1"); len(rows) != 1 {
		t.Errorf("queue = %+v, want the row a live turn accepted", rows)
	}
}

// An opened row is sending; an unqueue says so.
func TestUnqueue_AnOpenedRowIsSending(t *testing.T) {
	h, cs, _ := drainHub(t)
	seedQueue(t, cs, "c1",
		marotte.QueuedPrompt{ID: "m-u1", Text: "first"},
		marotte.QueuedPrompt{ID: "m-u2", Text: "second"},
	)
	if err := h.coord.Dequeue(t.Context(), "c1", "m-u1"); err != nil {
		t.Fatalf("Dequeue(m-u1) = %v", err)
	}

	if sending, err := h.coord.Unqueue(t.Context(), "c1", "m-u1"); err != nil || !sending {
		t.Errorf("Unqueue(opened m-u1) = (%v, %v), want (true, nil)", sending, err)
	}
	if sending, err := h.coord.Unqueue(t.Context(), "c1", "m-u2"); err != nil || sending {
		t.Errorf("Unqueue(m-u2) = (%v, %v), want (false, nil)", sending, err)
	}
	if rows := queuedRows(t, cs, "c1"); len(rows) != 0 {
		t.Errorf("queue = %+v, want both rows gone", rows)
	}
}
