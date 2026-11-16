package composition

// The boot heal makes each Bot's record and its tab agree once the chat scan is complete: a lost tab reopens, a
// closed Bot stays closed, and a reopen whose record write was lost is finished.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// seedBotRecord writes a Bot record: started unless draft, closed at closedAt (0 is open).
func seedBotRecord(t *testing.T, s *chat.Store, id string, draft bool, closedAt int64) {
	t.Helper()
	started := int64(1_700_000_000_000)
	if draft {
		started = 0
	}
	if _, err := s.Mutate(t.Context(), marotte.ChatID(id), func(c *marotte.Chat, _ bool) bool {
		c.Name, c.NameSetByUser, c.CurrentModeID = id, true, marotte.BotRoleID
		c.Bot = &marotte.Bot{Goal: "Keep CI green.", StartedAt: started, ClosedAt: closedAt}
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the Bot %s: %v", id, err)
	}
}

func chatTabsOf(r *keepRig, id string) []marotte.TabSubject {
	open, _ := r.tabs.List()
	var out []marotte.TabSubject
	for _, tab := range open {
		if tab.Kind == marotte.TabKindChat && tab.Ref == id {
			out = append(out, tab)
		}
	}
	return out
}

func closedAtOf(t *testing.T, r *keepRig, id string) int64 {
	t.Helper()
	c, found, err := r.chats.Lookup(t.Context(), marotte.ChatID(id))
	if err != nil || !found || c.Bot == nil {
		t.Fatalf("Lookup(%s) = found %v, %v, want a Bot record", id, found, err)
	}
	return c.Bot.ClosedAt
}

// wantHealedTab asserts id has one top-level Bot tab and holds its tab fact.
func wantHealedTab(t *testing.T, r *keepRig, id string) {
	t.Helper()
	got := chatTabsOf(r, id)
	if len(got) != 1 {
		t.Fatalf("%s has %d tabs after the heal, want one", id, len(got))
	}
	if !got[0].Bot || got[0].Parent != "" || !got[0].Owns {
		t.Errorf("%s's healed tab = bot %v parent %q owns %v, want an owning top-level Bot tab", id, got[0].Bot, got[0].Parent, got[0].Owns)
	}
	if c, _, _ := r.chats.Lookup(t.Context(), marotte.ChatID(id)); c != nil {
		if _, held := c.Keep[marotte.KeyTab]; !held {
			t.Errorf("%s's healed tab holds no tab fact, so the purge would not see it open", id)
		}
	}
}

func TestKeepRound_HealsEachBotsTabFromItsRecord(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedBotRecord(t, first.chats, "c-lost", false, 0)
	seedBotRecord(t, first.chats, "c-lost-draft", true, 0)
	seedBotRecord(t, first.chats, "c-closed", false, 5)
	seedBotRecord(t, first.chats, "c-reopened", false, 5)
	seedChat(t, first.chats, "c-plain", "")
	if _, _, _, err := first.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-reopened", Owns: true, Bot: true}); err != nil {
		t.Fatalf("Setup: open c-reopened's tab: %v", err)
	}
	r := newKeepRig(t, root)

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}

	wantHealedTab(t, r, "c-lost")
	wantHealedTab(t, r, "c-lost-draft")
	if got := chatTabsOf(r, "c-closed"); len(got) != 0 {
		t.Errorf("the closed Bot has tabs %+v, want none: the user closed it", got)
	}
	if got := closedAtOf(t, r, "c-closed"); got != 5 {
		t.Errorf("the closed Bot's closed_at = %d, want 5 kept", got)
	}
	if got := closedAtOf(t, r, "c-reopened"); got != 0 {
		t.Errorf("an open Bot's closed_at = %d after the heal, want 0: its reopen's write was lost", got)
	}
	if got := chatTabsOf(r, "c-reopened"); len(got) != 1 {
		t.Errorf("c-reopened has %d tabs, want its one", len(got))
	}
	if got := chatTabsOf(r, "c-plain"); len(got) != 0 {
		t.Errorf("a regular chat with no tab gained %+v, want none", got)
	}
}

func TestKeepRound_AnUnreadTabSetReopensEveryOpenBotAndNoClosedOne(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedBotRecord(t, first.chats, "c-open-a", false, 0)
	seedBotRecord(t, first.chats, "c-open-b", false, 0)
	seedBotRecord(t, first.chats, "c-closed", false, 5)
	if err := os.WriteFile(filepath.Join(root, "tabs.json"), []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("Setup: write tabs.json: %v", err)
	}
	r := newKeepRig(t, root)
	if r.tabs.Unread() == "" {
		t.Fatal("Setup: the tab store read a garbage arrangement")
	}

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}

	wantHealedTab(t, r, "c-open-a")
	wantHealedTab(t, r, "c-open-b")
	if got := chatTabsOf(r, "c-closed"); len(got) != 0 {
		t.Errorf("the closed Bot has tabs %+v after the moved-aside set healed, want none", got)
	}
}

func TestKeepRound_HealsABotsTabPastTheTabLimit(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedBotRecord(t, first.chats, "c-lost", false, 0)
	for i := range tabs.MaxOpenTabs {
		if _, _, _, err := first.tabs.Open(t.Context(), marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "/workspace/f" + strconv.Itoa(i) + ".go"}); err != nil {
			t.Fatalf("Setup: fill tab %d: %v", i, err)
		}
	}
	r := newKeepRig(t, root)

	if !r.round(t) {
		t.Fatal("keepRound did not finish")
	}

	wantHealedTab(t, r, "c-lost")
	if open, _ := r.tabs.List(); len(open) != tabs.MaxOpenTabs+1 {
		t.Errorf("the set holds %d tabs, want %d", len(open), tabs.MaxOpenTabs+1)
	}
}

// failingHeal is the real coordinator whose Bot heal reports one Bot it could not heal.
type failingHeal struct{ keepMembership }

func (failingHeal) HealBotTabs(context.Context, []marotte.ChatID) command.BotHeal {
	return command.BotHeal{Failed: 1}
}

func TestKeepRound_AFailedBotHealKeepsThePassRunning(t *testing.T) {
	r := newKeepRig(t, t.TempDir())
	seedBotRecord(t, r.chats, "c-lost", false, 0)
	r.deps.membership = failingHeal{r.deps.membership}

	if r.round(t) {
		t.Error("keepRound reported done with a Bot left unhealed, want a retry")
	}
}

func TestKeepRound_HealsNothingUntilTheChatScanIsComplete(t *testing.T) {
	root := t.TempDir()
	first := newKeepRig(t, root)
	seedBotRecord(t, first.chats, "c-lost", false, 0)
	seedChat(t, first.chats, "c-unread", "sess_a")
	path := filepath.Join(first.chatDir, "c-unread", "chat.json")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("Setup: corrupt: %v", err)
	}
	r := newKeepRig(t, root)

	if r.round(t) {
		t.Fatal("keepRound reported done with an unreadable record left")
	}
	if got := chatTabsOf(r, "c-lost"); len(got) != 0 {
		t.Errorf("the heal opened %+v from an incomplete scan, want nothing yet", got)
	}

	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !r.round(t) {
		t.Fatal("keepRound did not finish once every record was readable")
	}
	wantHealedTab(t, r, "c-lost")
}
