package command

// The steer ledger: which mid-turn steers are the USER's own words.
//
// Nothing on the wire separates them from a workflow's report (see
// marotte.SteerOrigin), so the server records the id of every steer it sends.
// In-memory, TTL'd and bounded like createLedger next door, because a steer's
// whole lifetime is one turn.

// ACCEPTED COST: a restart mid-turn loses the set, so a steer sent before it and
// read after labels as the agent's — unreachable in practice, since the restart
// kills the turn that would have read it.

import (
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// steerTTL is how long a recorded steer id answers "the user's".
//
// A steer is consumed at the next node boundary, so minutes is already
// generous; it is deliberately not the turn's own length, because nothing tells
// this ledger when a turn ended and a turn can legitimately run for hours.
const steerTTL = 30 * time.Minute

// maxSteerOps bounds the map. A steer is a deliberate human gesture typed into
// a running turn, so the live population inside one TTL is single digits; the
// bound exists so a pathological producer cannot grow it without limit.
const maxSteerOps = 512

// steerKey addresses one steer. A STRUCT key rather than a joined string: a
// steer id is KAS's, so composing one would put a separator inside a value
// marotte does not own the shape of.
type steerKey struct {
	chat marotte.ChatID
	id   string
}

// sentSteer is what the ledger holds per steer this server sent: when the record
// expires, and the dropped steers whose text it re-sends.
type sentSteer struct {
	expires time.Time
	resends []string
}

// SteerLedger records the steers this server sent. Safe for concurrent use.
// Construct with NewSteerLedger.
type SteerLedger struct {
	sent map[steerKey]sentSteer
	now  func() time.Time
	ttl  time.Duration
	maxN int
	mu   sync.Mutex
}

// NewSteerLedger returns an empty ledger.
func NewSteerLedger() *SteerLedger {
	return &SteerLedger{
		sent: make(map[steerKey]sentSteer),
		now:  time.Now,
		ttl:  steerTTL,
		maxN: maxSteerOps,
	}
}

// RecordUserSteer records that this server sent steerID for chatID, re-sending
// the dropped steers resends names (nil for an ordinary steer).
//
// steerID is the id the caller chose before the RPC (marotte.SteerIDFor). It
// matches every later frame because KAS builds its own steer id from the
// messageId it is sent.
func (l *SteerLedger) RecordUserSteer(chatID marotte.ChatID, steerID string, resends []string) {
	if l == nil || steerID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	l.sent[steerKey{chat: chatID, id: steerID}] = sentSteer{expires: now.Add(l.ttl), resends: resends}
}

// SteerResends answers the dropped steers the steer re-sends, as recorded when
// this server sent it; nil for an unrecorded or expired id. Same lifetime as
// SteerOrigin, so a steer read past the TTL is labelled a plain steer.
func (l *SteerLedger) SteerResends(chatID marotte.ChatID, steerID string) []string {
	if l == nil || steerID == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.sent[steerKey{chat: chatID, id: steerID}]; ok && l.now().Before(s.expires) {
		return s.resends
	}
	return nil
}

// SteerOrigin answers whose words the steer is. A recorded, unexpired id is the
// user's; everything else is the agent's.
//
// Deliberately NOT a lookup that can fail: absence is a real answer here, and
// returning "unknown" would push a decision the client has no vocabulary for
// onto every consumer.
func (l *SteerLedger) SteerOrigin(chatID marotte.ChatID, steerID string) marotte.SteerOrigin {
	if l == nil || steerID == "" {
		return marotte.SteerOriginAgent
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.sent[steerKey{chat: chatID, id: steerID}]; ok && l.now().Before(s.expires) {
		return marotte.SteerOriginUser
	}
	return marotte.SteerOriginAgent
}

// ForgetChat drops every steer recorded for one chat, at its teardown.
//
// A linear scan over a map the bound above keeps in the low hundreds, because
// the alternative — a second index by chat — is a second thing to keep in step
// with the first for a sweep that runs once per chat close.
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

// sweep drops expired entries and, if the map is still full, the entry closest
// to expiry — createLedger's own shape, so losing one record costs one
// mislabelled note rather than every later one. Caller holds l.mu.
func (l *SteerLedger) sweep(now time.Time) {
	for k, s := range l.sent {
		if !now.Before(s.expires) {
			delete(l.sent, k)
		}
	}
	for len(l.sent) >= l.maxN {
		var oldest steerKey
		var found time.Time
		for k, s := range l.sent {
			if found.IsZero() || s.expires.Before(found) {
				oldest, found = k, s.expires
			}
		}
		if found.IsZero() {
			return
		}
		delete(l.sent, oldest)
	}
}
