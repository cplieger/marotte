package agent

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/subject"
)

// pendingPermsTracker tracks unresolved asks, replayed on every SSE connection, each under an ask id
// it mints: the wire's request_id. Never the ACP request id, which every bridge mints from zero, so
// a successor's ask would replace an old bridge's still on screen. Deliberately no TTL: the ACP
// request stays open until answered, cancelled or its bridge ends.
type pendingPermsTracker struct {
	asks map[int64]*askEntry
	// versions holds the shared `pending` counter every change to what List answers bumps under mu (mintPending).
	versions *subject.Versions
	lastID   int64
	mu       sync.Mutex
}

type pendingAsk struct {
	origin acpResponder
	evt    marotte.ServerEvent
	acpID  int64
}

// askEntry is one ask: pending until a take claims it, then settled once by Complete. A terminal
// selector that finds it claimed records end, which outranks the reply's write.
type askEntry struct {
	ask     pendingAsk
	end     askEnd
	claimed bool
}

type askEnd uint8

const (
	endNone askEnd = iota
	// endCleared settles silently: the chat's turn was cancelled or the run finished.
	endCleared
	endEnded
	endMoot
)

type settledAsk struct {
	evt marotte.ServerEvent
	id  int64
}

// claimOutcome is how a claim settled: delivered, lost to its bridge's end, withdrawn by KAS,
// cleared silently, or failed on a bridge still running, which returns the ask to pending.
type claimOutcome int

const (
	claimDelivered claimOutcome = iota
	claimEnded
	claimMoot
	claimCleared
	claimReopened
)

func newPendingPermsTracker() *pendingPermsTracker {
	return &pendingPermsTracker{asks: make(map[int64]*askEntry)}
}

// add registers the ask KAS sent as acpID on origin and returns evt carrying the minted ask id.
func (t *pendingPermsTracker) add(acpID int64, evt marotte.ServerEvent, origin acpResponder) marotte.ServerEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastID++
	evt = withAskID(evt, t.lastID)
	t.asks[t.lastID] = &askEntry{ask: pendingAsk{origin: origin, evt: evt, acpID: acpID}}
	mintPending(&t.versions)
	return evt
}

func withAskID(evt marotte.ServerEvent, id int64) marotte.ServerEvent {
	switch p := evt.Payload.(type) {
	case marotte.PermissionNeededPayload:
		p.RequestID = id
		evt.Payload = p
	case marotte.ElicitationNeededPayload:
		p.RequestID = id
		evt.Payload = p
	case marotte.UserInputNeededPayload:
		p.RequestID = id
		evt.Payload = p
	}
	return evt
}

func (t *pendingPermsTracker) pendingLocked(id int64) *askEntry {
	if e, ok := t.asks[id]; ok && !e.claimed {
		return e
	}
	return nil
}

// takeIfPresent claims one chat's ask, false when someone else answered. One lock spans lookup and
// claim, so two tabs or a human racing the unattended floor cannot both answer.
func (t *pendingPermsTracker) takeIfPresent(chatID marotte.ChatID, id int64) (pendingAsk, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.pendingLocked(id)
	if e == nil || e.ask.evt.ChatID != chatID {
		return pendingAsk{}, false
	}
	t.claimLocked(e)
	return e.ask, true
}

// takeOnOrigin claims the ask origin sent as acpID, the one identity KAS itself knows it by.
func (t *pendingPermsTracker) takeOnOrigin(origin acpResponder, acpID int64) (int64, pendingAsk, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, e := range t.asks {
		if !e.claimed && e.ask.origin == origin && e.ask.acpID == acpID {
			t.claimLocked(e)
			return id, e.ask, true
		}
	}
	return 0, pendingAsk{}, false
}

// takePermissionOption validates one advertised option and claims the request
// in the same critical section. An off-list answer leaves the request pending.
func (t *pendingPermsTracker) takePermissionOption(chatID marotte.ChatID, id int64, optionID string) (ask pendingAsk, pending, offered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.pendingLocked(id)
	if e == nil || e.ask.evt.ChatID != chatID {
		return pendingAsk{}, false, false
	}
	payload, ok := e.ask.evt.Payload.(marotte.PermissionNeededPayload)
	if !ok || !slices.ContainsFunc(payload.Options, func(option marotte.PermissionOption) bool {
		return option.OptionID == optionID
	}) {
		return e.ask, true, false
	}
	t.claimLocked(e)
	return e.ask, true, true
}

func (t *pendingPermsTracker) claimLocked(e *askEntry) {
	e.claimed = true
	mintPending(&t.versions)
}

// complete settles claim id once its reply's write returned wErr. A recorded end wins over the
// write; a refusal because origin ended (marotte.ErrBridgeExited) is that end; any other failure
// left the bridge running and returns the ask to pending.
func (t *pendingPermsTracker) complete(id int64, wErr error) (pendingAsk, claimOutcome, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.asks[id]
	if !ok || !e.claimed {
		return pendingAsk{}, 0, false
	}
	outcome := claimDelivered
	switch {
	case e.end == endEnded, e.end == endNone && errors.Is(wErr, marotte.ErrBridgeExited):
		outcome = claimEnded
	case e.end == endMoot:
		outcome = claimMoot
	case e.end == endCleared:
		outcome = claimCleared
	case wErr != nil:
		e.claimed = false
		mintPending(&t.versions)
		return e.ask, claimReopened, true
	}
	delete(t.asks, id)
	return e.ask, outcome, true
}

// settleLocked is the one terminal transition: a pending ask leaves now (true, for the caller to
// announce or drop), a claimed one records end for Complete. A recorded end is never replaced.
func (t *pendingPermsTracker) settleLocked(id int64, e *askEntry, end askEnd) bool {
	if e.claimed {
		if e.end == endNone {
			e.end = end
		}
		return false
	}
	delete(t.asks, id)
	return true
}

// endOrigin retires every ask that arrived on origin, now ended: nothing can receive their
// answers. It answers the pending ones; a claimed one settles through Complete. A nil origin
// matches nothing.
func (t *pendingPermsTracker) endOrigin(origin acpResponder) []settledAsk {
	if origin == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var ended []settledAsk
	for id, e := range t.asks {
		if e.ask.origin == origin && t.settleLocked(id, e, endEnded) {
			ended = append(ended, settledAsk{evt: e.ask.evt, id: id})
		}
	}
	if len(ended) > 0 {
		mintPending(&t.versions)
	}
	return ended
}

// peek returns one chat's unanswered permission ask without claiming it.
func (t *pendingPermsTracker) peek(chatID marotte.ChatID, id int64) (marotte.PermissionNeededPayload, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.pendingLocked(id)
	if e == nil || e.ask.evt.ChatID != chatID {
		return marotte.PermissionNeededPayload{}, false
	}
	payload, ok := e.ask.evt.Payload.(marotte.PermissionNeededPayload)
	return payload, ok
}

// takeForToolCall retires the chat's ask for toolCallID, false when none is open; should two be
// outstanding, the newest is the awaited one. settled is false for a claimed ask, which Complete
// announces.
func (t *pendingPermsTracker) takeForToolCall(chatID marotte.ChatID, toolCallID string) (a settledAsk, settled, ok bool) {
	if toolCallID == "" {
		return settledAsk{}, false, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var found *askEntry
	var foundID int64
	for id, e := range t.asks {
		if e.end != endNone || e.ask.evt.ChatID != chatID || decisionToolCallID(e.ask.evt.Payload) != toolCallID {
			continue
		}
		if found == nil || id > foundID {
			found, foundID = e, id
		}
	}
	if found == nil {
		return settledAsk{}, false, false
	}
	settled = t.settleLocked(foundID, found, endMoot)
	if settled {
		mintPending(&t.versions)
	}
	return settledAsk{evt: found.ask.evt, id: foundID}, settled, true
}

func decisionToolCallID(payload any) string {
	switch p := payload.(type) {
	case marotte.PermissionNeededPayload:
		return p.ToolCallID
	case marotte.UserInputNeededPayload:
		return p.ToolCallID
	}
	return ""
}

func (t *pendingPermsTracker) clearLocked(match func(marotte.ServerEvent) bool) {
	removed := false
	for id, e := range t.asks {
		if match(e.ask.evt) && t.settleLocked(id, e, endCleared) {
			removed = true
		}
	}
	if removed {
		mintPending(&t.versions)
	}
}

func (t *pendingPermsTracker) clearForChat(chatID marotte.ChatID) {
	if chatID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearLocked(func(evt marotte.ServerEvent) bool { return evt.ChatID == chatID })
}

// clearForRun drops every unresolved decision a run raised, wherever filed, so the connect replay
// stops offering a dead step's card. The run comes off the payload; an empty id is refused. No announcement.
func (t *pendingPermsTracker) clearForRun(workflowID string) {
	if workflowID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearLocked(func(evt marotte.ServerEvent) bool { return marotte.DecisionRunID(evt.Payload) == workflowID })
}

// openNodesForRun is the node id of every unresolved decision a run raised; nil for an empty
// workflowID; a decision naming no node is skipped.
func (t *pendingPermsTracker) openNodesForRun(workflowID string) map[string]struct{} {
	if workflowID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out map[string]struct{}
	for _, e := range t.asks {
		if e.claimed || marotte.DecisionRunID(e.ask.evt.Payload) != workflowID {
			continue
		}
		node := marotte.DecisionNodeID(e.ask.evt.Payload)
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

// list snapshots the unresolved decisions, optionally for one chat: exactly what TakeIfPresent
// still accepts. Order is the contract: ascending ask id, which is ask order.
func (t *pendingPermsTracker) list(chatFilter marotte.ChatID) []marotte.ServerEvent {
	t.mu.Lock()
	ids := make([]int64, 0, len(t.asks))
	for id, e := range t.asks {
		if e.claimed || chatFilter != "" && e.ask.evt.ChatID != "" && e.ask.evt.ChatID != chatFilter {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	result := make([]marotte.ServerEvent, 0, len(ids))
	for _, id := range ids {
		result = append(result, t.asks[id].ask.evt)
	}
	t.mu.Unlock()
	return result
}

// ClearPendingPermsForChat drops every unresolved decision owned by chatID.
func (b *bus) ClearPendingPermsForChat(chatID marotte.ChatID) {
	b.pendingPerms.clearForChat(chatID)
}

// ClearPendingPermsForRun drops a run's unresolved decisions at its terminal transition, silently (ClearForRun).
func (b *bus) ClearPendingPermsForRun(workflowID string) {
	b.pendingPerms.clearForRun(workflowID)
}

// PendingDecisionNodesForRun is the node id of every decision a run's steps still owe.
func (b *bus) PendingDecisionNodesForRun(workflowID string) map[string]struct{} {
	return b.pendingPerms.openNodesForRun(workflowID)
}

// TakePendingPerm claims an unanswered decision for exactly one surface, false when beaten. Take
// first, then answer kiro-cli through the reply: a loser must send nothing, and the reply's write
// decides the decision_settled the other surfaces get.
func (b *bus) TakePendingPerm(chatID marotte.ChatID, askID int64, settledBy marotte.SettledBy) (command.AskReply, bool) {
	ask, ok := b.pendingPerms.takeIfPresent(chatID, askID)
	if !ok {
		return nil, false
	}
	return &askReply{bus: b, ask: ask, id: askID, settledBy: settledBy}, true
}

// TakePendingPermOn claims the ask origin sent as acpID, for an answer marotte makes itself.
func (b *bus) TakePendingPermOn(origin acpResponder, acpID int64, settledBy marotte.SettledBy) (command.AskReply, bool) {
	id, ask, ok := b.pendingPerms.takeOnOrigin(origin, acpID)
	if !ok {
		return nil, false
	}
	return &askReply{bus: b, ask: ask, id: id, settledBy: settledBy}, true
}

// PendingPermission reads an unanswered permission ask without claiming it.
func (b *bus) PendingPermission(chatID marotte.ChatID, requestID int64) (marotte.PermissionNeededPayload, bool) {
	return b.pendingPerms.peek(chatID, requestID)
}

// TakePendingPermissionOption validates and claims a permission response.
func (b *bus) TakePendingPermissionOption(chatID marotte.ChatID, askID int64, optionID string, settledBy marotte.SettledBy) (reply command.AskReply, pending, offered bool) {
	ask, pending, offered := b.pendingPerms.takePermissionOption(chatID, askID, optionID)
	if !offered {
		return nil, pending, false
	}
	return &askReply{bus: b, ask: ask, id: askID, settledBy: settledBy}, true, true
}

// endAsksOf retires every ask that arrived on origin once that bridge ended, announcing each as
// ended so a card on screen says why it went.
func (b *bus) endAsksOf(origin acpResponder) {
	for _, a := range b.pendingPerms.endOrigin(origin) {
		b.announceDecisionSettled(a.evt, a.id, marotte.SettledByEnded)
	}
}

type askReply struct {
	bus       *bus
	settledBy marotte.SettledBy
	ask       pendingAsk
	id        int64
}

func (r *askReply) Respond(ctx context.Context, result any) (command.AnswerOutcome, error) {
	var wErr error
	if r.ask.origin != nil {
		wErr = r.ask.origin.Respond(ctx, r.ask.acpID, result, nil)
	}
	ask, outcome, ok := r.bus.pendingPerms.complete(r.id, wErr)
	if !ok {
		// Only Complete removes a claimed ask, so this is a second Respond on one reply.
		slog.Error("decision answer settled twice", "request_id", r.id, "error", wErr)
		return command.AnswerWithdrawn, nil
	}
	switch outcome {
	case claimDelivered:
		r.bus.announceDecisionSettled(ask.evt, r.id, r.settledBy)
		return command.AnswerDelivered, nil
	case claimEnded:
		slog.Debug("answer dropped: the bridge it arrived on has ended",
			"chat_id", ask.evt.ChatID, "type", ask.evt.Type, "error", wErr)
		r.bus.announceDecisionSettled(ask.evt, r.id, marotte.SettledByEnded)
	case claimMoot:
		r.bus.announceDecisionSettled(ask.evt, r.id, marotte.SettledByMoot)
	case claimCleared:
	case claimReopened:
		// A surface that connected during the write was snapshotted without the claimed ask.
		r.bus.emit(ask.evt)
		return command.AnswerReopened, wErr
	}
	return command.AnswerWithdrawn, nil
}

// PendingPermsWithdraw retires the ask KAS withdrew for toolCallID and announces it moot.
func (b *bus) PendingPermsWithdraw(chatID marotte.ChatID, toolCallID string) bool {
	a, settled, ok := b.pendingPerms.takeForToolCall(chatID, toolCallID)
	if settled {
		b.announceDecisionSettled(a.evt, a.id, marotte.SettledByMoot)
	}
	return ok
}

func (b *bus) announceDecisionSettled(evt marotte.ServerEvent, askID int64, settledBy marotte.SettledBy) {
	kind, known := marotte.DecisionKindForEvent(evt.Type)
	if !known {
		// Only the three *_needed events are tracked, so this is misuse; the claim stands.
		slog.Error("sse: tracked decision has no kind, cannot announce it",
			"type", evt.Type, "request_id", askID)
		return
	}
	b.emit(marotte.NewEvent(marotte.EventDecisionSettled, evt.ChatID, marotte.DecisionSettledPayload{
		RequestID: askID,
		Kind:      kind,
		SettledBy: settledBy,
	}))
	b.retractPush(notice.AskSubject(evt.ChatID, marotte.DecisionRunID(evt.Payload)))
}
