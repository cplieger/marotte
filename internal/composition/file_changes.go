package composition

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
)

type changeBroadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

type renameFollower interface {
	FollowRename(ctx context.Context, from, to string)
}

type slashRefresher interface {
	RefreshSlashCommands()
}

// fileChanges is the one reaction to a file the browser, the editor or the docs page changed: the tabs follow a
// rename, a shown browser re-lists, the docs page refetches, and the slash menu is re-read. It runs before the change's
// answer, so the tab moves and both frames reach the client first; the slash re-read is only started.
type fileChanges struct {
	hub      changeBroadcaster
	tabs     renameFollower
	slash    slashRefresher
	workDir  string
	kiroHome string
}

func (fc *fileChanges) changed(ctx context.Context, ch filebrowse.Change) {
	if ch.From != "" {
		fc.tabs.FollowRename(ctx, ch.From, ch.To)
	}
	dirs := make([]string, 0, len(ch.Paths))
	for _, p := range ch.Paths {
		if d := filepath.Dir(p); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	fc.hub.Broadcast(ctx, marotte.NewEvent(marotte.EventFilesChanged, "", marotte.FilesChangedPayload{Dirs: dirs}))
	if slices.ContainsFunc(ch.Paths, fc.inDocsRoot) {
		fc.hub.Broadcast(ctx, marotte.NewEvent(marotte.EventKiroDocsChanged, "", marotte.KiroDocsChangedPayload{}))
	}
	if slices.ContainsFunc(ch.Paths, fc.touchesSlashMenu) {
		fc.slash.RefreshSlashCommands()
	}
}

// inDocsRoot reports whether p lies in a .kiro the docs page lists (the workspace's or a non-dot first-level
// folder's, spec.Roots's shape) or is a non-dot first-level entry, which may be a folder holding one. It judges
// by name so a deleted entry still counts; a first-level file over-matches, costing one docs refetch.
func (fc *fileChanges) inDocsRoot(p string) bool {
	rel, err := filepath.Rel(fc.workDir, p)
	if err != nil || pathinside.RelEscapes(rel) {
		return false
	}
	seg := strings.Split(filepath.ToSlash(rel), "/")
	if seg[0] == ".kiro" {
		return true
	}
	return !strings.HasPrefix(seg[0], ".") && (len(seg) == 1 || seg[1] == ".kiro")
}

// touchesSlashMenu reports whether p is at, under or above a folder KAS reads slash commands from: the prompts and
// skills of the workspace's own .kiro and of the user's kiro home.
func (fc *fileChanges) touchesSlashMenu(p string) bool {
	at := pathinside.Root(p)
	for _, base := range []string{filepath.Join(fc.workDir, ".kiro"), fc.kiroHome} {
		for _, kind := range []string{"prompts", "skills"} {
			dir := filepath.Join(base, kind)
			if pathinside.Root(dir).Contains(p) || at.Contains(dir) {
				return true
			}
		}
	}
	return false
}
