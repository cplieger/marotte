package agent

// The run store's registry and appender: one entry log per run under
// `<root>/<workflowId>/`, one turnlog accumulator per open step, and the two maps
// that route a frame to its turn. KAS owns a run's state, so the log has no header
// and is never rewound, merged or paged by number.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/turnlog"
)

// runLogDir is the directory beside chats/ holding one entry log per run.
const runLogDir = "runs"

// errRunLogRemoved reports an append or an open against a run whose directory a
// delete already removed: the run's tombstone, in-process only.
var errRunLogRemoved = errors.New("run log: this run was deleted")

// errRunIDInvalid refuses a workflow id that is not a bare directory name; the id
// reaches the filesystem, and the REST route hands it over unparsed.
var errRunIDInvalid = errors.New("run log: invalid workflow id")

// errRunNoClosedTurn reports a late entry for a step path this process never saw
// close, so there is no turn_close to file it after.
var errRunNoClosedTurn = errors.New("run log: the step path has no closed turn")

// runTurn is one open step turn: its accumulator plus the position of its newest
// sealed entry, which the digest answers for a `run_turn` stamp.
type runTurn struct {
	turn *turnlog.Turn
	// rawStop is the last turn_end's stop reason; the close carries it beside the
	// node_complete mapping.
	rawStop marotte.StopReason
	path    string
	// session is the ACP session this step runs on, the value its turn_open records.
	// Held here as well because the question it answers — which sessions are a run's
	// own open steps — is about the OPEN set, which is this registry's alone: a
	// process that did not open these turns holds none of them. So a read off the log
	// would spend a whole turn's bytes re-deriving what only memory knows the set of.
	session string
	// seq is the newest seq the sink assigned, read off the dispatch goroutine's
	// writes by the digest on a request goroutine.
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

// runRecord is one run the registry knows: its log, its open turns by node path,
// the newest closed turn per path (the between-turns target) and the bridge whose
// death closes the open ones.
type runRecord struct {
	log    *chat.EntryLog
	open   map[string]*runTurn
	closed map[string]string
	host   marotte.ChatID
	// removed is the tombstone: a frame arriving after Delete opens nothing.
	removed bool
}

// runLog is the registry the content handlers, the lifecycle handlers, the death
// closer and Runs.Delete all reach. One mutex, held across the store call: the log's
// own lock is taken inside it, and nothing here is called while the log's is held.
type runLog struct {
	runs map[string]*runRecord
	root string
	mu   sync.Mutex
}

// newRunLog builds the registry over `<configDir>/runs`; a run root takes none of
// the header hooks.
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

// recordLocked answers the run's record, opening its log on first use. A run whose
// directory Delete removed answers errRunLogRemoved until the process restarts.
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

// Log answers a run's entry log for a READ, or nil when the run has no directory:
// a read must not create one, because an empty directory is a run the reaper then
// has to account for.
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

// stepTurns is one step path's transcript out of the run's log: every turn whose
// turn_open names the path, in file order, the open tails of those still open, and
// one run_turn stamp per open turn. The stamp's seq is the newest the SERVED
// entries hold for that turn, never the record's counter: the sink advances that
// counter outside the registry's lock, so a seal landing between the two reads
// would stamp a version one ahead of the page and a reconnect would never fetch
// it. found is false when the log holds no turn for the path.
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

// turnRange is ONE step turn's tail out of the run's log: the entries from seq `from`
// INCLUSIVE (from == 0 the whole turn), that turn's open tails while it is still open,
// and its run_turn stamp. The twin of the chat store's TurnPage over the same one
// EntryLog.TurnPage, for the repair a run pane runs on a `run_turn` mismatch or a seq
// hole. found is false when the run has no log or the log holds no such turn; the turn id
// reaches no filesystem name, so an id nothing minted is that answer rather than a
// refusal. A non-nil err is a log this server could not READ, which the route answers as
// a 500 rather than as a missing turn.
func (r *runLog) turnRange(ctx context.Context, workflowID, turn string, from uint64) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []*marotte.SubjectStamp, found bool, err error) {
	l, err := r.Log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, nil, nil, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, newestSeq, err := l.TurnPage(turn, from)
	if err != nil {
		// Only an id the log holds no turn for is the caller's answer; a log that cannot be
		// READ is this server's fault, and folding the two made the route's 500 arm and its
		// Warn unreachable for a disk or decode failure.
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

// stepEntries keeps the entries of every turn whose turn_open names nodePath, in
// file order, with each kept turn's newest seq.
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

// Turn answers the open turn for a step, or nil.
func (r *runLog) Turn(workflowID, nodePath string) *turnlog.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.runs[workflowID]; ok {
		if t := rec.open[nodePath]; t != nil {
			return t.turn
		}
	}
	return nil
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

// OpenSessions answers the ACP sessions of the run's OPEN step turns: the set the
// idle window asks the terminal registry about, so a bound reads whether one of THIS
// run's steps is waiting on a command. A step whose turn_open carried no session
// contributes nothing, and a run this process holds no open turn for answers an empty
// set — both of which the caller reads as no evidence of work.
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

// Open opens the step's turn when none is open and answers the turn either way;
// opened is the turn_open it appended, nil when the path was already open. The
// caller's chat id becomes the run's host when unset: the chat the frame arrived
// on, or the parentless dispatcher's `run:<id>` key when it handed an empty one.
func (r *runLog) Open(ctx context.Context, workflowID, nodePath, sessionID string, chatID marotte.ChatID) (turn *turnlog.Turn, opened *marotte.Entry, err error) {
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
		return t.turn, nil, nil
	}
	e, err := rec.log.OpenTurn(ctx, &chat.TurnSpec{
		Source:    marotte.TurnOpenNameWorkflowStep,
		Run:       workflowID,
		NodePath:  nodePath,
		SessionID: sessionID,
	})
	if err != nil {
		return nil, nil, err
	}
	t := &runTurn{path: nodePath, session: sessionID}
	t.turn = turnlog.Open(e.ID, runSink{log: rec.log, turn: t})
	rec.open[nodePath] = t
	return t.turn, e, nil
}

// hostFor is the host a frame's chat id names: itself, or the parentless bridge's
// key when the lifecycle handler handed an empty id.
func hostFor(workflowID string, chatID marotte.ChatID) marotte.ChatID {
	if chatID == "" {
		return runChatID(workflowID)
	}
	return chatID
}

// AppendAfterClosed files a content entry after the newest closed turn of its
// path, the run log's reading of the between-turns rule. It answers false when the
// path has no closed turn this process knows, so the caller opens one instead.
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

// Meter folds a step's turn_completion into its open turn. False when the path has
// no open turn: a metering frame after node_complete has no aggregate to join.
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

// StopReason records a step's turn_end stop reason on its open turn; the last one
// wins, and the close carries it.
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

// CloseNode closes the step's turn with KAS's node_complete status. Sealed is
// everything the close wrote, in order; closed is false when the path had no open
// turn, which the caller logs at Debug.
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
	// KAS grades a step by whether it RAN, so a refused step still reports
	// `completed`. The step's last turn_end is the only frame that states the model
	// declined, and that grading outranks the status: a refusal is never a success.
	if stop := marotte.ConcludeStopReason(t.rawStop); stop.Outcome == marotte.TurnOutcomeRefused {
		c = stop
	}
	c.Reason = reason
	// A mapped status keeps the step's last turn_end as the raw stop; an unmapped
	// one is itself the raw stop, which is what makes it recoverable.
	if c.Known && t.rawStop != "" {
		c.RawStop = t.rawStop
	}
	sealed, err = r.closeLocked(ctx, rec, t, c)
	return sealed, true, err
}

// CloseRun closes every open turn of a run with one outcome and, when terminal,
// drops the host: the run_complete closer, and Runs.Delete's first step.
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

// CloseHost closes every open turn of every run the dead bridge hosted, with the
// caller's outcome: the death closer's run arm. It answers the sealed entries per
// run so the caller can broadcast them under the run's scope.
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

// Delete closes every open turn `cancelled`, drops the run's maps and tombstones
// its log; RemoveDir is the caller's last step, after KAS's own delete and
// clearEnd.
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

// RemoveDir removes the run's directory. Only after Delete: the tombstone is what
// keeps a late frame from re-creating it.
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

// closeLocked closes one turn and moves it from open to closed. The path leaves
// `open` whatever the close answered: a refused write latched the log, and the
// next frame for the path must open a fresh turn rather than fold into one the
// store refuses.
func (r *runLog) closeLocked(ctx context.Context, rec *runRecord, t *runTurn, c marotte.TurnConclusion) ([]turnlog.Sealed, error) {
	sealed, err := t.turn.Close(ctx, c)
	delete(rec.open, t.path)
	rec.closed[t.path] = t.turn.ID()
	return sealed, err
}

// runOutcome maps KAS's node or run status onto the turn's outcome: `completed`,
// `failed` and `cancelled` to their own, anything else to `unknown` carrying the
// status as the raw stop.
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

// sortedKeys is a map's keys in order, so a close over several open paths writes
// them deterministically.
func sortedKeys(m map[string]*runTurn) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
