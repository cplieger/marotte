package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// newNamedFixture is newFixture with the rule saved under a recipe name, which the push names.
func newNamedFixture(t *testing.T, now time.Time) (*launchrule.Store, *fakeLauncher, *Runner) {
	t.Helper()
	return newNamedFixtureIn(t, t.TempDir(), now)
}

// newNamedFixtureIn is newNamedFixture with the store in dir.
func newNamedFixtureIn(t *testing.T, dir string, now time.Time) (*launchrule.Store, *fakeLauncher, *Runner) {
	t.Helper()
	st := mustStoreIn(t, dir)
	rule := dailyAt2("bundled://demo")
	rule.Name = "nightly"
	e, err := st.Create(t.Context(), rule)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	anchorAt(t, st, e.Row(), at(2026, time.August, 3, 12, 0))
	l := &fakeLauncher{}
	r := NewRunner(st, l)
	r.now = func() time.Time { return now }
	return st, l, r
}

// Three slots in a row that started no run tell the user once, on the first; a slot whose run completes ends the
// streak, so the next failed slot tells them again.
func TestSweep_PushesOnlyTheFirstOfConsecutiveSlotsThatStartedNoRun(t *testing.T) {
	st, l, r := newNamedFixture(t, time.Time{})
	id := st.Clocks()[0].ID
	l.failWith(errors.New("workflow new: no such recipe"))
	for day := 4; day <= 6; day++ {
		r.now = func() time.Time { return at(2026, time.August, day, 2, 0).Add(30 * time.Second) }
		r.sweep(t.Context())
	}
	want := notStartedCall{ruleID: id, name: "nightly", reason: "workflow new: no such recipe"}
	if got := l.notices(); len(got) != 1 || got[0] != want {
		t.Fatalf("pushes after three failed slots = %+v, want one: %+v", got, want)
	}

	l.failWith(nil)
	r.now = func() time.Time { return at(2026, time.August, 7, 2, 0).Add(30 * time.Second) }
	r.sweep(t.Context())
	run := launchrule.Run{ScheduleID: id, WorkflowID: st.Clocks()[0].LastRunID}
	if err := st.RecordEnding(t.Context(), run, launchrule.Outcome{Status: launchrule.StatusCompleted}); err != nil {
		t.Fatalf("RecordEnding: %v", err)
	}
	l.failWith(errors.New("workflow new: the engine refused"))
	r.now = func() time.Time { return at(2026, time.August, 8, 2, 0).Add(30 * time.Second) }
	r.sweep(t.Context())
	got := l.notices()
	if len(got) != 2 {
		t.Fatalf("pushes after a success and a new failure = %+v, want two", got)
	}
	if got[1].reason != "workflow new: the engine refused" {
		t.Errorf("second push reason = %q, want the new failure's", got[1].reason)
	}
}

// Each way a slot ends with no run pushes on the first such slot, carrying the reason its row records. The engine-busy
// case fails the next slot too, which pushes nothing: the streak started at the skipped one.
func TestSweep_EachSlotThatStartedNoRunPushes(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	refused := fmt.Errorf("%w: %w", ErrNoRunCanStart, errors.New("sign in to Kiro first"))
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	for _, tc := range []struct {
		drive  func(t *testing.T, l *fakeLauncher, r *Runner)
		name   string
		reason string
	}{
		{name: "a launch error", reason: "workflow new: no such recipe", drive: func(t *testing.T, l *fakeLauncher, r *Runner) {
			l.failWith(errors.New("workflow new: no such recipe"))
			r.sweep(t.Context())
		}},
		{name: "a run created but not started", reason: "workflow invoke: refused", drive: func(t *testing.T, l *fakeLauncher, r *Runner) {
			l.failID = "wf-created"
			l.failWith(errors.New("workflow invoke: refused"))
			r.sweep(t.Context())
		}},
		{name: "a sign-in held past the grace", reason: refused.Error(), drive: func(t *testing.T, l *fakeLauncher, r *Runner) {
			l.failWith(refused)
			r.sweep(t.Context())
			r.now = func() time.Time { return due.Add(missGrace + time.Minute) }
			r.sweep(t.Context())
		}},
		{name: "an engine busy until the next slot", reason: reasonEngineBusy, drive: func(t *testing.T, l *fakeLauncher, r *Runner) {
			l.failWith(busy)
			r.sweep(t.Context())
			l.failWith(errors.New("workflow new: no such recipe"))
			r.now = func() time.Time { return due.AddDate(0, 0, 1).Add(30 * time.Second) }
			r.sweep(t.Context())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, l, r := newNamedFixture(t, due.Add(30*time.Second))
			tc.drive(t, l, r)
			row := st.Clocks()[0]
			got := l.notices()
			if len(got) != 1 {
				t.Fatalf("pushes = %+v, want one", got)
			}
			if got[0].ruleID != row.ID || got[0].name != "nightly" {
				t.Errorf("push names rule (%q, %q), want (%q, nightly)", got[0].ruleID, got[0].name, row.ID)
			}
			if got[0].reason != tc.reason {
				t.Errorf("push reason = %q, want %q", got[0].reason, tc.reason)
			}
		})
	}
}

// A slot ends the way no push follows: the streak already started, the run's own bound refused it, marotte was down,
// the slot is still inside its grace or its wait on the engine, or its row was never written.
func TestSweep_PushesNothingForASlotThatIsNotTheFirstToStartNoRun(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	for _, tc := range []struct {
		drive func(t *testing.T, dir string, st *launchrule.Store, l *fakeLauncher, r *Runner)
		name  string
	}{
		{name: "the previous slot's run failed", drive: func(t *testing.T, _ string, st *launchrule.Store, l *fakeLauncher, r *Runner) {
			row := st.Clocks()[0].Row()
			prev := launchrule.Run{ScheduleID: row.ID, WorkflowID: "wf-old"}
			if err := st.RecordStart(t.Context(), prev); err != nil {
				t.Fatalf("RecordStart: %v", err)
			}
			if err := st.RecordEnding(t.Context(), prev, launchrule.Outcome{Status: launchrule.StatusFailed}); err != nil {
				t.Fatalf("RecordEnding: %v", err)
			}
			l.failWith(errors.New("workflow new: no such recipe"))
			r.sweep(t.Context())
		}},
		{name: "a slot whose run starts after a failed one", drive: func(t *testing.T, _ string, st *launchrule.Store, _ *fakeLauncher, r *Runner) {
			row := st.Clocks()[0].Row()
			prev := launchrule.Run{ScheduleID: row.ID, WorkflowID: "wf-old"}
			if err := st.RecordStart(t.Context(), prev); err != nil {
				t.Fatalf("RecordStart: %v", err)
			}
			if err := st.RecordEnding(t.Context(), prev, launchrule.Outcome{Status: launchrule.StatusFailed}); err != nil {
				t.Fatalf("RecordEnding: %v", err)
			}
			r.sweep(t.Context())
		}},
		{name: "the row's own run is bounded at this slot", drive: func(t *testing.T, _ string, st *launchrule.Store, l *fakeLauncher, r *Runner) {
			l.failWith(&BusyError{WorkflowID: "wf-live", ScheduleID: st.Clocks()[0].ID, SlotAt: due})
			r.sweep(t.Context())
		}},
		{name: "a slot missed while offline", drive: func(t *testing.T, _ string, _ *launchrule.Store, _ *fakeLauncher, r *Runner) {
			r.now = func() time.Time { return due.Add(2 * time.Hour) }
			r.sweep(t.Context())
		}},
		{name: "a sign-in hold inside the grace", drive: func(t *testing.T, _ string, _ *launchrule.Store, l *fakeLauncher, r *Runner) {
			l.failWith(fmt.Errorf("%w: %w", ErrNoRunCanStart, errors.New("sign in to Kiro first")))
			r.sweep(t.Context())
		}},
		{name: "a wait on the engine before the next slot", drive: func(t *testing.T, _ string, _ *launchrule.Store, l *fakeLauncher, r *Runner) {
			l.failWith(fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded")))
			r.sweep(t.Context())
		}},
		{name: "a failed launch whose rule is deleted meanwhile", drive: func(t *testing.T, _ string, st *launchrule.Store, l *fakeLauncher, r *Runner) {
			// Two guards hold this: the ErrNotFound arm, and the zero row recordFire returns when UpdateClock never ran
			// its callback.
			l.failWith(errors.New("workflow new: no such recipe"))
			l.during = func(string) {
				if _, err := st.Delete(t.Context(), st.Clocks()[0].ID); err != nil {
					t.Errorf("Delete: %v", err)
				}
			}
			r.sweep(t.Context())
		}},
		{name: "a failed launch whose row write fails", drive: func(t *testing.T, dir string, st *launchrule.Store, l *fakeLauncher, r *Runner) {
			// A directory where the state file goes fails the write, even as root.
			path := filepath.Join(dir, "launch-state", st.Clocks()[0].ID+".json")
			l.failWith(errors.New("workflow new: no such recipe"))
			l.during = func(string) {
				if err := os.Remove(path); err != nil {
					t.Errorf("Remove: %v", err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Errorf("Mkdir: %v", err)
				}
			}
			r.sweep(t.Context())
			if got := st.Clocks()[0].FailedSlots; got != 0 {
				t.Fatalf("FailedSlots = %d after a write that failed, want 0", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, l, r := newNamedFixtureIn(t, dir, due.Add(30*time.Second))
			tc.drive(t, dir, st, l, r)
			if got := l.notices(); len(got) != 0 {
				t.Errorf("pushes = %+v, want none", got)
			}
		})
	}
}
