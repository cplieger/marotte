package agent

// These drive frames through consumeFrame (Forward's body minus its loop), which makes them deterministic with
// production sequences, deferred advance and settle wait.

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/sse"
)

// entriesOfBroadcasts returns the turn_close entry of every turn_closed frame in events.
func entriesOfBroadcasts(t *testing.T, events []sse.ReplayEvent) []marotte.Entry {
	t.Helper()
	var out []marotte.Entry
	for _, p := range payloadsOfType[marotte.TurnClosedPayload](t, events, marotte.EventTurnClosed) {
		out = append(out, p.Entry)
	}
	return out
}

// closedStops returns the raw stop of every turn_closed frame broadcast so far.
func closedStops(t *testing.T, h *Runtime) []marotte.StopReason {
	t.Helper()
	var out []marotte.StopReason
	for _, c := range closedBroadcasts(t, h) {
		out = append(out, marotte.StopReason(c.StopReasonRaw))
	}
	return out
}

// A settle taken on the response alone ends the turn with the response's outcome while turn_end and trailing content
// are unread. The wire says refusal, the response end_turn, and the chunk arrives behind the response.
func TestSettle_WaitsForQueuedFramesAndTakesTheWireOutcome(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	_, _ = cs.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	gen := h.coord.turns.attachForward(chatID)
	epoch, _ := h.stagePromptTurn(t, chatID)
	defer h.ReleaseTurn(chatID, epoch)

	queued := []marotte.Notification{
		{Msg: newTurnStartMsg(), Seq: 1},
		{Msg: newChunkMsg("the whole reply"), Seq: 2},
		{Msg: newTurnEndMsg("refusal"), Seq: 3},
	}

	before := h.bus.fanout.Position().Head
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		h.SettleTurnOnResponse(ctx, chatID, epoch, 3,
			&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, epoch, 3)

	for _, n := range queued {
		h.coord.consumeFrame(chatID, gen, n)
	}
	<-settled

	ends := closesOf(t, entriesOfBroadcasts(t, bufferedSince(h, before)))
	if len(ends) != 1 {
		t.Fatalf("turn_closed count = %d, want exactly 1 complete turn", len(ends))
	}
	if ends[0].StopReasonRaw != string(marotte.StopReasonRefusal) {
		t.Errorf("stop reason = %q, want the WIRE's %q: the local settle ran on the "+
			"response while turn_end was still queued", ends[0].StopReasonRaw, marotte.StopReasonRefusal)
	}
	result, err := h.AwaitTurn(ctx, chatID, epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded {
		t.Error("the turn reports WireEnded false, so the empty-turn recovery is armed on a locally-closed turn")
	}
	if result.EmittedNothing {
		t.Error("the turn reports EmittedNothing, so the settle measured it before the reply was folded")
	}
	if !cs.hasText(t, chatID, "the whole reply") {
		t.Error("the persisted turn does not carry the reply that arrived behind the response")
	}
}

// A settle whose last frame folds nothing still closes. On a caught publishTerminalEvent failure turn_completion is
// sent before the turn-end broadcast, so a fold-bound position parked here and thinking never cleared.
func TestSettle_ClosesWhenTheLastDeliveredFrameFoldsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	_, _ = cs.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	gen := h.coord.turns.attachForward(chatID)
	epoch, _ := h.stagePromptTurn(t, chatID)
	defer h.ReleaseTurn(chatID, epoch)

	// No turn_end: the fault path; the trailing frame is metering.
	queued := []marotte.Notification{
		{Msg: newChunkMsg("half an answer"), Seq: 1},
		{Msg: newTurnCompletionMsg(), Seq: 2},
	}

	before := h.bus.fanout.Position().Head
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		h.SettleTurnOnResponse(ctx, chatID, epoch, 2,
			&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, epoch, 2)

	for _, n := range queued {
		h.coord.consumeFrame(chatID, gen, n)
	}
	<-settled

	ends := payloadsOfType[marotte.TurnClosedPayload](t, bufferedSince(h, before), marotte.EventTurnClosed)
	if len(ends) != 1 {
		t.Fatalf("turn_closed count = %d, want 1: the settle parked behind a frame that folds nothing", len(ends))
	}
	result, err := h.AwaitTurn(ctx, chatID, epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if result.WireEnded {
		t.Error("a turn no bracket closed reports WireEnded true")
	}
}

// A closer armed for turn N, firing after N+1 opened, closes nothing; epoch-scoped claiming keeps a late settle off
// the next turn.
func TestSettle_ArmedForAnEarlierTurnClosesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	_, _ = cs.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	first, _ := h.stagePromptTurn(t, chatID)
	defer h.ReleaseTurn(chatID, first)
	h.SettleTurnOnResponse(ctx, chatID, first, 0,
		&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	second, _ := h.stagePromptTurn(t, chatID)
	defer h.ReleaseTurn(chatID, second)

	before := h.bus.fanout.Position().Head
	h.SettleTurnOnResponse(ctx, chatID, first, 0,
		&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})})

	if ends := payloadsOfType[marotte.TurnClosedPayload](t, bufferedSince(h, before), marotte.EventTurnClosed); len(ends) != 0 {
		t.Errorf("a closer armed for turn %q announced an end after turn %q opened: %+v", first, second, ends)
	}
	if _, open := ownID(h, chatID); !open {
		t.Error("the second turn is no longer open, so the stale closer took it")
	}
}

// A bridge dying mid-prompt closes the turn once and the parked settle defers: the death closer names the cause, and
// Forward seals the position first.
func TestBridgeDeath_ClosesTheTurnOnceAndTheParkedSettleDefers(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	epoch, _ := streamingPromptTurn(t, h, chatID, "half an answer")
	sb, _ := h.bridge.mgr.orInsert(chatID)
	sb.bridge = br

	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		h.coord.Forward(chatID, br)
	}()
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		// A position the dead bridge will never deliver.
		h.SettleTurnOnResponse(ctx, chatID, epoch, 99,
			&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, epoch, 99)

	// Stop without removing the bridge: the process died on its own.
	br.Stop()
	<-forwardDone
	<-settled

	if got := closedStops(t, h); !slices.Equal(got, []marotte.StopReason{marotte.StopReasonInterrupted}) {
		t.Errorf("turn_closed stops = %v, want exactly one interrupted: the parked settle "+
			"either beat the death closer or announced a second end", got)
	}
	result, err := h.AwaitTurn(ctx, chatID, epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if closes := closesOf(t, logOf(t, cs, chatID)); len(closes) != 1 || closes[0].FailureReason != deathInterruptCause {
		t.Errorf("turn_close entries = %+v, want one carrying the death cause: nothing durable says the agent process went away", closes)
	}
	if result.WireEnded {
		t.Error("a turn no bracket closed reports WireEnded true, which arms the empty-turn recovery on a dead bridge")
	}
}

// The wait does not stop at the awaited turn's close: the empty-turn gate's later-turn read is only sound once the
// folder has consumed everything before the response, or it re-sends an answered prompt.
func TestAwaitPosition_DoesNotStopAtTheAwaitedTurnsOwnClose(t *testing.T) {
	r := newTurnRegistry()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	gen := r.attachForward(chatID)

	// Opened at the registry alone; the store append is not the subject.
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	turn := lc.openLocked(chatID, &marotte.Entry{ID: "t-1"}, marotte.TurnSourcePrompt, "", turnlog.Open("t-1", nil))
	lc.mu.Unlock()
	claimed, won := r.claimOwn(ctx, chatID)
	if !won {
		t.Fatal("claimOwn lost the claim on a freshly opened turn")
	}
	r.finish(claimed, marotte.TurnResult{Stop: marotte.StopReasonEndTurn})

	reached := make(chan bool, 1)
	go func() { reached <- r.awaitPosition(ctx, chatID, turn.ID, 5) }()

	select {
	case got := <-reached:
		t.Fatalf("awaitPosition returned %v with the folder still at position 0: the settle "+
			"stopped at the turn's own close, so the recovery decision races the folder", got)
	case <-time.After(50 * time.Millisecond):
	}

	r.observe(chatID, gen, 5)
	if got := <-reached; !got {
		t.Error("awaitPosition reported the position unreachable after the folder reached it")
	}
}

// A zero-content auto-wake can bind the pre-open and close it with end_turn and nothing emitted, satisfying the gate's
// first three clauses. Only the later-turn clause saves the prompt, once its own turn_start has folded.
func TestSettle_ReturnsOnlyAfterTheFolderHasCaughtUp(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID marotte.ChatID = "c1"
	_, _ = cs.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	gen := h.coord.turns.attachForward(chatID)
	preOpen, _ := h.stagePromptTurn(t, chatID)
	defer h.ReleaseTurn(chatID, preOpen)

	// The auto-wake's brackets mis-bind and close the pre-open; the prompted turn's bracket is still queued.
	h.coord.consumeFrame(chatID, gen, marotte.Notification{Msg: newTurnStartMsg(), Seq: 1})
	h.coord.consumeFrame(chatID, gen, marotte.Notification{Msg: newTurnEndMsg("end_turn"), Seq: 2})

	var openedAfterAtSettle bool
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		h.SettleTurnOnResponse(ctx, chatID, preOpen, 4,
			&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
		openedAfterAtSettle = h.coord.TurnOpenedAfter(chatID, preOpen)
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, preOpen, 4)

	h.coord.consumeFrame(chatID, gen, marotte.Notification{Msg: newTurnStartMsg(), Seq: 3})
	h.coord.consumeFrame(chatID, gen, marotte.Notification{Msg: newTurnEndMsg("end_turn"), Seq: 4})
	<-settled

	if !openedAfterAtSettle {
		t.Error("the settle returned before the folder consumed the prompted turn's bracket, so " +
			"the empty-turn gate read no-later-turn and would re-send an answered prompt")
	}
	result, err := h.AwaitTurn(ctx, chatID, preOpen)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded || !result.EmittedNothing {
		t.Errorf("the mis-bound pre-open = {wire:%v empty:%v}, want both true — the fixture is not "+
			"reproducing the chain the fourth clause exists for", result.WireEnded, result.EmittedNothing)
	}
}
