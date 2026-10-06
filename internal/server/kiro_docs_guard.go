// What the `.kiro` docs scan may open: two layers of one mechanism. (1) Every entry is
// resolved and refused if it leaves the root (os.DirFS does not confine symlinks). (2) The
// resolved path is checked against filebrowse.Sensitive, the file browser's own deny list,
// which matches absolute paths and so needs the resolution to match anything.

package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/logsafe"
)

// docVerdict is the guard's answer about one entry: whether the scan may read it, and
// whether its row may offer a DELETE. There is no writability bit: a save through a
// followed link writes the canonical target, so a symlinked entry is still editable.
type docVerdict struct {
	allowed bool
	// deleteProtected marks an entry whose DELETE must not be offered: its OWN final component
	// is a symlink, so a delete would remove a different row's file (a file under a linked
	// directory is not that case). Advisory only; the delete route keeps every server-side check.
	deleteProtected bool
}

// pathGuard reports what the scan may do with one entry, by its path WITHIN the walked fs.FS.
// A nil guard admits everything (the MapFS tests); a restriction is asserted, never inferred.
type pathGuard func(rel string) docVerdict

func (g pathGuard) allows(rel string) bool {
	return g == nil || g(rel).allowed
}

// rootGuard is the production guard for one `.kiro` tree.
type rootGuard struct {
	// dir is the root as EvalSymlinks resolved it (an operator may symlink `.kiro` in), so its
	// target is the boundary.
	dir string
	// category names the tree in the log line, so a refusal is attributable
	// without the operator having to guess which walk produced it.
	category string
	// sensitive is the file browser's own deny list.
	sensitive filebrowse.Sensitive
}

// newRootGuard resolves dir and returns a guard over it; an unresolvable dir yields a guard
// that refuses everything.
func newRootGuard(dir, category string, sensitive filebrowse.Sensitive) pathGuard {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		slog.Warn("kiro docs: root not resolvable, skipping", "dir", dir, "error", logsafe.Field(err.Error()))
		return func(string) docVerdict { return docVerdict{} }
	}
	g := &rootGuard{dir: resolved, category: category, sensitive: sensitive}
	return g.allow
}

func (g *rootGuard) allow(rel string) docVerdict {
	full := filepath.Join(g.dir, rel)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		// Dangling, vanished mid-walk, or a permission wall: unreadable either way.
		return docVerdict{}
	}
	if !g.inRoot(resolved) {
		slog.Warn("kiro docs: refusing a link out of the scanned tree",
			"category", g.category, "path", logsafe.Field(rel), "root", g.dir)
		return docVerdict{}
	}
	if g.sensitive.Blocks(resolved) {
		slog.Warn("kiro docs: refusing a path on the sensitive denylist",
			"category", g.category, "path", logsafe.Field(rel))
		return docVerdict{}
	}
	// An in-root link is listed and editable; only its delete is withheld (see deleteProtected).
	return docVerdict{allowed: true, deleteProtected: finalComponentIsLink(full)}
}

// finalComponentIsLink reports whether the last component of full is itself a symlink, the
// only shape whose delete removes a DIFFERENT row's file. An error withholds the affordance.
func finalComponentIsLink(full string) bool {
	fi, err := os.Lstat(full)
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// inRoot reports whether an already-resolved absolute path is the root or beneath it. The
// separator is part of the comparison: without it `/workspace/.kiro-evil` would pass.
func (g *rootGuard) inRoot(resolved string) bool {
	return resolved == g.dir || strings.HasPrefix(resolved, g.dir+string(filepath.Separator))
}

// errRefused is what a guarded read returns for a declined path, on the same channel as an
// unreadable file because every caller does the same thing: no row, keep scanning.
var errRefused = errors.New("kiro docs: path refused by the scan guard")

// readGuardedFS is readCappedFS with the guard in front, returning the verdict with the bytes
// so the caller need not resolve again. The entity scanner shares readCappedFS unguarded.
func readGuardedFS(root fs.FS, name string, guard pathGuard) ([]byte, docVerdict, error) {
	v := docVerdict{allowed: true}
	if guard != nil {
		v = guard(name)
	}
	if !v.allowed {
		return nil, v, errRefused
	}
	data, err := readCappedFS(root, name)
	return data, v, err
}

// readGuardedDir is fs.ReadDir with the guard in front. Refuse AT the directory: a listing is
// itself a disclosure, since skills and agents rows are built from the names.
func readGuardedDir(root fs.FS, name string, guard pathGuard) ([]fs.DirEntry, error) {
	if !guard.allows(name) {
		return nil, errRefused
	}
	return fs.ReadDir(root, name)
}
