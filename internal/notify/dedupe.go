package notify

import (
	"sync"
	"time"
)

// A key accepted at t is a duplicate until t+dedupeWindow, a fixed window from its first acceptance that a repeat
// does not extend. maxDedupeEntries bounds the table: past it, expired entries go first, then the oldest accepted.
const (
	dedupeWindow     = 10 * time.Minute
	maxDedupeEntries = 4096
)

type dedupeKey struct{ session, key string }

type reserveOutcome int

const (
	reserveFresh reserveOutcome = iota
	reserveDuplicate
	// reserveInFlight is a key another request is delivering, or a table whose every entry is one.
	reserveInFlight
)

type dedupeEntry struct {
	accepted time.Time
	inFlight bool
}

// dedupeTable records the (session, dedupe_key) pairs delivered within the window. A key is reserved before its
// delivery and committed only after it succeeds, so a failed delivery's retry is delivered rather than deduplicated.
type dedupeTable struct {
	entries map[dedupeKey]dedupeEntry
	mu      sync.Mutex
}

func newDedupeTable() *dedupeTable { return &dedupeTable{entries: map[dedupeKey]dedupeEntry{}} }

func (d *dedupeTable) reserve(k dedupeKey, now time.Time) reserveOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.entries[k]; ok {
		switch {
		case e.inFlight:
			return reserveInFlight
		case now.Sub(e.accepted) < dedupeWindow:
			return reserveDuplicate
		}
		delete(d.entries, k)
	}
	if len(d.entries) >= maxDedupeEntries && !d.makeRoomLocked(now) {
		return reserveInFlight
	}
	d.entries[k] = dedupeEntry{inFlight: true}
	return reserveFresh
}

// makeRoomLocked sweeps expired entries, else evicts the oldest accepted one; false when every entry is in flight.
func (d *dedupeTable) makeRoomLocked(now time.Time) bool {
	var (
		oldest    dedupeKey
		oldestAt  time.Time
		haveOlder bool
	)
	for k, e := range d.entries {
		if e.inFlight {
			continue
		}
		if now.Sub(e.accepted) >= dedupeWindow {
			delete(d.entries, k)
			continue
		}
		if !haveOlder || e.accepted.Before(oldestAt) {
			oldest, oldestAt, haveOlder = k, e.accepted, true
		}
	}
	if len(d.entries) < maxDedupeEntries {
		return true
	}
	if !haveOlder {
		return false
	}
	delete(d.entries, oldest)
	return true
}

func (d *dedupeTable) commit(k dedupeKey, accepted time.Time) {
	d.mu.Lock()
	d.entries[k] = dedupeEntry{accepted: accepted}
	d.mu.Unlock()
}

func (d *dedupeTable) release(k dedupeKey) {
	d.mu.Lock()
	delete(d.entries, k)
	d.mu.Unlock()
}

func (d *dedupeTable) len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}
