package agent

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runNotice is one finished chat-parented run whose completion notice KAS queued into the launching chat's steering
// buffer. label is the name the notice quotes (autoWakeParentOnComplete: runLabel, else workflowName, else the id).
type runNotice struct {
	workflowID string
	label      string
	producedTs int64
}

// maxRunNoticesPerChat bounds the queue against notices KAS never delivers.
const maxRunNoticesPerChat = 16

// recordRunNotice queues a terminal run behind the notice KAS is about to append, only for a real chat
// whose turn is live: KAS prompts an idle parent instead. This mirrors KAS's hasActiveExecution.
func (rs *Runs) recordRunNotice(chatID marotte.ChatID, workflowID, label string) {
	if workflowIDOf(chatID) != "" || rs.coord == nil || !rs.coord.turns.live(chatID) {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.notices == nil {
		rs.notices = map[marotte.ChatID][]runNotice{}
	}
	q := rs.notices[chatID]
	q = append(q, runNotice{workflowID: workflowID, label: label, producedTs: time.Now().UnixMilli()})
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

// RunNotice consumes the chat's notice whose label the read notice's text quotes: KAS mints the notice id at random,
// so the words are the only join, and a notice no label matches names no run. translate.runOriginAccess.
func (rs *Runs) RunNotice(chatID marotte.ChatID, text string) (workflowID string, producedTs int64, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	q := rs.notices[chatID]
	for i, n := range q {
		if n.label == "" || !strings.Contains(text, `"`+n.label+`"`) {
			continue
		}
		rs.dropNoticesLocked(chatID, slices.Delete(q, i, i+1))
		return n.workflowID, n.producedTs, true
	}
	return "", 0, false
}

// ForgetRunNotices drops n of the chat's notices, oldest first, for a clear naming that many: it carries no words to
// join them by, and a kept notice would hold clearStaleNotices open.
func (rs *Runs) ForgetRunNotices(chatID marotte.ChatID, n int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	q := rs.notices[chatID]
	rs.dropNoticesLocked(chatID, q[min(n, len(q)):])
}

// Callers hold rs.mu.
func (rs *Runs) dropNoticesLocked(chatID marotte.ChatID, left []runNotice) {
	if len(left) == 0 {
		delete(rs.notices, chatID)
		return
	}
	rs.notices[chatID] = left
}
