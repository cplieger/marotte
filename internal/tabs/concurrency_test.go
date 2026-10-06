package tabs

import (
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestOpen_ConcurrentOpensSurviveInMemoryAndOnDisk pins the lock ordering: with writeMu taken
// AFTER the clone, two opens clone one state and the second persists over the first. Every
// returned subject must be in memory AND on disk, and the version must equal the opens.
// Probabilistic, so generous; red-checked against the broken order.
func TestOpen_ConcurrentOpensSurviveInMemoryAndOnDisk(t *testing.T) {
	const opens = 8
	for round := range 4 {
		s, dir := newTestStore(t)
		got := make([]marotte.TabSubject, opens)
		errs := make([]error, opens)

		var wg sync.WaitGroup
		for i := range opens {
			wg.Go(func() {
				sub, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i)))
				got[i], errs[i] = sub, err
			})
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: Open(chat c-%d): %v", round, i, err)
			}
		}
		tabs, version := s.List()
		inMemory := idsOf(tabs)
		onFile := idsOf(onDisk(t, dir).Tabs)
		for i, sub := range got {
			if !slices.Contains(inMemory, sub.ID) {
				t.Errorf("round %d: Open(chat c-%d) returned success for %q, which is not in List(): a committed write was lost in memory",
					round, i, sub.ID)
			}
			if !slices.Contains(onFile, sub.ID) {
				t.Errorf("round %d: Open(chat c-%d) returned success for %q, which is not in tabs.json: a committed write was lost on disk",
					round, i, sub.ID)
			}
		}
		if version != opens {
			t.Errorf("round %d: %d concurrent opens left version %d, want %d: every commit takes its own version",
				round, opens, version, opens)
		}
		if len(tabs) != opens {
			t.Errorf("round %d: List() = %d tabs, want %d", round, len(tabs), opens)
		}
	}
}

// TestOpen_AgainstReorderLosesNothing races an insert against a replace: a Reorder over a stale
// list is REFUSED (correct), but a tab must never disappear.
func TestOpen_AgainstReorderLosesNothing(t *testing.T) {
	const opens = 12
	s, dir := newTestStore(t)
	opened := make([]marotte.TabSubject, opens)

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range opens {
			sub, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("Open(chat c-%d): %v", i, err)
				return
			}
			opened[i] = sub
		}
	})
	wg.Go(func() {
		for range opens * 3 {
			tabs, _ := s.List()
			if len(tabs) < 2 {
				continue
			}
			ids := idsOf(tabs)
			slices.Reverse(ids)
			if _, err := s.Reorder(t.Context(), ids); err != nil && !errors.Is(err, ErrOrderMismatch) {
				t.Errorf("Reorder(%d ids) = %v, want either success or ErrOrderMismatch", len(ids), err)
			}
		}
	})
	wg.Wait()

	tabs, version := s.List()
	if len(tabs) != opens {
		t.Errorf("List() = %d tabs after %d opens against a reordering reader, want %d", len(tabs), opens, opens)
	}
	inMemory := idsOf(tabs)
	onFile := idsOf(onDisk(t, dir).Tabs)
	for i, sub := range opened {
		if sub.ID == "" {
			continue // its Open already reported a failure above
		}
		if !slices.Contains(inMemory, sub.ID) {
			t.Errorf("chat c-%d (%q) is not in List(): a reorder was applied to a set that no longer existed", i, sub.ID)
		}
		if !slices.Contains(onFile, sub.ID) {
			t.Errorf("chat c-%d (%q) is not in tabs.json", i, sub.ID)
		}
	}
	if len(onFile) != len(inMemory) {
		t.Errorf("tabs.json holds %d tabs and memory holds %d; the last publish and the last write disagree", len(onFile), len(inMemory))
	}
	if version < opens {
		t.Errorf("version = %d after %d opens, want at least %d", version, opens, opens)
	}
}

// TestList_PairsTheSetWithItsOwnVersion pins that version N always means N tabs. Readers use
// t.Errorf, never t.Fatal, off the test goroutine.
func TestList_PairsTheSetWithItsOwnVersion(t *testing.T) {
	const (
		opens   = 40
		readers = 4
	)
	s, _ := newTestStore(t)
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for i := range opens {
			if _, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i))); err != nil {
				t.Errorf("Open(chat c-%d): %v", i, err)
				return
			}
		}
	})
	for range readers {
		wg.Go(func() {
			reads := 0
			for {
				select {
				case <-done:
					if reads == 0 {
						t.Error("a reader observed nothing; the race window closed before it started")
					}
					return
				default:
				}
				tabs, version := s.List()
				reads++
				if uint64(len(tabs)) != version {
					t.Errorf("List() = %d tabs at version %d, want them to agree: this store's Nth mutation is its Nth tab, so a mismatch is a set paired with a version it does not describe",
						len(tabs), version)
					return
				}
			}
		})
	}
	wg.Wait()

	if tabs, version := s.List(); len(tabs) != opens || version != opens {
		t.Errorf("List() = %d tabs at version %d, want %d and %d", len(tabs), version, opens, opens)
	}
}
