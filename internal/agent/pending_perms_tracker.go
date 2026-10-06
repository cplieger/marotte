package agent

import (
	"cmp"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// pendingPermsTracker tracks unresolved permission_needed events by chat and request id, replayed
// on every SSE connection. Deliberately no TTL: the ACP request stays open until answered or
// cancelled. The Take and Clear paths bound growth.
type pendingPermsTracker struct {
	perms map[permKey]marotte.ServerEvent
	// versions holds the shared `pending` counter every mutation bumps under mu (mintPending).
	versions *subject.Versions
	mu       sync.Mutex
}

// permKey is the chat plus the ACP request id, never the id alone: each bridge mints ids from
// zero. Every bridge replacement or death first drops that chat's entries, so no generation is needed.
type permKey struct {
	chat marotte.ChatID
	id   int64
}

func newPendingPermsTracker() *pendingPermsTracker {
	return &pendingPermsTracker{perms: make(map[permKey]marotte.ServerEvent)}
}

// Add records a permission_needed event under the event's own chat id.
func (t *pendingPermsTracker) Add(id int64, evt marotte.ServerEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.perms[permKey{chat: evt.ChatID, id: id}] = evt
	mintPending(&t.versions)
}

// TakeIfPresent claims one chat's request, deleting and returning it, false when someone else
// answered. One lock spans lookup and delete, so two tabs or a human racing the unattended floor
// cannot both answer.
func (t *pendingPermsTracker) TakeIfPresent(chatID marotte.ChatID, id int64) (marotte.ServerEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := permKey{chat: chatID, id: id}
	evt, ok := t.perms[k]
	if !ok {
		return marotte.ServerEvent{}, false
	}
	delete(t.perms, k)
	mintPending(&t.versions)
	return evt, true
}

// TakePermissionOption validates one advertised option and claims the request
// in the same critical section. An off-list answer leaves the request pending.
func (t *pendingPermsTracker) TakePermissionOption(chatID marotte.ChatID, id int64, optionID string) (evt marotte.ServerEvent, pending, offered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := permKey{chat: chatID, id: id}
	evt, pending = t.perms[k]
	if !pending {
		return marotte.ServerEvent{}, false, false
	}
	payload, ok := evt.Payload.(marotte.PermissionNeededPayload)
	if !ok || !slices.ContainsFunc(payload.Options, func(option marotte.PermissionOption) bool {
		return option.OptionID == optionID
	}) {
		return evt, true, false
	}
	delete(t.perms, k)
	mintPending(&t.versions)
	return evt, true, true
}

// TakeForToolCall claims the chat's pending ask for toolCallID; should two be outstanding, the newest request id is the awaited one.
func (t *pendingPermsTracker) TakeForToolCall(chatID marotte.ChatID, toolCallID string) (marotte.ServerEvent, int64, bool) {
	if toolCallID == "" {
		return marotte.ServerEvent{}, 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var found permKey
	ok := false
	for k, evt := range t.perms {
		if k.chat != chatID || decisionToolCallID(evt.Payload) != toolCallID {
			continue
		}
		if !ok || k.id > found.id {
			found, ok = k, true
		}
	}
	if !ok {
		return marotte.ServerEvent{}, 0, false
	}
	evt := t.perms[found]
	delete(t.perms, found)
	mintPending(&t.versions)
	return evt, found.id, true
}

// decisionToolCallID is the tool call a tracked decision belongs to, "" for none.
func decisionToolCallID(payload any) string {
	switch p := payload.(type) {
	case marotte.PermissionNeededPayload:
		return p.ToolCallID
	case marotte.UserInputNeededPayload:
		return p.ToolCallID
	}
	return ""
}

// ClearForChat drops every unresolved permission_needed entry owned by chatID.
func (t *pendingPermsTracker) ClearForChat(chatID marotte.ChatID) {
	if chatID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := false
	for k := range t.perms {
		if k.chat == chatID {
			delete(t.perms, k)
			removed = true
		}
	}
	if removed {
		mintPending(&t.versions)
	}
}

// ClearForRun drops every unresolved decision a run raised, wherever filed, so the connect replay
// stops offering a dead step's card. The run comes off the payload; an empty id is refused. No announcement.
func (t *pendingPermsTracker) ClearForRun(workflowID string) {
	if workflowID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := false
	for k, evt := range t.perms {
		if marotte.DecisionRunID(evt.Payload) == workflowID {
			delete(t.perms, k)
			removed = true
		}
	}
	if removed {
		mintPending(&t.versions)
	}
}

// OpenNodesForRun is the node id of every unresolved decision a run raised; nil for an empty
// workflowID; a decision naming no node is skipped.
func (t *pendingPermsTracker) OpenNodesForRun(workflowID string) map[string]struct{} {
	if workflowID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out map[string]struct{}
	for _, evt := range t.perms {
		if marotte.DecisionRunID(evt.Payload) != workflowID {
			continue
		}
		node := marotte.DecisionNodeID(evt.Payload)
		if node == "" {
			continue
		}
		if out == nil {
			out = make(map[string]struct{})
		}
		out[node] = struct{}{}
	}
	return out
}

// List snapshots the unresolved decisions, optionally for one chat: exactly what TakeIfPresent
// still accepts. Order is the contract: ascending request id, ties broken by chat.
func (t *pendingPermsTracker) List(chatFilter marotte.ChatID) []marotte.ServerEvent {
	t.mu.Lock()
	keys := make([]permKey, 0, len(t.perms))
	for k := range t.perms {
		if chatFilter != "" && k.chat != "" && k.chat != chatFilter {
			continue
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b permKey) int {
		return cmp.Or(cmp.Compare(a.id, b.id), cmp.Compare(a.chat, b.chat))
	})
	result := make([]marotte.ServerEvent, 0, len(keys))
	for _, k := range keys {
		result = append(result, t.perms[k])
	}
	t.mu.Unlock()
	return result
}

// ClearPendingPermsForChat drops every unresolved decision owned by chatID.
func (b *bus) ClearPendingPermsForChat(chatID marotte.ChatID) {
	b.pendingPerms.ClearForChat(chatID)
}

// ClearPendingPermsForRun drops a run's unresolved decisions at its terminal transition, silently (ClearForRun).
func (b *bus) ClearPendingPermsForRun(workflowID string) {
	b.pendingPerms.ClearForRun(workflowID)
}

// PendingDecisionNodesForRun is the node id of every decision a run's steps still owe.
func (b *bus) PendingDecisionNodesForRun(workflowID string) map[string]struct{} {
	return b.pendingPerms.OpenNodesForRun(workflowID)
}

// TakePendingPerm claims an unanswered decision for exactly one surface, false when beaten, and
// announces decision_settled. Takes the chat because ids are per bridge (permKey). Take first,
// then answer kiro-cli: a loser must send nothing.
func (b *bus) TakePendingPerm(chatID marotte.ChatID, requestID int64, settledBy marotte.SettledBy) bool {
	evt, ok := b.pendingPerms.TakeIfPresent(chatID, requestID)
	if !ok {
		return false
	}
	b.announceDecisionSettled(evt, requestID, settledBy)
	return true
}

// PendingPermsWithdraw retires the ask KAS withdrew for toolCallID and announces it moot.
func (b *bus) PendingPermsWithdraw(chatID marotte.ChatID, toolCallID string) bool {
	evt, requestID, ok := b.pendingPerms.TakeForToolCall(chatID, toolCallID)
	if !ok {
		return false
	}
	b.announceDecisionSettled(evt, requestID, marotte.SettledByMoot)
	return true
}

// TakePendingPermissionOption validates and claims a permission response.
func (b *bus) TakePendingPermissionOption(chatID marotte.ChatID, requestID int64, optionID string, settledBy marotte.SettledBy) (pending, offered bool) {
	evt, pending, offered := b.pendingPerms.TakePermissionOption(chatID, requestID, optionID)
	if offered {
		b.announceDecisionSettled(evt, requestID, settledBy)
	}
	return pending, offered
}

// announceDecisionSettled retires a claimed decision on every other surface.
func (b *bus) announceDecisionSettled(evt marotte.ServerEvent, requestID int64, settledBy marotte.SettledBy) {
	kind, known := marotte.DecisionKindForEvent(evt.Type)
	if !known {
		// Only the three *_needed events are tracked, so this is misuse; the claim stands.
		slog.Error("sse: tracked decision has no kind, cannot announce it",
			"type", evt.Type, "request_id", requestID)
		return
	}
	b.emit(marotte.NewEvent(marotte.EventDecisionSettled, evt.ChatID, marotte.DecisionSettledPayload{
		RequestID: requestID,
		Kind:      kind,
		SettledBy: settledBy,
	}))
	b.retractPush(marotte.ChatSubject(evt.ChatID))
}
