package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// framesBus keeps the frames a run publishes.
type framesBus struct{ events []marotte.ServerEvent }

func (b *framesBus) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	b.events = append(b.events, evt)
}

// A parent's message and its take-up filed into a step's open turn while the step streams leave
// the step's say one entry, written after both rows in the run's own log.
func TestRunSteer_MidStreamLeavesTheStepsSayWhole(t *testing.T) {
	ctx := t.Context()
	r := newRunLog(t.TempDir())
	openStepTurn(t, r, "s")
	rs := &Runs{log: r, bus: &framesBus{}}
	turn := r.turn("wf1", "s")
	if _, err := turn.ThinkingDelta(ctx, "", "b2c1-say", "merged land"); err != nil {
		t.Fatal(err)
	}
	rs.RunSteer(ctx, "wf1", "s", "wfmsg-1", &marotte.EntrySteer{
		Text: "also check the tests", Origin: marotte.SteerOriginParent, ProducedTs: 1,
	})
	if _, err := turn.ThinkingDelta(ctx, "", "b2c1-say", "ings look"); err != nil {
		t.Fatal(err)
	}
	rs.RunSteerDelivered(ctx, "wf1", "s", &marotte.EntrySteerDelivered{SteerID: "wfmsg-1", ReadTs: 2})
	if _, err := turn.ThinkingDelta(ctx, "", "b2c1-say", " right"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.closeNode(ctx, "wf1", "s", "completed", ""); err != nil {
		t.Fatal(err)
	}

	entries := runLogEntries(t, r, "wf1")
	var got []string
	for _, e := range entries {
		got = append(got, string(e.Kind)+":"+e.ID)
	}
	want := []string{"steer:wfmsg-1", "steer_delivered:wfmsg-1:delivered", "thinking:b2c1-say"}
	if len(entries) < 5 || !equalStrings(got[1:4], want) {
		t.Fatalf("the run log is %v, want the turn's open, %v, then its close", got, want)
	}
	var say marotte.EntryThinking
	if err := json.Unmarshal(entries[3].Payload, &say); err != nil || say.Text != "merged landings look right" {
		t.Errorf("the step's say is %q (%v), want the whole stream", say.Text, err)
	}
}
