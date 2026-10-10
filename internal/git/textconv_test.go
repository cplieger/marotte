package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// It appears in a diff as a context line, which is why the fixture works as an assertion in both
// directions rather than only proving the flag was passed.
const textconvMarker = "TEXTCONV_DRIVER_RAN"

// textconvDriver is a diff.<driver>.textconv value that prints the marker and
// then the file. git appends the path as the command's last argument, so under
// `sh -c` the path lands in $0.
const textconvDriver = "sh -c 'echo " + textconvMarker + "; cat \"$0\"'"

func armTextconv(t *testing.T, dir string) {
	t.Helper()
	initFixtureRepo(t, dir)
	writeRepoFile(t, dir, ".gitattributes", "changed.txt diff=leak\n")
	runGit(t, dir, "config", "diff.leak.textconv", textconvDriver)
	writeCommit(t, dir, "changed.txt", "committed line\n", "add changed.txt")
	writeRepoFile(t, dir, "changed.txt", "working line\n")
}

func writeRepoFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// The fixture's own proof, and it must hold for the tests below to mean
// anything: without --no-textconv, git really does execute the repo's command
// and its output really does reach the caller. A green pair of "the marker is
// absent" assertions over an unarmed fixture would assert nothing at all.
func TestTextconv_FixtureIsArmed(t *testing.T) {
	dir := t.TempDir()
	armTextconv(t, dir)

	out, _ := gitCmd(t.Context(), dir, "diff", "HEAD", "--", "changed.txt")
	if !strings.Contains(out, textconvMarker) {
		t.Fatalf("diff without --no-textconv did not run the driver; the fixture is not armed:\n%s", out)
	}
	// `git show <ref>:<path>` does not apply textconv by default (git 2.55.0); only `--textconv`
	// runs the driver. Both halves are asserted so the fixture is proven armed.
	enabled, err := gitCmd(t.Context(), dir, "show", "--textconv", "HEAD:changed.txt")
	if err != nil {
		t.Fatalf("git show --textconv: %v\n%s", err, enabled)
	}
	if !strings.Contains(enabled, textconvMarker) {
		t.Fatalf("show --textconv did not run the driver; the fixture is not armed:\n%s", enabled)
	}
	bare, err := gitCmd(t.Context(), dir, "show", "HEAD:changed.txt")
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, bare)
	}
	if strings.Contains(bare, textconvMarker) {
		t.Errorf("git's blob-dump default changed: bare `show <ref>:<path>` now runs textconv:\n%s", bare)
	}
}

// TestGitShowCmd_ReturnsTheRawBlobNotTextconvOutput: gitShowCmd backs the editor's diff-vs-HEAD
// pane, a read an untrusted repo reaches.
func TestGitShowCmd_ReturnsTheRawBlobNotTextconvOutput(t *testing.T) {
	dir := t.TempDir()
	armTextconv(t, dir)

	blob, stderr, err := gitShowCmd(t.Context(), dir, "HEAD", "changed.txt", defaultShowMax)
	if err != nil {
		t.Fatalf("gitShowCmd: %v\n%s", err, stderr)
	}
	out := string(blob)
	if strings.Contains(out, textconvMarker) {
		t.Errorf("show ran the repo's textconv driver:\n%s", out)
	}
	if !strings.Contains(out, "committed line") {
		t.Errorf("show lost the blob content:\n%s", out)
	}
}

// core.fsmonitor is the other config-driven execution path, and it fires on the
// two subcommands the git panel uses most. It is cleared centrally in
// gitExec, so this asserts the end-to-end consequence: a repo that sets it
// cannot get it run.
func TestGitExec_DoesNotRunARepoFsmonitorHook(t *testing.T) {
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	marker := filepath.Join(dir, "fsmonitor-ran")
	runGit(t, dir, "config", "core.fsmonitor", "sh -c 'touch \""+marker+"\"'")

	for _, args := range [][]string{
		{"status", "--porcelain"},
		{"diff", "--no-textconv", "HEAD"},
	} {
		if out, err := gitCmd(t.Context(), dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("core.fsmonitor from the repo's own config was executed by git %v", args)
		}
	}
}
