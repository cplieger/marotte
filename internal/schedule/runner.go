package schedule

import (
	"context"
	"log/slog"
	"time"
)

// TickInterval is how often the runner looks for due schedules. One shared ticker
// rather than a timer per schedule: there is no per-entry lifecycle to leak.
const TickInterval = time.Minute

// MissGrace is how late a due slot may be and still fire; a later slot was missed while
// down and is SKIPPED (a burst of overdue runs is worse). It must exceed TickInterval, or a
// slot landing between ticks would never run.
const MissGrace = 3 * time.Minute

// Launcher starts one workflow run on behalf of a schedule. scheduleID lets the host
// attribute the run's outcome to the row. slotAt is an input to the run's bound; zero means
// no slot.
type Launcher interface {
	LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error)
}

// Runner fires due schedules. Construct with NewRunner and call Run in a
// goroutine; it returns when ctx is cancelled.
type Runner struct {
	store    *Store
	launcher Launcher
	now      func() time.Time
	tick     time.Duration
	grace    time.Duration
}

// NewRunner wires a runner over the store and launcher.
func NewRunner(store *Store, launcher Launcher) *Runner {
	return &Runner{store: store, launcher: launcher, now: time.Now, tick: TickInterval, grace: MissGrace}
}

// Run polls until ctx is cancelled. It does NOT sweep on entry: a schedule due
// during a restart is a missed slot, and firing it at boot is what this avoids.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// sweep fires every schedule whose slot is due and recent.
func (r *Runner) sweep(ctx context.Context) {
	now := r.now()
	list := r.store.List()
	for i := range list {
		e := &list[i]
		if !e.Enabled {
			continue
		}
		// No floor: the runner is the one caller that must SEE a slot already gone,
		// so the branches below can tell a due slot from one missed while down.
		due, err := NextRunFrom(e.Spec, e.Anchor, time.Time{})
		if err != nil {
			// Say so once per tick rather than disabling the user's row.
			slog.Warn("schedule has no next run", "id", e.ID, "error", err)
			continue
		}
		if now.Before(due) {
			continue
		}
		if now.Sub(due) > r.grace {
			slog.Info("schedule slot missed while offline, skipping",
				"id", e.ID, "due", due, "late_by", now.Sub(due).Round(time.Second))
			if err := r.store.skipTo(ctx, e.ID, now); err != nil {
				slog.Error("schedule skip failed", "id", e.ID, "error", err)
			}
			continue
		}
		r.fire(ctx, e, due)
	}
}

// fire launches one run and records the outcome. The anchor advances either
// way so a schedule whose launch keeps failing does not retry every tick.
func (r *Runner) fire(ctx context.Context, e *Entry, due time.Time) {
	// Bound the run by the next slot after `due` (not now), so a late fire cannot extend the
	// budget into it. An uncomputable slot degrades to the idle window, never to unbounded.
	slotAt, dErr := NextRun(e.Spec, due)
	if dErr != nil {
		slog.Warn("schedule cannot name its next slot, so its run is bounded by its idle window alone",
			"id", e.ID, "source", e.Source, "error", dErr)
		slotAt = time.Time{}
	}
	runID, name, err := r.launcher.LaunchScheduled(ctx, e.Source, e.ID, slotAt)
	outcome := Outcome{Status: StatusStarted}
	if err != nil {
		// An overlap (one live run per recipe) is the expected refusal: this slot is skipped.
		outcome = Outcome{Status: StatusFailed, Reason: err.Error()}
		slog.Warn("scheduled run did not start", "id", e.ID, "source", e.Source, "error", err)
	} else {
		slog.Info("scheduled run started", "id", e.ID, "run_id", runID, "recipe", name, "due", due)
	}
	if err := r.store.recordFire(ctx, e.ID, due, outcome); err != nil {
		slog.Error("schedule record failed", "id", e.ID, "error", err)
	}
}
