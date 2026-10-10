package agent

// Restart orphans: runs marotte launched whose owning process died. The boot sweep asks "is this system
// idle", the admission backstop "may this run start". No automatic relaunch. A run absent from a good
// list starts a persisted clock; six continuous hours releases the lease as bookkeeping.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/workflow"
)

// orphanSweepBudget bounds the boot sweep: sequential `inspect`s at 45s each could hold boot for minutes.
const orphanSweepBudget = 2 * time.Minute

const leaseAbsenceBudget = 6 * time.Hour

const reasonLeaseAbsent = "no terminal signal was seen and the run stayed absent for 6 hours"

// reasonOrphaned is the schedule row's reason for a run swept after its process died; the next slot is the recovery.
const reasonOrphaned = "stopped because the server restarted while it was running. Run it again, or wait for the next slot"

// SweepOrphaned clears every lease whose run a dead process left paused. Best-effort: skipping costs a stale
// row, cancelling a live run destroys work. Reports whether it reached KAS.
func (rs *Runs) SweepOrphaned(ctx context.Context) (reached bool) {
	if len(rs.leaseStore().List()) == 0 {
		// No leases: nothing to ask.
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, orphanSweepBudget)
	defer cancel()

	runs, err := rs.listRaw(cctx)
	if err != nil {
		// Leave every lease; the admission backstop is the second chance.
		slog.Warn("boot: run list unavailable, skipping the orphan sweep", "error", err)
		return false
	}
	status := make(map[string]marotte.RunStatus, len(runs))
	for i := range runs {
		status[runs[i].WorkflowID] = marotte.RunStatus(runs[i].Status)
	}
	rs.reconcileLeasePresence(cctx, status, time.Now(), true)

	held := rs.leaseStore().List()
	for i := range held {
		l := &held[i]
		st, known := status[l.WorkflowID]
		if !known {
			// Absence starts a clock and clears nothing.
			continue
		}
		switch {
		case l.Origin == runlease.OriginAgent:
			// KAS parents an agent's run on its chat, so it heals with that chat.
		case st == marotte.RunStatusPaused && rs.restartPaused(cctx, l.WorkflowID):
			rs.clearOrphaned(cctx, l)
		}
	}
	return true
}

// releaseIfOver releases a run's lease when KAS says it is over, for a stop with no confirmation. Unknown
// needs no check, since a landed cancel proved KAS resolved the id; an unreadable run is left; a running run is a no-op.
func (rs *Runs) releaseIfOver(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	if _, held := rs.lease(workflowID); !held {
		// Already released.
		return
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok || res.WorkflowID != workflowID || !res.State.Status.Terminal() {
		return
	}
	slog.Info("releasing the lease of a run that stopped without a terminal frame",
		"workflow_id", workflowID, "status", res.State.Status)
	rs.releaseLease(ctx, workflowID)
}

// reconcileLeasePresence settles every lease against one run list: terminal releases, listed resets the
// absence clock, absent starts or spends it. Absence never cancels. Boot passes inspectAbsent.
func (rs *Runs) reconcileLeasePresence(ctx context.Context, status map[string]marotte.RunStatus, now time.Time, inspectAbsent bool) {
	held := rs.leaseStore().List()
	for i := range held {
		l := &held[i]
		st, known := status[l.WorkflowID]
		if known {
			if st.Terminal() {
				slog.Info("releasing the lease of a run with terminal status",
					"workflow_id", l.WorkflowID, "recipe", l.Recipe, "status", st)
				rs.releaseLease(ctx, l.WorkflowID)
				continue
			}
			if !l.FirstAbsentAt.IsZero() {
				rs.setFirstAbsentAt(ctx, l.WorkflowID, time.Time{})
			}
			continue
		}

		if inspectAbsent && rs.inspectConfirmsGone(ctx, l.WorkflowID) {
			slog.Info("boot: releasing the lease of a run inspect confirmed is over",
				"workflow_id", l.WorkflowID, "recipe", l.Recipe)
			rs.releaseLease(ctx, l.WorkflowID)
			continue
		}
		rs.observeAbsentLease(ctx, l, now)
	}
}

// inspectConfirmsGone asks KAS about one absent run; any failure but workflowNotFound leaves the lease.
func (rs *Runs) inspectConfirmsGone(ctx context.Context, workflowID string) bool {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return workflowNotFound(err)
	}
	var res inspectRunState
	if json.Unmarshal(raw, &res) != nil {
		return false
	}
	return res.WorkflowID == workflowID && res.State.Status.Terminal()
}

// workflowNotFound matches KAS's unknown-workflow refusal by text: it is an untyped throw.
func workflowNotFound(err error) bool {
	return strings.Contains(strings.ToLower(rpcerr.Details(err)), "workflow not found")
}

// Only leaseAbsenceBudget of unbroken absence releases, and the row says the outcome is unknown.
func (rs *Runs) observeAbsentLease(ctx context.Context, l *runlease.Lease, now time.Time) {
	if l.FirstAbsentAt.IsZero() {
		rs.setFirstAbsentAt(ctx, l.WorkflowID, now)
		return
	}
	if now.Sub(l.FirstAbsentAt) < leaseAbsenceBudget {
		return
	}

	slog.Warn("run stayed absent past the lease budget; no terminal signal was ever seen",
		"workflow_id", l.WorkflowID, "recipe", l.Recipe, "first_absent_at", l.FirstAbsentAt)
	rs.recordScheduleOutcome(ctx, l.ScheduleID, schedule.Outcome{Status: schedule.StatusUnknown, Reason: reasonLeaseAbsent})
	rs.releaseLease(ctx, l.WorkflowID)
}

func (rs *Runs) setFirstAbsentAt(ctx context.Context, workflowID string, at time.Time) {
	if err := rs.leaseStore().SetFirstAbsentAt(ctx, workflowID, at); err != nil {
		slog.Warn("run lease absence clock not persisted",
			"workflow_id", workflowID, "error", err)
	}
}

// clearBlockingOrphan is the admission backstop: clear a blocking row if it is marotte's own orphan.
// Reads KAS's list, which also sees runs marotte did not launch.
func (rs *Runs) clearBlockingOrphan(ctx context.Context, workflowID string, status marotte.RunStatus) bool {
	if status != marotte.RunStatusPaused {
		// A running row is not an orphan.
		return false
	}
	l, held := rs.lease(workflowID)
	if !held || l.Origin == runlease.OriginAgent {
		// No lease means the TUI's; an agent's run belongs to its chat.
		return false
	}
	if !rs.restartPaused(ctx, workflowID) {
		return false
	}
	return rs.clearOrphaned(ctx, &l)
}

// clearOrphaned is the shared release: claim, re-confirm, cancel, then record and release, as
// finishTermination orders it. The check-to-cancel window is accepted; do not widen it or drop the
// auto-cancel without a compare-and-cancel.
func (rs *Runs) clearOrphaned(ctx context.Context, l *runlease.Lease) bool {
	if !rs.claimTermination(l.WorkflowID) {
		// Already ending; the winner releases.
		return false
	}
	if !rs.restartPaused(ctx, l.WorkflowID) {
		// No longer an orphan: hand the claim back.
		rs.releaseTermination(l.WorkflowID)
		slog.Info("a run stopped reading as restart-orphaned before its cancel; leaving it alone",
			"workflow_id", l.WorkflowID, "recipe", l.Recipe)
		return false
	}
	rs.disarmDeadline(ctx, l.WorkflowID)
	if err := rs.cancelRPC(ctx, l.WorkflowID, runStop{}, nil); err != nil {
		rs.releaseTermination(l.WorkflowID)
		slog.Error("could not cancel a restart-orphaned run; its recipe stays busy until the next try",
			"workflow_id", l.WorkflowID, "error", err)
		return false
	}
	rs.recordEnd(l.WorkflowID, runEndOrphaned)
	// ERROR: an alert rule keys on logMsgRunOrphaned.
	slog.Error(logMsgRunOrphaned, "workflow_id", l.WorkflowID, "recipe", l.Recipe,
		"origin", string(l.Origin), "schedule_id", l.ScheduleID)
	// The schedule's row too, or every later slot is refused as an overlap.
	rs.recordScheduleOutcome(ctx, l.ScheduleID, schedule.Outcome{Status: schedule.StatusFailed, Reason: reasonOrphaned})
	rs.releaseLease(ctx, l.WorkflowID)
	return true
}

// restartPaused reports whether KAS says a run's owning process died: a byte-equal reason (never
// `pauseDetail`, unlike involuntarilyPaused, since this cancels), status and identity off the same
// reply, and false on any RPC failure.
func (rs *Runs) restartPaused(ctx context.Context, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok {
		return false
	}
	return res.WorkflowID == workflowID &&
		res.State.Status == marotte.RunStatusPaused &&
		res.State.PauseReason == stalePauseReason &&
		!res.userParked()
}

// involuntarilyPaused reports whether marotte should resume a pause unasked: restartPaused with the wider
// predicate (resumablePause); false on any RPC failure.
func (rs *Runs) involuntarilyPaused(ctx context.Context, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok {
		return false
	}
	return res.WorkflowID == workflowID &&
		res.State.Status == marotte.RunStatusPaused &&
		resumablePause(res.State.PauseReason, res.State.PauseDetail) &&
		!res.userParked()
}

// inspectRunState is what the pause predicates read off `_kiro/workflow/inspect`, detail included, so this
// guard sees what pauseFrame's gate sees.
type inspectRunState struct {
	State struct {
		PauseDetail  *pauseDetail `json:"pauseDetail"`
		PausePending *struct {
			Initiator string `json:"initiator"`
		} `json:"pausePending"`
		Status        marotte.RunStatus `json:"status"`
		PauseReason   string            `json:"pauseReason"`
		StopInitiator string            `json:"stopInitiator"`
	} `json:"state"`
	WorkflowID string `json:"workflowId"`
}

// userParked mirrors KAS's test for a user pause, pending or landed; neither the sweep nor a heal may touch one.
func (r *inspectRunState) userParked() bool {
	st := &r.State
	return (st.PausePending != nil && st.PausePending.Initiator == initiatorUser) ||
		(st.Status == marotte.RunStatusPaused && st.StopInitiator == initiatorUser)
}

// inspect reads one run's inspect reply, false when it cannot be read or decoded at all.
func (rs *Runs) inspect(ctx context.Context, workflowID string) (inspectRunState, bool) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a paused run's state, so it is left alone",
			"workflow_id", workflowID, "error", err)
		return inspectRunState{}, false
	}
	var res inspectRunState
	if !unmarshalKeepingReadable(raw, &res) {
		return inspectRunState{}, false
	}
	return res, true
}

// rawInspect issues `_kiro/workflow/inspect` and types its failure, so callers use errors.Is. Every
// successful read seeds the step-session registry before any caller acts on it: after a restart it is
// the only attribution a step's unmarked frames get, and a caller may send to a step from this read.
func (rs *Runs) rawInspect(ctx context.Context, workflowID string) (json.RawMessage, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow inspect call", methodKiroWorkflowInspect,
		callerParams(map[string]any{keyWorkflowID: workflowID}))
	if err != nil {
		return nil, workflow.Classify(err)
	}
	rs.translate.RecordRunSteps(raw)
	return raw, nil
}
