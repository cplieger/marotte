package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// errSearchTurnCancelled is the cause a turn cancel gives a chat's running searches.
var errSearchTurnCancelled = errors.New("search: the turn was cancelled")

// searchCancels holds each chat's running searches, so a turn cancel ends them: KAS's wait for a
// search answer has no abort of its own. The zero value is ready.
type searchCancels struct {
	byChat map[marotte.ChatID]map[uint64]context.CancelCauseFunc
	mu     sync.Mutex
	next   uint64
}

// track derives a search context from ctx that cancelChat ends; release must run when the search returns.
func (s *searchCancels) track(ctx context.Context, chatID marotte.ChatID) (searchCtx context.Context, release func()) {
	searchCtx, cancel := context.WithCancelCause(ctx)
	s.mu.Lock()
	if s.byChat == nil {
		s.byChat = map[marotte.ChatID]map[uint64]context.CancelCauseFunc{}
	}
	if s.byChat[chatID] == nil {
		s.byChat[chatID] = map[uint64]context.CancelCauseFunc{}
	}
	s.next++
	id := s.next
	s.byChat[chatID][id] = cancel
	s.mu.Unlock()
	return searchCtx, func() {
		s.mu.Lock()
		delete(s.byChat[chatID], id)
		if len(s.byChat[chatID]) == 0 {
			delete(s.byChat, chatID)
		}
		s.mu.Unlock()
		cancel(nil)
	}
}

// cancelChat ends every search chatID has running; each still answers with what it found.
func (s *searchCancels) cancelChat(chatID marotte.ChatID) {
	s.mu.Lock()
	running := s.byChat[chatID]
	delete(s.byChat, chatID)
	s.mu.Unlock()
	for _, cancel := range running {
		cancel(errSearchTurnCancelled)
	}
}
