package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

var (
	errSearchTurnCancelled = errors.New("search: the turn was cancelled")
	errSearchRunEnded      = errors.New("search: the run ended")
	errSearchSessionEnded  = errors.New("search: the session it arrived on ended")
)

// searchCancels holds the running searches, so a turn cancel, a run's end or the end of the session
// a search arrived on ends it: KAS's wait for a search answer has no abort of its own. The zero
// value is ready.
type searchCancels struct {
	running map[uint64]trackedSearch
	mu      sync.Mutex
	next    uint64
}

// trackedSearch is one running search: runID is the run it belongs to, as inbound.searchRun decides.
type trackedSearch struct {
	origin acpResponder
	cancel context.CancelCauseFunc
	chatID marotte.ChatID
	runID  string
}

// track derives a search context from ctx that the cancels end; release must run when the search returns.
func (s *searchCancels) track(ctx context.Context, chatID marotte.ChatID, runID string, origin acpResponder) (searchCtx context.Context, release func()) {
	searchCtx, cancel := context.WithCancelCause(ctx)
	s.mu.Lock()
	if s.running == nil {
		s.running = map[uint64]trackedSearch{}
	}
	s.next++
	id := s.next
	s.running[id] = trackedSearch{origin: origin, cancel: cancel, chatID: chatID, runID: runID}
	s.mu.Unlock()
	return searchCtx, func() {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
		cancel(nil)
	}
}

// cancelChat ends chatID's searches in scope, as inScope scopes its asks; each still answers with what it found.
func (s *searchCancels) cancelChat(chatID marotte.ChatID, scope command.CancelScope) {
	s.cancelWhere(errSearchTurnCancelled, func(t trackedSearch) bool {
		return t.chatID == chatID && (scope == command.ScopeChatAndRuns || t.runID == "")
	})
}

// cancelRun ends every search a step of workflowID sent.
func (s *searchCancels) cancelRun(workflowID string) {
	if workflowID == "" {
		return
	}
	s.cancelWhere(errSearchRunEnded, func(t trackedSearch) bool { return t.runID == workflowID })
}

// cancelOrigin ends every search that arrived on origin, which can no longer carry an answer.
func (s *searchCancels) cancelOrigin(origin acpResponder) {
	s.cancelWhere(errSearchSessionEnded, func(t trackedSearch) bool { return t.origin == origin })
}

func (s *searchCancels) cancelWhere(cause error, match func(trackedSearch) bool) {
	var ended []context.CancelCauseFunc
	s.mu.Lock()
	for id, t := range s.running {
		if match(t) {
			ended = append(ended, t.cancel)
			delete(s.running, id)
		}
	}
	s.mu.Unlock()
	for _, cancel := range ended {
		cancel(cause)
	}
}
