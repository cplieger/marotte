package command

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
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
	f.mem = newMembership(&membershipDeps{Chats: f.chats, Tabs: f.tabs, Bus: &tabBus{}, Sessions: store})
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
