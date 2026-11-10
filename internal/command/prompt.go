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

	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/settings"
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
	return strings.TrimSpace(textsafe.SanitizeSingleLineBounded(s, maxDisplayLabelBytes))
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

// sendTurnPrompt writes turn's session/prompt, retrying only the classes a second attempt can actually fix. It is
// the only writer of a turn's prompt: gate, when non-nil, holds every attempt, a busy retry's included, and each
// attempt's write runs the commit it answers.
func sendTurnPrompt(ctx context.Context, gate promptSendGate, sb promptWriter, params map[string]any, turn TurnRef) (promptReply, error) {
	return retry(ctx, 2, func(err error) bool {
		if closedCancelled(err) {
			return false
		}
		class := classifyPromptFailure(err)
		retry := class == classBusy || class == classTransient
		slog.Warn("prompt failure",
			"chat_id", turn.Chat, "class", class.String(), "retry", retry, keyError, err)
		return retry
	}, func() (promptReply, error) {
		var commit func() error
		if gate != nil {
			var err error
			if commit, err = gate.AwaitPromptSend(ctx, turn.Chat, turn.Turn); err != nil {
				return promptReply{}, err
			}
		}
		resp, seq, err := sb.PromptAt(ctx, params, commit)
		return promptReply{resp: resp, seq: seq}, err
	})
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

// retryEmptyTurnPrompt reloads the session and re-sends the prompt as a turn of
// its own: turn_open{source: empty_retry} with the same prompt, and runs that turn to
// its close. The caller holds the retry's admission reservation res; emptyTurnID is
// the turn the retry replaces. It answers the retry turn's id, or why none opened.
func retryEmptyTurnPrompt(ctx context.Context, roles *promptRoles, res Reservation, emptyTurnID string, p *marotte.PromptCommand, params map[string]any) (string, error) {
	chatID := res.Chat
	sb2, release, err2 := roles.bridges.OpenBridgeAt(ctx, chatID, p.Model, res)
	defer release()
	if closedCancelled(err2) {
		slog.Info("empty turn: the chat closed during the reload, not retrying", "chat_id", chatID)
		return "", err2
	}
	if err2 != nil {
		slog.Error("empty turn: reload failed",
			"chat_id", chatID, keyError, err2)
		// The retry's turn never opens, so no turn carries this failure and the
		// toast this code routes to is its only surface.
		roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
			Code:    marotte.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + rpcerr.Text(err2),
		}))
		return "", err2
	}
	params[marotte.KeySessionID] = sb2.SessionID()
	// The retry is a turn of its own, closed on every path out of here, since
	// the turn it replaces is already closed. The open refuses once a stop followed
	// that turn, in the section a stop's capture also takes.
	retryTurn, err := roles.turnOutcome.OpenTurn(ctx, chatID, TurnOpen{
		Source: marotte.TurnSourceEmptyRetry, Prompt: promptEntry(p, nil), Replaces: emptyTurnID, Admission: res.Token,
	})
	if errors.Is(err, ErrTurnStopped) || errors.Is(err, marotte.ErrChatClosedDuringCall) {
		slog.Info("empty turn: a stop or the chat's close arrived during the reload, not retrying",
			"chat_id", chatID, "turn", emptyTurnID)
		return "", err
	}
	if err != nil {
		slog.Warn("empty turn: the retry turn could not open", "chat_id", chatID, keyError, err)
		return "", err
	}
	defer roles.turnOutcome.ReleaseTurn(chatID, retryTurn)
	// The retry's call context, as promptAdmittedTurn's: a grace or interrupt aimed at the retry reaches only its call.
	callCtx, cancelRetry := context.WithCancelCause(ctx)
	defer cancelRetry(nil)
	turn := TurnRef{Chat: chatID, Turn: retryTurn}
	if !roles.turnOutcome.StartTurn(callCtx, turn, cancelRetry) {
		slog.Warn("empty turn: the retry turn could not start", "chat_id", chatID)
		stop, reason := turnStopBeforeStart(callCtx, "The retry was cancelled before the agent answered.")
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, retryTurn, stop, reason, "", 0)
		return retryTurn, nil
	}
	deliverParkedSteers(callCtx, roles, chatID, retryTurn)
	setPromptMeta(callCtx, params, roles.workspace.ConfigDir, p, retryTurn)
	reply, retryErr := sendTurnPrompt(callCtx, roles.sendGate, sb2, params, turn)
	roles.turnOutcome.EndPromptCall(turn)
	if retryErr != nil {
		stop, reason, kind := promptFailureAccount(callCtx, retryErr, promptParamsInlineImage(params))
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, retryTurn, stop, reason, kind, reply.seq)
		if stop != marotte.StopReasonCancelled {
			// Suppressed on a cancel, the reader's own Stop and the chat's close included. The abandon above
			// already stamped the reason on the retry's turn_close.
			logPromptFailure("retry prompt failed", chatID, retryErr)
			g := gradeFailedTurn(ctx, roles, chatID, retryTurn, retryErr, reason)
			code := marotte.ErrCodeRecoveryFailed
			if g.auth {
				code = marotte.ErrCodeAuthTokenUnavailable
			}
			roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
				Code:       code,
				Message:    "Retry prompt failed: " + g.reason,
				TurnScoped: g.onCard,
			}))
		}
		return retryTurn, nil
	}
	roles.turnOutcome.SettleTurnOnResponse(ctx, chatID, retryTurn, reply.seq, reply.resp)
	return retryTurn, nil
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
	return &marotte.EntryPrompt{
		ID: p.MessageID, Text: p.Text, Label: p.DisplayText, Attachments: p.Attachments, Resends: resends, Caller: p.Caller,
	}
}

// The header fallback names, models and supervises a chat with no record; an existing record is
// left alone.
func openPromptTurn(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, l *launch) (string, error) {
	supervisedDefault := supervisedDefaultSetting(ctx, roles.workspace.ConfigDir)
	init := func(c *marotte.Chat) {
		c.Name = marotte.DefaultChatName
		c.Model = p.Model
		c.SupervisedMode = supervisedDefault
	}
	return roles.turnOutcome.OpenTurn(ctx, chatID, TurnOpen{
		Source: marotte.TurnSourcePrompt, Prompt: promptEntry(p, l.resends), Init: init, Awaited: l.awaited,
		Admission: l.res.Token, Fence: l.fence,
	})
}

// settleComposerOnPrompt is the header write a sent prompt owes: the draft is cleared (so a lost
// set_draft POST cannot restore the sent text on reload) and a still-default name takes the
// prompt's first 80 runes. An API caller's words were never the composer's, so its prompt leaves
// the user's draft and staged files on every device.
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
		hadComposer = p.Caller == nil && (c.Draft != "" || len(c.Attachments) > 0)
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

// reasonStarting is the 409 refusal class for a send whose chat a held close kept from starting.
const reasonStarting = "starting"

// reasonChatGone is the 409 refusal class for a prompt into a chat whose
// record was deleted: nothing will ever accept it, so the client renders a
// terminal outcome rather than the busy face.
const reasonChatGone = "chat_not_found"

// reasonChatClosed is the 409 refusal class for a close of the chat that committed after the prompt began, so the
// close ended it. The client keeps the text as the chat's draft.
const reasonChatClosed = "chat_closed"

// reasonPromptOnly is the 409 refusal class for prompt-only text that would be steered or wait behind a running
// turn. The client keeps the text in the composer.
const reasonPromptOnly = "prompt_only"

// promptAck is the prompt's answer: where the message went (As) and the run order after it.
type promptAck struct {
	MessageID string          `json:"message_id"`
	As        SubmitAs        `json:"as"`
	Run       marotte.RunView `json:"run"`
	Accepted  bool            `json:"accepted"`
}

// cmdPrompt handles the prompt command: validate, then place it on the chat's run order, which steers it into the
// running turn, starts it, or queues it (VerbFor). A started turn runs on its own goroutine (runPromptTurn), so the
// POST answers at its turn open, or as queued past AdmissionWait.
func cmdPrompt(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}
	p.Caller = cmd.From
	// The prompt's intent begins here: a close that commits after this stamp ends it wherever it waits.
	stamp := roles.admission.LifeStamp()
	s := Submission{
		Row: marotte.QueuedPrompt{
			ID: p.MessageID, Text: p.Text, Label: p.DisplayText, Attachments: p.Attachments, Caller: p.Caller,
			Model: p.Model,
		},
		Origin: marotte.RunFromPrompt, Kind: sendKindOf(&p), PromptOnly: p.PromptOnly,
		Wait: true, Composer: true, Retry: !kasClaimsPromptText(p.Text),
	}
	for {
		ans, err := roles.runq.Submit(ctx, cmd.ChatID, stamp, &s)
		if err != nil {
			return nil, submitRefusal(err)
		}
		if ans.Steer {
			steered, err := steerPrompt(ctx, roles, cmd.ChatID, &p)
			if err != nil {
				return nil, err
			}
			if !steered {
				s.NoSteer = true
				continue
			}
			ans = SubmitAnswer{As: submitSteered, Run: roles.runq.RunView(ctx, cmd.ChatID)}
		}
		if ans.Settle {
			settleComposerOnPrompt(ctx, roles.chats, roles.bus, cmd.ChatID, &p)
		}
		return promptAck{Accepted: true, MessageID: p.MessageID, As: ans.As, Run: ans.Run}, nil
	}
}

// sendKindOf is what a send can be: display text or attachments are a prompt's alone, which a steer cannot carry.
func sendKindOf(p *marotte.PromptCommand) marotte.SendKind {
	if p.DisplayText != "" || len(p.Attachments) > 0 {
		return marotte.SendLabelled
	}
	return marotte.SendPlain
}

// steerPrompt routes a prompt the run order steers into the running turn, reporting false when that turn is gone
// (the record refused it no_turn) or the text reads as a KAS notification (filed under a notify- id no row
// carries), which the caller places again with steering ruled out.
func steerPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand) (bool, error) {
	if notificationPrefix.MatchString(p.Text) {
		return false, nil
	}
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return false, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	steerID := marotte.SteerIDFor(p.MessageID)
	switch refuse, sendErr := steerOne(ctx, roles, chatID, steerID, p.Text, "", p.Caller); {
	case refuse == SteerRefuseFull:
		return false, StatusErrorReason(http.StatusConflict, reasonFull, errSteerFull)
	case refuse != "":
		return false, nil
	case sendErr != nil:
		return false, StatusError(http.StatusBadGateway, sendErr)
	}
	slog.Info("prompt steered into the running turn", "chat", chatID, "steer_id", steerID)
	return true, nil
}

// submitRefusal answers a Submit error as HTTP; a status error passes through.
func submitRefusal(err error) error {
	if _, ok := errors.AsType[*statusError](err); ok {
		return err
	}
	switch {
	case errors.Is(err, ErrSubmitNoTurn):
		return StatusErrorReason(http.StatusConflict, reasonNoTurn, err)
	case errors.Is(err, ErrSubmitTooBig):
		return StatusError(http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, ErrSubmitPromptOnly):
		return StatusErrorReason(http.StatusConflict, reasonPromptOnly, err)
	case errors.Is(err, errSubmitFull):
		return StatusErrorReason(http.StatusConflict, reasonFull, err)
	case errors.Is(err, ErrSubmitStarting):
		return StatusErrorReason(http.StatusConflict, reasonStarting, err)
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		return StatusErrorReason(http.StatusConflict, reasonChatClosed, errChatClosed)
	case errors.Is(err, chat.ErrTombstoned), errors.Is(err, ErrChatNotFound):
		return StatusErrorReason(http.StatusConflict, reasonChatGone, ErrChatNotFound)
	}
	return StatusError(http.StatusInternalServerError, err)
}

// launch is how startPrompt opens a turn: resends names the steer keys the prompt carries, res is the take's
// reservation, which the prompt owns from here, awaited sets TurnOpen.Awaited, and claimComposer, for a page's
// POST, claims the composer settle it owes (nil for none).
type launch struct {
	claimComposer func() bool
	resends       []string
	res           Reservation
	// fence is the take's: a re-fence since (a shutdown, a teardown) refuses the open.
	fence   TurnFence
	awaited bool
}

// errLaunchUnreserved fails a prompt launched with no reservation, a wiring error.
var errLaunchUnreserved = errors.New("a prompt was launched without its admission reservation")

func startPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, p *marotte.PromptCommand, l *launch) (string, error) {
	if l.res.Token == 0 {
		slog.Error("a prompt launched without its reservation", "chat_id", chatID)
		return "", StatusError(http.StatusInternalServerError, errLaunchUnreserved)
	}
	turnID, err := openPromptTurn(ctx, roles, chatID, p, l)
	if err != nil {
		roles.admission.ReleaseTurnReservation(l.res)
		return "", err
	}
	// After OpenTurn returned, so the turn_opened frame precedes the header write.
	if l.claimComposer != nil && l.claimComposer() {
		settleComposerOnPrompt(ctx, roles.chats, roles.bus, chatID, p)
	}

	// In-flight before the answer, so a shutdown between it and the goroutine's first step still
	// waits. The turn context is detached from the POST's; the goroutine owns the cancel.
	roles.lifecycle.InflightAdd(1)
	turnCtx, cancel := roles.lifecycle.TurnContext(ctx)
	go runPromptTurn(turnCtx, cancel, roles, l.res, turnID, p)
	return turnID, nil
}

// errChatClosed is the prompt's refusal when the chat's close ended it before it ran.
var errChatClosed = errors.New("this chat was closed, so the message was not sent")

// Every path out releases the inflight count, the turn, the reservation and the door's open hold. Failures past
// the ack are SSE-only: the POST has already answered.
func runPromptTurn(ctx context.Context, cancel context.CancelFunc, roles *promptRoles, res Reservation, turnID string, p *marotte.PromptCommand) {
	defer roles.lifecycle.InflightDone()
	defer cancel()
	chatID := res.Chat
	defer roles.turnOutcome.ReleaseTurn(chatID, turnID)
	sb, release, err := roles.bridges.OpenBridgeAt(ctx, chatID, p.Model, res)
	defer release()
	if closedCancelled(err) {
		roles.admission.ReleaseTurnReservation(res)
		slog.Debug("prompt: the chat closed before its session opened", "chat_id", chatID, "turn", turnID)
		closeBeforeStart(ctx, roles, chatID, turnID, marotte.StopReasonCancelled, "")
		return
	}
	if err != nil {
		roles.admission.ReleaseTurnReservation(res)
		reason := rpcerr.Text(err)
		closeBeforeStart(ctx, roles, chatID, turnID, marotte.StopReasonInterrupted, reason)
		roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{Code: marotte.ErrCodeBridgeStartFailed, Message: reason, TurnScoped: true}))
		return
	}
	promptAdmittedTurn(ctx, roles, sb, res, turnID, p)
}

// closedCancelled reports a prompt the chat's close ended past its ack: its door refused or its lease was cut
// (marotte.ErrChatClosedDuringCall), or its turn was already forgotten (marotte.ErrNoSuchTurn). Such a prompt closes
// cancelled with no reason and no error frame.
func closedCancelled(err error) bool {
	return errors.Is(err, marotte.ErrChatClosedDuringCall) || errors.Is(err, marotte.ErrNoSuchTurn)
}

// closedBeforeStart reports a turn another closer already ended `cancelled` (the chat's close) or forgot, which
// owes no failure frame. The caller's ctx is live and StartTurn refused, so the turn is finalized or finalizing and
// the wait is short.
func closedBeforeStart(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string) bool {
	result, err := roles.turnOutcome.AwaitTurn(ctx, chatID, turnID)
	if err != nil {
		return closedCancelled(err)
	}
	return result.Stop == marotte.StopReasonCancelled
}

// closeBeforeStart runs the turn end rule on a prompt's turn that never reached
// StartTurn. Its parked steers are the turn end's to resend, like any turn's.
func closeBeforeStart(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string) {
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, "", 0)
}

// promptAdmittedTurn runs the turn under its reservation. The release ORDER is the contract: capture the finalized
// result through the still-held turn handle, then the reservation, and the deferred ReleaseTurn last so every
// turn-keyed predicate reads a live record.
func promptAdmittedTurn(ctx context.Context, roles *promptRoles, sb Bridge, res Reservation, turnID string, p *marotte.PromptCommand) {
	chatID := res.Chat
	// callCtx is this call's alone: its cancel is the turn's (StartTurn), so the settle and the empty-turn retry
	// run under ctx, out of reach of an interrupt or grace aimed at this turn after it closed.
	callCtx, cancelPrompt := context.WithCancelCause(ctx)
	defer cancelPrompt(nil)
	turn := TurnRef{Chat: chatID, Turn: turnID}

	// At bridge-ready, so the model and credit baseline are captured with the bridge live and the
	// session open is not timed.
	if !roles.turnOutcome.StartTurn(callCtx, turn, cancelPrompt) {
		// Dead ctx, or the death closer took the turn: the turn end rule runs here, and a closed
		// turn writes nothing.
		roles.admission.ReleaseTurnReservation(res)
		if callCtx.Err() == nil && closedBeforeStart(ctx, roles, chatID, turnID) {
			return
		}
		stop, reason := turnStopBeforeStart(callCtx, "The turn was cancelled before the agent answered.")
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
	deliverParkedSteers(callCtx, roles, chatID, turnID)
	slog.Info("prompt", "chat_id", chatID, "len", len(p.Text))
	start := time.Now()
	promptParams, inlinedImage := buildPromptParams(callCtx, roles.workspace, sb, p, turnID, historyInlineImages(callCtx, roles.chats, chatID))
	reply, err := sendTurnPrompt(callCtx, roles.sendGate, sb, promptParams, turn)
	roles.turnOutcome.EndPromptCall(turn)
	elapsed := time.Since(start)
	if err != nil {
		// callCtx: its cause tells a stop's expired grace from any other cancel.
		reportPromptFailure(callCtx, roles, chatID, turnID, err, reply.seq, elapsed, inlinedImage)
		roles.admission.ReleaseTurnReservation(res)
		return
	}
	slog.Info("prompt complete", "chat_id", chatID, "elapsed", elapsed)
	if roles.auth != nil {
		roles.auth.record(nil)
	}

	// The close settles the withheld steer carry and decides the turn's empty retry on the run order; the
	// reservation goes once the turn finalized.
	roles.turnOutcome.SettleTurnOnResponse(ctx, chatID, turnID, reply.seq, reply.resp)
	if _, aErr := roles.turnOutcome.AwaitTurn(ctx, chatID, turnID); aErr != nil {
		slog.Debug("prompt: no turn outcome to wait for", "chat_id", chatID, keyError, aErr)
	}
	roles.admission.ReleaseTurnReservation(res)
}

// deliverParkedSteers runs between StartTurn and session/prompt under the chat's steer lock, which
// orders delivery against a delete; a later delete's clear reply and read-loop barrier decide
// whether it removed the steer or lost to KAS's read. Pending turn ends resolve into this turn
// first; a clear owed for KAS's load re-injection goes next, and if skipped or lost those rows
// stay unsent, never sent beside KAS's copies.
func deliverParkedSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
	// The ends and the post-load clear are the chat's current record's, not the turn's:
	// a turn a close ended while this waited for the lock must leave them to the reopened chat.
	if !roles.turnOutcome.TurnIsOpen(chatID, turnID) {
		return
	}
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
		if refuse, _ := steerOne(ctx, roles, chatID, key, text, turnID, nil); refuse != "" {
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

const logMsgPromptEngineEnded = "prompt ended because the agent engine exited"

// logPromptFailure logs a failed prompt and counts it under its class. It logs at Error under msg,
// except an engine-exit casualty: the engine's one exit line is that failure, so the casualty is a
// Warn under logMsgPromptEngineEnded.
func logPromptFailure(msg string, chatID marotte.ChatID, err error, attrs ...any) {
	class := classifyPromptFailure(err)
	obs.PromptFailed(class.String())
	attrs = append([]any{"chat_id", chatID, "class", class.String(), keyError, err}, attrs...)
	if class == classPipeDeath {
		slog.Warn(logMsgPromptEngineEnded, attrs...)
		return
	}
	slog.Error(msg, attrs...)
}

// reportPromptFailure finalizes a turn whose prompt Call failed and broadcasts the failure, the
// only live surface after the ack. It renders the cause once, so RPCErrorText's fallback cannot
// overwrite promptFailureReason's prose.
func reportPromptFailure(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, err error, seq uint64, elapsed time.Duration, inlinedImage bool) {
	stop, reason, kind := promptFailureAccount(ctx, err, inlinedImage)
	if stop == marotte.StopReasonCancelled {
		// No error frame: nothing is actionable, and a red toast for a stop the reader
		// asked for, or for the chat's close, is the wrong signal.
		if closedCancelled(err) || errors.Is(context.Cause(ctx), marotte.ErrChatClosedDuringCall) {
			slog.Debug("prompt: the chat's close ended its call", "chat_id", chatID, "elapsed", elapsed)
		} else {
			slog.Info("prompt cancelled by the reader's stop: kept unsent, or unanswered within the grace budget",
				"chat_id", chatID, "elapsed", elapsed)
		}
		roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, kind, seq)
		return
	}
	logPromptFailure("prompt failed", chatID, err, "elapsed", elapsed)
	roles.turnOutcome.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, kind, seq)
	g := gradeFailedTurn(ctx, roles, chatID, turnID, err, reason)
	code := marotte.ErrCodePromptFailed
	if g.auth {
		code = marotte.ErrCodeAuthTokenUnavailable
	}
	roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID,
		marotte.ErrorPayload{Code: code, Message: g.reason, TurnScoped: g.onCard}))
}

// failedTurnGrade is how a failed prompt's error frame reports it.
type failedTurnGrade struct {
	reason string
	// auth: a refused credential, the one failure whose remedy is not "send again".
	auth bool
	// onCard: reason is the text the turn's card carries (ErrorPayload.TurnScoped).
	onCard bool
}

// gradeFailedTurn reads the close of turnID, already abandoned for err, and grades it: reason is
// what its turn_close carries (fallback when it carries none), so a toast says what the card
// says, and a refused credential latches AuthReadiness. A failure inside the execution names its
// class only on the turn.
func gradeFailedTurn(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, turnID string, err error, fallback string) failedTurnGrade {
	g := failedTurnGrade{reason: fallback, onCard: true}
	result, aErr := roles.turnOutcome.AwaitTurn(ctx, chatID, turnID)
	if aErr == nil && result.Reason != "" {
		g.reason = result.Reason
	}
	if classifyPromptFailure(err) != classAuth && (aErr != nil || !authClass(result.EngineErrorType)) {
		return g
	}
	// KAS grades every refused credential ask as unauthenticated, so only the vault knows the provider was
	// down; the card then carries KAS's sign-in sentence, which the vault's reason replaces.
	if roles.signIn != nil {
		if why, down := roles.signIn.ProviderUnreachable(); down {
			return failedTurnGrade{reason: why}
		}
	}
	g.auth = true
	if roles.auth != nil {
		roles.auth.record(err)
	}
	return g
}

// buildPromptParams constructs the full session/prompt parameter map for turnID's prompt and
// reports whether this prompt contains an inlined image.
func buildPromptParams(ctx context.Context, ws Workspace, sb SessionCaller, p *marotte.PromptCommand, turnID string, historyImages int) (map[string]any, bool) {
	blocks := BuildPromptBlocks(ctx, p.Text, p.Attachments, historyImages, ws, sb)
	params := sessionParams(sb, map[string]any{
		marotte.KeyPrompt: blocks,
	})
	// The client's message id is what makes rewind addressable: revertMultiple requires a messageId
	// naming a user message, one KAS only knows because it was sent here.
	if p.MessageID != "" {
		params["messageId"] = p.MessageID
	}
	setPromptMeta(ctx, params, ws.ConfigDir, p, turnID)
	return params, inlineImageBlockCount(blocks) > 0
}

// setPromptMeta writes params' _meta for turnID's prompt, replacing any earlier attempt's: the tag names the
// turn whose turn_start binds it, so a retry turn re-sending the same params needs its own.
func setPromptMeta(ctx context.Context, params map[string]any, configDir string, p *marotte.PromptCommand, turnID string) {
	delete(params, "_meta")
	if meta := kascap.PromptMeta(promptFacts(ctx, configDir, p, turnID)); len(meta) > 0 {
		params["_meta"] = map[string]any{"kiro": meta}
	}
}

// promptFacts withholds the output style when it is the default, unset or not shaped like an id,
// and from a prompt an agent reads (p.ForAgent). An id KAS does not serve rides; KAS reads it as
// default.
func promptFacts(ctx context.Context, configDir string, p *marotte.PromptCommand, turnID string) *kascap.Prompt {
	var style string
	read := !p.ForAgent && configDir != "" && settings.FieldInto(ctx, configDir, settings.KeyOutputStyle, &style)
	if !read || !settings.ValidOutputStyle(style) || style == settings.OutputStyleDefault {
		style = ""
	}
	var caller string
	if p.Caller != nil {
		caller = p.Caller.Label
	}
	return &kascap.Prompt{OutputStyle: style, Label: p.DisplayText, Tag: turnID, Caller: caller}
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
	// classBusy is KAS's SessionBusyError: a prompt or close still in flight, a real wait.
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
	// request. gradeFailedTurn owns when it sends marotte.ErrCodeAuthTokenUnavailable,
	// the only code the client routes to a Sign in CTA.
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
// the credential was not usable; kascap.TestAuthClassesMatchBundle pins them
// to the pinned bundle's auth classes.
//
// ModelRegistryAccessDeniedError is deliberately absent: upstream tells
// that user the account lacks model access, which no sign-in fixes.
var authErrorNames = []string{
	"TokenInvalidError",
	"TokenExpiredError",
	"CredentialAbsentError",
	"AuthRefreshFailedError",
	"SignInRequiredError",
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
	if errors.Is(err, marotte.ErrSessionBusy) {
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
	case marotte.RPCCodeSessionBusy:
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
	if authClass(m.ErrorType) {
		return classAuth
	}
	return classFatal
}

// authClass reports whether name is a KAS class for a refused credential.
func authClass(name string) bool {
	return slices.Contains(authErrorNames, name)
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

// A stop concludes `cancelled`: ErrPromptStopped for a prompt it kept unsent, and ErrCancelGraceExpired on
// the cause (only the grace timer stamps it) for a sent one. Any other cancel is a fault and grades `interrupted`.
func promptFailureAccount(ctx context.Context, err error, inlinedImage bool) (stop marotte.StopReason, reason string, kind marotte.FailureKind) {
	if errors.Is(err, ErrPromptStopped) || closedCancelled(err) || errors.Is(context.Cause(ctx), marotte.ErrChatClosedDuringCall) {
		return marotte.StopReasonCancelled, "", ""
	}
	if errors.Is(err, context.Canceled) {
		if errors.Is(context.Cause(ctx), ErrCancelGraceExpired) {
			// No prose: DefaultFailureReason(TurnOutcomeCancelled) is empty because the
			// footer's own word already reads "Cancelled" a row away.
			return marotte.StopReasonCancelled, "", ""
		}
		return marotte.StopReasonInterrupted, "The turn was cancelled before the agent answered.", ""
	}
	if re, ok := errors.AsType[*marotte.RPCError](err); ok {
		if m, mapped := rpcerr.MappedOf(re); mapped {
			text, overflow := rpcerr.Account(m, re.Message)
			if overflow {
				kind = marotte.FailureKindContextLimit
			}
			return marotte.StopReasonInterrupted, text, kind
		}
	}
	return marotte.StopReasonInterrupted, promptFailureReason(err, inlinedImage), ""
}

// promptFailureReason renders an untyped failure (no KAS error envelope) into
// something worth showing the user.
func promptFailureReason(err error, inlinedImage bool) string {
	re, ok := errors.AsType[*marotte.RPCError](err)
	if !ok {
		return rpcerr.Text(err)
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
