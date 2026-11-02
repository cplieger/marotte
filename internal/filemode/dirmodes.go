package filemode

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// DirMode is one folder SetDirModes acts on: Path is absolute, and Want maps the folder's
// permission bits to the ones it must carry.
type DirMode struct {
	Want func(perm fs.FileMode) fs.FileMode
	Path string
}

// NoGroupOrOtherWrite is a DirMode.Want that removes group and other write.
func NoGroupOrOtherWrite(perm fs.FileMode) fs.FileMode { return perm &^ 0o022 }

// Exactly returns a DirMode.Want that sets perm.
func Exactly(perm fs.FileMode) func(fs.FileMode) fs.FileMode {
	return func(fs.FileMode) fs.FileMode { return perm }
}

// ErrNotADir marks a symlink, or something other than a folder, where SetDirModes expected a folder.
var ErrNotADir = errors.New("is a symlink or not a folder")

// DirModeResult is one change SetDirModes made, or the place it stopped.
type DirModeResult struct {
	// Err is nil for a change. Otherwise Path, which may be a folder on the way to a listed one,
	// and every folder below it kept their modes.
	Err      error
	Path     string
	Old, New fs.FileMode
}

// SetDirModes gives each folder its wanted mode with fchmod on a descriptor opened without
// following a symlink, so neither a planted link nor a later rename redirects it. A folder beneath
// another listed one (by its name or its kernel path) is opened from it one name at a time, any
// other through its parent, which the kernel resolves. A missing folder is skipped with everything
// below it, and special bits are kept. Results hold each change and each failure once.
func SetDirModes(dirs []DirMode) []DirModeResult {
	w := &dirWalk{opened: map[string]walkStep{}}
	defer w.close()
	sorted := make([]DirMode, 0, len(dirs))
	for _, d := range dirs {
		d.Path = filepath.Clean(d.Path)
		sorted = append(sorted, d)
		w.listed = append(w.listed, d.Path)
	}
	// Shallow first, so a folder's base is open, and its kernel path known, before the walk needs it.
	slices.SortStableFunc(sorted, func(a, b DirMode) int {
		return cmp.Compare(strings.Count(a.Path, "/"), strings.Count(b.Path, "/"))
	})
	for _, d := range sorted {
		if !filepath.IsAbs(d.Path) {
			w.results = append(w.results, DirModeResult{Path: d.Path, Err: errors.New("is not an absolute path")})
			continue
		}
		if fd, ok := w.open(d.Path); ok {
			w.set(fd, d)
		}
	}
	return w.results
}

// errSkipped marks a folder below one whose failure is already reported.
var errSkipped = errors.New("skipped")

// testHookOpened runs after each folder is opened, so a test can rename it from under the walk.
var testHookOpened = func(string) {}

// fchmod is unix.Fchmod, replaced by a test to make it fail.
var fchmod = unix.Fchmod

type walkStep struct {
	err error
	fd  int
}

type dirWalk struct {
	opened  map[string]walkStep
	listed  []string
	fds     []int
	results []DirModeResult
}

func (w *dirWalk) close() {
	for _, fd := range w.fds {
		_ = unix.Close(fd)
	}
}

func (w *dirWalk) open(p string) (int, bool) {
	if s, ok := w.opened[p]; ok {
		return s.fd, s.err == nil
	}
	var fd int
	var err error
	if w.hasBase(p) {
		parent, ok := w.open(filepath.Dir(p))
		if !ok {
			w.opened[p] = walkStep{fd: -1, err: errSkipped}
			return -1, false
		}
		fd, err = openNoFollow(parent, filepath.Base(p))
	} else {
		fd, err = openAnchor(p)
	}
	if err != nil {
		w.opened[p] = walkStep{fd: -1, err: err}
		if !errors.Is(err, fs.ErrNotExist) {
			w.results = append(w.results, DirModeResult{Path: p, Err: err})
		}
		return -1, false
	}
	w.fds = append(w.fds, fd)
	w.opened[p] = walkStep{fd: fd}
	// Below the folder's kernel path the walk goes on from this descriptor as well.
	if k, kerr := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd)); kerr == nil && k != p {
		if _, seen := w.opened[k]; !seen {
			w.opened[k] = walkStep{fd: fd}
		}
	}
	testHookOpened(p)
	return fd, true
}

// hasBase reports whether p lies strictly beneath a listed folder or one the walk opened.
func (w *dirWalk) hasBase(p string) bool {
	if slices.ContainsFunc(w.listed, func(d string) bool { return beneath(d, p) }) {
		return true
	}
	for d, s := range w.opened {
		if s.err == nil && beneath(d, p) {
			return true
		}
	}
	return false
}

func beneath(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

func openAnchor(p string) (int, error) {
	parent := filepath.Dir(p)
	if parent == p {
		return unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	}
	pfd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", parent, err)
	}
	defer func() { _ = unix.Close(pfd) }()
	return openNoFollow(pfd, filepath.Base(p))
}

// openNoFollow opens name in dirfd as a folder. O_NONBLOCK keeps a FIFO at the name from blocking
// the boot.
func openNoFollow(dirfd int, name string) (int, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return -1, ErrNotADir
	}
	return fd, err
}

func (w *dirWalk) set(fd int, d DirMode) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		w.results = append(w.results, DirModeResult{Path: d.Path, Err: err})
		return
	}
	old := fs.FileMode(st.Mode & 0o777)
	want := d.Want(old) & 0o777
	if want == old {
		return
	}
	if err := fchmod(fd, st.Mode&0o7000|uint32(want)); err != nil {
		w.results = append(w.results, DirModeResult{Path: d.Path, Old: old, Err: err})
		return
	}
	w.results = append(w.results, DirModeResult{Path: d.Path, Old: old, New: want})
}
