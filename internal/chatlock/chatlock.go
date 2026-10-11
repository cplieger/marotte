// Package chatlock is one context-aware mutex per chat, for work that must not interleave with
// itself on one chat while other chats proceed.
package chatlock

import (
	"context"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// Set holds one lock per chat, reclaimed when its last holder or waiter leaves. Safe for
// concurrent use; construct with New.
type Set struct {
	m  map[marotte.ChatID]*entry
	mu sync.Mutex
}

type entry struct {
	ch   chan struct{}
	refs int
}

// New returns an empty set.
func New() *Set {
	return &Set{m: make(map[marotte.ChatID]*entry)}
}

// Lock blocks until chatID's lock is free or ctx ends. The caller must call the returned unlock
// exactly once.
func (s *Set) Lock(ctx context.Context, chatID marotte.ChatID) (unlock func(), err error) {
	s.mu.Lock()
	e := s.m[chatID]
	if e == nil {
		e = &entry{ch: make(chan struct{}, 1)}
		s.m[chatID] = e
	}
	e.refs++
	s.mu.Unlock()
	select {
	case e.ch <- struct{}{}:
		return func() {
			<-e.ch
			s.leave(chatID, e)
		}, nil
	case <-ctx.Done():
		s.leave(chatID, e)
		return nil, ctx.Err()
	}
}

func (s *Set) leave(chatID marotte.ChatID, e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(s.m, chatID)
	}
}
