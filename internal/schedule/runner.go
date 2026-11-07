// Package schedule launches a recipe at each due slot of its clock schedule.
package schedule

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
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

// boundBy reports whether the live run holds row's lease with a slot no later than due, the one case where
// its own bound ends it at this slot. A workflow id alone cannot say so: a person's retry reuses it under a manual lease.
func (b *BusyError) boundBy(row launchrule.Row, due time.Time) bool {
	return b.ScheduleID != "" && b.ScheduleID == row.ID && !b.SlotAt.IsZero() && !b.SlotAt.After(due)
}

// Launcher starts one workflow run on behalf of a clock rule. row lets the host attribute the run's outcome to
// that rule's row. inputs are the rule's fixed inputs. slotAt is an input to the run's bound; zero means no slot. An error with no id names no run, as with ErrNoRunCanStart and ErrEngineBusy always. An id
// with an error names a run created but not started, whose failure the host recorded on the row.
type Launcher interface {
	LaunchScheduled(
		ctx context.Context, source string, row launchrule.Row, inputs map[string]string, defaults marotte.RunDefaults,
		slotAt time.Time,
	) (id, name string, err error)
	// NotifyNotStarted tells the user that a slot of rule started no run; reason is the text its row records.
	NotifyNotStarted(ctx context.Context, rule *launchrule.Rule, reason string)
}

// Runner fires the due slots of clock rules. Construct with NewRunner and call Run in a
// goroutine; it returns when ctx is cancelled.
type Runner struct {
	store    *launchrule.Store
	launcher Launcher
	now      func() time.Time
	// held is each row's due slot a launch refused with ErrNoRunCanStart, so its miss is recorded as that.
	held map[launchrule.Row]missedSlot
	// retry is each waiting slot's backoff (Status.WaitingFor holds the wait itself), so a restart retries at once.
	retry map[launchrule.Row]engineRetry
	// published is every rule whose schedule series the metrics page carries.
	published map[string]bool
	// blockedSince is when publish first saw each enabled rule blocked, the slot its next-due series reads.
	blockedSince map[string]time.Time
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

// NewRunner wires a runner over the store's clock rules and the launcher.
func NewRunner(store *launchrule.Store, launcher Launcher) *Runner {
	return &Runner{store: store, launcher: launcher, now: time.Now, tick: tickInterval, grace: missGrace}
}

// Run polls until ctx is cancelled. It does NOT sweep on entry: a schedule due
// during a restart is a missed slot, and firing it at boot is what this avoids.
func (r *Runner) Run(ctx context.Context) {
	r.publish(r.store.Clocks())
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
	list := r.store.Clocks()
	// A hold belongs to an enabled row's due slot: a disabled or deleted one keeps none.
	enabled := make(map[launchrule.Row]bool, len(list))
	for i := range list {
		enabled[list[i].Row()] = list[i].Enabled
	}
	maps.DeleteFunc(r.held, func(row launchrule.Row, _ missedSlot) bool { return !enabled[row] })
	maps.DeleteFunc(r.retry, func(row launchrule.Row, _ engineRetry) bool { return !enabled[row] })
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		r.judge(ctx, list[i].ID, start)
	}
	r.publish(r.store.Clocks())
}

// publish exports each enabled clock rule's failed-slot count and next due slot, and removes the series of every rule
// disabled or deleted since the last publish. A slot waiting on the engine reads due at the end of its wait. A blocked
// rule never fires, so it reads due since publish first saw it blocked, with no count.
func (r *Runner) publish(list []launchrule.Clock) {
	live := make(map[string]bool, len(list))
	blocked := make(map[string]time.Time)
	for i := range list {
		c := &list[i]
		if !c.Enabled {
			continue
		}
		live[c.ID] = true
		if c.Err != nil {
			since, ok := r.blockedSince[c.ID]
			if !ok {
				since = r.now()
			}
			blocked[c.ID] = since
			obs.ScheduleBlocked(c.ID, since)
			continue
		}
		due, err := NextRunFrom(*c.When.Clock, c.Anchor, time.Time{})
		switch {
		case err != nil:
			due = time.Time{}
		case c.WaitingFor.Equal(due):
			due = r.waitBound(*c.When.Clock, due)
		}
		obs.Schedule(c.ID, c.FailedSlots, due)
	}
	for id := range r.published {
		if !live[id] {
			obs.ScheduleGone(id)
		}
	}
	r.published, r.blockedSince = live, blocked
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
		e, ok := r.store.Clock(id)
		// An unreadable row is the store's to report; firing it would measure from an anchor nobody knows.
		if !ok || !e.Enabled || e.Err != nil {
			return
		}
		// No floor: the runner is the one caller that must SEE a slot already gone, to tell a due slot from a missed one.
		due, err := NextRunFrom(*e.When.Clock, e.Anchor, time.Time{})
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
func (r *Runner) reach(ctx context.Context, e *launchrule.Clock, due, start, now time.Time) slotTurn {
	turn := slotTurn{due: due, at: now}
	switch {
	// A run started at or after due is this slot's launch whose record never landed.
	case e.LastRunID != "" && !e.LastRunAt.Before(due):
		turn.event, turn.runID = slotLaunched, e.LastRunID
		return turn
	case e.WaitingFor.Equal(due):
		if !now.Before(r.waitBound(*e.When.Clock, due)) {
			turn.event = slotOvertaken
			return turn
		}
		if b, ok := r.retry[e.Row()]; ok && b.due.Equal(due) && now.Before(b.at) {
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
func (r *Runner) waitBound(s launchrule.Spec, due time.Time) time.Time {
	bound, err := launchrule.NextRun(s, due)
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
func (r *Runner) launch(ctx context.Context, e *launchrule.Clock, due time.Time) slotTurn {
	// Bound the run by the next slot after `due` (not now), so a late fire cannot extend the
	// budget into it. An uncomputable slot degrades to the idle window, never to unbounded.
	slotAt, dErr := launchrule.NextRun(*e.When.Clock, due)
	if dErr != nil {
		slog.Warn("schedule cannot name its next slot, so its run is bounded by its idle window alone",
			"id", e.ID, "source", e.Source, "error", dErr)
		slotAt = time.Time{}
	}
	runID, name, err := r.launcher.LaunchScheduled(ctx, e.Source, e.Row(), e.Inputs,
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
	case isBusy && busy.boundBy(e.Row(), due):
		turn.event = slotBoundBusy
	default:
		turn.event = slotFailed
	}
	return turn
}

// settle is the one writer of a slot's state: the row (recordFire, skipTo, skipFailed, skipBusy, setWaiting) and the
// runner's held and retry memory, returning the row write's error. A launch that outlived its rule (deleted
// meanwhile) records nothing. The row writes ignore ctx's cancel, so a slot settled during a shutdown is recorded
// rather than judged again at the next start.
func (r *Runner) settle(ctx context.Context, e *launchrule.Clock, t *slotTurn) error {
	id, row := e.ID, e.Row()
	wctx := context.WithoutCancel(ctx)
	var err error
	// missed is the row as a slot that started no run left it, zero for any other slot.
	var missed launchrule.Status
	switch t.event {
	case slotBackingOff:
		return nil
	case slotCancelled:
		slog.Info("schedule slot left for the next start: the runner stopped during its launch",
			"id", id, "due", t.due, "error", t.err)
		return nil
	case slotEngineBusy:
		delete(r.held, row)
		step := r.backOff(row, t.due, t.at)
		slog.Warn("schedule slot waiting on the agent engine", "id", id, "due", t.due, "retry_in", step, "reason", t.err)
		err = r.setWaiting(wctx, row, t.due)
	case slotSignInHeld:
		// The engine answered, so the slot is the sign-in gate's hold now, bounded by its grace.
		delete(r.retry, row)
		r.hold(row, t.due, t.err)
		if !e.WaitingFor.IsZero() {
			err = r.setWaiting(wctx, row, time.Time{})
		}
	case slotGraceExpired:
		missed, err = r.miss(wctx, row, t.due, t.at)
	case slotOvertaken:
		delete(r.retry, row)
		slog.Warn("schedule slot skipped: the agent engine did not answer before the next slot", "id", id, "due", t.due)
		obs.ScheduleSlotNotStarted(obs.SlotEngineBusy)
		missed, err = r.skipBusy(wctx, row, t.due)
	case slotBoundBusy:
		r.forget(row)
		// The live run is cancelled at this slot by its bound, which writes the row; the refusal would hide that outcome.
		busy, _ := errors.AsType[*BusyError](t.err)
		slog.Warn("scheduled run did not start", "id", id, "source", e.Source, "error", t.err, "run_id", busy.WorkflowID)
		err = r.skipTo(wctx, row, t.due)
	case slotFailed:
		// The anchor advances so a launch that keeps failing does not retry every tick.
		r.forget(row)
		slog.Warn("scheduled run did not start", "id", id, "source", e.Source, "error", t.err)
		obs.ScheduleSlotNotStarted(obs.SlotLaunchFailed)
		missed, err = r.recordFire(wctx, row, t.due, "", launchrule.Outcome{
			Status: launchrule.StatusFailed, Reason: launchrule.ReasonText(t.err.Error()),
		})
	case slotLaunched:
		r.forget(row)
		missed, err = r.settleLaunched(wctx, e, t)
	}
	switch {
	case errors.Is(err, launchrule.ErrNotFound):
		slog.Info("schedule slot not recorded: the schedule was deleted during its launch",
			"id", id, "due", t.due, "run_id", t.runID)
	case err != nil:
		slog.Error("schedule record failed", "id", id, "error", err)
	default:
		r.notifyFirstMiss(wctx, e, &missed)
	}
	return err
}

// notifyFirstMiss tells the user about a slot that started no run only when it begins a streak: settleSlot adds one
// to FailedSlots for each new failed slot, so a count of 1 after the slot's write means it went from 0 to 1.
func (r *Runner) notifyFirstMiss(ctx context.Context, e *launchrule.Clock, missed *launchrule.Status) {
	if missed.FailedSlots == 1 {
		r.launcher.NotifyNotStarted(ctx, &e.Rule, missed.LastReason)
	}
}

// settleLaunched records the run the launch of t's slot created, failed when the launch also returned an error. It
// returns the row as it landed when the slot is one that started no run, zero otherwise.
func (r *Runner) settleLaunched(ctx context.Context, e *launchrule.Clock, t *slotTurn) (launchrule.Status, error) {
	o := launchrule.Outcome{Status: launchrule.StatusStarted}
	if t.err != nil {
		o = launchrule.Outcome{Status: launchrule.StatusFailed, Reason: launchrule.ReasonText(t.err.Error())}
		slog.Warn("scheduled run did not start", "id", e.ID, "source", e.Source, "error", t.err, "run_id", t.runID)
	} else {
		slog.Info("scheduled run started", "id", e.ID, "run_id", t.runID, "recipe", t.name, "due", t.due)
	}
	landed, err := r.recordFire(ctx, e.Row(), t.due, t.runID, o)
	// A launch error the run's own record contradicts, a cancel after the host recorded the run started, is not a
	// slot that produced no run.
	if t.err == nil || landed.LastStatus != launchrule.StatusFailed {
		return launchrule.Status{}, err
	}
	obs.ScheduleSlotNotStarted(obs.SlotLaunchFailed)
	return landed, err
}

// forget drops the runner's memory of the schedule's slot once its launch had an answer that ends it.
func (r *Runner) forget(row launchrule.Row) {
	delete(r.held, row)
	delete(r.retry, row)
}

// backOff sets when the slot due waiting on the engine is tried next: one tick after at, the time its launch returned,
// doubling per try up to busyRetryMax.
func (r *Runner) backOff(row launchrule.Row, due, at time.Time) time.Duration {
	step := r.tick
	if b, ok := r.retry[row]; ok && b.due.Equal(due) {
		step = min(2*b.step, busyRetryMax)
	}
	if r.retry == nil {
		r.retry = make(map[launchrule.Row]engineRetry)
	}
	r.retry[row] = engineRetry{due: due, at: at.Add(step), step: step}
	return step
}

// hold keeps a due slot for a later tick inside its grace; why is the launch's ErrNoRunCanStart refusal.
func (r *Runner) hold(row launchrule.Row, due time.Time, why error) {
	slog.Warn("schedule slot held until a run can start", "id", row.ID, "due", due, "reason", why)
	if r.held == nil {
		r.held = make(map[launchrule.Row]missedSlot)
	}
	r.held[row] = missedSlot{due: due, why: launchrule.ReasonText(why.Error())}
}

// miss skips a slot past its grace. A held slot is recorded failed with its last refusal, and the row as it landed is
// returned; an offline miss records nothing and returns a zero row.
func (r *Runner) miss(ctx context.Context, row launchrule.Row, due, now time.Time) (launchrule.Status, error) {
	lateBy := now.Sub(due).Round(time.Second)
	if held, ok := r.held[row]; ok && held.due.Equal(due) {
		delete(r.held, row)
		slog.Info("schedule slot missed while no run could start, skipping", "id", row.ID, "due", due, "late_by", lateBy)
		obs.ScheduleSlotNotStarted(obs.SlotSignInHeld)
		return r.skipFailed(ctx, row, now, held)
	}
	slog.Info("schedule slot missed while offline, skipping", "id", row.ID, "due", due, "late_by", lateBy)
	return launchrule.Status{}, r.skipTo(ctx, row, now)
}

// reasonEngineBusy is the reason of a slot the agent engine never answered before the schedule's next slot.
const reasonEngineBusy = "engine busy"

// recordFire advances a rule's anchor after a fire or a skip, to the DUE time rather than now, so the schedule
// cannot drift by the tick's latency. runID is the run the fire launched, "" for none; a row already tracking it
// keeps its outcome, since a fast run can end (RecordEnding) before its launch returns. It returns the row as it
// landed.
func (r *Runner) recordFire(
	ctx context.Context, row launchrule.Row, due time.Time, runID string, o launchrule.Outcome,
) (launchrule.Status, error) {
	var landed launchrule.Status
	err := r.store.UpdateClock(ctx, row, func(st *launchrule.Status) bool {
		st.Anchor = due
		st.LastRunAt = due
		st.WaitingFor = time.Time{}
		if runID == "" || st.LastRunID != runID {
			st.BeginSlot(runID, o)
		}
		landed = *st
		return true
	})
	return landed, err
}

// skipTo advances the anchor WITHOUT recording a run, for a slot missed while the container was down or refused
// by a run whose bound ends it at that slot (BusyError.boundBy).
func (r *Runner) skipTo(ctx context.Context, row launchrule.Row, to time.Time) error {
	return r.store.UpdateClock(ctx, row, func(st *launchrule.Status) bool {
		st.Anchor, st.WaitingFor = to, time.Time{}
		return true
	})
}

// missedSlot is a due slot every launch refused until its grace ran out, and the last refusal's text.
type missedSlot struct {
	due time.Time
	why string
}

// skipFailed advances the anchor like skipTo and records the missed slot as failed. It detaches the row's run,
// so a late outcome of the previous slot's run cannot replace this slot's failure.
func (r *Runner) skipFailed(ctx context.Context, row launchrule.Row, to time.Time, m missedSlot) (launchrule.Status, error) {
	return r.skipRecorded(ctx, row, to, m.due, launchrule.Outcome{Status: launchrule.StatusFailed, Reason: m.why})
}

// skipBusy records the slot due as skipped because the engine never answered, anchored at due so the schedule's
// next slot, the one that ended the wait, still fires.
func (r *Runner) skipBusy(ctx context.Context, row launchrule.Row, due time.Time) (launchrule.Status, error) {
	return r.skipRecorded(ctx, row, due, due, launchrule.Outcome{Status: launchrule.StatusSkipped, Reason: reasonEngineBusy})
}

// skipRecorded advances the anchor to `to` and records o as the slot due's outcome, detaching the row's run. It
// returns the row as it landed.
func (r *Runner) skipRecorded(
	ctx context.Context, row launchrule.Row, to, due time.Time, o launchrule.Outcome,
) (launchrule.Status, error) {
	var landed launchrule.Status
	err := r.store.UpdateClock(ctx, row, func(st *launchrule.Status) bool {
		st.Anchor, st.LastRunAt, st.WaitingFor = to, due, time.Time{}
		st.BeginSlot("", o)
		landed = *st
		return true
	})
	return landed, err
}

// setWaiting marks (a non-zero due) or clears (zero) the slot the row waits on the engine for, leaving the anchor
// and the last outcome, which stay the previous slot's until this one ends.
func (r *Runner) setWaiting(ctx context.Context, row launchrule.Row, due time.Time) error {
	return r.store.UpdateClock(ctx, row, func(st *launchrule.Status) bool {
		if st.WaitingFor.Equal(due) {
			return false
		}
		st.WaitingFor = due
		return true
	})
}
