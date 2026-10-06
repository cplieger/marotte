package agent

import (
	"context"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestChatSteeringReachesBothChatVerbsAndNotTheUtility pins chat-only steering on session/new
// and session/load, and its absence from the utility session.
func TestChatSteeringReachesBothChatVerbsAndNotTheUtility(t *testing.T) {
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
			// A bridge per spawn: a resumed session's rehydration starts the utility bridge too, so a
			// shared fake's last start can be the utility's.
			h := New(context.Background(), "/tmp/work", func() ACPBridge { return newFakeBridge() }, cs)
			cs.wire(h)
			h.SetChatSteering(func(context.Context) []marotte.ClientSteeringDoc { return sentinel })
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.ACPSessionID = tc.session
				return true
			})

			sb, err := h.coord.OpenBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			br, ok := sb.bridge.(*fakeBridge)
			if !ok {
				t.Fatalf("the chat's bridge is %T, want the fake", sb.bridge)
			}
			opts := br.lastStartOpts()
			if opts == nil {
				t.Fatal("the bridge was never started")
			}
			if opts.SessionID != tc.session {
				t.Fatalf("StartOpts.SessionID = %q, want %q (the case did not take the path it names)", opts.SessionID, tc.session)
			}
			if len(opts.Steering) != 1 || opts.Steering[0] != sentinel[0] {
				t.Errorf("chat StartOpts.Steering = %v, want the sentinel doc", opts.Steering)
			}
		})
	}

	t.Run("utility session", func(t *testing.T) {
		cs := newTestChatStore()
		br := newFakeBridge()
		h := New(context.Background(), "/tmp/work", func() ACPBridge { return br }, cs)
		cs.wire(h)
		h.SetChatSteering(func(context.Context) []marotte.ClientSteeringDoc { return sentinel })

		u := h.utility.get()
		if _, err := u.session.acquire(t.Context()); err != nil {
			t.Fatalf("acquire utility session: %v", err)
		}
		defer u.session.Stop()
		opts := br.lastStartOpts()
		if opts == nil {
			t.Fatal("the utility bridge was never started")
		}
		if len(opts.Steering) != 0 {
			t.Errorf("utility StartOpts.Steering = %v, want none", opts.Steering)
		}
	})
}
