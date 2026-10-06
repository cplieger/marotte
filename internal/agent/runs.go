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

// Runs owns the workflow-run surface: launch, cancel, retry, the durable lease, the
// deadline and turn-cap arms, and the schedule store that drives unattended launches.
// It carries COLLABORATORS rather than a *Runtime back-pointer; the ACP request
// ladder for a run's bridge stays on Runtime. `mu` guards bounds.
type Runs struct {
	chats     runChatReader
	translate runTranslator
	perms     runPermClaimer
	bus       runBroadcaster
	schedules *schedule.Store `wiring:"optional"`
	leases    *runlease.Store `wiring:"optional"`
	// log is the run record: one entry log per run under <configDir>/runs, the
	// open step turns and the host each run's death closer reaches through.
	log     *runLog `wiring:"optional"`
	bridges *bridgeManager
	coord   *BridgeCoordinator
	// notices queues, per launching chat, the finished runs whose completion notice
	// KAS put in that chat's steering buffer; run_notices.go.
	notices map[marotte.ChatID][]runNotice
	// terminals answers whether a run's carrier is waiting on a live shell command,
	// the one liveness the idle window cannot read off frames.
	terminals runTerminalReader `wiring:"optional"`
	utility   func() *utilityRuntime
	lifecycle *lifetime
	// workDir is the workspace root a projected diff path is made relative to, carried
	// as a VALUE rather than read off lifecycle: a step-transcript read then needs no
	// lifetime, which is what lets the step tests build the bare Runs they build.
	workDir string
	// asks holds the questions a step asked and nobody answered — its own registry
	// because a run ask is durable where a permission dies with its bridge. run_ask.go.
	asks pendingRunAsks
	// stepReplays holds the step-transcript reads in flight (step_replay.go). HERE
	// rather than on the utility session, which must stay ignorant of workflows.
	stepReplays stepReplays
	// carriers counts which run carriers a verb is holding right now — the one fact
	// the kept-carrier bound cannot infer from a timeout (run_host.go).
	carriers carrierUse
	// positions serializes a run's positional step-status write with its heal's
	// resume: `_kiro/workflow/update` targets a step positionally, so a resume landing
	// between SetStepStatus's read and its write would mark another step.
	positions runLocks
	// hosts serializes, per run, finding or starting the carrier a verb runs on with
	// entering it (acquireHost), so one unhosted run is loaded once.
	hosts  runLocks
	bounds runBoundsState
	// cancelRetryBase is the first wait of a refused cancel's re-attempt ladder
	// (retryTermination). A field rather than a package var because the ladder runs on
	// untracked timers that can outlive whoever set the value. Set once, before the
	// first cancel; zero means defaultCancelRetryBase.
	cancelRetryBase time.Duration
	mu              sync.Mutex
}

// runChatReader is the chat store as the run surface uses it: a chat's session
// chain, and (via ListComplete) the chat owning a given run's parent session.
type runChatReader interface {
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// ListComplete also reports whether every existing chat was read, which is what
	// lets a run's launching session be proved parentless.
	ListComplete(ctx context.Context) ([]marotte.ChatHeader, bool)
}

// recordScheduleOutcome puts a run's ending on the launching SCHEDULE's row. It is
// the one writer for that fact, because the four paths that end a run unattended
// each owe the same statement and a path that forgets it leaves the row reading
// "started" while the schedule silently stops producing — which is invisible from
// outside, since a wedge and a long run look alike from the log. A run with no
// schedule behind it, or a store that is not wired, is a no-op rather than an
// error: the outcome has nowhere to go and the run's own row already carries it.
func (rs *Runs) recordScheduleOutcome(ctx context.Context, scheduleID string, outcome schedule.Outcome) {
	if rs.schedules == nil || scheduleID == "" {
		return
	}
	if err := rs.schedules.RecordOutcome(ctx, scheduleID, outcome); err != nil {
		slog.Warn("could not record the schedule's outcome",
			"schedule_id", scheduleID, "error", err)
	}
}

// runTranslator is the translator as the run surface uses it: the two run-shaped
// notifications it wraps, the step-session seed, and one ask decode.
type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse)
	RecordRunSteps(raw json.RawMessage)
	// ForgetRunSteps drops a run's step-session registry entries. The GATE is this
	// side's, because `paused` reaches `run_complete` on a run still going.
	ForgetRunSteps(workflowID string)
	// SessionNotifyAsk derives the ask a `_kiro/session/notify` frame carries, or
	// reports false. A DERIVATION: this surface owns the ask's whole lifecycle.
	SessionNotifyAsk(msg *marotte.RPCResponse) (marotte.RunInputNeededPayload, bool)
}

// runTerminalReader is the agent-terminal registry as the run surface uses it: is
// one of the sessions a run's own open steps named waiting on a live command. The
// question is session-scoped rather than chat-scoped because a parallel run is
// several steps on ONE carrier chat, so a chat-wide answer reports every step as
// working while any one of them, or the chat's own conversation, holds a shell.
type runTerminalReader interface {
	LiveTerminalForSession(chatID marotte.ChatID, sessions map[string]struct{}) bool
}

// runBroadcaster is the event fan-out as the run surface uses it: publish an ask and
// its settlement. The ask is the only run event marotte itself originates.
type runBroadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// runPermClaimer is the pending-decision tracker as the run surface uses it: claim a
// request so exactly one surface answers it, and drop a run's unanswered decisions
// when it ends. The run-terminal clear is here rather than on the chat-scoped door
// because a step's ask is keyed to the LAUNCHING chat, which outlives the run.
type runPermClaimer interface {
	TakePendingPerm(chatID marotte.ChatID, requestID int64, settledBy marotte.SettledBy) bool
	ClearPendingPermsForRun(workflowID string)
}

// Runs exposes the run surface to the composition root, which starts the orphan
// sweep and hands it to the schedule runner as its schedule.Launcher.
func (rt *Runtime) Runs() *Runs { return rt.runs }
