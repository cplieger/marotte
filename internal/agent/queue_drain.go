package agent

// The follow-up queue's server half. Chat.QueuedPrompts holds the rows, so every device draws the
// same list and one owner sends it.

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

var errQueueChatGone = errors.New("agent: the chat has no record to queue on")

// AppendIfLive applies fn to the record while the chat holds a live turn, a KAS hold or a running compaction, in the
// lifecycle section (lifecycle then store), waiting out a finalizing turn: the close or the compaction's end sees the
// row, or the caller sees nothing to queue behind. A life that ended or a close being decided appends nothing, so a
// close's impact read sees every row it stops. A row appended with no turn live is owed a send despite a stop or a
// broken close: kasExec.holdRows owns which drain takes one appended under a hold, compactCall.owed one appended
// behind a compaction. Unless that drain sends the queue's head (compactCall.head), the row is placed ahead of the
// rows the close paused (placeOwed), so the queue lists rows in the order they go.
func (bc *bridgeCoordinator) AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	return bc.appendBehind(ctx, chatID, false, fn)
}

// AppendIfCompacting is AppendIfLive for a prompt: only while a compaction runs with no turn live, since a live turn
// refuses the prompt at admission instead.
func (bc *bridgeCoordinator) AppendIfCompacting(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	return bc.appendBehind(ctx, chatID, true, fn)
}

// Compacting reports a compaction running on the chat.
func (bc *bridgeCoordinator) Compacting(chatID marotte.ChatID) bool {
	return bc.autoCompact.compacting(chatID)
}

func (bc *bridgeCoordinator) appendBehind(
	ctx context.Context, chatID marotte.ChatID, onlyCompacting bool, fn func(c *marotte.Chat) error,
) (bool, error) {
	live := false
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if v, _ := bc.turns.gates.admit(chatID, lc.stamp); lc.ended || v != gateAdmitted {
			return nil
		}
		b := bc.behindLocked(lc)
		switch {
		case onlyCompacting && (b.turnLive || !b.compacting):
			return nil
		case !b.turnLive && !b.holds && !b.compacting:
			return nil
		}
		live = true
		keep, place := bc.owedWrites(lc, b)
		return bc.appendLocked(ctx, chatID, keep, place, fn)
	})
	if errors.Is(err, errNoLife) {
		return false, nil
	}
	return live, err
}

// queueBehind is what a row appended in the lifecycle section queues behind.
type queueBehind struct {
	// owed are the rows already owed to the running compaction (compactCall.owed).
	owed []string
	// head is the running compaction's compactCall.head.
	turnLive, holds, compacting, head bool
}

func (bc *bridgeCoordinator) behindLocked(lc *chatLifecycle) queueBehind {
	compacting, head, owed := bc.autoCompact.behind(lc.chatID)
	return queueBehind{
		owed: owed, turnLive: lc.liveLockedState(), holds: lc.kas.may().holdRows, compacting: compacting, head: head,
	}
}

// owedWrites is how appendLocked records rows appended with no turn live: keep hands them to the drain they are owed
// to, place moves them ahead of the rows a broken close paused. Both nil while a turn is live.
func (bc *bridgeCoordinator) owedWrites(lc *chatLifecycle, b queueBehind) (keep func([]string), place func(*marotte.Chat, []string)) {
	chatID := lc.chatID
	switch {
	case b.turnLive:
		return nil, nil
	case b.holds:
		keep = func(added []string) {
			lc.kas.keepHoldRows(added)
			if b.compacting {
				bc.autoCompact.owe(chatID, nil)
			}
		}
	default:
		keep = func(added []string) { bc.autoCompact.owe(chatID, added) }
	}
	if b.compacting && b.head {
		return keep, nil
	}
	owed := slices.Concat(lc.kas.holdRows, b.owed)
	return keep, func(c *marotte.Chat, added []string) {
		bc.placeOwed(chatID, c, added, owed)
		// In the row's own write, so no page reads the moved row under a compaction that sends none.
		if p := c.CompactPending; b.compacting && p != nil && p.Running {
			p.Sends = true
		}
	}
}

// appendLocked is AppendIfLive's write, in the lifecycle section: fn on the record, then place on the rows it added in
// the same write, and those rows handed to keep, which records them for the drain they are owed to; nil skips either.
func (bc *bridgeCoordinator) appendLocked(
	ctx context.Context, chatID marotte.ChatID,
	keep func(added []string), place func(c *marotte.Chat, added []string), fn func(c *marotte.Chat) error,
) error {
	var fnErr error
	var added []string
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			fnErr = errQueueChatGone
			return false
		}
		added, fnErr = appendedRows(c, fn)
		if fnErr == nil && len(added) > 0 && place != nil {
			place(c, added)
		}
		return fnErr == nil && len(added) > 0
	}); err != nil {
		return err
	}
	if fnErr == nil && keep != nil {
		keep(added)
	}
	return fnErr
}

// placeOwed moves the rows just added, which a broken close owes a send, ahead of the rows it paused: after the row
// marked next and the rows owed before them, every order kept, as MarkNext moves a row.
func (bc *bridgeCoordinator) placeOwed(chatID marotte.ChatID, c *marotte.Chat, added, owed []string) {
	moved := make([]marotte.QueuedPrompt, 0, len(added))
	rest := make([]marotte.QueuedPrompt, 0, len(c.QueuedPrompts))
	for i := range c.QueuedPrompts {
		if slices.Contains(added, c.QueuedPrompts[i].ID) {
			moved = append(moved, c.QueuedPrompts[i])
		} else {
			rest = append(rest, c.QueuedPrompts[i])
		}
	}
	at := slices.IndexFunc(rest, func(q marotte.QueuedPrompt) bool {
		return bc.sendable(chatID, &q) && !q.Next && !slices.Contains(owed, q.ID)
	})
	if at < 0 {
		at = len(rest)
	}
	c.QueuedPrompts = slices.Insert(rest, at, moved...)
}

// sendable reports a queued row a drain may send: not held or carried, and its turn not opened by this process.
func (bc *bridgeCoordinator) sendable(chatID marotte.ChatID, q *marotte.QueuedPrompt) bool {
	return !q.Held && !q.Carried() && !bc.chatStore.Opened(chatID, q.ID)
}

// appendedRows applies fn to c and answers the ids of the queued rows it added.
func appendedRows(c *marotte.Chat, fn func(c *marotte.Chat) error) ([]string, error) {
	before := make(map[string]struct{}, len(c.QueuedPrompts))
	for i := range c.QueuedPrompts {
		before[c.QueuedPrompts[i].ID] = struct{}{}
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	var added []string
	for i := range c.QueuedPrompts {
		if _, old := before[c.QueuedPrompts[i].ID]; !old {
			added = append(added, c.QueuedPrompts[i].ID)
		}
	}
	return added, nil
}

var _ command.QueueAccess = (*bridgeCoordinator)(nil)

// NextUserRow answers the first unheld queued user row whose turn this process has not opened, among only when
// non-nil, via the mutate path so a pending removal applies first.
func (bc *bridgeCoordinator) NextUserRow(ctx context.Context, chatID marotte.ChatID, only []string) (marotte.QueuedPrompt, bool, error) {
	var row marotte.QueuedPrompt
	found := false
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := range c.QueuedPrompts {
			q := &c.QueuedPrompts[i]
			if !q.Held && !q.Carried() && !bc.chatStore.Opened(chatID, q.ID) && (only == nil || slices.Contains(only, q.ID)) {
				row, found = *q, true
				break
			}
		}
		return false
	})
	return row, found, err
}

// NextMarkedRow answers the row MarkNext chose while it is unheld and its turn has not opened.
func (bc *bridgeCoordinator) NextMarkedRow(ctx context.Context, chatID marotte.ChatID) (marotte.QueuedPrompt, bool, error) {
	var row marotte.QueuedPrompt
	found := false
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := range c.QueuedPrompts {
			q := &c.QueuedPrompts[i]
			if q.Next && !q.Held && !q.Carried() && !bc.chatStore.Opened(chatID, q.ID) {
				row, found = *q, true
				break
			}
		}
		return false
	})
	return row, found, err
}

// MarkNext marks the row with id as the next to send and moves it ahead of every other sendable
// row, which keep their order. An id whose turn this process opened answers sending; a held or
// carried row answers held and is left as it is. The caller holds the steer lock.
func (bc *bridgeCoordinator) MarkNext(ctx context.Context, chatID marotte.ChatID, id string) (found, sending, held bool, err error) {
	if bc.chatStore.Opened(chatID, id) {
		return true, true, false, nil
	}
	_, err = bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		at := -1
		if exists {
			at = slices.IndexFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return q.ID == id })
		}
		if at < 0 {
			return false
		}
		found = true
		row := c.QueuedPrompts[at]
		if !bc.sendable(chatID, &row) {
			held = true
			return false
		}
		rest := slices.Delete(c.QueuedPrompts, at, at+1)
		for i := range rest {
			rest[i].Next = false
		}
		head := slices.IndexFunc(rest, func(q marotte.QueuedPrompt) bool { return bc.sendable(chatID, &q) })
		if head < 0 {
			head = len(rest)
		}
		row.Next = true
		c.QueuedPrompts = slices.Insert(rest, head, row)
		return true
	})
	return found, sending, held, err
}

// Sending reports whether this process opened the turn of the queued row id.
func (bc *bridgeCoordinator) Sending(chatID marotte.ChatID, id string) bool {
	return bc.chatStore.Opened(chatID, id)
}

// ClearNext drops the mark MarkNext set on the row with id, leaving the order as it is.
func (bc *bridgeCoordinator) ClearNext(ctx context.Context, chatID marotte.ChatID, id string) error {
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := range c.QueuedPrompts {
			if q := &c.QueuedPrompts[i]; q.ID == id && q.Next {
				q.Next = false
				return true
			}
		}
		return false
	})
	return err
}

// queuedPromptsPending is the ids of chatID's queued rows whose turn this process has not opened, read from the
// cached header: the rows its page shows waiting.
func (bc *bridgeCoordinator) queuedPromptsPending(ctx context.Context, chatID marotte.ChatID) []string {
	c, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return nil
	}
	var ids []string
	for i := range c.QueuedPrompts {
		if id := c.QueuedPrompts[i].ID; !bc.chatStore.Opened(chatID, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// Dequeue takes a sent row off the header, after its turn_opened frame.
func (bc *bridgeCoordinator) Dequeue(ctx context.Context, chatID marotte.ChatID, id string) error {
	return bc.chatStore.Dequeue(durable.Context(ctx), chatID, id)
}

// Unqueue removes one queued row; an id whose turn this process opened answers sending. The caller
// holds the steer lock.
func (bc *bridgeCoordinator) Unqueue(ctx context.Context, chatID marotte.ChatID, id string) (bool, error) {
	if bc.chatStore.Opened(chatID, id) {
		return true, nil
	}
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		n := len(c.QueuedPrompts)
		c.QueuedPrompts = slices.DeleteFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return q.ID == id })
		return len(c.QueuedPrompts) != n
	})
	return false, err
}

// holdUnread joins the unread steers a shutdown took into the chat's Held row (created if absent),
// which the next process shows but never sends. A failed write is logged: each steer's restart entry keeps its words.
func (bc *bridgeCoordinator) holdUnread(ctx context.Context, chatID marotte.ChatID, rows []shutdownRow) {
	if len(rows) == 0 {
		return
	}
	texts := make([]string, 0, len(rows))
	keys := make([]string, 0, len(rows))
	callers := make([]*marotte.Caller, 0, len(rows))
	for _, r := range rows {
		texts = append(texts, r.Text)
		keys = append(keys, r.Key)
		callers = append(callers, r.Caller)
	}
	row := marotte.CarriedRow("m-"+newMessageID(), texts, keys)
	row.Caller = marotte.SharedCaller(callers)
	row.Held = true
	if _, err := bc.chatStore.Mutate(durable.Context(ctx), chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		if i := slices.IndexFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return q.Carried() }); i >= 0 {
			c.QueuedPrompts[i].Absorb(&row)
			c.QueuedPrompts[i].Held = true
			return true
		}
		at := 0
		for at < len(c.QueuedPrompts) && c.QueuedPrompts[at].Carried() {
			at++
		}
		c.QueuedPrompts = slices.Insert(c.QueuedPrompts, at, row)
		return true
	}); err != nil {
		slog.Error("shutdown: unread steers could not be held on the chat; their restart entries keep the words",
			"chat_id", chatID, "steers", len(rows), "error", err)
	}
}

// settleAfterClose waits for the chat to idle after a close; a bare reservation is usually the
// closing prompt's. False once a turn opens, or on budget or ctx expiry.
func (lc *chatLifecycle) settleAfterClose(ctx context.Context, budget time.Duration) bool {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		lc.mu.Lock()
		idle, opened, changed := !lc.liveLockedState(), lc.own != nil || lc.pending != nil, lc.changed
		lc.mu.Unlock()
		switch {
		case idle:
			return true
		case opened:
			return false
		}
		select {
		case <-changed:
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}
