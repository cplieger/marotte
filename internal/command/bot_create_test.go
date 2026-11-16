package command

import (
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// createBot sends create_chat {bot: true} with opID through mem and answers the new chat's id.
func createBot(t *testing.T, mem *Membership, opID string) marotte.ChatID {
	t.Helper()
	body, err := cmdCreateChat(t.Context(), mem,
		createReq(t, "", marotte.CreateChatCommand{OpID: opID, Model: "model-a-5", Bot: true}))
	if statusOf(err) != http.StatusOK {
		t.Fatalf("create_chat {bot: true} = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	return chatIDOfResponse(t, body)
}

func TestCmdCreateChat_ABotIsADraftOnItsRoleWithAUserName(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	id := createBot(t, mem, "op-bot")

	c, ok := store.Get(t.Context(), id)
	if !ok {
		t.Fatalf("the Bot %q is not in the store", id)
	}
	if c.Bot == nil {
		t.Fatal("the record has no bot block")
	}
	if c.Bot.StartedAt != 0 {
		t.Errorf("started_at = %d, want 0: a new Bot is a draft", c.Bot.StartedAt)
	}
	if c.CurrentModeID != marotte.BotRoleID {
		t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, marotte.BotRoleID)
	}
	if c.Name != "New Bot" || !c.NameSetByUser {
		t.Errorf("name = %q set by user %v, want \"New Bot\" set by user", c.Name, c.NameSetByUser)
	}
	if c.Model != "model-a-5" {
		t.Errorf("model = %q, want the payload's", c.Model)
	}
}

func TestCmdCreateChat_ABotOpensATopLevelBotTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)

	id := createBot(t, mem, "op-bot")

	open, _ := st.List()
	if len(open) != 1 {
		t.Fatalf("the set holds %d tabs, want the Bot's one", len(open))
	}
	tab := open[0]
	if tab.Kind != marotte.TabKindChat || tab.Ref != string(id) {
		t.Errorf("tab = %s %q, want the chat tab of %q", tab.Kind, tab.Ref, id)
	}
	if !tab.Bot {
		t.Error("the tab has no bot fact")
	}
	if !tab.Owns {
		t.Error("the tab does not own its chat")
	}
	if tab.Pinned || tab.Parent != "" {
		t.Errorf("pinned %v parent %q, want an unpinned top-level tab", tab.Pinned, tab.Parent)
	}
}

func TestCmdCreateChat_ARegularChatsTabHasNoBotFact(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)

	if _, err := cmdCreateChat(t.Context(), mem, createReq(t, "", marotte.CreateChatCommand{OpID: "op-chat"})); err != nil {
		t.Fatalf("create_chat = %v", err)
	}

	open, _ := st.List()
	if len(open) != 1 || open[0].Bot {
		t.Errorf("tabs = %+v, want one tab with no bot fact", open)
	}
	if c, _ := store.Get(t.Context(), marotte.ChatID(open[0].Ref)); c.Bot != nil {
		t.Errorf("a regular chat carries a bot block %+v", c.Bot)
	}
}

func TestCmdCreateChat_ABotsReplayResolvesToTheSameDraft(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)

	first := createBot(t, mem, "op-bot")
	second := createBot(t, mem, "op-bot")

	if first != second {
		t.Errorf("the replay answered %q, want the first attempt's %q", second, first)
	}
	if got := storedIDs(t, store); len(got) != 1 {
		t.Errorf("the store holds %v, want one Bot", got)
	}
	if open, _ := st.List(); len(open) != 1 {
		t.Errorf("the set holds %d tabs, want one", len(open))
	}
}

func TestCmdCreateChat_ABotAtTheTabLimitIsRefused(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	fillTabs(t, mem, tabs.MaxOpenTabs)
	before := len(storedChatIDs(t, store))

	_, err := cmdCreateChat(t.Context(), mem, createReq(t, "", marotte.CreateChatCommand{OpID: "op-full", Bot: true}))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonTabsFull {
		t.Errorf("status %d reason %q, want 409 tabs_full", statusOf(err), reasonOf(err))
	}
	if got := len(storedChatIDs(t, store)); got != before {
		t.Errorf("the store holds %d chats, want the %d it held", got, before)
	}
}
