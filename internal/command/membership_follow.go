package command

import (
	"context"
	"log/slog"
	"path"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// tabMove is one tab a rename moves: its id and the ref it moves to.
type tabMove struct {
	id, ref string
}

// FollowRename moves every open editor and web tab whose path is from or lies under it to the same place under to,
// one tabs_changed frame per tab. A web tab already at a destination is closed first, through the ordinary close, in
// its own frame; an editor tab there is never closed, since its unsaved text would go with it, so the editor tab
// that would move onto it is refused by Retarget and stays (the rename itself refuses that case, EditorTabAt).
// Paths compare cleaned, so "workspace/a" and "/workspace/a" match, and a moved ref keeps its own leading-slash
// spelling. Matching is by spelled path: a tab opened through a symlink alias does not follow. Failures are logged
// and the remaining tabs still move; it never refuses the rename it follows.
func (m *Membership) FollowRename(ctx context.Context, from, to string) {
	from, to = cleanTabPath(from), cleanTabPath(to)
	if m.tabs == nil || from == "/" || from == to {
		return
	}
	m.mu.Lock()
	open, _ := m.tabs.List()
	holders := destinationHolders(open, renameMoves(open, from, to))
	m.mu.Unlock()
	// closeTab takes mu itself; a tab opened at a destination in between is refused by Retarget below.
	for _, id := range holders {
		if _, _, err := m.closeTab(ctx, id, "", false, nil); err != nil {
			slog.Warn("rename: the tab at the destination was not closed", "tab", id, "error", err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	open, _ = m.tabs.List()
	for _, mv := range renameMoves(open, from, to) {
		recent := m.tabs.Recent()
		before := m.version()
		version, err := m.tabs.Retarget(ctx, mv.id, mv.ref)
		if err != nil {
			slog.Warn("rename: the tab did not follow the file", "tab", mv.id, "to", mv.ref, "error", err)
			continue
		}
		if changed, ok := m.subject(mv.id); ok && version != before {
			m.emit(ctx, &marotte.TabsChangedPayload{Changed: &changed, Recent: m.recentChange(recent), Version: version})
		}
	}
}

// renameMoves is every editor and web tab at or under from, with the ref it takes under to. from and to are cleaned.
func renameMoves(open []marotte.TabSubject, from, to string) []tabMove {
	var out []tabMove
	for _, t := range open {
		if t.Kind != marotte.TabKindEditor && t.Kind != marotte.TabKindWeb {
			continue
		}
		p := cleanTabPath(t.Ref)
		if p != from && !strings.HasPrefix(p, from+"/") {
			continue
		}
		ref := to + p[len(from):]
		if !strings.HasPrefix(t.Ref, "/") {
			ref = strings.TrimPrefix(ref, "/")
		}
		out = append(out, tabMove{id: t.ID, ref: ref})
	}
	return out
}

// destinationHolders is every web tab, not itself moving, that already holds a web move's destination.
func destinationHolders(open []marotte.TabSubject, moves []tabMove) []string {
	moving := make(map[string]bool, len(moves))
	for _, mv := range moves {
		moving[mv.id] = true
	}
	var out []string
	for _, mv := range moves {
		if open[indexOfTab(open, mv.id)].Kind != marotte.TabKindWeb {
			continue
		}
		dest := cleanTabPath(mv.ref)
		for _, t := range open {
			if t.Kind == marotte.TabKindWeb && !moving[t.ID] && cleanTabPath(t.Ref) == dest {
				out = append(out, t.ID)
			}
		}
	}
	return out
}

// cleanTabPath is the one spelling a path ref compares in: absolute and cleaned.
func cleanTabPath(ref string) string {
	return path.Clean("/" + ref)
}

// EditorTabAt reports whether an editor tab is open at or under p, compared cleaned. A rename onto such a path is
// refused before it happens: Retarget refuses to move the source's tab onto a ref an editor tab already holds, so
// that tab would stay on the path the file left.
func (m *Membership) EditorTabAt(p string) bool {
	if m.tabs == nil {
		return false
	}
	p = cleanTabPath(p)
	m.mu.Lock()
	open, _ := m.tabs.List()
	m.mu.Unlock()
	return slices.ContainsFunc(open, func(t marotte.TabSubject) bool {
		ref := cleanTabPath(t.Ref)
		return t.Kind == marotte.TabKindEditor && (ref == p || strings.HasPrefix(ref, p+"/"))
	})
}
