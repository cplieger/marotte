package testsupport

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// InMemoryChatStore is an in-memory chat store with real Mutate/Get semantics and an optional
// Bus for lifecycle events.
type InMemoryChatStore struct {
	// Bus is the fan-out lifecycle events go to; see RecordingChatStore.Bus
	// for why the type is spelled out rather than named.
	Bus interface {
		Broadcast(ctx context.Context, evt marotte.ServerEvent)
	}
	chats    map[marotte.ChatID]*marotte.Chat
	versions chatVersions
	mu       sync.Mutex
}

// NewInMemoryChatStore returns a ready-to-use InMemoryChatStore.
func NewInMemoryChatStore() *InMemoryChatStore {
	return &InMemoryChatStore{chats: make(map[marotte.ChatID]*marotte.Chat)}
}

// Exists reports whether the fake holds id.
func (s *InMemoryChatStore) Exists(id marotte.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.chats[id]
	return ok
}

// Get returns a copy of the stored chat for id, or (nil, false) if not found.
func (s *InMemoryChatStore) Get(_ context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return nil, false
	}
	return cloneChat(c), true
}

// List returns headers for all stored chats.
func (s *InMemoryChatStore) List(_ context.Context) []marotte.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]marotte.ChatHeader, 0, len(s.chats))
	for _, c := range s.chats {
		hs = append(hs, c.Header())
	}
	return hs
}

// SessionClaimed reports whether any held chat's session chain names sessionID;
// the in-memory store reads every record, so the answer is always complete.
func (s *InMemoryChatStore) SessionClaimed(_ context.Context, sessionID string) (claimed, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.chats {
		if slices.Contains(c.SessionChain(), sessionID) {
			return true, true
		}
	}
	return false, true
}

// Mutate applies the mutate function to the chat with the given id, creating it if needed.
func (s *InMemoryChatStore) Mutate(_ context.Context, id marotte.ChatID, mutate func(*marotte.Chat, bool) bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, exists := s.chats[id]
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
	s.chats[id] = &c
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

// SetDraft stores the chat's draft without touching UpdatedAt and without broadcasting (see
// (*chat.Store).SetDraft). A no-op on an absent chat; reports the state that landed, or nil.
func (s *InMemoryChatStore) SetDraft(_ context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok || c.Draft == text {
		return nil, nil
	}
	c.Draft = text
	state := c.Composer()
	state.Version = s.versions.bump(id)
	return &state, nil
}

// SetAttachments stores the paths staged beside the draft under the same
// contract: no UpdatedAt, no broadcast, no-op on an absent chat.
func (s *InMemoryChatStore) SetAttachments(_ context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return nil, nil
	}
	next := slices.Clone(paths)
	if len(next) == 0 {
		next = nil
	}
	if slices.Equal(c.Attachments, next) {
		return nil, nil
	}
	c.Attachments = next
	state := c.Composer()
	state.Version = s.versions.bump(id)
	return &state, nil
}

// Delete removes the chat with the given id and broadcasts a chat_deleted event.
func (s *InMemoryChatStore) Delete(_ context.Context, id marotte.ChatID) error {
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Broadcast(context.Background(), marotte.ServerEvent{Type: marotte.EventChatDeleted, ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

// Compile-time assertion.
var _ chatStoreUnion = (*InMemoryChatStore)(nil)
