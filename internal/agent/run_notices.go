package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runNotice is one finished chat-parented run whose completion notice KAS queued into the launching chat's steering buffer.
type runNotice struct {
	workflowID string
	producedTs int64
}

// maxRunNoticesPerChat bounds the queue against notices KAS never delivers.
const maxRunNoticesPerChat = 16

// recordRunNotice queues a terminal run behind the notice KAS is about to append, only for a real chat
// whose turn is live: KAS prompts an idle parent instead. This mirrors KAS's hasActiveExecution.
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

// clearStaleNotices closes KAS's gate: KAS skips its turn-end clear when a turn ends abnormally, leaving
// a notice to ambush the next prompt. On a turn close with a notice held and the chat settled, it sends
// `_session/steer/clear` on the chat's bridge; that also drops unread user steers.
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

// RunNotice consumes the chat's oldest notice: KAS drains in append order and finishes are recorded in
// order, so the queues pair FIFO. translate.runOriginAccess.
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
