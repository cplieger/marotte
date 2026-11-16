package command

// The History window as the expiry runner applies it: the setting read at the runner's start and after each change,
// re-trimming and re-expanding the list from what the tab store holds, with one tabs_changed frame per change.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

const warnWindowRetry = "tabs: the History window could not be applied; retrying"

// windowSetting is a History window setting the test changes between the runner's reads.
type windowSetting struct {
	d  time.Duration
	mu sync.Mutex
}

func (w *windowSetting) read(context.Context) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d
}

func (w *windowSetting) set(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.d = d
}

// newWindowedMembership is newTabbedMembership over a tab store opened with the window opened and a coordinator
// reading the setting through window; it also answers the tab store's directory.
func newWindowedMembership(t *testing.T, chats chatStore, opened time.Duration, window func(context.Context) time.Duration) (*Membership, *tabs.Store, *tabBus, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := tabs.NewStore(dir, opened)
	if err != nil {
		t.Fatalf("Setup: open tab store: %v", err)
	}
	bus := &tabBus{}
	mem := newMembership(&membershipDeps{
		Teardown: &recordingTeardown{}, Chats: withKeep(chats), Tabs: st, Bus: bus, Runs: &fakeRunOwner{}, HistoryWindow: window,
	})
	return mem, st, bus, dir
}

// failingWindow is the real tab store with its first fails SetRecentWindow writes refused.
type failingWindow struct {
	*tabs.Store
	fails int
	mu    sync.Mutex
}

var errWindowWrite = errors.New("simulated tabs.json write failure")

func (f *failingWindow) SetRecentWindow(ctx context.Context, window time.Duration) (uint64, error) {
	f.mu.Lock()
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	f.mu.Unlock()
	if fail {
		return 0, errWindowWrite
	}
	return f.Store.SetRecentWindow(ctx, window)
}

func TestRunRecentExpiry_TheDefaultWindowIs24Hours(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		configDir := t.TempDir()
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus, _ := newWindowedMembership(t, store, 6*time.Hour, func(ctx context.Context) time.Duration {
			return settings.HistoryWindow(ctx, configDir)
		})
		seedNamed(t, store, "c-a", "Alpha")
		stop := runExpiry(t, mem)
		synctest.Wait()
		closeListed(t, mem, "c-a")
		synctest.Wait()
		mark := len(bus.frames(t))

		time.Sleep(24*time.Hour - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Errorf("the runner emitted %d frames before the close was 24h old, want 0", n)
		}
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-a"}) {
			t.Errorf("Recent() 1ms before 24h = %v, want [c-a]", got)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the runner emitted %d frames at 24h, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || len(refs) != 0 {
			t.Errorf("the 24h frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		stop()
	})
}

func TestRunRecentExpiry_ANarrowerWindowAppliesLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setting := &windowSetting{d: 24 * time.Hour}
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus, _ := newWindowedMembership(t, store, 24*time.Hour, setting.read)
		seedNamed(t, store, "c-a", "Alpha")
		seedNamed(t, store, "c-b", "Beta")
		stop := runExpiry(t, mem)
		synctest.Wait()
		closeListed(t, mem, "c-a")
		time.Sleep(7 * time.Hour)
		closeListed(t, mem, "c-b")
		time.Sleep(time.Hour)
		synctest.Wait()
		mark := len(bus.frames(t))

		setting.set(6 * time.Hour)
		mem.HistoryWindowChanged()
		synctest.Wait()

		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("narrowing to 6h emitted %d frames, want 1", len(frames))
		}
		_, version := st.List()
		if refs, ok := frameRecentRefs(&frames[0]); !ok || !slices.Equal(refs, []string{"c-b"}) || frames[0].Version != version {
			t.Errorf("the narrowing frame = recent %v (carried %v) at v%d, want [c-b] at the store's v%d", refs, ok, frames[0].Version, version)
		}
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-b"}) {
			t.Errorf("Recent() under 6h = %v, want [c-b]", got)
		}

		time.Sleep(5*time.Hour - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 1 {
			t.Fatalf("frames before c-b's 6h = %d, want still 1", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames = bus.frames(t)[mark:]
		if len(frames) != 2 {
			t.Fatalf("frames at c-b's 6h = %d, want 2", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[1]); !ok || len(refs) != 0 {
			t.Errorf("c-b's expiry frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		stop()
	})
}

func TestRunRecentExpiry_AWiderWindowBringsBackWhatTheStoreHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setting := &windowSetting{d: 6 * time.Hour}
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus, _ := newWindowedMembership(t, store, 6*time.Hour, setting.read)
		seedNamed(t, store, "c-gone", "Gone")
		seedNamed(t, store, "c-a", "Alpha")
		stop := runExpiry(t, mem)
		synctest.Wait()
		closeListed(t, mem, "c-gone")
		closeListed(t, mem, "c-a")
		synctest.Wait()
		mark := len(bus.frames(t))

		time.Sleep(6 * time.Hour)
		synctest.Wait()
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the runner emitted %d frames at 6h, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || len(refs) != 0 {
			t.Fatalf("the 6h frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		time.Sleep(2 * time.Hour)
		if err := mem.DeleteChatAndCloseTabs(t.Context(), "c-gone", "op-del", nil); err != nil {
			t.Fatalf("DeleteChatAndCloseTabs(c-gone) = %v", err)
		}
		synctest.Wait()
		mark = len(bus.frames(t))

		setting.set(24 * time.Hour)
		mem.HistoryWindowChanged()
		synctest.Wait()

		frames = bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("widening to 24h emitted %d frames, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || !slices.Equal(refs, []string{"c-a"}) {
			t.Errorf("the widening frame recent = %v (carried %v), want [c-a] back and c-gone not", refs, ok)
		}
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-a"}) {
			t.Errorf("Recent() under 24h = %v, want [c-a]", got)
		}

		time.Sleep(16*time.Hour - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 1 {
			t.Fatalf("frames before c-a's 24h = %d, want still 1", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames = bus.frames(t)[mark:]
		if len(frames) != 2 {
			t.Fatalf("frames at c-a's 24h = %d, want 2", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[1]); !ok || len(refs) != 0 {
			t.Errorf("c-a's 24h frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		stop()
	})
}

func TestRunRecentExpiry_AppliesTheSettingAtStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setting := &windowSetting{d: 6 * time.Hour}
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus, _ := newWindowedMembership(t, store, 24*time.Hour, setting.read)
		seedNamed(t, store, "c-a", "Alpha")
		closeListed(t, mem, "c-a")
		time.Sleep(8 * time.Hour)
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-a"}) {
			t.Fatalf("Setup: Recent() under the store's 24h = %v, want [c-a]", got)
		}
		mark := len(bus.frames(t))

		stop := runExpiry(t, mem)
		synctest.Wait()

		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the runner's start emitted %d frames, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || len(refs) != 0 {
			t.Errorf("the start's frame recent = %v (carried %v), want the 8h-old close hidden under 6h", refs, ok)
		}
		stop()
	})
}

func TestRunRecentExpiry_RetriesAFailedWindowAfterAMinute(t *testing.T) {
	logs := capture.Default(t)
	synctest.Test(t, func(t *testing.T) {
		setting := &windowSetting{d: 6 * time.Hour}
		store := testsupport.NewInMemoryChatStore()
		st, err := tabs.NewStore(t.TempDir(), 24*time.Hour)
		if err != nil {
			t.Fatalf("Setup: open tab store: %v", err)
		}
		flaky := &failingWindow{Store: st, fails: 1}
		bus := &tabBus{}
		mem := newMembership(&membershipDeps{
			Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: flaky, Bus: bus, Runs: &fakeRunOwner{}, HistoryWindow: setting.read,
		})
		seedNamed(t, store, "c-a", "Alpha")
		closeListed(t, mem, "c-a")
		time.Sleep(8 * time.Hour)
		mark := len(bus.frames(t))

		stop := runExpiry(t, mem)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Errorf("a failed window write emitted %d frames, want 0", n)
		}
		if n := logs.CountExact(warnWindowRetry); n != 1 {
			t.Errorf("Warn %q logged %d times after the failure, want 1", warnWindowRetry, n)
		}
		time.Sleep(time.Minute - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Errorf("the runner emitted %d frames before its retry, want 0", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the retry emitted %d frames, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || len(refs) != 0 {
			t.Errorf("the retry's frame recent = %v (carried %v), want the 8h-old close hidden", refs, ok)
		}
		if n := logs.CountExact(warnWindowRetry); n != 1 {
			t.Errorf("Warn %q logged %d times after the retry, want 1", warnWindowRetry, n)
		}
		stop()
	})
}

func TestForgetRecent_AHiddenEntryLeavesTheFileAndAnnouncesItsVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setting := &windowSetting{d: 6 * time.Hour}
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus, dir := newWindowedMembership(t, store, 6*time.Hour, setting.read)
		seedNamed(t, store, "c-a", "Alpha")
		seedNamed(t, store, "c-b", "Beta")
		stop := runExpiry(t, mem)
		synctest.Wait()
		closeListed(t, mem, "c-a")
		time.Sleep(time.Hour)
		closeListed(t, mem, "c-b")
		time.Sleep(5 * time.Hour)
		synctest.Wait()
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-b"}) {
			t.Fatalf("Setup: Recent() at c-a's 6h = %v, want [c-b]", got)
		}
		_, before := st.List()
		mark := len(bus.frames(t))

		if err := mem.DeleteChatAndCloseTabs(t.Context(), "c-a", "op-del", nil); err != nil {
			t.Fatalf("DeleteChatAndCloseTabs(c-a) = %v", err)
		}
		synctest.Wait()

		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("deleting a hidden listed chat emitted %d frames, want 1", len(frames))
		}
		_, version := st.List()
		if frames[0].Recent != nil || frames[0].Version != version || version != before+1 {
			t.Errorf("the delete's frame = recent %+v at v%d, want no list at the store's new v%d (was v%d)",
				frames[0].Recent, frames[0].Version, version, before)
		}
		held, err := tabs.NewStore(dir, tabs.RecentHold)
		if err != nil {
			t.Fatalf("NewStore(%v): %v", tabs.RecentHold, err)
		}
		if got := refsOf(held.Recent()); !slices.Equal(got, []string{"c-b"}) {
			t.Errorf("tabs.json read under the whole hold = %v, want [c-b]: the deleted chat's entry left the file", got)
		}

		mark = len(bus.frames(t))
		setting.set(24 * time.Hour)
		mem.HistoryWindowChanged()
		synctest.Wait()
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-b"}) {
			t.Errorf("Recent() widened to 24h = %v, want [c-b]: a deleted chat never comes back", got)
		}
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Errorf("widening a list it leaves unchanged emitted %d frames, want 0", n)
		}
		stop()
	})
}
