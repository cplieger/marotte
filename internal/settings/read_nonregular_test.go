package settings

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
)

// fieldWithin runs Field on its own goroutine and fails if it has not returned inside budget:
// on a FIFO the defect HANGS inside the singleflight slot, wedging every settings reader.
func fieldWithin(t *testing.T, budget time.Duration, dir string) bool {
	t.Helper()
	out := make(chan bool, 1)
	go func() {
		_, ok := Field[bool](t.Context(), dir, KeyDebugLogs)
		out <- ok
	}()
	select {
	case ok := <-out:
		return ok
	case <-time.After(budget):
		t.Fatalf("Field still blocked after %v: the settings read followed a non-regular file into open(2)", budget)
		return false
	}
}

func TestField_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, Filename), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if ok := fieldWithin(t, 3*time.Second, dir); ok {
		t.Error("Field reported a value from a FIFO planted at config.json")
	}
}

func TestReadBytes_RefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, Filename), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readBytes(t.Context(), dir)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Errorf("readBytes over a FIFO = %v, want atomicfile.ErrNotRegular", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readBytes still blocked after 3s over a FIFO")
	}
}

// TestReadBytes_RefusesASymlink pins that a link at config.json is not read as the settings.
func TestReadBytes_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"debug_logs":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, Filename)); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if v, ok := Field[bool](t.Context(), dir, KeyDebugLogs); ok || v {
		t.Errorf("Field followed a symlink at config.json: (%v, %v)", v, ok)
	}
}
