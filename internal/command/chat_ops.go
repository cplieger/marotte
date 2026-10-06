package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
)

// CancelGrace is how long a cooperative session/cancel gets to end the turn before marotte unblocks
// it itself (KiroCrew's `_CANCEL_GRACE_SECS`). A var only so a test can shrink it.
var CancelGrace = 10 * time.Second

// ErrCancelGraceExpired is the cause the grace timer attaches to the prompt context it cancels, so
// the failure site concludes `cancelled` rather than `interrupted`. Shutdown and cancelPromptCall
// leave the cause at context.Canceled, and the first cancellation sets the cause, so whichever of
// shutdown and the grace fires first decides the outcome.
var ErrCancelGraceExpired = errors.New("cancel grace expired")

// CmdCreateChat creates a new chat, opens its tab, and returns both, so the caller can address what
// it created. It mints the id when the envelope carries none; a client-minted id is still accepted
// (validChatID gates it).
func CmdCreateChat(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.CreateChatCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	name := p.Name
	if name == "" {
		name = marotte.DefaultChatName
	}
	if len(name) > marotte.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ValidIdent(p.Model) || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	opened, err := mem.CreateChatAndOpen(ctx, ChatCreate{
		OpID:   p.OpID,
		ChatID: cmd.ChatID,
		Init: func(c *marotte.Chat) {
			c.Name = name
			c.Model = p.Model
		},
	})
	if err != nil {
		return nil, err
	}
	return openedResponse(&opened, nil), nil
}

// openedResponse is the shape every create-and-open answers with: the chat
// header, the tab opened for it, and the version that open produced, plus
// whatever the command adds. Subject is omitted when no tab store is wired
// (a zero-valued subject would name a tab with an empty id).
func openedResponse(opened *ChatOpened, extra map[string]any) any {
	body := make(map[string]any)
	maps.Copy(body, extra)
	body["chat"] = opened.Chat.Header()
	body[keyVersion] = opened.Version
	if opened.Subject.ID != "" {
		body["subject"] = opened.Subject
	}
	return responseWith(body)
}

// CmdDeleteChat removes a chat: tear down its side effects, remove the
// record, then close its tabs. The order is the coordinator's — the record
// leads, so an open_tab that slips in after finds no chat and is refused.
func CmdDeleteChat(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.CloseTabCommand
	if len(cmd.Payload) > 0 {
		// Optional and carries only an op_id; ignored rather than refused if
		// unreadable, since the subject is the envelope's chat id.
		_ = json.Unmarshal(cmd.Payload, &p)
	}
	if !ValidIdent(p.OpID) {
		p.OpID = ""
	}
	if err := mem.DeleteChatAndCloseTabs(ctx, cmd.ChatID, p.OpID); err != nil {
		return nil, err
	}
	return responseOK, nil
}

// CmdCancel cancels the active turn, if any. The turn's end leaves its unread
// steers for the next send; a payload lead names the row the reader wants first. It
// never waits on the steer lock, so a stop never queues behind a delete.
func CmdCancel(
	ctx context.Context,
	bridges BridgeAccess,
	perms PendingPermAccess,
	terms TerminalAccess,
	stops TurnStopper,
	queue SteerQueue,
	cmd *marotte.ClientCommand,
) (any, error) {
	// An absent or empty payload is a plain cancel.
	var p marotte.CancelCommand
	if len(cmd.Payload) > 0 && json.Unmarshal(cmd.Payload, &p) != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// First, before any signal: a retry reading the stop after it registered its
	// prompt call is then guaranteed a grace budget this cancel can arm.
	stops.RequestStop(cmd.ChatID)

	// Only pending permissions are cleared; KAS owns the write gate and
	// cancelling a turn already reverts its own approval.
	perms.ClearPendingPermsForChat(cmd.ChatID)

	// Stopping the model does not stop the processes its turn already
	// spawned. Scoped to the turn's own terminals only.
	terms.KillForTurn(cmd.ChatID)

	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return responseOK, nil
	}
	undoLead := func() {}
	if queue != nil && p.Lead != "" {
		undoLead = queue.SetSteerLead(cmd.ChatID, p.Lead)
	}
	if err := sb.Notify(ctx, marotte.MethodCancel, SessionParams(sb)); err != nil {
		undoLead()
		slog.Error("cancel failed", "chat_id", cmd.ChatID, keyError, err)
	}
	// session/cancel is a notification nothing acks: the turn ends only when KAS answers
	// session/prompt. If it never does, the grace budget cancels the prompt's context so the
	// ordinary prompt-failure path finalizes the turn.
	if !sb.ArmCancelGrace(sb.PromptGeneration(), CancelGrace) {
		slog.Debug("cancel: no in-flight prompt to arm a grace budget against", "chat_id", cmd.ChatID)
	}
	return responseOK, nil
}

// closeChatTeardown is the tab-close teardown: the turn, the runs and the process go; the chat
// RECORD stays. Ordering is the contract: the turn is cancelled first (graceful stop), then the
// runs (durable state a dead process would only pause), then the process teardown, which flushes
// the in-flight buffer and kills the chat's agent terminals.
func closeChatTeardown(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, stops TurnStopper, teardown ChatTeardown, chatID marotte.ChatID) {
	slog.Info("chat tab closed; tearing down its bridge", "chat_id", chatID)
	stops.RequestStop(chatID)
	perms.ClearPendingPermsForChat(chatID)
	teardown.BeginChatTeardown(chatID, true)
	if sb := bridges.Bridge(chatID); sb != nil {
		if err := sb.Notify(ctx, marotte.MethodCancel, SessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
		}
	}
	teardown.CloseChatState(ctx, chatID)
}

// deleteChatTeardown is closeChatTeardown's delete grade, for a chat the
// retention-off close escalation has already erased: same graceful cancel
// first, then the delete-grade teardown driven from the session chain
// captured before the record went (a record-reading teardown would no-op on
// a deleted chat). Mirrors closeChatTeardown so the two grades cannot drift.
func deleteChatTeardown(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, stops TurnStopper, teardown ChatTeardown, chatID marotte.ChatID, sessionChain []string) {
	stops.RequestStop(chatID)
	perms.ClearPendingPermsForChat(chatID)
	teardown.BeginChatTeardown(chatID, false)
	if sb := bridges.Bridge(chatID); sb != nil {
		if err := sb.Notify(ctx, marotte.MethodCancel, SessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
		}
	}
	teardown.DeleteChatStateByChain(ctx, chatID, sessionChain, RunStopTabClosed)
}

// CmdPermission forwards the user's permission dialog choice to kiro-cli.
func CmdPermission(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return nil, StatusError(http.StatusBadRequest, errNoBridge)
	}
	var p marotte.PermissionResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Refused before the claim, so a bad note leaves the request answerable.
	reason := strings.TrimSpace(p.RejectionReason)
	if utf8.RuneCountInString(reason) > marotte.MaxRejectionReasonRunes ||
		(reason != "" && len(p.FileDecisions) > 0) {
		return nil, StatusError(http.StatusBadRequest, errRejectionReasonInvalid)
	}
	// Claim the request before answering it: two tabs on one chat can both
	// see the card, and kiro-cli silently discards the second answer for a
	// request id already resolved.
	pending, offered := perms.TakePendingPermissionOption(cmd.ChatID, p.RequestID, p.OptionID, marotte.SettledByUser)
	if !pending {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	if !offered {
		return nil, StatusError(http.StatusBadRequest, errPermissionOptionNotOffered)
	}
	// A turn approval answers on the same reply, with per-file decisions in
	// _meta; built through one helper so the omitted-id-means-reject rule
	// lives in one place.
	outcome := marotte.PermissionOutcomeWithFileDecisions(p.OptionID, p.FileDecisions)
	if len(p.FileDecisions) == 0 {
		outcome = marotte.PermissionOutcomeWithRejectionReason(p.OptionID, reason)
	}
	if err := sb.Respond(ctx, p.RequestID, outcome, nil); err != nil {
		slog.Error("permission response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
