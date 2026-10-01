package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// closeOf returns the chat's one turn_close, failing when the log holds none or more
// than one: the prompt-failure closer runs once per turn, and two closes are two
// footers for one turn.
func closeOf(t *testing.T, cs *testChatStore, chatID marotte.ChatID) marotte.EntryTurnClose {
	t.Helper()
	closes := closesOf(t, logOf(t, cs, chatID))
	if len(closes) != 1 {
		t.Fatalf("turn_close count = %d, want exactly 1", len(closes))
	}
	return closes[0]
}

// TestAbandonInFlightTurn_SealsThePartial pins the direction of the close, and it is
// the reason this is a closer of its own rather than a discard: a failed prompt KEEPS
// what streamed. The user watched that text arrive, and invariant 1 says the client
// never displays what the server has not persisted, so the lane's open entry is
// sealed into the log ahead of the turn_close rather than dropped. Dropping it would
// make the transcript diverge on reload, the vanishing-message class this codebase has
// already paid for once.
func TestAbandonInFlightTurn_SealsThePartial(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const partial = "the model got this far before the pipe died"
	id, _ := streamingPromptTurn(t, h, "c1", partial)

	logs := captureLogs(t)
	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, "the pipe died")

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

// TestAbandonInFlightTurn_ClosesATurnThatNeverStreamed is the case the user actually
// hits: a 429 or a capacity refusal answers BEFORE the first chunk, so the turn holds
// only its turn_open. The close still lands, carrying the outcome and the reason, or
// the rail derives `completed` for a rate-limited turn and it renders pixel-identical
// to a clean short answer. Nothing else is written: an empty text entry would render
// as a blank reply bubble under the prompt.
func TestAbandonInFlightTurn_ClosesATurnThatNeverStreamed(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const reason = "Too many requests, please wait before trying again."
	id, _ := h.stagePromptTurn(t, "c1")
	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, reason)

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

// TestAbandonInFlightTurn_CarriesTheCallersReason is the ordinary failed prompt. The
// error frame that also carries the reason is ephemeral — one client surface, for the
// active chat, until the next reload — so the turn_close is where the reason survives.
func TestAbandonInFlightTurn_CarriesTheCallersReason(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const reason = "The model is at capacity. (request req-9)"
	id, _ := streamingPromptTurn(t, h, "c1", "half an answer")

	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, reason)

	if got := closeOf(t, cs, "c1").FailureReason; got != reason {
		t.Errorf("failure_reason = %q, want %q — without it the transcript cannot say "+
			"why the turn stopped once the error frame is gone", got, reason)
	}
}

// TestAbandonInFlightTurn_StashedReasonBeatsTheCallers pins the PRECEDENCE between the
// two writers. InterruptTurn stashes the cause when kiro-cli's tool-use security filter
// stops a turn; the prompt Call then fails as a consequence of that stop, so the
// caller's reason describes the fallout rather than the cause. The specific one has to
// win, or a security-filter stop reads as whatever RPC error it produced.
func TestAbandonInFlightTurn_StashedReasonBeatsTheCallers(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()

	const stashed = "Stopped by kiro-cli's tool-use security filter"
	const callers = "The turn was cancelled before the agent answered."

	// Interrupt through the coordinator, so the wiring is exercised rather than the
	// bridge's method in isolation. The cause lands on the open turn, which is why the
	// fixture opens one first.
	id, _ := streamingPromptTurn(t, h, "c1", "about to call a tool")

	// A spawned bridge is starting until its Start returns; this test holds no real
	// process, so it moves it to idle the way spawnBridge does at Start's return.
	sb, _ := h.bridge.mgr.orInsert("c1")
	sb.setIdle()
	if !sb.tryAcquireForPrompt() {
		t.Fatal("fresh bridge must be acquirable")
	}
	pctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	sb.BeginPromptCall(cancel)

	h.coord.InterruptTurn("c1", stashed)

	// The prompt context must be dead, or the blocked Call never returns and the chat
	// answers 409 busy to every later Send.
	select {
	case <-pctx.Done():
	default:
		t.Error("InterruptTurn left the prompt context live")
	}

	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, callers)

	if got := closeOf(t, cs, "c1").FailureReason; got != stashed {
		t.Errorf("failure_reason = %q, want the stashed %q — the caller's %q describes "+
			"the RPC failure the filter caused, not the stop itself", got, stashed, callers)
	}
}

// TestAbandonInFlightTurn_AnnouncesATurnThatNeverStreamed is the LIVE half of the
// unstreamed branch. The turn_closed broadcast is the only thing that clears
// `thinking`, restores Send and refreshes the rail — the error handler deliberately
// touches no turn state — so a throttle, an auth expiry or a bridge dying before the
// first chunk left a spinning composer on an idle server until the tab was reloaded.
func TestAbandonInFlightTurn_AnnouncesATurnThatNeverStreamed(t *testing.T) {
	h, _, _ := newTestHub()
	ctx := t.Context()

	id, _ := h.stagePromptTurn(t, "c1")
	before := h.bus.fanout.Position().Head
	h.AbandonInFlightTurn(ctx, "c1", id, marotte.StopReasonInterrupted, "Too many requests, please wait before trying again.")

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
