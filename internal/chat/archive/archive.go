// Package archive implements chat RETENTION: the age-based purge and its
// scheduler. Nothing is archived and no chat moves — "archived" is computed from
// a chat's age against the retention window, never stored as a state.
package archive

import (
	"context"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// A directory without one is not a chat.
const headerFileName = "chat.json"

// RetentionHeader is the projection of a chat a retention decision reads. Nothing
// else: a chat's messages, blocks, tool calls and diffs decide nothing here and
// cost megabytes to decode. The store implements the read.
type RetentionHeader struct {
	// SessionChain is every KAS session the chat has run on, current one last.
	SessionChain []string
	// UpdatedAt is last activity in Unix milliseconds; zero or negative means the
	// chat records none and purgeOne falls back to the file mtime.
	UpdatedAt int64
	// Drafting reports words written and not sent: composer text, which
	// Store.SetDraft writes WITHOUT stamping UpdatedAt, or a queued follow-up.
	Drafting bool
}

// StoreAccess is what retention needs from the chat store.
type StoreAccess interface {
	// Lock returns the per-chat mutex.
	Lock(chatID marotte.ChatID) *sync.Mutex
	// Remove deletes the chat and records its tombstone, returning the `chats`
	// version the removal minted. The caller must hold Lock for chatID across
	// this call.
	Remove(chatID marotte.ChatID) (string, error)
	// Dir returns the store's base directory.
	Dir() string
	// LoadRetentionHeader reads a chat's retention projection without
	// materializing its messages.
	LoadRetentionHeader(chatID marotte.ChatID) (RetentionHeader, error)
}

// Service implements the archive lifecycle operations.
type Service struct {
	store   StoreAccess
	onPurge func(chatID marotte.ChatID, sessionChain []string)
	// broadcast carries the chat_deleted frame a purge produces to every client;
	// nil drops it.
	broadcast func(ctx context.Context, evt marotte.ServerEvent)
	// isLive reports a running bridge; such a chat is never purged, however old.
	isLive func(chatID marotte.ChatID) bool
	// hasOpenTab reports an open TAB, a different fact from isLive: a reader can
	// have a chat open with no bridge running.
	hasOpenTab func(chatID marotte.ChatID) bool
}

// New creates an archive Service backed by the given StoreAccess.
func New(store StoreAccess, opts ...Option) *Service {
	s := &Service{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures the archive Service.
type Option func(*Service)

// WithLiveChats registers the predicate reporting active use. A live chat is
// exempt from purging regardless of age; retention is about abandoned work.
// Injected because this package cannot see the runtime that owns bridges.
func WithLiveChats(fn func(chatID marotte.ChatID) bool) Option {
	return func(s *Service) { s.isLive = fn }
}

// WithOpenTabs registers the predicate reporting an OPEN TAB, which exempts a chat from purging
// regardless of age: reading a chat stamps nothing the age test can see. Injected because this
// package cannot see the tab store.
func WithOpenTabs(fn func(chatID marotte.ChatID) bool) Option {
	return func(s *Service) { s.hasOpenTab = fn }
}

// WithBroadcaster registers the SSE fan-out a purge announces its deletions
// through. A purge is a delete like any other, so a client holding the row must
// learn of it the same way: through chat_deleted, stamped with the `chats`
// version Remove minted.
func WithBroadcaster(fn func(ctx context.Context, evt marotte.ServerEvent)) Option {
	return func(s *Service) { s.broadcast = fn }
}

// WithOnPurge registers a callback fired after a chat is purged, carrying every
// KAS session it ran on, read before the file was removed. The purge reaps its OWN
// session directories through this rather than leaning on the orphan sweep, whose
// keep-list is derived by reading every chat file.
func WithOnPurge(fn func(chatID marotte.ChatID, sessionChain []string)) Option {
	return func(s *Service) { s.onPurge = fn }
}
