package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

// The package's dependency contracts: each role interface is shaped by what its handler invokes,
// and a handler's reach is its signature, so widening one is a diff at its declaration.

type sessionScoped interface {
	// SessionID returns the current ACP session ID.
	SessionID() marotte.SessionID
}

type bridgeCaller interface {
	// Call sends an RPC call to kiro-cli.
	Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error)
	// CallAt is Call plus the read loop position at which the response arrived:
	// notifications queue on a buffered channel while a response goes straight to
	// the waiting Call, so the wire's own turn_end is routinely still unread.
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
}

// SessionCaller is one call addressed to one session: a chat's own, or the session of one of a
// run's steps.
type SessionCaller interface {
	bridgeCaller
	sessionScoped
}

// bridgeRPC is the full JSON-RPC surface — request, notification, and the
// answer to an inbound request from kiro-cli. Carries no prompt-slot method:
// sending a frame and owning the turn are different rights, and the cancel
// path sends a notification without holding the slot.
type bridgeRPC interface {
	SessionCaller

	// Notify sends a one-way notification to kiro-cli.
	Notify(ctx context.Context, method string, params any) error
	// Respond sends a permission response to kiro-cli.
	Respond(ctx context.Context, requestID int64, result any, err error) error
}

// promptSlot is the per-chat bridge lock a prompt call holds. The call's cancel and its grace budget live on the
// call's turn (turnOutcomeAccess.StartTurn, turnStopper.ArmStopGrace), never on the bridge, which would name
// whichever call holds it when the budget runs out.
type promptSlot interface {
	// TryAcquireForPrompt attempts to lock the bridge for prompting.
	TryAcquireForPrompt() bool
	// ReleaseAfterPrompt releases the prompt lock.
	ReleaseAfterPrompt()
}

// Bridge is the per-chat ACP bridge as a whole. Only a handler that owns a chat for
// the length of a turn needs it; helpers take a narrower parameter. Exported because
// bridgeAccess returns it and the runtime's wiring names it.
type Bridge interface {
	bridgeRPC
	promptSlot
}

// bridgeAccess provides bridge lifecycle operations needed by prompt,
// cancel, subagent, slash, and permission handlers.
type bridgeAccess interface {
	Bridge(chatID marotte.ChatID) Bridge
	// OpenBridge answers once the chat's log holds KAS's account of its session (sessionLoader).
	OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (Bridge, error)
	// BridgeLive reports a bridge for the chat that is past its session open: the state
	// a steer can be delivered into, where an opening one parks it.
	BridgeLive(chatID marotte.ChatID) bool
	// CloseBridge closes every turn the session hosted with outcome, then closes the
	// session: `cancelled` for a stop the reader asked for, `interrupted` otherwise.
	CloseBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome)
}

// sessionLoader opens a chat's bridge and answers nil once the chat's log holds KAS's account of
// its session: a resumed session's replay merged, or a fresh session started. ErrChatGone is
// terminal: the chat no longer exists. Any other error, the context's included, is retryable and
// means the log may still lag what KAS holds.
type sessionLoader interface {
	LoadSession(ctx context.Context, chatID marotte.ChatID) error
}

// ErrChatGone reports a chat whose record no longer exists; a bridge open wraps it.
var ErrChatGone = errors.New("chat not found")

// sessionClaims reports whether any chat's session chain holds sessionID, from a
// read that sees every record written before the call. complete is false when a
// chat file that exists could not be read.
type sessionClaims interface {
	SessionClaimed(ctx context.Context, sessionID string) (claimed, complete bool)
}

// stepSessions answers whether a KAS session is a workflow step's, read from KAS's session list.
type stepSessions interface {
	StepSessionOf(ctx context.Context, sessionID string) (workflowID string, found bool, err error)
}

// chatStore is the chat store as the command handlers use it: read the header,
// mutate it, record a draft and its attachments, delete the chat, and the two
// log operations a command owns, the rewind's truncate and the attachment count.
// It excludes List, RegisterRoutes and every append but the turn_open the turn
// registry writes. Exported because *agent.Runtime names this.
type chatStore interface {
	// Get returns the chat header at id, or false if it does not exist or could not be read.
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// Lookup is Get keeping missing apart from unreadable: found is false with
	// a nil error only when no record exists.
	Lookup(ctx context.Context, id marotte.ChatID) (c *marotte.Chat, found bool, err error)
	// Mutate is the header write primitive: load, apply, save, broadcast
	// chat_created / chat_updated.
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	// Revert appends the log's record of a rewind: turn and everything after it
	// become unreadable rather than absent, so nothing is cut and nothing is
	// re-closed. It answers the appended record, and the carrier's own turn_open
	// and turn_close when no turn survived the window and the log minted one to
	// hold it.
	Revert(ctx context.Context, chatID marotte.ChatID, turn, kasMessageID string) (record *marotte.Entry, minted []*marotte.Entry, err error)
	// RewindTarget resolves the prompt id a rewind addresses to the turn that
	// prompt opened and the id KAS holds the prompt under. False when no turn's
	// prompt carries that id.
	RewindTarget(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error)
	// PromptReceipt reads what the log holds of one prompt id: opened, bound by KAS, and its words.
	PromptReceipt(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.PromptReceipt, error)
	// PromptAttachmentPaths is every attachment path on the log's prompts after the
	// compaction watermark entry (the whole log when watermark is ""), in turn
	// order: what KAS still holds, which bounds how many more images this prompt
	// may inline. The image predicate is the caller's.
	PromptAttachmentPaths(ctx context.Context, chatID marotte.ChatID, watermark string) ([]string, error)
	// TurnCount is the header's turn_count, the whole-history read the rewind and
	// the model switch key on.
	TurnCount(ctx context.Context, chatID marotte.ChatID) (uint64, bool)
	// ReadBound is the chat's read position now, the clamp for an ack_tab.
	ReadBound(ctx context.Context, chatID marotte.ChatID) (uint64, error)
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
	// Delete removes the chat file and broadcasts chat_deleted, answering the session chain the
	// removed record held, read atomically with the removal; a record it cannot read is kept and
	// the error answered. Bridge exits, model switches and restarts never delete.
	Delete(ctx context.Context, id marotte.ChatID) (sessionChain []string, err error)
}

// broadcaster publishes a domain event to every connected client.
type broadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// RunStopCause is why a by-chain teardown ends a chat's runs, which decides whether
// the stop is attributed to the user.
type RunStopCause int

const (
	// RunStopTabClosed is the user closing the chat's tab with retention off.
	RunStopTabClosed RunStopCause = iota
	// RunStopRetention is the retention purge, which nobody asked for.
	RunStopRetention
	// RunStopChatDeleted is the user deleting the chat.
	RunStopChatDeleted
)

// chatTeardown ends a chat's life, in one of two ways. Ending the chat's runs is
// inside both rather than a step a caller pairs with them: a run is durable state a
// dead bridge only pauses, so it must be ended before the bridge goes.
type chatTeardown interface {
	// DeleteChatStateByChain is the delete path: delete the chat's runs and its KAS
	// sessions, and drop every in-memory trace. Every delete removes the record
	// first, so a bridge opened once this runs answers ErrChatGone; the run and session
	// deletes are driven from the session chain the removed record held.
	DeleteChatStateByChain(ctx context.Context, chatID marotte.ChatID, sessionChain []string, cause RunStopCause)
	// CloseChatState is the close path: cancel the chat's runs and drop the
	// in-memory state, but leave the runs and the durable KAS session on disk
	// so the chat can be reopened and so History can still list it.
	CloseChatState(ctx context.Context, chatID marotte.ChatID)
	// BeginChatTeardown marks the chat as going away BEFORE the teardown's cancel,
	// so the turn end that cancel causes resends nothing. keep is a close, whose
	// record survives and keeps a note per unread steer. Idempotent.
	BeginChatTeardown(chatID marotte.ChatID, keep bool)
}

// pendingPermAccess provides the pending-permission bookkeeping handlers
// need: an unanswered request is replayed to a reconnecting client, so
// answering or abandoning one has to retire the entry.
// permissionCanceller answers a chat's pending permission asks `cancelled` ahead of a session/cancel.
type permissionCanceller interface {
	// CancelPendingPermissions takes each pending permission ask of chatID in scope, in ask order, and answers it
	// cancelled. An ask a card answer claimed first is skipped. It returns an error wrapping marotte.ErrBridgeExited
	// when the session ended under an answer, and one wrapping ErrCancelNotDelivered when an answer failed on a live
	// session after its retries; either stops before the next ask.
	CancelPendingPermissions(ctx context.Context, chatID marotte.ChatID, scope CancelScope) error
}

type pendingPermAccess interface {
	permissionCanceller
	// ClearPendingInputAsks drops chatID's elicitation and user-input asks in scope, silently.
	ClearPendingInputAsks(chatID marotte.ChatID, scope CancelScope)
	// TakePendingPerm claims the request before its answer is sent, reporting false
	// when another surface already answered it, its bridge ended, or it is not a
	// kind ask of chatID's. settledBy travels with the claim because the delivered
	// answer is broadcast to the surfaces that lost, and their card says who answered.
	TakePendingPerm(chatID marotte.ChatID, requestID int64, kind marotte.DecisionKind, settledBy marotte.SettledBy) (AskReply, bool)
	// TakePendingPermissionOption retains an off-list request and reports it as
	// pending but not offered. A successful result validates and claims in one
	// operation, so another surface cannot answer between those steps.
	TakePendingPermissionOption(chatID marotte.ChatID, requestID int64, optionID string, settledBy marotte.SettledBy) (reply AskReply, pending, offered bool)
	// PendingPermission reads an unanswered permission ask without claiming it.
	PendingPermission(chatID marotte.ChatID, requestID int64) (marotte.PermissionNeededPayload, bool)
}

// AnswerOutcome is how a claimed ask settled under its answer.
type AnswerOutcome uint8

const (
	// AnswerDelivered is an answer the agent received.
	AnswerDelivered AnswerOutcome = iota
	// AnswerWithdrawn is an ask a turn cancel, run end, KAS withdrawal or its bridge's end settled
	// first: the answer reached nobody, and that settlement retires the card.
	AnswerWithdrawn
	// AnswerReopened is a write that failed on a live bridge: the ask is pending again.
	AnswerReopened
)

// AskReply answers one claimed ask on the bridge it arrived on, never on the chat's bridge at
// answer time: a successor would resolve an unrelated request under the same id. Respond is
// called once and settles the ask exactly once, announced as decision_settled: a terminal
// transition that landed during the write wins (turn cancel or run end silently, a KAS withdrawal
// as moot, the bridge's end as ended) and is AnswerWithdrawn, as is a write refused because the
// bridge ended; else AnswerDelivered, as the claim's settledBy. Any other failure re-offers the
// ask as pending and is AnswerReopened with the write's error, the only outcome carrying one.
type AskReply interface {
	Respond(ctx context.Context, result any) (AnswerOutcome, error)
}

// profileSwitcher moves the security profile to Custom before an always answer saves a rule.
type profileSwitcher interface {
	// EnsureCustomProfile is a no-op on Custom; otherwise it runs Customize and returns once
	// the user permissions file is written. Errors are securityprofile's.
	EnsureCustomProfile(ctx context.Context) error
}

// terminalAccess is the interrupt's process half: a turn cancel must reach
// the turn's agent terminals or it strands them.
type terminalAccess interface {
	// KillForTurn kills the running terminals the named turn created, and nothing else: a
	// background command an earlier turn left running is not the cancel's to kill. A zero ref
	// kills nothing. It does not unregister them; the agent's release does.
	KillForTurn(turn TurnRef)
}

// turnStopper is the reader's stop, scoped to what ran when it was asked for.
type turnStopper interface {
	// RequestStop records a stop on the chat's turn sequence and captures, in one section, the turns it targets.
	// It is called BEFORE any signal, so a turn opened to replace one the reader stopped (the empty-turn retry)
	// sees it whichever bridge the signal reached. session/cancel names no turn, so a prompt written before it would
	// be the one KAS cancels: until ReleaseStop the send gate lets no prompt go, and one it let go before the stop is
	// written ahead of the stop's session/cancel or not at all (AwaitPromptSend's commit).
	RequestStop(chatID marotte.ChatID) *TurnStop
	// ReleaseStop lifts the stop's send hold; called once per RequestStop.
	ReleaseStop(stop *TurnStop)
	// ArmStopGrace acts on the captured targets once the cancel was sent, each revalidated: Next is kept unsent,
	// and unless its end comes within d, KAS's turn (Wire) is closed and the sent prompt's call (Call) is
	// cancelled with ErrCancelGraceExpired. False when no target was still live.
	ArmStopGrace(stop *TurnStop, d time.Duration) bool
}

// TurnRef names one turn of one chat. Turn ids are never reused, so an operation handed a ref acts on that turn or,
// once it has gone, on nothing. The zero ref names none.
type TurnRef struct {
	Chat marotte.ChatID
	Turn string
}

// Named reports whether r names a turn.
func (r TurnRef) Named() bool { return r.Turn != "" }

// TurnStop is what one stop targets, captured at its admission; a zero ref names none.
type TurnStop struct {
	// Own is the chat's own turn, closing included: its terminals are the stop's.
	Own TurnRef
	// Call is a sent prompt whose session/prompt KAS has not answered: Own, or a prompt a later turn_start
	// superseded.
	Call TurnRef
	// Next is the admitted, unsent prompt the send gate would send next because nothing else runs.
	Next TurnRef
	Chat marotte.ChatID
	// Token names the stop's send hold.
	Token uint64
	// Admitted names the reservation of a prompt admitted as Next would be but whose turn has not opened; zero for
	// none. The turn that reservation opens is kept unsent.
	Admitted uint64
	// Wire reports Own as an open turn KAS started.
	Wire bool
}

// ErrPromptStopped fails a prompt the reader stopped before its session/prompt was sent; it concludes cancelled.
var ErrPromptStopped = errors.New("the prompt was stopped before it was sent")

// ErrTurnStopped is OpenTurn's refusal of an open that replaces a turn the reader stopped (TurnOpen.Replaces).
var ErrTurnStopped = errors.New("the turn this open replaces was stopped")

// shellReader reads the built-in shell's output for a `#` terminal reference.
type shellReader interface {
	// ReadShell returns the newest maxLines lines; ok is false while no shell
	// process has started.
	ReadShell(maxLines int) (out ShellText, ok bool)
}

// ShellText is the built-in shell's output as plain text, oldest line first.
type ShellText struct {
	Text string
	// AltScreen reports that a full-screen program holds the screen, so Text is
	// the shell output beneath it rather than what is shown.
	AltScreen bool
	// Exited reports that the shell process ended; Text is its final output.
	Exited bool
}

// Workspace carries the paths handlers resolve against: the working directory the
// hook writer and the shell spawn need, the config directory the prompt reads a
// setting out of, and the uploads directory an attachment may also name. An
// attachment path is confined to those roots before it is read. It also carries
// the built-in shell a `#` terminal reference reads.
type Workspace struct {
	// Terminal is the built-in shell; nil resolves a terminal reference to a reason.
	Terminal shellReader
	// Dir is the workspace root every relative path resolves against.
	Dir string
	// ConfigDir is where settings and hook files live.
	ConfigDir string
	// UploadsDir is the second root an ATTACHMENT path may name (where composer uploads land,
	// beside the workspace). Nothing else resolves against it, and a relative path never reaches
	// it. Empty is legal and inert: workspace-only confinement.
	UploadsDir string
}

// checkInside refuses an attachment path that escapes every root one may name.
// The workspace leads, so a relative path resolves there and an unresolvable
// path reports the workspace's own error.
func (w Workspace) checkInside(rel string) error {
	_, _, err := workspace.ConfineAnyAbs(w.attachmentRoots(), rel)
	return err
}

// errAttachmentOutsideRoots wraps a confinement refusal from OpenAttachment, so a
// caller can tell a path that names no allowed root from a file that failed to
// open.
var errAttachmentOutsideRoots = errors.New("attachment path is outside the allowed roots")

// openAttachment opens the regular file p names under the attachment roots and
// returns it with its descriptor's FileInfo and its absolute path. The caller
// closes f. A refused path wraps errAttachmentOutsideRoots; a FIFO, device or
// directory is atomicfile.ErrNotRegular, and the open never blocks on one.
func (w Workspace) openAttachment(p string) (f *os.File, info os.FileInfo, abs string, err error) {
	root, name, err := workspace.ConfineAnyAbs(w.attachmentRoots(), p)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: %w", errAttachmentOutsideRoots, err)
	}
	f, info, err = openConfined(root, name)
	if err != nil {
		return nil, nil, "", err
	}
	return f, info, filepath.Join(root, name), nil
}

func (w Workspace) attachmentRoots() []string {
	return []string{w.Dir, w.UploadsDir}
}

// openConfined opens name through an os.Root on root, so every component is
// re-resolved inside root at the open rather than trusting the earlier verdict.
// The root is opened per call: the uploads directory is created after the
// command layer is wired, so a root held from construction would be nil on a
// first boot.
func openConfined(root, name string) (*os.File, os.FileInfo, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	return atomicfile.OpenRegularInRoot(r, name)
}

// lifecycleAccess is the process-lifetime seam: the context a turn runs under, and
// the in-flight accounting that makes agent shutdown wait for it. Shutdown cancels
// the first and waits on the second.
type lifecycleAccess interface {
	// TurnContext returns the context an in-flight turn runs under, plus
	// the teardown its handler defers.
	TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc)
	InflightAdd(delta int)
	InflightDone()
	// Draining reports that shutdown has begun, after which nothing sends.
	Draining() bool
}

// AdmissionOutcome is ReserveTurnForPrompt's answer.
type AdmissionOutcome int

const (
	// AdmissionAcquired means the caller holds the chat's admission slot.
	AdmissionAcquired AdmissionOutcome = iota
	// AdmissionBusy is the holder a steer can reach
	// (marotte.TurnOpenSource.SteerableHolder). Answered as the plain 409, on
	// which the client steers or queues, per the chat's mode.
	AdmissionBusy
	// AdmissionStarting is the fail-closed answer when no steerable holder can take
	// the prompt: a cold session open, a shell, or a run or close hold kiro-cli
	// reports with no steerable turn. Answered as 409 with the additive
	// `reason: "starting"`, which hands the text back.
	AdmissionStarting
)

// turnAdmission is the chat's admission slot as the command handlers hold it:
// a bare per-chat reservation decided before any turn exists, plus the read
// that says who holds it. Separate from turnOutcomeAccess because the steer reads
// the holder and the rewind reserves the slot, and neither opens a turn.
type turnAdmission interface {
	// ReserveTurnForPrompt takes the chat's admission slot for a prompt,
	// minting no turn — a bare per-chat reservation, decided synchronously
	// before any bridge exists. A held slot parks the caller up to wait;
	// the refusal is keyed on the holder's source — see AdmissionOutcome.
	ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration) AdmissionOutcome
	// TryReserveTurn takes the admission slot iff it is free, never waiting.
	TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool
	// TryReserveIdleTurn is TryReserveTurn that also refuses while a turn is open
	// without a reservation (a wire turn), checked in the same acquisition.
	TryReserveIdleTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool
	// TryReserveTurnFenced is TryReserveTurn for a close's drain: it also refuses
	// once the fence no longer holds, sparing a reservation the open would refuse.
	TryReserveTurnFenced(chatID marotte.ChatID, source marotte.TurnOpenSource, fence TurnFence) bool
	// PromptHolder names the prompt-class turn holding admission: the pending
	// prompt, else an own prompt-class turn. A bare reservation or a wire turn is
	// never named.
	PromptHolder(chatID marotte.ChatID) (turnID string, ok bool)
	// ReleaseTurnReservation frees the admission slot, waking every
	// waiter.
	ReleaseTurnReservation(chatID marotte.ChatID)
	// AdmissionHolderSource reports who holds the chat's admission: the open turn's
	// source when one is open, else the reservation's. A non-prompt holder matters
	// because a steer aimed into it lands somewhere the reader did not mean — a
	// workflow step's turn reads it as the step's own input — so cmdSteer refuses
	// instead.
	AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool)
}

// TurnFence names a closed turn by its lifecycle epoch and open sequence. A drain
// opens its turn only while no turn opened after the closed one; the zero fence is
// unfenced.
type TurnFence struct {
	Epoch uint64
	Seq   uint64
}

// TurnOpen is what OpenTurn appends and registers: the source, the prompt (nil
// for a source with none), the header fallback, and the fence the open must find
// intact.
type TurnOpen struct {
	Prompt *marotte.EntryPrompt
	Init   func(*marotte.Chat)
	// Replaces names the turn this open stands in for (the empty-turn retry's): a stop requested after that turn
	// opened refuses the open with ErrTurnStopped, decided in the section that would append it.
	Replaces string
	Source   marotte.TurnOpenSource
	Fence    TurnFence
	// Awaited takes a second completion handle for a caller that awaits the turn
	// past its runner's release; that caller releases it.
	Awaited bool
}

// ErrTurnSuperseded is OpenTurn's refusal of a fenced open after a newer turn
// opened, or after the chat's lifecycle was torn down.
var ErrTurnSuperseded = errors.New("a newer turn opened since the closed one")

// turnOutcomeAccess is what a prompt handler needs to run an admitted turn's
// lifecycle: open it, wait for its outcome, and close it — whether the engine
// answered or the call failed.
type turnOutcomeAccess interface {
	// OpenTurn appends the turn_open at admission and creates the turn's registry
	// record in one operation: prompt is the turn's prompt (nil for a source with
	// none) and init is the header fallback for an id no record exists for, nil for
	// every source but prompt and local_shell. Refuses on a dead ctx before it
	// appends, and with ErrTurnSuperseded when the open's fence no longer holds.
	// The id it returns is what StartTurn and every closer are handed.
	OpenTurn(ctx context.Context, chatID marotte.ChatID, open TurnOpen) (string, error)
	// StartTurn stamps the model and the credit baseline onto the named turn, with
	// the bridge live, immediately before the call that drives it. cancel trips that
	// call when kiro-cli abandons the turn or a stop's grace runs out, nil for a turn
	// with no prompt call; a non-nil cancel is the turn's until EndPromptCall. False
	// for a dead ctx and for a turn the registry no longer holds, and a caller
	// answering false runs the turn end rule on that turn itself.
	StartTurn(ctx context.Context, turn TurnRef, cancel context.CancelCauseFunc) bool
	// EndPromptCall forgets the cancel StartTurn stored once its call returned, so no
	// later interrupt or grace reaches it.
	EndPromptCall(turn TurnRef)
	// AwaitTurn blocks until the named turn has finalized and reports what
	// it did. A caller holding that turn's handle never receives
	// marotte.ErrNoSuchTurn.
	AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error)
	// AwaitTurnBound blocks until KAS binds the named turn's prompt, its proof of receipt (true),
	// or the turn finalizes unbound (false). The caller holds the turn's handle.
	AwaitTurnBound(ctx context.Context, chatID marotte.ChatID, turnID string) (bool, error)
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
	// StopRequestedAfter reports whether the reader asked to stop after turnID
	// opened — the empty-turn retry's consent clause.
	StopRequestedAfter(chatID marotte.ChatID, turnID string) bool
	// FinalizeLocalShellTurn closes a `!cmd` turn marotte ran itself, appending
	// its one text entry first.
	FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, turnID, output string)
	// AbandonInFlightTurn finalizes a turn the prompt call could not finish, and
	// the prompt's exits before any call. stop is what the failure CONCLUDES —
	// `interrupted` for a fault, `cancelled` for a user cancel KAS never acked — and
	// reason is the user-facing account of it, which the turn_close carries with
	// kind, the classified failure the client offers a remedy for (empty for
	// none). It waits for no read loop position.
	AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string, kind marotte.FailureKind, seq uint64)
}

// promptSendGate holds an admitted prompt's session/prompt until the chat's session may take it.
type promptSendGate interface {
	// AwaitPromptSend returns once turnID's prompt may be sent, with the commit its session/prompt call carries
	// (marotte.WithWriteCommit; nil for none), else the error that fails it unsent: ctx ended, the turn closed during
	// the wait, or the reader stopped it (ErrPromptStopped), which the commit also answers for a stop that followed.
	AwaitPromptSend(ctx context.Context, chatID marotte.ChatID, turnID string) (commit func() error, err error)
}

// preSendCompactor is the compaction policy's before-a-send trigger. It returns
// once the prompt may be sent, whatever the compaction did; it never refuses.
type preSendCompactor interface {
	PreSendCompact(ctx context.Context, chatID marotte.ChatID)
}

// SteerRecorder is what the steer commands need of the steer ledger: record that
// THIS server sent a steer under an id before the RPC, which is the only ordering
// that beats KAS's own notification (see cmdSteer).
type SteerRecorder interface {
	RecordUserSteer(chatID marotte.ChatID, steerID string)
}

// The refusal classes a steer operation answers with; the client branches on them.
const (
	SteerRefuseNoTurn     = "no_turn"
	SteerRefuseFull       = "full"
	SteerRefuseNotWaiting = "not_waiting"
	SteerRefuseNotUser    = "not_user"
	SteerRefuseAgentRows  = "agent_rows_waiting"
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
	Source marotte.TurnOpenSource
	// TurnSeq is the closed turn's open sequence: a close pipeline resolves only
	// the ends at or below its own.
	TurnSeq     uint64
	BridgeDeath bool
}

// SteerEnd is one pending turn end: the owner its rows carry and the end itself.
type SteerEnd struct {
	Owner string
	End   SteerTurnEnd
}

// SteerJob is a flush a frame fold hands to the command side, which runs it under
// the chat's steer lock.
type SteerJob struct {
	Chat marotte.ChatID
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
	// Turn is the prompt turn a delivering route is aimed at; the record routes
	// the row only while it names that turn as starting or bound.
	Turn        string
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

// SteerRow is one row a pending end holds, or one unread row the drain sends.
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
	BeginDiscard(chatID marotte.ChatID, opID string) (needsClear bool)
	// DiscardCleared re-reads after the discard's clear, or settles it with none.
	DiscardCleared(chatID marotte.ChatID, opID string, landed bool) SteerOpResult
	OpSent(chatID marotte.ChatID, opID string, s SteerSend, queued bool, err error)
	// EndOp releases every row the op still owns. When a turn closed under the op
	// its rows go to a pending end that close's pipeline resolves; when a stale
	// bind ended the op's channel they are settled and reroute is true, for the
	// caller to route them before it lets go of the steer lock.
	EndOp(chatID marotte.ChatID, opID string) (reroute bool)
	OnSteerJob(run func(SteerJob))
	// SetSteerLead records the send-now arrow's row, sent first; undo takes it
	// back when the stop could not be sent.
	SetSteerLead(chatID marotte.ChatID, key string) (undo func())
}

// SteerJobs is the record as the close pipeline and the prompt's drain drive it.
type SteerJobs interface {
	// Ends lists the chat's pending turn ends, oldest first.
	Ends(chatID marotte.ChatID) []SteerEnd
	// JobRows re-reads an end's rows, lead first; gone is the chat's teardown.
	JobRows(chatID marotte.ChatID, owner string) (rows []SteerRow, gone bool)
	// StraysCleared marks the end's in-KAS rows named by a clear (all of them for a
	// dead bridge) as not in KAS; a row read before the clear leaves the set.
	StraysCleared(chatID marotte.ChatID, owner string, cleared []string, all bool)
	// Release hands the end's remaining rows, or only its in-KAS ones, back.
	Release(chatID marotte.ChatID, owner string, inKASOnly bool)
	// EndUnsent makes every row the end still owns that KAS does not hold unsent
	// and unowned, and drops the end.
	EndUnsent(chatID marotte.ChatID, owner string)
	// UnsentRows answers the unread rows the drain may send, lead first; none
	// while KAS may re-inject them, unless the drain follows a drained death.
	UnsentRows(chatID marotte.ChatID, deathDrained bool) []SteerRow
	// Delivered retires the named rows, once the prompt carrying them has opened.
	Delivered(chatID marotte.ChatID, keys []string)
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
	// NextParked answers the head parked row for turnID, the lead first, after
	// parking every unowned unsent row; it parks and answers nothing while the
	// record names turnID neither starting nor bound, or KAS may re-inject them.
	NextParked(chatID marotte.ChatID, turnID string) (key, text string, ok bool)
}

// runCutter is what a rewind needs of the run registry: which of the runs a cut
// launched are still live, and a cancel that returns once the run has stopped, so
// nothing appends into the range being cut. Declared here, at the consumer;
// *agent.Runs satisfies it.
type runCutter interface {
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

// effortRecorder is what set_effort needs to leave a transcript record: one write of the
// model_switched entry an effort change produces. Its own role because it reaches the chat's LOG,
// which bridgeAccess's lifecycle job does not.
type effortRecorder interface {
	// PersistEffortChange appends the entry for a tier the session accepted. It
	// writes nothing for an empty model or a chat no prompt has reached, and reports
	// no error: the level is already on the chat record, so a failed append costs the
	// record and not the change.
	PersistEffortChange(ctx context.Context, chatID marotte.ChatID, model string, level marotte.EffortLevel)
	// ThinkingDefaultOff reports whether the model runs with thinking off when the
	// chat has not chosen, which decides whether a tier pick must turn it back on.
	ThinkingDefaultOff(model string) bool
	// EffortSeedable reports whether level may be remembered as model's new-chat
	// seed: false only when the catalog lists model's tiers and level is not one.
	EffortSeedable(model string, level marotte.EffortLevel) bool
}

// modeRecorder is what set_mode needs to leave a transcript record: one write of the mode_switched
// entry a mode change produces; a role is named for the command that needs it.
type modeRecorder interface {
	// PersistModeSwitch appends the entry for a mode the chat took, and nothing on a
	// chat no prompt has reached. It reports no error: the mode is already on the chat
	// record, so a failed append costs the record and not the change.
	PersistModeSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModeSwitched)
}

// ErrRunStillLive is CancelRun's answer for a run whose cancel landed but whose
// lease was still held when the wait ran out.
var ErrRunStillLive = errors.New("the run was told to stop but has not stopped yet")

// Roles is the wiring-time role set: the host names which interface answers each role once, at
// registration, and RegisterDefaults hands every handler only the roles its signature declares. A
// plain struct no handler takes, so nothing widens as the host grows.
type Roles struct {
	Bridges bridgeAccess
	// Loader resumes a chat whose log must reflect KAS before anything is decided from it.
	Loader sessionLoader
	// Chats is the chat store directly, not through a getter on a wider composite —
	// that composite would need to carry Broadcast and the run surface too, which is
	// how a host becomes the one thing that qualifies.
	Chats chatStore
	// Sessions answers whether a chat already claims a KAS session, read fresh
	// so the run purge can decide under the membership lock.
	Sessions  sessionClaims
	Bus       broadcaster
	Teardown  chatTeardown
	Perms     pendingPermAccess
	Profiles  profileSwitcher
	Terminals terminalAccess
	Stops     turnStopper
	// Tabs is the open-tab set, and it may be nil: a build with no config
	// dir has no store to persist an arrangement to. The coordinator
	// answers the tab half of every operation with a 503 in that state.
	Tabs TabSet
	// Runs answers which chat launched a run, so a run tab opened with no
	// parent still nests under its conversation.
	Runs runOwner
	// RunCutter is the run registry as a rewind uses it: the live runs a cut
	// launched, and the cancel that stops them before the cut.
	RunCutter runCutter
	// Effort records the transcript entry a reader's reasoning-tier change leaves.
	Effort effortRecorder
	// Modes records the transcript entry a reader's mode switch leaves.
	Modes modeRecorder
	// Lifecycle is the process lifetime: the turn context and the
	// in-flight counter a shutdown waits on.
	Lifecycle   lifecycleAccess
	Admission   turnAdmission
	TurnOutcome turnOutcomeAccess
	// Steers records the steers this server sent, so the translate layer can
	// tell the user's own words from a workflow reporting into the same buffer.
	Steers SteerRecorder
	// Queue appends a follow-up only while the chat's turn is live.
	Queue QueueAccess
	// SteerQueue is the server's record of the user's steers and KAS's buffer,
	// and SteerJobs the same record as the close pipeline drives it.
	SteerQueue SteerQueue
	SteerJobs  SteerJobs
	// Status ends a chat's retained waiting_on_user claim after a command that IS
	// the user answering.
	Status chatStatus
	// AuthReadiness carries prompt authentication outcomes to readiness.
	AuthReadiness *AuthReadiness
	// Compactor runs the compaction policy before a prompt is sent. Nil sends
	// with no pre-send check.
	Compactor preSendCompactor
	// SendGate holds each session/prompt a chat's prompt sends until the session may
	// take it. Nil sends at once.
	SendGate promptSendGate
	// Renamer renames every KAS session in a chat's chain, so a closed chat's
	// rename reaches History at once.
	Renamer SessionRenamer
	// SpecApprovals is the spec-phase approval record, and it may be nil: a build
	// with no config dir has no store to persist one to. The handler answers a
	// 503 in that state rather than reporting an approval nothing kept.
	SpecApprovals SpecApprovals
	// Tangents resolves a tangent's parent and reads its summary for a merge.
	Tangents TangentAccess
	// StepSessions refuses resuming a workflow step's session as a chat. Nil checks nothing.
	StepSessions stepSessions
	// Workspace is last for fieldalignment (a trailing length word stops
	// the leading-pointer count early).
	Workspace Workspace
}

// promptRoles holds the prompt path's collaborators and is shared with shell
// interception. It is built once at registration.
type promptRoles struct {
	bridges bridgeAccess
	chats   chatStore
	// bus is separate from chats because they are separate owners.
	bus         broadcaster
	lifecycle   lifecycleAccess
	admission   turnAdmission
	turnOutcome turnOutcomeAccess
	steers      SteerRecorder
	queue       SteerQueue
	jobs        SteerJobs
	followups   QueueAccess
	compactor   preSendCompactor
	sendGate    promptSendGate
	auth        *AuthReadiness
	workspace   Workspace // last for fieldalignment, as in Roles
}
