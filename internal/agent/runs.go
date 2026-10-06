package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// Runs owns the workflow-run surface: launch, cancel, retry, the durable lease, the deadline and turn-cap
// arms, and the schedule store. It holds collaborators, not a *Runtime; `mu` guards bounds.
type Runs struct {
	// locks answers the governance lock map a run bridge's spawn composes from.
	locks     func() map[string]marotte.GovernanceLock
	chats     runChatReader
	translate runTranslator
	perms     runPermClaimer
	bus       runBroadcaster
	schedules *schedule.Store `wiring:"optional"`
	leases    *runlease.Store `wiring:"optional"`
	// log is the run record: one entry log per run under <configDir>/runs, its open step turns and hosts.
	log     *runLog `wiring:"optional"`
	bridges *bridgeManager
	coord   *BridgeCoordinator
	// notices queues per launching chat the finished runs whose notice KAS put in its steering buffer (run_notices.go).
	notices map[marotte.ChatID][]runNotice
	// terminals answers whether a run's carrier waits on a live shell command, which frames cannot show.
	terminals runTerminalReader `wiring:"optional"`
	utility   func() *utilityRuntime
	// runEnded asks the retention purge for a pass when a run ends.
	runEnded  func() `wiring:"optional"`
	lifecycle *lifetime
	// workDir is the root projected diff paths are relative to, a value so step tests need no lifetime.
	workDir string
	// asks holds unanswered step questions, its own registry because a run ask outlives its bridge (run_ask.go).
	asks pendingRunAsks
	// stepReplays holds the step-transcript reads in flight (step_replay.go), here so the utility session stays workflow-agnostic.
	stepReplays stepReplays
	// carriers counts which run carriers a verb holds now (run_host.go).
	carriers carrierUse
	// positions serializes a run's positional step-status write with its heal's resume, or a resume between read and write marks another step.
	positions runLocks
	// hosts serializes, per run, finding or starting a verb's carrier with entering it (acquireHost).
	hosts  runLocks
	bounds runBoundsState
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

// recordScheduleOutcome puts a run's ending on the launching schedule's row, the one writer for the four
// unattended endings. No schedule or no store is a no-op.
func (rs *Runs) recordScheduleOutcome(ctx context.Context, scheduleID string, outcome schedule.Outcome) {
	if rs.schedules == nil || scheduleID == "" {
		return
	}
	if err := rs.schedules.RecordOutcome(ctx, scheduleID, outcome); err != nil {
		slog.Warn("could not record the schedule's outcome",
			"schedule_id", scheduleID, "error", err)
	}
}

// runTranslator is the translator as the run surface uses it.
type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	RecordRunSteps(raw json.RawMessage)
	// ForgetRunSteps drops a run's step-session entries; the gate is the caller's, since `paused` reaches `run_complete` on a live run.
	ForgetRunSteps(workflowID string)
	// SessionNotifyAsk derives the ask a `_kiro/session/notify` frame carries, or false.
	SessionNotifyAsk(msg *marotte.RPCResponse) (marotte.RunInputNeededPayload, bool)
}

// runTerminalReader asks whether a session a run's own open steps named waits on a live command,
// session-scoped because a parallel run's steps share one carrier.
type runTerminalReader interface {
	LiveTerminalForSession(chatID marotte.ChatID, sessions map[string]struct{}) bool
}

// runBroadcaster publishes an ask and its settlement, the only run events marotte originates.
type runBroadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// runPermClaimer is the decision tracker as the run surface uses it: claim, drop a run's decisions, name
// steps owed an answer. Run-scoped because a step's ask is keyed to the launching chat.
type runPermClaimer interface {
	TakePendingPerm(chatID marotte.ChatID, requestID int64, settledBy marotte.SettledBy) bool
	ClearPendingPermsForRun(workflowID string)
	PendingDecisionNodesForRun(workflowID string) map[string]struct{}
}

// Runs exposes the run surface to the composition root (orphan sweep, schedule.Launcher).
func (rt *Runtime) Runs() *Runs { return rt.runs }
