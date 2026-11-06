package agent

// The run host: launching a parentless workflow run and the session that holds it. An agent-launched
// run is parented on its chat's session by KAS; a manual or scheduled one gets one host session under
// the synthetic chat id `run:<workflowId>`, which has no chat file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/trigger"
	"github.com/cplieger/marotte/internal/workflow"
)

// Workflow RPC param keys, shared across the five verbs.
const (
	keyWorkflowID     = "workflowId"
	keyWorkspacePaths = "workspacePaths"
)

// Real ids are `c-*` uuids.
const runChatPrefix = "run:"

func runChatID(workflowID string) marotte.ChatID {
	return marotte.ChatID(runChatPrefix + workflowID)
}

func isRunChat(chatID marotte.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

func workflowIDOf(chatID marotte.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// unanswered wraps a launch's err with errEngineSlow when the engine ran out of time or was not ready. Only for an
// error returned before workflow/new is sent: after it, a run may exist and a retry could double it. ctx is the
// caller's, so a launch its caller abandoned is not the engine's fault.
func unanswered(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, marotte.ErrBridgeExited) {
		return fmt.Errorf("%w: %w", errEngineSlow, err)
	}
	return err
}

var errLaunchUnconfirmed = errors.New("the run was created and its start was sent, but the answer was lost or " +
	"could not be read, so whether it started is unknown. Open the run from History to see where it is")

var errLaunchNewUnconfirmed = errors.New("the engine's answer to the launch was lost or could not be read, " +
	"so a run may have been created. Check History before launching again")

// launchTimeout bounds the launch: the host session's door, `workflow/new` and `invoke`, extended through an engine
// stall (engineOwner.stallAware). A var so a test can shorten it.
var launchTimeout = 120 * time.Second

// launch starts one parentless attended run of the recipe and returns its workflow id and name.
func (rs *Runs) launch(
	ctx context.Context, source string, inputs map[string]string, defaults marotte.RunDefaults,
) (id, name string, err error) {
	return rs.launchAs(ctx, source, inputs, defaults, manualLaunch())
}

// LaunchScheduled launches an unattended run for a schedule. slotAt is its next slot; zero leaves the idle window and backstop.
// Only the inputs the recipe declares are sent; the caller's map is not modified. The sign-in gate's refusal wraps
// schedule.ErrNoRunCanStart; an engine that did not answer before the run was created, or a model list too unreadable
// to check the recipe's step picks against, wraps schedule.ErrEngineBusy.
func (rs *Runs) LaunchScheduled(
	ctx context.Context, source string, row launchrule.Row, inputs map[string]string, defaults marotte.RunDefaults,
	slotAt time.Time,
) (id, name string, err error) {
	// The schedule runner outlives Shutdown, so a launch is held on inflight and refused once draining.
	release, ok := rs.lifecycle.enterUnlessDraining()
	if !ok {
		return "", "", errRuntimeDraining
	}
	defer release()
	id, name, err = rs.launchAs(ctx, source, inputs, defaults, scheduledLaunch(row, slotAt))
	switch {
	case errors.Is(err, errRunsNeedSignIn) || errors.Is(err, errRunsProviderDown):
		err = fmt.Errorf("%w: %w", schedule.ErrNoRunCanStart, err)
	case errors.Is(err, errEngineSlow) || errors.Is(err, errDefaultsUncheckable):
		err = fmt.Errorf("%w: %w", schedule.ErrEngineBusy, err)
	case errors.Is(err, errLaunchUnconfirmed):
		// The runner records any error as a failed slot; the run may be running, and its ending settles the row.
		err = nil
	}
	return id, name, err
}

func (rs *Runs) launchAs(
	ctx context.Context, source string, inputs map[string]string, defaults marotte.RunDefaults, o launchOrigin,
) (id, name string, err error) {
	// One admission from before workflow/new through invoke, so no run is created that the gate then never starts.
	a, gErr := rs.admitRunWork(refuseClosed, "", "")
	if gErr != nil {
		return "", "", gErr
	}
	defer func() { a.settle(id) }()
	budgetEnd := time.Now().Add(launchTimeout)
	cctx, cancel := rs.engine.stallAware(ctx, launchTimeout)
	defer cancel()

	recipe, err := rs.launchRecipe(cctx, source, inputs)
	if err != nil {
		return "", "", unanswered(ctx, err)
	}
	if pErr := rs.checkRecipePins(cctx, recipe.Plan, defaults); pErr != nil {
		return "", "", pErr
	}
	if o.origin == runlease.OriginManual && !isAgentSource(source) {
		// A manual run yields to its recipe's next slot.
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	unlock, bErr := rs.lockIdleRecipe(cctx, recipe.Name, &o)
	if bErr != nil {
		return "", "", unanswered(ctx, bErr)
	}
	defer unlock()
	var dropped []string
	if o.origin == runlease.OriginScheduled {
		inputs, dropped = declaredInputs(inputs, recipe.Inputs)
		// Before the door: KAS's reset reconnects unconditionally, so a reset after the host's acquire began a fresh
		// connect would connect twice and close the good one.
		rs.resetFailedMCP(cctx)
	}

	// The host session's presets are resolved once and recorded on the lease: a reload after a restart applies
	// these, never the profile as it is then.
	settings := rs.doorSettings(cctx)
	presets := settings.Presets
	sb := newSharedBridge(runHostLife{ctx: rs.lifecycle.shutdownCtx})
	var relay *hostRelay
	door := &marotte.SessionOpts{
		Role: marotte.SessionRoleHost, Presets: presets, AdditionalDirectories: settings.AdditionalDirectories,
	}
	if _, oErr := rs.engine.openSession(cctx, door,
		func(s engineSession) error {
			relay = newHostRelay(s, func(msg *marotte.RPCResponse) { rs.answerEarly(s, msg) })
			sb.attach(relay)
			return nil
		}); oErr != nil {
		return "", "", unanswered(ctx, fmt.Errorf("%w: %w", errRunHostStart, oErr))
	}
	parent := sb.SessionID()
	if parent == "" {
		sb.closeOwned(rs.bridges.closeSession)
		return "", "", fmt.Errorf("%w: the host session's door answered no session id", errRunHostStart)
	}
	var wfID string
	err = sb.useRun(cctx, func(ctx context.Context, s engineSession) error {
		var nErr error
		wfID, nErr = rs.workflowNew(ctx, s, recipe.Source, inputs, defaults, parent, o.runLabel(recipe.Name))
		return nErr
	})
	if err != nil {
		sb.closeOwned(rs.bridges.closeSession)
		if ctx.Err() == nil && (verdictOf(err) == kasUnconfirmed || isCtxErr(err)) {
			err = fmt.Errorf("%w: %w", errLaunchNewUnconfirmed, err)
		}
		return "", "", err
	}
	if !rs.bridges.insert(runChatID(wfID), sb) {
		sb.closeOwned(rs.bridges.closeSession)
		return "", "", fmt.Errorf("workflow %s already has a host session", wfID)
	}
	// The run exists: a caller that stops waiting now must not leave it half launched, its row naming a run that
	// nothing tracks, so the rest keeps the launch's budget and drops the caller's cancel. A stall-aware context's
	// Deadline is its extension ceiling, not its budget, so the budget's end is carried over instead.
	rctx, rcancel := rs.engine.stallAware(context.WithoutCancel(ctx), time.Until(budgetEnd))
	defer rcancel()
	sb.setIdle()
	sb.settled.settle(nil)
	_ = sb.useRun(cctx, func(_ context.Context, s engineSession) error {
		rs.coord.goRunForward(runChatID(wfID), s)
		return nil
	})
	relay.begin()
	if sErr := rs.startLaunched(rctx, a, sb, wfID, recipe.Name, &o, hostSecurityOf(presets), unlock); sErr != nil {
		return wfID, recipe.Name, sErr
	}
	logLaunched(wfID, recipe.Name, dropped)
	return wfID, recipe.Name, nil
}

// lockIdleRecipe takes the recipe's launch lock and runs the single-run check under it. The caller holds the lock until
// the run's lease is granted, when KAS already lists the created run as active, so an overlapping launch finds it
// busy; unlock is idempotent.
func (rs *Runs) lockIdleRecipe(ctx context.Context, recipe string, o *launchOrigin) (unlock func(), err error) {
	if o.event != nil && rs.confirmer == nil {
		return nil, errNoTriggerConfirmer
	}
	release, err := rs.launchLocks.Lock(ctx, recipe)
	if err != nil {
		return nil, err
	}
	unlock = sync.OnceFunc(release)
	if err := rs.recipeIdle(ctx, recipe, o.ruleID()); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// startLaunched leases, routes and invokes a run workflow/new created on sb, releasing the recipe's launch lock once
// the lease is granted. A run that must not be invoked has its host session closed.
func (rs *Runs) startLaunched(
	ctx context.Context, a runAdmission, sb *sharedBridge, wfID, recipe string, o *launchOrigin,
	host *runlease.HostSecurity, unlock func(),
) error {
	err := rs.leaseLaunched(ctx, wfID, recipe, o, host)
	unlock()
	if err != nil {
		rs.coord.closeBridge(ctx, runChatID(wfID), marotte.TurnOutcomeInterrupted)
		return err
	}
	parent := sb.SessionID()
	rs.recordParentSession(ctx, wfID, string(parent))
	// Visible to Route before invoke is written, so the run's first frame reaches the host's lane.
	rs.router.registerOwner(wfID, parent)
	if _, err := rs.write(ctx, a, sb, wfID, methodKiroWorkflowInvoke,
		map[string]any{keyWorkflowID: wfID}, mutationDrive); err != nil {
		return rs.invokeFailed(durable.Context(ctx), wfID, recipe, o, err)
	}
	// After invoke, so a run that never started leaves no timer.
	rs.armDeadline(ctx, wfID)
	return nil
}

// leaseLaunched grants a created run's lease before anything can execute (runlease.Lease). A triggered launch first
// names its attempt; an error from either step means the run must not be invoked, and the launch sends no cancel:
// NameAttempt's error means the confirmer already queued one, trigger.ErrLeaseNotGranted that the caller queues it.
func (rs *Runs) leaseLaunched(
	ctx context.Context, wfID, recipe string, o *launchOrigin, host *runlease.HostSecurity,
) error {
	if o.event == nil {
		rs.grantLease(ctx, wfID, recipe, *o, host)
		return nil
	}
	// Owned from the naming through the grant, so a cancel the rule queues meanwhile runs once the lease is written.
	release := rs.owners.holdAlways(wfID)
	defer release()
	if err := rs.confirmer.NameAttempt(o.event.ruleID, o.event.eventKey, wfID); err != nil {
		return err
	}
	if !rs.grantLease(ctx, wfID, recipe, *o, host) {
		// After a restart nothing would name the run, so its run_start would mint a lease that confirms nothing.
		return trigger.ErrLeaseNotGranted
	}
	return nil
}

func logLaunched(wfID, recipe string, dropped []string) {
	attrs := []any{"workflow_id", wfID, "recipe", recipe}
	if len(dropped) > 0 {
		attrs = append(attrs, "dropped_inputs", logsafe.Field(strings.Join(dropped, ",")))
	}
	slog.Info("workflow run launched", attrs...)
}

// invokeFailed settles a launch whose invoke failed and returns its error. For a manual or scheduled launch, an
// unconfirmed (verdictOf) or deadline-outlived answer may have started the run, so it keeps its lease, route and
// bridge under an armed deadline (errLaunchUnconfirmed); a refusal records the failure on the row grantLease wrote and
// releases the run. A triggered launch returns with its lease and host session kept and its deadline unarmed,
// whatever the error: a lost reply can follow a run_start that already confirmed the event and armed it, and the
// attempt table decides from what the error proves.
func (rs *Runs) invokeFailed(ctx context.Context, wfID, recipe string, o *launchOrigin, err error) error {
	err = fmt.Errorf("workflow invoke: %w", err)
	if o.event != nil {
		return err
	}
	if verdictOf(err) == kasUnconfirmed || isCtxErr(err) {
		rs.armDeadline(ctx, wfID)
		slog.Warn("a workflow run's start was sent but its answer was lost; kept as launched",
			"workflow_id", wfID, "recipe", recipe, "error", err)
		return fmt.Errorf("%w: %w", errLaunchUnconfirmed, err)
	}
	rs.recordScheduleOutcome(ctx, launchrule.Run{ScheduleID: o.scheduleRow().ID, WorkflowID: wfID},
		launchrule.Outcome{Status: launchrule.StatusFailed, Reason: launchrule.ReasonText(err.Error())})
	rs.releaseLease(ctx, wfID)
	rs.coord.closeBridge(ctx, runChatID(wfID), marotte.TurnOutcomeInterrupted)
	return err
}

func declaredInputs(inputs, declared map[string]string) (kept map[string]string, dropped []string) {
	kept = make(map[string]string, len(inputs))
	for k, v := range inputs {
		if _, ok := declared[k]; ok {
			kept[k] = v
		} else {
			dropped = append(dropped, k)
		}
	}
	slices.Sort(dropped)
	return kept, dropped
}

// runStop is who a stop tells KAS asked. The zero value attributes nothing: an attributed stop tells the agent the user stopped it.
type runStop struct {
	why    string
	byUser bool
}

// KAS caps a reason at 500 characters and refuses one sent without an initiator.
const (
	initiatorUser      = "user"
	stopWhyTabClosed   = "tab closed"
	stopWhyChatDeleted = "chat deleted"
	stopWhyRewound     = "chat rewound"
)

// why is set only when the cause is not the stop button.
func userStop(why string) runStop { return runStop{byUser: true, why: why} }

func (s runStop) apply(params map[string]any) {
	if !s.byUser {
		return
	}
	params["initiator"] = initiatorUser
	if s.why != "" {
		params["reason"] = s.why
	}
}

// controlCaller is who sent a person's run control over REST, as callerOf reads the request.
type controlCaller int

const (
	// callerPage is marotte's own page: the reader's gesture.
	callerPage controlCaller = iota
	// callerAPI is any other caller, such as an agent's curl, which KAS would otherwise record as the user.
	callerAPI
)

// apiCallerReason rides beside initiator "user" on a callerAPI control, so KAS and userParked still read it as a
// person's while the client words it as an agent's (static-src/run-store.ts, testdata/pause_reasons.json).
const apiCallerReason = "asked through marotte's run API by a caller outside its page"

// stop is the attribution c's pause or stop carries.
func (c controlCaller) stop() runStop {
	if c == callerAPI {
		return userStop(apiCallerReason)
	}
	return userStop("")
}

// cancel stops a run on caller's behalf. A lost termination claim returns nil; a refused RPC is
// returned, and retryTermination retries it.
func (rs *Runs) cancel(ctx context.Context, workflowID string, caller controlCaller) error {
	return rs.cancelAs(ctx, workflowID, caller.stop())
}

// cancelAs is Cancel attributed to stop.
func (rs *Runs) cancelAs(ctx context.Context, workflowID string, stop runStop) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	return rs.finishTermination(ctx, workflowID, "", stop, func(ctx context.Context) error {
		// Parked or released: marotte no longer bounds this run.
		if !rs.bounded(workflowID) {
			return nil
		}
		return rs.cancelAs(ctx, workflowID, stop)
	})
}

var errNoTriggerConfirmer = errors.New("a triggered launch needs the launch rules, which are not wired")

// cancelRPC issues the cancel verb only. The host session stays resident: the run certifies the cancel at its node
// boundary, and its terminal `run_complete` releases the host.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string, stop runStop) error {
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
	err := rs.stopVerb(ctx, workflowID, methodKiroWorkflowCancel, params)
	if err != nil {
		return fmt.Errorf("workflow cancel call: %w", err)
	}
	return nil
}

// delete removes a run and everything kept about it, on the user's behalf; unrecoverable, and a refused
// delete leaves everything. No termination claim (KAS's delete cancels a live run itself), and the bridge
// is closed. errRunDeleting refuses a run another delete holds; errDeleteUnconfirmed reports a delete
// KAS was sent whose outcome is still being read back.
func (rs *Runs) delete(ctx context.Context, workflowID string) error {
	return rs.deleteAs(ctx, workflowID, userStop(""))
}

var errDeleteUnconfirmed = errors.New("the delete reached the run's agent but its answer was lost; " +
	"the run takes no messages until marotte reads back whether it is gone")

// deleteReconcileRetry is how long an unconfirmed delete waits before its run is read again. A var for tests.
var deleteReconcileRetry = 30 * time.Second

// deleteAs is Delete attributed to stop.
func (rs *Runs) deleteAs(ctx context.Context, workflowID string, stop runStop) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	d, err := rs.life.beginDelete(ctx, workflowID)
	if err != nil {
		return err
	}
	return rs.deleteHeld(ctx, d, stop)
}

// deleteHeld sends the delete d holds its run closed for, and settles d once KAS's answer is known. A run KAS no longer
// finds is torn down all the same, and its not-found error returned.
func (rs *Runs) deleteHeld(ctx context.Context, d *runDeletion, stop runStop) error {
	workflowID := d.workflowID
	var got kasVerdict
	var err error
	rs.stopSerialized(ctx, workflowID, func(sctx context.Context) bool {
		got, err = rs.deleteCall(sctx, workflowID, stop)
		return got != kasRefused
	})
	switch got {
	case kasRefused:
		if workflowNotFound(err) {
			// No later list names the run, so nothing else would drop marotte's record of it.
			rs.teardownDeleted(ctx, workflowID)
		}
		d.settle()
		return err
	case kasUnconfirmed:
		slog.Warn("a workflow delete was sent but its answer was lost; reading the run back",
			"workflow_id", workflowID, "error", err)
		return rs.reconcileDelete(ctx, d, err)
	case kasTook:
	}
	rs.teardownDeleted(ctx, workflowID)
	d.settle()
	return nil
}

// deleteCall sends the delete once the engine is ready, then waits for its answer detached from ctx: a delete KAS
// may have applied is waited out, never abandoned.
func (rs *Runs) deleteCall(ctx context.Context, workflowID string, stop runStop) (kasVerdict, error) {
	params := map[string]any{keyWorkflowID: workflowID}
	stop.apply(params)
	h, _, release, err := rs.engine.acquire(ctx)
	if err != nil {
		return kasRefused, err
	}
	defer release()
	_, err = h.Call(durable.Context(ctx), methodKiroWorkflowDelete, params)
	err = workflow.Classify(err)
	return verdictOf(err), err
}

// reconcileDelete settles a delete whose answer was lost by reading the run back: one KAS still has
// reopens, one KAS no longer knows is torn down here, and an unreadable answer keeps the run closed and
// asks again later.
func (rs *Runs) reconcileDelete(ctx context.Context, d *runDeletion, cause error) error {
	switch rs.readBack(ctx, d.workflowID) {
	case runKept:
		slog.Info("a run whose delete was unconfirmed is still there, so it takes messages again",
			"workflow_id", d.workflowID)
		rs.clearStopping(ctx, d.workflowID)
		d.settle()
		return cause
	case runGone:
		rs.teardownDeleted(ctx, d.workflowID)
		d.settle()
		return nil
	case runUnread:
	}
	rs.reconcileLater(d)
	return fmt.Errorf("%w: %w", errDeleteUnconfirmed, cause)
}

func (rs *Runs) reconcileLater(d *runDeletion) {
	rs.lifecycle.afterUnlessDraining(deleteReconcileRetry, func() {
		ctx := rs.lifecycle.shutdownCtx
		if ctx.Err() != nil {
			return
		}
		if rs.reconcileDelete(ctx, d, errDeleteUnconfirmed) == nil {
			slog.Info("an unconfirmed workflow delete was confirmed", "workflow_id", d.workflowID)
		}
	})
}

type readBackVerdict int

const (
	runUnread readBackVerdict = iota
	runKept
	runGone
)

func (rs *Runs) readBack(ctx context.Context, workflowID string) readBackVerdict {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		if workflowNotFound(err) {
			return runGone
		}
		slog.Warn("could not read back a run whose delete was unconfirmed", "workflow_id", workflowID, "error", err)
		return runUnread
	}
	if _, err := workflow.DecodeInspect(workflowID, raw); err != nil {
		return runUnread
	}
	return runKept
}

// teardownDeleted drops everything marotte keeps about a run KAS deleted or no longer finds.
func (rs *Runs) teardownDeleted(ctx context.Context, workflowID string) {
	rs.dropMCPSessions(ctx, rs.router.forgetWorkflow(workflowID))
	rs.translate.ForgetRunSteps(workflowID)
	rs.retries.clear(workflowID)
	rs.deleteRunLog(ctx, workflowID)
	rs.coord.closeBridge(ctx, runChatID(workflowID), marotte.TurnOutcomeInterrupted)
	rs.forgetBounds(ctx, workflowID)
	rs.clearEnd(workflowID)
	if rs.log != nil {
		if err := rs.log.removeDir(workflowID); err != nil {
			slog.Warn("run log: remove run directory", "workflow_id", workflowID, "error", err)
		}
	}
	slog.Info("workflow run deleted", "workflow_id", workflowID)
}

// pause is caller's ask that a run stop at its next node boundary; the reply confirms the ask. runTransitions
// decides it as a person's.
func (rs *Runs) pause(ctx context.Context, workflowID string, caller controlCaller) error {
	_, err := rs.transition(ctx, workflowID, &runRequest{want: personPause, verb: verbPause, caller: caller})
	return err
}

// resume is caller's re-drive of a paused run with a fresh executing budget. runTransitions decides it as a person's.
func (rs *Runs) resume(ctx context.Context, workflowID string, caller controlCaller) error {
	_, err := rs.transition(ctx, workflowID, &runRequest{want: personResume, verb: verbResume, caller: caller})
	return err
}

// Below clientRequestBudget, from which it is derived: the retry's own deadline, never a browser abort, decides
// whether it released a lease it minted. It bounds the host session's load and the call. A var for tests.
var retryTimeout = clientRequestBudget - 5*time.Second

// clientRequestBudget is the timeout @cplieger/ui/fetch hard-wires into every apiAction.
const clientRequestBudget = 30 * time.Second

var errRetryRunDeleting = errors.New("this run is being deleted or checked for deletion, so nothing was retried. " +
	"Try again in a moment")

// errRetryRunOwned is a retry that waited out retryTimeout for the run's owner (runOwners): its launch rule's cancel
// or another retry of it.
var errRetryRunOwned = errors.New("this run is still being cancelled or retried, so nothing was retried. " +
	"Try again in a moment")

// errRetrySlow reports a retry whose run's session did not load within retryTimeout, so the verb was never sent;
// the REST layer answers 503.
var errRetrySlow = errors.New(
	"the run's session did not load in time, so nothing was retried. Try again",
)

// errRetryOutcomeUnknown reports a retry verb sent and not answered within retryTimeout: KAS may be running it, so
// the lease stays and the REST layer answers 504 still_applying.
var errRetryOutcomeUnknown = errors.New(
	"kiro-cli has not answered the retry yet and may still be running it. The run updates when it does",
)

// errRetryOutcomeUnreadable reports a retry KAS may have taken whose report was lost or unreadable. The run may be
// executing, so the remedy is refreshing, not retrying.
var errRetryOutcomeUnreadable = errors.New(
	"the retry was sent but its report was lost or could not be read, so which steps it reset " +
		"is unknown. Refresh the run to see where it is",
)

// RetriedNodeIDs tells a five-node retry from one that reset nothing.
type kasRetryOutcome struct {
	WorkflowID     string   `json:"workflowId"`
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retriedNodeIds"`
}

// retry resets a failed or aborted run's failed work and reports what it reset, hosted from one read of the run's origin.
func (rs *Runs) retry(
	ctx context.Context, workflowID string, aff *runAffordance,
) (out marotte.RunRetriedResponse, err error) {
	if workflowID == "" {
		return out, errors.New("missing workflow id")
	}
	// The purge re-reads a run under its delete hold and deletes on that answer, so no retry may run inside the hold.
	lease, err := rs.life.admit(workflowID)
	if err != nil {
		return out, command.StatusError(http.StatusConflict, errRetryRunDeleting)
	}
	defer lease.end()
	// Read before the verb and reused throughout: nothing can learn it once the run re-drives, and two reads can disagree.
	origin := rs.originOf(ctx, workflowID)
	cctx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()

	recipe := origin.recipe
	holders, launchers, err := rs.retryHolders(cctx, workflowID, &origin)
	if err != nil {
		return out, err
	}
	if refused := rs.refuseClosedLauncher(workflowID, launchers); refused != nil {
		return out, refused
	}
	own, err := rs.ownForRetry(cctx, workflowID)
	if err != nil {
		return out, err
	}
	defer own()
	// The lease before the verb, since retry's `run_start` can beat the reply, and before the host session's load,
	// which reads its presets from it.
	minted := rs.leaseForRetry(cctx, workflowID, &origin, holders, aff)
	rs.markRunAlive(cctx, workflowID, holders)
	host, err := rs.ensureResident(cctx, workflowID)
	if err != nil {
		if minted {
			rs.releaseLease(cctx, workflowID)
		}
		return out, rs.retryResidencyErr(cctx, err)
	}
	err = rs.controlSerialized(cctx, workflowID, func(sctx context.Context) error {
		var cErr error
		out, cErr = rs.retryCall(sctx, host, workflowID)
		return cErr
	})
	if errors.Is(err, errRetryOutcomeUnreadable) || errors.Is(err, errRetryOutcomeUnknown) {
		// The retry may be running; tearing down would kill the work mid-node. cctx may be spent and the client may
		// have left, so the re-arm's writes run detached.
		rs.rearmRetried(durable.Context(ctx), workflowID, &origin, holders)
		return out, err
	}
	if err != nil {
		// Return the minted lease, except on a context error, where KAS may have taken the retry and would run deadline-less.
		if minted && !isCtxErr(err) {
			rs.releaseLease(cctx, workflowID)
		}
		return out, err
	}
	// Only on success: a refused retry re-drove nothing.
	rs.rearmRetried(cctx, workflowID, &origin, holders)
	slog.Info("workflow run retried",
		"workflow_id", workflowID, "recipe", recipe,
		"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
	return out, nil
}

// ownForRetry makes the retry the run's owner (runOwners), then hands an abandoned run back to the person
// (handBackAbandoned). release is nil exactly when err is not.
func (rs *Runs) ownForRetry(ctx context.Context, workflowID string) (release func(), err error) {
	release, err = rs.owners.hold(ctx, workflowID)
	if err != nil {
		return nil, command.StatusError(http.StatusConflict, errRetryRunOwned)
	}
	if err := rs.handBackAbandoned(ctx, workflowID); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// leaseForRetry renews the run's lease for the retry, or grants one from origin, reporting whether it minted one.
func (rs *Runs) leaseForRetry(
	ctx context.Context, workflowID string, origin *runOrigin, holders []marotte.ChatID, aff *runAffordance,
) (minted bool) {
	if l, held := rs.lease(workflowID); held && (l.ScheduleID != "" || l.RuleID != "") {
		rs.settleMissedEnd(ctx, &l, aff.status)
	}
	if l, held := rs.lease(workflowID); held {
		// A manual or agent lease is the one this origin would grant; the run under it is a new one all the same.
		rs.restartLife(ctx, workflowID)
		rs.recordKeepChats(ctx, &l, holders)
		return false
	}
	rs.grantRetryLease(ctx, workflowID, origin, holders)
	return true
}

// retryCall issues the verb and decodes its report.
func (rs *Runs) retryCall(
	ctx context.Context, host *drivenHost, workflowID string,
) (marotte.RunRetriedResponse, error) {
	none := marotte.RunRetriedResponse{}
	rs.retries.mark(workflowID, time.Now())
	resp, err := host.call(ctx, rs, workflowID, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
	if err != nil && ctx.Err() == nil && verdictOf(err) == kasUnconfirmed {
		return none, fmt.Errorf("%w: %w", errRetryOutcomeUnreadable, err)
	}
	if err != nil {
		return none, fmt.Errorf("workflow retry: %w", rs.retryDeadlineErr(ctx, err))
	}
	// Past here KAS accepted the verb: any failure is a lost report.
	if resp == nil || len(resp.Result) == 0 {
		return none, fmt.Errorf("%w: the reply carried no outcome", errRetryOutcomeUnreadable)
	}
	var out kasRetryOutcome
	if uErr := json.Unmarshal(resp.Result, &out); uErr != nil {
		return none, fmt.Errorf("%w: undecodable outcome: %w", errRetryOutcomeUnreadable, uErr)
	}
	// The reply must be about the run asked for.
	if out.WorkflowID != "" && out.WorkflowID != workflowID {
		return none, fmt.Errorf("%w: the reply names run %q, not %q",
			errRetryOutcomeUnreadable, out.WorkflowID, workflowID)
	}
	// Never nil on the wire.
	nodes := out.RetriedNodeIDs
	if nodes == nil {
		nodes = []string{}
	}
	return marotte.RunRetriedResponse{Status: out.Status, RetriedNodeIDs: nodes}, nil
}

// retryResidencyErr labels a failed load of the run's session: this call's deadline, or the load failure itself.
func (*Runs) retryResidencyErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetrySlow
	}
	return fmt.Errorf("retry: load the run's session: %w", err)
}

// retryDeadlineErr labels a verb this call's deadline abandoned errRetryOutcomeUnknown, since KAS may still apply it.
func (*Runs) retryDeadlineErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", errRetryOutcomeUnknown, err)
	}
	return err
}

// errStepStatusRefused means KAS declined the update (a terminal run, a step that is not paused, a caller
// that is not the owning parent, or a paused step given a status other than completed or failed); the REST
// layer answers 409.
var errStepStatusRefused = errors.New("this run's step status was not changed")

// updateStatusAction is `_kiro/workflow/update`'s step-status action, sent explicitly because the tool schema requires it.
const updateStatusAction = "update_status"

// stepStatusRefusal returns KAS's reason for a declined update, else "". `Updated` is a *bool so absent reads as taken.
func stepStatusRefusal(resp *marotte.RPCResponse) string {
	if resp == nil || len(resp.Result) == 0 {
		return ""
	}
	var reply struct {
		Updated *bool  `json:"updated"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Result, &reply) != nil {
		return ""
	}
	if reply.Updated == nil || *reply.Updated {
		return ""
	}
	if reply.Message == "" {
		return "KAS gave no reason"
	}
	return reply.Message
}

// The node statuses marotte writes: a finished repeat, and a person's end of a paused step.
const (
	runStepCompleted = string(marotte.RunStepEndCompleted)
	runStepFailed    = string(marotte.RunStepEndFailed)
)

// updateNodeStatus writes status on the node nodeID names, a repeat paused at its cap or a paused step, and arms the
// run's deadline; KAS's decline is errStepStatusRefused with its reason.
func (rs *Runs) updateNodeStatus(ctx context.Context, host *drivenHost, workflowID, nodeID, status string) error {
	resp, err := host.call(ctx, rs, workflowID, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"action":      updateStatusAction,
		"status":      status,
		"nodeId":      nodeID,
	})
	if err != nil {
		if marotte.ReplyUnread(err) {
			// KAS may have ended the step and re-driven the run, so it is bounded as an end that took.
			rs.armDeadline(ctx, workflowID)
		}
		return err
	}
	if refusal := stepStatusRefusal(resp); refusal != "" {
		return fmt.Errorf("%w: %s", errStepStatusRefused, refusal)
	}
	rs.armDeadline(ctx, workflowID)
	return nil
}

// answerInput answers one parked step with the words, as a `session/prompt` to its own session;
// from is the API caller that sent them, nil for the user's own. The ask is checked against a
// fresh read of the run's host under the run's serializer, after marotte's own pending send was
// registered; the serializer is released before the prompt, whose refused arm can last a whole
// ordinary turn.
func (rs *Runs) answerInput(ctx context.Context, workflowID, askID, text string, from *marotte.Caller) (err error) {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("an answer cannot be empty")
	}
	c := rs.cellFor(workflowID)
	if c == nil {
		return errAskGone
	}
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		return err
	}
	lease, err := rs.life.admit(workflowID)
	if err != nil {
		// A delete KAS keeps reopens the run, so this is not "gone".
		return errAskStopping
	}
	defer lease.end()
	defer rs.kickCell(c)
	if aErr := c.acquire(ctx); aErr != nil {
		return aErr
	}
	held := true
	release := func() {
		if held {
			held = false
			c.release()
		}
	}
	defer release()
	if refusal := c.holdRefusal(); refusal != nil {
		return refusal
	}
	p := rs.registerPending(ctx, c)
	r, err := rs.readAsks(ctx, c)
	if err != nil {
		rs.clearPending(ctx, c, p)
		slog.Warn("could not read a run before answering it", "workflow_id", workflowID, "error", err)
		return errAskUnreadable
	}
	ask, err := askByID(r.asks, askID)
	if err != nil {
		rs.clearPending(ctx, c, p)
		return err
	}
	at := stepAddr{workflowID: workflowID, nodePath: workflow.PathKey(ask.NodePath), session: ask.StepSessionID}
	unlock, err := rs.steers.lock(ctx, workflowID)
	if err != nil {
		rs.clearPending(ctx, c, p)
		return err
	}
	defer unlock()
	c.sendingPending(p, ask.StepSessionID)
	release()
	got, err := rs.sendStepWords(ctx, host, at, text, "answer-"+askID, from)
	if got == kasRefused {
		rs.clearPending(ctx, c, p)
		return err
	}
	rs.repliedPending(ctx, c, p)
	if err != nil {
		return err
	}
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_path", scrubLog(at.nodePath))
	return nil
}

// "" for parentless.
type runParent struct {
	chat    marotte.ChatID
	session string
}

// err says why parent cannot route a verb.
type runOrigin struct {
	err    error
	parent runParent
	recipe string
}

// An ownerless parent is trusted only after a complete chat scan: loading a chat's session as a
// run host replays that chat's transcript into the run.
func (rs *Runs) originOf(ctx context.Context, workflowID string) runOrigin {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("could not read the run inventory, so a run's parent chat and recipe are unknown",
			"workflow_id", workflowID, "error", err)
		return runOrigin{err: fmt.Errorf("%w: reading the run inventory: %w", errRunHostStart, err)}
	}
	i := slices.IndexFunc(runs, func(r kasWorkflowRun) bool { return r.WorkflowID == workflowID })
	if i < 0 {
		return runOrigin{err: errRunNotListed}
	}
	// WorkflowName is the recipe the single-run rule compares; Name is `runLabel ?? workflowName`.
	o := runOrigin{
		recipe: runs[i].WorkflowName, parent: runParent{session: runs[i].ParentSessionID},
	}
	if o.parent.session == "" {
		o.err = fmt.Errorf("%w: the run inventory names no launching session", errLaunchSessionUnavailable)
		return o
	}
	var complete bool
	o.parent.chat, complete = rs.chatForSession(ctx, o.parent.session)
	if o.parent.chat == "" && !complete {
		o.err = fmt.Errorf("%w: a chat file could not be read, so which chat launched the run is unknown",
			errRunHostStart)
	}
	return o
}

// isCtxErr reports a cancellation or deadline at any depth: KAS may have taken a verb whose context ended.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Host requests and the three ask kinds reuse the chat handlers; lifecycle frames go
// workspace-global with an empty chat id; session/update goes to the run's own log
// (`runs/<workflowId>/entries.jsonl`) as run-scoped entry events.
func (rt *Runtime) dispatch(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, origin, msg)
		return
	}
	// Before the prefix test.
	if msg.Method == methodKiroSessionNotify {
		rt.runs.onNotify(ctx, chatID, msg)
		return
	}
	// Keeps the run's chat id, so a toast names the run.
	if rt.dispatchWorkspace(ctx, chatID, msg) {
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/") {
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		if msg.Method == methodWFRunComplete {
			rt.releaseEndedHost(chatID, msg)
		}
		return
	}
	if msg.Method == marotte.MethodSessionUpdate {
		rt.handleRunStepFrame(ctx, chatID, msg)
		return
	}
	slog.Debug("run host session: unhandled notification", "method", msg.Method, "chat_id", chatID)
}

// An unmatched one is refused, since silence wedges the step.
func (rt *Runtime) dispatchRequest(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) {
	switch {
	case rt.inbound.handleFSRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroFSRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroClientRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroSearchRequest(ctx, chatID, origin, msg),
		rt.inbound.handleKiroSecretRequest(ctx, chatID, origin, msg):
		return
	case strings.HasPrefix(msg.Method, methodTermPrefix):
		rt.handleTerminalRequest(ctx, chatID, origin, msg.Method, msg)
		return
	}
	if fn, ok := rt.askHandlers[msg.Method]; ok {
		fn(ctx, chatID, origin, msg)
		return
	}
	slog.Warn("run host session: refusing unexpected request", "method", msg.Method, "chat_id", chatID)
	_ = respondOn(ctx, rt.bridge.mgr, chatID, origin, msg, nil, &marotte.RPCError{
		Code:    marotte.RPCCodeMethodNotFound,
		Message: "unsupported on a run's host session: " + msg.Method,
	})
}

// releaseEndedHost closes a parentless run's host session once its run is terminal; a paused run keeps it resident,
// so resume, retry, Continue and answers need no reload. A goroutine, since this runs on the session's own forward.
func (rt *Runtime) releaseEndedHost(chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var p struct {
		Status marotte.RunStatus `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || !p.Status.Terminal() {
		return
	}
	if rt.bridge.mgr.get(chatID) == nil {
		return
	}
	slog.Info("a run ended, closing its host session", "chat_id", chatID, "status", p.Status)
	rt.lifecycle.goUnlessDraining(func() {
		rt.coord.closeBridge(rt.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
	})
}

const agentSourcePrefix = "agent://"

// agentNamePattern is KAS's own check on the name after agent://.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func isAgentSource(source string) bool { return strings.HasPrefix(source, agentSourcePrefix) }

// launchRecipe resolves a listed recipe or an agent:// source. An agent run needs a prompt, and its
// recipe name is KAS's `agent-<name>`, so one live run per agent. KAS refuses an unregistered agent.
func (rs *Runs) launchRecipe(ctx context.Context, source string, inputs map[string]string) (marotte.Recipe, error) {
	if !isAgentSource(source) {
		return rs.recipeBySource(ctx, source)
	}
	name := strings.TrimPrefix(source, agentSourcePrefix)
	if !agentNamePattern.MatchString(name) {
		return marotte.Recipe{}, fmt.Errorf("invalid agent name %q", name)
	}
	if strings.TrimSpace(inputs["prompt"]) == "" {
		return marotte.Recipe{}, errors.New("an agent run needs a prompt")
	}
	return marotte.Recipe{Name: "agent-" + name, Source: source}, nil
}

func (rs *Runs) recipeBySource(ctx context.Context, source string) (marotte.Recipe, error) {
	if source == "" {
		return marotte.Recipe{}, errMissingRecipeSource
	}
	recipes, err := rs.recipes.live(ctx, rs.lifecycle, rs.fetchRecipes)
	if err != nil {
		return marotte.Recipe{}, err
	}
	for _, r := range recipes {
		if r.Source == source {
			return r, nil
		}
	}
	return marotte.Recipe{}, fmt.Errorf("%w %q", errUnknownRecipe, source)
}

// recipeIdle enforces the single-run rule (one live run per recipe, globally) against KAS's run list, the only source that sees runs
// marotte did not launch. The leases explain a blocking row as marotte's own orphan.
// ruleID names the event rule of a triggered launch, which is handed the run list's statuses; empty for any other.
func (rs *Runs) recipeIdle(ctx context.Context, name, ruleID string) error {
	runs, err := rs.list(ctx, nil)
	if err != nil {
		// Launching blind could allow a second live run.
		return fmt.Errorf("run list unavailable: %w", err)
	}
	status := make(map[string]marotte.RunStatus, len(runs))
	for i := range runs {
		status[runs[i].WorkflowID] = runs[i].Status
	}
	rs.reconcileLeasePresence(ctx, status, time.Now(), false)
	if ruleID != "" && rs.confirmer != nil {
		rs.confirmer.PruneAbandoned(ruleID, status)
	}

	for i := range runs {
		// WorkflowName is the recipe; Name carries any stamped label, which made this guard fail open.
		if runs[i].WorkflowName != name || !runs[i].Status.Active() {
			continue
		}
		if rs.clearBlockingOrphan(ctx, runs[i].WorkflowID, runs[i].Status) {
			slog.Info("cleared a restart-orphaned run that was holding its recipe",
				"workflow_id", runs[i].WorkflowID, "recipe", name)
			continue
		}
		busy := &schedule.BusyError{WorkflowID: runs[i].WorkflowID}
		if l, held := rs.lease(busy.WorkflowID); held {
			busy.ScheduleID, busy.SlotAt = l.ScheduleID, l.SlotAt
		}
		return busy
	}
	return nil
}

// `plan` rides through as raw JSON (marotte.Recipe).
type kasRecipe struct {
	Inputs          map[string]string `json:"inputs"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	ValidationError string            `json:"validationError"`
	Source          string            `json:"source"`
	Plan            json.RawMessage   `json:"plan"`
	BuiltIn         bool              `json:"builtIn"`
}

// recipeList is the list a person opens: a fresh listing, or the held catalog once that listing outlasts
// recipeListPatience.
func (rs *Runs) recipeList(ctx context.Context) ([]marotte.Recipe, error) {
	return rs.recipes.display(ctx, rs.lifecycle, rs.fetchRecipes, recipeListPatience)
}

// fetchRecipes walks KAS's whole catalog; KAS admits every recipe file on each call.
func (rs *Runs) fetchRecipes(ctx context.Context) ([]marotte.Recipe, error) {
	raw, err := rs.engine.rawCall(ctx, "workflow listRecipes call", methodKiroWorkflowListRecipes,
		map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}})
	if err != nil {
		return nil, err
	}
	var list struct {
		Recipes []kasRecipe `json:"recipes"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]marotte.Recipe, 0, len(list.Recipes))
	for _, r := range list.Recipes {
		if r.Name == "" || r.Source == "" {
			continue
		}
		docKey, _ := translate.DocKeyFromAbs(r.Source)
		out = append(out, marotte.Recipe{
			Name:            r.Name,
			Description:     r.Description,
			ValidationError: r.ValidationError,
			Source:          r.Source,
			DocKey:          docKey,
			Inputs:          r.Inputs,
			Plan:            r.Plan,
			BuiltIn:         r.BuiltIn,
		})
	}
	return out, nil
}

// workflowNew creates the run on bridge and returns its id. parent is the run host session's id, so
// frames route back to that session. workspacePaths stays for pre-0.63.3 engines.
func (rs *Runs) workflowNew(
	ctx context.Context, bridge acpCaller, source string, inputs map[string]string,
	defaults marotte.RunDefaults, parent marotte.SessionID, label string,
) (string, error) {
	// Always a map: KAS answers "inputs is not iterable" without one.
	in := map[string]any{}
	for k, v := range inputs {
		in[k] = v
	}
	params := map[string]any{
		"workflowPath":    source,
		keyWorkspacePaths: []string{rs.lifecycle.workDir},
		"inputs":          in,
		"parentSessionId": string(parent),
	}
	if label != "" {
		params["runLabel"] = label
	}
	if defaults.Model != "" {
		params["modelId"] = defaults.Model
	}
	if defaults.Effort != "" {
		params["effortLevel"] = defaults.Effort
	}
	resp, err := bridge.Call(ctx, methodKiroWorkflowNew, params)
	if cErr := workflow.Classify(err); cErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("workflow new: kiro-cli did not answer in time and may have created the run: %w", cErr)
		}
		return "", fmt.Errorf("workflow new: %w", cErr)
	}
	var res struct {
		WorkflowID string `json:"workflowId"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil || res.WorkflowID == "" {
		return "", fmt.Errorf("%w: workflow new: reply carried no workflowId", errLaunchNewUnconfirmed)
	}
	return res.WorkflowID, nil
}

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON for a run whose owner died. A literal: only this reason licenses a cancel.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// resumableReasonPrefixes open the reasons KAS's pauseVerdict gives a stop nobody chose, licensing a resume
// only. Prefixes, because KAS rewords the tails between releases and interpolates the network code; the
// interruption arm carries no pauseDetail, so its prefix is its only arm. TestPauseReasonsMatchBundle
// (internal/kascap) checks them against the pinned agent server.
var resumableReasonPrefixes = []string{
	"Step interrupted (agent shutdown or connection reset);",
	"Model service error (service 5xx or throttling);",
	"Transient connection error (",
}

// transientErrorClass is the `pauseDetail.class` KAS stamps for every transient fault; it reaches a
// parallel branch's pause, whose prose KAS re-renders.
const transientErrorClass = "transient-error"

// pauseDetail is KAS's pause classification on the `paused` frame and in inspect, one declaration for
// the predicate path. `occurredAt` is deliberately absent: only run_ask.go reads it.
type pauseDetail struct {
	Class string `json:"class"`
	Code  string `json:"code"`
}

// resumablePause reports whether marotte should resume a pause unasked: reason OR detail, since detail-less
// pauses and re-rendered prose each need one arm. restartPaused (run_orphan.go) cancels and never reads a detail.
func resumablePause(reason string, detail *pauseDetail) bool {
	if detail != nil && detail.Class == transientErrorClass {
		return true
	}
	if reason == stalePauseReason {
		return true
	}
	return slices.ContainsFunc(resumableReasonPrefixes, func(p string) bool { return strings.HasPrefix(reason, p) })
}

// orphanResumeDelay is when the resume sweep re-reads a run KAS still lists running after an engine end:
// KAS judges a run's owner dead 135 s after its last beat (staleAfterMs, 4.5 beats of 30 s)
// and allows one beat of reader drift (kiro-cli 2.29.1). A var for tests.
var orphanResumeDelay = 165 * time.Second

// resumeInterruptedRuns resumes the restart-paused runs this chat's session chain launched, and sweeps again after
// orphanResumeDelay while KAS has not yet reconciled one; never resumeAll, which would undo deliberate pauses. Each
// later sweep is lc's work, so once the chat's life ended the sweeps stand down: its next open runs its own.
func (rs *Runs) resumeInterruptedRuns(ctx context.Context, lc *chatLifecycle, chatID marotte.ChatID) {
	if s := rs.resumeChainRuns(ctx, chatID, lc.endedNow); s.running || s.failed {
		slog.Info("rehydrate: a run is still listed running, or the chat's runs could not be listed or resumed; sweeping again later",
			"chat_id", chatID, "delay", orphanResumeDelay)
		begin := func() (context.Context, func(), bool) {
			ctx, done, ok := lc.beginWork(workSweep)
			if !ok {
				slog.Debug("rehydrate: the chat closed; its later run sweeps stand down", "chat_id", chatID)
			}
			return ctx, done, ok
		}
		rs.retryResumeLater(begin, func(ctx context.Context) bool { return rs.resumeChainRuns(ctx, chatID, lc.endedNow).failed },
			func(sweeps int) {
				slog.Error("rehydrate: the chat's runs could not be listed or resumed by any sweep; the chat's next reopen tries again",
					"chat_id", chatID, "sweeps", sweeps)
			})
	}
}

// maxResweeps bounds a resume's tries, the caller's first included (retryResumeLater).
const maxResweeps = 5

// retryResumeLater follows the caller's one try: it calls try after orphanResumeDelay, again while it reports a
// failed resume, until maxResweeps tries in all, then spent with that count. Each try runs on the context begin
// answers and calls its done when it returns; a refused begin, or a stopping runtime, tries nothing more.
func (rs *Runs) retryResumeLater(begin func() (context.Context, func(), bool), try func(context.Context) (failed bool), spent func(tries int)) {
	var after func(tries int)
	after = func(tries int) {
		if tries >= maxResweeps {
			spent(tries)
			return
		}
		rs.lifecycle.afterUnlessDraining(orphanResumeDelay, func() {
			ctx, done, ok := begin()
			if !ok {
				return
			}
			failed := try(ctx)
			done()
			if failed {
				after(tries + 1)
			}
		})
	}
	after(1)
}

// runtimeSweep is a sweep outside any chat's life, on the runtime's lifetime: it always begins.
func (rs *Runs) runtimeSweep() (context.Context, func(), bool) {
	ctx, cancel := rs.lifecycle.derivedContext()
	return ctx, cancel, true
}

// chainSweep is what one resume sweep left unreconciled: a run still listed running, which after an engine end KAS
// reads until its staleness window passes, or a run list or automatic resume that failed.
type chainSweep struct {
	running bool
	failed  bool
}

// resumeChainRuns is one resume sweep of chatID's session chain, standing down before a resume once ended reports
// the chat's life over.
func (rs *Runs) resumeChainRuns(ctx context.Context, chatID marotte.ChatID, ended func() bool) (s chainSweep) {
	rec, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return s
	}
	chain := make(map[string]bool, len(rec.PriorACPSessionIDs)+1)
	chain[rec.ACPSessionID] = true
	for _, id := range rec.PriorACPSessionIDs {
		chain[id] = true
	}

	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("rehydrate: run list unavailable, skipping resume sweep", "chat_id", chatID, "error", err)
		s.failed = true
		return s
	}
	for i := range runs {
		r := &runs[i]
		if !chain[r.ParentSessionID] {
			continue
		}
		switch marotte.RunStatus(r.Status) {
		case marotte.RunStatusPaused:
			if ended() {
				return s
			}
			if rs.resumeIfInterrupted(ctx, chatID, r.WorkflowID) == resumeFailed {
				s.failed = true
			}
		case marotte.RunStatusRunning:
			s.running = true
		}
	}
	// The chat's session is resident again, so every run it hosts is read again.
	rs.kickChat(chatID)
	return s
}

// maxAutoHeals bounds automatic resumes between two pieces of progress; a fourth try against a dead network says nothing.
const maxAutoHeals = 3

// healBaseDelay is the first automatic resume's delay, doubling (5s, 10s, 20s), so a deliberate cancel
// can land first. A var for tests.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS just parked for a reason nobody chose, and finds the gate's own landed pause
// (findHolds), off the `_kiro/workflow/paused` frame. After `next`, so the client renders the pause first.
func (rs *Runs) healPaused(
	next func(context.Context, marotte.ChatID, *marotte.RPCResponse),
) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		next(ctx, chatID, msg)
		f := decodePauseFrame(msg)
		if f.WorkflowID == "" {
			return
		}
		if isHoldReason(f.InitiatorReason) {
			// Every run kind's: a parentless run's frame names no chat.
			rs.findHoldsAsync(f.WorkflowID)
			return
		}
		if chatID == "" {
			chatID = rs.parentlessRunChat(f.WorkflowID)
		}
		if chatID == "" {
			return
		}
		if !resumablePause(f.PauseReason, f.PauseDetail) {
			// Without this a decline is silent; Debug because deliberate pauses land here too.
			slog.Debug("a paused run was left alone: its pause is not one marotte resumes unasked",
				"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
				"pause_reason", scrubLog(f.PauseReason),
				"pause_class", scrubLog(pauseClassOf(f.PauseDetail)),
				"pause_code", scrubLog(pauseCodeOf(f.PauseDetail)))
			return
		}
		// The resume claims the heal (resumeIfInterrupted); the count here spaces the attempts and stops them at the
		// budget.
		spent := rs.healsSpent(f.WorkflowID)
		if spent >= maxAutoHeals {
			// At once: past the budget there is no delay to space.
			rs.lifecycle.goUnlessDraining(func() {
				hctx, cancel := rs.lifecycle.derivedContext()
				defer cancel()
				rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
			})
			return
		}
		delay := healBaseDelay * time.Duration(1<<spent)
		slog.Info("scheduling the automatic resume of an involuntarily paused run",
			"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
			"pause_reason", scrubLog(f.PauseReason), "delay", delay)
		rs.lifecycle.afterUnlessDraining(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// parentlessRunChat is the chat id a parentless run's heal is addressed by, as resumeParentless addresses it; "" for
// an agent's run, whose frames name its chat, and for a run marotte holds no lease for.
func (rs *Runs) parentlessRunChat(workflowID string) marotte.ChatID {
	if l, held := rs.lease(workflowID); held && l.Origin != runlease.OriginAgent {
		return runChatID(workflowID)
	}
	return ""
}

// pauseFrame is the fields the heal reads off `_kiro/workflow/paused`, decoded separately
// from lifecycleFrame. Pointer first for govet's fieldalignment.
type pauseFrame struct {
	PauseDetail     *pauseDetail `json:"pauseDetail"`
	WorkflowID      string       `json:"workflowId"`
	PauseReason     string       `json:"pauseReason"`
	InitiatorReason string       `json:"initiatorReason"`
}

// pauseClassOf names a pause's class for a log line only; predicates compare the field directly.
func pauseClassOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Class
}

func pauseCodeOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Code
}

// unmarshalKeepingReadable decodes data into dst, keeping every field that decoded when one field's type
// drifted: `encoding/json` finishes the object on a type mismatch. A syntax error is not tolerated.
func unmarshalKeepingReadable(data []byte, dst any) bool {
	err := json.Unmarshal(data, dst)
	if err == nil {
		return true
	}
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func decodePauseFrame(msg *marotte.RPCResponse) pauseFrame {
	var f pauseFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if !unmarshalKeepingReadable(msg.Params, &f) {
		return pauseFrame{}
	}
	return f
}

// healProgress refills a run's per-stall budgets when a node completes.
func (rs *Runs) healProgress(
	next func(context.Context, marotte.ChatID, *marotte.RPCResponse),
) func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// The cancel ladder too: a refusal speaks only for its moment (maxCancelRetries).
			rs.clearCancelRetries(f.WorkflowID)
			// And the idle window. Both run populations reach this: the run host session reuses chatHandlers for every `_kiro/workflow/*` method.
			rs.refillDeadline(ctx, f.WorkflowID)
		}
		next(ctx, chatID, msg)
	}
}

type nodeFrame struct {
	WorkflowID string `json:"workflowId"`
}

func decodeNodeFrame(msg *marotte.RPCResponse) nodeFrame {
	var f nodeFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return nodeFrame{}
	}
	return f
}

// resumeOutcome is what one automatic resume did; only resumeFailed is worth another try.
type resumeOutcome int

const (
	resumeWritten resumeOutcome = iota + 1
	// resumeSkipped: not a pause marotte resumes unasked, or the run's heal budget is spent (recorded as marotte's
	// stop, no pause written), or a scheduled run ended.
	resumeSkipped
	resumeHeld
	// resumeFailed: the run could not be read, its session made resident or its resume written, or a scheduled run
	// past its heal budget could not be ended.
	resumeFailed
)

// resumeIfInterrupted is every automatic resume: it resumes a run paused for a cause nobody chose under the run's
// heal budget, or holds it while the sign-in gate is closed.
func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID marotte.ChatID, workflowID string) resumeOutcome {
	out, _ := rs.retryResume(ctx, chatID, workflowID, false)
	return out
}

// retryResume is resumeIfInterrupted for one try of a resume its caller retries: charged says an earlier try's heal
// still stands, so this try claims none. It reports whether a heal stands after this try. runTransitions decides it.
func (rs *Runs) retryResume(
	ctx context.Context, chatID marotte.ChatID, workflowID string, charged bool,
) (resumeOutcome, bool) {
	c, _ := rs.transition(ctx, workflowID, &runRequest{want: healResume, chatID: chatID, charged: charged})
	return c.outcome, c.charged
}

func isStatusRefusal(err error) bool {
	_, _, ok := command.StatusOf(err)
	return ok
}

// errRunListPartial is a workflow/list reply with no runs array: it proves no run absent, so it is no empty list.
var errRunListPartial = errors.New("the run list reply carries no runs array")

// listRaw lists runs with their raw parent session ids, for callers that scope by
// session chain rather than by resolved chat. A reply with no runs array is errRunListPartial.
func (rs *Runs) listRaw(ctx context.Context) ([]kasWorkflowRun, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	// workspacePaths is a required array (methodKiroWorkflowList).
	raw, err := rs.engine.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}})
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	if list.Runs == nil {
		return nil, errRunListPartial
	}
	return *list.Runs, nil
}

// deleteForSessions deletes every run these sessions launched, whatever its status: KAS refuses a session
// delete while the session owns a live run.
func (rs *Runs) deleteForSessions(ctx context.Context, chatID marotte.ChatID, sessionChain []string, stop runStop) {
	if len(sessionChain) == 0 {
		return
	}
	// The same test RunHolders attributes every run keep fact by, so this deletes exactly the runs whose facts
	// would have kept the chat.
	owners := chat.OwnersOfChain(chatID, sessionChain)
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("delete: run list unavailable, skipping run delete", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if len(owners.Of(r.ParentSessionID)) == 0 {
			continue
		}
		if dErr := rs.deleteAs(ctx, r.WorkflowID, stop); dErr != nil {
			slog.Warn("delete: run delete failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", dErr)
		}
	}
}
