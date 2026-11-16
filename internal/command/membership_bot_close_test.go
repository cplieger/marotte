package command

// A Bot's tab closes and reopens as a chat tab does, and the Bot's record says whether the user closed it: closed_at,
// written under the coordinator's lock by the close and cleared by every reopen.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

const warnBotCloseMark = "bots: a closed Bot's record could not be marked closed"

// headerProbe is a chat store's bus that records each chat_updated header.
type headerProbe struct {
	headers []marotte.ChatHeader
	mu      sync.Mutex
}

func (p *headerProbe) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	if evt.Type != marotte.EventChatUpdated {
		return
	}
	h, _ := evt.Payload.(marotte.ChatHeader)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.headers = append(p.headers, h)
}

// closedUpdates is how many chat_updated headers for id summarized the Bot as closed.
func (p *headerProbe) closedUpdates(id marotte.ChatID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, h := range p.headers {
		if h.ID == string(id) && h.Bot != nil && h.Bot.State == "closed" {
			n++
		}
	}
	return n
}

// awaitClosedUpdates polls for the store's coalesced header publish, up to a second, and answers the count.
func (p *headerProbe) awaitClosedUpdates(t *testing.T, id marotte.ChatID) int {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if n := p.closedUpdates(id); n > 0 {
			return n
		}
	}
	return p.closedUpdates(id)
}

// closeMarkWatch is the real chat store recording, for each record write that set a Bot's closed_at, whether the
// coordinator's lock was held.
type closeMarkWatch struct {
	*chat.Store
	mem     *Membership
	underMu []bool
	mu      sync.Mutex
}

func (w *closeMarkWatch) Mutate(ctx context.Context, id marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	held := false
	if w.mem != nil && !w.mem.mu.TryLock() {
		held = true
	} else if w.mem != nil {
		w.mem.mu.Unlock()
	}
	before, _ := w.Get(ctx, id)
	v, err := w.Store.Mutate(ctx, id, fn)
	if after, ok := w.Get(ctx, id); ok && after.Bot != nil && after.Bot.ClosedAt != 0 &&
		before != nil && before.Bot != nil && before.Bot.ClosedAt == 0 {
		w.mu.Lock()
		w.underMu = append(w.underMu, held)
		w.mu.Unlock()
	}
	return v, err
}

func (w *closeMarkWatch) marks() []bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.underMu)
}

// botHost is a coordinator over the real chat and tab stores with the retention-off escalation wired, retention read
// from retention, every chat_updated recorded by probe and every closed_at write by watch.
type botHost struct {
	*escalationHost
	chats *chat.Store
	probe *headerProbe
	watch *closeMarkWatch
}

func newBotHost(t *testing.T, retention retentionRead) *botHost {
	t.Helper()
	probe := &headerProbe{}
	store, err := chat.NewStore(t.TempDir(), chat.WithBroadcaster(probe))
	if err != nil {
		t.Fatalf("Setup: chat.NewStore: %v", err)
	}
	watch := &closeMarkWatch{Store: store}
	h := &botHost{escalationHost: newEscalationHost(t, watch, retention), chats: store, probe: probe, watch: watch}
	watch.mem = h.mem
	return h
}

func seedStartedBot(t *testing.T, store chatStore, id marotte.ChatID, name string) {
	t.Helper()
	seedBot(t, store, id, name, &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1_700_000_000_000})
}

func TestCloseTab_ABotsTabClosesAsAChatTabAndItsRecordIsMarkedClosed(t *testing.T) {
	h := newBotHost(t, retentionOn)
	seedStartedBot(t, h.chats, "c-bot", "CI watcher")
	tab := openChatTab(t, h.mem, "c-bot", "")
	mark := len(h.bus.frames(t))

	closed, _, err := h.mem.closeTab(t.Context(), tab.ID, "op-close", true, nil)
	if err != nil {
		t.Fatalf("closeTab(bot) = %v", err)
	}

	if ids := subjectIDs(closed); !slices.Equal(ids, []string{tab.ID}) {
		t.Errorf("closed = %v, want the Bot's tab", ids)
	}
	frames := h.bus.frames(t)[mark:]
	if len(frames) != 1 || !slices.Equal(frames[0].RemovedIDs, []string{tab.ID}) {
		t.Fatalf("frames = %+v, want one tabs_changed removing the Bot's tab", frames)
	}
	c := botOf(t, h.chats, "c-bot")
	recent := h.st.Recent()
	if len(recent) != 1 {
		t.Fatalf("Recent() = %+v, want the closed Bot", recent)
	}
	if c.Bot.ClosedAt == 0 || c.Bot.ClosedAt != recent[0].ClosedAt {
		t.Errorf("closed_at = %d, want the close's own time %d", c.Bot.ClosedAt, recent[0].ClosedAt)
	}
	if got := h.watch.marks(); !slices.Equal(got, []bool{true}) {
		t.Errorf("closed_at writes (under the lock) = %v, want one, written before the close let the lock go", got)
	}
	if n := h.probe.awaitClosedUpdates(t, "c-bot"); n != 1 {
		t.Errorf("chat_updated with state closed sent %d times, want 1", n)
	}
	if _, held := c.Keep[marotte.KeyTab]; held {
		t.Error("the Bot still holds its tab fact after the close")
	}
	if _, stop := c.Keep[marotte.KeyStop]; !stop {
		t.Error("a person's close of the Bot holds no stop fact, so its runs would not stop")
	}
}

func TestCloseTab_AStaleImpactRefusesABotsCloseAsAChats(t *testing.T) {
	h := newCloseHost(t)
	seedStartedBot(t, h.store, "c-bot", "CI watcher")
	tab := openChatTab(t, h.mem, "c-bot", "")
	h.impact.work["c-bot"] = CloseWork{Turns: []CloseTurn{{ID: "t-1"}}}

	_, _, err := h.mem.closeTab(t.Context(), tab.ID, "op-close", true, &marotte.CloseImpact{Chats: []marotte.ChatWork{{Ref: "c-bot"}}})

	refusedImpact(t, err)
	if !h.mem.tabOpen(tab.ID) {
		t.Error("a refused close closed the Bot's tab")
	}
	if c := botOf(t, h.store, "c-bot"); c.Bot.ClosedAt != 0 {
		t.Errorf("closed_at = %d after a refused close, want 0", c.Bot.ClosedAt)
	}
}

// refusedCloseMark is the real chat store refusing the one record write that would mark a Bot closed.
type refusedCloseMark struct {
	*chat.Store
}

var errCloseMarkRefused = errors.New("simulated chat.json write failure")

func (r *refusedCloseMark) Mutate(ctx context.Context, id marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	if c, ok := r.Get(ctx, id); ok && c.Bot != nil && c.Bot.ClosedAt == 0 {
		if fn(c, true) && c.Bot != nil && c.Bot.ClosedAt != 0 {
			return "", errCloseMarkRefused
		}
	}
	return r.Store.Mutate(ctx, id, fn)
}

func TestCloseTab_AFailedClosedMarkWarnsAndLeavesTheCloseDone(t *testing.T) {
	logs := capture.Default(t)
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: chat.NewStore: %v", err)
	}
	h := newEscalationHost(t, &refusedCloseMark{Store: store}, retentionOn)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	tab := openChatTab(t, h.mem, "c-bot", "")

	closed, _, err := h.mem.closeTab(t.Context(), tab.ID, "op-close", true, nil)

	if err != nil || len(closed) != 1 {
		t.Fatalf("closeTab = %v, %v; want the Bot's tab closed", closed, err)
	}
	if h.mem.tabOpen(tab.ID) {
		t.Error("the Bot's tab is still open")
	}
	if got := refsOf(h.st.Recent()); !slices.Equal(got, []string{"c-bot"}) {
		t.Errorf("Recent() = %v, want the Bot listed", got)
	}
	if c := botOf(t, store, "c-bot"); c.Bot.ClosedAt != 0 {
		t.Errorf("closed_at = %d, want the refused write to have changed nothing", c.Bot.ClosedAt)
	}
	if n := logs.CountExact(warnBotCloseMark); n != 1 {
		t.Errorf("Warn %q logged %d times, want 1", warnBotCloseMark, n)
	}
}

func TestCloseTab_ABotsRecentEntryCarriesItsNameAndTheBotFact(t *testing.T) {
	h := newBotHost(t, retentionOn)
	seedStartedBot(t, h.chats, "c-bot", "CI watcher")
	seedChatNamed(t, h.chats, "c-chat", "Plain chat")
	closeListed(t, h.mem, "c-chat")
	closeListed(t, h.mem, "c-bot")

	recent := h.st.Recent()
	want := []marotte.RecentClose{{Ref: "c-bot", Name: "CI watcher", Bot: true}, {Ref: "c-chat", Name: "Plain chat"}}
	if len(recent) != len(want) {
		t.Fatalf("Recent() = %+v, want %+v", recent, want)
	}
	for i, w := range want {
		if got := recent[i]; got.Ref != w.Ref || got.Name != w.Name || got.Bot != w.Bot {
			t.Errorf("Recent()[%d] = %+v, want %+v", i, got, w)
		}
	}
}

// seedChatNamed puts a tabless chat named name in any chat store.
func seedChatNamed(t *testing.T, store chatStore, id marotte.ChatID, name string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = name
		return true
	}); err != nil {
		t.Fatalf("Setup: seed %q: %v", id, err)
	}
}

func TestCloseTab_AClosedBotIsKeptAndListedAtEveryDaysKept(t *testing.T) {
	for name, retention := range map[string]retentionRead{"days kept 7": retentionOn, "days kept 0": retentionOff} {
		t.Run(name, func(t *testing.T) {
			h := newBotHost(t, retention)
			seedChatNamed(t, h.chats, "c-older", "Older")
			closeListed(t, h.mem, "c-older")
			seedStartedBot(t, h.chats, "c-bot", "CI watcher")

			closeListed(t, h.mem, "c-bot")

			if c, ok := h.chats.Get(t.Context(), "c-bot"); !ok || c.Bot == nil {
				t.Fatal("closing a started Bot's tab deleted its record")
			}
			recent := h.st.Recent()
			if len(recent) == 0 || recent[0].Ref != "c-bot" || !recent[0].Bot {
				t.Errorf("Recent() = %+v, want the closed Bot at its head", recent)
			}
			if _, deleted := h.teardown.deletedByChain["c-bot"]; deleted {
				t.Error("the Bot's close ran the delete-grade teardown")
			}
		})
	}
}

func TestCloseTab_ADraftsCloseDeletesItAtEveryDaysKept(t *testing.T) {
	for name, retention := range map[string]retentionRead{"days kept 7": retentionOn, "days kept 0": retentionOff} {
		for _, byPerson := range []bool{true, false} {
			who := "this device"
			if !byPerson {
				who = "another device"
			}
			t.Run(name+" from "+who, func(t *testing.T) {
				h := newBotHost(t, retention)
				draft := createBot(t, h.mem, "op-draft")
				tab := h.mem.tabForChat(draft)
				mark := len(h.bus.frames(t))

				if _, _, err := h.mem.closeTab(t.Context(), tab, "op-close", byPerson, nil); err != nil {
					t.Fatalf("closeTab(draft) = %v", err)
				}

				if _, ok := h.chats.Get(t.Context(), draft); ok {
					t.Error("the draft's record survived its close")
				}
				if got := h.st.Recent(); len(got) != 0 {
					t.Errorf("Recent() = %+v, want the draft unlisted", got)
				}
				for _, f := range h.bus.frames(t)[mark:] {
					if f.Recent != nil {
						t.Errorf("frame %q carries recent %+v, want the list unchanged", f.OpID, f.Recent.Items)
					}
				}
				if _, deleted := h.teardown.deletedByChain[draft]; !deleted {
					t.Error("the draft's close ran no delete-grade teardown")
				}
			})
		}
	}
}

// closedBot seeds a started Bot that was closed, paused as given, with no tab.
func closedBot(t *testing.T, store chatStore, id marotte.ChatID, paused bool) {
	t.Helper()
	seedBot(t, store, id, "CI watcher", &marotte.Bot{Goal: "Keep CI green.", StartedAt: 1, ClosedAt: 2, Paused: paused})
}

func TestOpenTab_EveryReopenOfAClosedBotClearsItsCloseAndKeepsItsPause(t *testing.T) {
	doors := []struct {
		open func(t *testing.T, mem *Membership)
		name string
	}{
		{name: "Recently closed", open: func(t *testing.T, mem *Membership) {
			if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Owns: true}, "op-reopen"); err != nil {
				t.Fatalf("OpenTab(c-bot) = %v", err)
			}
		}},
		{name: "History", open: func(t *testing.T, mem *Membership) {
			cmd := &marotte.ClientCommand{Type: marotte.CmdOpenTab, Payload: []byte(`{"kind":"chat","ref":"c-bot","owns":true,"op_id":"op-history"}`)}
			if _, err := cmdOpenTab(t.Context(), mem, cmd); err != nil {
				t.Fatalf("open_tab(c-bot) = %v", err)
			}
		}},
		{name: "a run tab it launched", open: func(t *testing.T, mem *Membership) {
			if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
				t.Fatalf("OpenTab(run) = %v", err)
			}
		}},
	}
	for _, door := range doors {
		for _, paused := range []bool{false, true} {
			t.Run(door.name+map[bool]string{false: " of a running Bot", true: " of a paused Bot"}[paused], func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				st, err := tabs.NewStore(t.TempDir())
				if err != nil {
					t.Fatalf("Setup: open tab store: %v", err)
				}
				bus := &tabBus{}
				mem := newMembership(&membershipDeps{
					Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: st, Bus: bus,
					Runs: &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-bot"}},
				})
				closedBot(t, store, "c-bot", paused)
				closeListed(t, mem, "c-bot")
				if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
					c.Bot.ClosedAt = 2
					return true
				}); err != nil {
					t.Fatal(err)
				}

				door.open(t, mem)

				open, _ := st.List()
				botTabs := tabsForChat(open, "c-bot")
				if len(botTabs) != 1 {
					t.Fatalf("the Bot has %d tabs, want one: %v", len(botTabs), open)
				}
				if tab := botTabs[0]; !tab.Bot || tab.Parent != "" {
					t.Errorf("the Bot's tab = bot %v parent %q, want a top-level Bot tab", tab.Bot, tab.Parent)
				}
				c := botOf(t, store, "c-bot")
				if c.Bot.ClosedAt != 0 {
					t.Errorf("closed_at = %d after the reopen, want 0", c.Bot.ClosedAt)
				}
				if c.Bot.Paused != paused {
					t.Errorf("paused = %v after the reopen, want %v as before the close", c.Bot.Paused, paused)
				}
				if got := refsOf(st.Recent()); slices.Contains(got, "c-bot") {
					t.Errorf("Recent() = %v, want the reopened Bot gone from it", got)
				}
			})
		}
	}
}

func TestOpenTab_AClosedBotsReopenAtTheTabLimitKeepsItClosed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	closedBot(t, store, "c-bot", false)
	fillTabs(t, mem, tabs.MaxOpenTabs)

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Owns: true}, "op-reopen")

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonTabsFull {
		t.Errorf("status %d reason %q, want 409 tabs_full", statusOf(err), reasonOf(err))
	}
	if open, _ := st.List(); len(tabsForChat(open, "c-bot")) != 0 {
		t.Error("a refused reopen opened the Bot's tab")
	}
	if c := botOf(t, store, "c-bot"); c.Bot.ClosedAt != 2 {
		t.Errorf("closed_at = %d after a refused reopen, want 2 kept", c.Bot.ClosedAt)
	}
}
