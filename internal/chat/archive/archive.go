// Package archive implements chat RETENTION: the age-based purge and its
// scheduler. Nothing is archived and no chat moves — "archived" is computed from
// a chat's age against the retention window, never stored as a state.
package archive

import (
	"context"
	"errors"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// ErrMissingHook refuses a scheduler built without a hook it cannot run without.
var ErrMissingHook = errors.New("chat purge: a required hook is missing")

// PurgeOutcome is what PurgeIdle did with one chat.
type PurgeOutcome int

const (
	// PurgeAbsent means the record did not exist.
	PurgeAbsent PurgeOutcome = iota
	// PurgeKept means the record stays.
	PurgeKept
	// PurgePurged means the record was removed.
	PurgePurged
)

// The reasons a chat past retention is kept, one field each on the pass's count line. The fact kinds match
// marotte.KeepKind's answers.
const (
	KeptByTab          = marotte.KeepKindTab
	KeptByRun          = marotte.KeepKindRun
	KeptByRunTab       = marotte.KeepKindRunTab
	KeptByTurn         = marotte.KeepKindTurn
	KeptByStop         = marotte.KeepKindStop
	KeptByUnknown      = "unknown"
	KeptByPending      = "pending"
	KeptByUnreconciled = "unreconciled"
	KeptByUnattributed = "unattributed"
	KeptByWords        = "words"
)

var keptByKinds = []string{
	KeptByTab, KeptByRun, KeptByRunTab, KeptByTurn, KeptByStop, KeptByUnknown,
	KeptByPending, KeptByUnreconciled, KeptByUnattributed, KeptByWords,
}

// PurgeVerdict is PurgeIdle's answer for one chat.
type PurgeVerdict struct {
	// Deadline is set only for a chat kept by age: the instant it becomes purgeable.
	Deadline time.Time
	// KeptBy is set only for a chat kept past the cutoff: the first reason found, one of the KeptBy constants.
	KeptBy string
	// SessionChain is set only for Purged, read from the record the call removed.
	SessionChain []string
	Outcome      PurgeOutcome
}

// StoreAccess is what retention needs from the chat store.
type StoreAccess interface {
	// Dir returns the store's base directory.
	Dir() string
	// PurgeIdle removes chatID when its own record, read under the record lock, says it may go; see
	// chat.Store.PurgeIdle.
	PurgeIdle(ctx context.Context, chatID marotte.ChatID, cutoff time.Time) (PurgeVerdict, error)
}

// Service runs purge passes over a store.
type Service struct {
	store   StoreAccess
	onPurge func(chatID marotte.ChatID, sessionChain []string)
}

// New creates a Service. onPurge receives every chat a pass removed with its session chain, after the record lock
// is released; it must not be nil.
func New(store StoreAccess, onPurge func(chatID marotte.ChatID, sessionChain []string)) *Service {
	return &Service{store: store, onPurge: onPurge}
}
