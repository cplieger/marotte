package agent

// The run store's registry and appender: one entry log per run under `<root>/<workflowId>/`, one turnlog
// accumulator per open step, and two routing maps. KAS owns run state, so the log has no header and is
// never rewound, merged or paged by number.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

// runLogDir is the directory beside chats/ holding one entry log per run.
const runLogDir = "runs"

// errRunLogRemoved reports an append or open against a run a delete removed: the in-process tombstone.
var errRunLogRemoved = errors.New("run log: this run was deleted")

// errRunIDInvalid refuses a workflow id that is not a bare directory name; the REST route passes it unparsed.
var errRunIDInvalid = errors.New("run log: invalid workflow id")

// errRunNoClosedTurn reports a late entry for a path this process never saw close.
var errRunNoClosedTurn = errors.New("run log: the step path has no closed turn")

// runTurn is one open step turn: its accumulator and its newest sealed seq for a `run_turn` stamp.
type runTurn struct {
	turn *turnlog.Turn
	// rawStop is the last turn_end's stop reason, carried by the close.
	rawStop marotte.StopReason
	path    string
	// nodeID is the step's own id, held because path is never split back (workflow.PathKey).
	nodeID string
	// session is the step's ACP session, as its turn_open records. Held here because the open set is this registry's alone.
	session string
	// seq is the newest seq the sink assigned, written on the dispatch goroutine and read by the digest.
	seq atomic.Uint64
}

// runSink wraps the run's log so every append records the turn's newest seq.
type runSink struct {
	log  *chat.EntryLog
	turn *runTurn
}

func (s runSink) Append(ctx context.Context, e *marotte.Entry) error {
	if err := s.log.Append(ctx, e); err != nil {
		return err
	}
	s.turn.seq.Store(e.Seq)
	return nil
}

// runRecord is one run: its log, open turns by node path, newest closed turn per path, and the bridge whose death closes them.
type runRecord struct {
	log    *chat.EntryLog
	open   map[string]*runTurn
	closed map[string]string
	// ends is each path whose newest turn closed broken, nil until a read scans the log; every close then keeps it current.
	ends map[string]marotte.RunStepEnd
	// starts is each path's current attempt, nil until a read scans the log; every open and close then keeps it current.
	starts map[string]stepAttempt
	host   marotte.ChatID
	// removed is the tombstone: a frame arriving after Delete opens nothing.
	removed bool
}

// runLog is the registry the content, lifecycle and death handlers and Runs.Delete reach. One mutex, held
// across the store call; nothing here runs under the log's own lock.
type runLog struct {
	runs map[string]*runRecord
	root string
	mu   sync.Mutex
}

// newRunLog builds the registry over `<configDir>/runs`.
func newRunLog(configDir string) *runLog {
	return &runLog{
		runs: make(map[string]*runRecord),
		root: filepath.Join(configDir, runLogDir),
	}
}

// dir is a run's directory, refused for an id that is not a plain name.
func (r *runLog) dir(workflowID string) (string, error) {
	if !ids.ValidChatID(workflowID) {
		return "", errRunIDInvalid
	}
	return filepath.Join(r.root, workflowID), nil
}

// recordLocked answers the run's record, opening its log on first use; a deleted run answers errRunLogRemoved until restart.
func (r *runLog) recordLocked(ctx context.Context, workflowID string) (*runRecord, error) {
	rec, ok := r.runs[workflowID]
	if ok && rec.removed {
		return nil, errRunLogRemoved
	}
	if ok && rec.log != nil {
		return rec, nil
	}
	dir, err := r.dir(workflowID)
	if err != nil {
		return nil, err
	}
	l, err := chat.OpenEntryLog(ctx, dir, chat.NoHeader())
	if err != nil {
		return nil, err
	}
	if !ok {
		rec = &runRecord{open: make(map[string]*runTurn), closed: make(map[string]string)}
		r.runs[workflowID] = rec
	}
	rec.log = l
	return rec, nil
}

// Log answers a run's log for a read, or nil with no directory: a read must not create one.
func (r *runLog) Log(ctx context.Context, workflowID string) (*chat.EntryLog, error) {
	if workflowID == "" {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.runs[workflowID]; ok {
		if rec.removed {
			return nil, nil
		}
		if rec.log != nil {
			return rec.log, nil
		}
	}
	dir, err := r.dir(workflowID)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	rec, err := r.recordLocked(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	return rec.log, nil
}

// stepTurns is one step path's transcript: its turns in file order, open tails, and one run_turn stamp per
// open turn. The stamp's seq comes from the served entries, never the record's counter, which advances
// outside the lock. found is false with no such turn.
func (r *runLog) stepTurns(ctx context.Context, workflowID, nodePath string) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []*marotte.SubjectStamp, found bool, err error) {
	l, err := r.Log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, nil, nil, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := l.All()
	if err != nil {
		return nil, nil, nil, false, err
	}
	entries, newest := stepEntries(all, nodePath)
	if len(newest) == 0 {
		return nil, nil, nil, false, nil
	}
	if rec, ok := r.runs[workflowID]; ok {
		if t := rec.open[nodePath]; t != nil {
			open = t.turn.OpenEntries()
			id := t.turn.ID()
			stamps = append(stamps, marotte.NewSubjectStamp(string(subject.KindRunTurn), workflowID+"/"+id, turnVersion(id, newest[id])))
		}
	}
	return entries, open, stamps, true, nil
}

// turnRange is one step turn's tail from seq `from` inclusive (0 the whole turn), its open tails and its
// stamp, over EntryLog.TurnPage like the chat's. found is false for no log or no such turn; err is an unreadable log (500).
func (r *runLog) turnRange(ctx context.Context, workflowID, turn string, from uint64) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []*marotte.SubjectStamp, found bool, err error) {
	l, err := r.Log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, nil, nil, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, newestSeq, err := l.TurnPage(turn, from)
	if err != nil {
		// Only a missing turn is the caller's answer; an unreadable log is a 500.
		if errors.Is(err, chat.ErrTurnNotInLog) {
			return nil, nil, nil, false, nil
		}
		return nil, nil, nil, false, err
	}
	rec, ok := r.runs[workflowID]
	if !ok {
		return entries, nil, nil, true, nil
	}
	for _, t := range rec.open {
		if t.turn.ID() != turn {
			continue
		}
		open = t.turn.OpenEntries()
		stamps = append(stamps, marotte.NewSubjectStamp(string(subject.KindRunTurn),
			workflowID+"/"+turn, turnVersion(turn, newestSeq)))
	}
	return entries, open, stamps, true, nil
}

// stepEntries keeps the entries of every turn whose turn_open names nodePath, with each turn's newest seq.
func stepEntries(all []marotte.Entry, nodePath string) (entries []marotte.Entry, newest map[string]uint64) {
	newest = make(map[string]uint64)
	for i := range all {
		e := &all[i]
		if e.Kind == marotte.EntryKindTurnOpen {
			var to marotte.EntryTurnOpen
			if json.Unmarshal(e.Payload, &to) == nil && to.NodePath == nodePath {
				newest[e.Turn] = e.Seq
			}
		}
		if _, ok := newest[e.Turn]; ok {
			entries = append(entries, *e)
			newest[e.Turn] = e.Seq
		}
	}
	return entries, newest
}

// foldTurn answers the step's open turn, or nil, naming its node when the turn was opened with none.
func (r *runLog) foldTurn(step translate.RunStep) *turnlog.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(step.RunID, step.NodePath)
	if t == nil {
		return nil
	}
	t.learnNodeID(step.NodeID)
	return t.turn
}

// learnNodeID names a turn opened with no id: a content frame can open it before the step registry knows
// the step (after a restart), and awaitingAnswer matches asks by this id. Caller holds runLog.mu.
func (t *runTurn) learnNodeID(id string) {
	if t.nodeID == "" {
		t.nodeID = id
	}
}

// hasClosed reports whether the step path has a closed turn this process knows.
func (r *runLog) hasClosed(workflowID, nodePath string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.runs[workflowID]; ok {
		_, closed := rec.closed[nodePath]
		return closed
	}
	return false
}

// hostsOpen reports whether any run hosted by the chat holds an open step turn.
func (r *runLog) hostsOpen(chatID marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.runs {
		if rec.host == chatID && len(rec.open) > 0 {
			return true
		}
	}
	return false
}

// OpenSeqs answers every open turn of a run as turn id to newest seq: the digest's
// `run_turn` arm.
func (r *runLog) OpenSeqs(workflowID string) map[string]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || len(rec.open) == 0 {
		return nil
	}
	out := make(map[string]uint64, len(rec.open))
	for _, t := range rec.open {
		out[t.turn.ID()] = t.seq.Load()
	}
	return out
}

// OpenSessions answers the ACP sessions of the run's open step turns, for the idle window's terminal
// check. A session-less step adds nothing; no record answers empty.
func (r *runLog) OpenSessions(workflowID string) map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(rec.open))
	for _, t := range rec.open {
		if t.session != "" {
			out[t.session] = struct{}{}
		}
	}
	return out
}

// OpenNodeIDs answers each open step turn's node id, or nil for no record. A turn opened with no id adds nothing.
func (r *runLog) OpenNodeIDs(workflowID string) map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(rec.open))
	for _, t := range rec.open {
		if t.nodeID != "" {
			out[t.nodeID] = struct{}{}
		}
	}
	return out
}

// Open opens the step's turn, keyed by its NodePath, when none is open and answers it; opened is the
// appended turn_open, nil when already open. The frame's chat id becomes the run's host when unset
// (`run:<id>` for an empty one).
func (r *runLog) Open(ctx context.Context, step translate.RunStep, chatID marotte.ChatID) (turn *turnlog.Turn, opened *marotte.Entry, err error) {
	workflowID, nodePath := step.RunID, step.NodePath
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.recordLocked(ctx, workflowID)
	if err != nil {
		return nil, nil, err
	}
	if rec.host == "" {
		rec.host = hostFor(workflowID, chatID)
	}
	if t := rec.open[nodePath]; t != nil {
		t.learnNodeID(step.NodeID)
		return t.turn, nil, nil
	}
	e, err := rec.log.OpenTurn(ctx, &chat.TurnSpec{
		Source:    marotte.TurnOpenNameWorkflowStep,
		Run:       workflowID,
		NodePath:  nodePath,
		SessionID: step.SessionID,
	})
	if err != nil {
		return nil, nil, err
	}
	t := &runTurn{path: nodePath, nodeID: step.NodeID, session: step.SessionID}
	t.turn = turnlog.Open(e.ID, runSink{log: rec.log, turn: t})
	rec.open[nodePath] = t
	if rec.starts != nil {
		noteAttemptOpen(rec.starts, nodePath, e.Ts)
	}
	return t.turn, e, nil
}

// hostFor is the host a frame's chat id names: itself, or the parentless bridge's key for an empty id.
func hostFor(workflowID string, chatID marotte.ChatID) marotte.ChatID {
	if chatID == "" {
		return runChatID(workflowID)
	}
	return chatID
}

// AppendAfterClosed files a content entry after the newest closed turn of its path; false when none is known, so the caller opens one.
func (r *runLog) AppendAfterClosed(ctx context.Context, workflowID, nodePath string, e *marotte.Entry) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return false, nil
	}
	if rec.removed {
		return false, errRunLogRemoved
	}
	turn, ok := rec.closed[nodePath]
	if !ok || rec.log == nil {
		return false, nil
	}
	e.Turn = turn
	return true, rec.log.Append(ctx, e)
}

// Meter folds a step's turn_completion into its open turn; false with no open turn.
func (r *runLog) Meter(workflowID, nodePath string, credits, elapsedMs float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return false
	}
	t.turn.Meter(credits, elapsedMs)
	return true
}

// StopReason records a step's turn_end stop reason on its open turn; the last wins.
func (r *runLog) StopReason(workflowID, nodePath string, raw marotte.StopReason) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return false
	}
	t.rawStop = raw
	return true
}

func (r *runLog) openLocked(workflowID, nodePath string) *runTurn {
	if rec, ok := r.runs[workflowID]; ok {
		return rec.open[nodePath]
	}
	return nil
}

// CloseNode closes the step's turn with KAS's node_complete status, answering everything it sealed; closed
// is false with no open turn.
func (r *runLog) CloseNode(ctx context.Context, workflowID, nodePath, status, reason string) (sealed []turnlog.Sealed, closed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil, false, nil
	}
	t := rec.open[nodePath]
	if t == nil {
		return nil, false, nil
	}
	c := runOutcome(status)
	c.Reason = reason
	// KAS grades a refused step, and one its iteration limit stopped, `completed`; the step's last turn_end
	// outranks that status. A failed or cancelled status keeps KAS's own account.
	switch stop := marotte.ConcludeStopReason(t.rawStop); {
	case stop.Outcome == marotte.TurnOutcomeRefused:
		c = stop
		c.Reason = reason
	case stop.FailureKind == marotte.FailureKindModelCallLimit && c.Outcome == marotte.TurnOutcomeCompleted:
		c = stop
		c.Reason = marotte.ModelCallLimitStepReason
	}
	// An unmapped status is itself the raw stop, keeping it recoverable.
	if c.Known && t.rawStop != "" {
		c.RawStop = t.rawStop
	}
	sealed, err = r.closeLocked(ctx, rec, t, c)
	return sealed, true, err
}

// CloseRun closes every open turn of a run with one outcome and, when terminal, drops the host.
func (r *runLog) CloseRun(ctx context.Context, workflowID string, c marotte.TurnConclusion, terminal bool) ([]turnlog.Sealed, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil, nil
	}
	sealed, err := r.closeAllLocked(ctx, rec, c)
	if terminal {
		rec.host = ""
	}
	return sealed, err
}

// CloseHost closes every open turn of every run the dead bridge hosted (the death closer's run arm),
// answering sealed entries per run.
func (r *runLog) CloseHost(ctx context.Context, chatID marotte.ChatID, c marotte.TurnConclusion) (map[string][]turnlog.Sealed, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out map[string][]turnlog.Sealed
	var errs []error
	for id, rec := range r.runs {
		if rec.host != chatID || len(rec.open) == 0 {
			continue
		}
		sealed, err := r.closeAllLocked(ctx, rec, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", id, err))
		}
		if len(sealed) > 0 {
			if out == nil {
				out = make(map[string][]turnlog.Sealed)
			}
			out[id] = sealed
		}
	}
	return out, errors.Join(errs...)
}

// Delete closes every open turn `cancelled`, drops the maps and tombstones the log; RemoveDir is last,
// after KAS's delete and clearEnd.
func (r *runLog) Delete(ctx context.Context, workflowID string) ([]turnlog.Sealed, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		rec = &runRecord{}
		r.runs[workflowID] = rec
	}
	sealed, err := r.closeAllLocked(ctx, rec, marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCancelled, RawStop: marotte.StopReasonCancelled, Known: true})
	rec.open = nil
	rec.closed = nil
	rec.host = ""
	rec.removed = true
	if rec.log != nil {
		err = errors.Join(err, rec.log.Remove())
	}
	return sealed, err
}

// RemoveDir removes the run's directory, only after Delete: the tombstone stops a late frame recreating it.
func (r *runLog) RemoveDir(workflowID string) error {
	if workflowID == "" {
		return nil
	}
	dir, err := r.dir(workflowID)
	if err != nil {
		return err
	}
	err = os.RemoveAll(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// closeAllLocked closes every open turn of a run, in path order.
func (r *runLog) closeAllLocked(ctx context.Context, rec *runRecord, c marotte.TurnConclusion) ([]turnlog.Sealed, error) {
	var out []turnlog.Sealed
	var errs []error
	for _, path := range sortedKeys(rec.open) {
		s, err := r.closeLocked(ctx, rec, rec.open[path], c)
		out = append(out, s...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

// closeLocked closes one turn and moves it to closed. The path leaves `open` whatever the close answered:
// a refused write latched the log.
func (r *runLog) closeLocked(ctx context.Context, rec *runRecord, t *runTurn, c marotte.TurnConclusion) ([]turnlog.Sealed, error) {
	sealed, err := t.turn.Close(ctx, c)
	delete(rec.open, t.path)
	rec.closed[t.path] = t.turn.ID()
	if rec.ends != nil {
		noteStepEnd(rec.ends, t.path, c.Outcome, c.Reason, c.FailureKind)
	}
	if rec.starts != nil {
		noteAttemptClose(rec.starts, t.path, c.Outcome)
	}
	return sealed, err
}

// noteStepEnd records a path's newest close: a broken one is kept, any other drops what an earlier turn left.
func noteStepEnd(ends map[string]marotte.RunStepEnd, path string, o marotte.TurnOutcome, reason string, kind marotte.FailureKind) {
	if marotte.SeverityOf(o) != marotte.TurnSeverityBroken {
		delete(ends, path)
		return
	}
	ends[path] = marotte.RunStepEnd{Outcome: o, FailureReason: reason, FailureKind: kind}
}

// StepEnds answers the run's broken step ends by node path, scanning the log on the first read of the process;
// nil with no log.
func (r *runLog) StepEnds(ctx context.Context, workflowID string) (map[string]marotte.RunStepEnd, error) {
	l, err := r.Log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return nil, nil
	}
	if err := scanStepFactsLocked(rec, l); err != nil {
		return nil, err
	}
	return maps.Clone(rec.ends), nil
}

// scanStepFactsLocked fills the record's step ends and attempt starts from its log once per process, both in one scan;
// every open and close keeps them current after. Caller holds runLog.mu.
func scanStepFactsLocked(rec *runRecord, l *chat.EntryLog) error {
	if rec.ends != nil {
		return nil
	}
	all, err := l.All()
	if err != nil {
		return err
	}
	rec.ends, rec.starts = scanStepFacts(all)
	return nil
}

// scanStepFacts folds a log's turn_open paths and turn_close outcomes in file order into each path's broken end, so a
// path's newest turn wins, and its current attempt.
func scanStepFacts(all []marotte.Entry) (ends map[string]marotte.RunStepEnd, starts map[string]stepAttempt) {
	ends = make(map[string]marotte.RunStepEnd)
	starts = make(map[string]stepAttempt)
	paths := make(map[string]string)
	for i := range all {
		e := &all[i]
		switch e.Kind {
		case marotte.EntryKindTurnOpen:
			var to marotte.EntryTurnOpen
			if json.Unmarshal(e.Payload, &to) == nil && to.NodePath != "" {
				paths[e.Turn] = to.NodePath
				noteAttemptOpen(starts, to.NodePath, e.Ts)
			}
		case marotte.EntryKindTurnClose:
			var tc marotte.EntryTurnClose
			if path, ok := paths[e.Turn]; ok && json.Unmarshal(e.Payload, &tc) == nil {
				noteStepEnd(ends, path, tc.Outcome, tc.FailureReason, tc.FailureKind)
				noteAttemptClose(starts, path, tc.Outcome)
			}
		}
	}
	return ends, starts
}

// stepAttempt is one path's current attempt: when its first turn opened, whether its newest turn is open, and
// whether that turn's close was an interruption (a bridge death or a restart), which the path's next turn continues.
type stepAttempt struct {
	at          int64
	open        bool
	interrupted bool
}

// noteAttemptOpen records a path's turn_open: a new attempt unless the path's newest turn is still open or was
// interrupted, because KAS resumes that node rather than starting it again.
func noteAttemptOpen(starts map[string]stepAttempt, path string, ts int64) {
	a, ok := starts[path]
	if !ok || (!a.open && !a.interrupted) {
		a.at = ts
	}
	a.open, a.interrupted = true, false
	starts[path] = a
}

// noteAttemptClose records a path's turn_close; a path with no recorded open is left alone.
func noteAttemptClose(starts map[string]stepAttempt, path string, o marotte.TurnOutcome) {
	a, ok := starts[path]
	if !ok {
		return
	}
	a.open, a.interrupted = false, o == marotte.TurnOutcomeInterrupted
	starts[path] = a
}

// StepStarts answers each node path's current attempt, scanning the log on the first read of the process; nil with
// no log.
func (r *runLog) StepStarts(ctx context.Context, workflowID string) (map[string]marotte.RunStepStart, error) {
	l, err := r.Log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return nil, nil
	}
	if err := scanStepFactsLocked(rec, l); err != nil {
		return nil, err
	}
	out := make(map[string]marotte.RunStepStart, len(rec.starts))
	for path, a := range rec.starts {
		out[path] = marotte.RunStepStart{
			StartedAt: time.UnixMilli(a.at).UTC().Format(time.RFC3339Nano),
			Ended:     !a.open && !a.interrupted,
		}
	}
	return out, nil
}

// runOutcome maps KAS's status to `completed`, `failed`, `cancelled`, or `unknown` with the status as raw stop.
func runOutcome(status string) marotte.TurnConclusion {
	c := marotte.TurnConclusion{RawStop: marotte.StopReason(status), Known: true}
	switch marotte.RunStatus(status) {
	case marotte.RunStatusCompleted:
		c.Outcome = marotte.TurnOutcomeCompleted
	case marotte.RunStatusFailed:
		c.Outcome = marotte.TurnOutcomeFailed
	case marotte.RunStatusCancelled:
		c.Outcome = marotte.TurnOutcomeCancelled
	case marotte.RunStatusAborted, marotte.RunStatusRunning, marotte.RunStatusPaused:
		c.Outcome = marotte.TurnOutcomeUnknown
		c.Known = false
	default:
		c.Outcome = marotte.TurnOutcomeUnknown
		c.Known = false
	}
	return c
}

// sortedKeys orders a map's keys, so a multi-path close writes deterministically.
func sortedKeys(m map[string]*runTurn) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
