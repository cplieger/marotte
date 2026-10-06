// Path resolution for the file handler; every layer must pass: (1) mount match plus
// sensitive-prefix check on the lexically cleaned path, unknown roots denied; (2) symlink
// evaluation of the target, or its parent when absent, then (1) again on the real path; (3) the
// per-mount os.Root for every operation, so a later swap cannot escape; (4) per-action guards for
// containers the lexical layer protects only as leaves.

package filebrowse

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/pathinside/v2"
)

// mount is one granted browse root: a cleaned absolute directory plus
// its kernel-confined os.Root handle.
type mount struct {
	root *os.Root
	dir  string // clean, absolute, no trailing slash, never "/"
	name string // dir without the leading slash — the synthetic root entry name
}

// loc is a fully resolved location: the granted mount that owns it and
// the real (symlink-evaluated) absolute path inside it. All filesystem
// operations derive from a loc so they run through the mount's os.Root.
type loc struct {
	m   *mount
	abs string
}

// rel returns the path relative to the owning mount, in the form
// os.Root operations expect ("." for the mount directory itself).
func (l loc) rel() string {
	rel := strings.TrimPrefix(l.abs, l.m.dir)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}

// relOf converts sibling absolute path p (must be inside l's mount —
// true by construction at its call site, which joins a basename onto
// a directory already resolved into this mount) to root-relative form.
func (l loc) relOf(p string) string {
	return loc{m: l.m, abs: p}.rel()
}

// isMountPoint reports whether the location is the granted root
// itself, which create/rename/delete refuse to touch.
func (l loc) isMountPoint() bool { return l.abs == l.m.dir }

// errOutsideRoots is the uniform denial for paths outside every
// granted mount. Symlink-probe failures and plain out-of-tree requests
// share one message so an attacker can't distinguish them.
var errOutsideRoots = errors.New("access denied: outside granted roots")

// mountFor returns the granted mount owning the cleaned absolute path, or nil; mounts are sorted
// longest-first, so a nested grant wins. pathinside.Inside keeps a lookalike sibling
// ("/workspace-evil") out.
func (h *Handler) mountFor(clean string) *mount {
	for i := range h.mounts {
		m := &h.mounts[i]
		if pathinside.Root(m.dir).Contains(clean) {
			return m
		}
	}
	return nil
}

// enforce runs the allow-list + sensitive-path policy on an
// already-canonicalised absolute path. Applied to both the lexical and
// the real-path forms by resolvePath so they enforce identical policy —
// any drift would create a symlink-based bypass.
func (h *Handler) enforce(clean string) (*mount, error) {
	m := h.mountFor(clean)
	if m == nil {
		return nil, errOutsideRoots
	}
	if h.sensitive.Blocks(clean) {
		return nil, errors.New("access denied: protected path")
	}
	return m, nil
}

// resolvePath cleans reqPath and enforces the access policy on both its lexical and real-path
// forms; a symlink escaping the mounts or landing on a sensitive path gets the lexical error. A
// target that does not exist yet is resolved through its parent. The loc carries the mount owning
// the REAL path.
func (h *Handler) resolvePath(reqPath string) (loc, error) {
	clean := filepath.Clean("/" + reqPath)
	if _, err := h.enforce(clean); err != nil {
		return loc{}, err
	}
	realPath, err := resolveRealPath(clean)
	if err != nil {
		return loc{}, err
	}
	m, err := h.enforce(realPath)
	if err != nil {
		return loc{}, err
	}
	return loc{m: m, abs: realPath}, nil
}

// resolveRealPath evaluates symlinks on an absolute, cleaned path. For a target that does not exist
// it walks up to the first ancestor that resolves and recomposes the missing suffix, so a symlinked
// ancestor over a deep missing leaf cannot leak an unresolved segment past enforce.
func resolveRealPath(clean string) (string, error) {
	if realPath, err := filepath.EvalSymlinks(clean); err == nil {
		return realPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Walk up until an ancestor resolves; at "/" every component was missing and the validated
	// lexical path is safe.
	var tail []string
	cur := clean
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean, nil
		}
		tail = append(tail, filepath.Base(cur))
		realParent, err := filepath.EvalSymlinks(parent)
		if err == nil {
			out := realParent
			for _, t := range slices.Backward(tail) {
				out = filepath.Join(out, t)
			}
			return out, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		cur = parent
	}
}

// ParseBrowseRoots normalises a colon-separated root list (the
// MAROTTE_BROWSE_ROOTS format, PATH-style) into cleaned absolute
// directories. Relative entries and "/" are rejected with an error
// listing so the caller can log them; duplicates collapse.
func ParseBrowseRoots(raw string) (roots, invalid []string) {
	seen := make(map[string]bool)
	for entry := range strings.SplitSeq(raw, ":") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !filepath.IsAbs(entry) {
			invalid = append(invalid, entry+" (not absolute)")
			continue
		}
		clean := filepath.Clean(entry)
		if clean == "/" {
			invalid = append(invalid, entry+" (root grant defeats the allow-list)")
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		roots = append(roots, clean)
	}
	return roots, invalid
}

// openMounts opens an os.Root per granted directory. A directory that cannot be opened is skipped
// with its error recorded, so a typo'd grant cannot brick the UI; the caller fails if none survive.
func openMounts(rootDirs []string) ([]mount, []error) {
	var errs []error
	seen := make(map[string]bool)
	mounts := make([]mount, 0, len(rootDirs))
	for _, dir := range rootDirs {
		clean := filepath.Clean(dir)
		if !filepath.IsAbs(clean) || clean == "/" {
			errs = append(errs, fmt.Errorf("browse root %q: must be an absolute path other than /", dir))
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		root, err := os.OpenRoot(clean)
		if err != nil {
			errs = append(errs, fmt.Errorf("browse root %q: %w", dir, err))
			continue
		}
		mounts = append(mounts, mount{
			root: root,
			dir:  clean,
			name: strings.TrimPrefix(clean, "/"),
		})
	}
	// Longest dir first so nested grants win prefix matching.
	slices.SortFunc(mounts, func(a, b mount) int {
		if d := len(b.dir) - len(a.dir); d != 0 {
			return d
		}
		return strings.Compare(a.dir, b.dir)
	})
	return mounts, errs
}
