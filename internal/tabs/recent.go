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

// RecentHold is how long tabs.json holds a close whatever the window: a wider window brings back
// only what is still held.
const RecentHold = 7 * 24 * time.Hour

// maxRecent caps the list: one close of a chat with tangents lists each of them, so a smaller cap
// would let one close push every earlier entry out.
const maxRecent = 10

// RecentFacts is what a closed chat's entry carries from its record, which the store cannot read.
type RecentFacts struct {
	Name string
	Bot  bool
}

// pastWindow reports whether e's window d has ended at now (Unix ms).
func pastWindow(e *marotte.RecentClose, now int64, d time.Duration) bool {
	return e.ClosedAt+d.Milliseconds() <= now
}

// dropPastHold removes every entry held for RecentHold at now.
func dropPastHold(recent []marotte.RecentClose, now int64) []marotte.RecentClose {
	return slices.DeleteFunc(recent, func(e marotte.RecentClose) bool { return pastWindow(&e, now, RecentHold) })
}

// shownAt returns, in a new slice, the entries of recent inside window at now: what every reader gets.
func shownAt(recent []marotte.RecentClose, window time.Duration, now int64) []marotte.RecentClose {
	out := make([]marotte.RecentClose, 0, len(recent))
	for _, e := range recent {
		if !pastWindow(&e, now, window) {
			out = append(out, e)
		}
	}
	return out
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
		f := facts[t.Ref]
		head = append(head, marotte.RecentClose{Ref: t.Ref, Name: f.Name, ClosedAt: owed[i].ClosedAt, Pinned: t.Pinned, Bot: f.Bot})
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

// sanitizeRecent drops each loaded entry this store could not have written or must not hold, and
// returns the list newest first, deduped and capped. Shape checks only; a gone chat is Prune's.
func sanitizeRecent(in []marotte.RecentClose, open []marotte.TabSubject, now int64) []marotte.RecentClose {
	out := make([]marotte.RecentClose, 0, len(in))
	for _, e := range in {
		if !ids.ValidChatID(e.Ref) || e.ClosedAt <= 0 || e.ClosedAt > now+RecentHold.Milliseconds() {
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
		return pastWindow(&e, now, RecentHold) || indexOfSubject(open, marotte.TabKindChat, e.Ref) >= 0
	})
	if len(out) == 0 {
		return nil
	}
	return out[:min(len(out), maxRecent)]
}

// pruneRecent drops each entry for which exists returns false, logging each drop, and reports
// whether it dropped one. A nil exists keeps every entry.
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

// Forget drops ref's entry, for a chat being deleted or purged, shown or not, so no wider window
// brings it back. A ref not held bumps nothing.
func (s *Store) Forget(ctx context.Context, ref string) (uint64, error) {
	return s.mutate(ctx, func(st *state, _ int64) (bool, error) {
		var found bool
		st.recent, _, found = takeRecent(st.recent, ref)
		return found, nil
	})
}

// Relist puts entries, chats a close unlisted, at the head of the Recently closed list in their order, and cuts the
// list to maxRecent. No entries bumps nothing.
func (s *Store) Relist(ctx context.Context, entries []marotte.RecentClose) (uint64, error) {
	return s.mutate(ctx, func(st *state, _ int64) (bool, error) {
		if len(entries) == 0 {
			return false, nil
		}
		// No dedupe: an unlisted chat has no entry, and no open tab to gain one.
		head := append(slices.Clone(entries), st.recent...)
		st.recent = head[:min(len(head), maxRecent)]
		return true, nil
	})
}

// ExpireRecent hides every shown entry whose window has ended by the store's clock, with a version
// of its own, and bumps nothing when none has. tabs.json still holds a hidden entry until RecentHold.
func (s *Store) ExpireRecent(ctx context.Context) (uint64, error) {
	return s.mutate(ctx, s.reshow)
}

// SetRecentWindow makes window the store's History window, capped at RecentHold, and shows the held
// entries inside it with a version of its own. A shown list it leaves unchanged bumps nothing, but
// the window still moves RecentExpiry. On error the window is set and the shown list is not.
func (s *Store) SetRecentWindow(ctx context.Context, window time.Duration) (uint64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.stateMu.Lock()
	s.window = min(window, RecentHold)
	s.stateMu.Unlock()
	return s.mutateLocked(ctx, s.reshow)
}

// reshow is the apply of a change to the shown list alone: it reports whether the held entries
// inside the window at now differ from the shown ones, and mutateLocked shows them. Callers hold writeMu.
func (s *Store) reshow(st *state, now int64) (bool, error) {
	return !slices.Equal(shownAt(st.recent, s.window, now), st.shown), nil
}

// RecentExpiry returns when the oldest shown entry's window ends, with false when none is shown.
func (s *Store) RecentExpiry() (time.Time, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if len(s.shown) == 0 {
		return time.Time{}, false
	}
	oldest := slices.MinFunc(s.shown, func(a, b marotte.RecentClose) int { return cmp.Compare(a.ClosedAt, b.ClosedAt) })
	return time.UnixMilli(oldest.ClosedAt + s.window.Milliseconds()), true
}

// Recent returns a copy of the Recently closed list the sidebar shows, newest first.
func (s *Store) Recent() []marotte.RecentClose {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return slices.Clone(s.shown)
}

// Snapshot returns the set, the shown Recently closed list and the version both reflect, from ONE
// critical section, so a reader never pairs one mutation's list with another's version.
func (s *Store) Snapshot() (tabs []marotte.TabSubject, recent []marotte.RecentClose, version uint64) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return cloneTabs(s.tabs), slices.Clone(s.shown), s.version
}
