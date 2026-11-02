package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/keylock"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/translate"
)

// Runs owns the workflow-run surface: launch, cancel, retry, the durable lease, the deadline and turn-cap
// arms, and the schedule store. It holds collaborators, not a *Runtime; `mu` guards bounds.
type Runs struct {
	chats     runChatReader
	translate runTranslator
	perms     runPermClaimer
	bus       runBroadcaster
	schedules *schedule.Store `wiring:"optional"`
	leases    *runlease.Store `wiring:"optional"`
	// log is the run record: one entry log per run under <configDir>/runs, its open step turns and hosts.
	log     *runLog `wiring:"optional"`
	bridges *bridgeManager
	coord   *bridgeCoordinator
	// notices queues per launching chat the finished runs whose notice KAS put in its steering buffer (run_notices.go).
	notices map[marotte.ChatID][]runNotice
	// plans holds each run's newest plan revision (run_plan.go), under mu.
	plans runPlans
	// engine is the one agent engine every read and stop is an engine call on; router routes the run's frames.
	engine *engineOwner
	router *engineRouter
	// mutations are the driving and stopping writes whose effect is not observed yet (run_mutations.go).
	mutations *runMutations
	// answerEarly answers a request a new host session's door sent before its run's lane was known.
	answerEarly func(s engineSession, msg *marotte.RPCResponse)
	// servedModels is GET /api/config-template's model list (Runtime.servedModels), for checkRunDefaults.
	servedModels func(context.Context) ([]marotte.SessionModel, bool)
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
	// sweep pauses the running runs while the gate is closed (run_gate.go).
	sweep gateSweep
	// hosts serializes, per run, making its launching session resident (ensureResident). hosts, resumes and
	// sweep.turns are built in New; a nil set panics on its first lock.
	hosts *keylock.Set[string]
	// resumes serializes, per run, every resume marotte writes, so an automatic one reads the run after any other.
	resumes *keylock.Set[string]
	bounds  runBoundsState
	// gate holds run work while the engine's sign-in cannot be answered (run_gate.go).
	gate runGate
	// cancelRetryBase is the refused-cancel ladder's first wait (retryTermination), a field because untracked
	// timers can outlive whoever set a package var. Zero means defaultCancelRetryBase.
	cancelRetryBase time.Duration
	mu              sync.Mutex
}

// runChatReader is the chat store as the run surface uses it: session chains and the chat owning a run's parent session.
type runChatReader interface {
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// ListComplete also reports whether every chat was read, needed to prove a launch session parentless.
	ListComplete(ctx context.Context) ([]marotte.ChatHeader, bool)
}

// useLog installs the run record.
func (rs *Runs) useLog(l *runLog) {
	rs.log = l
}

// No schedule or no store is a no-op.
func (rs *Runs) recordScheduleOutcome(ctx context.Context, scheduleID string, outcome schedule.Outcome) {
	if rs.schedules == nil || scheduleID == "" {
		return
	}
	if err := rs.schedules.RecordOutcome(ctx, scheduleID, outcome); err != nil {
		slog.Warn("could not record the schedule's outcome",
			"schedule_id", scheduleID, "error", err)
	}
}

type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	RecordRunSteps(raw json.RawMessage)
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
	ClearPendingPermsForRun(workflowID string)
}

// Runs exposes the run surface to the composition root (orphan sweep, schedule.Launcher).
func (rt *Runtime) Runs() *Runs { return rt.runs }
