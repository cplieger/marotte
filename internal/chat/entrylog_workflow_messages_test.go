package chat

import (
	"encoding/json"
	"maps"
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
		marotte.EntrySteerDelivered{SteerID: "wfmsg-b", ReadTs: 300, Cleared: true})

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
		if b := steerAt(t, rows, "wfmsg-b"); !b.Cleared || b.State != "" || b.ReadTs != 0 {
			t.Errorf("%s: wfmsg-b = %+v, want cleared, with no state and no read time", phase, b)
		}
		if c := steerAt(t, rows, "wfmsg-c"); c.ReadTs != 0 || c.State != "" {
			t.Errorf("%s: wfmsg-c = %+v, want still unsettled", phase, c)
		}
		waiting := f.log.UnsettledWorkflowMessages()
		if len(waiting) != 1 || waiting[0].ID != "wfmsg-c" {
			t.Errorf("%s: UnsettledWorkflowMessages() = %+v, want only wfmsg-c", phase, waiting)
		}
	}
}

func waitingIDs(l *EntryLog) []string {
	var ids []string
	for _, m := range l.UnsettledWorkflowMessages() {
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
			t.Errorf("%s: UnsettledWorkflowMessages() = %v, want [wfmsg-a]", phase, ids)
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
			t.Errorf("%s: UnsettledWorkflowMessages() = %v, want [wfmsg-later]: a rewound row takes no later take-up", phase, ids)
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
		t.Errorf("UnsettledWorkflowMessages() = %v, want %v: oldest send first, whichever turn holds it", ids, want)
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

// A steer_cleared settles cleared the rows sent to its session before it, and only those: the chat's clear settles a
// step's reports, a step's clear the parent's messages to that step, and a row filed after either, or naming no
// session, stays unsettled. The epoch is read from the log, so it holds across a reopen.
func TestEntryLog_ASteeringClearSettlesTheRowsSentToItsSession(t *testing.T) {
	f := newLogFixture(t)
	first := f.prompt("one")
	rows := []struct {
		id    string
		steer marotte.EntrySteer
	}{
		{"wfmsg-report", marotte.EntrySteer{Text: "a", Origin: marotte.SteerOriginStep, StepPath: "wf/a", ToSession: "sess-chat"}},
		{"wfmsg-to-a", marotte.EntrySteer{Text: "b", Origin: marotte.SteerOriginParent, StepPath: "wf/a", ToSession: "sess-a"}},
		{"wfmsg-to-b", marotte.EntrySteer{Text: "c", Origin: marotte.SteerOriginParent, StepPath: "wf/b", ToSession: "sess-b"}},
		{"wfmsg-sessionless", marotte.EntrySteer{Text: "d", Origin: marotte.SteerOriginStep, StepPath: "wf/a"}},
	}
	for _, r := range rows {
		f.append(first, "", r.id, marotte.EntryKindSteer, r.steer)
	}
	f.append(first, "", marotte.SteerClearedIDPrefix+"1", marotte.EntryKindSteerCleared,
		marotte.EntrySteerCleared{Cause: marotte.SteerClearCauseBoundary, Session: "sess-chat", IDs: []string{"notify-1"}})
	f.append(first, "", marotte.SteerClearedIDPrefix+"2", marotte.EntryKindSteerCleared,
		marotte.EntrySteerCleared{Cause: marotte.SteerClearCauseCancel, Session: "sess-a", IDs: []string{}})
	f.append(first, "", "wfmsg-later", marotte.EntryKindSteer,
		marotte.EntrySteer{Text: "e", Origin: marotte.SteerOriginStep, ToSession: "sess-chat"})

	wantCleared := map[string]bool{
		"wfmsg-report": true, "wfmsg-to-a": true, "wfmsg-to-b": false, "wfmsg-sessionless": false, "wfmsg-later": false,
	}
	for _, phase := range []string{"live", "reopened"} {
		if phase == "reopened" {
			f.reopen()
		}
		read := mustRange(t, f, first)
		got := map[string]bool{}
		for id := range wantCleared {
			got[id] = steerAt(t, read, id).Cleared
		}
		if !maps.Equal(got, wantCleared) {
			t.Errorf("%s: rows read cleared = %v, want %v", phase, got, wantCleared)
		}
		if ids := waitingIDs(f.log); !slices.Equal(ids, []string{"wfmsg-to-b", "wfmsg-sessionless", "wfmsg-later"}) {
			t.Errorf("%s: UnsettledWorkflowMessages() = %v, want the rows no clear of their session followed", phase, ids)
		}
	}
}

// A clear's fact settles its epoch by itself: a log cut before the fact leaves the rows unsettled, and one cut after
// it, before any steer_delivered, reads them cleared, live and reopened, whatever the clear's cause. A row already
// taken up keeps its read.
func TestEntryLog_AClearsFactSettlesItsEpochWithoutItsSettlements(t *testing.T) {
	cases := []struct {
		clear       *marotte.EntrySteerCleared
		name        string
		wantCleared bool
	}{
		{name: "cut_before_the_fact"},
		{
			name:        "cut_after_a_cancels_fact",
			clear:       &marotte.EntrySteerCleared{Cause: marotte.SteerClearCauseCancel, Session: "sess-chat", IDs: []string{"notify-1"}},
			wantCleared: true,
		},
		{
			name:        "cut_after_a_boundarys_fact",
			clear:       &marotte.EntrySteerCleared{Cause: marotte.SteerClearCauseBoundary, Session: "sess-chat", IDs: []string{"notify-1", "notify-2"}},
			wantCleared: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLogFixture(t)
			turn := f.prompt("one")
			for _, id := range []string{"wfmsg-a", "wfmsg-b", "wfmsg-taken"} {
				f.append(turn, "", id, marotte.EntryKindSteer, marotte.EntrySteer{
					Text: id, Origin: marotte.SteerOriginStep, ToSession: "sess-chat", ProducedTs: 100,
				})
			}
			f.append(turn, "", marotte.SteerDeliveredID("wfmsg-taken"), marotte.EntryKindSteerDelivered,
				marotte.EntrySteerDelivered{SteerID: "wfmsg-taken", KASID: "notify-0", ReadTs: 150})
			if tc.clear != nil {
				f.append(turn, "", marotte.SteerClearedIDPrefix+"1", marotte.EntryKindSteerCleared, *tc.clear)
			}

			for _, phase := range []string{"live", "reopened"} {
				if phase == "reopened" {
					f.reopen()
				}
				rows := mustRange(t, f, turn)
				for _, id := range []string{"wfmsg-a", "wfmsg-b"} {
					if got := steerAt(t, rows, id); got.Cleared != tc.wantCleared || got.State != "" || got.ReadTs != 0 {
						t.Errorf("%s: %s = %+v, want cleared %t with no state and no read time", phase, id, got, tc.wantCleared)
					}
				}
				if got := steerAt(t, rows, "wfmsg-taken"); got.Cleared || got.State != marotte.SteerStateRead || got.ReadTs != 150 {
					t.Errorf("%s: wfmsg-taken = %+v, want its take-up's read at 150", phase, got)
				}
				var want []string
				if !tc.wantCleared {
					want = []string{"wfmsg-a", "wfmsg-b"}
				}
				if ids := waitingIDs(f.log); !slices.Equal(ids, want) {
					t.Errorf("%s: UnsettledWorkflowMessages() = %v, want %v", phase, ids, want)
				}
			}
		})
	}
}
