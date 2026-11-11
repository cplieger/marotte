package runlease

import (
	"strings"
	"testing"
	"time"
)

// TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor pins the composition of
// one clock over the idle window, the slot and the floor.
func TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	const idle = 15 * time.Minute
	const floor = 5 * time.Minute

	for name, tc := range map[string]struct {
		in   Bounds
		want time.Time
	}{
		"no slot, so the idle window is the whole bound": {
			in:   Bounds{Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"a slot inside the idle window wins": {
			in:   Bounds{SlotAt: now.Add(10 * time.Minute), Idle: idle, Floor: floor},
			want: now.Add(10 * time.Minute),
		},
		"a slot beyond the window loses, so a daily schedule is still bounded": {
			in:   Bounds{SlotAt: now.Add(24 * time.Hour), Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"a slot exactly at the window's end changes nothing": {
			in:   Bounds{SlotAt: now.Add(idle), Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"a slot already passed is floored, not honoured": {
			in:   Bounds{SlotAt: now.Add(-time.Minute), Idle: idle, Floor: floor},
			want: now.Add(floor),
		},
		"a slot inside the floor is floored: a bound too small to finish in is no bound": {
			in:   Bounds{SlotAt: now.Add(90 * time.Second), Idle: idle, Floor: floor},
			want: now.Add(floor),
		},
		"a slot exactly at the floor is honoured": {
			in:   Bounds{SlotAt: now.Add(floor), Idle: idle, Floor: floor},
			want: now.Add(floor),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := NextDeadline(now, tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("NextDeadline = %v, want %v (in %v / %v from now)",
					got, tc.want, got.Sub(now), tc.want.Sub(now))
			}
			if got.Before(now.Add(floor)) {
				t.Errorf("NextDeadline = %v, below the floor %v", got.Sub(now), floor)
			}
		})
	}
}

// TestLeaseBounded_IsTheSuccessorOfTheArmMap pins the zero-deadline meaning three readers
// depend on: the timer's liveness test, the step cap, and the re-arm.
func TestLeaseBounded_IsTheSuccessorOfTheArmMap(t *testing.T) {
	t.Parallel()
	now := time.Now()

	parked := Lease{WorkflowID: "wf_1", Origin: OriginManual}
	if parked.Bounded() {
		t.Error("a lease with no deadline reported bounded; a parked run must not be cancellable")
	}
	if parked.expired(now.Add(time.Hour)) {
		t.Error("a parked lease reported expired; a run parked for a week must not be cancelled for it")
	}

	live := Lease{WorkflowID: "wf_1", Origin: OriginManual, Deadline: now.Add(time.Minute)}
	if !live.Bounded() {
		t.Error("a lease with a deadline reported unbounded")
	}
	if live.expired(now) {
		t.Error("a lease expired before its deadline")
	}
	if !live.expired(now.Add(time.Minute)) {
		t.Error("a lease was not expired AT its deadline; the timer fires then")
	}
}

// TestOriginValid pins that an unknown origin is refused on write and dropped on load.
func TestOriginValid(t *testing.T) {
	t.Parallel()
	for _, o := range []Origin{OriginScheduled, OriginManual, OriginAgent} {
		if !o.valid() {
			t.Errorf("%q rejected", o)
		}
	}
	for _, o := range []Origin{"", "tui", "Scheduled", "manual "} {
		if o.valid() {
			t.Errorf("%q accepted", o)
		}
	}
}

// TestOriginTriggeredValid pins the event-trigger origin as one the store writes and loads, and check's
// refusal as naming it among the known origins.
func TestOriginTriggeredValid(t *testing.T) {
	t.Parallel()
	if !OriginTriggered.valid() {
		t.Errorf("%q rejected", OriginTriggered)
	}
	err := (&Lease{WorkflowID: "wf_1", Origin: "tui"}).check()
	if err == nil || !strings.Contains(err.Error(), "scheduled/manual/agent/triggered") {
		t.Errorf("check() = %v, want the refusal to list triggered among the origins", err)
	}
}

// expired reports whether the lease's deadline has passed at now; a parked lease never expires.
func (l *Lease) expired(now time.Time) bool {
	return l.Bounded() && !now.Before(l.Deadline)
}
