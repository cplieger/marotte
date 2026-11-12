package composition

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// botSweepFixture is a chat store holding c-live and a KIRO_HOME whose bots/ holds c-live's and c-gone's directories.
func botSweepFixture(t *testing.T) (chats *chat.Store, chatsDir string, paths bot.Paths) {
	t.Helper()
	root := t.TempDir()
	chatsDir = filepath.Join(root, "chats")
	chats, err := chat.NewStore(chatsDir)
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	if _, err := chats.Mutate(t.Context(), "c-live", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Weekly answers"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	paths = bot.Paths{KiroHome: filepath.Join(root, "kiro")}
	for _, id := range []string{"c-live", "c-gone"} {
		dir := filepath.Join(paths.KiroHome, "bots", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("notes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return chats, chatsDir, paths
}

func botDirExists(t *testing.T, paths bot.Paths, id string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(paths.KiroHome, "bots", id))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// TestSweepBotDirs_RemovesTheDirectoryOfAChatWithNoRecord pins against the real chat store that the directory of a chat
// with no record goes, the one with a record stays.
func TestSweepBotDirs_RemovesTheDirectoryOfAChatWithNoRecord(t *testing.T) {
	chats, _, paths := botSweepFixture(t)

	sweepBotDirs(t.Context(), chats, paths)

	if botDirExists(t, paths, "c-gone") {
		t.Error("c-gone's directory is still there")
	}
	if !botDirExists(t, paths, "c-live") {
		t.Error("c-live's directory was removed")
	}
}

// TestSweepBotDirs_AnUnreadRecordRemovesNothing pins that a chat record that exists and could not be read makes the
// scan incomplete, and an incomplete scan removes no directory.
func TestSweepBotDirs_AnUnreadRecordRemovesNothing(t *testing.T) {
	chats, chatsDir, paths := botSweepFixture(t)
	if err := os.MkdirAll(filepath.Join(chatsDir, "c-unread"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, "c-unread", "chat.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	sweepBotDirs(t.Context(), chats, paths)

	if !botDirExists(t, paths, "c-gone") {
		t.Error("c-gone's directory was removed by an incomplete scan")
	}
}

// TestBotDirSweepRunsOnceBeforeTheAgent pins the boot wiring: Build sweeps once, after the chat store opens and before
// agent.New, so no Bot door can open while the sweep runs.
func TestBotDirSweepRunsOnceBeforeTheAgent(t *testing.T) {
	bc := readBuildCalls(t)
	store, sweep, agentNew := bc.first(t, "chat.NewStore"), bc.first(t, "sweepBotDirs"), bc.first(t, "agent.New")
	if store >= sweep || sweep >= agentNew {
		t.Errorf("order chat.NewStore %d, sweepBotDirs %d, agent.New %d; want them in that order", store, sweep, agentNew)
	}
}
