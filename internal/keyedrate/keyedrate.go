// Package keyedrate is a token bucket per key, for a limit that holds per caller or per resource rather than per
// process. A key unused for ten minutes is dropped, so the map holds only keys in recent use.
package keyedrate

import (
	"sync"
	"time"
)

// idleAfter is how long a key may go unused before its bucket is dropped. A dropped key starts full again, which is
// what its bucket would hold by then for any Every × Burst up to idleAfter.
const idleAfter = 10 * time.Minute

// Config sets a Limiter's buckets: each holds up to Burst tokens and gains one every Every. Now is the clock;
// nil means time.Now.
type Config struct {
	Now   func() time.Time
	Every time.Duration
	Burst int
}

// Limiter answers whether a key may act now. Its zero value is not usable; build one with New. It is safe for
// concurrent use.
type Limiter struct {
	now     func() time.Time
	buckets map[string]*bucket
	every   time.Duration
	burst   int
	mu      sync.Mutex
}

// bucket is one key's tokens; refillAt is when the next token arrives and is zero while the bucket is full.
type bucket struct {
	refillAt time.Time
	lastUsed time.Time
	tokens   int
}

// New answers a Limiter over cfg.
func New(cfg Config) *Limiter {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, buckets: map[string]*bucket{}, every: cfg.Every, burst: cfg.Burst}
}

// Allow takes a token from key's bucket and answers true, or answers false and how long until its next token.
func (l *Limiter) Allow(key string) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.evictIdle(now)
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst}
		l.buckets[key] = b
	}
	b.lastUsed = now
	l.refill(b, now)
	if b.tokens == 0 {
		return false, b.refillAt.Sub(now)
	}
	if b.tokens == l.burst {
		b.refillAt = now.Add(l.every)
	}
	b.tokens--
	return true, 0
}

func (l *Limiter) refill(b *bucket, now time.Time) {
	if b.tokens == l.burst || now.Before(b.refillAt) {
		return
	}
	gained := 1 + int(now.Sub(b.refillAt)/l.every)
	if b.tokens+gained >= l.burst {
		b.tokens, b.refillAt = l.burst, time.Time{}
		return
	}
	b.tokens += gained
	b.refillAt = b.refillAt.Add(time.Duration(gained) * l.every)
}

func (l *Limiter) evictIdle(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.lastUsed) >= idleAfter {
			delete(l.buckets, key)
		}
	}
}
