package agent

import (
	"context"
	"slices"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// steerQueue is the steer record plus the turn registry's read-loop barrier, as
// command.SteerQueue, so neither collaborator learns the other.
type steerQueue struct {
	recs  *steerRecords
	coord *bridgeCoordinator
	locks *keylock.Set[marotte.ChatID]
	// steps names the host session whose read loop folds a step key's frames.
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
	return q.coord.turns.awaitPosition(ctx, chatID, seq)
}

func (q steerQueue) OnSteerJob(run func(command.SteerJob)) { q.recs.runJob = run }

// SetSteerLead also marks a row the run list handed over as send-now's on the list, which owns its words once the
// turn end returns it.
func (q steerQueue) SetSteerLead(chatID marotte.ChatID, key string) (func(), bool) {
	undo, waiting, entry := q.recs.setLead(chatID, key)
	if !waiting || entry == "" || !q.coord.leadSteer(chatID, entry) {
		return undo, waiting
	}
	return func() {
		undo()
		q.coord.ClearNext(chatID, entry)
	}, true
}

func (q steerQueue) NeedsPostLoadClear(chatID marotte.ChatID) bool {
	return q.recs.needsPostLoadClear(chatID)
}

// PostLoadCleared: a landed clear leaves kiro-cli holding none of the record's rows. An entry row waiting for it goes
// back to the run list still a steer, for the turn whose start ran the clear (settleEntries); any other row the clear
// named is parked again, and a later send goes under a fresh id.
func (q steerQueue) PostLoadCleared(chatID marotte.ChatID, cleared []string, landed bool) {
	if !landed {
		return
	}
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		rec.reinject = false
		rec.live(func(w *dockRow) {
			switch {
			case w.owner != "":
			case w.entry != "" && (w.state == rowUnsent || w.state == rowParked):
				w.back = fateCleared
				fx.changed = true
			case slices.Contains(cleared, w.kasID) && rowRuleFor(w.state, evPostLoadClear) == rPark:
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
func (q steerQueue) RouteSteer(chatID marotte.ChatID, key, text string, from *marotte.Caller, h command.SteerHolder) (sends []command.SteerSend, refuse string) {
	_, step := chatID.StepSession()
	if !q.recs.step(chatID, !step, func(rec *steerRecord, fx *steerFx) {
		if rec.noCarry && rec.channel == chanNone {
			refuse = command.SteerRefuseNoTurn
			return
		}
		sends, refuse = rec.routeSteerLocked(key, text, from, h, fx)
	}) {
		return nil, command.SteerRefuseNoTurn
	}
	return sends, refuse
}

func (rec *steerRecord) routeSteerLocked(
	key, text string, from *marotte.Caller, h command.SteerHolder, fx *steerFx,
) (sends []command.SteerSend, refuse string) {
	w := rec.row(key)
	if w == nil {
		if w, refuse = rec.newRow(key, text, from, h.Held, fx); w == nil {
			return nil, refuse
		}
	}
	if !h.Held {
		w.owner = ""
		setState(w, rowUnsent, fx)
		return nil, command.SteerRefuseNoTurn
	}
	if h.PromptClass && (!h.Live || rec.parkedBesides(w)) {
		setState(w, rowParked, fx)
		return nil, ""
	}
	if rowRuleFor(w.state, evSteer) == rRoute {
		sends = rec.routeLocked(w, fx)
	}
	return sends, ""
}

// RouteEntry routes a steer the chat's run list offered, under the entry's key. It is never parked: with no turn
// holding admission, or no live bridge, it is refused, and the list takes the entry as a prompt.
func (q steerQueue) RouteEntry(chatID marotte.ChatID, s *command.EntrySteer, h command.SteerHolder) (sends []command.SteerSend, refuse string) {
	if _, step := chatID.StepSession(); step {
		return nil, command.SteerRefuseNoTurn
	}
	if !q.recs.step(chatID, true, func(rec *steerRecord, fx *steerFx) {
		sends, refuse = rec.routeEntryLocked(s, h, fx)
	}) {
		return nil, command.SteerRefuseNoTurn
	}
	return sends, refuse
}

func (rec *steerRecord) routeEntryLocked(
	s *command.EntrySteer, h command.SteerHolder, fx *steerFx,
) (sends []command.SteerSend, refuse string) {
	key := marotte.SteerIDFor(s.Entry)
	if !h.Held || !h.Live || rec.row(key) != nil {
		return nil, command.SteerRefuseNoTurn
	}
	w, refuse := rec.newRow(key, s.Text, s.Caller, true, fx)
	if w == nil {
		return nil, refuse
	}
	w.entry, w.offer, w.sent = s.Entry, s.Offer, s.Resent
	return rec.routeLocked(w, fx), ""
}

func (rec *steerRecord) newRow(key, text string, from *marotte.Caller, held bool, fx *steerFx) (w *dockRow, refuse string) {
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
	rec.rows = append(rec.rows, dockRow{key: key, text: text, caller: from, seq: rec.nextSeq, state: rowParked})
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
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if err != nil || queued {
			rec.ackedLocked(s.ID)
			return
		}
		rec.refusedLocked(s.ID, fx)
	})
}

func (rec *steerRecord) ackedLocked(id string) {
	for _, w := range rec.under(id) {
		if w.state.inKAS() {
			w.acked = true
		}
	}
}

func (rec *steerRecord) refusedLocked(id string, fx *steerFx) {
	for _, w := range rec.under(id) {
		if w.state.inKAS() && rowRuleFor(w.state, evRefused) == rCleared {
			w.refused = true
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
	return q.beginRemove(chatID, key, opID, false)
}

// BeginQueueInstead is BeginRemove whose target's words go back to the run list as the user's queued row.
func (q steerQueue) BeginQueueInstead(chatID marotte.ChatID, key, opID string) (needsClear bool, refuse string) {
	return q.beginRemove(chatID, key, opID, true)
}

func (q steerQueue) beginRemove(chatID marotte.ChatID, key, opID string, queue bool) (needsClear bool, refuse string) {
	if !q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		needsClear, refuse = rec.beginRemoveLocked(key, opID, queue, fx)
	}) {
		refuse = command.SteerRefuseNotWaiting
		if q.recs.gone(chatID) {
			refuse = command.SteerRefuseChatGone
		}
	}
	return needsClear, refuse
}

// removeResends reports, changing nothing, whether deleting key resubmits the rows kept with it: exactly where
// BeginRemove marks the op's rows for a clear.
func (q steerQueue) removeResends(chatID marotte.ChatID, key string) (resends bool) {
	q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		_, rule, refuse := rec.removePlanLocked(key)
		resends = refuse == "" && rule == rResubmit
	})
	return resends
}

// removePlanLocked is deleting key's decision, changing nothing: the row, its delete rule, or the refusal.
func (rec *steerRecord) removePlanLocked(key string) (w *dockRow, rule rowRule, refuse string) {
	if slices.ContainsFunc(rec.others, func(o marotte.SteerQueuedPayload) bool { return o.SteerID == key }) {
		return nil, rUnhandled, command.SteerRefuseNotUser
	}
	w = rec.row(key)
	switch {
	case w == nil:
		return nil, rUnhandled, command.SteerRefuseNotWaiting
	case w.owner != "" && w.state.inKAS():
		return nil, rUnhandled, command.SteerRefuseSettling
	}
	rule = rowRuleFor(w.state, deleteEvent(w.state))
	switch {
	case rule == rResubmit && rec.clearRaces():
		return nil, rUnhandled, command.SteerRefuseSettling
	case rule == rResubmit && len(rec.others) > 0:
		return nil, rUnhandled, command.SteerRefuseAgentRows
	}
	return w, rule, ""
}

// delivering: KAS holds w while a turn end is unsettled, so its continuation reads it or its boundary drops it.
// It matches removePlanLocked's settling refusal, so a row the dock offers a delete for is one the server takes.
func (rec *steerRecord) delivering(w *dockRow) bool {
	return w.state.inKAS() && rec.clearRaces()
}

func (rec *steerRecord) endOwned(w *dockRow) bool {
	return w.owner != "" && slices.ContainsFunc(rec.ends, func(e pendingEnd) bool { return e.owner == w.owner })
}

// clearRaces: a clear now would empty the buffer under a continuation KAS may run for a turn end, which then
// answers KAS's hidden "Additional input arrived" prompt with nothing after it. It holds from the end's fold to
// KAS's idle report, and past a continuation's bind until KAS no longer holds any row it held at the end or the bind.
func (rec *steerRecord) clearRaces() bool {
	races := rec.settling || slices.ContainsFunc(rec.endHeld, rec.inKASNow)
	rec.live(func(w *dockRow) { races = races || (w.state.inKAS() && rec.endOwned(w)) })
	return races
}

func (rec *steerRecord) beginRemoveLocked(key, opID string, queue bool, fx *steerFx) (needsClear bool, refuse string) {
	w, rule, refuse := rec.removePlanLocked(key)
	switch {
	case refuse != "":
		return false, refuse
	case queue && (w.entry == "" || w.caller != nil):
		return false, command.SteerRefuseNotQueueable
	}
	w.owner = ""
	ev := deleteEvent(w.state)
	switch rule {
	case rDeleted:
		rec.takeOut(w, queue, fx)
		if channelRule(rec.channel, ev) == cOwed && rec.probe == "" && len(rec.unownedWaiting()) == 0 {
			rec.channel = chanUnconfirmed
		}
	case rResubmit:
		rec.mark(opID, key)
		rec.opQueue = queue
		return true, ""
	case rUnhandled, rImpossible, rKeep, rRead, rCleared, rDiscarded, rCollect, rJoinProbe, rRoute, rDrop, rPark:
	}
	return false, ""
}

// takeOut ends a delete's target: deleted, or for a queue-instead handed back to the run list as the user's row
// that waits for a turn of its own (fateBack).
func (*steerRecord) takeOut(w *dockRow, queue bool, fx *steerFx) {
	if queue {
		handBack(w, fateBack, fx)
		return
	}
	retire(w, nil, fx)
}

// handBack leaves entry row w's words to the run list under fate f; settleEntries retires the row.
func handBack(w *dockRow, f steerFate, fx *steerFx) {
	w.owner, w.back = "", f
	setState(w, rowCleared, fx)
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
			res.Resend = rec.resubmitLocked(handBackCallers(kept, fx), fx)
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
			rec.takeOut(w, rec.opQueue, fx)
		case w.state == rowRead:
			w.owner = ""
		default:
			kept = append(kept, w)
		}
	}
	return kept, consumed
}

// handBackCallers hands each kept row of a caller's run-list entry back to the list, which places it behind the
// user's (fateCleared), and answers the rest: one resubmit never joins a caller's words to the user's.
func handBackCallers(kept []*dockRow, fx *steerFx) []*dockRow {
	return slices.DeleteFunc(kept, func(w *dockRow) bool {
		if w.entry == "" || w.caller == nil {
			return false
		}
		handBack(w, fateCleared, fx)
		return true
	})
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
	rec.op, rec.opTarget, rec.opEnd, rec.opEndStale, rec.opQueue = "", "", nil, false, false
}

func (q steerQueue) OpSent(chatID marotte.ChatID, opID string, s command.SteerSend, queued bool, err error) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		if rec.op == opID && err == nil && !queued {
			rec.refusedLocked(s.ID, fx)
		}
	})
}

// EndOp is the op's last look at its turn, in the step that releases it. A close hands the unread rows to a
// pending end; a stale bind settles them here.
func (q steerQueue) EndOp(chatID marotte.ChatID, opID string) {
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
		q.settleOpUnread(rec, unread, end, stale, fx)
	})
}

// A step's rows go "not read" at the boundary because a finished step carries nothing.
func (q steerQueue) settleOpUnread(rec *steerRecord, unread []*dockRow, end *command.SteerTurnEnd, stale bool, fx *steerFx) {
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
	case len(unread) > 0:
		q.recs.pendEndLocked(rec, unread, *end, fx)
	}
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

// BeginDiscard makes every row the op's, a job's included; it refuses while a clear would race a turn end's
// continuation.
func (q steerQueue) BeginDiscard(chatID marotte.ChatID, opID string) (needsClear bool, refuse string) {
	q.do(chatID, func(rec *steerRecord, fx *steerFx) {
		userInKAS := false
		rec.live(func(w *dockRow) { userInKAS = userInKAS || w.state.inKAS() })
		if (userInKAS || len(rec.others) > 0) && rec.clearRaces() {
			refuse = command.SteerRefuseSettling
			return
		}
		rec.op, rec.opTarget, rec.opEnd, rec.opEndStale, rec.opChannel, rec.opQueue = opID, "", nil, false, rec.channel, false
		rec.live(func(w *dockRow) { w.owner = opID })
		needsClear = userInKAS || len(rec.others) > 0
		if !needsClear {
			rec.discardLocked(opID, fx)
		}
	})
	return needsClear, refuse
}

// DiscardCleared: a row read before the clear landed keeps its read entry; the rest
// leave with none. A clear with no reply leaves the rows the op's for EndOp.
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
			retire(w, nil, fx)
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

func (q steerQueue) DeathDrained(chatID marotte.ChatID, owner string) {
	q.do(chatID, func(rec *steerRecord, _ *steerFx) {
		for _, w := range rec.owned(owner) {
			w.drained = true
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
			sends = append(sends, rec.probeOf(oneSender(waiting), fx))
		}
	})
	return sends
}

// oneSender is the rows of the first row's sender, in order: one probe never joins two senders' words, and the rest
// wait for the channel the probe confirms.
func oneSender(rows []*dockRow) []*dockRow {
	first := rows[0].caller
	return slices.DeleteFunc(slices.Clone(rows), func(w *dockRow) bool {
		if w.caller == nil || first == nil {
			return w.caller != first
		}
		return w.caller.Label != first.Label
	})
}
