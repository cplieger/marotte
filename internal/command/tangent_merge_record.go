package command

// A merge_tangent op's state is a marotte.TangentMerge on the tangent's chat record: admission
// writes it running before the summary turn opens, and it settles only once KAS holds the findings
// (a queued row, or the parent prompt's turn_bind) or the merge failed, so a restart forgets nothing.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
)

// The answers beyond the record's own states.
const (
	// mergeAdmitting: this process is admitting the op and has written no record yet.
	mergeAdmitting = "admitting"
	// mergeAbsent: no record, or a terminal one past mergeTerminalTTL.
	mergeAbsent = "absent"
)

// How long a terminal record answers: past the client's longest wait for a reply plus an SSE
// reconnect.
const mergeTerminalTTL = 10 * time.Minute

const mergeInterrupted = "The merge stopped before its findings reached the chat this tangent came from, so nothing was merged."

const mergeParentGone = "The chat this tangent came from no longer exists, so nothing was merged."

type mergeKey struct {
	tangent marotte.ChatID
	op      string
}

type mergeStatus struct {
	State        string         `json:"state"`
	ParentChatID marotte.ChatID `json:"parent_chat_id,omitempty"`
	Message      string         `json:"message,omitempty"`
}

// claims are the ops whose admission or background half runs in this process: liveness, never
// state, so a running record no claim covers was left by a process that ended without settling it,
// and the reader that finds it resumes it.
type tangentMerges struct {
	roles  *promptRoles
	mem    *Membership
	loader sessionLoader
	claims map[mergeKey]bool
	now    func() time.Time
	mu     sync.Mutex
}

func newTangentMerges(roles *promptRoles, mem *Membership, loader sessionLoader) *tangentMerges {
	return &tangentMerges{roles: roles, mem: mem, loader: loader, claims: make(map[mergeKey]bool), now: time.Now}
}

func (m *tangentMerges) claim(k mergeKey) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claims[k] {
		return false
	}
	m.claims[k] = true
	return true
}

func (m *tangentMerges) release(k mergeKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claims, k)
}

func (m *tangentMerges) claimed(k mergeKey) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claims[k]
}

func (m *tangentMerges) record(ctx context.Context, k mergeKey) (marotte.TangentMerge, bool) {
	c, ok := m.roles.chats.Get(ctx, k.tangent)
	if !ok {
		return marotte.TangentMerge{}, false
	}
	i := slices.IndexFunc(c.TangentMerges, func(r marotte.TangentMerge) bool { return r.OpID == k.op })
	if i < 0 || m.expired(&c.TangentMerges[i]) {
		return marotte.TangentMerge{}, false
	}
	return c.TangentMerges[i], true
}

func (m *tangentMerges) expired(r *marotte.TangentMerge) bool {
	return r.SettledAt != 0 && !m.now().Before(time.UnixMilli(r.SettledAt).Add(mergeTerminalTTL))
}

// Every write drops the expired records, which is what bounds a tangent's list. fn answers whether
// it changed anything.
func (m *tangentMerges) write(ctx context.Context, k mergeKey, fn func(recs []marotte.TangentMerge) ([]marotte.TangentMerge, bool)) error {
	_, err := m.roles.chats.Mutate(durable.Context(ctx), k.tangent, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		before := len(c.TangentMerges)
		c.TangentMerges = slices.DeleteFunc(c.TangentMerges, func(r marotte.TangentMerge) bool { return m.expired(&r) })
		var changed bool
		c.TangentMerges, changed = fn(c.TangentMerges)
		return changed || len(c.TangentMerges) != before
	})
	return err
}

func (m *tangentMerges) admit(ctx context.Context, k mergeKey, parent marotte.ChatID, deliveryID string) error {
	return m.write(ctx, k, func(recs []marotte.TangentMerge) ([]marotte.TangentMerge, bool) {
		return append(recs, marotte.TangentMerge{OpID: k.op, Parent: parent, DeliveryID: deliveryID, State: marotte.TangentMergeRunning}), true
	})
}

func (m *tangentMerges) abandon(ctx context.Context, k mergeKey) error {
	return m.write(ctx, k, func(recs []marotte.TangentMerge) ([]marotte.TangentMerge, bool) {
		n := len(recs)
		recs = slices.DeleteFunc(recs, func(r marotte.TangentMerge) bool { return r.OpID == k.op })
		return recs, len(recs) != n
	})
}

// settle never overwrites a terminal outcome; settled reports that this call committed one.
func (m *tangentMerges) settle(ctx context.Context, k mergeKey, state marotte.TangentMergeState, message string) (settled bool, err error) {
	err = m.write(ctx, k, func(recs []marotte.TangentMerge) ([]marotte.TangentMerge, bool) {
		i := slices.IndexFunc(recs, func(r marotte.TangentMerge) bool { return r.OpID == k.op })
		settled = i >= 0 && recs[i].State == marotte.TangentMergeRunning
		if settled {
			recs[i].State, recs[i].Message, recs[i].SettledAt = state, message, m.now().UnixMilli()
		}
		return recs, settled
	})
	return settled && err == nil, err
}

func (m *tangentMerges) status(ctx context.Context, k mergeKey) mergeStatus {
	rec, ok := m.record(ctx, k)
	switch {
	case !ok && m.claimed(k):
		return mergeStatus{State: mergeAdmitting}
	case !ok:
		return mergeStatus{State: mergeAbsent}
	case rec.State == marotte.TangentMergeRunning && m.claim(k):
		m.resume(ctx, k, &rec)
	}
	return mergeStatusOf(&rec)
}

// resume settles a running record an ended process left, holding k's claim (the caller's) until it
// has; its terminal frame reports the outcome.
func (m *tangentMerges) resume(ctx context.Context, k mergeKey, rec *marotte.TangentMerge) {
	parent, deliveryID := rec.Parent, rec.DeliveryID
	m.roles.lifecycle.InflightAdd(1)
	rctx, cancel := m.roles.lifecycle.TurnContext(ctx)
	go func() {
		defer m.roles.lifecycle.InflightDone()
		defer cancel()
		defer m.release(k)
		if failure, settled := m.reconcile(rctx, parent, deliveryID); settled {
			m.conclude(durable.Context(rctx), k, parent, failure)
		}
	}()
}

// reconcile decides only once the parent's log holds KAS's account of an interrupted turn, and
// leaves the record running while that is retryable; a deleted parent fails it. A queued row or a
// bound prompt was accepted; an unbound prompt is sent again under the same id; nothing under the id
// means the merge ended before delivering.
func (m *tangentMerges) reconcile(ctx context.Context, parent marotte.ChatID, deliveryID string) (failure string, settled bool) {
	if err := m.loader.LoadSession(ctx, parent); err != nil {
		if errors.Is(err, ErrChatGone) {
			return mergeParentGone, true
		}
		if ctx.Err() == nil {
			slog.Warn("tangent merge: the parent's session could not be reconciled, so an interrupted merge stays running", "parent", parent, keyError, err)
		}
		return "", false
	}
	if c, ok := m.roles.chats.Get(ctx, parent); ok && slices.ContainsFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return q.ID == deliveryID }) {
		return "", true
	}
	receipt, err := m.roles.chats.PromptReceipt(ctx, parent, deliveryID)
	switch {
	case errors.Is(err, chat.ErrTombstoned) || errors.Is(err, chat.ErrChatNotFound):
		// Deleted after its session was ready: the delete is the same terminal answer.
		return mergeParentGone, true
	case err != nil:
		slog.Warn("tangent merge: the parent's log could not be read to settle an interrupted merge", "parent", parent, keyError, err)
		return "", false
	case receipt.Bound:
		return "", true
	case receipt.Opened:
		return m.deliver(ctx, parent, &marotte.PromptCommand{Text: receipt.Text, MessageID: deliveryID, DisplayText: receipt.Label})
	}
	return mergeInterrupted, true
}

// deliver sends the findings under p.MessageID and answers "" once KAS holds them, or the failure
// the user reads; settled is false when ctx ended before either was known. The prompt is retried
// once when the parent's turn ends between the two attempts.
func (m *tangentMerges) deliver(ctx context.Context, parent marotte.ChatID, p *marotte.PromptCommand) (failure string, settled bool) {
	for range 2 {
		if m.roles.admission.TryReserveTurn(parent, marotte.TurnSourcePrompt) {
			turnID, err := startPrompt(ctx, m.roles, parent, p, launch{awaited: true})
			if err != nil {
				slog.Warn("tangent merge: the findings prompt could not open", "parent", parent, keyError, err)
				return deliveryFailure(""), true
			}
			return m.awaitReceipt(ctx, parent, turnID)
		}
		live, err := m.roles.followups.AppendIfLive(ctx, parent, func(c *marotte.Chat) error {
			return appendUserRow(c, &marotte.QueuedPrompt{ID: p.MessageID, Text: p.Text, Label: p.DisplayText})
		})
		if err != nil {
			slog.Warn("tangent merge: the findings could not be queued", "parent", parent, keyError, err)
			return deliveryFailure(""), true
		}
		if live {
			return "", true
		}
	}
	return deliveryFailure(""), true
}

// The caller's turn_bind is KAS's receipt: it lands after KAS accepts the message and before the
// model runs, so a bridge or prompt failure before it leaves the turn unbound.
func (m *tangentMerges) awaitReceipt(ctx context.Context, parent marotte.ChatID, turnID string) (failure string, settled bool) {
	defer m.roles.turnOutcome.ReleaseTurn(parent, turnID)
	bound, err := m.roles.turnOutcome.AwaitTurnBound(ctx, parent, turnID)
	switch {
	case ctx.Err() != nil:
		return "", false
	case err != nil:
		slog.Warn("tangent merge: the findings prompt's turn could not be awaited", "parent", parent, keyError, err)
		return deliveryFailure(""), true
	case bound:
		return "", true
	}
	result, err := m.roles.turnOutcome.AwaitTurn(ctx, parent, turnID)
	if err != nil {
		return deliveryFailure(""), true
	}
	return deliveryFailure(result.Reason), true
}

func deliveryFailure(reason string) string {
	const failed = "The summary could not be sent to the chat this tangent came from, so it is still in this tangent."
	if reason == "" {
		return failed
	}
	return failed + " " + reason
}

// conclude publishes only an outcome this call committed, so a device never retires a merge whose
// record still answers running; a failed write leaves it running for the next reader to resume. The
// parent's tab reopens only for a merge that landed.
func (m *tangentMerges) conclude(ctx context.Context, k mergeKey, parent marotte.ChatID, failure string) {
	state := marotte.TangentMergeSucceeded
	if failure != "" {
		state = marotte.TangentMergeFailed
	}
	settled, err := m.settle(ctx, k, state, failure)
	if err != nil {
		slog.Error("tangent merge: the outcome could not be recorded, so the merge stays running", "chat_id", k.tangent, "op_id", k.op, keyError, err)
	}
	if !settled {
		return
	}
	if failure == "" {
		if _, err := m.mem.OpenTab(ctx, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(parent)}, ""); err != nil {
			slog.Warn("tangent merge: the parent's tab could not be reopened", "parent", parent, keyError, err)
		}
	}
	if failure != "" {
		m.roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, k.tangent,
			marotte.ErrorPayload{Code: marotte.ErrCodeTangentMergeFailed, Message: failure, OpID: k.op}))
		return
	}
	slog.Info("tangent merged", "chat_id", k.tangent, "parent", parent)
	m.roles.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventTangentMerged, k.tangent,
		marotte.TangentMergedPayload{ParentChatID: string(parent), OpID: k.op}))
}

func mergeStatusOf(r *marotte.TangentMerge) mergeStatus {
	return mergeStatus{State: string(r.State), ParentChatID: r.Parent, Message: r.Message}
}

// ServeMergeStatus answers GET /api/chats/{id}/merges/{op}: where the merge_tangent attempt with
// that op_id on that tangent stands, "absent" when none was admitted or its outcome expired.
func (d *Dispatcher) ServeMergeStatus(w http.ResponseWriter, r *http.Request) {
	id, op := r.PathValue("id"), r.PathValue("op")
	if !ids.ValidChatID(id) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	if !validIdent(op) || op == "" {
		httpreply.BadRequest(w, "invalid op_id")
		return
	}
	webhttp.WriteJSON(w, d.merges.status(r.Context(), mergeKey{tangent: marotte.ChatID(id), op: op}))
}
