package keyedrate

import (
	"testing"
	"time"
)

// clock is a settable fake time source.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// held counts the keys that hold a bucket.
func held(l *Limiter) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newLimiter(c *clock) *Limiter {
	return New(Config{Burst: 30, Every: 2 * time.Second, Now: c.Now})
}

// drain takes n tokens from key and fails the test on any refusal.
func drain(t *testing.T, l *Limiter, key string, n int) {
	t.Helper()
	for i := range n {
		if ok, _ := l.Allow(key); !ok {
			t.Fatalf("Allow(%q) #%d refused, want %d allowed", key, i+1, n)
		}
	}
}

// A key starts with a full bucket of Burst, refills one token per Every up to Burst, and answers how long until its
// next token when it refuses; another key's bucket is its own.
func TestLimiter_AKeysBucketRefillsAtItsRate(t *testing.T) {
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	l := newLimiter(c)

	drain(t, l, "a", 30)
	if ok, retry := l.Allow("a"); ok || retry != 2*time.Second {
		t.Errorf("the 31st Allow = %v retry %v, want refused, retry 2s", ok, retry)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("another key refused, want its own full bucket")
	}

	c.advance(2*time.Second - time.Millisecond)
	if ok, retry := l.Allow("a"); ok || retry != time.Millisecond {
		t.Errorf("Allow 1 ms before the refill = %v retry %v, want refused, retry 1ms", ok, retry)
	}
	c.advance(time.Millisecond)
	drain(t, l, "a", 1)
	if ok, _ := l.Allow("a"); ok {
		t.Error("Allow right after the one refilled token, want refused")
	}

	c.advance(10 * time.Minute)
	drain(t, l, "a", 30)
	if ok, _ := l.Allow("a"); ok {
		t.Error("Allow past Burst after a long idle, want the bucket capped at 30")
	}
}

// A key unused for idleAfter is evicted, so the map holds only keys in recent use; a key in use stays.
func TestLimiter_EvictsAKeyIdleForTenMinutes(t *testing.T) {
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	l := newLimiter(c)

	drain(t, l, "a", 30)
	c.advance(5 * time.Minute)
	drain(t, l, "b", 1)
	c.advance(5*time.Minute - time.Second)
	drain(t, l, "c", 1)
	if got := held(l); got != 3 {
		t.Errorf("keys held = %d a second before a's idle limit, want 3", got)
	}

	c.advance(time.Second)
	drain(t, l, "c", 1)
	if got := held(l); got != 2 {
		t.Errorf("keys held = %d once a sat idle for %v, want 2 (b and c)", got, idleAfter)
	}
	if idleAfter != 10*time.Minute {
		t.Errorf("idleAfter = %v, want 10m", idleAfter)
	}

	c.advance(idleAfter)
	drain(t, l, "d", 1)
	if got := held(l); got != 1 {
		t.Errorf("keys held = %d after b and c sat idle, want 1", got)
	}
}
