package spec

import (
	"sync"
	"time"
)

// Notifier coalesces marks on a spec directory into one emit per window: the
// first mark opens the window, marks inside it are absorbed, emit runs once
// when it closes, and a mark after that opens the next. It is workspace-global
// because several sessions write one spec, and safe for concurrent use.
type Notifier struct {
	emit    func(dir string)
	pending map[string]struct{}
	mu      sync.Mutex
	window  time.Duration
}

// NewNotifier returns a Notifier that calls emit, outside its lock, once per
// window per directory.
func NewNotifier(window time.Duration, emit func(dir string)) *Notifier {
	return &Notifier{emit: emit, pending: make(map[string]struct{}), window: window}
}

// Mark records that dir changed.
func (n *Notifier) Mark(dir string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, open := n.pending[dir]; open {
		return
	}
	n.pending[dir] = struct{}{}
	time.AfterFunc(n.window, func() { n.fire(dir) })
}

func (n *Notifier) fire(dir string) {
	n.mu.Lock()
	delete(n.pending, dir)
	n.mu.Unlock()
	n.emit(dir)
}
