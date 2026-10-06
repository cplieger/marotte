package composition

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAcquireInstanceLock pins the single-instance guard: a fresh configDir locks, a second
// acquisition on it is refused while the first fd is open.
func TestAcquireInstanceLock(t *testing.T) {
	t.Run("fresh dir acquires", func(t *testing.T) {
		dir := t.TempDir()
		if err := acquireInstanceLock(dir); err != nil {
			t.Errorf("acquireInstanceLock(fresh dir) = %v, want nil", err)
		}
	})

	t.Run("second acquisition on same dir is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := acquireInstanceLock(dir); err != nil {
			t.Fatalf("first acquireInstanceLock = %v, want nil", err)
		}
		err := acquireInstanceLock(dir)
		if err == nil {
			t.Fatal("second acquireInstanceLock = nil, want error (lock already held)")
		}
		if !strings.Contains(err.Error(), "flock") {
			t.Errorf("second acquisition error = %q, want it to mention flock", err)
		}
	})

	t.Run("missing config dir fails to open", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nonexistent-parent", "config")
		err := acquireInstanceLock(dir)
		if err == nil {
			t.Fatal("acquireInstanceLock(missing dir) = nil, want error")
		}
		if !strings.Contains(err.Error(), "open lock file") {
			t.Errorf("missing-dir error = %q, want it to mention open lock file", err)
		}
	})
}
