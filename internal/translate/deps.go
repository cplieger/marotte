package translate

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/turnlog"
)

// turnAccess resolves the accumulator a frame's content folds into and files the
// entries that belong to no open turn.
type turnAccess interface {
	// OwnTurn returns the chat's own open turn without opening one, for a frame
	// that may fold into a turn but must never start one.
	OwnTurn(chatID marotte.ChatID) (*turnlog.Turn, bool)
	// TurnFoldTarget returns the chat's own open turn, opening a wire_turn_start
	// turn when none is open. Nil when the open was refused (a dead ctx, a write
	// error), and the caller drops the frame. Never called with a step frame: a
	// step's content is the run log's.
	TurnFoldTarget(ctx context.Context, chatID marotte.ChatID) *turnlog.Turn
	// PromptTurn returns the chat's prompt-class turn awaiting or holding its
	// bracket, the one a turn_bind joins; false when none is open.
	PromptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool)
	// StopRequestedAfter reports whether the reader asked to stop after the named turn
	// opened; false for a turn the chat does not hold (a run's step turn).
	StopRequestedAfter(chatID marotte.ChatID, turnID string) bool
	// AppendBetweenTurns files a chat's lane-less entry after its newest turn's
	// turn_close, minting a closed event carrier on an empty log. The carrier's
	// turn_open and turn_close, when it minted one, come back for the announcement.
	AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) ([]*marotte.Entry, error)
}

// RunStep names the step a run-log turn opens for: NodePath is its workflow.PathKey
// and NodeID rides beside it, since the key is never split back.
type RunStep struct {
	RunID     string
	NodePath  string
	NodeID    string
	SessionID string
	// Path is NodePath's segments when the opening frame carried them, since the key is never split back.
	Path []string
}

// RunAppender is the run registry as the content handlers reach it for a step's
// frame: the run's log keys a turn on the node PATH, so every method takes both.
type RunAppender interface {
	// RunNodeStart opens the step's turn on the run's node_start frame, the run
	// turn's opening bracket, recording chatID as the run's host when it is the
	// run's first turn; a path already open is a no-op on the log.
	RunNodeStart(ctx context.Context, step *RunStep, chatID marotte.ChatID)
	// RunNodeComplete closes the turn of the step at path (its workflow.PathKey)
	// with KAS's status mapped onto the outcome, the run turn's closing bracket; a
	// path with no open turn closes nothing.
	RunNodeComplete(ctx context.Context, runID string, path []string, status, reason string)
	// RunFoldTarget returns the run's open turn for the step, opening one when its
	// path has no turn yet and recording chatID as the run's host. False for a
	// path whose turn already closed, where RunAppendAfterClosed files a late entry,
	// and for an open the store refused.
	RunFoldTarget(ctx context.Context, step *RunStep, chatID marotte.ChatID) (*turnlog.Turn, bool)
	// RunAppendAfterClosed files an entry after the turn_close of the path's newest
	// closed turn, the run log's between-turns rule.
	RunAppendAfterClosed(ctx context.Context, runID, nodePath string, e *marotte.Entry) error
	// RunMeter folds a step's turn_completion into its open turn's aggregate; false
	// when the path has no open turn, which the caller logs at Debug.
	RunMeter(runID, nodePath string, credits, elapsedMs float64) bool
	// RunStopReason records a step's last turn_end stop reason on its open turn;
	// false when the path has no open turn.
	RunStopReason(runID, nodePath string, raw marotte.StopReason) bool
	// RunNodePaused ends the step's steering turn at its node_paused: the execution reading the
	// buffer has stopped. The log's turn stays open.
	RunNodePaused(ctx context.Context, runID string, path []string)
	// RunSteer files a step steer's durable entry in the path's open turn, else after its newest
	// closed one.
	RunSteer(ctx context.Context, runID, nodePath, steerID string, steer *marotte.EntrySteer)
	// RunSteerDelivered files a workflow message's take-up where RunSteer would file a steer.
	RunSteerDelivered(ctx context.Context, runID, nodePath string, d *marotte.EntrySteerDelivered)
	// RunWaitingWorkflowMessages is the run log's workflow messages no take-up has settled, oldest
	// first, every step's.
	RunWaitingWorkflowMessages(ctx context.Context, runID string) ([]marotte.WorkflowMessage, error)
	// RunPlanUpdate records a run's queued plan revision, or settles it with KAS's outcome.
	RunPlanUpdate(ctx context.Context, chatID marotte.ChatID, runID string, u marotte.RunPlanUpdate)
	// RunStepReading reports whether the step a StepSteerKey names is mid-turn, reading its buffer.
	RunStepReading(key marotte.ChatID) bool
}

// turnBoundary is the wire's own turn bracket, which KAS emits for every
// turn, agent-initiated included.
type turnBoundary interface {
	// WireTurnStart binds the bracket to the pending pre-open, or closes a turn
	// whose own end never arrived.
	WireTurnStart(ctx context.Context, chatID marotte.ChatID)
	// WireTurnEnd closes the chat's open turn with the wire's own outcome. A no-op
	// when none is open. The wire carries no stop prose, so the failure reason is
	// the outcome's own default.
	WireTurnEnd(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason)
	// ReviseTurnBinding re-targets routing on an agent-initiated frame: a new
	// wire_turn_start turn takes own and the prompt's turn drops back to pending.
	ReviseTurnBinding(ctx context.Context, chatID marotte.ChatID)
}

// lineRecorder records the changed lines a frame's diffs describe.
type lineRecorder interface {
	RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string)
}

// sentSteers answers whose words a mid-turn steer this server sent carries, total
// by construction: an unknown id still gets an answer.
type sentSteers interface {
	SteerOrigin(chatID marotte.ChatID, steerID string) marotte.SteerOrigin
}

// steerBuffer is the host's record of KAS's steering buffer and of the user's own steers:
// nothing can read that buffer back. Separate from sentSteers because the lifetimes differ
// (a TTL'd origin vs KAS's buffer).
type steerBuffer interface {
	// SteerWaiting folds steering_queued; true means an agent row the caller broadcasts.
	SteerWaiting(chatID marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool)
	// SteerForgotten folds an acknowledgement-evidenced read and answers what was
	// read, with its text, which no frame carries here, and in Replaces the row keys
	// an id that is not a row's own carries.
	SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload
	// SteerRead folds steering_injected for the one id it names and answers the row
	// keys that id carries when it is not a row's own (nil otherwise).
	SteerRead(chatID marotte.ChatID, steerID string) []string
	// SteerCleared folds steering_cleared and answers the AGENT rows it named. A
	// user row gets no entry at a clear: the host writes one at the row's own
	// terminal transition, so a row resent after the clear is never noted unread.
	SteerCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload
}

// chatRecords is the chat store's HEADER as this package uses it. Every write
// lands after the frame that caused it, so chat.ErrTombstoned is an expected
// outcome here.
type chatRecords interface {
	// Get returns the chat header at id, or false if it does not exist.
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// Mutate is the header write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	// PromptTexts is every prompt the chat's log holds, in turn order: what KAS
	// derives a session title from, so the focus filter can tell a derivation
	// from an agent's own title.
	PromptTexts(ctx context.Context, id marotte.ChatID) ([]string, error)
	// EmptyCompactions is how many empty-summary compaction entries the chat's log
	// holds, which numbers the next one's id.
	EmptyCompactions(ctx context.Context, id marotte.ChatID) (int, error)
	// WaitingWorkflowMessages is the chat log's workflow messages no take-up has settled, oldest first.
	WaitingWorkflowMessages(ctx context.Context, id marotte.ChatID) ([]marotte.WorkflowMessage, error)
	// DepartedName is the name a chat had when its record was deleted, for a notice
	// its bridge raises before teardown; false for a chat that was not deleted.
	DepartedName(id marotte.ChatID) (string, bool)
}

// Roles is the wiring-time role set: the host names which of its interfaces
// answers each role once, at construction.
type Roles struct {
	// Bus is the event fan-out.
	Bus Broadcaster
	// Chats is the chat store.
	Chats chatRecords
	// Turns is the open turn's accumulator.
	Turns turnAccess
	// Runs is the run registry's appender, for a workflow step's frames.
	Runs RunAppender
	// Bracket is the wire's own turn bracket.
	Bracket turnBoundary
	// Lines is the changed-line tracker.
	Lines lineRecorder
	// Steers answers whose words a steer carries.
	Steers sentSteers
	// SteerBuffer is the projection of KAS's steering buffer a reconnect replays.
	SteerBuffer steerBuffer
	// PendingPerms registers an unanswered decision for reconnect replay.
	PendingPerms pendingPermAdder
	// Push sends a web-push notification.
	Push pusher
	// Sessions resolves a chat's parent ACP session.
	Sessions sessionResolver
	// Terminals reads an agent terminal's rendered output.
	Terminals terminalReader
	// HookStatus reports whether hook status display is on.
	HookStatus hookStatusReader
	// Catalog is where a live config_option_update's model list lands.
	Catalog modelCatalog
	// Slash is the workspace slash-menu catalog; SteeringIssues KAS's steering
	// configuration issues. Either may be nil.
	Slash          SlashCatalog
	SteeringIssues SteeringIssues
	MCP            mcpRecorder
	Governance     governanceAccess
	RunOrigin      runOriginAccess
	RunBounds      runBoundsAccess
	// TurnInterrupt ends a turn kiro-cli abandoned without answering.
	TurnInterrupt turnInterruptAccess
	// Metering is the per-turn accounting a turn_completion frame writes.
	Metering turnMetering
	// WorkDir is the workspace root. Last for fieldalignment.
	WorkDir string
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// pendingPermAdder registers an unanswered decision for reconnect replay.
type pendingPermAdder interface {
	// PendingPermsAdd records the ask KAS sent as acpID with the bridge it arrived on, which carries
	// its answer and whose end retires it, and returns evt carrying the ask's own id: the request_id
	// a client sees and echoes, since acpID restarts on every bridge.
	PendingPermsAdd(acpID int64, evt marotte.ServerEvent, origin AskOrigin) marotte.ServerEvent
	// PendingPermsWithdraw retires the chat's ask for toolCallID, which KAS
	// stopped waiting on, reporting whether one was open.
	PendingPermsWithdraw(chatID marotte.ChatID, toolCallID string) bool
}

// AskOrigin is the bridge a server-to-client ask arrived on: every answer to it goes back there,
// a declined one included (KAS's sendRequest has no timeout).
type AskOrigin interface {
	Respond(ctx context.Context, requestID int64, result any, err error) error
}

// pusher shows a notification on the page and as a Web Push.
type pusher interface {
	// NoticeTarget names the tab a notification about chatID, or about its run, opens.
	NoticeTarget(ctx context.Context, chatID marotte.ChatID, runID string) notice.Target
	Notify(ctx context.Context, chatID marotte.ChatID, n *marotte.NotificationPayload)
}

// sessionResolver answers a chat's parent ACP session id, or "" when no bridge
// is running.
type sessionResolver interface {
	ParentACPSession(chatID marotte.ChatID) string
}

// hookStatusReader reports whether hook status display is enabled.
type hookStatusReader interface {
	IsHookStatusEnabled() bool
}

// modelCatalog is the workspace's model vocabulary, which a live
// config_option_update updates. SetModels reports whether the list changed.
type modelCatalog interface {
	SetModels(models []marotte.SessionModel) bool
}

// terminalReader returns an agent terminal's rendered output: plain text with
// escapes parsed off, plus the spans styling it. ok reports whether the terminal
// is known, not whether it printed anything — a registered terminal that produced
// no output answers ("", nil, true).
type terminalReader interface {
	Output(terminalID string) (text string, spans []marotte.TextSpan, ok bool)
}

// governanceAccess is the runtime's governance cache: the account profile, the MCP
// registry and the administrator rules it composes into the lock map.
type governanceAccess interface {
	// SetGovernance replaces the cached account profile.
	SetGovernance(ctx context.Context, state marotte.GovernanceStatePayload)
	// SetMCPRegistry replaces the organization's MCP registry; nil means no registry.
	SetMCPRegistry(ctx context.Context, registry *marotte.GovernanceMCPRegistry)
	// PolicyChanged reports a policy notification's errors, so the administrator
	// rules are read again and a fatal administration error fails them closed.
	// reloaded marks a completed reload, whose errors are the whole set.
	PolicyChanged(ctx context.Context, errs []marotte.PolicyErrorItem, reloaded bool)
}

// mcpRecorder groups MCP server state tracking methods.
type mcpRecorder interface {
	// RecordConnected marks a server connected and replaces what it advertises
	// (tools, prompts, resources, resource templates) wholesale; any may be nil.
	RecordConnected(ctx context.Context, serverName string, src marotte.MCPSource, tools []string, prompts []marotte.MCPPromptInfo, resources []marotte.MCPResourceInfo, templates []marotte.MCPResourceTemplateInfo)
	RecordOAuth(ctx context.Context, serverName string, src marotte.MCPSource, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName string, src marotte.MCPSource, errMsg string)
	// RecordDisabled reports a server KAS says is off. Never recorded for the
	// entry marotte owns, so it cannot resurrect one the user switched off.
	RecordDisabled(ctx context.Context, serverName string, src marotte.MCPSource)
}

// Translator holds the package's stateful translate logic, one field per role.
type Translator struct {
	bus            Broadcaster
	chats          chatRecords
	turns          turnAccess
	runs           RunAppender
	bracket        turnBoundary
	lines          lineRecorder
	steers         sentSteers
	steerBuffer    steerBuffer
	pendingPerms   pendingPermAdder
	push           pusher
	sessions       sessionResolver
	terminals      terminalReader
	hookStatus     hookStatusReader
	catalog        modelCatalog
	slash          SlashCatalog
	steeringIssues SteeringIssues
	mcp            mcpRecorder
	governance     governanceAccess
	runOrigin      runOriginAccess
	runBounds      runBoundsAccess
	turnInterrupt  turnInterruptAccess
	metering       turnMetering
	// steps maps a workflow step's ACP session id to its run and node, fed from the
	// wire (node_start) and from an inspect read.
	steps   *stepRegistry
	workDir string // last for fieldalignment, as in Roles
}

// New constructs a Translator over the roles the host supplies.
func New(r *Roles, opts ...Option) *Translator {
	t := &Translator{
		bus:            r.Bus,
		chats:          r.Chats,
		turns:          r.Turns,
		runs:           r.Runs,
		bracket:        r.Bracket,
		lines:          r.Lines,
		steers:         r.Steers,
		steerBuffer:    r.SteerBuffer,
		pendingPerms:   r.PendingPerms,
		push:           r.Push,
		sessions:       r.Sessions,
		terminals:      r.Terminals,
		hookStatus:     r.HookStatus,
		catalog:        r.Catalog,
		slash:          r.Slash,
		steeringIssues: r.SteeringIssues,
		workDir:        r.WorkDir,
		mcp:            r.MCP,
		governance:     r.Governance,
		runOrigin:      r.RunOrigin,
		runBounds:      r.RunBounds,
		turnInterrupt:  r.TurnInterrupt,
		metering:       r.Metering,
		steps:          newStepRegistry(),
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Option configures a Translator.
type Option func(*Translator)

// deriveSubSession returns the sessionID when it belongs to a subagent, and ""
// for the launching chat itself or for a workflow step.
func (t *Translator) deriveSubSession(chatID marotte.ChatID, sessionID string) string {
	if t.classifyFrame(chatID, sessionID, false) == ownerSubagent {
		return sessionID
	}
	return ""
}

// runOriginAccess answers where a run-shaped fact came from. In-memory on the host: a
// run that outlives a restart reports false afterwards.
type runOriginAccess interface {
	// IsScheduled reports whether a run was launched by a schedule, keyed by
	// workflow id because a parentless run's frames carry no topic.
	IsScheduled(workflowID string) bool
	// RunLabel is the label marotte gave a run at launch, "" when it gave none.
	RunLabel(workflowID string) string
	// RunNotice CONSUMES the oldest finished run whose completion notice KAS queued
	// into chatID's steering buffer, answering its id and the instant it finished
	// (Unix ms). False when no such run is recorded, in which case the notice is
	// written without provenance rather than with a guess.
	RunNotice(chatID marotte.ChatID) (workflowID string, producedTs int64, ok bool)
}

// runBoundsAccess reports the observable progress that rolls a run's idle window
// forward. Detection belongs here (the step's frames pass through this package) while
// the bound belongs on the host, which owns the bridges and the only stop verb.
type runBoundsAccess interface {
	// RunMadeProgress reports that a run's step did something observable, so its idle window may
	// roll forward. Called per step frame: cheap, idempotent, a no-op for an unbounded run. Keyed
	// on the RUN, because the window is the run's.
	RunMadeProgress(workflowID string)
}

// turnInterruptAccess carries terminal signals that arrive without a response frame: detected
// here, terminated on the host. Advisory; a compaction failure does not prove the turn ended.
type turnInterruptAccess interface {
	CompactionFailed(chatID marotte.ChatID, detail string)
	InterruptTurn(chatID marotte.ChatID, reason string)
}

// turnMetering is the per-turn accounting a turn_completion frame writes, split
// in two because a step's credits belong to the launching chat while the
// conversation turn count and duration are the conversation's.
type turnMetering interface {
	// AccumulateSpend adds a turn_completion's credit spend, step frames included.
	AccumulateSpend(ctx context.Context, chatID marotte.ChatID, credits float64)
	// StageConversationTurnSummary accumulates a conversation turn's reported
	// duration, so several frames for one turn sum.
	StageConversationTurnSummary(ctx context.Context, chatID marotte.ChatID, elapsedMs float64)
}
