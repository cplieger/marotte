package command

// The op ledger maps an op_id to what its first attempt made (a create's chat, a bot_save's rule
// ids), so a retry past the Idempotency-Key cache's 5-minute lazy TTL resolves to that instead of
// making it again. In memory rather than a chat-record field: chat.Store has no by-field index.

import (
	"cmp"
	"sync"
	"time"
)

// Longer than the Idempotency-Key cache's 5 minutes on purpose: covering that cache's fall-through
// is this ledger's whole job.
const opTTL = 10 * time.Minute

// maxOps bounds the map: a create or a save is a deliberate human gesture, so
// the live population inside one TTL is single digits.
const maxOps = 512

// Safe for concurrent use. The zero value is usable, with opTTL and maxOps.
type opLedger[V any] struct {
	ops  map[string]ledgerOp[V]
	now  func() time.Time
	ttl  time.Duration
	maxN int
	mu   sync.Mutex
}

type ledgerOp[V any] struct {
	expires time.Time
	value   V
}

func newOpLedger[V any]() *opLedger[V] {
	return &opLedger[V]{
		ops:  make(map[string]ledgerOp[V]),
		now:  time.Now,
		ttl:  opTTL,
		maxN: maxOps,
	}
}

// resolve returns the value op names, minting one with mint when this is
// the first time op has been seen. replay reports which happened.
//
// An empty op always mints and records nothing (no key to record it under).
// Reserve and mint happen under one lock, so two attempts of one op cannot
// mint two values even if they overlap.
func (l *opLedger[V]) resolve(op string, mint func() V) (v V, replay bool) {
	if op == "" {
		return mint(), false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.live(op); ok {
		return e, true
	}
	v = mint()
	l.putLocked(op, v)
	return v, false
}

// put records v under op, replacing what op held; an empty op records nothing. For an op whose
// caller already serializes its attempts and records only once the work succeeded.
func (l *opLedger[V]) put(op string, v V) {
	if op == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.putLocked(op, v)
}

// peek reports the value op already resolved to, without minting one or extending its TTL; false for
// an unseen, expired or empty op. For a create whose capacity reservation must run before the mint.
func (l *opLedger[V]) peek(op string) (V, bool) {
	if op == "" {
		var zero V
		return zero, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live(op)
}

// Caller holds l.mu.
func (l *opLedger[V]) live(op string) (V, bool) {
	e, ok := l.ops[op]
	if !ok || !l.clock().Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Caller holds l.mu.
func (l *opLedger[V]) putLocked(op string, v V) {
	if l.ops == nil {
		l.ops = make(map[string]ledgerOp[V])
	}
	now := l.clock()
	l.sweep(now)
	l.ops[op] = ledgerOp[V]{value: v, expires: now.Add(cmp.Or(l.ttl, opTTL))}
}

func (l *opLedger[V]) clock() time.Time {
	if l.now == nil {
		return time.Now()
	}
	return l.now()
}

// sweep drops expired entries and, if the map is still full, the entry
// closest to expiry. Caller holds l.mu.
func (l *opLedger[V]) sweep(now time.Time) {
	for k, e := range l.ops {
		if !now.Before(e.expires) {
			delete(l.ops, k)
		}
	}
	for len(l.ops) >= cmp.Or(l.maxN, maxOps) {
		oldest, found := "", time.Time{}
		for k, e := range l.ops {
			if found.IsZero() || e.expires.Before(found) {
				oldest, found = k, e.expires
			}
		}
		if oldest == "" {
			return
		}
		delete(l.ops, oldest)
	}
}
