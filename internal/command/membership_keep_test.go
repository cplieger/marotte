package command

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// faultyReleases is the real chat store whose Release fails while fail is set: an unwritable record at the moment a
// close owes its release.
type faultyReleases struct {
	*chat.Store
	fail atomic.Bool
}

func (f *faultyReleases) Release(ctx context.Context, id marotte.ChatID, key string, ev chat.Evidence) error {
	if f.fail.Load() {
		return errors.New("disk full")
	}
	return f.Store.Release(ctx, id, key, ev)
}

// failingOpens is the real tab store whose Open fails while fail is set: a tabs.json that refuses every write.
type failingOpens struct {
	*tabs.Store
	fail atomic.Bool
}

func (f *failingOpens) Open(ctx context.Context, spec marotte.OpenTab) (marotte.TabSubject, bool, uint64, error) {
	if f.fail.Load() {
		return marotte.TabSubject{}, false, 0, errors.New("tabs.json: read-only file system")
	}
	return f.Store.Open(ctx, spec)
}

type keepFixture struct {
	mem   *Membership
	store *chat.Store
	chats *faultyReleases
	tabs  *failingOpens
}

func newKeepFixture(t *testing.T) *keepFixture {
	t.Helper()
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: chat.NewStore: %v", err)
	}
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	f := &keepFixture{store: store, chats: &faultyReleases{Store: store}, tabs: &failingOpens{Store: st}}
	f.mem = newMembership(&membershipDeps{Chats: f.chats, Tabs: f.tabs, Bus: &tabBus{}, Sessions: store, Runs: &fakeRunOwner{}})
	return f
}

func (f *keepFixture) fact(t *testing.T, id marotte.ChatID, key string) (marotte.KeepFact, bool) {
	t.Helper()
	c, found, err := f.store.Lookup(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("Lookup(%s) = found %v, %v", id, found, err)
	}
	fact, held := c.Keep[key]
	return fact, held
}

func (f *keepFixture) closeTabOf(t *testing.T, id marotte.ChatID) {
	t.Helper()
	tabID := f.mem.tabForChat(id)
	if tabID == "" {
		t.Fatalf("no tab shows %s", id)
	}
	if _, _, err := f.mem.closeTab(t.Context(), tabID, "op-close"); err != nil {
		t.Fatalf("closeTab(%s): %v", id, err)
	}
}

func TestCreateChatAndOpen_HoldsTheTabFactOnEveryPath(t *testing.T) {
	t.Run("fresh create", func(t *testing.T) {
		f := newKeepFixture(t)
		opened := createChat(t, f.mem, "op-a")
		if _, held := f.fact(t, marotte.ChatID(opened.Chat.ID), marotte.KeyTab); !held {
			t.Error("a fresh create's chat has no tab fact while its tab is open")
		}
	})
	t.Run("op_id replay after the tab closed", func(t *testing.T) {
		f := newKeepFixture(t)
		id := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
		f.closeTabOf(t, id)
		if _, held := f.fact(t, id, marotte.KeyTab); held {
			t.Fatal("Setup: the close did not release the tab fact")
		}
		replay := createChat(t, f.mem, "op-a")
		if !replay.Replay || marotte.ChatID(replay.Chat.ID) != id {
			t.Fatalf("Setup: replay = %+v, want the first attempt's chat", replay)
		}
		if _, held := f.fact(t, id, marotte.KeyTab); !held {
			t.Error("a replay that reopened the tab holds no tab fact")
		}
	})
	t.Run("supplied existing id", func(t *testing.T) {
		f := newKeepFixture(t)
		id := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
		f.closeTabOf(t, id)
		if _, err := f.mem.CreateChatAndOpen(t.Context(), &ChatCreate{OpID: "op-b", ChatID: id, Init: func(*marotte.Chat) {}}); err != nil {
			t.Fatalf("CreateChatAndOpen(supplied %s): %v", id, err)
		}
		if _, held := f.fact(t, id, marotte.KeyTab); !held {
			t.Error("a create naming an existing record reopened its tab with no tab fact")
		}
	})
	t.Run("failed tab write rolls the fresh hold back", func(t *testing.T) {
		f := newKeepFixture(t)
		f.tabs.fail.Store(true)
		_, err := f.mem.CreateChatAndOpen(t.Context(), &ChatCreate{OpID: "op-a", Init: func(*marotte.Chat) {}})
		if err == nil {
			t.Fatal("CreateChatAndOpen with a failing tab write = nil, want the error")
		}
		id, _ := f.mem.resolvedChat("op-a")
		if _, held := f.fact(t, id, marotte.KeyTab); held {
			t.Error("a create whose tab never opened still holds the tab fact")
		}
	})
}

func TestOpenTab_ChatTabHoldsAndAFailedOpenRollsBackOnlyItsOwnHold(t *testing.T) {
	f := newKeepFixture(t)
	id := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
	f.closeTabOf(t, id)
	spec := marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(id), Owns: true}

	f.tabs.fail.Store(true)
	if _, err := f.mem.OpenTab(t.Context(), spec, "op-open"); err == nil {
		t.Fatal("OpenTab with a failing tab write = nil, want the error")
	}
	if _, held := f.fact(t, id, marotte.KeyTab); held {
		t.Error("a failed open of a chat with no fact left a tab fact behind")
	}

	f.tabs.fail.Store(false)
	if _, err := f.mem.OpenTab(t.Context(), spec, "op-open"); err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if _, held := f.fact(t, id, marotte.KeyTab); !held {
		t.Error("an open chat tab holds no tab fact")
	}

	f.chats.fail.Store(true)
	f.closeTabOf(t, id)
	f.chats.fail.Store(false)
	earlier, held := f.fact(t, id, marotte.KeyTab)
	if !held {
		t.Fatal("Setup: the failed release dropped the fact")
	}
	f.tabs.fail.Store(true)
	if _, err := f.mem.OpenTab(t.Context(), spec, "op-reopen"); err == nil {
		t.Fatal("OpenTab with a failing tab write = nil, want the error")
	}
	if got, held := f.fact(t, id, marotte.KeyTab); !held || got != earlier {
		t.Errorf("after a failed reopen the tab fact = %+v (held %v), want the earlier fact %+v unchanged", got, held, earlier)
	}
	f.tabs.fail.Store(false)

	f.mem.SettleOwed(t.Context())
	if _, held := f.fact(t, id, marotte.KeyTab); held {
		t.Error("SettleOwed kept the fact of a tab that closed and never reopened")
	}
	if owed := f.tabs.Owed(); len(owed) != 0 {
		t.Errorf("owed after SettleOwed = %+v, want the entry settled", owed)
	}
}

func TestCloseTab_ReleasesTheTabFactOfEveryChatTheCloseLeftTabless(t *testing.T) {
	f := newKeepFixture(t)
	parent := createChat(t, f.mem, "op-parent")
	parentID := marotte.ChatID(parent.Chat.ID)
	fork, err := f.mem.CreateChatAndOpen(t.Context(), &ChatCreate{OpID: "op-fork", ParentChat: parentID, Init: func(*marotte.Chat) {}})
	if err != nil {
		t.Fatalf("Setup: fork: %v", err)
	}
	forkID := marotte.ChatID(fork.Chat.ID)
	otherID := marotte.ChatID(createChat(t, f.mem, "op-other").Chat.ID)

	f.closeTabOf(t, parentID)

	for _, id := range []marotte.ChatID{parentID, forkID} {
		if _, held := f.fact(t, id, marotte.KeyTab); held {
			t.Errorf("chat %s still holds its tab fact after the close took its only tab", id)
		}
	}
	if _, held := f.fact(t, otherID, marotte.KeyTab); !held {
		t.Error("a chat whose tab stayed open lost its tab fact")
	}
	if owed := f.tabs.Owed(); len(owed) != 0 {
		t.Errorf("owed after successful releases = %+v, want none", owed)
	}
}

func TestSettleOwed_KeepsAFactNewerThanTheClose(t *testing.T) {
	f := newKeepFixture(t)
	id := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
	f.chats.fail.Store(true)
	f.closeTabOf(t, id)
	f.chats.fail.Store(false)
	if owed := f.tabs.Owed(); len(owed) != 1 {
		t.Fatalf("Setup: owed = %+v, want the failed release owed", owed)
	}

	if _, err := f.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(id)}, "op-reopen"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	f.closeTabOf(t, id)
	// Since and ClosedAt are Unix ms: the hold must land in a later millisecond than the first close.
	time.Sleep(2 * time.Millisecond)
	if _, err := f.store.Hold(t.Context(), id, marotte.KeyTab, chat.KeepDurable); err != nil {
		t.Fatalf("Setup: a hold after the close: %v", err)
	}

	f.mem.SettleOwed(t.Context())
	if _, held := f.fact(t, id, marotte.KeyTab); !held {
		t.Error("SettleOwed released a tab fact written after the owed close")
	}
	if owed := f.tabs.Owed(); len(owed) != 0 {
		t.Errorf("owed after SettleOwed = %+v, want it settled", owed)
	}
}

func TestSettleOwed_AFailedReleaseStaysOwed(t *testing.T) {
	f := newKeepFixture(t)
	id := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
	f.chats.fail.Store(true)
	f.closeTabOf(t, id)
	f.mem.SettleOwed(t.Context())
	if owed := f.tabs.Owed(); len(owed) != 1 || owed[0].Ref != string(id) {
		t.Errorf("owed after a failing SettleOwed = %+v, want the entry kept", owed)
	}
	if _, held := f.fact(t, id, marotte.KeyTab); !held {
		t.Error("a failed release dropped the fact")
	}
}

func TestDeleteChatAndCloseTabs_ReleasesTheFactsOfTheTabsItClosesUnderTheChat(t *testing.T) {
	f := newKeepFixture(t)
	f.mem.teardown = &recordingTeardown{}
	parentID := marotte.ChatID(createChat(t, f.mem, "op-parent").Chat.ID)
	fork, err := f.mem.CreateChatAndOpen(t.Context(), &ChatCreate{OpID: "op-fork", ParentChat: parentID, Init: func(*marotte.Chat) {}})
	if err != nil {
		t.Fatalf("Setup: fork: %v", err)
	}
	forkID := marotte.ChatID(fork.Chat.ID)

	if err := f.mem.DeleteChatAndCloseTabs(t.Context(), parentID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs: %v", err)
	}
	if _, held := f.fact(t, forkID, marotte.KeyTab); held {
		t.Error("the fork whose tab closed under its deleted parent still holds its tab fact")
	}
	if owed := f.tabs.Owed(); len(owed) != 0 {
		t.Errorf("owed = %+v, want the deleted chat's entry settled without a release", owed)
	}
}

func TestOpenTab_ARunTabReopeningItsLauncherHoldsTheLaunchersTabFact(t *testing.T) {
	f := newKeepFixture(t)
	launcher := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
	f.closeTabOf(t, launcher)
	f.mem.runs = &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": launcher}}

	if _, err := f.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
		t.Fatalf("OpenTab(run): %v", err)
	}
	if _, held := f.fact(t, launcher, marotte.KeyTab); !held {
		t.Error("the launcher's reopened chat tab holds no tab fact")
	}
}

func TestHoldTabErr_MapsEachRefusal(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   error
	}{
		{name: "in teardown", err: chat.ErrInTeardown, status: http.StatusConflict, want: errChatPurging},
		{name: "tombstoned", err: chat.ErrTombstoned, status: http.StatusNotFound, want: errOpenChatUnknown},
		{name: "not found", err: chat.ErrChatNotFound, status: http.StatusNotFound, want: errOpenChatUnknown},
		{name: "unreadable", err: errors.New("chat.json: unexpected end of JSON input"), status: http.StatusServiceUnavailable, want: errChatUnread},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := holdTabErr(tc.err)
			if code, _, _ := StatusOf(got); code != tc.status || !errors.Is(got, tc.want) {
				t.Errorf("holdTabErr(%v) = %v (status %d), want %v (status %d)", tc.err, got, code, tc.want, tc.status)
			}
		})
	}
}

type ownerScan chat.OwnerScan

func (o ownerScan) SessionOwners(context.Context) chat.OwnerScan { return chat.OwnerScan(o) }

func TestCreateChatAndOpen_ClaimSessionRefusesASessionItCannotOwn(t *testing.T) {
	cases := []struct {
		name   string
		scan   chat.OwnerScan
		status int
		want   error
	}{
		{name: "another chat owns it", scan: chat.OwnerScan{Owners: chat.SessionOwners{"sess_1": {"c-other"}}, Listed: true, Complete: true}, status: http.StatusConflict, want: errSessionClaimed},
		{name: "an incomplete scan found no owner", scan: chat.OwnerScan{Owners: chat.SessionOwners{}, Listed: true, Skipped: []marotte.ChatID{"c-x"}}, status: http.StatusServiceUnavailable, want: errChatUnread},
		{name: "unclaimed on a complete scan", scan: chat.OwnerScan{Owners: chat.SessionOwners{}, Listed: true, Complete: true}},
		{name: "its own chat owns it (a replay)", scan: chat.OwnerScan{Owners: chat.SessionOwners{"sess_1": {"c-self"}}, Listed: true, Skipped: []marotte.ChatID{"c-x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeepFixture(t)
			f.mem.sessions = ownerScan(tc.scan)
			_, err := f.mem.CreateChatAndOpen(t.Context(), &ChatCreate{
				OpID: "op-resume", ChatID: "c-self", ClaimSession: "sess_1",
				Init: func(c *marotte.Chat) { c.RecordSession("sess_1") },
			})
			if tc.want == nil {
				if err != nil {
					t.Errorf("CreateChatAndOpen = %v, want it created", err)
				}
				return
			}
			if code, _, _ := StatusOf(err); code != tc.status || !errors.Is(err, tc.want) {
				t.Errorf("CreateChatAndOpen = %v (status %d), want %v (status %d)", err, code, tc.want, tc.status)
			}
			if _, found, _ := f.store.Lookup(t.Context(), "c-self"); found {
				t.Error("a refused claim still wrote the record")
			}
		})
	}
}

// seedTabless writes a record with no tab and no keep fact, bound to session when it is not empty.
func (f *keepFixture) seedTabless(t *testing.T, id marotte.ChatID, session string) {
	t.Helper()
	if _, err := f.store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = marotte.DefaultChatName
		if session != "" {
			c.RecordSession(session)
		}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed %s: %v", id, err)
	}
}

// purgeParked removes id by the retention purge and leaves its teardown running, as OnPurge does until it returns.
func (f *keepFixture) purgeParked(t *testing.T, id marotte.ChatID) {
	t.Helper()
	v, err := f.store.PurgeIdle(t.Context(), id, time.Now().Add(time.Hour))
	if err != nil || v.Outcome != archive.PurgePurged {
		t.Fatalf("Setup: PurgeIdle(%s) = %+v, %v; want purged", id, v, err)
	}
}

func (f *keepFixture) runTab(t *testing.T, workflowID string) (marotte.TabSubject, bool) {
	t.Helper()
	open, _ := f.tabs.List()
	for _, tab := range open {
		if tab.Kind == marotte.TabKindRun && tab.Ref == workflowID {
			return tab, true
		}
	}
	return marotte.TabSubject{}, false
}

func TestOpenTab_ANewRunTabHoldsItsRunTabFactOnEveryHolder(t *testing.T) {
	f := newKeepFixture(t)
	f.seedTabless(t, "c-a", "sess_shared")
	f.seedTabless(t, "c-b", "sess_shared")
	f.mem.runs = &fakeRunOwner{holders: map[string][]marotte.ChatID{"wf_1": {"c-a", "c-b"}}}
	key, _ := marotte.KeyRunTab("wf_1")

	if _, err := f.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
		t.Fatalf("OpenTab(run): %v", err)
	}
	for _, id := range []marotte.ChatID{"c-a", "c-b"} {
		if _, held := f.fact(t, id, key); !held {
			t.Errorf("%s holds no %s while the run's tab is open", id, key)
		}
	}
	tab, ok := f.runTab(t, "wf_1")
	if !ok {
		t.Fatal("no run tab opened")
	}
	if !slices.Equal(tab.LaunchChats, []string{"c-a", "c-b"}) || !tab.LaunchResolved {
		t.Errorf("run tab launch = %v resolved %v, want [c-a c-b] resolved", tab.LaunchChats, tab.LaunchResolved)
	}
	if tab.Parent == "" || tab.Parent != f.mem.tabForChat("c-a") {
		t.Errorf("run tab parent = %q, want the first holder's reopened tab", tab.Parent)
	}

	if _, _, err := f.mem.closeTab(t.Context(), tab.ID, "op-close"); err != nil {
		t.Fatalf("closeTab(run): %v", err)
	}
	for _, id := range []marotte.ChatID{"c-a", "c-b"} {
		if _, held := f.fact(t, id, key); held {
			t.Errorf("%s still holds %s after the user closed the run's tab", id, key)
		}
	}
	if owed := f.tabs.Owed(); len(owed) != 0 {
		t.Errorf("owed = %+v, want the run tab's releases settled", owed)
	}
}

func TestOpenTab_ANewRunTabIsRefusedWhenItsOwnersCannotBeHeld(t *testing.T) {
	key, _ := marotte.KeyRunTab("wf_1")
	cases := []struct {
		name   string
		setup  func(t *testing.T, f *keepFixture) (runs *fakeRunOwner, spec marotte.OpenTab)
		status int
		want   error
	}{
		{
			name: "no source names the run",
			setup: func(*testing.T, *keepFixture) (*fakeRunOwner, marotte.OpenTab) {
				return &fakeRunOwner{unknown: map[string]bool{"wf_1": true}}, marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}
			},
			status: http.StatusServiceUnavailable, want: errRunUnknown,
		},
		{
			name: "the owners could not be read and the caller named an open parent",
			setup: func(t *testing.T, f *keepFixture) (*fakeRunOwner, marotte.OpenTab) {
				parent := createChat(t, f.mem, "op-parent")
				return &fakeRunOwner{chatErr: errors.New("engine draining")},
					marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1", Parent: parent.Subject.ID}
			},
			status: http.StatusServiceUnavailable, want: errRunLauncherUnread,
		},
		{
			name: "the launcher's teardown still runs",
			setup: func(t *testing.T, f *keepFixture) (*fakeRunOwner, marotte.OpenTab) {
				f.seedTabless(t, "c-gone", "sess_launch")
				f.seedTabless(t, "c-a", "")
				f.purgeParked(t, "c-gone")
				return &fakeRunOwner{
					holders: map[string][]marotte.ChatID{"wf_1": {"c-a"}}, parents: map[string]string{"wf_1": "sess_launch"},
				}, marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}
			},
			status: http.StatusConflict, want: errRunPurging,
		},
		{
			name: "a holder was purged after its owners were read",
			setup: func(t *testing.T, f *keepFixture) (*fakeRunOwner, marotte.OpenTab) {
				f.seedTabless(t, "c-a", "")
				f.seedTabless(t, "c-gone", "")
				f.purgeParked(t, "c-gone")
				return &fakeRunOwner{holders: map[string][]marotte.ChatID{"wf_1": {"c-a", "c-gone"}}},
					marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}
			},
			status: http.StatusConflict, want: errRunPurging,
		},
		{
			name: "a holder's record cannot be read",
			setup: func(t *testing.T, f *keepFixture) (*fakeRunOwner, marotte.OpenTab) {
				f.seedTabless(t, "c-a", "")
				f.seedTabless(t, "c-torn", "")
				if err := os.WriteFile(filepath.Join(f.store.Dir(), "c-torn", "chat.json"), []byte("{"), 0o600); err != nil {
					t.Fatalf("Setup: tear c-torn: %v", err)
				}
				return &fakeRunOwner{holders: map[string][]marotte.ChatID{"wf_1": {"c-a", "c-torn"}}},
					marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}
			},
			status: http.StatusServiceUnavailable, want: errRunLauncherUnread,
		},
		{
			name: "no run owner is wired",
			setup: func(*testing.T, *keepFixture) (*fakeRunOwner, marotte.OpenTab) {
				return nil, marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}
			},
			status: http.StatusServiceUnavailable, want: errRunLauncherUnread,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeepFixture(t)
			runs, spec := tc.setup(t, f)
			f.mem.runs = nil
			if runs != nil {
				f.mem.runs = runs
			}

			_, err := f.mem.OpenTab(t.Context(), spec, "op-run")

			if code, _, _ := StatusOf(err); code != tc.status || !errors.Is(err, tc.want) {
				t.Errorf("OpenTab(run) = %v (status %d), want %v (status %d)", err, code, tc.want, tc.status)
			}
			if _, ok := f.runTab(t, "wf_1"); ok {
				t.Error("a refused open wrote a run tab")
			}
			if _, found, _ := f.store.Lookup(t.Context(), "c-a"); found {
				if _, held := f.fact(t, "c-a", key); held {
					t.Errorf("c-a holds %s after a refused open: the claims taken before the refusal were not rolled back", key)
				}
			}
		})
	}
}

func TestOpenTab_AFailedRunTabWriteRollsBackItsRunTabFacts(t *testing.T) {
	f := newKeepFixture(t)
	launcher := marotte.ChatID(createChat(t, f.mem, "op-a").Chat.ID)
	f.mem.runs = &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": launcher}}
	key, _ := marotte.KeyRunTab("wf_1")
	f.tabs.fail.Store(true)

	if _, err := f.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err == nil {
		t.Fatal("OpenTab(run) over a tab store refusing writes = nil, want its failure")
	}
	if _, held := f.fact(t, launcher, key); held {
		t.Errorf("the launcher holds %s for a run tab that never opened", key)
	}
	if _, held := f.fact(t, launcher, marotte.KeyTab); !held {
		t.Error("the failed run tab rolled back the launcher's own tab fact, which its open chat tab still needs")
	}
}

func TestCreateChatAndOpen_RefusesASessionThatLaunchedALiveOrShownRun(t *testing.T) {
	cases := []struct {
		name    string
		runs    *fakeRunOwner
		showRun bool
		status  int
		want    error
	}{
		{name: "a live run", runs: &fakeRunOwner{parented: map[string][]string{"sess_1": {"wf_live"}}}, status: http.StatusConflict, want: errSessionParentsRun},
		{name: "a finished run a tab shows", runs: &fakeRunOwner{parentedAll: map[string][]string{"sess_1": {"wf_1"}}}, showRun: true, status: http.StatusConflict, want: errSessionParentsRun},
		{name: "the session's runs could not be read", runs: &fakeRunOwner{parentedErr: errors.New("runs/ not read yet")}, status: http.StatusServiceUnavailable, want: errChatUnread},
		{name: "a finished run no tab shows", runs: &fakeRunOwner{parentedAll: map[string][]string{"sess_1": {"wf_1"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeepFixture(t)
			f.mem.runs = tc.runs
			if tc.showRun {
				if _, err := f.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
					t.Fatalf("Setup: open the run's tab: %v", err)
				}
			}
			_, err := f.mem.CreateChatAndOpen(t.Context(), &ChatCreate{
				OpID: "op-resume", ChatID: "c-self", ClaimSession: "sess_1",
				Init: func(c *marotte.Chat) { c.RecordSession("sess_1") },
			})
			if tc.want == nil {
				if err != nil {
					t.Errorf("CreateChatAndOpen = %v, want it created", err)
				}
				return
			}
			if code, _, _ := StatusOf(err); code != tc.status || !errors.Is(err, tc.want) {
				t.Errorf("CreateChatAndOpen = %v (status %d), want %v (status %d)", err, code, tc.want, tc.status)
			}
			if _, found, _ := f.store.Lookup(t.Context(), "c-self"); found {
				t.Error("a refused resume still wrote the record")
			}
		})
	}
}

func TestAdmitRunPurge_RefusesWhileTheArrangementIsUnread(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tabs.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("Setup: write tabs.json: %v", err)
	}
	st, _ := tabs.NewStore(dir)
	if st.Unread() == "" {
		t.Fatal("Setup: the tab store read a garbage arrangement")
	}
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: chat.NewStore: %v", err)
	}
	mem := newMembership(&membershipDeps{Chats: store, Tabs: st, Bus: &tabBus{}, Sessions: store, Runs: &fakeRunOwner{}})

	_, refused := mem.AdmitRunPurge(t.Context(), "wf_1", "sess_host")
	var unread *ArrangementUnreadError
	if !errors.As(refused, &unread) || unread.Aside != st.Unread() {
		t.Errorf("AdmitRunPurge with the arrangement unread = %v, want *ArrangementUnreadError naming %s", refused, st.Unread())
	}
}

func TestAdmitRunPurge_RefusesARunWhoseLauncherIsInTeardown(t *testing.T) {
	f := newKeepFixture(t)
	f.seedTabless(t, "c-launcher", "sess_launcher")
	f.purgeParked(t, "c-launcher")

	if _, refused := f.mem.AdmitRunPurge(t.Context(), "wf_1", "sess_launcher"); refused == nil {
		t.Error("AdmitRunPurge admitted a run whose launching chat's teardown still runs, want a refusal")
	}
	f.store.EndTeardown("c-launcher", []string{"sess_launcher"})
	done, refused := f.mem.AdmitRunPurge(t.Context(), "wf_1", "sess_launcher")
	if refused != nil {
		t.Fatalf("AdmitRunPurge after the teardown ended = %v, want it admitted", refused)
	}
	done()
}
