package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// revertRecord appends one turn_revert into carrier naming the window from..through, with the store's id shape.
func revertRecord(f *logFixture, carrier, from, through string, fromN uint64) {
	f.t.Helper()
	f.append(carrier, "", from+":revert", marotte.EntryKindTurnRevert, marotte.EntryTurnRevert{
		From: from, FromN: fromN, Through: through, Cause: marotte.TurnRevertCauseRewind,
	})
}

// railIDs returns the rail index's turns, the surviving view.
func railIDs(l *EntryLog) []string {
	rows := l.RailRows()
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// turnsIn is the set of turns the entries name, in first-seen order: turns of one log interleave in file order.
func turnsIn(entries []marotte.Entry) []string {
	seen := make(map[string]struct{}, len(entries))
	var order []string
	for _, e := range entries {
		if _, ok := seen[e.Turn]; ok {
			continue
		}
		seen[e.Turn] = struct{}{}
		order = append(order, e.Turn)
	}
	return order
}

func mustAll(t *testing.T, l *EntryLog) []marotte.Entry {
	t.Helper()
	entries, err := l.All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	return entries
}

// The skip rule over a real file: the window is file order, bounded by the named turn, the carrier is excluded, and
// the record stays visible in its surviving turn.
func TestRevert_SkipRuleTakesTheWindowAndSparesTheCarrier(t *testing.T) {
	f := newLogFixture(t)
	a, b := f.prompt("a"), f.prompt("b")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	c, d := f.prompt("c"), f.prompt("d")
	f.closeTurn(c, marotte.TurnOutcomeCompleted)
	f.closeTurn(d, marotte.TurnOutcomeCompleted)
	revertRecord(f, b, c, d, 3)

	check := func(when string) {
		t.Helper()
		if got, want := railIDs(f.log), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: RailRows() names %v, want the surviving turns %v", when, got, want)
		}
		if got, want := turnsIn(mustAll(f.t, f.log)), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: All() holds turns %v, want %v", when, got, want)
		}
		count, _ := f.log.Counters()
		if want := f.log.turns[b].n; count != want {
			t.Errorf("%s: turn_count is %d, want the surviving high-water %d", when, count, want)
		}
		if _, err := f.log.TurnRange(c, 0); !errors.Is(err, ErrTurnNotInLog) {
			t.Errorf("%s: TurnRange(%q) = %v, want ErrTurnNotInLog", when, c, err)
		}
		if _, err := f.log.Window(10, c); err == nil {
			t.Errorf("%s: Window(10, %q) returned no error, want the refusal the route renders as 400", when, c)
		}
		w, err := f.log.Window(10, "")
		if err != nil {
			t.Fatalf("%s: Window(10, \"\"): %v", when, err)
		}
		if got, want := turnsIn(w.Entries), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: Window holds turns %v, want %v", when, got, want)
		}
		if w.HasMore {
			t.Errorf("%s: HasMore is true over %d surviving turns, want it read off the surviving order", when, 2)
		}
	}

	// The append path and the scan must build the same index, or a reload changes the view.
	check("live")
	f.reopen()
	check("after a reopen")

	// The lane-less record lives in the carrier and survives with it.
	var records int
	for _, e := range mustAll(t, f.log) {
		if e.Kind == marotte.EntryKindTurnRevert {
			records++
			if e.Turn != b {
				t.Errorf("turn_revert sits in turn %q, want the carrier %q", e.Turn, b)
			}
		}
	}
	if records != 1 {
		t.Errorf("All() holds %d turn_revert entries, want exactly 1", records)
	}
}

// The carrier's own exclusion matters only when its turn_open is inside the window (the minted no-survivor carrier);
// without it a reload shows no boundary row and an empty view.
func TestRevert_CarrierSurvivesItsOwnWindow(t *testing.T) {
	f := newLogFixture(t)
	a := f.prompt("a")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	carrier := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameRevert})
	f.closeTurn(carrier, marotte.TurnOutcomeCompleted)
	revertRecord(f, carrier, a, carrier, 1)

	check := func(when string) {
		t.Helper()
		if got, want := turnsIn(mustAll(f.t, f.log)), []string{carrier}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: All() holds turns %v, want the carrier alone %v", when, got, want)
		}
		count, _ := f.log.Counters()
		if want := f.log.turns[carrier].n; count != want {
			t.Errorf("%s: turn_count is %d, want the carrier's own n %d", when, count, want)
		}
		if open := f.log.openTurnsLocked(); len(open) != 0 {
			t.Errorf("%s: openTurnsLocked() names %v, want none", when, open)
		}
		if _, err := f.log.TurnRange(a, 0); !errors.Is(err, ErrTurnNotInLog) {
			t.Errorf("%s: TurnRange(%q) = %v, want ErrTurnNotInLog", when, a, err)
		}
	}
	check("live")
	f.reopen()
	check("after a reopen")
}

// The carrier's crash state: its turn_open on disk, its record not. The scan marks it reverted, restoring the
// pre-revert view and keeping the store-open closer, whose closer is the reconcile signal, off it.
func TestRevert_IncompleteCarrierIsRevertedAndSynthesizesNoCloser(t *testing.T) {
	f := newLogFixture(t)
	a := f.prompt("a")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	carrier := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameRevert})

	f.reopen()

	if got, want := turnsIn(mustAll(t, f.log)), []string{a}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("All() holds turns %v, want the pre-revert view %v", got, want)
	}
	count, _ := f.log.Counters()
	if want := f.log.turns[a].n; count != want {
		t.Errorf("turn_count is %d, want the pre-revert %d", count, want)
	}
	if open := f.log.openTurnsLocked(); len(open) != 0 {
		t.Errorf("openTurnsLocked() names %v, want none: a reverted turn is not the closer's", open)
	}
	all, reverted, err := f.log.AllWithReverted()
	if err != nil {
		t.Fatalf("AllWithReverted(): %v", err)
	}
	if _, ok := reverted[carrier]; !ok {
		t.Errorf("the reverted set is %v, want it to name the incomplete carrier %q", reverted, carrier)
	}
	for _, e := range all {
		if e.Turn == carrier && e.Kind == marotte.EntryKindTurnClose {
			t.Errorf("a closer was synthesized for the incomplete carrier %q: %+v", carrier, e)
		}
	}
	if _, ok := f.log.NewestRevert(); ok {
		t.Error("NewestRevert() answered a record, want false: the carrier holds none")
	}
}

// NewestRevert reads the index's own order: the last record the scan met.
func TestRevert_NewestRevertIsTheLastRecordInFileOrder(t *testing.T) {
	f := newLogFixture(t)
	if id, ok := f.log.NewestRevert(); ok || id != "" {
		t.Fatalf("NewestRevert() = (%q, %v) on a log holding none, want (\"\", false)", id, ok)
	}
	a, b := f.prompt("a"), f.prompt("b")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	revertRecord(f, a, b, b, 2)
	c := f.prompt("c")
	f.closeTurn(c, marotte.TurnOutcomeCompleted)
	revertRecord(f, a, c, c, 2)

	for _, when := range []string{"live", "after a reopen"} {
		if id, ok := f.log.NewestRevert(); !ok || id != c+":revert" {
			t.Errorf("%s: NewestRevert() = (%q, %v), want (%q, true)", when, id, ok, c+":revert")
		}
		f.reopen()
	}
}

// AllWithReverted, the merge's read, returns every entry in file order and the marked set.
func TestRevert_AllWithRevertedHoldsEverything(t *testing.T) {
	f := newLogFixture(t)
	a, b := f.prompt("a"), f.prompt("b")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	revertRecord(f, a, b, b, 2)

	surviving := mustAll(t, f.log)
	all, reverted, err := f.log.AllWithReverted()
	if err != nil {
		t.Fatalf("AllWithReverted(): %v", err)
	}
	if len(all) <= len(surviving) {
		t.Errorf("AllWithReverted() holds %d entries against All()'s %d, want strictly more", len(all), len(surviving))
	}
	if got, want := turnsIn(all), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("AllWithReverted() holds turns %v, want the whole file %v", got, want)
	}
	if len(reverted) != 1 {
		t.Errorf("the reverted set is %v, want exactly the one turn the record takes", reverted)
	}
	if _, ok := reverted[b]; !ok {
		t.Errorf("the reverted set is %v, want it to name %q", reverted, b)
	}
}

// The skip rule survives the merge's rewrite, the reason the record states Through: the rewrite puts the record in its
// carrier's group before the window, so resolving mid-scan would mark nothing. The reopen arm measures it.
func TestRevert_RewriteDoesNotUnRevert(t *testing.T) {
	f := newLogFixture(t)
	a, b := f.prompt("a"), f.prompt("b")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	c, d := f.prompt("c"), f.prompt("d")
	f.closeTurn(c, marotte.TurnOutcomeCompleted)
	f.closeTurn(d, marotte.TurnOutcomeCompleted)
	revertRecord(f, b, c, d, 3)

	check := func(when string) {
		t.Helper()
		if got, want := turnsIn(mustAll(t, f.log)), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: All() holds turns %v, want the surviving view %v", when, got, want)
		}
		if got, want := railIDs(f.log), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: RailRows() names %v, want %v", when, got, want)
		}
		_, reverted, err := f.log.AllWithReverted()
		if err != nil {
			t.Fatalf("%s: AllWithReverted(): %v", when, err)
		}
		for _, id := range []string{c, d} {
			if _, ok := reverted[id]; !ok {
				t.Errorf("%s: the reverted set is %v, want it to name %q", when, reverted, id)
			}
		}
		if _, ok := reverted[b]; ok {
			t.Errorf("%s: the reverted set names the carrier %q, want it spared", when, b)
		}
	}
	check("live")

	// The swap's store half over the whole file, as MergeEntries hands Rewrite.
	all, _, err := f.log.AllWithReverted()
	if err != nil {
		t.Fatalf("AllWithReverted(): %v", err)
	}
	if err := f.log.Rewrite(t.Context(), all); err != nil {
		t.Fatalf("Rewrite(): %v", err)
	}
	check("after the rewrite")
	f.reopen()
	check("after a reopen")
}

// The carrier is the newest survivor, so reverting turn 1 of k reverts all k and the record stays visible.
func TestRevert_CarrierIsTheNewestSurvivor(t *testing.T) {
	f := newLogFixture(t)
	turns := make([]string, 0, 4)
	for _, name := range []string{"a", "b", "c", "d"} {
		id := f.prompt(name)
		f.closeTurn(id, marotte.TurnOutcomeCompleted)
		turns = append(turns, id)
	}
	record, opened, err := f.log.Revert(t.Context(), turns[2], marotte.TurnRevertCauseRewind, "kas-1")
	if err != nil {
		t.Fatalf("Revert(): %v", err)
	}
	if opened != nil {
		t.Errorf("Revert minted a carrier %q with two turns surviving, want step 2's own choice", opened.Turn)
	}
	if record.Turn != turns[1] {
		t.Errorf("the record sits in turn %q, want the newest survivor %q", record.Turn, turns[1])
	}
	if record.ID != turns[2]+":revert" {
		t.Errorf("record id = %q, want %q", record.ID, turns[2]+":revert")
	}
	var payload marotte.EntryTurnRevert
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("decode turn_revert: %v", err)
	}
	if payload.Through != turns[3] {
		t.Errorf("Through = %q, want the newest turn in file order %q", payload.Through, turns[3])
	}
	if payload.FromN != 3 || payload.KASMessageID != "kas-1" || payload.Cause != marotte.TurnRevertCauseRewind {
		t.Errorf("payload = %+v, want from_n 3, the kas id and the rewind cause", payload)
	}
	if got, want := turnsIn(mustAll(t, f.log)), turns[:2]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("All() holds turns %v, want %v", got, want)
	}
}

// A second rewind picks its carrier against every window, or its record lands in a turn the first revert took.
func TestRevert_ASecondRevertsRecordIsVisible(t *testing.T) {
	f := newLogFixture(t)
	turns := make([]string, 0, 7)
	for _, name := range []string{"a", "b", "c", "d"} {
		id := f.prompt(name)
		f.closeTurn(id, marotte.TurnOutcomeCompleted)
		turns = append(turns, id)
	}
	if _, _, err := f.log.Revert(t.Context(), turns[2], marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("first Revert(): %v", err)
	}
	for _, name := range []string{"e", "f", "g"} {
		id := f.prompt(name)
		f.closeTurn(id, marotte.TurnOutcomeCompleted)
		turns = append(turns, id)
	}
	second, _, err := f.log.Revert(t.Context(), turns[4], marotte.TurnRevertCauseRewind, "")
	if err != nil {
		t.Fatalf("second Revert(): %v", err)
	}
	if second.Turn != turns[1] {
		t.Errorf("the second record sits in turn %q, want %q — the scan must skip the turns the FIRST record took", second.Turn, turns[1])
	}

	check := func(when string) {
		t.Helper()
		if got, want := turnsIn(mustAll(t, f.log)), turns[:2]; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: All() holds turns %v, want %v", when, got, want)
		}
		var records int
		for _, e := range mustAll(t, f.log) {
			if e.Kind == marotte.EntryKindTurnRevert {
				records++
			}
		}
		if records != 2 {
			t.Errorf("%s: the surviving view holds %d turn_revert entries, want both records visible", when, records)
		}
		w, err := f.log.Window(10, "")
		if err != nil {
			t.Fatalf("%s: Window(10, \"\"): %v", when, err)
		}
		if got, want := turnsIn(w.Entries), turns[:2]; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: Window holds turns %v, want %v", when, got, want)
		}
	}
	check("live")
	f.reopen()
	check("after a reopen")
}

// When the window takes every turn, the log mints a carrier at the post-revert high-water and closes it before the
// record: an open carrier would raise the reconcile signal.
func TestRevert_NoSurvivorMintsAClosedCarrierAtOrdinalOne(t *testing.T) {
	f := newLogFixture(t)
	turns := make([]string, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		id := f.prompt(name)
		f.closeTurn(id, marotte.TurnOutcomeCompleted)
		turns = append(turns, id)
	}
	record, opened, err := f.log.Revert(t.Context(), turns[0], marotte.TurnRevertCauseRewind, "")
	if err != nil {
		t.Fatalf("Revert(): %v", err)
	}
	if opened == nil {
		t.Fatal("Revert minted no carrier with every turn inside the window, want step 3's own turn")
	}
	if record.Turn != opened.Turn {
		t.Errorf("the record sits in %q, want the minted carrier %q", record.Turn, opened.Turn)
	}

	check := func(when string) {
		t.Helper()
		if got, want := turnsIn(mustAll(t, f.log)), []string{opened.Turn}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: All() holds turns %v, want the carrier alone %v", when, got, want)
		}
		st := f.log.turns[opened.Turn]
		if st.n != 1 {
			t.Errorf("%s: the carrier's n is %d, want 1 — the post-revert high-water plus one, never k+1", when, st.n)
		}
		if st.source != marotte.TurnOpenNameRevert {
			t.Errorf("%s: the carrier's source is %q, want %q", when, st.source, marotte.TurnOpenNameRevert)
		}
		if !st.closed || st.unterminated {
			t.Errorf("%s: the carrier is closed=%v unterminated=%v, want a completed close", when, st.closed, st.unterminated)
		}
		if count, _ := f.log.Counters(); count != 1 {
			t.Errorf("%s: turn_count is %d, want 1", when, count)
		}
		if open := f.log.openTurnsLocked(); len(open) != 0 {
			t.Errorf("%s: openTurnsLocked() = %v, want none: a closed carrier earns no synthesized closer", when, open)
		}
	}
	check("live")
	f.reopen()
	check("after a reopen")
}

// A between-turns append after a revert lands in a surviving turn; it once landed inside the window, unread.
func TestRevert_BetweenTurnsAppendLandsInASurvivingTurn(t *testing.T) {
	f := newLogFixture(t)
	a := f.prompt("a")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	b := f.prompt("b")
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	if _, _, err := f.log.Revert(t.Context(), b, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("Revert(): %v", err)
	}

	e := entryOf("", "", "", marotte.EntryKindModeSwitched, marotte.EntryModeSwitched{To: "spec"})
	opened, err := f.log.AppendBetweenTurns(t.Context(), e)
	if err != nil {
		t.Fatalf("AppendBetweenTurns(): %v", err)
	}
	if opened != nil {
		t.Errorf("AppendBetweenTurns minted turn %q with a survivor present, want it to join %q", opened.Turn, a)
	}
	if e.Turn != a {
		t.Errorf("the entry landed in turn %q, want the newest SURVIVING turn %q", e.Turn, a)
	}
	found := func(entries []marotte.Entry) bool {
		for _, got := range entries {
			if got.Kind == marotte.EntryKindModeSwitched {
				return true
			}
		}
		return false
	}
	if !found(mustAll(t, f.log)) {
		t.Error("All() does not hold the between-turns entry: it landed where no reader looks")
	}
	w, err := f.log.Window(10, "")
	if err != nil {
		t.Fatalf("Window(10, \"\"): %v", err)
	}
	if !found(w.Entries) {
		t.Error("the window does not hold the between-turns entry")
	}

	// With the carrier as the only turn, a between-turns append joins it.
	if _, _, err := f.log.Revert(t.Context(), a, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("second Revert(): %v", err)
	}
	e2 := entryOf("", "", "", marotte.EntryKindModeSwitched, marotte.EntryModeSwitched{To: "vibe"})
	if opened, err := f.log.AppendBetweenTurns(t.Context(), e2); err != nil || opened != nil {
		t.Fatalf("AppendBetweenTurns() = (%v, %v), want it to join the surviving carrier", opened, err)
	}
	if !found(mustAll(t, f.log)) {
		t.Error("the second between-turns entry is invisible to All()")
	}
}

// countingReaderAt counts bytes read: two reads return the same entries, so only bytes tell per-range from one span.
type countingReaderAt struct {
	r    io.ReaderAt
	read int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read += int64(n)
	return n, err
}

// bytesOfTurns sums the set's own byte ranges from the file, newlines included.
func bytesOfTurns(t *testing.T, path string, want map[string]struct{}) int64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var total int64
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e marotte.Entry
		if err := json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &e); err != nil {
			t.Fatalf("parse a line of %s: %v", path, err)
		}
		if _, ok := want[e.Turn]; ok {
			total += int64(len(line))
		}
	}
	return total
}

// A window read issues one pread per byte range, keeping cost proportional to the page after a rewind: a span or a
// per-run read would cross every reverted byte up to the carrier's record at the tail. The oracle is the entries plus a
// byte bound.
func TestRevert_WindowReadsOnePreadPerRange(t *testing.T) {
	f := newLogFixture(t)
	a, b := f.prompt("a"), f.prompt("b")
	f.closeTurn(a, marotte.TurnOutcomeCompleted)
	f.closeTurn(b, marotte.TurnOutcomeCompleted)
	c, d, e := f.prompt("c"), f.prompt("d"), f.prompt("e")
	for _, turn := range []string{c, d, e} {
		f.closeTurn(turn, marotte.TurnOutcomeCompleted)
	}
	// Two records leave {a, b, e} surviving with gaps at c and d; b holds two ranges, its body and its record.
	revertRecord(f, b, c, c, 3)
	revertRecord(f, e, d, d, 4)

	want := map[string]struct{}{a: {}, b: {}, e: {}}
	budget := bytesOfTurns(t, filepath.Join(f.root, entriesFileName), want)

	var counted *countingReaderAt
	restore := readEntries
	readEntries = func(file *os.File) io.ReaderAt {
		counted = &countingReaderAt{r: file}
		return counted
	}
	t.Cleanup(func() { readEntries = restore })

	w, err := f.log.Window(10, "")
	if err != nil {
		t.Fatalf("Window(10, \"\"): %v", err)
	}
	if got, expect := turnsIn(w.Entries), []string{a, b, e}; strings.Join(got, ",") != strings.Join(expect, ",") {
		t.Errorf("Window holds turns %v, want the surviving turns %v", got, expect)
	}
	if counted == nil {
		t.Fatalf("the window read opened no file, so its cost was not measured")
	}
	if counted.read > budget {
		t.Errorf("Window read %d bytes over a set whose own ranges are %d bytes, want no more than the set's own: a reverted range between two surviving turns is being read and thrown away", counted.read, budget)
	}
}

// The scan precomputes the reconcile predicate's inputs: the index row (closer stop reason, text-less steer) and the
// three sets (reconciled turns, reconciled sessions, bound sessions).
func TestScan_CarriesTheReconcileAnswers(t *testing.T) {
	f := newLogFixture(t)
	crashed, live := f.prompt("crashed"), f.prompt("live")
	f.append(crashed, "", crashed+":close", marotte.EntryKindTurnClose, marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
	})
	f.append(live, "", "steer-1", marotte.EntryKindSteer, marotte.EntrySteer{
		Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped,
	})
	f.append(live, "", "bind-1", marotte.EntryKindTurnBind, marotte.EntryTurnBind{
		KASMessageID: "kas-1", SessionID: "sess-1",
	})
	f.closeTurn(live, marotte.TurnOutcomeCompleted)
	f.append(live, "", "rec-turn", marotte.EntryKindReconciled, marotte.EntryReconciled{Turn: crashed})
	f.append(live, "", "rec-session", marotte.EntryKindReconciled, marotte.EntryReconciled{Session: "sess-1"})

	check := func(when string) {
		t.Helper()
		if !f.log.turns[crashed].unterminated {
			t.Errorf("%s: the turn closed stop_reason_raw=unterminated is not marked unterminated, so the crash signal is invisible to the predicate", when)
		}
		if f.log.turns[live].unterminated {
			t.Errorf("%s: an ordinary turn_close marked the turn unterminated, which raises a lost-history claim for a chat that lost nothing", when)
		}
		if !f.log.turns[live].emptySteer {
			t.Errorf("%s: a text-less steer is not marked, so words KAS holds and this process never received leave no signal", when)
		}
		if f.log.turns[crashed].emptySteer {
			t.Errorf("%s: a turn with no steer at all is marked emptySteer", when)
		}
		if _, ok := f.log.boundSessions["sess-1"]; !ok {
			t.Errorf("%s: boundSessions is %v, want the session the turn_bind names", when, f.log.boundSessions)
		}
		if _, ok := f.log.reconciledTurns[crashed]; !ok {
			t.Errorf("%s: reconciledTurns is %v, want the turn the record names", when, f.log.reconciledTurns)
		}
		if _, ok := f.log.reconciledSessions["sess-1"]; !ok {
			t.Errorf("%s: reconciledSessions is %v, want the session the record names", when, f.log.reconciledSessions)
		}
		if _, ok := f.log.reconciledTurns["sess-1"]; ok {
			t.Errorf("%s: the session form of a reconciled record landed in reconciledTurns, so a per-turn condition clears on a session's name", when)
		}
	}

	check("live")
	f.reopen()
	check("after a reopen")
}

// An incomplete carrier is reverted at either interruption point, and the flags follow the record, so a failed revert
// matches a fresh open. The interruption is the k-th syncEntries (turn_open, turn_close, record); a failed sync leaves
// the line on disk, so the crash drops the unsynced tail.
func TestRevert_AnIncompleteCarrierIsRevertedAtEitherInterruptionPoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failAt int
		holds  []marotte.EntryKind
	}{
		{
			name:   "the carrier's turn_open lands and its turn_close does not",
			failAt: 2,
			holds:  []marotte.EntryKind{marotte.EntryKindTurnOpen},
		},
		{
			name:   "both land and the record does not",
			failAt: 3,
			holds:  []marotte.EntryKind{marotte.EntryKindTurnOpen, marotte.EntryKindTurnClose},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLogFixture(t)
			a, b := f.prompt("a"), f.prompt("b")
			f.closeTurn(a, marotte.TurnOutcomeCompleted)
			f.closeTurn(b, marotte.TurnOutcomeCompleted)
			wantCount, _ := f.log.Counters()
			wantRail := strings.Join(railIDs(f.log), ",")

			boom := errors.New("no space left on device")
			var calls int
			var durable int64
			restore := syncEntries
			syncEntries = func(file *os.File) error {
				calls++
				if calls == tc.failAt {
					return boom
				}
				if err := file.Sync(); err != nil {
					return err
				}
				if info, err := file.Stat(); err == nil {
					durable = info.Size()
				}
				return nil
			}
			t.Cleanup(func() { syncEntries = restore })

			// Reverting the oldest turn takes b too, so the carrier this arm interrupts is minted.
			_, opened, err := f.log.Revert(t.Context(), a, marotte.TurnRevertCauseRewind, "kas-a")
			if !errors.Is(err, boom) {
				t.Fatalf("Revert(%q) with sync %d failing = %v, want the write error", a, tc.failAt, err)
			}
			syncEntries = restore
			if opened == nil {
				t.Fatal("the failed revert answered no carrier, so its caller cannot name the turn it left on disk")
			}

			for _, id := range []string{a, b} {
				if f.log.turns[id].reverted {
					t.Errorf("turn %q is reverted after a FAILED revert, so the attempt hid history no record states", id)
				}
			}
			if !f.log.turns[opened.Turn].reverted {
				t.Errorf("the carrier %q still survives in memory after the revert failed, where a fresh open marks it reverted: the flags follow the record, not the attempt",
					opened.Turn)
			}
			if got := strings.Join(railIDs(f.log), ","); got != wantRail {
				t.Errorf("RailRows() names %q after the failed revert, want the pre-revert rail %q", got, wantRail)
			}
			if got, _ := f.log.Counters(); got != wantCount {
				t.Errorf("turn_count is %d after the failed revert, want the pre-revert %d", got, wantCount)
			}
			if err := f.log.Append(t.Context(), entryOf(b, "", "say-later", marotte.EntryKindText,
				marotte.EntryText{Text: "later"})); !errors.Is(err, errEntryLogFailed) {
				t.Errorf("a later append answered %v, want the latch", err)
			}

			// The line whose sync failed never reached the device; the next open reads the durable prefix.
			if err := os.Truncate(filepath.Join(f.root, entriesFileName), durable); err != nil {
				t.Fatalf("drop the unsynced tail: %v", err)
			}
			f.reopen()

			if st := f.log.turns[opened.Turn]; st == nil || !st.reverted {
				t.Fatalf("a fresh open leaves the carrier %q in the surviving view, want it reverted: its turn_open is on disk and its record is not",
					opened.Turn)
			}
			if got, want := turnsIn(mustAll(t, f.log)), []string{a, b}; strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("a fresh open answers turns %v, want the pre-revert view %v", got, want)
			}
			if got, _ := f.log.Counters(); got != wantCount {
				t.Errorf("a fresh open answers turn_count %d, want the pre-revert %d", got, wantCount)
			}
			if open := f.log.openTurnsLocked(); len(open) != 0 {
				t.Errorf("openTurnsLocked() names %v, want none: a reverted carrier is never re-closed", open)
			}
			if got := kindsOfTurn(t, f.log, opened.Turn); !slices.Equal(got, tc.holds) {
				t.Errorf("the carrier holds %v on disk, want %v with NO closer synthesized for it", got, tc.holds)
			}
		})
	}
}

// kindsOfTurn reads one turn's kinds past the surviving view; TurnRange refuses reverted turns.
func kindsOfTurn(t *testing.T, l *EntryLog, turn string) []marotte.EntryKind {
	t.Helper()
	all, _, err := l.AllWithReverted()
	if err != nil {
		t.Fatalf("AllWithReverted(): %v", err)
	}
	kinds := make([]marotte.EntryKind, 0, 4)
	for _, e := range all {
		if e.Turn != turn {
			continue
		}
		kinds = append(kinds, e.Kind)
		if e.Kind != marotte.EntryKindTurnClose {
			continue
		}
		var footer marotte.EntryTurnClose
		if err := json.Unmarshal(e.Payload, &footer); err != nil {
			t.Fatalf("parse the turn_close of %q: %v", turn, err)
		}
		if footer.StopReasonRaw == string(marotte.StopReasonUnterminated) {
			t.Errorf("turn %q holds a synthesized closer, want none: a reverted carrier is not an unterminated turn", turn)
		}
	}
	return kinds
}

// Store.NewestRevert is what a resume's projection snapshots, a store read because the log is only reachable inside
// the swap. A chat with nothing to snapshot must answer the empty id, or every first resume is discarded.
func TestStore_NewestRevertIsTheRecordAResumeSnapshots(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"

	if got, held := s.NewestRevert(t.Context(), id); held || got != "" {
		t.Errorf("NewestRevert of a chat with no log = %q/%v, want an empty id and false", got, held)
	}

	first := openPromptTurn(t, s, id, "m-1")
	closeTurn(t, s, id, first, marotte.TurnOutcomeCompleted)
	second := openPromptTurn(t, s, id, "m-2")
	closeTurn(t, s, id, second, marotte.TurnOutcomeCompleted)
	if got, held := s.NewestRevert(t.Context(), id); held || got != "" {
		t.Errorf("NewestRevert of a log holding no revert = %q/%v, want an empty id and false", got, held)
	}

	record, _, err := s.Revert(t.Context(), id, second, "")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	got, held := s.NewestRevert(t.Context(), id)
	if !held || got != record.ID {
		t.Errorf("NewestRevert after the rewind = %q/%v, want the appended record's own id %q",
			got, held, record.ID)
	}
	if got != second+":revert" {
		t.Errorf("the snapshotted id is %q, want the <from>:revert the store mints", got)
	}
}

// A turn opened after a revert reuses an ordinal, so groupByTurn keeps the read's file order; here n order and file
// order differ.
func TestRevert_RewriteKeepsTheReadsOrderWhenAnOrdinalIsReused(t *testing.T) {
	f := newLogFixture(t)
	turns := make([]string, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		id := f.prompt(name)
		f.closeTurn(id, marotte.TurnOutcomeCompleted)
		turns = append(turns, id)
	}
	// The window is b..c, so a carries the record.
	if _, opened, err := f.log.Revert(t.Context(), turns[1], marotte.TurnRevertCauseRewind, "kas-1"); err != nil {
		t.Fatalf("Revert(): %v", err)
	} else if opened != nil {
		t.Fatalf("Revert minted a carrier %q with turn a surviving, want step 2's own choice", opened.Turn)
	}
	fresh := f.prompt("d")
	f.closeTurn(fresh, marotte.TurnOutcomeCompleted)

	all, reverted, err := f.log.AllWithReverted()
	if err != nil {
		t.Fatalf("AllWithReverted(): %v", err)
	}
	// Guard: without reuse the two orders coincide and this measures nothing.
	if got, want := ordinalOfTurnIn(t, all, fresh), ordinalOfTurnIn(t, all, turns[1]); got != want {
		t.Fatalf("the new turn's n = %d and the reverted turn b's = %d; this fixture only "+
			"discriminates while an ordinal is REUSED, so it is measuring nothing", got, want)
	}
	if _, hidden := reverted[turns[1]]; !hidden {
		t.Fatalf("the reverted set does not name b (%q), so the fixture holds no hidden turn", turns[1])
	}
	read := turnOrderIn(all)

	if err := f.log.Rewrite(t.Context(), all); err != nil {
		t.Fatalf("Rewrite(): %v", err)
	}
	check := func(when string) {
		t.Helper()
		got, _, err := f.log.AllWithReverted()
		if err != nil {
			t.Fatalf("%s: AllWithReverted(): %v", when, err)
		}
		if order := turnOrderIn(got); !slices.Equal(order, read) {
			t.Errorf("%s: the log's turn order is %v, want the READ's own order %v — an `n` "+
				"sort decided the rewrite, so the reused ordinal put the new turn ahead of a "+
				"turn the read held before it", when, order, read)
		}
	}
	check("after the rewrite")
	f.reopen()
	check("after a reopen")
}

// turnOrderIn is a read's turn order: each turn id at its first appearance.
func turnOrderIn(entries []marotte.Entry) []string {
	var order []string
	seen := make(map[string]struct{}, len(entries))
	for i := range entries {
		if _, ok := seen[entries[i].Turn]; ok {
			continue
		}
		seen[entries[i].Turn] = struct{}{}
		order = append(order, entries[i].Turn)
	}
	return order
}

// ordinalOfTurnIn is one turn's stored n out of a flat read.
func ordinalOfTurnIn(t *testing.T, entries []marotte.Entry, turn string) uint64 {
	t.Helper()
	for i := range entries {
		if entries[i].Turn != turn || entries[i].Kind != marotte.EntryKindTurnOpen {
			continue
		}
		var open marotte.EntryTurnOpen
		if err := json.Unmarshal(entries[i].Payload, &open); err != nil {
			t.Fatalf("parse the turn_open of %q: %v", turn, err)
		}
		return open.N
	}
	t.Fatalf("the read holds no turn_open for %q", turn)
	return 0
}
