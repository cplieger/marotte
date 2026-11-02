package translate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

// maxRunNames bounds the names held and the runs awaited, oldest first dropped from each.
const maxRunNames = 256

// runNames is each run's display name (marotte.RunDisplayName) learned from KAS's frames and reads,
// the chats whose wake named a run before its name was known, which a run_named entry tells, and the
// runs with a read in flight. Process memory: after a restart the run's lease chat is asked from its
// log instead (learnRunName).
type runNames struct {
	names     map[string]string
	awaiting  map[string][]marotte.ChatID
	reading   map[string]struct{}
	order     []string
	waitOrder []string
	mu        sync.Mutex
}

func newRunNames() *runNames {
	return &runNames{
		names:    map[string]string{},
		awaiting: map[string][]marotte.ChatID{},
		reading:  map[string]struct{}{},
	}
}

func (r *runNames) of(run string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.names[run]
}

// await records that chatID's wake names run with no name; true when no read of run is in flight, so
// the caller asks for one and reports its end through readDone.
func (r *runNames) await(run string, chatID marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	chats, held := r.awaiting[run]
	if !held {
		r.waitOrder = append(r.waitOrder, run)
		if len(r.waitOrder) > maxRunNames {
			delete(r.awaiting, r.waitOrder[0])
			r.waitOrder = r.waitOrder[1:]
		}
	}
	if !slices.Contains(chats, chatID) {
		r.awaiting[run] = append(chats, chatID)
	}
	if _, busy := r.reading[run]; busy {
		return false
	}
	r.reading[run] = struct{}{}
	return true
}

// readDone ends run's read, named or not, so a later wake of a run still unnamed asks again.
func (r *runNames) readDone(run string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.reading, run)
}

// learn stores run's name and hands back the chats awaiting it, once, and whether this is the
// first name this process learned for run.
func (r *runNames) learn(run, name string) (chats []marotte.ChatID, first bool) {
	if run == "" || name == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.names[run]; !ok {
		first = true
		r.order = append(r.order, run)
		if len(r.order) > maxRunNames {
			delete(r.names, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.names[run] = name
	chats = r.awaiting[run]
	if chats != nil {
		delete(r.awaiting, run)
		r.waitOrder = slices.DeleteFunc(r.waitOrder, func(id string) bool { return id == run })
	}
	return chats, first
}

// nameWake fills w's runs from the names known now, and for each run still unnamed records chatID as
// awaiting it and asks for the run's state, whose reply names it (RecordInspect).
func (t *Translator) nameWake(chatID marotte.ChatID, w *marotte.EntryTurnWake) {
	if w.Run != "" && w.RunName == "" {
		w.RunName = t.runNames.of(w.Run)
	}
	for i := range w.Steers {
		if s := &w.Steers[i]; s.RunName == "" {
			s.RunName = t.runNames.of(s.Run)
		}
	}
	for _, run := range marotte.WakeRunsUnnamed(w) {
		if t.runNames.await(run, chatID) {
			if t.runOrigin == nil {
				t.runNames.readDone(run)
				continue
			}
			t.runOrigin.ReadRun(run, func() { t.runNames.readDone(run) })
		}
	}
}

// learnRunName files a run_named entry to each chat awaiting run's name. A name this process learns
// first with no chat awaiting it, as after a restart, goes to the run's lease chat when that chat's
// log still holds a wake leaving the run unnamed.
func (t *Translator) learnRunName(ctx context.Context, run, name string) {
	chats, first := t.runNames.learn(run, name)
	if first && len(chats) == 0 && t.runOrigin != nil {
		if chatID, ok := t.runOrigin.LeaseChat(run); ok {
			awaits, err := t.chats.AwaitsRunName(ctx, chatID, run)
			if err != nil {
				slog.Warn("run name: the chat's log is unreadable, so its wakes keep the placeholder",
					"chat_id", chatID, "workflow_id", run, "error", err)
			}
			if awaits {
				chats = []marotte.ChatID{chatID}
			}
		}
	}
	for _, chatID := range chats {
		t.fileRunNamed(ctx, chatID, &marotte.EntryRunNamed{Run: run, Name: name})
	}
}

// fileRunNamed appends n to chatID's open turn, else after its newest turn's close: a turn can close
// between the two, so a refused in-turn write falls back to the between-turns one.
func (t *Translator) fileRunNamed(ctx context.Context, chatID marotte.ChatID, n *marotte.EntryRunNamed) {
	if turn, ok := t.turns.OwnTurn(chatID); ok {
		sealed, err := turn.RunNamed(ctx, n)
		t.publishSealed(ctx, chatScope(chatID), sealed)
		if err == nil {
			return
		}
		if !errors.Is(err, turnlog.ErrClosed) {
			appendFailed(chatScope(chatID), string(marotte.EntryKindRunNamed), err)
			return
		}
	}
	t.appendBetweenTurns(ctx, chatID, marotte.EntryKindRunNamed, "", *n)
}

// RecordInspect reads a raw `_kiro/workflow/inspect` reply on every run read: it seeds the step
// registry, so a restart does not leave a resumed run's frames classified as a subagent's, and learns
// the run's name. Best-effort: a decode failure must not fail the read.
func (t *Translator) RecordInspect(ctx context.Context, raw json.RawMessage) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	for _, st := range workflow.StepSessions(res.State) {
		t.RecordStepSession(st.SessionID, res.State.WorkflowID, st.NodeID, workflow.PathKey(st.Path))
	}
	t.learnRunName(ctx, cmp.Or(res.State.WorkflowID, res.WorkflowID), marotte.RunDisplayName(res.State.RunLabel, res.State.WorkflowName))
}
