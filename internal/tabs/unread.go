package tabs

import (
	"errors"
	"fmt"

	"github.com/cplieger/marotte/internal/asidefile"
)

// errWriteRefused means the store started over bytes it could neither read nor move aside, or beside
// a directory it could not list, so a write could destroy the only copy of the arrangement.
var errWriteRefused = errors.New("tab arrangement writes refused: an unread arrangement could not be kept aside")

// Test seams for the rename and the listing failures, which root-run tests cannot provoke.
var (
	moveAside = asidefile.Move
	findAside = asidefile.Find
)

// Unread names the arrangement this store could not read: its aside path, tabs.json itself when the
// move aside failed, or the directory when it could not be listed. Empty when every arrangement was
// read. Set once at NewStore and never cleared by a write, so a restart after a write still sees it
// while the aside exists.
func (s *Store) Unread() string { return s.unread }

// keepAside moves the unread tabs.json aside so no write replaces it. A failed move makes the store
// refuse writes, since the next write would replace the bytes. Returns the diagnostic for loadErr.
func (s *Store) keepAside(loadErr error) error {
	aside, err := moveAside(s.path)
	if err != nil {
		s.unread, s.refuseWrites = s.path, true
		return fmt.Errorf("%w (starting empty; moving it aside failed, so tab writes are refused): %w", loadErr, err)
	}
	s.unread = aside
	return fmt.Errorf("%w (moved aside to %s, starting empty)", loadErr, aside)
}

// markEarlierAside marks the store unread when an earlier boot's aside is still in dir. A listing that
// fails hides whether one exists, so it refuses writes too.
func (s *Store) markEarlierAside(dir string) error {
	aside, err := findAside(dir, fileName)
	if err != nil {
		s.unread, s.refuseWrites = dir, true
		return fmt.Errorf("list %s for an unread arrangement (tab writes are refused): %w", dir, err)
	}
	s.unread = aside
	return nil
}
