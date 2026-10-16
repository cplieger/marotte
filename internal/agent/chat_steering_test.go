package agent

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestChatSteeringReachesBothChatVerbsAndNotAnObserver pins chat-only steering on session/new and session/load, and
// its absence from an observer pass.
func TestChatSteeringReachesBothChatVerbsAndNotAnObserver(t *testing.T) {
	sentinel := []marotte.ClientSteeringDoc{{Name: "marotte", Inclusion: "always", Content: "probe"}}
	for _, tc := range []struct {
		name    string
		session string
	}{
		{"fresh chat", ""},
		{"resumed chat", "sess_resume_probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChatStore()
			h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs)
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			h.SetChatSteering(func(context.Context) []marotte.ClientSteeringDoc { return sentinel })
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.ACPSessionID = tc.session
				return true
			})

			sb, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			br, ok := sb.bridge.(*fakeBridge)
			if !ok {
				t.Fatalf("the chat's bridge is %T, want the fake", sb.bridge)
			}
			opts := br.lastSessionOpts()
			if opts == nil {
				t.Fatal("the session was never opened")
			}
			if opts.SessionID != tc.session {
				t.Fatalf("SessionOpts.SessionID = %q, want %q (the case did not take the path it names)", opts.SessionID, tc.session)
			}
			if len(opts.Steering) != 1 || opts.Steering[0] != sentinel[0] {
				t.Errorf("chat SessionOpts.Steering = %v, want the sentinel doc", opts.Steering)
			}
		})
	}

	t.Run("observer pass", func(t *testing.T) {
		cs := newTestChatStore()
		observer := newFakeBridge()
		src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
		src.observer = func() engineSession { return observer }
		h := newRuntimeOn(context.Background(), "/tmp/work", src, cs)
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		h.SetChatSteering(func(context.Context) []marotte.ClientSteeringDoc { return sentinel })

		if err := h.engine.observe(t.Context(), nil, nil, func(context.Context, engineSession) error { return nil }); err != nil {
			t.Fatalf("observer pass: %v", err)
		}
		opts := observer.lastSessionOpts()
		if opts == nil {
			t.Fatal("the observer session was never opened")
		}
		if len(opts.Steering) != 0 {
			t.Errorf("observer SessionOpts.Steering = %v, want none", opts.Steering)
		}
	})
}
