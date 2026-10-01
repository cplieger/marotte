package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// Properties 7, 10 and 11 of the design's test strategy, against the REAL phase-2 store:
// the merge runs only on evidence of loss, a stale projection is discarded by
// provenance, and a rewrite preserves every id it was given.
//
// Scenarios rather than generated properties, because each one names an exact sequence
// of store operations (a rewind between the projection's open and its settle, an
// orphaned turn closed by the next process) rather than a space of inputs.

// swapFixture is one chat root: the entry log, the header beside it, and the readers a
// scenario asserts through.
type swapFixture struct {
	t      *testing.T
	log    *chat.EntryLog
	header chat.EntryHeader
	root   string
}

func newSwapFixture(t *testing.T) *swapFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "chats", "c-abcdef01")
	h := chat.NewEntryHeader(root)
	lg, err := chat.OpenEntryLog(t.Context(), root, h)
	if err != nil {
		t.Fatalf("open entry log: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	f := &swapFixture{t: t, log: lg, header: h, root: root}
	// The record beside the log, as CreateChatAndOpen writes it before any prompt: every
	// chat the UI opens is created first, so a root with a log and no chat.json is a
	// state the store does not produce.
	if _, err := h.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ID, c.Name, c.Model = "c-abcdef01", "a chat", "opus"
		return true
	}); err != nil {
		t.Fatalf("write the initial header: %v", err)
	}
	return f
}

// reopen closes and reopens the log, which is what runs the scan and the store-open
// closer again — the only way a test raises the reconcile signal NeedsReconcile()
// derives, without synthesizing the closer itself.
func (f *swapFixture) reopen() {
	f.t.Helper()
	if err := f.log.Close(); err != nil {
		f.t.Fatalf("close log: %v", err)
	}
	lg, err := chat.OpenEntryLog(f.t.Context(), f.root, f.header)
	if err != nil {
		f.t.Fatalf("reopen entry log: %v", err)
	}
	f.log = lg
}

// promptTurn opens a prompt turn and answers its id.
func (f *swapFixture) promptTurn(text string) string {
	f.t.Helper()
	e, err := f.log.OpenTurn(f.t.Context(), &chat.TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + text, Text: text},
	})
	if err != nil {
		f.t.Fatalf("open turn: %v", err)
	}
	return e.Turn
}

func (f *swapFixture) append(turn string, kind marotte.EntryKind, id string, payload any) {
	f.t.Helper()
	e := marotte.Entry{ID: id, Turn: turn, Kind: kind, Payload: mustJSON(f.t, payload)}
	if err := f.log.Append(f.t.Context(), &e); err != nil {
		f.t.Fatalf("append %s: %v", kind, err)
	}
}

// closeTurn appends the closer a live process would write.
func (f *swapFixture) closeTurn(turn string, outcome marotte.TurnOutcome, raw marotte.StopReason) {
	f.t.Helper()
	f.append(turn, marotte.EntryKindTurnClose, turn+":close-live",
		marotte.EntryTurnClose{Outcome: outcome, StopReasonRaw: string(raw), Model: "opus"})
}

// record is the log's own account, grouped the way the merge's spine needs it: the
// WHOLE file plus the reverted set, which is the read the production swap performs.
func (f *swapFixture) record() []RecordTurn {
	f.t.Helper()
	entries, reverted := f.everything()
	return RecordTurnsOf(entries, reverted)
}

// revert appends the record a rewind writes, which is what marks a turn's window
// reverted for every reader that asks for the surviving view.
func (f *swapFixture) revert(turn string) *marotte.Entry {
	f.t.Helper()
	record, _, err := f.log.Revert(f.t.Context(), turn, marotte.TurnRevertCauseRewind, "")
	if err != nil {
		f.t.Fatalf("revert %s: %v", turn, err)
	}
	return record
}

// everything is the merge's own read — the whole file plus the reverted set — beside
// `record`, which is that read grouped. A test asserting the rewrite preserved the
// reverted material reads it here rather than through the surviving view, which by
// definition cannot see what it is asserting about.
func (f *swapFixture) everything() ([]marotte.Entry, map[string]struct{}) {
	f.t.Helper()
	entries, reverted, err := f.log.AllWithReverted()
	if err != nil {
		f.t.Fatalf("AllWithReverted(): %v", err)
	}
	return entries, reverted
}

// entriesOfTurn is one turn's entries out of a flat log read, in the order the read
// held them.
func entriesOfTurn(entries []marotte.Entry, turn string) []marotte.Entry {
	var out []marotte.Entry
	for i := range entries {
		if entries[i].Turn == turn {
			out = append(out, entries[i])
		}
	}
	return out
}

// bytes is the log file as it stands, for a byte-identical assertion.
func (f *swapFixture) bytes() []byte {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "entries.jsonl"))
	if err != nil {
		f.t.Fatalf("read the log file: %v", err)
	}
	return data
}

// snapshot is the provenance a projection opening NOW would carry: the log's newest
// turn_revert, empty when it holds none. Read off the log rather than the header,
// because that is where the gate reads it from.
func (f *swapFixture) snapshot() string {
	f.t.Helper()
	id, _ := f.log.NewestRevert()
	return id
}

func (f *swapFixture) chat() *marotte.Chat {
	f.t.Helper()
	c, err := f.header.Read(f.t.Context())
	if err != nil {
		f.t.Fatalf("read the header: %v", err)
	}
	return c
}

// oneClosedTurn is the shape every scenario below starts from: one prompt turn holding
// one say, closed by a process.
func (f *swapFixture) oneClosedTurn(say string) string {
	f.t.Helper()
	turn := f.promptTurn("go")
	f.append(turn, marotte.EntryKindText, say, marotte.EntryText{Text: "half an answer"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted, "end_turn")
	return turn
}

// armReconcileSignal appends the empty-text steer entry §2.6 row 4 writes: the record
// of a steer KAS persisted that this process never received. It is one of the three
// conditions EntryLog.NeedsReconcile reads off the log, and it is what a test arms
// where the deleted header flag used to be marked by hand.
func (f *swapFixture) armReconcileSignal(turn string) {
	f.t.Helper()
	f.append(turn, marotte.EntryKindSteer, "steer-unread", marotte.EntrySteer{
		Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped,
	})
}

// differingReplay is a projection that pairs with the fixture's turn by rule two and
// carries a longer say plus a steer the record never held, so a merge that RUNS is
// unmistakable.
func differingReplay(t *testing.T, say string) []translate.ProjectedTurn {
	t.Helper()
	return []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow(say, "", "half an answer and the tail a crash lost"),
		steerRow("steer-lost", marotte.EntrySteer{Text: "unread", Origin: marotte.SteerOriginUser}),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}
}

// TestSwapMerged_WithNoEvidenceOfLossNothingIsWritten is property 11's first half: a
// normal resume builds the projection because KAS pushes the replay whatever marotte
// wants, and DISCARDS it unless the header says the record is missing something. Without
// that gate every resume would cost a whole-file rewrite.
func TestSwapMerged_WithNoEvidenceOfLossNothingIsWritten(t *testing.T) {
	f := newSwapFixture(t)
	f.oneClosedTurn("S")
	before, beforeChat := f.bytes(), f.chat()
	if f.log.NeedsReconcile() {
		t.Fatalf("the fixture's log already holds evidence of loss, so the gate under test is bypassed")
	}

	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: differingReplay(t, "S"), SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if changed {
		t.Errorf("changed = true, want false — the log holds no evidence of loss")
	}
	if got := f.bytes(); string(got) != string(before) {
		t.Errorf("the log moved:\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if after := f.chat(); after.TurnCount != beforeChat.TurnCount || after.LastTurnOutcome != beforeChat.LastTurnOutcome {
		t.Errorf("the header moved to %+v, want the counters at %d/%q unchanged",
			after, beforeChat.TurnCount, beforeChat.LastTurnOutcome)
	}
}

// TestSwapMerged_AnOrphanedTurnMakesTheMergeRun is property 11's second half. The
// signal is written by the STORE's own open closer rather than by the test, because the
// closer IS the signal: a turn left open by a process that died is closed
// `unterminated` by the next one, and the predicate reads that closer.
func TestSwapMerged_AnOrphanedTurnMakesTheMergeRun(t *testing.T) {
	f := newSwapFixture(t)
	f.oneClosedTurn("S0")
	orphan := f.promptTurn("and again")
	f.append(orphan, marotte.EntryKindText, "S1", marotte.EntryText{Text: "half an answer"})
	// No closer: the process died here. Reopening is the next process, which appends the
	// placeholder closer whose "unterminated" stop reason IS the signal.
	f.reopen()

	if !f.log.NeedsReconcile() {
		t.Fatalf("the store-open closer left no reconcile signal, so this scenario has nothing to test")
	}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S1", "", "half an answer and the tail a crash lost"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}

	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true — the replay holds the tail the record lost")
	}
	after := f.chat()
	// The signal self-clears in the swap's own output: the union rewrote the placeholder
	// closer into KAS's own account, so no surviving turn is closed "unterminated".
	if f.log.NeedsReconcile() {
		t.Errorf("the reconcile signal survived a completed swap that replaced the placeholder closer")
	}
	// The header's two caches are the LOG's, so they must agree with what the rewrite
	// wrote rather than with what the merge was handed.
	count, last := f.log.Counters()
	if uint64(after.TurnCount) != count {
		t.Errorf("header turn_count = %d, want the log's %d", after.TurnCount, count)
	}
	if after.LastTurnOutcome != last {
		t.Errorf("header last_turn_outcome = %q, want the log's %q", after.LastTurnOutcome, last)
	}
	if last != marotte.TurnOutcomeCancelled {
		t.Errorf("last_turn_outcome = %q, want KAS's own account of the crashed turn", last)
	}
	// The placeholder the store-open closer wrote keeps its POSITION and its id — those
	// are the record's — and loses its conclusion to KAS's own account of the turn.
	merged := f.record()
	closer := merged[len(merged)-1].Entries[len(merged[len(merged)-1].Entries)-1]
	if closer.Kind != marotte.EntryKindTurnClose {
		t.Fatalf("the crashed turn ends with %s, want its closer last:\n%s", closer.Kind, f.bytes())
	}
	if !strings.HasSuffix(closer.ID, ":unterminated") {
		t.Errorf("closer id = %q, want the store's own placeholder id kept", closer.ID)
	}
	var conclusion marotte.EntryTurnClose
	decodePayload(t, closer, &conclusion)
	if conclusion.StopReasonRaw == string(marotte.StopReasonUnterminated) {
		t.Errorf("stop_reason_raw is still the placeholder, want KAS's own account")
	}
}

// TestSwapMerged_AStaleProvenanceDiscardsTheProjection is property 10: a rewind between
// the projection's open and its settle APPENDS a turn_revert, so a projection built from
// the PRE-revert replay can never hand the reverted turns back. The gate is the record
// itself — the newest turn_revert's id, read off the log on both sides — rather than a
// header counter, so nothing has to be stamped for the refusal to work.
func TestSwapMerged_AStaleProvenanceDiscardsTheProjection(t *testing.T) {
	logs := captureLogs(t)
	f := newSwapFixture(t)
	first := f.oneClosedTurn("S0")
	second := f.promptTurn("and again")
	f.append(second, marotte.EntryKindText, "S1", marotte.EntryText{Text: "the turn the reader reverts"})
	f.closeTurn(second, marotte.TurnOutcomeCompleted, "end_turn")
	// The log's own signal leads, so the provenance gate is what the scenario reaches.
	f.armReconcileSignal(first)
	snapshot := f.snapshot()
	if snapshot != "" {
		t.Fatalf("the fixture's log already holds a revert (%q), so the gate cannot be shown to move", snapshot)
	}

	// The rewind: it records the revert and moves the provenance the gate reads.
	if _, _, err := f.log.Revert(t.Context(), second, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("revert: %v", err)
	}
	afterRewind := f.snapshot()
	if afterRewind == "" || afterRewind == snapshot {
		t.Fatalf("the provenance is %q after the rewind, want the appended revert's own id", afterRewind)
	}
	before := f.bytes()

	// The projection settles now, still holding the turn the rewind removed.
	projected := []translate.ProjectedTurn{
		projTurn(t, "P0", openRow("P0", 0, nil), textRow("S0", "", "half an answer"),
			closeRow("P0:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"})),
		projTurn(t, "P1", openRow("P1", 0, nil), textRow("S1", "", "the turn the reader reverts"),
			closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"})),
	}
	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if changed {
		t.Errorf("changed = true, want false — the projection is stale")
	}
	if got := f.bytes(); string(got) != string(before) {
		t.Errorf("the log moved:\nbefore:\n%s\nafter:\n%s", before, got)
	}
	if got := f.snapshot(); got != afterRewind {
		t.Errorf("the provenance is %q after the discard, want %q untouched", got, afterRewind)
	}
	if !f.log.NeedsReconcile() {
		t.Error("the discard cleared the log's reconcile signal, want it untouched")
	}
	warned := logs.String()
	if got := strings.Count(warned, "a revert landed while the replay was in flight"); got != 1 {
		t.Errorf("logged the discard %d times, want exactly one Warn:\n%s", got, warned)
	}
	// Both sides by NAME plus the newest revert's own id, so the line says which record
	// refused the projection rather than only that one did. Matched without the
	// key/value separator, because the handler's format is the caller's choice.
	if !strings.Contains(warned, "snapshot_revert") || !strings.Contains(warned, "newest_revert") ||
		!strings.Contains(warned, afterRewind) {
		t.Errorf("the Warn does not name both revert ids (want an empty snapshot and %q):\n%s", afterRewind, warned)
	}

	// A LATER load with a fresh snapshot merges as usual: the discard postpones the
	// reconciliation rather than abandoning it. KAS's replay is POST-revert now, so the
	// projection holds the surviving turn alone, carrying the tail the record lost; the
	// reverted turn stays out of the SURVIVING view, which is the sense in which it is
	// gone (RE-KEYED: the arm below counted the merge's own input, which is the whole
	// file now, so it was asserting the compaction decision 2 forbids).
	fresh := f.snapshot()
	postRevert := []translate.ProjectedTurn{
		projTurn(t, "P0", openRow("P0", 0, nil), textRow("S0", "", "half an answer and the tail a crash lost"),
			closeRow("P0:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"})),
	}
	changed, err = SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: postRevert, SessionID: "sid-1", Snapshot: fresh,
	})
	if err != nil {
		t.Fatalf("second SwapMerged: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false on the fresh snapshot, want true")
	}
	if got := f.snapshot(); got != fresh {
		t.Errorf("the provenance is %q after the merge, want %q — a rewrite appends no revert",
			got, fresh)
	}
	surviving, revertedAfter := f.everything()
	if _, marked := revertedAfter[strings.TrimSuffix(afterRewind, ":revert")]; !marked {
		t.Errorf("the reverted set after the merge is %v, want it to still hold the turn the "+
			"rewind cut:\n%s", revertedAfter, f.bytes())
	}
	kept := 0
	for i := range surviving {
		if _, hidden := revertedAfter[surviving[i].Turn]; !hidden {
			continue
		}
		kept++
	}
	if kept == 0 {
		t.Errorf("the rewritten file holds no entry of the reverted turn, so the rewrite "+
			"compacted the history a rewind is a record of:\n%s", f.bytes())
	}
	merged := f.record()
	var survivors []RecordTurn
	for i := range merged {
		if !merged[i].Reverted {
			survivors = append(survivors, merged[i])
		}
	}
	if len(survivors) != 1 {
		t.Fatalf("the merged log holds %d surviving turns, want one — the reverted turn must "+
			"stay out of the view every other reader asks for:\n%s", len(survivors), f.bytes())
	}
	var say marotte.EntryText
	decodePayload(t, entryIn(t, MergedTurn(survivors[0]), marotte.EntryKindText), &say)
	if say.Text != "half an answer and the tail a crash lost" {
		t.Errorf("merged say = %q, want the replay's longer text", say.Text)
	}
}

// TestSwapMerged_ARewritePreservesEveryIDAndTheTurnOrder is property 7: after a rewrite
// every id present before is present after, in the same relative order within its turn,
// with the turns contiguous in n order — and a SECOND load of the same replay with the
// flag set again reports no change and rewrites nothing but the flag.
func TestSwapMerged_ARewritePreservesEveryIDAndTheTurnOrder(t *testing.T) {
	f := newSwapFixture(t)
	turn := f.oneClosedTurn("S")
	f.armReconcileSignal(turn)
	before := f.record()
	beforeIDs := map[string][]string{}
	for i := range before {
		id := before[i].Entries[0].Turn
		for _, e := range before[i].Entries {
			beforeIDs[id] = append(beforeIDs[id], e.ID)
		}
	}

	projected := differingReplay(t, "S")
	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: before,
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	after := f.record()
	if len(after) != len(before) {
		t.Fatalf("the rewrite left %d turns, want %d:\n%s", len(after), len(before), f.bytes())
	}
	for i := range after {
		var got []string
		for _, e := range after[i].Entries {
			got = append(got, e.ID)
		}
		want := beforeIDs[after[i].Entries[0].Turn]
		if !isSubsequence(want, got) {
			t.Errorf("turn %s: ids %v do not hold %v in order", after[i].Entries[0].Turn, got, want)
		}
	}
	// Turns are written contiguously in n order, so the window read gives them back in
	// that order with the ordinals renumbered from 1.
	for i := range after {
		var open marotte.EntryTurnOpen
		decodePayload(t, after[i].Entries[0], &open)
		if open.N != uint64(i)+1 {
			t.Errorf("turn %d has n %d, want %d", i, open.N, i+1)
		}
	}
	if after[0].Entries[0].Turn != turn {
		t.Errorf("the paired turn is %q, want the record's own id %q", after[0].Entries[0].Turn, turn)
	}

	// RE-KEYED when the rewrite branch gained its own records: the first swap now CLEARS
	// the steer signal it could not settle, so the second load needs a signal of its own
	// to run on. Condition (i) is the cheapest one to arm without touching the turns this
	// test's first half asserted on — the header names a session no turn_bind and no
	// reconciled record names.
	if f.log.NeedsReconcile() {
		t.Fatalf("the rewrite left the signal armed, so it did not record what it could not fix")
	}
	if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ACPSessionID = "sid-1"
		return true
	}); err != nil {
		t.Fatalf("bind the header's session: %v", err)
	}
	if !f.log.NeedsReconcile() {
		t.Fatalf("the re-armed signal is absent, so the second load has nothing to test")
	}
	settled, settledChat := f.bytes(), f.chat()
	changed, err = SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("second SwapMerged: %v", err)
	}
	if changed {
		t.Errorf("changed = true on the second load, want false")
	}
	// The second load REWRITES nothing — every byte the first swap settled is still
	// there, in order — and it APPENDS the record that says it looked and had nothing to
	// add, which is what stops a third load asking the same question. Before that record
	// existed this branch wrote nothing at all, so the merge re-ran on every resume for
	// the life of the chat.
	if got := string(f.bytes()); !strings.HasPrefix(got, string(settled)) {
		t.Errorf("the second load rewrote the log:\nbefore:\n%s\nafter:\n%s", settled, got)
	}
	if got := swapReconciled(t, f, turn); len(got) != 2 ||
		got[0].Turn != turn || got[1].Session != "sid-1" {
		t.Errorf("records = %+v, want the rewrite's own then the second load's adoption", got)
	}
	if f.log.NeedsReconcile() {
		t.Error("the signal survived the second load, so a third one runs the same merge again")
	}
	if second := f.chat(); second.TurnCount != settledChat.TurnCount {
		t.Errorf("turn_count = %d, want %d — an unchanged swap re-caches nothing else",
			second.TurnCount, settledChat.TurnCount)
	}
}

// TestSwapMerged_AnInsertedCompactionMovesTheWatermark is rule 3's other half: the
// context bar counts up to the watermark, so a compaction only the replay holds has to
// move it or the reader's own bar never learns the summary happened.
func TestSwapMerged_AnInsertedCompactionMovesTheWatermark(t *testing.T) {
	f := newSwapFixture(t)
	f.armReconcileSignal(f.oneClosedTurn("S"))
	summary := "a sub-execution compacted into the parent's log"
	id := marotte.CompactionEntryID([]byte(summary), 1)
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "half an answer"),
		entryRow{
			kind: marotte.EntryKindCompaction, id: id,
			payload: marotte.EntryCompaction{Summary: summary},
		},
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	if _, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	}); err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if got := f.chat().CompactionWatermark; got != id {
		t.Errorf("watermark = %q, want the inserted compaction's id %q", got, id)
	}
}

// TestSwapMerged_AMergeThatAddedNothingRecordsThatItLooked is the other half of the
// reconcile loop: gate 1 reads the evidence off the log, so a merge that ran and found
// nothing to add leaves that evidence exactly where it was — and the next resume asks
// the same question, rebuilds the same projection and rewrites nothing, for the life of
// the chat. The records that say "this was looked at" are that swap's whole write.
func TestSwapMerged_AMergeThatAddedNothingRecordsThatItLooked(t *testing.T) {
	f := newSwapFixture(t)
	if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ACPSessionID = "sid-1"
		return true
	}); err != nil {
		t.Fatalf("bind the header's session: %v", err)
	}
	turn := f.oneClosedTurn("S")
	f.armReconcileSignal(turn)
	if !f.log.NeedsReconcile() {
		t.Fatal("the fixture holds no evidence of loss, so the branch under test is unreachable")
	}

	// No projected turns at all: KAS replayed nothing this record lacks, which is the
	// case §2.6 exists for.
	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: nil, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if changed {
		t.Errorf("changed = true, want false: the merge added no entry, and a reconciled record draws nothing")
	}
	if f.log.NeedsReconcile() {
		t.Error("the signal survived the swap, so every later resume re-runs a merge that changes nothing")
	}
	if got := swapReconciled(t, f, turn); len(got) != 2 ||
		got[0].Turn != turn || got[1].Session != "sid-1" {
		t.Errorf("records = %+v, want the turn's own then the session's adoption", got)
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the records are not read back by the scan")
	}
}

// TestSwapMerged_ARewriteRecordsWhatItCouldNotFix is the REWRITE branch's half of
// §2.6: a merge that changed the log still has to record the signals its own output
// left standing, in the SAME write. A rewrite that clears nothing leaves a chat whose
// every later resume re-runs a whole-file merge over an orphan KAS has no account of.
func TestSwapMerged_ARewriteRecordsWhatItCouldNotFix(t *testing.T) {
	f := newSwapFixture(t)
	if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ACPSessionID = "sid-1"
		return true
	}); err != nil {
		t.Fatalf("bind the header's session: %v", err)
	}
	// The orphan the store-open closer left: closed `unterminated`, and the replay
	// carries nothing that pairs with it, so the merge cannot settle it.
	orphan := f.promptTurn("go")
	f.append(orphan, marotte.EntryKindText, "S-orphan", marotte.EntryText{Text: "half an answer"})
	f.closeTurn(orphan, marotte.TurnOutcomeInterrupted, marotte.StopReasonUnterminated)
	// A second turn that lost nothing, so the two forms land in two different turns and
	// each assertion names one fact.
	newest := f.promptTurn("again")
	f.append(newest, marotte.EntryKindText, "S-newest", marotte.EntryText{Text: "a whole answer"})
	f.closeTurn(newest, marotte.TurnOutcomeCompleted, "end_turn")
	// A projected turn no rule pairs, so the merge INSERTS it and the swap rewrites.
	projected := []translate.ProjectedTurn{projTurn(t, "P2",
		openRow("P2", 0, nil),
		textRow("S-kas", "", "a turn the record never held"),
		closeRow("P2:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, so the rewrite branch is unreachable and this test measures nothing")
	}
	if f.log.NeedsReconcile() {
		t.Error("the rewrite cleared no signal, so every later resume re-merges this chat")
	}
	if got := swapReconciled(t, f, orphan); len(got) != 1 || got[0].Turn != orphan {
		t.Errorf("the orphan's records = %+v, want exactly one naming %q", got, orphan)
	}
	// The record files AFTER the turn's closer, which is where a reader of a reconciled
	// turn's body finds it: turn_open, …, turn_close, reconciled.
	entries, err := f.log.TurnRange(orphan, 0)
	if err != nil {
		t.Fatalf("turn range %q: %v", orphan, err)
	}
	if n := len(entries); n < 2 || entries[n-1].Kind != marotte.EntryKindReconciled ||
		entries[n-2].Kind != marotte.EntryKindTurnClose {
		t.Errorf("the orphan's body ends %s, want turn_close then reconciled",
			swapKinds(entries))
	}
	// The session form is lane-less, so it lands in the newest turn of the REWRITTEN
	// order — never in the turn whose own signal it says nothing about.
	if got := swapReconciled(t, f, newest); len(got) != 1 || got[0].Session != "sid-1" {
		t.Errorf("the newest turn's records = %+v, want one adopting session sid-1", got)
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the rewrite's records are not on disk")
	}

	// A LATER resume, raised by a NEW signal, files no SECOND record for the orphan.
	// insertReconciled's clearer set is read off the MERGED entries, which carry the
	// first swap's own record, so the orphan is skipped even though its unterminated
	// closer is still the evidence that raised it — an orphan no merge can ever settle
	// would otherwise collect one record per resume for the life of the chat.
	f.armReconcileSignal(newest)
	later := []translate.ProjectedTurn{projTurn(t, "P3",
		openRow("P3", 0, nil),
		textRow("S-kas-2", "", "a second turn the record never held"),
		closeRow("P3:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}
	changed, err = SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: later, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("the second SwapMerged: %v", err)
	}
	if !changed {
		t.Fatal("the second swap changed nothing, so it took the no-rewrite branch and " +
			"insertReconciled never ran: this arm measures the guard, not the clearers")
	}
	if got := swapReconciled(t, f, orphan); len(got) != 1 || got[0].Turn != orphan {
		t.Errorf("after a second resume the orphan holds %d records %+v, want exactly one "+
			"naming %q: the merge re-files a record for a turn an earlier swap already "+
			"answered", len(got), got, orphan)
	}
	// The new signal IS answered, so the second swap did the work the guard let through.
	if got := swapReconciled(t, f, newest); len(got) != 2 {
		t.Errorf("the newest turn's records = %+v, want two (the first swap's session form "+
			"and this swap's answer to the steer it could not fill)", got)
	}
}

// TestSwapMerged_ARewriteRecordsNothingForASignalItSETTLED is why the clearers are
// computed over the MERGED entries rather than over the predicate the gate read: the
// union rewrote this turn's placeholder closer into KAS's own account, so the signal is
// gone and a record saying "looked, nothing to add" would state the opposite of what
// happened — and it would claim a turn the merge DID add to.
func TestSwapMerged_ARewriteRecordsNothingForASignalItSETTLED(t *testing.T) {
	f := newSwapFixture(t)
	turn := f.promptTurn("go")
	f.append(turn, marotte.EntryKindText, "S", marotte.EntryText{Text: "half an answer"})
	f.closeTurn(turn, marotte.TurnOutcomeInterrupted, marotte.StopReasonUnterminated)
	before, _ := f.log.ReconcileTargets()
	if len(before) != 1 || before[0] != turn {
		t.Fatalf("pre-merge targets = %v, want the placeholder-closed turn %q", before, turn)
	}
	// Pairs with the record turn by rule two (the say id), and carries KAS's own closer.
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "half an answer and the tail a crash lost"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}

	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, so the union did not run and this test measures nothing")
	}
	if f.log.NeedsReconcile() {
		t.Error("the signal survived a merge that replaced the placeholder closer")
	}
	if got := swapReconciled(t, f, turn); len(got) != 0 {
		t.Errorf("records = %+v, want none: the merge SETTLED this turn's signal", got)
	}
}

// TestSwapMerged_AResumedSessionTheRecordNeverAdoptedMakesTheMergeRun is property 11's
// condition-(i) arm on the SWAP side, and its whole point is the ORDER the fixture is
// built in. `resume_session` binds the chat to a KAS session it copied no messages from,
// then the first prompt appends the log's own first line (`openPromptTurn`) BEFORE
// `OpenBridge` drives the session/load and this swap — so the gate is asked about a log
// that exists and holds a closed turn, and the only evidence of loss is the session the
// record has never adopted. A file-existence condition clears on that first append, so
// the merge never runs and the resumed history is silently lost; the chat-side half of
// this arm is TestNeedsReconcile_ConditionOneReadsTheBindSetRatherThanTheFile.
func TestSwapMerged_AResumedSessionTheRecordNeverAdoptedMakesTheMergeRun(t *testing.T) {
	f := newSwapFixture(t)
	if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
		c.RecordSession("sess-1")
		return true
	}); err != nil {
		t.Fatalf("bind the chat to the session it was resumed onto: %v", err)
	}
	turn := f.promptTurn("first")
	f.closeTurn(turn, marotte.TurnOutcomeCompleted, "end_turn")

	// The fixture's own honesty check, and it asks about the TURNS alone: no turn here
	// carries a signal, so whatever the gate reads it reads from condition (i) — and the
	// session half stays unasserted on purpose, because `changed` below is what observes
	// it and a guard on the predicate would fire before the consequence could.
	if turns, _ := f.log.ReconcileTargets(); len(turns) != 0 {
		t.Fatalf("pre-merge turn targets = %q, want none: a turn signal would make this "+
			"scenario pass for a reason that is not (i)", turns)
	}
	// The history KAS holds: one turn no pairing rule can reach, so the merge INSERTS it
	// and the swap rewrites.
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S-kas", "", "the history the resumed session holds"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sess-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true: the swap read no evidence of loss, so a "+
			"resumed session's whole history never merges into this log:\n%s", f.bytes())
	}
	if got := textsOf(t, f.everythingEntries()); !strings.Contains(strings.Join(got, "|"), "the history the resumed session holds") {
		t.Errorf("the merged log holds texts %q, want the replayed history", got)
	}
	// The session form is the clearer, so a chat that just adopted its session does not
	// re-merge on the next resume.
	if f.log.NeedsReconcile() {
		t.Error("the signal survived the swap, so every later resume rewrites this whole file again")
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the rewrite's own record is not on disk")
	}
}

// everythingEntries is the merge's read with the reverted set dropped, for an assertion
// whose subject is the entries alone.
func (f *swapFixture) everythingEntries() []marotte.Entry {
	f.t.Helper()
	entries, _ := f.everything()
	return entries
}

// swapKinds names a turn's body in order, for a failure that has to show placement.
func swapKinds(entries []marotte.Entry) string {
	kinds := make([]string, 0, len(entries))
	for i := range entries {
		kinds = append(kinds, string(entries[i].Kind))
	}
	return strings.Join(kinds, ", ")
}

// swapReconciled decodes the reconciled payloads of one turn, in seq order.
func swapReconciled(t *testing.T, f *swapFixture, turn string) []marotte.EntryReconciled {
	t.Helper()
	entries, err := f.log.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("turn range %q: %v", turn, err)
	}
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

// TestSwapMerged_ARewriteEmitsEveryRevertedEntryUnchanged is property 10's other half:
// a rewind is a RECORD, so the rewrite a merge ends with carries the reverted material
// through untouched. The merge is therefore fed the whole file plus the reverted set,
// never the surviving view — that input makes Rewrite a physical compaction, the
// reverted turns and the turn_revert that hides them leave the file, and no reader can
// undo it. Decision 2 places compaction out of scope.
//
// It also pins the two halves that make the preservation observable: the reverted turn
// keeps the n it had, because under §2.3 an n is not a coordinate any reader reads and
// renumbering over every turn would renumber a hidden one into a surviving one's slot;
// and the surviving view is the same across a reopen, so the skip rule survives the
// rescan the rewrite ends with.
func TestSwapMerged_ARewriteEmitsEveryRevertedEntryUnchanged(t *testing.T) {
	f := newSwapFixture(t)
	survivor := f.oneClosedTurn("S0")
	// TWO turns in the rewind's window, so the surviving count at the hidden turns'
	// position and their own stored n disagree: with one hidden turn the two happen to
	// coincide and a renumber that ignored the flag would be unobservable.
	hiddenA := f.promptTurn("and again")
	f.append(hiddenA, marotte.EntryKindText, "S1", marotte.EntryText{Text: "a turn the rewind took"})
	f.closeTurn(hiddenA, marotte.TurnOutcomeCompleted, "end_turn")
	hiddenB := f.promptTurn("once more")
	f.append(hiddenB, marotte.EntryKindText, "S2", marotte.EntryText{Text: "and the one after it"})
	f.closeTurn(hiddenB, marotte.TurnOutcomeCompleted, "end_turn")
	f.revert(hiddenA) // the window is stated through the newest turn, so it takes both
	f.armReconcileSignal(survivor)

	before, revertedBefore := f.everything()
	hiddenTurns := []string{hiddenA, hiddenB}
	for _, hidden := range hiddenTurns {
		if _, marked := revertedBefore[hidden]; !marked {
			t.Fatalf("the fixture's reverted set is %v, want it to hold %q: nothing below "+
				"asserts anything about material the log does not consider reverted",
				revertedBefore, hidden)
		}
		if len(entriesOfTurn(before, hidden)) == 0 {
			t.Fatalf("the whole-file read holds no entry of the reverted turn %q:\n%s", hidden, f.bytes())
		}
		if got := entriesOfTurn(surviving(t, f), hidden); len(got) != 0 {
			t.Fatalf("the surviving view already holds %d entries of the reverted turn %q, so a "+
				"rewrite that dropped them would not be visible here", len(got), hidden)
		}
	}

	// Two projected turns: one pairs the survivor by rule two and carries a longer say, so
	// the merge RUNS; the second is keyed on the HIDDEN turn's say and would pair with it
	// if a reverted turn were a pairing candidate, which is what the zero keys forbid. A
	// real replay cannot carry reverted content (KAS pops its own log through the
	// tombstone), so this second turn is the defensive half — what it pins is that the
	// record's hidden turn is not UNIONED, never what becomes of the surplus.
	projected := append(differingReplay(t, "S0"),
		projTurn(t, "P2", openRow("P2", 0, nil),
			textRow("S1", "", "a turn the rewind took, with a tail the record lacks"),
			closeRow("P2:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"})))

	changed, err := SwapMerged(t.Context(), &Swap{
		Log: f.log, Header: f.header, Record: f.record(),
		Projected: projected, SessionID: "sid-1", Snapshot: f.snapshot(),
	})
	if err != nil {
		t.Fatalf("SwapMerged: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true: without a rewrite this test asserts nothing "+
			"about what a rewrite preserves\n%s", f.bytes())
	}

	after, revertedAfter := f.everything()
	for _, hidden := range hiddenTurns {
		if _, marked := revertedAfter[hidden]; !marked {
			t.Errorf("the reverted set after the rewrite is %v, want it to still hold %q: the "+
				"rewritten log no longer hides the window the rewind cut\n%s",
				revertedAfter, hidden, f.bytes())
		}
		hiddenBefore, hiddenAfter := entriesOfTurn(before, hidden), entriesOfTurn(after, hidden)
		if len(hiddenAfter) != len(hiddenBefore) {
			t.Fatalf("the reverted turn %q holds %d entries after the rewrite, want its %d "+
				"unchanged — the merge was fed the surviving view, so the rewrite compacted "+
				"the history away\n%s", hidden, len(hiddenAfter), len(hiddenBefore), f.bytes())
		}
		for i := range hiddenBefore {
			got, want := hiddenAfter[i], hiddenBefore[i]
			if got.ID != want.ID || got.Kind != want.Kind || got.Seq != want.Seq ||
				!bytes.Equal(got.Payload, want.Payload) {
				t.Errorf("the reverted turn %q's entry %d is %s/%s/seq %d after the rewrite, want "+
					"%s/%s/seq %d with its payload unchanged\ngot  %s\nwant %s",
					hidden, i, got.ID, got.Kind, got.Seq, want.ID, want.Kind, want.Seq, got.Payload, want.Payload)
			}
		}
		if n, ok := ordinalOfTurn(t, after, hidden); !ok || n != mustOrdinal(t, before, hidden) {
			t.Errorf("the reverted turn %q's n is %d after the rewrite, want the %d it had: an n "+
				"renumbered over every turn puts a hidden turn in a surviving one's slot",
				hidden, n, mustOrdinal(t, before, hidden))
		}
	}
	if !holdsKind(after, marotte.EntryKindTurnRevert) {
		t.Errorf("the rewritten log holds no turn_revert, so nothing hides the reverted "+
			"window on the next open\n%s", f.bytes())
	}

	// The surviving view is what every other reader asks for, and it must answer the same
	// across the rescan the rewrite ends with.
	survived := surviving(t, f)
	f.reopen()
	reopened := surviving(t, f)
	if len(reopened) != len(survived) {
		t.Fatalf("the surviving view holds %d entries after a reopen, want the %d the "+
			"rewrite left\n%s", len(reopened), len(survived), f.bytes())
	}
	for i := range survived {
		if reopened[i].ID != survived[i].ID {
			t.Errorf("the surviving view's entry %d is %q after a reopen, want %q",
				i, reopened[i].ID, survived[i].ID)
		}
	}
}

// surviving is the view every reader but the merge asks for.
func surviving(t *testing.T, f *swapFixture) []marotte.Entry {
	t.Helper()
	entries, err := f.log.All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	return entries
}

// ordinalOfTurn reads a turn's stored n out of a flat log read.
func ordinalOfTurn(t *testing.T, entries []marotte.Entry, turn string) (uint64, bool) {
	t.Helper()
	open, ok := turnOpenOf(entriesOfTurn(entries, turn))
	return open.N, ok
}

func mustOrdinal(t *testing.T, entries []marotte.Entry, turn string) uint64 {
	t.Helper()
	n, ok := ordinalOfTurn(t, entries, turn)
	if !ok {
		t.Fatalf("the read holds no turn_open for %q", turn)
	}
	return n
}
