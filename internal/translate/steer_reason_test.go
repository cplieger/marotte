package translate

// WHY a steer went unread is the reason field, and the boundary drop is the value
// marotte itself writes. KAS clears its buffer at every turn boundary, so a steer
// the model never reached is dropped BY that boundary — which is a different fact
// from the `restart` the replay merge stamps on a steer the record never held, and
// a dropped row carrying no reason at all leaves the note's label saying only that
// the words were not read, with nothing about what ended the turn first.
//
// Both constructions in steeringCleared write it: the HELD agent rows (the ones
// the buffer still carried, so the note's text is in reach) and the TEXT-LESS
// agent note.

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// An agent note the buffer still held, cleared unread. The note's label words the
// clause from this field, so the row has to carry it rather than leaving it empty.
func TestSteeringCleared_TheHeldAgentNoteWordsItsBoundaryReason(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "notify-wf-1",
			"content":   "a run finished",
		}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"notify-wf-1"},
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer entries, want 1", len(rows))
	}
	if rows[0].State != marotte.SteerStateDropped {
		t.Errorf("State = %q, want %q", rows[0].State, marotte.SteerStateDropped)
	}
	if rows[0].Reason != marotte.SteerReasonBoundary {
		t.Errorf("Reason = %q, want %q — the turn ended before the model read it",
			rows[0].Reason, marotte.SteerReasonBoundary)
	}
}

// The agent's own note, never held by this process: recorded TEXT-LESS, and it
// wants the same reason. The two constructions are one rule, so a reason written
// only at the held row leaves half the dropped population unworded.
func TestSteeringCleared_TheTextlessAgentNoteWordsItsBoundaryReason(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"notify-wf-7d2c"},
		}), FrameAttribution{})

	rows := steerRows(t, deps, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer entries, want 1", len(rows))
	}
	if rows[0].Origin != marotte.SteerOriginAgent {
		t.Fatalf("Origin = %q, want %q — the text-less note is the agent's",
			rows[0].Origin, marotte.SteerOriginAgent)
	}
	if rows[0].Text != "" {
		t.Errorf("Text = %q, want empty: the words it lacks are what the merge fills", rows[0].Text)
	}
	if rows[0].Reason != marotte.SteerReasonBoundary {
		t.Errorf("Reason = %q, want %q", rows[0].Reason, marotte.SteerReasonBoundary)
	}
}
