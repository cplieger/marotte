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
	errQueueHeld    = errors.New("this follow-up was queued before the server restarted; edit it to send it")
)

// reasonSending is the 409 refusal class for an unqueue of a row a drain is
// already sending.
const reasonSending = "sending"

// reasonHeld is the 409 refusal class for a send-now of a row no turn end sends.
const reasonHeld = "held"

// QueueAccess is the follow-up queue as the commands and the drain drive it. An
// append sees the turn registry, so a live turn cannot close between "it is
// running" and "it is queued" and leave a row no close will send.
type QueueAccess interface {
	// AppendIfLive applies fn to the chat's record while the chat holds a live turn,
	// or waits for kiro-cli to end a run the user stopped (the next drain once that wait
	// ends may send one such row despite a stop, CloseFacts.AfterStop; the rest go as
	// after any close), reporting
	// live=false (and writing nothing) when neither. fn's error aborts the write and
	// is returned as is.
	AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (live bool, err error)
	// NextUserRow answers the first queued user row that is not held and whose
	// turn this process has not opened, among only when it is non-nil.
	NextUserRow(ctx context.Context, chatID marotte.ChatID, only []string) (marotte.QueuedPrompt, bool, error)
	// Unqueue removes the row with id. An id whose turn this process opened answers
	// sending=true; an id no row carries is success.
	Unqueue(ctx context.Context, chatID marotte.ChatID, id string) (sending bool, err error)
	// Dequeue takes a sent row off the header once its turn is announced. A failed
	// write is applied by the chat's next mutation.
	Dequeue(ctx context.Context, chatID marotte.ChatID, id string) error
	// MarkNext marks the row with id Next and moves it ahead of the other sendable
	// rows, which keep their order. An id whose turn this process opened answers
	// sending; a held or carried row answers held and is left alone.
	MarkNext(ctx context.Context, chatID marotte.ChatID, id string) (found, sending, held bool, err error)
	// ClearNext drops the row's Next mark, keeping the order.
	ClearNext(ctx context.Context, chatID marotte.ChatID, id string) error
	// Sending reports whether this process opened the row's turn.
	Sending(chatID marotte.ChatID, id string) bool
	// NextMarkedRow answers the Next row while it is unheld and its turn has not opened.
	NextMarkedRow(ctx context.Context, chatID marotte.ChatID) (marotte.QueuedPrompt, bool, error)
}

// chatMutator is the one chatStore write the record-only queue and mode handlers
// make.
type chatMutator interface {
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
}

// cmdQueuePrompt holds a message for the end of the running turn. An idle chat is
// a 409 no_turn, the refusal a steer gives, so the client sends a prompt instead.
func cmdQueuePrompt(ctx context.Context, queue QueueAccess, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	row, err := queuedRow(cmd)
	if err != nil {
		return nil, err
	}
	live, err := queue.AppendIfLive(ctx, cmd.ChatID, func(c *marotte.Chat) error {
		return appendUserRow(c, &row)
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

// appendUserRow adds a user row to the queue: a repeated id is already there, and the cap
// leaves room for the carried rows a close can add.
func appendUserRow(c *marotte.Chat, row *marotte.QueuedPrompt) error {
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
	c.QueuedPrompts = append(c.QueuedPrompts, *row)
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
	case len(p.Text) > marotte.MaxDraftBytes:
		return marotte.QueuedPrompt{}, StatusError(http.StatusRequestEntityTooLarge, errQueueTooBig)
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
// row); a row whose turn opened is a 409 sending. The steer lock orders it against a drain's
// pick-to-open transfer.
func cmdUnqueuePrompt(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
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

// cmdSendQueuedNow sends one queued row next (SendQueuedNowCommand). The mark and the
// idle reservation share the steer lock with a drain's pick, so a drain either sends
// this row or ran before the mark. A turn closing on its own between the mark and the
// stop has its drain open this row, so the stop re-checks the row under the lock and
// cancels nothing once its turn opened.
func cmdSendQueuedNow(
	ctx context.Context,
	roles *promptRoles,
	perms pendingPermAccess,
	terms terminalAccess,
	stops turnStopper,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SendQueuedNowCommand
	if json.Unmarshal(cmd.Payload, &p) != nil || p.MessageID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	stop, err := markQueuedNext(ctx, roles, cmd.ChatID, p.MessageID)
	if !stop || err != nil {
		return responseOrErr(err)
	}
	return stopChatTurn(ctx, roles.bridges, perms, terms, stops, cmd.ChatID, func() (func(), bool) {
		if queuedRowSending(ctx, roles, cmd.ChatID, p.MessageID) {
			return nil, true
		}
		return func() {
			if err := roles.followups.ClearNext(ctx, cmd.ChatID, p.MessageID); err != nil {
				slog.Warn("send now: the follow-up stays marked next", "chat_id", cmd.ChatID, "message_id", p.MessageID, keyError, err)
			}
		}, false
	})
}

// queuedRowSending reports whether the row's turn opened. It reads under the steer
// lock because a drain holds that lock from its pick until the row is dequeued, so a
// drain mid-launch is waited out rather than read as not yet sending. A lock the
// request's context cannot take answers false, leaving the stop to the cancel.
func queuedRowSending(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, id string) bool {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return false
	}
	defer unlock()
	return roles.followups.Sending(chatID, id)
}

// markQueuedNext marks the row next and, with no turn running, sends it at once.
// stop reports a marked row whose turn the caller must stop.
func markQueuedNext(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, id string) (stop bool, err error) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return false, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	found, sending, held, err := roles.followups.MarkNext(ctx, chatID, id)
	switch {
	case err != nil:
		return false, StatusError(http.StatusInternalServerError, err)
	case !found || sending:
		return false, nil
	case held:
		return false, StatusErrorReason(http.StatusConflict, reasonHeld, errQueueHeld)
	}
	if !roles.admission.TryReserveIdleTurn(chatID, marotte.TurnSourcePrompt) {
		return true, nil
	}
	row, ok, err := roles.followups.NextMarkedRow(ctx, chatID)
	if err != nil || !ok {
		roles.admission.ReleaseTurnReservation(chatID)
		if err != nil {
			return false, StatusError(http.StatusInternalServerError, err)
		}
		return false, nil
	}
	return false, launchQueuedRow(ctx, roles, chatID, TurnFence{}, &row)
}

func responseOrErr(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return responseOK, nil
}

// CloseFacts is what a winning close hands its drain: the closed turn's outcome
// and the fence naming it.
type CloseFacts struct {
	Outcome marotte.TurnOutcome
	// AfterStop are rows queued while kiro-cli ended the stopped run, with no turn
	// live: one of them may go after this close although it was stopped (after any
	// unread steers, which go first); the rest wait for the next close's drain.
	AfterStop []string
	Fence     TurnFence
	// Unattended is a close no one stopped (marotte closing a turn KAS went idle without
	// ending), after which queued user rows still go, as after a clean close.
	Unattended bool
}

// drainAfterClose sends at most one prompt for a close, under the chat's steer lock from the pick
// to launchPrompt's return: the row marked Next first, after any close leaving a live bridge (the
// unread steers ride into its turn); then unread steers (after a close leaving a live bridge or a
// drained death), else one queued user row (after a clean or unattended close, or one of AfterStop,
// bridge live). A pending end, a held slot, a newer turn or a shutdown sends nothing.
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
	sendReserved(ctx, roles, chatID, cl, ends)
}

// sendReserved is drainAfterClose's pick, on the reservation it took, which it owns from here.
func sendReserved(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, cl CloseFacts, ends EndFacts) {
	live := roles.bridges.BridgeLive(chatID)
	if live && roles.followups != nil && sendMarkedRow(ctx, roles, chatID, cl.Fence) {
		return
	}
	deathDrained := ends.Death && !ends.Undrained
	if live || deathDrained {
		if rows := roles.jobs.UnsentRows(chatID, deathDrained); len(rows) > 0 {
			sendUnread(ctx, roles, chatID, rows, cl.Fence)
			return
		}
	}
	clean := marotte.SeverityOf(cl.Outcome) == marotte.TurnSeverityClean || cl.Unattended
	var only []string
	if !clean {
		only = cl.AfterStop
	}
	if !live || (!clean && len(only) == 0) || roles.followups == nil {
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	sendNextUserRow(ctx, roles, chatID, cl.Fence, only)
}

// sendMarkedRow launches the row marked Next, reporting whether it took the
// reservation's turn; with no such row, or one that cannot be read, the reservation
// stays the caller's.
func sendMarkedRow(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, fence TurnFence) bool {
	row, ok, err := roles.followups.NextMarkedRow(ctx, chatID)
	if err != nil {
		slog.Warn("the follow-up marked next could not be read; the drain goes on without it", "chat_id", chatID, keyError, err)
	}
	if err != nil || !ok {
		return false
	}
	if err := launchQueuedRow(ctx, roles, chatID, fence, &row); err != nil {
		logDrainRefused(chatID, row.ID, err)
	}
	return true
}

// No row, or one that cannot be read, releases it. only narrows the pick (NextUserRow).
func sendNextUserRow(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, fence TurnFence, only []string) {
	row, ok, err := roles.followups.NextUserRow(ctx, chatID, only)
	if err != nil || !ok {
		if err != nil {
			slog.Warn("queued prompts could not be read; they stay queued", "chat_id", chatID, keyError, err)
		}
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	if err := launchQueuedRow(ctx, roles, chatID, fence, &row); err != nil {
		logDrainRefused(chatID, row.ID, err)
	}
}

// launchQueuedRow opens row's turn on the caller's reservation, which it owns from here
// (launchPrompt), and takes the row off the header once the turn is announced.
func launchQueuedRow(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, fence TurnFence, row *marotte.QueuedPrompt) error {
	p := &marotte.PromptCommand{
		Text: row.Text, MessageID: row.ID, DisplayText: row.Label, Attachments: row.Attachments, Caller: row.Caller,
	}
	if err := launchPrompt(ctx, roles, chatID, p, launch{dequeue: row.ID, fence: fence}); err != nil {
		return err
	}
	slog.Info("queued prompt dispatched", "chat_id", chatID, "message_id", row.ID)
	return nil
}

// sendUnread opens one prompt carrying the unread rows, joined by a blank line, and
// retires them once its turn_open is appended. A refused open leaves them unsent.
// The words are the rows' own, so the prompt carries no DisplayText: a label would
// replace them in the transcript and in KAS's replay. It is an API caller's only when
// no row is the user's (SharedCaller).
func sendUnread(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, rows []SteerRow, fence TurnFence) {
	keys := make([]string, 0, len(rows))
	texts := make([]string, 0, len(rows))
	callers := make([]*marotte.Caller, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
		texts = append(texts, r.Text)
		callers = append(callers, r.Caller)
	}
	p := &marotte.PromptCommand{
		Text: strings.Join(texts, "\n\n"), MessageID: ids.NewMessageID(), Caller: marotte.SharedCaller(callers),
	}
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
