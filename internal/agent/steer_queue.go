package agent

import (
	"context"
	"slices"

	"github.com/cplieger/marotte/internal/chatlock"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// steerQueue is the steer record plus the turn registry's read-loop barrier, as
// command.SteerQueue, so neither collaborator learns the other.
type steerQueue struct {
	recs  *steerRecords
	coord *bridgeCoordinator
	locks *chatlock.Set
	// steps names the carrier whose read loop folds a step key's frames.
	steps *stepSteers
}

var (
	_ command.SteerQueue = steerQueue{}
	_ command.SteerJobs  = steerQueue{}
)

func (q steerQueue) LockSteerOps(ctx context.Context, chatID marotte.ChatID) (func(), error) {
	return q.locks.Lock(ctx, chatID)
}

func (q steerQueue) AwaitReadLoop(ctx context.Context, chatID marotte.ChatID, seq uint64) bool {
	if _, step := chatID.StepSession(); step {
		t, ok := q.steps.target(chatID)
		if !ok {
			return false
		}
		chatID = t.host
	}
	return q.coord.turns.awaitPosition(ctx, chatID, "", seq)
}

func (q steerQueue) OnSteerJob(run func(command.SteerJob)) { q.recs.runJob = run }

func (q steerQueue) SetSteerLead(chatID marotte.ChatID, key string) func() {
	return q.recs.setLead(chatID, key)
}

func (q steerQueue) NeedsPostLoadClear(chatID marotte.ChatID) bool {
	return q.recs.needsPostLoadClear(chatID)
}

// PostLoadCleared parks again a row sent before the clear (one that arrived during the MCP wait), so the
// following drain resends it in order under a fresh id.
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

// RouteSteer writes the row's state before the caller sends. A step's record takes a steer only while
// its steering turn is bound: a finished or paused step stays so.
func (q steerQueue) RouteSteer(chatID marotte.ChatID, key, text string, h command.SteerHolder) (sends []command.SteerSend, refuse string) {
	_, step := chatID.StepSession()
	if !q.recs.step(chatID, !step, func(rec *steerRecord, fx *steerFx) {
		if rec.noCarry && rec.channel == chanNone {
			refuse = command.SteerRefuseNoTurn
			return
		}
		sends, refuse = rec.routeSteerLocked(key, text, h, fx)
	}) {
		return nil, command.SteerRefuseNoTurn
	}
	return sends, refuse
}

func (rec *steerRecord) routeSteerLocked(key, text string, h command.SteerHolder, fx *steerFx) (sends []command.SteerSend, refuse string) {
	w := rec.row(key)
	if h.Delivering && h.Turn != "" && !rec.names(h.Turn) {
		// The turn the delivery was aimed at is no longer starting or bound.
		if w != nil {
			w.owner = ""
			setState(w, rowUnsent, fx)
		}
		return nil, command.SteerRefuseNoTurn
	}
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
	n, bytes := 0, marotte.CarryCost(text)
	rec.live(func(o *dockRow) {
		n++
		bytes += marotte.CarryCost(o.text)
	})
	if n >= maxSteerRows || bytes > maxSteerBytes {
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
func (*steerRecord) sendSelf(w *dockRow, fx *steerFx) command.SteerSend {
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

func (*steerRecord) combine(id string, rows []*dockRow, s rowState, fx *steerFx) command.SteerSend {
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

// BeginRemove deletes a row KAS cannot hold at once, and marks the op's rows for a clear when it may. A
// job-held row is taken over when KAS does not hold it, refused while it might.
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
	rec.op, rec.opTarget, rec.opEnd, rec.opEndStale, rec.opChannel = opID, target, nil, false, rec.channel
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

// RemoveCleared is the delete's re-read after its clear: gone, then a turn end the op saw, then every unread
// kept row resubmitted as one send. A turn end leaves the kept rows for EndOp.
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
		rec.clearOp()
	}
}

func (rec *steerRecord) clearOp() {
	rec.op, rec.opTarget, rec.opEnd, rec.opEndStale = "", "", nil, false
}

func (q steerQueue) OpSent(chatID marotte.ChatID, opID string, s command.SteerSend, queued bool, err error) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if rec.op == opID && err == nil && !queued {
			rec.refusedLocked(s.ID, fx)
		}
	})
}

// EndOp is the op's last look at its turn, in the step that releases it. A close hands the unread rows to a
// pending end; a stale bind settles them here and answers reroute.
func (q steerQueue) EndOp(chatID marotte.ChatID, opID string) (reroute bool) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if rec.op != opID {
			return
		}
		end, stale := rec.opEnd, rec.opEndStale
		if end == nil {
			rec.release(opID)
			if len(rec.unownedWaiting()) > 0 && (rec.channel == chanOpen || (rec.channel == chanProbing && rec.probe == "")) {
				fx.jobs = append(fx.jobs, command.SteerJob{Chat: chatID})
			}
			return
		}
		unread := rec.takeUnread(opID)
		rec.clearOp()
		reroute = q.settleOpUnread(rec, unread, end, stale, fx)
	})
	return reroute
}

// A step's rows go "not read" at the boundary because a finished step carries nothing.
func (q steerQueue) settleOpUnread(rec *steerRecord, unread []*dockRow, end *command.SteerTurnEnd, stale bool, fx *steerFx) (reroute bool) {
	switch {
	case rec.noCarry:
		for _, w := range unread {
			w.owner = ""
			retire(w, boundarySteer(w), fx)
		}
	case stale:
		for _, w := range unread {
			settleStaleLocked(w, fx)
		}
		return true
	case len(unread) > 0:
		q.recs.pendEndLocked(rec, unread, *end, fx)
	}
	return false
}

// takeUnread answers the rows owner still holds that the agent has not read,
// unowning the read ones.
func (rec *steerRecord) takeUnread(owner string) []*dockRow {
	var unread []*dockRow
	for _, w := range rec.owned(owner) {
		if w.state == rowRead {
			w.owner = ""
			continue
		}
		unread = append(unread, w)
	}
	return unread
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
		rec.op, rec.opTarget, rec.opEnd, rec.opEndStale, rec.opChannel = opID, "", nil, false, rec.channel
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

// Ends lists the chat's pending turn ends, oldest first.
func (q steerQueue) Ends(chatID marotte.ChatID) []command.SteerEnd {
	var out []command.SteerEnd
	q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		for _, e := range rec.ends {
			out = append(out, command.SteerEnd{Owner: e.owner, End: e.end})
		}
	})
	return out
}

// JobRows answers the rows still the end's, lead first, then in arrival order. A
// row an op took over, or the agent read, is no longer here.
func (q steerQueue) JobRows(chatID marotte.ChatID, owner string) (rows []command.SteerRow, gone bool) {
	gone = !q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		var mine []*dockRow
		for _, w := range rec.owned(owner) {
			if w.state == rowRead {
				w.owner = ""
				continue
			}
			mine = append(mine, w)
		}
		sortLeadFirst(mine, rec.lead)
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

// EndUnsent makes every row the end still owns unsent and unowned, KAS-held ones
// released where they are, and drops the end.
func (q steerQueue) EndUnsent(chatID marotte.ChatID, owner string) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		for _, w := range rec.owned(owner) {
			w.owner = ""
			if w.state != rowRead && !w.state.inKAS() {
				setState(w, rowUnsent, fx)
			}
		}
		rec.ends = slices.DeleteFunc(rec.ends, func(e pendingEnd) bool { return e.owner == owner })
		fx.changed = true
	})
}

// UnsentRows answers the unowned unread rows a drain may send as one prompt, lead first; none while KAS may
// hold copies, unless the drain follows a drained bridge death, whose prompt clears KAS first.
func (q steerQueue) UnsentRows(chatID marotte.ChatID, deathDrained bool) []command.SteerRow {
	var out []command.SteerRow
	q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		if rec.reinject && !deathDrained {
			return
		}
		var rows []*dockRow
		rec.live(func(w *dockRow) {
			if w.owner == "" && w.state == rowUnsent {
				rows = append(rows, w)
			}
		})
		sortLeadFirst(rows, rec.lead)
		for _, w := range rows {
			out = append(out, command.SteerRow{Key: w.key, Text: w.text})
		}
	})
	return out
}

// Delivered retires the named rows once the prompt carrying them has opened: each
// leaves the dock on a removed frame and writes no entry, since its words are that prompt's.
func (q steerQueue) Delivered(chatID marotte.ChatID, keys []string) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		for _, k := range keys {
			w := rec.row(k)
			if w == nil || w.state == rowRead {
				continue
			}
			w.owner = ""
			retire(w, nil, fx)
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

// NextParked parks every un-owned unsent row for turnID and answers its head (lead, else oldest), only when
// the record names turnID; while KAS may hold copies unsent rows stay unsent.
func (q steerQueue) NextParked(chatID marotte.ChatID, turnID string) (key, text string, ok bool) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if !rec.names(turnID) {
			return
		}
		var parked []*dockRow
		rec.live(func(w *dockRow) {
			if w.owner != "" {
				return
			}
			if w.state == rowUnsent && !rec.reinject {
				setState(w, rowParked, fx)
			}
			if w.state == rowParked {
				parked = append(parked, w)
			}
		})
		if len(parked) == 0 {
			return
		}
		sortLeadFirst(parked, rec.lead)
		key, text, ok = parked[0].key, parked[0].text, true
	})
	return key, text, ok
}
