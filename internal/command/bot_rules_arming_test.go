package command

// Arming: a Bot's triggers are armed while its rule is enabled and the Bot is started, not paused and not closed.
// Every move out of that state drops what waits (except a prompt already queued in the chat, and at a pause a Run now
// press, the user's own act); every move into it stamps a new generation and resets the anchors, so nothing fires for
// the time the Bot could not run. Each command tells the rule engine to reindex the Bot after its write.

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// watchedBot is a started Bot whose one rule r-watch has every 60 trigger t-watch armed at 1000, last evaluated at
// 5000, with queueOf's events waiting.
func watchedBot(b marotte.Bot) *marotte.Bot {
	r := storedRule("watch", 1_000, 5_000)
	r.Status.Queue = queueOf("t-watch")
	b.Goal = "Keep CI green."
	if b.StartedAt == 0 {
		b.StartedAt = 1
	}
	b.Rules = []rule.Rule{r}
	return &b
}

func watchStatus(t *testing.T, store chatStore) rule.Status {
	t.Helper()
	return ruleOf(t, botOf(t, store, "c-bot"), "r-watch").Status
}

func TestCmdBotSetPaused_PauseDisarmsAndDropsTheQueueAndResumeRearms(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
	roles, eng := newRuleRoles(t, store)

	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", true)); err != nil {
		t.Fatalf("pause = %v", err)
	}

	st := watchStatus(t, store)
	if got := eventIDs(st.Queue); !slices.Equal(got, []string{"manual:op-press", "every:t-watch:3"}) || st.Dropped != 2 {
		t.Errorf("after the pause queue %v dropped %d; want the Run now press and the prompt already in the chat kept, "+
			"the trigger's two events dropped and counted", got, st.Dropped)
	}
	if h := botOf(t, store, "c-bot").Header(); h.Bot.NextAt != 0 {
		t.Errorf("a paused Bot's next_at = %d, want none", h.Bot.NextAt)
	}

	before := time.Now()
	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", false)); err != nil {
		t.Fatalf("resume = %v", err)
	}
	after := time.Now()

	trig := watchStatus(t, store).Triggers["t-watch"]
	armedWindow(t, "after the resume", trig, 1_000, before, after)
	if h := botOf(t, store, "c-bot").Header(); h.Bot.NextAt != trig.ArmedAt+time.Hour.Milliseconds() {
		t.Errorf("next_at = %d, want an hour after the resume (%d): nothing fires for the paused time",
			h.Bot.NextAt, trig.ArmedAt+time.Hour.Milliseconds())
	}
	if got := eng.reindexes(); !slices.Equal(got, []marotte.ChatID{"c-bot", "c-bot"}) {
		t.Errorf("reindexed %v, want c-bot after the pause and after the resume", got)
	}
}

func TestCmdBotSetPaused_ResumingAClosedBotArmsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{Paused: true, ClosedAt: 2}))
	roles, _ := newRuleRoles(t, store)

	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", false)); err != nil {
		t.Fatalf("resume = %v", err)
	}

	if got := watchStatus(t, store).Triggers["t-watch"]; got.ArmedAt != 1_000 || got.LastAt != 5_000 {
		t.Errorf("trigger state %+v, want it untouched: a closed Bot stays disarmed until reopened", got)
	}
}

func TestCloseTab_ABotsCloseDisarmsAndDropsItsQueue(t *testing.T) {
	h := newBotHost(t, retentionOn)
	seedBot(t, h.chats, "c-bot", "CI watcher", watchedBot(marotte.Bot{}))
	eng := &fakeRuleEngine{}
	h.mem.SetRuleIndex(eng)
	tab := openChatTab(t, h.mem, "c-bot", "")

	if _, _, err := h.mem.closeTab(t.Context(), tab.ID, "op-close", true, nil); err != nil {
		t.Fatalf("closeTab(bot) = %v", err)
	}

	st := watchStatus(t, h.chats)
	if got := eventIDs(st.Queue); !slices.Equal(got, []string{"every:t-watch:3"}) || st.Dropped != 3 {
		t.Errorf("after the close queue %v dropped %d; want only the prompt already in the chat kept and 3 dropped",
			got, st.Dropped)
	}
	if got := eng.reindexes(); !slices.Equal(got, []marotte.ChatID{"c-bot"}) {
		t.Errorf("reindexed %v, want c-bot once after the close", got)
	}
}

func TestOpenTab_EveryReopenOfAClosedBotRearmsWithNothingFiredForTheClosedTime(t *testing.T) {
	doors := []struct {
		open func(t *testing.T, mem *Membership)
		name string
	}{
		{name: "History or Recently closed", open: func(t *testing.T, mem *Membership) {
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
				mem := newMembership(&membershipDeps{
					Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: st, Bus: &tabBus{},
					Runs: &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-bot"}},
				})
				eng := &fakeRuleEngine{}
				mem.SetRuleIndex(eng)
				seedBot(t, store, "c-bot", "CI watcher", watchedBot(marotte.Bot{Paused: paused}))
				closeListed(t, mem, "c-bot")
				if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
					c.Bot.ClosedAt = 2
					c.Bot.Rules[0].Status.Triggers["t-watch"] = rule.TriggerState{ArmedAt: 1_000, LastAt: 5_000}
					return true
				}); err != nil {
					t.Fatal(err)
				}
				mark := len(eng.reindexes())

				before := time.Now()
				door.open(t, mem)
				after := time.Now()

				c := botOf(t, store, "c-bot")
				if c.Bot.ClosedAt != 0 {
					t.Fatalf("closed_at = %d, want the reopen to clear it", c.Bot.ClosedAt)
				}
				trig := ruleOf(t, c, "r-watch").Status.Triggers["t-watch"]
				if paused {
					if trig.ArmedAt != 1_000 || c.Header().Bot.NextAt != 0 {
						t.Errorf("trigger %+v next_at %d, want a paused Bot left disarmed by its reopen", trig, c.Header().Bot.NextAt)
					}
					return
				}
				armedWindow(t, "after the reopen", trig, 1_000, before, after)
				if got := c.Header().Bot.NextAt; got != trig.ArmedAt+time.Hour.Milliseconds() {
					t.Errorf("next_at = %d, want an hour after the reopen (%d)", got, trig.ArmedAt+time.Hour.Milliseconds())
				}
				if got := eng.reindexes()[mark:]; !slices.Equal(got, []marotte.ChatID{"c-bot"}) {
					t.Errorf("reindexed %v after the reopen, want c-bot once", got)
				}
			})
		}
	}
}

func TestHealBotTabs_ALostReopenRearms(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	eng := &fakeRuleEngine{}
	mem.SetRuleIndex(eng)
	seedBot(t, store, "c-bot", "CI watcher", watchedBot(marotte.Bot{}))
	openChatTab(t, mem, "c-bot", "")
	if _, err := store.Mutate(t.Context(), "c-bot", func(c *marotte.Chat, _ bool) bool {
		c.Bot.ClosedAt = 2
		return true
	}); err != nil {
		t.Fatal(err)
	}
	mark := len(eng.reindexes())

	before := time.Now()
	heal := mem.HealBotTabs(t.Context(), []marotte.ChatID{"c-bot"})
	after := time.Now()

	if heal.Cleared != 1 {
		t.Fatalf("heal = %+v, want the open tab's closed_at cleared", heal)
	}
	armedWindow(t, "after the heal", watchStatus(t, store).Triggers["t-watch"], 1_000, before, after)
	if got := eng.reindexes()[mark:]; !slices.Equal(got, []marotte.ChatID{"c-bot"}) {
		t.Errorf("reindexed %v after the heal, want c-bot once", got)
	}
}
