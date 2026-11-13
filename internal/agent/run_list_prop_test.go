package agent

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
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
	resumes    int // resumes opened since the last turn the reader sent opened
	admit      bool
	gate       bool
	composerBy map[string]int      // submit-origin entry id -> composer claims made
	reached    map[string]int      // the shapes and events the run reached
	idle       *runEntry           // a revert or summary whose caller waits on takeIdle
	retryAhead map[string][]string // a retry's id -> the turn-kind entries pending ahead of it when it was listed
	failsafeOf map[string]string   // a resume's id -> the compaction it follows
	accepted   map[string]bool     // compactions whose call was accepted with no cancel
	cancelled  map[string]bool     // compactions the reader cancelled while running
	// readerStopped names the resumes that waited when the reader stopped something or pressed send-now.
	readerStopped map[string]bool
	// limited names the turns a failsafe stop at the resume limit holds, whose close owes the reader the notice.
	limited map[string]bool
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
		failsafeOf: map[string]string{}, accepted: map[string]bool{}, cancelled: map[string]bool{},
		readerStopped: map[string]bool{}, limited: map[string]bool{},
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
		newMessageID: func() string { w.ids++; return "u-" + strconv.Itoa(w.ids) },
		newOffer:     func() uint64 { w.ids++; return uint64(w.ids) },
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
		if e.unread {
			w.reached["take unread"]++
		}
		if e.kind == marotte.RunKindRetry && before[e.id] {
			w.fail("retry %s taken after a later wire turn or a stop overtook its turn", e.id)
		}
		if e.kind == marotte.RunKindResume && !w.accepted[w.failsafeOf[e.id]] {
			w.fail("resume %s taken though its compaction %s was not accepted", e.id, w.failsafeOf[e.id])
		}
		if e.kind == marotte.RunKindResume && w.readerStopped[e.id] {
			w.fail("resume %s taken after a stop of the reader's", e.id)
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
			w.fail("a live entry %s behind a waiting one", e.id)
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
			w.reached["turn under a compaction"]++
		case s2 && b.state == marotte.RunRunning:
			w.reached["wire turn beside a sent prompt"]++
		case s2:
			w.reached["wire turn beside a taken turn"]++
		case s3:
			w.reached["fold turn ahead of its compaction"]++
		default:
			w.fail("two live entries in no allowed shape")
		}
	default:
		w.fail("%d live entries", n)
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
			w.fail("a run entry beside another live entry")
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
		w.fail("%d compactions", compactions)
	}
	w.checkResume()
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
			w.fail("a run entry %v with %d other live entries and admit %v", hasRun, others, w.admit)
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

// checkResume asserts the failsafe's rules: at most one resume waiting (a running resume can itself be stopped and
// list the next), and a resume waiting behind its compaction or after that compaction was accepted.
func (w *listWorld) checkResume() {
	resumes := w.where(func(e *runEntry) bool { return e.kind == marotte.RunKindResume && !e.live() })
	if len(resumes) > 1 {
		w.fail("%d resumes waiting", len(resumes))
	}
	for _, r := range resumes {
		c := w.failsafeOf[r.id]
		ci, ce := w.l.find(c)
		ri, _ := w.l.find(r.id)
		if (ce == nil || ci > ri) && !w.accepted[c] {
			w.fail("resume %s waits with its compaction %s neither ahead of it nor accepted", r.id, c)
		}
	}
}

// failsafe is a reading at the failsafe point during the turn of a live entry, or a reading under the point.
func (w *listWorld) failsafe() {
	if w.r.IntN(3) == 0 {
		w.l.usageUnderThePoint()
		return
	}
	turn := ""
	if h := w.pick(w.live()); h != nil {
		turn = h.turnID
	}
	w.fireIn(turn)
}

// refill reads under the point and then at the failsafe point in a resume's own turn (sent once the stop holds
// ended), or in kiro-cli's turn opened ahead of a waiting resume.
func (w *listWorld) refill() {
	live := w.live()
	switch {
	case len(live) == 1 && live[0].kind == marotte.RunKindResume && live[0].turnID != "":
		// kiro-cli admitted the resume's take and waits for its prompt.
		clear(w.stops)
		w.admit = true
		w.send(live[0])
	case len(live) == 0 && w.l.resume() != nil:
		w.wireOpen()
	default:
		return
	}
	w.l.usageUnderThePoint()
	if h := w.live(); len(h) == 1 {
		w.fireIn(h[0].turnID)
	}
}

// fireIn is a reading at the failsafe point during turn.
func (w *listWorld) fireIn(turn string) {
	waiting := w.l.resume()
	if c := w.l.compaction(); c != nil && c.origin == marotte.RunFromFailsafe {
		waiting = nil
	}
	notices := len(w.l.fx.notices)
	w.tok++
	token := w.tok
	w.ids++
	resumeID := "fs-" + strconv.Itoa(w.ids)
	snap := w.snapshot()
	plan := w.l.failsafe(failsafeFacts{turnID: turn, resumeID: resumeID, token: token})
	if plan.arm == stopNothing {
		if !slices.EqualFunc(snap, w.l.project(), runEntryEqual) {
			w.fail("a failsafe that stopped nothing changed the list")
		}
		return
	}
	if plan.arm != stopRunning || plan.entry == nil || plan.entry.turnID != turn {
		w.fail("the failsafe for %s answered %+v", turn, plan)
	}
	w.reached["failsafe fired"]++
	c := w.l.compaction()
	if c == nil {
		w.fail("the failsafe listed no compaction")
	}
	_, listed := w.l.find(resumeID)
	var waitedOn string
	switch {
	case waiting != nil:
		w.reached["failsafe ahead of a waiting resume"]++
		if listed != nil || w.l.resume() != waiting {
			w.fail("a fire ahead of the waiting resume %s left %v waiting", waiting.id, w.l.resume())
		}
		waitedOn, w.failsafeOf[waiting.id] = w.failsafeOf[waiting.id], c.id
	case listed != nil:
		w.failsafeOf[resumeID] = c.id
	case c.fsToken == token:
		w.reached["failsafe at the resume limit"]++
		w.limited[turn] = true
	}
	if len(w.l.fx.notices) > notices {
		w.fail("the failsafe told the reader of its stop before the stop held")
	}
	w.stops[token] = true
	w.stopSeq = w.nextSeq
	// A fire at the resume limit is rare and is left standing, so the generator reaches its notice at the close.
	if !w.limited[turn] && w.r.IntN(4) == 0 {
		w.reached["failsafe undone"]++
		delete(w.limited, turn)
		w.l.stopUndone(token, func(id string) bool { return w.turns[id] != nil })
		if _, r := w.l.find(resumeID); r != nil {
			w.fail("an undone failsafe left its resume %s", resumeID)
		}
		if waiting != nil {
			if w.l.resume() != waiting {
				w.fail("an undone failsafe took the resume %s an earlier stop listed", waiting.id)
			}
			w.failsafeOf[waiting.id] = waitedOn
		}
		return
	}
	if tr := w.turns[turn]; tr != nil {
		tr.stopped = true
	}
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
		switch e.kind {
		case marotte.RunKindCompaction:
			if w.admit && len(w.stops) == 0 {
				w.fail("pending compaction %s not taken", e.id)
			}
		case marotte.RunKindRevert:
		default:
			// A row waits while the reader's stop holds the queue, which the page reads as StopPaused.
			if w.admit && !w.gate && w.reserved == 0 && !slices.ContainsFunc(l.project(), func(p marotte.RunEntry) bool {
				return p.StopPaused
			}) {
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
	if !a.taken && a.refused == takeGranted {
		i, _ := w.l.find(e.id)
		for _, p := range w.l.entries[:max(i, 0)] {
			if p.state == marotte.RunPaused {
				w.fail("new row %s queued behind paused %s", e.id, p.id)
			}
		}
		// The glue answers a queued composer send with the settle (submitLocked).
		if waits {
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
		removed := w.l.launched(e.id, e.res.Token, "", launchFailed, marotte.ErrChatClosedDuringCall)
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
	switch e.kind {
	case marotte.RunKindResume:
		if w.resumes++; w.resumes > compactionFailsafeResumes {
			w.fail("resume %s is the %d. since the reader's last turn", e.id, w.resumes)
		}
	case marotte.RunKindRow:
		w.resumes = 0
	}
	if w.composerBy[e.id] == 0 && w.l.claimComposer(e.id) {
		w.composerBy[e.id]++
	}
	if w.composerBy[e.id] > 1 {
		w.fail("%s settled the composer twice", e.id)
	}
	w.l.launched(e.id, e.res.Token, id, launchOpened, nil)
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
	notices := len(w.l.fx.notices)
	w.l.turnClosed(listClose{
		turnID: turnID, retryID: "r-" + strconv.Itoa(w.ids), compact: compact, seq: tr.seq, clean: clean, completed: clean,
		handoff: handoff, emptyRetry: clean && w.r.IntN(2) == 0,
	}, w.facts())
	told := len(w.l.fx.notices) > notices
	if want := w.limited[turnID] && !clean; told != want {
		w.fail("the close of %s told the reader of a stop at the resume limit: %v, want %v", turnID, told, want)
	}
	if told {
		w.reached["resume limit notice at the close"]++
	}
	delete(w.limited, turnID)
	for i, r := range w.l.entries {
		if r.kind != marotte.RunKindRetry || r.replaces != turnID {
			continue
		}
		if w.nextSeq > tr.seq || (w.stopSeq != 0 && w.stopSeq >= tr.seq) {
			w.fail("retry %s listed for a turn a later open or a stop overtook", r.id)
		}
		for _, id := range queued {
			if j, q := w.l.find(id); q != nil && j > i {
				w.fail("retry %s listed ahead of %s, queued before its turn closed", r.id, id)
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
	if accepted && !w.cancelled[id] {
		w.accepted[id] = true
	}
	w.l.compactionEnded(id, accepted, err)
	if _, still := w.l.find(id); still != nil {
		w.reached["compaction requeued"]++
	}
	if _, still := w.l.find(id); still != nil && !call.sawTurn {
		w.fail("compaction %s requeued with no turn beside its attempt", id)
	}
}

func (w *listWorld) stop() {
	// A reader stop withdraws a waiting resume; most runs leave it, so the generator reaches the resume's take.
	if w.l.resume() != nil && w.r.IntN(4) > 0 {
		return
	}
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
	// Any stop of the reader's withdraws the waiting resume, whatever runs then, and changes nothing else.
	if r := w.readerStopping(); r != nil {
		snap = slices.DeleteFunc(snap, func(x marotte.RunEntry) bool { return x.ID == r.id })
	}
	plan := w.l.readerStop(target, token)
	clear(w.limited)
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
		w.fail("a stop of %q stopped %s", target, plan.entry.id)
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

// readerStopping records a stop of the reader's against the resume waiting then, which must never start, and
// returns that resume, or nil. TestFailsafe_NoResumeStartsAfterAReaderStop walks every window such a stop lands in.
func (w *listWorld) readerStopping() *runEntry {
	r := w.l.resume()
	if r != nil {
		w.readerStopped[r.id] = true
		w.reached["reader stop ahead of a resume"]++
	}
	return r
}

func runEntryEqual(a, b marotte.RunEntry) bool {
	return a.ID == b.ID && a.State == b.State && a.Kind == b.Kind && a.Turn == b.Turn && a.Next == b.Next
}

func (w *listWorld) sendNow() {
	// A press drops a waiting resume; most runs leave it, so the generator reaches the resume's take.
	if w.l.resume() != nil && w.r.IntN(4) > 0 {
		return
	}
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
	waiting := w.l.resume()
	h, err := w.l.sendNow(e.id, stop, has)
	if err != nil {
		return
	}
	// Any press is the reader taking over (readerTookOver), stopping something or not: no failsafe stop it lands
	// on tells of the resume limit.
	clear(w.limited)
	if waiting != nil {
		w.readerStopped[waiting.id] = true
	}
	if h != nil {
		if has && h.id != stop {
			w.fail("send-now named %s for a press drawn against %s", h.id, stop)
		}
		if w.l.liveLen() != 1 {
			w.fail("send-now stops beside another live entry")
		}
		w.tok++
		plan := w.l.readerStop(h.id, w.tok)
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
	switch w.r.IntN(27) {
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
		// As in sendNow: a cancel of the failsafe's compaction drops its resume.
		if c := w.l.compaction(); c != nil && (c.origin != marotte.RunFromFailsafe || w.r.IntN(4) == 0) {
			if c.state == marotte.RunRunning {
				w.cancelled[c.id] = true
			}
			w.l.cancelCompaction(c.id)
		}
	case 20:
		w.ends()
	case 21:
		w.gate = !w.gate && w.r.IntN(4) == 0
	case 22:
		w.takeIdle()
	case 23, 24, 25:
		w.failsafe()
	case 26:
		w.refill()
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

// ends moves the steer queue's pending-end count, or has a turn end leave a steer of the reader's unread.
func (w *listWorld) ends() {
	if w.r.IntN(2) == 0 {
		w.ids++
		id := "s-" + strconv.Itoa(w.ids)
		w.l.insertPending(&runEntry{
			id: id, kind: marotte.RunKindRow, origin: marotte.RunFromPrompt, state: marotte.RunInKAS, offer: 1,
			want: marotte.RunWantEither, row: &marotte.QueuedPrompt{ID: id, Text: id},
		})
		w.l.turnEnded([]string{id})
		return
	}
	n := 0
	if w.r.IntN(4) == 0 {
		n = 1
	}
	w.l.endsChanged(uint64(w.step), n)
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
		"turn under a compaction", "wire turn beside a taken turn", "wire turn beside a sent prompt",
		"fold turn ahead of its compaction", "run entry", "take row", "take compaction", "take unread", "take retry",
		"compaction requeued", "withdraw", "stop unsent", "stop running", "stop undone", "idle revert", "idle turn",
		"failsafe fired", "failsafe undone", "take resume", "failsafe ahead of a waiting resume",
		"failsafe at the resume limit", "resume limit notice at the close",
		"reader stop ahead of a resume",
	} {
		if reached[want] == 0 {
			t.Errorf("the generator never reached %q", want)
		}
	}
}

// holdWorld drives a run list through the events the user's Stop ruling names (Stop, the user's send, Resume, a wake,
// an API caller's send, a turn kiro-cli opens, a clean or failed close, a failed launch, a send-now, a close and
// reopen, a restart) and records what the ruling says about the queue: a reader's Stop with rows waiting, or a row
// an outside caller sends while the stopped turn is live, holds the queue until the user sends or resumes.
type holdWorld struct {
	*tableWorld
	t    *testing.T
	r    *rand.Rand
	step int
	ids  int
	tok  uint64
	// held is the ruling's state: no row may be taken or steered while it is set.
	held bool
	// acted: the event was the user's send, send-now or Resume, after which no row waits on the reader.
	acted bool
	// stopping names the turns a reader's Stop is ending, whose window a late row joins paused.
	stopping map[string]bool
	log      []string
}

func (h *holdWorld) fail(format string, args ...any) {
	h.t.Helper()
	h.t.Fatalf("step %d: %s\nlist: %s\nevents:\n%s", h.step, fmt.Sprintf(format, args...), h.order(), strings.Join(h.log[max(0, len(h.log)-16):], "\n"))
}

func (h *holdWorld) id(prefix string) string { h.ids++; return prefix + strconv.Itoa(h.ids) }

// waitingRows reports a row waiting on the list (pending or paused), which a Stop pauses.
func (h *holdWorld) waitingRows() bool {
	return slices.ContainsFunc(h.l.entries, func(e *runEntry) bool {
		return e.kind == marotte.RunKindRow && (e.state == marotte.RunPending || e.state == marotte.RunPaused)
	})
}

func (h *holdWorld) liveTurns() []*runEntry {
	var out []*runEntry
	for _, e := range h.l.entries[:h.l.liveLen()] {
		if e.turnID != "" && e.state != marotte.RunClosing {
			out = append(out, e)
		}
	}
	return out
}

func (h *holdWorld) do() {
	op := h.r.IntN(12)
	if op <= 4 && len(h.l.entries) >= 6 {
		op = 8
	}
	switch op {
	case 0, 1:
		h.log = append(h.log, "user send")
		h.userActs()
		want := marotte.RunWantPrompt
		if h.r.IntN(2) == 0 {
			want = marotte.RunWantEither
		}
		h.user(h.id("u-"), want)
	case 2, 3:
		h.log = append(h.log, "wake")
		h.lateJoin()
		h.door(h.id("d-"), h.id("new "))
	case 4:
		h.log = append(h.log, "api send")
		h.lateJoin()
		h.api(h.id("a-"), marotte.RunWantPrompt)
	case 5:
		h.stop()
	case 6:
		h.log = append(h.log, "resume")
		h.userActs()
		h.l.resumeQueue()
		h.advance()
	case 7:
		h.kiroCLITurn()
	case 8, 9:
		h.closeOne()
	case 10:
		h.launchOne()
	case 11:
		h.lifeEvent()
	}
}

// userActs is the user's send or Resume: the queue goes on, and the stopped turn's late window holds nothing more.
func (h *holdWorld) userActs() {
	h.held, h.acted = false, true
	clear(h.stopping)
}

// lateJoin is the ruling's late window: a row an outside caller sends while a turn the reader stopped is live joins
// the queue paused.
func (h *holdWorld) lateJoin() {
	for _, e := range h.liveTurns() {
		if h.stopping[e.turnID] {
			h.held = true
		}
	}
}

func (h *holdWorld) stop() {
	h.tok++
	if h.r.IntN(4) == 0 {
		h.sendNow()
		return
	}
	h.log = append(h.log, "stop")
	plan := h.l.readerStop("", h.tok)
	switch plan.arm {
	case stopWithdraw:
		h.tableWorld.held = false
		h.l.reservationReleased(plan.res.Token)
		h.l.launched(plan.entry.id, plan.res.Token, "", launchWithdrawn, command.ErrTakeWithdrawn)
	case stopRunning, stopUnsent:
		for _, e := range h.liveTurns() {
			if e.stopBy == h.tok {
				h.stopping[e.turnID] = true
			}
		}
	case stopNothing, stopRun, stopSession:
		return
	}
	if h.waitingRows() {
		h.held = true
	}
	h.advance()
}

// sendNow is the user's send-now press on a waiting row of theirs, which stops the turn it was drawn against.
func (h *holdWorld) sendNow() {
	var rows []*runEntry
	for _, e := range h.l.entries[h.l.liveLen():] {
		if e.byUser() && e.kind == marotte.RunKindRow && (e.state == marotte.RunPending || e.state == marotte.RunPaused) {
			rows = append(rows, e)
		}
	}
	if len(rows) == 0 {
		return
	}
	h.log = append(h.log, "send-now")
	h.userActs()
	head, err := h.l.sendNow(rows[h.r.IntN(len(rows))].id, "", false)
	if err != nil || head == nil {
		h.advance()
		return
	}
	plan := h.l.readerStop(head.id, h.tok)
	switch plan.arm {
	case stopWithdraw:
		h.tableWorld.held = false
		h.l.reservationReleased(plan.res.Token)
		h.l.launched(plan.entry.id, plan.res.Token, "", launchWithdrawn, command.ErrTakeWithdrawn)
	case stopRunning, stopUnsent, stopNothing, stopRun, stopSession:
	}
	h.advance()
}

// kiroCLITurn opens a turn kiro-cli runs by itself (its re-prompt after a cancel), on an idle list or beside the
// turn a Stop is ending.
func (h *holdWorld) kiroCLITurn() {
	live := h.l.entries[:h.l.liveLen()]
	if len(live) > 1 || (len(live) == 1 && live[0].state != marotte.RunStopping) {
		return
	}
	h.log = append(h.log, "kiro-cli turn")
	h.wireOpen(h.id("w-"), true)
}

// closeOne closes a live turn, clean or failed, kiro-cli first leaving the steers it holds unread.
func (h *holdWorld) closeOne() {
	live := h.liveTurns()
	if len(live) == 0 {
		return
	}
	e := live[h.r.IntN(len(live))]
	var held []string
	for _, s := range h.l.entries {
		if s.inFlight() {
			held = append(held, s.id)
		}
	}
	clean := h.r.IntN(2) == 0
	h.log = append(h.log, fmt.Sprintf("close %s clean=%v", e.id, clean))
	h.l.turnEnded(held)
	delete(h.stopping, e.turnID)
	h.closed(e.id, listClose{clean: clean, completed: clean})
}

// launchOne moves a take on: its turn opens and its prompt is written, or its launch fails; kiro-cli takes a steer
// offered, or refuses it.
func (h *holdWorld) launchOne() {
	for _, e := range h.l.entries {
		switch {
		case e.state == marotte.RunOffered && h.r.IntN(2) == 0:
			h.log = append(h.log, "steer queued "+e.id)
			h.queued(e.id)
			return
		case e.state == marotte.RunOffered:
			h.log = append(h.log, "steer refused "+e.id)
			h.l.steerBack(e.id)
			h.advance()
			return
		case e.state == marotte.RunStarting && e.turnID == "" && h.r.IntN(4) == 0:
			h.log = append(h.log, "failed launch "+e.id)
			h.failLaunch(h.t, e.id)
			return
		case e.state == marotte.RunStarting && e.turnID == "":
			h.log = append(h.log, "opened "+e.id)
			h.running(e.id)
			h.prompted(e.id)
			return
		}
	}
}

// lifeEvent closes and reopens the chat in the same process, or restarts marotte, on a list with nothing live.
func (h *holdWorld) lifeEvent() {
	if h.l.liveLen() != 0 {
		return
	}
	next, _ := h.l.endLife()
	if h.r.IntN(3) == 0 {
		// A restart holds every row it keeps for the user (RunHeld) and drops the wakes: nothing is paused.
		h.log = append(h.log, "restart")
		next = marotte.SanitizeRun(h.l.project(), true)
		h.held = false
	} else {
		h.log = append(h.log, "reopen")
	}
	h.l = &runList{}
	h.l.load(next, next, 0)
	h.tableWorld.held = false
	h.advance()
}

// check asserts the ruling after one event: nothing taken or steered while the queue is held, nothing left waiting on
// the reader after the user acts, and one caller has at most one waiting row.
func (h *holdWorld) check() {
	fx := h.l.takeEffects()
	if n := len(h.log); n > 0 && !strings.Contains(h.log[n-1], " -> ") {
		h.log[n-1] += " -> " + h.order()
	}
	if h.held {
		for _, e := range fx.launch {
			if e.kind == marotte.RunKindRow || e.kind == marotte.RunKindRetry {
				h.fail("%s taken while a Stop holds the queue", e.id)
			}
		}
		for _, e := range fx.steer {
			h.fail("%s steered while a Stop holds the queue", e.id)
		}
	}
	if h.acted && slices.ContainsFunc(h.l.project(), func(e marotte.RunEntry) bool { return e.StopPaused }) {
		h.fail("a row still waits on the reader after the user's send or Resume")
	}
	h.acted = false
	waiting := map[string]int{}
	for _, e := range h.l.entries {
		if e.origin == marotte.RunFromNotify && (e.state == marotte.RunPending || e.state == marotte.RunPaused) {
			if waiting[e.row.Caller.Label]++; waiting[e.row.Caller.Label] > 1 {
				h.fail("caller %s has two waiting rows", e.row.Caller.Label)
			}
		}
	}
}

// The user's Stop ruling as a property: after a Stop that paused the queue, no row is taken or steered until the
// user sends or resumes, whatever kiro-cli, the outside callers, the closes, a failed launch or a reopen do.
func TestRunList_AStopHoldsTheQueueUntilTheUserActs(t *testing.T) {
	steps := 400
	if testing.Short() {
		steps = 100
	}
	held := 0
	for seed := range uint64(200) {
		t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
			h := &holdWorld{
				tableWorld: newTableWorld(), t: t, r: rand.New(rand.NewPCG(seed, seed^0x51ab)),
				stopping: map[string]bool{},
			}
			for h.step = range steps {
				h.do()
				h.check()
				if h.held {
					held++
				}
			}
		})
	}
	if held == 0 {
		t.Error("the generator never held the queue")
	}
}
