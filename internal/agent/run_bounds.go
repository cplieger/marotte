package agent

// Run bounds: one deadline per run, the per-step turn cap, and the recorded reason. The idle window,
// the absolute backstop and a scheduled run's next slot all feed the lease's one deadline
// (runlease.NextDeadline), re-stamped on start, parked on pause and rolled forward on progress,
// so it bounds executing and stalled time, not wall time. Zero is unbounded.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// runIdleWindow is the primary, stall bound: progress (a node completing, a step frame) rolls it
// forward, and idleVerdict decides what its expiry means. Longer than KAS's 300s stream idle timeout,
// which retries silently.
const runIdleWindow = 15 * time.Minute

// runBackstop is the absolute executing-time budget, for a runaway loop that refills the idle
// window forever. 36 hours is observed (recipes have run for eight). A constant: a raisable
// backstop is none.
const runBackstop = 36 * time.Hour

// refillGranularity is the smallest move a refill writes: each is a fsynced rewrite of runs.json. It
// makes the stall tolerance [runIdleWindow - refillGranularity, runIdleWindow]; Store.SetDeadline stays exact.
const refillGranularity = time.Minute

// minRunBudget is the smallest executing budget the slot may yield, internal/schedule's
// minMinuteInterval: change them together. It must not lift the backstop.
const minRunBudget = 5 * time.Minute

// The abnormal terminations a run's row reports; a user cancel records nothing. Each needs a sentence
// in static-src/history.ts END_REASON_TEXT. Stalled is the idle window's verdict; overran is the slot or the backstop.
const (
	runEndOverran  = "overran"
	runEndStalled  = "stalled"
	runEndOrphaned = "orphaned"
)

// logMsgRunOrphaned is a constant so the restart cancel is greppable as one line.
const logMsgRunOrphaned = "run was orphaned by a restart; cancelling so its recipe is idle again"

// logMsgRunStalled and logMsgRunBackstop are split because an operator acts differently: one stopped
// producing, the other worked its whole budget.
const (
	logMsgRunStalled  = "run made no progress inside its idle window; cancelling"
	logMsgRunBackstop = "run spent its absolute executing-time backstop; cancelling"
)

// logMsgRunYieldedToSlot is logged at INFO when a manual run yields to its recipe's next slot: the
// bound working, kept off logMsgRunOverran's alert.
const logMsgRunYieldedToSlot = "manual run reached its recipe's next scheduled slot; " +
	"cancelling so the schedule can run"

// It claims nothing about the run, since an unknown workflow id also lands here.
const logMsgCancelUnretried = "a run's cancel failed on every attempt; " +
	"marotte has stopped trying to stop it"

// Records outlive the run (History reads them), so the oldest goes first rather than clearing on
// the terminal frame.
const maxRunEndReasons = 256

// The deadline lives on the durable lease. Field order is govet's fieldalignment.
type runBoundsState struct {
	// timers holds each run's live deadline timer; a fired callback re-reads the deadline it was armed for.
	timers map[string]*time.Timer
	// terminating names runs whose termination was claimed (user cancel, schedule, own deadline); only the
	// winner records a reason and cancels. Dropped on terminal (forgetBounds) and on retry (clearEnd).
	terminating map[string]struct{}
	// heals counts automatic resumes since the run last made progress (run_host.go healPaused).
	heals map[string]int
	// cancelRetries counts refused-cancel retries since the last progress, separate from heals so neither starves the other.
	cancelRetries map[string]int
	// armedAt is when each bounded run's current executing stretch began, mirroring Bounded(). The
	// backstop's anchor, not Lease.StartedAt, which is wall time.
	armedAt map[string]time.Time
	// executed is executing time banked across completed stretches, so a parked run spends no backstop.
	// In memory: a restart grants a fresh backstop.
	executed map[string]time.Duration
	// arms is each run's latest armDeadline: a park decided before it must not undo it.
	arms map[string]runArm
	// reasons maps a workflow id to why it stopped; order is its FIFO eviction queue.
	reasons map[string]string
	order   []string
	// armSeq numbers every armDeadline call; runArm.seq takes the next.
	armSeq uint64
}

// runArm is one armDeadline call, numbered.
type runArm struct {
	seq uint64
}

// stampDeadline is the one transaction armDeadline and refillDeadline share: read the lease, decide,
// write the deadline and swap the timer under one hold of mu, or two stampers can leave a bounded
// run with no timer. Lock order mu then the lease store's; decide must not take mu. A lease-less run is refused.
func (rs *Runs) stampDeadline(
	ctx context.Context, workflowID string,
	decide func(l runlease.Lease, now time.Time) (time.Time, bool),
) bool {
	if workflowID == "" {
		return false
	}
	store := rs.leaseStore()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	l, held := store.Get(workflowID)
	if !held {
		return false
	}
	deadline, stamp := decide(l, time.Now())
	if !stamp {
		return false
	}
	err := store.SetDeadline(ctx, workflowID, deadline)
	if errors.Is(err, runlease.ErrNotFound) {
		// The lease went away mid-transaction: nothing to bound.
		delete(rs.bounds.armedAt, workflowID)
		delete(rs.bounds.executed, workflowID)
		delete(rs.bounds.arms, workflowID)
		return false
	}
	if err != nil {
		// Durability only: SetDeadline already set the in-memory deadline, so returning would leave it with no timer.
		slog.Error("a run's deadline is not durable, so it will not survive a restart; "+
			"this process still bounds the run",
			"workflow_id", workflowID, "deadline", deadline, "error", err)
	}
	rs.setTimerLocked(workflowID, deadline)
	return true
}

// runBoundsLocked composes NextDeadline's three inputs. stretchStart fixes the backstop instant for
// the whole stretch, so a refill recomputes the same value. Caller holds mu.
func (rs *Runs) runBoundsLocked(l *runlease.Lease, stretchStart time.Time) runlease.Bounds {
	return runlease.Bounds{
		SlotAt: l.SlotAt,
		// The floor must not lift this: NextDeadline clamps on it last, so a spent budget fires at once.
		BackstopAt: stretchStart.Add(runBackstop - rs.bounds.executed[l.WorkflowID]),
		Idle:       runIdleWindow,
		Floor:      minRunBudget,
	}
}

// Idempotent: `run_start` re-fires on resume, so the earliest arm wins. Every call on a leased run is
// recorded as the run's latest arm, stamped or not.
func (rs *Runs) armDeadline(ctx context.Context, workflowID string) {
	rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		rs.numberArmLocked(workflowID)
		if l.Bounded() {
			return time.Time{}, false
		}
		if rs.bounds.armedAt == nil {
			rs.bounds.armedAt = map[string]time.Time{}
		}
		rs.bounds.armedAt[workflowID] = now
		return runlease.NextDeadline(now, rs.runBoundsLocked(&l, now)), true
	})
}

// numberArmLocked numbers an arm, so a park decided before it does not undo it. Caller holds mu.
func (rs *Runs) numberArmLocked(workflowID string) {
	if rs.bounds.arms == nil {
		rs.bounds.arms = map[string]runArm{}
	}
	rs.bounds.armSeq++
	rs.bounds.arms[workflowID] = runArm{seq: rs.bounds.armSeq}
}

// lastArmSeq is the sequence number of the run's latest arm, zero for none.
func (rs *Runs) lastArmSeq(workflowID string) uint64 {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.bounds.arms[workflowID].seq
}

// refillDeadline rolls a bounded run's deadline forward on progress, reporting whether it stamped.
// A parked run is not refillable. Throttled at refillGranularity while the deadline is ahead, which
// also stops it moving earlier.
func (rs *Runs) refillDeadline(ctx context.Context, workflowID string) bool {
	return rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		if !l.Bounded() {
			return time.Time{}, false
		}
		// A stretch this process did not arm: treat it as starting now and claim none of the backstop.
		start, armed := rs.bounds.armedAt[workflowID]
		if !armed {
			start = now
		}
		next := runlease.NextDeadline(now, rs.runBoundsLocked(&l, start))
		if l.Deadline.After(now) && !next.After(l.Deadline.Add(refillGranularity)) {
			return time.Time{}, false
		}
		return next, true
	})
}

// RunMadeProgress rolls a run's idle window forward. Satisfies translate.runBoundsAccess. Called per
// tool-call frame, so the `bounded` pre-check keeps it one map read.
func (rs *Runs) RunMadeProgress(workflowID string) {
	if !rs.bounded(workflowID) {
		return
	}
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	rs.refillDeadline(ctx, workflowID)
}

// setTimerLocked installs a run's one deadline timer, stopping any earlier one (an unstopped AfterFunc
// stays pending). Caller holds mu.
func (rs *Runs) setTimerLocked(workflowID string, deadline time.Time) {
	if old := rs.bounds.timers[workflowID]; old != nil {
		old.Stop()
	}
	if rs.bounds.timers == nil {
		rs.bounds.timers = map[string]*time.Timer{}
	}
	// The deadline travels into the callback, which compares it with the lease at fire time.
	rs.bounds.timers[workflowID] = rs.lifecycle.afterUnlessDraining(time.Until(deadline),
		func() { rs.cancelExpired(workflowID, deadline) })
}

func (rs *Runs) stopTimer(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.stopTimerLocked(workflowID)
}

func (rs *Runs) stopTimerLocked(workflowID string) {
	t, ok := rs.bounds.timers[workflowID]
	if !ok {
		return
	}
	t.Stop()
	delete(rs.bounds.timers, workflowID)
}

// disarmDeadline parks a run: clears the lease's deadline and stops its timer, reporting whether it
// had one. A stale lease deadline would fool the step cap and the next re-arm.
func (rs *Runs) disarmDeadline(ctx context.Context, workflowID string) bool {
	parked := rs.disarmUnless(ctx, workflowID, nil)
	if parked {
		rs.quietEdge()
	}
	return parked
}

// quietEdge is a run that stopped executing: an engine restart pending on quiescence re-checks.
func (rs *Runs) quietEdge() {
	if rs.engine != nil {
		rs.engine.poke()
	}
}

// disarmUnless is disarmDeadline refused when keep, which runs under mu, says so: the decision and the
// park share one hold of mu, so an arm cannot land between them and be undone. Lock order as stampDeadline's.
func (rs *Runs) disarmUnless(ctx context.Context, workflowID string, keep func(l runlease.Lease) bool) bool {
	if workflowID == "" {
		return false
	}
	store := rs.leaseStore()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	l, ok := store.Get(workflowID)
	if !ok || !l.Bounded() {
		// A lease released by a terminal frame can still have a timer pending.
		rs.stopTimerLocked(workflowID)
		return false
	}
	if keep != nil && keep(l) {
		return false
	}
	// Bank the stretch before the park, the only moment it is knowable.
	rs.bankExecutedLocked(workflowID)
	if err := store.SetDeadline(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("could not park a run's deadline", "workflow_id", workflowID, "error", err)
	}
	rs.stopTimerLocked(workflowID)
	return true
}

// The delete keeps two parks from double-charging. Caller holds mu.
func (rs *Runs) bankExecutedLocked(workflowID string) {
	start, armed := rs.bounds.armedAt[workflowID]
	if !armed {
		return
	}
	if rs.bounds.executed == nil {
		rs.bounds.executed = map[string]time.Duration{}
	}
	rs.bounds.executed[workflowID] += time.Since(start)
	delete(rs.bounds.armedAt, workflowID)
}

// clearExecuted drops a run's backstop and arm accounting for good, so a reused workflow id starts fresh.
func (rs *Runs) clearExecuted(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.armedAt, workflowID)
	delete(rs.bounds.executed, workflowID)
	delete(rs.bounds.arms, workflowID)
}

// bounded reports whether marotte believes the run is executing under its deadline. Its readers must not clear it.
func (rs *Runs) bounded(workflowID string) bool {
	l, ok := rs.lease(workflowID)
	return ok && l.Bounded()
}

// claimTermination takes a run's single termination claim, true for the one caller that may end it:
// a user cancel, the schedule, or the clock. Otherwise a second recordEnd turns a deliberate stop into a timeout.
func (rs *Runs) claimTermination(workflowID string) bool {
	if workflowID == "" {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.claimLocked(workflowID)
}

// claimExpiredDeadline checks the deadline inside the claim: Timer.Stop does not halt a running func,
// so a stale callback could cancel a freshly resumed run. Lock order: bounds mutex, then lease store.
func (rs *Runs) claimExpiredDeadline(workflowID string, armedFor time.Time) bool {
	store := rs.leaseStore()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	l, ok := store.Get(workflowID)
	if !ok || !l.Deadline.Equal(armedFor) {
		return false
	}
	return rs.claimLocked(workflowID)
}

// claimLocked is the claim for a caller holding unattendedMu.
func (rs *Runs) claimLocked(workflowID string) bool {
	if _, taken := rs.bounds.terminating[workflowID]; taken {
		return false
	}
	if rs.bounds.terminating == nil {
		rs.bounds.terminating = map[string]struct{}{}
	}
	rs.bounds.terminating[workflowID] = struct{}{}
	return true
}

// releaseTermination hands back a claim whose cancel RPC failed, so Cancel still works next time.
func (rs *Runs) releaseTermination(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.terminating, workflowID)
}

// finishTermination is what the claim winner does, the only place a reason is recorded with its
// cancel; an empty reason is a user cancel. Nothing changes until the cancel lands: a refused one
// leaves the run bounded with no outcome. A landed cancel ends in releaseIfOver, here for every
// deliberate stop but the orphan sweep.
func (rs *Runs) finishTermination(
	ctx context.Context, workflowID, reason string, stop runStop,
) error {
	// Read before the cancel: the terminal frame it can cause may release the lease (forgetBounds) before the call returns.
	l, held := rs.lease(workflowID)
	var err error
	rs.stopSerialized(ctx, workflowID, func(sctx context.Context) bool {
		err = rs.cancelRPC(sctx, workflowID, stop)
		return err == nil
	})
	if err != nil {
		rs.releaseTermination(workflowID)
		rs.retryTermination(workflowID, reason, stop)
		return err
	}
	rs.disarmDeadline(ctx, workflowID)
	rs.recordEnd(workflowID, reason)
	if held {
		rs.noteRunEnd(ctx, &l, reason)
	}
	rs.clearCancelRetries(workflowID)
	rs.releaseIfOver(ctx, workflowID)
	return nil
}

var runEndNoteText = map[string]string{
	runEndStalled: "was stopped: no activity and no live shell for " +
		strconv.Itoa(int(runIdleWindow.Minutes())) + " minutes",
	runEndOverran: "was stopped: it ran past its time limit",
}

// runEndNoteID is the note's steer id in KAS's notify- shape (agent origin), distinct per stop.
func runEndNoteID(workflowID string, producedTs int64) string {
	return "notify-" + workflowID + ":stopped:" + strconv.FormatInt(producedTs, 10)
}

// noteRunEnd tells the launching chat of the run l leases why a bound stopped it, as an agent-origin
// steer: KAS's cancel carries no reason. Here, not on the terminal frame, which a cancel does not always produce.
func (rs *Runs) noteRunEnd(ctx context.Context, l *runlease.Lease, reason string) {
	text, known := runEndNoteText[reason]
	if !known || l.ChatID == "" || rs.coord == nil {
		return
	}
	workflowID := l.WorkflowID
	producedTs := time.Now().UnixMilli()
	rs.coord.recordSteer(ctx, marotte.ChatID(l.ChatID), runEndNoteID(workflowID, producedTs), &marotte.EntrySteer{
		Text:       cmp.Or(l.Recipe, "Workflow run") + " " + text,
		Origin:     marotte.SteerOriginAgent,
		State:      marotte.SteerStateRead,
		Severity:   "warning",
		OriginRun:  workflowID,
		ProducedTs: producedTs,
	})
}

// maxCancelRetries bounds refused-cancel retries between two pieces of progress (healProgress
// refills it). cancelOn and cancelBounded share the budget.
const maxCancelRetries = 3

// Nonzero because a cancel refused by an overstayed local claim (kiro-cli 2.31.0 takeLocalClaimTurn) succeeds only
// once that claim's time-bounded marker expires. Runs.cancelRetryBase falls back to it.
const defaultCancelRetryBase = 5 * time.Second

// claimCancelRetry takes one cancel retry, false once spent, returning the attempt number for the
// backoff. It bounds our retries, never the run's deadline.
func (rs *Runs) claimCancelRetry(workflowID string) (attempt int, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.cancelRetries == nil {
		rs.bounds.cancelRetries = map[string]int{}
	}
	if rs.bounds.cancelRetries[workflowID] >= maxCancelRetries {
		return rs.bounds.cancelRetries[workflowID], false
	}
	rs.bounds.cancelRetries[workflowID]++
	return rs.bounds.cancelRetries[workflowID], true
}

func (rs *Runs) clearCancelRetries(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.cancelRetries, workflowID)
}

// retryTermination schedules one retry of a cancel KAS refused: the error path keeps the deadline
// while the fired timer is spent. It re-reads before acting; a refusal lands back here.
func (rs *Runs) retryTermination(workflowID, reason string, stop runStop) {
	if workflowID == "" {
		return
	}
	attempt, ok := rs.claimCancelRetry(workflowID)
	if !ok {
		slog.Error(logMsgCancelUnretried, "workflow_id", workflowID,
			"attempts", attempt, "reason", reason)
		return
	}
	delay := cmp.Or(rs.cancelRetryBase, defaultCancelRetryBase) * time.Duration(1<<(attempt-1))
	slog.Warn("a run's cancel was refused; re-attempting it, and the run stays bounded",
		"workflow_id", workflowID, "attempt", attempt, "delay", delay)
	rs.lifecycle.afterUnlessDraining(delay, func() {
		// Parked or released: marotte no longer bounds this run.
		if !rs.bounded(workflowID) {
			return
		}
		if !rs.claimTermination(workflowID) {
			return
		}
		ctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		if err := rs.finishTermination(ctx, workflowID, reason, stop); err != nil {
			slog.Error("a re-attempted cancel was refused too",
				"workflow_id", workflowID, "error", err)
		}
	})
}

// forgetBounds drops a run's deadline, budgets, accounting, claim and lease once it stopped for good;
// the one site every origin reaches. The lease goes FIRST: it is the refill's door, so a late
// progress frame cannot file a timer nothing removes.
func (rs *Runs) forgetBounds(ctx context.Context, workflowID string) {
	rs.releaseLease(ctx, workflowID)
	rs.stopTimer(workflowID)
	rs.releaseTermination(workflowID)
	rs.clearHeals(workflowID)
	rs.clearCancelRetries(workflowID)
	rs.clearExecuted(workflowID)
	// The run's request-shaped asks live in the decision tracker; its step asks ended with the lease.
	rs.clearRunPerms(workflowID)
}

func (rs *Runs) clearRunPerms(workflowID string) {
	if rs.perms == nil {
		return
	}
	rs.perms.ClearPendingPermsForRun(workflowID)
}

// claimHeal takes one automatic-resume attempt, false once spent: a heal and a pause can drive each
// other while the network is down. Returns the attempt number for the backoff.
func (rs *Runs) claimHeal(workflowID string) (attempt int, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.heals == nil {
		rs.bounds.heals = map[string]int{}
	}
	if rs.bounds.heals[workflowID] >= maxAutoHeals {
		return rs.bounds.heals[workflowID], false
	}
	rs.bounds.heals[workflowID]++
	return rs.bounds.heals[workflowID], true
}

func (rs *Runs) clearHeals(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.heals, workflowID)
}

// clearEnd forgets a run's recorded termination so a re-driven run is bounded again and its row
// stops reading failed; the claim is dropped before the reason lookup, since a user cancel records none.
func (rs *Runs) clearEnd(workflowID string) {
	if workflowID == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.terminating, workflowID)
	// Above the early return: a run whose cancels all failed records no reason.
	delete(rs.bounds.cancelRetries, workflowID)
	if _, ok := rs.bounds.reasons[workflowID]; !ok {
		return
	}
	delete(rs.bounds.reasons, workflowID)
	// order is the map's eviction queue; a dangling entry lets the map outgrow its cap.
	rs.bounds.order = slices.DeleteFunc(rs.bounds.order,
		func(id string) bool { return id == workflowID })
}

// An already-hosted retry may still carry its old deadline; one after a terminal frame needs a new
// lease, named from KAS's run list for the single-run rule. The slot stays zero.
func (rs *Runs) rearmRetried(ctx context.Context, workflowID string, o *runOrigin) {
	rs.clearEnd(workflowID)
	rs.disarmDeadline(ctx, workflowID)
	if _, held := rs.lease(workflowID); !held {
		rs.grantRetryLease(ctx, workflowID, o)
	}
	rs.armDeadline(ctx, workflowID)
}

// recordEnd notes why a run was stopped, so its row can say so.
func (rs *Runs) recordEnd(workflowID, reason string) {
	if workflowID == "" || reason == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.reasons == nil {
		rs.bounds.reasons = map[string]string{}
	}
	if _, dup := rs.bounds.reasons[workflowID]; !dup {
		rs.bounds.order = append(rs.bounds.order, workflowID)
	}
	rs.bounds.reasons[workflowID] = reason
	for len(rs.bounds.order) > maxRunEndReasons {
		delete(rs.bounds.reasons, rs.bounds.order[0])
		rs.bounds.order = rs.bounds.order[1:]
	}
}

// endReason reports why a run was stopped, or "" for one that ended on its own terms or by user cancel.
func (rs *Runs) endReason(workflowID string) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.bounds.reasons[workflowID]
}

// cancelExpired stops a run whose deadline (armedFor) ran out and records why. The slot test is "not
// after the deadline" because the floor can push the deadline past SlotAt. Only the idle window
// consults idleVerdict.
func (rs *Runs) cancelExpired(workflowID string, armedFor time.Time) {
	l, held := rs.lease(workflowID)
	slotRanOut := held && !l.SlotAt.IsZero() && !l.SlotAt.After(armedFor)
	backstopSpent := rs.backstopSpent(workflowID)
	if held && !slotRanOut && !backstopSpent && rs.idleExpiryHandled(workflowID, armedFor) {
		return
	}
	if !rs.claimExpiredDeadline(workflowID, armedFor) {
		return
	}
	reason := runEndOverran
	switch {
	case slotRanOut && l.ScheduleID != "":
		slog.Error(logMsgRunOverran, "workflow_id", workflowID, "schedule_id", l.ScheduleID,
			"slot_at", l.SlotAt, "armed_for", armedFor)
	case slotRanOut:
		slog.Info(logMsgRunYieldedToSlot, "workflow_id", workflowID, "recipe", l.Recipe,
			"slot_at", l.SlotAt)
	case backstopSpent:
		slog.Error(logMsgRunBackstop, "workflow_id", workflowID,
			"backstop", runBackstop.String(), "recipe", l.Recipe)
	default:
		reason = runEndStalled
		slog.Error(logMsgRunStalled, "workflow_id", workflowID,
			"idle_window", runIdleWindow.String(), "recipe", l.Recipe)
	}
	rs.cancelBounded(workflowID, reason)
	if !slotRanOut {
		return
	}
	// Surface it on the row, or the schedule reads "started" while producing nothing.
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	rs.recordScheduleOutcome(ctx, l.ScheduleID, schedule.Outcome{Status: schedule.StatusFailed, Reason: reasonOverran})
}

// backstopSpent reports whether the absolute budget is gone now, counting the open stretch: the
// backstop fires mid-stretch, so `executed` alone reads every expiry as a stall.
func (rs *Runs) backstopSpent(workflowID string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	spent := rs.bounds.executed[workflowID]
	if start, armed := rs.bounds.armedAt[workflowID]; armed {
		spent += time.Since(start)
	}
	return spent >= runBackstop
}

// idleExpiryHandled applies idleVerdict to an expired idle window, true when it refilled or parked the
// run; false leaves cancelExpired to stop it as stalled. A park refused for an arm since the verdict
// refills instead: the timer that fired was the run's only one.
func (rs *Runs) idleExpiryHandled(workflowID string, armedFor time.Time) bool {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	seq := rs.lastArmSeq(workflowID)
	switch rs.idleVerdict(ctx, workflowID) {
	case idleWorking:
		return rs.refillDeadline(ctx, workflowID)
	case idleParked:
		if !rs.parkExpired(ctx, workflowID, armedFor, seq) {
			rs.refillDeadline(ctx, workflowID)
		}
		return true
	case idleStalled:
	}
	return false
}

// idleVerdict is what an expired idle window means for a run.
type idleVerdict int

const (
	idleStalled idleVerdict = iota
	idleWorking
	idleParked
)

// idleVerdict reads an expired idle window. An open step turn is working: KAS has the step in flight
// and owns in-turn liveness (stream idle watchdog, stall retries), and neither a KAS-side tool nor a
// model reasoning silently sends a frame; the backstop bounds a runaway. A run its chat resumes that KAS reports
// paused is parked: KAS reconciles a dead owner's run to paused on its next read and emits no frame, so its pause
// never reached observePaused.
// Anything else, an unreadable run and a parentless orphan included, is stalled.
func (rs *Runs) idleVerdict(ctx context.Context, workflowID string) idleVerdict {
	if rs.log != nil && rs.log.hasOpen(workflowID) {
		return idleWorking
	}
	if !rs.chatResumes(workflowID) {
		return idleStalled
	}
	if rs.kasPaused(ctx, workflowID) {
		return idleParked
	}
	return idleStalled
}

// chatResumes reports whether the run's launching chat resumes it once its owner died
// (resumeInterruptedRuns): an agent-launched run. Nothing resumes a parentless one, which the orphan
// sweep clears instead (run_orphan.go).
func (rs *Runs) chatResumes(workflowID string) bool {
	l, held := rs.lease(workflowID)
	return held && l.Origin == runlease.OriginAgent
}

// kasPaused reports whether KAS's own read says the run is paused; false on any RPC or decode failure.
func (rs *Runs) kasPaused(ctx context.Context, workflowID string) bool {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return false
	}
	var res inspectRunState
	if !unmarshalKeepingReadable(raw, &res) {
		return false
	}
	return res.WorkflowID == workflowID && res.State.Status == marotte.RunStatusPaused
}

// parkExpired disarms a run the idle verdict found parked, reporting whether it did. It refuses when
// the deadline moved since armedFor or an arm landed after seq: a resume in between owns the run's
// clock. A restart pause is resumed by its chat's return (resumeInterruptedRuns).
func (rs *Runs) parkExpired(ctx context.Context, workflowID string, armedFor time.Time, seq uint64) bool {
	resumed := func(l runlease.Lease) bool {
		return !l.Deadline.Equal(armedFor) || rs.bounds.arms[workflowID].seq != seq
	}
	if !rs.disarmUnless(ctx, workflowID, resumed) {
		return false
	}
	rs.quietEdge()
	slog.Info("a run's idle window ran out while KAS held it paused; "+
		"holding it for a resume instead of stopping it", "workflow_id", workflowID)
	return true
}

// cancelBounded issues cancelExpired's cancel for a caller already holding the claim; the public Cancel
// would refuse it. A failure goes to finishTermination's ladder.
func (rs *Runs) cancelBounded(workflowID, reason string) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if err := rs.finishTermination(ctx, workflowID, reason, runStop{}); err != nil {
		slog.Error("could not cancel a run that breached its bound",
			"workflow_id", workflowID, "error", err)
	}
}

// runStartLaunch classifies a lease minted from `run_start` by the lane it arrived on: a real chat id is
// agent-launched (the resume sweep owns it, not the orphan cancel); anything else is parentless and
// sweepable. Lease absence cannot decide: a retry's first frame can beat its lease grant.
func runStartLaunch(chatID marotte.ChatID) launchOrigin {
	if chatID == "" || isRunChat(chatID) {
		return launchOrigin{origin: runlease.OriginManual}
	}
	return launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}
}

// `run_start` is the first sight of an agent-launched run, so its lease is minted here; it also
// re-arms a resumed run.
func (rs *Runs) observeStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" {
		if _, held := rs.lease(f.WorkflowID); !held {
			rs.grantLease(ctx, f.WorkflowID, f.WorkflowName, runStartLaunch(chatID), nil)
		}
		rs.recordParentSession(ctx, f.WorkflowID, f.ParentSessionID)
		rs.armDeadline(ctx, f.WorkflowID)
	}
	rs.translate.HandleRunStart(ctx, chatID, msg)
}

// observeComplete drops a terminal run's bounds and step registry, closing open step turns, then
// translates. A non-terminal run_complete (an onMaxIterations pause) keeps the arm.
func (rs *Runs) observeComplete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" && f.Status.Terminal() {
		// Before HandleRunComplete: the client repaints on its `run_finished` invalidation.
		rs.closeRun(ctx, f.WorkflowID, string(f.Status))
		rs.recordRunNotice(chatID, f.WorkflowID)
		// Before forgetBounds, which releases the lease the label and the parent come from.
		rs.notifyRunOutcome(ctx, chatID, &f)
		rs.forgetBounds(ctx, f.WorkflowID)
		rs.translate.ForgetRunSteps(f.WorkflowID)
		if rs.runEnded != nil {
			defer rs.runEnded()
		}
	} else if f.WorkflowID != "" && f.Status == marotte.RunStatusPaused {
		rs.notifyRunOutcome(ctx, chatID, &f)
	}
	rs.translate.HandleRunComplete(ctx, chatID, msg)
}

// notifyRunOutcome notifies a run's `run_complete`: the only channel to a reader off the page, for
// every origin. notice.RunEnded decides which statuses say anything. The nil guard covers a bare &Runs{}.
func (rs *Runs) notifyRunOutcome(ctx context.Context, chatID marotte.ChatID, f *lifecycleFrame) {
	if rs.coord == nil {
		return
	}
	var parent string
	if p := rs.parentChat(chatID, f.WorkflowID); p != "" {
		parent = rs.coord.NoticeTarget(ctx, p, "").Name
	}
	n, ok := notice.RunEnded(notice.RunTarget(f.WorkflowID, rs.frameLabel(f)), f.Status, parent,
		f.FinalState.StopReason, f.FinalState.Pause.Kind)
	if !ok {
		return
	}
	rs.coord.Notify(ctx, chatID, &n)
}

// parentChat is the chat whose agent launched the run, "" for a parentless run: the lease's
// record, else the frame's door when that is a real chat.
func (rs *Runs) parentChat(chatID marotte.ChatID, workflowID string) marotte.ChatID {
	if l, held := rs.lease(workflowID); held {
		return marotte.ChatID(l.ChatID)
	}
	if chatID == "" || isRunChat(chatID) {
		return ""
	}
	return chatID
}

// frameLabel names the run a lifecycle frame is about, as its tab does.
func (rs *Runs) frameLabel(f *lifecycleFrame) string {
	return cmp.Or(f.FinalState.RunLabel, f.FinalState.WorkflowName, f.WorkflowName, rs.tabLabel(f.WorkflowID))
}

// tabLabel is the name a run's tab shows: the label the launch sent KAS, else its recipe.
// run_complete's top-level name is empty, so a terminal run is named from its lease.
func (rs *Runs) tabLabel(workflowID string) string {
	l, ok := rs.lease(workflowID)
	if !ok {
		return ""
	}
	return cmp.Or(launchOrigin{origin: l.Origin}.runLabel(l.Recipe), l.Recipe)
}

// A node pause is a step waiting inside a running run.
func (rs *Runs) observePaused(next func(context.Context, marotte.ChatID, *marotte.RPCResponse)) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		rs.disarmDeadline(ctx, workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}

// lifecycleFrame is the fields the bounds and the run's notification read off a lifecycle frame,
// decoded separately from translate's KAS contract. WorkflowName is on `run_start`, FinalState
// on `run_complete`.
type lifecycleFrame struct {
	WorkflowID      string            `json:"workflowId"`
	WorkflowName    string            `json:"workflowName"`
	ParentSessionID string            `json:"parentSessionId"`
	Status          marotte.RunStatus `json:"status"`
	FinalState      struct {
		WorkflowName string `json:"workflowName"`
		RunLabel     string `json:"runLabel"`
		StopReason   string `json:"stopReason"`
		Pause        struct {
			Kind string `json:"kind"`
		} `json:"pause"`
	} `json:"finalState"`
}

func decodeLifecycleFrame(msg *marotte.RPCResponse) lifecycleFrame {
	var f lifecycleFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return lifecycleFrame{}
	}
	return f
}

func workflowIDOfFrame(msg *marotte.RPCResponse) string {
	return decodeLifecycleFrame(msg).WorkflowID
}
