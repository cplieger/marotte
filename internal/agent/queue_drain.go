package agent

// The follow-up rows' server half beyond the run list: the close confirmation's read of the rows a close stops, and
// the shutdown's carried row of unread steers.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// queuedPromptsPending is the ids of chatID's rows waiting on its run list, read from the cached header: the rows
// its page shows waiting, which a close stops.
func (bc *bridgeCoordinator) queuedPromptsPending(ctx context.Context, chatID marotte.ChatID) []string {
	c, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return nil
	}
	var ids []string
	for i := range c.Run {
		e := &c.Run[i]
		queued := e.Kind == marotte.RunKindRow || e.Kind == marotte.RunKindUnread || e.Kind == marotte.RunKindRetry
		if queued && e.Turn == "" && (!e.State.Live() || e.State == marotte.RunStarting) {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// holdUnread joins the unread steers a shutdown took into the chat's carried row (created if absent, held), which
// the next process shows but never sends (runList.holdCarried). A failed write is logged: each steer's restart entry
// keeps its words.
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
	if err := bc.holdCarried(ctx, chatID, &row); err != nil {
		slog.Error("shutdown: unread steers could not be held on the chat; their restart entries keep the words",
			"chat_id", chatID, "steers", len(rows), "error", err)
	}
}
