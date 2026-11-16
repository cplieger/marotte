package agent

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func botSaveCmd(t *testing.T, chatID marotte.ChatID, p marotte.BotSaveCommand) marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return marotte.ClientCommand{Type: marotte.CmdBotSave, ChatID: chatID, Payload: payload}
}

func seedBotDraft(t *testing.T, cs *testChatStore, chatID marotte.ChatID) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name, c.NameSetByUser = marotte.DefaultBotName, true
		c.CurrentModeID = marotte.BotRoleID
		c.Bot = &marotte.Bot{}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", chatID, err)
	}
}

// TestBotSave_AStartOpensTheBotsFirstSessionOnItsRoleWithTheKickoff pins the registered bot_save: Create bot on a
// draft sends the kickoff through the prompt command, which opens the Bot's first session with its role and logs the
// prompt under "Started <name>" with no caller.
func TestBotSave_AStartOpensTheBotsFirstSessionOnItsRoleWithTheKickoff(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs := botRuntime(t, br)
	seedBotDraft(t, cs, "c1")

	rec := postCmd(t, h, botSaveCmd(t, "c1", marotte.BotSaveCommand{Name: "CI watcher", Goal: "Keep CI green.", Start: true}))

	if rec.Code != http.StatusOK {
		t.Fatalf("bot_save = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "kickoff_error") {
		t.Errorf("answer %s carries a kickoff error", rec.Body.String())
	}
	waitFor(t, func() bool { return len(closesOf(t, logOf(t, cs, "c1"))) == 1 })
	opens := opensOf(t, logOf(t, cs, "c1"))
	if len(opens) != 1 || opens[0].Prompt == nil {
		t.Fatalf("turn opens = %+v, want the kickoff's", opens)
	}
	if p := opens[0].Prompt; p.Label != "Started CI watcher" || p.Caller != nil || !strings.HasPrefix(p.Text, "You have just been set up.") {
		t.Errorf("kickoff = label %q caller %+v text %q, want the user's own labelled kickoff", p.Label, p.Caller, p.Text)
	}
	if opts := br.lastSessionOpts(); opts == nil || len(opts.CustomAgents) != 1 || opts.CustomAgents[0].ID != marotte.BotRoleID {
		t.Errorf("the first session's opts = %+v, want the Bot's role", opts)
	}
}

// TestMarkBotReload_AnEditedBotsNextMessageLoadsItsNewRole pins the runtime's reload mark: after a goal edit, the next
// message closes the resident session and loads it again with the edited role's prompt.
func TestMarkBotReload_AnEditedBotsNextMessageLoadsItsNewRole(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	reload := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs := botRuntime(t, br, reload)
	seedModeChat(t, cs, "c1", true, false)
	runPrompt(t, h, cs, "c1", "m1")
	sessionID := botSessionID(t, cs, "c1")

	rec := postCmd(t, h, botSaveCmd(t, "c1", marotte.BotSaveCommand{Name: "Weekly answers", Goal: "Answer the monthly questions.", Memory: true}))
	if rec.Code != http.StatusOK {
		t.Fatalf("bot_save = %d %s, want 200", rec.Code, rec.Body.String())
	}
	runPrompt(t, h, cs, "c1", "m2")

	if !sessionClosed(br) {
		t.Error("the session holding the old role was kept for the next message")
	}
	opts := reload.lastSessionOpts()
	if opts == nil || opts.SessionID != sessionID {
		t.Fatalf("the next message's door sent %+v, want a session/load of %q", opts, sessionID)
	}
	if len(opts.CustomAgents) != 1 || !strings.Contains(opts.CustomAgents[0].Prompt, "Answer the monthly questions.") {
		t.Errorf("the reloaded role = %+v, want the edited goal", opts.CustomAgents)
	}
}
