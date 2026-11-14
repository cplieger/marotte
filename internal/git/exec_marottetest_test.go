//go:build marotte_test

package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/systembin"
)

// tests/perf/build.sh runs the TestMarotteTestGit_ tests by name: CI's go test builds no tags.

func TestMarotteTestGit_CountsEachFetchThroughTheShim(t *testing.T) {
	realGit, ok := systembin.Resolve("git")
	if !ok {
		t.Fatal("no git in the trusted system directories")
	}
	shim, err := filepath.Abs(filepath.Join("..", "..", "tests", "perf", "instance", "git"))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	runGit(t, base, "clone", "-q", serveFixtureRepo(t, base), "work")
	work, log := filepath.Join(base, "work"), filepath.Join(base, "git.jsonl")

	// A fresh process, so the shim is reached only through init(), as the perf build's marotte
	// reaches it.
	child := exec.Command(os.Args[0], "-test.run=^TestMarotteTestGit_ChildFetchesThroughInit$")
	child.Env = append(os.Environ(),
		"MAROTTE_TEST_GIT="+shim,
		"PERF_GIT_REAL="+realGit,
		"PERF_GIT_LOG="+log,
		"MAROTTE_TEST_GIT_CHILD_DIR="+work,
	)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the child test binary: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the shim logged nothing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want the one fetch: %q", len(lines), lines)
	}
	var entry struct{ Op, Dir string }
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("log line %q: %v", lines[0], err)
	}
	if entry.Op != "fetch" || entry.Dir != work {
		t.Fatalf("logged %+v, want op fetch in %s", entry, work)
	}
}

// TestMarotteTestGit_ChildFetchesThroughInit is the child process of
// TestMarotteTestGit_CountsEachFetchThroughTheShim and skips when run on its own.
func TestMarotteTestGit_ChildFetchesThroughInit(t *testing.T) {
	work := os.Getenv("MAROTTE_TEST_GIT_CHILD_DIR")
	if work == "" {
		t.Skip("runs only as the child of TestMarotteTestGit_CountsEachFetchThroughTheShim")
	}
	if out, err := gitCmd(t.Context(), work, "fetch", "--quiet"); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if out, err := gitCmd(t.Context(), work, "status", "--porcelain"); err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
}

func TestMarotteTestGit_IgnoresARelativePath(t *testing.T) {
	t.Setenv("MAROTTE_TEST_GIT", "tests/perf/instance/git")
	prev := resolveGitBinary
	t.Cleanup(func() { resolveGitBinary = prev })
	resolveGitBinary = func() (string, bool) { return "/usr/bin/git", true }
	useTestGitFromEnv()

	if got, _ := resolveGitBinary(); got != "/usr/bin/git" {
		t.Fatalf("resolveGitBinary() = %q, want the systembin git kept", got)
	}
}
