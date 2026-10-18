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

// heldRoots keeps the claimed lock files open. It must stay reachable for the process lifetime:
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
		f, err := lockRoot(r)
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
		held.files = append(held.files, f)
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

func lockRoot(r ownedRoot) (*os.File, error) {
	path := filepath.Join(r.dir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		pid := readHolderPID(f)
		_ = f.Close()
		return nil, &rootHeldError{Role: r.role, Dir: r.dir, LockFile: path, HolderPID: pid}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	if err := writeHolderPID(f); err != nil {
		slog.Warn("locked this folder but could not record this pid in its lock file",
			"root", r.role, "path", r.dir, "lock_file", path, "error", err)
	}
	return f, nil
}

func writeHolderPID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// readHolderPID answers 0 for an empty or unparsable file: the holder locks before it writes.
func readHolderPID(f *os.File) int {
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
