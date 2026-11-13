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

// steerRecords holds every steering buffer's user rows and what KAS's buffer holds of them, in memory, keyed by
// the session a record steers: a chat id names the chat's own session, marotte.StepSteerKey a run step's. KAS's
// read cursor does not rewind on a clear (kirodotdev/Kiro#11449), so a send after an in-turn clear is one
// combined probe whose fate is observed.
type steerRecords struct {
	chats     map[marotte.ChatID]*steerRecord
	versions  *subject.Versions
	note      func(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) `wiring:"optional"`
	broadcast func(marotte.ServerEvent)                                                                   `wiring:"optional"`
	// stepEvent addresses a step key's row frame to its run tab; nil drops step frames.
	stepEvent func(key marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.ServerEvent, bool) `wiring:"optional"`
	// promptHeld decides a cleared row's fate when no turn is bound.
	promptHeld func(marotte.ChatID) bool `wiring:"optional"`
	runJob     func(command.SteerJob)
	// stepJob runs a step key's flush on the step's host session; a chat key's goes to runJob.
	stepJob func(command.SteerJob) `wiring:"optional"`
	// onRunFacts wakes the chat's run list when a step moved its pending turn ends (runFacts) or an entry row's fate.
	// Called under mu, where a chat's lifecycle lock may be held too, so it only starts work on another goroutine.
	onRunFacts func(marotte.ChatID) `wiring:"optional"`
	endSeq     uint64
	// factsSeq counts the steps that moved the run facts, so the run list drops an older read than one it applied.
	factsSeq uint64
	mu       sync.Mutex
}

type steerRecord struct {
	exit <-chan struct{}
	// opEnd is the turn end that skipped the in-flight op's rows: the first one,
	// unless a close end follows a stale one (opEndStale), which it replaces.
	opEnd *command.SteerTurnEnd
	// starting is a turn startTurn began whose bracket has not bound the channel:
	// its execution may already be reading the buffer.
	starting string
	turnID   string
	probe    string
	op       string
	opTarget string
	lead     string
	rows     []dockRow
	others   []marotte.SteerQueuedPayload
	// fates are the entry rows' fates not yet taken by the chat's run list (takeEntryFates), in the order they met
	// them.
	fates []entryFate
	// endHeld are the rows KAS held as a turn end's window ran: the agent rows at the end, and every row KAS holds
	// when a continuation binds. That continuation still runs for them, so no clear may race it until each is read
	// or gone (prune drops it).
	endHeld []wireKey
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
	reinject bool
	// settling: set when the own turn's end folds, cleared when a turn binds or KAS reports idle; KAS may be
	// deciding a continuation meanwhile. clearRaces is the clear refusal.
	settling   bool
	gone       bool
	flushing   bool
	opEndStale bool
	// opQueue: the op is a queue-instead, so its target's words go back to the chat's run list.
	opQueue bool
	// noCarry marks a run step's record: a finished step stays finished, so its turn end leaves every
	// unread row "not read" instead of carrying it.
	noCarry bool
}

type pendingEnd struct {
	owner string
	end   command.SteerTurnEnd
}

type dockRow struct {
	// caller is the API caller that sent the row, nil for the user's.
	caller *marotte.Caller
	key    string
	kasID  string
	text   string
	owner  string
	// entry is the run-list entry the chat's list offered as this row ("" for a row routed here first), and offer
	// that offer's token: the row's fates go to the list (settleEntries), which owns its words.
	entry string
	seq   uint64
	offer uint64
	state rowState
	// fate is the last fate reported for entry.
	fate steerFate
	// sent: the KEY has reached KAS, so it is never sent under the key again.
	sent bool
	// noted: a steer entry exists under the key, so it never gets a second one.
	noted bool
	// back is the fate a clear gave this entry row when it handed the words back to the run list.
	back steerFate
	// acked: KAS answered the send queued, or the answer was lost while KAS may hold it; refused: it answered not
	// queued.
	acked, refused bool
	// drained: the row's turn died with a bridge that drained, so kiro-cli's buffer for it is gone (DeathDrained).
	drained bool
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

// maxSteerBytes bounds the user rows' joined text, so the one prompt a turn end's
// unread rows join into is always a valid prompt.
var maxSteerBytes = command.MaxPromptBytes + marotte.CarryCost("")

func newSteerRecords() *steerRecords {
	return &steerRecords{chats: make(map[marotte.ChatID]*steerRecord)}
}

func (r *steerRecords) record(chatID marotte.ChatID, create bool) *steerRecord {
	rec := r.chats[chatID]
	if create && rec == nil {
		_, step := chatID.StepSession()
		rec = &steerRecord{noCarry: step}
		r.chats[chatID] = rec
	}
	return rec
}

// runFacts are the record's facts the chat's run list reads, never under the lifecycle lock: the pending turn ends
// and the sequence of the read. A final fate the list has not taken counts as an end owed, so the list takes no turn
// before it applies that fate: a wake behind the words a turn end gave back would take their turn.
func (r *steerRecords) runFacts(chatID marotte.ChatID) (ends int, seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.record(chatID, false)
	if rec == nil || rec.gone {
		return 0, r.factsSeq
	}
	ends = len(rec.ends)
	for _, f := range rec.fates {
		if f.fate.final() {
			ends++
		}
	}
	return ends, r.factsSeq
}

// takeEntryFates hands the chat's run list the fates its entry rows met since the last take, in order. Never called
// under the lifecycle lock.
func (r *steerRecords) takeEntryFates(chatID marotte.ChatID) []entryFate {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.record(chatID, false)
	if rec == nil {
		return nil
	}
	fs := rec.fates
	rec.fates = nil
	return fs
}

// settleEntries records each entry row's new fate for the run list, and retires a row whose words went back to the
// list: KAS refused it, a clear of the user's handed it back, or a turn end left it unread with no copy a session/load
// may re-inject. It runs before prune, which drops a read or removed row.
func (rec *steerRecord) settleEntries(fx *steerFx) (moved bool) {
	for i := range rec.rows {
		w := &rec.rows[i]
		if w.entry == "" || w.fate.final() {
			continue
		}
		f := rec.fateOf(w)
		if f == fateNone || f == w.fate {
			continue
		}
		w.fate = f
		rec.fates = append(rec.fates, entryFate{id: w.entry, offer: w.offer, fate: f})
		if f == fateBack || f == fateUnread || f == fateCleared {
			w.owner = ""
			retire(w, nil, fx)
		}
		moved = true
	}
	return moved
}

// fateOf is what entry row w's state says of its offer. A row an op or a pending end owns waits for that owner,
// which decides whether a continuation read it.
func (rec *steerRecord) fateOf(w *dockRow) steerFate {
	switch {
	case w.state == rowRead:
		return fateRead
	case w.state == rowDone:
		return fateGone
	case w.owner != "":
	case w.back != fateNone:
		return w.back
	case w.state == rowCleared && w.refused:
		return fateBack
	case (w.state == rowUnsent || w.state == rowParked) && (!rec.reinject || w.drained):
		return fateUnread
	}
	if w.acked && w.state.inKAS() {
		return fateInKAS
	}
	return fateNone
}

func (r *steerRecords) eventFor(key marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.ServerEvent, bool) {
	if _, step := key.StepSession(); !step {
		return marotte.NewEvent(marotte.EventSteerQueued, key, *p), true
	}
	if r.stepEvent == nil {
		return marotte.ServerEvent{}, false
	}
	return r.stepEvent(key, p)
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
	before := rec.wires()
	ends := len(rec.ends)
	fn(rec, fx)
	fated := rec.settleEntries(fx)
	rec.prune()
	rec.reframe(fx, before)
	if fx.changed || len(fx.frames) > 0 || len(fx.notes) > 0 {
		mintPending(&r.versions)
	}
	moved := fated || ends != len(rec.ends)
	if moved {
		r.factsSeq++
	}
	r.awaitLocked(rec, fx)
	if moved && r.onRunFacts != nil {
		r.onRunFacts(chatID)
	}
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
	if !gone && r.broadcast != nil {
		for i := range fx.frames {
			if ev, ok := r.eventFor(fx.chat, &fx.frames[i]); ok {
				r.broadcast(ev)
			}
		}
	}
	for _, j := range fx.jobs {
		r.runSteerJob(j)
	}
}

func (r *steerRecords) runSteerJob(j command.SteerJob) {
	run := r.runJob
	if _, step := j.Chat.StepSession(); step {
		run = r.stepJob
	}
	if run != nil {
		run(j)
	}
}

func (rec *steerRecord) prune() {
	rec.rows = slices.DeleteFunc(rec.rows, func(w dockRow) bool {
		return w.state == rowDone || (w.state == rowRead && w.owner == "")
	})
	if rec.lead != "" && rec.row(rec.lead) == nil {
		rec.lead = ""
	}
	rec.endHeld = slices.DeleteFunc(rec.endHeld, func(k wireKey) bool { return !rec.inKASNow(k) })
}

// inKASNow: KAS still holds k's row; a read, a clear naming it, an eviction or a retirement ends that.
func (rec *steerRecord) inKASNow(k wireKey) bool {
	if k.agent {
		return slices.ContainsFunc(rec.others, func(o marotte.SteerQueuedPayload) bool { return o.SteerID == k.id })
	}
	w := rec.row(k.id)
	return w != nil && w.state.inKAS()
}

// awaitsPrompt: the unsent rows wait for the next prompt's post-load clear, since KAS may re-inject them.
func (rec *steerRecord) awaitsPrompt() bool { return rec.reinject }

// rowWire is what a live row's frame tells a client: a step that moves it owes the row a frame.
type rowWire struct {
	state  marotte.SteerRowState
	awaits bool
}

// wireKey keeps an agent row's id apart from a user row's key.
type wireKey struct {
	id    string
	agent bool
}

func (rec *steerRecord) wires() map[wireKey]rowWire {
	out := make(map[wireKey]rowWire, len(rec.rows)+len(rec.others))
	rec.live(func(w *dockRow) {
		f := rec.frameOf(w)
		out[wireKey{id: w.key}] = rowWire{f.State, f.AwaitsPrompt}
	})
	for i := range rec.others {
		out[wireKey{id: rec.others[i].SteerID, agent: true}] = rowWire{state: rec.otherFrame(&rec.others[i]).State}
	}
	return out
}

// reframe is the one owner of a live row's wire state: a row frame the step queued takes the row as the step left
// it, and a row whose wire state moved with no frame of its own gets one. delivering and awaitsPrompt follow the
// record (a turn end, KAS going idle, a prompt opening), so a row can move with no change of its own. A new agent
// row is not framed here: SteerWaiting answers its frame for the caller to broadcast.
func (rec *steerRecord) reframe(fx *steerFx, before map[wireKey]rowWire) {
	rec.live(func(w *dockRow) {
		now := rec.frameOf(w)
		framed := false
		for i := range fx.frames {
			if f := &fx.frames[i]; f.SteerID == w.key && len(f.Replaces) == 0 {
				f.State, f.AwaitsPrompt = now.State, now.AwaitsPrompt
				framed = true
			}
		}
		if was, ok := before[wireKey{id: w.key}]; !framed && (!ok || was != (rowWire{now.State, now.AwaitsPrompt})) {
			fx.frames = append(fx.frames, now)
		}
	})
	for i := range rec.others {
		now := rec.otherFrame(&rec.others[i])
		if was, ok := before[wireKey{id: now.SteerID, agent: true}]; ok && was.state != now.State {
			fx.frames = append(fx.frames, now)
		}
	}
}

// otherFrame is an agent row's frame as the record reads it: delivering while a clear would race a turn end.
func (rec *steerRecord) otherFrame(in *marotte.SteerQueuedPayload) marotte.SteerQueuedPayload {
	o := *in
	o.State = marotte.SteerRowQueued
	if rec.clearRaces() {
		o.State = marotte.SteerRowDelivering
	}
	return o
}

// frameOf is w's frame as the record reads it, the record-wide delivering and awaitsPrompt included.
func (rec *steerRecord) frameOf(w *dockRow) marotte.SteerQueuedPayload {
	f := rowFrame(w)
	switch {
	case rec.delivering(w):
		f.State = marotte.SteerRowDelivering
	case f.State == marotte.SteerRowUnsent:
		f.AwaitsPrompt = rec.awaitsPrompt()
	}
	return f
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

// rowFrame is w's frame from its own state; frameOf adds what the record decides.
func rowFrame(w *dockRow) marotte.SteerQueuedPayload {
	state := marotte.SteerRowQueued
	switch w.state {
	case rowDone:
		state = marotte.SteerRowRemoved
	case rowUnsent:
		state = marotte.SteerRowUnsent
	case rowParked, rowQueued, rowOutstanding, rowWaiting, rowCleared, rowRead, nRowStates:
	}
	return marotte.SteerQueuedPayload{SteerID: w.key, Text: w.text, Origin: marotte.SteerOriginUser, State: state, Caller: w.caller}
}

func batchFrame(id string, members []*dockRow) marotte.SteerQueuedPayload {
	keys := make([]string, 0, len(members))
	texts := make([]string, 0, len(members))
	callers := make([]*marotte.Caller, 0, len(members))
	for _, w := range members {
		keys = append(keys, w.key)
		texts = append(texts, w.text)
		callers = append(callers, w.caller)
	}
	return marotte.SteerQueuedPayload{
		SteerID: id, Text: joinSteers(texts), Origin: marotte.SteerOriginUser,
		Replaces: keys, State: marotte.SteerRowQueued, Caller: marotte.SharedCaller(callers),
	}
}

func joinSteers(texts []string) string { return strings.Join(texts, "\n\n") }

func freshSteerID() string { return marotte.SteerIDFor(ids.NewMessageID()) }

// setState moves a live row; the step's reframe owes its frame only when the WIRE state moves.
func setState(w *dockRow, s rowState, fx *steerFx) {
	w.state = s
	fx.changed = true
}

// retire ends a row. One that writes no new entry (already noted, or steer nil)
// leaves on a removed frame, the only signal that takes it off a client's dock.
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

func boundarySteer(w *dockRow) *marotte.EntrySteer {
	return &marotte.EntrySteer{
		Text: w.text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
		Caller: w.caller,
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
		p = rec.otherFrame(&p)
		fx.changed = true
	})
	return p, other
}

// SteerRead folds steering_injected and answers what the read entry takes from the rows: in Replaces the row keys
// steerID carries when it is not a row's own key (nil otherwise), for its Resends, and the rows' Caller.
func (r *steerRecords) SteerRead(chatID marotte.ChatID, steerID string) (read marotte.SteerQueuedPayload) {
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		read, _ = rec.readLocked(steerID, false, fx)
	})
	return read
}

// SteerForgotten folds an acknowledgement-evidenced read and answers what it read,
// with the text and the carried row keys (Replaces) a lane's read entry needs.
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
	for i := range rec.others {
		if o := rec.others[i]; o.SteerID == id {
			rec.others = slices.Delete(rec.others, i, i+1)
			return o, true
		}
	}
	return marotte.SteerQueuedPayload{}, false
}

// readLocked answers the read text, and in Replaces the row keys an id that is not
// its one row's own key carries. byAck is a delegate's read, which proves nothing
// about the main cursor reaching new rows.
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
	out := marotte.SteerQueuedPayload{SteerID: id, Text: read.Text, Origin: marotte.SteerOriginUser, Caller: read.Caller}
	if len(members) > 1 || members[0].key != id {
		out.Replaces = read.Replaces
	}
	return out, true
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

// With none waiting the channel is UNCONFIRMED.
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

// takeAgentRows drains the agent rows a dead bridge leaves unread, for the death
// closer's text-less entries. It works on a torn-down record too.
func (r *steerRecords) takeAgentRows(chatID marotte.ChatID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[chatID]
	if rec == nil || len(rec.others) == 0 {
		return nil
	}
	out := make([]string, 0, len(rec.others))
	for i := range rec.others {
		out = append(out, rec.others[i].SteerID)
	}
	rec.others, rec.endHeld = nil, nil
	mintPending(&r.versions)
	return out
}

// turnStarted records the turn startTurn began. It runs under the turn registry's
// lock, so it must not wait on a publication.
func (r *steerRecords) turnStarted(chatID marotte.ChatID, turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec := r.record(chatID, true); !rec.gone {
		rec.starting = turnID
	}
}

// turnBound opens the channel for a turn that became own; unbound rows already in
// KAS join it, because that turn's execution is the one whose cursor reads them.
func (r *steerRecords) turnBound(chatID marotte.ChatID, turnID string) {
	r.step(chatID, true, func(rec *steerRecord, fx *steerFx) {
		if channelRule(rec.channel, evBind) == cImpossible {
			slog.Warn("a steer channel was still open when another turn bound", "chat_id", chatID, "turn", rec.turnID)
			rec.endLocked(r, command.SteerTurnEnd{TurnID: rec.turnID}, true, fx)
		}
		rec.turnID = turnID
		rec.channel = chanOpen
		rec.probe = ""
		rec.starting = ""
		if rec.settling {
			rec.holdAtBind()
		}
		rec.settling = false
	})
}

// holdAtBind keeps the window open for every row KAS holds when a turn binds inside it: KAS admits no prompt
// while busy, so that turn is the end's continuation, and a row KAS took after the end folded may be its reason.
func (rec *steerRecord) holdAtBind() {
	hold := func(k wireKey) {
		if !slices.Contains(rec.endHeld, k) {
			rec.endHeld = append(rec.endHeld, k)
		}
	}
	rec.live(func(w *dockRow) {
		if w.state.inKAS() && !rec.endOwned(w) {
			hold(wireKey{id: w.key})
		}
	})
	for i := range rec.others {
		hold(wireKey{id: rec.others[i].SteerID, agent: true})
	}
}

// kasSettled clears settling and endHeld when KAS reports idle; the end's rows stay guarded until resolveEnds drops
// the end.
func (r *steerRecords) kasSettled(chatID marotte.ChatID) {
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		if rec.settling || len(rec.endHeld) > 0 {
			rec.settling, rec.endHeld = false, nil
			fx.changed = true
		}
	})
}

// turnEnded hands the closing turn's un-owned rows to a pending end, which the
// close's pipeline resolves once KAS is idle (else a continuation's close, or the
// next starting prompt); a row an op owns records the end for the op.
func (r *steerRecords) turnEnded(chatID marotte.ChatID, end command.SteerTurnEnd) {
	r.step(chatID, false, func(rec *steerRecord, fx *steerFx) {
		rec.endLocked(r, end, false, fx)
	})
}

// A stale bind has no pipeline to resolve a pending end, so its unowned rows are settled here and a
// flush job routes them.
func (rec *steerRecord) endLocked(r *steerRecords, end command.SteerTurnEnd, stale bool, fx *steerFx) {
	if rec.noCarry {
		rec.endStepLocked(end, fx)
		return
	}
	own := end.TurnID != "" && end.TurnID == rec.turnID
	unbound := rec.channel == chanNone && end.Source.PromptClass()
	ev := endEvent(end)
	if end.TurnID != "" && end.TurnID == rec.starting {
		rec.starting = ""
	}
	collected := rec.collectEnded(end, own, unbound, ev, stale)
	if !stale && !end.BridgeDeath && (own || unbound) {
		rec.openEndWindow()
	}
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

func (rec *steerRecord) openEndWindow() {
	rec.settling = true
	rec.endHeld = rec.endHeld[:0]
	for i := range rec.others {
		rec.endHeld = append(rec.endHeld, wireKey{id: rec.others[i].SteerID, agent: true})
	}
}

func endEvent(end command.SteerTurnEnd) steerEvent {
	if end.BridgeDeath {
		return evTurnDeath
	}
	return evTurnEnd
}

// Whatever the channel. A row an op owns records the end for that op, whose EndOp retires it the same way.
func (rec *steerRecord) endStepLocked(end command.SteerTurnEnd, fx *steerFx) {
	rec.live(func(w *dockRow) {
		switch {
		case w.state == rowRead:
		case w.owner != "" && w.owner == rec.op:
			rec.noteOpEnd(w, end, false)
		default:
			w.owner = ""
			retire(w, boundarySteer(w), fx)
		}
	})
	rec.turnID, rec.channel, rec.probe, rec.starting = "", chanNone, "", ""
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

// bridgeGone leaves the rows a dead buffer held with no turn to collect them unsent
// for the chat's next prompt; KAS's log may re-inject them on load.
func (r *steerRecords) bridgeGone(chatID marotte.ChatID) {
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

// beginTeardown runs before the teardown's cancel, so every later fold, job and op finds the record gone. It
// answers the unread rows for the close's notes, an entry row's being the run list's (takeEndedSteers), keeps agent
// rows for the death closer, and returns once earlier steps published.
func (r *steerRecords) beginTeardown(chatID marotte.ChatID, exit <-chan struct{}) []marotte.SteerQueuedPayload {
	r.mu.Lock()
	rec := r.record(chatID, true)
	if rec.gone {
		r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
		return nil
	}
	var unread []marotte.SteerQueuedPayload
	rec.live(func(w *dockRow) {
		if !w.noted && w.entry == "" && rowRuleFor(w.state, evTeardown) == rDrop {
			unread = append(unread, marotte.SteerQueuedPayload{SteerID: w.key, Text: w.text, Origin: marotte.SteerOriginUser, Caller: w.caller})
		}
	})
	rec.rows, rec.ends, rec.endHeld, rec.settling = nil, nil, nil, false
	rec.op, rec.opEnd, rec.opEndStale, rec.lead, rec.reinject, rec.starting = "", nil, false, "", false, ""
	rec.channel, rec.turnID, rec.probe = chanNone, "", ""
	rec.exit, rec.gone = exit, true
	mintPending(&r.versions)
	r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
	return unread
}

// unreadKeys is the keys of the rows a teardown of chatID would drop (beginTeardown), the dock rows its page shows.
func (r *steerRecords) unreadKeys(chatID marotte.ChatID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[chatID]
	if rec == nil || rec.gone {
		return nil
	}
	var keys []string
	rec.live(func(w *dockRow) {
		if rowRuleFor(w.state, evTeardown) == rDrop {
			keys = append(keys, w.key)
		}
	})
	return keys
}

// shutdownRow is one row a shutdown takes: KASHeld when KAS may hold its words (textless entry, decided at next
// boot), Agent for a workflow's row.
type shutdownRow struct {
	Caller  *marotte.Caller
	Key     string
	Text    string
	KASHeld bool
	Agent   bool
}

// shutdownTake marks the record gone and takes every live row in one section, lead first, then the agent rows;
// it returns once earlier steps published.
func (r *steerRecords) shutdownTake(chatID marotte.ChatID) []shutdownRow {
	r.mu.Lock()
	rec := r.chats[chatID]
	if rec == nil || rec.gone {
		r.mu.Unlock()
		return nil
	}
	var live []*dockRow
	rec.live(func(w *dockRow) {
		if w.state != rowRead && w.entry == "" {
			live = append(live, w)
		}
	})
	sortLeadFirst(live, rec.lead)
	out := make([]shutdownRow, 0, len(live)+len(rec.others))
	for _, w := range live {
		held := w.state.inKAS() || (w.state == rowUnsent && rec.reinject && w.sent)
		out = append(out, shutdownRow{Key: w.key, Text: w.text, KASHeld: held, Caller: w.caller})
	}
	for i := range rec.others {
		out = append(out, shutdownRow{Key: rec.others[i].SteerID, Agent: true})
	}
	rec.rows, rec.others, rec.ends, rec.endHeld, rec.settling = nil, nil, nil, nil, false
	rec.op, rec.opEnd, rec.opEndStale, rec.lead, rec.starting = "", nil, false, "", ""
	rec.channel, rec.turnID, rec.probe, rec.gone = chanNone, "", "", true
	mintPending(&r.versions)
	r.awaitLocked(rec, &steerFx{chat: chatID, done: make(chan struct{})})
	return out
}

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

// endTeardown forgets the record once every queued step has drained, so a late fold
// cannot recreate it for a chat that is gone. The ResendBridgeWait bound has no normal-path
// case: closeBridge, the owner of the forward's exit, already awaited it.
func (r *steerRecords) endTeardown(chatID marotte.ChatID) {
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

// list answers the connect replay: every visible row with its wire state, a batch
// frame for every id a row is held under that is not its key, then agent rows.
func (r *steerRecords) list(chatFilter marotte.ChatID) []marotte.ServerEvent {
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
		out = r.appendReplay(out, r.chats[id], id)
	}
	return out
}

func (r *steerRecords) appendReplay(out []marotte.ServerEvent, rec *steerRecord, id marotte.ChatID) []marotte.ServerEvent {
	add := func(p marotte.SteerQueuedPayload) {
		if ev, ok := r.eventFor(id, &p); ok {
			out = append(out, ev)
		}
	}
	batches := map[string][]*dockRow{}
	var order []string
	rec.live(func(w *dockRow) {
		add(rec.frameOf(w))
		if w.kasID != "" && w.kasID != w.key {
			if _, seen := batches[w.kasID]; !seen {
				order = append(order, w.kasID)
			}
			batches[w.kasID] = append(batches[w.kasID], w)
		}
	})
	for _, kas := range order {
		add(batchFrame(kas, batches[kas]))
	}
	for i := range rec.others {
		add(rec.otherFrame(&rec.others[i]))
	}
	return out
}

// forgetStep drops a step's record once earlier steps published and nothing in it is live: the
// step's turn ended and no op holds a row. A record still holding rows stays for its op to finish.
func (r *steerRecords) forgetStep(key marotte.ChatID) {
	if _, step := key.StepSession(); !step {
		return
	}
	r.mu.Lock()
	rec := r.chats[key]
	if rec == nil {
		r.mu.Unlock()
		return
	}
	r.awaitLocked(rec, &steerFx{chat: key, done: make(chan struct{})})
	r.mu.Lock()
	defer r.mu.Unlock()
	idle := rec.op == "" && len(rec.others) == 0
	rec.live(func(*dockRow) { idle = false })
	if idle && r.chats[key] == rec {
		delete(r.chats, key)
	}
}

func (r *steerRecords) boundTo(key marotte.ChatID, turnID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[key]
	return rec != nil && !rec.gone && rec.channel != chanNone && rec.turnID == turnID
}

// A step's steer may be sent only while this holds.
func (r *steerRecords) stepOpen(key marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[key]
	return rec != nil && !rec.gone && rec.channel != chanNone
}

// setLead records the send-now arrow's row, sent first until it is sent or
// terminal; a key no live row has records nothing and answers waiting false.
// entry is the run-list entry the row carries, "" for a row routed here first.
func (r *steerRecords) setLead(chatID marotte.ChatID, key string) (undo func(), waiting bool, entry string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.record(chatID, false)
	if rec == nil || rec.gone || key == "" || rec.row(key) == nil {
		return func() {}, false, ""
	}
	rec.lead = key
	entry = rec.row(key).entry
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if rec.lead == key {
			rec.lead = ""
		}
	}, true, entry
}

// needsPostLoadClear: KAS may hold copies this record also holds, nothing bound is
// reading, and no agent row would be lost to the clear.
func (r *steerRecords) needsPostLoadClear(chatID marotte.ChatID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.chats[chatID]
	return rec != nil && !rec.gone && rec.reinject && rec.turnID == "" && len(rec.others) == 0
}

func (b *bus) SteerWaiting(chatID marotte.ChatID, p *marotte.SteerQueuedPayload) (marotte.SteerQueuedPayload, bool) {
	return b.steers.SteerWaiting(chatID, p)
}

func (b *bus) SteerRead(chatID marotte.ChatID, steerID string) marotte.SteerQueuedPayload {
	return b.steers.SteerRead(chatID, steerID)
}

func (b *bus) SteerForgotten(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return b.steers.SteerForgotten(chatID, steerIDs)
}

func (b *bus) SteerCleared(chatID marotte.ChatID, steerIDs []string) []marotte.SteerQueuedPayload {
	return b.steers.SteerCleared(chatID, steerIDs)
}
