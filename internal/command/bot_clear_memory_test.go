package command

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

const startingMemoryText = "# Memory\n\nYour notes for future turns. Keep this file under 40,000 characters.\n"

func clearMemoryReq(id marotte.ChatID) *marotte.ClientCommand {
	return &marotte.ClientCommand{Type: marotte.CmdBotClearMemory, ChatID: id, Payload: []byte(`{}`)}
}

// botNotes writes notes as the Bot id's memory file under roles' KIRO_HOME and answers its path.
func botNotes(t *testing.T, roles *botRoles, id marotte.ChatID, notes string) string {
	t.Helper()
	file := filepath.Join(roles.paths.KiroHome, "bots", string(id), "memory.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func memoryText(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCmdBotClearMemory_RewritesTheNotesToTheStartingText(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1, Memory: true})
	roles, _ := newBotRoles(t, store)
	file := botNotes(t, roles, "c-bot", "- the backlog is at seven\n")

	if _, err := cmdBotClearMemory(t.Context(), roles, clearMemoryReq("c-bot")); err != nil {
		t.Fatalf("bot_clear_memory = %d %v, want 200", statusOf(err), err)
	}

	if got := memoryText(t, file); got != startingMemoryText {
		t.Errorf("memory.md = %q, want the starting text", got)
	}
}

func TestCmdBotClearMemory_MemoryOffIsNotFound(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch CI.", StartedAt: 1})
	roles, _ := newBotRoles(t, store)
	file := botNotes(t, roles, "c-bot", "- kept for when memory is on again\n")

	_, err := cmdBotClearMemory(t.Context(), roles, clearMemoryReq("c-bot"))

	if statusOf(err) != http.StatusNotFound {
		t.Errorf("status = %d, want 404 while memory is off (%v)", statusOf(err), err)
	}
	if got := memoryText(t, file); got != "- kept for when memory is on again\n" {
		t.Errorf("memory.md = %q, want the notes kept", got)
	}
}

func TestCmdBotClearMemory_RefusesWhatIsNoStartedBot(t *testing.T) {
	cases := []struct {
		seed   func(t *testing.T, store *testsupport.InMemoryChatStore)
		name   string
		reason string
	}{
		{name: "a draft", reason: "bot_draft", seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
			seedBot(t, store, "c-x", "New Bot", &marotte.Bot{Memory: true})
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

			_, err := cmdBotClearMemory(t.Context(), roles, clearMemoryReq("c-x"))

			if statusOf(err) != http.StatusConflict || reasonOf(err) != tc.reason {
				t.Errorf("status %d reason %q, want 409 %s", statusOf(err), reasonOf(err), tc.reason)
			}
			if _, err := os.Lstat(filepath.Join(roles.paths.KiroHome, "bots", "c-x")); !os.IsNotExist(err) {
				t.Errorf("a refused clear left a Bot directory (stat %v)", err)
			}
		})
	}
}
