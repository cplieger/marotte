package chat

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func steerAt(t *testing.T, entries []marotte.Entry, id string) marotte.EntrySteer {
	t.Helper()
	for i := range entries {
		if entries[i].ID == id && entries[i].Kind == marotte.EntryKindSteer {
			var s marotte.EntrySteer
			if err := json.Unmarshal(entries[i].Payload, &s); err != nil {
				t.Fatalf("steer %q payload %s: %v", id, entries[i].Payload, err)
			}
			return s
		}
	}
	t.Fatalf("no steer %q in %v", id, shapes(entries))
	return marotte.EntrySteer{}
}

func TestEntryLog_AWorkflowMessageRowCarriesItsLaterTakeUp(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	for _, id := range []string{"wfmsg-a", "wfmsg-b", "wfmsg-c"} {
		f.append(first, "", id, marotte.EntryKindSteer, marotte.EntrySteer{
			Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 100,
		})
	}
	f.closeTurn(first, marotte.TurnOutcomeCompleted)
	second := f.prompt("two")
	f.append(second, "", marotte.SteerDeliveredID("wfmsg-a"), marotte.EntryKindSteerDelivered,
		marotte.EntrySteerDelivered{SteerID: "wfmsg-a", ReadTs: 200})
	f.append(second, "", marotte.SteerDeliveredID("wfmsg-b"), marotte.EntryKindSteerDelivered,
		marotte.EntrySteerDelivered{SteerID: "wfmsg-b", ReadTs: 300, Dropped: true})

	for _, phase := range []string{"live", "reopened", "rewritten"} {
		switch phase {
		case "reopened":
			f.reopen()
		case "rewritten":
			all, _, err := f.log.AllWithReverted()
			if err != nil {
				t.Fatalf("Setup: read for the rewrite: %v", err)
			}
			if err := f.log.Rewrite(t.Context(), all); err != nil {
				t.Fatalf("Setup: rewrite: %v", err)
			}
		}
		rows := mustRange(t, f, first)
		if a := steerAt(t, rows, "wfmsg-a"); a.ReadTs != 200 || a.State != marotte.SteerStateRead {
			t.Errorf("%s: wfmsg-a read from its own turn = %+v, want read at 200", phase, a)
		}
		if b := steerAt(t, rows, "wfmsg-b"); b.State != marotte.SteerStateDropped || b.Reason != marotte.SteerReasonBoundary || b.ReadTs != 0 {
			t.Errorf("%s: wfmsg-b = %+v, want dropped at the boundary with no read time", phase, b)
		}
		if c := steerAt(t, rows, "wfmsg-c"); c.ReadTs != 0 || c.State != "" {
			t.Errorf("%s: wfmsg-c = %+v, want still unsettled", phase, c)
		}
		waiting := f.log.WaitingWorkflowMessages()
		if len(waiting) != 1 || waiting[0].ID != "wfmsg-c" {
			t.Errorf("%s: WaitingWorkflowMessages() = %+v, want only wfmsg-c", phase, waiting)
		}
	}
}

func waitingIDs(l *EntryLog) []string {
	var ids []string
	for _, m := range l.WaitingWorkflowMessages() {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestEntryLog_ARevertedTakeUpLeavesItsRowWaiting(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	f.append(first, "", "wfmsg-a", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 100,
	})
	f.closeTurn(first, marotte.TurnOutcomeCompleted)
	second := f.prompt("two")
	f.append(second, "", marotte.SteerDeliveredID("wfmsg-a"), marotte.EntryKindSteerDelivered,
		marotte.EntrySteerDelivered{SteerID: "wfmsg-a", ReadTs: 200})
	f.closeTurn(second, marotte.TurnOutcomeCompleted)
	if a := steerAt(t, mustRange(t, f, first), "wfmsg-a"); a.ReadTs != 200 {
		t.Fatalf("Setup: wfmsg-a before the rewind = %+v, want read at 200", a)
	}
	if _, _, err := f.log.Revert(t.Context(), second, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("Setup: Revert(%q): %v", second, err)
	}

	for _, phase := range []string{"live", "reopened"} {
		if phase == "reopened" {
			f.reopen()
		}
		if a := steerAt(t, mustRange(t, f, first), "wfmsg-a"); a.ReadTs != 0 || a.State != "" {
			t.Errorf("%s: wfmsg-a after its take-up's turn was rewound = %+v, want unsettled", phase, a)
		}
		if ids := waitingIDs(f.log); !slices.Equal(ids, []string{"wfmsg-a"}) {
			t.Errorf("%s: WaitingWorkflowMessages() = %v, want [wfmsg-a]", phase, ids)
		}
	}
}

func TestEntryLog_ARevertedRowIsNotWaitingForALaterIdenticalMessage(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	f.closeTurn(first, marotte.TurnOutcomeCompleted)
	second := f.prompt("two")
	f.append(second, "", "wfmsg-hidden", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 100,
	})
	f.closeTurn(second, marotte.TurnOutcomeCompleted)
	if _, _, err := f.log.Revert(t.Context(), second, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("Setup: Revert(%q): %v", second, err)
	}
	third := f.prompt("three")
	f.append(third, "", "wfmsg-later", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 300,
	})

	for _, phase := range []string{"live", "reopened"} {
		if phase == "reopened" {
			f.reopen()
		}
		if ids := waitingIDs(f.log); !slices.Equal(ids, []string{"wfmsg-later"}) {
			t.Errorf("%s: WaitingWorkflowMessages() = %v, want [wfmsg-later]: a rewound row takes no later take-up", phase, ids)
		}
	}
}

func TestEntryLog_WorkflowMessagesWaitInFileOrderAcrossOpenTurns(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	second := f.prompt("two")
	f.append(second, "", "wfmsg-sent-first", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 100,
	})
	f.append(first, "", "wfmsg-sent-second", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 200,
	})

	want := []string{"wfmsg-sent-first", "wfmsg-sent-second"}
	if ids := waitingIDs(f.log); !slices.Equal(ids, want) {
		t.Errorf("WaitingWorkflowMessages() = %v, want %v: oldest send first, whichever turn holds it", ids, want)
	}
}

func TestEntryLog_ARewriteKeepsATakeUpARewindCanStillRemove(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	f.append(first, "", "wfmsg-a", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "tests pass", Origin: marotte.SteerOriginStep, ProducedTs: 100,
	})
	f.closeTurn(first, marotte.TurnOutcomeCompleted)
	second := f.prompt("two")
	f.append(second, "", marotte.SteerDeliveredID("wfmsg-a"), marotte.EntryKindSteerDelivered,
		marotte.EntrySteerDelivered{SteerID: "wfmsg-a", ReadTs: 200})
	f.closeTurn(second, marotte.TurnOutcomeCompleted)
	all, _, err := f.log.AllWithReverted()
	if err != nil {
		t.Fatalf("Setup: read for the rewrite: %v", err)
	}
	if err := f.log.Rewrite(t.Context(), all); err != nil {
		t.Fatalf("Setup: rewrite: %v", err)
	}
	if _, _, err := f.log.Revert(t.Context(), second, marotte.TurnRevertCauseRewind, ""); err != nil {
		t.Fatalf("Setup: Revert(%q): %v", second, err)
	}

	if a := steerAt(t, mustRange(t, f, first), "wfmsg-a"); a.ReadTs != 0 || a.State != "" {
		t.Errorf("wfmsg-a after a rewrite then a rewind of its take-up = %+v, want unsettled", a)
	}
}
