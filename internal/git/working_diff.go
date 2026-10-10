package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// workingDiffTimeout bounds one WorkingDiff; a diff is local, so a wait longer
// than this is a wedged filesystem, not a slow remote.
const workingDiffTimeout = 10 * time.Second

// ErrNotARepo is WorkingDiff's answer for a directory with no git directory it
// may use: none at all, or one that resolves outside the root.
var ErrNotARepo = errors.New("not a git repository")

// WorkingDiff returns the staged and unstaged diff of the repository at name inside root, each cut
// to maxBytes. git gets descriptors opened through root as GIT_WORK_TREE, GIT_DIR and
// GIT_COMMON_DIR, which outrank core.worktree, so no swapped component, outside .git or
// core.worktree can point it elsewhere.
func WorkingDiff(ctx context.Context, root *os.Root, name string, maxBytes int) (staged, unstaged string, err error) {
	if ctx.Err() != nil {
		return "", "", ErrNotARepo
	}
	repo, err := openRepo(root, name)
	if err != nil {
		return "", "", err
	}
	defer repo.close()
	repoOpened()
	ctx, cancel := context.WithTimeout(ctx, workingDiffTimeout)
	defer cancel()
	staged, err = diffOutput(ctx, repo, maxBytes, "--cached")
	if err != nil {
		return "", "", err
	}
	unstaged, err = diffOutput(ctx, repo, maxBytes)
	return staged, unstaged, err
}

var repoOpened = func() {}

// pinnedRepo holds the descriptors git is pointed at. git inherits them as
// ExtraFiles, so each path names the CHILD's descriptor: files[i] is its fd
// 3+i.
type pinnedRepo struct {
	workTree, gitDir, commonDir string
	files                       []*os.File
}

func (r *pinnedRepo) close() {
	for _, f := range r.files {
		_ = f.Close()
	}
}

func (r *pinnedRepo) keep(f *os.File) string {
	r.files = append(r.files, f)
	return "/proc/self/fd/" + strconv.Itoa(2+len(r.files))
}

func openRepo(root *os.Root, name string) (*pinnedRepo, error) {
	wt, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open work tree: %w", err)
	}
	repo := &pinnedRepo{}
	repo.workTree = repo.keep(wt)
	gitDir, gitRel, err := openGitDir(root, wt, name)
	if err != nil {
		repo.close()
		return nil, err
	}
	repo.gitDir = repo.keep(gitDir)
	common, err := openCommonDir(root, gitDir, gitRel)
	if err != nil {
		repo.close()
		return nil, err
	}
	if common != nil {
		repo.commonDir = repo.keep(common)
	}
	return repo, nil
}

// The root-relative paths are joined without cleaning so os.Root resolves each ".." after following
// the symlinks before it, as git does.
func openGitDir(root *os.Root, wt *os.File, name string) (*os.File, string, error) {
	rel := name + "/" + gitDirName
	g, err := openNoFollow(wt, gitDirName)
	if errors.Is(err, unix.ELOOP) {
		g, err = root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, "", ErrNotARepo
	}
	st, err := g.Stat()
	switch {
	case err != nil:
	case st.IsDir():
		return g, rel, nil
	case st.Mode().IsRegular():
		target, ok := readGitFile(g)
		_ = g.Close()
		if !ok {
			return nil, "", ErrNotARepo
		}
		return openInRoot(root, name, target)
	}
	_ = g.Close()
	return nil, "", ErrNotARepo
}

// openCommonDir opens the common directory a linked worktree's git directory
// names in its commondir file, or returns nil when it names none.
func openCommonDir(root *os.Root, gitDir *os.File, gitRel string) (*os.File, error) {
	f, err := openNoFollow(gitDir, "commondir")
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrNotARepo
	}
	raw, err := io.ReadAll(io.LimitReader(f, headDocMaxBytes))
	_ = f.Close()
	target := strings.TrimSpace(string(raw))
	if err != nil || target == "" {
		return nil, ErrNotARepo
	}
	common, _, err := openInRoot(root, gitRel, target)
	return common, err
}

func openInRoot(root *os.Root, base, target string) (*os.File, string, error) {
	rel := base + "/" + target
	if filepath.IsAbs(target) {
		var ok bool
		if rel, ok = rootRelative(root, target); !ok {
			return nil, "", ErrNotARepo
		}
	}
	d, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", ErrNotARepo
	}
	return d, rel, nil
}

// rootRelative maps an absolute target under root to a root-relative path. git
// writes the real path of the main repository, so the root's own path is tried
// both as opened and with its symlinks resolved.
func rootRelative(root *os.Root, target string) (string, bool) {
	bases := []string{root.Name()}
	if resolved, err := filepath.EvalSymlinks(root.Name()); err == nil {
		bases = append(bases, resolved)
	}
	for _, base := range bases {
		rel, err := filepath.Rel(base, target)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return rel, true
		}
	}
	return "", false
}

func readGitFile(f *os.File) (string, bool) {
	raw, err := io.ReadAll(io.LimitReader(f, headDocMaxBytes))
	if err != nil {
		return "", false
	}
	return parseGitFile(string(raw))
}

func openNoFollow(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func diffOutput(ctx context.Context, repo *pinnedRepo, maxBytes int, extra ...string) (string, error) {
	if _, ok := resolveGitBinary(); !ok {
		return "", errGitUnavailable
	}
	args := append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv"}, extra...)
	out := &headBuffer{max: maxBytes}
	// The child chdirs before exec while it still holds the parent's
	// descriptors, so git runs in the directory the caller opened even if a
	// component of its pathname has been swapped since.
	cmd := gitExec(ctx, "/proc/self/fd/"+strconv.Itoa(int(repo.files[0].Fd())), args...)
	cmd.ExtraFiles = repo.files
	cmd.Env = append(cmd.Env, "GIT_DIR="+repo.gitDir, "GIT_WORK_TREE="+repo.workTree)
	if repo.commonDir != "" {
		cmd.Env = append(cmd.Env, "GIT_COMMON_DIR="+repo.commonDir)
	}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out.buf), nil
}

// headBuffer keeps the first max bytes and accepts the rest silently, so git
// never sees a short write and the output stays bounded.
type headBuffer struct {
	buf []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
