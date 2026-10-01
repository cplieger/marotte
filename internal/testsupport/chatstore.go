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
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// chatStoreUnion is the union of what the real consumers declare, spelled out
// here rather than named, so a consumer that grows a method fails to compile
// against these fakes instead of silently outgrowing them. It is
// ChatStoreContract's 5 plus the two composer writers, which internal/command
// needs and the contract suite does not exercise.
//
// RegisterRoutes is NOT here, and each fake dropped its no-op: only
// internal/server mounts the chat routes and it does so on the concrete store,
// so both fakes carried a method no test could reach.
type chatStoreUnion interface {
	ChatStoreContract
	SetDraft(ctx context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error)
	SetAttachments(ctx context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error)
	// Exists is the digest resolver's lock-free `chat` gone predicate (agent/deps.go).
	Exists(id marotte.ChatID) bool
}

// RecordingChatStore is an in-memory chat store that keeps chats in a
// map and fires broadcasts via an attached Broadcaster. Suitable for
// integration-style tests that need a ChatStore that actually stores things.
type RecordingChatStore struct {
	// Bus is the fan-out lifecycle events go to. The type is spelled out
	// rather than named because there is no shared Broadcaster interface any
	// more: internal/chat and internal/forges each declare their own 1-method
	// copy, and this is the union of the two.
	Bus interface {
		Broadcast(ctx context.Context, evt marotte.ServerEvent)
	}
	Chats    map[marotte.ChatID]*marotte.Chat
	versions chatVersions
	// Gets counts Get calls, for a test whose subject is how OFTEN the store is
	// read rather than what it answers. The real store's Get is a per-chat mutex,
	// a whole-file read and a json.Unmarshal of the entire history, so a caller
	// that makes one per streamed frame is a defect no assertion on the ANSWER can
	// see.
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

// List returns headers for all stored chats.
func (s *RecordingChatStore) List(_ context.Context) []marotte.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]marotte.ChatHeader, 0, len(s.Chats))
	for _, c := range s.Chats {
		hs = append(hs, c.Header())
	}
	return hs
}

// ChatVersion is the `chat` version the last write to id minted, "" before any
// write. A test that needs to know which version a frame SHOULD carry reads it
// here rather than re-deriving the fake's counter.
func (s *RecordingChatStore) ChatVersion(id marotte.ChatID) string {
	return s.versions.current(id)
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

// SetDraft stores the chat's draft without touching UpdatedAt and without
// broadcasting, which is the contract (*chat.Store).SetDraft holds and the three
// interfaces naming it — agent/deps.go, command/deps.go and chatStoreUnion above —
// depend on. A fake that went through Mutate would stamp activity and make a
// test unable to observe the one property the real method exists to hold.
// Absent chat: no-op, like the real store's load-then-write.
//
// It reports the state that landed, nil-for-nothing, because the draft_changed
// broadcast keys on exactly that: a fake that always reported a write would make
// a test unable to see the no-op cases the real method has.
func (s *RecordingChatStore) SetDraft(_ context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Chats[id]
	if !ok || c.Draft == text {
		return nil, nil
	}
	c.Draft = text
	state := c.Composer()
	state.Version = s.versions.bump(id)
	return &state, nil
}

// SetAttachments stores the chat's staged attachment paths under the same
// contract SetDraft holds: no UpdatedAt, no broadcast, no-op on an absent chat.
func (s *RecordingChatStore) SetAttachments(_ context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Chats[id]
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
func (s *RecordingChatStore) Delete(_ context.Context, id marotte.ChatID) error {
	s.mu.Lock()
	delete(s.Chats, id)
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Broadcast(context.Background(), marotte.ServerEvent{Type: marotte.EventChatDeleted, ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

// Compile-time assertion.
var _ chatStoreUnion = (*RecordingChatStore)(nil)
