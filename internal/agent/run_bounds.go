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
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// runIdleWindow is the primary, stall bound: progress (a node completing, a tool call starting) rolls
// it forward, and at expiry it yields to a live shell or an unanswered decision on the run's own
// open steps. Longer than KAS's 300s stream idle timeout, which retries silently.
const runIdleWindow = 15 * time.Minute

// runBackstop is the absolute executing-time budget, for a runaway loop that refills the idle window
// forever. 36 hours is observed (recipes have run for eight). A constant: a raisable backstop is none.
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

// logMsgRunOrphaned is a constant because an external alert rule keys on the message.
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

// logMsgCancelUnretried is logged when every cancel attempt failed; it claims nothing about the run,
// since an unknown workflow id also lands here.
const logMsgCancelUnretried = "a run's cancel failed on every attempt; " +
	"marotte has stopped trying to stop it"

// maxRunEndReasons bounds the termination map. Records outlive the run (History reads them), so
// the oldest goes first rather than clearing on the terminal frame.
const maxRunEndReasons = 256

// runBoundsState holds the live timers, the termination claim and the recorded reasons, in memory;
// the deadline lives on the durable lease. Field order is govet's fieldalignment.
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
	// reasons maps a workflow id to why it stopped; order is its FIFO eviction queue.
	reasons map[string]string
	order   []string
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

// armDeadline gives a run a fresh budget and its timer, opening the executing stretch. Idempotent:
// `run_start` re-fires on resume, so the earliest arm wins.
func (rs *Runs) armDeadline(ctx context.Context, workflowID string) {
	rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
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

// RunMadeProgress rolls a run's idle window forward. Satisfies translate.RunBoundsAccess. Called per
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
	rs.bounds.timers[workflowID] = time.AfterFunc(time.Until(deadline),
		func() { rs.cancelExpired(workflowID, deadline) })
}

// stopTimer stops and forgets a run's timer.
func (rs *Runs) stopTimer(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
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
	if workflowID == "" {
		return false
	}
	l, ok := rs.lease(workflowID)
	if !ok || !l.Bounded() {
		// A lease released by a terminal frame can still have a timer pending.
		rs.stopTimer(workflowID)
		return false
	}
	// Bank the stretch before the park, the only moment it is knowable.
	rs.bankExecuted(workflowID)
	if err := rs.leaseStore().SetDeadline(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("could not park a run's deadline", "workflow_id", workflowID, "error", err)
	}
	rs.stopTimer(workflowID)
	return true
}

// bankExecuted adds the current stretch to the total and closes it. The delete keeps two parks from double-charging.
func (rs *Runs) bankExecuted(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
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

// clearExecuted drops a run's backstop accounting for good, so a reused workflow id starts fresh.
func (rs *Runs) clearExecuted(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.armedAt, workflowID)
	delete(rs.bounds.executed, workflowID)
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
	ctx context.Context, workflowID, reason string, stop runStop, carrier *sharedBridge,
) error {
	if err := rs.cancelRPC(ctx, workflowID, stop, carrier); err != nil {
		rs.releaseTermination(workflowID)
		rs.retryTermination(workflowID, reason, stop)
		return err
	}
	rs.disarmDeadline(ctx, workflowID)
	rs.recordEnd(workflowID, reason)
	rs.noteRunEnd(ctx, workflowID, reason)
	rs.clearCancelRetries(workflowID)
	rs.releaseIfOver(ctx, workflowID)
	return nil
}

// runEndNoteText is the sentence a bound leaves in the launching chat, one per reason.
var runEndNoteText = map[string]string{
	runEndStalled: "was stopped: no activity and no live shell for " +
		strconv.Itoa(int(runIdleWindow.Minutes())) + " minutes",
	runEndOverran: "was stopped: it ran past its time limit",
}

// runEndNoteID is the note's steer id in KAS's notify- shape (agent origin), distinct per stop.
func runEndNoteID(workflowID string, producedTs int64) string {
	return "notify-" + workflowID + ":stopped:" + strconv.FormatInt(producedTs, 10)
}

// noteRunEnd tells the launching chat why a bound stopped its run, as an agent-origin steer: KAS's
// cancel carries no reason. Here because a cancel landing in KAS's live registry emits no terminal frame.
func (rs *Runs) noteRunEnd(ctx context.Context, workflowID, reason string) {
	text, known := runEndNoteText[reason]
	l, held := rs.lease(workflowID)
	if !known || !held || l.ChatID == "" || rs.coord == nil {
		return
	}
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

// defaultCancelRetryBase is the first retry delay, doubling (5s, 10s, 20s); nonzero because another
// process owns the run. Runs.cancelRetryBase falls back to it.
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

// clearCancelRetries refills the retry budget on a landed cancel, on progress, and at a final stop.
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
	// Untracked: it re-issues a stop already earned, and the guards below re-read.
	time.AfterFunc(delay, func() {
		// Parked or released: marotte no longer bounds this run.
		if !rs.bounded(workflowID) {
			return
		}
		if !rs.claimTermination(workflowID) {
			return
		}
		ctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		// nil carrier: any hint is a ladder delay old, so re-resolve.
		if err := rs.finishTermination(ctx, workflowID, reason, stop, nil); err != nil {
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
	// A finished run waits on nobody; announce rather than drop, or a dead card hides the chat's later asks.
	rs.settleAsksForRun(ctx, workflowID)
	// The run's request-shaped asks live in the decision tracker; silent, since SettledByMoot is for run asks.
	rs.clearRunPerms(workflowID)
}

// clearRunPerms drops a run's unanswered request-shaped decisions, tolerating a bare &Runs{}.
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

// clearHeals gives a run its full heal budget back. Called when a node
// completes and when the run ends.
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

// rearmRetried gives a re-driven run a clean row and a fresh budget. An already-hosted retry may
// still carry its old deadline; one after a terminal frame needs a new lease, named from KAS's run
// list for the single-run rule. The slot stays zero.
func (rs *Runs) rearmRetried(ctx context.Context, workflowID, recipe string) {
	rs.clearEnd(workflowID)
	rs.disarmDeadline(ctx, workflowID)
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(ctx, workflowID, recipe, manualLaunch())
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
// yields to a working or waiting step.
func (rs *Runs) cancelExpired(workflowID string, armedFor time.Time) {
	l, held := rs.lease(workflowID)
	slotRanOut := held && !l.SlotAt.IsZero() && !l.SlotAt.After(armedFor)
	backstopSpent := rs.backstopSpent(workflowID)
	if held && !slotRanOut && !backstopSpent &&
		(rs.stepWorking(l.ChatID, workflowID) || rs.awaitingAnswer(workflowID)) {
		ctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		if rs.refillDeadline(ctx, workflowID) {
			return
		}
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

// stepWorking reports whether one of the run's own open steps waits on a live agent terminal, on the
// carrier (the launching chat, else the run's synthetic chat). Scoped to the step's sessions: a
// parallel run's steps share one carrier. Every absence answers false, so the bound applies.
func (rs *Runs) stepWorking(leaseChatID, workflowID string) bool {
	if rs.terminals == nil || rs.log == nil {
		return false
	}
	sessions := rs.log.OpenSessions(workflowID)
	if len(sessions) == 0 {
		return false
	}
	carrier := marotte.ChatID(leaseChatID)
	if carrier == "" {
		carrier = runChatID(workflowID)
	}
	return rs.terminals.LiveTerminalForSession(carrier, sessions)
}

// awaitingAnswer reports whether one of the run's own open steps has an unanswered decision. Scoped to
// open steps: the engine abandons a finished step's ask without withdrawing it. Absence answers false.
func (rs *Runs) awaitingAnswer(workflowID string) bool {
	if rs.perms == nil || rs.log == nil {
		return false
	}
	asked := rs.perms.PendingDecisionNodesForRun(workflowID)
	if len(asked) == 0 {
		return false
	}
	for node := range rs.log.OpenNodeIDs(workflowID) {
		if _, ok := asked[node]; ok {
			return true
		}
	}
	return false
}

// cancelBounded issues cancelExpired's cancel for a caller already holding the claim; the public Cancel
// would refuse it. A failure goes to finishTermination's ladder.
func (rs *Runs) cancelBounded(workflowID, reason string) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if err := rs.finishTermination(ctx, workflowID, reason, runStop{}, nil); err != nil {
		slog.Error("could not cancel a run that breached its bound",
			"workflow_id", workflowID, "error", err)
	}
}

// runStartLaunch classifies a lease minted from `run_start` by its carrier: a real chat id is
// agent-launched (the resume sweep owns it, not the orphan cancel); anything else is parentless and
// sweepable. Lease absence cannot decide: a retry's first frame can beat its lease grant.
func runStartLaunch(chatID marotte.ChatID) launchOrigin {
	if chatID == "" || isRunChat(chatID) {
		return launchOrigin{origin: runlease.OriginManual}
	}
	return launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}
}

// observeStart arms the deadline, then translates. `run_start` is the first sight of an
// agent-launched run, so its lease is minted here; it also re-arms a resumed run.
func (rs *Runs) observeStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" {
		if _, held := rs.lease(f.WorkflowID); !held {
			rs.grantLease(ctx, f.WorkflowID, f.WorkflowName, runStartLaunch(chatID))
		}
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
		// Before forgetBounds, which releases the lease the label comes from.
		rs.notifyRunOutcome(ctx, f)
		rs.forgetBounds(ctx, f.WorkflowID)
		rs.translate.ForgetRunSteps(f.WorkflowID)
		if rs.runEnded != nil {
			defer rs.runEnded()
		}
	}
	rs.translate.HandleRunComplete(ctx, chatID, msg)
}

// notifyRunOutcome pushes a terminal run's verdict: the only channel to a reader off the page, for
// every origin. The nil guard covers a bare &Runs{}.
func (rs *Runs) notifyRunOutcome(ctx context.Context, f lifecycleFrame) {
	if rs.coord == nil {
		return
	}
	rs.coord.NotifyPushSubject(ctx,
		runOutcomeBody(f.Status, rs.runOutcomeLabel(f)),
		marotte.PushKindRunOutcome,
		marotte.RunSubject(f.WorkflowID))
}

// runOutcomeLabel names the run. run_complete's top-level name is empty (it sits at
// finalState.workflowName), so the lease's recipe is read instead.
func (rs *Runs) runOutcomeLabel(f lifecycleFrame) string {
	var recipe string
	if l, held := rs.lease(f.WorkflowID); held {
		recipe = l.Recipe
	}
	return cmp.Or(f.WorkflowName, recipe, "Workflow run")
}

// runOutcomeBody mirrors static-src/handlers/run.ts toastCompletion verbatim. Total over the four
// terminal statuses; the live two get the generic.
func runOutcomeBody(status marotte.RunStatus, label string) string {
	switch status {
	case marotte.RunStatusCompleted:
		return label + " finished"
	case marotte.RunStatusFailed:
		return label + " failed"
	case marotte.RunStatusAborted:
		return label + " was aborted"
	case marotte.RunStatusCancelled:
		return label + " was cancelled"
	case marotte.RunStatusRunning, marotte.RunStatusPaused:
		return label + " finished"
	}
	return label + " finished"
}

// observePaused parks the deadline of a run-level `paused`, then translates; a node pause is a step waiting inside a running run.
func (rs *Runs) observePaused(next func(context.Context, marotte.ChatID, *marotte.RPCResponse)) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		rs.disarmDeadline(ctx, workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}

// lifecycleFrame is the three fields the bounds read off a lifecycle frame, decoded separately from
// translate's KAS contract. WorkflowName is on `run_start`.
type lifecycleFrame struct {
	WorkflowID   string            `json:"workflowId"`
	WorkflowName string            `json:"workflowName"`
	Status       marotte.RunStatus `json:"status"`
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
