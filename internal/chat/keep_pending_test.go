package chat

import "github.com/cplieger/marotte/internal/marotte"

// hasPending reports whether chatID has a keep write pended for retry.
func (s *Store) hasPending(chatID marotte.ChatID) bool {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	return len(s.keep.pending[chatID]) > 0
}
