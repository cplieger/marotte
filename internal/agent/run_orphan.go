package agent

// Restart orphans: runs marotte launched whose owning process died. The boot sweep asks "is this system
// idle", the admission backstop "may this run start". No automatic relaunch. A run absent from a good
// list starts an in-memory clock; six continuous hours reports it and ends its asks, and releases nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
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

// sweepOrphaned clears every lease whose run a dead process left paused. Best-effort: skipping costs a stale
// row, cancelling a live run destroys work. Reports whether it reached KAS.
func (rs *Runs) sweepOrphaned(ctx context.Context) (reached bool) {
	before := rs.leaseStore().List()
	if len(before) == 0 {
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
	rs.reconcileLeasePresence(cctx, before, status, time.Now(), true)

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

// BootSweep is the boot pass: the finder of the sign-in gate's pauses, which retries on its own, the orphan sweep,
// then one read of every leased run's asks. Reports whether the sweep reached KAS; when it did not, nothing was read.
// Its loop outlives Shutdown, so the pass is held on inflight and none runs once draining.
func (rs *Runs) BootSweep(ctx context.Context) (reached bool) {
	release, ok := rs.lifecycle.enterUnlessDraining()
	if !ok {
		return false
	}
	defer release()
	rs.findHoldsAsync()
	reached = rs.sweepOrphaned(ctx)
	// After the sweep, which releases the orphans; a clock needs no answer from KAS.
	rs.watchParkedSlots()
	leases := rs.leaseStore().List()
	for i := range leases {
		rs.watchUnattendedPause(leases[i].WorkflowID)
	}
	if !reached {
		return false
	}
	rs.askBootPass(ctx)
	return true
}

// askBootPass reads each leased run through the engine, one at a time. A chat-parented run's chat session is made
// resident for it, which resumes that chat's restart-paused runs now rather than at the user's return.
func (rs *Runs) askBootPass(ctx context.Context) {
	leases := rs.leaseStore().List()
	if len(leases) == 0 {
		return
	}
	started := time.Now()
	read := 0
	for i := range leases {
		id := leases[i].WorkflowID
		c := rs.cellFor(id)
		if c == nil {
			continue
		}
		if rs.absence.notFound(id) {
			// KAS has no such run: no asks to read, and no reason to make its chat resident.
			continue
		}
		if leases[i].Origin == runlease.OriginAgent {
			if _, err := rs.ensureResident(ctx, id); err != nil {
				slog.Warn("boot: a chat-parented run's chat could not be reopened", "workflow_id", id, "error", err)
			}
		}
		if _, err := rs.readAsks(ctx, c); err != nil {
			slog.Warn("boot: a leased run could not be read", "workflow_id", id, "error", err)
			continue
		}
		read++
	}
	slog.Info("boot: read every leased run's questions", "leases", len(leases), "read", read,
		"took", time.Since(started).Round(time.Millisecond))
}

// releaseIfOver releases a run's lease when KAS says it is over, for a stop with no confirmation. Unknown
// needs no check, since a landed cancel proved KAS resolved the id; an unreadable run is left; a running run is a no-op.
func (rs *Runs) releaseIfOver(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	life := rs.lifeOf(workflowID)
	if !life.held {
		// Already released.
		return
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok || res.WorkflowID != workflowID || !res.State.Status.Terminal() {
		return
	}
	slog.Info("releasing the lease of a run that stopped without a terminal frame",
		"workflow_id", workflowID, "status", res.State.Status)
	if l, held := rs.lease(workflowID); held {
		rs.recordScheduleEnding(ctx, scheduleRun(&l), res.State.Status, res.State.StopReason)
	}
	rs.releaseLease(ctx, workflowID, endTerminal, life)
}

// reconcileLeasePresence settles the leases held, read before the run list status, against that list: terminal
// releases, listed clears the absence clock, absent starts or spends it. Absence never cancels and releases nothing.
// Boot passes inspectAbsent.
func (rs *Runs) reconcileLeasePresence(
	ctx context.Context, held []runlease.Lease, status map[string]marotte.RunStatus, now time.Time, inspectAbsent bool,
) {
	for i := range held {
		l := &held[i]
		st, known := status[l.WorkflowID]
		if known {
			if st.Terminal() {
				slog.Info("releasing the lease of a run with terminal status",
					"workflow_id", l.WorkflowID, "recipe", l.Recipe, "status", st)
				rs.recordScheduleEnding(ctx, scheduleRun(l), st, "")
				rs.releaseLease(ctx, l.WorkflowID, endTerminal, lifeOfLease(l))
				continue
			}
			rs.forgetAbsence(l.WorkflowID)
			continue
		}
		if inspectAbsent {
			terminal, notFound := rs.inspectTerminal(ctx, l.WorkflowID)
			if terminal {
				slog.Info("boot: releasing the lease of a run inspect says is over",
					"workflow_id", l.WorkflowID, "recipe", l.Recipe)
				rs.releaseLease(ctx, l.WorkflowID, endTerminal, lifeOfLease(l))
				continue
			}
			if notFound {
				rs.absence.markNotFound(l.WorkflowID, now)
			}
		}
		rs.observeAbsentLease(ctx, l, now)
	}
}

// inspectTerminal asks KAS about one absent run: terminal only for a reply naming it with a terminal status;
// notFound for KAS's "workflow not found", which is absence, not an end.
func (rs *Runs) inspectTerminal(ctx context.Context, workflowID string) (terminal, notFound bool) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return false, workflowNotFound(err)
	}
	var res inspectRunState
	if json.Unmarshal(raw, &res) != nil {
		return false, false
	}
	return res.WorkflowID == workflowID && res.State.Status.Terminal(), false
}

// workflowNotFound reports KAS's WorkflowNotFoundError from a search that covered every session (kiro-cli 2.37.0
// acp-server.js): KAS sets searchIncomplete when a relayed session it could not reach may still hold the run.
func workflowNotFound(err error) bool {
	e, ok := errors.AsType[*marotte.RPCError](err)
	if !ok {
		return false
	}
	var data struct {
		ErrorType        string `json:"errorType"`
		SearchIncomplete bool   `json:"searchIncomplete"`
	}
	return json.Unmarshal(e.Data, &data) == nil && data.ErrorType == "WorkflowNotFoundError" && !data.SearchIncomplete
}

// observeAbsentLease runs the in-memory absence clock: past leaseAbsenceBudget, once per process, it reports every
// holder, records the unknown outcome, ends the run's asks and lets its chats' idle bridges close. The lease and its
// keep facts stay until the run's end is seen.
func (rs *Runs) observeAbsentLease(ctx context.Context, l *runlease.Lease, now time.Time) {
	since, spent := rs.absence.observe(l.WorkflowID, now)
	if !spent {
		return
	}
	for _, h := range leaseHolders(l) {
		slog.Warn(logMsgRunAbsentKept, "chat_id", h, "workflow_id", l.WorkflowID,
			"absent_since", since, "absent_for", now.Sub(since).Round(time.Second))
	}
	rs.recordScheduleOutcome(ctx, scheduleRun(l), schedule.Outcome{Status: schedule.StatusUnknown, Reason: reasonLeaseAbsent})
	rs.endAsks(ctx, l.WorkflowID)
	if rs.coord != nil {
		for _, h := range leaseHolders(l) {
			rs.coord.releaseIfUnheld(h)
		}
	}
}

// logMsgRunAbsentKept is fixed text: an alert rule keys on it.
const logMsgRunAbsentKept = "keep fact kept: a run stayed absent past the lease budget with no terminal signal; " +
	"its chat stays kept until the run's end is seen"

func (rs *Runs) forgetAbsence(workflowID string) { rs.absence.forget(workflowID) }

// runAbsence is each held lease's absence clock. It lives in memory, so a restart restarts it: the clock decides
// nothing durable. The zero value is usable.
type runAbsence struct {
	runs map[string]absentRun
	mu   sync.Mutex
}

type absentRun struct {
	since time.Time
	// past is set once the clock spent leaseAbsenceBudget; notFound once a boot inspect answered "workflow not found".
	past, notFound bool
}

// observe starts the clock at now if it is not running, and reports once, when it first passes the budget.
func (a *runAbsence) observe(workflowID string, now time.Time) (since time.Time, spent bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runs == nil {
		a.runs = map[string]absentRun{}
	}
	r, ok := a.runs[workflowID]
	if !ok {
		r.since = now
	}
	if !r.past && now.Sub(r.since) >= leaseAbsenceBudget {
		r.past, spent = true, true
	}
	a.runs[workflowID] = r
	return r.since, spent
}

func (a *runAbsence) markNotFound(workflowID string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runs == nil {
		a.runs = map[string]absentRun{}
	}
	r, ok := a.runs[workflowID]
	if !ok {
		r.since = now
	}
	r.notFound = true
	a.runs[workflowID] = r
}

func (a *runAbsence) forget(workflowID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.runs, workflowID)
}

func (a *runAbsence) past(workflowID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs[workflowID].past
}

func (a *runAbsence) notFound(workflowID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs[workflowID].notFound
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

// clearOrphaned is the shared release: claim, then re-read and cancel in one transition, under the run's resume lock
// so no resume of marotte's lands between them, then record and release, as finishTermination orders it. A resume
// from outside marotte can still land between the read and the cancel; do not drop the auto-cancel without a
// compare-and-cancel.
func (rs *Runs) clearOrphaned(ctx context.Context, l *runlease.Lease) bool {
	if !rs.claimTermination(l.WorkflowID) {
		// Already ending; the winner releases.
		return false
	}
	switch c, err := rs.transition(ctx, l.WorkflowID, &runRequest{want: orphanCancel}); {
	case err != nil:
		rs.releaseTermination(l.WorkflowID)
		slog.Error("could not cancel a restart-orphaned run; its recipe stays busy until the next try",
			"workflow_id", l.WorkflowID, "error", err)
		return false
	case c.act != actCancelOrphan:
		// No longer an orphan: hand the claim back.
		rs.releaseTermination(l.WorkflowID)
		slog.Info("a run stopped reading as restart-orphaned before its cancel; leaving it alone",
			"workflow_id", l.WorkflowID, "recipe", l.Recipe)
		return false
	}
	rs.recordEnd(l.WorkflowID, runEndOrphaned)
	// ERROR: an alert rule keys on logMsgRunOrphaned.
	slog.Error(logMsgRunOrphaned, "workflow_id", l.WorkflowID, "recipe", l.Recipe,
		"origin", string(l.Origin), "schedule_id", l.ScheduleID)
	// The schedule's row too, or every later slot is refused as an overlap.
	rs.recordScheduleOutcome(ctx, scheduleRun(l), schedule.Outcome{Status: schedule.StatusFailed, Reason: reasonOrphaned})
	rs.releaseLease(ctx, l.WorkflowID, endCancelled, lifeOfLease(l))
	return true
}

// restartPaused reports whether KAS says a run's owning process died (seenRestartPaused): a selection only, since
// the cancel's own transition reads the run again.
func (rs *Runs) restartPaused(ctx context.Context, workflowID string) bool {
	return workflowID != "" && rs.readPauseState(ctx, workflowID).seen == seenRestartPaused
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
		StopReason    string            `json:"stopReason"`
		WorkflowID    string            `json:"workflowId"`
		// ParentSessionID is the run's launching session, how RunOwner attributes a run the inventory omits.
		ParentSessionID string `json:"parentSessionId"`
	} `json:"state"`
	WorkflowID string `json:"workflowId"`
}

// names reports whether the reply is about workflowID, as workflow.DecodeInspect decides it: every id it names is
// workflowID, and it names one.
func (r *inspectRunState) names(workflowID string) bool {
	top, inner := r.WorkflowID, r.State.WorkflowID
	return (top != "" || inner != "") && (top == "" || top == workflowID) && (inner == "" || inner == workflowID)
}

// heldForSignIn reports the sign-in gate's own landed pause, which KAS attributes to the user; a later user pause
// replaces its reason.
func (r *inspectRunState) heldForSignIn() bool {
	st := &r.State
	return st.Status == marotte.RunStatusPaused && st.PausePending == nil &&
		st.StopInitiator == initiatorUser && isHoldReason(st.StopReason)
}

// userParked mirrors KAS's test for a user pause, pending or landed; neither the sweep nor a heal may touch one.
func (r *inspectRunState) userParked() bool {
	st := &r.State
	return (st.PausePending != nil && st.PausePending.Initiator == initiatorUser) ||
		(st.Status == marotte.RunStatusPaused && st.StopInitiator == initiatorUser)
}

// inspect reads one run's inspect reply, false when it cannot be read or decoded at all.
func (rs *Runs) inspect(ctx context.Context, workflowID string) (inspectRunState, bool) {
	res, _, err := rs.readInspect(ctx, workflowID)
	return res, err == nil
}

var errInspectUndecoded = errors.New("a run's inspect reply could not be decoded")

// readInspect is inspect with the raw reply and its failure: the RPC's, or errInspectUndecoded.
func (rs *Runs) readInspect(ctx context.Context, workflowID string) (inspectRunState, json.RawMessage, error) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a paused run's state, so it is left alone",
			"workflow_id", workflowID, "error", err)
		return inspectRunState{}, nil, err
	}
	var res inspectRunState
	if !unmarshalKeepingReadable(raw, &res) {
		return inspectRunState{}, nil, errInspectUndecoded
	}
	return res, raw, nil
}

// rawInspect issues `_kiro/workflow/inspect` and types its failure, so callers use errors.Is. Every
// successful read seeds the step-session registry before any caller acts on it: after a restart it is
// the only attribution a step's unmarked frames get, and a caller may send to a step from this read.
func (rs *Runs) rawInspect(ctx context.Context, workflowID string) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := rs.engine.rawCall(cctx, "workflow inspect call", methodKiroWorkflowInspect,
		map[string]any{keyWorkflowID: workflowID})
	if err != nil {
		return nil, workflow.Classify(err)
	}
	rs.translate.RecordInspect(context.WithoutCancel(ctx), raw)
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) == nil && res.State != nil {
		rs.seedRouterSteps(workflowID, res.State)
		rs.signals.observe(workflowID, res.State)
	}
	return raw, nil
}

// seedRouterSteps teaches the router every step session an inspect names, so a step's frames after a restart reach
// its run's owner before any node_start is read again.
func (rs *Runs) seedRouterSteps(workflowID string, s *workflow.State) {
	if rs.router == nil {
		return
	}
	var ids []string
	for _, st := range workflow.Steps(s) {
		if st.SessionID != "" {
			ids = append(ids, st.SessionID)
		}
	}
	rs.router.seedSteps(workflowID, ids)
}

// observeInspected reads workflowID once and feeds its status to the run clock: an executing run is armed, a stopped
// one disarmed. It decides a run mutation no frame settled.
func (rs *Runs) observeInspected(ctx context.Context, workflowID string) error {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		if workflowNotFound(err) {
			return nil
		}
		return err
	}
	in, err := workflow.DecodeInspect(workflowID, raw)
	if err != nil {
		return err
	}
	switch marotte.RunStatus(in.State.Status) {
	case marotte.RunStatusRunning:
		if !rs.bounded(workflowID) {
			rs.armDeadline(ctx, workflowID)
		}
	case marotte.RunStatusPaused:
		if rs.bounded(workflowID) {
			rs.parkDeadline(ctx, workflowID)
			rs.watchUnattendedPause(workflowID)
		}
	default:
		if rs.bounded(workflowID) {
			rs.disarmDeadline(ctx, workflowID)
		}
	}
	return nil
}
