package command

import (
	"cmp"
	"net/http"
	"testing"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// TestCmdSetMode_ABotsRoleIsNeverSwitched pins that a Bot stays on its role and no other chat takes it, refused 409
// bot_mode before the live session is asked, so the record, the session and every pill keep the mode they hold.
func TestCmdSetMode_ABotsRoleIsNeverSwitched(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(c *marotte.Chat)
		mode     string
		wantMode string
	}{
		{
			name: "a Bot switched to another mode",
			seed: func(c *marotte.Chat) {
				c.CurrentModeID = marotte.BotRoleID
				c.Bot = &marotte.Bot{Goal: "g", StartedAt: 1}
			},
			mode:     "kiro_spec",
			wantMode: marotte.BotRoleID,
		},
		{
			name:     "a draft Bot switched to another mode",
			seed:     func(c *marotte.Chat) { c.CurrentModeID = marotte.BotRoleID; c.Bot = &marotte.Bot{} },
			mode:     marotte.DefaultModeID,
			wantMode: marotte.BotRoleID,
		},
		{
			name:     "a regular chat switched to the Bot role",
			seed:     func(c *marotte.Chat) { c.CurrentModeID = marotte.DefaultModeID },
			mode:     marotte.BotRoleID,
			wantMode: marotte.DefaultModeID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A chat"
				tc.seed(c)
				return true
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			br := &recordingBridge{sessionID: "s"}
			spy := &promptSpy{hostDouble: newBridgeHost(store, br)}

			_, err := cmdSetMode(t.Context(), spy, spy, spy, spy, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", tc.mode))

			if got := statusOf(err); got != http.StatusConflict {
				t.Errorf("status = %d, want %d", got, http.StatusConflict)
			}
			if got := reasonOf(err); got != "bot_mode" {
				t.Errorf("reason = %q, want bot_mode", got)
			}
			if br.callCount != 0 {
				t.Errorf("the session was asked %d time(s) (%s); a refused switch reaches no engine", br.callCount, br.gotMethod)
			}
			c, _ := store.Get(t.Context(), "c1")
			if c.CurrentModeID != tc.wantMode {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.wantMode)
			}
			for _, evt := range spy.events {
				if evt.Type == marotte.EventModeChanged {
					t.Error("a refused switch broadcast mode_changed")
				}
			}
		})
	}
}

// TestCmdSetMode_TheBotRoleIsRefusedOnAChatWithNoRecord pins that the auto-create path is no way around the refusal:
// a fresh chat whose first pick is the role is refused and no record is written.
func TestCmdSetMode_TheBotRoleIsRefusedOnAChatWithNoRecord(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)

	_, err := cmdSetMode(t.Context(), host, host, host, host, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", marotte.BotRoleID))

	if got := reasonOf(err); statusOf(err) != http.StatusConflict || got != "bot_mode" {
		t.Errorf("status %d reason %q, want 409 bot_mode", statusOf(err), got)
	}
	if _, ok := store.Get(t.Context(), "c1"); ok {
		t.Error("a refused pick created the chat's record")
	}
}
