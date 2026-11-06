package schedule

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// A launch the deleted rule started, still in flight when a rule for the same recipe is added, settles nothing on
// the new rule: it reads as the store created it.
func TestSweep_ARecreatedScheduleKeepsItsOwnRow(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	for _, tc := range []struct {
		launch func(old launchrule.Row) error
		name   string
	}{
		{name: "launched", launch: func(launchrule.Row) error { return nil }},
		{name: "engine busy", launch: func(launchrule.Row) error { return busy }},
		{name: "failed", launch: func(launchrule.Row) error { return errors.New("workflow new: no such recipe") }},
		{name: "refused by its own bounded run", launch: func(old launchrule.Row) error {
			return &BusyError{WorkflowID: "wf-0", ScheduleID: old.ID, SlotAt: due}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, l, r := newFixture(t, due.Add(30*time.Second))
			l.failWith(tc.launch(st.Clocks()[0].Row()))
			var reborn launchrule.Clock
			l.during = func(string) {
				old := st.Clocks()[0]
				if err := st.Delete(t.Context(), old.ID); err != nil {
					t.Errorf("Delete: %v", err)
				}
				rule := putClock(t, st, clockSeed{Source: old.Source, Spec: launchrule.Spec{Freq: launchrule.FreqHourly, Interval: 1}})
				reborn, _ = st.Clock(rule.ID)
			}
			r.sweep(t.Context())
			got := st.Clocks()[0]
			if got.ID != reborn.ID || !reflect.DeepEqual(got.Status, reborn.Status) {
				t.Errorf("recreated row = %+v, want it as the store created it: %+v", got, reborn)
			}
		})
	}
}

// A recreated schedule's launch refused by a run its predecessor started is its own failure: that run's bound
// reports to the predecessor, so the slot would otherwise pass with no outcome.
func TestSweep_ARunOfThePredecessorHoldingTheRecipeFailsTheSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	old := st.Clocks()[0]
	if err := st.Delete(t.Context(), old.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	putClock(t, st, clockSeed{Source: old.Source, Spec: *old.When.Clock, Anchor: old.Anchor})
	l.failWith(&BusyError{WorkflowID: "wf-0", ScheduleID: old.ID, SlotAt: due})
	r.sweep(t.Context())
	if got := st.Clocks()[0]; got.LastStatus != launchrule.StatusFailed || got.LastReason != "this recipe already has a live run" {
		t.Errorf("row = %+v, want the slot failed with the refusal", got)
	}
}

// A run leased for a deleted schedule reports nothing onto the schedule added again for its recipe, while a run of
// the new schedule still does.
func TestStore_ARunOfARecreatedScheduleLeavesItsSuccessorAlone(t *testing.T) {
	st := mustStore(t)
	old := putClock(t, st, clockSeed{Source: "bundled://x", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}})
	stale := launchrule.Run{ScheduleID: old.ID, WorkflowID: "wf-old"}
	if err := st.Delete(t.Context(), old.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reborn := putClock(t, st, clockSeed{Source: old.Source, Spec: launchrule.Spec{Freq: launchrule.FreqHourly, Interval: 1}})
	if err := st.RecordStart(t.Context(), stale); !errors.Is(err, launchrule.ErrNotFound) {
		t.Errorf("RecordStart = %v, want ErrNotFound for a deleted rule", err)
	}
	if err := st.RecordEnding(t.Context(), stale, launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "step build failed"}); !errors.Is(err, launchrule.ErrNotFound) {
		t.Errorf("RecordEnding = %v, want ErrNotFound for a deleted rule", err)
	}
	if got := st.Clocks()[0]; got.LastRunID != "" || got.LastStatus != "" || got.LastReason != "" || !got.LastRunAt.IsZero() {
		t.Errorf("recreated row = %+v, want no run recorded", got)
	}

	own := launchrule.Run{ScheduleID: reborn.ID, WorkflowID: "wf-new"}
	if err := st.RecordStart(t.Context(), own); err != nil {
		t.Fatalf("RecordStart (own): %v", err)
	}
	if got := st.Clocks()[0]; got.LastRunID != "wf-new" || got.LastStatus != launchrule.StatusStarted {
		t.Errorf("row = %+v, want its own run wf-new started", got)
	}
}

// A launch's error is written onto the row as one line of bounded length, whichever arm records it.
func TestSweep_ALongLaunchErrorIsCappedOnTheRow(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	long := errors.New("workflow new: \x1b[31mrefused\x1b[0m\n" + strings.Repeat("x", 4096))
	for _, tc := range []struct {
		name   string
		failID string
	}{
		{name: "a launch that created nothing"},
		{name: "a run created but not started", failID: "wf-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, l, r := newFixture(t, due.Add(30*time.Second))
			l.failWith(long)
			l.failID = tc.failID
			r.sweep(t.Context())
			got := st.Clocks()[0].LastReason
			if len(got) > launchrule.MaxReasonBytes || !strings.HasPrefix(got, "workflow new: refused x") || !strings.HasSuffix(got, "...") ||
				strings.ContainsAny(got, "\x1b\n") {
				t.Errorf("a 4 KB launch error reads %d bytes on the row (%.60q...), want one line of at most %d, marked as cut",
					len(got), got, launchrule.MaxReasonBytes)
			}
		})
	}
}
