package agent

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

// A cold-governance fake pushes no profile, so its boot observer pass gives up quickly rather than holding inflight
// work past joinInflight's bound. holdRouter is installed before m.Run, so no parallel test swaps the logger.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(holdRouter))
	governanceWarmTimeout = 100 * time.Millisecond
	bootSeedTimeout = 100 * time.Millisecond
	root, err := os.MkdirTemp("", "marotte-agent-chats-")
	if err != nil {
		panic("test chat root: " + err.Error())
	}
	testChatRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
