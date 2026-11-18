package chat

import (
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// botSet is every chat id whose record has a bot block, kept by the store's own writes and filled by its scans.
type botSet struct {
	ids map[marotte.ChatID]struct{}
	// dropped are the ids removed while a scan runs: that scan may have read the record before the remove.
	dropped  map[marotte.ChatID]struct{}
	scans    int
	mu       sync.Mutex
	complete bool
}

func newBotSet() botSet {
	return botSet{ids: make(map[marotte.ChatID]struct{}), dropped: make(map[marotte.ChatID]struct{})}
}

// note records what a header write left on disk for chatID.
func (b *botSet) note(chatID marotte.ChatID, isBot bool) {
	if !isBot {
		b.drop(chatID)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ids[chatID] = struct{}{}
	delete(b.dropped, chatID)
}

func (b *botSet) drop(chatID marotte.ChatID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.ids, chatID)
	if b.scans > 0 {
		b.dropped[chatID] = struct{}{}
	}
}

func (b *botSet) scanStarted() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scans++
}

// scanDone folds one scan's read rows in; a complete scan makes the set complete from then on.
func (b *botSet) scanDone(scan *headerScan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range scan.rows {
		id := scan.rows[i].id
		if _, gone := b.dropped[id]; scan.rows[i].header.Bot != nil && !gone {
			b.ids[id] = struct{}{}
		}
	}
	b.scans--
	if b.scans == 0 {
		clear(b.dropped)
	}
	if scan.complete() {
		b.complete = true
	}
}

// BotIDs answers every Bot's chat id, sorted, and whether the set is whole: false until a scan of every chat header
// (List's or SessionOwners') has completed, when it holds only the Bots this process wrote.
func (s *Store) BotIDs() ([]marotte.ChatID, bool) {
	s.bots.mu.Lock()
	defer s.bots.mu.Unlock()
	return slices.Sorted(maps.Keys(s.bots.ids)), s.bots.complete
}
