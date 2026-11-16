package agent

// The Bot write lock serialises the commands that write a Bot's view and nothing else: a regular chat's rename never
// waits on it, and rename_chat answers once its write lands, never still_applying.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const lockTestBudget = 50 * time.Millisecond

func renameCmd(name string) marotte.ClientCommand {
	return marotte.ClientCommand{Type: marotte.CmdRenameChat, ChatID: "c1", Payload: json.RawMessage(`{"name":"` + name + `"}`)}
}

// holdEffortPick posts set_effort high on c1 with its engine call held past the answer budget, so the command keeps
// running after its 504 still_applying. The runtime was built under answerBudgetOf(lockTestBudget).
func holdEffortPick(t *testing.T, h *Runtime, br *fakeBridge) (release func()) {
	t.Helper()
	release = holdCall(t, br, marotte.MethodSetConfigOption)
	res := postCmdWithin(t, h, marotte.ClientCommand{Type: marotte.CmdSetEffort, ChatID: "c1", Payload: json.RawMessage(`{"level":"high"}`)})
	assertCommandStillApplying(t, res)
	return release
}

func TestRenameChat_ARegularChatDoesNotWaitForAHeldEffortPick(t *testing.T) {
	answerBudgetOf(t, lockTestBudget)
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	holdEffortPick(t, h, br)

	res := postCmdWithin(t, h, renameCmd("Renamed"))

	if res.Code != http.StatusOK {
		t.Errorf("rename_chat = %d %s, want 200 while the effort pick still waits on kiro-cli", res.Code, res.Body.String())
	}
	if got := chatOf(t, h, "c1").Name; got != "Renamed" {
		t.Errorf("name = %q, want Renamed", got)
	}
}

func TestRenameChat_ABotsRenameBehindAHeldEffortPickAnswersOnceItLands(t *testing.T) {
	answerBudgetOf(t, lockTestBudget)
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs := botRuntime(t, br)
	seedSpecBot(t, cs)
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open the Bot's session: %v", err)
	}
	release := holdEffortPick(t, h, br)
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- postCmd(t, h, renameCmd("CI watcher")) }()

	select {
	case res := <-answered:
		t.Fatalf("rename_chat answered %d %s while the effort pick held the Bot's write lock, want it to wait",
			res.Code, res.Body.String())
	case <-time.After(10 * lockTestBudget):
	}
	release()
	var res *httptest.ResponseRecorder
	select {
	case res = <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("rename_chat never answered after the effort pick landed")
	}

	if res.Code != http.StatusOK {
		t.Errorf("rename_chat = %d %s, want 200 once its write landed", res.Code, res.Body.String())
	}
	c := chatOf(t, h, "c1")
	if c.Name != "CI watcher" || c.Effort != "high" || c.Bot.SpecVersion != 5 {
		t.Errorf("name %q effort %q spec_version %d, want both writes landed at 5", c.Name, c.Effort, c.Bot.SpecVersion)
	}
}
