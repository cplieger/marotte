package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/marotte/internal/marotte"
)

// logFixture is one entry log under its own root, with the header beside it, so a
// test can assert on both halves of what an operation wrote.
type logFixture struct {
	t      *testing.T
	log    *EntryLog
	header EntryHeader
	root   string
}

// newLogFixture opens a CHAT root: the header's three hooks are wired, so the session
// the header names, the closer's model and the two counters are all observable.
func newLogFixture(t *testing.T) *logFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "chats", "c-abcdef01")
	h := NewEntryHeader(root)
	lg, err := OpenEntryLog(t.Context(), root, h)
	if err != nil {
		t.Fatalf("open entry log: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	return &logFixture{t: t, log: lg, header: h, root: root}
}

// reopen closes and reopens the log, which is what runs the scan, the torn-tail rule
// and the store-open closer again.
func (f *logFixture) reopen() {
	f.t.Helper()
	if err := f.log.Close(); err != nil {
		f.t.Fatalf("close log: %v", err)
	}
	lg, err := OpenEntryLog(f.t.Context(), f.root, f.header)
	if err != nil {
		f.t.Fatalf("reopen entry log: %v", err)
	}
	f.log = lg
}

func (f *logFixture) openTurn(spec *TurnSpec) string {
	f.t.Helper()
	e, err := f.log.OpenTurn(f.t.Context(), spec)
	if err != nil {
		f.t.Fatalf("open turn: %v", err)
	}
	return e.Turn
}

func (f *logFixture) prompt(text string) string {
	f.t.Helper()
	return f.openTurn(&TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + text, Text: text},
	})
}

func (f *logFixture) append(turn, lane, id string, kind marotte.EntryKind, payload any) {
	f.t.Helper()
	if err := f.log.Append(f.t.Context(), entryOf(turn, lane, id, kind, payload)); err != nil {
		f.t.Fatalf("append %s: %v", kind, err)
	}
}

func (f *logFixture) closeTurn(turn string, outcome marotte.TurnOutcome) {
	f.t.Helper()
	f.append(turn, "", turn+":close", marotte.EntryKindTurnClose, marotte.EntryTurnClose{Outcome: outcome})
}

func (f *logFixture) readHeader() *marotte.Chat {
	f.t.Helper()
	c, err := f.header.Read(f.t.Context())
	if err != nil {
		f.t.Fatalf("read header: %v", err)
	}
	return c
}

// entryOf is one sealed entry ready for Append; the store assigns seq and ts.
func entryOf(turn, lane, id string, kind marotte.EntryKind, payload any) *marotte.Entry {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic("entryOf: " + err.Error())
	}
	return &marotte.Entry{ID: id, Turn: turn, Lane: lane, Kind: kind, Payload: raw}
}

// shapes is each entry as "<seq>:<lane>/<kind>", the envelope facts every rule here
// is about.
func shapes(entries []marotte.Entry) []string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		out = append(out, fmt.Sprintf("%d:%s/%s", entries[i].Seq, entries[i].Lane, entries[i].Kind))
	}
	return out
}

func wantShapes(t *testing.T, got []marotte.Entry, want []string, what string) {
	t.Helper()
	if strings.Join(shapes(got), ",") != strings.Join(want, ",") {
		t.Errorf("%s holds %v, want %v", what, shapes(got), want)
	}
}

// A directory holding chat.json and no entries.jsonl is the normal state of a fresh
// chat: every read answers empty, no descriptor is held, and the first turn open
// creates the log.
func TestEntryLog_AMissingLogIsAChatOfZeroTurns(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", Name: "fresh"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	f.reopen()

	window, err := f.log.Window(20, "")
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	count, last := f.log.Counters()
	switch {
	case len(window.Entries) != 0 || window.HasMore:
		t.Errorf("window is %+v, want empty with has_more false", window)
	case len(f.log.RailRows()) != 0:
		t.Errorf("rail index is %+v, want empty", f.log.RailRows())
	case count != 0 || last != "":
		t.Errorf("counters are (%d, %q), want (0, \"\")", count, last)
	}
	if _, err := os.Stat(filepath.Join(f.root, entriesFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat of the log answered %v, want it absent until the first append", err)
	}

	turn := f.prompt("first")
	if _, err := os.Stat(filepath.Join(f.root, entriesFileName)); err != nil {
		t.Errorf("the first turn open did not create the log: %v", err)
	}
	if got, _ := f.log.Counters(); got != 1 {
		t.Errorf("turn_count after one open is %d, want 1", got)
	}
	if rows := f.log.RailRows(); len(rows) != 1 || rows[0].ID != turn || rows[0].N != 1 {
		t.Errorf("rail rows are %+v, want one row for turn 1", rows)
	}
}

// seq is contiguous from 0 per turn, where the turn_open is 0, and the header's two
// caches follow the log.
func TestEntryLog_SeqIsContiguousAndTheHeaderCachesFollow(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "hi"})
	f.append(turn, "", "call-1", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "call-1"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	entries, err := f.log.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("turn range: %v", err)
	}
	wantShapes(t, entries, []string{"0:/turn_open", "1:/text", "2:/tool_call", "3:/turn_close"}, "one turn")

	if err := f.log.WriteCounters(t.Context()); err != nil {
		t.Fatalf("write counters: %v", err)
	}
	h := f.readHeader()
	if h.TurnCount != 1 || h.LastTurnOutcome != marotte.TurnOutcomeCompleted {
		t.Errorf("header caches are (%d, %q), want (1, completed)", h.TurnCount, h.LastTurnOutcome)
	}
}

// last_turn_outcome is the newest FINISHED turn's, so a turn still running does not
// blank it.
func TestEntryLog_LastOutcomeIsTheNewestFinishedTurn(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	f.closeTurn(first, marotte.TurnOutcomeFailed)
	second := f.prompt("two")
	f.closeTurn(second, marotte.TurnOutcomeCompleted)
	f.prompt("three")

	count, last := f.log.Counters()
	if count != 3 || last != marotte.TurnOutcomeCompleted {
		t.Errorf("counters are (%d, %q), want (3, completed)", count, last)
	}
}

// The store half of property 1's second generator: two of a chat's turns are open
// together in one registry state, their entries interleave in the file, and each
// turn's seq stays contiguous on its own. n is the prompt's plus one.
func TestEntryLog_TwoOpenTurnsInterleave(t *testing.T) {
	f := newLogFixture(t)
	promptTurn := f.prompt("do it")
	agentTurn := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})

	f.append(promptTurn, "", "hook-1", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "hook-1"})
	f.append(agentTurn, "", "say-a", marotte.EntryKindText, marotte.EntryText{Text: "agent"})
	f.append(promptTurn, "", "say-p", marotte.EntryKindText, marotte.EntryText{Text: "prompt"})
	f.closeTurn(agentTurn, marotte.TurnOutcomeCompleted)
	f.closeTurn(promptTurn, marotte.TurnOutcomeCompleted)

	wantShapes(t, mustRange(t, f, promptTurn),
		[]string{"0:/turn_open", "1:/tool_call", "2:/text", "3:/turn_close"}, "the prompt's turn")
	wantShapes(t, mustRange(t, f, agentTurn),
		[]string{"0:/turn_open", "1:/text", "2:/turn_close"}, "the agent's turn")

	rows := f.log.RailRows()
	if len(rows) != 2 || rows[0].N != 1 || rows[1].N != 2 {
		t.Fatalf("rail rows are %+v, want n 1 then 2", rows)
	}
	if !rows[1].AgentInitiated {
		t.Error("the wire_turn_start turn is not marked agent_initiated")
	}
}

// A torn tail is one partial line: it is dropped at the last complete newline, and
// the next append continues seq.
func TestEntryLog_ATornTailIsDroppedAndSeqContinues(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)
	whole, err := os.ReadFile(filepath.Join(f.root, entriesFileName))
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.root, entriesFileName),
		append(whole, []byte(`{"id":"torn","turn":"`)...), fileMode); err != nil {
		t.Fatalf("plant a torn tail: %v", err)
	}

	f.reopen()
	entries := mustRange(t, f, turn)
	wantShapes(t, entries, []string{"0:/turn_open", "1:/text", "2:/turn_close"}, "the surviving log")

	f.append(turn, "", "after", marotte.EntryKindSteer, marotte.EntrySteer{Text: "next"})
	if got := mustRange(t, f, turn); got[len(got)-1].Seq != 3 {
		t.Errorf("the append after a dropped tail took seq %d, want 3", got[len(got)-1].Seq)
	}
	if got := fileHead(t, filepath.Join(f.root, entriesFileName), len(whole)); got != string(whole) {
		t.Error("the surviving bytes are not the prefix that was there before the tail was planted")
	}
}

// The store-open closer: every open turn is closed with interrupted/unterminated —
// which IS the reconcile signal, so nothing is written on the header first — every
// unsettled call is aborted in its own lane, and the next append continues seq.
func TestEntryLog_StoreOpenCloserClosesEveryOrphanedTurn(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", Model: "opus"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	first := f.prompt("one")
	f.append(first, "d1", "call-1", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "call-1"})
	f.append(first, "", "call-2", marotte.EntryKindToolCall, marotte.EntryToolCall{ID: "call-2"})
	f.append(first, "", marotte.ToolResultID("call-2"), marotte.EntryKindToolResult,
		marotte.EntryToolResult{Status: marotte.ToolCompleted})
	second := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})

	f.reopen()

	// The synthesized closer IS the reconcile signal now, so the predicate reads it
	// off the log rather than off a header flag written beside it.
	if !f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is false after the closer synthesized an unterminated turn_close")
	}
	entries := mustRange(t, f, first)
	wantShapes(t, entries, []string{
		"0:/turn_open", "1:d1/tool_call", "2:/tool_call", "3:/tool_result",
		"4:d1/tool_result", "5:/turn_close",
	}, "the re-closed turn")
	var aborted marotte.EntryToolResult
	if err := json.Unmarshal(entries[4].Payload, &aborted); err != nil {
		t.Fatalf("parse the aborted result: %v", err)
	}
	if aborted.Status != marotte.ToolAborted {
		t.Errorf("the unsettled call settled as %q, want aborted", aborted.Status)
	}
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(entries[5].Payload, &footer); err != nil {
		t.Fatalf("parse the synthesized closer: %v", err)
	}
	switch {
	case footer.Outcome != marotte.TurnOutcomeInterrupted:
		t.Errorf("outcome is %q, want interrupted", footer.Outcome)
	case footer.StopReasonRaw != string(marotte.StopReasonUnterminated):
		t.Errorf("stop_reason_raw is %q, want unterminated", footer.StopReasonRaw)
	case footer.Model != "opus":
		t.Errorf("model is %q, want the header's", footer.Model)
	case footer.Credits != 0:
		t.Errorf("credits are %v, want absent: nothing metered them", footer.Credits)
	}
	if got := mustRange(t, f, second); len(got) != 2 || got[1].Kind != marotte.EntryKindTurnClose {
		t.Errorf("the second open turn is %v, want its own synthesized closer", shapes(got))
	}

	f.append(second, "", "later", marotte.EntryKindSteer, marotte.EntrySteer{Text: "after"})
	if got := mustRange(t, f, second); got[len(got)-1].Seq != 2 {
		t.Errorf("the append after the closer took seq %d, want 2", got[len(got)-1].Seq)
	}
}

// The header's counters are CACHES: a header left behind by a crash is recomputed
// from the log at open, and the log wins.
func TestEntryLog_HeaderCountersAreRecomputedOnOpen(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("one")
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)
	second := f.prompt("two")
	f.closeTurn(second, marotte.TurnOutcomeFailed)

	if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
		c.TurnCount = 1
		c.LastTurnOutcome = marotte.TurnOutcomeCompleted
		return true
	}); err != nil {
		t.Fatalf("plant a stale header: %v", err)
	}

	f.reopen()
	h := f.readHeader()
	if h.TurnCount != 2 || h.LastTurnOutcome != marotte.TurnOutcomeFailed {
		t.Errorf("header caches are (%d, %q) after open, want the log's (2, failed)",
			h.TurnCount, h.LastTurnOutcome)
	}
}

// The between-turns rule: a lane-less entry with no open turn joins the NEWEST turn
// after its turn_close, and on an empty log it opens turn_open{source: event}.
func TestEntryLog_BetweenTurnsAppend(t *testing.T) {
	ctx := t.Context()
	t.Run("an empty log opens a headerless event turn", func(t *testing.T) {
		f := newLogFixture(t)
		e := entryOf("", "", "switch-1", marotte.EntryKindModelSwitched,
			marotte.EntryModelSwitched{From: "a", To: "b"})
		if _, err := f.log.AppendBetweenTurns(ctx, e); err != nil {
			t.Fatalf("between-turns append: %v", err)
		}
		rows := f.log.RailRows()
		if len(rows) != 1 || !rows[0].AgentInitiated || rows[0].N != 1 {
			t.Fatalf("rail rows are %+v, want one agent-initiated row at n 1", rows)
		}
		entries := mustRange(t, f, e.Turn)
		wantShapes(t, entries, []string{"0:/turn_open", "1:/model_switched"}, "the event turn")
		var open marotte.EntryTurnOpen
		if err := json.Unmarshal(entries[0].Payload, &open); err != nil {
			t.Fatalf("parse turn_open: %v", err)
		}
		if open.Source != marotte.TurnOpenNameEvent {
			t.Errorf("source is %q, want event", open.Source)
		}
	})

	t.Run("a closed newest turn takes the entry after its close", func(t *testing.T) {
		f := newLogFixture(t)
		turn := f.prompt("one")
		f.closeTurn(turn, marotte.TurnOutcomeCompleted)
		e := entryOf("", "", "compaction-x", marotte.EntryKindCompaction,
			marotte.EntryCompaction{Summary: "s"})
		if _, err := f.log.AppendBetweenTurns(ctx, e); err != nil {
			t.Fatalf("between-turns append: %v", err)
		}
		if e.Turn != turn {
			t.Errorf("the entry landed in turn %q, want the newest turn %q", e.Turn, turn)
		}
		wantShapes(t, mustRange(t, f, turn),
			[]string{"0:/turn_open", "1:/turn_close", "2:/compaction"}, "the newest turn")
		if got, _ := f.log.Counters(); got != 1 {
			t.Errorf("turn_count is %d, want 1: a between-turns entry opens no turn", got)
		}
	})
}

// A window is a SET of turns read in file order, and a line of a turn outside the set
// inside that byte range is skipped.
func TestEntryLog_WindowPagesByTurn(t *testing.T) {
	f := newLogFixture(t)
	var turns []string
	for i := range 4 {
		turn := f.prompt(fmt.Sprintf("p%d", i))
		f.append(turn, "", fmt.Sprintf("say-%d", i), marotte.EntryKindText, marotte.EntryText{Text: "x"})
		f.closeTurn(turn, marotte.TurnOutcomeCompleted)
		turns = append(turns, turn)
	}

	newest, err := f.log.Window(2, "")
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if !newest.HasMore {
		t.Error("has_more is false with two older turns on disk")
	}
	if got := turnsOf(newest.Entries); strings.Join(got, ",") != turns[2]+","+turns[3] {
		t.Errorf("the newest page holds turns %v, want the last two", got)
	}

	older, err := f.log.Window(2, turns[2])
	if err != nil {
		t.Fatalf("window before: %v", err)
	}
	if older.HasMore {
		t.Error("has_more is true with nothing older than the first turn")
	}
	if got := turnsOf(older.Entries); strings.Join(got, ",") != turns[0]+","+turns[1] {
		t.Errorf("the ?before page holds turns %v, want the first two", got)
	}

	if _, err := f.log.Window(2, "t-nosuchturn"); err == nil {
		t.Error("a window before an unknown turn reported success")
	}
}

// The interleave a window's byte range can contain is SKIPPED, not returned.
func TestEntryLog_WindowSkipsAForeignTurnInsideItsRange(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	f.closeTurn(first, marotte.TurnOutcomeCompleted)
	second := f.prompt("two")
	third := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.append(second, "", "say-2", marotte.EntryKindText, marotte.EntryText{Text: "p"})
	f.closeTurn(third, marotte.TurnOutcomeCompleted)
	f.closeTurn(second, marotte.TurnOutcomeCompleted)

	window, err := f.log.Window(1, third)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	for _, id := range turnsOf(window.Entries) {
		if id != second {
			t.Fatalf("the window returned an entry of turn %q, want only %q", id, second)
		}
	}
	wantShapes(t, window.Entries,
		[]string{"0:/turn_open", "1:/text", "2:/turn_close"}, "the interleaved turn's window")
}

// The rail index holds a row for a DRAWN turn only, and a turn becomes drawn at the
// first append satisfying the predicate — so an undrawn turn keeps its n and the
// visible numbering skips one.
func TestEntryLog_RailRowsHoldOnlyDrawnTurns(t *testing.T) {
	f := newLogFixture(t)
	prompt := f.prompt("ask something\nsecond line")
	f.closeTurn(prompt, marotte.TurnOutcomeCompleted)

	bindOnly := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.append(bindOnly, "", "bind-1", marotte.EntryKindTurnBind, marotte.EntryTurnBind{KASMessageID: "k"})
	f.closeTurn(bindOnly, marotte.TurnOutcomeUnknown)

	delegateOnly := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.append(delegateOnly, "d1", "say-d", marotte.EntryKindText, marotte.EntryText{Text: "delegate"})
	f.closeTurn(delegateOnly, marotte.TurnOutcomeCompleted)

	invocation := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.append(invocation, "", "act-1", marotte.EntryKindToolCall,
		marotte.EntryToolCall{ID: "act-1", AgentSubtaskID: "d1"})
	f.closeTurn(invocation, marotte.TurnOutcomeCompleted)

	f.openTurn(&TurnSpec{
		Source: marotte.TurnOpenNameEmptyRetry,
		Prompt: &marotte.EntryPrompt{ID: "m-retry", Text: "ask something"},
	})

	rows := f.log.RailRows()
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%d:%s", r.N, r.Outcome))
	}
	// Turns 2 and 3 are numbered and never drawn: a turn_bind renders at no position
	// of its own, and a delegate lane's entries other than the invocation are dropped
	// by the transcript, so neither turn has a card for the rail to jump to.
	want := []string{"1:completed", "4:completed", "5:running"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rail rows are %v, want %v", got, want)
	}
	if rows[0].FirstLine != "ask something" {
		t.Errorf("first_line is %q, want the prompt's first line alone", rows[0].FirstLine)
	}
	if rows[2].AgentInitiated {
		t.Error("the empty_retry turn is marked agent_initiated, and it carries the prompt")
	}
	if count, _ := f.log.Counters(); count != 5 {
		t.Errorf("turn_count is %d, want 5: it counts turns drawn or not", count)
	}
}

// A `steer_ack` makes its turn DRAWN: an ack renders at its own position rather than
// folding into the steer note alone, so a turn holding nothing else still draws a card
// and needs the rail row its jump target reads.
func TestEntryLog_AnAckOnlyTurnIsDrawn(t *testing.T) {
	f := newLogFixture(t)
	turn := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.append(turn, "", "bind-1", marotte.EntryKindTurnBind, marotte.EntryTurnBind{KASMessageID: "k"})
	if rows := f.log.RailRows(); len(rows) != 0 {
		t.Fatalf("rail rows are %+v, want none yet: a turn_bind renders at no position of its own", rows)
	}

	f.append(turn, "", "steer-1:ack", marotte.EntryKindSteerAck,
		marotte.EntrySteerAck{SteerID: "steer-1", Text: "reading your note"})

	rows := f.log.RailRows()
	if len(rows) != 1 || rows[0].ID != turn {
		t.Fatalf("rail rows are %+v, want the one turn the ack drew", rows)
	}
	if !rows[0].AgentInitiated {
		t.Error("the ack-drawn turn is not agent_initiated, and it carries no prompt")
	}
}

// A between-turns entry can make the newest turn drawn, and it gains its rail row at
// that append.
func TestEntryLog_ABetweenTurnsEntryCanDrawTheNewestTurn(t *testing.T) {
	f := newLogFixture(t)
	turn := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)
	if len(f.log.RailRows()) != 0 {
		t.Fatalf("rail rows are %+v, want none for a turn of two lines", f.log.RailRows())
	}
	e := entryOf("", "", "compaction-y", marotte.EntryKindCompaction, marotte.EntryCompaction{Summary: "s"})
	if _, err := f.log.AppendBetweenTurns(t.Context(), e); err != nil {
		t.Fatalf("between-turns append: %v", err)
	}
	if rows := f.log.RailRows(); len(rows) != 1 || rows[0].ID != turn {
		t.Errorf("rail rows are %+v, want the turn the compaction drew", rows)
	}
}

// A write error refuses every further append for this log through the latch alone, and
// reuses no seq. Nothing is handed back, so the client never learns of the entry.
func TestEntryLog_AWriteErrorRefusesFurtherAppends(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})

	boom := errors.New("no space left on device")
	restore := syncEntries
	syncEntries = func(*os.File) error { return boom }
	t.Cleanup(func() { syncEntries = restore })

	failed := entryOf(turn, "", "say-2", marotte.EntryKindText, marotte.EntryText{Text: "two"})
	err := f.log.Append(t.Context(), failed)
	if !errors.Is(err, boom) || !errors.Is(err, errEntryLogFailed) {
		t.Fatalf("the failed append answered %v, want the wrapped write error", err)
	}
	syncEntries = restore

	later := entryOf(turn, "", "say-3", marotte.EntryKindText, marotte.EntryText{Text: "three"})
	if err := f.log.Append(t.Context(), later); !errors.Is(err, errEntryLogFailed) {
		t.Fatalf("a later append answered %v, want the latch", err)
	}
	if err := f.log.Append(t.Context(), entryOf(turn, "", "x", marotte.EntryKindTurnClose,
		marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})); !errors.Is(err, errEntryLogFailed) {
		t.Errorf("the turn's own closer answered %v, want the latch: the turn stays open on disk", err)
	}
}

// A line over the per-entry cap is refused before anything reaches disk, so the log
// stays exactly as it was and the refusal does NOT latch: a size refusal is about one
// entry, where a write error is about the device.
func TestEntryLog_ALineOverThePerEntryCapIsRefused(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	before := fileHead(t, filepath.Join(f.root, entriesFileName), 0)

	oversize := entryOf(turn, "", "say-huge", marotte.EntryKindText,
		marotte.EntryText{Text: strings.Repeat("x", maxEntryLineBytes+1)})
	if err := f.log.Append(t.Context(), oversize); !errors.Is(err, errEntryLineTooLong) {
		t.Fatalf("an oversize line answered %v, want the per-entry cap refusal", err)
	}
	if got := fileHead(t, filepath.Join(f.root, entriesFileName), 0); got != before {
		t.Error("the refused line changed the log")
	}
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "ordinary"})
	if got := mustRange(t, f, turn); got[len(got)-1].Seq != 1 {
		t.Errorf("the next append took seq %d, want 1: a size refusal reuses no seq and latches nothing",
			got[len(got)-1].Seq)
	}
}

// The whole-log cap refuses a write that would cross it, before anything reaches
// disk. The cap is the CALLER's to set: the log derives nothing, so a run root and a
// chat root are bounded by whatever their store passes.
func TestEntryLog_TheWholeLogCapRefusesBeforeTheWrite(t *testing.T) {
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "chats", "c-abcdef01")
	lg, err := OpenEntryLog(ctx, root, NoHeader(), WithEntryFileCap(400))
	if err != nil {
		t.Fatalf("open entry log: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })

	opened, err := lg.OpenTurn(ctx, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "ask"},
	})
	if err != nil {
		t.Fatalf("open turn: %v", err)
	}
	filler := entryOf(opened.Turn, "", "say-1", marotte.EntryKindText,
		marotte.EntryText{Text: strings.Repeat("y", 400)})
	if err := lg.Append(ctx, filler); !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Fatalf("a write past the cap answered %v, want ErrFileTooLarge", err)
	}
	entries, err := lg.TurnRange(opened.Turn, 0)
	if err != nil {
		t.Fatalf("read the turn: %v", err)
	}
	wantShapes(t, entries, []string{"0:/turn_open"}, "the capped log")
}

// Remove closes the descriptor and refuses every later append, so a turn still
// folding into a removed chat cannot re-create its log.
func TestEntryLog_RemoveRefusesLaterAppends(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	if err := f.log.Remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.log.Append(t.Context(), entryOf(turn, "", "late", marotte.EntryKindText,
		marotte.EntryText{Text: "x"})); !errors.Is(err, ErrTombstoned) {
		t.Errorf("an append after Remove answered %v, want ErrTombstoned", err)
	}
	if _, _, err := f.log.Revert(t.Context(), turn, marotte.TurnRevertCauseRewind, ""); !errors.Is(err, ErrTombstoned) {
		t.Errorf("a revert after Remove answered %v, want ErrTombstoned", err)
	}
}

// The repair read is TurnRange from an inclusive bound: only the entries at or above the
// seq the client asked from, which for the wire's `?after=1` is from 2.
func TestEntryLog_TurnRangeAnswersTheTailFromItsBound(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "one"})
	f.append(turn, "", "say-2", marotte.EntryKindText, marotte.EntryText{Text: "two"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	tail, err := f.log.TurnRange(turn, 2)
	if err != nil {
		t.Fatalf("turn range: %v", err)
	}
	wantShapes(t, tail, []string{"2:/text", "3:/turn_close"}, "the tail from seq 2")
	if _, err := f.log.TurnRange("t-nosuchturn", 0); err == nil {
		t.Error("a range read of an unknown turn reported success")
	}
}

// A turn the log holds nothing for is the ONE range-read failure a CALLER can cause, so
// it carries ErrTurnNotInLog and every other failure — a log that cannot be read or
// decoded — does not. Both routes over this read answer the sentinel 404 and anything
// else 500, so folding the two classes into one bare error makes the 500 arm and its
// Warn unreachable: a disk or decode fault then reaches the client as "that turn does
// not exist", which tells it to stop asking about a turn that is really there.
func TestEntryLog_AMissingTurnCarriesTheSentinel(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("hello")
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	if _, err := f.log.TurnRange("t-nosuchturn", 0); !errors.Is(err, ErrTurnNotInLog) {
		t.Errorf("TurnRange(unknown, 0) = %v, want ErrTurnNotInLog", err)
	}
	if _, err := f.log.TurnRange("t-nosuchturn", 3); !errors.Is(err, ErrTurnNotInLog) {
		t.Errorf("TurnRange(unknown, from 3) = %v, want ErrTurnNotInLog", err)
	}
	if _, _, err := f.log.TurnPage("t-nosuchturn", 0); !errors.Is(err, ErrTurnNotInLog) {
		t.Errorf("TurnPage(unknown) = %v, want ErrTurnNotInLog", err)
	}
	// The other direction, or a sentinel returned for every read would pass the two
	// above and say nothing.
	if _, err := f.log.TurnRange(turn, 0); err != nil {
		t.Errorf("TurnRange(%q) = %v, want the turn's entries and no error", turn, err)
	}
}

// A rewrite whose grouping is malformed is REFUSED with the log byte-unchanged. The
// rescan a rewrite ends with reads a first line naming a turn it does not hold as a
// torn tail, so committing before the check would truncate the whole log at offset 0,
// log it as an unreadable tail and answer success.
func TestEntryLog_ARewriteMissingATurnOpenIsRefused(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	turn := f.prompt("one")
	f.append(turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "p"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)
	merged, err := f.log.Window(10, "")
	if err != nil {
		t.Fatalf("read the whole log: %v", err)
	}
	path := filepath.Join(f.root, entriesFileName)
	before := fileHead(t, path, 0)
	// The counters are the only thing a rewrite writes on the header now, so they are
	// what says a refused rewrite wrote nothing there either.
	turnCount := f.readHeader().TurnCount

	malformed := slices.DeleteFunc(merged.Entries, func(e marotte.Entry) bool {
		return e.Kind == marotte.EntryKindTurnOpen
	})
	if err := f.log.Rewrite(t.Context(), malformed); err == nil {
		t.Fatal("a rewrite of a turn whose turn_open is missing reported success")
	}
	if got := fileHead(t, path, 0); got != before {
		t.Error("the refused rewrite changed the log")
	}
	if got := f.readHeader().TurnCount; got != turnCount {
		t.Errorf("turn_count is %d after a refused rewrite, want %d unmoved", got, turnCount)
	}
	wantShapes(t, mustRange(t, f, turn),
		[]string{"0:/turn_open", "1:/text", "2:/turn_close"}, "the log after a refused rewrite")

	f.append(turn, "", "say-2", marotte.EntryKindText, marotte.EntryText{Text: "after"})
	if got := mustRange(t, f, turn); got[len(got)-1].Seq != 3 {
		t.Errorf("the append after a refused rewrite took seq %d, want 3", got[len(got)-1].Seq)
	}
}

// A rewrite writes each turn contiguously in n order with seq renumbered from 0 and
// stamps nothing on the header but the counters: the evidence a merge answers is in the
// log, and the swap's own output is what answers it.
func TestEntryLog_RewriteMakesTurnsContiguous(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	first := f.prompt("one")
	second := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.append(first, "", "say-p", marotte.EntryKindText, marotte.EntryText{Text: "p"})
	f.append(second, "", "say-a", marotte.EntryKindText, marotte.EntryText{Text: "a"})
	f.closeTurn(second, marotte.TurnOutcomeCompleted)
	f.closeTurn(first, marotte.TurnOutcomeCompleted)

	merged, err := f.log.Window(10, "")
	if err != nil {
		t.Fatalf("read the whole log: %v", err)
	}
	if err := f.log.Rewrite(t.Context(), merged.Entries); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	after, err := f.log.Window(10, "")
	if err != nil {
		t.Fatalf("read the rewritten log: %v", err)
	}
	if got := turnsOf(after.Entries); strings.Join(got, ",") != first+","+second {
		t.Errorf("the rewritten log holds turn groups %v, want each contiguous in n order (%s then %s)",
			got, first, second)
	}
	wantShapes(t, mustRange(t, f, first),
		[]string{"0:/turn_open", "1:/text", "2:/turn_close"}, "the first turn after a rewrite")
	// The rewrite stamps no provenance: what it writes on the header is the counters,
	// re-cached from the merged log. A projection opened before it is gated by the
	// reverts the log holds, and this log holds none.
	if h := f.readHeader(); h.TurnCount != 2 {
		t.Errorf("turn_count is %d after the rewrite, want the 2 turns the merged log holds", h.TurnCount)
	}
	if id, held := f.log.NewestRevert(); held {
		t.Errorf("the rewritten log reports newest revert %q, want none", id)
	}
	if len(after.Entries) != len(merged.Entries) {
		t.Errorf("the rewrite holds %d entries, want the %d it was handed", len(after.Entries), len(merged.Entries))
	}
}

// A RUN root has no header, so none of the hooks is wired and nothing here
// reaches one: the log's rules about the log itself are both stores'.
func TestEntryLog_ARunRootNeedsNoHeader(t *testing.T) {
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "runs", "wf_1")
	lg, err := OpenEntryLog(ctx, root, NoHeader())
	if err != nil {
		t.Fatalf("open run log: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })

	step, err := lg.OpenTurn(ctx, &TurnSpec{
		Source: marotte.TurnOpenNameWorkflowStep, Run: "wf_1",
		NodePath: "build", SessionID: "sess_1",
	})
	if err != nil {
		t.Fatalf("open step turn: %v", err)
	}
	if err := lg.Append(ctx, entryOf(step.Turn, "", "say-1", marotte.EntryKindText,
		marotte.EntryText{Text: "building"})); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := OpenEntryLog(ctx, root, NoHeader())
	if err != nil {
		t.Fatalf("reopen run log: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	entries, err := reopened.TurnRange(step.Turn, 0)
	if err != nil {
		t.Fatalf("read the step turn: %v", err)
	}
	wantShapes(t, entries,
		[]string{"0:/turn_open", "1:/text", "2:/turn_close"}, "the reopened step turn")
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(entries[2].Payload, &footer); err != nil {
		t.Fatalf("parse the synthesized closer: %v", err)
	}
	if footer.Model != "" {
		t.Errorf("the closer stamped model %q, want none: a run root has no header to read", footer.Model)
	}
	if _, err := os.Stat(filepath.Join(root, headerFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat of chat.json answered %v, want it absent under a run root", err)
	}
}

func mustRange(t *testing.T, f *logFixture, turn string) []marotte.Entry {
	t.Helper()
	entries, err := f.log.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("turn range %q: %v", turn, err)
	}
	return entries
}

func turnsOf(entries []marotte.Entry) []string {
	var out []string
	var prev string
	for i := range entries {
		if entries[i].Turn != prev {
			out = append(out, entries[i].Turn)
			prev = entries[i].Turn
		}
	}
	return out
}

// fileHead is the file's first n bytes, or the whole file for n <= 0, so a
// truncation or a refusal can be checked against the bytes that were there before
// rather than against a length alone.
func fileHead(t *testing.T, path string, n int) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if n <= 0 {
		return string(data)
	}
	if len(data) < n {
		t.Fatalf("%s holds %d bytes, want at least the %d that preceded the tail", path, len(data), n)
	}
	return string(data[:n])
}
