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
	// ambiguous marks a notice sharing its label with one already read: no read can name its run any more.
	ambiguous bool
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
// so the words are the only join. A text no label matches names no run. One several notices match (two launches of
// one workflow) names none either, since any pick could stamp another run's provenance: it consumes the first and
// marks the rest ambiguous, each still owed its own read or clear. translate.runOriginAccess.
func (rs *Runs) RunNotice(chatID marotte.ChatID, text string) (workflowID string, producedTs int64, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	q := slices.Clone(rs.notices[chatID])
	quoted := func(n runNotice) bool { return n.label != "" && strings.Contains(text, `"`+n.label+`"`) }
	i := slices.IndexFunc(q, quoted)
	if i < 0 {
		return "", 0, false
	}
	n := q[i]
	q = slices.Delete(q, i, i+1)
	ambiguous := n.ambiguous
	for j := range q {
		if quoted(q[j]) {
			q[j].ambiguous, ambiguous = true, true
		}
	}
	rs.dropNoticesLocked(chatID, q)
	if ambiguous {
		return "", 0, false
	}
	return n.workflowID, n.producedTs, true
}

// ForgetRunNotices drops n of the chat's notices for a clear naming that many: it carries no words to join them by,
// and a kept notice would hold clearStaleNotices open. Ambiguous notices go first, since no read can name their run
// anyway, then the oldest.
func (rs *Runs) ForgetRunNotices(chatID marotte.ChatID, n int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	q := slices.Clone(rs.notices[chatID])
	q = slices.DeleteFunc(q, func(rn runNotice) bool {
		if n > 0 && rn.ambiguous {
			n--
			return true
		}
		return false
	})
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
