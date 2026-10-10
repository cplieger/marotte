package command

// The steer ledger records the id of every steer this server sends, because nothing on the wire
// separates the user's words from a workflow's report (marotte.SteerOrigin). In memory, TTL'd and
// bounded, since a steer lives one turn.

// A restart mid-turn loses the set; accepted, because the restart also kills the turn that would
// read it.

import (
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Not the turn's length: nothing tells the ledger when a turn ends, and turns can run for hours.
const steerTTL = 30 * time.Minute

// maxSteerOps bounds the map against a pathological producer; the live population is single digits.
const maxSteerOps = 512

// A STRUCT key rather than a joined string: a steer id is KAS's, so composing one would put a
// separator inside a value marotte does not own the shape of.
type steerKey struct {
	chat marotte.ChatID
	id   string
}

// SteerLedger records the steers this server sent, each until it expires. Safe for
// concurrent use. Construct with NewSteerLedger.
type SteerLedger struct {
	sent map[steerKey]time.Time
	now  func() time.Time
	ttl  time.Duration
	maxN int
	mu   sync.Mutex
}

// NewSteerLedger returns an empty ledger.
func NewSteerLedger() *SteerLedger {
	return &SteerLedger{
		sent: make(map[steerKey]time.Time),
		now:  time.Now,
		ttl:  steerTTL,
		maxN: maxSteerOps,
	}
}

// RecordUserSteer records that this server sent steerID for chatID. KAS builds its own steer id
// from the messageId, so steerID matches every later frame.
func (l *SteerLedger) RecordUserSteer(chatID marotte.ChatID, steerID string) {
	if l == nil || steerID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	l.sent[steerKey{chat: chatID, id: steerID}] = now.Add(l.ttl)
}

// SteerOrigin answers whose words the steer is: a recorded, unexpired id is the user's, everything
// else the agent's. Absence is a real answer, not a failed lookup.
func (l *SteerLedger) SteerOrigin(chatID marotte.ChatID, steerID string) marotte.SteerOrigin {
	if l == nil || steerID == "" {
		return marotte.SteerOriginAgent
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if expires, ok := l.sent[steerKey{chat: chatID, id: steerID}]; ok && l.now().Before(expires) {
		return marotte.SteerOriginUser
	}
	return marotte.SteerOriginAgent
}

// ForgetChat drops every steer recorded for one chat, at its teardown. A linear scan over the
// bounded map rather than a second index.
func (l *SteerLedger) ForgetChat(chatID marotte.ChatID) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.sent {
		if k.chat == chatID {
			delete(l.sent, k)
		}
	}
}

// sweep drops expired entries and, if still full, the entry closest to expiry, so a full map costs
// one mislabelled note. Caller holds l.mu.
func (l *SteerLedger) sweep(now time.Time) {
	for k, expires := range l.sent {
		if !now.Before(expires) {
			delete(l.sent, k)
		}
	}
	for len(l.sent) >= l.maxN {
		var oldest steerKey
		var found time.Time
		for k, expires := range l.sent {
			if found.IsZero() || expires.Before(found) {
				oldest, found = k, expires
			}
		}
		if found.IsZero() {
			return
		}
		delete(l.sent, oldest)
	}
}
