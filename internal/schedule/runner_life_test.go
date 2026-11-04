package schedule

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A schedule deleted and added again for the same recipe has the same id. A launch the old row started, still in flight
// when that happens, settles nothing on the new row: the new row reads as Put wrote it.
func TestSweep_ARecreatedScheduleKeepsItsOwnRow(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	for _, tc := range []struct {
		launch func(old Row) error
		name   string
	}{
		{name: "launched", launch: func(Row) error { return nil }},
		{name: "engine busy", launch: func(Row) error { return busy }},
		{name: "failed", launch: func(Row) error { return errors.New("workflow new: no such recipe") }},
		{name: "refused by its own bounded run", launch: func(old Row) error {
			return &BusyError{WorkflowID: "wf-0", ScheduleID: old.ID, ScheduleLife: old.Life, SlotAt: due}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, l, r := newFixture(t, due.Add(30*time.Second))
			l.failWith(tc.launch(st.List()[0].Row()))
			var reborn Entry
			l.during = func(string) {
				old := st.List()[0]
				if err := st.Delete(t.Context(), old.ID()); err != nil {
					t.Errorf("Delete: %v", err)
				}
				reborn = Entry{Source: old.Source, Spec: Spec{Freq: freqHourly, Interval: 1}, Enabled: true}
				if err := st.Put(t.Context(), &reborn); err != nil {
					t.Errorf("Put: %v", err)
				}
			}
			r.sweep(t.Context())
			got := st.List()[0]
			if !got.Anchor.Equal(reborn.Anchor) || !got.LastRunAt.IsZero() || got.LastRunID != "" ||
				got.LastStatus != "" || got.LastReason != "" || !got.WaitingFor.IsZero() {
				t.Errorf("recreated row = %+v, want it as Put wrote it (anchor %v, no run, no wait)", got, reborn.Anchor)
			}
		})
	}
}

// A recreated schedule's launch refused by a run its predecessor started is its own failure: that run's bound
// reports to the predecessor, so the slot would otherwise pass with no outcome.
func TestSweep_ARunOfThePredecessorHoldingTheRecipeFailsTheSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	old := st.List()[0]
	if err := st.Delete(t.Context(), old.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reborn := Entry{Source: old.Source, Spec: old.Spec, Enabled: true, Anchor: old.Anchor}
	if err := st.Put(t.Context(), &reborn); err != nil {
		t.Fatalf("Put: %v", err)
	}
	l.failWith(&BusyError{WorkflowID: "wf-0", ScheduleID: old.ID(), ScheduleLife: old.Life, SlotAt: due})
	r.sweep(t.Context())
	if got := st.List()[0]; got.LastStatus != StatusFailed || got.LastReason != "this recipe already has a live run" {
		t.Errorf("row = %+v, want the slot failed with the refusal", got)
	}
}

// A run leased for a deleted schedule reports nothing onto the schedule added again for its recipe, while a run of
// the new schedule still does.
func TestStore_ARunOfARecreatedScheduleLeavesItsSuccessorAlone(t *testing.T) {
	st := mustStore(t)
	old := Entry{Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 2}, Enabled: true}
	if err := st.Put(t.Context(), &old); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stale := Run{ScheduleID: old.ID(), ScheduleLife: old.Life, WorkflowID: "wf-old"}
	if err := st.Delete(t.Context(), old.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reborn := Entry{Source: old.Source, Spec: Spec{Freq: freqHourly, Interval: 1}, Enabled: true}
	if err := st.Put(t.Context(), &reborn); err != nil {
		t.Fatalf("Put (recreate): %v", err)
	}
	if err := st.RecordStart(t.Context(), stale); !errors.Is(err, ErrNotFound) {
		t.Errorf("RecordStart = %v, want ErrNotFound for a deleted life", err)
	}
	if err := st.RecordEnding(t.Context(), stale, Outcome{Status: StatusFailed, Reason: "step build failed"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("RecordEnding = %v, want ErrNotFound for a deleted life", err)
	}
	if got := st.List()[0]; got.LastRunID != "" || got.LastStatus != "" || got.LastReason != "" || !got.LastRunAt.IsZero() {
		t.Errorf("recreated row = %+v, want no run recorded", got)
	}

	own := Run{ScheduleID: reborn.ID(), ScheduleLife: reborn.Life, WorkflowID: "wf-new"}
	if err := st.RecordStart(t.Context(), own); err != nil {
		t.Fatalf("RecordStart (own): %v", err)
	}
	if got := st.List()[0]; got.LastRunID != "wf-new" || got.LastStatus != statusStarted {
		t.Errorf("row = %+v, want its own run wf-new started", got)
	}
}

// A row's life is minted when Put creates it and kept by an edit and a reopen, so only a recreation changes it.
func TestStore_ARowKeepsItsLifeUntilItIsRecreated(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 2}, Enabled: true, Life: "forged"}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	born := e.Life
	if born == "" || born == "forged" {
		t.Fatalf("a new row's life = %q, want one Put minted", born)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	edit := Entry{Source: e.Source, Spec: Spec{Freq: FreqDaily, Hour: 4}, Enabled: true}
	if err := reopened.Put(t.Context(), &edit); err != nil {
		t.Fatalf("Put (edit): %v", err)
	}
	if got := reopened.List()[0].Life; got != born {
		t.Errorf("life after a reopen and an edit = %q, want %q", got, born)
	}
	if err := reopened.Delete(t.Context(), e.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	again := Entry{Source: e.Source, Spec: Spec{Freq: FreqDaily, Hour: 2}, Enabled: true}
	if err := reopened.Put(t.Context(), &again); err != nil {
		t.Fatalf("Put (recreate): %v", err)
	}
	if again.Life == "" || again.Life == born {
		t.Errorf("a recreated row's life = %q, want a new one (was %q)", again.Life, born)
	}
}

// A row written before rows carried a life reads an empty life, and a run leased with an empty life still lands on it.
func TestStore_ARowWithNoLifeTakesARunWithNone(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, `[{"source":"bundled://x","spec":{"freq":"daily","hour":2},"enabled":true,`+
		`"anchor":"2026-08-03T12:00:00Z"}]`); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	row := st.List()[0]
	if row.Life != "" {
		t.Fatalf("a row with no life on disk reads life %q", row.Life)
	}
	if err := st.RecordStart(t.Context(), Run{ScheduleID: row.ID(), WorkflowID: "wf-1"}); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	if got := st.List()[0]; got.LastRunID != "wf-1" {
		t.Errorf("row = %+v, want wf-1 recorded", got)
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
			got := st.List()[0].LastReason
			if len(got) > MaxReasonBytes || !strings.HasPrefix(got, "workflow new: refused x") || !strings.HasSuffix(got, "...") ||
				strings.ContainsAny(got, "\x1b\n") {
				t.Errorf("a 4 KB launch error reads %d bytes on the row (%.60q...), want one line of at most %d, marked as cut",
					len(got), got, MaxReasonBytes)
			}
		})
	}
}
