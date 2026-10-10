package chat

import (
	"context"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// dequeues is the store's record of the queued rows this process opened a turn
// for: the ids per chat, at most marotte.MaxQueuedPrompts, oldest evicted, and the
// ones whose header removal has not landed yet.
type dequeues struct {
	opened  map[marotte.ChatID][]string
	pending map[marotte.ChatID][]string
	mu      sync.Mutex
}

func (d *dequeues) open(chatID marotte.ChatID, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opened == nil {
		d.opened = make(map[marotte.ChatID][]string)
		d.pending = make(map[marotte.ChatID][]string)
	}
	if !slices.Contains(d.opened[chatID], id) {
		d.opened[chatID] = append(d.opened[chatID], id)
		if n := len(d.opened[chatID]); n > marotte.MaxQueuedPrompts {
			d.opened[chatID] = d.opened[chatID][n-marotte.MaxQueuedPrompts:]
		}
	}
	if !slices.Contains(d.pending[chatID], id) {
		d.pending[chatID] = append(d.pending[chatID], id)
	}
}

func (d *dequeues) isOpened(chatID marotte.ChatID, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.opened[chatID], id)
}

func (d *dequeues) strip(chatID marotte.ChatID, c *marotte.Chat) bool {
	d.mu.Lock()
	pending := d.pending[chatID]
	d.mu.Unlock()
	if len(pending) == 0 {
		return false
	}
	n := len(c.QueuedPrompts)
	c.QueuedPrompts = slices.DeleteFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool {
		return slices.Contains(pending, q.ID)
	})
	return len(c.QueuedPrompts) != n
}

// settle drops the chat's pending removals once a header write carrying them landed.
func (d *dequeues) settle(chatID marotte.ChatID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pending, chatID)
}

func (d *dequeues) forget(chatID marotte.ChatID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.opened, chatID)
	delete(d.pending, chatID)
}

// Dequeue takes a queued row off the header once its turn has opened. The id goes
// into the chat's opened set first, so Opened answers true even when the write
// fails; a failed write is applied by the chat's next mutation.
func (s *Store) Dequeue(ctx context.Context, chatID marotte.ChatID, id string) error {
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	s.dequeued.open(chatID, id)
	_, err := s.mutateLocked(ctx, chatID, func(*marotte.Chat, bool) bool { return false })
	return err
}

// Opened reports whether this process opened a turn for the queued row id.
func (s *Store) Opened(chatID marotte.ChatID, id string) bool {
	return s.dequeued.isOpened(chatID, id)
}
