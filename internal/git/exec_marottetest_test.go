//go:build marotte_test

package git

import (
	"encoding/json"
	"os"
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
	t.Setenv("MAROTTE_TEST_GIT", shim)
	t.Setenv("PERF_GIT_REAL", realGit)
	t.Setenv("PERF_GIT_LOG", log)
	prev := resolveGitBinary
	t.Cleanup(func() { resolveGitBinary = prev })
	useTestGitFromEnv()

	if out, err := gitCmd(t.Context(), work, "fetch", "--quiet"); err != nil {
		t.Fatalf("fetch through the shim: %v\n%s", err, out)
	}
	if out, err := gitCmd(t.Context(), work, "status", "--porcelain"); err != nil {
		t.Fatalf("status through the shim: %v\n%s", err, out)
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
