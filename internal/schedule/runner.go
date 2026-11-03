package schedule

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// tickInterval is how often the runner looks for due schedules. One shared ticker
// rather than a timer per schedule: there is no per-entry lifecycle to leak.
const tickInterval = time.Minute

// missGrace is how late a due slot may be and still fire; a later slot was missed while
// down and is SKIPPED (a burst of overdue runs is worse). It must exceed tickInterval, or a
// slot landing between ticks would never run.
const missGrace = 3 * time.Minute

// ErrNoRunCanStart marks a LaunchScheduled error that refused the launch because no run can start now; the
// runner holds that slot for a later tick inside its grace and records it failed only once the grace runs out.
var ErrNoRunCanStart = errors.New("no run can start now")

// BusyError is a LaunchScheduled refusal under the one-live-run-per-recipe rule. WorkflowID is the live run;
// ScheduleID and SlotAt are its run lease's, empty when marotte holds no scheduled lease for it.
type BusyError struct {
	SlotAt     time.Time
	WorkflowID string
	ScheduleID string
}

func (*BusyError) Error() string { return "this recipe already has a live run" }

// boundBy reports whether the live run holds scheduleID's lease with a slot no later than due, the one case where
// its own bound ends it at this slot. A workflow id alone cannot say so: a person's retry reuses it under a manual lease.
func (b *BusyError) boundBy(scheduleID string, due time.Time) bool {
	return b.ScheduleID != "" && b.ScheduleID == scheduleID && !b.SlotAt.IsZero() && !b.SlotAt.After(due)
}

// Launcher starts one workflow run on behalf of a schedule. scheduleID lets the host
// attribute the run's outcome to the row. slotAt is an input to the run's bound; zero means
// no slot. An error wrapping ErrNoRunCanStart started nothing.
type Launcher interface {
	LaunchScheduled(
		ctx context.Context, source, scheduleID string, defaults marotte.RunDefaults, slotAt time.Time,
	) (id, name string, err error)
}

// Runner fires due schedules. Construct with NewRunner and call Run in a
// goroutine; it returns when ctx is cancelled.
type Runner struct {
	store    *Store
	launcher Launcher
	now      func() time.Time
	// held is each schedule's due slot a launch refused with ErrNoRunCanStart, so its miss is recorded as that.
	held  map[string]missedSlot
	tick  time.Duration
	grace time.Duration
}

// NewRunner wires a runner over the store and launcher.
func NewRunner(store *Store, launcher Launcher) *Runner {
	return &Runner{store: store, launcher: launcher, now: time.Now, tick: tickInterval, grace: missGrace}
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

func (r *Runner) sweep(ctx context.Context) {
	now := r.now()
	list := r.store.List()
	// A hold belongs to an enabled schedule's due slot: a disabled or deleted one keeps none.
	enabled := make(map[string]bool, len(list))
	for i := range list {
		enabled[list[i].ID()] = list[i].Enabled
	}
	maps.DeleteFunc(r.held, func(id string, _ missedSlot) bool { return !enabled[id] })
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
			slog.Warn("schedule has no next run", "id", e.ID(), "error", err)
			continue
		}
		if now.Before(due) {
			continue
		}
		if now.Sub(due) > r.grace {
			r.miss(ctx, e.ID(), due, now)
			continue
		}
		r.fire(ctx, e, due)
	}
}

// hold keeps a due slot for a later tick inside its grace; why is the launch's ErrNoRunCanStart refusal.
func (r *Runner) hold(id string, due time.Time, why error) {
	slog.Warn("schedule slot held until a run can start", "id", id, "due", due, "reason", why)
	if r.held == nil {
		r.held = make(map[string]missedSlot)
	}
	r.held[id] = missedSlot{due: due, why: why.Error()}
}

// miss skips a slot past its grace. A held slot is recorded failed with its last refusal; an offline miss records nothing.
func (r *Runner) miss(ctx context.Context, id string, due, now time.Time) {
	lateBy := now.Sub(due).Round(time.Second)
	var err error
	if held, ok := r.held[id]; ok && held.due.Equal(due) {
		delete(r.held, id)
		slog.Info("schedule slot missed while no run could start, skipping", "id", id, "due", due, "late_by", lateBy)
		err = r.store.skipFailed(ctx, id, now, held)
	} else {
		slog.Info("schedule slot missed while offline, skipping", "id", id, "due", due, "late_by", lateBy)
		err = r.store.skipTo(ctx, id, now)
	}
	if err != nil {
		slog.Error("schedule skip failed", "id", id, "error", err)
	}
}

// The anchor advances either way so a schedule whose launch keeps failing does not retry every
// tick, except for a launch refused with ErrNoRunCanStart: that slot is held for a later tick.
func (r *Runner) fire(ctx context.Context, e *Entry, due time.Time) {
	// Bound the run by the next slot after `due` (not now), so a late fire cannot extend the
	// budget into it. An uncomputable slot degrades to the idle window, never to unbounded.
	slotAt, dErr := nextRun(e.Spec, due)
	if dErr != nil {
		slog.Warn("schedule cannot name its next slot, so its run is bounded by its idle window alone",
			"id", e.ID(), "source", e.Source, "error", dErr)
		slotAt = time.Time{}
	}
	runID, name, err := r.launcher.LaunchScheduled(ctx, e.Source, e.ID(),
		marotte.RunDefaults{Model: e.Model, Effort: e.Effort}, slotAt)
	if errors.Is(err, ErrNoRunCanStart) {
		r.hold(e.ID(), due, err)
		return
	}
	delete(r.held, e.ID())
	if busy, ok := errors.AsType[*BusyError](err); ok && busy.boundBy(e.ID(), due) {
		// The live run is cancelled at this slot by its bound, which writes the row; the refusal would hide that outcome.
		slog.Warn("scheduled run did not start", "id", e.ID(), "source", e.Source, "error", err, "run_id", busy.WorkflowID)
		if sErr := r.store.skipTo(ctx, e.ID(), due); sErr != nil {
			slog.Error("schedule record failed", "id", e.ID(), "error", sErr)
		}
		return
	}
	outcome := Outcome{Status: statusStarted}
	if err != nil {
		// An overlap (one live run per recipe) is the expected refusal: this slot is skipped.
		outcome = Outcome{Status: StatusFailed, Reason: err.Error()}
		slog.Warn("scheduled run did not start", "id", e.ID(), "source", e.Source, "error", err)
	} else {
		slog.Info("scheduled run started", "id", e.ID(), "run_id", runID, "recipe", name, "due", due)
	}
	if err := r.store.recordFire(ctx, e.ID(), due, runID, outcome); err != nil {
		slog.Error("schedule record failed", "id", e.ID(), "error", err)
	}
}
