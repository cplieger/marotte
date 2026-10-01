package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runNotice is one finished chat-parented run whose completion notice KAS queued
// into the launching chat's steering buffer instead of prompting it.
type runNotice struct {
	workflowID string
	producedTs int64
}

// maxRunNoticesPerChat bounds the queue: a notice KAS never delivers (a buffer
// cleared by a boundary this process did not see) must not grow it forever.
const maxRunNoticesPerChat = 16

// recordRunNotice queues a terminal run behind the notice KAS is about to append.
//
// Recorded only for a run parented on a real chat whose turn is LIVE: KAS PROMPTS an
// idle parent instead of queueing, so a finish recorded while idle would pair with
// the next unrelated notice. The registry is this process's reading of what KAS
// tests with hasActiveExecution; a disagreement costs one notice its provenance.
func (rs *Runs) recordRunNotice(chatID marotte.ChatID, workflowID string) {
	if workflowIDOf(chatID) != "" || rs.coord == nil || !rs.coord.turns.live(chatID) {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.notices == nil {
		rs.notices = map[marotte.ChatID][]runNotice{}
	}
	q := rs.notices[chatID]
	q = append(q, runNotice{workflowID: workflowID, producedTs: time.Now().UnixMilli()})
	if len(q) > maxRunNoticesPerChat {
		q = q[len(q)-maxRunNoticesPerChat:]
	}
	rs.notices[chatID] = q
}

// clearStaleNotices closes KAS's own gate. Its turn-end clear is skipped when a turn
// ends abnormally, so a notice queued during that turn waits in the buffer until the
// reader's NEXT prompt drains it and an hours-old result lands as news. Run when a
// chat's turn closes: while this process still holds a notice for the chat (a finish
// neither read nor dropped) and the chat has settled, `_session/steer/clear` goes out
// on the chat's own bridge. The steering_cleared frame KAS answers with is what writes
// the dropped entries, provenance included. Session-scoped, so an unread user steer
// queued in the same turn is dropped with it, as at every other boundary.
func (rs *Runs) clearStaleNotices(ctx context.Context, chatID marotte.ChatID) {
	if !rs.holdsNotice(chatID) || rs.coord.turns.live(chatID) {
		return
	}
	sb := rs.bridges.get(chatID)
	if sb == nil {
		return
	}
	params := map[string]any{marotte.KeySessionID: sb.SessionID()}
	if _, err := sb.Call(ctx, marotte.MethodSessionSteerClear, params); err != nil {
		slog.Warn("run notice: boundary clear failed; the notice stays queued for the next prompt", "chat_id", chatID, "error", err)
	}
}

func (rs *Runs) holdsNotice(chatID marotte.ChatID) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.notices[chatID]) > 0
}

// RunNotice consumes the oldest queued notice for a chat: KAS drains its steering
// buffer in append order, and observeComplete records in finish order, so the two
// queues pair FIFO. translate.RunOriginAccess.
func (rs *Runs) RunNotice(chatID marotte.ChatID) (workflowID string, producedTs int64, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	q := rs.notices[chatID]
	if len(q) == 0 {
		return "", 0, false
	}
	if len(q) == 1 {
		delete(rs.notices, chatID)
	} else {
		rs.notices[chatID] = q[1:]
	}
	return q[0].workflowID, q[0].producedTs, true
}
