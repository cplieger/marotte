package chat

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// reconciledOf decodes the reconciled payloads of one turn, in seq order.
func reconciledOf(t *testing.T, entries []marotte.Entry) []marotte.EntryReconciled {
	t.Helper()
	var out []marotte.EntryReconciled
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindReconciled {
			continue
		}
		var rec marotte.EntryReconciled
		if err := json.Unmarshal(entries[i].Payload, &rec); err != nil {
			t.Fatalf("parse the reconciled of %q: %v", entries[i].ID, err)
		}
		out = append(out, rec)
	}
	return out
}

// The TURN form clears the signal of the turn it NAMES, and only that one: the record
// is the merge saying it looked at this turn and had nothing to add, so a record filed
// anywhere else would stop a signal about a turn nobody read.
func TestAppendReconciled_TurnFormClearsTheSignalOfTheTurnItNames(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", Model: "opus"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	first := f.prompt("one")
	second := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	f.reopen() // the store-open closer synthesizes an unterminated close on both

	turns, session := f.log.ReconcileTargets()
	if len(turns) != 2 || turns[0] != first || turns[1] != second {
		t.Fatalf("targets = %q, want both orphaned turns in file order", turns)
	}
	if session != "" {
		t.Errorf("session target = %q, want none: this header names no session", session)
	}

	record, opened, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Turn: first})
	if err != nil {
		t.Fatalf("append reconciled{turn}: %v", err)
	}
	if opened != nil {
		t.Errorf("the turn form opened turn %q, want none: it files into the turn it names", opened.Turn)
	}
	if record.Turn != first {
		t.Errorf("record.Turn = %q, want %q, the turn its payload names", record.Turn, first)
	}
	entries := mustRange(t, f, first)
	wantShapes(t, entries, []string{"0:/turn_open", "1:/turn_close", "2:/reconciled"},
		"the reconciled turn")
	if got := reconciledOf(t, entries); len(got) != 1 || got[0].Turn != first || got[0].Session != "" {
		t.Errorf("payloads = %+v, want one naming the turn alone", got)
	}
	if !f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is false with the second turn's closer still unanswered")
	}

	if _, _, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Turn: second}); err != nil {
		t.Fatalf("append reconciled{turn} for the second turn: %v", err)
	}
	if f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is still true once every orphaned turn carries its record")
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the scan is not reading the records back")
	}
}

// The SESSION form is condition (i)'s clearer, and on an EMPTY log behind a session it
// has no turn to live in: it mints one and CLOSES it in the same operation. The close
// is the whole point — an open carrier is synthesized `unterminated` at the next open,
// which is condition (ii), so the write that clears (i) would raise the signal again.
func TestAppendReconciled_SessionFormMintsAndClosesItsOwnCarrier(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if turns, session := f.log.ReconcileTargets(); len(turns) != 0 || session != "sess-1" {
		t.Fatalf("targets = (%q, %q), want the session alone on an empty log", turns, session)
	}

	record, opened, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Session: "sess-1"})
	if err != nil {
		t.Fatalf("append reconciled{session}: %v", err)
	}
	if opened == nil {
		t.Fatal("no carrier was opened, and an empty log holds no turn the record could name")
	}
	if record.Turn != opened.Turn {
		t.Errorf("record.Turn = %q, want the carrier %q", record.Turn, opened.Turn)
	}
	var open marotte.EntryTurnOpen
	if err := json.Unmarshal(opened.Payload, &open); err != nil {
		t.Fatalf("parse the carrier's turn_open: %v", err)
	}
	if open.N != 1 || open.Source != marotte.TurnOpenNameEvent {
		t.Errorf("carrier = n %d source %q, want n 1 source event (revert marks an INCOMPLETE carrier)",
			open.N, open.Source)
	}
	wantShapes(t, mustRange(t, f, opened.Turn),
		[]string{"0:/turn_open", "1:/turn_close", "2:/reconciled"}, "the minted carrier")
	if f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is true with the session's record on disk")
	}

	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log needs reconciling, so the carrier reached the store-open closer open")
	}
	if count, _ := f.log.Counters(); count != 1 {
		t.Errorf("turn_count = %d, want 1: the carrier is the log's only turn", count)
	}
}

// With a turn to live in, the session form takes the newest SURVIVING one and mints
// nothing: a carrier per adoption would leave one bodyless turn per resume.
func TestAppendReconciled_SessionFormJoinsTheNewestSurvivingTurn(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	turn := f.prompt("one")
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	record, opened, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Session: "sess-1"})
	if err != nil {
		t.Fatalf("append reconciled{session}: %v", err)
	}
	if opened != nil {
		t.Errorf("a carrier was minted at %q while turn %q survives", opened.Turn, turn)
	}
	if record.Turn != turn {
		t.Errorf("record.Turn = %q, want the surviving turn %q", record.Turn, turn)
	}
	if got := reconciledOf(t, mustRange(t, f, turn)); len(got) != 1 || got[0].Session != "sess-1" {
		t.Errorf("payloads = %+v, want one naming the session alone", got)
	}
	if f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is true with the session adopted")
	}
}

// Condition (i) is a read of the BIND SET, and the arm is generated in the order
// production produces it: `openPromptTurn` appends the log's first line
// (internal/command/prompt.go) BEFORE `OpenBridge` drives the session/load and the merge,
// so the predicate is always asked about a log that already exists and already holds a
// turn. A file-existence condition — what the design's earlier rounds specified — clears
// on exactly that first append, so it answers false for every resumed session and that
// chat's whole history is never merged, silently, for the life of its log.
func TestNeedsReconcile_ConditionOneReadsTheBindSetRatherThanTheFile(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	turn := f.prompt("first")
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	turns, session := f.log.ReconcileTargets()
	if session != "sess-1" || len(turns) != 0 {
		t.Fatalf("targets = (%q, %q), want the session alone: the log exists and its one "+
			"turn is closed, so (i) is the only live condition", turns, session)
	}
	if !f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is false for a chat bound to a session no turn_bind of " +
			"this log names, with the prompt's own turn already on disk")
	}

	// turn_bind is what adopts the session, and it lands AFTER the prompt is dispatched:
	// internal/translate/user_message_id.go writes it when KAS answers with the id it
	// stored the prompt under. That is the fact condition (i) reads.
	f.append(turn, "", turn+":bind", marotte.EntryKindTurnBind,
		marotte.EntryTurnBind{KASMessageID: "kas-1", SessionID: "sess-1"})
	if f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is still true once a turn_bind of this log names the header's session")
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the scan is not reading the bind set back")
	}
}

// The ORDINARY chat is the other half of the same read: its own prompt's bind names the
// session, so a log full of turns answers false and no resume rewrites it.
func TestNeedsReconcile_AChatWhoseOwnBindNamesItsSessionIsQuiet(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	for _, text := range []string{"one", "two"} {
		turn := f.prompt(text)
		f.append(turn, "", turn+":bind", marotte.EntryKindTurnBind,
			marotte.EntryTurnBind{KASMessageID: "kas-" + text, SessionID: "sess-1"})
		f.closeTurn(turn, marotte.TurnOutcomeCompleted)
	}
	if turns, session := f.log.ReconcileTargets(); len(turns) != 0 || session != "" {
		t.Errorf("targets = (%q, %q), want none on a chat that lost nothing", turns, session)
	}
	if f.log.NeedsReconcile() {
		t.Error("NeedsReconcile() is true on an ordinary chat, so every resume would rewrite the whole file")
	}
}

// A rewind loses nothing, so it raises no reconcile signal — for j > 1, where the record
// files into a survivor, and for j = 1, where §2.2 step 3 mints a carrier because no turn
// survives. The CLOSE of that carrier is the load-bearing half (design §9 item 33): an
// open turn is synthesized `turn_close{unterminated}` at the next open, which IS condition
// (ii), so a rewind that lost nothing would raise the signal one restart later and every
// later resume of that chat would rewrite its whole file. j = 1 is the arm that can see
// it, because it is the only one that mints a carrier at all.
func TestNeedsReconcile_ARewindRaisesNoSignal(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   int // the zero-based turn the rewind reverts
	}{
		{name: "j>1 files the record into a survivor", at: 1},
		{name: "j=1 mints a carrier and closes it", at: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLogFixture(t)
			var ids []string
			for _, name := range []string{"a", "b", "c"} {
				id := f.prompt(name)
				f.closeTurn(id, marotte.TurnOutcomeCompleted)
				ids = append(ids, id)
			}
			if f.log.NeedsReconcile() {
				t.Fatal("the fixture raises a signal before the rewind, so this arm would " +
					"pass for a reason that is not the rewind's")
			}

			if _, _, err := f.log.Revert(t.Context(), ids[tc.at], marotte.TurnRevertCauseRewind, ""); err != nil {
				t.Fatalf("Revert(): %v", err)
			}
			if turns, session := f.log.ReconcileTargets(); len(turns) != 0 || session != "" {
				t.Errorf("targets = (%q, %q) after the rewind, want none", turns, session)
			}

			// The next process, which is where an unclosed carrier costs: the store-open
			// closer runs over whatever the rewind left behind.
			f.reopen()
			if turns, _ := f.log.ReconcileTargets(); len(turns) != 0 {
				t.Errorf("the next open synthesized a closer for %q, so the rewind left a "+
					"turn of its own open on disk", turns)
			}
		})
	}
}

// Exactly one field, refused at the door: a record naming both clears two signals from
// one read of one thing, and a record naming neither clears none while still appending
// an entry every later scan has to classify.
func TestAppendReconciled_RefusesUnlessExactlyOneFieldIsSet(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("one")

	for _, rec := range []marotte.EntryReconciled{
		{Turn: turn, Session: "sess-1"},
		{},
	} {
		record, opened, err := f.log.AppendReconciled(t.Context(), rec)
		if err == nil {
			t.Errorf("AppendReconciled(%+v) = %+v, want a refusal", rec, record)
		}
		if record != nil || opened != nil {
			t.Errorf("AppendReconciled(%+v) wrote record %+v and opened %+v, want neither", rec, record, opened)
		}
	}
	if got := reconciledOf(t, mustRange(t, f, turn)); len(got) != 0 {
		t.Errorf("the turn holds %+v, want no reconciled entry", got)
	}
}
