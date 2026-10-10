package translate

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// TurnAccess resolves the accumulator a frame's content folds into and files the
// entries that belong to no open turn.
type TurnAccess interface {
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
}

// RunAppender is the run registry as the content handlers reach it for a step's
// frame: the run's log keys a turn on the node PATH, so every method takes both.
type RunAppender interface {
	// RunNodeStart opens the step's turn on the run's node_start frame, the run
	// turn's opening bracket, recording chatID as the run's host when it is the
	// run's first turn; a path already open is a no-op on the log.
	RunNodeStart(ctx context.Context, step RunStep, chatID marotte.ChatID)
	// RunNodeComplete closes the turn of the step at path (its workflow.PathKey)
	// with KAS's status mapped onto the outcome, the run turn's closing bracket; a
	// path with no open turn closes nothing.
	RunNodeComplete(ctx context.Context, runID string, path []string, status, reason string)
	// RunFoldTarget returns the run's open turn for the step, opening one when its
	// path has no turn yet and recording chatID as the run's host. False for a
	// path whose turn already closed, where RunAppendAfterClosed files a late entry,
	// and for an open the store refused.
	RunFoldTarget(ctx context.Context, step RunStep, chatID marotte.ChatID) (*turnlog.Turn, bool)
	// RunAppendAfterClosed files an entry after the turn_close of the path's newest
	// closed turn, the run log's between-turns rule.
	RunAppendAfterClosed(ctx context.Context, runID, nodePath string, e *marotte.Entry) error
	// RunMeter folds a step's turn_completion into its open turn's aggregate; false
	// when the path has no open turn, which the caller logs at Debug.
	RunMeter(runID, nodePath string, credits, elapsedMs float64) bool
	// RunStopReason records a step's last turn_end stop reason on its open turn;
	// false when the path has no open turn.
	RunStopReason(runID, nodePath string, raw marotte.StopReason) bool
}

// TurnBoundary is the wire's own turn bracket, which KAS emits for every
// turn, agent-initiated included.
type TurnBoundary interface {
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

// LineRecorder records the changed lines a frame's diffs describe.
type LineRecorder interface {
	RecordFromDiffs(chatID marotte.ChatID, diffs []marotte.ToolDiff, turn int, kind string)
}

// SentSteers answers what this server recorded about a mid-turn steer when it sent
// it: whose words it carries, total by construction (an unknown id still gets an
// answer), and which dropped steers it re-sends (nil for an unknown id).
type SentSteers interface {
	SteerOrigin(chatID marotte.ChatID, steerID string) marotte.SteerOrigin
	SteerResends(chatID marotte.ChatID, steerID string) []string
}

// SteerBuffer is the host's record of KAS's steering buffer and of the user's own steers:
// nothing can read that buffer back. Separate from SentSteers because the lifetimes differ
// (a TTL'd origin vs KAS's buffer).
type SteerBuffer interface {
	// SteerWaiting folds steering_queued; true means an agent row the caller broadcasts.
	SteerWaiting(chatID marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool)
	// SteerForgotten folds an acknowledgement-evidenced read and answers what was
	// read, with its text, which no frame carries here.
	SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload
	// SteerRead folds steering_injected for the one id it names.
	SteerRead(chatID marotte.ChatID, steerID string)
	// SteerCleared folds steering_cleared and answers the AGENT rows it named. A
	// user row gets no entry at a clear: the host writes one at the row's own
	// terminal transition, so a row resent after the clear is never noted unread.
	SteerCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload
}

// ChatRecords is the chat store's HEADER as this package uses it. Every write
// lands after the frame that caused it, so chat.ErrTombstoned is an expected
// outcome here.
type ChatRecords interface {
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
	Chats ChatRecords
	// Turns is the open turn's accumulator.
	Turns TurnAccess
	// Runs is the run registry's appender, for a workflow step's frames.
	Runs RunAppender
	// Bracket is the wire's own turn bracket.
	Bracket TurnBoundary
	// Lines is the changed-line tracker.
	Lines LineRecorder
	// Steers answers whose words a steer carries.
	Steers SentSteers
	// SteerBuffer is the projection of KAS's steering buffer a reconnect replays.
	SteerBuffer SteerBuffer
	// PendingPerms registers an unanswered decision for reconnect replay.
	PendingPerms PendingPermAdder
	// Respond answers a server-to-client request on the chat's bridge.
	Respond Responder
	// Push sends a web-push notification.
	Push Pusher
	// Sessions resolves a chat's parent ACP session.
	Sessions SessionResolver
	// Terminals reads an agent terminal's rendered output.
	Terminals TerminalReader
	// HookStatus reports whether hook status display is on.
	HookStatus HookStatusReader
	// Catalog is where a live config_option_update's model list lands.
	Catalog ModelCatalog
	// Slash is the workspace slash-menu catalog; SteeringIssues KAS's steering
	// configuration issues. Either may be nil.
	Slash          SlashCatalog
	SteeringIssues SteeringIssues
	MCP            MCPRecorder
	Governance     GovernanceAccess
	RunOrigin      RunOriginAccess
	RunBounds      RunBoundsAccess
	// TurnInterrupt ends a turn kiro-cli abandoned without answering.
	TurnInterrupt TurnInterruptAccess
	// Metering is the per-turn accounting a turn_completion frame writes.
	Metering TurnMetering
	// WorkDir is the workspace root. Last for fieldalignment.
	WorkDir string
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// PendingPermAdder registers an unanswered decision for reconnect replay.
type PendingPermAdder interface {
	PendingPermsAdd(requestID int64, evt marotte.ServerEvent)
	// PendingPermsWithdraw retires the chat's ask for toolCallID, which KAS
	// stopped waiting on, reporting whether one was pending.
	PendingPermsWithdraw(chatID marotte.ChatID, toolCallID string) bool
}

// Responder answers a server-to-client ACP request on the chat's bridge: a declined request
// must still be answered (KAS's sendRequest has no timeout). A chat with no bridge reports nil.
type Responder interface {
	BridgeRespond(ctx context.Context, chatID marotte.ChatID, requestID int64, result any, err error) error
}

// Pusher delivers a web-push notification for a chat.
type Pusher interface {
	NotifyPush(ctx context.Context, body string, kind marotte.PushKind, chatID marotte.ChatID)
}

// SessionResolver answers a chat's parent ACP session id, or "" when no bridge
// is running.
type SessionResolver interface {
	ParentACPSession(chatID marotte.ChatID) string
}

// HookStatusReader reports whether hook status display is enabled.
type HookStatusReader interface {
	IsHookStatusEnabled() bool
}

// ModelCatalog is the workspace's model vocabulary, which a live
// config_option_update updates. SetModels reports whether the list changed.
type ModelCatalog interface {
	SetModels(models []marotte.SessionModel) bool
}

// TerminalReader returns an agent terminal's rendered output: plain text with
// escapes parsed off, plus the spans styling it. ok reports whether the terminal
// is known, not whether it printed anything — a registered terminal that produced
// no output answers ("", nil, true).
type TerminalReader interface {
	Output(terminalID string) (text string, spans []marotte.TextSpan, ok bool)
}

// GovernanceAccess is the runtime's governance cache: the account profile, the MCP
// registry and the administrator rules it composes into the lock map.
type GovernanceAccess interface {
	// SetGovernance replaces the cached account profile.
	SetGovernance(ctx context.Context, state marotte.GovernanceStatePayload)
	// SetMCPRegistry replaces the organization's MCP registry; nil means no registry.
	SetMCPRegistry(ctx context.Context, registry *marotte.GovernanceMCPRegistry)
	// PolicyChanged reports a policy notification's errors, so the administrator
	// rules are read again and a fatal administration error fails them closed.
	// reloaded marks a completed reload, whose errors are the whole set.
	PolicyChanged(ctx context.Context, errs []marotte.PolicyErrorItem, reloaded bool)
}

// MCPRecorder groups MCP server state tracking methods.
type MCPRecorder interface {
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
	chats          ChatRecords
	turns          TurnAccess
	runs           RunAppender
	bracket        TurnBoundary
	lines          LineRecorder
	steers         SentSteers
	steerBuffer    SteerBuffer
	pendingPerms   PendingPermAdder
	respond        Responder
	push           Pusher
	sessions       SessionResolver
	terminals      TerminalReader
	hookStatus     HookStatusReader
	catalog        ModelCatalog
	slash          SlashCatalog
	steeringIssues SteeringIssues
	mcp            MCPRecorder
	governance     GovernanceAccess
	runOrigin      RunOriginAccess
	runBounds      RunBoundsAccess
	turnInterrupt  TurnInterruptAccess
	metering       TurnMetering
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
		respond:        r.Respond,
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
	if t.ClassifyFrame(chatID, sessionID, false) == OwnerSubagent {
		return sessionID
	}
	return ""
}

// RunOriginAccess answers where a run-shaped fact came from. In-memory on the host: a
// run that outlives a restart reports false afterwards.
type RunOriginAccess interface {
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

// RunBoundsAccess reports the observable progress that rolls a run's idle window
// forward. Detection belongs here (the step's frames pass through this package) while
// the bound belongs on the host, which owns the bridges and the only stop verb.
type RunBoundsAccess interface {
	// RunMadeProgress reports that a run's step did something observable, so its idle window may
	// roll forward. Called per step frame: cheap, idempotent, a no-op for an unbounded run. Keyed
	// on the RUN, because the window is the run's.
	RunMadeProgress(workflowID string)
}

// TurnInterruptAccess carries terminal signals that arrive without a response frame: detected
// here, terminated on the host. Advisory; a compaction failure does not prove the turn ended.
type TurnInterruptAccess interface {
	CompactionFailed(chatID marotte.ChatID, detail string)
	InterruptTurn(chatID marotte.ChatID, reason string)
}

// TurnMetering is the per-turn accounting a turn_completion frame writes, split
// in two because a step's credits belong to the launching chat while the
// conversation turn count and duration are the conversation's.
type TurnMetering interface {
	// AccumulateSpend adds a turn_completion's credit spend, step frames included.
	AccumulateSpend(ctx context.Context, chatID marotte.ChatID, credits float64)
	// StageConversationTurnSummary accumulates a conversation turn's reported
	// duration, so several frames for one turn sum.
	StageConversationTurnSummary(ctx context.Context, chatID marotte.ChatID, elapsedMs float64)
}
