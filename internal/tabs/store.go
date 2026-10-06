package tabs

import (
	"context"
	"fmt"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
)

// state is the ordered set and the version it describes, travelling together because they
// are ONE fact.
type state struct {
	tabs    []marotte.TabSubject
	version uint64
}

// mutate is the ONE write path, holding the package doc's lock ordering. apply reports whether
// it CHANGED anything; false means no bump, no write, no publish. On error nothing is applied
// and the version is 0, a value no state carries. A failed persist needs no rollback: the
// clone was mutated, so the published state is untouched.
func (s *Store) mutate(ctx context.Context, apply func(st *state) (bool, error)) (uint64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	next := s.snapshot()
	changed, err := apply(&next)
	if err != nil {
		return 0, err
	}
	if !changed {
		return next.version, nil
	}
	next.version++
	if err := s.persist(ctx, &next); err != nil {
		return 0, err
	}
	s.publish(&next)
	return next.version, nil
}

// snapshot clones the state under stateMu, shallowly: marotte.TabSubject holds no reference
// type (a slice or map field would require a deep clone; see List).
func (s *Store) snapshot() state {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return state{tabs: slices.Clone(s.tabs), version: s.version}
}

// publish installs a mutated clone. Called only after the clone is durable, so
// what a reader sees is always what is on disk.
func (s *Store) publish(st *state) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.tabs, s.version = st.tabs, st.version
}

// Open adds a tab for spec, or returns the one already open for its (Kind, Ref). created is
// false then, and nothing is emitted, so the caller resolves from the response. The id is
// minted here; the position follows the client's insertSpec. A spec naming an unopened parent
// is PROMOTED to top level, so use the returned subject. Returns ErrBadKind, ErrBadRef or
// ErrTooMany; on any error nothing is applied and the version is 0.
func (s *Store) Open(ctx context.Context, spec marotte.OpenTab) (subject marotte.TabSubject, created bool, version uint64, err error) {
	err = checkSubject(spec.Kind, spec.Ref)
	if err != nil {
		return marotte.TabSubject{}, false, 0, err
	}
	version, err = s.mutate(ctx, func(st *state) (bool, error) {
		if i := indexOfSubject(st.tabs, spec.Kind, spec.Ref); i >= 0 {
			subject = st.tabs[i]
			return false, nil
		}
		if len(st.tabs) >= MaxOpenTabs {
			return false, fmt.Errorf("%w: %d open, limit %d", ErrTooMany, len(st.tabs), MaxOpenTabs)
		}
		subject = marotte.TabSubject{
			ID:     newID(),
			Kind:   spec.Kind,
			Ref:    spec.Ref,
			Parent: spec.Parent,
			Owns:   spec.Owns,
		}
		st.tabs = insert(st.tabs, &subject)
		created = true
		return true, nil
	})
	if err != nil {
		return marotte.TabSubject{}, false, 0, err
	}
	return subject, created, version, nil
}

// Close removes the tab and its descendants as ONE mutation and returns everything removed.
// An id not open yields an empty slice and no error (two devices can close one tab), and
// bumps nothing. Its only error is a failed write.
func (s *Store) Close(ctx context.Context, id string) ([]marotte.TabSubject, uint64, error) {
	var closed []marotte.TabSubject
	version, err := s.mutate(ctx, func(st *state) (bool, error) {
		doomed := closure(st.tabs, id)
		if len(doomed) == 0 {
			return false, nil
		}
		closed = make([]marotte.TabSubject, 0, len(doomed))
		for _, t := range st.tabs {
			if _, gone := doomed[t.ID]; gone {
				closed = append(closed, t)
			}
		}
		st.tabs = slices.DeleteFunc(st.tabs, func(t marotte.TabSubject) bool {
			_, gone := doomed[t.ID]
			return gone
		})
		return true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return closed, version, nil
}

// Reorder replaces the order. ids must name every open tab EXACTLY ONCE, or ErrOrderMismatch
// and nothing is applied: that exact-set check is the whole precondition. No base-version
// precondition (it would discard a valid drag after an unrelated pin). An unchanged order
// bumps nothing. The pinned partition is the client's rendering rule, not applied here.
func (s *Store) Reorder(ctx context.Context, ids []string) (uint64, error) {
	return s.mutate(ctx, func(st *state) (bool, error) {
		if len(ids) != len(st.tabs) {
			return false, fmt.Errorf("%w: %d ids for %d open tabs", ErrOrderMismatch, len(ids), len(st.tabs))
		}
		open := make(map[string]marotte.TabSubject, len(st.tabs))
		for _, t := range st.tabs {
			open[t.ID] = t
		}
		next := make([]marotte.TabSubject, 0, len(ids))
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				return false, fmt.Errorf("%w: %q appears twice", ErrOrderMismatch, id)
			}
			t, isOpen := open[id]
			if !isOpen {
				return false, fmt.Errorf("%w: %q is not open", ErrOrderMismatch, id)
			}
			seen[id] = struct{}{}
			next = append(next, t)
		}
		if slices.Equal(st.tabs, next) {
			return false, nil
		}
		st.tabs = next
		return true, nil
	})
}

// SetPinned pins or unpins one tab, idempotent both ways. An id not open is not an error (a
// pin racing a close). Its only error is a failed write.
func (s *Store) SetPinned(ctx context.Context, id string, pinned bool) (uint64, error) {
	return s.mutate(ctx, func(st *state) (bool, error) {
		i := indexOfID(st.tabs, id)
		if i < 0 || st.tabs[i].Pinned == pinned {
			return false, nil
		}
		st.tabs[i].Pinned = pinned
		return true, nil
	})
}

// Reparent hangs the tab under parent and moves its row behind parent's existing
// children by insert's rule. The row travels alone: its own descendants keep
// their positions and their Parent, because the one caller reparents leaves.
// Idempotent when Parent already equals parent (no version bump).
//
// Returns ErrNotOpen when id is not open and ErrCycle when parent is id or one
// of its descendants; a parent that is not open is promoted to top level like
// Open. Nothing is applied on error.
func (s *Store) Reparent(ctx context.Context, id, parent string) (uint64, error) {
	return s.mutate(ctx, func(st *state) (bool, error) {
		i := indexOfID(st.tabs, id)
		if i < 0 {
			return false, fmt.Errorf("%w: %q", ErrNotOpen, id)
		}
		if st.tabs[i].Parent == parent {
			return false, nil
		}
		if _, cycle := closure(st.tabs, id)[parent]; cycle {
			return false, fmt.Errorf("%w: %q is %q or descends from it", ErrCycle, parent, id)
		}
		sub := st.tabs[i]
		sub.Parent = parent
		st.tabs = insert(slices.Delete(st.tabs, i, i+1), &sub)
		return true, nil
	})
}

// List returns the set in order plus the version it reflects, from ONE critical section. The
// result is a shallow copy, sufficient because TabSubject holds no reference type.
func (s *Store) List() (tabs []marotte.TabSubject, version uint64) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return slices.Clone(s.tabs), s.version
}

// Subtree returns the tab with this id plus every descendant, in order, or nil when id is not
// open: the same closure Close removes, so a caller predicting a close cannot disagree with it.
func (s *Store) Subtree(id string) []marotte.TabSubject {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	doomed := closure(s.tabs, id)
	if len(doomed) == 0 {
		return nil
	}
	out := make([]marotte.TabSubject, 0, len(doomed))
	for _, t := range s.tabs {
		if _, gone := doomed[t.ID]; gone {
			out = append(out, t)
		}
	}
	return out
}

// Prune is LOAD-TIME crash recovery, not the live integrity mechanism (the membership
// coordinator's ordering is). It drops tabs whose subject no longer resolves (nil exists
// resolves everything) and promotes tabs whose parent is absent, in one mutation.
func (s *Store) Prune(ctx context.Context, exists func(marotte.TabSubject) bool) ([]marotte.TabSubject, uint64, error) {
	var dropped []marotte.TabSubject
	version, err := s.mutate(ctx, func(st *state) (bool, error) {
		kept, gone := partitionByExistence(st.tabs, exists)
		promoted := promoteOrphans(kept)
		if len(gone) == 0 && promoted == 0 {
			return false, nil
		}
		dropped = gone
		st.tabs = kept
		return true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return dropped, version, nil
}

// partitionByExistence splits tabs into the ones exists still resolves and the
// ones it does not. A nil exists resolves everything.
func partitionByExistence(tabs []marotte.TabSubject, exists func(marotte.TabSubject) bool) (kept, gone []marotte.TabSubject) {
	if exists == nil {
		return tabs, nil
	}
	kept = make([]marotte.TabSubject, 0, len(tabs))
	for _, t := range tabs {
		if !exists(t) {
			gone = append(gone, t)
			continue
		}
		kept = append(kept, t)
	}
	return kept, gone
}

// promoteOrphans clears the Parent of every tab whose parent is not in the set and reports
// how many. Not transitive: a tab whose own parent survives keeps it.
func promoteOrphans(tabs []marotte.TabSubject) int {
	open := make(map[string]struct{}, len(tabs))
	for _, t := range tabs {
		open[t.ID] = struct{}{}
	}
	promoted := 0
	for i := range tabs {
		if tabs[i].Parent == "" {
			continue
		}
		if _, ok := open[tabs[i].Parent]; !ok {
			tabs[i].Parent = ""
			promoted++
		}
	}
	return promoted
}

// insert places sub at its canonical position, the client's insertSpec rule exactly: a
// top-level tab at the end, a child after the CONTIGUOUS run of its parent's children (a
// reorder may separate them). A parent that is not open promotes sub to top level.
func insert(tabs []marotte.TabSubject, sub *marotte.TabSubject) []marotte.TabSubject {
	at := -1
	if sub.Parent != "" {
		at = indexOfID(tabs, sub.Parent)
	}
	if at < 0 {
		sub.Parent = ""
		return append(tabs, *sub)
	}
	at++
	for at < len(tabs) && tabs[at].Parent == sub.Parent {
		at++
	}
	return slices.Insert(tabs, at, *sub)
}

// closure returns id plus every descendant, as a set, or nil when id is not open. Iterative
// and marking, so a hand-edited parent cycle cannot exhaust the stack. The child index is
// rebuilt per call: a persistent one could desync from the parent pointers.
func closure(tabs []marotte.TabSubject, id string) map[string]struct{} {
	if indexOfID(tabs, id) < 0 {
		return nil
	}
	children := make(map[string][]string, len(tabs))
	for _, t := range tabs {
		if t.Parent != "" {
			children[t.Parent] = append(children[t.Parent], t.ID)
		}
	}
	out := map[string]struct{}{id: {}}
	pending := []string{id}
	for len(pending) > 0 {
		cur := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, kid := range children[cur] {
			if _, seen := out[kid]; seen {
				continue
			}
			out[kid] = struct{}{}
			pending = append(pending, kid)
		}
	}
	return out
}

// indexOfID returns the position of the tab with this id, or -1.
func indexOfID(tabs []marotte.TabSubject, id string) int {
	return slices.IndexFunc(tabs, func(t marotte.TabSubject) bool { return t.ID == id })
}

// indexOfSubject returns the position of the tab open for this (Kind, Ref), or -1: the
// uniqueness rule that makes Open idempotent.
func indexOfSubject(tabs []marotte.TabSubject, kind marotte.TabKind, ref string) int {
	return slices.IndexFunc(tabs, func(t marotte.TabSubject) bool {
		return t.Kind == kind && t.Ref == ref
	})
}
