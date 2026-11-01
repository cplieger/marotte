package translate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

// maxRunNames bounds the names held, oldest learned dropped first.
const maxRunNames = 256

// runNames is each run's display name (marotte.RunDisplayName) learned from KAS's frames and reads,
// and the chats whose wake named a run before its name was known, which a run_named entry tells.
// Process memory: a restart between a wake and its run's name leaves that wake on the placeholder.
type runNames struct {
	names    map[string]string
	awaiting map[string][]marotte.ChatID
	order    []string
	mu       sync.Mutex
}

func newRunNames() *runNames {
	return &runNames{names: map[string]string{}, awaiting: map[string][]marotte.ChatID{}}
}

func (r *runNames) of(run string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.names[run]
}

// await records that chatID's wake names run with no name; true when the chat did not await it yet,
// so the caller asks for one read.
func (r *runNames) await(run string, chatID marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	chats := r.awaiting[run]
	if slices.Contains(chats, chatID) {
		return false
	}
	r.awaiting[run] = append(chats, chatID)
	return true
}

// learn stores run's name and hands back the chats awaiting it, once.
func (r *runNames) learn(run, name string) []marotte.ChatID {
	if run == "" || name == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.names[run]; !ok {
		r.order = append(r.order, run)
		if len(r.order) > maxRunNames {
			delete(r.names, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.names[run] = name
	chats := r.awaiting[run]
	delete(r.awaiting, run)
	return chats
}

// nameWake fills w's runs from the names known now, and for each run still unnamed records chatID as
// awaiting it and asks for the run's state once, whose reply names it (RecordInspect).
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
		if t.runNames.await(run, chatID) && t.runOrigin != nil {
			t.runOrigin.ReadRun(run)
		}
	}
}

// learnRunName records run's name and files a run_named entry to each chat whose wake awaits it.
func (t *Translator) learnRunName(ctx context.Context, run, name string) {
	for _, chatID := range t.runNames.learn(run, name) {
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
