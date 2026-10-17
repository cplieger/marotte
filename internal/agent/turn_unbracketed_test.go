package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func endTurnResponse() *marotte.RPCResponse {
	return &marotte.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
}

// The recorded shape of turn n=834: KAS ran the prompt (it launched a workflow) but its session adapter owned another
// execution, so no turn_start, chunk or tool call reached marotte before KAS answered the prompt end_turn. The record
// says KAS sent none of the run, never that the agent answered with nothing.
func TestPromptTurn_AnUnbracketedSilentEndIsNotGradedEmpty(t *testing.T) {
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	turnID, _ := h.stagePromptTurn(t, "c1")

	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, endTurnResponse())

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %+v, want one", closes)
	}
	c := closes[0]
	if c.Outcome != marotte.TurnOutcomeUnknown || c.StopReasonRaw != string(marotte.StopReasonEndTurn) ||
		c.FailureReason != marotte.UnobservedRunReason {
		t.Errorf("turn_close = %+v, want outcome unknown, raw end_turn and the unobserved-run reason", c)
	}
}

// The contrast: a bracketed turn KAS ended with nothing is the agent's empty answer.
func TestPromptTurn_ABracketedSilentEndIsGradedEmpty(t *testing.T) {
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnStart(t.Context(), "c1")

	h.SettleTurnOnResponse(t.Context(), "c1", turnID, 0, endTurnResponse())

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 || closes[0].Outcome != marotte.TurnOutcomeEmpty {
		t.Errorf("turn_close entries = %+v, want one graded empty", closes)
	}
}

// The recorded shape of turn n=834's start: the cancel's grace closed n=833 while KAS still ran executions for the
// session (its re-wake of the cleared notifications held it busy), and marotte sent the carried steer 6 ms later,
// which KAS queued beside its re-wake and orphaned. The drain waits until KAS reports the session idle.
func TestAfterTurnClose_AGraceClosedPromptsDrainWaitsForKASToGoIdle(t *testing.T) {
	setCancelGrace(t, time.Minute)
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	var drained atomic.Int32
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) {
		drained.Add(1)
	}
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnActivity("c1", true)

	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerPromptFailure, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})
	time.Sleep(100 * time.Millisecond)
	if n := drained.Load(); n != 0 {
		t.Fatalf("drained %d times while KAS still reported the session busy, want none", n)
	}

	h.coord.WireTurnActivity("c1", false)
	waitFor(t, func() bool { return drained.Load() == 1 })
}

// The contrast: a grace close while KAS last reported the session idle drains at once.
func TestAfterTurnClose_AGraceClosedPromptDrainsAtOnceWhenKASIsIdle(t *testing.T) {
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	var drained atomic.Int32
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) {
		drained.Add(1)
	}
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnActivity("c1", true)
	h.coord.WireTurnActivity("c1", false)

	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerPromptFailure, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})
	waitFor(t, func() bool { return drained.Load() == 1 })
}

// A wire turn KAS opens (its re-wake) ends the wait without a drain: that turn holds admission, and its own close drains.
func TestAfterTurnClose_AGraceClosedPromptsWaitEndsAtTheNextWireTurn(t *testing.T) {
	h, _, _ := newTestHub()
	defer shutdownHub(t, h)
	var drained atomic.Int32
	h.coord.drainAfterClose = func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) {
		drained.Add(1)
	}
	turnID, _ := h.stagePromptTurn(t, "c1")
	h.coord.WireTurnActivity("c1", true)
	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerPromptFailure, Stop: marotte.StopReasonCancelled, Turn: turnID,
	})

	h.coord.WireTurnStart(t.Context(), "c1")
	time.Sleep(100 * time.Millisecond)
	if n := drained.Load(); n != 0 {
		t.Fatalf("drained %d times beside KAS's own turn, want none until that turn closes", n)
	}

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)
	waitFor(t, func() bool { return drained.Load() == 1 })
}
