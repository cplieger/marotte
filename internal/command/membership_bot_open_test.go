package command

// Every chat-tab opener takes a Bot's tab fact and its top-level place from the record, whatever the caller sent.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// openerHost is a coordinator over a real tab store whose run wf_1 was launched by c-target, with c-host's tab open
// as a parent a caller can name.
type openerHost struct {
	mem     *Membership
	st      *tabs.Store
	store   *testsupport.InMemoryChatStore
	hostTab string
}

func newOpenerHost(t *testing.T, bot bool) *openerHost {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir(), settings.DefaultHistoryWindow)
	if err != nil {
		t.Fatalf("Setup: open tab store: %v", err)
	}
	mem := newMembership(&membershipDeps{
		Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: st, Bus: &tabBus{},
		Runs: &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-target"}},
	})
	seedChatNamed(t, store, "c-host", "Host")
	if bot {
		seedStartedBot(t, store, "c-target", "CI watcher")
	} else {
		seedChatNamed(t, store, "c-target", "Plain")
	}
	h := &openerHost{mem: mem, st: st, store: store}
	h.hostTab = openChatTab(t, mem, "c-host", "").ID
	return h
}

func (h *openerHost) targetTab(t *testing.T) marotte.TabSubject {
	t.Helper()
	open, _ := h.st.List()
	found := tabsForChat(open, "c-target")
	if len(found) != 1 {
		t.Fatalf("c-target has %d tabs, want one: %+v", len(found), open)
	}
	return found[0]
}

func TestOpenTab_EveryChatTabOpenerTakesTheBotFactFromTheRecord(t *testing.T) {
	openers := []struct {
		open func(t *testing.T, h *openerHost)
		name string
		// keepsParent is whether a regular chat opens under the caller's parent.
		keepsParent bool
	}{
		{name: "History naming a parent", keepsParent: true, open: func(t *testing.T, h *openerHost) {
			payload, _ := json.Marshal(map[string]any{"kind": "chat", "ref": "c-target", "parent": h.hostTab, "owns": true, "op_id": "op-history"})
			if _, err := cmdOpenTab(t.Context(), h.mem, &marotte.ClientCommand{Type: marotte.CmdOpenTab, Payload: payload}); err != nil {
				t.Fatalf("open_tab(c-target under c-host) = %v", err)
			}
		}},
		{name: "a run tab whose launcher has no tab", open: func(t *testing.T, h *openerHost) {
			if _, err := h.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
				t.Fatalf("OpenTab(run wf_1) = %v", err)
			}
		}},
		{name: "a message from an outside caller", open: func(t *testing.T, h *openerHost) {
			h.mem.ReopenForMessage(t.Context(), "c-target", grelotCaller)
		}},
	}
	for _, o := range openers {
		t.Run(o.name+" opens a Bot top level with the Bot fact", func(t *testing.T) {
			h := newOpenerHost(t, true)
			o.open(t, h)
			if tab := h.targetTab(t); !tab.Bot || tab.Parent != "" {
				t.Errorf("the Bot's tab = bot %v parent %q, want a top-level Bot tab", tab.Bot, tab.Parent)
			}
		})
		t.Run(o.name+" opens a regular chat with no Bot fact", func(t *testing.T) {
			h := newOpenerHost(t, false)
			o.open(t, h)
			tab := h.targetTab(t)
			if tab.Bot {
				t.Error("a regular chat's tab has the Bot fact")
			}
			if o.keepsParent && tab.Parent != h.hostTab {
				t.Errorf("parent = %q, want the caller's %q kept", tab.Parent, h.hostTab)
			}
		})
	}
}

func TestMergeTangent_ReopensABotParentTopLevelAsABot(t *testing.T) {
	f := newMergeFixture(t)
	if _, err := f.host.Mutate(t.Context(), "c-parent", func(c *marotte.Chat, _ bool) bool {
		c.CurrentModeID = marotte.BotRoleID
		c.NameSetByUser = true
		c.Bot = &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1}
		return true
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "Found it."}); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	if rec := awaitMergeSettled(t, f); rec.State != marotte.TangentMergeSucceeded {
		t.Fatalf("merge = %+v, want succeeded", rec)
	}

	open, _ := f.tabs.List()
	parent := tabsForChat(open, "c-parent")
	if len(parent) != 1 {
		t.Fatalf("the Bot parent has %d tabs after the merge, want one: %+v", len(parent), open)
	}
	if !parent[0].Bot || parent[0].Parent != "" {
		t.Errorf("the reopened parent = bot %v parent %q, want a top-level Bot tab", parent[0].Bot, parent[0].Parent)
	}
}

// unloadableRecord is a chat store whose Get cannot load one record while its keep-fact hold still can.
type unloadableRecord struct {
	membershipChats
	id marotte.ChatID
}

func (u *unloadableRecord) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	if id == u.id {
		return nil, false
	}
	return u.membershipChats.Get(ctx, id)
}

func TestOpenTab_ARecordTheOpenCannotLoadFailsTheOpenAndReleasesItsHold(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir(), settings.DefaultHistoryWindow)
	if err != nil {
		t.Fatalf("Setup: open tab store: %v", err)
	}
	mem := newMembership(&membershipDeps{
		Teardown: &recordingTeardown{}, Chats: &unloadableRecord{membershipChats: withKeep(store), id: "c-bot"}, Tabs: st, Bus: &tabBus{},
	})
	seedStartedBot(t, store, "c-bot", "CI watcher")

	_, err = mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Owns: true}, "op-open")

	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("OpenTab = %d (%v), want 503: an unread record must not open as a regular tab", statusOf(err), err)
	}
	if open, _ := st.List(); len(tabsForChat(open, "c-bot")) != 0 {
		t.Errorf("tabs = %+v, want no tab for the unread record", open)
	}
	if c, _ := store.Get(t.Context(), "c-bot"); c != nil {
		if _, held := c.Keep[marotte.KeyTab]; held {
			t.Error("the failed open left its tab fact held")
		}
	}
}

// overCapHost is a coordinator whose tab set holds one more tab than the product limit, as the boot heal leaves it.
func overCapHost(t *testing.T) (*Membership, *tabs.Store) {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir(), settings.DefaultHistoryWindow)
	if err != nil {
		t.Fatalf("Setup: open tab store: %v", err)
	}
	mem := newMembership(&membershipDeps{
		Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: st, Bus: &tabBus{},
		Runs: &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-closed"}},
	})
	fillTabs(t, mem, tabs.MaxOpenTabs)
	seedStartedBot(t, store, "c-healed", "Healed")
	if _, _, _, err := st.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-healed", Owns: true, Bot: true, OverCap: true}); err != nil {
		t.Fatalf("Setup: the heal's open past the cap: %v", err)
	}
	seedChatNamed(t, store, "c-closed", "Closed")
	return mem, st
}

func TestOpeners_RefuseEveryOpenWhileTheSetIsOverTheTabLimit(t *testing.T) {
	opens := map[string]func(t *testing.T, mem *Membership) error{
		"New chat": func(t *testing.T, mem *Membership) error {
			_, err := cmdCreateChat(t.Context(), mem, createReq(t, "", marotte.CreateChatCommand{OpID: "op-chat"}))
			return err
		},
		"New Bot": func(t *testing.T, mem *Membership) error {
			_, err := cmdCreateChat(t.Context(), mem, createReq(t, "", marotte.CreateChatCommand{OpID: "op-bot", Bot: true}))
			return err
		},
		"a History open": func(t *testing.T, mem *Membership) error {
			_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-closed", Owns: true}, "op-history")
			return err
		},
		"a run-tab open": func(t *testing.T, mem *Membership) error {
			_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
			return err
		},
	}
	for name, open := range opens {
		t.Run(name, func(t *testing.T) {
			mem, st := overCapHost(t)

			err := open(t, mem)

			if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonTabsFull {
				t.Errorf("%s at %d tabs = %d %q, want 409 tabs_full", name, tabs.MaxOpenTabs+1, statusOf(err), reasonOf(err))
			}
			if open, _ := st.List(); len(open) != tabs.MaxOpenTabs+1 {
				t.Errorf("the set holds %d tabs, want %d: a refused open applies nothing", len(open), tabs.MaxOpenTabs+1)
			}
		})
	}
}
