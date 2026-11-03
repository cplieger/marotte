package schedule

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A slot whose launch the engine did not answer keeps retrying, with backoff, until the schedule's next slot; the row
// says it waits meanwhile, and a slot the next one overtakes is skipped for the busy engine, never failed.
func TestSweep_WaitsOnABusyEngineUntilTheNextSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	next := due.AddDate(0, 0, 1)
	anchor := at(2026, time.August, 3, 12, 0)
	busy := fmt.Errorf("%w: %w", ErrEngineBusy, errors.New("workflow list call: context deadline exceeded"))

	t.Run("the row waits and keeps its last outcome", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		got := st.List()[0]
		if !got.WaitingFor.Equal(due) {
			t.Errorf("waiting for %v, want the slot %v", got.WaitingFor, due)
		}
		if !got.Anchor.Equal(anchor) || got.LastStatus != "" || !got.LastRunAt.IsZero() {
			t.Errorf("row = %+v, want the anchor and the last outcome untouched while it waits", got)
		}
	})

	t.Run("it fires once the engine answers, past the grace", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		l.failWith(nil)
		r.now = func() time.Time { return due.Add(2 * time.Hour) }
		r.sweep(t.Context())
		got := st.List()[0]
		if n := l.launched(); n != 2 {
			t.Errorf("launches = %d, want the waiting slot tried again", n)
		}
		if got.LastStatus != statusStarted || !got.Anchor.Equal(due) || !got.WaitingFor.IsZero() {
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
		_, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		for i := range 10 {
			r.now = func() time.Time { return due.Add(time.Duration(i) * time.Hour) }
			r.sweep(t.Context())
		}
		if b := r.retry[idOf("bundled://demo")]; b.step != busyRetryMax {
			t.Errorf("step = %v, want the cap %v", b.step, busyRetryMax)
		}
	})

	t.Run("the next slot skips it for the busy engine", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		logs := captureLogs(t)
		r.now = func() time.Time { return next.Add(30 * time.Second) }
		r.sweep(t.Context())
		got := st.List()[0]
		if got.LastStatus != StatusSkipped || got.LastReason != "engine busy" || !got.LastRunAt.Equal(due) {
			t.Errorf("row = (%q, %q, %v), want the slot %v skipped for the busy engine",
				got.LastStatus, got.LastReason, got.LastRunAt, due)
		}
		if !got.WaitingFor.IsZero() || !got.Anchor.Equal(due) {
			t.Errorf("waiting %v, anchor %v, want no wait and the anchor at the skipped slot", got.WaitingFor, got.Anchor)
		}
		if n := l.launched(); n != 1 {
			t.Errorf("launches = %d, want none once the next slot came", n)
		}
		if s := logs.String(); !strings.Contains(s, "the agent engine did not answer before the next slot") {
			t.Errorf("logs = %q, want the skip named for the engine", s)
		}
		l.failWith(nil)
		r.sweep(t.Context())
		if got := st.List()[0]; got.LastStatus != statusStarted || !got.Anchor.Equal(next) {
			t.Errorf("row = %+v, want the next slot fired on its own", got)
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
		if got := st.List()[0]; got.LastStatus != statusStarted || !got.Anchor.Equal(due) {
			t.Errorf("row = %+v, want the waiting slot fired after the restart", got)
		}
	})

	// The engine answered; the sign-in gate's refusal is its own reason, bounded by its own grace.
	t.Run("a sign-in refusal ends the wait", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		refused := fmt.Errorf("%w: %w", ErrNoRunCanStart, errors.New("sign in to Kiro first"))
		l.failWith(refused)
		r.now = func() time.Time { return due.Add(time.Hour) }
		r.sweep(t.Context())
		if got := st.List()[0]; !got.WaitingFor.IsZero() {
			t.Errorf("waiting for %v, want the wait cleared once the engine answered", got.WaitingFor)
		}
		r.now = func() time.Time { return due.Add(time.Hour + time.Minute) }
		r.sweep(t.Context())
		if got := st.List()[0]; got.LastStatus != StatusFailed || got.LastReason != refused.Error() {
			t.Errorf("row = (%q, %q), want the gate's refusal recorded failed", got.LastStatus, got.LastReason)
		}
	})

	t.Run("any other launch error is recorded failed", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		l.failWith(errors.New("workflow new: no such recipe"))
		r.now = func() time.Time { return due.Add(time.Hour) }
		r.sweep(t.Context())
		if got := st.List()[0]; got.LastStatus != StatusFailed || !got.WaitingFor.IsZero() || !got.Anchor.Equal(due) {
			t.Errorf("row = %+v, want the failure recorded and no wait left", got)
		}
	})

	t.Run("disabling the schedule drops its wait", func(t *testing.T) {
		st, l, r := newFixture(t, due.Add(30*time.Second))
		l.failWith(busy)
		r.sweep(t.Context())
		e := st.List()[0]
		e.Enabled = false
		if err := st.Put(t.Context(), &e); err != nil {
			t.Fatalf("Put: %v", err)
		}
		r.sweep(t.Context())
		if got := st.List()[0]; !got.WaitingFor.IsZero() {
			t.Errorf("waiting for %v, want none on a disabled schedule", got.WaitingFor)
		}
		if _, ok := r.retry[e.ID()]; ok {
			t.Error("a disabled schedule kept its backoff")
		}
	})
}

// An edit keeps the wait while it still names the row's due slot, and drops it once the edit moves the slot.
func TestStore_PutKeepsAWaitOnlyForTheSameSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	for name, spec := range map[string]Spec{
		"same slot":  {Freq: FreqDaily, Hour: 2, Minute: 0},
		"moved slot": {Freq: FreqDaily, Hour: 3, Minute: 0},
	} {
		t.Run(name, func(t *testing.T) {
			st, _, _ := newFixture(t, due)
			id := st.List()[0].ID()
			if err := st.setWaiting(t.Context(), id, due); err != nil {
				t.Fatalf("setWaiting: %v", err)
			}
			e := Entry{Source: "bundled://demo", Spec: spec, Enabled: true}
			if err := st.Put(t.Context(), &e); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got := st.List()[0].WaitingFor
			if kept := got.Equal(due); kept != (name == "same slot") {
				t.Errorf("waiting for %v after the edit (%s)", got, name)
			}
		})
	}
}

// The wait is persisted, so a reopened store still knows the slot it waits on.
func TestStore_KeepsAWaitAcrossAReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{
		Source: "bundled://demo", Spec: Spec{Freq: FreqDaily, Hour: 2}, Enabled: true,
		Anchor: at(2026, time.August, 3, 12, 0),
	}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	due := at(2026, time.August, 4, 2, 0)
	if err := st.setWaiting(t.Context(), e.ID(), due); err != nil {
		t.Fatalf("setWaiting: %v", err)
	}
	again, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := again.List()[0].WaitingFor; !got.Equal(due) {
		t.Errorf("waiting for %v after a reopen, want %v", got, due)
	}
}
