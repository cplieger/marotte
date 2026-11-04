package agent

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// awaitTeardown waits until no close of chatID is held: its background teardown returned.
func awaitTeardown(t *testing.T, h *Runtime, chatID marotte.ChatID) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); h.coord.turns.gates.held(chatID); {
		if time.Now().After(deadline) {
			t.Fatalf("%s's teardown never returned", chatID)
		}
		time.Sleep(time.Millisecond)
	}
}

// setTeardownHook runs fn between cleanupChatState's steps for the rest of the test.
func setTeardownHook(t *testing.T, h *Runtime, fn func(step string)) {
	t.Helper()
	h.teardownHook.Store(&fn)
	t.Cleanup(func() { h.teardownHook.Store(nil) })
}
