package chat

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStoreSlog swaps the default logger for a buffer; tests using it must run serially. The log
// package's writer and flags are restored too, because slog.SetDefault repoints log and does not
// point it back for the stock handler.
func captureStoreSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestNewStore_VerifiesTheModeItCreated asserts that under a setgid parent MkdirAll(dir, 0o700) stores a mode
// the caller did not request. The witness fails the test as INVALID if the kernel stops inheriting
// the bit.
// Limit: the mode assertion cannot tell EnforceDir from a bare os.Chmod; the read-back is proved by
// the logged mode and the refusals pinned below.
func TestNewStore_VerifiesTheModeItCreated(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}

	witness := filepath.Join(parent, "witness")
	if err := os.Mkdir(witness, 0o700); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(witness)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode()&os.ModeSetgid == 0 {
		t.Skipf("kernel did not widen a 0o700 mkdir under a setgid parent (got %v); "+
			"this test cannot distinguish a verified create from an unverified one here", wfi.Mode())
	}

	dir := filepath.Join(parent, "chats")
	buf := captureStoreSlog(t)
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode(); got != os.ModeDir|0o700 {
		t.Fatalf("created chat dir mode = %v, want %v: the mode the kernel stored was not corrected",
			got, os.ModeDir|0o700)
	}
	if log := buf.String(); !strings.Contains(log, "mode="+fi.Mode().Perm().String()) {
		t.Errorf("startup line does not report the stored mode %v; log=%q", fi.Mode().Perm(), log)
	}
}

// TestNewStore_EnforcesTheModeOnAHandleNotAPathname asserts that a symlink at the chat-dir name is refused
// (O_NOFOLLOW|O_DIRECTORY) instead of chmod'ing its target. The store still opens: the exposure is
// reported, not enforced.
func TestNewStore_EnforcesTheModeOnAHandleNotAPathname(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o777); err != nil {
		t.Fatal(err)
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if tfi.Mode().Perm() != 0o777 {
		t.Skipf("filesystem stored %v for a 0o777 chmod; a redirected chmod would be "+
			"indistinguishable from a refused one here", tfi.Mode().Perm())
	}

	dir := filepath.Join(t.TempDir(), "chats")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}

	buf := captureStoreSlog(t)
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore over a symlinked chat dir = %v; a refused mode enforcement must not abort boot", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o777 {
		t.Errorf("symlink target mode = %v, want 0777: the chmod followed the pathname and "+
			"tightened a directory outside the config dir", got)
	}
	log := buf.String()
	if !strings.Contains(log, "could not be made 0700") {
		t.Errorf("refused enforcement logged no warning; the exposure would be silent. log=%q", log)
	}
	if !strings.Contains(log, "mode=unverified") {
		t.Errorf("startup line claimed a mode it never read; log=%q", log)
	}
}

// TestNewStore_RefusesANonDirectoryAtTheChatDirName pins the O_DIRECTORY half: a regular file at
// the chat-dir name is refused rather than chmod'ed.
func TestNewStore_RefusesANonDirectoryAtTheChatDirName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chats")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("NewStore over a regular file = nil, want an error")
	}
}
