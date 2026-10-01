package agent

// The open sources, provisional acknowledgement, and the wire's brackets.

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// ownID is the id of the chat's own open turn, read the way the lifecycle holds
// it rather than by claiming it.
func ownID(h *Runtime, chatID marotte.ChatID) (string, bool) {
	t, ok := h.coord.turns.ownTurn(chatID)
	if !ok {
		return "", false
	}
	return t.ID, true
}

// pendingID is the id of the chat's unacknowledged prompt turn, read the way the
// lifecycle holds it rather than by consuming it: bindPending RETIRES what it
// binds, so a test that called it to ask the question would answer it and change
// it in one step.
func pendingID(h *Runtime, chatID marotte.ChatID) (string, bool) {
	lc := h.coord.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.pending == nil {
		return "", false
	}
	return lc.pending.ID, true
}

// ownSource is the source of the chat's own open turn.
func ownSource(h *Runtime, chatID marotte.ChatID) marotte.TurnOpenSource {
	lc := h.coord.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.own.Source
}

// openText is the text still coalescing in the chat's own turn, in the agent's
// own lane: where the next delta would land.
func openText(h *Runtime, chatID marotte.ChatID) string {
	log := h.liveTurn(chatID)
	if log == nil {
		return ""
	}
	open, _ := log.Open("")
	return open.Text
}

// A wire turn_start binds to the pending prompt turn rather than closing it and
// opening a turn of its own.
//
// Without the binding, every prompt would produce TWO turns: the prompt's own,
// closed by its own bracket, and a wire_turn_start turn the prompt has no handle
// on, so the prompt's outcome would land on a turn nothing was waiting for.
func TestWireTurnStart_BindsThePendingPreOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	id, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, id)

	h.translateACPEvent(chatID, newTurnStartMsg())

	if h.coord.TurnOpenedAfter(chatID, id) {
		t.Error("the bracket opened a SECOND turn instead of binding to the prompt's")
	}
	if own, _ := ownID(h, chatID); own != id {
		t.Errorf("own turn = %q, want the prompt's %q", own, id)
	}
	if pending, ok := pendingID(h, chatID); ok {
		t.Errorf("turn %q is still pending after its bracket, so the NEXT turn_start would bind it again", pending)
	}
	if got := closesOf(t, logOf(t, cs, chatID)); len(got) != 0 {
		t.Errorf("the bracket closed a turn: %+v", got)
	}
}

// A turn_start arriving with a turn already open and NOTHING pending means the
// previous turn's end never came. It is closed `unknown` and a wire_turn_start turn
// opens in its place: the agent-initiated class, and it needs no timer.
func TestWireTurnStart_ClosesATurnWhoseEndNeverArrived(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	first, _ := streamingPromptTurn(t, h, chatID, "the first turn's reply")
	// Acknowledge it, so the next bracket has nothing pending to bind.
	h.translateACPEvent(chatID, newTurnStartMsg())

	h.translateACPEvent(chatID, newTurnStartMsg())

	closes := closesOf(t, logOf(t, cs, chatID))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeUnknown || closes[0].StopReasonRaw != string(marotte.StopReasonUnknown) {
		t.Errorf("turn_close entries = %+v, want exactly one unknown/unknown", closes)
	}
	second, open := ownID(h, chatID)
	if !open {
		t.Fatal("no turn is open after the second bracket, so the agent's turn has no record")
	}
	if second == first {
		t.Errorf("the second bracket reused turn %q instead of opening its own", first)
	}
	if !cs.hasText(t, chatID, "the first turn's reply") {
		t.Error("the lost turn's reply was not sealed into the log")
	}
}

// A frame carrying agentInitiated revises a provisional binding: the started turn
// was the AGENT's, and the prompt turn is owed its own bracket after all.
//
// This is the only discriminator on the wire (the flag rides content frames and
// never the bracket), so what the test pins is the handover: the agent's turn
// holds the text the frame folded, and the prompt turn is pending again.
func TestReviseTurnBinding_HandsTheLogToTheAgentsTurn(t *testing.T) {
	h, _, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	preOpen, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, preOpen)
	h.translateACPEvent(chatID, newTurnStartMsg())

	h.translateACPEvent(chatID, newAgentInitiatedChunkMsg("the agent woke itself"))

	agentTurn, open := ownID(h, chatID)
	if !open {
		t.Fatal("no turn is open after the revision")
	}
	if agentTurn == preOpen {
		t.Fatal("the agent's content is still folding into the prompt's turn, so the binding was never revised")
	}
	if !h.coord.TurnOpenedAfter(chatID, preOpen) {
		t.Error("the agent's turn does not follow the prompt's in the open sequence, which the empty-turn gate's structural clause reads")
	}
	if got := openText(h, chatID); got != "the agent woke itself" {
		t.Errorf("the agent's turn holds %q, want the content folded before the revision", got)
	}
	if pending, ok := pendingID(h, chatID); !ok || pending != preOpen {
		t.Errorf("pending turn = (%q, %v), want the prompt's %q back: its own bracket has nothing to bind to otherwise", pending, ok, preOpen)
	}
}

// A prompt turn whose call fails LOCALLY is retired, so the next prompt's bracket
// binds to the next prompt's turn.
//
// Without retirement the dead turn stays the binding candidate forever: the next
// prompt's turn_start acknowledges a finished turn, that prompt's own turn never
// receives a bracket, and it closes on the prompt response's outcome, which can
// only ever be end_turn or cancelled, so a refusal or an error would persist as
// `completed`.
func TestPreOpen_IsRetiredWhenItFinalizes(t *testing.T) {
	h, _, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	failed, _ := h.stagePromptTurn(t, chatID)
	h.coord.AbandonInFlightTurn(t.Context(), chatID, failed, marotte.StopReasonInterrupted, "the pipe died")
	h.coord.ReleaseTurn(chatID, failed)

	if _, ok := pendingID(h, chatID); ok {
		t.Fatal("a finalized prompt turn is still the binding candidate, so the NEXT prompt's bracket would acknowledge a dead turn")
	}

	// The next prompt gets its own bracket and the wire's own outcome.
	next, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, next)
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newChunkMsg("the second answer"))
	h.translateACPEvent(chatID, newTurnEndMsg("refusal"))

	result, err := h.coord.AwaitTurn(t.Context(), chatID, next)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded || result.Stop != marotte.StopReasonRefusal {
		t.Errorf("the next prompt's turn = {wire:%v stop:%q}, want the wire's own refusal", result.WireEnded, result.Stop)
	}
}

// A turn_end for a chat with NO open turn records nothing.
//
// Without the no-op, a cancel-grace expiry that closed its turn locally would meet
// the later wire bracket, and manufacturing a turn out of it would put a phantom in
// the log: an extra turn in the rail, with an outcome, that the user never took.
func TestWireTurnEnd_WithNoOpenTurnPersistsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	cs.seed(t, chatID, func(c *marotte.Chat) { c.Name = "A" })

	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))

	if got := logOf(t, cs, chatID); len(got) != 0 {
		t.Errorf("entries = %+v, want none: a bracket for a turn nobody opened is a no-op", got)
	}
	if got := closedBroadcasts(t, h); len(got) != 0 {
		t.Errorf("turn_closed broadcasts = %+v, want none", got)
	}
	if _, open := ownID(h, chatID); open {
		t.Error("the bracket opened a turn in order to close it")
	}
}

// A REPLAYED turn_end closes no live turn.
//
// KAS replays a whole transcript on session/load as ordinary session/update
// notifications, so without the per-frame replay gate a resume would close the
// turn that resume is happening during.
func TestWireTurnEnd_ReplayedBracketClosesNoLiveTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	id, _ := streamingPromptTurn(t, h, chatID, "a live reply")

	h.translateACPEvent(chatID, newReplayedTurnEndMsg("end_turn"))

	if own, open := ownID(h, chatID); !open || own != id {
		t.Errorf("own turn = (%q, %v), want the live turn %q still open", own, open, id)
	}
	if got := closesOf(t, logOf(t, cs, chatID)); len(got) != 0 {
		t.Errorf("turn_close entries = %+v, want none: stored history is not something happening now", got)
	}
}

// A fold with no open turn OPENS one: a turn marotte did not prompt needs a record
// like any other, or its content has nothing to end it, nothing to account for it,
// and the next turn's frames to extend it.
func TestFold_WithNoOpenTurnOpensAWireTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	cs.seed(t, chatID, func(c *marotte.Chat) { c.Name = "A" })

	h.translateACPEvent(chatID, newChunkMsg("nobody prompted this"))

	id, open := ownID(h, chatID)
	if !open {
		t.Fatal("a fold with no open turn opened no turn, so this content belongs to nothing")
	}
	if source := ownSource(h, chatID); source != marotte.TurnSourceWireTurnStart {
		t.Errorf("turn %q source = %v, want wire_turn_start", id, source)
	}
	if got := openText(h, chatID); got != "nobody prompted this" {
		t.Errorf("the turn's open text = %q, want the folded chunk", got)
	}
	entries := logOf(t, cs, chatID)
	if len(entries) != 1 || entries[0].Kind != marotte.EntryKindTurnOpen || entries[0].ID != id {
		t.Errorf("entries = %+v, want the turn_open alone on disk while the text coalesces", entries)
	}
}

// A local shell turn REFUSES while another turn is open: a shell turn cannot begin
// during an agent turn. The admission slot catches the turns marotte itself
// prompted; this catches the ones it did not, which is the class with no slot to
// hold. Running anyway would fold the command's output into the agent's live turn.
func TestOpenTurn_LocalShellRefusesWhileATurnIsOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	cs.seed(t, chatID, func(c *marotte.Chat) { c.Name = "A" })
	h.translateACPEvent(chatID, newTurnStartMsg())

	if id, err := h.coord.OpenTurn(t.Context(), chatID, marotte.TurnSourceLocalShell,
		&marotte.EntryPrompt{ID: "m-shell", Text: "!ls"}, nil); err == nil {
		t.Errorf("OpenTurn(local_shell) = %q, want a refusal while an agent turn is open", id)
	}

	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))
	id := shellTurn(t, h, chatID)
	h.coord.ReleaseTurn(chatID, id)
}

// The full handover, both directions. After a revision the prompt turn is pending
// again, so the agent's turn_end closes the agent's turn, the NEXT turn_start binds
// the prompt turn, and the prompt's reply folds into the turn the prompt is
// holding rather than opening a third.
func TestReviseTurnBinding_ThePreOpenStillReceivesItsOwnBracket(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	preOpen, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, preOpen)
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newAgentInitiatedChunkMsg("the agent woke itself"))
	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))

	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newChunkMsg("the answer to the prompt"))

	if own, open := ownID(h, chatID); !open || own != preOpen {
		t.Fatalf("own turn = (%q, %v), want the prompt's %q: its bracket bound a turn nothing can reach, so the fold opened a third one", own, open, preOpen)
	}
	if got := openText(h, chatID); got != "the answer to the prompt" {
		t.Errorf("the prompt's turn holds %q, want the prompt's reply", got)
	}

	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))
	result, err := h.coord.AwaitTurn(t.Context(), chatID, preOpen)
	if err != nil {
		t.Fatalf("AwaitTurn on the turn this caller opened: %v; a handle holder can never be told its own turn does not exist", err)
	}
	if !result.WireEnded {
		t.Error("the prompt's turn closed without a wire bracket, so its own turn_end reached something else")
	}
	if !cs.hasText(t, chatID, "the answer to the prompt") {
		t.Error("the prompt's reply is not sealed under its own turn")
	}
	if !cs.hasText(t, chatID, "the agent woke itself") {
		t.Error("the agent's reply is not sealed")
	}
	if got := closesOf(t, logOf(t, cs, chatID)); len(got) != 2 {
		t.Errorf("turn_close entries = %d, want two: the agent's and the prompt's", len(got))
	}
}
