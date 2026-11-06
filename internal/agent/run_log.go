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
	// noteLeases is, per path, the lease node_complete read for its close, until a trigger records that close's
	// grade: run_complete releases the lease right after a run's last node_complete, so a run read recording the
	// grade afterwards still notes the chat it named.
	noteLeases map[string]noteLease
	host       marotte.ChatID
	// removed is the tombstone: a frame arriving after Delete opens nothing.
	removed bool
}

// One mutex, held across the store call; nothing here runs under the log's own lock.
type runLog struct {
	runs map[string]*runRecord
	// removeAll is removeDir's os.RemoveAll, a field so a test can fail one removal as root cannot be refused.
	removeAll func(path string) error
	root      string
	mu        sync.Mutex
}

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

// stepTurns is one step path's transcript: its turns in file order, open tails, and one run_turn stamp per
// open turn. The stamp's seq comes from the served entries, never the record's counter, which advances
// outside the lock. found is false with no such turn.
func (r *runLog) stepTurns(ctx context.Context, workflowID, nodePath string) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []marotte.SubjectStamp, found bool, err error) {
	l, err := r.log(ctx, workflowID)
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
			stamps = append(stamps, marotte.SubjectStamp{Kind: string(subject.KindRunTurn), Ref: workflowID + "/" + id, Version: turnVersion(id, newest[id])})
		}
	}
	return entries, open, stamps, true, nil
}

// turnRange is one step turn's tail from seq `from` inclusive (0 the whole turn), its open tails and its
// stamp, over EntryLog.TurnPage like the chat's. found is false for no log or no such turn; err is an unreadable log (500).
func (r *runLog) turnRange(ctx context.Context, workflowID, turn string, from uint64) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []marotte.SubjectStamp, found bool, err error) {
	l, err := r.log(ctx, workflowID)
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
		stamps = append(stamps, marotte.SubjectStamp{
			Kind: string(subject.KindRunTurn), Ref: workflowID + "/" + turn, Version: turnVersion(turn, newestSeq),
		})
	}
	return entries, open, stamps, true, nil
}

// lastStepMessage is a step path's last words (lastWords) in its newest turn of the run's own record, "" when the
// record holds none or cannot be read.
func (r *runLog) lastStepMessage(ctx context.Context, workflowID, nodePath string) string {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return ""
	}
	r.mu.Lock()
	all, err := l.All()
	r.mu.Unlock()
	if err != nil {
		return ""
	}
	entries, _ := stepEntries(all, nodePath)
	return lastWords(newestTurn(entries))
}

// newestTurn is the entries of the newest turn among a path's entries, which follow its turn_open:
// a later turn of the path opens after it.
func newestTurn(entries []marotte.Entry) []marotte.Entry {
	for i, e := range slices.Backward(entries) {
		if e.Kind == marotte.EntryKindTurnOpen {
			return entries[i:]
		}
	}
	return nil
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
		// KAS sends node_start twice per step; the second continues this turn.
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
	rec.open[nodePath] = t
	if rec.starts != nil {
		noteAttemptOpen(rec.starts, nodePath, e.Ts)
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
	return true
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
	t.answer.reset()
	return true
}

// paused records a step's node_paused on its open turn: whatever ends the step next, its turn did not.
func (r *runLog) paused(workflowID, nodePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.openLocked(workflowID, nodePath); t != nil {
		t.ownEnd = false
	}
}

// unanswered is whether the step's own turn ended it having said nothing. KAS completes a step at its turn's end
// only when nothing else ended it; Mark step done or a parent's update_status lands after a node_paused or inside a
// turn, and KAS clears the step's signal on that path too (applyPausedStepFinish), so only this tells them apart.
func (t *runTurn) unanswered() bool {
	return t.ownEnd && t.answer.silent()
}

func (r *runLog) openLocked(workflowID, nodePath string) *runTurn {
	if rec, ok := r.runs[workflowID]; ok {
		return rec.open[nodePath]
	}
	return nil
}

// closeNode closes the step's turn with KAS's node_complete status, answering everything it sealed; closed
// is false with no open turn.
func (r *runLog) closeNode(ctx context.Context, workflowID, nodePath, status, reason string) (sealed []turnlog.Sealed, closed bool, err error) {
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
	closing, cerr := r.closeLocked(ctx, rec, t, c)
	return append(sealed, closing...), true, errors.Join(err, cerr)
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
func (*runLog) closeLocked(ctx context.Context, rec *runRecord, t *runTurn, c marotte.TurnConclusion) ([]turnlog.Sealed, error) {
	sealed, err := t.turn.Close(ctx, c)
	delete(rec.open, t.path)
	rec.closed[t.path] = t.turn.ID()
	if rec.closes != nil {
		// The scan's constructor, so a live close and a scanned one cannot differ: the conclusion has no refusal.
		if tc, ok := turnCloseOf(sealed); ok {
			rec.closes[t.path] = stepCloseOf(&tc, t.turn.ID())
		} else {
			rec.closes[t.path] = stepClose{
				outcome: c.Outcome, reason: c.Reason, kind: c.FailureKind, rawStop: c.RawStop, unanswered: c.Unanswered,
				turn: t.turn.ID(),
			}
		}
	}
	if rec.starts != nil {
		noteAttemptClose(rec.starts, t.path, c.Outcome)
	}
	return sealed, err
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
	graded, err = r.gradeDueLocked(ctx, rec, kas)
	return stepEndsLocked(rec, kas), graded, err
}

// gradeDueLocked records the grade of every path's newest close kas makes due, in path order. Caller holds runLog.mu
// and has scanned the record's step facts.
func (r *runLog) gradeDueLocked(ctx context.Context, rec *runRecord, kas map[string]workflow.StepRecord) ([]*stepGrade, error) {
	var graded []*stepGrade
	var errs []error
	for _, path := range slices.Sorted(maps.Keys(rec.closes)) {
		st, known := kas[path]
		if !known {
			continue
		}
		g, err := r.gradeLocked(ctx, rec, path, st)
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
		end, ok := c.end()
		if !ok {
			continue
		}
		if end.Outcome == marotte.TurnOutcomeInterrupted {
			st, known := kas[path]
			// A grade is recorded only under a completed record, so a graded interruption was resumed.
			end.Resumed = rec.starts[path].open || c.hasGrade || (known && st.Completed())
		}
		ends[path] = end
	}
	return ends
}

// dueGradeLocked is whether the path's newest close c needs a grade under KAS's record. A turn open on the path is a
// newer execution KAS's record already describes, so a grade filed then would paint c with its result. A turn a
// crash left open is closed unterminated when the log opens, so only rec.open can hold one.
func dueGradeLocked(rec *runRecord, path string, c *stepClose) bool {
	return c.turn != "" && !c.hasGrade && c.mayBeBroken() && rec.open[path] == nil
}

// gradeLocked records the grade of the path's newest close under kas, KAS's record of the node, and answers it; nil
// when the close is not due one or KAS has not completed the node. It is the only grader of a close the run log
// holds. Caller holds runLog.mu and has scanned the record's step facts.
func (*runLog) gradeLocked(ctx context.Context, rec *runRecord, path string, kas workflow.StepRecord) (*stepGrade, error) {
	c, ok := rec.closes[path]
	if !ok || !dueGradeLocked(rec, path, &c) || !kas.Completed() {
		return nil, nil
	}
	p, graded := c.recordedGrade(kas)
	payload, err := json.Marshal(&p)
	if err != nil {
		return nil, err
	}
	e := &marotte.Entry{Turn: c.turn, Kind: marotte.EntryKindStepGraded, Payload: payload}
	if err := rec.log.Append(ctx, e); err != nil {
		return nil, err
	}
	rec.closes[path] = graded
	g := &stepGrade{entry: e, nodePath: path, grade: graded, sessionID: kas.SessionID, mayNote: c.mayNote()}
	if n, held := rec.noteLeases[path]; held {
		delete(rec.noteLeases, path)
		if n.turn == c.turn {
			g.note = &n.lease
		}
	}
	return g, nil
}

// gradeStep records the grade of the path's newest close when it is due one, reading KAS's record through read only
// then, and answers it; nil when it recorded none. No lock is held across read. note, node_complete's lease read
// (nil from any other trigger), is the close's note lease whichever trigger records its grade (stepGrade.note).
func (r *runLog) gradeStep(
	ctx context.Context, workflowID, nodePath string, note *runlease.Lease, read func() (workflow.StepRecord, bool),
) (*stepGrade, error) {
	l, err := r.log(ctx, workflowID)
	if err != nil || l == nil {
		return nil, err
	}
	turn, err := r.dueTurn(workflowID, nodePath, l, note)
	if err != nil || turn == "" {
		return nil, err
	}
	kas, known := read()
	if !known {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	// The close read changed, or the run was deleted, while KAS's record was read.
	if !ok || rec.removed || rec.closes[nodePath].turn != turn {
		return nil, nil
	}
	return r.gradeLocked(ctx, rec, nodePath, kas)
}

// dueTurn is the turn of the path's newest close when it is due a grade, "" otherwise, holding note, when not nil,
// as that close's note lease.
func (r *runLog) dueTurn(workflowID, nodePath string, l *chat.EntryLog, note *runlease.Lease) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok || rec.removed {
		return "", nil
	}
	if err := scanStepFactsLocked(rec, l); err != nil {
		return "", err
	}
	c, ok := rec.closes[nodePath]
	if !ok || !dueGradeLocked(rec, nodePath, &c) {
		return "", nil
	}
	if note != nil {
		if rec.noteLeases == nil {
			rec.noteLeases = make(map[string]noteLease)
		}
		rec.noteLeases[nodePath] = noteLease{lease: *note, turn: c.turn}
	}
	return c.turn, nil
}

// noteLease is the lease node_complete read for the close of turn.
type noteLease struct {
	turn  string
	lease runlease.Lease
}

// scanStepFactsLocked fills the record's step closes and attempt starts from its log once per process, both in one
// scan; every open and close keeps them current after. Caller holds runLog.mu.
func scanStepFactsLocked(rec *runRecord, l *chat.EntryLog) error {
	if rec.closes != nil {
		return nil
	}
	all, err := l.All()
	if err != nil {
		return err
	}
	rec.closes, rec.starts = scanStepFacts(all)
	return nil
}

// scanStepFacts folds a log's turn_open paths, turn_close and step_graded entries in file order into each path's
// newest close, under its grade, and its current attempt.
func scanStepFacts(all []marotte.Entry) (closes map[string]stepClose, starts map[string]stepAttempt) {
	closes = make(map[string]stepClose)
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
				closes[path] = stepCloseOf(&tc, e.Turn)
				noteAttemptClose(starts, path, tc.Outcome)
			}
		case marotte.EntryKindStepGraded:
			var g marotte.EntryStepGraded
			path, ok := paths[e.Turn]
			if c, has := closes[path]; ok && has && c.turn == e.Turn && json.Unmarshal(e.Payload, &g) == nil {
				closes[path] = c.withGrade(&g)
			}
		}
	}
	return closes, starts
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
