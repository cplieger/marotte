package command

import (
	"context"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// SteerEntry's acceptance is kiro-cli's queued reply to the send carrying the entry, never the routing alone: the
// agent's unattended mark clears on it.
func TestSteerEntry_AcceptsOnlyOnKiroCLIsQueuedReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		host func() hostDouble
		want bool
	}{
		{name: "queued", want: true, host: func() hostDouble { return newBridgeHost(nil, newSteerBridge()) }},
		{name: "dropped", host: func() hostDouble {
			b := newSteerBridge()
			b.results[marotte.MethodSessionSteer] = map[string]any{"queued": false, "dropped": "buffer_full"}
			return newBridgeHost(nil, b)
		}},
		{name: "bridge_error", host: func() hostDouble {
			b := newSteerBridge()
			b.errs[marotte.MethodSessionSteer] = errors.New("kas gone")
			return newBridgeHost(nil, b)
		}},
		{name: "queued_reply_with_an_error", host: func() hostDouble {
			return newBridgeHost(nil, &failingQueuedBridge{steerBridge: newSteerBridge()})
		}},
		{name: "no_resident_caller", host: func() hostDouble { return newBenchDeps() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newStubSteerQueue()
			refuse, accepted := steerEntry(t.Context(), steerRolesOf(tc.host(), NewSteerLedger(), q), "c1",
				&EntrySteer{Entry: "m-1", Text: "look at the tests too"})
			if refuse != "" {
				t.Fatalf("refuse = %q, want the entry routed", refuse)
			}
			if accepted != tc.want {
				t.Errorf("accepted = %v, want %v", accepted, tc.want)
			}
			if len(q.sent) != 1 {
				t.Errorf("sends reported = %d, want the one send's fate on the record", len(q.sent))
			}
		})
	}
}

// failingQueuedBridge answers session/steer's queued reply and fails the call too, which issueSteers counts a failure.
type failingQueuedBridge struct{ *steerBridge }

func (b *failingQueuedBridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, _ := b.steerBridge.Call(ctx, method, params)
	return resp, errors.New("the reply came with a transport error")
}
