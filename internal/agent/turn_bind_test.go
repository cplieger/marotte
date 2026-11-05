package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// ownID is the id of the chat's own open turn, read the way the lifecycle holds
// it rather than by claiming it.
func ownID(h *Runtime, chatID marotte.ChatID) (string, bool) {
	lc := h.coord.turns.testLifecycle(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing {
		return "", false
	}
	return lc.own.ID, true
}

// pendingID reads the unacknowledged prompt turn's id from the lifecycle: bindPending retires what it binds.
func pendingID(h *Runtime, chatID marotte.ChatID) (string, bool) {
	lc := h.coord.turns.testLifecycle(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.pending == nil {
		return "", false
	}
	return lc.pending.ID, true
}

func ownSource(h *Runtime, chatID marotte.ChatID) marotte.TurnOpenSource {
	lc := h.coord.turns.testLifecycle(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.own.Source
}

func openText(h *Runtime, chatID marotte.ChatID) string {
	log := h.liveTurn(chatID)
	if log == nil {
		return ""
	}
	open, _ := log.Open("")
	return open.Text
}

// The turn_start echoing the prompt's tag binds its turn; otherwise each prompt makes two turns and its outcome lands
// on the wrong one.
func TestWireTurnStart_TheEchoedTagBindsThePrompt(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	id, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, id)

	h.translateACPEvent(chatID, h.originOf(chatID), newPromptTurnStartMsg(id))

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

// A turn_start with a turn open and nothing pending means the previous end was lost: it closes `unknown` and a
// wire turn opens, with no timer.
func TestWireTurnStart_ClosesATurnWhoseEndNeverArrived(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	// streamingPromptTurn's turn_start acknowledges it, so the next bracket finds nothing pending.
	first, _ := streamingPromptTurn(t, h, chatID, "the first turn's reply")

	h.translateACPEvent(chatID, h.originOf(chatID), newTurnStartMsg())

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

// A turn_start that does not echo the waiting prompt's tag is a turn KAS started itself: it opens a wire turn,
// and the prompt stays pending for its own bracket.
func TestWireTurnStart_ABracketWithoutThePromptsTagOpensTheAgentsTurn(t *testing.T) {
	for name, start := range map[string]*marotte.RPCResponse{
		"no tag":       newTurnStartMsg(),
		"a minted tag": newSessionInfoMsg(map[string]any{"kind": "turn_start", "turnStart": true, "userMessageTag": "prompt_0b8f1c2e-4a5d-4e6f-9a7b-1c2d3e4f5a6b"}),
	} {
		t.Run(name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			const chatID marotte.ChatID = "c1"
			prompt, _ := h.stagePromptTurn(t, chatID)
			defer h.coord.ReleaseTurn(chatID, prompt)

			h.translateACPEvent(chatID, h.originOf(chatID), start)
			h.translateACPEvent(chatID, h.originOf(chatID), newChunkMsg("the agent woke itself"))

			agentTurn, open := ownID(h, chatID)
			if !open || agentTurn == prompt {
				t.Fatalf("own turn = (%q, %v), want a wire turn beside the prompt's %q", agentTurn, open, prompt)
			}
			if got := openText(h, chatID); got != "the agent woke itself" {
				t.Errorf("the agent's turn holds %q, want its own reply", got)
			}
			if pending, ok := pendingID(h, chatID); !ok || pending != prompt {
				t.Errorf("pending turn = (%q, %v), want the prompt's %q, owed its own bracket", pending, ok, prompt)
			}

			h.translateACPEvent(chatID, h.originOf(chatID), newTurnEndMsg("end_turn"))
			h.translateACPEvent(chatID, h.originOf(chatID), newPromptTurnStartMsg(prompt))
			h.translateACPEvent(chatID, h.originOf(chatID), newChunkMsg("the answer to the prompt"))
			if own, open := ownID(h, chatID); !open || own != prompt {
				t.Fatalf("own turn = (%q, %v), want the prompt's %q once its bracket arrived", own, open, prompt)
			}
			if got := openText(h, chatID); got != "the answer to the prompt" {
				t.Errorf("the prompt's turn holds %q, want only its own reply", got)
			}

			h.translateACPEvent(chatID, h.originOf(chatID), newTurnEndMsg("end_turn"))
			result, err := h.coord.AwaitTurn(t.Context(), chatID, prompt)
			if err != nil {
				t.Fatalf("AwaitTurn(prompt): %v", err)
			}
			if !result.WireEnded {
				t.Error("the prompt's turn closed without its own turn_end")
			}
			if got := closesOf(t, logOf(t, cs, chatID)); len(got) != 2 {
				t.Errorf("turn_close entries = %d, want two: the agent's and the prompt's", len(got))
			}
		})
	}
}

// An agent run KAS aborts on reading the prompt can end with no content frame: its cancelled turn_end closes the
// agent's turn, never the prompt's, which goes on to hold its reply.
func TestWireTurnStart_AnAgentRunAbortedBeforeContentLeavesThePromptOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	prompt, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, prompt)

	h.translateACPEvent(chatID, h.originOf(chatID), newTurnStartMsg())
	h.translateACPEvent(chatID, h.originOf(chatID), newTurnEndMsg("cancelled"))
	if pending, ok := pendingID(h, chatID); !ok || pending != prompt {
		t.Fatalf("pending turn = (%q, %v) after the aborted run, want the prompt's %q still open", pending, ok, prompt)
	}

	h.translateACPEvent(chatID, h.originOf(chatID), newPromptTurnStartMsg(prompt))
	h.translateACPEvent(chatID, h.originOf(chatID), newChunkMsg("the answer to the prompt"))
	h.translateACPEvent(chatID, h.originOf(chatID), newTurnEndMsg("end_turn"))

	result, err := h.coord.AwaitTurn(t.Context(), chatID, prompt)
	if err != nil {
		t.Fatalf("AwaitTurn(prompt): %v", err)
	}
	if !result.WireEnded || result.Stop != marotte.StopReasonEndTurn {
		t.Errorf("the prompt's turn = {wire:%v stop:%q}, want its own end_turn, not the aborted run's cancel", result.WireEnded, result.Stop)
	}
	if !cs.hasText(t, chatID, "the answer to the prompt") {
		t.Error("the prompt's reply was not sealed")
	}
	if got := closesOf(t, logOf(t, cs, chatID)); len(got) != 2 {
		t.Errorf("turn_close entries = %d, want two: the aborted run's and the prompt's", len(got))
	}
}

// A locally failed prompt turn is retired, or the next prompt's bracket binds the dead turn and its own closes
// on end_turn or cancelled, persisting refusals as `completed`.
func TestPreOpen_IsRetiredWhenItFinalizes(t *testing.T) {
	h, _, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	failed, _ := h.stagePromptTurn(t, chatID)
	h.coord.AbandonInFlightTurn(t.Context(), chatID, failed, marotte.StopReasonInterrupted, "the pipe died", "", 0)
	h.coord.ReleaseTurn(chatID, failed)

	if _, ok := pendingID(h, chatID); ok {
		t.Fatal("a finalized prompt turn is still the binding candidate, so the NEXT prompt's bracket would acknowledge a dead turn")
	}

	// The next prompt gets its own bracket and outcome.
	next, _ := h.stagePromptTurn(t, chatID)
	defer h.coord.ReleaseTurn(chatID, next)
	h.translateACPEvent(chatID, h.originOf(chatID), newPromptTurnStartMsg(next))
	h.translateACPEvent(chatID, h.originOf(chatID), newChunkMsg("the second answer"))
	h.translateACPEvent(chatID, h.originOf(chatID), newTurnEndMsg("refusal"))

	result, err := h.coord.AwaitTurn(t.Context(), chatID, next)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded || result.Stop != marotte.StopReasonRefusal {
		t.Errorf("the next prompt's turn = {wire:%v stop:%q}, want the wire's own refusal", result.WireEnded, result.Stop)
	}
}

// A turn_end with no open turn records nothing; otherwise a locally closed turn's late bracket makes a phantom.
func TestWireTurnEnd_WithNoOpenTurnPersistsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	cs.seed(t, chatID, func(c *marotte.Chat) { c.Name = "A" })

	h.translateACPEvent(chatID, h.originOf(chatID), newTurnEndMsg("end_turn"))

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

// A replayed turn_end closes no live turn, or a resume closes the turn it runs during.
func TestWireTurnEnd_ReplayedBracketClosesNoLiveTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	id, _ := streamingPromptTurn(t, h, chatID, "a live reply")

	h.translateACPEvent(chatID, h.originOf(chatID), newReplayedTurnEndMsg("end_turn"))

	if own, open := ownID(h, chatID); !open || own != id {
		t.Errorf("own turn = (%q, %v), want the live turn %q still open", own, open, id)
	}
	if got := closesOf(t, logOf(t, cs, chatID)); len(got) != 0 {
		t.Errorf("turn_close entries = %+v, want none: stored history is not something happening now", got)
	}
}

// A fold with no open turn opens one, or its content has no record or end.
func TestFold_WithNoOpenTurnOpensAWireTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID marotte.ChatID = "c1"
	cs.seed(t, chatID, func(c *marotte.Chat) { c.Name = "A" })

	h.translateACPEvent(chatID, h.originOf(chatID), newChunkMsg("nobody prompted this"))

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
