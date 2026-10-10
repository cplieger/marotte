package chat

import "github.com/cplieger/marotte/internal/marotte"

// turnRange is TurnPage without the page's seq.
func turnRange(l *EntryLog, turn string, from uint64) ([]marotte.Entry, error) {
	entries, _, err := l.TurnPage(turn, from)
	return entries, err
}

// counters reads the header caches as the log states them.
func counters(l *EntryLog) (turnCount uint64, last marotte.TurnOutcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.countersLocked()
}
