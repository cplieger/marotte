package composition

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireInstanceLock takes a non-blocking exclusive flock on <configDir>/marotte.lock, failing if
// another process holds it. The fd is never closed: the lock lives as long as the process, and the
// kernel releases it on exit, SIGKILL included.
func acquireInstanceLock(configDir string) error {
	path := filepath.Join(configDir, "marotte.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// The close error is discarded explicitly (CodeQL go/unhandled-writable-file-close): the fd
		// holds no writes.
		_ = f.Close()
		return fmt.Errorf("flock: %w", err)
	}
	return nil
}
