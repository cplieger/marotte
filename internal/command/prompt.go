package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/runesafe/v2"
)

func validatePromptPayload(cmd *marotte.ClientCommand) (marotte.PromptCommand, int, error) {
	var p marotte.PromptCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	// The bounds run before the empty check, so a malformed list cannot
	// stand in for prompt content and open an attachment-only turn.
	if len(p.Attachments) > marotte.MaxAttachments {
		return p, http.StatusRequestEntityTooLarge, errTooManyAttachments
	}
	for _, a := range p.Attachments {
		if a.Path == "" || len(a.Path) > marotte.MaxAttachmentPathBytes {
			return p, http.StatusBadRequest, errBadAttachmentPath
		}
	}
	if p.Text == "" && len(p.Attachments) == 0 {
		return p, http.StatusBadRequest, errEmptyPrompt
	}
	if len(p.Text) > MaxPromptBytes {
		return p, http.StatusRequestEntityTooLarge, errPromptTooLong
	}
	if p.MessageID == "" {
		return p, http.StatusBadRequest, errMissingMessageID
	}
	if !validMessageID(p.MessageID) {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	if !validIdent(p.Model) {
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	p.DisplayText = displayLabel(p.DisplayText)
	return p, 0, nil
}

const maxDisplayLabelBytes = 256

// displayLabel prepares a prompt's display label: one line, control runes removed, bounded.
func displayLabel(s string) string {
	return strings.TrimSpace(runesafe.SanitizeSingleLineBounded(s, maxDisplayLabelBytes))
}

// promptLabel is what names a chat from its first prompt: the display label, the text, or the
// first attachment's file name for an attachment-only prompt.
func promptLabel(p *marotte.PromptCommand) string {
	if p.DisplayText != "" {
		return p.DisplayText
	}
	if strings.TrimSpace(p.Text) != "" || len(p.Attachments) == 0 {
		return p.Text
	}
	return filepath.Base(p.Attachments[0].Path)
}

const promptRetryDelay = 2 * time.Second

// promptReply is a session/prompt response and the read loop position it arrived at, so the local
// settle can order itself against notifications still queued behind it.
type promptReply struct {
	resp *marotte.RPCResponse
	seq  uint64
}

// The delay is fixed, no backoff.
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

// recoverEmptyTurn re-prompts a turn that produced nothing: recreate the session, then send the
// same prompt once more as a turn of its own. result is the FINALIZED turn's captured outcome,
// never the live accumulator. Both admission holds are released, so the retry re-reserves with a
// try and a user prompt that won the slot abandons it.
func recoverEmptyTurn(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, result marotte.TurnResult, p *marotte.PromptCommand, params map[string]any) {
	// A verb KAS answers itself produces no content by design, so an
	// empty turn is the correct outcome and recovery is pure damage.
	if kasClaimsPromptText(p.Text) {
		return
	}
	// A locally-closed turn's outcome is only the prompt response's, so re-prompting on it is a
	// guess.
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
	// The reader stopped the turn this retry would replace. Read before the
	// refresh, so a stop already recorded costs no session teardown or respawn.
	if roles.turnOutcome.StopRequestedAfter(chatID, turnID) {
		slog.Info("empty turn: a stop was requested, not retrying", "chat_id", chatID, "turn", turnID)
		return
	}
	slog.Warn("empty turn detected, recreating session", "chat_id", chatID)
	refreshRetrySession(ctx, roles.bridges, roles.chats, chatID)
	retryEmptyTurnPrompt(ctx, roles, chatID, turnID, p, params)
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

// refreshRetrySession abandons the session that answered nothing: close its bridge and detach the
// chat from it. Nothing is written: the empty turn already closed `outcome: empty`.
func refreshRetrySession(ctx context.Context, bridges bridgeAccess, chats chatStore, chatID marotte.ChatID) {
	bridges.CloseBridge(ctx, chatID, marotte.TurnOutcomeInterrupted)
	if _, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		// Detach, don't forget: the abandoned session holds the chat's earlier transcript and must
		// stay in the chain or the reaper sweeps it.
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
	}
}

// retryEmptyTurnPrompt respawns the bridge and re-sends the prompt as a turn of
// its own: turn_open{source: empty_retry} with the same prompt. The caller holds
// the retry's admission reservation; emptyTurnID is the turn the retry replaces.
func retryEmptyTurnPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, emptyTurnID string, p *marotte.PromptCommand, params map[string]any) {
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
	// Take the new bridge's slot rather than assert it: OpenBridge leaves it idle, so a concurrent
	// taker can win it first. Losing abandons the retry.
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
	// After BeginPromptCall: cmdCancel records its stop before it arms, so a stop this read misses
	// finds the prompt registered and owns it through the grace budget.
	if roles.turnOutcome.StopRequestedAfter(chatID, emptyTurnID) {
		slog.Info("empty turn: a stop arrived during the respawn, not retrying", "chat_id", chatID, "turn", emptyTurnID)
		return
	}

	params[marotte.KeySessionID] = sb2.SessionID()
	// The retry is a turn of its own, closed on every path out of here, since
	// the turn it replaces is already closed.
	retryTurn, err := roles.turnOutcome.OpenTurn(ctx, chatID, TurnOpen{Source: marotte.TurnSourceEmptyRetry, Prompt: promptEntry(p, nil)})
	if err != nil {
		slog.Warn("empty turn: the retry turn could not open", "chat_id", chatID, keyError, err)
		return
	}
	defer roles.turnOutcome.ReleaseTurn(chatID, retryTurn)
	if !roles.turnOutcome.StartTurn(ctx, chatID, retryTurn) {
		slog.Warn("empty turn: the retry turn could not start", "chat_id", chatID)
		stop, reason := turnStopBeforeStart(ctx, "The retry was cancelled before the agent answered.")
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, retryTurn, stop, reason, "", 0)
		return
	}
	deliverParkedSteers(ctx, roles, chatID, retryTurn)
	reply, retryErr := callPromptWithRetry(ctx, sb2, params, chatID)
	if retryErr != nil {
		slog.Error("retry prompt failed", "chat_id", chatID, keyError, retryErr)
		stop, reason, kind := promptFailureAccount(ctx, retryErr, promptParamsInlineImage(params))
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, retryTurn, stop, reason, kind, reply.seq)
		if stop != marotte.StopReasonCancelled {
			// Suppressed on a cancel, the reader's own Stop included (the retry registers its own
			// cancel). The abandon above already stamped the reason on the retry's turn_close.
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

// Fails closed to false.
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
	return &marotte.EntryPrompt{ID: p.MessageID, Text: p.Text, Label: p.DisplayText, Attachments: p.Attachments, Resends: resends}
}

// The header fallback names, models and supervises a chat with no record; an existing record is
// left alone.
func openPromptTurn(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, l launch) (string, error) {
	supervisedDefault := supervisedDefaultSetting(ctx, roles.workspace.ConfigDir)
	init := func(c *marotte.Chat) {
		c.Name = marotte.DefaultChatName
		c.Model = p.Model
		c.SupervisedMode = supervisedDefault
	}
	return roles.turnOutcome.OpenTurn(ctx, chatID, TurnOpen{
		Source: marotte.TurnSourcePrompt, Prompt: promptEntry(p, l.resends), Init: init, Fence: l.fence, Awaited: l.awaited,
	})
}

// settleComposerOnPrompt is the header write a sent prompt owes: the draft is cleared (so a lost
// set_draft POST cannot restore the sent text on reload) and a still-default name takes the
// prompt's first 80 runes.
func settleComposerOnPrompt(ctx context.Context, chats chatStore, bus broadcaster, chatID marotte.ChatID, p *marotte.PromptCommand) {
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
		if label := promptLabel(p); c.Name == marotte.DefaultChatName && !c.NameSetByUser && label != "" {
			name := truncateRunes(label, 80)
			if name != label {
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

// AdmissionWait is how long a prompt waits for a contended admission slot before answering 409; it
// must stay below the client's API timeout (TestAdmissionWait_StaysUnderTheClientAPITimeout). A var
// only so tests can shrink it.
var AdmissionWait = 20 * time.Second

// reasonStarting is the 409 refusal class whose holder cannot receive a
// steer: a cold spawn or a shell. The client renders the busy face and retries
// instead of converting the 409 to a steer.
const reasonStarting = "starting"

// reasonChatGone is the 409 refusal class for a prompt into a chat whose
// record was deleted: nothing will ever accept it, so the client renders a
// terminal outcome rather than the busy face.
const reasonChatGone = "chat_not_found"

// promptAck is the prompt's early acknowledgement, sent once the user message is persisted and the
// admission slot is held.
type promptAck struct {
	MessageID string `json:"message_id"`
	Accepted  bool   `json:"accepted"`
}

// cmdPrompt handles the prompt command: validate → persist → admit → ack.
// The turn itself runs on its own goroutine (runPromptTurn), so the POST
// answers in the time of a disk append rather than a turn.
func cmdPrompt(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}
	if strings.HasPrefix(p.Text, "!") {
		return handleShellInterception(ctx, roles, cmd, &p)
	}

	// Admission FIRST: a refused admission writes nothing, so the log never holds a turn no process
	// owns. It also dedupes a re-send of a running prompt, which meets Busy and becomes a steer.
	if err := reservePromptAdmission(ctx, roles, cmd.ChatID); err != nil {
		return nil, err
	}
	if err := launchPrompt(ctx, roles, cmd.ChatID, &p, launch{composer: true}); err != nil {
		return nil, err
	}
	return promptAck{Accepted: true, MessageID: p.MessageID}, nil
}

// launch is how launchPrompt opens a turn. resends names the steer keys the
// prompt carries, dequeue the queued row it takes off the header once the turn is
// announced, fence the closed turn the open must find unsuperseded, and composer
// whether the prompt's words were the draft. awaited sets TurnOpen.Awaited.
type launch struct {
	dequeue  string
	resends  []string
	fence    TurnFence
	composer bool
	awaited  bool
}

// launchPrompt opens an admitted prompt's turn and starts it, owning the
// reservation its caller took. A refused open releases it and returns the error;
// ErrTurnSuperseded is returned as is, for the drain to stand down on.
func launchPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, l launch) error {
	_, err := startPrompt(ctx, roles, chatID, p, l)
	return err
}

func startPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, l launch) (string, error) {
	turnID, err := openPromptTurn(ctx, roles, chatID, p, l)
	if err != nil {
		roles.admission.ReleaseTurnReservation(chatID)
		switch {
		case errors.Is(err, ErrTurnSuperseded):
			return "", err
		case errors.Is(err, chat.ErrTombstoned):
			return "", StatusErrorReason(http.StatusConflict, reasonChatGone, ErrChatNotFound)
		}
		return "", StatusError(http.StatusInternalServerError, err)
	}
	// After OpenTurn returned, so the turn_opened frame precedes the header write
	// that drops the row.
	if l.dequeue != "" && roles.followups != nil {
		if err := roles.followups.Dequeue(ctx, chatID, l.dequeue); err != nil {
			slog.Warn("prompt: the sent follow-up stays listed until the chat's next write",
				"chat_id", chatID, "message_id", l.dequeue, keyError, err)
		}
	}
	if l.composer {
		settleComposerOnPrompt(ctx, roles.chats, roles.bus, chatID, p)
	}

	// In-flight before the ack, so a shutdown between ack and the goroutine's first step still
	// waits. The turn context is detached from the POST's; the goroutine owns the cancel.
	roles.lifecycle.InflightAdd(1)
	turnCtx, cancel := roles.lifecycle.TurnContext(ctx)
	go runPromptTurn(turnCtx, cancel, roles, chatID, turnID, p)
	return turnID, nil
}

// reservePromptAdmission takes the chat's admission slot for a prompt: a prompt-class holder with a
// live bridge answers the plain 409 (which the client converts to a steer), any other holder 409
// with reason "starting".
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

// Every path out releases all four. Failures past the ack are SSE-only: the POST has already
// answered.
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
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, "", 0)
}

// promptAdmittedTurn runs the turn with both holds owned. The release ORDER is the contract:
// capture the finalized result through the still-held turn handle, then the bridge slot, then the
// reservation, and the deferred ReleaseTurn last so every turn-keyed predicate reads a live record.
func promptAdmittedTurn(ctx context.Context, roles *promptRoles, sb Bridge, chatID marotte.ChatID, turnID string, p *marotte.PromptCommand) {
	ctx, cancelPrompt := context.WithCancelCause(ctx)
	defer cancelPrompt(nil)
	sb.BeginPromptCall(cancelPrompt)
	defer sb.EndPromptCall()

	// Before StartTurn so a pre-send compaction is not timed as the turn. Not on
	// the empty-turn retry, which reaches StartTurn on its own path: this prompt
	// just ran the check.
	if roles.compactor != nil {
		roles.compactor.PreSendCompact(ctx, chatID)
	}
	// At bridge-ready, so the model and credit baseline are captured with the bridge live and the
	// spawn is not timed.
	if !roles.turnOutcome.StartTurn(ctx, chatID, turnID) {
		// Dead ctx, or the death closer took the turn: the turn end rule runs here, and a closed
		// turn writes nothing.
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
	deliverParkedSteers(ctx, roles, chatID, turnID)
	slog.Info("prompt", "chat_id", chatID, "len", len(p.Text))
	start := time.Now()
	promptParams, inlinedImage := buildPromptParams(ctx, roles.workspace, sb, p, historyInlineImages(ctx, roles.chats, chatID))
	reply, err := callPromptWithRetry(ctx, sb, promptParams, chatID)
	elapsed := time.Since(start)
	if err != nil {
		reportPromptFailure(ctx, roles, chatID, turnID, err, reply.seq, elapsed, inlinedImage)
		sb.ReleaseAfterPrompt()
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	slog.Info("prompt complete", "chat_id", chatID, "elapsed", elapsed)
	if roles.auth != nil {
		roles.auth.record(nil)
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

// deliverParkedSteers runs between StartTurn and session/prompt under the chat's steer lock, so a
// delete decided before StartTurn finishes first and one deciding after refuses. Pending turn ends
// resolve into this turn first; a clear owed for KAS's load re-injection goes next, and if skipped
// or lost those rows stay unsent, never sent beside KAS's copies.
func deliverParkedSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
	resolveEnds(ctx, roles, chatID, math.MaxUint64)
	if roles.jobs.NeedsPostLoadClear(chatID) {
		cleared, landed := clearSteerBuffer(ctx, roles.steerTarget(chatID))
		roles.jobs.PostLoadCleared(chatID, cleared, landed)
	}
	deliverUnread(ctx, roles, chatID, turnID)
}

// deliverUnread parks the chat's unread rows for turnID and sends them into it,
// the lead first, then in acceptance order. The record parks and routes a row only
// while it names turnID as starting or bound, so a turn that ends mid-loop stops
// the loop with the rest still unsent. The caller holds the steer lock.
func deliverUnread(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string) {
	seen := map[string]bool{}
	for {
		key, text, ok := roles.jobs.NextParked(chatID, turnID)
		if !ok || seen[key] {
			return
		}
		seen[key] = true
		if refuse, _ := steerOne(ctx, roles, chatID, key, text, turnID); refuse != "" {
			slog.Warn("steer: a parked steer could not be delivered", "chat_id", chatID, "steer_id", key, "reason", refuse)
			return
		}
	}
}

// historyInlineImages counts the images KAS still holds inline for this chat (image attachments
// after the compaction watermark), bounding how many more this prompt may inline. An unreadable
// history answers the cap.
func historyInlineImages(ctx context.Context, chats chatStore, chatID marotte.ChatID) int {
	c, ok := chats.Get(ctx, chatID)
	if !ok {
		return maxHistoryInlineImages
	}
	paths, err := chats.PromptAttachmentPaths(ctx, chatID, c.CompactionWatermark)
	if err != nil {
		return maxHistoryInlineImages
	}
	count := 0
	for _, p := range paths {
		if isImagePath(p) {
			count++
		}
	}
	return count
}

// reportPromptFailure finalizes a turn whose prompt Call failed and broadcasts the failure, the
// only live surface after the ack. It renders the cause once, so RPCErrorText's fallback cannot
// overwrite promptFailureReason's prose.
func reportPromptFailure(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, err error, seq uint64, elapsed time.Duration, inlinedImage bool) {
	stop, reason, kind := promptFailureAccount(ctx, err, inlinedImage)
	if stop == marotte.StopReasonCancelled {
		// Info and no error frame: nothing is actionable, and a red toast for a stop the reader
		// asked for is the wrong signal.
		slog.Info("prompt cancelled: the cancel was never acked, so the grace budget unblocked the turn",
			"chat_id", chatID, "elapsed", elapsed)
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, kind, seq)
		return
	}
	slog.Error("prompt failed", "chat_id", chatID, keyError, err, "elapsed", elapsed)
	// An auth failure is the one prompt failure whose remedy is not "send
	// again", so it routes through a different code.
	code := marotte.ErrCodePromptFailed
	if classifyPromptFailure(err) == classAuth {
		code = marotte.ErrCodeAuthTokenUnavailable
		if roles.auth != nil {
			roles.auth.record(err)
		}
	}
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, kind, seq)
	// The reason the turn_close carries, which is the engine's own sentence when its
	// turn_end closed the turn first, so the off-screen toast says what the card says.
	if result, aErr := roles.turnOutcome.AwaitTurn(ctx, chatID, turnID); aErr == nil && result.Reason != "" {
		reason = result.Reason
	}
	// Turn-scoped: the card says it durably, and a toast for the chat on screen
	// would be a second copy of it.
	roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID,
		marotte.ErrorPayload{Code: code, Message: reason, TurnScoped: true}))
}

// buildPromptParams constructs the full session/prompt parameter map and
// reports whether this prompt contains an inlined image.
func buildPromptParams(ctx context.Context, ws Workspace, sb SessionCaller, p *marotte.PromptCommand, historyImages int) (map[string]any, bool) {
	blocks := BuildPromptBlocks(ctx, p.Text, p.Attachments, historyImages, ws, sb)
	params := sessionParams(sb, map[string]any{
		marotte.KeyPrompt: blocks,
	})
	// The client's message id is what makes rewind addressable: revertMultiple requires a messageId
	// naming a user message, one KAS only knows because it was sent here.
	if p.MessageID != "" {
		params["messageId"] = p.MessageID
	}
	if meta := kascap.PromptMeta(promptFacts(ctx, ws.ConfigDir, p.DisplayText)); len(meta) > 0 {
		params["_meta"] = map[string]any{"kiro": meta}
	}
	return params, inlineImageBlockCount(blocks) > 0
}

// promptFacts withholds the output style when it is the default, unset or unknown.
func promptFacts(ctx context.Context, configDir, label string) *kascap.Prompt {
	var style string
	read := configDir != "" && settings.FieldInto(ctx, configDir, settings.KeyOutputStyle, &style)
	if !read || !settings.ValidOutputStyle(style) || style == settings.OutputStyleDefault {
		style = ""
	}
	return &kascap.Prompt{OutputStyle: style, Label: label}
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
	// Retrying the same bridge is provably useless (readLoop closed done permanently).
	classPipeDeath
	// classBusy is the session still finishing a prior turn: a real wait.
	classBusy
	// classTransient is a write that never reached kiro-cli, which a second
	// attempt can fix. An answered prompt is never transient: KAS may already
	// have run it, and its own retry layers have already spent their attempts.
	classTransient
	// Not retryable here: KAS's own client already exhausted its adaptive attempts.
	classThrottled
	// classRejected is a backend validation refusal (validationErrorNames).
	// Non-retryable by construction: the payload is what was refused.
	classRejected
	// classAuth is the backend refusing the token rather than the
	// request. cmdPrompt sends marotte.ErrCodeAuthTokenUnavailable for
	// it, the only code the client routes to a Sign in CTA.
	classAuth
)

// validationErrorNames are the backend's names for a request refused as malformed or oversized
// (kiro-cli-chat 2.19.0 string table). Each is about the BYTES sent, so a retry gets the same
// answer. Quota, capacity, model and Kms* names are excluded: they do not describe the payload.
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

// classifyRPCFailure classifies an RPC error. KAS's typed-error envelope is read
// before any code, because it is code-independent.
func classifyRPCFailure(re *marotte.RPCError) promptFailureClass {
	if m, ok := rpcerr.MappedOf(re); ok {
		return classifyMapped(m)
	}
	switch re.Code {
	case marotte.RPCCodeNotIdle:
		return classBusy
	case marotte.RPCCodeInternal:
		// KAS's catch-all, answered after the engine ran the prompt (it closes the
		// turn with turn_end{error} first), so a resend is a fresh execution.
		if isAuthShaped(re) {
			return classAuth
		}
		if isValidationShaped(re) {
			return classRejected
		}
	}
	return classFatal
}

// classifyMapped grades a typed KAS error. Only a throttle and a refused
// credential get a class of their own; KAS's retry layers already ran for the
// rest, knowing what the turn emitted, so none is resent.
func classifyMapped(m rpcerr.Mapped) promptFailureClass {
	if m.RetryErrorType == rpcerr.RetryThrottling {
		return classThrottled
	}
	if slices.Contains(authErrorNames, m.ErrorType) {
		return classAuth
	}
	return classFatal
}

// contextWindowExceeded reports a turn that overflowed the model's context
// window, which KAS sends as a typed error at any code.
func contextWindowExceeded(err error) bool {
	m, ok := rpcerr.MappedOf(err)
	return ok && m.ErrorType == rpcerr.ContextWindowExceededError
}

// isAuthShaped reports whether an internal error is really an authentication failure, matched on
// the payload since KAS collapses both onto -32603. `credentials` is not a marker: it matched an
// AWS SDK message promising a refresh.
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

// Only the cancel-grace timer stamps ErrCancelGraceExpired on the cause, so an absent sentinel is a
// fault and grades `interrupted`.
func promptFailureAccount(ctx context.Context, err error, inlinedImage bool) (stop marotte.StopReason, reason string, kind marotte.FailureKind) {
	if errors.Is(err, context.Canceled) {
		if errors.Is(context.Cause(ctx), ErrCancelGraceExpired) {
			// No prose: DefaultFailureReason(TurnOutcomeCancelled) is empty because the
			// footer's own word already reads "Cancelled" a row away.
			return marotte.StopReasonCancelled, "", ""
		}
		return marotte.StopReasonInterrupted, "The turn was cancelled before the agent answered.", ""
	}
	if contextWindowExceeded(err) {
		kind = marotte.FailureKindContextLimit
	}
	return marotte.StopReasonInterrupted, promptFailureReason(err, inlinedImage), kind
}

// promptFailureReason renders a failure into something worth showing the
// user: a typed KAS error through rpcerr.Account, anything else as its text.
func promptFailureReason(err error, inlinedImage bool) string {
	re, ok := errors.AsType[*marotte.RPCError](err)
	if !ok {
		return rpcerr.Text(err)
	}
	if m, mapped := rpcerr.MappedOf(re); mapped {
		return rpcerr.Account(m, re.Message)
	}
	return rpcerr.Text(err) + validationGuidance(re, inlinedImage)
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
		text += " Make the prompt or its attachments smaller, then send again. If the refusal continues, use Rewind to remove an earlier turn that carried an image, whether you attached it or a file or MCP tool returned it."
	default:
		// From KAS 2.26.0 a tool-returned image is persisted and replayed into
		// the model's context, so reopening the chat no longer clears it.
		text += " Use Rewind to remove the earlier turn that carried the image, whether you attached it or a file or MCP tool returned it. Reopening the chat does not clear it."
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
