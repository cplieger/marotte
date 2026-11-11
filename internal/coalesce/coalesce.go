// Package coalesce folds repeated change marks on one key into one emit per window.
package coalesce

import (
	"sync"
	"time"
)

// Notifier coalesces marks on a key into one emit per window: the first mark
// opens the window, marks inside it are absorbed, emit runs once when it
// closes, and a mark after that opens the next. Safe for concurrent use.
type Notifier[K comparable] struct {
	emit    func(key K)
	pending map[K]struct{}
	mu      sync.Mutex
	window  time.Duration
}

// New returns a Notifier that calls emit, outside its lock, once per window
// per key.
func New[K comparable](window time.Duration, emit func(key K)) *Notifier[K] {
	return &Notifier[K]{emit: emit, pending: make(map[K]struct{}), window: window}
}

// Mark records that key changed.
func (n *Notifier[K]) Mark(key K) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, open := n.pending[key]; open {
		return
	}
	n.pending[key] = struct{}{}
	time.AfterFunc(n.window, func() { n.fire(key) })
}

func (n *Notifier[K]) fire(key K) {
	n.mu.Lock()
	delete(n.pending, key)
	n.mu.Unlock()
	n.emit(key)
}
