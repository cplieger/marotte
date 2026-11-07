package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
)

// Runs owns the workflow-run surface: launch, cancel, retry, the durable lease, the deadline and turn-cap
// arms, and the launch-rule store. It holds collaborators, not a *Runtime; `mu` guards bounds.
type Runs struct {
	chats       runChatReader
	translate   runTranslator
	perms       runPermClaimer
	bus         runBroadcaster
	launchRules *launchrule.Store `wiring:"optional"`
	leases      *runlease.Store   `wiring:"optional"`
	eventRules  eventRuleChecker  `wiring:"optional"`
	confirmer   TriggerConfirmer  `wiring:"optional"`
	// launchLocks serializes, per recipe name, a launch's single-run check through its lease grant (launchAs).
	launchLocks *keylock.Set[string]
	// log is the run record: one entry log per run under <configDir>/runs, its open step turns and hosts.
	log     *runLog `wiring:"optional"`
	bridges *bridgeManager
	coord   *bridgeCoordinator
	// notices queues per launching chat the finished runs whose notice KAS put in its steering buffer (run_notices.go).
	notices map[marotte.ChatID][]runNotice
	// plans holds each run's newest plan revision (run_plan.go), under mu.
	plans runPlans
	// recipes shares one listRecipes call among its readers and holds the last answer for GET /api/recipes alone.
	recipes recipeCatalog
	// engine is the one agent engine every read and stop is an engine call on; router routes the run's frames.
	engine *engineOwner
	router *engineRouter
	// mutations are the driving and stopping writes whose effect is not observed yet (run_mutations.go).
	mutations *runMutations
	// answerEarly answers a request a new host session's door sent before its run's lane was known.
	answerEarly func(s engineSession, msg *marotte.RPCResponse)
	// doorSettings is a host door's inputs (Runtime.readSessionSettings).
	doorSettings func(context.Context) sessionSettings
	// servedModels is GET /api/config-template's model list (Runtime.servedModels), for checkRunDefaults.
	servedModels func(context.Context) ([]marotte.SessionModel, bool)
	// dropMCPSessions ends the MCP views of a deleted run's step sessions (mcpRegistry.dropSessions).
	dropMCPSessions func(context.Context, []marotte.SessionID)
	// resetFailedMCP re-attempts the failed MCP servers before a scheduled run (mcpRegistry.resetFailedForLaunch).
	resetFailedMCP func(context.Context)
	// runEnded asks the retention purge for a pass when a run ends.
	runEnded  func() `wiring:"optional"`
	lifecycle *lifetime
	// workDir is the root projected diff paths are relative to, a value so step tests need no lifetime.
	workDir string
	steers  *stepSteers
	// asks is the ask core's per-run cells (run_asks.go).
	asks runAsks
	// stepReplays holds the step-transcript reads in flight (step_replay.go).
	stepReplays stepReplays
	// unconfirmed answers a retried step message whose engine died before replying (run_message.go).
	unconfirmed unconfirmedSends
	// life admits every step-session mutation against a Delete (run_lifecycle.go).
	life runLifecycle
	// owners serializes, per run, an abandoned run's cancel and release against a retry and a triggered launch's
	// lease grant (run_abandon.go).
	owners runOwners
	// purgeFailed holds the runs whose finished-run purge delete failed recently (run_purge.go).
	purgeFailed purgeFailures
	// purgePace is when the finished-run purge last reached a delete (run_purge.go).
	purgePace deletePacer
	// logSweep is the orphaned run-log sweep's poison set and pace (run_log_sweep.go).
	logSweep logSweepState
	// recorded is the keep-state reconcile's runs/ attribution, which RunsParentedBy reads (run_owner.go).
	recorded recordedRuns
	// marks throttles the run keep marks' progress refresh and keeps ended unattributed runs (run_keepstate.go).
	marks runMarks
	// retries marks each run whose retry KAS has not shown taking effect, the purge's retry in flight (run_purge.go).
	retries retryMarks
	// sweep pauses the running runs while the gate is closed (run_gate.go).
	sweep gateSweep
	// hosts serializes, per run, making its launching session resident (ensureResident). hosts, resumes and
	// sweep.turns are built in New; a nil set panics on its first lock.
	hosts *keylock.Set[string]
	// resumes serializes, per run, every resume marotte writes, so an automatic one reads the run after any other.
	resumes *keylock.Set[string]
	bounds  runBoundsState
	// signals watches for KAS moving a paused step's lane on early (run_signal_tripwire.go).
	signals signalTripwire
	// gate holds run work while the engine's sign-in cannot be answered (run_gate.go).
	gate runGate
	// cancelRetryBase is the refused-cancel ladder's first wait (retryTermination), a field because untracked
	// timers can outlive whoever set a package var. Zero means defaultCancelRetryBase.
	cancelRetryBase time.Duration
	mu              sync.Mutex
}

// runChatReader is the chat store as the run surface uses it: session chains, the chat owning a run's parent session,
// and the run keep facts.
type runChatReader interface {
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	Lookup(ctx context.Context, id marotte.ChatID) (c *marotte.Chat, found bool, err error)
	// SessionOwners is one fresh owner scan, the attribution every run keep fact and the purge teardown share.
	SessionOwners(ctx context.Context) chat.OwnerScan
	HoldOrPend(ctx context.Context, id marotte.ChatID, key string, scope chat.KeepScope) (chat.Claim, error)
	Release(ctx context.Context, id marotte.ChatID, key string, ev chat.Evidence) error
	EndStop(ctx context.Context, claim *chat.Claim)
	HoldUnattributed(workflowID string)
	ClearUnattributed(workflowID string)
	SessionInTeardown(sessionID string) bool
}

// useLog installs the run record.
func (rs *Runs) useLog(l *runLog) {
	rs.log = l
}

// ruleRun names the rule row a lease's run reports to: a clock rule's ScheduleID, an event rule's RuleID, neither
// for a manual or agent run.
func ruleRun(l *runlease.Lease) launchrule.Run {
	return launchrule.Run{StartedAt: l.StartedAt, ScheduleID: l.ScheduleID, RuleID: l.RuleID, WorkflowID: l.WorkflowID}
}

// recordScheduleOutcome overwrites run's outcome on its row (launchrule.Store.RecordOutcome). An event rule's run goes
// to the confirmer, which lands it only for the run that confirmed the rule's event, so an abandoned attempt's run and
// a deleted rule's run write nothing. No rule or no store is a no-op.
func (rs *Runs) recordScheduleOutcome(ctx context.Context, run launchrule.Run, outcome launchrule.Outcome) {
	if run.RuleID != "" {
		if rs.confirmer != nil {
			rs.confirmer.RecordOutcome(run.RuleID, run.WorkflowID, outcome)
		}
		return
	}
	rs.writeSchedule(run, func() error { return rs.launchRules.RecordOutcome(ctx, run, outcome) })
}

// recordScheduleEnding fills run's ending on a row still reading started (launchrule.Store.RecordEnding).
func (rs *Runs) recordScheduleEnding(ctx context.Context, run launchrule.Run, status marotte.RunStatus, stopReason string) {
	rs.recordSlotEnding(ctx, run, runEnding(status, stopReason))
}

// recordSlotEnding fills run's ending, already worded as an Outcome, on a row still reading started (launchrule.Store.RecordEnding).
func (rs *Runs) recordSlotEnding(ctx context.Context, run launchrule.Run, outcome launchrule.Outcome) {
	if run.RuleID != "" {
		if rs.confirmer != nil {
			rs.confirmer.RecordEnding(run.RuleID, run.WorkflowID, outcome)
		}
		return
	}
	rs.writeSchedule(run, func() error { return rs.launchRules.RecordEnding(ctx, run, outcome) })
}

func (rs *Runs) writeSchedule(run launchrule.Run, write func() error) {
	if rs.launchRules == nil || run.ScheduleID == "" || run.RuleID != "" {
		return
	}
	switch err := write(); {
	case errors.Is(err, launchrule.ErrNotFound):
		slog.Info("schedule outcome not recorded: the schedule was deleted during its run",
			"schedule_id", run.ScheduleID, "workflow_id", run.WorkflowID)
	case err != nil:
		slog.Warn("could not record the schedule's outcome",
			"schedule_id", run.ScheduleID, "workflow_id", run.WorkflowID, "error", err)
	}
}

// runEnding is the row's outcome for a terminal status: completed, or failed with KAS's stopReason, else a
// sentence naming the status.
func runEnding(status marotte.RunStatus, stopReason string) launchrule.Outcome {
	stopReason = launchrule.ReasonText(stopReason)
	switch status {
	case marotte.RunStatusCompleted:
		return launchrule.Outcome{Status: launchrule.StatusCompleted}
	case marotte.RunStatusAborted:
		return launchrule.Outcome{Status: launchrule.StatusFailed, Reason: cmp.Or(stopReason, "the run was aborted")}
	case marotte.RunStatusCancelled:
		return launchrule.Outcome{Status: launchrule.StatusFailed, Reason: cmp.Or(stopReason, "the run was cancelled")}
	default:
		return launchrule.Outcome{Status: launchrule.StatusFailed, Reason: cmp.Or(stopReason, "the run failed")}
	}
}

type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	RecordInspect(ctx context.Context, raw json.RawMessage)
	// RunName is the run's display name the translator has learned, "" when none yet.
	RunName(workflowID string) string
	// ForgetRunSteps drops a run's step-session entries; the gate is the caller's, since `paused` reaches `run_complete` on a live run.
	ForgetRunSteps(workflowID string)
	// SessionNotifySignal reads a `_kiro/session/notify` frame naming a run, or false.
	SessionNotifySignal(msg *marotte.RPCResponse) (translate.NotifySignal, bool)
	// HandleWorkflowMessage records the workflow message a `_kiro/session/notify` frame carries.
	HandleWorkflowMessage(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
}

// runBroadcaster publishes a run's asks, the only run event marotte originates.
type runBroadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// runPermClaimer is the decision tracker as the run surface uses it: claim, and drop a run's decisions.
// Run-scoped because a step's ask is keyed to the launching chat.
type runPermClaimer interface {
	TakePendingPermOn(origin acpResponder, acpID int64, settledBy marotte.SettledBy) (command.AskReply, bool)
	ClearPendingPermsForRun(ctx context.Context, workflowID string)
}

// Runs exposes the run surface to the composition root (orphan sweep, schedule.Launcher).
func (rt *Runtime) Runs() *Runs { return rt.runs }
