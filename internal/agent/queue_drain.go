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
// (lifecycle then store), waiting out a finalizing turn: the close sees the row or the caller sees no turn. A row
// appended under a hold with no turn live is owed a send despite the stop; kasExec.holdRows owns which drain takes it.
func (bc *bridgeCoordinator) AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	live := false
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		turnLive := lc.liveLockedState()
		if !turnLive && !lc.kas.may().holdRows {
			return nil
		}
		live = true
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
	})
	return live, err
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
	for _, r := range rows {
		texts = append(texts, r.Text)
		keys = append(keys, r.Key)
	}
	row := marotte.CarriedRow("m-"+newMessageID(), texts, keys)
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
func (bc *bridgeCoordinator) settleAfterClose(ctx context.Context, chatID marotte.ChatID, budget time.Duration) bool {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return true
	}
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
