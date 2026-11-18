package agent

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

const warnBotDirLeft = "bots: a deleted Bot's directory could not be removed; marotte's next start removes it"

// botDirOf writes chat id's memory file under the test HOME's KIRO_HOME and answers the Bot's directory.
func botDirOf(t *testing.T, id string) string {
	t.Helper()
	kiroHome, _ := botMemoryHome(t)
	dir := filepath.Join(kiroHome, "bots", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.md"), []byte("- notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDeleteChatStateByChain_RemovesTheBotsDirectoryAfterItsSessions(t *testing.T) {
	t.Parallel()
	h, _, rig := newDeleteRig(t, noRuns(t))
	dir := botDirOf(t, "c-bot-del")
	var mu sync.Mutex
	var presentAtDelete []bool
	rig.onCall = func(method string, _ map[string]any) {
		if method != marotte.MethodSessionDelete {
			return
		}
		_, err := os.Lstat(dir)
		mu.Lock()
		presentAtDelete = append(presentAtDelete, err == nil)
		mu.Unlock()
	}

	h.DeleteChatStateByChain(t.Context(), "c-bot-del", []string{"sess_old", "sess_cur"}, command.RunStopChatDeleted)

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(presentAtDelete, []bool{true, true}) {
		t.Errorf("the directory at each session/delete = %v, want it still there for both", presentAtDelete)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the Bot's directory after the delete: stat %v, want it removed", err)
	}
}

func TestDeleteChatStateByChain_AFailedDirectoryRemovalIsLeftForTheSweep(t *testing.T) {
	t.Parallel()
	logs := capture.Default(t)
	h, _, _ := newDeleteRig(t, noRuns(t))
	kiroHome, _ := botMemoryHome(t)
	elsewhere := filepath.Join(kiroHome, "steering-bots")
	kept := filepath.Join(elsewhere, "c-bot-del", "memory.md")
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("- notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bots := filepath.Join(kiroHome, "bots")
	if err := os.RemoveAll(bots); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("steering-bots", bots); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(bots)
		_ = os.RemoveAll(elsewhere)
	})

	h.DeleteChatStateByChain(t.Context(), "c-bot-del", []string{"sess_cur"}, command.RunStopChatDeleted)

	if _, err := os.Lstat(kept); err != nil {
		t.Errorf("the notes below the link: stat %v, want them left", err)
	}
	if n := logs.CountExact(warnBotDirLeft); n != 1 {
		t.Errorf("Warn %q logged %d times, want 1", warnBotDirLeft, n)
	}
}
