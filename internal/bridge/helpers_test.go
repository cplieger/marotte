package bridge

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/cplieger/slogx/capture"
)

// captureLogs swaps the slog default to a buffer-backed debug handler for the test and restores it; not for parallel
// tests.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// newTestSession is an open session handle on an unstarted engine, for tests of what a door result leaves on it.
func newTestSession() *Session {
	return &Session{
		engine:    NewEngine("/nonexistent", "/work"),
		lane:      newLane(),
		closeDone: make(chan struct{}),
		id:        "sess-test",
		state:     stateOpen,
	}
}

// engineWithSession is an unstarted engine holding one open session on its lane table.
func engineWithSession() (*Engine, *Session) {
	s := newTestSession()
	e := s.engine
	e.sessions[s.id] = s
	return e, s
}

// syncWithScript makes one round trip, so a fake kiro-cli has logged every frame written before it.
func syncWithScript(t *testing.T, e *Engine) {
	t.Helper()
	if _, err := e.Call(t.Context(), "_probe/ping", nil); err != nil {
		t.Fatalf("Setup: round trip to the fake kiro-cli: %v", err)
	}
}
