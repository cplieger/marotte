package schedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

func mustStore(t *testing.T) *launchrule.Store {
	t.Helper()
	return mustStoreIn(t, t.TempDir())
}

// fakeLauncher records launches and can be made to fail. It carries its own mutex because
// the loop tests record off the test goroutine (synctest.Wait is no race-detector edge).
type fakeLauncher struct {
	mu      sync.Mutex
	sources []string
	// schedules records the id each launch was attributed to, which is what lets
	// a later denial land on the right row.
	schedules []string
	// deadlines records the run bound each launch carried, so a test can assert
	// the interval was derived rather than merely passed.
	deadlines []time.Time
	// defaults records the model and effort each launch carried.
	defaults []marotte.RunDefaults
	inputs   []map[string]string
	// notStarted records each NotifyNotStarted call.
	notStarted []notStartedCall
	err        error
	// during runs inside each launch, so a test can move the runner's clock while the launch holds the sweep.
	during func(source string)
	// failID is the run id a failing launch returns: a run it created but did not start.
	failID string
}

// notStartedCall is one NotifyNotStarted call: the rule it named and the reason it carried.
type notStartedCall struct {
	ruleID string
	name   string
	reason string
}

func (f *fakeLauncher) NotifyNotStarted(_ context.Context, rule *launchrule.Rule, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notStarted = append(f.notStarted, notStartedCall{ruleID: rule.ID, name: rule.Name, reason: reason})
}

func (f *fakeLauncher) notices() []notStartedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.notStarted)
}

func (f *fakeLauncher) LaunchScheduled(
	_ context.Context, source string, row launchrule.Row, inputs map[string]string, defaults marotte.RunDefaults, deadline time.Time,
) (string, string, error) {
	if f.during != nil {
		f.during(source)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaults = append(f.defaults, defaults)
	f.inputs = append(f.inputs, inputs)
	f.sources = append(f.sources, source)
	f.schedules = append(f.schedules, row.ID)
	f.deadlines = append(f.deadlines, deadline)
	if f.err != nil {
		return f.failID, "", f.err
	}
	return "wf-1", "recipe", nil
}

// snap returns copies of what the fake has recorded, so a caller reading it
// never holds a slice the launcher may append to.
func (f *fakeLauncher) snap() (sources, schedules []string, slots []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sources), slices.Clone(f.schedules), slices.Clone(f.deadlines)
}

func (f *fakeLauncher) launched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sources)
}

// failWith makes every later launch fail.
func (f *fakeLauncher) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// newFixture builds a store with one daily 02:00 clock rule anchored yesterday,
// plus a runner whose clock the test drives.
func newFixture(t *testing.T, now time.Time) (*launchrule.Store, *fakeLauncher, *Runner) {
	t.Helper()
	return newFixtureIn(t, t.TempDir(), now)
}

// newFixtureIn is newFixture with its store in dir, for a test that reopens it.
func newFixtureIn(t *testing.T, dir string, now time.Time) (*launchrule.Store, *fakeLauncher, *Runner) {
	t.Helper()
	st, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e, err := st.Create(t.Context(), dailyAt2("bundled://demo"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	anchorAt(t, st, e.Row(), at(2026, time.August, 3, 12, 0))
	l := &fakeLauncher{}
	r := NewRunner(st, l)
	r.now = func() time.Time { return now }
	return st, l, r
}

// TestSweep_FiresADueSlot is the happy path: the slot is due and recent.
func TestSweep_FiresADueSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	r.sweep(t.Context())

	if got := l.launched(); got != 1 {
		t.Fatalf("expected one launch, got %d", got)
	}
	got := st.Clocks()[0]
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor must advance to the DUE time (not now) to prevent drift: got %v want %v", got.Anchor, due)
	}
	if got.LastStatus != launchrule.StatusStarted || got.LastReason != "" {
		t.Errorf("last outcome = (%q, %q), want (%q, \"\")", got.LastStatus, got.LastReason, launchrule.StatusStarted)
	}
	// The schedule's id must travel with the launch, or an unattended denial has
	// no row to report itself on.
	if _, schedules, _ := l.snap(); len(schedules) != 1 || schedules[0] != st.Clocks()[0].ID {
		t.Errorf("launch must carry the schedule id, got %v", schedules)
	}
}

// A schedule's stored model and effort go out with each fire; a row with none sends the zero
// value, which launches on KAS's own defaults.
func TestSweep_SendsTheSchedulesModelPick(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	e := st.Clocks()[0].Rule
	e.Model, e.Effort = "opus", "high"
	if _, _, err := st.Replace(t.Context(), e.ID, &e, 0); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	r.sweep(t.Context())

	l.mu.Lock()
	got := slices.Clone(l.defaults)
	l.mu.Unlock()
	if want := []marotte.RunDefaults{{Model: "opus", Effort: "high"}}; !slices.Equal(got, want) {
		t.Errorf("launch defaults = %+v, want %+v", got, want)
	}
}

func TestFirePassesInputs(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	e := st.Clocks()[0].Rule
	e.Inputs = map[string]string{"lane": "fast", "cap": "3"}
	if _, _, err := st.Replace(t.Context(), e.ID, &e, 0); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	r.sweep(t.Context())

	l.mu.Lock()
	got := slices.Clone(l.inputs)
	l.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("launches = %d, want 1", len(got))
	}
	if want := map[string]string{"lane": "fast", "cap": "3"}; !maps.Equal(got[0], want) {
		t.Errorf("launch inputs = %v, want %v", got[0], want)
	}
}

// TestSweep_BoundsTheRunByItsOwnInterval pins the run bound to nextRun's own answer for the
// spec, measured from DUE (the fire is 30s late inside the grace window), not from now.
func TestSweep_BoundsTheRunByItsOwnInterval(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	r.sweep(t.Context())

	_, _, slots := l.snap()
	if len(slots) != 1 {
		t.Fatalf("expected one launch, got %d", len(slots))
	}
	want, err := launchrule.NextRun(*st.Clocks()[0].When.Clock, due)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !slots[0].Equal(want) {
		t.Errorf("run bound = %v, want the next slot %v (the interval IS the number)", slots[0], want)
	}
	if !slots[0].Equal(due.Add(24 * time.Hour)) {
		t.Errorf("a daily schedule must bound its run at 24h after the slot, got %v", slots[0].Sub(due))
	}
}

// TestSweep_SkipsASlotMissedWhileOffline is the recorded decision: a slot late
// by more than the grace is skipped, not fired, and the schedule resumes.
func TestSweep_SkipsASlotMissedWhileOffline(t *testing.T) {
	now := at(2026, time.August, 4, 9, 0) // 7h after the 02:00 slot
	st, l, r := newFixture(t, now)
	r.sweep(t.Context())

	if srcs, _, _ := l.snap(); len(srcs) != 0 {
		t.Errorf("a slot missed while offline must not fire: %v", srcs)
	}
	got := st.Clocks()[0]
	if !got.Anchor.Equal(now) {
		t.Errorf("anchor must advance past the missed slot: got %v want %v", got.Anchor, now)
	}
	if !got.LastRunAt.IsZero() || got.LastStatus != "" {
		t.Errorf("a skip must not record a run: LastRunAt = %v, LastStatus = %q", got.LastRunAt, got.LastStatus)
	}
	next, err := launchrule.NextRun(*got.When.Clock, got.Anchor)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if want := at(2026, time.August, 5, 2, 0); !next.Equal(want) {
		t.Errorf("next slot after a skip = %v, want %v", next, want)
	}
}

// TestSweep_DoesNotFireBeforeDue keeps a schedule from running early.
func TestSweep_DoesNotFireBeforeDue(t *testing.T) {
	_, l, r := newFixture(t, at(2026, time.August, 4, 1, 59))
	r.sweep(t.Context())
	if srcs, _, _ := l.snap(); len(srcs) != 0 {
		t.Errorf("fired before its due time: %v", srcs)
	}
}

// TestSweep_FiresOnlyOncePerSlot is what the anchor is for: a second tick inside
// the same grace window must not launch again.
func TestSweep_FiresOnlyOncePerSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	_, l, r := newFixture(t, due.Add(10*time.Second))
	r.sweep(t.Context())
	r.now = func() time.Time { return due.Add(70 * time.Second) }
	r.sweep(t.Context())

	if got := l.launched(); got != 1 {
		t.Errorf("expected exactly one launch across two ticks, got %d", got)
	}
}

// TestSweep_SkipsDisabled covers the enable toggle.
func TestSweep_SkipsDisabled(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	e := st.Clocks()[0].Rule
	e.Enabled = false
	if _, _, err := st.Replace(t.Context(), e.ID, &e, 0); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	r.sweep(t.Context())
	if srcs, _, _ := l.snap(); len(srcs) != 0 {
		t.Errorf("a disabled schedule must not fire: %v", srcs)
	}
}

// TestSweep_AdvancesPastAFailedLaunch stops a failing recipe (or the
// one-live-run-per-recipe refusal) from retrying on every tick.
func TestSweep_AdvancesPastAFailedLaunch(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	l.failWith(errors.New("this recipe already has a live run"))
	r.sweep(t.Context())

	got := st.Clocks()[0]
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor must advance even on failure: got %v", got.Anchor)
	}
	if got.LastStatus != launchrule.StatusFailed || got.LastReason != "this recipe already has a live run" {
		t.Errorf("last outcome = (%q, %q), want (%q, the launch error)", got.LastStatus, got.LastReason, launchrule.StatusFailed)
	}
	r.sweep(t.Context())
	if got := l.launched(); got != 1 {
		t.Errorf("a failed launch must not be retried on the next tick, got %d attempts", got)
	}
}

// A slot refused by a run holding this schedule's lease, bounded by this slot, leaves that run on the row: the run is
// cancelled at this slot, and its outcome, landing before or after the refusal, is the slot's story. A refusal by any
// other run, the row's own workflow id retried under a manual lease included, is the row's outcome and detaches it.
func TestSweep_ABusyRefusalByTheRowsOwnRunKeepsItsOutcome(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	overran := launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "the run was still going at its next slot, so it was cancelled"}
	setup := func(t *testing.T) (*launchrule.Store, *fakeLauncher, *Runner, launchrule.Run) {
		t.Helper()
		st, l, r := newFixture(t, due.Add(30*time.Second))
		row := st.Clocks()[0].Row()
		run := launchrule.Run{ScheduleID: row.ID, WorkflowID: "wf-old"}
		if err := st.RecordStart(t.Context(), run); err != nil {
			t.Fatalf("RecordStart: %v", err)
		}
		return st, l, r, run
	}

	t.Run("its outcome lands after the refusal", func(t *testing.T) {
		st, l, r, run := setup(t)
		l.failWith(&BusyError{WorkflowID: run.WorkflowID, ScheduleID: run.ScheduleID, SlotAt: due})
		r.sweep(t.Context())
		got := st.Clocks()[0]
		if !got.Anchor.Equal(due) || got.LastRunID != run.WorkflowID || got.LastStatus != launchrule.StatusStarted {
			t.Fatalf("after the refusal the row = %+v, want the anchor at the slot and wf-old still started", got)
		}
		if err := st.RecordOutcome(t.Context(), run, overran); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if got := st.Clocks()[0]; got.LastStatus != overran.Status || got.LastReason != overran.Reason {
			t.Errorf("the row reads (%q, %q), want the live run's overran outcome", got.LastStatus, got.LastReason)
		}
	})

	t.Run("its outcome landed before the refusal", func(t *testing.T) {
		st, l, r, run := setup(t)
		if err := st.RecordOutcome(t.Context(), run, overran); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		l.failWith(&BusyError{WorkflowID: run.WorkflowID, ScheduleID: run.ScheduleID, SlotAt: due})
		r.sweep(t.Context())
		if got := st.Clocks()[0]; !got.Anchor.Equal(due) || got.LastReason != overran.Reason {
			t.Errorf("the row = %+v, want the anchor at the slot and the overran outcome kept", got)
		}
	})

	// The launch granted wf-old (RecordStart) and then failed to start it: no run is live, the failure is the slot's.
	t.Run("the granted run never started", func(t *testing.T) {
		st, l, r, _ := setup(t)
		l.failWith(errors.New("workflow invoke: engine gone"))
		r.sweep(t.Context())
		if got := st.Clocks()[0]; got.LastStatus != launchrule.StatusFailed || got.LastReason != "workflow invoke: engine gone" ||
			got.LastRunID != "" {
			t.Errorf("the row = %+v, want the launch failure recorded and no run tracked", got)
		}
	})

	t.Run("another run holds the recipe", func(t *testing.T) {
		st, l, r, run := setup(t)
		if err := st.RecordEnding(t.Context(), run, launchrule.Outcome{Status: launchrule.StatusCompleted}); err != nil {
			t.Fatalf("RecordEnding: %v", err)
		}
		l.failWith(&BusyError{WorkflowID: "wf-manual"})
		r.sweep(t.Context())
		got := st.Clocks()[0]
		if got.LastStatus != launchrule.StatusFailed || got.LastReason != "this recipe already has a live run" || got.LastRunID != "" {
			t.Fatalf("the row = %+v, want the refusal recorded and no run tracked", got)
		}
		if err := st.RecordOutcome(t.Context(), run, overran); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if got := st.Clocks()[0]; got.LastReason != "this recipe already has a live run" {
			t.Errorf("a late outcome of the ended wf-old replaced the slot's refusal: %+v", got)
		}
	})

	// No bound of this schedule ends these runs at this slot, so the refusal is the only outcome the slot gets.
	for name, busy := range map[string]func(launchrule.Run) *BusyError{
		"a person's retry of the row's run holds the recipe": func(run launchrule.Run) *BusyError {
			return &BusyError{WorkflowID: run.WorkflowID}
		},
		"the row's run is bounded by a later slot": func(run launchrule.Run) *BusyError {
			return &BusyError{WorkflowID: run.WorkflowID, ScheduleID: run.ScheduleID, SlotAt: due.Add(24 * time.Hour)}
		},
		"another schedule's run holds the recipe": func(run launchrule.Run) *BusyError {
			return &BusyError{WorkflowID: run.WorkflowID, ScheduleID: "other-schedule", SlotAt: due}
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, l, r, run := setup(t)
			if err := st.RecordEnding(t.Context(), run, launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "step build failed"}); err != nil {
				t.Fatalf("RecordEnding: %v", err)
			}
			l.failWith(busy(run))
			r.sweep(t.Context())
			got := st.Clocks()[0]
			if !got.Anchor.Equal(due) || got.LastStatus != launchrule.StatusFailed ||
				got.LastReason != "this recipe already has a live run" || got.LastRunID != "" {
				t.Errorf("the row = %+v, want the anchor at the slot, the refusal recorded and no run tracked", got)
			}
		})
	}
}

// A launch refused with ErrNoRunCanStart is held, not recorded: the slot fires on a later tick inside its grace,
// a hold that outlasts the grace is recorded failed with the refusal, not skipped as an offline miss, and any other
// launch error is recorded.
func TestSweep_HoldsASlotWhileNoRunCanStart(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	anchor := at(2026, time.August, 3, 12, 0)
	refused := fmt.Errorf("%w: %w", ErrNoRunCanStart, errors.New("sign in to Kiro first"))

	t.Run("held then fired", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(refused)
		logs := captureLogs(t)
		counted := slotsNotStarted(t)
		r.sweep(t.Context())
		if got := logs.String(); !strings.Contains(got, "schedule slot held until a run can start") ||
			!strings.Contains(got, "sign in to Kiro first") || strings.Contains(got, "scheduled run did not start") {
			t.Errorf("logs = %q, want the hold and its reason, not a launch failure", got)
		}
		// A held slot may still fire inside its grace, so it is no slot that produced no run yet.
		for event, n := range slotsNotStarted(t) {
			if n != counted[event] {
				t.Errorf("a held slot moved the %s slots by %v, want 0", event, n-counted[event])
			}
		}
		if got := st.Clocks()[0]; !got.Anchor.Equal(anchor) || got.LastStatus != "" {
			t.Errorf("entry = %+v, want the anchor and outcome untouched", got)
		}
		l.failWith(nil)
		r.now = func() time.Time { return due.Add(90 * time.Second) }
		r.sweep(t.Context())
		if got := st.Clocks()[0]; !got.Anchor.Equal(due) || got.LastStatus != launchrule.StatusStarted {
			t.Errorf("entry = %+v, want the held slot fired on the next tick", got)
		}
	})

	t.Run("a held slot whose next launch fails is recorded", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(refused)
		r.sweep(t.Context())
		l.failWith(errors.New("this recipe already has a live run"))
		r.now = func() time.Time { return due.Add(90 * time.Second) }
		r.sweep(t.Context())
		if got := st.Clocks()[0]; !got.Anchor.Equal(due) || got.LastStatus != launchrule.StatusFailed {
			t.Errorf("entry = %+v, want the failure recorded and the anchor advanced", got)
		}
		if n := l.launched(); n != 2 {
			t.Errorf("launches = %d, want the held slot tried once more and no more", n)
		}
	})

	t.Run("held past the grace", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(refused)
		r.sweep(t.Context())
		logs := captureLogs(t)
		missedAt := due.Add(missGrace + time.Minute)
		r.now = func() time.Time { return missedAt }
		r.sweep(t.Context())
		if n := l.launched(); n != 1 {
			t.Errorf("launches = %d, want none for a slot past its grace", n)
		}
		if got := logs.String(); !strings.Contains(got, "schedule slot missed while no run could start") {
			t.Errorf("logs = %q, want the miss named for the hold", got)
		}
		got := st.Clocks()[0]
		if got.LastStatus != launchrule.StatusFailed || got.LastReason != refused.Error() || !got.LastRunAt.Equal(due) {
			t.Errorf("row = (%q, %q, %v), want the slot %v failed with the refusal %q",
				got.LastStatus, got.LastReason, got.LastRunAt, due, refused.Error())
		}
		if !got.Anchor.Equal(missedAt) || got.LastRunID != "" {
			t.Errorf("anchor %v, run %q, want the anchor at %v and no run tracked", got.Anchor, got.LastRunID, missedAt)
		}
	})

	// The run the previous slot started may still be live, parked on the same sign-in; its late outcome is not this slot's.
	t.Run("a missed slot detaches the previous run", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		row := st.Clocks()[0].Row()
		prev := launchrule.Run{ScheduleID: row.ID, WorkflowID: "wf-old"}
		if err := st.RecordStart(t.Context(), prev); err != nil {
			t.Fatalf("RecordStart: %v", err)
		}
		l.failWith(refused)
		r.sweep(t.Context())
		r.now = func() time.Time { return due.Add(missGrace + time.Minute) }
		r.sweep(t.Context())
		if err := st.RecordOutcome(t.Context(), prev, launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "stopped"}); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if got := st.Clocks()[0]; got.LastStatus != launchrule.StatusFailed || got.LastReason != refused.Error() {
			t.Errorf("row = (%q, %q), want the missed slot's refusal kept", got.LastStatus, got.LastReason)
		}
	})

	// The slot was missed while the schedule was off, not while no run could start.
	t.Run("a hold ends when its schedule is disabled", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(refused)
		r.sweep(t.Context())
		e := st.Clocks()[0].Rule
		e.Enabled = false
		if _, _, err := st.Replace(t.Context(), e.ID, &e, 0); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		r.sweep(t.Context())
		e.Enabled = true
		if _, _, err := st.Replace(t.Context(), e.ID, &e, 0); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		logs := captureLogs(t)
		r.now = func() time.Time { return due.Add(missGrace + time.Minute) }
		r.sweep(t.Context())
		if got := logs.String(); !strings.Contains(got, "schedule slot missed while offline") {
			t.Errorf("logs = %q, want the miss of a slot no hold covers", got)
		}
	})

	t.Run("a deleted schedule keeps no hold", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(refused)
		r.sweep(t.Context())
		if _, err := st.Delete(t.Context(), st.Clocks()[0].ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		r.sweep(t.Context())
		if len(r.held) != 0 {
			t.Errorf("held = %v, want none for a deleted schedule", r.held)
		}
	})
}

// TestSweep_FiresASlotExactlyAtTheGraceEdge pins that a slot exactly missGrace late still
// fires; the other branch advances the anchor and loses the occurrence.
func TestSweep_FiresASlotExactlyAtTheGraceEdge(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(missGrace))
	r.sweep(t.Context())

	if got := l.launched(); got != 1 {
		t.Fatalf("a slot exactly %v late launched %d times, want 1", missGrace, got)
	}
	got := st.Clocks()[0]
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor = %v, want the due time %v", got.Anchor, due)
	}
	if got.LastStatus != launchrule.StatusStarted {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, launchrule.StatusStarted)
	}

	_, late, lateRunner := newFixture(t, due.Add(missGrace+time.Second))
	lateRunner.sweep(t.Context())
	if n := late.launched(); n != 0 {
		t.Errorf("a slot %v late launched %d times, want 0 (missed while offline)", missGrace+time.Second, n)
	}
}

// TestSweep_ReportsNoStoreFailureOnTheOrdinaryPaths pins that a successful sweep logs no
// store Error: those lines are the only signal that an anchor did not move.
func TestSweep_ReportsNoStoreFailureOnTheOrdinaryPaths(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)

	t.Run("a fire records its outcome silently", func(t *testing.T) {
		_, l, r := newFixture(t, due.Add(30*time.Second))
		logs := captureLogs(t)
		r.sweep(t.Context())
		if n := l.launched(); n != 1 {
			t.Fatalf("expected one launch, got %d", n)
		}
		if got := logs.String(); strings.Contains(got, "schedule record failed") {
			t.Errorf("logs = %q, must not report a record failure for a fire that recorded fine", got)
		}
	})

	t.Run("a missed slot advances its anchor silently", func(t *testing.T) {
		now := due.Add(7 * time.Hour)
		st, _, r := newFixture(t, now)
		logs := captureLogs(t)
		r.sweep(t.Context())
		if got := st.Clocks()[0]; !got.Anchor.Equal(now) {
			t.Fatalf("anchor = %v, want it advanced to %v", got.Anchor, now)
		}
		if got := logs.String(); strings.Contains(got, "schedule skip failed") {
			t.Errorf("logs = %q, must not report a skip failure for a skip that took", got)
		}
	})
}

// The handler is global, so this package's tests never run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestMissGraceExceedsTick pins the relationship the classification depends on:
// were the grace shorter than the tick, a slot landing between ticks would be
// called missed and never run.
func TestMissGraceExceedsTick(t *testing.T) {
	if missGrace <= tickInterval {
		t.Errorf("MissGrace (%v) must exceed TickInterval (%v) or in-window slots are misclassified as missed",
			missGrace, tickInterval)
	}
}

// TestRecordOutcome_DoesNotMoveTheAnchor pins that a late outcome leaves the anchor, or the
// schedule drifts later every time it fails.
func TestRecordOutcome_DoesNotMoveTheAnchor(t *testing.T) {
	st := mustStore(t)
	ctx := t.Context()
	e, err := st.Create(ctx, dailyAt2("bundled://x"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	due := at(2026, time.August, 4, 2, 0)
	if _, err := NewRunner(st, nil).recordFire(ctx, e.Row(), due, "wf-1",
		launchrule.Outcome{Status: launchrule.StatusStarted}); err != nil {
		t.Fatalf("recordFire: %v", err)
	}

	denial := launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "needed approval for Write with nobody watching"}
	if err := st.RecordOutcome(ctx, launchrule.Run{ScheduleID: e.ID, WorkflowID: "wf-1"}, denial); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	got := st.Clocks()[0]
	if got.LastStatus != denial.Status || got.LastReason != denial.Reason {
		t.Errorf("last outcome = (%q, %q), want the denial %+v", got.LastStatus, got.LastReason, denial)
	}
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor moved: got %v want %v — the next run would drift", got.Anchor, due)
	}
}

// anchorAt moves a clock rule's anchor, which the store sets to the rule's creation time.
func anchorAt(t *testing.T, st *launchrule.Store, row launchrule.Row, anchor time.Time) {
	t.Helper()
	if err := st.UpdateClock(t.Context(), row, func(s *launchrule.Status) bool { s.Anchor = anchor; return true }); err != nil {
		t.Fatalf("UpdateClock: %v", err)
	}
}

// clockSeed is an enabled clock rule a test stores, anchored at Anchor, or at its creation when that is zero.
type clockSeed struct {
	Anchor time.Time
	Source string
	Spec   launchrule.Spec
}

func putClock(t *testing.T, st *launchrule.Store, c clockSeed) launchrule.Rule {
	t.Helper()
	spec := c.Spec
	r, err := st.Create(t.Context(), &launchrule.Rule{Source: c.Source, Enabled: true, When: launchrule.When{Clock: &spec}})
	if err != nil {
		t.Fatalf("Create(%s): %v", c.Source, err)
	}
	if !c.Anchor.IsZero() {
		anchorAt(t, st, r.Row(), c.Anchor)
	}
	return r
}

// waitOn marks the rule's row as waiting on the engine for the slot due.
func waitOn(t *testing.T, st *launchrule.Store, row launchrule.Row, due time.Time) {
	t.Helper()
	if err := NewRunner(st, nil).setWaiting(t.Context(), row, due); err != nil {
		t.Fatalf("setWaiting: %v", err)
	}
}

func dailyAt2(source string) *launchrule.Rule {
	return &launchrule.Rule{
		Source: source, Enabled: true,
		When: launchrule.When{Clock: &launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}},
	}
}

func TestClockRuleFromLaunchRuleStore(t *testing.T) {
	dir := t.TempDir()
	st, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	rule, err := st.Create(t.Context(), dailyAt2("bundled://demo"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	anchorAt(t, st, rule.Row(), at(2026, time.August, 3, 12, 0))
	due := at(2026, time.August, 4, 2, 0)
	l := &fakeLauncher{}
	r := NewRunner(st, l)
	r.now = func() time.Time { return due.Add(30 * time.Second) }
	r.sweep(t.Context())

	if _, ids, _ := l.snap(); !slices.Equal(ids, []string{rule.ID}) {
		t.Errorf("launches attributed to %v, want the one rule %s", ids, rule.ID)
	}
	reopened, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Clock(rule.ID)
	if !ok || got.Err != nil {
		t.Fatalf("Clock(%s) = %+v, %v", rule.ID, got, ok)
	}
	if !got.Anchor.Equal(due) || got.LastRunID != "wf-1" || got.LastStatus != launchrule.StatusStarted {
		t.Errorf("stored row = (anchor %v, run %q, %q), want (%v, wf-1, started)", got.Anchor, got.LastRunID, got.LastStatus, due)
	}
}

func TestClockRunnerSkipsUnreadableRule(t *testing.T) {
	dir := t.TempDir()
	st, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	rule, err := st.Create(t.Context(), dailyAt2("bundled://demo"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(dir, "launch-state", rule.ID+".json")
	const broken = `{"status":{"anchor":`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	l := &fakeLauncher{}
	r := NewRunner(reopened, l)
	r.now = func() time.Time { return at(2026, time.August, 4, 2, 0).Add(30 * time.Second) }
	logs := captureLogs(t)
	r.sweep(t.Context())

	t.Run("it does not fire", func(t *testing.T) {
		if n := l.launched(); n != 0 {
			t.Errorf("launched %d runs, want none", n)
		}
	})
	t.Run("its row shows the read error", func(t *testing.T) {
		got, ok := reopened.Clock(rule.ID)
		want := "could not read launch-state/" + rule.ID + ".json: "
		if !ok || got.Err == nil || !strings.HasPrefix(got.Err.Error(), want) {
			t.Errorf("Clock(%s) = %v, %v, want an error starting %q", rule.ID, got.Err, ok, want)
		}
	})
	t.Run("its slot is not settled", func(t *testing.T) {
		if got := logs.String(); strings.Contains(got, "schedule record failed") || strings.Contains(got, "skipping") {
			t.Errorf("logs = %q, want no slot judged for a rule whose anchor is unknown", got)
		}
	})
	t.Run("the file is unchanged", func(t *testing.T) {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != broken {
			t.Errorf("state file = %q, %v, want it untouched", raw, err)
		}
	})
}

// A run can end before its launch returns, so its ending reaches the row before the fire is recorded; the
// fire then advances the anchor and keeps that ending.
func TestRecordFireKeepsAnEndingItsRunRecordedFirst(t *testing.T) {
	st := mustStore(t)
	e, err := st.Create(t.Context(), dailyAt2("bundled://x"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	run := launchrule.Run{ScheduleID: e.ID, WorkflowID: "wf-1"}
	if err := st.RecordStart(t.Context(), run); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	fast := launchrule.Outcome{Status: launchrule.StatusFailed, Reason: "step build failed"}
	if err := st.RecordEnding(t.Context(), run, fast); err != nil {
		t.Fatalf("RecordEnding: %v", err)
	}
	due := at(2026, time.August, 4, 2, 0)
	if _, err := NewRunner(st, nil).recordFire(t.Context(), e.Row(), due, "wf-1",
		launchrule.Outcome{Status: launchrule.StatusStarted}); err != nil {
		t.Fatalf("recordFire: %v", err)
	}
	got := st.Clocks()[0]
	if got.LastStatus != fast.Status || got.LastReason != fast.Reason {
		t.Errorf("row = (%q, %q), want the run's own ending %+v", got.LastStatus, got.LastReason, fast)
	}
	if !got.Anchor.Equal(due) || !got.LastRunAt.Equal(due) {
		t.Errorf("anchor %v, last run %v, want both at the slot %v", got.Anchor, got.LastRunAt, due)
	}
}
