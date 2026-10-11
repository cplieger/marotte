package chat

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

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

// The turn form clears only the named turn's signal.
func TestAppendReconciled_TurnFormClearsTheSignalOfTheTurnItNames(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.write(t.Context(), &marotte.Chat{ID: "c-abcdef01", Model: "opus"}); err != nil {
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

	record, minted, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Turn: first})
	if err != nil {
		t.Fatalf("append reconciled{turn}: %v", err)
	}
	if len(minted) != 0 {
		t.Errorf("the turn form minted turn %q, want none: it files into the turn it names", minted[0].Turn)
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

// On an empty log the session form mints a carrier and closes it at once: an open carrier would raise condition (ii).
func TestAppendReconciled_SessionFormMintsAndClosesItsOwnCarrier(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if turns, session := f.log.ReconcileTargets(); len(turns) != 0 || session != "sess-1" {
		t.Fatalf("targets = (%q, %q), want the session alone on an empty log", turns, session)
	}

	record, minted, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Session: "sess-1"})
	if err != nil {
		t.Fatalf("append reconciled{session}: %v", err)
	}
	if len(minted) != 2 || minted[1].Kind != marotte.EntryKindTurnClose {
		t.Fatalf("minted %+v, want a carrier's turn_open and turn_close: an empty log holds no turn the record could name",
			minted)
	}
	opened := minted[0]
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
	if count, _ := counters(f.log); count != 1 {
		t.Errorf("turn_count = %d, want 1: the carrier is the log's only turn", count)
	}
}

// With a surviving turn the session form joins it and mints nothing.
func TestAppendReconciled_SessionFormJoinsTheNewestSurvivingTurn(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	turn := f.prompt("one")
	f.closeTurn(turn, marotte.TurnOutcomeCompleted)

	record, minted, err := f.log.AppendReconciled(t.Context(), marotte.EntryReconciled{Session: "sess-1"})
	if err != nil {
		t.Fatalf("append reconciled{session}: %v", err)
	}
	if len(minted) != 0 {
		t.Errorf("a carrier was minted at %q while turn %q survives", minted[0].Turn, turn)
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

// Condition (i) reads the bind set. openPromptTurn appends the first line (internal/command/prompt.go) before
// OpenBridge loads and merges, so a file-existence condition would clear on that append and no resumed history would
// ever merge.
func TestNeedsReconcile_ConditionOneReadsTheBindSetRatherThanTheFile(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
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

	// turn_bind lands after dispatch, when KAS answers with the stored prompt id (internal/translate/user_message_id.go).
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

// An ordinary chat's own bind names its session, so it answers false.
func TestNeedsReconcile_AChatWhoseOwnBindNamesItsSessionIsQuiet(t *testing.T) {
	f := newLogFixture(t)
	if err := f.header.write(t.Context(), &marotte.Chat{ID: "c-abcdef01", ACPSessionID: "sess-1"}); err != nil {
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

// A rewind raises no reconcile signal, for j > 1 (record in a survivor) and j = 1 (a minted carrier). The carrier's
// close matters: an open one is synthesized unterminated at the next open, and every resume would rewrite the file.
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

			// The next process, where an unclosed carrier would cost.
			f.reopen()
			if turns, _ := f.log.ReconcileTargets(); len(turns) != 0 {
				t.Errorf("the next open synthesized a closer for %q, so the rewind left a "+
					"turn of its own open on disk", turns)
			}
		})
	}
}

// Exactly one field: both clears two signals from one read, neither clears nothing and still appends.
func TestAppendReconciled_RefusesUnlessExactlyOneFieldIsSet(t *testing.T) {
	f := newLogFixture(t)
	turn := f.prompt("one")

	for _, rec := range []marotte.EntryReconciled{
		{Turn: turn, Session: "sess-1"},
		{},
	} {
		record, minted, err := f.log.AppendReconciled(t.Context(), rec)
		if err == nil {
			t.Errorf("AppendReconciled(%+v) = %+v, want a refusal", rec, record)
		}
		if record != nil || len(minted) != 0 {
			t.Errorf("AppendReconciled(%+v) wrote record %+v and minted %+v, want neither", rec, record, minted)
		}
	}
	if got := reconciledOf(t, mustRange(t, f, turn)); len(got) != 0 {
		t.Errorf("the turn holds %+v, want no reconciled entry", got)
	}
}
