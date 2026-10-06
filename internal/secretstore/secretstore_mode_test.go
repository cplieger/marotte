package secretstore

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// seedStoreFile writes a one-entry store at path so the load path has something
// to parse before it reaches the mode enforcement.
func seedStoreFile(t *testing.T, path string) {
	t.Helper()
	seeded := `{"secrets":{"k":"` + base64.StdEncoding.EncodeToString([]byte("v")) + `"}}`
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}
}

// TestLoad_ModeIsVerifiedNotRequested pins that the load tightens by enforcement, driving
// the drift with an explicit chmod and failing as INVALID if the filesystem will not store it.
func TestLoad_ModeIsVerifiedNotRequested(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	seedStoreFile(t, path)
	if err := os.Chmod(path, 0o646); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode().Perm() != 0o646 {
		t.Skipf("filesystem stored %v for a 0o646 chmod; there is no drift here to enforce away",
			wfi.Mode().Perm())
	}

	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("store mode = %v, want 0600: load did not enforce the mode it reported", got)
	}
	if v, ok := s.Get("k"); !ok || v != "v" {
		t.Errorf("Get() = (%q, %v), want (\"v\", true): enforcing the mode must not lose the contents", v, ok)
	}
}

// TestLoad_RefusesAStoreWhoseModeItCannotVerify pins that an unverifiable 0600 FAILS the
// open (the mode is these tokens' whole protection). A symlink at the name reaches the
// branch here; a mode-widening filesystem reaches it via atomicfile.ErrModeNotStored.
func TestLoad_RefusesAStoreWhoseModeItCannotVerify(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "planted.json")
	seedStoreFile(t, target)
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatal(err)
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if tfi.Mode().Perm() != 0o666 {
		t.Skipf("filesystem stored %v for a 0o666 chmod; a redirected chmod would be "+
			"indistinguishable from a refused one here", tfi.Mode().Perm())
	}
	if err := os.Symlink(target, filepath.Join(dir, fileName)); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir)
	if err == nil {
		t.Fatal("New() error = nil over a store whose mode could not be verified, want a refusal")
	}
	if s != nil {
		t.Errorf("New() returned a usable store alongside the error: %#v", s)
	}

	// The chmod must not have been redirected at the planted target...
	fi, statErr := os.Lstat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := fi.Mode().Perm(); got != 0o666 {
		t.Errorf("planted symlink target mode = %v, want 0666: the chmod followed the pathname", got)
	}
	// ...and the refusal must not DELETE the file (that would lose credentials every boot).
	if _, statErr := os.Lstat(filepath.Join(dir, fileName)); statErr != nil {
		t.Errorf("the refused store was removed from the config dir: %v", statErr)
	}
}

// TestLoad_FirstRunIsStillNotAnError pins that an absent store is the first-run state.
func TestLoad_FirstRunIsStillNotAnError(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() over an empty config dir = %v, want nil", err)
	}
	if s == nil {
		t.Fatal("New() returned no store on the first-run path")
	}
	if _, ok := s.Get("anything"); ok {
		t.Error("a first-run store reported a hit")
	}
	if _, statErr := os.Lstat(filepath.Join(dir, fileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("opening a store created the file eagerly: %v", statErr)
	}
}
