package filebrowse

// The pinned descent shared by the two recursive routes, search and the zip
// download: every component is opened against its parent's descriptor, so no
// path is resolved twice and a symlink is never followed.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	// pinnedDirFlags opens a directory for a pinned walk: O_DIRECTORY refuses a swapped-in
	// non-directory, O_NOFOLLOW a symlink, O_NONBLOCK a FIFO; O_CLOEXEC keeps it out of a bridge
	// spawn.
	pinnedDirFlags = os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	// pinnedFileFlags is the same open for a leaf, which may legitimately be
	// any file type; the type is refused off the DESCRIPTOR, never the name.
	pinnedFileFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
)

// openChild opens one child (a single component) of an already-open directory
// BY NAME. openat(2) against the descriptor names no ancestor to substitute,
// and O_NOFOLLOW makes the kernel refuse a symlink at the name, which no
// check-then-open can do without a race. SyscallConn keeps dirfd valid for
// the call even when a fan-out shares it across goroutines.
func openChild(dir *os.File, name, displayPath string, flags int) (*os.File, error) {
	conn, err := dir.SyscallConn()
	if err != nil {
		return nil, err
	}
	var fd int
	var openErr error
	if ctlErr := conn.Control(func(dirFD uintptr) {
		for {
			fd, openErr = syscall.Openat(int(dirFD), name, flags, 0)
			if !errors.Is(openErr, syscall.EINTR) {
				return
			}
		}
	}); ctlErr != nil {
		return nil, ctlErr
	}
	if openErr != nil {
		return nil, &os.PathError{Op: "openat", Path: displayPath, Err: openErr}
	}
	return os.NewFile(uintptr(fd), displayPath), nil
}

// openPinnedRoot walks from the mount's root handle down to l one component
// at a time, refusing a symlink at every step: l is already symlink-resolved,
// so a symlink now was substituted since, and an os.Root would follow it
// within the mount. The final component may name a file.
func openPinnedRoot(l loc) (*os.File, error) {
	dir, err := l.m.root.OpenFile(".", pinnedDirFlags, 0)
	if err != nil {
		return nil, err
	}
	rel := l.rel()
	if rel == "." {
		return dir, nil
	}
	names := strings.Split(rel, "/")
	for i, name := range names {
		if name == "" || name == "." || name == ".." {
			_ = dir.Close()
			return nil, fmt.Errorf("filebrowse: root %q has a non-component segment %q", l.abs, name)
		}
		flags := pinnedDirFlags
		if i == len(names)-1 {
			flags = pinnedFileFlags
		}
		child, childErr := openChild(dir, name, filepath.Join(l.m.dir, filepath.Join(names[:i+1]...)), flags)
		_ = dir.Close()
		if childErr != nil {
			return nil, childErr
		}
		dir = child
	}
	return dir, nil
}

// isSwapRefusal reports whether err is the kernel refusing an open because the
// name no longer holds what the walk classified: a symlink under O_NOFOLLOW
// (ELOOP), or a non-directory under O_DIRECTORY (ENOTDIR).
func isSwapRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR)
}
