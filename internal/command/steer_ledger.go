package command

// The steer ledger: which mid-turn steers are the USER's own words.
//
// Nothing on the wire separates them from a workflow's report (see
// marotte.SteerOrigin), so CmdSteer records the id KAS returned for every steer
// this server sent. In-memory, TTL'd and bounded like createLedger next door,
// because a steer's whole lifetime is one turn.

// ACCEPTED COST: a restart mid-turn loses the set, so a steer sent before it and
// read after labels as the agent's — unreachable in practice, since the restart
// kills the turn that would have read it.

import (
	"slices"
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
	// parked holds, per chat and in arrival order, the steers accepted for an
	// admitted prompt whose bridge is not live yet; the prompt's drain delivers
	// them after StartTurn. Capped at maxParkedPerChat with a `full` refusal.
	parked map[marotte.ChatID][]ParkedSteer
	now    func() time.Time
	ttl    time.Duration
	maxN   int
	mu     sync.Mutex
}

// maxParkedPerChat bounds one chat's parked set: the 65th steer is refused with
// reason `full` rather than growing a slice nothing drains until a bridge spawns.
const maxParkedPerChat = 64

// NewSteerLedger returns an empty ledger.
func NewSteerLedger() *SteerLedger {
	return &SteerLedger{
		sent:   make(map[steerKey]sentSteer),
		parked: make(map[marotte.ChatID][]ParkedSteer),
		now:    time.Now,
		ttl:    steerTTL,
		maxN:   maxSteerOps,
	}
}

// ParkSteer holds a steer for the chat's admitted prompt until its bridge is
// live; false when the chat's parked set is full.
func (l *SteerLedger) ParkSteer(chatID marotte.ChatID, steerID, text string, resends []string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.parked[chatID]) >= maxParkedPerChat {
		return false
	}
	l.parked[chatID] = append(l.parked[chatID], ParkedSteer{ID: steerID, Text: text, Resends: resends})
	return true
}

// HasParkedSteers reports whether the chat holds an undelivered parked steer.
func (l *SteerLedger) HasParkedSteers(chatID marotte.ChatID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.parked[chatID]) > 0
}

// NextParkedSteer is the oldest parked steer still undelivered.
func (l *SteerLedger) NextParkedSteer(chatID marotte.ChatID) (ParkedSteer, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := l.parked[chatID]
	if len(rows) == 0 {
		return ParkedSteer{}, false
	}
	return rows[0], true
}

// ForgetParkedSteer drops one delivered or refused row from the parked set.
func (l *SteerLedger) ForgetParkedSteer(chatID marotte.ChatID, steerID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := slices.DeleteFunc(l.parked[chatID], func(p ParkedSteer) bool { return p.ID == steerID })
	if len(rows) == 0 {
		delete(l.parked, chatID)
		return
	}
	l.parked[chatID] = rows
}

// TakeParkedSteers drains the chat's whole parked set, in arrival order.
func (l *SteerLedger) TakeParkedSteers(chatID marotte.ChatID) []ParkedSteer {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := l.parked[chatID]
	delete(l.parked, chatID)
	return rows
}

// RecordUserSteer records that this server sent steerID for chatID, re-sending
// the dropped steers resends names (nil for an ordinary steer).
//
// Called with the id KAS RETURNED, never the one marotte derived: the reply's
// `messageId` is what every later frame is keyed by, so recording anything else
// would file the steer under a name no frame carries.
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

// ForgetUserSteer drops ONE recorded steer, for a send that turned out not to
// have reached KAS's buffer. CmdSteer records the derived id BEFORE its RPC (the
// only ordering that beats the notification), so a refused send has to take its
// entry back. Not a correctness fix: nothing else can carry a `steer-` id, so a
// stale entry mislabels nothing and the TTL reclaims it. What it protects is the
// bounded map, whose sweep evicts the entry closest to expiry once it is full, so
// slots spent on sends that never happened cost real records.
func (l *SteerLedger) ForgetUserSteer(chatID marotte.ChatID, steerID string) {
	if l == nil || steerID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sent, steerKey{chat: chatID, id: steerID})
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
	delete(l.parked, chatID)
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
