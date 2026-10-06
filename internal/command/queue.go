package command

// The follow-up queue. In Queue mode a message sent mid-turn waits on the chat
// record rather than joining the turn, and the server sends it after the turn
// closes (drainAfterClose); no client sends a queued row.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

var (
	errQueueNoTurn = errors.New("nothing is running to queue behind, so send this as a prompt instead")
	errQueueFull   = errors.New("this chat already holds the most follow-ups it can queue; wait for one to send, then try again")
	errQueueTooBig = errors.New("a queued message is capped at the draft size")
	// errQueueTooMany refuses an attachment list past MaxAttachments.
	errQueueTooMany = errors.New("too many attachments on one queued message")
	errQueueSending = errors.New("this follow-up is already being sent")
)

// reasonSending is the 409 refusal class for an unqueue of a row a drain is
// already sending.
const reasonSending = "sending"

// QueueAccess is the follow-up queue as the commands and the drain drive it. An
// append sees the turn registry, so a turn cannot close between "it is running"
// and "it is queued" and leave a row no close will send.
type QueueAccess interface {
	// AppendIfLive applies fn to the chat's record while the chat holds a live turn,
	// reporting live=false (and writing nothing) when it holds none. fn's error
	// aborts the write and is returned as is.
	AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (live bool, err error)
	// NextUserRow answers the first queued user row that is not held and whose
	// turn this process has not opened.
	NextUserRow(ctx context.Context, chatID marotte.ChatID) (marotte.QueuedPrompt, bool, error)
	// Unqueue removes the row with id. An id whose turn this process opened answers
	// sending=true; an id no row carries is success.
	Unqueue(ctx context.Context, chatID marotte.ChatID, id string) (sending bool, err error)
	// Dequeue takes a sent row off the header once its turn is announced. A failed
	// write is applied by the chat's next mutation.
	Dequeue(ctx context.Context, chatID marotte.ChatID, id string) error
}

// chatMutator is the one ChatStore write the record-only queue and mode handlers
// make.
type chatMutator interface {
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
}

// CmdQueuePrompt holds a message for the end of the running turn. An idle chat is
// a 409 no_turn, the refusal a steer gives, so the client sends a prompt instead.
func CmdQueuePrompt(ctx context.Context, queue QueueAccess, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	row, err := queuedRow(cmd)
	if err != nil {
		return nil, err
	}
	live, err := queue.AppendIfLive(ctx, cmd.ChatID, func(c *marotte.Chat) error {
		if slices.ContainsFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return q.ID == row.ID }) {
			return nil
		}
		users := 0
		for i := range c.QueuedPrompts {
			if !c.QueuedPrompts[i].Carried() {
				users++
			}
		}
		if users >= marotte.MaxQueuedPrompts-2 {
			return StatusErrorReason(http.StatusConflict, reasonFull, errQueueFull)
		}
		c.QueuedPrompts = append(c.QueuedPrompts, row)
		return nil
	})
	switch {
	case err != nil:
		if _, ok := errors.AsType[*statusError](err); ok {
			return nil, err
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	case !live:
		return nil, StatusErrorReason(http.StatusConflict, reasonNoTurn, errQueueNoTurn)
	}
	slog.Info("prompt queued", "chat", cmd.ChatID, "message_id", row.ID)
	return responseWith(map[string]any{"message_id": row.ID}), nil
}

// queuedRow validates the queue payload into the row it stores.
func queuedRow(cmd *marotte.ClientCommand) (marotte.QueuedPrompt, error) {
	var p marotte.QueuePromptCommand
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	switch {
	case strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0:
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, errEmptyPrompt)
	case len(p.Text) > marotte.MaxDraftBytes:
		return marotte.QueuedPrompt{}, StatusError(http.StatusRequestEntityTooLarge, errQueueTooBig)
	case !ValidMessageID(p.MessageID):
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, errMissingMessageID)
	case len(p.Attachments) > marotte.MaxAttachments:
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, errQueueTooMany)
	}
	for _, a := range p.Attachments {
		if a.Path == "" || len(a.Path) > marotte.MaxAttachmentPathBytes {
			return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	return marotte.QueuedPrompt{ID: p.MessageID, Text: p.Text, Attachments: p.Attachments}, nil
}

// CmdUnqueuePrompt removes one queued row. An unknown id is success (two devices can discard one
// row); a row whose turn opened is a 409 sending. The steer lock orders it against a drain's
// pick-to-open transfer.
func CmdUnqueuePrompt(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.UnqueuePromptCommand
	if json.Unmarshal(cmd.Payload, &p) != nil || p.MessageID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	unlock, err := roles.queue.LockSteerOps(ctx, cmd.ChatID)
	if err != nil {
		return nil, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	sending, err := roles.followups.Unqueue(ctx, cmd.ChatID, p.MessageID)
	switch {
	case err != nil:
		return nil, StatusError(http.StatusInternalServerError, err)
	case sending:
		return nil, StatusErrorReason(http.StatusConflict, reasonSending, errQueueSending)
	}
	return responseOK, nil
}

// CloseFacts is what a winning close hands its drain: the closed turn's outcome
// and the fence naming it.
type CloseFacts struct {
	Outcome marotte.TurnOutcome
	Fence   TurnFence
}

// drainAfterClose sends at most one prompt for a close, under the chat's steer lock from the pick
// to launchPrompt's return: unread steers first (after a close leaving a live bridge or a drained
// death), else one queued user row (after a clean close, bridge live). A pending end, a held slot,
// a newer turn or a shutdown sends nothing; no shell interception.
func drainAfterClose(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, cl CloseFacts, ends EndFacts) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
	if roles.lifecycle.Draining() || len(roles.jobs.Ends(chatID)) > 0 {
		return
	}
	if !roles.admission.TryReserveTurnFenced(chatID, marotte.TurnSourcePrompt, cl.Fence) {
		return
	}
	live := roles.bridges.BridgeLive(chatID)
	deathDrained := ends.Death && !ends.Undrained
	if live || deathDrained {
		if rows := roles.jobs.UnsentRows(chatID, deathDrained); len(rows) > 0 {
			sendUnread(ctx, roles, chatID, rows, cl.Fence)
			return
		}
	}
	if !live || marotte.SeverityOf(cl.Outcome) != marotte.TurnSeverityClean || roles.followups == nil {
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	sendNextUserRow(ctx, roles, chatID, cl.Fence)
}

// sendNextUserRow opens the chat's first sendable queued row, holding the
// reservation the drain took; no row, or one that cannot be read, releases it.
func sendNextUserRow(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, fence TurnFence) {
	row, ok, err := roles.followups.NextUserRow(ctx, chatID)
	if err != nil || !ok {
		if err != nil {
			slog.Warn("queued prompts could not be read; they stay queued", "chat_id", chatID, keyError, err)
		}
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	p := &marotte.PromptCommand{Text: row.Text, MessageID: row.ID, Attachments: row.Attachments}
	if err := launchPrompt(ctx, roles, chatID, p, launch{dequeue: row.ID, fence: fence}); err != nil {
		logDrainRefused(chatID, row.ID, err)
		return
	}
	slog.Info("queued prompt dispatched", "chat_id", chatID, "message_id", row.ID)
}

// sendUnread opens one prompt carrying the unread rows, joined by a blank line, and
// retires them once its turn_open is appended. A refused open leaves them unsent.
func sendUnread(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, rows []SteerRow, fence TurnFence) {
	keys := make([]string, 0, len(rows))
	texts := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
		texts = append(texts, r.Text)
	}
	p := &marotte.PromptCommand{Text: strings.Join(texts, "\n\n"), MessageID: ids.NewMessageID()}
	if err := launchPrompt(ctx, roles, chatID, p, launch{resends: keys, fence: fence}); err != nil {
		logDrainRefused(chatID, p.MessageID, err)
		return
	}
	roles.jobs.Delivered(chatID, keys)
	slog.Info("unread steers sent as the next prompt", "chat_id", chatID, "steers", len(keys))
}

func logDrainRefused(chatID marotte.ChatID, messageID string, err error) {
	if errors.Is(err, ErrTurnSuperseded) {
		slog.Debug("drain stands down: a newer turn opened", "chat_id", chatID, "message_id", messageID)
		return
	}
	slog.Warn("queued send could not open; it waits for the next close", "chat_id", chatID, "message_id", messageID, keyError, err)
}
