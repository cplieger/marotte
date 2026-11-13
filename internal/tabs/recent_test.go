package tabs

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/marotte"
)

// testClock is the store's clock under the test's control; a store test runs on one goroutine.
type testClock struct{ at time.Time }

func (c *testClock) Now() time.Time { return c.at }

func (c *testClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// withClock gives s a clock starting at the wall clock's millisecond, so a document the store
// writes is still inside the window when a later NewStore loads it with the real clock.
func withClock(s *Store) *testClock {
	c := &testClock{at: time.UnixMilli(time.Now().UnixMilli())}
	s.now = c.Now
	return c
}

func recentRefs(list []marotte.RecentClose) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Ref)
	}
	return out
}

// mustClose closes id as a person's close with nothing unlisted.
func mustClose(t *testing.T, s *Store, id string, facts map[string]RecentFacts) {
	t.Helper()
	if _, _, _, err := s.Close(t.Context(), id, nil, facts); err != nil {
		t.Fatalf("Setup: Close(%q): %v", id, err)
	}
}

// closedChat opens chat ref and closes it at the clock's time, leaving it listed.
func closedChat(t *testing.T, s *Store, ref string) {
	t.Helper()
	tab := mustOpen(t, s, chatSpec(ref))
	mustClose(t, s, tab.ID, nil)
}

func captureLogs(t *testing.T) *strings.Builder {
	t.Helper()
	buf := &strings.Builder{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

func TestClose_ListsAChatWhoseLastTabClosed(t *testing.T) {
	s, dir := newTestStore(t)
	clock := withClock(s)
	tab := mustOpen(t, s, chatSpec("c-a"))
	if _, err := s.SetPinned(t.Context(), tab.ID, true); err != nil {
		t.Fatalf("Setup: SetPinned: %v", err)
	}

	_, owed, version, err := s.Close(t.Context(), tab.ID, nil, map[string]RecentFacts{"c-a": {Name: "Alpha"}})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := marotte.RecentClose{Ref: "c-a", Name: "Alpha", ClosedAt: clock.at.UnixMilli(), Pinned: true}
	recent := s.Recent()
	if len(recent) != 1 || recent[0] != want {
		t.Fatalf("Recent() after closing c-a = %+v, want [%+v]", recent, want)
	}
	if len(owed) != 1 || owed[0].ClosedAt != recent[0].ClosedAt {
		t.Errorf("owed %+v, want one entry whose closed_at is the listed entry's %d", owed, recent[0].ClosedAt)
	}
	if doc := onDisk(t, dir); len(doc.Recent) != 1 || doc.Recent[0] != want || doc.Version != version {
		t.Errorf("tabs.json recent %+v at version %d, want [%+v] in the close's own write (version %d)", doc.Recent, doc.Version, want, version)
	}
}

func TestClose_ANameNotHandedInIsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	withClock(s)
	tab := mustOpen(t, s, chatSpec("c-a"))
	mustClose(t, s, tab.ID, map[string]RecentFacts{"c-other": {Name: "Other"}})
	if recent := s.Recent(); len(recent) != 1 || recent[0].Name != "" || recent[0].Pinned {
		t.Errorf("Recent() = %+v, want c-a listed with no name and unpinned", recent)
	}
}

func TestClose_ListsOnlyAChatTabsOwnClose(t *testing.T) {
	cases := []struct {
		name string
		spec func(chat marotte.TabSubject) marotte.OpenTab
	}{
		{name: "run", spec: func(c marotte.TabSubject) marotte.OpenTab {
			return marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1", Parent: c.ID, Launch: &marotte.RunLaunch{Chats: []string{"c-a"}}}
		}},
		{name: "editor", spec: func(c marotte.TabSubject) marotte.OpenTab {
			return marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "/w/a.ts", Parent: c.ID}
		}},
		{name: "files", spec: func(marotte.TabSubject) marotte.OpenTab {
			return marotte.OpenTab{Kind: marotte.TabKindFiles, Ref: "/w"}
		}},
		{name: "git", spec: func(marotte.TabSubject) marotte.OpenTab { return marotte.OpenTab{Kind: marotte.TabKindGit} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			withClock(s)
			chat := mustOpen(t, s, chatSpec("c-a"))
			other := mustOpen(t, s, tc.spec(chat))
			_, _, version, err := s.Close(t.Context(), other.ID, nil, map[string]RecentFacts{"c-a": {Name: "Alpha"}})
			if err != nil || version == 0 {
				t.Fatalf("Close(%s) = (v%d, %v), want a committed close", tc.name, version, err)
			}
			if recent := s.Recent(); len(recent) != 0 {
				t.Errorf("Recent() after closing a %s tab = %+v, want nothing: c-a keeps its tab", tc.name, recent)
			}
		})
	}
}

func TestClose_AnUnlistedChatIsNotListed(t *testing.T) {
	s, _ := newTestStore(t)
	withClock(s)
	parent := mustOpen(t, s, chatSpec("c-parent"))
	mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-fork", Parent: parent.ID})

	if _, _, _, err := s.Close(t.Context(), parent.ID, []string{"c-parent"}, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-fork"}) {
		t.Errorf("Recent() = %v, want only c-fork: c-parent was unlisted", got)
	}
}

func TestClose_ASubtreeListsEachChatInTabOrderAtTheHead(t *testing.T) {
	s, _ := newTestStore(t)
	clock := withClock(s)
	closedChat(t, s, "c-older")
	clock.advance(time.Minute)
	parent := mustOpen(t, s, chatSpec("c-parent"))
	mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-fork", Parent: parent.ID})
	mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "/w/a.ts", Parent: parent.ID})

	mustClose(t, s, parent.ID, map[string]RecentFacts{"c-parent": {Name: "Parent"}, "c-fork": {Name: "Fork"}})
	recent := s.Recent()
	if got := recentRefs(recent); !slices.Equal(got, []string{"c-parent", "c-fork", "c-older"}) {
		t.Fatalf("Recent() = %v, want the subtree's chats in tab order ahead of the older close", got)
	}
	if recent[1].Name != "Fork" || recent[1].ClosedAt != clock.at.UnixMilli() {
		t.Errorf("the fork's entry = %+v, want its own name and the close's time", recent[1])
	}
}

func TestClose_ASecondCloseOfAChatReplacesItsEntry(t *testing.T) {
	s, _ := newTestStore(t)
	clock := withClock(s)
	closedChat(t, s, "c-a")
	clock.advance(time.Minute)
	closedChat(t, s, "c-b")
	clock.advance(time.Minute)
	closedChat(t, s, "c-a")

	recent := s.Recent()
	if got := recentRefs(recent); !slices.Equal(got, []string{"c-a", "c-b"}) {
		t.Fatalf("Recent() = %v, want one entry for c-a, at the head", got)
	}
	if recent[0].ClosedAt != clock.at.UnixMilli() {
		t.Errorf("c-a closed_at = %d, want the second close's %d", recent[0].ClosedAt, clock.at.UnixMilli())
	}
}

func TestClose_TheEleventhEntryDropsTheOldest(t *testing.T) {
	s, _ := newTestStore(t)
	clock := withClock(s)
	for i := range maxRecent + 1 {
		closedChat(t, s, "c-"+strconv.Itoa(i))
		clock.advance(time.Second)
	}
	got := recentRefs(s.Recent())
	if len(got) != maxRecent || got[0] != "c-10" || slices.Contains(got, "c-0") {
		t.Errorf("Recent() = %v, want %d entries headed by c-10 and without c-0", got, maxRecent)
	}
}

func TestRecent_TheBoundsAreTheProductsNumbers(t *testing.T) {
	if RecentWindow != 6*time.Hour || maxRecent != 10 {
		t.Errorf("RecentWindow, maxRecent = %v, %d, want 6h and 10", RecentWindow, maxRecent)
	}
}

func TestRecent_ExpiresOnlyInsideARealChange(t *testing.T) {
	type fixture struct {
		s     *Store
		clock *testClock
		open  marotte.TabSubject
		other marotte.TabSubject
	}
	setup := func(t *testing.T) fixture {
		s, _ := newTestStore(t)
		clock := withClock(s)
		open := mustOpen(t, s, chatSpec("c-open"))
		other := mustOpen(t, s, chatSpec("c-other"))
		closedChat(t, s, "c-a")
		clock.advance(RecentWindow)
		return fixture{s: s, clock: clock, open: open, other: other}
	}
	noops := []struct {
		name string
		do   func(t *testing.T, f fixture) (uint64, error)
	}{
		{name: "open of a tab already open", do: func(t *testing.T, f fixture) (uint64, error) {
			_, _, v, err := f.s.Open(t.Context(), chatSpec("c-open"))
			return v, err
		}},
		{name: "seen not past the stored one", do: func(t *testing.T, f fixture) (uint64, error) {
			return f.s.SetSeen(t.Context(), f.open.ID, 0)
		}},
		{name: "pin to the flag it has", do: func(t *testing.T, f fixture) (uint64, error) {
			return f.s.SetPinned(t.Context(), f.open.ID, false)
		}},
		{name: "reorder to the same order", do: func(t *testing.T, f fixture) (uint64, error) {
			return f.s.Reorder(t.Context(), []string{f.open.ID, f.other.ID})
		}},
	}
	for _, tc := range noops {
		t.Run("no-op "+strings.ReplaceAll(tc.name, " ", "-"), func(t *testing.T) {
			f := setup(t)
			_, before := f.s.List()
			v, err := tc.do(t, f)
			if err != nil || v != before {
				t.Errorf("%s = (v%d, %v), want version %d unchanged", tc.name, v, err, before)
			}
			if got := recentRefs(f.s.Recent()); !slices.Equal(got, []string{"c-a"}) {
				t.Errorf("Recent() after a no-op %s = %v, want c-a still listed: only a change or the runner drops it", tc.name, got)
			}
		})
	}

	changes := []struct {
		name string
		do   func(t *testing.T, f fixture) (uint64, error)
	}{
		{name: "open of a new tab", do: func(t *testing.T, f fixture) (uint64, error) {
			_, _, v, err := f.s.Open(t.Context(), chatSpec("c-new"))
			return v, err
		}},
		{name: "seen advanced", do: func(t *testing.T, f fixture) (uint64, error) { return f.s.SetSeen(t.Context(), f.open.ID, 3) }},
		{name: "pin", do: func(t *testing.T, f fixture) (uint64, error) { return f.s.SetPinned(t.Context(), f.open.ID, true) }},
		{name: "reorder", do: func(t *testing.T, f fixture) (uint64, error) {
			return f.s.Reorder(t.Context(), []string{f.other.ID, f.open.ID})
		}},
		{name: "reparent", do: func(t *testing.T, f fixture) (uint64, error) {
			return f.s.Reparent(t.Context(), f.other.ID, f.open.ID)
		}},
	}
	for _, tc := range changes {
		t.Run("change "+strings.ReplaceAll(tc.name, " ", "-"), func(t *testing.T) {
			f := setup(t)
			_, before := f.s.List()
			v, err := tc.do(t, f)
			if err != nil || v != before+1 {
				t.Errorf("%s = (v%d, %v), want version %d", tc.name, v, err, before+1)
			}
			if recent := f.s.Recent(); len(recent) != 0 {
				t.Errorf("Recent() after a %s past the window = %+v, want the expired entry dropped in the same version", tc.name, recent)
			}
		})
	}

	t.Run("a change one millisecond inside the window keeps the entry", func(t *testing.T) {
		f := setup(t)
		f.clock.advance(-time.Millisecond)
		if _, err := f.s.SetPinned(t.Context(), f.open.ID, true); err != nil {
			t.Fatalf("SetPinned: %v", err)
		}
		if got := recentRefs(f.s.Recent()); !slices.Equal(got, []string{"c-a"}) {
			t.Errorf("Recent() = %v, want c-a kept until closed_at + RecentWindow", got)
		}
	})
}

func TestRecent_RealChangesInsideTheWindowLeaveTheList(t *testing.T) {
	s, _ := newTestStore(t)
	withClock(s)
	a := mustOpen(t, s, chatSpec("c-open"))
	b := mustOpen(t, s, chatSpec("c-other"))
	closedChat(t, s, "c-a")
	want := s.Recent()

	steps := []func() error{
		func() error { _, err := s.Reorder(t.Context(), []string{b.ID, a.ID}); return err },
		func() error { _, err := s.SetPinned(t.Context(), a.ID, true); return err },
		func() error { _, err := s.SetSeen(t.Context(), a.ID, 7); return err },
		func() error { _, err := s.Reparent(t.Context(), b.ID, a.ID); return err },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if got := s.Recent(); !slices.Equal(got, want) {
		t.Errorf("Recent() after a reorder, pin, ack and reparent = %+v, want %+v unchanged", got, want)
	}
}

func TestExpireRecent_DropsWithAVersionOfItsOwn(t *testing.T) {
	s, dir := newTestStore(t)
	clock := withClock(s)
	closedChat(t, s, "c-old")
	clock.advance(time.Hour)
	closedChat(t, s, "c-new")
	_, before := s.List()

	clock.advance(RecentWindow - time.Hour)
	v, err := s.ExpireRecent(t.Context())
	if err != nil || v != before+1 {
		t.Fatalf("ExpireRecent at c-old's expiry = (v%d, %v), want version %d", v, err, before+1)
	}
	if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-new"}) {
		t.Errorf("Recent() = %v, want only c-new", got)
	}
	if doc := onDisk(t, dir); len(doc.Recent) != 1 || doc.Version != v {
		t.Errorf("tabs.json recent %+v at version %d, want c-new alone at %d", doc.Recent, doc.Version, v)
	}

	again, err := s.ExpireRecent(t.Context())
	if err != nil || again != v {
		t.Errorf("ExpireRecent with nothing expired = (v%d, %v), want version %d unchanged", again, err, v)
	}
}

func TestOpen_ConsumesTheEntryInTheSameVersion(t *testing.T) {
	s, dir := newTestStore(t)
	withClock(s)
	closedChat(t, s, "c-a")
	closedChat(t, s, "c-b")
	_, before := s.List()

	_, created, v, err := s.Open(t.Context(), chatSpec("c-a"))
	if err != nil || !created || v != before+1 {
		t.Fatalf("Open(c-a) = (created %v, v%d, %v), want a new tab at version %d", created, v, err, before+1)
	}
	if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-b"}) {
		t.Errorf("Recent() after reopening c-a = %v, want only c-b", got)
	}
	if doc := onDisk(t, dir); len(doc.Recent) != 1 || doc.Recent[0].Ref != "c-b" || doc.Version != v {
		t.Errorf("tabs.json recent %+v at version %d, want only c-b in the open's own write (version %d)", doc.Recent, doc.Version, v)
	}
}

func TestOpen_RestoresPinnedOnlyAtTopLevel(t *testing.T) {
	closedPinned := func(t *testing.T) *Store {
		t.Helper()
		s, _ := newTestStore(t)
		withClock(s)
		tab := mustOpen(t, s, chatSpec("c-a"))
		if _, err := s.SetPinned(t.Context(), tab.ID, true); err != nil {
			t.Fatalf("Setup: SetPinned: %v", err)
		}
		mustClose(t, s, tab.ID, nil)
		return s
	}

	t.Run("top level is pinned", func(t *testing.T) {
		s := closedPinned(t)
		if sub := mustOpen(t, s, chatSpec("c-a")); !sub.Pinned || sub.Parent != "" {
			t.Errorf("Open(c-a) = %+v, want a pinned top-level tab", sub)
		}
	})
	t.Run("under an open parent is a sub-tab, unpinned", func(t *testing.T) {
		s := closedPinned(t)
		parent := mustOpen(t, s, chatSpec("c-parent"))
		sub := mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a", Parent: parent.ID})
		if sub.Pinned || sub.Parent != parent.ID {
			t.Errorf("Open(c-a under c-parent) = %+v, want an unpinned sub-tab", sub)
		}
		if len(s.Recent()) != 0 {
			t.Errorf("Recent() = %+v, want the entry consumed", s.Recent())
		}
	})
	t.Run("under a parent not open is promoted and pinned", func(t *testing.T) {
		s := closedPinned(t)
		sub := mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-a", Parent: "t-gone"})
		if !sub.Pinned || sub.Parent != "" {
			t.Errorf("Open(c-a under a closed parent) = %+v, want a pinned top-level tab", sub)
		}
	})
	t.Run("an unpinned entry opens unpinned", func(t *testing.T) {
		s, _ := newTestStore(t)
		withClock(s)
		closedChat(t, s, "c-a")
		if sub := mustOpen(t, s, chatSpec("c-a")); sub.Pinned {
			t.Errorf("Open(c-a) = %+v, want unpinned", sub)
		}
	})
}

func TestOpen_LeavesTheListForOtherSubjects(t *testing.T) {
	cases := []struct {
		name string
		spec marotte.OpenTab
	}{
		{name: "an editor whose ref is a listed chat id", spec: marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "c-a"}},
		{name: "a run whose ref is a listed chat id", spec: marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "c-a"}},
		{name: "a chat that is not listed", spec: chatSpec("c-other")},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "-"), func(t *testing.T) {
			s, _ := newTestStore(t)
			withClock(s)
			closedChat(t, s, "c-a")
			mustOpen(t, s, tc.spec)
			if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-a"}) {
				t.Errorf("Recent() after opening %s = %v, want c-a still listed", tc.name, got)
			}
		})
	}
}

func TestOpen_RefusedAtTheLimitLeavesTheList(t *testing.T) {
	s, _ := newTestStore(t)
	withClock(s)
	closedChat(t, s, "c-a")
	for i := range MaxOpenTabs {
		mustOpen(t, s, chatSpec("c-fill-"+strconv.Itoa(i)))
	}
	_, before := s.List()

	_, _, v, err := s.Open(t.Context(), chatSpec("c-a"))
	if err == nil || v != 0 {
		t.Fatalf("Open(c-a) at %d tabs = (v%d, %v), want ErrTooMany", MaxOpenTabs, v, err)
	}
	if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-a"}) {
		t.Errorf("Recent() after a refused open = %v, want c-a still listed", got)
	}
	if _, after := s.List(); after != before {
		t.Errorf("version %d -> %d after a refused open, want no bump", before, after)
	}
}

func TestForget_DropsAnEntryAndBumpsOnlyWhenThereWasOne(t *testing.T) {
	s, dir := newTestStore(t)
	withClock(s)
	closedChat(t, s, "c-a")
	closedChat(t, s, "c-b")
	_, before := s.List()

	v, err := s.Forget(t.Context(), "c-a")
	if err != nil || v != before+1 {
		t.Fatalf("Forget(c-a) = (v%d, %v), want version %d", v, err, before+1)
	}
	if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-b"}) {
		t.Errorf("Recent() after Forget(c-a) = %v, want only c-b", got)
	}
	if doc := onDisk(t, dir); len(doc.Recent) != 1 || doc.Version != v {
		t.Errorf("tabs.json recent %+v at version %d, want only c-b at %d", doc.Recent, doc.Version, v)
	}

	again, err := s.Forget(t.Context(), "c-a")
	if err != nil || again != v {
		t.Errorf("Forget(c-a) again = (v%d, %v), want version %d unchanged", again, err, v)
	}
}

func TestSnapshot_ReturnsTabsRecentAndVersionTogether(t *testing.T) {
	s, _ := newTestStore(t)
	withClock(s)
	open := mustOpen(t, s, chatSpec("c-open"))
	closedChat(t, s, "c-a")

	tabs, recent, version := s.Snapshot()
	listed, listedVersion := s.List()
	if !slices.Equal(idsOf(tabs), []string{open.ID}) || !slices.Equal(idsOf(listed), idsOf(tabs)) {
		t.Errorf("Snapshot() tabs = %v, want [%s]", idsOf(tabs), open.ID)
	}
	if !slices.Equal(recentRefs(recent), []string{"c-a"}) || version != listedVersion {
		t.Errorf("Snapshot() = (recent %v, v%d), want ([c-a], v%d)", recentRefs(recent), version, listedVersion)
	}
	recent[0].Name = "forged"
	if s.Recent()[0].Name == "forged" {
		t.Error("a caller's edit of Snapshot's recent reached the store")
	}
}

// TestSnapshot_PairsTheListsWithTheirOwnVersion runs open-then-close cycles: at version v the
// store holds v%2 tabs and min(v/2, maxRecent) entries, so any other pairing is a torn read.
func TestSnapshot_PairsTheListsWithTheirOwnVersion(t *testing.T) {
	const (
		cycles  = 20
		readers = 4
	)
	s, _ := newTestStore(t)
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for i := range cycles {
			sub, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			if _, _, _, err := s.Close(t.Context(), sub.ID, nil, nil); err != nil {
				t.Errorf("Close: %v", err)
				return
			}
		}
	})
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				tabs, recent, v := s.Snapshot()
				if uint64(len(tabs)) != v%2 || uint64(len(recent)) != min(v/2, maxRecent) {
					t.Errorf("Snapshot() = %d tabs, %d entries at version %d, want %d and %d",
						len(tabs), len(recent), v, v%2, min(v/2, maxRecent))
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestNewStore_RoundTripsTheRecentList(t *testing.T) {
	s, dir := newTestStore(t)
	clock := withClock(s)
	tab := mustOpen(t, s, chatSpec("c-a"))
	if _, err := s.SetPinned(t.Context(), tab.ID, true); err != nil {
		t.Fatalf("Setup: SetPinned: %v", err)
	}
	mustClose(t, s, tab.ID, map[string]RecentFacts{"c-a": {Name: "Alpha"}})
	clock.advance(time.Second)
	closedChat(t, s, "c-b")
	want := s.Recent()

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := reopened.Recent(); !slices.Equal(got, want) {
		t.Errorf("Recent() after a restart = %+v, want %+v", got, want)
	}
}

func TestNewStore_SanitizesTheRecentList(t *testing.T) {
	now := time.Now().UnixMilli()
	hour := time.Hour.Milliseconds()
	entry := func(ref string, closedAt int64) marotte.RecentClose {
		return marotte.RecentClose{Ref: ref, Name: "N " + ref, ClosedAt: closedAt}
	}
	const dropped = "tabs: invalid recently closed entry dropped on load"
	cases := []struct {
		name      string
		tabs      []marotte.TabSubject
		in        []marotte.RecentClose
		wantRefs  []string
		wantWarns int
	}{
		{name: "valid entries kept in order", in: []marotte.RecentClose{entry("c-a", now-hour), entry("c-b", now-2*hour)}, wantRefs: []string{"c-a", "c-b"}},
		{name: "invalid ref", in: []marotte.RecentClose{entry("../x", now), entry("", now), entry("c-ok", now)}, wantRefs: []string{"c-ok"}, wantWarns: 2},
		{name: "closed_at zero or below", in: []marotte.RecentClose{entry("c-a", 0), entry("c-b", -5), entry("c-ok", now)}, wantRefs: []string{"c-ok"}, wantWarns: 2},
		{name: "closed_at more than the window ahead", in: []marotte.RecentClose{entry("c-a", now+7*hour), entry("c-ok", now+hour)}, wantRefs: []string{"c-ok"}, wantWarns: 1},
		{name: "duplicates keep the newest", in: []marotte.RecentClose{entry("c-a", now-2*hour), entry("c-b", now-hour), entry("c-a", now-time.Minute.Milliseconds())}, wantRefs: []string{"c-a", "c-b"}},
		{name: "an entry with an open tab", tabs: []marotte.TabSubject{{ID: "t1", Kind: marotte.TabKindChat, Ref: "c-a"}}, in: []marotte.RecentClose{entry("c-a", now), entry("c-b", now)}, wantRefs: []string{"c-b"}},
		{name: "expired entries", in: []marotte.RecentClose{entry("c-a", now-7*hour), entry("c-b", now-hour)}, wantRefs: []string{"c-b"}},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "-"), func(t *testing.T) {
			logs := captureLogs(t)
			dir := t.TempDir()
			writeDoc(t, dir, file{Tabs: tc.tabs, Recent: tc.in, Version: 3})
			s, err := NewStore(dir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			if got := recentRefs(s.Recent()); !slices.Equal(got, tc.wantRefs) {
				t.Errorf("Recent() = %v, want %v", got, tc.wantRefs)
			}
			if got := strings.Count(logs.String(), dropped); got != tc.wantWarns {
				t.Errorf("%d %q lines, want %d:\n%s", got, dropped, tc.wantWarns, logs.String())
			}
		})
	}

	t.Run("past the cap", func(t *testing.T) {
		in := make([]marotte.RecentClose, 0, maxRecent+3)
		for i := range maxRecent + 3 {
			in = append(in, entry("c-"+strconv.Itoa(i), now-int64(i)*time.Second.Milliseconds()))
		}
		dir := t.TempDir()
		writeDoc(t, dir, file{Recent: in})
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		got := recentRefs(s.Recent())
		if len(got) != maxRecent || got[0] != "c-0" || got[maxRecent-1] != "c-9" {
			t.Errorf("Recent() = %v, want the newest %d, c-0 to c-9", got, maxRecent)
		}
	})

	t.Run("a name over the cap is cleared and the entry kept", func(t *testing.T) {
		const cleared = "tabs: a recently closed entry's name was too long and is cleared on load"
		logs := captureLogs(t)
		dir := t.TempDir()
		writeDoc(t, dir, file{Recent: []marotte.RecentClose{
			{Ref: "c-long", Name: strings.Repeat("n", marotte.MaxChatNameBytes+1), ClosedAt: now},
			{Ref: "c-cap", Name: strings.Repeat("n", marotte.MaxChatNameBytes), ClosedAt: now},
		}})
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		recent := s.Recent()
		if got := recentRefs(recent); !slices.Equal(got, []string{"c-long", "c-cap"}) {
			t.Fatalf("Recent() = %v, want every entry kept", got)
		}
		if recent[0].Name != "" || len(recent[1].Name) != marotte.MaxChatNameBytes {
			t.Errorf("names = %q, %d bytes, want \"\" and the 512-byte name kept", recent[0].Name, len(recent[1].Name))
		}
		if got := strings.Count(logs.String(), cleared); got != 1 {
			t.Errorf("%d %q lines, want 1:\n%s", got, cleared, logs.String())
		}
	})

	t.Run("an unreadable file starts with an empty list", func(t *testing.T) {
		dir := t.TempDir()
		writeRaw(t, dir, []byte(`{"recent":[{"ref":"c-a","name":"A","closed_at":`), fileMode)
		s, _ := NewStore(dir)
		if recent := s.Recent(); len(recent) != 0 {
			t.Errorf("Recent() over an unreadable tabs.json = %+v, want empty", recent)
		}
	})
}

func TestPrune_DropsAnEntryWhoseChatIsGone(t *testing.T) {
	gone := func(refs ...string) func(marotte.TabSubject) bool {
		return func(sub marotte.TabSubject) bool {
			return sub.Kind != marotte.TabKindChat || !slices.Contains(refs, sub.Ref)
		}
	}

	t.Run("with a tab in the same mutation", func(t *testing.T) {
		s, dir := newTestStore(t)
		withClock(s)
		closedChat(t, s, "c-listed-gone")
		closedChat(t, s, "c-listed-kept")
		tab := mustOpen(t, s, chatSpec("c-tab-gone"))
		_, before := s.List()

		dropped, v, err := s.Prune(t.Context(), gone("c-listed-gone", "c-tab-gone"))
		if err != nil || v != before+1 {
			t.Fatalf("Prune = (v%d, %v), want one mutation at version %d", v, err, before+1)
		}
		if got := idsOf(dropped); !slices.Equal(got, []string{tab.ID}) {
			t.Errorf("Prune dropped %v, want the tab alone: dropped names tabs only", got)
		}
		if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-listed-kept"}) {
			t.Errorf("Recent() = %v, want only c-listed-kept", got)
		}
		if doc := onDisk(t, dir); len(doc.Recent) != 1 || doc.Version != v {
			t.Errorf("tabs.json recent %+v at version %d, want c-listed-kept alone at %d", doc.Recent, doc.Version, v)
		}
	})

	t.Run("entries alone still write and bump", func(t *testing.T) {
		logs := captureLogs(t)
		s, dir := newTestStore(t)
		withClock(s)
		closedChat(t, s, "c-gone")
		_, before := s.List()

		dropped, v, err := s.Prune(t.Context(), gone("c-gone"))
		if err != nil || v != before+1 || len(dropped) != 0 {
			t.Fatalf("Prune = (%d dropped, v%d, %v), want no tab dropped at version %d", len(dropped), v, err, before+1)
		}
		if len(s.Recent()) != 0 || onDisk(t, dir).Version != v {
			t.Errorf("Recent() = %+v, tabs.json at version %d, want empty at %d", s.Recent(), onDisk(t, dir).Version, v)
		}
		const info = "tabs: a recently closed entry whose chat is gone was dropped on load"
		if !strings.Contains(logs.String(), info) || !strings.Contains(logs.String(), "c-gone") {
			t.Errorf("logs lack %q for c-gone:\n%s", info, logs.String())
		}
	})

	t.Run("an entry whose chat exists is kept", func(t *testing.T) {
		s, _ := newTestStore(t)
		withClock(s)
		closedChat(t, s, "c-a")
		_, before := s.List()
		if _, v, err := s.Prune(t.Context(), func(marotte.TabSubject) bool { return true }); err != nil || v != before {
			t.Errorf("Prune = (v%d, %v), want nothing changed at %d", v, err, before)
		}
		if got := recentRefs(s.Recent()); !slices.Equal(got, []string{"c-a"}) {
			t.Errorf("Recent() = %v, want c-a kept", got)
		}
	})
}
