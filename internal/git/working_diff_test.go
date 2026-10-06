package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diffFixtureRepo(t *testing.T, dir, change string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dir, "f.txt"), "base\n")
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-qm", "init")
	writeFixture(t, filepath.Join(dir, "f.txt"), change+"\n")
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// outsideRepo is a repository outside every test root, whose change must never
// appear in a diff WorkingDiff produces.
func outsideRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	diffFixtureRepo(t, dir, "outside change")
	return dir
}

func TestWorkingDiff_RunsInTheOpenedDirectoryAfterItsPathIsSwapped(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "repo")
	diffFixtureRepo(t, inside, "inside change")
	outside := outsideRepo(t)
	prev := repoOpened
	t.Cleanup(func() { repoOpened = prev })
	repoOpened = func() {
		if err := os.Rename(inside, filepath.Join(ws, "moved")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(outside, inside); err != nil {
			t.Error(err)
		}
	}

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "repo", 1<<20)
	if err != nil {
		t.Fatalf("WorkingDiff on the opened directory: %v", err)
	}
	if !strings.Contains(unstaged, "+inside change") || strings.Contains(unstaged, "outside") {
		t.Errorf("WorkingDiff after the path swap = %q, want the opened repository's change only", unstaged)
	}
}

func TestWorkingDiff_RefusesADirectoryWithoutAGitDir(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, "plain"), 0o750); err != nil {
		t.Fatal(err)
	}

	if _, _, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "plain", 1<<20); !errors.Is(err, ErrNotARepo) {
		t.Errorf("WorkingDiff on a plain directory: err = %v, want ErrNotARepo", err)
	}
}

func TestWorkingDiff_RefusesAGitSymlinkLeavingTheRoot(t *testing.T) {
	ws := t.TempDir()
	repo := filepath.Join(ws, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideRepo(t), ".git"), filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "repo", 1<<20)
	if !errors.Is(err, ErrNotARepo) {
		t.Errorf("WorkingDiff with .git linked outside the root: err = %v, unstaged %q, want ErrNotARepo", err, unstaged)
	}
}

func TestWorkingDiff_FollowsAGitSymlinkInsideTheRoot(t *testing.T) {
	ws := t.TempDir()
	store := filepath.Join(ws, "store")
	diffFixtureRepo(t, store, "store change")
	repo := filepath.Join(ws, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "store", ".git"), filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(repo, "f.txt"), "linked change\n")

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "repo", 1<<20)
	if err != nil {
		t.Fatalf("WorkingDiff with .git linked inside the root: %v", err)
	}
	if !strings.Contains(unstaged, "+linked change") {
		t.Errorf("WorkingDiff with .git linked inside the root = %q, want the repo directory's change", unstaged)
	}
}

func TestWorkingDiff_RefusesAGitfileLeavingTheRoot(t *testing.T) {
	ws := t.TempDir()
	repo := filepath.Join(ws, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Rel(repo, filepath.Join(outsideRepo(t), ".git"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(repo, ".git"), "gitdir: "+target+"\n")

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "repo", 1<<20)
	if !errors.Is(err, ErrNotARepo) {
		t.Errorf("WorkingDiff with a gitfile naming a directory outside the root: err = %v, unstaged %q, want ErrNotARepo", err, unstaged)
	}
}

func TestWorkingDiff_IgnoresACoreWorktreeOutsideTheRoot(t *testing.T) {
	ws := t.TempDir()
	repo := filepath.Join(ws, "repo")
	diffFixtureRepo(t, repo, "inside change")
	runGit(t, repo, "config", "core.worktree", outsideRepo(t))

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "repo", 1<<20)
	if err != nil {
		t.Fatalf("WorkingDiff with core.worktree outside the root: %v", err)
	}
	if !strings.Contains(unstaged, "+inside change") || strings.Contains(unstaged, "outside") {
		t.Errorf("WorkingDiff with core.worktree outside the root = %q, want the opened directory's change only", unstaged)
	}
}

func linkedWorktree(t *testing.T, ws string) string {
	t.Helper()
	diffFixtureRepo(t, filepath.Join(ws, "main"), "main change")
	runGit(t, filepath.Join(ws, "main"), "worktree", "add", "-q", filepath.Join(ws, "wt"))
	writeFixture(t, filepath.Join(ws, "wt", "f.txt"), "worktree change\n")
	return filepath.Join(ws, "main", ".git", "worktrees", "wt")
}

func TestWorkingDiff_DiffsALinkedWorktreeInsideTheRoot(t *testing.T) {
	ws := t.TempDir()
	linkedWorktree(t, ws)

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "wt", 1<<20)
	if err != nil {
		t.Fatalf("WorkingDiff on a linked worktree inside the root: %v", err)
	}
	if !strings.Contains(unstaged, "+worktree change") || strings.Contains(unstaged, "main change") {
		t.Errorf("WorkingDiff on a linked worktree = %q, want the worktree's own change", unstaged)
	}
}

func TestWorkingDiff_UsesTheCommonDirItCheckedAfterCommondirIsRewritten(t *testing.T) {
	ws := t.TempDir()
	gitDir := linkedWorktree(t, ws)
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	runGit(t, empty, "init", "-q")
	outside := filepath.Join(empty, ".git")
	prev := repoOpened
	t.Cleanup(func() { repoOpened = prev })
	repoOpened = func() { writeFixture(t, filepath.Join(gitDir, "commondir"), outside+"\n") }

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "wt", 1<<20)
	if err != nil {
		t.Fatalf("WorkingDiff after its commondir is rewritten: %v", err)
	}
	if !strings.Contains(unstaged, "+worktree change") {
		t.Errorf("WorkingDiff after its commondir is rewritten = %q, want the worktree's own change", unstaged)
	}
}

func TestWorkingDiff_RefusesACommondirLeavingTheRoot(t *testing.T) {
	ws := t.TempDir()
	gitDir := linkedWorktree(t, ws)
	writeFixture(t, filepath.Join(gitDir, "commondir"), filepath.Join(outsideRepo(t), ".git")+"\n")

	_, unstaged, err := WorkingDiff(t.Context(), openTestRoot(t, ws), "wt", 1<<20)
	if !errors.Is(err, ErrNotARepo) {
		t.Errorf("WorkingDiff with a commondir outside the root: err = %v, unstaged %q, want ErrNotARepo", err, unstaged)
	}
}
