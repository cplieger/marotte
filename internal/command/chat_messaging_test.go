package command

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// appliedChats records every chat ApplyChatMessaging was handed, in order.
type appliedChats struct{ chats []marotte.ChatID }

func (a *appliedChats) ApplyChatMessaging(_ context.Context, chatID marotte.ChatID) {
	a.chats = append(a.chats, chatID)
}

func chatMessagingReq(t *testing.T, chatID marotte.ChatID, enabled bool) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetChatMessagingCommand{Enabled: enabled})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdSetChatMessaging, ChatID: chatID, Payload: payload}
}

func TestCmdSetChatMessaging_PersistsTheChoiceAndAppliesIt(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "a chat"; return true }); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	applier := &appliedChats{}

	for _, enabled := range []bool{false, true} {
		if _, err := cmdSetChatMessaging(t.Context(), store, applier, chatMessagingReq(t, "c1", enabled)); statusOf(err) != http.StatusOK {
			t.Fatalf("set_chat_messaging enabled=%v: status = %d (%s), want 200", enabled, statusOf(err), errText(err))
		}
		if c, _ := store.Get(t.Context(), "c1"); c.MessagingOff == enabled {
			t.Errorf("after enabled=%v, messaging_off = %v, want %v", enabled, c.MessagingOff, !enabled)
		}
	}
	if want := []marotte.ChatID{"c1", "c1"}; !slices.Equal(applier.chats, want) {
		t.Errorf("the applier was handed %v, want %v", applier.chats, want)
	}
}

func TestCmdSetChatMessaging_CreatesTheRecordOfANewChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()

	if _, err := cmdSetChatMessaging(t.Context(), store, &appliedChats{}, chatMessagingReq(t, "c-new", false)); statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", statusOf(err), errText(err))
	}

	c, ok := store.Get(t.Context(), "c-new")
	if !ok || !c.MessagingOff {
		t.Errorf("record after an off before the first message = %+v (found %v), want one with messaging_off", c, ok)
	}
}
