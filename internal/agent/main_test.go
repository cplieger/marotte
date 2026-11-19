package agent

import (
	"os"
	"testing"
	"time"
)

// A cold-governance fake pushes no profile, so its boot observer pass gives up quickly rather than holding inflight
// work past joinInflight's bound.
func TestMain(m *testing.M) {
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
