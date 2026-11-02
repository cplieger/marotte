// Package asidefile keeps a state file a store could not read: Move renames it out of the way of the
// store's next write, and Find tells a later boot that an earlier one left such a copy.
package asidefile

import (
	"errors"
	"fmt"
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
