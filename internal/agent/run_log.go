package agent

// The run store's registry and appender: one entry log per run under `<root>/<workflowId>/`, one turnlog
// accumulator per open step, and two routing maps. KAS owns run state, so the log has no header and is
// never rewound, merged or paged by number.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

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
	// endWait is closed at the current KAS turn's turn_end, made only when a heal waits for it (runLog.askTurnEnd).
	endWait chan struct{}
	// tail is the newest line the turn's own lane printed (printedLine), nil until it prints; written by the sink.
	tail atomic.Pointer[string]
	// lastError is the newest model error KAS displayed in the step's current KAS turn, zero when none.
	lastError marotte.EngineError
	// rawStop is the last turn_end's stop reason, carried by the close.
	rawStop marotte.StopReason
	path    string
	// top is the run's top-level step this one sits under ("" when unknown), held because path is never
	// split back (workflow.PathKey).
	top string
	// answer folds the step's current KAS turn: the sink feeds it, and a turn_start resets it.
	answer answerFold
	// ownEnd is whether the step's newest edge is its own turn's turn_end, with no turn_start or node_paused after.
	ownEnd bool
	// parked is whether KAS paused the node with no node_start since: a turn a steer runs on a parked node does not
	// resume it, so its end is not the step's.
	parked bool
	// errStop is whether the step's current KAS turn ended `error`. Unlike ownEnd it outlives the node_paused that
	// follows the failing turn_end, which the throttled-ask heal reads (runLog.transientFailure).
	errStop bool
	// asked is whether the step sent a warning in its current KAS turn, and answered whether a message reached the
	// step after it in that turn (runLog.answeredAsk).
	asked, answered bool
	// ended is whether the current KAS turn's turn_end arrived; unlike ownEnd it outlives node_paused.
	ended bool
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
	s.turn.answer.see(e)
	if line := printedLine(e); line != "" {
		s.turn.tail.Store(&line)
	}
	return nil
}

// runRecord is one run: its log, open turns by node path, newest closed turn per path, and the bridge whose death closes them.
type runRecord struct {
	log    *chat.EntryLog
	open   map[string]*runTurn
	closed map[string]string
	// staged holds, per path, the user's words a paused step resumes on, until KAS reopens the path's
	// turn: they become that turn's prompt.
	staged map[string]*marotte.EntryPrompt
	// closes is each path's newest turn_close, nil until a read scans the log; every close then keeps it current.
	closes map[string]stepClose
	// starts is each path's current attempt, nil until a read scans the log; every open and close then keeps it current.
	starts map[string]stepAttempt
	// turns is each path's turn ids in file order, nil until a read scans the log; every open then keeps it current.
	turns map[string][]string
	// paths is each step turn's path by turn id, kept with turns.
	paths map[string]string
	// noteLeases is, per path, the lease node_complete read for the close it sealed, until a trigger records that
	// close's grade: closeNode is the only writer, gradeLocked the only reader.
	noteLeases map[string]noteLease
	host       marotte.ChatID
	// removed is the tombstone: a frame arriving after Delete opens nothing.
	removed bool
}

// One mutex, held across the store call; nothing here runs under the log's own lock.
type runLog struct {
	runs map[string]*runRecord
	// steps learns each step turn a chat's session hosts, from its open to its close; nil records none.
	steps stepHolds
	// removeAll is removeDir's os.RemoveAll, a field so a test can fail one removal as root cannot be refused.
	removeAll func(path string) error
	root      string
	mu        sync.Mutex
}

// stepHolds is the turn registry as the run log reports a hosted step turn: the host's session may not close while
// the turn is open. Both run under runLog.mu (lock order runLog.mu -> turnRegistry.mu -> chatLifecycle.mu).
type stepHolds interface {
	holdStep(host marotte.ChatID, turnID string)
	endStep(host marotte.ChatID, turnID string)
}

var _ stepHolds = (*turnRegistry)(nil)

func newRunLog(configDir string) *runLog {
	return &runLog{
		runs:      make(map[string]*runRecord),
		removeAll: os.RemoveAll,
		root:      filepath.Join(configDir, runLogDir),
	}
}

// dir is a run's directory, refused for an id that is not a plain name.
func (r *runLog) dir(workflowID string) (string, error) {
	if !ids.ValidChatID(workflowID) {
		return "", errRunIDInvalid
	}
	return filepath.Join(r.root, workflowID), nil
}

// recorded lists the workflow ids with a directory under the root; a missing root is none. A name runLog.dir would
// refuse is skipped.
func (r *runLog) recorded() ([]string, error) {
	entries, err := os.ReadDir(r.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list run records: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !ids.ValidChatID(e.Name()) {
			slog.Debug("run records: skipped an entry that names no run", "name", logsafe.Field(e.Name()))
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// A deleted run answers errRunLogRemoved until restart.
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

// log answers a run's log for a read, or nil with no directory: a read must not create one.
func (r *runLog) log(ctx context.Context, workflowID string) (*chat.EntryLog, error) {
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

// turnHighWater is the number of turns the run's log holds, 0 with no log.
func (r *runLog) turnHighWater(ctx context.Context, workflowID string) (uint64, error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return 0, err
	}
	return l.TurnHighWater(), nil
}

// stepReadHook runs between a turn read's snapshot and its decode; nil outside tests.
var stepReadHook func()

// openTail is a step turn's open tails and id as a read snapshots them; id is "" when the turn is not open.
type openTail struct {
	id      string
	entries []marotte.OpenEntry
}

// stamps is the tail's run_turn stamp at seq newest, which comes from the served entries, never the record's counter:
// that advances outside the lock. None when the turn is not open.
func (o openTail) stamps(workflowID string, newest uint64) []marotte.SubjectStamp {
	if o.id == "" {
		return nil
	}
	return []marotte.SubjectStamp{{Kind: string(subject.KindRunTurn), Ref: workflowID + "/" + o.id, Version: turnVersion(o.id, newest)}}
}

// newestSeqOf is turn's newest seq among entries, 0 when none is its.
func newestSeqOf(entries []marotte.Entry, turn string) uint64 {
	for _, e := range slices.Backward(entries) {
		if e.Turn == turn {
			return e.Seq
		}
	}
	return 0
}

// decodeTurns decodes a snapshot taken under the locks, holding neither.
func decodeTurns(read *chat.TurnRead) ([]marotte.Entry, error) {
	if stepReadHook != nil {
		stepReadHook()
	}
	return read.Entries()
}

// stepTurns is one step path's transcript: its turns in file order, open tails, and one run_turn stamp per
// open turn. found is false with no such turn.
func (r *runLog) stepTurns(ctx context.Context, workflowID, nodePath string) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []marotte.SubjectStamp, found bool, err error) {
	read, tail, err := r.stepSnapshot(ctx, workflowID, nodePath, false)
	if err != nil || read == nil {
		return nil, nil, nil, false, err
	}
	if entries, err = decodeTurns(read); err != nil {
		return nil, nil, nil, false, err
	}
	return entries, tail.entries, tail.stamps(workflowID, newestSeqOf(entries, tail.id)), true, nil
}

// stepSnapshot takes the read of a step path's turns (its newest only when newest) and its open tails, nil with no
// such turn. The path's turn ids come from the record, so no other step's lines are read.
func (r *runLog) stepSnapshot(ctx context.Context, workflowID, nodePath string, newest bool) (*chat.TurnRead, openTail, error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, openTail{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return nil, openTail{}, nil
	}
	if scanErr := scanStepFactsLocked(rec, l); scanErr != nil {
		return nil, openTail{}, scanErr
	}
	turns := rec.turns[nodePath]
	if newest && len(turns) > 0 {
		turns = turns[len(turns)-1:]
	}
	var tail openTail
	if t := rec.open[nodePath]; t != nil {
		tail = openTail{id: t.turn.ID(), entries: t.turn.OpenEntries()}
	}
	read, err := l.SnapshotTurns(turns, 0)
	return read, tail, err
}

// turnRange is one step turn's tail from seq `from` inclusive (0 the whole turn), its open tails and its
// stamp. found is false for no log or no such turn; err is an unreadable log (500).
func (r *runLog) turnRange(ctx context.Context, workflowID, turn string, from uint64) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []marotte.SubjectStamp, found bool, err error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, nil, nil, false, err
	}
	r.mu.Lock()
	var tail openTail
	if rec, ok := r.runs[workflowID]; ok {
		for _, t := range rec.open {
			if t.turn.ID() == turn {
				tail = openTail{id: turn, entries: t.turn.OpenEntries()}
			}
		}
	}
	read, err := l.SnapshotTurns([]string{turn}, from)
	r.mu.Unlock()
	if err != nil || read == nil {
		return nil, nil, nil, false, err
	}
	if entries, err = decodeTurns(read); err != nil {
		return nil, nil, nil, false, err
	}
	// The page speaks for its newest entry, or for the seq before from when empty.
	newest := from - 1
	if n := len(entries); n > 0 {
		newest = entries[n-1].Seq
	}
	return entries, tail.entries, tail.stamps(workflowID, newest), true, nil
}

// lastStepMessage is a step path's last words (lastWords) in its newest turn of the run's own record, "" when the
// record holds none or cannot be read.
func (r *runLog) lastStepMessage(ctx context.Context, workflowID, nodePath string) string {
	read, _, err := r.stepSnapshot(ctx, workflowID, nodePath, true)
	if err != nil || read == nil {
		return ""
	}
	entries, err := decodeTurns(read)
	if err != nil {
		return ""
	}
	return lastWords(entries)
}

// lastWords is a step turn's newest send_message message, else its trailing prose run: the step's own text
// entries back to the nearest other entry, one paragraph each. An applied model_fallback stays inside the run,
// because KAS continues the refused text on the fallback model; a sub-agent's fallback belongs to no step prose.
func lastWords(turn []marotte.Entry) string {
	var run []string
	ended := false
	for i := len(turn) - 1; i >= 0; i-- {
		e := &turn[i]
		if m := sentMessage(e); m != "" {
			return m
		}
		if ended {
			continue
		}
		switch {
		case e.Kind == marotte.EntryKindText && e.Lane == "":
			if s := entryProse(e.Payload); s != "" {
				run = append(run, s)
			}
		case e.Kind == marotte.EntryKindModelFallback && fallbackInsideProse(e.Payload):
		default:
			ended = len(run) > 0
		}
	}
	slices.Reverse(run)
	return strings.Join(run, "\n\n")
}

// fallbackInsideProse reports a model_fallback that leaves the step's prose run open: an applied one (a target and
// no decline reason) or a sub-agent's.
func fallbackInsideProse(payload json.RawMessage) bool {
	var f marotte.EntryModelFallback
	if json.Unmarshal(payload, &f) != nil {
		return false
	}
	return f.Subagent || (f.To != "" && f.Reason == "")
}

// entryProse is a text entry's text, trimmed; "" when it does not decode.
func entryProse(payload json.RawMessage) string {
	var t marotte.EntryText
	if json.Unmarshal(payload, &t) != nil {
		return ""
	}
	return strings.TrimSpace(t.Text)
}

// sentMessage is a send_message call's message: a tool call whose input holds both `message` and `severity`.
func sentMessage(e *marotte.Entry) string {
	if e.Kind != marotte.EntryKindToolCall {
		return ""
	}
	var call marotte.EntryToolCall
	if json.Unmarshal(e.Payload, &call) != nil || len(call.Input) == 0 {
		return ""
	}
	var in struct {
		Message  string `json:"message"`
		Severity string `json:"severity"`
	}
	if json.Unmarshal(call.Input, &in) != nil || in.Severity == "" {
		return ""
	}
	return strings.TrimSpace(in.Message)
}

// steerSeverityWarning is the send_message severity that parks a run, on a steer row.
const steerSeverityWarning = "warning"

// steerTurn is turn, recording steer on the open turn it answers for runTurn.asked and runTurn.answered.
func (r *runLog) steerTurn(workflowID, nodePath string, steer *marotte.EntrySteer) *turnlog.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return nil
	}
	switch {
	case steer.Origin == marotte.SteerOriginStep && steer.Severity == steerSeverityWarning:
		t.asked, t.answered = true, false
	case steer.Origin != marotte.SteerOriginStep && t.asked:
		t.answered = true
	}
	return t.turn
}

// answeredAsk is whether the step's current KAS turn sent a warning that a message then reached.
func (r *runLog) answeredAsk(workflowID, nodePath string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	return t != nil && t.asked && t.answered
}

// askTurnEnd answers a channel closed at the turn_end of an open step turn of the run that asked in its current KAS
// turn and has not ended it; nil when there is none.
func (r *runLog) askTurnEnd(workflowID string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil
	}
	for _, path := range slices.Sorted(maps.Keys(rec.open)) {
		if t := rec.open[path]; t.asked && !t.ended {
			if t.endWait == nil {
				t.endWait = make(chan struct{})
			}
			return t.endWait
		}
	}
	return nil
}

// turn answers the open turn for a step, or nil.
func (r *runLog) turn(workflowID, nodePath string) *turnlog.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.openLocked(workflowID, nodePath); t != nil {
		return t.turn
	}
	return nil
}

func (r *runLog) hasClosed(workflowID, nodePath string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.runs[workflowID]; ok {
		_, closed := rec.closed[nodePath]
		return closed
	}
	return false
}

// openSeqs answers every open turn of a run as turn id to newest seq: the digest's
// `run_turn` arm.
func (r *runLog) openSeqs(workflowID string) map[string]uint64 {
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

// hasOpen reports whether any step turn of the run is open.
func (r *runLog) hasOpen(workflowID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	return ok && len(rec.open) > 0
}

// openTops answers, in node path order, the top-level step each of the run's open step turns sits
// under, skipping a turn whose opening frame carried no path.
func (r *runLog) openTops(workflowID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil
	}
	var tops []string
	for _, path := range slices.Sorted(maps.Keys(rec.open)) {
		if top := rec.open[path].top; top != "" {
			tops = append(tops, top)
		}
	}
	return tops
}

// open opens the step's turn, keyed by its NodePath, when none is open and answers it; opened is the
// appended turn_open, nil when already open. The frame's chat id becomes the run's host when unset
// (`run:<id>` for an empty one).
func (r *runLog) open(ctx context.Context, step *translate.RunStep, chatID marotte.ChatID) (turn *turnlog.Turn, opened *marotte.Entry, err error) {
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
		// KAS sends node_start twice per step, and again when it resumes a parked one; each continues this turn.
		t.parked = false
		if t.top == "" {
			t.top = topStepOf(step)
		}
		return t.turn, nil, nil
	}
	e, err := rec.log.OpenTurn(ctx, &chat.TurnSpec{
		Prompt:    rec.staged[nodePath],
		Source:    marotte.TurnOpenNameWorkflowStep,
		Run:       workflowID,
		NodePath:  nodePath,
		SessionID: step.SessionID,
	})
	if err != nil {
		return nil, nil, err
	}
	delete(rec.staged, nodePath)
	t := &runTurn{path: nodePath, top: topStepOf(step)}
	t.turn = turnlog.Open(e.ID, runSink{log: rec.log, turn: t})
	if r.steps != nil {
		r.steps.holdStep(rec.host, e.ID)
	}
	rec.open[nodePath] = t
	if rec.starts != nil {
		noteAttemptOpen(rec.starts, nodePath, e.Ts)
		rec.turns[nodePath] = append(rec.turns[nodePath], e.ID)
		rec.paths[e.ID] = nodePath
	}
	return t.turn, e, nil
}

// False when the path's turn is open: KAS continues an open turn, so no turn_open would carry them.
func (r *runLog) stagePrompt(ctx context.Context, workflowID, nodePath string, p *marotte.EntryPrompt) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.recordLocked(ctx, workflowID)
	if err != nil {
		return false, err
	}
	if rec.open[nodePath] != nil {
		return false, nil
	}
	if rec.staged == nil {
		rec.staged = make(map[string]*marotte.EntryPrompt)
	}
	rec.staged[nodePath] = p
	return true, nil
}

func (r *runLog) unstagePrompt(workflowID, nodePath, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.staged[nodePath] == nil || rec.staged[nodePath].ID != id {
		return
	}
	delete(rec.staged, nodePath)
}

// For a run that will open no further turn.
func (r *runLog) takeStaged(workflowID string) map[string]*marotte.EntryPrompt {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || len(rec.staged) == 0 {
		return nil
	}
	out := rec.staged
	rec.staged = nil
	return out
}

// hostFor is the host a frame's chat id names: itself, or the parentless bridge's key for an empty id.
func hostFor(workflowID string, chatID marotte.ChatID) marotte.ChatID {
	if chatID == "" {
		return runChatID(workflowID)
	}
	return chatID
}

// appendAfterClosed files a content entry after the newest closed turn of its path; false when none is known, so the caller opens one.
func (r *runLog) appendAfterClosed(ctx context.Context, workflowID, nodePath string, e *marotte.Entry) (bool, error) {
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

// meter folds a step's turn_completion into its open turn; false with no open turn.
func (r *runLog) meter(workflowID, nodePath string, credits, elapsedMs float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return false
	}
	t.turn.Meter(credits, elapsedMs)
	return true
}

// stopReason records a step's turn_end stop reason on its open turn; the last wins.
func (r *runLog) stopReason(workflowID, nodePath string, raw marotte.StopReason) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return false
	}
	t.rawStop = raw
	t.ownEnd = true
	t.errStop = raw == marotte.StopReasonError
	t.ended = true
	if t.endWait != nil {
		close(t.endWait)
		t.endWait = nil
	}
	return true
}

// stepError records a model error KAS displayed for a step on its open turn; false with no open turn.
func (r *runLog) stepError(workflowID, nodePath string, e marotte.EngineError) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return false
	}
	t.lastError = e
	return true
}

// transientFailure answers the error class when the step's current KAS turn ended `error` on a model error a retry
// can clear (rpcerr.Mapped.Transient); ok is false otherwise or with no open turn.
func (r *runLog) transientFailure(workflowID, nodePath string) (code string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return "", false
	}
	return t.transientFailure()
}

// stepFailure is an open step turn whose current KAS turn ended `error`: its path, its error class, and whether that
// class is transient (runTurn.transientFailure).
type stepFailure struct {
	path, code string
	transient  bool
}

// failedStep answers the run's open step turn, first in path order, whose KAS turn ended `error`, a transient failure
// before any other; the zero value with none.
func (r *runLog) failedStep(workflowID string) stepFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return stepFailure{}
	}
	var out stepFailure
	for _, path := range slices.Sorted(maps.Keys(rec.open)) {
		t := rec.open[path]
		if !t.errStop {
			continue
		}
		if code, transient := t.transientFailure(); transient {
			return stepFailure{path: path, code: code, transient: true}
		}
		if out.path == "" {
			out = stepFailure{path: path, code: t.lastError.ErrorType}
		}
	}
	return out
}

func (t *runTurn) transientFailure() (code string, ok bool) {
	m := rpcerr.Mapped{ErrorType: t.lastError.ErrorType, RetryErrorType: t.lastError.RetryErrorType}
	if !t.errStop || !m.Transient() {
		return "", false
	}
	return t.lastError.ErrorType, true
}

// turnStarted records a step's turn_start on its open turn; false with no open turn.
func (r *runLog) turnStarted(workflowID, nodePath string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.openLocked(workflowID, nodePath)
	if t == nil {
		return false
	}
	t.ownEnd = false
	t.errStop = false
	t.ended = false
	t.asked, t.answered = false, false
	t.lastError = marotte.EngineError{}
	t.answer.reset()
	return true
}

// paused records a step's node_paused on its open turn: whatever ends the step next, its turn did not.
func (r *runLog) paused(workflowID, nodePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.openLocked(workflowID, nodePath); t != nil {
		t.ownEnd, t.parked = false, true
	}
}

// endedOffTurn is whether something other than the step's own turn ended it. KAS completes a step at its turn's end
// only when nothing else ended it; Mark step done or a parent's update_status lands on a parked node or inside a
// turn, and KAS clears the step's signal on that path too (applyPausedStepFinish), so only this tells them apart.
func (t *runTurn) endedOffTurn() bool {
	return !t.ownEnd || t.parked
}

// unanswered is whether the step's own turn ended it having said nothing.
func (t *runTurn) unanswered() bool {
	return !t.endedOffTurn() && t.answer.silent()
}

func (r *runLog) openLocked(workflowID, nodePath string) *runTurn {
	if rec, ok := r.runs[workflowID]; ok {
		return rec.open[nodePath]
	}
	return nil
}

// closeNode closes the step's turn with KAS's node_complete status, answering everything it sealed; closed
// is false with no open turn. note, the run's lease when it names a chat, is held for the close in the same hold
// of runLog.mu that makes the close gradable, so whichever trigger records its grade notes under it (holdNoteLocked).
// A node_complete that closes nothing holds none: no other closer's close can call for a note (stepClose.mayNote).
func (r *runLog) closeNode(
	ctx context.Context, workflowID, nodePath, status, reason string, note *runlease.Lease,
) (sealed []turnlog.Sealed, closed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return nil, false, nil
	}
	t := rec.open[nodePath]
	if t == nil {
		if marotte.RunStatus(status) == marotte.RunStatusCompleted {
			reopenGradeLocked(rec, nodePath)
		}
		return nil, false, nil
	}
	c := runOutcome(status)
	c.Reason = reason
	// KAS grades a refused step `completed`; at close time the step's last turn_end outranks that status. A tool_use
	// stop does not: only KAS's node record, which node_complete does not carry, says whether it was the model-call
	// limit. runLog.gradeLocked owns the grade under KAS's record.
	if stop := marotte.ConcludeStopReason(t.rawStop); stop.Outcome == marotte.TurnOutcomeRefused {
		c = stop
		c.Reason = reason
	}
	// An unmapped status is itself the raw stop, keeping it recoverable.
	if c.Known && t.rawStop != "" {
		c.RawStop = t.rawStop
	}
	// The fold reads sealed entries, and the answer is still coalescing until a seal.
	sealed, err = t.turn.SealOpen(ctx)
	c.Unanswered = err == nil && t.unanswered()
	c.EndedOffTurn = t.endedOffTurn()
	closing, cerr := r.closeLocked(ctx, rec, t, c)
	sc := sealedClose(closing, t.turn.ID(), &c)
	holdNoteLocked(rec, nodePath, &sc, status, note)
	return append(sealed, closing...), true, errors.Join(err, cerr)
}

// holdNoteLocked files note as the note lease of c, the path's newest close, when KAS completed its node and c may
// call for a note: only a completed node's close is ever graded. Any older close's lease on the path goes. Caller
// holds runLog.mu.
func holdNoteLocked(rec *runRecord, path string, c *stepClose, status string, note *runlease.Lease) {
	delete(rec.noteLeases, path)
	if note == nil || marotte.RunStatus(status) != marotte.RunStatusCompleted || !c.mayNote() {
		return
	}
	if rec.noteLeases == nil {
		rec.noteLeases = make(map[string]noteLease)
	}
	rec.noteLeases[path] = noteLease{lease: *note, turn: c.turn}
}

// reopenGradeLocked makes the path's newest close due a grade again after gradeLocked settled it: a completed
// node_complete that closes no turn is KAS completing the node that close ran, since the reads that settled it.
// Caller holds runLog.mu.
func reopenGradeLocked(rec *runRecord, path string) {
	if c, ok := rec.closes[path]; ok {
		c.misses, c.settled = 0, false
		rec.closes[path] = c
	}
}

// closeRunTurns closes every open turn of a run with one outcome and, when terminal, drops the host.
func (r *runLog) closeRunTurns(ctx context.Context, workflowID string, c marotte.TurnConclusion, terminal bool) ([]turnlog.Sealed, error) {
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

// closeHost closes every open turn of every run chatID's ended session hosted (the death closer's run arm),
// answering sealed entries per run.
func (r *runLog) closeHost(ctx context.Context, chatID marotte.ChatID, c marotte.TurnConclusion) (out map[string][]turnlog.Sealed, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for id, rec := range r.runs {
		if rec.host != chatID {
			continue
		}
		if len(rec.open) == 0 {
			continue
		}
		sealed, closeErr := r.closeAllLocked(ctx, rec, c)
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", id, closeErr))
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

// delete closes every open turn `cancelled`, drops the maps and tombstones the log; RemoveDir is last,
// after KAS's delete and clearEnd.
func (r *runLog) delete(ctx context.Context, workflowID string) ([]turnlog.Sealed, error) {
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
	rec.staged = nil
	rec.noteLeases = nil
	rec.host = ""
	rec.removed = true
	if rec.log != nil {
		err = errors.Join(err, rec.log.Remove())
	}
	return sealed, err
}

// removeDir removes the run's directory, only after Delete: the tombstone stops a late frame recreating it.
func (r *runLog) removeDir(workflowID string) error {
	if workflowID == "" {
		return nil
	}
	dir, err := r.dir(workflowID)
	if err != nil {
		return err
	}
	err = r.removeAll(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

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

// The path leaves `open` whatever the close answered: a refused write latched the log.
func (r *runLog) closeLocked(ctx context.Context, rec *runRecord, t *runTurn, c marotte.TurnConclusion) ([]turnlog.Sealed, error) {
	sealed, err := t.turn.Close(ctx, c)
	delete(rec.open, t.path)
	if r.steps != nil {
		r.steps.endStep(rec.host, t.turn.ID())
	}
	rec.closed[t.path] = t.turn.ID()
	if rec.closes != nil {
		rec.closes[t.path] = sealedClose(sealed, t.turn.ID(), &c)
	}
	if rec.starts != nil {
		noteAttemptClose(rec.starts, t.path, c.Outcome)
		if line := t.tail.Load(); line != nil {
			noteAttemptLine(rec.starts, t.path, *line)
		}
	}
	return sealed, err
}

// sealedClose is the close a closer sealed, through the scan's constructor so a live close and a scanned one cannot
// differ: the conclusion, the fallback when the turn_close does not decode, has no refusal.
func sealedClose(sealed []turnlog.Sealed, turn string, c *marotte.TurnConclusion) stepClose {
	if tc, ok := turnCloseOf(sealed); ok {
		return stepCloseOf(&tc, turn)
	}
	return stepClose{
		outcome: c.Outcome, reason: c.Reason, kind: c.FailureKind, rawStop: c.RawStop, unanswered: c.Unanswered,
		offTurn: c.EndedOffTurn, turn: turn,
	}
}

// stepEnds answers the run's step ends by node path (stepClose.end), first recording the grade of each path's newest
// close that kas, KAS's record of the run's steps by PathKey (nil when it was not read), makes due; graded is what it
// recorded. It scans the log on the first read of the process; nil with no log.
func (r *runLog) stepEnds(ctx context.Context, workflowID string, kas map[string]workflow.StepRecord) (ends map[string]marotte.RunStepEnd, graded []*stepGrade, err error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return nil, nil, nil
	}
	if scanErr := scanStepFactsLocked(rec, l); scanErr != nil {
		return nil, nil, scanErr
	}
	if kas != nil {
		graded, err = r.gradeDueLocked(ctx, rec, slices.Sorted(maps.Keys(rec.closes)), kasRead{steps: kas, ok: true})
	}
	return stepEndsLocked(rec, kas), graded, err
}

// kasRead is one read of KAS's record of a run's steps by PathKey, as gradeLocked weighs it: ok is false when the read
// failed, and last marks the run's terminal run_complete, the final read any close of the run is due.
type kasRead struct {
	steps map[string]workflow.StepRecord
	ok    bool
	last  bool
}

// gradeReadAttempts bounds the reads of KAS's record one close is due that each leave it ungraded (a failed read, a
// node still running, a refused write): a transient fault clears within a trigger or two, and a node KAS later
// completes reopens the close (reopenGradeLocked).
const gradeReadAttempts = 3

// gradeDueLocked grades each of paths' newest close under k (gradeLocked), in order. Caller holds runLog.mu and has
// scanned the record's step facts.
func (r *runLog) gradeDueLocked(ctx context.Context, rec *runRecord, paths []string, k kasRead) ([]*stepGrade, error) {
	var graded []*stepGrade
	var errs []error
	for _, path := range paths {
		g, err := r.gradeLocked(ctx, rec, path, k)
		if err != nil {
			errs = append(errs, err)
		}
		if g != nil {
			graded = append(graded, g)
		}
	}
	return graded, errors.Join(errs...)
}

// stepEndsLocked answers each path's step end from its newest close (stepClose.end). Caller holds runLog.mu.
func stepEndsLocked(rec *runRecord, kas map[string]workflow.StepRecord) map[string]marotte.RunStepEnd {
	ends := make(map[string]marotte.RunStepEnd)
	for path, c := range rec.closes {
		if end, ok := stepEndLocked(rec, path, &c, kas); ok {
			ends[path] = end
		}
	}
	return ends
}

func stepEndLocked(rec *runRecord, path string, c *stepClose, kas map[string]workflow.StepRecord) (marotte.RunStepEnd, bool) {
	end, ok := c.end()
	if ok && end.Outcome == marotte.TurnOutcomeInterrupted {
		st, known := kas[path]
		// A grade is recorded only under a completed record, so a graded interruption was resumed.
		end.Resumed = rec.starts[path].open || c.hasGrade || (known && st.Completed())
	}
	return end, ok
}

// stepRow is one step path's end and start rows, nil for a row the path has none of.
type stepRow struct {
	end   *marotte.RunStepEnd
	start *marotte.RunStepStart
	path  string
}

// stepRowOfTurn answers the rows of the path step turn turn ran on, as stepEnds and stepStarts answer them, recording
// no grade; false when the log holds no step turn by that id.
func (r *runLog) stepRowOfTurn(ctx context.Context, workflowID, turn string, kas map[string]workflow.StepRecord) (stepRow, bool, error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return stepRow{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return stepRow{}, false, nil
	}
	if err := scanStepFactsLocked(rec, l); err != nil {
		return stepRow{}, false, err
	}
	path, ok := rec.paths[turn]
	if !ok {
		return stepRow{}, false, nil
	}
	row := stepRow{path: path}
	if c, has := rec.closes[path]; has {
		if end, ok := stepEndLocked(rec, path, &c, kas); ok {
			row.end = &end
		}
	}
	if a, has := rec.starts[path]; has {
		start := stepStartLocked(rec, path, a)
		row.start = &start
	}
	return row, true, nil
}

// dueGradeLocked is whether the path's newest close c needs a grade under KAS's record and a read may still be spent
// on it (stepClose.settled). A turn open on the path is a newer execution KAS's record already describes, so a grade
// filed then would paint c with its result. A turn a crash left open is closed unterminated when the log opens, so
// only rec.open can hold one.
func dueGradeLocked(rec *runRecord, path string, c *stepClose) bool {
	return c.turn != "" && !c.hasGrade && !c.settled && c.mayBeBroken() && rec.open[path] == nil
}

// gradeLocked records the grade of the path's newest close, when due, under KAS's completed record of its node in k,
// and answers it. Otherwise it answers nil and the close stays due, or is settled: at once when k holds no node at
// the path or one KAS ended without completing, at the run's last read, or at its gradeReadAttempts-th ungraded read.
// It is the only grader of a close the run log holds and the only one to settle it (reopenGradeLocked undoes that).
// Caller holds runLog.mu and has scanned the record's step facts.
func (*runLog) gradeLocked(ctx context.Context, rec *runRecord, path string, k kasRead) (*stepGrade, error) {
	c, ok := rec.closes[path]
	if !ok || !dueGradeLocked(rec, path, &c) {
		return nil, nil
	}
	kas, found := k.steps[path]
	if !kas.Completed() {
		c.missed(k.last || (k.ok && (!found || kas.Ended())))
		rec.closes[path] = c
		return nil, nil
	}
	p, graded := c.recordedGrade(kas)
	payload, err := json.Marshal(&p)
	if err != nil {
		return nil, err
	}
	e := &marotte.Entry{Turn: c.turn, Kind: marotte.EntryKindStepGraded, Payload: payload}
	if err := rec.log.Append(ctx, e); err != nil {
		c.missed(k.last)
		rec.closes[path] = c
		return nil, err
	}
	rec.closes[path] = graded
	g := &stepGrade{entry: e, nodePath: path, grade: graded, sessionID: kas.SessionID}
	if n, held := rec.noteLeases[path]; held {
		delete(rec.noteLeases, path)
		if n.turn == c.turn {
			g.note = &n.lease
		}
	}
	return g, nil
}

// missed counts one read of KAS's record that left c ungraded, settling c when final or at gradeReadAttempts.
func (c *stepClose) missed(final bool) {
	c.misses++
	c.settled = final || c.misses >= gradeReadAttempts
}

// gradeRun records the grade of every path's newest close that is due one, reading KAS's record of the run's steps
// through read only when one is, and answers what it recorded; last marks the run's terminal run_complete (kasRead).
// No lock is held across read, so a close that changed meanwhile is left to the next grade.
func (r *runLog) gradeRun(ctx context.Context, workflowID string, read func() (map[string]workflow.StepRecord, bool), last bool) ([]*stepGrade, error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, err
	}
	due, err := r.dueTurns(workflowID, l)
	if err != nil || len(due) == 0 {
		return nil, err
	}
	kas, known := read()
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return nil, nil
	}
	paths := make([]string, 0, len(due))
	for path, turn := range due {
		if rec.closes[path].turn == turn {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return r.gradeDueLocked(ctx, rec, paths, kasRead{steps: kas, ok: known, last: last})
}

// dueTurns is, by path, the turn of each newest close due a grade.
func (r *runLog) dueTurns(workflowID string, l *chat.EntryLog) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return nil, nil
	}
	if err := scanStepFactsLocked(rec, l); err != nil {
		return nil, err
	}
	due := make(map[string]string)
	for path, c := range rec.closes {
		if dueGradeLocked(rec, path, &c) {
			due[path] = c.turn
		}
	}
	return due, nil
}

// noteLease is the lease node_complete read for the close of turn (holdNoteLocked).
type noteLease struct {
	turn  string
	lease runlease.Lease
}

// scanStepFactsLocked fills the record's step closes, attempt starts and turn ids from its log once per process, in
// one scan; every open and close keeps them current after. Caller holds runLog.mu.
func scanStepFactsLocked(rec *runRecord, l *chat.EntryLog) error {
	if rec.closes != nil {
		return nil
	}
	all, err := l.All()
	if err != nil {
		return err
	}
	sc := scanStepFacts(all)
	rec.closes, rec.starts, rec.turns, rec.paths = sc.closes, sc.starts, sc.turns, sc.paths
	return nil
}

// scanStepFacts folds a log's turn_open paths, turn_close and step_graded entries in file order into each path's
// newest close, under its grade, its current attempt, its turn ids and each turn's path.
func scanStepFacts(all []marotte.Entry) *stepFactScan {
	sc := &stepFactScan{
		closes: make(map[string]stepClose),
		starts: make(map[string]stepAttempt),
		turns:  make(map[string][]string),
		paths:  make(map[string]string),
	}
	for i := range all {
		sc.see(&all[i])
	}
	return sc
}

// stepFactScan is scanStepFacts' fold, with each turn's path as its turn_open named it.
type stepFactScan struct {
	closes map[string]stepClose
	starts map[string]stepAttempt
	turns  map[string][]string
	paths  map[string]string
}

func (sc *stepFactScan) see(e *marotte.Entry) {
	switch e.Kind {
	case marotte.EntryKindTurnOpen:
		var to marotte.EntryTurnOpen
		if json.Unmarshal(e.Payload, &to) == nil && to.NodePath != "" {
			sc.paths[e.Turn] = to.NodePath
			sc.turns[to.NodePath] = append(sc.turns[to.NodePath], e.Turn)
			noteAttemptOpen(sc.starts, to.NodePath, e.Ts)
		}
	case marotte.EntryKindTurnClose:
		var tc marotte.EntryTurnClose
		if path, ok := sc.paths[e.Turn]; ok && json.Unmarshal(e.Payload, &tc) == nil {
			sc.closes[path] = stepCloseOf(&tc, e.Turn)
			noteAttemptClose(sc.starts, path, tc.Outcome)
		}
	case marotte.EntryKindText, marotte.EntryKindThinking, marotte.EntryKindToolCall:
		if path, ok := sc.paths[e.Turn]; ok {
			noteAttemptLine(sc.starts, path, printedLine(e))
		}
	case marotte.EntryKindStepGraded:
		var g marotte.EntryStepGraded
		path, ok := sc.paths[e.Turn]
		if c, has := sc.closes[path]; ok && has && c.turn == e.Turn && json.Unmarshal(e.Payload, &g) == nil {
			sc.closes[path] = c.withGrade(&g)
		}
	}
}

// stepAttempt is one path's current attempt: when its first turn opened, whether its newest turn is open, whether
// that turn's close was an interruption (a bridge death or a restart), which the path's next turn continues, and the
// newest line the attempt's sealed turns printed (an open turn's own is runTurn.tail).
type stepAttempt struct {
	tail        string
	at          int64
	open        bool
	interrupted bool
}

// noteAttemptOpen records a path's turn_open: a new attempt unless the path's newest turn is still open or was
// interrupted, because KAS resumes that node rather than starting it again.
func noteAttemptOpen(starts map[string]stepAttempt, path string, ts int64) {
	a, ok := starts[path]
	if !ok || (!a.open && !a.interrupted) {
		a.at, a.tail = ts, ""
	}
	a.open, a.interrupted = true, false
	starts[path] = a
}

// noteAttemptLine records a line a path's attempt printed; "" and a path with no recorded open leave it alone.
func noteAttemptLine(starts map[string]stepAttempt, path, line string) {
	a, ok := starts[path]
	if !ok || line == "" {
		return
	}
	a.tail = line
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

// stepStarts answers each node path's current attempt, scanning the log on the first read of the process; nil with
// no log.
func (r *runLog) stepStarts(ctx context.Context, workflowID string) (map[string]marotte.RunStepStart, error) {
	l, err := r.log(ctx, workflowID)
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
		out[path] = stepStartLocked(rec, path, a)
	}
	return out, nil
}

func stepStartLocked(rec *runRecord, path string, a stepAttempt) marotte.RunStepStart {
	row := marotte.RunStepStart{
		StartedAt: time.UnixMilli(a.at).UTC().Format(time.RFC3339Nano),
		Ended:     !a.open && !a.interrupted,
	}
	if !row.Ended {
		row.Turn, row.Tail = attemptTail(rec, path, a)
	}
	return row
}

// attemptTail is an in-flight attempt's newest turn and the newest line it printed: the open turn's own line, else
// the line its sealed turns left.
func attemptTail(rec *runRecord, path string, a stepAttempt) (turn, tail string) {
	if turnIDs := rec.turns[path]; len(turnIDs) > 0 {
		turn = turnIDs[len(turnIDs)-1]
	}
	tail = a.tail
	if t := rec.open[path]; t != nil {
		if line := t.tail.Load(); line != nil {
			tail = *line
		}
	}
	return turn, tail
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
