package command

// The follow-up queue. In Queue mode a message sent mid-turn, and in every mode one
// sent while a compaction runs, waits on the chat record rather than joining a turn,
// and the server sends it after the turn closes (drainAfterClose) or the compaction
// ends (drainAfterCompaction); no client sends a queued row.

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
	errQueueTooBig = errors.New("a message queued behind a running turn is capped at the draft size")
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
	// AppendIfLive applies fn to the chat's record while the chat holds a live turn, waits
	// for kiro-cli to end a run the user stopped, or runs a compaction (the next drain
	// after that wait or compaction may send one row appended with no turn live despite a
	// stop, CloseFacts.AfterStop; the rest go as after any close), reporting live=false
	// (and writing nothing) when none holds. fn's error aborts the write and is returned
	// as is.
	AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (live bool, err error)
	// AppendIfCompacting applies fn as AppendIfLive does, only while the chat's compaction runs with no turn live.
	AppendIfCompacting(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (queued bool, err error)
	// Compacting reports a compaction running on the chat, whose end sends the next queued row.
	Compacting(chatID marotte.ChatID) bool
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

// cmdQueuePrompt holds a message for the end of the running turn or compaction. An idle
// chat is a 409 no_turn, the refusal a steer gives, so the client sends a prompt instead.
func cmdQueuePrompt(ctx context.Context, queue QueueAccess, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	row, err := queuedRow(cmd)
	if err != nil {
		return nil, err
	}
	appendRow := func(c *marotte.Chat) error { return appendUserRow(c, &row) }
	var live bool
	if len(row.Text) > marotte.MaxDraftBytes {
		live, err = appendPastDraftCap(ctx, queue, cmd.ChatID, appendRow)
	} else {
		live, err = queue.AppendIfLive(ctx, cmd.ChatID, appendRow)
	}
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

// appendPastDraftCap queues a row past the draft cap only behind a compaction with no turn
// live, where it stands for the prompt it would otherwise be. Behind a turn it is refused
// 413; an idle chat answers live=false, as for any row.
func appendPastDraftCap(ctx context.Context, queue QueueAccess, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	if queued, err := queue.AppendIfCompacting(ctx, chatID, fn); queued || err != nil {
		return queued, err
	}
	return queue.AppendIfLive(ctx, chatID, func(*marotte.Chat) error {
		return StatusError(http.StatusRequestEntityTooLarge, errQueueTooBig)
	})
}

// maxQueuedEncodedBytes bounds the queue's rows as the chat header encodes them. The header
// is capped at 8 MiB (internal/chat maxHeaderBytes) and must still hold a shutdown's carried
// row and the draft, each up to six times its text once JSON escapes it (\u003c for <).
const maxQueuedEncodedBytes = 4 << 20

// appendUserRow adds a user row to the queue: a repeated id is already there, and the caps
// leave room for the carried row a shutdown can add.
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
	rows := append(slices.Clip(c.QueuedPrompts), *row)
	if encoded, err := json.Marshal(rows); err != nil || len(encoded) > maxQueuedEncodedBytes {
		return StatusErrorReason(http.StatusConflict, reasonFull, errQueueFull)
	}
	c.QueuedPrompts = rows
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
// stop has its drain open this row, so the stop checks the row again before it is
// requested (stopChatTurn) and stops nothing once its turn opened.
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
	return stopChatTurn(ctx, roles.bridges, perms, terms, stops, roles.queue, cmd.ChatID, func() (func(), bool) {
		if roles.followups.Sending(cmd.ChatID, p.MessageID) {
			return nil, true
		}
		return func() {
			if err := roles.followups.ClearNext(ctx, cmd.ChatID, p.MessageID); err != nil {
				slog.Warn("send now: the follow-up stays marked next", "chat_id", cmd.ChatID, "message_id", p.MessageID, keyError, err)
			}
		}, false
	})
}

// markQueuedNext marks the row next and, with no turn running, sends it at once, unless
// a compaction runs: its end's drain sends the marked row first. stop reports a marked row
// whose turn the caller must stop.
func markQueuedNext(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, id string) (stop bool, err error) {
	// The send's intent begins here: a close that commits after this stamp refuses its reservation.
	stamp := roles.admission.LifeStamp()
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
	case roles.followups.Compacting(chatID):
		return false, nil
	}
	res, reserved := roles.admission.TryReserveIdleTurn(chatID, marotte.TurnSourcePrompt, stamp)
	if !reserved {
		return true, nil
	}
	row, ok, err := roles.followups.NextMarkedRow(ctx, chatID)
	if err != nil || !ok {
		roles.admission.ReleaseTurnReservation(res)
		if err != nil {
			return false, StatusError(http.StatusInternalServerError, err)
		}
		return false, nil
	}
	return false, launchQueuedRow(ctx, roles, res, TurnFence{}, &row)
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
	// AfterStop are rows queued with no turn live while kiro-cli ended the stopped run
	// or a compaction ran: one of them may go after this close although it was stopped
	// or broke (after any unread steers, which go first); the rest wait for the next
	// close's drain.
	AfterStop []string
	Fence     TurnFence
	// Unattended is a close no one stopped (marotte closing a turn KAS went idle without
	// ending), after which queued user rows still go, as after a clean close.
	Unattended bool
	// Tail names the close tail's hold on the chat's session, which TryReserveDrain ends; zero names none.
	Tail uint64
}

// drainAfterClose sends at most one prompt for a close, under the chat's steer lock from the pick
// to launchPrompt's return: the row marked Next first, after any close leaving a live bridge (the
// unread steers ride into its turn); then unread steers (after a close leaving a live bridge or a
// drained death), else one queued user row (after a clean or unattended close, or one of AfterStop,
// bridge live). A pending end, a held slot, a newer turn or a shutdown sends nothing, and
// nothing goes under a running compaction (drainOutsideCompaction).
func drainAfterClose(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, cl CloseFacts, ends EndFacts) {
	drainOutsideCompaction(ctx, roles, chatID, func() (Reservation, bool) {
		return roles.admission.TryReserveDrain(chatID, cl)
	}, func(res Reservation) { sendReserved(ctx, roles, res, cl, ends) })
}

// drainAfterCompaction sends at most one prompt once a compaction no close follows has
// ended, the first queued one whatever the compaction's outcome, picked as after a clean
// close; each later row goes at the next close.
func drainAfterCompaction(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) {
	stamp := roles.admission.LifeStamp()
	drainOutsideCompaction(ctx, roles, chatID, func() (Reservation, bool) {
		return roles.admission.TryReserveIdleTurn(chatID, marotte.TurnSourcePrompt, stamp)
	}, func(res Reservation) {
		sendReserved(ctx, roles, res, CloseFacts{Outcome: marotte.TurnOutcomeCompleted}, EndFacts{})
	})
}

// drainOutsideCompaction takes a drain's reservation under the chat's steer lock and hands it
// to send, never while a compaction runs. A reserved turn makes a later press queue behind it,
// so a compaction claimed before the reservation is the only one to fear: it is waited out,
// the reservation given back, and the pick repeated.
func drainOutsideCompaction(
	ctx context.Context, roles *promptRoles, chatID marotte.ChatID,
	reserve func() (Reservation, bool), send func(Reservation),
) {
	for {
		if roles.compactor != nil {
			roles.compactor.AwaitCompaction(ctx, chatID)
		}
		if !drainReserved(ctx, roles, chatID, reserve, send) {
			return
		}
	}
}

// drainReserved is one pick of drainOutsideCompaction, reporting a compaction that kept it
// from sending.
func drainReserved(
	ctx context.Context, roles *promptRoles, chatID marotte.ChatID,
	reserve func() (Reservation, bool), send func(Reservation),
) (compacting bool) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return false
	}
	defer unlock()
	if roles.lifecycle.Draining() || len(roles.jobs.Ends(chatID)) > 0 {
		return false
	}
	res, ok := reserve()
	if !ok {
		return false
	}
	if roles.followups != nil && roles.followups.Compacting(chatID) {
		roles.admission.ReleaseTurnReservation(res)
		return true
	}
	send(res)
	return false
}

// sendReserved is drainAfterClose's pick, on the reservation it took, which it owns from here.
func sendReserved(ctx context.Context, roles *promptRoles, res Reservation, cl CloseFacts, ends EndFacts) {
	chatID := res.Chat
	live := roles.bridges.BridgeLive(chatID)
	if live && roles.followups != nil && sendMarkedRow(ctx, roles, res, cl.Fence) {
		return
	}
	deathDrained := ends.Death && !ends.Undrained
	if live || deathDrained {
		if rows := roles.jobs.UnsentRows(chatID, deathDrained); len(rows) > 0 {
			sendUnread(ctx, roles, res, rows, cl.Fence)
			return
		}
	}
	clean := marotte.SeverityOf(cl.Outcome) == marotte.TurnSeverityClean || cl.Unattended
	var only []string
	if !clean {
		only = cl.AfterStop
	}
	if !live || (!clean && len(only) == 0) || roles.followups == nil {
		roles.admission.ReleaseTurnReservation(res)
		return
	}
	sendNextUserRow(ctx, roles, res, cl.Fence, only)
}

// sendMarkedRow launches the row marked Next, reporting whether it took the
// reservation's turn; with no such row, or one that cannot be read, the reservation
// stays the caller's.
func sendMarkedRow(ctx context.Context, roles *promptRoles, res Reservation, fence TurnFence) bool {
	chatID := res.Chat
	row, ok, err := roles.followups.NextMarkedRow(ctx, chatID)
	if err != nil {
		slog.Warn("the follow-up marked next could not be read; the drain goes on without it", "chat_id", chatID, keyError, err)
	}
	if err != nil || !ok {
		return false
	}
	if err := launchQueuedRow(ctx, roles, res, fence, &row); err != nil {
		logDrainRefused(chatID, row.ID, err)
	}
	return true
}

// No row, or one that cannot be read, releases it. only narrows the pick (NextUserRow).
func sendNextUserRow(ctx context.Context, roles *promptRoles, res Reservation, fence TurnFence, only []string) {
	chatID := res.Chat
	row, ok, err := roles.followups.NextUserRow(ctx, chatID, only)
	if err != nil || !ok {
		if err != nil {
			slog.Warn("queued prompts could not be read; they stay queued", "chat_id", chatID, keyError, err)
		}
		roles.admission.ReleaseTurnReservation(res)
		return
	}
	if err := launchQueuedRow(ctx, roles, res, fence, &row); err != nil {
		logDrainRefused(chatID, row.ID, err)
	}
}

// launchQueuedRow opens row's turn on the caller's reservation, which it owns from here
// (launchPrompt), and takes the row off the header once the turn is announced.
func launchQueuedRow(ctx context.Context, roles *promptRoles, res Reservation, fence TurnFence, row *marotte.QueuedPrompt) error {
	chatID := res.Chat
	p := &marotte.PromptCommand{
		Text: row.Text, MessageID: row.ID, DisplayText: row.Label, Attachments: row.Attachments, Caller: row.Caller,
	}
	if err := launchPrompt(ctx, roles, chatID, p, &launch{res: res, dequeue: row.ID, fence: fence}); err != nil {
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
func sendUnread(ctx context.Context, roles *promptRoles, res Reservation, rows []SteerRow, fence TurnFence) {
	chatID := res.Chat
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
	if err := launchPrompt(ctx, roles, chatID, p, &launch{res: res, resends: keys, fence: fence}); err != nil {
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
