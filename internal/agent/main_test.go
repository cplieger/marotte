package agent

import (
	"os"
	"testing"
	"time"
)

// The heal ladder is parked past any test run. UP rather than down because nothing
// WAITS on it: it installs an untracked timer, so at the production delay it outlives
// its own test and re-attempts against an unrelated test's fixture. A test that needs
// it to FIRE lowers the base itself. The cancel-retry ladder's base is a per-Runs
// field instead, parked by buildTestHub on each runtime it builds.
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
