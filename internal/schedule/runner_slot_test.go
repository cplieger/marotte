package schedule

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
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
	stopped := fmt.Errorf("workflow list call: %w", context.Canceled)
	ownRun := &BusyError{WorkflowID: "wf-0", ScheduleID: idOf("bundled://demo"), SlotAt: due}

	type row struct {
		anchor, lastRunAt, waitingFor time.Time
		status                        status
		reason                        string
	}
	cases := []struct {
		clock    time.Time
		launch   error
		want     row
		name     string
		from     string // "due", "held" (the sign-in gate refused it at 02:00:30) or "waiting" (busy at 02:00:30)
		launches int
		cancel   bool // the runner's context ends during the launch
	}{
		{
			name: "due, launch ok: launched", from: "due", clock: due.Add(30 * time.Second),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: statusStarted},
		},
		{
			name: "due, engine busy: waiting", from: "due", clock: due.Add(30 * time.Second), launch: busy,
			launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "due, launch error: failed", from: "due", clock: due.Add(30 * time.Second), launch: failure,
			launches: 1, want: row{anchor: due, lastRunAt: due, status: StatusFailed, reason: failure.Error()},
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
			name: "due, cancelled: left as it was", from: "due", clock: due.Add(30 * time.Second), launch: stopped,
			cancel: true, launches: 1, want: row{anchor: yesterday},
		},
		{
			name: "due, cancelled once the run started: launched", from: "due", clock: due.Add(30 * time.Second),
			cancel: true, launches: 1, want: row{anchor: due, lastRunAt: due, status: statusStarted},
		},
		{
			name: "due, grace expired: missed while offline", from: "due", clock: due.Add(4 * time.Minute),
			want: row{anchor: due.Add(4 * time.Minute)},
		},
		{
			name: "held, launch ok: launched", from: "held", clock: due.Add(2 * time.Minute),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: statusStarted},
		},
		{
			name: "held, grace expired: failed with the refusal", from: "held", clock: due.Add(4 * time.Minute),
			want: row{anchor: due.Add(4 * time.Minute), lastRunAt: due, status: StatusFailed, reason: refused.Error()},
		},
		{
			name: "waiting, tick before its retry: still waiting", from: "waiting", clock: due.Add(time.Minute),
			want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "waiting, launch ok: launched", from: "waiting", clock: due.Add(2 * time.Hour),
			launches: 1, want: row{anchor: due, lastRunAt: due, status: statusStarted},
		},
		{
			name: "waiting, engine busy: still waiting", from: "waiting", clock: due.Add(2 * time.Hour), launch: busy,
			launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "waiting, launch error: failed", from: "waiting", clock: due.Add(2 * time.Hour), launch: failure,
			launches: 1, want: row{anchor: due, lastRunAt: due, status: StatusFailed, reason: failure.Error()},
		},
		{
			name: "waiting, sign-in refusal: held, no longer waiting", from: "waiting", clock: due.Add(2 * time.Hour),
			launch: refused, launches: 1, want: row{anchor: yesterday},
		},
		{
			name: "waiting, cancelled: still waiting", from: "waiting", clock: due.Add(2 * time.Hour), launch: stopped,
			cancel: true, launches: 1, want: row{anchor: yesterday, waitingFor: due},
		},
		{
			name: "waiting, overtaken: the next slot launched", from: "waiting", clock: next.Add(30 * time.Second),
			launches: 1, want: row{anchor: next, lastRunAt: next, status: statusStarted},
		},
		{
			name: "waiting, overtaken, the next slot busy too: skipped, the next slot waiting", from: "waiting",
			clock: next.Add(30 * time.Second), launch: busy, launches: 1,
			want: row{anchor: due, lastRunAt: due, status: StatusSkipped, reason: "engine busy", waitingFor: next},
		},
		{
			name: "waiting, overtaken past the next slot's grace: skipped, the next slot missed", from: "waiting",
			clock: next.Add(4 * time.Minute),
			want:  row{anchor: next.Add(4 * time.Minute), lastRunAt: due, status: StatusSkipped, reason: "engine busy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, l, r := newFixture(t, due.Add(30*time.Second))
			switch tc.from {
			case "held":
				l.failWith(refused)
				r.sweep(t.Context())
			case "waiting":
				l.failWith(busy)
				r.sweep(t.Context())
			}
			before := l.launched()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				l.during = func(string) { cancel() }
			}
			l.failWith(tc.launch)
			r.now = func() time.Time { return tc.clock }
			r.sweep(ctx)

			if n := l.launched() - before; n != tc.launches {
				t.Errorf("launches = %d, want %d", n, tc.launches)
			}
			// The persisted row is the one a restart sees; the held one is what the next write persists.
			again, err := NewStore(filepath.Dir(st.path))
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			for where, e := range map[string]Entry{"held": st.List()[0], "persisted": again.List()[0]} {
				got := row{
					anchor: e.Anchor, lastRunAt: e.LastRunAt, waitingFor: e.WaitingFor, status: e.LastStatus, reason: e.LastReason,
				}
				if !got.anchor.Equal(tc.want.anchor) || !got.lastRunAt.Equal(tc.want.lastRunAt) ||
					!got.waitingFor.Equal(tc.want.waitingFor) || got.status != tc.want.status || got.reason != tc.want.reason {
					t.Errorf("%s row = %+v\nwant %+v", where, got, tc.want)
				}
			}
		})
	}
}

// A shutdown during a launch started nothing, so the next start fires the slot inside its grace, or resumes its wait.
func TestSweep_AShutdownDuringALaunchLeavesTheSlotForTheNextStart(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	for _, tc := range []struct {
		restart time.Time
		name    string
		waiting bool
	}{
		{name: "a first launch", restart: due.Add(2 * time.Minute)},
		{name: "a retry of a wait", restart: due.Add(2 * time.Hour), waiting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, l, r := newFixture(t, due.Add(30*time.Second))
			if tc.waiting {
				l.failWith(busy)
				r.sweep(t.Context())
				r.now = func() time.Time { return due.Add(time.Hour) }
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			l.during = func(string) { cancel() }
			l.failWith(fmt.Errorf("workflow list call: %w", context.Canceled))
			r.sweep(ctx)

			l.during = nil
			l.failWith(nil)
			reborn := NewRunner(st, l)
			reborn.now = func() time.Time { return tc.restart }
			reborn.sweep(t.Context())
			if got := st.List()[0]; got.LastStatus != statusStarted || !got.Anchor.Equal(due) || !got.WaitingFor.IsZero() {
				t.Errorf("row = %+v, want the 02:00 slot started by the next start", got)
			}
		})
	}
}
