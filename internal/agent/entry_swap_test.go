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

// Properties 7, 10 and 11 against the real store: the merge runs only on evidence of loss, a
// stale projection is discarded by provenance, and a rewrite keeps every id. Scenarios,
// since each is an exact operation sequence.

// swapFixture is one chat root: the entry log, its header and the readers a scenario asserts through.
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
	// The record CreateChatAndOpen writes before any prompt.
	if _, err := h.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ID, c.Name, c.Model = "c-abcdef01", "a chat", "opus"
		return true
	}); err != nil {
		t.Fatalf("write the initial header: %v", err)
	}
	return f
}

// reopen reruns the scan and the store-open closer, the only honest way to raise the reconcile signal.
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

// record is the log's account as the merge spine: the whole file plus the reverted set.
func (f *swapFixture) record() []RecordTurn {
	f.t.Helper()
	entries, reverted := f.everything()
	return RecordTurnsOf(entries, reverted)
}

// revert appends the record a rewind writes.
func (f *swapFixture) revert(turn string) *marotte.Entry {
	f.t.Helper()
	record, _, err := f.log.Revert(f.t.Context(), turn, marotte.TurnRevertCauseRewind, "")
	if err != nil {
		f.t.Fatalf("revert %s: %v", turn, err)
	}
	return record
}

// everything is the merge's own read: the whole file plus the reverted set, which the surviving view cannot show.
func (f *swapFixture) everything() ([]marotte.Entry, map[string]struct{}) {
	f.t.Helper()
	entries, reverted, err := f.log.AllWithReverted()
	if err != nil {
		f.t.Fatalf("AllWithReverted(): %v", err)
	}
	return entries, reverted
}

// entriesOfTurn is one turn's entries from a flat read, in read order.
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

// snapshot is the provenance a projection opening now would carry, read off the log as the gate reads it.
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

// oneClosedTurn is one prompt turn holding one say, closed by a process.
func (f *swapFixture) oneClosedTurn(say string) string {
	f.t.Helper()
	turn := f.promptTurn("go")
	f.append(turn, marotte.EntryKindText, say, marotte.EntryText{Text: "half an answer"})
	f.closeTurn(turn, marotte.TurnOutcomeCompleted, "end_turn")
	return turn
}

// armReconcileSignal appends the empty-text steer entry a lost read leaves, one of the three
// conditions EntryLog.NeedsReconcile reads.
func (f *swapFixture) armReconcileSignal(turn string) {
	f.t.Helper()
	f.append(turn, marotte.EntryKindSteer, "steer-unread", marotte.EntrySteer{
		Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped,
	})
}

// differingReplay pairs by rule two and carries a longer say plus an unseen steer, so a merge that runs is unmistakable.
func differingReplay(t *testing.T, say string) []translate.ProjectedTurn {
	t.Helper()
	return []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow(say, "", "half an answer and the tail a crash lost"),
		steerRow("steer-lost", marotte.EntrySteer{Text: "unread", Origin: marotte.SteerOriginUser}),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}
}

// TestSwapMerged_WithNoEvidenceOfLossNothingIsWritten pins that a normal
// resume discards the projection.
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

// TestSwapMerged_AnOrphanedTurnMakesTheMergeRun pins the merge running on loss; the store's own
// open closer raises the signal.
func TestSwapMerged_AnOrphanedTurnMakesTheMergeRun(t *testing.T) {
	f := newSwapFixture(t)
	f.oneClosedTurn("S0")
	orphan := f.promptTurn("and again")
	f.append(orphan, marotte.EntryKindText, "S1", marotte.EntryText{Text: "half an answer"})
	// No closer: the process died. Reopening appends the `unterminated` placeholder, the signal.
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
	// The union rewrote the placeholder, so the signal clears itself.
	if f.log.NeedsReconcile() {
		t.Errorf("the reconcile signal survived a completed swap that replaced the placeholder closer")
	}
	// The header caches are the log's and must match what the rewrite wrote.
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
	// The placeholder keeps its position and id and loses its conclusion to KAS's account.
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

// TestSwapMerged_AStaleProvenanceDiscardsTheProjection pins the provenance gate: a rewind between the
// projection's open and settle appends a turn_revert, and the gate reads that id off the log.
func TestSwapMerged_AStaleProvenanceDiscardsTheProjection(t *testing.T) {
	logs := captureLogs(t)
	f := newSwapFixture(t)
	first := f.oneClosedTurn("S0")
	second := f.promptTurn("and again")
	f.append(second, marotte.EntryKindText, "S1", marotte.EntryText{Text: "the turn the reader reverts"})
	f.closeTurn(second, marotte.TurnOutcomeCompleted, "end_turn")
	// The log's signal leads, so the provenance gate is reached.
	f.armReconcileSignal(first)
	snapshot := f.snapshot()
	if snapshot != "" {
		t.Fatalf("the fixture's log already holds a revert (%q), so the gate cannot be shown to move", snapshot)
	}

	// The rewind moves the provenance the gate reads.
	if _, _, err := f.log.Revert(t.Context(), second, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("revert: %v", err)
	}
	afterRewind := f.snapshot()
	if afterRewind == "" || afterRewind == snapshot {
		t.Fatalf("the provenance is %q after the rewind, want the appended revert's own id", afterRewind)
	}
	before := f.bytes()

	// The projection still holds the turn the rewind removed.
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
	// Both sides by name plus the newest revert id, without the key/value separator.
	if !strings.Contains(warned, "snapshot_revert") || !strings.Contains(warned, "newest_revert") ||
		!strings.Contains(warned, afterRewind) {
		t.Errorf("the Warn does not name both revert ids (want an empty snapshot and %q):\n%s", afterRewind, warned)
	}

	// A later load with a fresh snapshot merges as usual: the discard postpones reconciliation.
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

// TestSwapMerged_ARewritePreservesEveryIDAndTheTurnOrder pins every id and the turn order, and a second load of
// the same replay changes nothing.
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
	// Turns are written contiguously in n order, renumbered from 1.
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

	// The first swap cleared the steer signal, so the second load arms condition (i): a session no bind names.
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
	// The second load rewrites nothing and appends the record that stops a third load asking.
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

// TestSwapMerged_AnInsertedCompactionMovesTheWatermark is rule 3: a replay-only compaction moves the context bar's watermark.
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

// TestSwapMerged_AMergeThatAddedNothingRecordsThatItLooked pins the "looked" records as the
// whole write, or every resume repeats the merge.
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

	// No projected turns: the case reconciliation exists for.
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

// TestSwapMerged_ARewriteRecordsWhatItCouldNotFix pins the rewrite branch recording, in the same
// write, the signals its output left standing.
func TestSwapMerged_ARewriteRecordsWhatItCouldNotFix(t *testing.T) {
	f := newSwapFixture(t)
	if _, err := f.header.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ACPSessionID = "sid-1"
		return true
	}); err != nil {
		t.Fatalf("bind the header's session: %v", err)
	}
	// The orphan the store-open closer left; nothing in the replay pairs with it.
	orphan := f.promptTurn("go")
	f.append(orphan, marotte.EntryKindText, "S-orphan", marotte.EntryText{Text: "half an answer"})
	f.closeTurn(orphan, marotte.TurnOutcomeInterrupted, marotte.StopReasonUnterminated)
	// A second, lossless turn, so the two forms land in different turns.
	newest := f.promptTurn("again")
	f.append(newest, marotte.EntryKindText, "S-newest", marotte.EntryText{Text: "a whole answer"})
	f.closeTurn(newest, marotte.TurnOutcomeCompleted, "end_turn")
	// A projected turn no rule pairs, so the swap rewrites.
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
	// The record files after the turn's closer.
	entries, err := f.log.TurnRange(orphan, 0)
	if err != nil {
		t.Fatalf("turn range %q: %v", orphan, err)
	}
	if n := len(entries); n < 2 || entries[n-1].Kind != marotte.EntryKindReconciled ||
		entries[n-2].Kind != marotte.EntryKindTurnClose {
		t.Errorf("the orphan's body ends %s, want turn_close then reconciled",
			swapKinds(entries))
	}
	// The lane-less session form lands in the newest turn of the rewritten order.
	if got := swapReconciled(t, f, newest); len(got) != 1 || got[0].Session != "sid-1" {
		t.Errorf("the newest turn's records = %+v, want one adopting session sid-1", got)
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the rewrite's records are not on disk")
	}

	// A later resume files no second record for the orphan: clearers read the merged entries,
	// which carry the first record.
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
	// The new signal is answered.
	if got := swapReconciled(t, f, newest); len(got) != 2 {
		t.Errorf("the newest turn's records = %+v, want two (the first swap's session form "+
			"and this swap's answer to the steer it could not fill)", got)
	}
}

// TestSwapMerged_ARewriteRecordsNothingForASignalItSETTLED pins that a signal the union settled gets no record.
func TestSwapMerged_ARewriteRecordsNothingForASignalItSETTLED(t *testing.T) {
	f := newSwapFixture(t)
	turn := f.promptTurn("go")
	f.append(turn, marotte.EntryKindText, "S", marotte.EntryText{Text: "half an answer"})
	f.closeTurn(turn, marotte.TurnOutcomeInterrupted, marotte.StopReasonUnterminated)
	before, _ := f.log.ReconcileTargets()
	if len(before) != 1 || before[0] != turn {
		t.Fatalf("pre-merge targets = %v, want the placeholder-closed turn %q", before, turn)
	}
	// Pairs by rule two and carries KAS's closer.
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

// TestSwapMerged_AResumedSessionTheRecordNeverAdoptedMakesTheMergeRun pins condition (i) in
// fixture order: the first prompt's append precedes the session/load, so only the unadopted
// session is evidence. Chat side: TestNeedsReconcile_ConditionOneReadsTheBindSetRatherThanTheFile.
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

	// No turn carries a signal, so the gate reads condition (i); `changed` observes it.
	if turns, _ := f.log.ReconcileTargets(); len(turns) != 0 {
		t.Fatalf("pre-merge turn targets = %q, want none: a turn signal would make this "+
			"scenario pass for a reason that is not (i)", turns)
	}
	// One turn no pairing rule reaches, so the swap rewrites.
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
	// The session form clears it, so the next resume does not re-merge.
	if f.log.NeedsReconcile() {
		t.Error("the signal survived the swap, so every later resume rewrites this whole file again")
	}
	f.reopen()
	if f.log.NeedsReconcile() {
		t.Error("the reopened log re-raises the signal, so the rewrite's own record is not on disk")
	}
}

// everythingEntries is the merge's read without the reverted set.
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

// swapReconciled decodes one turn's reconciled payloads, in seq order.
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

// TestSwapMerged_ARewriteEmitsEveryRevertedEntryUnchanged pins that the
// rewrite carries reverted material untouched (compaction is out of scope). A reverted turn
// keeps its n, and the surviving view is unchanged across a reopen.
func TestSwapMerged_ARewriteEmitsEveryRevertedEntryUnchanged(t *testing.T) {
	f := newSwapFixture(t)
	survivor := f.oneClosedTurn("S0")
	// Two hidden turns, so their stored n and the surviving count disagree.
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

	// One pairs the survivor and carries a longer say, so the merge runs; the other is keyed on the
	// hidden turn's say, which the zero keys must refuse. A real replay cannot carry reverted content,
	// so this half is defensive: it pins that a hidden turn is never unioned.
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

	// The surviving view must answer the same across the rescan.
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
