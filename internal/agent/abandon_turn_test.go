package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// closeOf returns the chat's one turn_close, failing on none or several: two closes are two footers for one turn.
func closeOf(t *testing.T, cs *testChatStore, chatID marotte.ChatID) marotte.EntryTurnClose {
	t.Helper()
	closes := closesOf(t, logOf(t, cs, chatID))
	if len(closes) != 1 {
		t.Fatalf("turn_close count = %d, want exactly 1", len(closes))
	}
	return closes[0]
}

// TestAbandonInFlightTurn_SealsThePartial pins that a failed prompt KEEPS what streamed:
// the open entry is sealed ahead of the turn_close, or the transcript diverges on reload.
func TestAbandonInFlightTurn_SealsThePartial(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const partial = "the model got this far before the pipe died"
	id, _ := streamingPromptTurn(t, h, "c1", partial)

	logs := captureLogs(t)
	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, "the pipe died", "", 0)

	entries := logOf(t, cs, "c1")
	if texts := textsOf(t, entries); len(texts) != 1 || texts[0] != partial {
		t.Errorf("sealed texts = %q, want the partial the client already showed", texts)
	}
	c := closeOf(t, cs, "c1")
	if c.Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("outcome = %q, want interrupted: the transcript shows a truncated reply "+
			"with nothing saying why it stops", c.Outcome)
	}
	if h.liveTurn("c1") != nil {
		t.Error("the turn is still open after AbandonInFlightTurn; the next prompt's frames " +
			"would fold into this dead turn")
	}
	if out := logs.String(); strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("an ordinary abandon logged an error: %s", out)
	}
}

// TestAbandonInFlightTurn_ClosesATurnThatNeverStreamed covers a refusal before the first
// chunk: the close still carries the outcome and reason, and no empty text entry is written.
func TestAbandonInFlightTurn_ClosesATurnThatNeverStreamed(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const reason = "Too many requests, please wait before trying again."
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, reason, "", 0)

	entries := logOf(t, cs, "c1")
	if texts := textsOf(t, entries); len(texts) != 0 {
		t.Errorf("an unstreamed turn sealed text %q; the turn_close is the whole record", texts)
	}
	c := closeOf(t, cs, "c1")
	if c.Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("outcome = %q, want interrupted", c.Outcome)
	}
	if c.FailureReason != reason {
		t.Errorf("failure_reason = %q, want %q", c.FailureReason, reason)
	}
}

// TestAbandonInFlightTurn_CarriesTheCallersReason pins the turn_close as where the reason survives a reload.
func TestAbandonInFlightTurn_CarriesTheCallersReason(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const reason = "The model is at capacity. (request req-9)"
	id, _ := streamingPromptTurn(t, h, "c1", "half an answer")

	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, reason, "", 0)

	if got := closeOf(t, cs, "c1").FailureReason; got != reason {
		t.Errorf("failure_reason = %q, want %q — without it the transcript cannot say "+
			"why the turn stopped once the error frame is gone", got, reason)
	}
}

// TestAbandonInFlightTurn_StashedReasonBeatsTheCallers pins that the cause InterruptTurn
// stashed (a tool-use security-filter stop) wins over the caller's fallout error.
func TestAbandonInFlightTurn_StashedReasonBeatsTheCallers(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const stashed = "Stopped by kiro-cli's tool-use security filter"
	const callers = "The turn was cancelled before the agent answered."

	// Interrupt through the coordinator so the wiring is exercised; the cause lands on the open turn.
	id, _ := streamingPromptTurn(t, h, "c1", "about to call a tool")

	// No real process here, so move the bridge to idle the way spawnBridge does at Start's return.
	sb, _ := h.bridge.mgr.orInsert("c1")
	sb.setIdle()
	if !sb.tryAcquireForPrompt() {
		t.Fatal("fresh bridge must be acquirable")
	}
	pctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	sb.BeginPromptCall(cancel)

	h.coord.InterruptTurn("c1", stashed)

	// The prompt context must be dead, or the blocked Call never returns and the chat stays busy.
	select {
	case <-pctx.Done():
	default:
		t.Error("InterruptTurn left the prompt context live")
	}

	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, callers, "", 0)

	if got := closeOf(t, cs, "c1").FailureReason; got != stashed {
		t.Errorf("failure_reason = %q, want the stashed %q — the caller's %q describes "+
			"the RPC failure the filter caused, not the stop itself", got, stashed, callers)
	}
}

// TestAbandonInFlightTurn_AnnouncesATurnThatNeverStreamed pins the live broadcast: only
// turn_closed clears `thinking` and restores Send.
func TestAbandonInFlightTurn_AnnouncesATurnThatNeverStreamed(t *testing.T) {
	h, _, _ := newTestHub()
	ctx := t.Context()

	id, _ := h.stagePromptTurn(t, "c1")
	before := h.bus.fanout.Position().Head
	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, "Too many requests, please wait before trying again.", "", 0)

	ends := payloadsOfType[marotte.TurnClosedPayload](t, bufferedSince(h, before), marotte.EventTurnClosed)
	if len(ends) != 1 {
		t.Fatalf("turn_closed count = %d, want exactly 1: a turn that produced nothing "+
			"still closed, and the client clears its own state on nothing else", len(ends))
	}
	closes := closesOf(t, []marotte.Entry{ends[0].Entry})
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("outcome = %q, want %q", closes[0].Outcome, marotte.TurnOutcomeInterrupted)
	}
	if closes[0].StopReasonRaw != string(marotte.StopReasonInterrupted) {
		t.Errorf("stop_reason_raw = %q, want %q", closes[0].StopReasonRaw, marotte.StopReasonInterrupted)
	}
}

// TestAbandonInFlightTurn_StampsTheFailureKind pins that a classified failure kind reaches the turn_close, and an unclassified one carries none.
func TestAbandonInFlightTurn_StampsTheFailureKind(t *testing.T) {
	for _, kind := range []marotte.FailureKind{marotte.FailureKindContextLimit, ""} {
		h, cs, _ := newTestHub()
		id, _ := h.stagePromptTurn(t, "c1")
		h.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the context is full", kind, 0)
		if got := closeOf(t, cs, "c1").FailureKind; got != kind {
			t.Errorf("turn_close.failure_kind = %q, want %q", got, kind)
		}
	}
}
