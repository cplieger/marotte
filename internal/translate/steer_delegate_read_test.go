package translate

// A steer a DELEGATE read: KAS emits no steering_injected for a sub-agent's injection, so the
// delegate's `[STEERING <id>: …]` marker is the evidence; without it the steer would be
// reported dropped at the next boundary and resent.

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// laneSteer is a steer entry decoded with its id and its lane, which is the fact
// under test here.
type laneSteer struct {
	marotte.EntrySteer
	ID   string
	Lane string
}

// laneSteerRows is steerRows carrying each row's lane.
func laneSteerRows(t *testing.T, deps *baseDeps, chatID marotte.ChatID) []laneSteer {
	t.Helper()
	var out []laneSteer
	for _, entries := range [][]marotte.Entry{deps.chatEntries(chatID), deps.between[chatID]} {
		for i := range entries {
			if entries[i].Kind != marotte.EntryKindSteer {
				continue
			}
			out = append(out, laneSteer{
				EntrySteer: decodePayload[marotte.EntrySteer](t, &entries[i]),
				ID:         entries[i].ID,
				Lane:       entries[i].Lane,
			})
		}
	}
	return out
}

// TestDelegateAck_RecordsTheSteerReadInItsLaneAndTheClearWritesNothing pins the steer READ in
// the delegate's lane, and no dropped row from the following clear.
func TestDelegateAck_RecordsTheSteerReadInItsLaneAndTheClearWritesNothing(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-d": true}
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-d", "content": "run the suite first",
		}), FrameAttribution{})
	*events = nil

	// Two deltas, as KAS sends them: the marker arrives as its own chunk.
	feedLaneChunk(t, tr, "c1", "sub-7", "on it")
	feedLaneChunk(t, tr, "c1", "sub-7", "[STEERING steer-d: reran the suite]")

	rows := laneSteerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("steer entries after the delegate's ack = %+v, want exactly one read row", rows)
	}
	if rows[0].ID != "steer-d" || rows[0].State != marotte.SteerStateRead {
		t.Errorf("row = %+v, want id steer-d in state read", rows[0])
	}
	if rows[0].Lane != "sub-7" {
		t.Errorf("row lane = %q, want sub-7: the entry is what says which agent read it", rows[0].Lane)
	}
	if rows[0].Text != "run the suite first" || rows[0].Origin != marotte.SteerOriginUser {
		t.Errorf("row = %+v, want the queued text and the reader's own origin", rows[0])
	}
	if kinds := entryKinds(deps.chatEntries("c1")); !equalKinds(kinds,
		[]marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindSteer, marotte.EntryKindSteerAck}) {
		t.Errorf("sealed kinds = %v, want [text steer steer_ack]: the read precedes the statement about it", kinds)
	}
	if got := appendedSteers(t, *events); len(got) != 1 || got[0].State != marotte.SteerStateRead {
		t.Errorf("entry_appended{steer} frames = %+v, want the one read row: %v", got, eventTypes(*events))
	}
	if left := waitingOf(t, deps, "c1"); len(left) != 0 {
		t.Errorf("waiting after the ack = %v, want empty: the dock row leaves on the entry", left)
	}

	*events = nil
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{"messageIds": []string{"steer-d"}}), FrameAttribution{})

	if rows := laneSteerRows(t, deps, "c1"); len(rows) != 1 {
		t.Errorf("steer entries after the clear = %+v, want the read row alone", rows)
	}
	if got := appendedSteers(t, *events); len(got) != 0 {
		t.Errorf("the clear announced %+v, want nothing: an id already read is housekeeping", got)
	}
}
