package agent

// The outcome, durable: what a finalized turn RECORDS, and what a prime records
// instead (nothing at all).

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// turnEndedOutcomes returns the outcome of every turn_ended broadcast so far.
func turnEndedOutcomes(t *testing.T, h *Runtime) []marotte.TurnOutcome {
	t.Helper()
	var out []marotte.TurnOutcome
	for _, e := range bufferedSince(h, 0) {
		var msg struct {
			Type    marotte.EventType `json:"type"`
			Payload struct {
				Outcome marotte.TurnOutcome `json:"outcome"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == marotte.EventTurnEnded {
			out = append(out, msg.Payload.Outcome)
		}
	}
	return out
}

// TestCloseOnWireEnd_StampsTheOutcomeOnTheAssistantMessage: a stop reason rides
// the live turn_ended SSE only, so an unstored one leaves a reload inferring the
// outcome from whichever event rows survive — and `error` produces none, so a turn
// that streamed an answer and then failed reads `completed`.
func TestCloseOnWireEnd_StampsTheOutcomeOnTheAssistantMessage(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "here is half an answer")

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError, "")

	c, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}
	var stamped *marotte.Message
	for i := range c.Messages {
		if c.Messages[i].Role == marotte.RoleAssistant {
			stamped = &c.Messages[i]
		}
	}
	if stamped == nil {
		t.Fatal("no assistant message was persisted")
	}
	if stamped.TurnOutcome != marotte.TurnOutcomeFailed {
		t.Errorf("persisted outcome = %q, want failed — a reload reads this, not the SSE", stamped.TurnOutcome)
	}
	if stamped.TurnStopReasonRaw != marotte.StopReasonError {
		t.Errorf("persisted raw stop reason = %q, want %q", stamped.TurnStopReasonRaw, marotte.StopReasonError)
	}
	if got := turnEndedOutcomes(t, h); len(got) != 1 || got[0] != marotte.TurnOutcomeFailed {
		t.Errorf("broadcast outcomes = %v, want exactly [failed]", got)
	}
}

// TestCloseOnWireEnd_TruncationCompletesRatherThanFails pins the one mapping a
// reader would get wrong by instinct: a turn stopped at a bound finished the work
// it was allowed to do, so it COMPLETED with its answer cut off. Grading it failed
// would report a bounded turn as broken.
func TestCloseOnWireEnd_TruncationCompletesRatherThanFails(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "as much as the budget allowed")

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonMaxTokens, "")

	c, _ := cs.Get(t.Context(), "c1")
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.Role != marotte.RoleAssistant {
			continue
		}
		if m.TurnOutcome != marotte.TurnOutcomeCompleted || !m.TurnTruncated {
			t.Errorf("outcome=%q truncated=%v, want completed + truncated", m.TurnOutcome, m.TurnTruncated)
		}
	}
}

// TestCloseOnWireEnd_AnEmptyFailedTurnPersistsAMarker is the carrier rule.
//
// A turn that emitted nothing has no assistant message, so the outcome has nowhere
// to live: before the marker, such a turn appended nothing at all, read `completed`
// on reload, and — having no message of its own — joined the PREVIOUS turn's body
// and vanished from the turn index.
func TestCloseOnWireEnd_AnEmptyFailedTurnPersistsAMarker(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.StartTurn(t.Context(), "c1", marotte.TurnSourcePrompt)

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError, "")

	c, _ := cs.Get(t.Context(), "c1")
	var marker *marotte.Message
	for i := range c.Messages {
		if c.Messages[i].EventKind == marotte.EventTurnOutcome {
			marker = &c.Messages[i]
		}
	}
	if marker == nil {
		t.Fatalf("no outcome marker was persisted, so the failure is unreadable after a reload. messages=%d", len(c.Messages))
	}
	if marker.Role != marotte.RoleEvent || marker.TurnOutcome != marotte.TurnOutcomeFailed {
		t.Errorf("marker = %+v, want a RoleEvent carrying failed", marker)
	}
}

// TestCloseOnWireEnd_AnEmptyCompletedPromptedTurnPersistsItsMarkerToo: a clean
// empty prompted turn and one a restart killed were byte-identical on disk while
// the writer omitted the marker, so the derivation's `completed` default was the
// reader guessing. The marker is what tells the two apart.
//
// Cost, accepted: one invisible EventTurnOutcome row per clean empty prompted turn.
func TestCloseOnWireEnd_AnEmptyCompletedPromptedTurnPersistsItsMarkerToo(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.StartTurn(t.Context(), "c1", marotte.TurnSourcePrompt)

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn, "")

	c, _ := cs.Get(t.Context(), "c1")
	var marker *marotte.Message
	for i := range c.Messages {
		if c.Messages[i].EventKind == marotte.EventTurnOutcome {
			marker = &c.Messages[i]
		}
	}
	if marker == nil {
		t.Fatalf("an empty COMPLETED prompted turn persisted no carrier, so it is "+
			"indistinguishable on disk from a turn nothing closed. messages=%d", len(c.Messages))
	}
	if marker.Role != marotte.RoleEvent || marker.TurnOutcome != marotte.TurnOutcomeCompleted {
		t.Errorf("marker = %+v, want a RoleEvent carrying completed", marker)
	}
}

// TestCloseOnWireEnd_AnEmptyEngineTurnPersistsNoMarkerButStillAnnounces pins the SPLIT,
// which is why it asserts a suppression and an emission together. No marker: an
// engine-opened turn has no trigger row, so the marker would be the whole turn and it
// opens a headerless card that renders nothing. Still announced: this turn is the chat's
// own and it really ended, and the client latches busy from server truth — the connect's
// `busy_chats` sets thinking, GET /api/chats/{id} reports turn_open — with only a settled
// turn_ended or a transport gap able to retract them, so suppressing the frame leaves the
// chat reading `running` with Cancel showing and Send meaning steer, indefinitely.
func TestCloseOnWireEnd_AnEmptyEngineTurnPersistsNoMarkerButStillAnnounces(t *testing.T) {
	h, cs, _ := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	// A turn marotte did not prompt: the first frame of the bracket opens a
	// wireTurnStart turn, and nothing folds into it.
	h.stageTurnBuffer(t, "c1")

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn, "")

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("an empty engine-opened turn persisted %d messages, so a headerless card opens "+
			"for a turn holding nothing: %+v", len(c.Messages), c.Messages)
	}
	if got := turnEndedOutcomes(t, h); len(got) != 1 || got[0] != marotte.TurnOutcomeCompleted {
		t.Errorf("turn_ended outcomes = %v, want exactly [%s]", got, marotte.TurnOutcomeCompleted)
	}
}
