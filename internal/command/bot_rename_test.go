package command

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func TestCmdRenameChat_ABotsRenameMarksItForReload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	f := &botFakes{}

	if _, err := cmdRenameChat(t.Context(), store, f, f, renameReq(t, "c-bot", "CI watcher")); err != nil {
		t.Fatalf("rename_chat = %v", err)
	}

	if !slices.Equal(f.reloads, []marotte.ChatID{"c-bot"}) {
		t.Errorf("reload marks = %v, want one: the role's prompt carries the name", f.reloads)
	}
}

func TestCmdRenameChat_ARegularChatsRenameMarksNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRenameChat(t, store, "c1", "sess-1")
	f := &botFakes{}

	if _, err := cmdRenameChat(t.Context(), store, f, f, renameReq(t, "c1", "Named")); err != nil {
		t.Fatalf("rename_chat = %v", err)
	}

	if len(f.reloads) != 0 {
		t.Errorf("reload marks = %v, want none for a regular chat", f.reloads)
	}
}

func TestCmdRenameChat_AnUnchangedBotNameMarksNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	f := &botFakes{}

	if _, err := cmdRenameChat(t.Context(), store, f, f, renameReq(t, "c-bot", "Watcher")); err != nil {
		t.Fatalf("rename_chat = %v", err)
	}

	if len(f.reloads) != 0 {
		t.Errorf("reload marks = %v, want none for the name it already had", f.reloads)
	}
}
