package tabs

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// maxOwed caps the owed list, which grows only while releases keep failing. Dropping the oldest entry leaves its
// keep fact in place, so the cap fails toward keeping a chat and never refuses a close.
const maxOwed = 1024

// OwedRelease is a keep-fact release a committed close owes: the tab fact of chat Ref for a chat tab, or the run-tab
// fact of run Ref on each of Holders for a run tab. It is written in the same file write as the close, so a crash
// before the release leaves the evidence on disk.
type OwedRelease struct {
	ID       string          `json:"id"`
	Kind     marotte.TabKind `json:"kind"`
	Ref      string          `json:"ref"`
	Holders  []string        `json:"holders,omitempty"`
	ClosedAt int64           `json:"closed_at"`
}

// owedFor is the releases closing closed owes against what stays open, at now (Unix ms): one per closed subject no
// remaining tab shows.
func owedFor(closed, remaining []marotte.TabSubject, now int64) []OwedRelease {
	var out []OwedRelease
	for _, t := range closed {
		if !ids.ValidChatID(t.Ref) || indexOfSubject(remaining, t.Kind, t.Ref) >= 0 {
			continue
		}
		switch {
		case t.Kind == marotte.TabKindChat:
			out = append(out, OwedRelease{ID: newID(), Kind: t.Kind, Ref: t.Ref, ClosedAt: now})
		case t.Kind == marotte.TabKindRun && len(t.LaunchChats) > 0:
			out = append(out, OwedRelease{
				ID: newID(), Kind: t.Kind, Ref: t.Ref, Holders: slices.Clone(t.LaunchChats), ClosedAt: now,
			})
		}
	}
	return out
}

// appendOwed adds entries past the cap by dropping the oldest.
func appendOwed(owed, add []OwedRelease) []OwedRelease {
	owed = append(owed, add...)
	if over := len(owed) - maxOwed; over > 0 {
		for _, e := range owed[:over] {
			slog.Warn("tabs: owed keep-fact release dropped past the cap; its fact stays on the chat",
				"kind", e.Kind, "ref", e.Ref, "cap", maxOwed)
		}
		owed = slices.Delete(owed, 0, over)
	}
	return owed
}

// sanitizeOwed drops each loaded entry this store could not have written. A dropped entry leaves its fact in place.
func sanitizeOwed(in []OwedRelease) []OwedRelease {
	out := make([]OwedRelease, 0, len(in))
	for _, e := range in {
		if !validOwed(&e) {
			slog.Warn("tabs: invalid owed keep-fact release dropped on load; its fact stays on the chat",
				"kind", e.Kind, "ref", e.Ref)
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return appendOwed(nil, out)
}

func validOwed(e *OwedRelease) bool {
	if e.ID == "" || len(e.ID) > maxIDBytes || !ids.ValidChatID(e.Ref) {
		return false
	}
	if e.Kind != marotte.TabKindChat && e.Kind != marotte.TabKindRun {
		return false
	}
	return !slices.ContainsFunc(e.Holders, func(h string) bool { return !ids.ValidChatID(h) })
}

// Owed lists the releases closes still owe, oldest first.
func (s *Store) Owed() []OwedRelease {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return slices.Clone(s.owed)
}

// Settle removes the owed entries named by entryIDs. It bumps no version and the coordinator emits nothing: the
// client renders none of it.
func (s *Store) Settle(ctx context.Context, entryIDs []string) error {
	if len(entryIDs) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	next := s.snapshot()
	before := len(next.owed)
	next.owed = slices.DeleteFunc(next.owed, func(e OwedRelease) bool { return slices.Contains(entryIDs, e.ID) })
	if len(next.owed) == before {
		return nil
	}
	if err := s.persist(ctx, &next); err != nil {
		return err
	}
	s.publish(&next)
	return nil
}

// SetLaunchChats adds chats to run tab id's LaunchChats, sorted, and sets LaunchResolved when resolved is true; it
// never removes a holder or unresolves a tab. It bumps no version and the coordinator emits nothing: the client
// renders neither field. Returns errNotOpen when id is not an open run tab, ErrBadRef for an invalid chat id.
func (s *Store) SetLaunchChats(ctx context.Context, id string, chats []string, resolved bool) error {
	for _, c := range chats {
		if !ids.ValidChatID(c) {
			return fmt.Errorf("%w: launch chat %q", ErrBadRef, c)
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	next := s.snapshot()
	i := indexOfID(next.tabs, id)
	if i < 0 || next.tabs[i].Kind != marotte.TabKindRun {
		return fmt.Errorf("%w: run tab %q", errNotOpen, id)
	}
	t := &next.tabs[i]
	union := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(t.LaunchChats), chats...))))
	if slices.Equal(union, t.LaunchChats) && (t.LaunchResolved || !resolved) {
		return nil
	}
	if len(union) > 0 {
		t.LaunchChats = union
	}
	t.LaunchResolved = t.LaunchResolved || resolved
	if err := s.persist(ctx, &next); err != nil {
		return err
	}
	s.publish(&next)
	return nil
}
