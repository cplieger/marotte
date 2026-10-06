package bridge

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A Start that fails before any process leaves no read loop, so Stop closes NotifCh and a forward loop still ends.
func TestStop_ClosesTheStreamOfABridgeThatNeverStarted(t *testing.T) {
	for name, opts := range map[string]*marotte.StartOpts{
		"no kiro-cli installed":     {Lifetime: t.Context()},
		"an invalid session id":     {Lifetime: t.Context(), SessionID: "not a session id"},
		"no lifetime for the spawn": {},
	} {
		t.Run(name, func(t *testing.T) {
			b := New("", t.TempDir())
			if err := b.Start(t.Context(), opts); err == nil {
				t.Fatal("Setup: Start succeeded with no kiro-cli to run")
			}
			b.Stop()
			select {
			case _, open := <-b.NotifCh():
				if open {
					t.Error("NotifCh delivered a frame from a bridge that never started")
				}
			case <-time.After(time.Second):
				t.Error("NotifCh is still open after Stop, so a consumer ranging over it never ends")
			}
		})
	}
}
