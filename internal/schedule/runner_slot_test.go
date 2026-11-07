package schedule

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// Each row is one transition of the daily 02:00 slot: the state a sweep finds it in, the event that sweep meets, and
// the row it persists.
func TestSweep_SettlesEachSlotTransition(t *testing.T) {
	yesterday := at(2026, time.August, 3, 12, 0)
	due := at(2026, time.August, 4, 2, 0)
	next := due.AddDate(0, 0, 1)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	refused := fmt.Errorf("%w: %w", ErrNoRunCanStart, errors.New("sign in to Kiro first"))
	failure := errors.New("workflow new: no such recipe")
	notInvoked := errors.New("workflow invoke: the engine did not answer")
	stopped := fmt.Errorf("workflow list call: %w", context.Canceled)
	ownRun := &BusyError{WorkflowID: "wf-0", SlotAt: due}

	type row struct {
		anchor, lastRunAt, waitingFor time.Time
		status                        launchrule.OutcomeStatus
		reason, runID                 string
		failedSlots                   int
	}
	cases := []struct {
		clock  time.Time
		launch error
		want   row
		name   string
		// from is the state the sweep finds: "due"; "held" (the sign-in gate refused it at 02:00:30); "waiting" (busy
		// at 02:00:30); "held, then waiting" (refused at 02:00:30, busy at 02:01:30); "started" (a launch at 02:00:30
		// recorded wf-2 on the row and nothing recorded the slot); "ended" (that, then wf-2 completed); "previous
		// run" (wf-0, started at yesterday's anchor).
		from   string
		failID string
		// notStarted is the slot event the sweep counts as a slot that produced no run, "" for none.
		notStarted string
		launches   int
		cancel     bool // the runner's context ends during the launch
		started    bool // the launch records wf-2 on the row before it answers, as the host's lease grant does
	}{
		{
			name: "due, launch ok: launched", from: "due", clock: due.Add(30 * time.Second),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-1"},
		},
		{
			name: "due, engine busy: waiting", from: "due", clock: due.Add(30 * time.Second), launch: busy,
			launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "due, launch error: failed", from: "due", clock: due.Add(30 * time.Second), launch: failure,
			launches: 1, notStarted: "launch_failed", want: row{
				anchor: due, lastRunAt: due, status: launchrule.StatusFailed, reason: failure.Error(), failedSlots: 1,
			},
		},
		{
			name: "due, a run created but not started: failed with that run", from: "due",
			clock: due.Add(30 * time.Second), launch: notInvoked, failID: "wf-2", launches: 1, notStarted: "launch_failed",
			want: row{
				anchor: due, lastRunAt: due, status: launchrule.StatusFailed, reason: notInvoked.Error(), runID: "wf-2",
				failedSlots: 1,
			},
		},
		{
			name: "due, refused by its own run bounded at this slot: skipped, outcome kept", from: "due",
			clock: due.Add(30 * time.Second), launch: ownRun, launches: 1, want: row{anchor: due},
		},
		{
			name: "due, sign-in refusal: held", from: "due", clock: due.Add(30 * time.Second), launch: refused,
			launches: 1, want: row{anchor: yesterday},
		},
		{
			name: "due, cancelled before a run existed: left as it was", from: "due", clock: due.Add(30 * time.Second),
			launch: stopped, cancel: true, launches: 1, want: row{anchor: yesterday},
		},
		{
			name: "due, cancelled once the run started: launched", from: "due", clock: due.Add(30 * time.Second),
			cancel: true, launches: 1, want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-1"},
		},
		{
			name: "due, cancelled after the run was created: launched", from: "due", clock: due.Add(30 * time.Second),
			launch: stopped, failID: "wf-2", cancel: true, started: true, launches: 1,
			want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-2"},
		},
		{
			name: "due, cancelled after the run was recorded but before its id came back: the row names it",
			from: "due", clock: due.Add(30 * time.Second), launch: stopped, cancel: true, started: true, launches: 1,
			want: row{anchor: yesterday, lastRunAt: due.Add(30 * time.Second), status: launchrule.StatusStarted, runID: "wf-2"},
		},
		{
			name: "due, grace expired: missed while offline", from: "due", clock: due.Add(4 * time.Minute),
			want: row{anchor: due.Add(4 * time.Minute)},
		},
		{
			name: "started, inside its grace: launched, not fired again", from: "started", clock: due.Add(2 * time.Minute),
			want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-2"},
		},
		{
			name: "started, past its grace: launched, its run kept", from: "started", clock: due.Add(5 * time.Hour),
			want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-2"},
		},
		{
			name: "ended, inside its grace: launched, its ending kept", from: "ended", clock: due.Add(2 * time.Minute),
			want: row{anchor: due, lastRunAt: due, status: launchrule.StatusCompleted, runID: "wf-2"},
		},
		{
			name: "previous run, due: launched", from: "previous run", clock: due.Add(30 * time.Second),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-1"},
		},
		{
			name: "held, launch ok: launched", from: "held", clock: due.Add(2 * time.Minute),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-1"},
		},
		{
			name: "held, engine busy: waiting", from: "held", clock: due.Add(90 * time.Second), launch: busy,
			launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "held, grace expired: failed with the refusal", from: "held", clock: due.Add(4 * time.Minute),
			notStarted: "signin_held", want: row{
				anchor: due.Add(4 * time.Minute), lastRunAt: due, status: launchrule.StatusFailed, reason: refused.Error(),
				failedSlots: 1,
			},
		},
		{
			name: "held, then waiting, overtaken with the next slot busy: skipped for the engine, not the refusal",
			from: "held, then waiting", clock: next.Add(30 * time.Second), launch: busy, launches: 1,
			notStarted: "engine_busy", want: row{
				anchor: due, lastRunAt: due, status: launchrule.StatusSkipped, reason: "engine busy", waitingFor: next,
				failedSlots: 1,
			},
		},
		{
			name: "waiting, tick before its retry: still waiting", from: "waiting", clock: due.Add(time.Minute),
			want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "waiting, launch ok: launched", from: "waiting", clock: due.Add(2 * time.Hour),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: launchrule.StatusStarted, runID: "wf-1"},
		},
		{
			name: "waiting, engine busy: still waiting", from: "waiting", clock: due.Add(2 * time.Hour), launch: busy,
			launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "waiting, launch error: failed", from: "waiting", clock: due.Add(2 * time.Hour), launch: failure,
			launches: 1, notStarted: "launch_failed", want: row{
				anchor: due, lastRunAt: due, status: launchrule.StatusFailed, reason: failure.Error(), failedSlots: 1,
			},
		},
		{
			name: "waiting, sign-in refusal: held, no longer waiting", from: "waiting", clock: due.Add(2 * time.Hour),
			launch: refused, launches: 1, want: row{anchor: yesterday},
		},
		{
			name: "waiting, cancelled before a run existed: still waiting", from: "waiting", clock: due.Add(2 * time.Hour),
			launch: stopped, cancel: true, launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "waiting, overtaken: the next slot launched", from: "waiting", clock: next.Add(30 * time.Second),
			launches: 1, notStarted: "engine_busy", want: row{
				anchor: next, lastRunAt: next, status: launchrule.StatusStarted, runID: "wf-1", failedSlots: 1,
			},
		},
		{
			name: "waiting, overtaken, the next slot busy too: skipped, the next slot waiting", from: "waiting",
			clock: next.Add(30 * time.Second), launch: busy, launches: 1, notStarted: "engine_busy",
			want: row{
				anchor: due, lastRunAt: due, status: launchrule.StatusSkipped, reason: "engine busy", waitingFor: next,
				failedSlots: 1,
			},
		},
		{
			name: "waiting, overtaken past the next slot's grace: skipped, the next slot missed", from: "waiting",
			clock: next.Add(4 * time.Minute), notStarted: "engine_busy",
			want: row{
				anchor: next.Add(4 * time.Minute), lastRunAt: due, status: launchrule.StatusSkipped, reason: "engine busy",
				failedSlots: 1,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, l, r := newFixtureIn(t, dir, due.Add(30*time.Second))
			sched := st.Clocks()[0].Row()
			recordWF2 := func(at time.Time) {
				if err := st.RecordStart(t.Context(), launchrule.Run{StartedAt: at, ScheduleID: sched.ID, WorkflowID: "wf-2"}); err != nil {
					t.Fatalf("RecordStart: %v", err)
				}
			}
			switch tc.from {
			case "held":
				l.failWith(refused)
				r.sweep(t.Context())
			case "waiting":
				l.failWith(busy)
				r.sweep(t.Context())
			case "held, then waiting":
				l.failWith(refused)
				r.sweep(t.Context())
				r.now = func() time.Time { return due.Add(90 * time.Second) }
				l.failWith(busy)
				r.sweep(t.Context())
			case "started", "ended":
				recordWF2(due.Add(30 * time.Second))
				if tc.from == "ended" {
					if err := st.RecordEnding(t.Context(), launchrule.Run{ScheduleID: sched.ID, WorkflowID: "wf-2"},
						launchrule.Outcome{Status: launchrule.StatusCompleted}); err != nil {
						t.Fatalf("RecordEnding: %v", err)
					}
				}
				// The process that launched wf-2 is gone: a fresh runner keeps no memory of the slot.
				r = NewRunner(st, l)
			case "previous run":
				if err := st.RecordStart(t.Context(), launchrule.Run{StartedAt: yesterday, ScheduleID: sched.ID, WorkflowID: "wf-0"}); err != nil {
					t.Fatalf("RecordStart: %v", err)
				}
			}
			before := l.launched()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			l.during = func(string) {
				if tc.started {
					recordWF2(tc.clock)
				}
				if tc.cancel {
					cancel()
				}
			}
			launch := tc.launch
			if b, ok := launch.(*BusyError); ok {
				own := *b
				own.ScheduleID = sched.ID
				launch = &own
			}
			l.failWith(launch)
			l.failID = tc.failID
			r.now = func() time.Time { return tc.clock }
			counted := slotsNotStarted(t)
			r.sweep(ctx)

			for event, n := range slotsNotStarted(t) {
				want := 0.0
				if event == tc.notStarted {
					want = 1
				}
				if d := n - counted[event]; d != want {
					t.Errorf("%s slots not started moved by %v, want %v", event, d, want)
				}
			}

			if n := l.launched() - before; n != tc.launches {
				t.Errorf("launches = %d, want %d", n, tc.launches)
			}
			// The persisted row is the one a restart sees; the held one is what the next write persists.
			again, _, err := launchrule.NewStore(dir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			for where, e := range map[string]launchrule.Clock{"held": st.Clocks()[0], "persisted": again.Clocks()[0]} {
				got := row{
					anchor: e.Anchor, lastRunAt: e.LastRunAt, waitingFor: e.WaitingFor, status: e.LastStatus,
					reason: e.LastReason, runID: e.LastRunID, failedSlots: e.FailedSlots,
				}
				if !got.anchor.Equal(tc.want.anchor) || !got.lastRunAt.Equal(tc.want.lastRunAt) ||
					!got.waitingFor.Equal(tc.want.waitingFor) || got.status != tc.want.status ||
					got.reason != tc.want.reason || got.runID != tc.want.runID ||
					got.failedSlots != tc.want.failedSlots {
					t.Errorf("%s row = %+v\nwant %+v", where, got, tc.want)
				}
			}
		})
	}
}

// A shutdown during a launch is judged by what the launch created. A launch that created nothing leaves the row as it
// was, so the next start fires the slot inside its grace or resumes its wait. A launch that recorded its run on the
// row is that slot's launch, whether or not its id came back: the next start neither fires the slot again nor drops
// the run from the row.
func TestSweep_AShutdownDuringALaunchRecordsWhatTheLaunchCreated(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	stopped := fmt.Errorf("workflow invoke: %w", context.Canceled)
	for _, tc := range []struct {
		restart time.Time
		name    string
		wantRun string
		failID  string
		waiting bool
		started bool
		refires bool
	}{
		{name: "a first launch that created nothing", restart: due.Add(2 * time.Minute), wantRun: "wf-1", refires: true},
		{
			name: "a retry of a wait that created nothing", restart: due.Add(2 * time.Hour), waiting: true,
			wantRun: "wf-1", refires: true,
		},
		{
			name: "a launch that recorded its run and returned its id", restart: due.Add(2 * time.Minute),
			started: true, failID: "wf-2", wantRun: "wf-2",
		},
		{
			name: "a launch that recorded its run and never returned its id", restart: due.Add(2 * time.Minute),
			started: true, wantRun: "wf-2",
		},
		{
			name: "a launch that recorded its run, restarted past the grace", restart: due.Add(5 * time.Hour),
			started: true, wantRun: "wf-2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, l, r := newFixtureIn(t, dir, due.Add(30*time.Second))
			if tc.waiting {
				l.failWith(busy)
				r.sweep(t.Context())
				r.now = func() time.Time { return due.Add(time.Hour) }
			}
			before := st.Clocks()[0]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			l.during = func(string) {
				if tc.started {
					run := launchrule.Run{StartedAt: r.now(), ScheduleID: before.ID, WorkflowID: "wf-2"}
					if err := st.RecordStart(t.Context(), run); err != nil {
						t.Errorf("RecordStart: %v", err)
					}
				}
				cancel()
			}
			l.failWith(stopped)
			l.failID = tc.failID
			r.sweep(ctx)
			if got := st.Clocks()[0]; !tc.started && (!got.Anchor.Equal(before.Anchor) || !got.LastRunAt.Equal(before.LastRunAt) ||
				!got.WaitingFor.Equal(before.WaitingFor) || got.LastStatus != before.LastStatus || got.LastRunID != before.LastRunID) {
				t.Errorf("row after the shutdown = %+v, want it as it was: %+v", got, before)
			}

			l.during = nil
			l.failWith(nil)
			launched := l.launched()
			reborn := NewRunner(st, l)
			reborn.now = func() time.Time { return tc.restart }
			reborn.sweep(t.Context())
			if refired := l.launched() > launched; refired != tc.refires {
				t.Errorf("the next start fired the slot = %v, want %v", refired, tc.refires)
			}
			again, _, err := launchrule.NewStore(dir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			got := again.Clocks()[0]
			if got.LastStatus != launchrule.StatusStarted || got.LastRunID != tc.wantRun || !got.Anchor.Equal(due) ||
				!got.WaitingFor.IsZero() {
				t.Errorf("row = %+v, want the 02:00 slot started as %s", got, tc.wantRun)
			}
		})
	}
}
