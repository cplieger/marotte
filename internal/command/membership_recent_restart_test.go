package command

// The Recently closed list across a restart: a new tab store and coordinator on the same directory and chat store
// see the list the previous boot wrote, under the same window and cut.

import (
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// bootMembership is one boot's tab store and coordinator over dir and store, as newTabbedMembership builds them.
func bootMembership(t *testing.T, dir string, store *testsupport.InMemoryChatStore) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: open tab store: %v", err)
	}
	bus := &tabBus{}
	mem := newMembership(&membershipDeps{Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: st, Bus: bus, Runs: &fakeRunOwner{}})
	return mem, st, bus
}

func TestRecent_TheWindowRunsOnAcrossARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store := testsupport.NewInMemoryChatStore()
		mem1, st1, _ := bootMembership(t, dir, store)
		seedNamed(t, store, "c-a", "Alpha")
		seedNamed(t, store, "c-b", "Beta")
		closeListed(t, mem1, "c-a")
		time.Sleep(time.Hour)
		closeListed(t, mem1, "c-b")
		stop1 := runExpiry(t, mem1)
		synctest.Wait()
		stop1()
		before := st1.Recent()
		if !slices.Equal(refsOf(before), []string{"c-b", "c-a"}) {
			t.Fatalf("Setup: Recent() before the restart = %v, want [c-b c-a]", refsOf(before))
		}

		time.Sleep(4 * time.Hour)
		mem, st, bus := bootMembership(t, dir, store)
		if got := st.Recent(); !slices.Equal(got, before) {
			t.Fatalf("Recent() after the restart = %+v, want the previous boot's %+v", got, before)
		}
		stop := runExpiry(t, mem)
		synctest.Wait()
		mark := len(bus.frames(t))

		time.Sleep(time.Hour - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Fatalf("the runner emitted %d frames before c-a's expiry, want 0", n)
		}
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-b", "c-a"}) {
			t.Errorf("Recent() before c-a's expiry = %v, want [c-b c-a]", got)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the runner emitted %d frames at c-a's expiry, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || !slices.Equal(refs, []string{"c-b"}) {
			t.Errorf("c-a's expiry frame recent = %v (carried %v), want [c-b]", refs, ok)
		}

		time.Sleep(time.Hour - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 1 {
			t.Fatalf("frames before c-b's expiry = %d, want still 1", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames = bus.frames(t)[mark:]
		if len(frames) != 2 {
			t.Fatalf("frames at c-b's expiry = %d, want 2", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[1]); !ok || len(refs) != 0 {
			t.Errorf("c-b's expiry frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		if got := st.Recent(); len(got) != 0 {
			t.Errorf("Recent() after both expiries = %+v, want empty", got)
		}
		stop()
	})
}

func TestRecent_ARestartPastTheWindowLoadsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store := testsupport.NewInMemoryChatStore()
		mem1, st1, _ := bootMembership(t, dir, store)
		seedNamed(t, store, "c-a", "Alpha")
		closeListed(t, mem1, "c-a")
		if got := refsOf(st1.Recent()); !slices.Equal(got, []string{"c-a"}) {
			t.Fatalf("Setup: Recent() before the restart = %v, want [c-a]", got)
		}

		time.Sleep(tabs.RecentWindow)
		_, st, _ := bootMembership(t, dir, store)
		if got := st.Recent(); len(got) != 0 {
			t.Errorf("Recent() after a restart past the window = %+v, want empty", got)
		}
	})
}

func TestRecent_TheTenEntryCutHoldsAcrossARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store := testsupport.NewInMemoryChatStore()
		mem1, st1, _ := bootMembership(t, dir, store)
		for i := range 12 {
			ref := marotte.ChatID(fmt.Sprintf("c-%d", i))
			seedNamed(t, store, ref, string(ref))
			closeListed(t, mem1, ref)
			time.Sleep(time.Second)
		}
		want := newestFirst(11, 2)
		if got := refsOf(st1.Recent()); !slices.Equal(got, want) {
			t.Fatalf("Setup: Recent() before the restart = %v, want %v", got, want)
		}

		mem, st, _ := bootMembership(t, dir, store)
		if got := refsOf(st.Recent()); !slices.Equal(got, want) {
			t.Errorf("Recent() after the restart = %v, want %v", got, want)
		}
		seedNamed(t, store, "c-12", "c-12")
		closeListed(t, mem, "c-12")
		if got, want := refsOf(st.Recent()), newestFirst(12, 3); !slices.Equal(got, want) {
			t.Errorf("Recent() after a close on the new boot = %v, want %v", got, want)
		}
	})
}

// newestFirst is the refs c-hi down to c-lo.
func newestFirst(hi, lo int) []string {
	out := make([]string, 0, hi-lo+1)
	for i := hi; i >= lo; i-- {
		out = append(out, fmt.Sprintf("c-%d", i))
	}
	return out
}
