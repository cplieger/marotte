// Package asidefile keeps a state file a store could not read: Move renames it out of the way of the
// store's next write, and Find tells a later boot that an earlier one left such a copy.
package asidefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// infix joins the file's name to the UTC nanosecond stamp of the boot that could not read it.
const infix = ".unreadable-"

// Move renames path to <path>.unreadable-<UTC ns> and returns the new path.
func Move(path string) (string, error) {
	aside := fmt.Sprintf("%s%s%d", path, infix, time.Now().UTC().UnixNano())
	if err := os.Rename(path, aside); err != nil {
		return "", err
	}
	return aside, nil
}

// Copy writes path's bytes to <path>.unreadable-<UTC ns> with perm and returns the new path, for a store that
// keeps the file in place and repairs it. A failed copy leaves no aside.
func Copy(path string, perm os.FileMode) (aside string, err error) {
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	aside = fmt.Sprintf("%s%s%d", path, infix, time.Now().UTC().UnixNano())
	dst, err := os.OpenFile(aside, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(dst, src)
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(aside)
		return "", err
	}
	return aside, nil
}

// Absent reports whether err says the path does not exist: missing, or under a component that is not a
// directory, which holds nothing either.
func Absent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// Find returns the path of the first aside of name in dir, or "" when there is none. An absent dir holds
// none; any other listing error is returned, since it hides whether an aside exists.
func Find(dir, name string) (string, error) {
	entries, err := os.ReadDir(dir)
	if Absent(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name+infix) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", nil
}
