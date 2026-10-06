package agent

import (
	"os"
	"testing"
	"time"
)

// The heal ladder is parked past any test run: its untracked timer would outlive its test.
// A test that needs it to fire lowers the base.
func TestMain(m *testing.M) {
	healBaseDelay = time.Hour
	root, err := os.MkdirTemp("", "marotte-agent-chats-")
	if err != nil {
		panic("test chat root: " + err.Error())
	}
	testChatRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
