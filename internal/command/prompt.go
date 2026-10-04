package command

// Prompt command handler: validates, admits, opens the turn, acquires the
// bridge, sends to kiro-cli, handles empty-turn recovery.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/settings"
)

// validatePromptPayload parses and validates the prompt command payload.
func validatePromptPayload(cmd *marotte.ClientCommand) (marotte.PromptCommand, int, error) {
	var p marotte.PromptCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	if p.Text == "" {
		return p, http.StatusBadRequest, errEmptyPrompt
	}
	if len(p.Text) > maxPromptBytes {
		return p, http.StatusRequestEntityTooLarge, errPromptTooLong
	}
	if p.MessageID == "" {
		return p, http.StatusBadRequest, errMissingMessageID
	}
	if !ValidMessageID(p.MessageID) {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	if !ValidIdent(p.Model) {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	return p, 0, nil
}

// promptRetryDelay is the one wait this package retries at.
const promptRetryDelay = 2 * time.Second

// promptReply is a session/prompt response and the read loop position at
// which it arrived. The position travels with the response so the turn's
// local settle can order itself against notifications still queued behind
// it — reading the response alone would decide the wire never closed the
// turn while turn_end is still a few frames back.
type promptReply struct {
	resp *marotte.RPCResponse
	seq  uint64
}

// retry re-invokes fn up to maxAttempts more times, promptRetryDelay
// apart, for as long as shouldRetry keeps saying yes. The delay is fixed,
// no backoff.
func retry(ctx context.Context, maxAttempts int, shouldRetry func(error) bool, fn func() (promptReply, error)) (promptReply, error) {
	result, err := fn()
	if err == nil || !shouldRetry(err) {
		return result, err
	}
	for range maxAttempts {
		select {
		case <-time.After(promptRetryDelay):
		case <-ctx.Done():
			return result, err
		}
		result, err = fn()
		if err == nil || !shouldRetry(err) {
			break
		}
	}
	return result, err
}

// callPromptWithRetry sends the prompt to kiro-cli, retrying only the
// classes a second attempt can actually fix.
func callPromptWithRetry(ctx context.Context, sb bridgeCaller, params map[string]any, chatID marotte.ChatID) (promptReply, error) {
	return retry(ctx, 2, func(err error) bool {
		class := classifyPromptFailure(err)
		retry := class == classBusy || class == classTransient
		slog.Warn("prompt failure",
			"chat_id", chatID, "class", class.String(), "retry", retry, keyError, err)
		return retry
	}, func() (promptReply, error) {
		resp, seq, err := sb.CallAt(ctx, marotte.MethodPrompt, params)
		return promptReply{resp: resp, seq: seq}, err
	})
}

// recoverEmptyTurn re-prompts a turn that ended having produced nothing:
// recreate the session, then send the same prompt once more as a turn of its
// own, with the same prompt in its turn_open.
//
// result is the FINALIZED turn's captured outcome, never the live accumulator,
// which can still be withholding the turn's only text when this runs. Both
// admission holds are already released, so the retry re-reserves with a try and
// a user prompt that won the slot abandons it.
func recoverEmptyTurn(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, result marotte.TurnResult, p *marotte.PromptCommand, params map[string]any) {
	// A verb KAS answers itself produces no content by design, so an
	// empty turn is the correct outcome and recovery is pure damage.
	if kasClaimsPromptText(p.Text) {
		return
	}
	// WireEnded: a locally-closed turn's outcome is only the prompt
	// response's, nothing richer, so re-prompting on it is a guess.
	if !result.WireEnded || result.Stop != marotte.StopReasonEndTurn || !result.EmittedNothing {
		return
	}
	if roles.turnOutcome.TurnOpenedAfter(chatID, turnID) {
		slog.Info("empty turn: a later turn opened on this chat, so the binding was not ours",
			"chat_id", chatID, "turn", turnID)
		return
	}
	if !roles.admission.TryReserveTurn(chatID, marotte.TurnSourceEmptyRetry) {
		slog.Warn("empty turn: another turn was admitted during recovery, abandoning retry",
			"chat_id", chatID)
		return
	}
	defer roles.admission.ReleaseTurnReservation(chatID)
	slog.Warn("empty turn detected, recreating session", "chat_id", chatID)
	refreshRetrySession(ctx, roles.bridges, roles.chats, chatID)
	retryEmptyTurnPrompt(ctx, roles, chatID, p, params)
}

// turnStopBeforeStart decides what a prompt exit that never reached StartTurn
// concludes: promptFailureAccount's cancelled arm read off the context, since no
// ACP call was made and there is no failure to classify. The cancelled arm
// supplies no prose by rule, so `interrupted` is the caller's to word.
func turnStopBeforeStart(ctx context.Context, interrupted string) (stop marotte.StopReason, reason string) {
	if errors.Is(context.Cause(ctx), ErrCancelGraceExpired) {
		return marotte.StopReasonCancelled, ""
	}
	return marotte.StopReasonInterrupted, interrupted
}

// refreshRetrySession abandons the session that answered nothing: close its
// bridge and detach the chat from it. The empty turn already closed with
// `outcome: empty`, and the retry that follows is a second card under it, so
// nothing is written to say the session was refreshed.
func refreshRetrySession(ctx context.Context, bridges BridgeAccess, chats ChatStore, chatID marotte.ChatID) {
	// The respawn kills the process, so a chat-parented run's open step turns
	// close interrupted through the death closer's run arm; the chat's own empty
	// turn settled before recovery began, so the chat arm finds nothing.
	bridges.CloseBridge(ctx, chatID, marotte.TurnOutcomeInterrupted)
	if _, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		// Detach, don't forget: the abandoned session still holds this
		// chat's earlier transcript on disk and must stay in the chain
		// or the reaper sweeps it as an orphan.
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
	}
}

// retryEmptyTurnPrompt respawns the bridge and re-sends the prompt as a turn of
// its own: turn_open{source: empty_retry} with the same prompt. The caller holds
// the retry's admission reservation.
func retryEmptyTurnPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, params map[string]any) {
	sb2, err2 := roles.bridges.OpenBridge(ctx, chatID, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		// The retry's turn never opens, so no turn carries this failure and the
		// toast this code routes to is its only surface.
		roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
			Code:    marotte.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + rpcerr.Text(err2),
		}))
		return
	}
	// Take the new bridge's prompt slot the same way the prompt goroutine
	// does, rather than asserting it: OpenBridge leaves the bridge
	// registered and idle, so a concurrent taker can win the slot between
	// that return and this line. Losing the race abandons the retry — the
	// right direction, since the empty turn it would replace already closed.
	if !sb2.TryAcquireForPrompt() {
		slog.Warn("empty turn: another turn started during recovery, abandoning retry",
			"chat_id", chatID)
		return
	}
	defer sb2.ReleaseAfterPrompt()

	// The retry needs its own cancellable context registered as the
	// in-flight prompt: session/cancel is a notification nothing acks, so
	// the grace budget is what unblocks a turn KAS never answers.
	ctx, cancelRetry := context.WithCancelCause(ctx)
	defer cancelRetry(nil)
	sb2.BeginPromptCall(cancelRetry)
	defer sb2.EndPromptCall()

	params[marotte.KeySessionID] = sb2.SessionID()
	// The retry is a turn of its own, closed on every path out of here, since
	// the turn it replaces is already closed.
	retryTurn, err := roles.turnOutcome.OpenTurn(ctx, chatID, marotte.TurnSourceEmptyRetry, promptEntry(p, nil), nil)
	if err != nil {
		// A dead ctx refused the open before anything was written, so no turn
		// exists to close; the reader's surface is the empty turn's own footer.
		slog.Warn("empty turn: the retry turn could not open", "chat_id", chatID, keyError, err)
		return
	}
	defer roles.turnOutcome.ReleaseTurn(chatID, retryTurn)
	if !roles.turnOutcome.StartTurn(ctx, chatID, retryTurn) {
		// The ctx died between the open and the start: the retry's turn exists and
		// closes through the turn end rule, and this exit broadcasts nothing.
		slog.Warn("empty turn: the retry turn could not start", "chat_id", chatID)
		stop, reason := turnStopBeforeStart(ctx, "The retry was cancelled before the agent answered.")
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, retryTurn, stop, reason)
		return
	}
	deliverParkedSteers(ctx, roles, chatID)
	reply, retryErr := callPromptWithRetry(ctx, sb2, params, chatID)
	if retryErr != nil {
		slog.Error("retry prompt failed", "chat_id", chatID, keyError, retryErr)
		stop, reason := promptFailureAccount(ctx, retryErr, promptParamsInlineImage(params))
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, retryTurn, stop, reason)
		if stop != marotte.StopReasonCancelled {
			// Suppressed on a cancel for reportPromptFailure's reason, and the reader's
			// own Stop reaches this branch: the retry holds the prompt slot and registers
			// its own cancel, so the grace expiry trips it here like anywhere else. A
			// cancel also supplies no prose, so the frame would read "Retry prompt
			// failed: " with nothing after it.
			//
			// Turn-scoped: the retry ran as a turn of its own and the abandon above
			// stamped this reason on its turn_close, so that card carries the cause.
			roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
				Code:       marotte.ErrCodeRecoveryFailed,
				Message:    "Retry prompt failed: " + reason,
				TurnScoped: true,
			}))
		}
		return
	}
	roles.turnOutcome.SettleTurnOnResponse(ctx, chatID, retryTurn, reply.seq, reply.resp)
}

// supervisedDefaultSetting reads the settings-wide Supervised default
// applied to newly auto-created chats. Fails closed to false.
func supervisedDefaultSetting(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeySupervisedDefault, &b) {
		return false
	}
	return b
}

// promptEntry is the turn_open's prompt for a prompt or an empty retry: the
// client's id, the text and the attachments, which for an image or document are
// the only record of what was attached, since the path never reaches the text.
func promptEntry(p *marotte.PromptCommand, resends []string) *marotte.EntryPrompt {
	return &marotte.EntryPrompt{ID: p.MessageID, Text: p.Text, Attachments: p.Attachments, Resends: resends}
}

// openPromptTurn appends the prompt's turn_open at admission and creates its
// registry record. The header fallback names, models and supervises a chat no
// record exists for, the shape membership's create writes; a record that exists
// is left alone here and named below.
func openPromptTurn(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, resends []string) (string, error) {
	supervisedDefault := supervisedDefaultSetting(ctx, roles.workspace.ConfigDir)
	init := func(c *marotte.Chat) {
		c.Name = marotte.DefaultChatName
		c.Model = p.Model
		c.SupervisedMode = supervisedDefault
	}
	return roles.turnOutcome.OpenTurn(ctx, chatID, marotte.TurnSourcePrompt, promptEntry(p, resends), init)
}

// settleComposerOnPrompt is the header write a sent prompt owes: the draft that
// held the text is spent, and a still-default name takes the prompt's first 80
// runes. Cleared here too, so a lost set_draft POST cannot put the sent message
// back in the box on reload; the draft_changed broadcast carries the clear to
// every other device.
func settleComposerOnPrompt(ctx context.Context, chats ChatStore, bus Broadcaster, chatID marotte.ChatID, p *marotte.PromptCommand) {
	var (
		hadComposer bool
		cleared     marotte.ComposerState
	)
	version, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := false
		hadComposer = c.Draft != "" || len(c.Attachments) > 0
		if hadComposer {
			c.Draft = ""
			c.Attachments = nil
			cleared = c.Composer()
			changed = true
		}
		if c.Name == marotte.DefaultChatName {
			name := TruncateRunes(p.Text, 80)
			if name != p.Text {
				name += ellipsis
			}
			c.Name = name
			changed = true
		}
		return changed
	})
	if err != nil {
		slog.Warn("prompt: composer settle", "chat_id", chatID, keyError, err)
		return
	}
	if hadComposer {
		cleared.Version = version
		broadcastComposer(ctx, bus, chatID, &cleared)
	}
}

// AdmissionWait is the design's ADMISSION_WAIT_MS: the longest a prompt
// waits for a contended admission slot before answering 409. Must stay
// below the client's API timeout — TestAdmissionWait_StaysUnderTheClientAPITimeout
// pins the margin. A var only so tests can shrink a deliberately contended
// wait; production never writes it.
var AdmissionWait = 20 * time.Second

// reasonStarting is the 409 refusal class whose holder cannot receive a
// steer: a cold spawn or a shell. The client renders the busy face and retries
// instead of converting the 409 to a steer.
const reasonStarting = "starting"

// reasonChatGone is the 409 refusal class for a prompt into a chat whose
// record was deleted: nothing will ever accept it, so the client renders a
// terminal outcome rather than the busy face.
const reasonChatGone = "chat_not_found"

// promptAck is the prompt's early acknowledgement: admission is decided
// synchronously and the turn runs on its own goroutine, so the POST
// answers as soon as the user message is persisted and the admission slot
// is held.
type promptAck struct {
	MessageID string `json:"message_id"`
	Accepted  bool   `json:"accepted"`
}

// CmdPrompt handles the prompt command: validate → persist → admit → ack.
// The turn itself runs on its own goroutine (runPromptTurn), so the POST
// answers in the time of a disk append rather than a turn.
func CmdPrompt(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}

	if strings.HasPrefix(p.Text, "!") {
		return HandleShellInterception(ctx, roles, cmd, &p)
	}

	// Admission FIRST, then the turn_open: a refused admission writes nothing,
	// so the log never holds a turn no process owns. Admission is also the
	// dedupe for a client's re-send of a running prompt, which meets Busy and
	// becomes a steer; a re-send of a finished prompt opens a second turn.
	if err := reservePromptAdmission(ctx, roles, cmd.ChatID); err != nil {
		return nil, err
	}
	if err := launchPrompt(ctx, roles, cmd.ChatID, &p, nil, true); err != nil {
		return nil, err
	}
	return promptAck{Accepted: true, MessageID: p.MessageID}, nil
}

// launchPrompt opens an admitted prompt's turn and starts it, owning the
// reservation its caller took. A turn-end resend passes the steers it carries and
// no composer: its text is the server's, not the composer's.
func launchPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, resends []string, composer bool) error {
	turnID, err := openPromptTurn(ctx, roles, chatID, p, resends)
	if err != nil {
		roles.admission.ReleaseTurnReservation(chatID)
		if errors.Is(err, chat.ErrTombstoned) {
			return StatusErrorReason(http.StatusConflict, reasonChatGone, ErrChatNotFound)
		}
		return StatusError(http.StatusInternalServerError, err)
	}
	if composer {
		settleComposerOnPrompt(ctx, roles.chats, roles.bus, chatID, p)
	}

	// Register the turn in-flight before the ack goes out, so a shutdown
	// arriving between the ack and the goroutine's first step still waits
	// for it. The turn runs under a context detached from the POST's own,
	// which returns at the ack; the goroutine owns the cancel.
	roles.lifecycle.InflightAdd(1)
	turnCtx, cancel := roles.lifecycle.TurnContext(ctx)
	go runPromptTurn(turnCtx, cancel, roles, chatID, turnID, p)
	return nil
}

// reservePromptAdmission takes the chat's admission slot for a prompt: a
// prompt-class holder with a live bridge answers the plain 409 (the
// client's 409→steer conversion works), and every other holder answers
// 409 with the additive reason "starting", on which the client never
// attempts an undeliverable steer.
func reservePromptAdmission(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) error {
	switch roles.admission.ReserveTurnForPrompt(ctx, chatID, AdmissionWait) {
	case AdmissionAcquired:
		return nil
	case AdmissionBusy:
		return StatusError(http.StatusConflict, errBusy)
	default:
		return StatusErrorReason(http.StatusConflict, reasonStarting, errBusy)
	}
}

// runPromptTurn drives one admitted prompt end to end, owning the reservation
// CmdPrompt took, the turn CmdPrompt opened, the turn context's cancel, and the
// in-flight registration; every path out releases all four. Failures past the
// ack are SSE-only: the POST has already answered.
func runPromptTurn(ctx context.Context, cancel context.CancelFunc, roles *promptRoles, chatID marotte.ChatID, turnID string, p *marotte.PromptCommand) {
	defer roles.lifecycle.InflightDone()
	defer cancel()
	defer roles.turnOutcome.ReleaseTurn(chatID, turnID)
	sb, err := roles.bridges.OpenBridge(ctx, chatID, p.Model)
	if err != nil {
		roles.admission.ReleaseTurnReservation(chatID)
		reason := rpcerr.Text(err)
		closeBeforeStart(ctx, roles, chatID, turnID, marotte.StopReasonInterrupted, reason)
		roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{Code: marotte.ErrCodeBridgeStartFailed, Message: reason, TurnScoped: true}))
		return
	}
	// The reservation already excludes every prompt and shell, so a held
	// bridge slot here is a programming error.
	if !sb.TryAcquireForPrompt() {
		roles.admission.ReleaseTurnReservation(chatID)
		slog.Error("prompt: bridge slot held despite an owned admission reservation", "chat_id", chatID)
		const reason = "The prompt could not start. Send it again."
		closeBeforeStart(ctx, roles, chatID, turnID, marotte.StopReasonInterrupted, reason)
		roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{Code: marotte.ErrCodePromptFailed, Message: reason, TurnScoped: true}))
		return
	}
	promptAdmittedTurn(ctx, roles, sb, chatID, turnID, p)
}

// closeBeforeStart runs the turn end rule on a prompt's turn that never reached
// StartTurn. Its parked steers are the turn end's to resend, like any turn's.
func closeBeforeStart(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string) {
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason)
}

// promptAdmittedTurn runs the turn with both holds owned: MCP wait, StartTurn
// at bridge-ready, the parked steers' delivery, the ACP call, the settle, and
// the ordered handoff into the empty-turn recovery.
//
// The release ORDER is the contract: capture the finalized result through
// the still-held turn handle, then the bridge slot, then the reservation,
// and the deferred ReleaseTurn last so every turn-keyed predicate reads a
// live record.
func promptAdmittedTurn(ctx context.Context, roles *promptRoles, sb Bridge, chatID marotte.ChatID, turnID string, p *marotte.PromptCommand) {
	// The prompt Call gets its own cancellable context so CmdCancel's
	// grace budget has something to trip when KAS never acks a
	// session/cancel.
	ctx, cancelPrompt := context.WithCancelCause(ctx)
	defer cancelPrompt(nil)
	sb.BeginPromptCall(cancelPrompt)
	defer sb.EndPromptCall()

	if !roles.mcp.WaitForReady(ctx, 30*time.Second) {
		pending := roles.mcp.PendingSummary(ctx)
		slog.Warn("MCP readiness timeout, proceeding anyway",
			"chat_id", chatID,
			"silent", pending.Silent,
			"failed", pending.Failed,
			"awaiting_auth", pending.AwaitingAuth)
	}
	// Start the turn at bridge-ready, immediately before dispatch, so the model
	// and the credit baseline are captured with the bridge live — the spawn and
	// the MCP wait are excluded.
	if !roles.turnOutcome.StartTurn(ctx, chatID, turnID) {
		// Dead ctx, or the bridge died between OpenBridge and here and the death
		// closer took the turn: the turn end rule runs on the turn CmdPrompt
		// opened, and a turn already closed writes nothing.
		sb.ReleaseAfterPrompt()
		roles.admission.ReleaseTurnReservation(chatID)
		stop, reason := turnStopBeforeStart(ctx, "The turn was cancelled before the agent answered.")
		closeBeforeStart(ctx, roles, chatID, turnID, stop, reason)
		if stop != marotte.StopReasonCancelled {
			// Suppressed on a cancel for reportPromptFailure's reason: prompt_failed
			// routes to a toast, and the reader asked for this stop.
			roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
				Code: marotte.ErrCodePromptFailed, Message: reason, TurnScoped: true,
			}))
		}
		return
	}
	deliverParkedSteers(ctx, roles, chatID)
	slog.Info("prompt", "chat_id", chatID, "len", len(p.Text))
	start := time.Now()
	promptParams, inlinedImage := BuildPromptParams(ctx, roles.workspace, sb, p, historyInlineImages(ctx, roles.chats, chatID))
	reply, err := callPromptWithRetry(ctx, sb, promptParams, chatID)
	elapsed := time.Since(start)
	if err != nil {
		reportPromptFailure(ctx, roles, chatID, turnID, err, elapsed, inlinedImage)
		sb.ReleaseAfterPrompt()
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	slog.Info("prompt complete", "chat_id", chatID, "elapsed", elapsed)
	if roles.auth != nil {
		roles.auth.Record(nil)
	}

	// Settle this turn before deciding whether it produced nothing: the
	// close is what settles the withheld steer carry and measures the
	// turn, and recovery reads that measurement.
	roles.turnOutcome.SettleTurnOnResponse(ctx, chatID, turnID, reply.seq, reply.resp)
	// Capture the result while the turn handle is still held, then free
	// both admission holds so a waiting prompt is admitted before
	// recovery's own bookkeeping runs.
	result, aErr := roles.turnOutcome.AwaitTurn(ctx, chatID, turnID)
	sb.ReleaseAfterPrompt()
	roles.admission.ReleaseTurnReservation(chatID)
	if aErr != nil {
		slog.Warn("empty turn check: no turn outcome", "chat_id", chatID, keyError, aErr)
		return
	}
	recoverEmptyTurn(ctx, roles, chatID, turnID, result, p, promptParams)
}

// deliverParkedSteers runs between StartTurn and the turn's session/prompt, under
// the chat's steer lock: a delete that decided before StartTurn ends before this
// turn's execution can read, and one deciding after sees the start and refuses. A
// clear owed for KAS's load re-injection goes first, while nothing reads; one that
// is skipped or does not land leaves those rows unsent, never sent beside KAS's
// copies.
func deliverParkedSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
	if roles.jobs.NeedsPostLoadClear(chatID) {
		cleared, landed := clearSteerBuffer(ctx, roles, chatID)
		roles.jobs.PostLoadCleared(chatID, cleared, landed)
	}
	seen := map[string]bool{}
	for {
		key, text, ok := roles.jobs.NextParked(chatID)
		if !ok || seen[key] {
			return
		}
		seen[key] = true
		if refuse, _ := steerOne(ctx, roles, chatID, key, text, true); refuse != "" {
			slog.Warn("steer: a parked steer could not be delivered", "chat_id", chatID, "steer_id", key, "reason", refuse)
			return
		}
	}
}

// historyInlineImages counts the images KAS still holds inline for this chat: the
// image attachments on every prompt after the compaction watermark, which bounds
// how many more this prompt may inline before falling back to a path reference.
// An unreadable history answers the cap, so a doubt costs a path reference rather
// than a refused prompt.
func historyInlineImages(ctx context.Context, chats ChatStore, chatID marotte.ChatID) int {
	c, ok := chats.Get(ctx, chatID)
	if !ok {
		return MaxHistoryInlineImages
	}
	paths, err := chats.PromptAttachmentPaths(ctx, chatID, c.CompactionWatermark)
	if err != nil {
		return MaxHistoryInlineImages
	}
	count := 0
	for _, p := range paths {
		if isImagePath(p) {
			count++
		}
	}
	return count
}

// reportPromptFailure finalizes a turn whose prompt Call failed and
// broadcasts the failure. The POST answered at the ack, so the error frame
// is the only live surface.
//
// One rendering of the cause on every surface that carries it: handing the
// raw error to the broadcast would let RPCErrorText's machine-triplet
// fallback overwrite the prose promptFailureReason produces.
func reportPromptFailure(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, err error, elapsed time.Duration, inlinedImage bool) {
	stop, reason := promptFailureAccount(ctx, err, inlinedImage)
	if stop == marotte.StopReasonCancelled {
		// Info rather than Error at THIS site: nothing here is actionable. The Warn
		// callPromptWithRetry logs one frame earlier is pre-existing and still fires, so
		// the cancelled path is not uniformly quiet. And NO error frame at all,
		// because prompt_failed routes to a toast and a red toast for a stop the reader
		// asked for is the same wrong signal as a red card.
		slog.Info("prompt cancelled: the cancel was never acked, so the grace budget unblocked the turn",
			"chat_id", chatID, "elapsed", elapsed)
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason)
		return
	}
	slog.Error("prompt failed", "chat_id", chatID, keyError, err, "elapsed", elapsed)
	// An auth failure is the one prompt failure whose remedy is not "send
	// again", so it routes through a different code.
	code := marotte.ErrCodePromptFailed
	if classifyPromptFailure(err) == classAuth {
		code = marotte.ErrCodeAuthTokenUnavailable
		if roles.auth != nil {
			roles.auth.Record(err)
		}
	}
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason)
	// Turn-scoped: the abandon above stamps this same reason on the turn_close,
	// so the card says it durably and a toast for the chat on screen would be a
	// second copy of it.
	roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID,
		marotte.ErrorPayload{Code: code, Message: reason, TurnScoped: true}))
}

// BuildPromptParams constructs the full session/prompt parameter map and
// reports whether this prompt contains an inlined image.
func BuildPromptParams(ctx context.Context, ws Workspace, sb sessionScoped, p *marotte.PromptCommand, historyImages int) (map[string]any, bool) {
	blocks := BuildPromptBlocks(ctx, p.Text, p.Attachments, historyImages, ws.ResolveInside)
	params := SessionParams(sb, map[string]any{
		marotte.KeyPrompt: blocks,
	})
	// Forward the client-generated user message id so KAS stores this
	// turn under marotte's own id — what makes rewind addressable:
	// revertMultiple requires a messageId naming a user message, one KAS
	// only knows because it was sent here.
	if p.MessageID != "" {
		params["messageId"] = p.MessageID
	}
	return params, inlineImageBlockCount(blocks) > 0
}

func promptParamsInlineImage(params map[string]any) bool {
	blocks, _ := params[marotte.KeyPrompt].([]map[string]any)
	return inlineImageBlockCount(blocks) > 0
}

// promptFailureClass names why a prompt failed: the causes want different
// actions, which one boolean could never express.
type promptFailureClass int

const (
	// classFatal is the default: surface it, do not retry.
	classFatal promptFailureClass = iota
	// classPipeDeath is a dead subprocess. Retrying the same bridge is
	// provably useless (readLoop closed done permanently).
	classPipeDeath
	// classBusy is the session still finishing a prior turn: a real wait.
	classBusy
	// classTransient is a write failure or internal error a second
	// attempt can plausibly fix.
	classTransient
	// classThrottled is a backend rate limit. Not retryable here: KAS's
	// own client already exhausted its adaptive attempts.
	classThrottled
	// classRejected is a backend validation refusal (validationErrorNames).
	// Non-retryable by construction: the payload is what was refused.
	classRejected
	// classAuth is the backend refusing the token rather than the
	// request. CmdPrompt sends marotte.ErrCodeAuthTokenUnavailable for
	// it, the only code the client routes to a Sign in CTA.
	classAuth
)

// validationErrorNames are the backend's own names for a request refused
// as malformed or oversized, read off the kiro-cli-chat 2.19.0 binary's
// string table. Every member is a statement about the BYTES sent, so a
// retry with the same bytes gets the same answer; without this table they
// land on -32603 and are retried twice more. Quota, capacity, model and
// Kms* names are excluded: none describes the payload, so classifying one
// here would tell a user to shrink a prompt when their allowance ran out.
var validationErrorNames = []string{
	"ImageSizeExceeded",
	"ImageDimensionExceeded",
	"ImageCountExceeded",
	"ImageFormatUnsupported",
	"ImageMimeMismatch",
	"PromptTooLong",
	"ContentLengthExceedsThreshold",
	"DisallowedFileType",
	"DocumentSizeExceeded",
	"DocumentMaximumPagesExceeded",
	"DocumentCountExceeded",
}

// authErrorNames are the backend's own names for a request refused because
// the credential was not usable, measured off the KAS 2.20.0 bundle.
//
// ModelRegistryAccessDeniedError is deliberately absent: upstream tells
// that user the account lacks model access, which no sign-in fixes.
var authErrorNames = []string{
	"TokenInvalidError",
	"TokenExpiredError",
	"AuthRefreshFailedError",
	"ModelRegistryUnauthenticatedError",
	"AccessDeniedError",
	"MISSING_TOKEN",
	"MALFORMED_TOKEN",
	"INVALID_AUTH",
	"INVALID_SSO_AUTH",
	"INVALID_IDC_AUTH",
}

// mappedErrorData is the shape KAS puts in `error.data` on a mapped
// backend error. Every KiroQError lands on -32000 with these fields, so
// the class is what distinguishes them, not the presence of the data.
type mappedErrorData struct {
	ErrorType      string `json:"errorType"`
	RetryErrorType string `json:"retryErrorType"`
	RequestID      string `json:"requestId"`
}

// retryErrorType values KAS assigns. Only THROTTLING is a rate limit —
// the classifier keys on this exact value rather than the mere presence
// of the data block, since most mapped classes are CLIENT_ERROR.
const (
	kasRetryThrottling         = "THROTTLING"
	contextWindowExceededError = "ContextWindowExceededError"
)

// The per-field budgets promptFailureReason bounds a MAPPED error's upstream
// text with. Two rather than one because that function composes marotte's own
// remedy sentence and the request id AFTER the prose: one bound over the whole
// result would spend it on the prose and cut the actionable half off.
//
// The prose cap is half rpcerr's own maxTextBytes, which is already far more
// than any real cause needs; the id is an opaque token, so anything longer is
// not one and truncating it loses nothing a reader could have used.
const (
	mappedProseCap     = 1024
	mappedRequestIDCap = 128
)

// classifyPromptFailure maps a prompt error onto its class.
func classifyPromptFailure(err error) promptFailureClass {
	if err == nil {
		return classFatal
	}
	// A dead bridge arrives wrapped in a TransportError whose Retryable
	// is true, so the identity check has to win first.
	if errors.Is(err, marotte.ErrBridgeExited) {
		return classPipeDeath
	}
	if errors.Is(err, marotte.ErrNotIdle) {
		return classBusy
	}
	if te, ok := errors.AsType[*marotte.TransportError](err); ok {
		if te.Retryable {
			return classTransient
		}
		return classFatal
	}
	if re, ok := errors.AsType[*marotte.RPCError](err); ok {
		return classifyRPCFailure(re)
	}
	return classFatal
}

// classifyRPCFailure classifies an RPC error by its code.
func classifyRPCFailure(re *marotte.RPCError) promptFailureClass {
	switch re.Code {
	case marotte.RPCCodeNotIdle:
		return classBusy
	case marotte.RPCCodeBridgeExited:
		// KAS's mapped-backend-error code, which happens to share a
		// number with marotte's own bridge-exited constant. A bridge
		// exit never arrives here as an RPCError, so this is KAS's.
		d := mappedFromData(re)
		if d == nil {
			return classFatal
		}
		if d.RetryErrorType == kasRetryThrottling {
			return classThrottled
		}
		if slices.Contains(authErrorNames, d.ErrorType) {
			return classAuth
		}
		return classFatal
	case marotte.RPCCodeInternal:
		// -32603 is KAS's catch-all: a genuine internal fault, plus
		// every validation and auth failure. Both are excluded from
		// retry — auth is pure latency, validation is a second upload
		// of the same rejected payload.
		if d := mappedFromData(re); d != nil && d.ErrorType == contextWindowExceededError {
			return classFatal
		}
		if isAuthShaped(re) {
			return classAuth
		}
		if isValidationShaped(re) {
			return classRejected
		}
		return classTransient
	}
	return classFatal
}

// mappedFromData decodes a mapped-error payload. Returns nil for an error
// that is not one of KAS's mapped backend classes.
func mappedFromData(re *marotte.RPCError) *mappedErrorData {
	raw := re.ErrorData()
	if len(raw) == 0 {
		return nil
	}
	var d mappedErrorData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if d.RetryErrorType == "" && d.ErrorType == "" {
		return nil
	}
	return &d
}

// isAuthShaped reports whether an internal error is really an
// authentication failure. Matched on the payload since KAS collapses both
// onto -32603.
//
// `credentials` is deliberately not among the markers: it matched an AWS
// SDK message stating a refresh will be attempted, so grading it
// terminal-auth suppressed the retry the message was asking for.
func isAuthShaped(re *marotte.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	for _, name := range authErrorNames {
		if strings.Contains(hay, name) {
			return true
		}
	}
	markers := []string{
		"Authentication failed",
		"Authentication token",
		"not logged in",
		"Unauthorized",
		"unauthorized",
		"ExpiredToken",
		"AccessDenied",
	}
	for _, marker := range markers {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

// isValidationShaped reports whether an internal error is really the
// backend refusing the request as sent. Matching a name rather than
// surrounding prose is what survives a reworded message.
func isValidationShaped(re *marotte.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	for _, name := range validationErrorNames {
		if strings.Contains(hay, name) {
			return true
		}
	}
	return false
}

func isImageValidationShaped(re *marotte.RPCError) bool {
	hay := re.Message + string(re.ErrorData())
	for _, name := range validationErrorNames {
		if strings.HasPrefix(name, "Image") && strings.Contains(hay, name) {
			return true
		}
	}
	return false
}

// promptFailureAccount decides what a failed prompt CONCLUDES and what it says,
// as one pair: the stop grades the turn and the prose explains it. Three
// cancellers reach the prompt context and only the cancel-grace timer stamps
// ErrCancelGraceExpired on the cause (InterruptTurn and shutdown do not; the HTTP
// request is detached by lifetime.TurnContext), so an absent sentinel is a fault
// and keeps grading `interrupted`.
func promptFailureAccount(ctx context.Context, err error, inlinedImage bool) (stop marotte.StopReason, reason string) {
	if errors.Is(err, context.Canceled) {
		if errors.Is(context.Cause(ctx), ErrCancelGraceExpired) {
			// No prose: DefaultFailureReason(TurnOutcomeCancelled) is empty because the
			// footer's own word already reads "Cancelled" a row away.
			return marotte.StopReasonCancelled, ""
		}
		return marotte.StopReasonInterrupted, "The turn was cancelled before the agent answered."
	}
	return marotte.StopReasonInterrupted, promptFailureReason(err, inlinedImage)
}

// promptFailureReason renders a failure into something worth showing the
// user. KAS's own message on a mapped error is already user-facing, so
// this adds to it rather than replacing it.
func promptFailureReason(err error, inlinedImage bool) string {
	re, ok := errors.AsType[*marotte.RPCError](err)
	if !ok {
		return rpcerr.Text(err)
	}
	d := mappedFromData(re)
	if d == nil {
		// Not one of KAS's mapped backend classes — 127 of 137 measured
		// engine errors are a -32603 whose message is the literal
		// "Internal error" and whose cause is in error.data.
		return rpcerr.Text(err) + validationGuidance(re, inlinedImage)
	}
	if re.Code == marotte.RPCCodeInternal && d.ErrorType == contextWindowExceededError {
		return "This chat exceeds the model's context limit. Type `/compact` or start a new chat, then send the prompt again."
	}
	if re.Code == marotte.RPCCodeBridgeExited && d.ErrorType == "ModelRegistryUnavailableError" {
		return rpcerr.Sanitize(re.Message, mappedProseCap) + " Run `kiro-cli login`, then send the prompt again."
	}
	// Sanitize rather than Text: a mapped error's `data` is the machine triplet,
	// which Text would compose in through its raw-JSON fallback, while the prose
	// is KAS's own userFacingSessionErrorMessage in `message`. Every field below
	// interpolates a user-authored agent id and model and lands on the PERSISTED
	// turn reason and the SSE error frame, so it is bounded and sanitised here.
	msg := strings.TrimSpace(rpcerr.Sanitize(re.Message, mappedProseCap))
	if msg == "" {
		msg = strings.TrimSpace(rpcerr.Sanitize(d.ErrorType, mappedProseCap))
		if msg == "" {
			msg = "the model backend refused the request"
		}
	}
	if d.RetryErrorType == kasRetryThrottling {
		msg += " kiro-cli already retried. Wait a moment before resending, because that is the only thing that helps."
	}
	// Bounded on its own budget rather than by capping the composition, or a
	// long upstream message would cut off the remedy sentence above and this id
	// — the two halves a reader can act on.
	if id := strings.TrimSpace(rpcerr.Sanitize(d.RequestID, mappedRequestIDCap)); id != "" {
		msg += " (request " + id + ")"
	}
	return msg
}

// validationGuidance is the recovery text for a validation-shaped refusal,
// split by whether an image is what tripped it and whether this prompt
// inlined one. Empty for everything else.
func validationGuidance(re *marotte.RPCError, inlinedImage bool) string {
	if !isValidationShaped(re) {
		return ""
	}
	text := " The request was refused as sent. Resending it unchanged will fail the same way."
	switch {
	case !isImageValidationShaped(re):
		text += " Make the prompt or its attachments smaller, then send again."
	case inlinedImage:
		text += " Make the prompt or its attachments smaller, then send again. If the refusal continues, use Rewind to remove an earlier prompt image, or reopen the chat to clear an image returned by a file or MCP tool."
	default:
		text += " Use Rewind to remove an earlier prompt image. Reopen the chat if an image returned by a file or MCP tool caused the refusal. Those images last only for the live session."
	}
	return text
}

// String names the class for logs. A number in a log line is a lookup the
// reader should not have to perform.
func (c promptFailureClass) String() string {
	switch c {
	case classPipeDeath:
		return "pipe_death"
	case classBusy:
		return "busy"
	case classTransient:
		return "transient"
	case classThrottled:
		return "throttled"
	case classRejected:
		return "rejected"
	case classAuth:
		return "auth"
	case classFatal:
		return "fatal"
	}
	return "unknown"
}
