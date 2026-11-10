package agent

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// listWorld models what a runList's glue reports to it: kiro-cli's state, the admission slot, the stop holds, the
// turns the registry holds open, close tails and reservations owed, and the compaction calls written.
type listWorld struct {
	r          *rand.Rand
	l          *runList
	stops      map[uint64]bool
	turns      map[string]*worldTurn
	tails      map[string]string // closing entry id -> its turn id, for each tail owed
	releases   []uint64          // reservations a closed turn's runner still holds
	calls      map[string]*worldCall
	t          *testing.T
	step       int
	tok        uint64
	reserved   uint64
	nextSeq    uint64
	stopSeq    uint64
	ids        int
	steers     int
	admit      bool
	gate       bool
	composerBy map[string]int      // submit-origin entry id -> composer claims made
	reached    map[string]int      // the shapes and events the run reached
	idle       *runEntry           // a revert or summary whose caller waits on takeIdle
	retryAhead map[string][]string // a retry's id -> the turn-kind entries pending ahead of it when it was listed
}

type worldTurn struct {
	entry     string
	token     uint64
	seq       uint64
	wire      bool
	bracketed bool
	sent      bool
	stopped   bool
}

type worldCall struct {
	sawTurn bool
}

func newListWorld(t *testing.T, seed uint64) *listWorld {
	return &listWorld{
		t: t, r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), l: &runList{}, admit: true,
		stops: map[uint64]bool{}, turns: map[string]*worldTurn{}, tails: map[string]string{},
		calls: map[string]*worldCall{}, composerBy: map[string]int{},
		reached: map[string]int{}, retryAhead: map[string][]string{},
	}
}

func (w *listWorld) facts() *orderFacts {
	return &orderFacts{
		reserve: func(_ *runEntry, _ uint64) (command.Reservation, takeAnswer) {
			if w.gate || w.reserved != 0 {
				return command.Reservation{}, takeWait
			}
			w.tok++
			w.reserved = w.tok
			return command.Reservation{Chat: "c1", Token: w.tok, Stamp: 1}, takeGranted
		},
		unsent:       func() int { return w.steers },
		newMessageID: func() string { w.ids++; return "u-" + strconv.Itoa(w.ids) },
		stamp:        1, nextSeq: w.nextSeq, stopSeq: w.stopSeq, stops: len(w.stops),
		admit: w.admit, sessionCall: w.admit,
	}
}

func (w *listWorld) fail(format string, args ...any) {
	w.t.Helper()
	w.t.Fatalf("step %d: %s\nlist: %s", w.step, fmt.Sprintf(format, args...), w.dump())
}

func (w *listWorld) dump() string {
	s := ""
	for _, e := range w.l.entries {
		s += fmt.Sprintf("[%s %s %s %s turn=%s tok=%d] ", e.id, e.kind, e.origin, e.state, e.turnID, e.res.Token)
	}
	return s
}

func (w *listWorld) release(token uint64) {
	if token != 0 && w.reserved == token {
		w.reserved = 0
	}
	e := w.l.byToken(token)
	w.l.reservationReleased(token)
	if e != nil && w.tails[e.id] != "" {
		if _, still := w.l.find(e.id); still == nil {
			w.fail("%s ended at its release while its close tail is owed", e.id)
		}
	}
}

// settle runs the advance every operation ends with and starts what it took.
func (w *listWorld) settle() {
	before := w.dumpRetries()
	w.l.advance(w.facts())
	fx := w.l.takeEffects()
	for _, e := range fx.launch {
		w.reached["take "+string(e.kind)]++
		if e.kind == marotte.RunKindRetry && before[e.id] {
			w.fail("retry %s taken after a later wire turn or a stop overtook its turn", e.id)
		}
		for _, id := range w.retryAhead[e.id] {
			if _, a := w.l.find(id); a != nil && a.state == marotte.RunPending {
				w.fail("retry %s taken while %s, queued ahead of it, still waits", e.id, id)
			}
		}
	}
	w.check()
}

// dumpRetries names the pending retries a later wire open or a stop has overtaken, by the world's own record.
func (w *listWorld) dumpRetries() map[string]bool {
	out := map[string]bool{}
	for _, e := range w.l.entries {
		if e.kind == marotte.RunKindRetry && (w.l.wireSeq > e.afterSeq || (w.stopSeq != 0 && e.afterSeq <= w.stopSeq)) {
			out[e.id] = true
		}
	}
	return out
}

func (w *listWorld) live() []*runEntry { return w.l.entries[:w.l.liveLen()] }

// check asserts the list's invariants.
func (w *listWorld) check() {
	w.t.Helper()
	l := w.l
	n := l.liveLen()
	for _, e := range l.entries[n:] {
		if e.live() {
			w.fail("a live entry %s behind a waiting one (R1)", e.id)
		}
	}
	live := l.entries[:n]
	switch n {
	case 0, 1:
	case 2:
		a, b := live[0], live[1]
		s1 := (a.kind == marotte.RunKindCompaction || a.kind == marotte.RunKindRevert) && a.state == marotte.RunRunning && b.wire()
		s2 := a.wire() && b.kind.TurnKind() && !b.kiroCLI() &&
			(a.state != marotte.RunClosing || (b.state != marotte.RunStarting && b.state != marotte.RunRunning))
		s3 := a.wire() && a.yields && a.state.Live() && a.state != marotte.RunStarting &&
			b.kind == marotte.RunKindCompaction && b.state == marotte.RunStarting
		switch {
		case s1:
			w.reached["S1"]++
		case s2 && b.state == marotte.RunRunning:
			w.reached["S2c"]++
		case s2:
			w.reached["S2"]++
		case s3:
			w.reached["S3"]++
		default:
			w.fail("two live entries in no allowed shape (R1)")
		}
	default:
		w.fail("%d live entries (R1)", n)
	}
	compactions, ids := 0, map[string]bool{}
	for i, e := range l.entries {
		if ids[e.id] {
			w.fail("id %s twice", e.id)
		}
		ids[e.id] = true
		if e.kind == marotte.RunKindCompaction {
			compactions++
		}
		if e.runEntry() && (i != 0 || n != 1) {
			w.fail("a run entry beside another live entry (R9)")
		}
		if e.opts.composer && !e.submitter {
			w.fail("%s keeps the composer settle with no submitter waiting", e.id)
		}
		if e.tailDone && e.released {
			w.fail("%s has its tail and its release and is still listed", e.id)
		}
		if e.kind.TurnKind() && !e.kiroCLI() && e.state == marotte.RunStarting && e.turnID == "" &&
			(e.res.Token == 0 || e.res.Token != w.reserved) {
			w.fail("%s starting without the slot it was granted", e.id)
		}
		if e.state == marotte.RunHeld && e.kind != marotte.RunKindRow {
			w.fail("a held %s", e.kind)
		}
	}
	if compactions > 1 {
		w.fail("%d compactions (R2)", compactions)
	}
	others := 0
	for _, e := range live {
		if !e.runEntry() {
			others++
		}
	}
	if !l.endsOwed {
		hasRun := n == 1 && live[0].runEntry()
		if hasRun {
			w.reached["run entry"]++
		}
		if hasRun != (others == 0 && !w.admit) {
			w.fail("a run entry %v with %d other live entries and admit %v (R9)", hasRun, others, w.admit)
		}
	}
	if by := l.beside(); by != nil && by.admits != w.admit {
		w.fail("the yielding head's admits %v, kiro-cli's admit %v", by.admits, w.admit)
	}
	for id, tr := range w.turns {
		named := 0
		for _, e := range live {
			if e.turnID == id {
				named++
			}
		}
		if named != 1 {
			w.fail("open turn %s (wire %v) has %d live entries", id, tr.wire, named)
		}
	}
	for _, e := range live {
		if e.kind == marotte.RunKindCompaction && e.state == marotte.RunRunning && w.calls[e.id] == nil {
			w.fail("compaction %s running with no call written", e.id)
		}
	}
	w.checkTaken()
}

// checkTaken asserts the head rule's liveness: a pending entry the rule allows is taken.
func (w *listWorld) checkTaken() {
	l := w.l
	if l.endsOwed {
		return
	}
	n := l.liveLen()
	if n > 0 && l.beside() == nil {
		return
	}
	for _, e := range l.entries[n:] {
		if e.state != marotte.RunPending {
			continue
		}
		switch {
		case e.kind == marotte.RunKindCompaction:
			if w.admit && len(w.stops) == 0 {
				w.fail("pending compaction %s not taken", e.id)
			}
		case e.kind == marotte.RunKindRevert:
		case e.kind == marotte.RunKindUnread && w.steers == 0:
		default:
			if w.admit && !w.gate && w.reserved == 0 {
				w.fail("pending %s not taken", e.id)
			}
		}
		return
	}
}

func (w *listWorld) msgID() string { w.ids++; return "m-" + strconv.Itoa(w.ids) }

func (w *listWorld) pick(entries []*runEntry) *runEntry {
	if len(entries) == 0 {
		return nil
	}
	return entries[w.r.IntN(len(entries))]
}

func (w *listWorld) where(cond func(*runEntry) bool) []*runEntry {
	var out []*runEntry
	for _, e := range w.l.entries {
		if cond(e) {
			out = append(out, e)
		}
	}
	return out
}

func (w *listWorld) snapshot() []marotte.RunEntry { return slices.Clone(w.l.project()) }

func (w *listWorld) submit() {
	if len(w.l.entries) >= 6 {
		return
	}
	e := &runEntry{
		id: w.msgID(), kind: marotte.RunKindRow, origin: marotte.RunFromPrompt,
		row: &marotte.QueuedPrompt{Text: "x"}, retry: w.r.IntN(2) == 0,
	}
	e.row.ID = e.id
	waits := w.r.IntN(2) == 0
	e.opts.composer = waits
	a := w.l.submit(e, w.facts(), 1, waits)
	if a.queued {
		i, _ := w.l.find(e.id)
		for _, p := range w.l.entries[:max(i, 0)] {
			if p.state == marotte.RunPaused {
				w.fail("new row %s queued behind paused %s (R5)", e.id, p.id)
			}
		}
		if a.settle {
			w.composerBy[e.id]++
		}
	}
	w.l.takeEffects()
}

func (w *listWorld) launch() {
	starting := w.where(func(e *runEntry) bool { return e.state == marotte.RunStarting })
	e := w.pick(starting)
	if e == nil {
		return
	}
	if e.kind == marotte.RunKindCompaction {
		step, fold := w.l.compactionStart(e.id, w.facts())
		switch step {
		case compactCloseFold:
			w.closeTurn(fold.turnID, true)
		case compactWrite:
			w.calls[e.id] = &worldCall{}
		case compactAbort, compactWaitFold:
		}
		return
	}
	if e.turnID != "" {
		w.send(e)
		return
	}
	if w.r.IntN(5) == 0 {
		token := e.res.Token
		w.release(token)
		removed := w.l.launched(e.id, "", launchClosed, marotte.ErrChatClosedDuringCall)
		if removed && w.l.compaction() != nil && w.l.compaction().id == e.id {
			w.fail("a refused launch removed the compaction")
		}
		return
	}
	w.nextSeq++
	id := "t-" + e.id + "-" + strconv.FormatUint(w.nextSeq, 10)
	if !w.l.turnOpened(openFacts{turnID: id, admission: e.res.Token, seq: w.nextSeq}) {
		w.fail("prompt-class open of %s not placed", e.id)
	}
	w.turns[id] = &worldTurn{entry: e.id, token: e.res.Token, seq: w.nextSeq}
	if w.composerBy[e.id] == 0 && w.l.claimComposer(e.id) {
		w.composerBy[e.id]++
	}
	if w.composerBy[e.id] > 1 {
		w.fail("%s settled the composer twice", e.id)
	}
	w.l.launched(e.id, id, launchOpened, nil)
}

// send is the send gate: it waits out stop holds and a bracketed wire turn, closes a fold-opened one, then sends.
func (w *listWorld) send(e *runEntry) {
	tr := w.turns[e.turnID]
	if tr == nil || tr.sent || len(w.stops) > 0 || !w.admit {
		return
	}
	if tr.stopped {
		w.closeTurn(e.turnID, false)
		return
	}
	for id, o := range w.turns {
		if o.wire {
			if o.bracketed {
				return
			}
			w.closeTurn(id, true)
		}
	}
	tr.sent = true
	w.l.turnSent(e.turnID)
}

func (w *listWorld) closeTurn(turnID string, handoff bool) {
	tr := w.turns[turnID]
	if tr == nil {
		return
	}
	_, e := w.l.byTurn(turnID)
	if e == nil {
		w.fail("open turn %s has no entry", turnID)
	}
	delete(w.turns, turnID)
	clean := !handoff && !tr.stopped && w.r.IntN(3) > 0
	var compact marotte.RunEntryOrigin
	if w.r.IntN(4) == 0 {
		compact = marotte.RunFromTurnEnd
	}
	var queued []string
	for _, q := range w.l.entries[w.l.liveLen():] {
		if q.kind.TurnKind() {
			queued = append(queued, q.id)
		}
	}
	w.ids++
	w.l.turnClosed(listClose{
		turnID: turnID, retryID: "r-" + strconv.Itoa(w.ids), compact: compact, seq: tr.seq, clean: clean,
		handoff: handoff, emptyRetry: clean && w.r.IntN(2) == 0,
	}, w.facts())
	for i, r := range w.l.entries {
		if r.kind != marotte.RunKindRetry || r.replaces != turnID {
			continue
		}
		if w.nextSeq > tr.seq || (w.stopSeq != 0 && w.stopSeq >= tr.seq) {
			w.fail("retry %s listed for a turn a later open or a stop overtook", r.id)
		}
		for _, id := range queued {
			if j, q := w.l.find(id); q != nil && j > i {
				w.fail("retry %s listed ahead of %s, queued before its turn closed (DG31)", r.id, id)
			}
		}
		for _, a := range w.l.entries[:i] {
			if a.state == marotte.RunPending && a.kind.TurnKind() {
				w.retryAhead[r.id] = append(w.retryAhead[r.id], a.id)
			}
		}
	}
	if _, still := w.l.find(e.id); still != nil {
		w.tails[e.id] = turnID
	} else if n := w.l.liveLen(); n == 0 || !w.l.entries[0].kind.TurnKind() ||
		(w.l.entries[0].state != marotte.RunStarting && w.l.entries[0].state != marotte.RunRunning) {
		w.fail("a closed turn's entry went with no taker live (its close hold has no entry)")
	}
	if !tr.wire {
		w.releases = append(w.releases, tr.token)
	}
}

func (w *listWorld) wireOpen() {
	for _, tr := range w.turns {
		if tr.wire {
			return
		}
	}
	w.nextSeq++
	id := "w-" + strconv.FormatUint(w.nextSeq, 10)
	bracketed := w.r.IntN(2) == 0
	for _, e := range w.live() {
		if e.kind == marotte.RunKindCompaction && e.state == marotte.RunRunning && w.calls[e.id] != nil {
			w.calls[e.id].sawTurn = true
		}
	}
	if !w.l.turnOpened(openFacts{turnID: id, seq: w.nextSeq, wire: true, bracketed: bracketed}) {
		w.fail("wire open not placed")
	}
	w.turns[id] = &worldTurn{wire: true, bracketed: bracketed, seq: w.nextSeq}
}

func (w *listWorld) endCall() {
	var id string
	for k := range w.calls {
		if id == "" || k < id {
			id = k
		}
	}
	if id == "" {
		return
	}
	call := w.calls[id]
	delete(w.calls, id)
	accepted := w.r.IntN(3) == 0
	var err error
	if !accepted && w.r.IntN(3) == 0 {
		err = errors.New("model failure")
	}
	w.l.compactionEnded(id, accepted, err)
	if _, still := w.l.find(id); still != nil {
		w.reached["compaction requeued"]++
	}
	if _, still := w.l.find(id); still != nil && !call.sawTurn {
		w.fail("compaction %s requeued with no turn beside its attempt (DG23)", id)
	}
}

func (w *listWorld) stop() {
	w.tok++
	token := w.tok
	var target string
	switch w.r.IntN(4) {
	case 0:
	case 1:
		target = "stale-" + strconv.Itoa(w.step)
	default:
		if e := w.pick(w.live()); e != nil {
			target = e.id
		}
	}
	before := w.l.compaction()
	var beforeState marotte.RunEntryState
	if before != nil {
		beforeState = before.state
	}
	snap := w.snapshot()
	plan := w.l.stopHead(target, token)
	if c := w.l.compaction(); before != nil && (c != before || c.state != beforeState) {
		w.fail("a stop changed the compaction")
	}
	if plan.arm == stopNothing || plan.arm == stopSession {
		if !slices.EqualFunc(snap, w.l.project(), runEntryEqual) {
			w.fail("a stop of %q that stopped no entry changed the list", target)
		}
		if plan.arm == stopSession && (target != "" || len(w.live()) > 0) {
			w.fail("a stop of %q went to the session beside a live entry", target)
		}
		return
	}
	if target != "" && plan.entry.id != target {
		w.fail("a stop of %q stopped %s (R8)", target, plan.entry.id)
	}
	w.stops[token] = true
	w.stopSeq = w.nextSeq
	switch plan.arm {
	case stopWithdraw:
		w.reached["withdraw"]++
		w.release(plan.res.Token)
	case stopUnsent:
		w.reached["stop unsent"]++
		w.turns[plan.entry.turnID].stopped = true
	case stopRunning, stopRun:
		w.reached["stop running"]++
		if w.r.IntN(4) == 0 {
			w.reached["stop undone"]++
			w.l.stopUndone(token, func(id string) bool { return w.turns[id] != nil })
			for _, e := range w.l.entries {
				if e.stopBy == token {
					w.fail("an undone stop left %s marked", e.id)
				}
			}
		} else if plan.arm == stopRunning {
			for _, e := range w.live() {
				if e.stopBy == token && w.turns[e.turnID] != nil {
					w.turns[e.turnID].stopped = true
				}
			}
		}
	case stopNothing, stopSession:
	}
}

func runEntryEqual(a, b marotte.RunEntry) bool {
	return a.ID == b.ID && a.State == b.State && a.Kind == b.Kind && a.Turn == b.Turn && a.Next == b.Next
}

func (w *listWorld) sendNow() {
	rows := w.where(func(e *runEntry) bool { return e.kind == marotte.RunKindRow && !e.live() })
	e := w.pick(rows)
	if e == nil {
		return
	}
	stop, has := "", w.r.IntN(2) == 0
	if has {
		if h := w.pick(w.live()); h != nil && w.r.IntN(3) > 0 {
			stop = h.id
		} else {
			stop = "gone-" + strconv.Itoa(w.step)
		}
	}
	h, err := w.l.sendNow(e.id, stop, has)
	if err != nil {
		return
	}
	if h != nil {
		if has && h.id != stop {
			w.fail("send-now named %s for a press drawn against %s (DG24)", h.id, stop)
		}
		if w.l.liveLen() != 1 {
			w.fail("send-now stops beside another live entry")
		}
		w.tok++
		plan := w.l.stopHead(h.id, w.tok)
		w.stops[w.tok] = true
		w.stopSeq = w.nextSeq
		switch plan.arm {
		case stopWithdraw:
			w.release(plan.res.Token)
		case stopUnsent, stopRunning:
			if tr := w.turns[plan.entry.turnID]; tr != nil {
				tr.stopped = true
			}
		case stopNothing, stopRun, stopSession:
		}
	}
}

func (w *listWorld) do() {
	switch w.r.IntN(23) {
	case 0, 1, 2:
		w.submit()
	case 3:
		if len(w.l.entries) >= 6 {
			break
		}
		e := &runEntry{id: w.msgID(), kind: marotte.RunKindRow, origin: marotte.RunFromQueue}
		e.row = &marotte.QueuedPrompt{ID: e.id, Text: "q"}
		if marotte.VerbFor(w.l.project(), marotte.InterruptQueue, marotte.SendQueuePrompt) != marotte.SendPrompt {
			w.l.submit(e, w.facts(), 1, false)
		}
	case 4:
		if w.r.IntN(2) == 0 {
			break
		}
		turn := ""
		if h := w.pick(w.live()); h != nil {
			turn = h.turnID
		}
		w.l.requestCompaction(marotte.RunFromRequest, turn)
	case 5, 6, 7:
		w.launch()
	case 8, 9:
		var open []string
		for id := range w.turns {
			open = append(open, id)
		}
		slices.Sort(open)
		if len(open) > 0 {
			w.closeTurn(open[w.r.IntN(len(open))], false)
		}
	case 10:
		var owed []string
		for id := range w.tails {
			owed = append(owed, id)
		}
		slices.Sort(owed)
		if len(owed) > 0 {
			id := owed[w.r.IntN(len(owed))]
			delete(w.tails, id)
			w.l.tailEnded(id, false)
		}
	case 11:
		if len(w.releases) > 0 {
			i := w.r.IntN(len(w.releases))
			tok := w.releases[i]
			w.releases = slices.Delete(w.releases, i, i+1)
			w.release(tok)
		}
	case 12:
		if w.r.IntN(8) == 0 {
			w.wireOpen()
		}
	case 13:
		w.endCall()
	case 14:
		w.admit = !w.admit || w.r.IntN(4) > 0
		if !w.admit {
			w.l.kasReportedActive()
			for _, c := range w.calls {
				c.sawTurn = true
			}
		}
	case 15:
		if w.r.IntN(3) == 0 {
			w.stop()
		}
	case 16:
		for tok := range w.stops {
			delete(w.stops, tok)
			break
		}
	case 17:
		w.sendNow()
	case 18:
		if e := w.pick(w.where(func(e *runEntry) bool { return !e.live() && e.row != nil })); e != nil {
			w.l.unqueue(e.id)
		}
	case 19:
		if c := w.l.compaction(); c != nil {
			w.l.cancelCompaction(c.id)
		}
	case 20:
		w.ends()
	case 21:
		w.gate = !w.gate && w.r.IntN(4) == 0
	case 22:
		w.takeIdle()
	}
	w.l.takeEffects()
	w.settle()
}

func (w *listWorld) takeIdle() {
	if w.idle == nil {
		w.ids++
		w.idle = &runEntry{id: "i-" + strconv.Itoa(w.ids), kind: marotte.RunKindRevert}
		if w.r.IntN(2) == 0 {
			w.idle.kind, w.idle.origin = marotte.RunKindTurn, marotte.RunFromSummary
		}
	}
	e := w.idle
	a := idleGranted
	if e.res.Token == 0 && !e.live() {
		a = w.l.takeIdle(e, w.facts(), 1)
	}
	switch a {
	case idleGranted:
		w.idle = nil
		w.reached["idle "+string(e.kind)]++
		if e.kind == marotte.RunKindRevert {
			w.releases = append(w.releases, e.res.Token)
		}
	case idleBusy:
		w.idle = nil
	case idleWait:
		if w.r.IntN(3) == 0 {
			if !w.l.idleExpired(e) {
				w.idle = nil
			} else {
				w.idle = nil
				if e.kind == marotte.RunKindRevert {
					w.releases = append(w.releases, e.res.Token)
				}
			}
		}
	}
}

func (w *listWorld) ends() {
	w.steers = w.r.IntN(2)
	if c := w.l.closingEntry(); c != nil && !c.endsDone && w.r.IntN(2) == 0 {
		w.l.closeEndsResolved(c.id, w.r.IntN(2) == 0, true, w.facts())
		return
	}
	n := 0
	if w.r.IntN(4) == 0 {
		n = 1
	}
	var unsent []string
	if w.steers > 0 {
		unsent = []string{"s-1"}
	}
	seq := uint64(w.step)
	if reread, drained := w.l.endsChanged(seq, n, unsent, false, true, w.facts()); reread {
		w.l.endsChanged(seq, n, unsent, drained, true, w.facts())
	}
}

func TestRunList_Invariants(t *testing.T) {
	steps := 10000
	if testing.Short() {
		steps = 2000
	}
	reached := map[string]int{}
	for seed := range uint64(32) {
		t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
			w := newListWorld(t, seed)
			for w.step = range steps {
				w.do()
			}
			for k, v := range w.reached {
				reached[k] += v
			}
		})
	}
	for _, want := range []string{
		"S1", "S2", "S2c", "S3", "run entry", "take row", "take compaction", "take unread", "take retry",
		"compaction requeued", "withdraw", "stop unsent", "stop running", "stop undone", "idle revert", "idle turn",
	} {
		if reached[want] == 0 {
			t.Errorf("the generator never reached %q", want)
		}
	}
}
