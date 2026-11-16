package command

// A Bot's tab is always top level and never pinned, so no close or delete of another chat reaches it.

import (
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func TestSetPinned_ABotsTabIsRefusedEitherWay(t *testing.T) {
	for _, pinned := range []bool{true, false} {
		t.Run(map[bool]string{true: "pin", false: "unpin"}[pinned], func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			mem, st, bus := newTabbedMembership(t, store)
			seedStartedBot(t, store, "c-bot", "CI watcher")
			tab := openChatTab(t, mem, "c-bot", "")
			_, before := st.List()
			mark := len(bus.frames(t))

			_, err := mem.setPinned(t.Context(), tab.ID, pinned, "op-pin")

			if statusOf(err) != http.StatusConflict || reasonOf(err) != "bot_tab" {
				t.Errorf("status %d reason %q, want 409 bot_tab", statusOf(err), reasonOf(err))
			}
			if _, v := st.List(); v != before {
				t.Errorf("version = %d, want %d unchanged", v, before)
			}
			if n := len(bus.frames(t)) - mark; n != 0 {
				t.Errorf("a refused pin emitted %d frames, want none", n)
			}
		})
	}
}

func TestSetPinned_ATabUnderABotPinsAsToday(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	seedNamed(t, store, "c-tangent", "Side question")
	bot := openChatTab(t, mem, "c-bot", "")
	tangent := openChatTab(t, mem, "c-tangent", bot.ID)

	if _, err := mem.setPinned(t.Context(), tangent.ID, true, "op-pin"); err != nil {
		t.Fatalf("setPinned(tangent) = %v, want the tab under the Bot pinned", err)
	}

	if sub, _ := mem.subject(tangent.ID); !sub.Pinned {
		t.Error("the tangent under the Bot is not pinned")
	}
}

func TestReparent_ABotsTabIsRefusedWithNoFrame(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	seedNamed(t, store, "c-chat", "Plain")
	bot := openChatTab(t, mem, "c-bot", "")
	other := openChatTab(t, mem, "c-chat", "")
	_, before := st.List()
	mark := len(bus.frames(t))

	_, _, err := mem.reparent(t.Context(), bot.ID, other.ID, "op-nest")

	if statusOf(err) != http.StatusConflict || reasonOf(err) != "bot_tab" {
		t.Errorf("status %d reason %q, want 409 bot_tab", statusOf(err), reasonOf(err))
	}
	if _, v := st.List(); v != before {
		t.Errorf("version = %d, want %d unchanged", v, before)
	}
	if n := len(bus.frames(t)) - mark; n != 0 {
		t.Errorf("a refused reparent emitted %d frames, want none", n)
	}
	if sub, _ := mem.subject(bot.ID); sub.Parent != "" {
		t.Errorf("the Bot's tab parent = %q, want top level", sub.Parent)
	}
}

func TestReparent_ASpecTabHangsUnderABotAsToday(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	seedStartedBot(t, store, "c-bot", "CI watcher")
	bot := openChatTab(t, mem, "c-bot", "")
	spec, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindSpec, Ref: "/workspace/.kiro/specs/ci"}, "op-spec")
	if err != nil {
		t.Fatalf("Setup: open the spec tab: %v", err)
	}

	changed, _, err := mem.reparent(t.Context(), spec.Subject.ID, bot.ID, "op-nest")

	if err != nil || changed.Parent != bot.ID {
		t.Errorf("reparent(spec under Bot) = parent %q, %v; want %q", changed.Parent, err, bot.ID)
	}
}

// botBeside opens a Bot through an opener that names a regular chat's tab as its parent, and a tangent under that
// regular chat, and answers the regular chat's tab and the Bot's.
func botBeside(t *testing.T, h *botHost) (chatTab, botTab marotte.TabSubject) {
	t.Helper()
	seedChatNamed(t, h.chats, "c-chat", "Plain")
	seedChatNamed(t, h.chats, "c-tangent", "Side question")
	seedStartedBot(t, h.chats, "c-bot", "CI watcher")
	chatTab = openChatTab(t, h.mem, "c-chat", "")
	openChatTab(t, h.mem, "c-tangent", chatTab.ID)
	botTab = openChatTab(t, h.mem, "c-bot", chatTab.ID)
	return chatTab, botTab
}

func botKeepsItsTab(t *testing.T, h *botHost, botTab string) {
	t.Helper()
	if !h.mem.tabOpen(botTab) {
		t.Fatal("the Bot's tab closed with another chat")
	}
	c := botOf(t, h.chats, "c-bot")
	if _, held := c.Keep[marotte.KeyTab]; !held {
		t.Error("the Bot lost its tab fact")
	}
	if c.Bot.ClosedAt != 0 {
		t.Errorf("closed_at = %d, want 0: the Bot was not closed", c.Bot.ClosedAt)
	}
}

func TestCloseTab_ARegularChatsCloseLeavesEveryBotOpen(t *testing.T) {
	for name, retention := range map[string]retentionRead{"days kept 7": retentionOn, "days kept 0": retentionOff} {
		t.Run(name, func(t *testing.T) {
			h := newBotHost(t, retention)
			chatTab, botTab := botBeside(t, h)
			predicted := subjectIDs(h.st.Subtree(chatTab.ID))

			closed, _, err := h.mem.closeTab(t.Context(), chatTab.ID, "op-close", true, nil)
			if err != nil {
				t.Fatalf("closeTab(chat) = %v", err)
			}

			botKeepsItsTab(t, h, botTab.ID)
			got := slices.Sorted(slices.Values(subjectIDs(closed)))
			if want := slices.Sorted(slices.Values(predicted)); !slices.Equal(got, want) {
				t.Errorf("closed %v, want the subtree the close impact predicted %v", got, want)
			}
			if slices.Contains(predicted, botTab.ID) {
				t.Error("the regular chat's subtree holds the Bot's tab")
			}
		})
	}
}

func TestDeleteChatAndCloseTabs_ARegularChatsDeleteLeavesEveryBotOpen(t *testing.T) {
	for name, retention := range map[string]retentionRead{"days kept 7": retentionOn, "days kept 0": retentionOff} {
		t.Run(name, func(t *testing.T) {
			h := newBotHost(t, retention)
			_, botTab := botBeside(t, h)

			if err := h.mem.DeleteChatAndCloseTabs(t.Context(), "c-chat", "op-del", nil); err != nil {
				t.Fatalf("DeleteChatAndCloseTabs(c-chat) = %v", err)
			}

			botKeepsItsTab(t, h, botTab.ID)
		})
	}
}
