package command

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func setPausedReq(t *testing.T, id marotte.ChatID, paused bool) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.BotSetPausedCommand{Paused: paused})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdBotSetPaused, ChatID: id, Payload: payload}
}

// botUpdates is every chat_updated header the bus carried for id, in order.
func botUpdates(t *testing.T, bus *tabBus, id marotte.ChatID) []marotte.ChatHeader {
	t.Helper()
	bus.mu.Lock()
	defer bus.mu.Unlock()
	var out []marotte.ChatHeader
	for _, evt := range bus.events {
		if evt.Type != marotte.EventChatUpdated || evt.ChatID != id {
			continue
		}
		h, ok := evt.Payload.(marotte.ChatHeader)
		if !ok {
			t.Fatalf("chat_updated payload = %T, want marotte.ChatHeader", evt.Payload)
		}
		out = append(out, h)
	}
	return out
}

func TestCmdBotSetPaused_PausesAndResumesAStartedBot(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1})
	bus := &tabBus{}
	store.Bus = bus
	roles, _ := newBotRoles(t, store)

	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", true)); err != nil {
		t.Fatalf("bot_set_paused {paused: true} = %d %v, want 200", statusOf(err), err)
	}
	if c := botOf(t, store, "c-bot"); !c.Bot.Paused {
		t.Error("the Bot is not paused")
	}
	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", false)); err != nil {
		t.Fatalf("bot_set_paused {paused: false} = %d %v, want 200", statusOf(err), err)
	}
	if c := botOf(t, store, "c-bot"); c.Bot.Paused {
		t.Error("the Bot is still paused after the resume")
	}

	updates := botUpdates(t, bus, "c-bot")
	if len(updates) != 2 {
		t.Fatalf("chat_updated sent %d times, want once per change", len(updates))
	}
	if got := updates[0].Bot; got == nil || got.State != "paused" {
		t.Errorf("the pause's header summary = %+v, want state paused", got)
	}
	if got := updates[1].Bot; got == nil || got.State != "active" {
		t.Errorf("the resume's header summary = %+v, want state active", got)
	}
}

func TestCmdBotSetPaused_TheStateItHoldsWritesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1, Paused: true})
	bus := &tabBus{}
	store.Bus = bus
	roles, _ := newBotRoles(t, store)

	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", true)); err != nil {
		t.Fatalf("bot_set_paused = %d %v, want 200", statusOf(err), err)
	}

	if n := len(botUpdates(t, bus, "c-bot")); n != 0 {
		t.Errorf("a pause of a paused Bot sent %d chat_updated, want none", n)
	}
}

func TestCmdBotSetPaused_AClosedBotStaysClosed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1, ClosedAt: 5})
	roles, _ := newBotRoles(t, store)

	if _, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-bot", true)); err != nil {
		t.Fatalf("bot_set_paused = %d %v, want 200", statusOf(err), err)
	}

	if c := botOf(t, store, "c-bot"); !c.Bot.Paused || c.Bot.ClosedAt != 5 {
		t.Errorf("paused %v closed_at %d, want paused with closed_at 5 kept", c.Bot.Paused, c.Bot.ClosedAt)
	}
}

func TestCmdBotSetPaused_RefusesWhatIsNoStartedBot(t *testing.T) {
	cases := []struct {
		seed   func(t *testing.T, store *testsupport.InMemoryChatStore)
		name   string
		reason string
	}{
		{name: "a draft", reason: "bot_draft", seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
			seedBot(t, store, "c-x", "New Bot", &marotte.Bot{})
		}},
		{name: "a regular chat", reason: "bot_not_found", seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
			seedNamed(t, store, "c-x", "Plain")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			tc.seed(t, store)
			roles, _ := newBotRoles(t, store)

			_, err := cmdBotSetPaused(t.Context(), roles, setPausedReq(t, "c-x", true))

			if statusOf(err) != http.StatusConflict || reasonOf(err) != tc.reason {
				t.Errorf("status %d reason %q, want 409 %s", statusOf(err), reasonOf(err), tc.reason)
			}
			if c, _ := store.Get(t.Context(), "c-x"); c.Bot != nil && c.Bot.Paused {
				t.Error("a refused pause paused the record")
			}
		})
	}
}
