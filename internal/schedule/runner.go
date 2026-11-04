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

// ErrEngineBusy marks a LaunchScheduled error that started nothing because the agent engine did not answer in time;
// the runner retries that slot until the schedule's next slot, then records it skipped.
var ErrEngineBusy = errors.New("the agent engine did not answer")

// busyRetryMax caps the backoff between launches of a slot waiting on the engine: each attempt can hold the sweep,
// and every other schedule's slot behind it, for the launch's own timeout.
const busyRetryMax = 8 * time.Minute

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
// no slot. An error with no id names no run, as with ErrNoRunCanStart and ErrEngineBusy always. An id
// with an error names a run created but not started, whose failure the host recorded on the row.
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
	held map[string]missedSlot
	// retry is each waiting slot's backoff (Entry.WaitingFor holds the wait itself), so a restart retries at once.
	retry map[string]engineRetry
	// lastStart and lastEnd bound the previous sweep, for lateness.
	lastStart time.Time
	lastEnd   time.Time
	tick      time.Duration
	grace     time.Duration
}

// engineRetry is when the slot due a launch left waiting on the engine is tried next, and the step that set it.
type engineRetry struct {
	due  time.Time
	at   time.Time
	step time.Duration
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
	start := r.now()
	defer func() { r.lastStart, r.lastEnd = start, r.now() }()
	list := r.store.List()
	// A hold belongs to an enabled schedule's due slot: a disabled or deleted one keeps none.
	enabled := make(map[string]bool, len(list))
	for i := range list {
		enabled[list[i].ID()] = list[i].Enabled
	}
	maps.DeleteFunc(r.held, func(id string, _ missedSlot) bool { return !enabled[id] })
	maps.DeleteFunc(r.retry, func(id string, _ engineRetry) bool { return !enabled[id] })
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		r.judge(ctx, list[i].ID(), start)
	}
}

// slotEvent is what happened to a schedule's due slot when a sweep reached it; settle alone records it.
type slotEvent int

const (
	// slotBackingOff: the slot waits on the engine and its retry is not due yet.
	slotBackingOff slotEvent = iota
	// slotLaunched: the launch created a run, or the row already names one started for this slot by a launch
	// whose record never landed.
	slotLaunched
	// slotEngineBusy: the engine did not answer before the run was created.
	slotEngineBusy
	// slotSignInHeld: the sign-in gate refused it (ErrNoRunCanStart).
	slotSignInHeld
	// slotBoundBusy: the row's own run holds the recipe and its bound ends it at this slot (BusyError.boundBy).
	slotBoundBusy
	// slotFailed: any other launch error.
	slotFailed
	// slotCancelled: the runner's context ended during a launch that returned no run id; the row stays as it was.
	slotCancelled
	// slotGraceExpired: never launched inside its grace, while marotte was down or the sign-in gate held it.
	slotGraceExpired
	// slotOvertaken: the schedule's next slot came while this one still waited on the engine.
	slotOvertaken
)

// slotTurn is one event on the slot due, with what settle records: at is the clock after it (after the launch, for a
// launch event), and runID and err are the launch's answer.
type slotTurn struct {
	due   time.Time
	at    time.Time
	err   error
	runID string
	name  string
	event slotEvent
}

// judge settles schedule id's due slot once the clock has reached it. An overtaken slot hands the sweep to the next
// slot in the same pass, judged against this sweep's start, so a long sweep cannot age it past its grace.
func (r *Runner) judge(ctx context.Context, id string, start time.Time) {
	for {
		// Read per slot, the row and the clock: each launch before it can hold the sweep for the launch's own
		// timeout, and the user may have edited, disabled or deleted the schedule meanwhile.
		e, ok := r.store.get(id)
		if !ok || !e.Enabled {
			return
		}
		// No floor: the runner is the one caller that must SEE a slot already gone, to tell a due slot from a missed one.
		due, err := NextRunFrom(e.Spec, e.Anchor, time.Time{})
		if err != nil {
			// Say so once per tick rather than disabling the user's row.
			slog.Warn("schedule has no next run", "id", id, "error", err)
			return
		}
		now := r.now()
		if now.Before(due) {
			return
		}
		turn := r.reach(ctx, &e, due, start, now)
		if r.settle(ctx, &e, &turn) != nil || turn.event != slotOvertaken {
			return
		}
	}
}

// reach decides the event on the slot due at now, launching it when nothing else ends it first.
func (r *Runner) reach(ctx context.Context, e *Entry, due, start, now time.Time) slotTurn {
	turn := slotTurn{due: due, at: now}
	switch {
	// A run started at or after due is this slot's launch whose record never landed.
	case e.LastRunID != "" && !e.LastRunAt.Before(due):
		turn.event, turn.runID = slotLaunched, e.LastRunID
		return turn
	case e.WaitingFor.Equal(due):
		if !now.Before(r.waitBound(e.Spec, due)) {
			turn.event = slotOvertaken
			return turn
		}
		if b, ok := r.retry[e.ID()]; ok && b.due.Equal(due) && now.Before(b.at) {
			turn.event = slotBackingOff
			return turn
		}
	case r.lateness(due, start) > r.grace:
		turn.event = slotGraceExpired
		return turn
	}
	return r.launch(ctx, e, due)
}

// waitBound is the schedule's next slot after due, which ends a wait on the engine; the grace when it has none.
func (r *Runner) waitBound(s Spec, due time.Time) time.Time {
	bound, err := nextRun(s, due)
	if err != nil {
		return due.Add(r.grace)
	}
	return bound
}

// lateness is how long the runner left due unswept, the time the grace bounds. A slot that fell due while the previous
// sweep was still launching counts from that sweep's end, and one that fell due during this sweep is not late.
func (r *Runner) lateness(due, start time.Time) time.Duration {
	from := due
	if due.After(r.lastStart) && due.Before(r.lastEnd) {
		from = r.lastEnd
	}
	return start.Sub(from)
}

// launch starts the slot due's run and classifies the answer.
func (r *Runner) launch(ctx context.Context, e *Entry, due time.Time) slotTurn {
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
	turn := slotTurn{due: due, at: r.now(), err: err, runID: runID, name: name}
	busy, isBusy := errors.AsType[*BusyError](err)
	switch {
	case err == nil || runID != "":
		turn.event = slotLaunched
	case ctx.Err() != nil:
		turn.event = slotCancelled
	case errors.Is(err, ErrEngineBusy):
		turn.event = slotEngineBusy
	case errors.Is(err, ErrNoRunCanStart):
		turn.event = slotSignInHeld
	case isBusy && busy.boundBy(e.ID(), due):
		turn.event = slotBoundBusy
	default:
		turn.event = slotFailed
	}
	return turn
}

// settle is the one writer of a slot's state: the row (recordFire, skipTo, skipFailed, skipBusy, setWaiting) and the
// runner's held and retry memory, returning the row write's error. The row writes ignore ctx's cancel, so a slot
// settled during a shutdown is recorded rather than judged again at the next start.
func (r *Runner) settle(ctx context.Context, e *Entry, t *slotTurn) error {
	id := e.ID()
	wctx := context.WithoutCancel(ctx)
	var err error
	switch t.event {
	case slotBackingOff:
		return nil
	case slotCancelled:
		slog.Info("schedule slot left for the next start: the runner stopped during its launch",
			"id", id, "due", t.due, "error", t.err)
		return nil
	case slotEngineBusy:
		delete(r.held, id)
		step := r.backOff(id, t.due, t.at)
		slog.Warn("schedule slot waiting on the agent engine", "id", id, "due", t.due, "retry_in", step, "reason", t.err)
		err = r.store.setWaiting(wctx, id, t.due)
	case slotSignInHeld:
		// The engine answered, so the slot is the sign-in gate's hold now, bounded by its grace.
		delete(r.retry, id)
		r.hold(id, t.due, t.err)
		if !e.WaitingFor.IsZero() {
			err = r.store.setWaiting(wctx, id, time.Time{})
		}
	case slotGraceExpired:
		err = r.miss(wctx, id, t.due, t.at)
	case slotOvertaken:
		delete(r.retry, id)
		slog.Warn("schedule slot skipped: the agent engine did not answer before the next slot", "id", id, "due", t.due)
		err = r.store.skipBusy(wctx, id, t.due)
	case slotBoundBusy:
		r.forget(id)
		// The live run is cancelled at this slot by its bound, which writes the row; the refusal would hide that outcome.
		busy, _ := errors.AsType[*BusyError](t.err)
		slog.Warn("scheduled run did not start", "id", id, "source", e.Source, "error", t.err, "run_id", busy.WorkflowID)
		err = r.store.skipTo(wctx, id, t.due)
	case slotFailed:
		// The anchor advances so a launch that keeps failing does not retry every tick.
		r.forget(id)
		slog.Warn("scheduled run did not start", "id", id, "source", e.Source, "error", t.err)
		err = r.store.recordFire(wctx, id, t.due, "", Outcome{Status: StatusFailed, Reason: t.err.Error()})
	case slotLaunched:
		r.forget(id)
		o := Outcome{Status: statusStarted}
		if t.err != nil {
			o = Outcome{Status: StatusFailed, Reason: t.err.Error()}
			slog.Warn("scheduled run did not start", "id", id, "source", e.Source, "error", t.err, "run_id", t.runID)
		} else {
			slog.Info("scheduled run started", "id", id, "run_id", t.runID, "recipe", t.name, "due", t.due)
		}
		err = r.store.recordFire(wctx, id, t.due, t.runID, o)
	}
	if err != nil {
		slog.Error("schedule record failed", "id", id, "error", err)
	}
	return err
}

// forget drops the runner's memory of the schedule's slot once its launch had an answer that ends it.
func (r *Runner) forget(id string) {
	delete(r.held, id)
	delete(r.retry, id)
}

// backOff sets when the slot due waiting on the engine is tried next: one tick after at, the time its launch returned,
// doubling per try up to busyRetryMax.
func (r *Runner) backOff(id string, due, at time.Time) time.Duration {
	step := r.tick
	if b, ok := r.retry[id]; ok && b.due.Equal(due) {
		step = min(2*b.step, busyRetryMax)
	}
	if r.retry == nil {
		r.retry = make(map[string]engineRetry)
	}
	r.retry[id] = engineRetry{due: due, at: at.Add(step), step: step}
	return step
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
func (r *Runner) miss(ctx context.Context, id string, due, now time.Time) error {
	lateBy := now.Sub(due).Round(time.Second)
	if held, ok := r.held[id]; ok && held.due.Equal(due) {
		delete(r.held, id)
		slog.Info("schedule slot missed while no run could start, skipping", "id", id, "due", due, "late_by", lateBy)
		return r.store.skipFailed(ctx, id, now, held)
	}
	slog.Info("schedule slot missed while offline, skipping", "id", id, "due", due, "late_by", lateBy)
	return r.store.skipTo(ctx, id, now)
}
