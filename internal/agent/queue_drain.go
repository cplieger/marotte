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

// AppendIfLive applies fn to the record while the chat holds a live turn or a KAS hold, in the lifecycle section
// (lifecycle then store), waiting out a finalizing turn: the close sees the row or the caller sees no turn. A life
// that ended or a close being decided appends nothing, so a close's impact read sees every row it stops. A row
// appended under a hold with no turn live is owed a send despite the stop; kasExec.holdRows owns which drain takes it.
func (bc *bridgeCoordinator) AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	live := false
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if v, _ := bc.turns.gates.admit(chatID, lc.stamp); lc.ended || v != gateAdmitted {
			return nil
		}
		turnLive := lc.liveLockedState()
		if !turnLive && !lc.kas.may().holdRows {
			return nil
		}
		live = true
		return bc.appendLocked(ctx, lc, chatID, turnLive, fn)
	})
	if errors.Is(err, errNoLife) {
		return false, nil
	}
	return live, err
}

// appendLocked is AppendIfLive's write, under lc.mu: fn on the record, and the rows it added kept for the KAS hold's
// drain when no turn is live.
func (bc *bridgeCoordinator) appendLocked(
	ctx context.Context, lc *chatLifecycle, chatID marotte.ChatID, turnLive bool, fn func(c *marotte.Chat) error,
) error {
	var fnErr error
	var added []string
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			fnErr = errQueueChatGone
			return false
		}
		added, fnErr = appendedRows(c, fn)
		return fnErr == nil && len(added) > 0
	}); err != nil {
		return err
	}
	if fnErr == nil && !turnLive {
		lc.kas.keepHoldRows(added)
	}
	return fnErr
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
