package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_ModeIsVerifiedNotRequested pins that load reads the mode BACK off the file; the drift is
// an explicit widening chmod, so the enforcement is what restores it.
func TestLoad_ModeIsVerifiedNotRequested(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o646); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode().Perm() != 0o646 {
		t.Skipf("filesystem stored %v for a 0o646 chmod; there is no drift here for load to enforce away, "+
			"so this test cannot tell a verified mode from a requested one", wfi.Mode().Perm())
	}

	buf := captureSlog(t)
	if _, err := New(t.Context(), dir, nil, withKASConfigPath(filepath.Join(dir, "kas-mcp.json"))); err != nil {
		t.Fatalf("New: %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mcp.json mode = %v, want 0600: load did not enforce the mode it reported", got)
	}
	if strings.Contains(buf.String(), "could not be made 0600") {
		t.Errorf("warned about an enforcement that succeeded; log=%q", buf.String())
	}
}

// TestLoad_EnforcesTheModeOnAHandleNotAPathname is the one assertion that separates the enforcement
// from a bare os.Chmod on a well-behaved filesystem: a symlink at the name is refused, not chmod'ed
// through.
func TestLoad_EnforcesTheModeOnAHandleNotAPathname(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "planted.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if err := os.Symlink(target, filepath.Join(dir, "mcp.json")); err != nil {
		t.Fatal(err)
	}

	buf := captureSlog(t)
	if _, err := New(t.Context(), dir, nil, withKASConfigPath(filepath.Join(dir, "kas-mcp.json"))); err != nil {
		t.Fatalf("New: %v (a refused mode enforcement must not fail the store at this site)", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o666 {
		t.Errorf("planted symlink target mode = %v, want 0666: the chmod followed the pathname "+
			"and tightened a file outside the config dir", got)
	}
	if !strings.Contains(buf.String(), "could not be made 0600") {
		t.Errorf("refused enforcement logged no warning; the exposure would be silent. log=%q", buf.String())
	}
}
