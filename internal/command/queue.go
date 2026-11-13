package command

// The follow-up queue's commands. A queued row is an entry of the chat's run order (runQueue); the order's taker
// sends it, and no client sends a queued row.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// errQueueTooMany refuses an attachment list past MaxAttachments.
var errQueueTooMany = errors.New("too many attachments on one queued message")

// reasonSending is the 409 refusal class for an unqueue of a row already being sent.
const reasonSending = "sending"

// reasonHeld is the 409 refusal class for a send-now of a row no turn end sends.
const reasonHeld = "held"

// cmdQueuePrompt holds a message for the end of what runs. An idle chat is a 409 no_turn, the refusal a steer gives,
// so the caller sends a prompt instead.
func cmdQueuePrompt(ctx context.Context, runq runQueue, admission turnAdmission, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	row, err := queuedRow(cmd)
	if err != nil {
		return nil, err
	}
	ans, err := runq.Submit(ctx, cmd.ChatID, admission.LifeStamp(), &Submission{
		Row: row, Origin: marotte.RunFromQueue, Kind: marotte.SendQueuePrompt, NoSteer: true,
		Retry: !kasClaimsPromptText(row.Text),
	})
	if err != nil {
		return nil, submitRefusal(err)
	}
	slog.Info("prompt queued", "chat", cmd.ChatID, "message_id", row.ID)
	return responseWith(map[string]any{"message_id": row.ID, "as": ans.As, "run": ans.Run}), nil
}

// maxQueuedEncodedBytes bounds the queue's rows as the chat header encodes them. The header
// is capped at 8 MiB (internal/chat maxHeaderBytes) and must still hold a carried row and the
// draft, each up to six times its text once JSON escapes it (\u003c for <).
const maxQueuedEncodedBytes = 4 << 20

// RowCapsRefusal is why row may not join a list already holding rows: the user rows stop short of
// marotte.MaxQueuedPrompts, so a carried row an earlier version wrote always has a slot, and the rows' encoded size stays under
// maxQueuedEncodedBytes. Nil when it may.
func RowCapsRefusal(rows []*marotte.QueuedPrompt, row *marotte.QueuedPrompt) error {
	users := 0
	all := make([]marotte.QueuedPrompt, 0, len(rows)+1)
	for _, q := range rows {
		if !q.Carried() {
			users++
		}
		all = append(all, *q)
	}
	if users >= marotte.MaxQueuedPrompts-2 {
		return errSubmitFull
	}
	if encoded, err := json.Marshal(append(all, *row)); err != nil || len(encoded) > maxQueuedEncodedBytes {
		return errSubmitFull
	}
	return nil
}

func queuedRow(cmd *marotte.ClientCommand) (marotte.QueuedPrompt, error) {
	var p marotte.QueuePromptCommand
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	switch {
	case strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0:
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, errEmptyPrompt)
	case len(p.Text) > MaxPromptBytes:
		return marotte.QueuedPrompt{}, StatusError(http.StatusRequestEntityTooLarge, errPromptTooLong)
	case !validMessageID(p.MessageID):
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, errMissingMessageID)
	case len(p.Attachments) > marotte.MaxAttachments:
		return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, errQueueTooMany)
	}
	for _, a := range p.Attachments {
		if a.Path == "" || len(a.Path) > marotte.MaxAttachmentPathBytes {
			return marotte.QueuedPrompt{}, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	return marotte.QueuedPrompt{
		ID: p.MessageID, Text: p.Text, Label: displayLabel(p.DisplayText), Attachments: p.Attachments, Caller: cmd.From,
	}, nil
}

// cmdUnqueuePrompt removes one queued row. An unknown id is success (two devices can discard one
// row); a row whose turn starts or runs, or whose steer is being sent, is a 409 sending. A row
// kiro-cli holds as a steer is deleted from its buffer as the dock's Delete does.
func cmdUnqueuePrompt(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.UnqueuePromptCommand
	if json.Unmarshal(cmd.Payload, &p) != nil || p.MessageID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	switch err := roles.runq.Unqueue(ctx, cmd.ChatID, p.MessageID); {
	case errors.Is(err, ErrRowInKAS):
		if rerr := removeChatSteer(ctx, roles, cmd.ChatID, marotte.SteerIDFor(p.MessageID), false); rerr != nil {
			return nil, rerr
		}
	case errors.Is(err, ErrRowSending):
		return nil, StatusErrorReason(http.StatusConflict, reasonSending, err)
	case err != nil:
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	return runAnswer(ctx, roles.runq, cmd.ChatID, nil), nil
}

// cmdResumeQueue is the queue's Resume (runQueue.Resume): the rows a stop paused go in order. A queue with nothing
// paused is success.
func cmdResumeQueue(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	if err := roles.runq.Resume(ctx, cmd.ChatID); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	return runAnswer(ctx, roles.runq, cmd.ChatID, nil), nil
}

// cmdSendQueuedNow sends one queued row next (SendQueuedNowCommand). It stops only the turn its Stop names, and only
// while that turn is still the lone live turn, so a press that crosses that turn's close stops nothing.
func cmdSendQueuedNow(
	ctx context.Context, runq runQueue, bridges bridgeAccess, perms pendingPermAccess, terms terminalAccess,
	stops turnStopper, cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SendQueuedNowCommand
	var raw map[string]json.RawMessage
	if json.Unmarshal(cmd.Payload, &p) != nil || p.MessageID == "" || json.Unmarshal(cmd.Payload, &raw) != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	_, hasStop := raw["stop"]
	rs, err := runq.SendNow(ctx, cmd.ChatID, p.MessageID, p.Stop, hasStop)
	switch {
	case errors.Is(err, ErrRowHeld):
		return nil, StatusErrorReason(http.StatusConflict, reasonHeld, err)
	case err != nil:
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	undo := func() { runq.ClearNext(cmd.ChatID, p.MessageID) }
	return carryOutStop(ctx, runq, bridges, perms, terms, stops, cmd.ChatID, rs, undo)
}

// runAnswer is a run-order command's answer: OK, extra, and the run order after it.
func runAnswer(ctx context.Context, runq runQueue, chatID marotte.ChatID, extra map[string]any) any {
	out := map[string]any{"run": runq.RunView(ctx, chatID)}
	maps.Copy(out, extra)
	return responseWith(out)
}

// CloseFacts is a turn close's grade as the run order reads it.
type CloseFacts struct {
	Outcome marotte.TurnOutcome
	// Unattended is a close no one stopped (marotte closing a turn KAS went idle without ending, or a fold-opened
	// turn it handed over), graded as a clean close.
	Unattended bool
}

// Clean reports a clean or unattended close.
func (cl CloseFacts) Clean() bool {
	return marotte.SeverityOf(cl.Outcome) == marotte.TurnSeverityClean || cl.Unattended
}
