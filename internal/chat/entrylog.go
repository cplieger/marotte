package chat

// The per-log entry store: one append-only JSONL file of marotte.Entry lines under a root the caller names
// (chats/<id>/ or runs/<id>/). Header rules go through the root's LogHeader; a run root supplies noHeader. One
// write(2) and sync per seal keeps the log prefix-consistent, so a torn tail is one partial line the next open drops.
// The offset index is never persisted.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// entriesFileName is the log; headerFileName the record beside it, on chat roots only.
const (
	entriesFileName = "entries.jsonl"
	headerFileName  = "chat.json"
)

// maxEntryLineBytes bounds one entry's line, even when the whole-log cap is unlimited. The tool bounds already cap
// output and diffs, so a longer line is a bug or a reshaped volume.
const maxEntryLineBytes = 16 << 20

// entryReadBuf is the line reader's buffer; longer lines are assembled across reads.
const entryReadBuf = 64 << 10

// syncEntries is the durability barrier after each appended line; a var so the crash property can fail the k-th.
var syncEntries = func(f *os.File) error { return f.Sync() }

// readEntries is the surface window reads pread against; a var because the per-range read cost is invisible in the
// entries returned.
var readEntries = func(f *os.File) io.ReaderAt { return f }

// errEntryLineTooLong reports a line over maxEntryLineBytes, on read and write alike.
var errEntryLineTooLong = errors.New("chat: entry line exceeds the per-entry cap")

// errEntryLogFailed is the latch a write error leaves: every later append to this log is refused until restart.
var errEntryLogFailed = errors.New("chat: entry log refused further appends after a write error")

// entryCall is one open tool_call in the index: its entry id and its result's lane, fixed at create.
type entryCall struct {
	id   string
	lane string
}

// byteRange is one contiguous run of a turn's own lines, [from, to).
type byteRange struct {
	from int64
	to   int64
}

// revertWindow is one turn_revert record: its carrier and the from..through window it takes. Resolved once the index
// is complete, because a record can sit below the turns it names (markRevertedLocked).
type revertWindow struct {
	carrier string
	from    string
	through string
}

// turnState is the offset index's row for one turn: its byte ranges and the facts window reads, rail rows, the
// reconcile predicate and a synthesized closer need without reopening a payload.
type turnState struct {
	results   map[string]struct{}
	firstLine string
	source    marotte.TurnOpenSourceName
	outcome   marotte.TurnOutcome
	calls     []entryCall
	// ranges are the turn's lines in file order, one per contiguous run: turns can be open together and the merge files
	// between-turns entries behind later turns, so a turn is not one span.
	ranges    []byteRange
	elapsedMs float64
	openTs    int64
	lastTs    int64
	n         uint64
	nextSeq   uint64
	plans     int
	drawn     bool
	closed    bool
	carrier   bool
	// reverted means a turn_revert took this turn, so every read surface skips it. The lines stay on disk.
	reverted bool
	// hasRevert is whether the turn holds its own turn_revert record, which tells a complete carrier from the crash state
	// a minted carrier can leave.
	hasRevert bool
	// unterminated is this turn's turn_close carrying the synthesized closer's stop reason, the crash's reconcile signal,
	// precomputed so the predicate is a map read.
	unterminated bool
	// emptySteer is a steer of this turn with no text: words KAS persisted that this process never received.
	emptySteer bool
}

// unsettled are the turn's tool_calls with no tool_result, in open order, which a close aborts them in.
func (st *turnState) unsettled() []entryCall {
	var out []entryCall
	for _, c := range st.calls {
		if _, done := st.results[marotte.ToolResultID(c.id)]; !done {
			out = append(out, c)
		}
	}
	return out
}

// LogHeader is the header policy a log's root supplies: chat.json for a chat, noHeader for a run, so the log needs no
// nil checks.
type LogHeader interface {
	// Counters caches the log's turn_count and last_turn_outcome; the log's values correct a header left behind by a
	// crash.
	Counters(ctx context.Context, turnCount uint64, last marotte.TurnOutcome) error
	// CloserModel is the model a synthesized closer stamps: turn_open carries none, so the header is the only honest
	// source for a later process.
	CloserModel() string
	// Reconcilable reports whether this root's header can name lost history. A run root has no session, so its
	// NeedsReconcile() is false.
	Reconcilable() bool
	// SessionID is the ACP session this root's header binds, "" for none: the half of reconcile condition (i) the log
	// cannot hold. A run root answers "".
	SessionID() string
}

// noHeader is the policy of a root with no header: nothing to cache, stamp or reconcile against.
type noHeader struct{}

// NoHeader is the header policy a run root supplies.
func NoHeader() LogHeader { return noHeader{} }

func (noHeader) Counters(context.Context, uint64, marotte.TurnOutcome) error { return nil }
func (noHeader) CloserModel() string                                         { return "" }
func (noHeader) Reconcilable() bool                                          { return false }
func (noHeader) SessionID() string                                           { return "" }

// EntryLog is one log root's append log and its in-memory offset index. Every exported operation takes the log's
// mutex, so seq is contiguous per turn. A caller sequencing a lifecycle decision takes its own mutex first and never
// holds one across a call here.
type EntryLog struct {
	f     *os.File
	turns map[string]*turnState
	// header is this root's header policy, never nil: a run root's is noHeader.
	header LogHeader
	failed error
	root   string
	// reconciledTurns and reconciledSessions are what reconciled records name: the clearers of the reconcile conditions.
	reconciledTurns    map[string]struct{}
	reconciledSessions map[string]struct{}
	// boundSessions are the sessions this log's turn_bind entries name, telling a self-minted session from a resumed one.
	boundSessions map[string]struct{}
	// newestRevert is the id of the last turn_revert met in file order: the provenance the merge's discard gate reads.
	newestRevert string
	// unappliedReverts are windows met but not resolved, drained once their order is complete: after the scan, and after
	// an append to a built index.
	unappliedReverts []revertWindow
	order            []string
	size             int64
	cap              chatFileCap
	mu               sync.Mutex
	removed          bool
}

// EntryLogOption configures an EntryLog at open. The header policy is positional on OpenEntryLog instead, so it
// cannot be forgotten.
type EntryLogOption func(*EntryLog)

// WithEntryFileCap sets the whole-log byte cap; n <= 0 is unlimited. The chat store derives it from the memory limit
// once per process.
func WithEntryFileCap(n int64) EntryLogOption {
	return func(l *EntryLog) { l.cap = chatFileCap(n) }
}

// OpenEntryLog opens the log under root, building the offset index in one envelope-only scan, dropping an unreadable
// tail, and closing every turn left open before serving anything. A missing log is a fresh chat: empty index, no
// descriptor until the first append. h is positional; a run root passes NoHeader().
func OpenEntryLog(ctx context.Context, root string, h LogHeader, opts ...EntryLogOption) (*EntryLog, error) {
	l := &EntryLog{root: root, header: h, turns: make(map[string]*turnState)}
	for _, opt := range opts {
		opt(l)
	}
	//nolint:gosec,nolintlint // G703: the path is <root>/<id>/... over an id a door already admitted (ids.ValidChatID for a chat, runLog.dir for a run)
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, fmt.Errorf("entry log: mkdir %s: %w", root, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rescanLocked(); err != nil {
		return nil, err
	}
	if open := l.openTurnsLocked(); len(open) > 0 {
		if err := l.closeUnterminatedLocked(ctx, open); err != nil {
			return nil, err
		}
	}
	if len(l.order) > 0 {
		if err := l.writeCountersLocked(ctx); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// path is the log file.
func (l *EntryLog) path() string { return filepath.Join(l.root, entriesFileName) }

// Close releases the write descriptor; the log stays readable and a later append reopens it.
func (l *EntryLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeDescriptorLocked()
}

// Remove closes the descriptor and refuses later appends with ErrTombstoned, so a turn folding into a removed chat
// cannot recreate its log. Unlinking the directory is the caller's.
func (l *EntryLog) Remove() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.removed = true
	return l.closeDescriptorLocked()
}

func (l *EntryLog) closeDescriptorLocked() error {
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close()
}

// TurnSpec is what opening a turn needs beyond the log's own bookkeeping: n is assigned and the id minted here, so
// the registry never holds a turn the log lacks.
type TurnSpec struct {
	Prompt    *marotte.EntryPrompt
	Source    marotte.TurnOpenSourceName
	Run       string
	NodePath  string
	SessionID string
}

// OpenTurn appends a turn_open and returns it; its ID is the turn id every later entry and closer carries. n is the
// newest turn's plus one, fixed at open.
func (l *EntryLog) OpenTurn(ctx context.Context, spec *TurnSpec) (*marotte.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.openTurnLocked(ctx, spec)
}

func (l *EntryLog) openTurnLocked(ctx context.Context, spec *TurnSpec) (*marotte.Entry, error) {
	return l.openTurnWithOrdinalLocked(ctx, spec, l.turnCountLocked()+1)
}

// openTurnWithOrdinalLocked opens a turn at a stated ordinal, for the revert carrier, whose ordinal is the high-water
// after the window it hides.
func (l *EntryLog) openTurnWithOrdinalLocked(ctx context.Context, spec *TurnSpec, ordinal uint64) (*marotte.Entry, error) {
	raw, err := json.Marshal(marotte.EntryTurnOpen{
		Prompt:    spec.Prompt,
		Source:    spec.Source,
		Run:       spec.Run,
		NodePath:  spec.NodePath,
		SessionID: spec.SessionID,
		N:         ordinal,
	})
	if err != nil {
		return nil, fmt.Errorf("entry log: marshal turn_open: %w", err)
	}
	id := "t-" + ids.New(16, ids.StdLower)
	e := &marotte.Entry{ID: id, Turn: id, Kind: marotte.EntryKindTurnOpen, Payload: raw}
	if err := l.appendLocked(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Append persists one sealed entry: it assigns seq (contiguous per turn, turn_open is 0), stamps ts, writes and
// syncs. It is turnlog's Sink; the descriptor is held for the process's life.
func (l *EntryLog) Append(ctx context.Context, e *marotte.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(ctx, e)
}

// AppendBetweenTurns files a lane-less entry belonging to no open turn after the newest surviving turn's turn_close,
// with its next seq. With no survivor it mints a closed turn_open{source: event} carrier and returns its turn_open and
// turn_close for the caller to announce before e; nil otherwise. Surviving, not newest in file order: after a revert
// the newest is inside the reverted window, where no read surface looks.
func (l *EntryLog) AppendBetweenTurns(ctx context.Context, e *marotte.Entry) (minted []*marotte.Entry, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	turn, ok := l.newestSurvivingLocked(nil)
	if !ok {
		minted, err = l.mintCarrierLocked(ctx, marotte.TurnOpenNameEvent, l.turnCountLocked()+1)
		if err != nil {
			return minted, err
		}
		turn = minted[0].Turn
	}
	e.Turn = turn
	return minted, l.appendLocked(ctx, e)
}

// AppendReconciled records that a merge examined one signal and had nothing to add, the only honest way to clear it,
// and returns the record plus any carrier it minted (turn_open, turn_close). Exactly one of rec's fields is set: Turn
// (unterminated closer, empty steer) or Session. The turn form files into the named turn. The session form uses the
// newest surviving turn, or mints a closed event carrier, never a revert one.
func (l *EntryLog) AppendReconciled(ctx context.Context, rec marotte.EntryReconciled) (record *marotte.Entry, minted []*marotte.Entry, err error) {
	if (rec.Turn == "") == (rec.Session == "") {
		return nil, nil, fmt.Errorf("entry log: reconciled{turn: %q, session: %q}, want exactly one of them set",
			rec.Turn, rec.Session)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	turn := rec.Turn
	if turn == "" {
		var held bool
		if turn, held = l.newestSurvivingLocked(nil); !held {
			minted, err = l.mintCarrierLocked(ctx, marotte.TurnOpenNameEvent, l.turnCountLocked()+1)
			if err != nil {
				return nil, minted, err
			}
			turn = minted[0].Turn
		}
	}
	payload, merr := json.Marshal(rec)
	if merr != nil {
		return nil, minted, fmt.Errorf("entry log: marshal reconciled: %w", merr)
	}
	record = &marotte.Entry{
		ID: ReconciledEntryID(rec), Turn: turn,
		Kind: marotte.EntryKindReconciled, Payload: payload,
	}
	if aerr := l.appendLocked(ctx, record); aerr != nil {
		return nil, minted, aerr
	}
	return record, minted, nil
}

// ReconciledEntryID derives the record's id from the signal it clears, so a repeat merge mints the existing id
// rather than an insertion. Turn ids are this log's, session ids KAS's, so they cannot collide. Exported because the
// merge's rewrite files its own records.
func ReconciledEntryID(rec marotte.EntryReconciled) string {
	if rec.Turn != "" {
		return rec.Turn + ":reconciled"
	}
	return rec.Session + ":reconciled"
}

// Revert appends this log's record of a rewind and returns it, plus a minted carrier's turn_open and turn_close for
// the caller to announce first. from is the reverted turn, resolved from the surviving view. Nothing is cut or
// re-closed: the range stays on disk, reads skip it, and a failed append changes nothing.
func (l *EntryLog) Revert(ctx context.Context, from string, cause marotte.TurnRevertCause, kasMessageID string) (record *marotte.Entry, minted []*marotte.Entry, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendRevertLocked(ctx, from, cause, kasMessageID)
}

// appendRevertLocked runs the revert's ordered steps: compute the window; pick a carrier outside it and outside
// earlier reverts; with none, mint a closed carrier; append the record with its Through; and mark the index after the
// write returns. Not AppendBetweenTurns, whose newest survivor is inside this window.
func (l *EntryLog) appendRevertLocked(ctx context.Context, from string, cause marotte.TurnRevertCause, kasMessageID string) (record *marotte.Entry, minted []*marotte.Entry, err error) {
	st := l.turns[from]
	if st == nil || st.reverted {
		return nil, nil, fmt.Errorf("%w: %q", ErrTurnNotInLog, from)
	}
	window, through := l.revertWindowLocked(from)
	carrier, held := l.newestSurvivingLocked(window)
	if !held {
		// The ordinal the window leaves behind, so the carrier shares no n with a surviving turn.
		minted, err = l.mintCarrierLocked(ctx, marotte.TurnOpenNameRevert, l.survivingHighWaterLocked(window)+1)
		// A carrier without its own record is what the scan marks reverted, so a failed revert leaves the same view in
		// memory as the next open would.
		if len(minted) > 0 {
			defer func() {
				if err != nil {
					l.turns[minted[0].Turn].reverted = true
				}
			}()
		}
		if err != nil {
			return nil, minted, err
		}
		carrier = minted[0].Turn
	}
	payload, merr := json.Marshal(marotte.EntryTurnRevert{
		From: from, FromN: st.n, Through: through,
		KASMessageID: kasMessageID, Cause: cause,
	})
	if merr != nil {
		return nil, minted, fmt.Errorf("entry log: marshal turn_revert: %w", merr)
	}
	record = &marotte.Entry{
		ID: from + ":revert", Turn: carrier,
		Kind: marotte.EntryKindTurnRevert, Payload: payload,
	}
	if aerr := l.appendLocked(ctx, record); aerr != nil {
		return nil, minted, aerr
	}
	return record, minted, nil
}

// revertWindowLocked is the window a revert to from takes: every turn from it to the newest in file order, with the
// newest turn's id as the Through the record states.
func (l *EntryLog) revertWindowLocked(from string) (window map[string]struct{}, through string) {
	first := slices.Index(l.order, from)
	window = make(map[string]struct{}, len(l.order)-first)
	for _, id := range l.order[first:] {
		window[id] = struct{}{}
	}
	return window, l.order[len(l.order)-1]
}

// survivingHighWaterLocked is the highest n among turns surviving excluding, the ordinal the next ordinary turn
// takes; zero when none survive.
func (l *EntryLog) survivingHighWaterLocked(excluding map[string]struct{}) uint64 {
	var high uint64
	for _, id := range l.order {
		if _, in := excluding[id]; in || l.turns[id].reverted {
			continue
		}
		if n := l.turns[id].n; n > high {
			high = n
		}
	}
	return high
}

// newestSurvivingLocked is the newest turn in file order no revert took, also skipping excluding (a window not yet
// marked). False when none survives. The one rule for where a lane-less entry belongs.
func (l *EntryLog) newestSurvivingLocked(excluding map[string]struct{}) (string, bool) {
	for _, id := range slices.Backward(l.order) {
		if _, in := excluding[id]; in || l.turns[id].reverted {
			continue
		}
		return id, true
	}
	return "", false
}

// mintCarrierLocked opens a turn to hold entries no surviving turn can, and closes it before anything lands in it:
// left open, the store-open closer would synthesize it unterminated (a reconcile no session can answer), and a client
// would read it as a running turn. It returns what it wrote: [turn_open, turn_close], or [turn_open] when the close
// failed.
func (l *EntryLog) mintCarrierLocked(ctx context.Context, source marotte.TurnOpenSourceName, ordinal uint64) ([]*marotte.Entry, error) {
	opened, err := l.openTurnWithOrdinalLocked(ctx, &TurnSpec{Source: source}, ordinal)
	if err != nil {
		return nil, err
	}
	closed, err := l.closeCarrierLocked(ctx, opened.Turn)
	if err != nil {
		return []*marotte.Entry{opened}, err
	}
	return []*marotte.Entry{opened, closed}, nil
}

// closeCarrierLocked closes a carrier this log opened: completed and marked carrier, no stop reason, model or
// elapsed. Never synthesizeCloseLocked, whose closer is the reconcile signal.
func (l *EntryLog) closeCarrierLocked(ctx context.Context, turn string) (*marotte.Entry, error) {
	payload, err := json.Marshal(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, Carrier: true})
	if err != nil {
		return nil, fmt.Errorf("entry log: marshal carrier turn_close: %w", err)
	}
	e := &marotte.Entry{
		ID: turn + ":close", Turn: turn,
		Kind: marotte.EntryKindTurnClose, Payload: payload,
	}
	if err := l.appendLocked(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// appendLocked is the one write path. A refused write latches: the seq is not reused, nothing is broadcast, and later
// appends fail until restart. A tool payload is bounded on the caller's entry before encoding, so the broadcast
// carries the bytes on disk.
func (l *EntryLog) appendLocked(ctx context.Context, e *marotte.Entry) error {
	switch {
	case l.removed:
		return ErrTombstoned
	case l.failed != nil:
		return l.failed
	case ctx.Err() != nil:
		return ctx.Err()
	}
	st := l.turns[e.Turn]
	if st == nil && e.Kind != marotte.EntryKindTurnOpen {
		return fmt.Errorf("entry log: %s names turn %q, which this log does not hold", e.Kind, e.Turn)
	}
	if st != nil {
		e.Seq = st.nextSeq
	}
	// An id-less entry was filed between turns; its position is unique within the turn and stable.
	if e.ID == "" {
		e.ID = e.Turn + ":s" + strconv.FormatUint(e.Seq, 10)
	}
	e.Ts = time.Now().UnixMilli()
	persistBoundEntry(e)
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("entry log: marshal %s: %w", e.Kind, err)
	}
	line = append(line, '\n')
	if err := l.checkBoundsLocked(int64(len(line))); err != nil {
		return err
	}
	if err := l.writeLineLocked(ctx, e, line); err != nil {
		return err
	}
	start := l.size
	l.size += int64(len(line))
	if err := l.observeLocked(e, start, l.size); err != nil {
		return err
	}
	// The index is complete here, so the window resolves at once through the one drain.
	l.applyRevertsLocked()
	return nil
}

// checkBoundsLocked refuses a line over the per-entry cap, or one taking the log past the whole-log cap, before
// anything is written.
func (l *EntryLog) checkBoundsLocked(lineBytes int64) error {
	if lineBytes > maxEntryLineBytes {
		return fmt.Errorf("%w: %d bytes (max %d)", errEntryLineTooLong, lineBytes, int64(maxEntryLineBytes))
	}
	if l.cap.unlimited() {
		return nil
	}
	capBytes := int64(l.cap)
	if l.size+lineBytes > capBytes {
		return errFileTooLarge("entry log "+l.root, l.size+lineBytes, capBytes)
	}
	if headroom := capBytes - l.size - lineBytes; headroom < capBytes/writeHeadroomFraction {
		slog.Warn("entry log: this log is near the file cap",
			"root", l.root, "size_bytes", l.size+lineBytes, "cap_bytes", capBytes,
			"headroom_bytes", headroom)
	}
	return nil
}

// writeLineLocked writes and syncs one line, latching a failure.
func (l *EntryLog) writeLineLocked(_ context.Context, e *marotte.Entry, line []byte) error {
	f, err := l.descriptorLocked()
	if err == nil {
		_, err = f.Write(line)
	}
	if err == nil {
		err = syncEntries(f)
	}
	if err == nil {
		return nil
	}
	slog.Error("entry log: write failed; this log refuses further appends until the process restarts",
		"root", l.root, "turn", e.Turn, "entry", e.ID, "kind", e.Kind, "error", err)
	l.failed = fmt.Errorf("%w: %w", errEntryLogFailed, err)
	return l.failed
}

// descriptorLocked opens the log for appending on first use, creating it.
func (l *EntryLog) descriptorLocked() (*os.File, error) {
	if l.f != nil {
		return l.f, nil
	}
	//nolint:gosec,nolintlint // G703: the path is <root>/<id>/... over an id a door already admitted (ids.ValidChatID for a chat, runLog.dir for a run)
	f, err := os.OpenFile(l.path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, fileMode)
	if err != nil {
		return nil, err
	}
	l.f = f
	return f, nil
}

// observeLocked folds one entry into the offset index for both the open scan and appends, so they build the same
// index.
func (l *EntryLog) observeLocked(e *marotte.Entry, start, end int64) error {
	if e.Kind == marotte.EntryKindTurnOpen {
		st, err := newTurnState(e, start, end)
		if err != nil {
			return err
		}
		l.turns[e.Turn] = st
		l.order = append(l.order, e.Turn)
		return nil
	}
	st := l.turns[e.Turn]
	if st == nil {
		return fmt.Errorf("entry log: %s names turn %q, which this log does not hold", e.Kind, e.Turn)
	}
	st.extend(e, start, end)
	switch e.Kind {
	case marotte.EntryKindToolCall:
		st.calls = append(st.calls, entryCall{id: e.ID, lane: e.Lane})
	case marotte.EntryKindToolResult:
		st.results[e.ID] = struct{}{}
	case marotte.EntryKindPlan:
		st.plans++
	case marotte.EntryKindTurnClose:
		return foldTurnClose(st, e)
	case marotte.EntryKindSteer:
		return foldSteer(st, e)
	case marotte.EntryKindTurnBind:
		return l.foldTurnBindLocked(e)
	case marotte.EntryKindReconciled:
		return l.foldReconciledLocked(e)
	case marotte.EntryKindTurnRevert:
		return l.foldTurnRevertLocked(st, e)
	}
	return nil
}

// extend folds e's envelope into st: byte range, timestamp, next seq and whether it draws the turn. Before the
// kind's fold, because drawnBy reads the plans counted so far.
func (st *turnState) extend(e *marotte.Entry, start, end int64) {
	if last := len(st.ranges) - 1; st.ranges[last].to == start {
		st.ranges[last].to = end
	} else {
		st.ranges = append(st.ranges, byteRange{from: start, to: end})
	}
	st.lastTs = e.Ts
	if e.Seq >= st.nextSeq {
		st.nextSeq = e.Seq + 1
	}
	if !st.drawn && drawnBy(e, st.plans) {
		st.drawn = true
	}
}

// decodeEntry parses e's payload as T, naming the kind and the turn on failure.
func decodeEntry[T any](e *marotte.Entry) (T, error) {
	var payload T
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return payload, fmt.Errorf("entry log: parse %s of %q: %w", e.Kind, e.Turn, err)
	}
	return payload, nil
}

func foldTurnClose(st *turnState, e *marotte.Entry) error {
	payload, err := decodeEntry[marotte.EntryTurnClose](e)
	if err != nil {
		return err
	}
	st.closed = true
	st.outcome = payload.Outcome
	st.elapsedMs = payload.ElapsedMs
	st.carrier = payload.Carrier
	st.unterminated = payload.StopReasonRaw == string(marotte.StopReasonUnterminated)
	return nil
}

func foldSteer(st *turnState, e *marotte.Entry) error {
	payload, err := decodeEntry[marotte.EntrySteer](e)
	if err != nil {
		return err
	}
	if payload.Text == "" {
		st.emptySteer = true
	}
	return nil
}

func (l *EntryLog) foldTurnBindLocked(e *marotte.Entry) error {
	payload, err := decodeEntry[marotte.EntryTurnBind](e)
	if err != nil {
		return err
	}
	if payload.SessionID != "" {
		l.boundSessions[payload.SessionID] = struct{}{}
	}
	return nil
}

func (l *EntryLog) foldReconciledLocked(e *marotte.Entry) error {
	payload, err := decodeEntry[marotte.EntryReconciled](e)
	if err != nil {
		return err
	}
	if payload.Turn != "" {
		l.reconciledTurns[payload.Turn] = struct{}{}
	}
	if payload.Session != "" {
		l.reconciledSessions[payload.Session] = struct{}{}
	}
	return nil
}

func (l *EntryLog) foldTurnRevertLocked(st *turnState, e *marotte.Entry) error {
	payload, err := decodeEntry[marotte.EntryTurnRevert](e)
	if err != nil {
		return err
	}
	st.hasRevert = true
	l.newestRevert = e.ID
	l.unappliedReverts = append(l.unappliedReverts,
		revertWindow{carrier: e.Turn, from: payload.From, through: payload.Through})
	return nil
}

// applyRevertsLocked resolves every window met since the last drain, in file order. Called only where the order is
// complete: after the scan and after the append that added a record.
func (l *EntryLog) applyRevertsLocked() {
	windows := l.unappliedReverts
	l.unappliedReverts = nil
	for _, w := range windows {
		l.markRevertedLocked(w)
	}
}

// markRevertedLocked applies the skip rule for one record: every turn whose turn_open lies from from through through
// in file order, except the carrier its envelope names. Bounded by the named turns, not this line's offset, and
// resolved against the completed order: a rewrite files the record in its carrier's group, below the window. A window
// naming a turn this log lacks marks nothing.
func (l *EntryLog) markRevertedLocked(w revertWindow) {
	first := slices.Index(l.order, w.from)
	last := slices.Index(l.order, w.through)
	if first < 0 || last < 0 || last < first {
		slog.Warn("entry log: turn_revert names a window this log cannot resolve",
			"root", l.root, "carrier", w.carrier, "from", w.from, "through", w.through)
		return
	}
	for _, id := range l.order[first : last+1] {
		if id == w.carrier {
			continue
		}
		l.turns[id].reverted = true
	}
}

// survivingOrderLocked is l.order without reverted turns, the order every read surface uses; the whole file needs
// AllWithReverted.
func (l *EntryLog) survivingOrderLocked() []string {
	order := make([]string, 0, len(l.order))
	for _, id := range l.order {
		if !l.turns[id].reverted {
			order = append(order, id)
		}
	}
	return order
}

// newTurnState opens the index row for a turn. The scan decodes only six payload kinds, each cheap: turn_open,
// turn_close, turn_revert, reconciled, steer (text only) and turn_bind (session only). Every other payload stays a
// json.RawMessage.
func newTurnState(e *marotte.Entry, start, end int64) (*turnState, error) {
	var payload marotte.EntryTurnOpen
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return nil, fmt.Errorf("entry log: parse turn_open of %q: %w", e.Turn, err)
	}
	st := &turnState{
		results: make(map[string]struct{}),
		source:  payload.Source,
		ranges:  []byteRange{{from: start, to: end}},
		openTs:  e.Ts,
		lastTs:  e.Ts,
		n:       payload.N,
		nextSeq: e.Seq + 1,
		drawn:   drawnSource(payload.Source),
	}
	if payload.Prompt != nil {
		st.firstLine = firstLineOf(payload.Prompt.Text)
	}
	return st, nil
}

// drawnSource reports whether a turn's source draws its own header, giving it a rail row before its body holds
// anything. Its negation is the rail's agent_initiated.
func drawnSource(s marotte.TurnOpenSourceName) bool {
	switch s {
	case marotte.TurnOpenNamePrompt, marotte.TurnOpenNameLocalShell, marotte.TurnOpenNameEmptyRetry:
		return true
	}
	return false
}

// drawnBy reports whether e draws its turn: an entry rendering at its own position in lane "". The sealed half of
// the drawn predicate; open entries never reach the log. The excluded kinds are those the client's entryRenders
// refuses, plus reconciled, which says nothing to a reader. steer_ack and turn_revert draw. plans counts the turn's
// plans so only the first draws.
func drawnBy(e *marotte.Entry, plans int) bool {
	if e.Lane != "" {
		return false
	}
	switch e.Kind {
	case marotte.EntryKindTurnOpen, marotte.EntryKindTurnBind,
		marotte.EntryKindToolResult, marotte.EntryKindTurnClose,
		marotte.EntryKindReconciled:
		return false
	case marotte.EntryKindPlan:
		return plans == 0
	}
	return true
}

// firstLineMax caps first_line so the session index stays small on a long chat.
const firstLineMax = 120

// firstLineOf is a prompt's first line, whitespace-collapsed and capped on a rune boundary, for
// the turn-map preview. static-src/rail-merge.ts firstLine is its twin for a resident turn.
func firstLineOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.Join(strings.Fields(line), " ")
	if utf8.RuneCountInString(line) <= firstLineMax {
		return line
	}
	return string([]rune(line)[:firstLineMax]) + "\u2026"
}

// rescanLocked rebuilds the index in one pass, truncating an unreadable tail, then applies what the pass cannot
// decide line by line: every turn_revert window, and reverting incomplete carriers.
func (l *EntryLog) rescanLocked() error {
	l.turns = make(map[string]*turnState)
	l.reconciledTurns = make(map[string]struct{})
	l.reconciledSessions = make(map[string]struct{})
	l.boundSessions = make(map[string]struct{})
	l.order = nil
	l.newestRevert = ""
	l.unappliedReverts = nil
	l.size = 0
	err := l.scanFileLocked()
	// After an error too: the index is prefix-consistent, and the dropped tail may have held a carrier's record. A window
	// naming a dropped turn marks nothing.
	l.applyRevertsLocked()
	l.markIncompleteCarriersLocked()
	return err
}

// markIncompleteCarriersLocked marks every `source: revert` turn without its own turn_revert: a carrier's crash state.
// Marking it restores the pre-revert view: readers skip it, openTurnsLocked synthesizes no closer for it, and its
// ordinal is never read.
func (l *EntryLog) markIncompleteCarriersLocked() {
	for _, id := range l.order {
		st := l.turns[id]
		if st.source == marotte.TurnOpenNameRevert && !st.hasRevert {
			st.reverted = true
		}
	}
}

// scanFileLocked is rescanLocked's sequential pass over the file.
func (l *EntryLog) scanFileLocked() error {
	//nolint:gosec,nolintlint // G703: the path is <root>/<id>/... over an id a door already admitted (ids.ValidChatID for a chat, runLog.dir for a run)
	f, err := os.Open(l.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("entry log: open %s: %w", l.path(), err)
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, entryReadBuf)
	var off int64
	for {
		line, rerr := readEntryLine(r)
		if len(line) == 0 && errors.Is(rerr, io.EOF) {
			l.size = off
			return nil
		}
		if err := l.observeLineLocked(line, off, rerr); err != nil {
			// The first unreadable line ends the log: an O_APPEND line is whole or a torn tail.
			return l.dropTailLocked(off, err)
		}
		off += int64(len(line))
	}
}

// observeLineLocked decodes one complete line and folds it in, reporting why an unusable line is unusable.
func (l *EntryLog) observeLineLocked(line []byte, off int64, rerr error) error {
	if rerr != nil {
		return rerr
	}
	var e marotte.Entry
	if err := json.Unmarshal(line[:len(line)-1], &e); err != nil {
		return err
	}
	return l.observeLocked(&e, off, off+int64(len(line)))
}

// dropTailLocked truncates the log at the last complete line and logs one Warn; nothing past off was observed.
func (l *EntryLog) dropTailLocked(off int64, cause error) error {
	l.size = off
	slog.Warn("entry log: dropped an unreadable tail at the last complete line",
		"root", l.root, "offset", off, "cause", cause)
	if err := os.Truncate(l.path(), off); err != nil {
		return fmt.Errorf("entry log: truncate torn tail of %s: %w", l.path(), err)
	}
	return nil
}

// readEntryLine reads one line including its newline, bounded by maxEntryLineBytes. A final line without a newline
// returns its bytes with io.EOF: the torn tail.
func readEntryLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if int64(len(buf)+len(chunk)) > maxEntryLineBytes {
			return nil, errEntryLineTooLong
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

// openTurnsLocked are the surviving turns with no turn_close, oldest first, left by a crash or the write-error rule.
// Reverted turns are never re-closed.
func (l *EntryLog) openTurnsLocked() []string {
	var open []string
	for _, id := range l.survivingOrderLocked() {
		if !l.turns[id].closed {
			open = append(open, id)
		}
	}
	return open
}

// closeUnterminatedLocked appends the synthesized closer to each named turn: an aborted tool_result per unsettled
// call in its lane, then turn_close{outcome: interrupted, stop_reason_raw: unterminated}.
func (l *EntryLog) closeUnterminatedLocked(ctx context.Context, turns []string) error {
	model := l.header.CloserModel()
	for _, id := range turns {
		if err := l.synthesizeCloseLocked(ctx, id, model); err != nil {
			return err
		}
	}
	slog.Warn("entry log: closed turns no live process holds",
		"root", l.root, "turns", len(turns))
	return nil
}

// synthesizeCloseLocked writes one unterminated turn's aborted results and closer. Credits are absent, since nothing
// metered them; elapsed_ms spans turn_open's ts to the newest entry's.
func (l *EntryLog) synthesizeCloseLocked(ctx context.Context, turn, model string) error {
	st := l.turns[turn]
	for _, c := range st.unsettled() {
		res, err := json.Marshal(marotte.EntryToolResult{Status: marotte.ToolAborted})
		if err != nil {
			return fmt.Errorf("entry log: marshal aborted result: %w", err)
		}
		e := &marotte.Entry{
			ID: marotte.ToolResultID(c.id), Turn: turn, Lane: c.lane,
			Kind: marotte.EntryKindToolResult, Payload: res,
		}
		if err := l.appendLocked(ctx, e); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
		Model:         model,
		ElapsedMs:     float64(st.lastTs - st.openTs),
	})
	if err != nil {
		return fmt.Errorf("entry log: marshal synthesized turn_close: %w", err)
	}
	e := &marotte.Entry{
		ID: turn + ":unterminated", Turn: turn,
		Kind: marotte.EntryKindTurnClose, Payload: payload,
	}
	return l.appendLocked(ctx, e)
}

// Rewrite replaces the log with entries, the merge swap's store half: turns contiguous, seq renumbered from 0 per
// turn, renamed into place through atomicfile. Turns not each led by their own turn_open are refused with the log
// untouched (groupByTurn). Nothing on the header is stamped.
func (l *EntryLog) Rewrite(ctx context.Context, entries []marotte.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.removed {
		return ErrTombstoned
	}
	groups, err := groupByTurn(entries)
	if err != nil {
		return err
	}
	if err := l.closeDescriptorLocked(); err != nil {
		return err
	}
	if err := l.writeRewriteLocked(ctx, groups); err != nil {
		return err
	}
	if err := l.rescanLocked(); err != nil {
		return err
	}
	return l.writeCountersLocked(ctx)
}

// writeRewriteLocked stages the merged log and renames it into place.
func (l *EntryLog) writeRewriteLocked(ctx context.Context, groups [][]marotte.Entry) error {
	pending, err := atomicfile.NewPendingFile(ctx, l.path(),
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode))
	if err != nil {
		return fmt.Errorf("entry log: stage rewrite of %s: %w", l.path(), err)
	}
	defer func() { _ = pending.Cleanup() }()
	w := bufio.NewWriter(pending)
	for _, group := range groups {
		for seq := range group {
			e := group[seq]
			e.Seq = uint64(seq)
			persistBoundEntry(&e)
			line, merr := json.Marshal(&e)
			if merr != nil {
				return fmt.Errorf("entry log: marshal %s during rewrite: %w", e.Kind, merr)
			}
			if _, werr := w.Write(append(line, '\n')); werr != nil {
				return fmt.Errorf("entry log: write rewrite of %s: %w", l.path(), werr)
			}
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("entry log: flush rewrite of %s: %w", l.path(), err)
	}
	if _, err := pending.Commit(ctx); err != nil {
		return fmt.Errorf("entry log: commit rewrite of %s: %w", l.path(), err)
	}
	return nil
}

// groupByTurn partitions entries into one contiguous group per turn, in the input's first-appearance order, seq order
// within. Not by turn_open.n: a revert reuses ordinals, so n is ambiguous. A group its own turn_open does not lead is
// refused, because the rescan would read it as a torn tail and truncate the log.
func groupByTurn(entries []marotte.Entry) ([][]marotte.Entry, error) {
	byTurn := make(map[string][]marotte.Entry)
	var order []string
	for _, e := range entries {
		if _, seen := byTurn[e.Turn]; !seen {
			order = append(order, e.Turn)
		}
		byTurn[e.Turn] = append(byTurn[e.Turn], e)
	}
	groups := make([][]marotte.Entry, 0, len(order))
	for _, turn := range order {
		group := byTurn[turn]
		slices.SortStableFunc(group, func(a, b marotte.Entry) int {
			return cmpUint(a.Seq, b.Seq)
		})
		if _, err := turnOrdinal(group); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// turnOrdinal is a group's stored n, read off the turn_open that must lead it; any other leader is refused.
func turnOrdinal(group []marotte.Entry) (uint64, error) {
	lead := group[0]
	if lead.Kind != marotte.EntryKindTurnOpen {
		return 0, fmt.Errorf("entry log: the rewrite of turn %q leads with %s, not with its turn_open",
			lead.Turn, lead.Kind)
	}
	var payload marotte.EntryTurnOpen
	if err := json.Unmarshal(lead.Payload, &payload); err != nil {
		return 0, fmt.Errorf("entry log: parse turn_open of %q for a rewrite: %w", lead.Turn, err)
	}
	return payload.N, nil
}

// ordinalAsInt is a turn's n as the int TurnSummary and TurnCount carry. The clamp is unreachable (a log has no more
// turns than lines) and states the bound.
func ordinalAsInt(n uint64) int {
	if n > math.MaxInt {
		return math.MaxInt
	}
	return int(n)
}

// cmpUint orders two uint64 without the int overflow a subtraction would carry.
func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// writeCountersLocked hands the header the log's counters, which win on a disagreement.
func (l *EntryLog) writeCountersLocked(ctx context.Context) error {
	count, last := l.countersLocked()
	if err := l.header.Counters(ctx, count, last); err != nil {
		return fmt.Errorf("entry log: cache counters for %s: %w", l.root, err)
	}
	return nil
}

// WriteCounters caches turn_count and last_turn_outcome on the header. Separate because a turn open holds the
// lifecycle mutex and this must not: the caller releases it first, and this takes only the log's lock.
func (l *EntryLog) WriteCounters(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeCountersLocked(ctx)
}

// Counters are the header's two caches as the log states them: the newest turn's n and how the newest finished
// non-carrier turn ended.
func (l *EntryLog) Counters() (turnCount uint64, last marotte.TurnOutcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.countersLocked()
}

func (l *EntryLog) countersLocked() (uint64, marotte.TurnOutcome) {
	var last marotte.TurnOutcome
	for _, id := range l.survivingOrderLocked() {
		// A carrier's outcome would paint a chat that has run nothing as done.
		if st := l.turns[id]; st.closed && !st.carrier {
			last = st.outcome
		}
	}
	return l.turnCountLocked(), last
}

// turnCountLocked is the newest surviving turn's n, so the count covers undrawn turns and a rewind shrinks it.
func (l *EntryLog) turnCountLocked() uint64 {
	order := l.survivingOrderLocked()
	if len(order) == 0 {
		return 0
	}
	return l.turns[order[len(order)-1]].n
}

// NewestSeq returns a turn's newest sealed seq, false for a turn not in the log: the live_turn stamp's version half.
// A turn with only its turn_open returns 0.
func (l *EntryLog) NewestSeq(turn string) (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.turns[turn]
	if !ok {
		return 0, false
	}
	return st.nextSeq - 1, true
}

// RailRows is the rail index: one marotte.TurnSummary per drawn turn, oldest first. A turn's row appears at the first
// drawing append and never moves.
func (l *EntryLog) RailRows() []marotte.TurnSummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	order := l.survivingOrderLocked()
	rows := make([]marotte.TurnSummary, 0, len(order))
	for _, id := range order {
		st := l.turns[id]
		if !st.drawn {
			continue
		}
		outcome := marotte.TurnOutcomeRunning
		if st.closed {
			outcome = st.outcome
		}
		rows = append(rows, marotte.TurnSummary{
			ID:             id,
			FirstLine:      st.firstLine,
			Outcome:        outcome,
			N:              ordinalAsInt(st.n),
			Ts:             st.openTs,
			AgentInitiated: !drawnSource(st.source),
			ElapsedMs:      st.elapsedMs,
		})
	}
	return rows
}

// Window is a page of the log: the entries of a set of turns, in file order.
type Window struct {
	Entries []marotte.Entry
	// HasMore reports that an older turn exists outside this page.
	HasMore bool
}

// Window reads the `turns` surviving turns with the highest n, or the `turns` surviving turns below before when it
// names a surviving turn here. `turns` is a page size: the read is a set of whole turns, never split. One pread per
// byte range; lines of turns outside the set are skipped.
func (l *EntryLog) Window(turns int, before string) (Window, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// One slice for resolution, selection and HasMore: resolving `before` against l.order would accept a reverted id
	// and mix in reverted turns. The error is unchanged, so the route's 400 still answers a stale cursor.
	order := l.survivingOrderLocked()
	upper := len(order)
	if before != "" {
		i := slices.Index(order, before)
		if i < 0 {
			return Window{}, fmt.Errorf("entry log: window before turn %q, which this log does not hold", before)
		}
		upper = i
	}
	lower := max(upper-max(turns, 0), 0)
	entries, err := l.readSetLocked(order[lower:upper])
	if err != nil {
		return Window{}, err
	}
	return Window{Entries: entries, HasMore: lower > 0}, nil
}

// All is every surviving entry in file order, the whole-transcript read. The whole file needs AllWithReverted by
// name.
func (l *EntryLog) All() ([]marotte.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readSetLocked(l.survivingOrderLocked())
}

// AllWithReverted is the merge's read: every entry in file order, reverted included, with the set of reverted turn
// ids. The one reader past the surviving view. The set is returned so the skip rule has one implementation.
func (l *EntryLog) AllWithReverted() ([]marotte.Entry, map[string]struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	reverted := make(map[string]struct{})
	for _, id := range l.order {
		if l.turns[id].reverted {
			reverted[id] = struct{}{}
		}
	}
	entries, err := l.readSetLocked(l.order)
	if err != nil {
		return nil, nil, err
	}
	return entries, reverted, nil
}

// NewestRevert is the id of the last turn_revert in file order, false when none: the provenance a projection
// snapshots and its swap re-reads. Not the highest from_n: a rewrite can reorder reverts. One projection per chat,
// consumed by its swap (load_projection.go), so only a tail append can change it.
func (l *EntryLog) NewestRevert() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.newestRevert, l.newestRevert != ""
}

// NeedsReconcile reports whether this record shows the session's own log has something it lacks, which is what runs
// the replay merge. Read from the log's records, not a header flag. Any of: (i) the header's session has no turn_bind
// or reconciled record here; (ii) a surviving turn closed unterminated with no reconciled record; (iii) a surviving
// empty steer with no reconciled record for its turn. A run root answers false through noHeader.
func (l *EntryLog) NeedsReconcile() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	turns, session := l.reconcileTargetsLocked()
	return len(turns) > 0 || session != ""
}

// ReconcileTargets is what NeedsReconcile is true of: the surviving turns whose signal no reconciled record clears,
// in file order, and the unadopted session. One predicate with two readers, so a merge cannot clear what it does not
// hold.
func (l *EntryLog) ReconcileTargets() (turns []string, session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reconcileTargetsLocked()
}

func (l *EntryLog) reconcileTargetsLocked() (turns []string, session string) {
	if !l.header.Reconcilable() {
		return nil, ""
	}
	for _, id := range l.order {
		st := l.turns[id]
		if st == nil || st.reverted {
			continue
		}
		if !st.unterminated && !st.emptySteer {
			continue
		}
		if _, done := l.reconciledTurns[id]; !done {
			turns = append(turns, id)
		}
	}
	return turns, l.unadoptedSessionLocked()
}

// unadoptedSessionLocked is the header's session when no turn_bind or reconciled record names it, else "".
func (l *EntryLog) unadoptedSessionLocked() string {
	s := l.header.SessionID()
	if s == "" {
		return ""
	}
	if _, bound := l.boundSessions[s]; bound {
		return ""
	}
	if _, done := l.reconciledSessions[s]; done {
		return ""
	}
	return s
}

// ErrTurnNotInLog is the one TurnRange failure a caller can cause: an id with no turn here. Other errors mean the log
// is unreadable, a 500.
var ErrTurnNotInLog = errors.New("chat: turn is not in this log")

// TurnRange returns turn's entries from seq from inclusive, in seq order; from == 0 is the whole turn. The wire's
// exclusive ?after= is translated at the door. A reverted turn returns the sentinel like an absent one.
func (l *EntryLog) TurnRange(turn string, from uint64) ([]marotte.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turnRangeLocked(turn, from)
}

func (l *EntryLog) turnRangeLocked(turn string, from uint64) ([]marotte.Entry, error) {
	if st := l.turns[turn]; st == nil || st.reverted {
		return nil, fmt.Errorf("%w: %q", ErrTurnNotInLog, turn)
	}
	entries, err := l.readSetLocked([]string{turn})
	if err != nil {
		return nil, err
	}
	if from == 0 {
		return entries, nil
	}
	return slices.DeleteFunc(entries, func(e marotte.Entry) bool { return e.Seq < from }), nil
}

// TurnPage is the read both roots share: turn's entries from from inclusive, plus the seq the page speaks for; each
// root stamps its own subject kind. newestSeq is the page's newest entry, or from - 1 for an empty page, never the
// log's counter, which a concurrent seal could advance past the page. A held turn always has its turn_open.
func (l *EntryLog) TurnPage(turn string, from uint64) (entries []marotte.Entry, newestSeq uint64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries, err = l.turnRangeLocked(turn, from)
	if err != nil {
		return nil, 0, err
	}
	if n := len(entries); n > 0 {
		return entries, entries[n-1].Seq, nil
	}
	return entries, from - 1, nil
}

// readSetLocked reads the set's own byte ranges, one pread each, returning the set's entries in file order. A span
// from lowest to highest offset would read every reverted byte up to the carrier's record at the file's tail.
func (l *EntryLog) readSetLocked(set []string) ([]marotte.Entry, error) {
	if len(set) == 0 {
		return nil, nil
	}
	want := make(map[string]struct{}, len(set))
	var ranges []byteRange
	for _, id := range set {
		want[id] = struct{}{}
		ranges = append(ranges, l.turns[id].ranges...)
	}
	slices.SortFunc(ranges, func(a, b byteRange) int { return cmp.Compare(a.from, b.from) })
	f, _, err := openChatFile(l.path(), "entry log "+l.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := readEntries(f)
	var out []marotte.Entry
	for _, rng := range ranges {
		entries, err := decodeEntryRange(io.NewSectionReader(r, rng.from, rng.to-rng.from), want)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

// decodeEntryRange decodes one byte range's lines, keeping the wanted turns' entries.
func decodeEntryRange(r io.Reader, want map[string]struct{}) ([]marotte.Entry, error) {
	var out []marotte.Entry
	br := bufio.NewReaderSize(r, entryReadBuf)
	for {
		line, err := readEntryLine(br)
		if len(line) == 0 {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return out, err
		}
		var e marotte.Entry
		if derr := json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &e); derr != nil {
			return out, fmt.Errorf("entry log: parse entry: %w", derr)
		}
		if _, ok := want[e.Turn]; ok {
			out = append(out, e)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
	}
}
