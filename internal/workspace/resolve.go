// Package workspace provides path-resolution primitives for
// workspace-scoped file operations. These are security primitives that
// prevent symlink escape and ".." traversal.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/pathinside/v2"
)

// ResolveInsideAbs confines p to absWork, which must already be absolute. It is
// a verdict, not enforcement: the kernel re-resolves the path at the later
// operation, so a caller that touches the filesystem must do so through an
// os.Root rooted at absWork. An empty absWork refuses everything rather than
// confining to the process working directory.
func ResolveInsideAbs(absWork, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(absWork, p)
	}
	return confine(absWork, p)
}

// ConfineAnyAbs confines p to the FIRST of roots that contains it, answering which root
// won and p's name relative to it ("." for the root), for an os.Root open. Roots must be
// absolute; "" is an inert placeholder. An absolute p tries each root in order (failing
// with roots[0]'s error); a relative p resolves against roots[0] only.
func ConfineAnyAbs(roots []string, p string) (root, rel string, err error) {
	if p == "" {
		return "", "", errors.New("empty path")
	}
	if len(roots) == 0 {
		return "", "", errors.New("no roots configured")
	}
	if !filepath.IsAbs(p) {
		return confineRel(roots[0], filepath.Join(roots[0], p))
	}
	var firstErr error
	for _, r := range roots {
		won, name, cErr := confineRel(r, p)
		if cErr == nil {
			return won, name, nil
		}
		if firstErr == nil {
			firstErr = cErr
		}
	}
	return "", "", firstErr
}

// confineRel is confine answering the root-relative name instead of the path.
func confineRel(absRoot, p string) (root, rel string, err error) {
	resolved, err := confine(absRoot, p)
	if err != nil {
		return "", "", err
	}
	rel, err = RelPath(absRoot, resolved)
	if err != nil {
		return "", "", err
	}
	return absRoot, rel, nil
}

// The boundary is one pathinside.Root built before any comparison, so no predicate pair can be
// transposed. Both callers share this body so a symlink-escape fix cannot miss a copy.
func confine(absRoot, p string) (string, error) {
	root := pathinside.Root(absRoot)
	clean := filepath.Clean(p)
	if !root.Contains(clean) {
		return "", errors.New("path escapes workspace")
	}
	if resolved, resErr := filepath.EvalSymlinks(clean); resErr == nil {
		if !root.Contains(resolved) {
			return "", fmt.Errorf("path %q escapes workspace via symlink", p)
		}
		return resolved, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		if os.IsNotExist(err) {
			return clean, nil
		}
		return "", err
	}
	if !root.Contains(parent) {
		return "", fmt.Errorf("path %q escapes workspace via symlink", p)
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

// RelPath returns the workspace-relative, forward-slash path for abs under workDir, or an
// error when filepath.Rel fails.
func RelPath(workDir, abs string) (string, error) {
	rel, err := filepath.Rel(workDir, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
