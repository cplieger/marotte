package agent

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// steerRecords holds every chat's user steer rows and what KAS's buffer holds of them, in memory. KAS's read
// cursor does not rewind on a clear (kirodotdev/Kiro#11449), so a send after an in-turn clear is one combined
// probe whose fate is observed.
type steerRecords struct {
	chats     map[marotte.ChatID]*steerRecord
	versions  *subject.Versions
	note      func(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) `wiring:"optional"`
	broadcast func(marotte.ServerEvent)                                                                   `wiring:"optional"`
	// promptHeld decides a cleared row's fate when no turn is bound.
	promptHeld func(marotte.ChatID) bool `wiring:"optional"`
	runJob     func(command.SteerJob)
	endSeq     uint64
	mu         sync.Mutex
}

type steerRecord struct {
	exit <-chan struct{}
	// opEnd is the turn end that skipped the in-flight op's rows: the first one,
	// unless a close end follows a stale one (opEndStale), which it replaces.
	opEnd *command.SteerTurnEnd
	// starting is a turn StartTurn began whose bracket has not bound the channel:
	// its execution may already be reading the buffer.
	starting string
	turnID   string
	probe    string
	op       string
	opTarget string
	lead     string
	rows     []dockRow
	others   []marotte.SteerQueuedPayload
	// ends are the turn ends whose rows wait for a close pipeline or a starting
	// prompt, oldest first.
	ends []pendingEnd
	// queue is published by one drainer at a time, so frames and notes go out in
	// the order the steps took the lock.
	queue   []*steerFx
	nextSeq uint64
	channel channelState
	// opChannel is the channel the op found, so a clear with no bound turn resends
	// its kept rows unbound rather than as a probe.
	opChannel channelState
	// reinject: a dead bridge's KAS log may hold rows no clear names, which the next
	// session/load queues again.
	reinject   bool
	gone       bool
	flushing   bool
	opEndStale bool
}

// pendingEnd is a turn end and the owner its collected rows carry.
type pendingEnd struct {
	owner string
	end   command.SteerTurnEnd
}

type dockRow struct {
	key   string
	kasID string
	text  string
	owner string
	seq   uint64
	state rowState
	// sent: the KEY has reached KAS, so it is never sent under the key again.
	sent bool
	// noted: a steer entry exists under the key, so it never gets a second one.
	noted bool
}

type steerFx struct {
	done    chan struct{}
	chat    marotte.ChatID
	frames  []marotte.SteerQueuedPayload
	notes   []steerNote
	jobs    []command.SteerJob
	changed bool
}

type steerNote struct {
	steer *marotte.EntrySteer
	id    string
}

// maxSteerRows bounds one chat: a 65th user row is refused, an agent row evicts
// the oldest agent row.
const maxSteerRows = 64

// maxSteerBytes bounds the user rows' joined text, so the drain's combined prompt
// of every unread row is always one valid prompt.
var maxSteerBytes = command.MaxPromptBytes + marotte.CarryCost("")

func newSteerRecords() *steerRecords {
	return &steerRecords{chats: make(map[marotte.ChatID]*steerRecord)}
}

func (r *steerRecords) record(chatID marotte.ChatID, create bool) *steerRecord {
	rec := r.chats[chatID]
	if create && rec == nil {
		rec = &steerRecord{}
		r.chats[chatID] = rec
	}
	return rec
}

// step runs fn under the lock, then publishes in step order; false for a torn-down chat. Never call it from
// a publication: the drainer would wait on itself.
func (r *steerRecords) step(chatID marotte.ChatID, create bool, fn func(rec *steerRecord, fx *steerFx)) bool {
	fx := &steerFx{chat: chatID, done: make(chan struct{})}
	r.mu.Lock()
	rec := r.record(chatID, create)
	if rec == nil || rec.gone {
		r.mu.Unlock()
		return false
	}
	fn(rec, fx)
	rec.prune()
	if fx.changed || len(fx.frames) > 0 || len(fx.notes) > 0 {
		mintPending(&r.versions)
	}
	r.awaitLocked(rec, fx)
	return true
}

// awaitLocked queues fx behind every earlier step and returns once it is published.
// It is entered holding r.mu and releases it.
func (r *steerRecords) awaitLocked(rec *steerRecord, fx *steerFx) {
	rec.queue = append(rec.queue, fx)
	drain := !rec.flushing
	rec.flushing = true
	r.mu.Unlock()
	if drain {
		r.drain(rec)
	}
	<-fx.done
}

func (r *steerRecords) drain(rec *steerRecord) {
	ctx := context.Background()
	r.mu.Lock()
	for len(rec.queue) > 0 {
		fx := rec.queue[0]
		rec.queue = rec.queue[1:]
		r.mu.Unlock()
		r.publish(ctx, rec, fx)
		close(fx.done)
		r.mu.Lock()
	}
	rec.flushing = false
	r.mu.Unlock()
}

// publish: a torn-down chat's frames are not owed; its notes and jobs still are.
// gone is read after the notes, which can block while a teardown begins.
func (r *steerRecords) publish(ctx context.Context, rec *steerRecord, fx *steerFx) {
	for _, n := range fx.notes {
		if r.note != nil {
			r.note(ctx, fx.chat, n.id, n.steer)
		}
	}
	r.mu.Lock()
	gone := rec.gone
	r.mu.Unlock()
	for _, f := range fx.frames {
		if !gone && r.broadcast != nil {
			r.broadcast(marotte.NewEvent(marotte.EventSteerQueued, fx.chat, f))
		}
	}
	for _, j := range fx.jobs {
		if r.runJob != nil {
			r.runJob(j)
		}
	}
}

func (rec *steerRecord) prune() {
	rec.rows = slices.DeleteFunc(rec.rows, func(w dockRow) bool {
		return w.state == rowDone || (w.state == rowRead && w.owner == "")
	})
	if rec.lead != "" && rec.row(rec.lead) == nil {
		rec.lead = ""
	}
}

// names reports whether turnID is the turn this record knows as starting or bound,
// the one target check delivery shares.
func (rec *steerRecord) names(turnID string) bool {
	return turnID != "" && (rec.starting == turnID || rec.turnID == turnID)
}

func (rec *steerRecord) row(key string) *dockRow {
	for i := range rec.rows {
		if w := &rec.rows[i]; !w.state.terminal() && w.key == key {
			return w
		}
	}
	return nil
}

func (rec *steerRecord) live(fn func(w *dockRow)) {
	for i := range rec.rows {
		if w := &rec.rows[i]; !w.state.terminal() {
			fn(w)
		}
	}
}

func (rec *steerRecord) under(id string) []*dockRow {
	var out []*dockRow
	rec.live(func(w *dockRow) {
		if id != "" && w.kasID == id && (w.state.inKAS() || w.state == rowUnsent) {
			out = append(out, w)
		}
	})
	return out
}

func (rec *steerRecord) unownedWaiting() []*dockRow {
	var out []*dockRow
	rec.live(func(w *dockRow) {
		if w.state == rowWaiting && w.owner == "" {
			out = append(out, w)
		}
	})
	return out
}

func rowFrame(w *dockRow) marotte.SteerQueuedPayload {
	state := marotte.SteerRowQueued
	switch w.state {
	case rowDone:
		state = marotte.SteerRowRemoved
	case rowUnsent:
		state = marotte.SteerRowUnsent
	case rowParked, rowQueued, rowOutstanding, rowWaiting, rowCleared, rowRead, nRowStates:
	}
	return marotte.SteerQueuedPayload{SteerID: w.key, Text: w.text, Origin: marotte.SteerOriginUser, State: state}
}

func batchFrame(id string, members []*dockRow) marotte.SteerQueuedPayload {
	keys := make([]string, 0, len(members))
	texts := make([]string, 0, len(members))
	for _, w := range members {
		keys = append(keys, w.key)
		texts = append(texts, w.text)
	}
	return marotte.SteerQueuedPayload{
		SteerID: id, Text: joinSteers(texts), Origin: marotte.SteerOriginUser,
		Replaces: keys, State: marotte.SteerRowQueued,
	}
}

func joinSteers(texts []string) string { return strings.Join(texts, "\n\n") }

func freshSteerID() string { return marotte.SteerIDFor(ids.NewMessageID()) }

// setState owes a row frame only when the WIRE state moves: every state but
// unsent reads queued, so a row moving between them repaints nothing.
func setState(w *dockRow, s rowState, fx *steerFx) {
	was := w.state == rowUnsent
	w.state = s
	if was != (s == rowUnsent) {
		fx.frames = append(fx.frames, rowFrame(w))
	}
	fx.changed = true
}

// retire leaves a row whose key already carries its one entry with a removed
// frame, the only signal that takes it off a client's dock.
func retire(w *dockRow, steer *marotte.EntrySteer, fx *steerFx) {
	w.state = rowDone
	fx.changed = true
	if w.noted || steer == nil {
		fx.frames = append(fx.frames, rowFrame(w))
		return
	}
	w.noted = true
	fx.notes = append(fx.notes, steerNote{id: w.key, steer: steer})
}

func deletedSteer(w *dockRow) *marotte.EntrySteer {
	return &marotte.EntrySteer{
		Text: w.text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonDeleted,
	}
}

func boundarySteer(w *dockRow) *marotte.EntrySteer {
	return &marotte.EntrySteer{
		Text: w.text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
	}
}

func (r *steerRecords) gone(chatID marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[chatID]
	return rec != nil && rec.gone
}

// SteerWaiting folds steering_queued. A frame naming this record's rows is
// published by the record in step order, so ok is false; an agent row answers
// itself for the caller to broadcast.
func (r *steerRecords) SteerWaiting(chatID marotte.ChatID, in *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
	p := *in
	if p.SteerID == "" {
		return p, false
	}
	other := false
	r.step(chatID, true, func(rec *steerRecord, fx *steerFx) {
		if members := rec.under(p.SteerID); len(members) > 0 {
			frame := batchFrame(p.SteerID, members)
			if len(members) == 1 && members[0].key == p.SteerID {
				frame = rowFrame(members[0])
			}
			fx.frames = append(fx.frames, frame)
			return
		}
		other = true
		p.State = marotte.SteerRowQueued
		rec.others = slices.DeleteFunc(rec.others, func(o marotte.SteerQueuedPayload) bool { return o.SteerID == p.SteerID })
		if len(rec.others) >= maxSteerRows {
			rec.others = rec.others[1:]
		}
		rec.others = append(rec.others, p)
		fx.changed = true
	})
	return p, other
}

// SteerRead folds steering_injected.
func (r *steerRecords) SteerRead(chatID marotte.ChatID, steerID string) {
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		rec.readLocked(steerID, false, fx)
	})
}

// SteerForgotten folds an acknowledgement-evidenced read and answers what it read,
// with the text a lane's read entry needs.
func (r *steerRecords) SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	var out []marotte.SteerQueuedPayload
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		for _, id := range steerIDs {
			if p, ok := rec.takeOther(id); ok {
				fx.changed = true
				out = append(out, p)
				continue
			}
			if p, ok := rec.readLocked(id, true, fx); ok {
				out = append(out, p)
			}
		}
	})
	return out
}

func (rec *steerRecord) takeOther(id string) (marotte.SteerQueuedPayload, bool) {
	for i, o := range rec.others {
		if o.SteerID == id {
			rec.others = slices.Delete(rec.others, i, i+1)
			return o, true
		}
	}
	return marotte.SteerQueuedPayload{}, false
}

// readLocked: byAck is a delegate's read, which proves nothing about the main
// cursor reaching new rows.
func (rec *steerRecord) readLocked(id string, byAck bool, fx *steerFx) (marotte.SteerQueuedPayload, bool) {
	if !byAck {
		if _, ok := rec.takeOther(id); ok {
			fx.changed = true
		}
	}
	members := rec.under(id)
	if len(members) == 0 {
		return marotte.SteerQueuedPayload{}, false
	}
	read := batchFrame(id, members)
	ev := readEvent(members, byAck)
	for _, w := range members {
		if rowRuleFor(w.state, ev) == rRead {
			w.state = rowRead
		}
	}
	fx.changed = true
	if rec.channel == chanProbing && id == rec.probe {
		rec.probeReadLocked(ev, fx)
	}
	return marotte.SteerQueuedPayload{SteerID: id, Text: read.Text, Origin: marotte.SteerOriginUser}, true
}

// readEvent names a probe's read whether or not it is still the channel's: a late
// read after its turn closed is a fact the turn-end job reads.
func readEvent(members []*dockRow, byAck bool) steerEvent {
	if !slices.ContainsFunc(members, func(w *dockRow) bool { return w.state == rowOutstanding }) {
		return evInjectedOther
	}
	if byAck {
		return evAckProbe
	}
	return evInjectedProbe
}

func (rec *steerRecord) probeReadLocked(ev steerEvent, fx *steerFx) {
	switch channelRule(rec.channel, ev) {
	case cOpen:
		rec.probe = ""
		rec.channel = chanOpen
		if len(rec.unownedWaiting()) > 0 {
			fx.jobs = append(fx.jobs, command.SteerJob{Chat: fx.chat})
		}
	case cOwed:
		rec.owe(fx)
	case cUnhandled, cImpossible, cKeep, cNone, cUnconfirmed, cProbing, cResubmit, cFlush:
	}
}

// owe hands un-owned waiting rows to a job as the next probe; with none waiting the
// channel is UNCONFIRMED.
func (rec *steerRecord) owe(fx *steerFx) {
	rec.probe = ""
	if len(rec.unownedWaiting()) == 0 {
		rec.channel = chanUnconfirmed
		return
	}
	rec.channel = chanProbing
	fx.jobs = append(fx.jobs, command.SteerJob{Chat: fx.chat})
}

// SteerCleared folds steering_cleared, ours or anyone's, and answers the AGENT rows
// it named. A user row gets no entry at a clear: a resubmitted row is never noted
// "Not read".
func (r *steerRecords) SteerCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	var out []marotte.SteerQueuedPayload
	promptHeld := r.heldByPrompt(chatID)
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		named := false
		for _, id := range steerIDs {
			if p, ok := rec.takeOther(id); ok {
				fx.changed = true
				out = append(out, p)
			}
			if rec.clearUnderLocked(id, promptHeld, fx) {
				named = true
			}
		}
		if named && channelRule(rec.channel, evForeignClear) == cOwed {
			rec.owe(fx)
		}
	})
	return out
}

// clearUnderLocked: with no turn and no held prompt to decide them, the rows the
// clear named are unsent.
func (rec *steerRecord) clearUnderLocked(id string, promptHeld bool, fx *steerFx) (named bool) {
	for _, w := range rec.under(id) {
		if !w.state.inKAS() {
			continue
		}
		named = true
		next := rowCleared
		if rec.channel == chanNone && w.owner == "" && !promptHeld {
			next = rowUnsent
		}
		if rowRuleFor(w.state, evForeignClear) == rCleared {
			setState(w, next, fx)
		}
	}
	if id == rec.probe && rec.channel == chanProbing {
		rec.probe = ""
	}
	return named
}

func (r *steerRecords) heldByPrompt(chatID marotte.ChatID) bool {
	return r.promptHeld != nil && r.promptHeld(chatID)
}

// TakeAgentRows drains the agent rows a dead bridge leaves unread, for the death
// closer's text-less entries. It works on a torn-down record too.
func (r *steerRecords) TakeAgentRows(chatID marotte.ChatID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[chatID]
	if rec == nil || len(rec.others) == 0 {
		return nil
	}
	out := make([]string, 0, len(rec.others))
	for _, o := range rec.others {
		out = append(out, o.SteerID)
	}
	rec.others = nil
	mintPending(&r.versions)
	return out
}

// TurnStarted records the turn StartTurn began. It runs under the turn registry's
// lock, so it must not wait on a publication.
func (r *steerRecords) TurnStarted(chatID marotte.ChatID, turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec := r.record(chatID, true); !rec.gone {
		rec.starting = turnID
	}
}

// TurnBound opens the channel for a turn that became own; unbound rows already in
// KAS join it, because that turn's execution is the one whose cursor reads them.
func (r *steerRecords) TurnBound(chatID marotte.ChatID, turnID string) {
	r.step(chatID, true, func(rec *steerRecord, fx *steerFx) {
		if channelRule(rec.channel, evBind) == cImpossible {
			slog.Warn("a steer channel was still open when another turn bound", "chat_id", chatID, "turn", rec.turnID)
			rec.endLocked(r, command.SteerTurnEnd{TurnID: rec.turnID}, true, fx)
		}
		rec.turnID = turnID
		rec.channel = chanOpen
		rec.probe = ""
		rec.starting = ""
	})
}

func (r *steerRecords) TurnRevised(chatID marotte.ChatID, turnID string) {
	r.step(chatID, false, func(rec *steerRecord, _ *steerFx) {
		if channelRule(rec.channel, evRevise) == cKeep {
			rec.turnID = turnID
		}
	})
}

// TurnEnded hands the closing turn's un-owned rows to a pending end, which the
// close's pipeline (or the next starting prompt) resolves; a row an op owns
// records the end for the op.
func (r *steerRecords) TurnEnded(chatID marotte.ChatID, end command.SteerTurnEnd) {
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		rec.endLocked(r, end, false, fx)
	})
}

// endLocked collects what a turn end takes. A stale bind has no pipeline to
// resolve a pending end, so its unowned rows are settled here and a flush job
// routes them.
func (rec *steerRecord) endLocked(r *steerRecords, end command.SteerTurnEnd, stale bool, fx *steerFx) {
	own := end.TurnID != "" && end.TurnID == rec.turnID
	unbound := rec.channel == chanNone && end.Source.PromptClass()
	ev := evTurnEnd
	if end.BridgeDeath {
		ev = evTurnDeath
	}
	if end.TurnID != "" && end.TurnID == rec.starting {
		rec.starting = ""
	}
	collected := rec.collectEnded(end, own, unbound, ev, stale)
	if own && channelRule(rec.channel, ev) == cNone {
		rec.turnID = ""
		rec.channel = chanNone
		rec.probe = ""
	}
	switch {
	case len(collected) == 0:
	case stale:
		for _, w := range collected {
			settleStaleLocked(w, fx)
		}
		fx.jobs = append(fx.jobs, command.SteerJob{Chat: fx.chat})
	default:
		r.pendEndLocked(rec, collected, end, fx)
	}
}

// settleStaleLocked: a row KAS holds is released to the binding execution, whose
// fresh cursor reads it or whose own end collects it again; a record-only row is
// unsent.
func settleStaleLocked(w *dockRow, fx *steerFx) {
	w.owner = ""
	if !w.state.inKAS() && w.state != rowRead {
		setState(w, rowUnsent, fx)
	}
	fx.changed = true
}

func (rec *steerRecord) collectEnded(end command.SteerTurnEnd, own, unbound bool, ev steerEvent, stale bool) []*dockRow {
	var collected []*dockRow
	adopt := rec.deleting() && rec.opEnd == nil
	rec.live(func(w *dockRow) {
		if !endCollects(w.state, own, unbound, end.Source.PromptClass()) || rowRuleFor(w.state, ev) != rCollect {
			return
		}
		if end.BridgeDeath && w.state.inKAS() {
			rec.reinject = true
		}
		if w.owner == "" && adopt {
			w.owner = rec.op
		}
		if w.owner != "" {
			rec.noteOpEnd(w, end, stale)
			return
		}
		collected = append(collected, w)
	})
	return collected
}

// deleting: one boundary under a delete is one send, so its first turn end takes every
// row the turn leaves. A discard takes none: a row sent after it began is not its to drop.
func (rec *steerRecord) deleting() bool { return rec.op != "" && rec.opTarget != "" }

// noteOpEnd keeps the first end an op saw, except that a close end replaces a
// stale bind's: a close has a pipeline waiting on the op's lock, a stale bind none.
func (rec *steerRecord) noteOpEnd(w *dockRow, end command.SteerTurnEnd, stale bool) {
	if w.owner != rec.op || (rec.opEnd != nil && (stale || !rec.opEndStale)) {
		return
	}
	rec.opEnd = &end
	rec.opEndStale = stale
}

func (r *steerRecords) pendEndLocked(rec *steerRecord, rows []*dockRow, end command.SteerTurnEnd, fx *steerFx) {
	r.endSeq++
	owner := "end-" + strconv.FormatUint(r.endSeq, 10)
	for _, w := range rows {
		w.owner = owner
	}
	rec.ends = append(rec.ends, pendingEnd{owner: owner, end: end})
	fx.changed = true
}

// endCollects: a closing turn takes the rows KAS holds for it, the rows its channel
// owns, and the parked rows when it is prompt-class.
func endCollects(s rowState, own, unbound, promptClass bool) bool {
	switch s {
	case rowQueued, rowCleared:
		return own || unbound
	case rowOutstanding, rowWaiting:
		return own
	case rowParked:
		return promptClass
	case rowUnsent, rowRead, rowDone, nRowStates:
	}
	return false
}

// BridgeGone leaves the rows a dead buffer held with no turn to collect them unsent
// for the chat's next prompt; KAS's log may re-inject them on load.
func (r *steerRecords) BridgeGone(chatID marotte.ChatID) {
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		if rec.channel != chanNone {
			return
		}
		rec.live(func(w *dockRow) {
			if w.owner == "" && w.state.inKAS() {
				rec.reinject = true
				setState(w, rowUnsent, fx)
			}
		})
	})
}

// BeginTeardown runs before the teardown's cancel, so every later fold, job and op finds the record gone. It
// answers the unread rows for the close's notes, keeps agent rows for the death closer, and returns once
// earlier steps published.
func (r *steerRecords) BeginTeardown(chatID marotte.ChatID, exit <-chan struct{}) []marotte.SteerQueuedPayload {
	r.mu.Lock()
	rec := r.record(chatID, true)
	if rec.gone {
		r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
		return nil
	}
	var unread []marotte.SteerQueuedPayload
	rec.live(func(w *dockRow) {
		if !w.noted && rowRuleFor(w.state, evTeardown) == rDrop {
			unread = append(unread, marotte.SteerQueuedPayload{SteerID: w.key, Text: w.text, Origin: marotte.SteerOriginUser})
		}
	})
	rec.rows, rec.ends = nil, nil
	rec.op, rec.opEnd, rec.opEndStale, rec.lead, rec.reinject, rec.starting = "", nil, false, "", false, ""
	rec.channel, rec.turnID, rec.probe = chanNone, "", ""
	rec.exit, rec.gone = exit, true
	mintPending(&r.versions)
	r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
	return unread
}

// shutdownRow is one row a shutdown takes: KASHeld when KAS may hold its words (textless entry, decided at next
// boot), Agent for a workflow's row.
type shutdownRow struct {
	Key     string
	Text    string
	KASHeld bool
	Agent   bool
}

// ShutdownTake marks the record gone and takes every live row in one section, lead first, then the agent rows;
// it returns once earlier steps published.
func (r *steerRecords) ShutdownTake(chatID marotte.ChatID) []shutdownRow {
	r.mu.Lock()
	rec := r.chats[chatID]
	if rec == nil || rec.gone {
		r.mu.Unlock()
		return nil
	}
	var live []*dockRow
	rec.live(func(w *dockRow) {
		if w.state != rowRead {
			live = append(live, w)
		}
	})
	sortLeadFirst(live, rec.lead)
	out := make([]shutdownRow, 0, len(live)+len(rec.others))
	for _, w := range live {
		held := w.state.inKAS() || (w.state == rowUnsent && rec.reinject && w.sent)
		out = append(out, shutdownRow{Key: w.key, Text: w.text, KASHeld: held})
	}
	for _, o := range rec.others {
		out = append(out, shutdownRow{Key: o.SteerID, Agent: true})
	}
	rec.rows, rec.others, rec.ends = nil, nil, nil
	rec.op, rec.opEnd, rec.opEndStale, rec.lead, rec.starting = "", nil, false, "", ""
	rec.channel, rec.turnID, rec.probe, rec.gone = chanNone, "", "", true
	mintPending(&r.versions)
	r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
	return out
}

// sortLeadFirst orders rows the lead first, then by acceptance.
func sortLeadFirst(rows []*dockRow, lead string) {
	slices.SortStableFunc(rows, func(a, b *dockRow) int {
		if (a.key == lead) != (b.key == lead) {
			if a.key == lead {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.seq, b.seq)
	})
}

// EndTeardown forgets the record once the chat's last forward goroutine and every
// queued step have drained, bounded, so a late fold cannot recreate it for a chat
// that is gone.
func (r *steerRecords) EndTeardown(chatID marotte.ChatID) {
	r.mu.Lock()
	rec := r.chats[chatID]
	r.mu.Unlock()
	if rec == nil {
		return
	}
	if rec.exit != nil {
		select {
		case <-rec.exit:
		case <-time.After(command.ResendBridgeWait):
			slog.Warn("a torn-down chat's bridge did not drain in time", "chat_id", chatID)
		}
	}
	r.mu.Lock()
	r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chats[chatID] == rec {
		delete(r.chats, chatID)
	}
}

// List answers the connect replay: every visible row with its wire state, a batch
// frame for every id a row is held under that is not its key, then agent rows.
func (r *steerRecords) List(chatFilter marotte.ChatID) []marotte.ServerEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	chats := make([]marotte.ChatID, 0, len(r.chats))
	for id, rec := range r.chats {
		if !rec.gone && (chatFilter == "" || id == chatFilter) {
			chats = append(chats, id)
		}
	}
	slices.Sort(chats)
	var out []marotte.ServerEvent
	for _, id := range chats {
		out = r.chats[id].appendReplay(out, id)
	}
	return out
}

func (rec *steerRecord) appendReplay(out []marotte.ServerEvent, id marotte.ChatID) []marotte.ServerEvent {
	batches := map[string][]*dockRow{}
	var order []string
	rec.live(func(w *dockRow) {
		out = append(out, marotte.NewEvent(marotte.EventSteerQueued, id, rowFrame(w)))
		if w.kasID != "" && w.kasID != w.key {
			if _, seen := batches[w.kasID]; !seen {
				order = append(order, w.kasID)
			}
			batches[w.kasID] = append(batches[w.kasID], w)
		}
	})
	for _, kas := range order {
		out = append(out, marotte.NewEvent(marotte.EventSteerQueued, id, batchFrame(kas, batches[kas])))
	}
	for _, o := range rec.others {
		out = append(out, marotte.NewEvent(marotte.EventSteerQueued, id, o))
	}
	return out
}

// SetLead records the send-now arrow's row, sent first until it is sent or
// terminal.
func (r *steerRecords) SetLead(chatID marotte.ChatID, key string) (undo func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.record(chatID, false)
	if rec == nil || rec.gone || key == "" {
		return func() {}
	}
	rec.lead = key
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if rec.lead == key {
			rec.lead = ""
		}
	}
}

// NeedsPostLoadClear: KAS may hold copies this record also holds, nothing bound is
// reading, and no agent row would be lost to the clear.
func (r *steerRecords) NeedsPostLoadClear(chatID marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[chatID]
	return rec != nil && !rec.gone && rec.reinject && rec.turnID == "" && len(rec.others) == 0
}

func (b *bus) SteerWaiting(chatID marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
	return b.steers.SteerWaiting(chatID, p)
}

func (b *bus) SteerRead(chatID marotte.ChatID, steerID string) {
	b.steers.SteerRead(chatID, steerID)
}

func (b *bus) SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return b.steers.SteerForgotten(chatID, steerIDs)
}

func (b *bus) SteerCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return b.steers.SteerCleared(chatID, steerIDs)
}
