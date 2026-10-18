package composition

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/cplieger/marotte/internal/workspace"
)

// The roots a marotte writes into, each claimed by exactly one process.
const (
	roleConfigDir = "config_dir"
	roleKiroHome  = "kiro_home"
	roleToolsDir  = "tools_dir"
)

const lockFileName = "marotte.lock"

// ownedRoot is one folder this process writes into, under the role the logs name it by.
type ownedRoot struct{ role, dir string }

// ownedRoots lists every root Build's writers reach. KIRO_WORK_DIR is not one: two instances on
// one workspace with separate homes are a supported shape.
func ownedRoots(cfg *Config) []ownedRoot {
	roots := []ownedRoot{{roleConfigDir, cfg.ConfigDir}}
	if home, err := workspace.KiroHomeDir(); err != nil {
		slog.Warn("could not lock this folder for this instance, so a second marotte on it would not be refused",
			"root", roleKiroHome, "path", workspace.KiroHome(), "error", err)
	} else {
		roots = append(roots, ownedRoot{roleKiroHome, home})
	}
	return append(roots, ownedRoot{roleToolsDir, cfg.ToolsDir})
}

// heldRoots keeps the claimed folders and lock files open. It must stay reachable for the process lifetime:
// an unreachable *os.File is closed by its finalizer, and closing the fd drops the flock.
type heldRoots struct{ files []*os.File }

func (h *heldRoots) release() {
	for _, f := range h.files {
		// The fd holds no pending writes, so a close error changes nothing.
		_ = f.Close()
	}
	h.files = nil
}

// rootHeldError is claimRoots' refusal: another process holds the root's lock.
type rootHeldError struct {
	Role, Dir, LockFile string
	// HolderPID is the pid written in the lock file, 0 when the holder had not written it yet.
	HolderPID int
}

func (e *rootHeldError) Error() string {
	if e.HolderPID == 0 {
		return fmt.Sprintf("another marotte instance holds %s %s", e.Role, e.Dir)
	}
	return fmt.Sprintf("another marotte instance (pid %d) holds %s %s", e.HolderPID, e.Role, e.Dir)
}

// claimRoots takes a non-blocking exclusive flock on <dir>/marotte.lock in every root and writes
// this pid there. A root another process holds logs an ERROR, releases every claim taken so far and
// returns *rootHeldError. Any other failure (mkdir, open, a share without locks) warns and boots on
// without that claim. The kernel drops a flock when its holder exits, SIGKILL included.
func claimRoots(roots []ownedRoot) (*heldRoots, error) {
	held := &heldRoots{}
	// A second flock on another fd of the same file conflicts inside one process, so a folder
	// reached under two roles or through a symlink is claimed once.
	seen := make(map[string]struct{}, len(roots))
	for _, r := range roots {
		if r.dir == "" {
			continue
		}
		key, err := resolveRoot(r.dir)
		if err != nil {
			warnUnclaimed(r, err)
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		files, err := lockRoot(r)
		if refused, ok := errors.AsType[*rootHeldError](err); ok {
			slog.Error("another marotte instance holds this folder, so this one is refusing to start",
				"root", refused.Role, "path", refused.Dir, "holder_pid", refused.HolderPID,
				"lock_file", refused.LockFile,
				"hint", "each marotte needs its own KIRO_CONFIG_DIR, KIRO_HOME and MAROTTE_TOOLS_DIR; stop the other one or point this one at separate folders. holder_pid is the process id inside the holder's own container")
			held.release()
			return nil, err
		}
		if err != nil {
			warnUnclaimed(r, err)
			continue
		}
		held.files = append(held.files, files...)
	}
	return held, nil
}

// resolveRoot creates dir, then answers its symlink-resolved path, which needs dir to exist.
func resolveRoot(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

// lockRoot flocks the folder itself, which deleting marotte.lock cannot release, then marotte.lock,
// which a build that locks only the file checks, as does a marotte on another host of a network
// share (a folder's flock is local to one kernel there). A folder held but a marotte.lock that
// cannot be opened or locked for another cause keeps the folder's claim and warns.
func lockRoot(r ownedRoot) ([]*os.File, error) {
	path := filepath.Join(r.dir, lockFileName)
	dir, err := os.Open(r.dir)
	if err != nil {
		return nil, err
	}
	if err = flockNB(dir); err != nil {
		_ = dir.Close()
		return nil, lockFailure(r, path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		if err = flockNB(f); err != nil {
			_ = f.Close()
		}
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		_ = dir.Close()
		return nil, lockFailure(r, path, err)
	}
	if err != nil {
		slog.Warn("locked this folder but not its lock file, so a marotte on another host would not be refused",
			"root", r.role, "path", r.dir, "lock_file", path, "error", err)
		return []*os.File{dir}, nil
	}
	if err := writeHolderPID(f); err != nil {
		slog.Warn("locked this folder but could not record this pid in its lock file",
			"root", r.role, "path", r.dir, "lock_file", path, "error", err)
	}
	return []*os.File{dir, f}, nil
}

func flockNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// lockFailure is a *rootHeldError for contention, naming the pid the holder wrote into path.
func lockFailure(r ownedRoot, path string, err error) error {
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return &rootHeldError{Role: r.role, Dir: r.dir, LockFile: path, HolderPID: readHolderPID(path)}
	}
	return fmt.Errorf("flock %s: %w", r.dir, err)
}

func writeHolderPID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// readHolderPID answers 0 for a missing, empty or unparsable file: the holder locks before it writes.
func readHolderPID(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 32))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

func warnUnclaimed(r ownedRoot, err error) {
	slog.Warn("could not lock this folder for this instance, so a second marotte on it would not be refused",
		"root", r.role, "path", r.dir, "error", err)
}
