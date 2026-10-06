package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

// Property 4: a crash after any seal leaves every sealed entry on disk.
//
// The crash is modelled as a TRUNCATION at a drawn line boundary, optionally leaving
// a partial line, rather than as a failing sync: a write that reached the page cache
// and whose fsync then failed is still visible to the next open in this process, so a
// failing sync models a write ERROR and not a crash. That arm is
// TestEntryLog_AWriteErrorRefusesFurtherAppends, which drives the same package-var
// seam the design names.
//
// The third arm — every turn closed with interrupted/unterminated, which IS the
// reconcile signal, aborted results, seq continuing after — is folded in here, because
// a truncated prefix is exactly a log with turns nothing can close.
func TestEntryLogCrashLeavesEverySealedEntry(t *testing.T) {
	ctx := t.Context()
	base := t.TempDir()
	var runs int
	rapid.Check(t, func(rt *rapid.T) {
		runs++
		root := filepath.Join(base, fmt.Sprintf("crash-%d", runs))
		h := NewEntryHeader(root)
		written := writeUnterminatedLog(rt, ctx, root, h)

		lines := readLogLines(rt, filepath.Join(root, entriesFileName))
		if len(lines) != len(written) {
			rt.Fatalf("the log holds %d lines for %d appended entries", len(lines), len(written))
		}
		durable := rapid.IntRange(1, len(lines)).Draw(rt, "durable_lines")
		torn := rapid.Bool().Draw(rt, "torn_tail")
		crash(rt, filepath.Join(root, entriesFileName), lines, durable, torn)

		lg, err := OpenEntryLog(ctx, root, h)
		if err != nil {
			rt.Fatalf("reopen after the crash: %v", err)
		}
		defer func() { _ = lg.Close() }()

		survived, err := lg.Window(len(written)+8, "")
		if err != nil {
			rt.Fatalf("read the surviving log: %v", err)
		}
		checkDurablePrefix(rt, survived.Entries, written[:durable])
		checkFileEndsAtALineBoundary(rt, filepath.Join(root, entriesFileName))
		checkEveryTurnIsClosed(rt, survived.Entries)
		checkReconcileSignalIsInTheLog(rt, lg)
		checkSeqContinues(rt, ctx, lg, survived.Entries)
	})
}

// writeUnterminatedLog builds a log of one or two turns with nothing closed, which is
// what a crash mid-turn leaves, and answers the entries it appended in order.
func writeUnterminatedLog(rt *rapid.T, ctx context.Context, root string, h EntryHeader) []marotte.Entry {
	lg, err := OpenEntryLog(ctx, root, h)
	if err != nil {
		rt.Fatalf("open entry log: %v", err)
	}
	defer func() { _ = lg.Close() }()
	if err := h.Write(ctx, &marotte.Chat{ID: "c-abcdef01", Model: "opus"}); err != nil {
		rt.Fatalf("write header: %v", err)
	}

	var written []marotte.Entry
	turns := rapid.IntRange(1, 2).Draw(rt, "turns")
	for turn := range turns {
		opened, oerr := lg.OpenTurn(ctx, &TurnSpec{
			Source: marotte.TurnOpenNamePrompt,
			Prompt: &marotte.EntryPrompt{ID: fmt.Sprintf("m-%d", turn), Text: "ask"},
		})
		if oerr != nil {
			rt.Fatalf("open turn: %v", oerr)
		}
		written = append(written, *opened)
		n := rapid.IntRange(0, 5).Draw(rt, fmt.Sprintf("entries%d", turn))
		for i := range n {
			e := drawEntry(rt, opened.Turn, fmt.Sprintf("%d-%d", turn, i))
			if err := lg.Append(ctx, e); err != nil {
				rt.Fatalf("append: %v", err)
			}
			written = append(written, *e)
		}
	}
	return written
}

// drawEntry is one sealed entry of a kind the crash arm cares about: a tool_call
// leaves work the closer must abort, and the rest are ordinary body entries.
func drawEntry(rt *rapid.T, turn, tag string) *marotte.Entry {
	lane := rapid.SampledFrom([]string{"", "d1"}).Draw(rt, "lane"+tag)
	switch rapid.IntRange(0, 2).Draw(rt, "kind"+tag) {
	case 0:
		return entryOf(turn, lane, "call-"+tag, marotte.EntryKindToolCall,
			marotte.EntryToolCall{ID: "call-" + tag, Status: marotte.ToolInProgress})
	case 1:
		return entryOf(turn, lane, "say-"+tag, marotte.EntryKindText, marotte.EntryText{Text: "prose " + tag})
	default:
		return entryOf(turn, "", "steer-"+tag, marotte.EntryKindSteer,
			marotte.EntrySteer{Text: "also", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead})
	}
}

// crash truncates the log after the durable-th line, optionally leaving a partial
// line, which is what a host crash mid-write leaves.
func crash(rt *rapid.T, path string, lines [][]byte, durable int, torn bool) {
	var buf bytes.Buffer
	for _, line := range lines[:durable] {
		buf.Write(line)
	}
	if torn && durable < len(lines) {
		partial := lines[durable]
		buf.Write(partial[:len(partial)/2])
	}
	if err := os.WriteFile(path, buf.Bytes(), fileMode); err != nil {
		rt.Fatalf("simulate the crash: %v", err)
	}
}

// readLogLines is the log's lines INCLUDING their newlines.
func readLogLines(rt *rapid.T, path string) [][]byte {
	data, err := os.ReadFile(path)
	if err != nil {
		rt.Fatalf("read the log: %v", err)
	}
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, data)
			break
		}
		out = append(out, data[:i+1])
		data = data[i+1:]
	}
	return out
}

// checkDurablePrefix is the property: every entry whose seal returned before the
// crash is on disk, in order, with the seq it took.
func checkDurablePrefix(rt *rapid.T, survived, want []marotte.Entry) {
	if len(survived) < len(want) {
		rt.Fatalf("the log holds %d entries after the crash, want at least the %d durable ones: %v",
			len(survived), len(want), shapes(survived))
	}
	for i := range want {
		if survived[i].ID != want[i].ID || survived[i].Seq != want[i].Seq || survived[i].Turn != want[i].Turn {
			rt.Fatalf("durable entry %d is %s seq %d of turn %s, want %s seq %d of turn %s",
				i, survived[i].ID, survived[i].Seq, survived[i].Turn,
				want[i].ID, want[i].Seq, want[i].Turn)
		}
	}
}

func checkFileEndsAtALineBoundary(rt *rapid.T, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		rt.Fatalf("read the log: %v", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		rt.Fatalf("the log does not end at a line boundary: %q", tailOf(data))
	}
}

// checkEveryTurnIsClosed holds the invariant an open turn in the log always belongs
// to a live process: after an open, none does.
func checkEveryTurnIsClosed(rt *rapid.T, entries []marotte.Entry) {
	open := map[string]bool{}
	unsettled := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		switch e.Kind {
		case marotte.EntryKindTurnOpen:
			open[e.Turn] = true
		case marotte.EntryKindTurnClose:
			open[e.Turn] = false
			var footer marotte.EntryTurnClose
			if err := json.Unmarshal(e.Payload, &footer); err != nil {
				rt.Fatalf("parse turn_close of %s: %v", e.Turn, err)
			}
			if footer.Outcome != marotte.TurnOutcomeInterrupted || footer.StopReasonRaw != string(marotte.StopReasonUnterminated) {
				rt.Fatalf("the synthesized closer of %s is (%q, %q), want (interrupted, unterminated)",
					e.Turn, footer.Outcome, footer.StopReasonRaw)
			}
			if footer.Model != "opus" {
				rt.Fatalf("the closer of %s stamped model %q, want the header's", e.Turn, footer.Model)
			}
		case marotte.EntryKindToolCall:
			unsettled[e.ID] = true
		case marotte.EntryKindToolResult:
			delete(unsettled, strings.TrimSuffix(e.ID, ":result"))
			var res marotte.EntryToolResult
			if err := json.Unmarshal(e.Payload, &res); err != nil {
				rt.Fatalf("parse tool_result %s: %v", e.ID, err)
			}
			if res.Status != marotte.ToolAborted {
				rt.Fatalf("tool_result %s settled as %q, want aborted", e.ID, res.Status)
			}
		}
	}
	for turn, still := range open {
		if still {
			rt.Fatalf("turn %s is still open after the store-open closer ran", turn)
		}
	}
	if len(unsettled) != 0 {
		rt.Fatalf("calls left with no result after the closer: %v", unsettled)
	}
}

// checkReconcileSignalIsInTheLog holds that the crash the closer repaired is still
// ASKED about: the synthesized closer's "unterminated" stop reason IS the signal, so
// the predicate answers true off the log with no header flag written beside it.
func checkReconcileSignalIsInTheLog(rt *rapid.T, lg *EntryLog) {
	if !lg.NeedsReconcile() {
		rt.Fatal("NeedsReconcile() is false after a crash the closer had to repair")
	}
}

// checkSeqContinues holds that the next append picks up after the closer rather than
// reusing a seq the crash removed.
func checkSeqContinues(rt *rapid.T, ctx context.Context, lg *EntryLog, entries []marotte.Entry) {
	newest := entries[len(entries)-1].Turn
	want := entries[len(entries)-1].Seq + 1
	e := entryOf(newest, "", "after-the-crash", marotte.EntryKindCompaction,
		marotte.EntryCompaction{Summary: "s"})
	if err := lg.Append(ctx, e); err != nil {
		rt.Fatalf("append after the crash: %v", err)
	}
	if e.Seq != want {
		rt.Fatalf("the append after the crash took seq %d, want %d", e.Seq, want)
	}
}

func tailOf(data []byte) string {
	if len(data) > 80 {
		return string(data[len(data)-80:])
	}
	return string(data)
}

// The store half of property 9: a run's log holds one turn per node path, their
// entries interleave, each turn's seq is contiguous on its own, a crash closes the
// open step turn with the placeholder, and the resumed run's next frame for the same
// path opens a NEW turn. The run root creates no chats/ directory.
func TestRunLogHoldsOneTurnPerNodePath(t *testing.T) {
	ctx := t.Context()
	configDir := t.TempDir()
	root := filepath.Join(configDir, "runs", "wf_1")
	lg, err := OpenEntryLog(ctx, root, NoHeader())
	if err != nil {
		t.Fatalf("open run log: %v", err)
	}

	build := openStepTurn(t, ctx, lg, "build")
	test := openStepTurn(t, ctx, lg, "test")
	for i := range 3 {
		appendStep(t, ctx, lg, build, fmt.Sprintf("build-%d", i))
		appendStep(t, ctx, lg, test, fmt.Sprintf("test-%d", i))
	}
	wantShapes(t, mustRunRange(t, ctx, lg, build),
		[]string{"0:/turn_open", "1:/text", "2:/text", "3:/text"}, "the build step's turn")
	wantShapes(t, mustRunRange(t, ctx, lg, test),
		[]string{"0:/turn_open", "1:/text", "2:/text", "3:/text"}, "the test step's turn")

	// node_complete closes one path's turn and leaves the other open.
	if err := lg.Append(ctx, entryOf(build, "", build+":close", marotte.EntryKindTurnClose,
		marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})); err != nil {
		t.Fatalf("close the build turn: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("close run log: %v", err)
	}

	resumed, err := OpenEntryLog(ctx, root, NoHeader())
	if err != nil {
		t.Fatalf("reopen run log: %v", err)
	}
	t.Cleanup(func() { _ = resumed.Close() })
	closed := mustRunRange(t, ctx, resumed, test)
	last := closed[len(closed)-1]
	if last.Kind != marotte.EntryKindTurnClose {
		t.Fatalf("the open step turn is %v after a crash, want the placeholder", shapes(closed))
	}
	var footer marotte.EntryTurnClose
	if err := json.Unmarshal(last.Payload, &footer); err != nil {
		t.Fatalf("parse the placeholder: %v", err)
	}
	if footer.StopReasonRaw != string(marotte.StopReasonUnterminated) || footer.Model != "" {
		t.Errorf("the placeholder is (%q, model %q), want (unterminated, no model)",
			footer.StopReasonRaw, footer.Model)
	}

	retried := openStepTurn(t, ctx, resumed, "test")
	if retried == test {
		t.Error("the resumed run reused the crashed turn, want a NEW turn for the same node path")
	}
	if got := nodePathOf(t, resumed, retried); got != "test" {
		t.Errorf("the new turn's node_path is %q, want test", got)
	}
	if _, err := os.Stat(filepath.Join(configDir, "chats")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat of chats/ answered %v, want no chat directory for a run", err)
	}
}

func openStepTurn(t *testing.T, ctx context.Context, lg *EntryLog, nodePath string) string {
	t.Helper()
	e, err := lg.OpenTurn(ctx, &TurnSpec{
		Source: marotte.TurnOpenNameWorkflowStep, Run: "wf_1",
		NodePath: nodePath, SessionID: "sess-" + nodePath,
	})
	if err != nil {
		t.Fatalf("open the %s step turn: %v", nodePath, err)
	}
	return e.Turn
}

func appendStep(t *testing.T, ctx context.Context, lg *EntryLog, turn, tag string) {
	t.Helper()
	if err := lg.Append(ctx, entryOf(turn, "", "say-"+tag, marotte.EntryKindText,
		marotte.EntryText{Text: tag})); err != nil {
		t.Fatalf("append %s: %v", tag, err)
	}
}

func mustRunRange(t *testing.T, _ context.Context, lg *EntryLog, turn string) []marotte.Entry {
	t.Helper()
	entries, err := lg.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("read turn %s: %v", turn, err)
	}
	return entries
}

func nodePathOf(t *testing.T, lg *EntryLog, turn string) string {
	t.Helper()
	entries, err := lg.TurnRange(turn, 0)
	if err != nil {
		t.Fatalf("read turn %s: %v", turn, err)
	}
	var open marotte.EntryTurnOpen
	if err := json.Unmarshal(entries[0].Payload, &open); err != nil {
		t.Fatalf("parse turn_open: %v", err)
	}
	return open.NodePath
}

// Property 6: a revert is a RECORD, and the range it names is unreadable through every
// surface but the merge's own. The generator is the COMPOSITION: k turns with bodies, one
// of which may STRADDLE the revert (open before it, closed after, an unsettled tool_call
// inside), a revert at j — the oldest turn included, so the no-survivor path is drawn —
// further turns, then a SECOND revert drawn from the SURVIVING view. The model beside it
// applies the skip rule independently of the index, so an implementation that merely
// agrees with itself still fails.
func TestEntryLogARevertIsARecordAndTheRangeIsUnreadable(t *testing.T) {
	ctx := t.Context()
	base := t.TempDir()
	var runs int
	rapid.Check(t, func(rt *rapid.T) {
		runs++
		s := openRevertScene(rt, ctx, filepath.Join(base, fmt.Sprintf("revert-%d", runs)))
		defer s.close()

		straddler := ""
		oldest := 0
		if rapid.Bool().Draw(rt, "straddler") {
			straddler = s.openStraddler()
			// Its own turn_open is the file's first, so every revertible turn lies
			// above it and the straddle is a property of the drawn j, not of luck.
			oldest = 1
		}
		for i := range rapid.IntRange(1, 4).Draw(rt, "turns") {
			turn := s.openPrompt(fmt.Sprintf("p%d", i))
			for j := range rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("body%d", i)) {
				s.appendBody(turn, fmt.Sprintf("say-%d-%d", i, j))
			}
			s.closeTurn(turn)
		}
		if straddler != "" {
			// Closed AFTER every prompt turn's own closer, which is what makes its
			// byte range straddle whatever the revert takes.
			s.closeTurn(straddler)
		}

		s.revert(rapid.IntRange(oldest, len(s.order)-1).Draw(rt, "j1"))
		s.check("after the first revert")
		s.betweenTurns("after-j1")
		if straddler != "" {
			s.checkStraddlerSurvivedWhole("after the first revert", straddler)
		}

		for i := range rapid.IntRange(0, 2).Draw(rt, "later_turns") {
			turn := s.openPrompt(fmt.Sprintf("q%d", i))
			s.closeTurn(turn)
		}
		second := rapid.SampledFrom(s.survivingOrder()).Draw(rt, "j2")
		s.revert(slices.Index(s.order, second))
		s.check("after the second revert")
		s.betweenTurns("after-j2")

		s.reopen()
		s.check("after a reopen")
		if straddler != "" && !s.isReverted(straddler) {
			s.checkStraddlerSurvivedWhole("after a reopen", straddler)
		}
		s.checkNoSynthesizedCloser("after a reopen")
	})
}

// revertScene is one generated log beside the MODEL property 6 reads it against: every
// turn in FILE order, the ordinal each took at its open, the reverted set the skip rule
// produces, and the record ids appended so far.
type revertScene struct {
	rt       *rapid.T
	ctx      context.Context
	root     string
	header   EntryHeader
	log      *EntryLog
	order    []string
	ordinal  map[string]uint64
	reverted map[string]struct{}
	records  []recordedRevert
}

// recordedRevert is one record the model wrote, beside the CARRIER it lives in: a later
// revert that takes that carrier hides the record with it, which is the honest answer
// rather than a lost boundary row — the older cut is inside the range the newer rewind
// removed, so the transcript draws one row at the newer cut.
type recordedRevert struct{ id, carrier string }

func openRevertScene(rt *rapid.T, ctx context.Context, root string) *revertScene {
	h := NewEntryHeader(root)
	lg, err := OpenEntryLog(ctx, root, h)
	if err != nil {
		rt.Fatalf("OpenEntryLog(%s): %v", root, err)
	}
	return &revertScene{
		rt: rt, ctx: ctx, root: root, header: h, log: lg,
		ordinal: make(map[string]uint64), reverted: make(map[string]struct{}),
	}
}

func (s *revertScene) close() { _ = s.log.Close() }

// reopen is what runs the scan, the incomplete-carrier rule and the store-open closer
// again, so every arm is asserted against the append path AND against the index a
// fresh process builds from the same bytes.
func (s *revertScene) reopen() {
	if err := s.log.Close(); err != nil {
		s.rt.Fatalf("Close(): %v", err)
	}
	lg, err := OpenEntryLog(s.ctx, s.root, s.header)
	if err != nil {
		s.rt.Fatalf("reopen OpenEntryLog(%s): %v", s.root, err)
	}
	s.log = lg
}

func (s *revertScene) openPrompt(tag string) string {
	e, err := s.log.OpenTurn(s.ctx, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-" + tag, Text: tag},
	})
	if err != nil {
		s.rt.Fatalf("OpenTurn(prompt %s): %v", tag, err)
	}
	s.observeOpen(e, s.highWater(nil)+1, tag)
	return e.Turn
}

// openStraddler is the agent-initiated turn the straddler arm needs, holding one
// UNSETTLED tool_call: it is closed before the revert, so nothing is owed an aborted
// result and the arm asserts that nothing writes one.
func (s *revertScene) openStraddler() string {
	e, err := s.log.OpenTurn(s.ctx, &TurnSpec{Source: marotte.TurnOpenNameWireTurnStart})
	if err != nil {
		s.rt.Fatalf("OpenTurn(the straddler): %v", err)
	}
	s.observeOpen(e, s.highWater(nil)+1, "straddler")
	if err := s.log.Append(s.ctx, entryOf(e.Turn, "", "call-straddler", marotte.EntryKindToolCall,
		marotte.EntryToolCall{ID: "call-straddler", Status: marotte.ToolInProgress})); err != nil {
		s.rt.Fatalf("append the straddler's tool_call: %v", err)
	}
	return e.Turn
}

// observeOpen records the turn the log just opened AND asserts its ordinal: every
// turn takes the surviving high-water plus one, which after a revert is the reverted
// turn's own n and after a no-survivor revert is 1.
func (s *revertScene) observeOpen(e *marotte.Entry, want uint64, tag string) {
	var open marotte.EntryTurnOpen
	if err := json.Unmarshal(e.Payload, &open); err != nil {
		s.rt.Fatalf("parse the turn_open of %s: %v", tag, err)
	}
	if open.N != want {
		s.rt.Fatalf("the turn opened for %s after %d revert(s) took n %d, want the surviving high-water plus one, %d",
			tag, len(s.records), open.N, want)
	}
	s.order = append(s.order, e.Turn)
	s.ordinal[e.Turn] = open.N
}

func (s *revertScene) appendBody(turn, id string) {
	if err := s.log.Append(s.ctx, entryOf(turn, "", id, marotte.EntryKindText,
		marotte.EntryText{Text: id})); err != nil {
		s.rt.Fatalf("append %s to turn %s: %v", id, turn, err)
	}
}

func (s *revertScene) closeTurn(turn string) {
	if err := s.log.Append(s.ctx, entryOf(turn, "", turn+":close", marotte.EntryKindTurnClose,
		marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})); err != nil {
		s.rt.Fatalf("close turn %s: %v", turn, err)
	}
}

func (s *revertScene) isReverted(turn string) bool {
	_, gone := s.reverted[turn]
	return gone
}

// survivingOrder is the model's own surviving view: file order with every turn the
// rule marked dropped.
func (s *revertScene) survivingOrder() []string {
	order := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if !s.isReverted(id) {
			order = append(order, id)
		}
	}
	return order
}

// newestOutside is the carrier rule computed by the model: the newest turn in FILE
// order that is neither in this window nor already taken by an earlier record.
func (s *revertScene) newestOutside(window map[string]struct{}) (string, bool) {
	for _, id := range slices.Backward(s.order) {
		if _, in := window[id]; in || s.isReverted(id) {
			continue
		}
		return id, true
	}
	return "", false
}

func (s *revertScene) highWater(window map[string]struct{}) uint64 {
	var high uint64
	for _, id := range s.order {
		if _, in := window[id]; in || s.isReverted(id) {
			continue
		}
		if n := s.ordinal[id]; n > high {
			high = n
		}
	}
	return high
}

func (s *revertScene) fileBytes() []byte {
	data, err := os.ReadFile(filepath.Join(s.root, entriesFileName))
	if err != nil {
		s.rt.Fatalf("read the log: %v", err)
	}
	return data
}

// revert drives one revert and asserts the arms that are about the WRITE: the carrier
// is the newest survivor against the union of every window, a minted carrier is n = 1
// and CLOSED, the record states its own window, and the file is append-only.
func (s *revertScene) revert(idx int) {
	from := s.order[idx]
	before := s.fileBytes()
	window := make(map[string]struct{}, len(s.order)-idx)
	for _, id := range s.order[idx:] {
		window[id] = struct{}{}
	}
	through := s.order[len(s.order)-1]
	wantCarrier, held := s.newestOutside(window)
	wantN := s.highWater(window) + 1

	record, opened, err := s.log.Revert(s.ctx, from, marotte.TurnRevertCauseRewind, "kas-"+from)
	if err != nil {
		s.rt.Fatalf("Revert(%q): %v", from, err)
	}
	if held {
		if opened != nil {
			s.rt.Fatalf("Revert(%q) minted carrier %q, want the surviving turn %q", from, opened.Turn, wantCarrier)
		}
	} else {
		if opened == nil {
			s.rt.Fatalf("Revert(%q) minted no carrier, want one: its window takes every surviving turn", from)
		}
		s.observeOpen(opened, wantN, "the carrier")
		if n := s.ordinal[opened.Turn]; n != 1 {
			s.rt.Fatalf("the no-survivor carrier took n %d, want 1 over a log of %d turns — never k+1",
				n, len(s.order)-1)
		}
		st := s.log.turns[opened.Turn]
		switch {
		case st.source != marotte.TurnOpenNameRevert:
			s.rt.Fatalf("the minted carrier's source is %q, want %q", st.source, marotte.TurnOpenNameRevert)
		case !st.closed:
			s.rt.Fatalf("the minted carrier is open, want it CLOSED before the record exists")
		case st.outcome != marotte.TurnOutcomeCompleted:
			s.rt.Fatalf("the minted carrier closed %q, want %q", st.outcome, marotte.TurnOutcomeCompleted)
		case st.unterminated:
			s.rt.Fatal("the minted carrier closed unterminated, which IS the reconcile signal")
		}
		wantCarrier = opened.Turn
	}
	if record.Turn != wantCarrier {
		s.rt.Fatalf("Revert(%q) filed its record in turn %q, want the newest survivor %q", from, record.Turn, wantCarrier)
	}
	if want := from + ":revert"; record.ID != want {
		s.rt.Fatalf("the record's id is %q, want %q", record.ID, want)
	}
	var payload marotte.EntryTurnRevert
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		s.rt.Fatalf("parse the turn_revert of %q: %v", from, err)
	}
	if payload.From != from || payload.FromN != s.ordinal[from] || payload.Through != through ||
		payload.Cause != marotte.TurnRevertCauseRewind {
		s.rt.Fatalf("the record states {from %q, from_n %d, through %q, cause %q}, want {%q, %d, %q, %q}",
			payload.From, payload.FromN, payload.Through, payload.Cause,
			from, s.ordinal[from], through, marotte.TurnRevertCauseRewind)
	}
	for id := range window {
		if id != record.Turn {
			s.reverted[id] = struct{}{}
		}
	}
	s.records = append(s.records, recordedRevert{id: record.ID, carrier: record.Turn})
	if after := s.fileBytes(); !bytes.HasPrefix(after, before) {
		s.rt.Fatalf("Revert(%q) left %d bytes over %d, and the bytes before it are not a prefix: a revert appends and cuts nothing",
			from, len(after), len(before))
	}
}

// betweenTurns is the between-turns arm: after a revert the newest turn in FILE order is
// inside the window, so a lane-less append must land in the newest SURVIVING turn and
// continue THAT turn's seq.
func (s *revertScene) betweenTurns(tag string) {
	turn, ok := s.newestOutside(nil)
	if !ok {
		s.rt.Fatalf("%s: the model holds no surviving turn, which the carrier rule makes impossible", tag)
	}
	before, _ := s.log.NewestSeq(turn)
	e := entryOf("", "", "compaction-"+tag, marotte.EntryKindCompaction, marotte.EntryCompaction{Summary: tag})
	opened, err := s.log.AppendBetweenTurns(s.ctx, e)
	if err != nil {
		s.rt.Fatalf("%s: AppendBetweenTurns: %v", tag, err)
	}
	if opened != nil {
		s.rt.Fatalf("%s: AppendBetweenTurns minted turn %q, want the surviving %q: every revert leaves its carrier",
			tag, opened.Turn, turn)
	}
	if e.Turn != turn {
		s.rt.Fatalf("%s: the between-turns entry landed in turn %q, want the newest SURVIVING turn %q", tag, e.Turn, turn)
	}
	if want := before + 1; e.Seq != want {
		s.rt.Fatalf("%s: the between-turns entry took seq %d, want %d, continuing turn %q's own", tag, e.Seq, want, turn)
	}
	entries, err := s.log.All()
	if err != nil {
		s.rt.Fatalf("%s: All(): %v", tag, err)
	}
	if !holdsEntryID(entries, e.ID) {
		s.rt.Fatalf("%s: All() does not hold the between-turns entry %q, so it landed where no reader looks", tag, e.ID)
	}
	w, err := s.log.Window(len(s.order)+1, "")
	if err != nil {
		s.rt.Fatalf("%s: Window: %v", tag, err)
	}
	if !holdsEntryID(w.Entries, e.ID) {
		s.rt.Fatalf("%s: Window does not hold the between-turns entry %q", tag, e.ID)
	}
}

// check is every READ surface against the model, live and after a reopen alike.
func (s *revertScene) check(when string) {
	surviving := s.survivingOrder()
	entries, err := s.log.All()
	if err != nil {
		s.rt.Fatalf("%s: All(): %v", when, err)
	}
	if got := turnsIn(entries); !slices.Equal(got, surviving) {
		s.rt.Fatalf("%s: All() holds turns %v, want the surviving view %v", when, got, surviving)
	}
	// Every generated turn is drawn — a prompt by its source, the straddler by its
	// tool_call, a carrier by the record it carries — so the rail IS the surviving view.
	if got := railIDs(s.log); !slices.Equal(got, surviving) {
		s.rt.Fatalf("%s: RailRows() names %v, want %v", when, got, surviving)
	}
	count, _ := s.log.Counters()
	if want := uint64(len(surviving)); count != want {
		s.rt.Fatalf("%s: turn_count is %d, want the surviving count %d over %v", when, count, want, surviving)
	}
	if len(surviving) > 0 {
		if want := s.ordinal[surviving[len(surviving)-1]]; count != want {
			s.rt.Fatalf("%s: turn_count is %d, want the newest survivor's own n %d", when, count, want)
		}
	}
	if open := s.log.openTurnsLocked(); len(open) > 0 {
		s.rt.Fatalf("%s: openTurnsLocked() names %v, want none: every generated turn is closed and no carrier is left open",
			when, open)
	}
	w, err := s.log.Window(len(s.order)+1, "")
	if err != nil {
		s.rt.Fatalf("%s: Window: %v", when, err)
	}
	if got := turnsIn(w.Entries); !slices.Equal(got, surviving) {
		s.rt.Fatalf("%s: Window holds turns %v, want the surviving view %v", when, got, surviving)
	}
	if w.HasMore {
		s.rt.Fatalf("%s: HasMore is true over a page of every one of the %d surviving turns", when, len(surviving))
	}
	for id := range s.reverted {
		if _, err := s.log.TurnRange(id, 0); !errors.Is(err, ErrTurnNotInLog) {
			s.rt.Fatalf("%s: TurnRange(%q) = %v, want ErrTurnNotInLog for a reverted turn", when, id, err)
		}
		if _, err := s.log.Window(1, id); err == nil {
			s.rt.Fatalf("%s: Window(1, %q) refused nothing, want the refusal the chat route renders as 400", when, id)
		}
	}
	for _, id := range surviving {
		if _, err := s.log.TurnRange(id, 0); err != nil {
			s.rt.Fatalf("%s: TurnRange(%q) = %v, want the surviving turn's own entries", when, id, err)
		}
	}
	all, reverted, err := s.log.AllWithReverted()
	if err != nil {
		s.rt.Fatalf("%s: AllWithReverted(): %v", when, err)
	}
	if got := turnsIn(all); !slices.Equal(got, s.order) {
		s.rt.Fatalf("%s: AllWithReverted() holds turns %v, want every turn %v", when, got, s.order)
	}
	if !maps.Equal(reverted, s.reverted) {
		s.rt.Fatalf("%s: the index marks %v reverted, want the rule's own set %v", when, reverted, s.reverted)
	}
	// Every record whose CARRIER survives is visible, and the newest one always is: a
	// carrier chosen against this window alone files the second record into a turn the
	// first took, where no reader ever meets it.
	seen := make(map[string]struct{}, len(s.records))
	for _, e := range entries {
		if e.Kind == marotte.EntryKindTurnRevert {
			seen[e.ID] = struct{}{}
		}
	}
	want := make(map[string]struct{}, len(s.records))
	for _, r := range s.records {
		if !s.isReverted(r.carrier) {
			want[r.id] = struct{}{}
		}
	}
	if !maps.Equal(seen, want) {
		s.rt.Fatalf("%s: All() holds records %v, want every record whose carrier survives, %v", when, seen, want)
	}
	newest := s.records[len(s.records)-1]
	if _, ok := seen[newest.id]; !ok {
		s.rt.Fatalf("%s: All() does not hold the newest record %q, whose carrier %q survives by construction",
			when, newest.id, newest.carrier)
	}
	if id, ok := s.log.NewestRevert(); !ok || id != newest.id {
		s.rt.Fatalf("%s: NewestRevert() = (%q, %v), want the last record in FILE order %q", when, id, ok, newest.id)
	}
}

// checkStraddlerSurvivedWhole is property 6's STRADDLER arm, and every clause is an
// absence: the turn a revert reached across survives with its own three entries, its
// own closer, no aborted result and no second closer.
func (s *revertScene) checkStraddlerSurvivedWhole(when, turn string) {
	entries, err := s.log.TurnRange(turn, 0)
	if err != nil {
		s.rt.Fatalf("%s: TurnRange(the straddler %q) = %v, want it whole", when, turn, err)
	}
	kinds := make([]marotte.EntryKind, 0, len(entries))
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
	}
	own := []marotte.EntryKind{marotte.EntryKindTurnOpen, marotte.EntryKindToolCall, marotte.EntryKindTurnClose}
	if len(kinds) < len(own) || !slices.Equal(kinds[:len(own)], own) {
		s.rt.Fatalf("%s: the straddler holds %v, want its own %v first", when, kinds, own)
	}
	var closers int
	for _, e := range entries {
		switch e.Kind {
		case marotte.EntryKindToolResult:
			s.rt.Fatalf("%s: the straddler holds tool_result %q, want its unsettled call left alone: a revert settles nothing",
				when, e.ID)
		case marotte.EntryKindTurnClose:
			closers++
			var footer marotte.EntryTurnClose
			if err := json.Unmarshal(e.Payload, &footer); err != nil {
				s.rt.Fatalf("%s: parse the straddler's turn_close: %v", when, err)
			}
			if footer.Outcome != marotte.TurnOutcomeCompleted || footer.StopReasonRaw != "" {
				s.rt.Fatalf("%s: the straddler's closer is (%q, %q), want its own (%q, \"\"): none is synthesized for a turn a revert did not take",
					when, footer.Outcome, footer.StopReasonRaw, marotte.TurnOutcomeCompleted)
			}
		}
	}
	if closers != 1 {
		s.rt.Fatalf("%s: the straddler holds %d turn_close entries, want exactly its own", when, closers)
	}
}

// checkNoSynthesizedCloser reads the FILE rather than the index: a rewind leaves no
// closer behind anywhere, which is why it raises no reconcile signal.
func (s *revertScene) checkNoSynthesizedCloser(when string) {
	for _, line := range readLogLines(s.rt, filepath.Join(s.root, entriesFileName)) {
		var e marotte.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			s.rt.Fatalf("%s: parse a log line: %v", when, err)
		}
		if e.Kind != marotte.EntryKindTurnClose {
			continue
		}
		var footer marotte.EntryTurnClose
		if err := json.Unmarshal(e.Payload, &footer); err != nil {
			s.rt.Fatalf("%s: parse the turn_close of %q: %v", when, e.Turn, err)
		}
		if footer.StopReasonRaw == string(marotte.StopReasonUnterminated) {
			s.rt.Fatalf("%s: turn %q holds a synthesized closer, want none for any turn a revert reached", when, e.Turn)
		}
	}
}

func holdsEntryID(entries []marotte.Entry, id string) bool {
	return slices.ContainsFunc(entries, func(e marotte.Entry) bool { return e.ID == id })
}
