package runlease

import (
	"testing"
	"time"
)

// TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor pins the composition of
// one clock over three inputs.
func TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	const idle = 15 * time.Minute
	const floor = 5 * time.Minute

	for name, tc := range map[string]struct {
		in   Bounds
		want time.Time
	}{
		"no slot and no backstop, so the idle window is the whole bound": {
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
		"a backstop inside the idle window wins": {
			in:   Bounds{BackstopAt: now.Add(7 * time.Minute), Idle: idle, Floor: floor},
			want: now.Add(7 * time.Minute),
		},
		"a backstop beyond the window loses, so an ordinary refill is unaffected": {
			in:   Bounds{BackstopAt: now.Add(100 * idle), Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		// In the PAST: a spent backstop is honoured, not floored, or progress rolls it forward forever.
		"a spent backstop is honoured, not floored": {
			in:   Bounds{BackstopAt: now.Add(-time.Hour), Idle: idle, Floor: floor},
			want: now.Add(-time.Hour),
		},
		"a spent backstop outranks a slot inside the floor too": {
			in: Bounds{
				SlotAt: now.Add(90 * time.Second), BackstopAt: now.Add(-time.Hour),
				Idle: idle, Floor: floor,
			},
			want: now.Add(-time.Hour),
		},
		"a zero backstop does not bound the run": {
			in:   Bounds{Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"slot and backstop both set: the tighter one wins": {
			in: Bounds{
				SlotAt: now.Add(12 * time.Minute), BackstopAt: now.Add(9 * time.Minute),
				Idle: idle, Floor: floor,
			},
			want: now.Add(9 * time.Minute),
		},
		"slot and backstop both set: the slot can be the tighter one": {
			in: Bounds{
				SlotAt: now.Add(9 * time.Minute), BackstopAt: now.Add(12 * time.Minute),
				Idle: idle, Floor: floor,
			},
			want: now.Add(9 * time.Minute),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := NextDeadline(now, tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("NextDeadline = %v, want %v (in %v / %v from now)",
					got, tc.want, got.Sub(now), tc.want.Sub(now))
			}
			spent := !tc.in.BackstopAt.IsZero() && tc.in.BackstopAt.Before(now.Add(floor))
			if !spent && got.Before(now.Add(floor)) {
				t.Errorf("NextDeadline = %v, below the floor %v", got.Sub(now), floor)
			}
			if spent && !got.Equal(tc.in.BackstopAt) {
				t.Errorf("NextDeadline = %v with a spent backstop at %v; the backstop instant is "+
					"the answer, or a floor rolls the absolute bound forward on every stamp",
					got.Sub(now), tc.in.BackstopAt.Sub(now))
			}
		})
	}
}

// TestNextDeadline_FloorOutranksTheSlotButNotTheBackstop pins the one disagreement: the
// floor outranks the slot, while any backstop tighter than the floor wins.
func TestNextDeadline_FloorOutranksTheSlotButNotTheBackstop(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const floor = 5 * time.Minute

	slot := NextDeadline(now, Bounds{SlotAt: now.Add(time.Second), Idle: 15 * time.Minute, Floor: floor})
	if want := now.Add(floor); !slot.Equal(want) {
		t.Errorf("with a one-second slot NextDeadline = %v after now, want the floor %v",
			slot.Sub(now), want.Sub(now))
	}

	backstop := now.Add(-time.Hour)
	spent := NextDeadline(now, Bounds{BackstopAt: backstop, Idle: 15 * time.Minute, Floor: floor})
	if !spent.Equal(backstop) {
		t.Errorf("with a spent backstop NextDeadline = %v after now, want the backstop instant "+
			"%v; a floor here is a fresh budget on every stamp, so the absolute bound becomes a "+
			"rolling window a productive-looking runaway never reaches",
			spent.Sub(now), backstop.Sub(now))
	}

	// A refill a moment later computes the same instant: the backstop anchor is fixed.
	later := NextDeadline(now.Add(2*time.Minute), Bounds{BackstopAt: backstop, Idle: 15 * time.Minute, Floor: floor})
	if !later.Equal(spent) {
		t.Errorf("a stamp two minutes later moved the spent backstop from %v to %v", spent, later)
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

// expired reports whether the lease's deadline has passed at now; a parked lease never expires.
func (l *Lease) expired(now time.Time) bool {
	return l.Bounded() && !now.Before(l.Deadline)
}
