package command

import (
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// forkFrom forks c-parent, seeded by seedParent then bot, into c-tangent and returns the session/fork meta and the
// tangent's record.
func forkFrom(t *testing.T, bot *marotte.Bot) (map[string]any, *marotte.Chat) {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	if bot != nil {
		if _, err := store.Mutate(t.Context(), "c-parent", func(c *marotte.Chat, _ bool) bool {
			c.CurrentModeID = marotte.BotRoleID
			c.Bot = bot
			return true
		}); err != nil {
			t.Fatalf("seed the Bot: %v", err)
		}
	}
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br, "c-parent")

	_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tangent chat was not created")
	}
	return forkKiroMeta(t, br), c
}

// TestCmdForkChat_ABotsTangentStartsOnTheDefaultMode pins that a tangent from a Bot is a regular chat, so KAS persists
// the fork on the default mode (modeId beside createdReason) and the record names it, never the role the tangent's
// session cannot load.
func TestCmdForkChat_ABotsTangentStartsOnTheDefaultMode(t *testing.T) {
	kiro, c := forkFrom(t, &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1, Memory: true})

	if kiro["modeId"] != marotte.DefaultModeID {
		t.Errorf("_meta.kiro.modeId = %v, want %q", kiro["modeId"], marotte.DefaultModeID)
	}
	if kiro["createdReason"] != marotte.CreatedReasonTangent {
		t.Errorf("createdReason = %v, want %q beside the mode", kiro["createdReason"], marotte.CreatedReasonTangent)
	}
	if c.CurrentModeID != marotte.DefaultModeID {
		t.Errorf("the tangent's CurrentModeID = %q, want %q", c.CurrentModeID, marotte.DefaultModeID)
	}
	if c.Bot != nil {
		t.Errorf("the tangent carries a bot block %+v; a tangent is a regular chat", c.Bot)
	}
}

// TestCmdForkChat_ARegularTangentSendsNoMode pins that a regular parent's fork is unchanged: KAS keeps the live
// parent's mode when the meta names none.
func TestCmdForkChat_ARegularTangentSendsNoMode(t *testing.T) {
	kiro, c := forkFrom(t, nil)

	if got, ok := kiro["modeId"]; ok {
		t.Errorf("_meta.kiro.modeId = %v; a regular parent's fork names no mode", got)
	}
	if c.CurrentModeID != "plan" {
		t.Errorf("the tangent's CurrentModeID = %q, want the parent's plan", c.CurrentModeID)
	}
}
