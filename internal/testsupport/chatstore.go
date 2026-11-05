// Package testsupport provides the chat-store fakes that more than one sibling
// package's TEST binary needs, plus the contract suites those packages run
// against both a fake and the real implementation.
//
// Membership rule: a double belongs here only while at least two packages
// consume it. A double with a single consumer moves into that package's own
// _test.go, sized to that package's contract rather than the widest one going.
package testsupport

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// RecordingChatStore is an in-memory chat store that keeps chats in a
// map and fires broadcasts via an attached Broadcaster. Suitable for
// integration-style tests that need a ChatStore that actually stores things.
type RecordingChatStore struct {
	// Bus is the fan-out lifecycle events go to: the union of internal/chat's and
	// internal/forges' 1-method broadcasters.
	Bus interface {
		Broadcast(ctx context.Context, evt marotte.ServerEvent)
	}
	Chats    map[marotte.ChatID]*marotte.Chat
	versions chatVersions
	// Gets counts Get calls, for a test whose subject is how OFTEN the store is read.
	Gets atomic.Int64
	mu   sync.Mutex
}

// NewRecordingChatStore returns a ready-to-use RecordingChatStore.
func NewRecordingChatStore() *RecordingChatStore {
	return &RecordingChatStore{Chats: make(map[marotte.ChatID]*marotte.Chat)}
}

// Exists reports whether the fake holds id.
func (s *RecordingChatStore) Exists(id marotte.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Chats[id]
	return ok
}

// Get returns a copy of the stored chat for id, or (nil, false) if not found.
func (s *RecordingChatStore) Get(_ context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	s.Gets.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Chats[id]
	if !ok {
		return nil, false
	}
	return cloneChat(c), true
}

// Mutate applies the mutate function to the chat with the given id, creating it if needed.
func (s *RecordingChatStore) Mutate(_ context.Context, id marotte.ChatID, mutate func(*marotte.Chat, bool) bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, exists := s.Chats[id]
	var c marotte.Chat
	if exists {
		c = *orig
	} else {
		c = marotte.Chat{ID: string(id), CreatedAt: time.Now().UnixMilli()}
	}
	if !mutate(&c, exists) {
		return "", nil
	}
	c.UpdatedAt = time.Now().UnixMilli()
	s.Chats[id] = &c
	version := s.versions.bump(id)
	if s.Bus != nil {
		evt := marotte.EventChatUpdated
		if !exists {
			evt = marotte.EventChatCreated
		}
		s.Bus.Broadcast(context.Background(), marotte.ServerEvent{Type: evt, ChatID: id, Payload: c.Header()})
	}
	return version, nil
}
