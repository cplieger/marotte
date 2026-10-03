package agent

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// steerQueue is the steer record plus the turn registry's read-loop barrier, as
// command.SteerQueue, so neither collaborator learns the other.
type steerQueue struct {
	recs  *steerRecords
	coord *BridgeCoordinator
	locks *chatLocks
}

var (
	_ command.SteerQueue = steerQueue{}
	_ command.SteerJobs  = steerQueue{}
)

func (q steerQueue) LockSteerOps(ctx context.Context, chatID marotte.ChatID) (func(), error) {
	return q.locks.lock(ctx, chatID)
}

func (q steerQueue) AwaitReadLoop(ctx context.Context, chatID marotte.ChatID, seq uint64) bool {
	return q.coord.turns.awaitPosition(ctx, chatID, "", seq)
}

func (q steerQueue) OnSteerJob(run func(command.SteerJob)) { q.recs.runJob = run }

func (q steerQueue) SetSteerLead(chatID marotte.ChatID, key string) func() {
	return q.recs.SetLead(chatID, key)
}

func (q steerQueue) NeedsPostLoadClear(chatID marotte.ChatID) bool {
	return q.recs.NeedsPostLoadClear(chatID)
}

// PostLoadCleared: a row this record sent before the clear (a steer that arrived
// while the prompt waited for MCP) is parked again, so the drain that follows
// resends it in order under a fresh id.
func (q steerQueue) PostLoadCleared(chatID marotte.ChatID, cleared []string, landed bool) {
	if !landed {
		return
	}
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		rec.reinject = false
		rec.live(func(w *dockRow) {
			if w.owner == "" && slices.Contains(cleared, w.kasID) && rowRuleFor(w.state, evPostLoadClear) == rPark {
				setState(w, rowParked, fx)
			}
		})
	})
}

func (q steerQueue) AgentRowsWaiting(chatID marotte.ChatID) bool {
	waiting := false
	q.do(chatID, func(rec *steerRecord, _ *steerFx) { waiting = len(rec.others) > 0 })
	return waiting
}

func (q steerQueue) do(chatID marotte.ChatID, fn func(rec *steerRecord, fx *steerFx)) bool {
	return q.recs.step(chatID, false, fn)
}

// RouteSteer writes the row's state before the caller sends.
func (q steerQueue) RouteSteer(chatID marotte.ChatID, key, text string, h command.SteerHolder) (sends []command.SteerSend, refuse string) {
	if !q.recs.step(chatID, true, func(rec *steerRecord, fx *steerFx) {
		sends, refuse = rec.routeSteerLocked(key, text, h, fx)
	}) {
		return nil, command.SteerRefuseNoTurn
	}
	return sends, refuse
}

func (rec *steerRecord) routeSteerLocked(key, text string, h command.SteerHolder, fx *steerFx) (sends []command.SteerSend, refuse string) {
	w := rec.row(key)
	if w == nil {
		if w, refuse = rec.newRow(key, text, h.Held, fx); w == nil {
			return nil, refuse
		}
	}
	if !h.Held {
		w.owner = ""
		setState(w, rowUnsent, fx)
		return nil, command.SteerRefuseNoTurn
	}
	if h.PromptClass && (!h.Live || (!h.Delivering && rec.parkedBesides(w))) {
		setState(w, rowParked, fx)
		return nil, ""
	}
	if rowRuleFor(w.state, evSteer) == rRoute {
		sends = rec.routeLocked(w, fx)
	}
	return sends, ""
}

func (rec *steerRecord) newRow(key, text string, held bool, fx *steerFx) (w *dockRow, refuse string) {
	if !held {
		return nil, command.SteerRefuseNoTurn
	}
	n := 0
	rec.live(func(*dockRow) { n++ })
	if n >= maxSteerRows {
		return nil, command.SteerRefuseFull
	}
	rec.rows = append(rec.rows, dockRow{key: key, text: text, seq: rec.nextSeq, state: rowParked})
	rec.nextSeq++
	w = &rec.rows[len(rec.rows)-1]
	fx.frames = append(fx.frames, rowFrame(w))
	return w, ""
}

// routeLocked: in OPEN the un-owned waiting rows go ahead of the new one.
func (rec *steerRecord) routeLocked(w *dockRow, fx *steerFx) []command.SteerSend {
	switch rec.channel {
	case chanNone:
		return []command.SteerSend{rec.sendSelf(w, fx)}
	case chanOpen:
		var sends []command.SteerSend
		for _, o := range rec.unownedWaiting() {
			if o != w {
				sends = append(sends, rec.sendSelf(o, fx))
			}
		}
		return append(sends, rec.sendSelf(w, fx))
	case chanUnconfirmed:
		return []command.SteerSend{rec.probeOf([]*dockRow{w}, fx)}
	case chanProbing:
		w.kasID = ""
		setState(w, rowWaiting, fx)
	case nChannelStates:
	}
	return nil
}

func (rec *steerRecord) parkedBesides(w *dockRow) bool {
	found := false
	rec.live(func(o *dockRow) { found = found || (o != w && o.state == rowParked) })
	return found
}

// sendSelf uses a fresh id once the key may already be in KAS's log: one id never
// names two records there.
func (rec *steerRecord) sendSelf(w *dockRow, fx *steerFx) command.SteerSend {
	id := w.key
	var keys []string
	if w.sent || w.noted {
		id, keys = freshSteerID(), []string{w.key}
	} else {
		w.sent = true
	}
	w.kasID = id
	setState(w, rowQueued, fx)
	return command.SteerSend{ID: id, Text: w.text, Keys: keys}
}

func (rec *steerRecord) probeOf(rows []*dockRow, fx *steerFx) command.SteerSend {
	id := freshSteerID()
	rec.probe = id
	rec.channel = chanProbing
	return rec.combine(id, rows, rowOutstanding, fx)
}

func (rec *steerRecord) combine(id string, rows []*dockRow, s rowState, fx *steerFx) command.SteerSend {
	keys := make([]string, 0, len(rows))
	texts := make([]string, 0, len(rows))
	for _, w := range rows {
		w.kasID = id
		setState(w, s, fx)
		keys = append(keys, w.key)
		texts = append(texts, w.text)
	}
	return command.SteerSend{ID: id, Text: joinSteers(texts), Keys: keys}
}

// SteerSent: an errored send leaves its rows where KAS may hold them, so their fate
// is observed; only queued:false moves them.
func (q steerQueue) SteerSent(chatID marotte.ChatID, s command.SteerSend, queued bool, err error) {
	if err != nil || queued {
		return
	}
	q.do(chatID, func(rec *steerRecord, fx *steerFx) { rec.refusedLocked(s.ID, fx) })
}

func (rec *steerRecord) refusedLocked(id string, fx *steerFx) {
	for _, w := range rec.under(id) {
		if w.state.inKAS() && rowRuleFor(w.state, evRefused) == rCleared {
			setState(w, rowCleared, fx)
		}
	}
	if channelRule(rec.channel, evRefused) != cUnconfirmed || (rec.channel == chanProbing && id != rec.probe) {
		return
	}
	rec.live(func(w *dockRow) {
		if w.state == rowWaiting {
			setState(w, rowCleared, fx)
		}
	})
	rec.probe = ""
	rec.channel = chanUnconfirmed
}

// BeginRemove deletes a row KAS cannot hold at once; one it may hold marks the op's
// rows for a clear. A row a turn-end job holds is taken over when KAS does not hold
// it, and refused settling while KAS might.
func (q steerQueue) BeginRemove(chatID marotte.ChatID, key, opID string) (needsClear bool, refuse string) {
	if !q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		needsClear, refuse = rec.beginRemoveLocked(key, opID, fx)
	}) {
		refuse = command.SteerRefuseNotWaiting
		if q.recs.gone(chatID) {
			refuse = command.SteerRefuseChatGone
		}
	}
	return needsClear, refuse
}

func (rec *steerRecord) beginRemoveLocked(key, opID string, fx *steerFx) (needsClear bool, refuse string) {
	if slices.ContainsFunc(rec.others, func(o marotte.SteerQueuedPayload) bool { return o.SteerID == key }) {
		return false, command.SteerRefuseNotUser
	}
	w := rec.row(key)
	switch {
	case w == nil:
		return false, command.SteerRefuseNotWaiting
	case w.owner != "" && w.state.inKAS():
		return false, command.SteerRefuseSettling
	}
	w.owner = ""
	ev := deleteEvent(w.state)
	switch rowRuleFor(w.state, ev) {
	case rDeleted:
		retire(w, deletedSteer(w), fx)
		if channelRule(rec.channel, ev) == cOwed && rec.probe == "" && len(rec.unownedWaiting()) == 0 {
			rec.channel = chanUnconfirmed
		}
	case rResubmit:
		if len(rec.others) > 0 {
			return false, command.SteerRefuseAgentRows
		}
		if rec.channel == chanNone && rec.starting != "" {
			return false, command.SteerRefuseStarting
		}
		rec.mark(opID, key)
		return true, ""
	case rUnhandled, rImpossible, rKeep, rRead, rCleared, rDiscarded, rCollect, rJoinProbe, rRoute, rDrop, rPark:
	}
	return false, ""
}

func deleteEvent(s rowState) steerEvent {
	switch s {
	case rowWaiting:
		return evDeleteWaiting
	case rowQueued, rowOutstanding:
		return evDeleteQueued
	case rowParked, rowCleared, rowUnsent, rowRead, rowDone, nRowStates:
	}
	return evDeleteIdle
}

func (rec *steerRecord) mark(opID, target string) {
	rec.op, rec.opTarget, rec.opEnd, rec.opChannel = opID, target, nil, rec.channel
	rec.live(func(w *dockRow) {
		if w.owner == "" && w.state != rowParked && w.state != rowUnsent {
			w.owner = opID
		}
	})
}

func (rec *steerRecord) owned(opID string) []*dockRow {
	var out []*dockRow
	for i := range rec.rows {
		if w := &rec.rows[i]; w.state != rowDone && w.owner == opID {
			out = append(out, w)
		}
	}
	return out
}

// RemoveCleared is the delete's re-read after its clear: gone first, then a turn
// end the op saw, then the resubmit of every unread kept row as one send. A turn
// end leaves the kept rows the op's, for EndOp to hand over.
func (q steerQueue) RemoveCleared(chatID marotte.ChatID, opID string, cleared []string, landed bool) command.SteerOpResult {
	res := command.SteerOpResult{Reason: command.SteerRefuseChatGone}
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		res = command.SteerOpResult{}
		switch {
		case rec.op != opID:
			res.Reason = command.SteerRefuseChatGone
			return
		case !landed && rec.opEnd == nil:
			res.Reason = command.SteerRefuseNoReply
			return
		}
		kept, consumed := rec.keepOwnedLocked(opID, cleared, fx)
		if consumed {
			res.Reason = command.SteerRefuseConsumed
		}
		if rec.opEnd == nil {
			res.Resend = rec.resubmitLocked(kept, fx)
		}
	})
	return res
}

// keepOwnedLocked: a target the agent read before the clear landed is consumed,
// not deleted.
func (rec *steerRecord) keepOwnedLocked(opID string, cleared []string, fx *steerFx) (kept []*dockRow, consumed bool) {
	for _, w := range rec.owned(opID) {
		if slices.Contains(cleared, w.kasID) && rowRuleFor(w.state, evForeignClear) == rCleared {
			setState(w, rowCleared, fx)
		}
	}
	for _, w := range rec.owned(opID) {
		switch {
		case w.key == rec.opTarget && w.state == rowRead:
			w.owner = ""
			consumed = true
		case w.key == rec.opTarget:
			retire(w, deletedSteer(w), fx)
		case w.state == rowRead:
			w.owner = ""
		default:
			kept = append(kept, w)
		}
	}
	return kept, consumed
}

// resubmitLocked: the op's channel decides probe or plain send, unless its turn has
// closed since: nothing bound is left to probe.
func (rec *steerRecord) resubmitLocked(kept []*dockRow, fx *steerFx) *command.SteerSend {
	bound := channelRule(rec.opChannel, evDeleteQueued) == cResubmit && rec.channel != chanNone
	var s command.SteerSend
	switch {
	case len(kept) == 0:
		if bound {
			rec.probe = ""
			rec.channel = chanUnconfirmed
		}
		return nil
	case !bound:
		s = rec.combine(freshSteerID(), kept, rowQueued, fx)
	default:
		s = rec.probeOf(kept, fx)
	}
	return &s
}

func (rec *steerRecord) release(opID string) {
	for _, w := range rec.owned(opID) {
		w.owner = ""
	}
	if rec.op == opID {
		rec.op, rec.opTarget, rec.opEnd = "", "", nil
	}
}

func (q steerQueue) OpSent(chatID marotte.ChatID, opID string, s command.SteerSend, queued bool, err error) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if rec.op == opID && err == nil && !queued {
			rec.refusedLocked(s.ID, fx)
		}
	})
}

// EndOp is the op's last observation of its turn, in the same step as its release,
// so no turn end can land between them unseen. A turn that ended under the op
// leaves its rows the op's, as a job's, and is answered for the caller to resolve
// under the lock it still holds.
func (q steerQueue) EndOp(chatID marotte.ChatID, opID string) (end *command.SteerTurnEnd) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if rec.op != opID {
			return
		}
		if end = rec.opEnd; end != nil {
			rec.op, rec.opTarget, rec.opEnd = "", "", nil
			return
		}
		rec.release(opID)
		if len(rec.unownedWaiting()) > 0 && (rec.channel == chanOpen || (rec.channel == chanProbing && rec.probe == "")) {
			fx.jobs = append(fx.jobs, command.SteerJob{Chat: chatID})
		}
	})
	return end
}

// BeginDiscard makes every row the op's, a job's included.
func (q steerQueue) BeginDiscard(chatID marotte.ChatID, opID string) (needsClear bool, refuse string) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		userInKAS := false
		rec.live(func(w *dockRow) { userInKAS = userInKAS || w.state.inKAS() })
		if rec.channel == chanNone && rec.starting != "" && userInKAS {
			refuse = command.SteerRefuseStarting
			return
		}
		rec.op, rec.opTarget, rec.opEnd, rec.opChannel = opID, "", nil, rec.channel
		rec.live(func(w *dockRow) { w.owner = opID })
		needsClear = userInKAS || len(rec.others) > 0
		if !needsClear {
			rec.discardLocked(opID, fx)
		}
	})
	return needsClear, refuse
}

// DiscardCleared: a row read before the clear landed keeps its read entry and gets
// no deleted one. A clear with no reply leaves the rows the op's for EndOp.
func (q steerQueue) DiscardCleared(chatID marotte.ChatID, opID string, landed bool) command.SteerOpResult {
	res := command.SteerOpResult{Reason: command.SteerRefuseChatGone}
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		res = command.SteerOpResult{}
		switch {
		case rec.op != opID:
			res.Reason = command.SteerRefuseChatGone
		case !landed:
			res.Reason = command.SteerRefuseNoReply
		default:
			rec.discardClearedLocked()
			rec.discardLocked(opID, fx)
		}
	})
	return res
}

func (rec *steerRecord) discardClearedLocked() {
	if channelRule(rec.opChannel, evDiscard) == cUnconfirmed && rec.channel != chanNone {
		rec.probe = ""
		rec.channel = chanUnconfirmed
	}
}

func (rec *steerRecord) discardLocked(opID string, fx *steerFx) {
	for _, w := range rec.owned(opID) {
		if w.state == rowRead {
			w.owner = ""
			continue
		}
		if rowRuleFor(w.state, evDiscard) == rDiscarded {
			retire(w, deletedSteer(w), fx)
		}
	}
	rec.release(opID)
}

// JobRows answers the rows still the job's, lead first, then in arrival order. A
// row an op took over, or the agent read, is no longer here.
func (q steerQueue) JobRows(chatID marotte.ChatID, owner, lead string) (rows []command.SteerRow, gone bool) {
	gone = !q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		var mine []*dockRow
		for _, w := range rec.owned(owner) {
			if w.state == rowRead {
				w.owner = ""
				continue
			}
			mine = append(mine, w)
		}
		slices.SortStableFunc(mine, func(a, b *dockRow) int {
			if (a.key == lead) != (b.key == lead) {
				if a.key == lead {
					return -1
				}
				return 1
			}
			return cmp.Compare(a.seq, b.seq)
		})
		for _, w := range mine {
			rows = append(rows, command.SteerRow{Key: w.key, Text: w.text, InKAS: w.state.inKAS()})
		}
	})
	return rows, gone
}

func (q steerQueue) StraysCleared(chatID marotte.ChatID, owner string, cleared []string, all bool) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		for _, w := range rec.owned(owner) {
			if w.state.inKAS() && (all || slices.Contains(cleared, w.kasID)) {
				setState(w, rowCleared, fx)
			}
		}
	})
}

func (q steerQueue) Release(chatID marotte.ChatID, owner string, inKASOnly bool) {
	q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		for _, w := range rec.owned(owner) {
			if !inKASOnly || w.state.inKAS() {
				w.owner = ""
			}
		}
	})
}

func (q steerQueue) Unsent(chatID marotte.ChatID, owner string) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		for _, w := range rec.owned(owner) {
			w.owner = ""
			if w.state != rowRead {
				setState(w, rowUnsent, fx)
			}
		}
	})
}

// Resent writes each of the job's rows its boundary entry and answers their keys
// and joined text, lead first. The rows stay the job's until their prompt opens.
func (q steerQueue) Resent(chatID marotte.ChatID, owner, lead string) (keys []string, text string) {
	rows, _ := q.JobRows(chatID, owner, lead)
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		texts := make([]string, 0, len(rows))
		for _, r := range rows {
			w := rec.row(r.Key)
			if w == nil || w.owner != owner {
				continue
			}
			if !w.noted {
				w.noted = true
				fx.notes = append(fx.notes, steerNote{id: w.key, steer: boundarySteer(w)})
			}
			keys = append(keys, w.key)
			texts = append(texts, w.text)
		}
		text = joinSteers(texts)
	})
	return keys, text
}

// Delivered is called once the prompt carrying the job's words has opened.
func (q steerQueue) Delivered(chatID marotte.ChatID, owner string) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		for _, w := range rec.owned(owner) {
			if w.state != rowRead {
				retire(w, nil, fx)
			}
		}
	})
}

// PlanFlush sends the waiting rows as themselves in OPEN, or as the owed probe in
// PROBING; any other channel owes nothing.
func (q steerQueue) PlanFlush(chatID marotte.ChatID) (sends []command.SteerSend) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		waiting := rec.unownedWaiting()
		if len(waiting) == 0 || channelRule(rec.channel, evJobWake) != cFlush {
			return
		}
		if rec.channel == chanOpen {
			for _, w := range waiting {
				sends = append(sends, rec.sendSelf(w, fx))
			}
			return
		}
		if rec.probe == "" {
			sends = append(sends, rec.probeOf(waiting, fx))
		}
	})
	return sends
}

// NextParked parks every un-owned unsent row, so the prompt now starting carries
// them, and answers the oldest parked row. While KAS may hold copies of them (the
// post-load clear was skipped or did not land), unsent rows stay unsent: sending
// one would deliver its words twice.
func (q steerQueue) NextParked(chatID marotte.ChatID) (key, text string, ok bool) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		var head *dockRow
		rec.live(func(w *dockRow) {
			if w.owner != "" {
				return
			}
			if w.state == rowUnsent && !rec.reinject {
				setState(w, rowParked, fx)
			}
			if w.state == rowParked && (head == nil || w.seq < head.seq) {
				head = w
			}
		})
		if head != nil {
			key, text, ok = head.key, head.text, true
		}
	})
	return key, text, ok
}

// chatLocks is one context-aware mutex per chat, reclaimed when its last holder
// or waiter leaves, so different chats never wait on each other and the map
// holds only chats with an operation in flight.
type chatLocks struct {
	m  map[marotte.ChatID]*chatLock
	mu sync.Mutex
}

type chatLock struct {
	ch   chan struct{}
	refs int
}

func newChatLocks() *chatLocks {
	return &chatLocks{m: make(map[marotte.ChatID]*chatLock)}
}

func (c *chatLocks) lock(ctx context.Context, chatID marotte.ChatID) (func(), error) {
	c.mu.Lock()
	l := c.m[chatID]
	if l == nil {
		l = &chatLock{ch: make(chan struct{}, 1)}
		c.m[chatID] = l
	}
	l.refs++
	c.mu.Unlock()
	select {
	case l.ch <- struct{}{}:
		return func() {
			<-l.ch
			c.leave(chatID, l)
		}, nil
	case <-ctx.Done():
		c.leave(chatID, l)
		return nil, ctx.Err()
	}
}

func (c *chatLocks) leave(chatID marotte.ChatID, l *chatLock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l.refs--
	if l.refs == 0 {
		delete(c.m, chatID)
	}
}
