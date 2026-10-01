package chat

// The per-log entry store: one append-only JSONL file of marotte.Entry lines under a
// ROOT the caller names, so the chat store opens it under chats/<id>/ and the run
// store under runs/<id>/ with no header beside it. Every rule about the log is both
// stores'; every rule naming the header is the LogHeader the root supplies, and a run
// root supplies noHeader.
//
// One write(2) plus one sync per seal, so the log is prefix-consistent and a torn tail
// is one partial line the next open drops. The offset index is never persisted.

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

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// entriesFileName is the log; headerFileName is the record beside it, which only a
// chat root has.
const (
	entriesFileName = "entries.jsonl"
	headerFileName  = "chat.json"
)

// maxEntryLineBytes bounds ONE entry's line, independently of the whole-log cap
// and so in force when that cap is unlimited. The tool bounds already cap a call's
// output and diffs, so a line past this is a bug or a reshaped volume, and reading
// it would buffer the whole thing looking for its newline.
const maxEntryLineBytes = 16 << 20

// entryReadBuf is the line reader's buffer. Below it a line is assembled across
// reads, which readEntryLine does without a per-line allocation ceiling of its own.
const entryReadBuf = 64 << 10

// syncEntries is the durability barrier after each appended line. A package var so
// the crash property can fail the k-th one, the way atomicfile's own seams do.
var syncEntries = func(f *os.File) error { return f.Sync() }

// readEntries is the read surface a window read issues its preads against. A package
// var because the cost of that read — one pread per byte range rather than one span
// over the whole page — is not visible in the entries it answers.
var readEntries = func(f *os.File) io.ReaderAt { return f }

// errEntryLineTooLong reports a line over maxEntryLineBytes, on the read side and
// the write side alike.
var errEntryLineTooLong = errors.New("chat: entry line exceeds the per-entry cap")

// errEntryLogFailed is the latch a write error leaves: every further append for
// this log is refused with it until the process restarts.
var errEntryLogFailed = errors.New("chat: entry log refused further appends after a write error")

// entryCall is one tool_call the index holds open: its entry id, and the lane its
// result belongs in, fixed at the create frame.
type entryCall struct {
	id   string
	lane string
}

// byteRange is one contiguous run of a turn's own lines, [from, to).
type byteRange struct {
	from int64
	to   int64
}

// revertWindow is one turn_revert record as its envelope and payload state it: the
// carrier holding the record, and the window from..through the skip rule takes.
// Collected as the line is met and resolved once the index is COMPLETE, because a
// record can sit BELOW the turns it names — see markRevertedLocked.
type revertWindow struct {
	carrier string
	from    string
	through string
}

// turnState is the offset index's row for one turn: the byte ranges its lines
// occupy, and the facts the window read, the rail index, the reconcile predicate
// and a synthesized closer need without opening a payload again.
type turnState struct {
	results   map[string]struct{}
	firstLine string
	source    marotte.TurnOpenSourceName
	outcome   marotte.TurnOutcome
	calls     []entryCall
	// ranges are the turn's lines in file order, one entry per contiguous run.
	// Two of a chat's turns are open together in one registry state and the merge's
	// rewrite files a between-turns entry behind later turns, so a turn is not one
	// span; the window read issues one pread per range.
	ranges  []byteRange
	openTs  int64
	lastTs  int64
	n       uint64
	nextSeq uint64
	plans   int
	drawn   bool
	closed  bool
	// reverted is the skip rule's answer for this turn: a turn_revert took it, so
	// every read surface below answers as though it were not there. The line stays
	// on disk — a rewind is a record, never a truncate.
	reverted bool
	// hasRevert is whether this turn's body holds its own turn_revert record, which
	// is what tells a COMPLETE revert carrier from the one crash state §2.2 step 3
	// can leave behind.
	hasRevert bool
	// unterminated is this turn's turn_close carrying the synthesized closer's own
	// stop reason: the reconcile signal a crash leaves, which the scan answers here
	// so the predicate is a map read rather than a second pass.
	unterminated bool
	// emptySteer is a steer entry of this turn with no text: words KAS persisted
	// that this process never received, the same signal read from the other side.
	emptySteer bool
}

// unsettled are the turn's tool_calls with no tool_result, in the order they
// opened, which is the order a close aborts them in.
func (st *turnState) unsettled() []entryCall {
	var out []entryCall
	for _, c := range st.calls {
		if _, done := st.results[marotte.ToolResultID(c.id)]; !done {
			out = append(out, c)
		}
	}
	return out
}

// LogHeader is the header policy a log's root supplies. The chat root's is its
// chat.json; a run's log has no header, so the run root supplies noHeader and every
// rule naming the header is satisfied without a nil check anywhere in the log.
type LogHeader interface {
	// Counters caches the log's turn_count and last_turn_outcome. The values are the
	// LOG's, so a header that disagreed after a crash is corrected.
	Counters(ctx context.Context, turnCount uint64, last marotte.TurnOutcome) error
	// CloserModel is the model a synthesized closer stamps. The turn_open payload
	// carries none and a live closer stamps what its turn latched, so the header is
	// the only value a later process can honestly supply.
	CloserModel() string
	// Reconcilable reports whether this root's header can name lost history at all.
	// The chat root answers true; a run root has no session to reconcile against, so
	// the reconcile predicate's conditions are never asked there and
	// NeedsReconcile() answers false without an isRunRoot test anywhere.
	Reconcilable() bool
	// SessionID is the ACP session this root's header binds, "" for none: the one
	// half of the reconcile predicate's condition (i) the LOG cannot hold. The log
	// holds the rest — whether any turn_bind names that session, and whether a
	// reconciled record does — so the predicate needs the VALUE, not a bool. A run
	// root answers "".
	SessionID() string
}

// noHeader is the policy of a root with no header file: nothing to cache, no model
// to stamp, no session to reconcile against.
type noHeader struct{}

// NoHeader is the header policy a run root supplies.
func NoHeader() LogHeader { return noHeader{} }

func (noHeader) Counters(context.Context, uint64, marotte.TurnOutcome) error { return nil }
func (noHeader) CloserModel() string                                         { return "" }
func (noHeader) Reconcilable() bool                                          { return false }
func (noHeader) SessionID() string                                           { return "" }

// EntryLog is one log root's append log and its in-memory offset index.
//
// Every exported operation takes the log's own mutex, so the appender is the one
// writer and seq is contiguous per turn by construction. A caller sequencing a
// lifecycle decision against an append takes its own mutex FIRST and never holds
// one across a call here.
type EntryLog struct {
	f     *os.File
	turns map[string]*turnState
	// header is the policy this root's header supplies, never nil: a run root's is
	// noHeader, which answers every question the way a root with no header must.
	header LogHeader
	failed error
	root   string
	// reconciledTurns and reconciledSessions are the turns and the sessions a
	// reconciled record names: the clearers of the three reconcile conditions.
	reconciledTurns    map[string]struct{}
	reconciledSessions map[string]struct{}
	// boundSessions are the sessions this log's turn_bind entries name, which is
	// what tells a chat whose own prompts minted its session from a resumed one.
	boundSessions map[string]struct{}
	// newestRevert is the id of the LAST turn_revert the index met, in the file order
	// it holds: the provenance the merge's discard gate reads in place of a counter.
	newestRevert string
	// unappliedReverts are the windows met but not yet resolved, drained once the
	// order they address is complete: after the scan's pass, and after the one
	// append that adds a record to an index already built.
	unappliedReverts []revertWindow
	order            []string
	size             int64
	cap              chatFileCap
	mu               sync.Mutex
	removed          bool
}

// EntryLogOption configures an EntryLog at open. The header policy is NOT one: it is
// positional on OpenEntryLog, so a root cannot forget it.
type EntryLogOption func(*EntryLog)

// WithEntryFileCap sets the whole-log byte cap; n <= 0 means unlimited, matching the
// chat store's own encoding.
//
// The log DERIVES nothing: the cap comes from the container's memory limit, which
// the chat store already reads once per process, so deriving it here would read the
// same cgroup file and log the same line once per chat directory.
func WithEntryFileCap(n int64) EntryLogOption {
	return func(l *EntryLog) { l.cap = chatFileCap(n) }
}

// OpenEntryLog opens the log under root, building the offset index from one
// sequential envelope-only scan, dropping a tail it cannot read, and closing every
// turn the scan found open before it serves or appends anything.
//
// A directory holding no log is the normal state of a fresh chat: the scan finds no
// file, the index is empty, every read answers empty, and no descriptor is held
// until the first append.
//
// h is the root's header policy and is positional because every rule naming the
// header runs through it: a run root passes NoHeader().
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

// Close releases the write descriptor. The log stays readable and a later append
// reopens it.
func (l *EntryLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeDescriptorLocked()
}

// Remove closes the descriptor and refuses every later append with ErrTombstoned,
// so a turn still folding into a removed chat cannot re-create its log. The
// directory itself is the caller's to unlink.
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

// TurnSpec is what opening a turn needs beyond the bookkeeping the log owns: n is
// the log's to assign and the id is minted here, so the registry never holds a turn
// the log lacks.
type TurnSpec struct {
	Prompt    *marotte.EntryPrompt
	Source    marotte.TurnOpenSourceName
	Run       string
	NodePath  string
	SessionID string
}

// OpenTurn appends a turn_open and answers the entry it wrote, whose ID is the turn
// id every later entry carries and every closer is handed. n is the newest turn's
// plus one, assigned at open to EVERY turn, so it never moves under a later read.
func (l *EntryLog) OpenTurn(ctx context.Context, spec *TurnSpec) (*marotte.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.openTurnLocked(ctx, spec)
}

func (l *EntryLog) openTurnLocked(ctx context.Context, spec *TurnSpec) (*marotte.Entry, error) {
	return l.openTurnWithOrdinalLocked(ctx, spec, l.turnCountLocked()+1)
}

// openTurnWithOrdinalLocked opens a turn at a STATED ordinal, for the one caller
// whose ordinal is not the surviving high-water plus one: §2.2 step 3's revert
// carrier, whose ordinal is the high-water AFTER the window it is about to hide.
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

// Append persists one sealed entry: it assigns seq — contiguous from 0 per turn,
// where turn_open is 0 — stamps ts, writes the line and syncs. It is turnlog's Sink,
// and the descriptor it opens is held for the process's life.
func (l *EntryLog) Append(ctx context.Context, e *marotte.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(ctx, e)
}

// AppendBetweenTurns files a lane-less entry belonging to no open turn: it joins the
// newest SURVIVING turn after that turn's turn_close with that turn's next seq, and
// when no turn survives it opens a headerless turn_open{source: event} to land in,
// which it returns so the caller can announce it ahead of the entry; nil otherwise.
//
// SURVIVING rather than newest in file order, because after every revert the newest
// turn in file order is inside the window the record states, so a model switch, a
// mode switch, a between-turns steer or a steer_ack would land where no reader looks
// — invisible to All, Window, RailRows, the search filter and the export, with a
// live frame the client answers as a hole whose repair read this log refuses.
func (l *EntryLog) AppendBetweenTurns(ctx context.Context, e *marotte.Entry) (*marotte.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	turn, ok := l.newestSurvivingLocked(nil)
	if !ok {
		open, err := l.openTurnLocked(ctx, &TurnSpec{Source: marotte.TurnOpenNameEvent})
		if err != nil {
			return nil, err
		}
		e.Turn = open.Turn
		return open, l.appendLocked(ctx, e)
	}
	e.Turn = turn
	return nil, l.appendLocked(ctx, e)
}

// AppendReconciled records that a merge looked at one signal and had nothing to add,
// which is the only thing that can stop a reconcile signal honestly, and answers the
// record it wrote. EXACTLY ONE of rec's fields is set: Turn for the per-turn signals
// (a synthesized unterminated closer, a surviving empty-text steer) and Session for
// the per-session one, so a rec naming both or neither is refused rather than filed
// under a signal it does not clear.
//
// The TURN form files into the turn its payload NAMES, which lands it after that
// turn's turn_close with that turn's next seq. Never AppendBetweenTurns: that picks
// the turn itself, so the record would land in the newest surviving turn and clear
// nothing. The SESSION form names no turn, so it takes the newest surviving turn as
// its envelope and, when none survives, mints one and CLOSES it, answering that
// turn_open so the caller announces it ahead of the record.
//
// The carrier's close is load-bearing rather than tidy: an open turn is synthesized
// `unterminated` at the next open, which IS condition (ii), so the write that clears
// condition (i) would raise the signal it just cleared. Its source is `event` and
// never `revert`, which is the scan's incomplete-carrier marker.
func (l *EntryLog) AppendReconciled(ctx context.Context, rec marotte.EntryReconciled) (record, opened *marotte.Entry, err error) {
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
			opened, err = l.openTurnLocked(ctx, &TurnSpec{Source: marotte.TurnOpenNameEvent})
			if err != nil {
				return nil, nil, err
			}
			if cerr := l.closeCarrierLocked(ctx, opened.Turn); cerr != nil {
				return nil, opened, cerr
			}
			turn = opened.Turn
		}
	}
	payload, merr := json.Marshal(rec)
	if merr != nil {
		return nil, opened, fmt.Errorf("entry log: marshal reconciled: %w", merr)
	}
	record = &marotte.Entry{
		ID: ReconciledEntryID(rec), Turn: turn,
		Kind: marotte.EntryKindReconciled, Payload: payload,
	}
	if aerr := l.appendLocked(ctx, record); aerr != nil {
		return nil, opened, aerr
	}
	return record, opened, nil
}

// ReconciledEntryID is DERIVED from the signal the record clears, so a second merge
// that re-reaches one signal mints the id the log already holds rather than a fresh
// one the pairing would read as an insertion. The two id spaces cannot collide: a
// turn's id is this log's own mint and a session's is KAS's.
//
// Exported because the merge's REWRITE branch files its own records as members of the
// entry list it hands Rewrite, so the id rule has to be one rule rather than this
// log's convention plus a copy of it in internal/agent.
func ReconciledEntryID(rec marotte.EntryReconciled) string {
	if rec.Turn != "" {
		return rec.Turn + ":reconciled"
	}
	return rec.Session + ":reconciled"
}

// Revert appends this log's record of a rewind and answers it, plus the carrier's
// own turn_open when §2.2 step 3 had to mint one, so the caller announces that open
// ahead of the record. from is the reverted turn, resolved from the SURVIVING view.
//
// Nothing is cut and nothing is re-closed: the reverted range stays on disk, every
// read surface skips it, and a failed append leaves the surviving view exactly as it
// was. cause is the record's own, so a later cause needs no second entry point.
func (l *EntryLog) Revert(ctx context.Context, from string, cause marotte.TurnRevertCause, kasMessageID string) (record, opened *marotte.Entry, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendRevertLocked(ctx, from, cause, kasMessageID)
}

// appendRevertLocked is §2.2's five ordered steps, and the order is the
// specification: compute the window; choose the carrier against it AND against the
// turns an earlier record took; when none survives mint a carrier and CLOSE it
// before the record exists; append the record with its stated Through; and only then
// mark the index, which the append path's own drain does once the write returned.
//
// It is NOT AppendBetweenTurns: that files into the newest SURVIVING turn, which is
// inside this window by construction, so reverting turn 1 of 7 would revert 1-6 and
// leave 7 alive.
func (l *EntryLog) appendRevertLocked(ctx context.Context, from string, cause marotte.TurnRevertCause, kasMessageID string) (record, opened *marotte.Entry, err error) {
	st := l.turns[from]
	if st == nil || st.reverted {
		return nil, nil, fmt.Errorf("%w: %q", ErrTurnNotInLog, from)
	}
	window, through := l.revertWindowLocked(from)
	carrier, held := l.newestSurvivingLocked(window)
	if !held {
		// The ordinal is the high-water the window leaves behind, so the carrier
		// cannot share an n with a live turn in the surviving view.
		opened, err = l.openTurnWithOrdinalLocked(ctx,
			&TurnSpec{Source: marotte.TurnOpenNameRevert}, l.survivingHighWaterLocked(window)+1)
		if err != nil {
			return nil, nil, err
		}
		// A carrier holding no record of its own is what the SCAN marks reverted, so a
		// revert that fails after minting one leaves the same view in memory: the flags
		// follow the record, not the attempt. Without it this process answers a
		// carrier-shaped turn with no body and a turn_count one below the truth, where
		// the next open answers the pre-revert view.
		defer func() {
			if err != nil {
				l.turns[opened.Turn].reverted = true
			}
		}()
		// Closed BEFORE the record, so the one crash state this step can leave is a
		// complete turn on disk: an open carrier is synthesized `unterminated` at
		// the next open, which IS §2.6's reconcile signal, so a rewind that lost
		// nothing would raise it one restart later.
		if cerr := l.closeCarrierLocked(ctx, opened.Turn); cerr != nil {
			return nil, opened, cerr
		}
		carrier = opened.Turn
	}
	payload, merr := json.Marshal(marotte.EntryTurnRevert{
		From: from, FromN: st.n, Through: through,
		KASMessageID: kasMessageID, Cause: cause,
	})
	if merr != nil {
		return nil, opened, fmt.Errorf("entry log: marshal turn_revert: %w", merr)
	}
	record = &marotte.Entry{
		ID: from + ":revert", Turn: carrier,
		Kind: marotte.EntryKindTurnRevert, Payload: payload,
	}
	if aerr := l.appendLocked(ctx, record); aerr != nil {
		return nil, opened, aerr
	}
	return record, opened, nil
}

// revertWindowLocked is the window a revert to from takes: every turn from it to the
// newest in FILE order, inclusive, beside that newest turn's id — the Through the
// record STATES, which is what survives the merge's rewrite.
func (l *EntryLog) revertWindowLocked(from string) (window map[string]struct{}, through string) {
	first := slices.Index(l.order, from)
	window = make(map[string]struct{}, len(l.order)-first)
	for _, id := range l.order[first:] {
		window[id] = struct{}{}
	}
	return window, l.order[len(l.order)-1]
}

// survivingHighWaterLocked is the highest n among the turns that survive excluding,
// which is the ordinal an ordinary turn opened after the revert takes. Zero when
// none survives, so step 3's carrier is n = 1.
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

// newestSurvivingLocked is the newest turn in file order that no revert took,
// skipping the turns in excluding as well — the window a revert is about to take and
// has not marked yet. False when no turn survives.
//
// The ONE implementation of which turn a lane-less entry belongs to, so the revert's
// carrier and the between-turns append cannot disagree about it.
func (l *EntryLog) newestSurvivingLocked(excluding map[string]struct{}) (string, bool) {
	for _, id := range slices.Backward(l.order) {
		if _, in := excluding[id]; in || l.turns[id].reverted {
			continue
		}
		return id, true
	}
	return "", false
}

// closeCarrierLocked closes a turn this log opened to carry a record of its own:
// outcome completed, no stop reason, no model and no elapsed, because nothing ran in
// it. NEVER synthesizeCloseLocked, whose closer is interrupted / unterminated and is
// the reconcile-needed signal.
func (l *EntryLog) closeCarrierLocked(ctx context.Context, turn string) error {
	payload, err := json.Marshal(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})
	if err != nil {
		return fmt.Errorf("entry log: marshal carrier turn_close: %w", err)
	}
	return l.appendLocked(ctx, &marotte.Entry{
		ID: turn + ":close", Turn: turn,
		Kind: marotte.EntryKindTurnClose, Payload: payload,
	})
}

// appendLocked is the one write path. A refused write LATCHES: the failed seq is
// not reused, nothing is handed back for a broadcast, and every further append for
// this log answers the wrapped error until the process restarts. A tool payload is
// bounded to what the record keeps before the line is built, on the caller's own
// entry, so the frame broadcast from that pointer carries the bytes on disk.
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
	// An id-less entry is one filed between turns, outside any accumulator that
	// could mint one; the position it takes is unique within its turn and stable.
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
	// The append path's index is already complete, so a record's window resolves on
	// the spot; the drain is what keeps that one rule in one place.
	l.applyRevertsLocked()
	return nil
}

// checkBoundsLocked refuses a line over the per-entry cap, and a write that would
// take the log past the whole-log cap. Refused BEFORE anything reaches disk, so the
// log stays exactly as it was.
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

// descriptorLocked opens the log for appending on first use, creating it. A fresh
// chat holds no descriptor until here.
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

// observeLocked folds one entry into the offset index, from the open scan and from
// an append alike, so the two cannot build different indexes.
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

// extend folds e's envelope into st: its byte range, its timestamp, the next seq,
// and whether it draws the turn. Before the kind's own fold, because drawnBy reads the
// plans counted so far and a plan entry increments that count.
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

// applyRevertsLocked resolves every window met since the last drain, in the order
// the file holds them. Called where the order is COMPLETE: at the end of the scan's
// pass and after the append that added a record, never from the fold itself.
func (l *EntryLog) applyRevertsLocked() {
	windows := l.unappliedReverts
	l.unappliedReverts = nil
	for _, w := range windows {
		l.markRevertedLocked(w)
	}
}

// markRevertedLocked applies §2.2's skip rule for one record: every turn whose
// turn_open lies at or after from's and at or before through's, in file order,
// except the CARRIER the record's own envelope names.
//
// The bound is the turn the record NAMES, never this line's byte offset: the merge's
// rewrite regroups by turn and files a lane-less record inside its carrier's group,
// which sits BELOW the turns the revert took, so an offset-bounded rule would mark
// nothing after one swap and hand the reverted range back.
//
// It resolves against the COMPLETED order for the same reason, which is why the fold
// queues the window instead of calling this: after a rewrite the record travels into
// its carrier's group and the carrier precedes the window by construction (step 2
// picks the newest SURVIVOR, groupByTurn writes one contiguous group per turn), so
// both ends are still unread when the line is met and an inline resolution marks
// nothing — the same reverted range handed back, from the other direction.
//
// A window this log cannot resolve marks NOTHING. Both ends are turn ids this log is
// expected to hold, so an absent one is a record from another log or a torn line, and
// hiding turns on it would lose history the rule exists to preserve.
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

// survivingOrderLocked is l.order with every reverted turn dropped: the order every
// read surface answers over, so a reader that wants the whole file has to name
// AllWithReverted instead.
func (l *EntryLog) survivingOrderLocked() []string {
	order := make([]string, 0, len(l.order))
	for _, id := range l.order {
		if !l.turns[id].reverted {
			order = append(order, id)
		}
	}
	return order
}

// newTurnState opens the index row for a turn.
//
// The scan opens SIX payloads and no others, and the cost is stated per kind because
// the pass stays one sequential read: turn_open (source, n and the prompt's first
// line) and turn_close (the outcome, plus the stop reason that IS the reconcile
// signal) are one line per turn each; turn_revert is one per REVERT and carries the
// skip rule's window; reconciled is one per reconciled turn or session and carries
// the clearers; steer is rare inside a turn and gives up its text alone; turn_bind is
// one per bound turn and gives up its session alone. Every other kind's payload stays
// a json.RawMessage.
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

// drawnSource reports whether a turn's source draws a header of its own, so the
// turn has a rail row before its body holds anything. Its negation is the rail's
// agent_initiated.
func drawnSource(s marotte.TurnOpenSourceName) bool {
	switch s {
	case marotte.TurnOpenNamePrompt, marotte.TurnOpenNameLocalShell, marotte.TurnOpenNameEmptyRetry:
		return true
	}
	return false
}

// drawnBy reports whether e makes its turn drawn: an entry rendering at its own
// position in lane "". The sealed half of the one drawn predicate; the client's
// open-entry clause has no server twin, because an open entry never reaches the
// log. The excluded kinds are the ones the client's entryRenders refuses, and a
// steer_ack is NOT among them: an ack is agent words at its own position, so a
// turn holding nothing else draws a card and needs its rail row.
//
// A reconciled record is excluded because it clears a signal rather than saying
// anything to a reader, so a turn holding it alone must not take a rail row; a
// turn_revert is NOT excluded, because it IS the boundary row decision 3 asks for.
//
// plans is how many plan entries the turn already holds, so only the first draws.
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

// firstLineOf is a prompt's first line, whitespace-collapsed, for the rail row's
// hover label.
func firstLineOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.Join(strings.Fields(line), " ")
}

// rescanLocked rebuilds the index from one sequential pass, decoding each line's
// envelope and truncating a tail it cannot read, then applies the two rules the pass
// cannot answer line by line: every turn_revert's window, which can name turns the
// pass had not reached when it met the record, and an incomplete revert carrier being
// itself reverted.
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
	// After the error too: dropTailLocked leaves a prefix-consistent index, and a
	// scan that stopped early can still hold a carrier whose record is in the tail
	// it dropped, which is exactly the crash state the clause exists for. A window
	// naming a turn the dropped tail held resolves to nothing and marks nothing.
	l.applyRevertsLocked()
	l.markIncompleteCarriersLocked()
	return err
}

// markIncompleteCarriersLocked marks every `source: revert` turn holding no
// turn_revert of its own. That is §2.2 step 3's ONE crash state — the carrier's
// turn_open on disk and the record not — and marking it restores the pre-revert
// surviving view: every reader skips the turn, openTurnsLocked skips it so no closer
// is synthesized for it, and the ordinal it reserved is no coordinate any reader reads.
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
			// Prefix-consistent: the first line the scan cannot read ends the log,
			// because an O_APPEND write of one line is either wholly present or a
			// torn tail, so anything past it is unreachable either way.
			return l.dropTailLocked(off, err)
		}
		off += int64(len(line))
	}
}

// observeLineLocked decodes one complete line and folds it in, reporting why the
// line is unusable when it is.
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

// dropTailLocked truncates the log at the last complete line and logs one Warn. The
// index needs no repair: nothing past off was observed.
func (l *EntryLog) dropTailLocked(off int64, cause error) error {
	l.size = off
	slog.Warn("entry log: dropped an unreadable tail at the last complete line",
		"root", l.root, "offset", off, "cause", cause)
	if err := os.Truncate(l.path(), off); err != nil {
		return fmt.Errorf("entry log: truncate torn tail of %s: %w", l.path(), err)
	}
	return nil
}

// readEntryLine reads one line INCLUDING its newline, bounded by maxEntryLineBytes.
// A complete line answers a nil error; a final line with no newline answers io.EOF
// with the bytes, which is the torn tail.
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

// openTurnsLocked are the turns with no turn_close, oldest first: what a crash or the
// write-error rule left behind, over the surviving view, so a turn a revert took is
// never re-closed for having been reverted.
func (l *EntryLog) openTurnsLocked() []string {
	var open []string
	for _, id := range l.survivingOrderLocked() {
		if !l.turns[id].closed {
			open = append(open, id)
		}
	}
	return open
}

// closeUnterminatedLocked appends the synthesized closer to each named turn: an
// aborted tool_result for every unsettled call in its own lane, then
// turn_close{outcome: interrupted, stop_reason_raw: unterminated}.
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

// synthesizeCloseLocked writes one unterminated turn's aborted results and its
// closer. Credits are ABSENT because nothing metered them, and elapsed_ms spans the
// turn_open's ts to its newest entry's, both metadata read once here for a display
// value.
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

// Rewrite replaces the log with entries, the merge swap's store half: turns
// contiguous in n order, entries in seq order within a turn, seq renumbered from 0
// per turn, renamed into place through atomicfile. A set whose turns are not each
// led by their own turn_open is refused with the log untouched; groupByTurn owns why.
//
// Nothing on the header is stamped by it: the evidence a merge answers is in the log,
// and this rewrite's own output is what answers it, so a completed swap needs no
// counter for a later projection to compare against.
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

// groupByTurn partitions entries into one contiguous group per turn, groups in the
// INPUT's own first-appearance order and entries in seq order within a group. An
// interleave the input held is deliberately NOT reproduced: it recorded nothing once
// both turns are on disk.
//
// The group order is never turn_open.n: a revert reuses an ordinal (§2.3), so two turns
// can carry one n and a sort over it is ambiguous — and it would interleave reverted
// turns with surviving ones. The caller's order is the merge's spine, which is this
// log's own file order, so it is the order to keep.
//
// It REFUSES a group its own turn_open does not lead, because the rescan a rewrite
// ends with reads a first line naming a turn it does not hold as a torn tail: the
// caller must learn its merge is malformed rather than have the log truncated to
// nothing and reported as a success. That refusal is turnOrdinal's, which is why the
// call stays after the sort it used to key.
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

// turnOrdinal is a group's stored n, read off the turn_open that must LEAD it. A
// group led by anything else is refused rather than sorted first: first is exactly
// where the rewrite's rescan would cut the file.
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

// ordinalAsInt is a turn's n as the int the wire's TurnSummary and the header's
// TurnCount carry. A log holds no more turns than it has lines, so the clamp cannot
// be reached; it is here so the conversion states its own bound rather than wrapping.
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

// writeCountersLocked hands the header the LOG's counters, which win on a
// disagreement: they are caches recomputed from the log whenever the two differ.
func (l *EntryLog) writeCountersLocked(ctx context.Context) error {
	count, last := l.countersLocked()
	if err := l.header.Counters(ctx, count, last); err != nil {
		return fmt.Errorf("entry log: cache counters for %s: %w", l.root, err)
	}
	return nil
}

// WriteCounters caches turn_count and last_turn_outcome on the header. A SEPARATE
// operation because a turn open takes the lifecycle mutex and this write must not:
// the caller releases lifecycle and then calls this, which takes the log's lock
// alone.
func (l *EntryLog) WriteCounters(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeCountersLocked(ctx)
}

// Counters are the header's two caches as the log states them: the newest turn's n,
// and how the newest FINISHED turn ended.
func (l *EntryLog) Counters() (turnCount uint64, last marotte.TurnOutcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.countersLocked()
}

func (l *EntryLog) countersLocked() (uint64, marotte.TurnOutcome) {
	var last marotte.TurnOutcome
	for _, id := range l.survivingOrderLocked() {
		if st := l.turns[id]; st.closed {
			last = st.outcome
		}
	}
	return l.turnCountLocked(), last
}

// turnCountLocked is the newest SURVIVING turn's n, so the sidebar counts turns
// whether they are drawn or not and a rewind shrinks the count it caches.
func (l *EntryLog) turnCountLocked() uint64 {
	order := l.survivingOrderLocked()
	if len(order) == 0 {
		return 0
	}
	return l.turns[order[len(order)-1]].n
}

// NewestSeq answers a turn's newest sealed seq, false for a turn the log does not
// hold. The live_turn stamp's version half; a turn holding its turn_open alone
// answers 0.
func (l *EntryLog) NewestSeq(turn string) (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.turns[turn]
	if !ok {
		return 0, false
	}
	return st.nextSeq - 1, true
}

// RailRows is the rail index: one marotte.TurnSummary per DRAWN turn, oldest first.
// A turn becomes drawn at the first append satisfying the predicate, so its row
// appears then and never afterwards moves.
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
		})
	}
	return rows
}

// Window is a page of the log: the entries of one SET of turns, in file order.
type Window struct {
	Entries []marotte.Entry
	// HasMore reports that an older turn exists outside this page.
	HasMore bool
}

// Window reads the `turns` turns with the highest n among the SURVIVING turns, or the
// `turns` surviving turns whose n is below before's when before names one this log
// holds and no revert took.
//
// `turns` is a PAGE SIZE, not a coordinate: §5.3 and §6.3 define the read as a SET of
// whole turns, so an entry count here would split a turn across two pages. R1 deletes
// the second COORDINATE system and leaves the page size named for what it counts.
//
// One read per byte range of the set, keeping the lines whose turn is in the set: a
// line of a turn OUTSIDE a range is skipped, which is the interleave the one
// two-open-turns registry state and a between-turns append both produce.
func (l *EntryLog) Window(turns int, before string) (Window, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// ONE slice for the resolution, the selection and HasMore. A reverted turn stays
	// in l.order, so resolving `before` against that would succeed for an id this
	// door must refuse, serve a page holding reverted turns, and read HasMore off a
	// set no other reader answers over; and indexing two different slices is an
	// off-by-N page. The error value and its message are unchanged, which is what
	// keeps the chat route's own 400 arm the answer to a stale cursor.
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

// All is every SURVIVING entry of the log in file order: the whole-transcript read.
// The skip is the default here, so a new caller gets the surviving view and one that
// wants the whole file has to write AllWithReverted's name instead.
func (l *EntryLog) All() ([]marotte.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readSetLocked(l.survivingOrderLocked())
}

// AllWithReverted is the merge's read: every entry in file order, reverted material
// included, beside the set of turn ids §2.2's rule marks. The ONE reader that sees
// past the surviving view, so a reviewer greps the name and finds one site.
//
// The set is RETURNED rather than recomputed by the caller: the rule has one
// implementation, the index's own scan, and a second copy in internal/agent is the
// two-parsers class this project refuses.
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

// NewestRevert is the id of the LAST turn_revert in FILE order as the index holds it,
// false when the log holds none. The provenance a projection snapshots and its swap
// re-reads, in place of the header counter revision was.
//
// Never the highest from_n: the two disagree, because a record lives in its CARRIER's
// group and a later revert's carrier can sit at a lower position, so one rewrite can
// swap two reverts' file positions. The gate survives that because at most one
// projection per chat is open and a swap consumes it (load_projection.go), so the only
// thing it compares is whether a revert appended while THIS projection was in flight,
// and an append is always at the file's tail.
func (l *EntryLog) NewestRevert() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.newestRevert, l.newestRevert != ""
}

// NeedsReconcile is whether this record holds evidence that the session's own log
// carries something it lacks, which is what makes the replay merge run at all. It is
// read from the records the log already holds rather than from a header flag: a flag
// was a second copy of a fact the log states, written first only because it could
// disagree with it.
//
// Three conditions, any one of them: (i) the header names a session and no turn_bind
// of this log names it and no reconciled record names it — a chat bound to a session
// whose history this record has never adopted; (ii) a surviving turn closed
// "unterminated" with no reconciled record naming it — the closer a crash leaves;
// (iii) a surviving steer entry with no text and no reconciled record naming its turn
// — words KAS persisted that this process never received.
//
// The run root answers false outright, because it names no session, a run turn closed
// unterminated is ordinary, and no merge runs against it. That is noHeader's
// Reconcilable doing the work rather than an isRunRoot test anywhere.
func (l *EntryLog) NeedsReconcile() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	turns, session := l.reconcileTargetsLocked()
	return len(turns) > 0 || session != ""
}

// ReconcileTargets is what NeedsReconcile is true OF: the surviving turns whose signal
// no reconciled record clears, in file order, and the session this record has never
// adopted. ONE predicate with two readers rather than two spellings of it — a merge
// that decided FROM the predicate and then re-derived WHICH signals to clear would be
// free to clear a signal the predicate does not hold, or to leave one it does.
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

// unadoptedSessionLocked is the header's session when neither a turn_bind nor a
// reconciled record in the log names it, else "".
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

// ErrTurnNotInLog is the one TurnRange failure a CALLER can cause: an id this log holds
// no turn for. Every other error is the log being unreadable, which a route reports as a
// 500 rather than as a missing turn, so the two classes may not share an answer.
var ErrTurnNotInLog = errors.New("chat: turn is not in this log")

// TurnRange answers turn's entries from seq from INCLUSIVE, in seq order, so from == 0
// is the whole turn and the repair read needs no second function and no bool. The wire's
// ?after= is EXCLUSIVE and is translated at the door (from = after + 1 when present, 0
// when absent). A REVERTED turn answers the sentinel exactly like an absent one, so both
// routes render the refusal they already render — 404 for the chat door, found == false
// for the run's.
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

// TurnPage is the read both roots share: turn's entries from from inclusive, beside the
// seq the PAGE speaks for. Each root stamps its own subject kind over it — live_turn for
// a chat, run_turn for a run — and the read itself is one implementation.
//
// newestSeq is the page's own newest entry, or from - 1 for an EMPTY page: the exclusive
// cursor the caller already held, never the log's own counter, which a seal landing
// between the two reads would advance so the stamp names a version one ahead of the page
// and a reconnect never fetches it. from == 0 cannot answer empty for a turn this log
// holds, since a held turn always carries its turn_open.
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

// readSetLocked reads the set's OWN byte ranges, one pread per range, and answers
// the entries whose turn is in the set, in file order.
//
// One span from the set's lowest offset to its highest is what this replaced, and
// after a rewind it reads every reverted byte between the page's oldest turn and its
// newest: a carrier's newest entry is its revert record at the file's tail, so the
// span reaches EOF even for a page of turns that all predate the revert. One pread
// per contiguous RUN of turns has the same defect for the same reason.
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

// decodeEntryRange decodes one byte range's lines, keeping the entries whose turn is
// wanted.
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
