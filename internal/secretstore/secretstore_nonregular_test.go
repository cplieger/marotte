package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
)

// newWithin runs New on its own goroutine and fails if it has not returned inside budget:
// the defect this pins HANGS rather than fails. A parked goroutine is abandoned (blocked
// in open(2)).
func newWithin(t *testing.T, budget time.Duration, configDir string) (*Store, error) {
	t.Helper()
	type res struct {
		s   *Store
		err error
	}
	out := make(chan res, 1)
	go func() {
		s, err := New(configDir)
		out <- res{s, err}
	}()
	select {
	case r := <-out:
		return r.s, r.err
	case <-time.After(budget):
		t.Fatalf("New still blocked after %v: the credential read followed a non-regular file into open(2)", budget)
		return nil, nil
	}
}

// TestNew_RefusesAFifoInsteadOfBlockingTheBoot pins that the mode verdict precedes any read.
func TestNew_RefusesAFifoInsteadOfBlockingTheBoot(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, fileName), fileMode); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	_, err := newWithin(t, 3*time.Second, dir)
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("New over a FIFO = %v, want atomicfile.ErrNotRegular", err)
	}
}

// TestNew_RefusesASymlinkBeforeReadingItsTarget pins that a symlink's target is never parsed.
func TestNew_RefusesASymlinkBeforeReadingItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"secrets":{"k":"dg=="}}`), fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, fileName)); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	s, err := New(dir)
	// Fatal: New returns a nil Store with the error.
	if err == nil {
		t.Fatalf("New followed a symlink at %s and loaded %d entries", fileName, len(s.secrets))
	}
}

// TestNew_BoundsTheFileBeforeAllocating pins that the size bound is checked before reading.
func TestNew_BoundsTheFileBeforeAllocating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file one byte over the bound: no disk, but a read-then-check would allocate it.
	if err := f.Truncate(maxFileBytes + 1); err != nil {
		_ = f.Close()
		t.Skipf("truncate unsupported here: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Errorf("New over an oversize store = %v, want atomicfile.ErrFileTooLarge", err)
	}
}
