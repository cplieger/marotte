package command

import (
	"context"
	"errors"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

// This file is the package's dependency contracts: the role interfaces each handler
// names in its own signature, and the per-chat bridge interfaces the handlers call.
// Each is shaped by what this package invokes, which keeps a test stub small enough
// to be obviously correct. No composite over these roles exists here: a handler's
// reach is its signature, so widening one is a diff at its declaration.

// sessionScoped names the ACP session an RPC is addressed to.
type sessionScoped interface {
	// SessionID returns the current ACP session ID.
	SessionID() marotte.SessionID
}

// bridgeCaller sends one request to kiro-cli and waits for its answer.
type bridgeCaller interface {
	// Call sends an RPC call to kiro-cli.
	Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error)
	// CallAt is Call plus the read loop position at which the response arrived:
	// notifications queue on a buffered channel while a response goes straight to
	// the waiting Call, so the wire's own turn_end is routinely still unread.
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
}

// sessionCaller is the commonest shape on this path: one call, addressed to
// the bridge's own session.
type sessionCaller interface {
	bridgeCaller
	sessionScoped
}

// bridgeRPC is the full JSON-RPC surface — request, notification, and the
// answer to an inbound request from kiro-cli. Carries no prompt-slot method:
// sending a frame and owning the turn are different rights, and the cancel
// path sends a notification without holding the slot.
type bridgeRPC interface {
	sessionCaller

	// Notify sends a one-way notification to kiro-cli.
	Notify(ctx context.Context, method string, params any) error
	// Respond sends a permission response to kiro-cli.
	Respond(ctx context.Context, requestID int64, result any, err error) error
}

// promptSlot is the per-chat turn lock and its unresponsive-cancel budget: acquire
// the slot, register the in-flight call's cancel func against a turn generation, arm
// the grace budget for that generation, then release. The generation stops an expired
// budget cancelling a later turn — time.Timer.Stop does not halt a running func.
type promptSlot interface {
	// TryAcquireForPrompt attempts to lock the bridge for prompting.
	TryAcquireForPrompt() bool
	// ReleaseAfterPrompt releases the prompt lock.
	ReleaseAfterPrompt()
	// BeginPromptCall registers the cancel func of the in-flight prompt's
	// context and returns the turn generation it belongs to. Paired with
	// EndPromptCall in the prompt handler's defer.
	BeginPromptCall(cancel context.CancelCauseFunc) uint64
	// EndPromptCall forgets the in-flight prompt's cancel func.
	EndPromptCall()
	// ArmCancelGrace starts the unresponsive-cancel budget: if the turn
	// identified by gen is still in flight after d, the prompt's context is
	// cancelled so the blocked Call returns and the slot is released.
	// Reports false if there was no in-flight prompt to arm against.
	ArmCancelGrace(gen uint64, d time.Duration) bool
	// PromptGeneration returns the current turn generation.
	PromptGeneration() uint64
}

// Bridge is the per-chat ACP bridge as a whole. Only a handler that owns a chat for
// the length of a turn needs it; helpers take a narrower parameter. Exported because
// BridgeAccess returns it and the runtime's wiring names it.
type Bridge interface {
	bridgeRPC
	promptSlot
}

// BridgeAccess provides bridge lifecycle operations needed by prompt,
// cancel, subagent, slash, and permission handlers.
type BridgeAccess interface {
	Bridge(chatID marotte.ChatID) Bridge
	OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (Bridge, error)
	// BridgeLive reports a bridge for the chat that is past its spawn: the state
	// a steer can be delivered into, where a spawning one parks it.
	BridgeLive(chatID marotte.ChatID) bool
	// CloseBridge closes every turn the bridge hosted with outcome, then stops the
	// process: `cancelled` for a stop the reader asked for, `interrupted` otherwise.
	CloseBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome)
}

// ChatStore is the chat store as the command handlers use it: read the header,
// mutate it, record a draft and its attachments, delete the chat, and the two
// log operations a command owns, the rewind's truncate and the attachment count.
// It excludes List, RegisterRoutes and every append but the turn_open the turn
// registry writes. Exported because *agent.Runtime names this.
type ChatStore interface {
	// Get returns the chat header at id, or false if it does not exist.
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// Mutate is the header write primitive: load, apply, save, broadcast
	// chat_created / chat_updated.
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	// Revert appends the log's record of a rewind: turn and everything after it
	// become unreadable rather than absent, so nothing is cut and nothing is
	// re-closed. It answers the appended record, and the carrier's own turn_open
	// when no turn survived the window and the log minted one to hold it.
	Revert(ctx context.Context, chatID marotte.ChatID, turn, kasMessageID string) (record, opened *marotte.Entry, err error)
	// RewindTarget resolves the prompt id a rewind addresses to the turn that
	// prompt opened and the id KAS holds the prompt under. False when no turn's
	// prompt carries that id.
	RewindTarget(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error)
	// PromptAttachmentPaths is every attachment path on the log's prompts after the
	// compaction watermark entry (the whole log when watermark is ""), in turn
	// order: what KAS still holds, which bounds how many more images this prompt
	// may inline. The image predicate is the caller's.
	PromptAttachmentPaths(ctx context.Context, chatID marotte.ChatID, watermark string) ([]string, error)
	// TurnCount is the header's turn_count, the whole-history read the rewind and
	// the model switch key on.
	TurnCount(ctx context.Context, chatID marotte.ChatID) (uint64, bool)
	// SetDraft persists the chat's unsent composer text. Its own method rather than a
	// Mutate call because a draft save is not activity: Mutate stamps UpdatedAt, which
	// the retention purge ages a chat from. A no-op for a chat that does not exist —
	// typing must not create one. The returned state is what landed, nil when nothing
	// did, and the draft_changed broadcast keys on it.
	SetDraft(ctx context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error)
	// SetAttachments persists the paths staged beside the draft, replacing
	// the list. The draft's twin, with the same no-UpdatedAt and no-op-on-
	// missing-record contracts.
	SetAttachments(ctx context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error)
	// Delete removes the chat file and broadcasts chat_deleted. cmdDeleteChat
	// is the only caller in the build: bridge exits, model switches and
	// restarts never delete.
	Delete(ctx context.Context, id marotte.ChatID) error
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// ChatTeardown ends a chat's life, in one of two ways. Cancelling the chat's runs is
// inside both rather than a step a caller pairs with them: a run is durable state a
// dead bridge only pauses, so it must be told to cancel before the bridge goes.
type ChatTeardown interface {
	// DeleteChatState is the delete path: cancel the chat's runs, drop every
	// in-memory trace, and reap the durable KAS session too. Resolves runs
	// and sessions off the chat record, so it belongs to the record-first
	// delete, where the teardown runs while the record still exists.
	DeleteChatState(ctx context.Context, chatID marotte.ChatID)
	// DeleteChatStateByChain is the delete grade for a chat whose record is
	// already gone: the close escalation removes the record inside its
	// commit, so the run cancel and the KAS reap are driven from the session
	// chain captured before it.
	DeleteChatStateByChain(ctx context.Context, chatID marotte.ChatID, sessionChain []string)
	// CloseChatState is the close path: the same cancel and in-memory
	// cleanup, but it leaves the durable KAS session on disk so the chat can
	// be reopened and so History can still list it.
	CloseChatState(ctx context.Context, chatID marotte.ChatID)
	// BeginChatTeardown marks the chat as going away BEFORE the teardown's cancel,
	// so the turn end that cancel causes resends nothing. keep is a close, whose
	// record survives and keeps a note per unread steer. Idempotent.
	BeginChatTeardown(chatID marotte.ChatID, keep bool)
}

// PendingPermAccess provides the pending-permission bookkeeping handlers
// need: an unanswered request is replayed to a reconnecting client, so
// answering or abandoning one has to retire the entry.
type PendingPermAccess interface {
	ClearPendingPermsForChat(chatID marotte.ChatID)
	// TakePendingPerm claims the request before its answer is sent, reporting false
	// when another surface already answered it. settledBy travels with the claim
	// because the winning take is broadcast to the surfaces that lost, and their card
	// says who answered. The chat is part of the claim because a request id is unique
	// only within one bridge: every bridge mints ids from zero.
	TakePendingPerm(chatID marotte.ChatID, requestID int64, settledBy marotte.SettledBy) bool
	// TakePendingPermissionOption retains an off-list request and reports it as
	// pending but not offered. A successful result validates and claims in one
	// operation, so another surface cannot answer between those steps.
	TakePendingPermissionOption(chatID marotte.ChatID, requestID int64, optionID string, settledBy marotte.SettledBy) (pending, offered bool)
}

// TerminalAccess is the interrupt's process half: a turn cancel must reach
// the turn's agent terminals or it strands them.
type TerminalAccess interface {
	// KillTurnTerminals kills the terminals the chat's current turn
	// created, and nothing else — a background command an earlier turn
	// left running on purpose is not the cancel's to kill.
	KillForTurn(chatID marotte.ChatID)
}

// Workspace carries the paths handlers resolve against: the working directory the
// hook writer and the shell spawn need, the config directory the prompt reads a
// setting out of, and the uploads directory an attachment may also name. An
// attachment path is confined to those roots before it is read.
type Workspace struct {
	// Dir is the workspace root every relative path resolves against.
	Dir string
	// ConfigDir is where settings and hook files live.
	ConfigDir string
	// UploadsDir is the second root an ATTACHMENT path may name — the directory a
	// composer upload lands in, which sits beside the workspace rather than inside
	// it. Nothing else resolves against it: it is not a second workspace root, and
	// a relative path never reaches it.
	//
	// Empty is legal and inert (an empty root contains no path), so a caller with
	// no upload surface leaves it unset and gets workspace-only confinement.
	UploadsDir string
}

// ResolveInside confines rel to the roots an attachment path may name, refusing
// anything that escapes all of them. The workspace leads, so a relative path
// resolves there and an unresolvable path reports the workspace's own error.
func (w Workspace) ResolveInside(rel string) (string, error) {
	return workspace.ResolveInsideAnyAbs([]string{w.Dir, w.UploadsDir}, rel)
}

// LifecycleAccess is the process-lifetime seam: the context a turn runs under, and
// the in-flight accounting that makes agent shutdown wait for it. Shutdown cancels
// the first and waits on the second.
type LifecycleAccess interface {
	// TurnContext returns the context an in-flight turn runs under, plus
	// the teardown its handler defers.
	TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc)
	InflightAdd(delta int)
	InflightDone()
}

// MCPAccess is the MCP readiness gate a prompt waits on, so a first turn does not
// reach the model before the workspace's MCP servers have connected, plus the account
// of what the wait was short of when it expires.
type MCPAccess interface {
	WaitForReady(ctx context.Context, timeout time.Duration) bool
	// PendingSummary names the servers a readiness wait is still short of.
	// Read only when the wait expires — a diagnostic, not part of the
	// decision.
	PendingSummary(ctx context.Context) MCPPendingSummary
}

// MCPPendingSummary is the three buckets a readiness timeout can be about,
// each wanting a different operator action.
type MCPPendingSummary struct {
	// Silent is the enabled servers that reported no terminal state
	// (including one still connecting, deliberately not recorded).
	Silent []string
	// Failed is the servers that reported a failure, each with the
	// upstream error text.
	Failed []string
	// AwaitingAuth is the servers waiting for an authorization nobody has
	// completed.
	AwaitingAuth []string
}

// AdmissionOutcome is ReserveTurnForPrompt's answer.
type AdmissionOutcome int

const (
	// AdmissionAcquired means the caller holds the chat's admission slot.
	AdmissionAcquired AdmissionOutcome = iota
	// AdmissionBusy is the holder a steer can reach: a prompt-class turn
	// with a live bridge. Answered as the plain 409, on which the client's
	// 409→steer conversion works.
	AdmissionBusy
	// AdmissionStarting is every other holder: a cold spawn, or a shell on a
	// bridged or bridgeless chat. Answered as 409 with the additive
	// `reason: "starting"`.
	AdmissionStarting
)

// TurnAdmission is the chat's admission slot as the command handlers hold it:
// a bare per-chat reservation decided before any turn exists, plus the read
// that says who holds it. Separate from TurnOutcomeAccess because the steer and
// the rewind read the holder and never open a turn.
type TurnAdmission interface {
	// ReserveTurnForPrompt takes the chat's admission slot for a prompt,
	// minting no turn — a bare per-chat reservation, decided synchronously
	// before any bridge exists. A held slot parks the caller up to wait;
	// the refusal is keyed on the holder's source — see AdmissionOutcome.
	ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration) AdmissionOutcome
	// TryReserveTurn takes the admission slot iff it is free — the shell
	// door's form (a `!cmd` during any held slot refuses immediately) and
	// the empty-turn recovery's (a competing prompt that won the slot
	// abandons the retry).
	TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool
	// ReleaseTurnReservation frees the admission slot, waking every
	// waiter.
	ReleaseTurnReservation(chatID marotte.ChatID)
	// AdmissionHolderSource reports who holds the chat's admission: the open turn's
	// source when one is open, else the reservation's. A non-prompt holder matters
	// because a steer aimed into it lands somewhere the reader did not mean — a
	// workflow step's turn reads it as the step's own input — so CmdSteer refuses
	// instead.
	AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool)
}

// TurnOutcomeAccess is what a prompt handler needs to run an admitted turn's
// lifecycle: open it, wait for its outcome, and close it — whether the engine
// answered or the call failed.
type TurnOutcomeAccess interface {
	// OpenTurn appends the turn_open at admission and creates the turn's registry
	// record in one operation: prompt is the turn's prompt (nil for a source with
	// none) and init is the header fallback for an id no record exists for, nil for
	// every source but prompt and local_shell. Refuses on a dead ctx before it
	// appends. The id it returns is what StartTurn and every closer are handed.
	OpenTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, prompt *marotte.EntryPrompt, init func(*marotte.Chat)) (string, error)
	// StartTurn stamps the model and the credit baseline onto the turn the id
	// names, with the bridge live, immediately before the call that drives it.
	// False for a dead ctx and for an id the registry no longer holds, and a caller
	// answering false runs the turn end rule on that turn itself.
	StartTurn(ctx context.Context, chatID marotte.ChatID, turnID string) bool
	// AwaitTurn blocks until the named turn has finalized and reports what
	// it did. A caller holding that turn's handle never receives
	// marotte.ErrNoSuchTurn.
	AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error)
	// ReleaseTurn gives up the handle OpenTurn issued, after which the
	// finalized record may be dropped.
	ReleaseTurn(chatID marotte.ChatID, turnID string)
	// SettleTurnOnResponse closes the named turn on the response that settled it —
	// the local fallback, which runs only if the wire's own turn_end did not get
	// there first. seq is the read loop position the response arrived at, and the
	// settle parks until the folder reaches it.
	SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse)
	// TurnOpenedAfter reports whether any own turn on the chat opened after
	// turnID — the structural half of the empty-turn gate.
	TurnOpenedAfter(chatID marotte.ChatID, turnID string) bool
	// FinalizeLocalShellTurn closes a `!cmd` turn marotte ran itself, appending
	// its one text entry first.
	FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, turnID, output string)
	// AbandonInFlightTurn finalizes a turn the prompt call could not finish, and
	// the prompt's exits before any call. stop is what the failure CONCLUDES —
	// `interrupted` for a fault, `cancelled` for a user cancel KAS never acked — and
	// reason is the user-facing account of it, which the turn_close carries. It
	// waits for no read loop position.
	AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string)
}

// SteerRecorder is what the steer commands need of the steer ledger: record that
// THIS server sent a steer under an id, and the rows it carries, before the RPC,
// which is the only ordering that beats KAS's own notification (see CmdSteer).
type SteerRecorder interface {
	// RecordUserSteer records a steer this server sent, with the row keys it
	// carries when the id is not a row's own (nil for an ordinary one).
	RecordUserSteer(chatID marotte.ChatID, steerID string, resends []string)
}

// The refusal classes a steer operation answers with; the client branches on them.
const (
	SteerRefuseNoTurn     = "no_turn"
	SteerRefuseFull       = "full"
	SteerRefuseNotWaiting = "not_waiting"
	SteerRefuseNotUser    = "not_user"
	SteerRefuseAgentRows  = "agent_rows_waiting"
	SteerRefuseStarting   = "starting"
	SteerRefuseSettling   = "settling"
	SteerRefuseChatGone   = "chat_gone"
	// SteerRefuseNoReply is a clear that got no reply: nothing changed.
	SteerRefuseNoReply = "no_reply"
	// SteerRefuseConsumed is a delete whose target the agent read first.
	SteerRefuseConsumed = "consumed"
)

// SteerTurnEnd is a turn's end as the steer record saw it.
type SteerTurnEnd struct {
	// Exit closes when the closing bridge's forward goroutine has drained, for a
	// bridge death: a resend admitted earlier would reach the dying process.
	Exit   <-chan struct{}
	TurnID string
	// Lead is the row the send-now arrow named, resent first.
	Lead        string
	Source      marotte.TurnOpenSource
	BridgeDeath bool
}

// SteerJob is work a frame fold or a turn end hands to the command side, which runs
// it under the chat's steer lock. A turn-end job carries End and the Owner its rows
// are marked with; a flush carries neither.
type SteerJob struct {
	End   *SteerTurnEnd
	Chat  marotte.ChatID
	Owner string
}

// SteerSend is one _session/steer the record decided on, state already written.
// Keys names the rows it carries when ID is not a row's own key.
type SteerSend struct {
	ID   string
	Text string
	Keys []string
}

// SteerHolder is what admission says about a chat when a steer is routed.
type SteerHolder struct {
	Held        bool
	PromptClass bool
	Live        bool
	// Delivering is the parked drain itself, which must not re-park its head row.
	Delivering bool
}

// SteerOpResult is what an op learns when it re-reads the record after its clear.
type SteerOpResult struct {
	// Resend is the kept rows' combined send, state already written; nil when
	// nothing is kept or their turn closed under the op.
	Resend *SteerSend
	// Reason is a refusal class: chat gone, the clear had no reply, or the target
	// was read before the clear landed.
	Reason string
}

// SteerRow is one row a turn-end job holds.
type SteerRow struct {
	Key   string
	Text  string
	InKAS bool
}

// SteerQueue is the server's record of the user's mid-turn steers and of what
// KAS's buffer holds, as the steer commands drive it. Every method decides under
// the record's lock and writes state before the caller's RPC; the caller holds the
// chat's steer lock across the whole operation.
type SteerQueue interface {
	// LockSteerOps serializes every operation on the chat's steering buffer;
	// different chats never wait on each other. The error is the context's.
	LockSteerOps(ctx context.Context, chatID marotte.ChatID) (unlock func(), err error)
	// AwaitReadLoop parks until the bridge's read loop has folded every frame that
	// preceded a response at seq, reporting whether it got there.
	AwaitReadLoop(ctx context.Context, chatID marotte.ChatID, seq uint64) bool
	// RouteSteer answers the sends to issue, in order, or the refusal class.
	RouteSteer(chatID marotte.ChatID, key, text string, h SteerHolder) (sends []SteerSend, refuse string)
	SteerSent(chatID marotte.ChatID, s SteerSend, queued bool, err error)
	// BeginRemove decides a delete. A row KAS does not hold is deleted at once and
	// needsClear is false; otherwise the op owns its rows until EndOp.
	BeginRemove(chatID marotte.ChatID, key, opID string) (needsClear bool, refuse string)
	// RemoveCleared re-reads after the delete's clear (landed false: no reply).
	RemoveCleared(chatID marotte.ChatID, opID string, cleared []string, landed bool) SteerOpResult
	// BeginDiscard decides a Discard all; needsClear is false when nothing is in KAS.
	BeginDiscard(chatID marotte.ChatID, opID string) (needsClear bool, refuse string)
	// DiscardCleared re-reads after the discard's clear, or settles it with none.
	DiscardCleared(chatID marotte.ChatID, opID string, landed bool) SteerOpResult
	OpSent(chatID marotte.ChatID, opID string, s SteerSend, queued bool, err error)
	// EndOp releases every row the op still owns, or, when their turn closed under
	// the op, answers that end with the rows still the op's, for the caller to
	// resolve before it lets go of the steer lock.
	EndOp(chatID marotte.ChatID, opID string) *SteerTurnEnd
	OnSteerJob(run func(SteerJob))
	// SetSteerLead records the send-now arrow's row for the next turn end; undo
	// takes it back when the stop could not be sent.
	SetSteerLead(chatID marotte.ChatID, key string) (undo func())
}

// SteerJobs is the record as the turn-end routine and the prompt's drain drive it.
type SteerJobs interface {
	// JobRows re-reads a job's rows in resend order; gone is the chat's teardown.
	JobRows(chatID marotte.ChatID, owner, lead string) (rows []SteerRow, gone bool)
	// StraysCleared marks the job's in-KAS rows named by a clear (all of them for a
	// dead bridge) as not in KAS; a row read before the clear leaves the set.
	StraysCleared(chatID marotte.ChatID, owner string, cleared []string, all bool)
	// Release hands the job's remaining rows, or only its in-KAS ones, back.
	Release(chatID marotte.ChatID, owner string, inKASOnly bool)
	// Unsent parks the job's rows as unsent, for the chat's next prompt.
	Unsent(chatID marotte.ChatID, owner string)
	// Resent writes each of the job's rows its boundary entry and answers their keys
	// and joined text in order; the rows stay the job's until Delivered or Unsent.
	Resent(chatID marotte.ChatID, owner, lead string) (keys []string, text string)
	// Delivered retires the job's rows once the prompt carrying them has opened.
	Delivered(chatID marotte.ChatID, owner string)
	// PlanFlush answers the sends the record owes after a read or a clear.
	PlanFlush(chatID marotte.ChatID) []SteerSend
	// NeedsPostLoadClear: KAS may re-inject steers this server also holds, and
	// nothing is reading yet.
	NeedsPostLoadClear(chatID marotte.ChatID) bool
	// PostLoadCleared: a row of this record's the clear named goes back to parked,
	// to be sent again in order.
	PostLoadCleared(chatID marotte.ChatID, cleared []string, landed bool)
	// AgentRowsWaiting reports an agent row in KAS's buffer, which a clear would
	// drop with no re-wake.
	AgentRowsWaiting(chatID marotte.ChatID) bool
	// NextParked answers the oldest parked row after turning every unsent row
	// into a parked one, so the chat's next prompt carries them; unsent rows stay
	// unsent while KAS may still hold their copies.
	NextParked(chatID marotte.ChatID) (key, text string, ok bool)
}

// RunCutter is what a rewind needs of the run registry: which of the runs a cut
// launched are still live, and a cancel that returns once the run has stopped, so
// nothing appends into the range being cut. Declared here, at the consumer;
// *agent.Runs satisfies it.
type RunCutter interface {
	// LiveRuns filters workflowIDs to the ones still holding a lease, each with the
	// label the reader knows it by, in the order given.
	LiveRuns(workflowIDs []string) []LiveRunRef
	// CancelRun stops one run and waits for its lease to be released. It answers
	// ErrRunStillLive when the run has not stopped inside the wait; the cancel itself
	// landed in that case.
	CancelRun(ctx context.Context, workflowID string) error
}

// LiveRunRef names one live run to the reader: KAS's workflow id and the recipe
// name it was launched from. The shape a rewind's 409 carries.
type LiveRunRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// EffortRecorder is what set_effort needs to leave a transcript record: one write
// of the model_switched entry an effort change produces. Declared here, at the
// consumer; *agent.BridgeCoordinator satisfies it.
//
// Its own role rather than a method on BridgeAccess, whose job is bridge lifecycle:
// this reaches the chat's LOG, and a lifecycle role that also writes the transcript
// is the aggregate the shape test refuses.
type EffortRecorder interface {
	// PersistEffortChange appends the entry for a tier the session accepted. It
	// writes nothing for an empty model, and reports no error: the level is already
	// on the chat record, so a failed append costs the record and not the change.
	PersistEffortChange(ctx context.Context, chatID marotte.ChatID, model string, level marotte.EffortLevel)
}

// ModeRecorder is what set_mode needs to leave a transcript record: one write of
// the mode_switched entry a mode change produces. Its own role beside
// EffortRecorder rather than a method on it, for that interface's reason — a role
// is named for the command that needs it — and *agent.BridgeCoordinator satisfies
// both.
type ModeRecorder interface {
	// PersistModeSwitch appends the entry for a mode the chat took. It reports no
	// error: the mode is already on the chat record, so a failed append costs the
	// record and not the change.
	PersistModeSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModeSwitched)
}

// ErrRunStillLive is CancelRun's answer for a run whose cancel landed but whose
// lease was still held when the wait ran out.
var ErrRunStillLive = errors.New("the run was told to stop but has not stopped yet")

// Roles is the wiring-time role set: the host names which of its interfaces answers
// each role once, at registration, and RegisterDefaults hands every handler only the
// roles that handler's signature declares. Taken by pointer wherever it travels.
//
// A plain struct rather than an interface, deliberately: no handler, helper or
// constructor takes this type, so nothing in the package widens as the host grows.
type Roles struct {
	Bridges BridgeAccess
	// Chats is the chat store directly, not through a getter on a wider composite —
	// that composite would need to carry Broadcast and the run surface too, which is
	// how a host becomes the one thing that qualifies.
	Chats     ChatStore
	Bus       Broadcaster
	Teardown  ChatTeardown
	Perms     PendingPermAccess
	Terminals TerminalAccess
	// Tabs is the open-tab set, and it may be nil: a build with no config
	// dir has no store to persist an arrangement to. The coordinator
	// answers the tab half of every operation with a 503 in that state.
	Tabs TabSet
	// Runs answers which chat launched a run, so a run tab opened with no
	// parent still nests under its conversation.
	Runs RunOwner
	// RunCutter is the run registry as a rewind uses it: the live runs a cut
	// launched, and the cancel that stops them before the cut.
	RunCutter RunCutter
	// Effort records the transcript entry a reader's reasoning-tier change leaves.
	Effort EffortRecorder
	// Modes records the transcript entry a reader's mode switch leaves.
	Modes ModeRecorder
	// Lifecycle is the process lifetime: the turn context and the
	// in-flight counter a shutdown waits on.
	Lifecycle   LifecycleAccess
	MCP         MCPAccess
	Admission   TurnAdmission
	TurnOutcome TurnOutcomeAccess
	// Steers records the steers this server sent, so the translate layer can
	// tell the user's own words from a workflow reporting into the same buffer.
	Steers SteerRecorder
	// SteerQueue is the server's record of the user's steers and KAS's buffer,
	// and SteerJobs the same record as the turn-end routine drives it.
	SteerQueue SteerQueue
	SteerJobs  SteerJobs
	// Status ends a chat's retained waiting_on_user claim after a command that IS
	// the user answering.
	Status ChatStatus
	// AuthReadiness carries prompt authentication outcomes to readiness.
	AuthReadiness *AuthReadiness
	// SpecApprovals is the spec-phase approval record, and it may be nil: a build
	// with no config dir has no store to persist one to. The handler answers a
	// 503 in that state rather than reporting an approval nothing kept.
	SpecApprovals SpecApprovals
	// Workspace is last for fieldalignment (a trailing length word stops
	// the leading-pointer count early).
	Workspace Workspace
}

// promptRoles holds the prompt path's collaborators and is shared with shell
// interception. It is built once at registration.
type promptRoles struct {
	bridges BridgeAccess
	chats   ChatStore
	// bus is separate from chats because they are separate owners.
	bus         Broadcaster
	lifecycle   LifecycleAccess
	mcp         MCPAccess
	admission   TurnAdmission
	turnOutcome TurnOutcomeAccess
	steers      SteerRecorder
	queue       SteerQueue
	jobs        SteerJobs
	auth        *AuthReadiness
	workspace   Workspace // last for fieldalignment, as in Roles
}
