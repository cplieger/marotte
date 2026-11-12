package tabs

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// RecentWindow is how long a closed chat stays in the Recently closed list.
const RecentWindow = 6 * time.Hour

// maxRecent caps the list: one close of a chat with tangents lists each of them, so a smaller cap
// would let one close push every earlier entry out.
const maxRecent = 10

// RecentFacts is what a closed chat's entry carries from its record, which the store cannot read.
type RecentFacts struct {
	Name string
}

// expired reports whether e's window has ended at now (Unix ms).
func expired(e *marotte.RecentClose, now int64) bool {
	return e.ClosedAt+RecentWindow.Milliseconds() <= now
}

// dropExpired removes every entry whose window has ended at now and reports whether it removed one.
func dropExpired(recent []marotte.RecentClose, now int64) ([]marotte.RecentClose, bool) {
	before := len(recent)
	recent = slices.DeleteFunc(recent, func(e marotte.RecentClose) bool { return expired(&e, now) })
	return recent, len(recent) != before
}

// listClosed puts each chat of closed that owed gives a chat release (its last tab closed) at the
// head of recent, in tab order, unless unlisted names it, and cuts the list to maxRecent.
func listClosed(recent []marotte.RecentClose, closed []marotte.TabSubject, owed []OwedRelease,
	unlisted []string, facts map[string]RecentFacts,
) []marotte.RecentClose {
	head := make([]marotte.RecentClose, 0, len(owed))
	for _, t := range closed {
		if t.Kind != marotte.TabKindChat || slices.Contains(unlisted, t.Ref) {
			continue
		}
		i := slices.IndexFunc(owed, func(o OwedRelease) bool { return o.Kind == t.Kind && o.Ref == t.Ref })
		if i < 0 {
			continue
		}
		head = append(head, marotte.RecentClose{Ref: t.Ref, Name: facts[t.Ref].Name, ClosedAt: owed[i].ClosedAt, Pinned: t.Pinned})
	}
	if len(head) == 0 {
		return recent
	}
	// No dedupe: a listed chat has no open tab (Open consumes its entry, load drops an open one).
	head = append(head, recent...)
	return head[:min(len(head), maxRecent)]
}

// takeRecent removes ref's entry and returns it, with false when ref is not listed.
func takeRecent(recent []marotte.RecentClose, ref string) (rest []marotte.RecentClose, taken marotte.RecentClose, found bool) {
	i := slices.IndexFunc(recent, func(e marotte.RecentClose) bool { return e.Ref == ref })
	if i < 0 {
		return recent, marotte.RecentClose{}, false
	}
	taken = recent[i]
	return slices.Delete(recent, i, i+1), taken, true
}

// sanitizeRecent drops each loaded entry this store could not have written or must not show, and
// returns the list newest first, deduped and capped. Shape checks only; a gone chat is Prune's.
func sanitizeRecent(in []marotte.RecentClose, open []marotte.TabSubject, now int64) []marotte.RecentClose {
	out := make([]marotte.RecentClose, 0, len(in))
	for _, e := range in {
		if !ids.ValidChatID(e.Ref) || e.ClosedAt <= 0 || e.ClosedAt > now+RecentWindow.Milliseconds() {
			slog.Warn("tabs: invalid recently closed entry dropped on load", "ref", e.Ref, "closed_at", e.ClosedAt)
			continue
		}
		// encoding/json already replaced any invalid UTF-8 with U+FFFD, so the length is the only check left.
		if len(e.Name) > marotte.MaxChatNameBytes {
			slog.Warn("tabs: a recently closed entry's name was too long and is cleared on load", "ref", e.Ref)
			e.Name = ""
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b marotte.RecentClose) int { return cmp.Compare(b.ClosedAt, a.ClosedAt) })
	seen := make(map[string]struct{}, len(out))
	out = slices.DeleteFunc(out, func(e marotte.RecentClose) bool {
		if _, dup := seen[e.Ref]; dup {
			return true
		}
		seen[e.Ref] = struct{}{}
		return expired(&e, now) || indexOfSubject(open, marotte.TabKindChat, e.Ref) >= 0
	})
	if len(out) == 0 {
		return nil
	}
	return out[:min(len(out), maxRecent)]
}

// pruneRecent drops each entry whose chat exists does not resolve, logging each, and reports
// whether it dropped one. A nil exists resolves everything.
func pruneRecent(recent []marotte.RecentClose, exists func(marotte.TabSubject) bool) ([]marotte.RecentClose, bool) {
	if exists == nil {
		return recent, false
	}
	before := len(recent)
	recent = slices.DeleteFunc(recent, func(e marotte.RecentClose) bool {
		if exists(marotte.TabSubject{Kind: marotte.TabKindChat, Ref: e.Ref}) {
			return false
		}
		slog.Info("tabs: a recently closed entry whose chat is gone was dropped on load", "ref", e.Ref)
		return true
	})
	return recent, len(recent) != before
}

// Forget drops ref's entry, for a chat being deleted or purged. A ref not listed bumps nothing.
func (s *Store) Forget(ctx context.Context, ref string) (uint64, error) {
	return s.mutate(ctx, func(st *state, _ int64) (bool, error) {
		var found bool
		st.recent, _, found = takeRecent(st.recent, ref)
		return found, nil
	})
}

// ExpireRecent drops every entry whose window has ended at now, with a version of its own, and
// bumps nothing when none has. It is the only removal that is not part of another change.
func (s *Store) ExpireRecent(ctx context.Context, now time.Time) (uint64, error) {
	return s.mutate(ctx, func(st *state, _ int64) (bool, error) {
		var changed bool
		st.recent, changed = dropExpired(st.recent, now.UnixMilli())
		return changed, nil
	})
}

// Recent returns a copy of the Recently closed list, newest first.
func (s *Store) Recent() []marotte.RecentClose {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return slices.Clone(s.recent)
}

// Snapshot returns the set, the Recently closed list and the version both reflect, from ONE
// critical section, so a reader never pairs one mutation's list with another's version.
func (s *Store) Snapshot() (tabs []marotte.TabSubject, recent []marotte.RecentClose, version uint64) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return cloneTabs(s.tabs), slices.Clone(s.recent), s.version
}
