package schedule

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

// A slot whose launch the engine did not answer keeps retrying, with backoff, until the schedule's next slot; the row
// says it waits meanwhile, and a slot the next one overtakes is skipped for the busy engine, never failed.
func TestSweep_WaitsOnABusyEngineUntilTheNextSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	next := due.AddDate(0, 0, 1)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))

	t.Run("it fires once the engine answers, past the grace", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		l.failWith(nil)
		r.now = func() time.Time { return due.Add(2 * time.Hour) }
		r.sweep(t.Context())
		got := st.Clocks()[0]
		if n := l.launched(); n != 2 {
			t.Errorf("launches = %d, want the waiting slot tried again", n)
		}
		if got.LastStatus != launchrule.StatusStarted || !got.Anchor.Equal(due) || !got.WaitingFor.IsZero() {
			t.Errorf("row = %+v, want the slot started at its due time and no wait left", got)
		}
		if _, _, slots := l.snap(); len(slots) != 2 || !slots[1].Equal(next) {
			t.Errorf("bounds = %v, want the late fire bounded by the next slot %v", slots, next)
		}
	})

	t.Run("retries back off", func(t *testing.T) {
		_, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		clock := due.Add(30 * time.Second)
		r.now = func() time.Time { return clock }
		var tried []time.Duration
		for range 16 {
			before := l.launched()
			r.sweep(t.Context())
			if l.launched() > before {
				tried = append(tried, clock.Sub(due.Add(30*time.Second)))
			}
			clock = clock.Add(r.tick)
		}
		want := []time.Duration{0, time.Minute, 3 * time.Minute, 7 * time.Minute, 15 * time.Minute}
		if fmt.Sprint(tried) != fmt.Sprint(want) {
			t.Errorf("tries at %v, want %v: a doubling backoff from one tick", tried, want)
		}
	})

	t.Run("the backoff is capped", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		for i := range 10 {
			r.now = func() time.Time { return due.Add(time.Duration(i) * time.Hour) }
			r.sweep(t.Context())
		}
		if b := r.retry[st.Clocks()[0].Row()]; b.step != busyRetryMax {
			t.Errorf("step = %v, want the cap %v", b.step, busyRetryMax)
		}
	})

	t.Run("the next slot skips it for the busy engine and fires itself", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		logs := captureLogs(t)
		l.failWith(nil)
		r.now = func() time.Time { return next.Add(30 * time.Second) }
		r.sweep(t.Context())
		if s := logs.String(); !strings.Contains(s, "the agent engine did not answer before the next slot") {
			t.Errorf("logs = %q, want the skip named for the engine", s)
		}
		sources, _, slots := l.snap()
		if len(sources) != 2 || !slots[1].Equal(next.AddDate(0, 0, 1)) {
			t.Errorf("bounds = %v, want the next slot launched in the same sweep, bounded by the one after it", slots)
		}
		if got := st.Clocks()[0]; got.LastStatus != launchrule.StatusStarted || !got.Anchor.Equal(next) || !got.WaitingFor.IsZero() {
			t.Errorf("row = %+v, want the next slot started on time", got)
		}
	})

	// The wait is on the row, so a restart retries the slot instead of skipping it as missed while offline.
	t.Run("a restart resumes the wait", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		l.failWith(nil)
		reborn := NewRunner(st, l)
		reborn.now = func() time.Time { return due.Add(3 * time.Hour) }
		reborn.sweep(t.Context())
		if got := st.Clocks()[0]; got.LastStatus != launchrule.StatusStarted || !got.Anchor.Equal(due) {
			t.Errorf("row = %+v, want the waiting slot fired after the restart", got)
		}
	})

	t.Run("disabling the schedule drops its wait", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		e := st.Clocks()[0].Rule
		e.Enabled = false
		if _, err := st.Replace(t.Context(), e.ID, &e, 0); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		r.sweep(t.Context())
		if got := st.Clocks()[0]; !got.WaitingFor.IsZero() {
			t.Errorf("waiting for %v, want none on a disabled schedule", got.WaitingFor)
		}
		if _, ok := r.retry[e.Row()]; ok {
			t.Error("a disabled schedule kept its backoff")
		}
	})
}

// A launch the engine leaves unanswered holds the sweep for its own timeout, so each slot is judged by the clock when
// the sweep reaches it, not when the sweep began.
func TestSweep_JudgesEachSlotByTheClockWhenItIsReached(t *testing.T) {
	const launchTakes = 2 * time.Minute // the launch's own timeout, as a slow engine spends it
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))
	daily := func(h, m int) launchrule.Spec { return launchrule.Spec{Freq: launchrule.FreqDaily, Hour: h, Minute: m} }
	yesterday := at(2026, time.August, 3, 12, 0)
	due := at(2026, time.August, 4, 2, 0)

	// sweepFixture puts the entries, marks those named in waiting as waiting on their 02:00 slot, and makes a launch
	// of a source named in slow advance the clock and come back busy.
	sweepFixture := func(t *testing.T, start time.Time, entries []clockSeed, waiting, slow []string) (*launchrule.Store, *fakeLauncher, *Runner) {
		t.Helper()
		st := mustStore(t)
		for i := range entries {
			r := putClock(t, st, entries[i])
			if slices.Contains(waiting, entries[i].Source) {
				waitOn(t, st, r.Row(), due)
			}
		}
		clock := start
		l := &fakeLauncher{}
		r := NewRunner(st, l)
		r.now = func() time.Time { return clock }
		l.during = func(src string) {
			if slices.Contains(slow, src) {
				clock = clock.Add(launchTakes)
				l.failWith(busy)
			} else {
				l.failWith(nil)
			}
		}
		return st, l, r
	}
	launchesOf := func(l *fakeLauncher, src string) int {
		sources, _, _ := l.snap()
		n := 0
		for _, s := range sources {
			if s == src {
				n++
			}
		}
		return n
	}
	rowOf := func(st *launchrule.Store, src string) launchrule.Clock {
		for _, e := range st.Clocks() {
			if e.Source == src {
				return e
			}
		}
		return launchrule.Clock{}
	}

	t.Run("the backoff counts from the launch's return", func(t *testing.T) {
		_, l, r := sweepFixture(t, due.Add(30*time.Second),
			[]clockSeed{{Source: "bundled://a", Spec: daily(2, 0), Anchor: yesterday}},
			nil, []string{"bundled://a"})
		r.sweep(t.Context())
		r.sweep(t.Context()) // the ticker's buffered tick, at once
		if n := l.launched(); n != 1 {
			t.Errorf("launches = %d, want 1: the retry came with no gap after a launch that took %v", n, launchTakes)
		}
		returned := due.Add(30*time.Second + launchTakes)
		r.now = func() time.Time { return returned.Add(r.tick) }
		r.sweep(t.Context())
		if n := l.launched(); n != 2 {
			t.Errorf("launches = %d, want the retry one tick after the launch returned", n)
		}
	})

	t.Run("a next slot passed during an earlier launch skips the wait", func(t *testing.T) {
		hourly := launchrule.Spec{Freq: launchrule.FreqHourly, Interval: 1, Minute: 0}
		st, l, r := sweepFixture(t, at(2026, time.August, 4, 2, 59), []clockSeed{
			{Source: "bundled://a", Spec: daily(2, 0), Anchor: yesterday},
			{Source: "bundled://b", Spec: hourly, Anchor: at(2026, time.August, 4, 1, 30)},
		}, []string{"bundled://a", "bundled://b"}, []string{"bundled://a", "bundled://b"})
		r.sweep(t.Context())
		got := rowOf(st, "bundled://b")
		if got.LastStatus != launchrule.StatusSkipped || got.LastReason != "engine busy" || !got.LastRunAt.Equal(due) {
			t.Errorf("b = (%q, %q, %v), want its 02:00 slot skipped for the busy engine",
				got.LastStatus, got.LastReason, got.LastRunAt)
		}
		if n := launchesOf(l, "bundled://b"); n != 1 || !got.WaitingFor.Equal(at(2026, time.August, 4, 3, 0)) {
			t.Errorf("b launched %d times, waiting for %v; want only its 03:00 slot tried, and waiting", n, got.WaitingFor)
		}
	})

	// A wait its next slot overtakes late in a slow sweep hands that slot to the same sweep, so it cannot age past
	// its grace behind the other schedules' launches.
	t.Run("a next slot reached late in a slow sweep still fires", func(t *testing.T) {
		hourly := launchrule.Spec{Freq: launchrule.FreqHourly, Interval: 1, Minute: 0}
		st, l, r := sweepFixture(t, at(2026, time.August, 4, 3, 0).Add(30*time.Second), []clockSeed{
			{Source: "bundled://a", Spec: daily(2, 0), Anchor: yesterday},
			{Source: "bundled://b", Spec: daily(2, 0), Anchor: yesterday},
			{Source: "bundled://c", Spec: hourly, Anchor: at(2026, time.August, 4, 1, 30)},
		}, []string{"bundled://a", "bundled://b", "bundled://c"}, []string{"bundled://a", "bundled://b"})
		r.sweep(t.Context()) // reaches c at 03:04:30, behind two slow launches
		r.sweep(t.Context())
		got := rowOf(st, "bundled://c")
		if n := launchesOf(l, "bundled://c"); n != 1 || got.LastStatus != launchrule.StatusStarted ||
			!got.Anchor.Equal(at(2026, time.August, 4, 3, 0)) {
			t.Errorf("c = %+v after %d launches, want its 03:00 slot started once, not missed while offline", got, n)
		}
	})

	t.Run("a slot due while this sweep launches still fires", func(t *testing.T) {
		st, l, r := sweepFixture(t, due.Add(30*time.Second), []clockSeed{
			{Source: "bundled://a", Spec: daily(2, 0), Anchor: yesterday},
			{Source: "bundled://b", Spec: daily(2, 0), Anchor: yesterday},
			{Source: "bundled://c", Spec: daily(2, 1), Anchor: yesterday},
		}, []string{"bundled://a", "bundled://b"}, []string{"bundled://a", "bundled://b"})
		r.sweep(t.Context())
		r.sweep(t.Context())
		if got := rowOf(st, "bundled://c"); launchesOf(l, "bundled://c") != 1 || got.LastStatus != launchrule.StatusStarted {
			t.Errorf("c = %+v after %d launches, want its 02:01 slot started, not skipped as missed while offline",
				got, launchesOf(l, "bundled://c"))
		}
	})

	t.Run("a slot due after this sweep passed it fires on the next", func(t *testing.T) {
		st, l, r := sweepFixture(t, due.Add(30*time.Second), []clockSeed{
			{Source: "bundled://a", Spec: daily(2, 0), Anchor: yesterday},
			{Source: "bundled://b", Spec: daily(2, 3), Anchor: yesterday},
			{Source: "bundled://c", Spec: daily(2, 0), Anchor: yesterday},
		}, []string{"bundled://a", "bundled://c"}, []string{"bundled://a", "bundled://c"})
		r.sweep(t.Context()) // reaches b at 02:02:30, before its slot, and ends at 02:04:30
		r.sweep(t.Context()) // reaches b at 02:06:30, behind a's retry
		if got := rowOf(st, "bundled://b"); launchesOf(l, "bundled://b") != 1 || got.LastStatus != launchrule.StatusStarted {
			t.Errorf("b = %+v, want its 02:03 slot started: it fell due while the runner was still launching", got)
		}
	})

	t.Run("a slot due while nothing swept is still missed", func(t *testing.T) {
		st, l, r := sweepFixture(t, at(2026, time.August, 4, 1, 0),
			[]clockSeed{{Source: "bundled://a", Spec: daily(2, 0), Anchor: yesterday}}, nil, nil)
		r.sweep(t.Context())
		r.now = func() time.Time { return at(2026, time.August, 4, 5, 0) }
		r.sweep(t.Context())
		if got := rowOf(st, "bundled://a"); l.launched() != 0 || got.LastStatus != "" {
			t.Errorf("a = %+v after %d launches, want its 02:00 slot skipped as missed while paused", got, l.launched())
		}
	})
}

// The wait is persisted, so a reopened store still knows the slot it waits on.
func TestWaitSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := putClock(t, st, clockSeed{
		Source: "bundled://demo", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2},
		Anchor: at(2026, time.August, 3, 12, 0),
	})
	due := at(2026, time.August, 4, 2, 0)
	waitOn(t, st, e.Row(), due)
	again, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := again.Clocks()[0].WaitingFor; !got.Equal(due) {
		t.Errorf("waiting for %v after a reopen, want %v", got, due)
	}
}

// A sweep launches a schedule's slot, a plainly due one or the one an overtaken wait hands on, only while the schedule
// still exists and is on: an earlier launch can hold the sweep long enough for the user to delete or disable it.
func TestSweep_AScheduleChangedMidSweepDoesNotLaunch(t *testing.T) {
	yesterday := at(2026, time.August, 3, 12, 0)
	due := at(2026, time.August, 4, 2, 0)
	states := []struct {
		anchor  time.Time
		arrange func(t *testing.T, st *launchrule.Store, b *launchrule.Rule)
		name    string
	}{
		{name: "due", anchor: due, arrange: func(*testing.T, *launchrule.Store, *launchrule.Rule) {}},
		{name: "waiting", anchor: yesterday, arrange: func(t *testing.T, st *launchrule.Store, b *launchrule.Rule) {
			waitOn(t, st, b.Row(), due)
		}},
	}
	changes := []struct {
		change func(t *testing.T, st *launchrule.Store, b *launchrule.Rule)
		name   string
	}{
		{name: "deleted", change: func(t *testing.T, st *launchrule.Store, b *launchrule.Rule) {
			if err := st.Delete(t.Context(), b.ID); err != nil {
				t.Errorf("Delete: %v", err)
			}
		}},
		{name: "disabled", change: func(t *testing.T, st *launchrule.Store, b *launchrule.Rule) {
			off := *b
			off.Enabled = false
			if _, err := st.Replace(t.Context(), b.ID, &off, 0); err != nil {
				t.Errorf("Replace: %v", err)
			}
		}},
	}
	for _, state := range states {
		for _, tc := range changes {
			t.Run(state.name+" "+tc.name, func(t *testing.T) {
				st := mustStore(t)
				a := putClock(t, st, clockSeed{Source: "bundled://a", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, Anchor: due})
				b := putClock(t, st, clockSeed{Source: "bundled://b", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}, Anchor: state.anchor})
				state.arrange(t, st, &b)
				l := &fakeLauncher{}
				l.during = func(src string) {
					if src == a.Source {
						tc.change(t, st, &b)
					}
				}
				r := NewRunner(st, l)
				r.now = func() time.Time { return due.AddDate(0, 0, 1).Add(30 * time.Second) }
				r.sweep(t.Context())
				if sources, _, _ := l.snap(); slices.Contains(sources, b.Source) {
					t.Errorf("launched %v, want no launch of b once it was %s", sources, tc.name)
				}
			})
		}
	}
}
