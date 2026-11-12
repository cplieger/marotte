package command

// The Recently closed list as the coordinator carries it: who is listed, the names read from the records, and the
// tabs_changed frames that announce the list, the expiry runner's included.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

const warnExpiryRetry = "tabs: an expired recently closed entry could not be removed; retrying"

// seedNamed puts a tabless chat named name in the store.
func seedNamed(t *testing.T, store *testsupport.InMemoryChatStore, id marotte.ChatID, name string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = name
		return true
	}); err != nil {
		t.Fatalf("Setup: seed %q: %v", id, err)
	}
}

func openChatTab(t *testing.T, mem *Membership, ref marotte.ChatID, parentTab string) marotte.TabSubject {
	t.Helper()
	opened, err := mem.OpenTab(t.Context(),
		marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(ref), Parent: parentTab}, "op-open-"+string(ref))
	if err != nil {
		t.Fatalf("Setup: open %q: %v", ref, err)
	}
	return opened.Subject
}

// closeListed opens a tab for the seeded chat ref and closes it as a person's close, leaving ref listed.
func closeListed(t *testing.T, mem *Membership, ref marotte.ChatID) {
	t.Helper()
	tab := openChatTab(t, mem, ref, "")
	if _, _, err := mem.closeTab(t.Context(), tab.ID, "op-close-"+string(ref), true, nil); err != nil {
		t.Fatalf("Setup: close %q: %v", ref, err)
	}
}

func refsOf(list []marotte.RecentClose) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Ref)
	}
	return out
}

// frameRecentRefs is the list a frame carries, and whether it carries one at all.
func frameRecentRefs(p *marotte.TabsChangedPayload) ([]string, bool) {
	if p.Recent == nil {
		return nil, false
	}
	return refsOf(p.Recent.Items), true
}

// unreadableNames answers Get as unreadable for the listed chats; Lookup, which the tab holds use, still reads them.
type unreadableNames struct {
	*testsupport.InMemoryChatStore
	unread map[marotte.ChatID]bool
}

func (u *unreadableNames) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	if u.unread[id] {
		return nil, false
	}
	return u.InMemoryChatStore.Get(ctx, id)
}

// failingExpiry is the real tab store with its first fails ExpireRecent writes refused.
type failingExpiry struct {
	*tabs.Store
	fails int
	mu    sync.Mutex
}

var errExpiryWrite = errors.New("simulated tabs.json write failure")

func (f *failingExpiry) ExpireRecent(ctx context.Context, now time.Time) (uint64, error) {
	f.mu.Lock()
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	f.mu.Unlock()
	if fail {
		return 0, errExpiryWrite
	}
	return f.Store.ExpireRecent(ctx, now)
}

func TestCloseTab_TheFrameCarriesTheListWithEachClosedChatsName(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-root", "Root plan")
	seedNamed(t, store, "c-tangent", "Side question")
	root := openChatTab(t, mem, "c-root", "")
	tangent := openChatTab(t, mem, "c-tangent", root.ID)
	mark := len(bus.frames(t))

	_, version, err := mem.closeTab(t.Context(), root.ID, "op-close", true, nil)
	if err != nil {
		t.Fatalf("closeTab(root) = %v", err)
	}

	frames := bus.frames(t)[mark:]
	if len(frames) != 1 {
		t.Fatalf("the close emitted %d frames, want 1", len(frames))
	}
	f := frames[0]
	removed := slices.Sorted(slices.Values(f.RemovedIDs))
	if want := slices.Sorted(slices.Values([]string{root.ID, tangent.ID})); !slices.Equal(removed, want) {
		t.Errorf("removed_ids = %v, want %v", f.RemovedIDs, want)
	}
	if f.Recent == nil {
		t.Fatal("the close's frame carries no recent, want the list the close changed")
	}
	got := f.Recent.Items
	if len(got) != 2 || got[0].Ref != "c-root" || got[0].Name != "Root plan" ||
		got[1].Ref != "c-tangent" || got[1].Name != "Side question" {
		t.Errorf("frame recent = %+v, want c-root named Root plan then c-tangent named Side question", got)
	}
	if !slices.Equal(got, st.Recent()) {
		t.Errorf("frame recent = %+v, want the store's list %+v", got, st.Recent())
	}
	if f.Version != version {
		t.Errorf("frame version = %d, want the close's %d", f.Version, version)
	}
}

func TestCloseTab_AChatWhoseRecordCannotBeReadIsListedWithNoName(t *testing.T) {
	store := &unreadableNames{
		InMemoryChatStore: testsupport.NewInMemoryChatStore(),
		unread:            map[marotte.ChatID]bool{"c-lost": true},
	}
	mem, st, _ := newTabbedMembership(t, store)
	seedNamed(t, store.InMemoryChatStore, "c-lost", "Will not be read")

	closeListed(t, mem, "c-lost")

	recent := st.Recent()
	if len(recent) != 1 || recent[0].Ref != "c-lost" || recent[0].Name != "" {
		t.Errorf("Recent() = %+v, want c-lost listed with an empty name", recent)
	}
}

func TestDeleteChatAndCloseTabs_TheCascadeListsTheChatsUnderTheDeletedChatByName(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-root", "Root plan")
	seedNamed(t, store, "c-tangent", "Side question")
	root := openChatTab(t, mem, "c-root", "")
	openChatTab(t, mem, "c-tangent", root.ID)
	mark := len(bus.frames(t))

	if err := mem.DeleteChatAndCloseTabs(t.Context(), "c-root", "op-del", nil); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs(c-root) = %v", err)
	}

	want := []marotte.RecentClose{{Ref: "c-tangent", Name: "Side question"}}
	got := st.Recent()
	if len(got) != 1 || got[0].Ref != want[0].Ref || got[0].Name != want[0].Name {
		t.Errorf("Recent() after deleting c-root = %+v, want only c-tangent named Side question", got)
	}
	frames := bus.frames(t)[mark:]
	if len(frames) == 0 {
		t.Fatal("the delete emitted no tabs_changed")
	}
	if refs, ok := frameRecentRefs(&frames[len(frames)-1]); !ok || !slices.Equal(refs, []string{"c-tangent"}) {
		t.Errorf("the delete's frame recent = %v (carried %v), want [c-tangent]", refs, ok)
	}
}

func TestOpenTab_AReopenFrameCarriesTheRestoredPinAndTheList(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-a", "Alpha")
	tab := openChatTab(t, mem, "c-a", "")
	if _, err := mem.setPinned(t.Context(), tab.ID, true, ""); err != nil {
		t.Fatalf("Setup: pin: %v", err)
	}
	if _, _, err := mem.closeTab(t.Context(), tab.ID, "", true, nil); err != nil {
		t.Fatalf("Setup: close: %v", err)
	}
	mark := len(bus.frames(t))

	opened, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a"}, "op-reopen")
	if err != nil {
		t.Fatalf("OpenTab(c-a) = %v", err)
	}

	frames := bus.frames(t)[mark:]
	if len(frames) != 1 {
		t.Fatalf("the reopen emitted %d frames, want 1", len(frames))
	}
	f := frames[0]
	if f.Changed == nil || f.Changed.ID != opened.Subject.ID || !f.Changed.Pinned {
		t.Errorf("frame changed = %+v, want the reopened tab %q pinned", f.Changed, opened.Subject.ID)
	}
	if refs, ok := frameRecentRefs(&f); !ok || len(refs) != 0 {
		t.Errorf("frame recent = %v (carried %v), want an empty list carried", refs, ok)
	}
	if got := st.Recent(); len(got) != 0 {
		t.Errorf("Recent() after the reopen = %+v, want empty", got)
	}
}

func TestTabMutations_AReorderAPinAndAnAckCarryNoRecent(t *testing.T) {
	r := newAckRig(t)
	seedNamed(t, r.chats.InMemoryChatStore, "c-listed", "Listed")
	closeListed(t, r.mem, "c-listed")
	a := r.chatTab(t)
	b := createChat(t, r.mem, "op-b").Subject.ID
	r.chats.bound = 3
	mark := len(r.bus.frames(t))

	if _, err := r.mem.reorderTabs(t.Context(), []string{b, a}, "op-drag"); err != nil {
		t.Fatalf("reorderTabs = %v", err)
	}
	if _, err := r.mem.setPinned(t.Context(), a, true, "op-pin"); err != nil {
		t.Fatalf("setPinned = %v", err)
	}
	if _, _, err := r.mem.ackTab(t.Context(), a, 3, "op-ack"); err != nil {
		t.Fatalf("ackTab = %v", err)
	}

	frames := r.bus.frames(t)[mark:]
	if len(frames) != 3 {
		t.Fatalf("the reorder, pin and ack emitted %d frames, want 3", len(frames))
	}
	for _, f := range frames {
		if f.Recent != nil {
			t.Errorf("frame %q carries recent %+v, want none: the list did not change", f.OpID, f.Recent.Items)
		}
	}
	if got := refsOf(r.st.Recent()); !slices.Equal(got, []string{"c-listed"}) {
		t.Errorf("Recent() = %v, want [c-listed] untouched", got)
	}
}

func TestCloseTab_RetentionOffListsNoChat(t *testing.T) {
	cases := []struct {
		store func() (chatStore, *testsupport.InMemoryChatStore)
		name  string
	}{
		{name: "the deleted chats are not listed", store: func() (chatStore, *testsupport.InMemoryChatStore) {
			s := testsupport.NewInMemoryChatStore()
			return s, s
		}},
		{name: "a doomed chat whose delete failed stays unlisted", store: func() (chatStore, *testsupport.InMemoryChatStore) {
			s := &failingDeleteStore{InMemoryChatStore: testsupport.NewInMemoryChatStore()}
			return s, s.InMemoryChatStore
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, seed := tc.store()
			h := newEscalationHost(t, store, retentionOff)
			seedNamed(t, seed, "c-root", "Root plan")
			seedNamed(t, seed, "c-tangent", "Side question")
			root := openChatTab(t, h.mem, "c-root", "")
			openChatTab(t, h.mem, "c-tangent", root.ID)
			mark := len(h.bus.frames(t))

			if _, _, err := h.mem.closeTab(t.Context(), root.ID, "op-close", true, nil); err != nil {
				t.Fatalf("closeTab(root) = %v", err)
			}

			if got := h.st.Recent(); len(got) != 0 {
				t.Errorf("Recent() after a retention-off close = %+v, want empty", got)
			}
			for _, f := range h.bus.frames(t)[mark:] {
				if f.Recent != nil {
					t.Errorf("frame %q carries recent %+v, want none: the list did not change", f.OpID, f.Recent.Items)
				}
			}
		})
	}
}

func TestDeleteChatAndCloseTabs_TheCascadeListsDescendantsOnlyWithRetentionOn(t *testing.T) {
	cases := []struct {
		retention retentionRead
		name      string
		want      []string
	}{
		{name: "retention off lists no descendant", retention: retentionOff, want: []string{}},
		{name: "retention on lists the descendant", retention: retentionOn, want: []string{"c-tangent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			h := newEscalationHost(t, store, tc.retention)
			seedNamed(t, store, "c-root", "Root plan")
			seedNamed(t, store, "c-tangent", "Side question")
			root := openChatTab(t, h.mem, "c-root", "")
			openChatTab(t, h.mem, "c-tangent", root.ID)

			if err := h.mem.DeleteChatAndCloseTabs(t.Context(), "c-root", "op-del", nil); err != nil {
				t.Fatalf("DeleteChatAndCloseTabs(c-root) = %v", err)
			}

			if got := refsOf(h.st.Recent()); !slices.Equal(got, tc.want) {
				t.Errorf("Recent() after the delete = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeleteChatAndCloseTabs_ATablessListedChatLeavesTheListInOneFrame(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-kept", "Kept")
	seedNamed(t, store, "c-doomed", "Doomed")
	closeListed(t, mem, "c-kept")
	closeListed(t, mem, "c-doomed")
	_, before := st.List()
	mark := len(bus.frames(t))

	if err := mem.DeleteChatAndCloseTabs(t.Context(), "c-doomed", "op-del", nil); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs(c-doomed) = %v", err)
	}

	frames := bus.frames(t)[mark:]
	if len(frames) != 1 {
		t.Fatalf("deleting a tabless listed chat emitted %d frames, want 1", len(frames))
	}
	if refs, ok := frameRecentRefs(&frames[0]); !ok || !slices.Equal(refs, []string{"c-kept"}) {
		t.Errorf("frame recent = %v (carried %v), want [c-kept]", refs, ok)
	}
	if frames[0].Version != before+1 {
		t.Errorf("frame version = %d, want %d", frames[0].Version, before+1)
	}
	if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-kept"}) {
		t.Errorf("Recent() = %v, want [c-kept]", got)
	}
}

func TestRetentionClose_ForgetsThePurgedChat(t *testing.T) {
	t.Run("a tabless listed chat", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus, _ := newTornDownMembership(t, store)
		seedNamed(t, store, "c-purged", "Old")
		closeListed(t, mem, "c-purged")
		mark := len(bus.frames(t))
		if _, err := store.Delete(t.Context(), "c-purged"); err != nil {
			t.Fatalf("Setup: delete: %v", err)
		}

		mem.RetentionClose(t.Context(), "c-purged", nil)

		if got := st.Recent(); len(got) != 0 {
			t.Errorf("Recent() after the purge = %+v, want empty", got)
		}
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the purge emitted %d frames, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || len(refs) != 0 {
			t.Errorf("frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
	})
	t.Run("a chat whose tab the purge closes", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		mem, st, _, _ := newTornDownMembership(t, store)
		seedNamed(t, store, "c-raced", "Raced")
		openChatTab(t, mem, "c-raced", "")
		if _, err := store.Delete(t.Context(), "c-raced"); err != nil {
			t.Fatalf("Setup: delete: %v", err)
		}

		mem.RetentionClose(t.Context(), "c-raced", nil)

		if got := st.Recent(); len(got) != 0 {
			t.Errorf("Recent() after the purge = %+v, want the purged chat unlisted", got)
		}
	})
}

func TestTabMutations_PastTheWindowOnlyARealChangeDropsTheEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus := newTabbedMembership(t, store)
		seedNamed(t, store, "c-old", "Old")
		seedNamed(t, store, "c-a", "Alpha")
		closeListed(t, mem, "c-old")
		a := openChatTab(t, mem, "c-a", "")
		time.Sleep(tabs.RecentWindow)
		_, before := st.List()
		mark := len(bus.frames(t))

		if _, err := mem.setPinned(t.Context(), a.ID, false, "op-noop-pin"); err != nil {
			t.Fatalf("no-op setPinned = %v", err)
		}
		if _, err := mem.reorderTabs(t.Context(), []string{a.ID}, "op-noop-drag"); err != nil {
			t.Fatalf("no-op reorderTabs = %v", err)
		}
		if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a"}, "op-noop-open"); err != nil {
			t.Fatalf("no-op OpenTab = %v", err)
		}
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Errorf("no-op mutations past the window emitted %d frames, want 0", n)
		}
		if _, v := st.List(); v != before {
			t.Errorf("version after no-op mutations = %d, want %d", v, before)
		}
		if got := refsOf(st.Recent()); !slices.Equal(got, []string{"c-old"}) {
			t.Errorf("Recent() after no-op mutations = %v, want [c-old] kept", got)
		}

		version, err := mem.setPinned(t.Context(), a.ID, true, "op-pin")
		if err != nil {
			t.Fatalf("setPinned = %v", err)
		}
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the pin emitted %d frames, want 1", len(frames))
		}
		if refs, ok := frameRecentRefs(&frames[0]); !ok || len(refs) != 0 {
			t.Errorf("the pin's frame recent = %v (carried %v), want the emptied list carried", refs, ok)
		}
		if frames[0].Changed == nil || !frames[0].Changed.Pinned || frames[0].Version != version || version != before+1 {
			t.Errorf("the pin's frame = %+v at v%d, want the pinned subject at v%d", frames[0], version, before+1)
		}
	})
}

// runExpiry starts the coordinator's expiry runner and answers its stop, which waits for it to return.
func runExpiry(t *testing.T, mem *Membership) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mem.RunRecentExpiry(ctx)
	}()
	return func() {
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("RunRecentExpiry did not return once its context ended")
		}
	}
}

func TestRunRecentExpiry_EmitsAtEachEntrysExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		mem, st, bus := newTabbedMembership(t, store)
		seedNamed(t, store, "c-a", "Alpha")
		seedNamed(t, store, "c-b", "Beta")
		stop := runExpiry(t, mem)
		synctest.Wait()
		closeListed(t, mem, "c-a")
		time.Sleep(time.Hour)
		closeListed(t, mem, "c-b")
		synctest.Wait()
		mark := len(bus.frames(t))

		time.Sleep(tabs.RecentWindow - time.Hour - time.Millisecond)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Fatalf("the runner emitted %d frames before c-a's expiry, want 0", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		frames := bus.frames(t)[mark:]
		if len(frames) != 1 {
			t.Fatalf("the runner emitted %d frames at c-a's expiry, want 1", len(frames))
		}
		_, version := st.List()
		f := frames[0]
		if refs, ok := frameRecentRefs(&f); !ok || !slices.Equal(refs, []string{"c-b"}) {
			t.Errorf("expiry frame recent = %v (carried %v), want [c-b]", refs, ok)
		}
		if f.Version != version || f.Changed != nil || len(f.RemovedIDs) != 0 || len(f.Order) != 0 {
			t.Errorf("expiry frame = %+v, want the list alone at the store's v%d", f, version)
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
			t.Errorf("second expiry frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		if got := st.Recent(); len(got) != 0 {
			t.Errorf("Recent() after both expiries = %+v, want empty", got)
		}
		stop()
	})
}

func TestRunRecentExpiry_RetriesAFailedWriteAfterAMinute(t *testing.T) {
	logs := capture.Default(t)
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		st, err := tabs.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("Setup: open tab store: %v", err)
		}
		flaky := &failingExpiry{Store: st, fails: 1}
		bus := &tabBus{}
		mem := newMembership(&membershipDeps{Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: flaky, Bus: bus, Runs: &fakeRunOwner{}})
		seedNamed(t, store, "c-a", "Alpha")
		closeListed(t, mem, "c-a")
		mark := len(bus.frames(t))
		stop := runExpiry(t, mem)

		time.Sleep(tabs.RecentWindow)
		synctest.Wait()
		if n := len(bus.frames(t)) - mark; n != 0 {
			t.Errorf("a failed expiry write emitted %d frames, want 0", n)
		}
		if n := logs.CountExact(warnExpiryRetry); n != 1 {
			t.Errorf("Warn %q logged %d times after the failure, want 1", warnExpiryRetry, n)
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
			t.Errorf("the retry's frame recent = %v (carried %v), want an empty list carried", refs, ok)
		}
		if n := logs.CountExact(warnExpiryRetry); n != 1 {
			t.Errorf("Warn %q logged %d times after the retry, want 1", warnExpiryRetry, n)
		}
		stop()
	})
}
