package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// closesOf decodes every turn_close in entries, in file order. Counting them is
// the point: a turn closed twice is two footers in the transcript for one turn.
func closesOf(t *testing.T, entries []marotte.Entry) []marotte.EntryTurnClose {
	t.Helper()
	var out []marotte.EntryTurnClose
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnClose {
			continue
		}
		var c marotte.EntryTurnClose
		if err := json.Unmarshal(entries[i].Payload, &c); err != nil {
			t.Fatalf("decode turn_close %q: %v", entries[i].ID, err)
		}
		out = append(out, c)
	}
	return out
}

// textsOf returns the text of every sealed text entry in entries, in file order.
func textsOf(t *testing.T, entries []marotte.Entry) []string {
	t.Helper()
	var out []string
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindText {
			continue
		}
		var text marotte.EntryText
		if err := json.Unmarshal(entries[i].Payload, &text); err != nil {
			t.Fatalf("decode text %q: %v", entries[i].ID, err)
		}
		out = append(out, text.Text)
	}
	return out
}

// logOf reads the chat's whole log.
func logOf(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []marotte.Entry {
	t.Helper()
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All(%q): %v", chatID, err)
	}
	return entries
}

// closedBroadcasts decodes the turn_close entry of every turn_closed frame
// broadcast so far, in order.
func closedBroadcasts(t *testing.T, h *Runtime) []marotte.EntryTurnClose {
	t.Helper()
	var entries []marotte.Entry
	for _, p := range payloadsOfType[marotte.TurnClosedPayload](t, bufferedSince(h, 0), marotte.EventTurnClosed) {
		entries = append(entries, p.Entry)
	}
	return closesOf(t, entries)
}

// deadContext is a context already cancelled, which is what BOTH doors into
// finalizeTurn are handed at shutdown: the prompt door's cancel is AfterFunc'd
// onto the shutdown context, and the bridge-death door is passed that context
// itself.
func deadContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// streamingPromptTurn stages a prompt turn with text already sealed into its
// accumulator, the way a live prompt leaves it mid-reply.
func streamingPromptTurn(t *testing.T, h *Runtime, chatID marotte.ChatID, text string) (string, *turnlog.Turn) {
	t.Helper()
	id, log := h.stagePromptTurn(t, chatID)
	if _, err := log.TextDelta(t.Context(), "", "say-1", text); err != nil {
		t.Fatalf("TextDelta: %v", err)
	}
	return id, log
}

// shellTurn opens a `!cmd` turn and starts it, answering its id.
func shellTurn(t *testing.T, h *Runtime, chatID marotte.ChatID) string {
	t.Helper()
	id, err := h.coord.OpenTurn(t.Context(), chatID, marotte.TurnSourceLocalShell,
		&marotte.EntryPrompt{ID: "m-shell", Text: "!ls"},
		func(c *marotte.Chat) { c.Name = "test chat" })
	if err != nil {
		t.Fatalf("OpenTurn(local_shell): %v", err)
	}
	if !h.coord.StartTurn(t.Context(), chatID, id) {
		t.Fatalf("StartTurn(%q) refused", id)
	}
	return id
}

// TestCloseTurnOnBridgeDeath_ClosesAnOpenTurn is the third actor. A prompt whose
// bridge dies has a settle that may never arrive, so nothing else is going to
// close the turn: the streamed text would stay unsealed, the next turn's frames
// would fold into it, and the client would be left with `thinking` set on a chat
// whose process is gone.
func TestCloseTurnOnBridgeDeath_ClosesAnOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const partial = "the model got this far before the pipe died"
	streamingPromptTurn(t, h, "c1", partial)

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1", marotte.TurnOutcomeInterrupted)

	entries := logOf(t, cs, "c1")
	if got := textsOf(t, entries); !slices.Equal(got, []string{partial}) {
		t.Errorf("sealed texts = %q, want the partial alone; a reload would lose what the client showed", got)
	}
	closes := closesOf(t, entries)
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted || closes[0].StopReasonRaw != string(marotte.StopReasonInterrupted) {
		t.Errorf("turn_close = outcome %q stop %q, want interrupted/interrupted", closes[0].Outcome, closes[0].StopReasonRaw)
	}
	if closes[0].FailureReason != deathInterruptCause {
		t.Errorf("failure_reason = %q, want %q", closes[0].FailureReason, deathInterruptCause)
	}
	if got := closedBroadcasts(t, h); len(got) != 1 || got[0].Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("turn_closed broadcasts = %+v, want exactly one interrupted", got)
	}
	if h.coord.turns.live("c1") {
		t.Error("the chat still reads live after its only turn closed")
	}
}

// TestCloseTurnOnBridgeDeath_IgnoresAChatWithNoOpenTurn: a bridge culled while
// idle, or one whose turn a closer already finished, must record nothing. Opening
// a turn in order to close it would report an interruption nothing was
// interrupted from, on every bridge exit the app performs.
func TestCloseTurnOnBridgeDeath_IgnoresAChatWithNoOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1", marotte.TurnOutcomeInterrupted)

	if got := logOf(t, cs, "c1"); len(got) != 0 {
		t.Errorf("entries = %+v, want none", got)
	}
	if got := closedBroadcasts(t, h); len(got) != 0 {
		t.Errorf("turn_closed broadcasts = %+v, want none", got)
	}
}

// TestCloseTurnOnBridgeDeath_ACancelledOutcomeConcludesCancelled is the deliberate
// stop's grade: a chat deleted or closed mid-turn asked for the stop, so the
// footer must not read BROKEN for a stop the reader caused.
func TestCloseTurnOnBridgeDeath_ACancelledOutcomeConcludesCancelled(t *testing.T) {
	h, cs, _ := newTestHub()
	streamingPromptTurn(t, h, "c1", "half an answer")

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1", marotte.TurnOutcomeCancelled)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeCancelled || closes[0].StopReasonRaw != string(marotte.StopReasonCancelled) {
		t.Errorf("turn_close = outcome %q stop %q, want cancelled/cancelled", closes[0].Outcome, closes[0].StopReasonRaw)
	}
}

// TestCloseTurnOnBridgeDeath_RecordsOneTextlessSteerPerKASQueuedRow is the death
// closer's third step, and the only thing that records a loss nothing else can see:
// a bridge that dies while KAS holds steers it queued and never delivered leaves
// the log short of words KAS persisted. One steer entry per queued id, with EMPTY
// text, is that record — an empty text says "KAS holds words this process never
// received", which is what the resume-time reconcile predicate reads to decide the
// projection must not be discarded silently. The text is dropped on purpose even
// when the buffered row carried one, so the arming rows here carry text.
func TestCloseTurnOnBridgeDeath_RecordsOneTextlessSteerPerKASQueuedRow(t *testing.T) {
	h, cs, _ := newTestHub()
	streamingPromptTurn(t, h, "c1", "the model was mid-reply when the pipe died")
	h.bus.steers.SteerWaiting("c1", marotte.SteerQueuedPayload{
		SteerID: "s-1", Text: "use tabs", Origin: marotte.SteerOriginUser,
	})
	h.bus.steers.SteerWaiting("c1", marotte.SteerQueuedPayload{
		SteerID: "s-2", Text: "and rename it", Origin: marotte.SteerOriginUser,
	})

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1", marotte.TurnOutcomeInterrupted)

	// The waiting set is a map, so the order the ids come back in is arbitrary and is
	// not part of the contract; what is pinned is ONE entry per id.
	recorded := map[string]marotte.EntrySteer{}
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var s marotte.EntrySteer
		if err := json.Unmarshal(e.Payload, &s); err != nil {
			t.Fatalf("decode the steer %q: %v", e.ID, err)
		}
		if _, held := recorded[e.ID]; held {
			t.Errorf("steer %q was recorded twice, want one entry per queued id", e.ID)
		}
		recorded[e.ID] = s
	}
	if got := slices.Sorted(maps.Keys(recorded)); !slices.Equal(got, []string{"s-1", "s-2"}) {
		t.Fatalf("steer entry ids = %q, want one per queued id: s-1, s-2", got)
	}
	for _, id := range slices.Sorted(maps.Keys(recorded)) {
		s := recorded[id]
		if s.Text != "" {
			t.Errorf("steer %q text = %q, want empty: the empty text is the whole record of what KAS kept and this process lost", id, s.Text)
		}
		if s.Origin != marotte.SteerOriginUser || s.State != marotte.SteerStateDropped {
			t.Errorf("steer %q = origin %q state %q, want user/dropped", id, s.Origin, s.State)
		}
	}
}

// TestFinalizeTurn_ALostClaimAmendsNothing pins the exclusion at the coordinator:
// two closers reaching one turn produce one turn_close, and the loser's account
// never reaches the footer the winner wrote. A lost claim is a Debug line now,
// not an amendment.
func TestFinalizeTurn_ALostClaimAmendsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	id, _ := streamingPromptTurn(t, h, "c1", "half an answer")

	h.coord.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "the first closer's account")
	h.coord.AbandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonCancelled, "the loser's account")

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted || closes[0].FailureReason != "the first closer's account" {
		t.Errorf("turn_close = outcome %q reason %q, want the winner's interrupted/first account", closes[0].Outcome, closes[0].FailureReason)
	}
	if got := closedBroadcasts(t, h); len(got) != 1 {
		t.Errorf("turn_closed broadcasts = %d, want exactly one", len(got))
	}
}

// TestFinalizeLocalShellTurn_WritesThreeEntriesAndAnnouncesTheEnd is the shell
// turn's whole shape: turn_open, one text entry carrying the output, turn_close
// concluding completed, and one turn_closed frame.
func TestFinalizeLocalShellTurn_WritesThreeEntriesAndAnnouncesTheEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	id := shellTurn(t, h, "c1")

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", id, "```\nfile.go\n```")

	entries := logOf(t, cs, "c1")
	var kinds []marotte.EntryKind
	for i := range entries {
		kinds = append(kinds, entries[i].Kind)
	}
	want := []marotte.EntryKind{marotte.EntryKindTurnOpen, marotte.EntryKindText, marotte.EntryKindTurnClose}
	if !slices.Equal(kinds, want) {
		t.Fatalf("entry kinds = %v, want %v", kinds, want)
	}
	if got := textsOf(t, entries); !slices.Equal(got, []string{"```\nfile.go\n```"}) {
		t.Errorf("texts = %q, want the rendered output", got)
	}
	closes := closesOf(t, entries)
	if closes[0].Outcome != marotte.TurnOutcomeCompleted || closes[0].StopReasonRaw != string(marotte.StopReasonEndTurn) {
		t.Errorf("turn_close = outcome %q stop %q, want completed/end_turn", closes[0].Outcome, closes[0].StopReasonRaw)
	}
	if got := closedBroadcasts(t, h); len(got) != 1 || got[0].StopReasonRaw != string(marotte.StopReasonEndTurn) {
		t.Errorf("turn_closed broadcasts = %+v, want exactly one end_turn", got)
	}
}

// TestWireTurnEnd_StampsTheOutcomeAndTheRawStop is the wire's own closer: the
// turn_close carries the graded outcome, the stop exactly as sent, and the
// engine's details as the failure reason.
func TestWireTurnEnd_StampsTheOutcomeAndTheRawStop(t *testing.T) {
	h, cs, _ := newTestHub()
	streamingPromptTurn(t, h, "c1", "half an answer")

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError, "the model refused the request")

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeFailed || closes[0].StopReasonRaw != string(marotte.StopReasonError) {
		t.Errorf("turn_close = outcome %q stop %q, want failed/error", closes[0].Outcome, closes[0].StopReasonRaw)
	}
	if closes[0].FailureReason != "the model refused the request" {
		t.Errorf("failure_reason = %q, want the wire's details", closes[0].FailureReason)
	}
}

// TestWireTurnEnd_TheFailureReasonIsSanitizedAndBounded: the details are upstream
// text, and a transcript footer is no place for a wall of it or for a control
// sequence.
func TestWireTurnEnd_TheFailureReasonIsSanitizedAndBounded(t *testing.T) {
	h, cs, _ := newTestHub()
	streamingPromptTurn(t, h, "c1", "half an answer")
	// The cap is spelled as a number here on purpose: a test that derives both its
	// input and its bound from maxReasonBytes cannot see the constant move.
	details := strings.Repeat("x", 3000) + "\n\x1b[31mred"

	h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError, details)

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	reason := closes[0].FailureReason
	if len(reason) > 2048+len("...") {
		t.Errorf("failure_reason is %d bytes, want at most 2048 plus the marker", len(reason))
	}
	if strings.ContainsAny(reason, "\n\x1b") {
		t.Errorf("failure_reason carries a newline or an escape: %q", reason)
	}
	if !strings.HasSuffix(reason, "...") {
		t.Errorf("failure_reason = %q, want the cap marker on a capped reason", reason)
	}
}

// TestWireTurnEnd_TruncationCompletesRatherThanFails pins the one mapping a reader
// would get wrong by instinct: a turn stopped at a bound finished the work it was
// allowed to do, so it completed with its answer cut off. Grading it failed would
// report a bounded turn as broken.
func TestWireTurnEnd_TruncationCompletesRatherThanFails(t *testing.T) {
	for _, stop := range []marotte.StopReason{marotte.StopReasonMaxTokens, marotte.StopReasonMaxTurnRequests} {
		t.Run(string(stop), func(t *testing.T) {
			h, cs, _ := newTestHub()
			streamingPromptTurn(t, h, "c1", "half an answer")

			h.coord.WireTurnEnd(t.Context(), "c1", stop, "")

			closes := closesOf(t, logOf(t, cs, "c1"))
			if len(closes) != 1 {
				t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
			}
			if closes[0].Outcome != marotte.TurnOutcomeCompleted || !closes[0].Truncated {
				t.Errorf("turn_close = outcome %q truncated %v, want completed + truncated", closes[0].Outcome, closes[0].Truncated)
			}
			if closes[0].StopReasonRaw != string(stop) {
				t.Errorf("stop_reason_raw = %q, want %q", closes[0].StopReasonRaw, stop)
			}
		})
	}
}

// TestFinalizeTurn_ATurnWithNoContentReportsEmittedNothing is the empty-turn
// check's input: the result names a turn that produced no content, so the prompt
// path can conclude `empty` and recover, and a turn that did produce content is
// never recreated.
func TestFinalizeTurn_ATurnWithNoContentReportsEmittedNothing(t *testing.T) {
	h, _, _ := newTestHub()
	id, _ := h.stagePromptTurn(t, "c1")

	h.coord.SettleTurnOnResponse(t.Context(), "c1", id, 0,
		&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	result, err := h.coord.AwaitTurn(t.Context(), "c1", id)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.EmittedNothing {
		t.Error("a turn with no content reported EmittedNothing false, so the empty-turn recovery never runs")
	}
	if result.Stop != marotte.StopReasonEndTurn {
		t.Errorf("Stop = %q, want end_turn", result.Stop)
	}
}

// TestFinalizeTurn_MeasuresEmittedAfterReleasingTheCarry is the flush-before-
// measure order, and the defect it closes is a user-visible one: the steering
// filter withholds any trailing text that could still grow into an acknowledgement
// marker, so a reply ending in `[` sits entirely in the carry when the turn
// settles. Measured before the release, that turn reports having produced nothing,
// the empty-turn recovery recreates the session and re-prompts a question the agent
// had already answered.
func TestFinalizeTurn_MeasuresEmittedAfterReleasingTheCarry(t *testing.T) {
	h, cs, _ := newTestHub()
	id, log := h.stagePromptTurn(t, "c1")
	const reply = "see the note in ["
	log.SetSteerCarry("", "say-1", reply)

	h.coord.SettleTurnOnResponse(t.Context(), "c1", id, 0,
		&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	result, err := h.coord.AwaitTurn(t.Context(), "c1", id)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if result.EmittedNothing {
		t.Error("a turn whose only text was an unreleased carry reported EmittedNothing; the session would be recreated and the prompt re-sent")
	}
	if got := textsOf(t, logOf(t, cs, "c1")); !slices.Equal(got, []string{reply}) {
		t.Errorf("sealed texts = %q, want the released carry %q", got, reply)
	}
}

// TestAwaitTurn_HandleOutlivesTheFinalizeAndDropsOnRelease is the retention bound.
// The record leaves the chat's lifecycle the moment the turn finalizes, so without
// a handle a caller awaiting its OWN turn would be told the turn never existed; and
// with retention keyed on anything but the handle it could be evicted before that
// caller read it.
func TestAwaitTurn_HandleOutlivesTheFinalizeAndDropsOnRelease(t *testing.T) {
	h, _, _ := newTestHub()
	id := shellTurn(t, h, "c1")

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", id, "out")

	result, err := h.coord.AwaitTurn(t.Context(), "c1", id)
	if err != nil {
		t.Fatalf("AwaitTurn on a held handle: %v", err)
	}
	if result.Stop != marotte.StopReasonEndTurn {
		t.Errorf("Stop = %q, want %q", result.Stop, marotte.StopReasonEndTurn)
	}
	if result.Turn != id {
		t.Errorf("result Turn = %q, want %q", result.Turn, id)
	}

	h.coord.ReleaseTurn("c1", id)
	if _, err := h.coord.AwaitTurn(t.Context(), "c1", id); !errors.Is(err, marotte.ErrNoSuchTurn) {
		t.Errorf("after the last handle was released, AwaitTurn error = %v, want ErrNoSuchTurn", err)
	}
}

// TestAwaitTurn_UnknownTurnReportsNoSuchTurn: an id the chat never held is the one
// case that answers ErrNoSuchTurn, which is what makes the sentinel meaningful for
// the caller that does hold a handle.
func TestAwaitTurn_UnknownTurnReportsNoSuchTurn(t *testing.T) {
	h, _, _ := newTestHub()
	if _, err := h.coord.AwaitTurn(t.Context(), "c1", "t-never-opened"); !errors.Is(err, marotte.ErrNoSuchTurn) {
		t.Errorf("AwaitTurn on an unknown turn = %v, want ErrNoSuchTurn", err)
	}
}

// TestAwaitTurn_DeadContextReturnsRatherThanParking: a waiter selects on its own
// context as well as on the handle, so a caller whose turn context died does not
// park until something finalizes. The finalize afterwards shows the abandoned wait
// left the chat's lifecycle usable.
func TestAwaitTurn_DeadContextReturnsRatherThanParking(t *testing.T) {
	h, cs, _ := newTestHub()
	id := shellTurn(t, h, "c1")
	defer h.coord.ReleaseTurn("c1", id)

	if _, err := h.coord.AwaitTurn(deadContext(t), "c1", id); !errors.Is(err, context.Canceled) {
		t.Errorf("AwaitTurn with a dead context = %v, want context.Canceled", err)
	}

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", id, "out")
	if got := closesOf(t, logOf(t, cs, "c1")); len(got) != 1 || got[0].StopReasonRaw != string(marotte.StopReasonEndTurn) {
		t.Errorf("turn_close entries = %+v, want exactly one end_turn", got)
	}
}

// An id-scoped closer handed an EMPTY id closes nothing.
//
// Empty is also what OpenTurn answers when it refuses, which is reachable, so
// reading it as "take whatever is open" let a failed prompt claim an
// agent-initiated turn and close it under an interrupt carrying ANOTHER turn's
// failure reason. That meaning has its own spelling (Own) now.
func TestAbandonInFlightTurn_WithNoTurnIDClosesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.seed(t, "c1", func(c *marotte.Chat) { c.Name = "A" })
	// A turn the ENGINE started, streaming. It holds no prompt slot, so admission
	// never refused the prompt whose open then failed.
	engine := h.stageWireTurn(t, "c1")
	if engine == nil {
		t.Fatal("no engine turn was opened")
	}
	if _, err := engine.TextDelta(t.Context(), "", "say-1", "the agent's own reply"); err != nil {
		t.Fatalf("TextDelta: %v", err)
	}

	h.coord.AbandonInFlightTurn(t.Context(), "c1", "", marotte.StopReasonInterrupted, "The turn was cancelled before the agent answered.")

	if own, ok := h.coord.OwnTurn("c1"); !ok || own != engine {
		t.Errorf("own turn = (%p, %v), want the engine's turn %p still open: a prompt failure "+
			"that never opened a turn closed one it does not own", own, ok, engine)
	}
	if got := closesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
		t.Errorf("turn_close entries = %+v, want none: the agent's turn was closed under the prompt's reason", got)
	}
	if got := closedBroadcasts(t, h); len(got) != 0 {
		t.Errorf("turn_closed broadcasts = %+v, want none", got)
	}
}

// TestAbandonInFlightTurn_AnUnsetStopNormalizesToInterrupted: an unset stop must
// never grade a prompt failure `unknown`, which would report a broken turn as one
// that merely stopped for a reason marotte could not read.
func TestAbandonInFlightTurn_AnUnsetStopNormalizesToInterrupted(t *testing.T) {
	h, cs, _ := newTestHub()
	id, _ := h.stagePromptTurn(t, "c1")

	h.coord.AbandonInFlightTurn(t.Context(), "c1", id, "", "a reason")

	closes := closesOf(t, logOf(t, cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted || closes[0].StopReasonRaw != string(marotte.StopReasonInterrupted) {
		t.Errorf("turn_close = outcome %q stop %q, want interrupted/interrupted", closes[0].Outcome, closes[0].StopReasonRaw)
	}
	if closes[0].FailureReason != "a reason" {
		t.Errorf("failure_reason = %q, want the caller's", closes[0].FailureReason)
	}
}

// The closer seals the accumulator under its own mutex, so a fold still in flight
// when the claim landed cannot tear the text: the delta that landed before the
// close is in the sealed entry whole, and the folder is refused after it. Run with
// -race; that is the point of it.
func TestCloseTurn_ConcurrentFoldDoesNotRaceTheSeal(t *testing.T) {
	h, cs, _ := newTestHub()
	id, log := streamingPromptTurn(t, h, "c1", "the first delta")

	stop := make(chan struct{})
	folding := make(chan struct{})
	running := make(chan struct{})
	go func() {
		defer close(folding)
		first := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = log.TextDelta(t.Context(), "", "say-1", "x")
			log.ChangedFile("a.go", 1, 0, false)
			if first {
				close(running)
				first = false
			}
		}
	}()
	// The folder must be LOOPING before the closer starts, or the two never overlap
	// and the detector has nothing to see: a race the test cannot reach is a test
	// that cannot fail.
	<-running

	h.coord.SettleTurnOnResponse(t.Context(), "c1", id, 0,
		&marotte.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	close(stop)
	<-folding

	entries := logOf(t, cs, "c1")
	texts := textsOf(t, entries)
	if len(texts) == 0 || !strings.HasPrefix(texts[0], "the first delta") {
		t.Errorf("the closer sealed no text for a turn that streamed; texts = %q", texts)
	}
	if got := closesOf(t, entries); len(got) != 1 {
		t.Errorf("turn_close entries = %d, want exactly one", len(got))
	}
}

// TestFinalizeTurn_PersistsOnACancelledContext is the incident: a chat streamed
// for 46 minutes, the container restarted, and the transcript came back holding
// only the user message. The store refused the close on its entry guard seeing the
// shutdown-cancelled context, and everything unsealed went with the refusal.
func TestFinalizeTurn_PersistsOnACancelledContext(t *testing.T) {
	h, cs, _ := newTestHub()
	const partial = "the model got this far before the container restarted"
	id, _ := streamingPromptTurn(t, h, "c1", partial)

	h.coord.finalizeTurn(deadContext(t), "c1", &turnClose{
		Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: id,
	})

	entries := logOf(t, cs, "c1")
	if got := textsOf(t, entries); !slices.Equal(got, []string{partial}) {
		t.Errorf("sealed texts = %q, want the partial; the close was refused on a dead context", got)
	}
	closes := closesOf(t, entries)
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one: without it the client reads this turn as completed", len(closes))
	}
	if closes[0].Outcome != marotte.TurnOutcomeInterrupted || closes[0].FailureReason != deathInterruptCause {
		t.Errorf("turn_close = outcome %q reason %q, want interrupted with the death cause", closes[0].Outcome, closes[0].FailureReason)
	}
}

// TestFinalizeTurn_DoesNotDetachThePositionWait is the placement guard: the
// detach sits BELOW the position wait rather than at the top of finalizeTurn.
//
// awaitPosition exits on the folder reaching the position, the folder going away,
// or ctx.Done(), and a wedged kiro-cli that never closes its pipe leaves the third
// as the only one that fires. A detach above it trades a lost turn for a hung
// shutdown, so this asserts on ELAPSED TIME: moved up, it hangs, not fails.
func TestFinalizeTurn_DoesNotDetachThePositionWait(t *testing.T) {
	h, cs, _ := newTestHub()
	id, log := streamingPromptTurn(t, h, "c1", "half an answer")

	// A position the folder will never reach: nothing is attached to advance it.
	const unreachable = 99
	settled := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(settled)
		h.coord.finalizeTurn(deadContext(t), "c1", &turnClose{
			Closer: closerPromptResponse,
			Turn:   id,
			Seq:    unreachable,
			Resp:   &marotte.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)},
		})
	}()
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("finalizeTurn parked on a position the folder cannot reach; the detach is above awaitPosition")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("finalizeTurn took %v to abandon an unreachable position, want a prompt return", elapsed)
	}

	// And it claimed nothing: the position could not be reached, so the
	// bridge-death closer owns whatever is still open.
	if own, ok := h.coord.OwnTurn("c1"); !ok || own != log {
		t.Errorf("own turn = (%p, %t), want turn %q still open", own, ok, id)
	}
	if got := closesOf(t, logOf(t, cs, "c1")); len(got) != 0 {
		t.Errorf("an abandoned settle closed the turn: %+v", got)
	}
}
