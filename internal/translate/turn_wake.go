package translate

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// recordWake files what started the chat's agent-initiated turn at its first agentInitiated frame,
// before the frame's own content and before the waker's settlement (settleWaker), so the waking row
// still reads unsettled. Later frames of the turn write nothing.
func (t *Translator) recordWake(ctx context.Context, chatID marotte.ChatID, turn *turnlog.Turn, reason agentInitiatedReason) {
	if turn.Woken() {
		return
	}
	w := t.wakeOf(ctx, chatID, reason.wakeReason())
	sealed, err := turn.TurnWake(ctx, &w)
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), string(marotte.EntryKindTurnWake), err)
	}
}

// wakeOf pairs a wake with its cause. A turn after a reader's cancel that cleared workflow messages
// re-delivers them, whatever KAS calls it (rewakeWithInfo sends a step-message wake). A step
// message's is otherwise the oldest message still waiting that the chat received before the turn
// opened and KAS could wake for: KAS reserves the wake for the first one reaching the idle session
// and buffers the rest behind it. A finished run's is the run_complete this chat saw while idle
// (wakeMemory.takeFinished).
func (t *Translator) wakeOf(ctx context.Context, chatID marotte.ChatID, reason marotte.WakeReason) marotte.EntryTurnWake {
	w := marotte.EntryTurnWake{Reason: reason}
	switch reason {
	case marotte.WakeReasonRunFinished:
		if f, ok := t.wakeMem.takeFinished(chatID); ok {
			w.Run, w.RunLabel, w.RunStatus, w.Initiator, w.StopReason = f.run, f.label, f.status, f.initiator, f.stopReason
		}
		return w
	case marotte.WakeReasonStepMessage, marotte.WakeReasonRewake:
	case marotte.WakeReasonUnknown:
		return w
	}
	causes, err := t.chats.WakeCauses(ctx, chatID, t.sessions.ParentACPSession(chatID))
	if err != nil {
		slog.Warn("turn wake: the chat's log is unreadable, so the wake names no message",
			"chat_id", chatID, "error", err)
		return w
	}
	if len(causes.Cleared) > 0 {
		w.Reason = marotte.WakeReasonRewake
	} else if reason == marotte.WakeReasonStepMessage {
		for i := range causes.Waiting {
			m := &causes.Waiting[i]
			if alwaysBuffered(m.Steer.Origin, m.Steer.Severity) {
				continue
			}
			w.Steers = []marotte.WakeSteer{t.wakeSteer(m)}
			n := t.wakeMem.takeNote(m.Steer.OriginRun, m.ID)
			w.Note, w.Loop, w.MaxIterations = n.note, n.loop, n.maxIterations
			return w
		}
	}
	for i := range causes.Cleared {
		w.Steers = append(w.Steers, t.wakeSteer(&causes.Cleared[i]))
	}
	return w
}

func (t *Translator) wakeSteer(m *marotte.WorkflowMessage) marotte.WakeSteer {
	s := marotte.WakeSteer{ID: m.ID, Run: m.Steer.OriginRun, Step: m.Steer.Step, Severity: m.Steer.Severity, Text: m.Steer.Text}
	if t.runOrigin != nil && s.Run != "" {
		s.RunLabel = t.runOrigin.RunName(s.Run)
	}
	return s
}

// observeRunEnd records what a wake run_complete can start: a terminal run reaching a real chat
// with no turn of its own running, which KAS wakes rather than buffers, and a repeat's cap pause,
// whose note follows the frame.
func (t *Translator) observeRunEnd(chatID marotte.ChatID, p *kasRunComplete) {
	if chatID == "" || strings.HasPrefix(string(chatID), marotte.RunSubjectPrefix) {
		return
	}
	status := marotte.RunStatus(p.Status)
	fs := &p.FinalState
	switch {
	case status.Terminal():
		if t.chatBusy(chatID) {
			return
		}
		label := fs.RunLabel
		if label == "" {
			label = fs.WorkflowName
		}
		t.wakeMem.runFinished(chatID, &runFinish{
			run: p.WorkflowID, label: label, status: status, initiator: fs.StopInitiator, stopReason: fs.StopReason,
		})
	case status == marotte.RunStatusPaused && fs.Pause.Kind == pauseKindMaxIterations:
		t.wakeMem.capPaused(p.WorkflowID, fs.Pause.NodeID, fs.Pause.MaxIterations)
	}
}

// pauseKindMaxIterations is KAS's `finalState.pause.kind` for a repeat paused at its cap.
const pauseKindMaxIterations = "maxIterations"

// chatBusy reports a chat turn open or awaiting its bracket, where KAS buffers a completion
// notice instead of waking (autoWakeParentOnComplete's hasRunningTurn).
func (t *Translator) chatBusy(chatID marotte.ChatID) bool {
	if _, ok := t.turns.OwnTurn(chatID); ok {
		return true
	}
	_, ok := t.turns.PromptTurn(chatID)
	return ok
}

// maxFinishedPerChat bounds a chat's queue against completions no wake ever claims.
const maxFinishedPerChat = 16

// wakeMemory is what the frames before a wake say about it that its turn's log cannot: process
// memory, so a restart between a cause and its wake leaves that wake naming less.
type wakeMemory struct {
	finished map[marotte.ChatID]*finishQueue
	caps     map[string]repeatCap
	// retryWait is each run's nodes inside a retry-wait, during which a note from the node is KAS's.
	retryWait map[string]map[string]struct{}
	notes     map[string]map[string]stepNote
	mu        sync.Mutex
}

// finishQueue holds the completions since the chat's newest turn_start (pending) and those before
// it (current), which only that turn can have been woken by.
type finishQueue struct {
	pending []runFinish
	current []runFinish
}

type runFinish struct {
	run        string
	label      string
	initiator  string
	stopReason string
	status     marotte.RunStatus
}

type repeatCap struct {
	loop          string
	maxIterations int
}

type stepNote struct {
	note          marotte.WakeNote
	loop          string
	maxIterations int
}

func newWakeMemory() *wakeMemory {
	return &wakeMemory{
		finished:  map[marotte.ChatID]*finishQueue{},
		caps:      map[string]repeatCap{},
		retryWait: map[string]map[string]struct{}{},
		notes:     map[string]map[string]stepNote{},
	}
}

func (w *wakeMemory) runFinished(chatID marotte.ChatID, f *runFinish) {
	w.mu.Lock()
	defer w.mu.Unlock()
	q := w.finished[chatID]
	if q == nil {
		q = &finishQueue{}
		w.finished[chatID] = q
	}
	q.pending = append(q.pending, *f)
	if len(q.pending) > maxFinishedPerChat {
		q.pending = q.pending[len(q.pending)-maxFinishedPerChat:]
	}
}

// turnStarted makes the completions seen since the last turn_start the new turn's candidates.
func (w *wakeMemory) turnStarted(chatID marotte.ChatID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	q := w.finished[chatID]
	if q == nil {
		return
	}
	q.current, q.pending = q.pending, nil
	if len(q.current) == 0 {
		delete(w.finished, chatID)
	}
}

// takeFinished consumes the turn's waker, the oldest candidate: KAS serialises wakes, so a later one
// was buffered behind it.
func (w *wakeMemory) takeFinished(chatID marotte.ChatID) (runFinish, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	q := w.finished[chatID]
	if q == nil {
		return runFinish{}, false
	}
	if len(q.current) == 0 {
		// A completion processed after its own wake's turn_start.
		q.current, q.pending = q.pending, nil
	}
	if len(q.current) == 0 {
		return runFinish{}, false
	}
	f := q.current[0]
	q.current = nil
	if len(q.pending) == 0 {
		delete(w.finished, chatID)
	}
	return f, true
}

func (w *wakeMemory) capPaused(run, loop string, maxIterations int) {
	if run == "" || loop == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.caps[run] = repeatCap{loop: loop, maxIterations: maxIterations}
}

func (w *wakeMemory) retrying(run, node string, waiting bool) {
	if run == "" || node == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	nodes := w.retryWait[run]
	if !waiting {
		delete(nodes, node)
		if len(nodes) == 0 {
			delete(w.retryWait, run)
		}
		return
	}
	if nodes == nil {
		nodes = map[string]struct{}{}
		w.retryWait[run] = nodes
	}
	nodes[node] = struct{}{}
}

// classify marks a step's row filed to the chat as KAS's note when its frames say so: a step-less
// note from the repeat whose cap pause just arrived, or a note from a node waiting out connection
// errors, which cannot be sending a message of its own.
func (w *wakeMemory) classify(run, node, rowID string, stepLess bool) {
	if run == "" || node == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var n stepNote
	if c, ok := w.caps[run]; ok && stepLess && c.loop == node {
		n = stepNote{note: marotte.WakeNoteRepeatCap, loop: c.loop, maxIterations: c.maxIterations}
		delete(w.caps, run)
	} else if _, ok := w.retryWait[run][node]; ok {
		n = stepNote{note: marotte.WakeNoteNetwork}
	} else {
		return
	}
	if w.notes[run] == nil {
		w.notes[run] = map[string]stepNote{}
	}
	w.notes[run][rowID] = n
}

func (w *wakeMemory) takeNote(run, rowID string) stepNote {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.notes[run][rowID]
	delete(w.notes[run], rowID)
	if len(w.notes[run]) == 0 {
		delete(w.notes, run)
	}
	return n
}

// forgetRun drops an ended run's marks; a completion queued for a wake stays.
func (w *wakeMemory) forgetRun(run string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.caps, run)
	delete(w.retryWait, run)
	delete(w.notes, run)
}
