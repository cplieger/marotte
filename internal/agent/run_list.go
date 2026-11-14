package agent

// runList is a chat's one ordered list of what runs next (chatLifecycle.run, guarded by lc.mu). It is a pure state
// machine: each operation takes the facts it needs, changes the entries and records effects for the glue
// (run_queue.go) to carry out once lc.mu is released. Only advance takes an entry.

import (
	"errors"
	"slices"
	"strconv"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// launchWait is one entry's first outcome for the POST that waits on it: closed at the take's turn open, at a refusal
// before it, once kiro-cli holds the entry's steer (steered), when the entry waits on the list as neither a take nor a
// steer (queued) or at the life's end. Closed exactly once, under lc.mu.
type launchWait struct {
	ch     chan struct{}
	err    error
	turnID string
	closed bool
	// queued answers as: queued; composer says the waiter owes the composer settle.
	queued, composer bool
	steered          bool
}

func newLaunchWait() *launchWait { return &launchWait{ch: make(chan struct{})} }

func (w *launchWait) finish(turnID string, err error, queued, composer bool) {
	if w.closed {
		return
	}
	w.closed, w.turnID, w.err, w.queued, w.composer = true, turnID, err, queued, composer
	close(w.ch)
}

func (w *launchWait) finishSteered() {
	if w.closed {
		return
	}
	w.closed, w.steered = true, true
	close(w.ch)
}

// compactWait is a compaction attempt's outcome for a /compact POST that waits on it.
type compactWait struct {
	ch       chan struct{}
	err      error
	closed   bool
	accepted bool
	queued   bool
}

func newCompactWait() *compactWait { return &compactWait{ch: make(chan struct{})} }

func (w *compactWait) finish(accepted, queued bool, err error) {
	if w.closed {
		return
	}
	w.closed, w.accepted, w.queued, w.err = true, accepted, queued, err
	close(w.ch)
}

// launchOpts are a submit-origin entry's launch options, in memory only; only the model is persisted (on its row).
type launchOpts struct {
	model string
	// composer is the composer settle a page's POST owes, claimed once (claimComposer or a queued answer).
	composer bool
	awaited  bool
}

type runEntry struct {
	row    *marotte.QueuedPrompt
	launch *launchWait
	done   *compactWait
	opts   launchOpts
	id     string
	kind   marotte.RunEntryKind
	state  marotte.RunEntryState
	origin marotte.RunEntryOrigin
	prior  marotte.RunEntryState
	turnID string
	// replaces is a retry's empty turn, passed as TurnOpen.Replaces so a stop during its refresh refuses the open.
	replaces string
	// forTurn is the turn whose reading or end asked for a compaction (cancel_compact declines it).
	forTurn string
	// fsPrior and fsPriorFor are the origin and turn a compaction had before the failsafe took it over, given back
	// when the failsafe's stop is undone.
	fsPrior    marotte.RunEntryOrigin
	fsPriorFor string
	want       marotte.RunWant
	// joined are the Unread rows of the lead's sender its prompt carries, taken off the list with it.
	joined   []*runEntry
	res      command.Reservation
	afterSeq uint64
	// offer is the token of the row's latest offer as a steer (orderFacts.newOffer), 0 before its first.
	offer  uint64
	stopBy uint64
	// pausedBy is the stop that paused the row; on a steer kiro-cli holds, the reader's stop that pauses it when it
	// comes back (holdReturns).
	pausedBy uint64
	// fsToken is the failsafe stop that listed this resume or took over this compaction.
	fsToken uint64
	next    bool
	retry   bool
	// submitter: submit's own entry, whose POST still waits on launch.
	submitter bool
	yields    bool
	steerable bool
	admits    bool
	tailDone  bool
	released  bool
	clean     bool
	cancelled bool
	sawTurn   bool
	waiter    bool
	// fsStopped marks a turn the failsafe stopped: a close whose own outcome is clean (listClose.completed) has nothing
	// to resume.
	fsStopped bool
	// fsNoResume marks a failsafe stop at compactionFailsafeResumes, which listed no resume: its close tells the reader
	// while fsStopped holds.
	fsNoResume bool
	unread     bool
	// prompted: the turn's session/prompt is written, so kiro-cli has a turn to file a steer in.
	prompted bool
	// userNext: the turn is one the user's send-now or stop asked for (marotte.Delivery.UserNext).
	userNext bool
	// resent: an earlier offer of the row may have reached kiro-cli under its key.
	resent bool
	// pausedBefore: a broken close or failed launch had paused the row (pausedBy 0) when the reader's stop took it
	// over, so that stop's undo leaves it paused.
	pausedBefore bool
	// afterSend: the reader's send released this stamped steer kiro-cli holds (releaseHold), so it comes back paused
	// with pausedBy 0 and waits for the next clean close, never steered into that send's reply (returned).
	afterSend bool
}

func (e *runEntry) live() bool { return e.state.Live() }

// byUser reports an entry the user sent: one whose row names no caller.
func (e *runEntry) byUser() bool { return e.row == nil || e.row.Caller == nil }

// inFlight reports a row handed to kiro-cli as a steer and not yet read or returned.
func (e *runEntry) inFlight() bool {
	return e.state == marotte.RunOffered || e.state == marotte.RunInKAS
}

func (e *runEntry) wire() bool {
	return e.kind == marotte.RunKindTurn && e.origin == marotte.RunFromWire
}

func (e *runEntry) runEntry() bool {
	return e.kind == marotte.RunKindTurn && e.origin == marotte.RunFromRun
}

// kiroCLI is a turn kiro-cli runs by itself: a wire turn or a run with no turn here.
func (e *runEntry) kiroCLI() bool { return e.wire() || e.runEntry() }

// takeAnswer is the admission slot's answer to a take.
type takeAnswer uint8

const (
	takeGranted takeAnswer = iota
	// takeWait: a held close or a reservation a replaced entry's launch still holds; a later wake retries.
	takeWait
	// takeClosed: a close committed since the stamp the take presented.
	takeClosed
	// takeGone: the chat is deleted.
	takeGone
)

// orderFacts are the lifecycle's facts an operation reads, gathered under the same lc.mu.
type orderFacts struct {
	// reserve takes the admission slot (turnRegistry.reserveLocked) for e at stamp.
	reserve func(e *runEntry, stamp uint64) (command.Reservation, takeAnswer)
	// newOffer mints a steer offer's token, unique in the process, so a fate the steer record reports for an earlier
	// offer, in this life or another, never matches a later one.
	newOffer func() uint64
	stamp    uint64
	// nextSeq and stopSeq are the lifecycle's open and stop sequences (chatLifecycle.nextSeq, stopSeq).
	nextSeq uint64
	stopSeq uint64
	stops   int
	// admit and sessionCall are kiro-cli's state (lc.kas.may()).
	admit       bool
	sessionCall bool
}

// runEffects are what the glue owes once lc.mu is released.
type runEffects struct {
	// launch lists the entries advance took, in take order.
	launch []*runEntry
	// toast lists the compactions whose failure the reader is told of (compactFailed).
	toast []*runEntry
	// notices are the words the reader is told a stopped turn is not continued in (ErrCodeResumeNotSent).
	notices []string
	// steer lists the rows advance offered as steers into the running turn, in offer order.
	steer []*runEntry
	// changed: the projection changed, so the version is bumped and the list written and announced.
	changed bool
}

// runList's live prefix is one entry, or two in one of three shapes: a compaction running with kiro-cli's own turn
// beside it (a turn under a compaction); kiro-cli's wire turn ahead of a marotte turn taken or sent beside it (a wire
// turn beside a taken turn); and a fold-opened wire turn ahead of the compaction that closes it (a fold turn ahead of
// its compaction).
type runList struct {
	// intent is submit's own entry and stamp for the advance it runs; intentRefused its refusal.
	intent   *runEntry
	crossed  string
	declined string
	// failsafeFor is the last turn the failsafe fired in: once per turn, even when its stop was undone.
	failsafeFor string
	// failsafeOwed is the turn a fire was skipped in because an ask was open, fired once the asks settle
	// (autoCompactor.fireOwed); it lapses with the turn, since failsafe fires only in the running turn it names.
	failsafeOwed string
	// steerRefused are the door entries kiro-cli refused as a steer since a turn's prompt was written or a turn
	// closed clean: they wait for a turn of their own rather than being offered into a full buffer again.
	steerRefused []string
	entries      []*runEntry
	stored       []marotte.RunEntry
	fx           runEffects
	// wireSeq is the open sequence of the newest wire turn: a retry overtaken by kiro-cli's own turn is dropped.
	wireSeq     uint64
	intentStamp uint64
	// pausing is the last stop that paused the queue (pauseRows): an outside caller's row sent while a turn it stopped
	// is live joins the queue paused (queuePaused).
	pausing uint64
	endsSeq uint64
	seq     uint64
	version uint64
	// resumed counts the resumes opened since the last turn the reader sent opened (compactionFailsafeResumes).
	resumed       int
	intentRefused takeAnswer
	endsOwed      bool
	// pressed: a send-now press marked its lead (sendNow, leadSteer) for the reader's stop that follows it.
	pressed bool
	// stopped: the chat's last head close was a stopped turn's.
	stopped bool
	dirty   bool
	ended   bool
	// failsafeHeld: a failsafe compaction was accepted and no reading under the point has come since.
	failsafeHeld bool
}

var (
	errRowNotQueued = errors.New("no queued message has that id")
	errRowSending   = errors.New("that message is already being sent")
	errRowInKAS     = errors.New("kiro-cli holds that message as a steer")
	errRowHeld      = errors.New("that message is held")
)

func (l *runList) touch() { l.fx.changed = true }

// owesEffects reports effects recorded since the last takeEffects.
func (l *runList) owesEffects() bool {
	return l.fx.changed || len(l.fx.launch) > 0 || len(l.fx.toast) > 0 || len(l.fx.notices) > 0 || len(l.fx.steer) > 0
}

// takeEffects hands the recorded effects to the glue, bumping the version when the projection changed.
func (l *runList) takeEffects() runEffects {
	fx := l.fx
	l.fx = runEffects{}
	if fx.changed {
		l.version++
	}
	return fx
}

func (l *runList) liveLen() int {
	n := 0
	for n < len(l.entries) && l.entries[n].live() {
		n++
	}
	return n
}

func (l *runList) find(id string) (int, *runEntry) {
	for i, e := range l.entries {
		if e.id == id {
			return i, e
		}
	}
	return -1, nil
}

func (l *runList) byTurn(turnID string) (int, *runEntry) {
	if turnID == "" {
		return -1, nil
	}
	for i, e := range l.entries {
		if e.turnID == turnID {
			return i, e
		}
	}
	return -1, nil
}

func (l *runList) byToken(token uint64) *runEntry {
	if token == 0 {
		return nil
	}
	for _, e := range l.entries {
		if e.res.Token == token {
			return e
		}
	}
	return nil
}

func (l *runList) compaction() *runEntry {
	for _, e := range l.entries {
		if e.kind == marotte.RunKindCompaction {
			return e
		}
	}
	return nil
}

func (l *runList) mint(prefix string) string {
	l.seq++
	return prefix + strconv.FormatUint(l.seq, 10)
}

func (l *runList) remove(e *runEntry) {
	if i := slices.Index(l.entries, e); i >= 0 {
		l.entries = slices.Delete(l.entries, i, i+1)
		l.touch()
	}
}

func (l *runList) insert(at int, e *runEntry) {
	l.entries = slices.Insert(l.entries, at, e)
	l.touch()
}

// insertPending puts e before the first paused row, so the list order is the send order.
func (l *runList) insertPending(e *runEntry) {
	at := len(l.entries)
	for i := l.liveLen(); i < len(l.entries); i++ {
		if l.entries[i].state == marotte.RunPaused {
			at = i
			break
		}
	}
	l.insert(at, e)
}

// frontOfPending is the first index after the live prefix, behind a pending compaction when behindCompaction.
func (l *runList) frontOfPending(behindCompaction bool) int {
	at := l.liveLen()
	if behindCompaction && at < len(l.entries) && l.entries[at].kind == marotte.RunKindCompaction {
		at++
	}
	return at
}

func (l *runList) project() []marotte.RunEntry {
	out := make([]marotte.RunEntry, len(l.entries))
	for i, e := range l.entries {
		out[i] = l.projectOne(e)
	}
	return out
}

// putBack moves a taken entry back to state at the front of the pending entries with a clean head, the rows its prompt
// joined behind it: a waiting submitter is answered queued (with the composer settle it still owes), and a waiting
// /compact queued.
func (l *runList) putBack(e *runEntry, state marotte.RunEntryState, pausedBy uint64) {
	if e.launch != nil && e.submitter {
		e.launch.finish("", nil, true, e.opts.composer)
	}
	if e.done != nil && e.waiter {
		e.done.finish(false, true, nil)
		e.done = newCompactWait()
	}
	e.res, e.turnID = command.Reservation{}, ""
	e.released, e.tailDone = false, false
	e.submitter, e.opts.awaited, e.opts.composer, e.waiter = false, false, false, false
	e.stopBy, e.prior, e.prompted, e.userNext = 0, "", false, false
	e.state, e.pausedBy, e.pausedBefore, e.afterSend = state, pausedBy, false, false
	if e.launch != nil {
		e.launch = newLaunchWait()
	}
	l.remove(e)
	at := l.frontOfPending(e.kind != marotte.RunKindCompaction)
	l.insert(at, e)
	for i, j := range e.joined {
		j.state, j.pausedBy = e.state, e.pausedBy
		l.insert(at+1+i, j)
	}
	e.joined = nil
}

// requeue puts back a take that did not open, paused by the stop by, which waits for the user's send or Resume, or by
// 0 (a failed launch), which waits for the next clean close. An outside caller's goes behind the user's waiting rows;
// a wake whose caller has a row waiting is folded into that newer row (one waiting row per caller), which goes behind
// them in its place.
func (l *runList) requeue(e *runEntry, by uint64) {
	if e.origin == marotte.RunFromNotify && l.waitingWake(e) != nil {
		if e.launch != nil && e.submitter {
			e.launch.finish("", nil, true, false)
		}
		e.submitter = false
		l.remove(e)
		l.doorsBehindUsers()
		return
	}
	l.putBack(e, marotte.RunPaused, by)
	if !e.byUser() {
		l.doorsBehindUsers()
	}
}

// requeueUnsent puts an outside caller's row whose prompt the stop by kept unsent back on the queue paused, under a
// new message id since its opened turn keeps the old one: its words reached no one, and the caller's message may be
// its only copy. A wake whose caller has a row waiting adds nothing, and that row goes behind the user's.
func (l *runList) requeueUnsent(e *runEntry, by uint64) {
	if e.byUser() || e.kind != marotte.RunKindRow {
		return
	}
	if e.origin == marotte.RunFromNotify && l.waitingWake(e) != nil {
		l.doorsBehindUsers()
		return
	}
	row := *e.row
	row.ID = "m-" + newMessageID()
	l.insert(l.frontOfPending(true), &runEntry{
		id: row.ID, kind: marotte.RunKindRow, origin: e.origin, row: &row, state: marotte.RunPaused, pausedBy: by,
		want: marotte.RunWantPrompt,
	})
	l.doorsBehindUsers()
}

// waitingWake is of's caller's other notify row waiting on the list, pending or paused, or nil.
func (l *runList) waitingWake(of *runEntry) *runEntry {
	for _, e := range l.entries[l.liveLen():] {
		if (e.state == marotte.RunPending || e.state == marotte.RunPaused) && sameWakeCaller(e, of) {
			return e
		}
	}
	return nil
}

// sameWakeCaller reports e as a notify-door row other than of from of's caller; of is a notify-door row, and every
// one carries its caller (command.Notify).
func sameWakeCaller(e, of *runEntry) bool {
	return e != of && e.origin == marotte.RunFromNotify && e.row.Caller.Label == of.row.Caller.Label
}

// pauseRows pauses every pending row but send-now's, stamped with the stop that paused it, and fixes it to a turn of
// its own: a paused row is never steered, even once unpaused. A stop pauses every sender's rows; a broken close (by 0)
// leaves a wake waiting, which goes once nothing of the user's waits ahead (marotte.Deliver). A steer that came back
// from kiro-cli (unread or refused) is left to holdReturns, so a broken close or the failsafe's stop does not hold it
// behind the rows it paused.
func (l *runList) pauseRows(by uint64) {
	if by != 0 {
		l.pausing = by
	}
	for _, e := range l.entries[l.liveLen():] {
		if e.state == marotte.RunPending && e.kind == marotte.RunKindRow && !e.next && e.offer == 0 &&
			(by != 0 || e.origin != marotte.RunFromNotify) {
			l.pause(e, by)
		}
	}
}

func (l *runList) pause(e *runEntry, by uint64) {
	e.state, e.pausedBy, e.pausedBefore, e.afterSend, e.want = marotte.RunPaused, by, false, false, marotte.RunWantPrompt
	l.touch()
}

// holdReturns is the rest of the reader's stop by: the rows a broken close or failed launch paused are held with the
// rest, the rows still pending (a steer kiro-cli gave back, a lead an earlier press left waiting) are paused, and every
// steer kiro-cli holds is stamped to come back paused (returned). The user's are left out when the stop is a send-now
// press: the press asked for them, behind or joined to its lead (marotte.Deliver).
func (l *runList) holdReturns(by uint64, press bool) {
	for _, e := range l.entries[l.liveLen():] {
		switch {
		case e.state == marotte.RunPaused && e.pausedBy == 0 && e.kind == marotte.RunKindRow:
			e.pausedBy, e.pausedBefore = by, true
			l.touch()
		case press && e.byUser():
		case e.inFlight():
			e.pausedBy = by
		case e.state == marotte.RunPending && e.kind == marotte.RunKindRow && (!e.next || !press):
			l.pause(e, by)
		}
	}
}

// queuePaused reports a queue a stop paused, with the pausedBy a row joining it takes: the stop while a turn it stopped
// is live, so the stop's undo unpauses what joined meanwhile; once that turn is gone, loadedStop, which no undo
// reaches, or the failsafe's stop while its resume is listed, whose clean close unpauses the row with the rest.
func (l *runList) queuePaused() (by uint64, paused bool) {
	for _, e := range l.entries[:l.liveLen()] {
		if l.pausing != 0 && e.stopBy == l.pausing {
			return l.pausing, true
		}
	}
	for _, e := range l.entries[l.liveLen():] {
		if e.kind != marotte.RunKindRow || e.state != marotte.RunPaused || e.pausedBy == 0 {
			continue
		}
		if l.stopPaused(e) {
			return loadedStop, true
		}
		by, paused = e.pausedBy, true
	}
	return by, paused
}

// resumeQueue is the reader's Resume: the hold is lifted (releaseHold) and every paused row goes back to pending in its
// place.
func (l *runList) resumeQueue() {
	l.releaseHold(false)
	l.unpauseRows()
}

// held reports a queue the reader's stop holds: a row it paused waits (stopPaused). Only the reader's Resume or send
// lifts it (releaseHold); no close, wake, failed launch, kiro-cli turn or reopen does.
func (l *runList) held() bool { return slices.ContainsFunc(l.entries, l.stopPaused) }

// mayLaunch is the one check a row or retry passes before it is taken or offered as a steer: none while held.
func (l *runList) mayLaunch(e *runEntry) bool {
	return (e.kind != marotte.RunKindRow && e.kind != marotte.RunKindRetry) || !l.held()
}

// releaseHold is the reader's send (send) or Resume: the rows a stop holds wait for the next clean close instead
// (pausedBy 0), and a row an outside caller sends while the stopped turn still closes joins the queue pending
// (queuePaused). A steer kiro-cli holds that a stop stamped comes back pending after a Resume, and after a send comes
// back paused like the released rows (afterSend), so it waits for the next clean close and is never steered into the
// send's reply.
func (l *runList) releaseHold(send bool) {
	for _, e := range l.entries {
		switch {
		case l.stopPaused(e):
			e.pausedBy, e.pausedBefore = 0, false
			l.touch()
		case e.inFlight() && e.pausedBy != 0:
			e.pausedBy, e.afterSend = 0, send
			l.touch()
		}
	}
	l.pausing = 0
}

func (l *runList) unpauseRows() {
	for _, e := range l.entries {
		if e.state == marotte.RunPaused {
			e.state, e.pausedBy, e.pausedBefore, e.afterSend = marotte.RunPending, 0, false, false
			l.touch()
		}
	}
}

// endIfDone ends a closed entry once its close tail ended and its reservation was given back; a revert, which has
// no tail, once it was given back.
func (l *runList) endIfDone(e *runEntry) {
	if !e.released || (e.kind != marotte.RunKindRevert && !e.tailDone) {
		return
	}
	l.remove(e)
}

// normalize keeps kiro-cli's state on the list: a run entry stands for a run KAS reports with no turn here,
// and a lone yielding wire turn carries whether a take may go beside it.
func (l *runList) normalize(f *orderFacts) {
	n := l.liveLen()
	switch {
	case n == 0 && !f.admit:
		l.insert(0, &runEntry{
			id: l.mint("run-"), kind: marotte.RunKindTurn, origin: marotte.RunFromRun,
			state: marotte.RunRunning, released: true,
		})
	case n >= 1 && l.entries[0].runEntry() && f.admit:
		l.remove(l.entries[0])
	}
	if l.liveLen() == 1 && l.entries[0].wire() && l.entries[0].yields && l.entries[0].admits != f.admit {
		l.entries[0].admits = f.admit
		l.touch()
	}
}

// beside is the lone yielding wire head a take may go beside, or nil.
func (l *runList) beside() *runEntry {
	if l.liveLen() == 1 {
		if h := l.entries[0]; h.wire() && h.yields && h.state == marotte.RunRunning {
			return h
		}
	}
	return nil
}

// overtaken reports a retry whose empty turn a later wire turn or a stop has overtaken.
func overtaken(e *runEntry, f *orderFacts, wireSeq uint64) bool {
	return wireSeq > e.afterSeq || (f.stopSeq != 0 && e.afterSeq <= f.stopSeq)
}

// advance is the one taker: it takes the first pending entry when the head rule allows, or offers a row as a steer
// into the running turn, or nothing. marotte.Deliver picks among rows; every other entry keeps its own take rule.
func (l *runList) advance(f *orderFacts) {
	if l.ended {
		return
	}
	defer l.answerSteerWaits()
	l.normalize(f)
	if l.endsOwed {
		return
	}
	n := l.liveLen()
	by := l.beside()
	if n > 0 && by == nil {
		l.offerSteer(f)
		return
	}
	e, dl := l.pick(f)
	if e == nil || !l.mayLaunch(e) {
		return
	}
	switch {
	case e.kind == marotte.RunKindCompaction:
		if !f.sessionCall || f.stops > 0 {
			return
		}
	case e.kind == marotte.RunKindRevert && by != nil, !f.admit:
		return
	default:
		if !l.reserveFor(e, f) {
			return
		}
	}
	l.take(e, n)
	if dl.Kind == marotte.DeliverPrompt {
		l.carry(e, dl)
	}
}

// pick is the entry advance would take, or nil: the first pending entry, or the row marotte.Deliver takes when that is
// a row, with its delivery.
func (l *runList) pick(f *orderFacts) (*runEntry, marotte.Delivery) {
	e := l.nextPending(f)
	if e == nil || e.kind != marotte.RunKindRow {
		return e, marotte.Delivery{}
	}
	dl := marotte.Deliver(l.project(), l.headFacts())
	if dl.Kind != marotte.DeliverPrompt {
		return nil, dl
	}
	_, e = l.find(dl.IDs[0])
	return e, dl
}

// offerSteer hands the row marotte.Deliver steers into the running turn to the glue (fx.steer), marked offered.
func (l *runList) offerSteer(f *orderFacts) {
	dl := marotte.Deliver(l.project(), l.headFacts())
	if dl.Kind != marotte.DeliverSteer {
		return
	}
	if _, e := l.find(dl.IDs[0]); e != nil && l.mayLaunch(e) {
		e.state, e.resent, e.offer = marotte.RunOffered, e.offer != 0, f.newOffer()
		l.fx.steer = append(l.fx.steer, e)
		l.touch()
	}
}

// carry copies the delivery's UserNext onto the taken lead and takes the rows its prompt joins off the list.
func (l *runList) carry(lead *runEntry, dl marotte.Delivery) {
	lead.userNext = dl.UserNext
	for _, id := range dl.IDs[1:] {
		if _, e := l.find(id); e != nil {
			l.remove(e)
			lead.joined = append(lead.joined, e)
		}
	}
}

// headFacts are the facts marotte.Deliver reads beside the entries.
func (l *runList) headFacts() marotte.HeadFacts {
	h := marotte.HeadFacts{SteerRefused: l.steerRefused, Stopped: l.stopped, EndsOwed: l.endsOwed}
	if l.liveLen() == 1 {
		h.Prompted, h.UserNext = l.entries[0].prompted, l.entries[0].userNext
	}
	return h
}

// reserveFor takes the admission slot for e: at the life's stamp, or at the POST's intent stamp for submit's own
// entry, whose refusal submit answers.
func (l *runList) reserveFor(e *runEntry, f *orderFacts) bool {
	stamp := f.stamp
	if e == l.intent {
		stamp = l.intentStamp
	}
	res, a := f.reserve(e, stamp)
	if a != takeGranted {
		if e == l.intent {
			l.intentRefused = a
		}
		return false
	}
	e.res = res
	return true
}

// nextPending is the first pending entry, dropping on the way a retry a later turn or a stop overtook.
func (l *runList) nextPending(f *orderFacts) *runEntry {
	for {
		var e *runEntry
		for _, c := range l.entries[l.liveLen():] {
			if c.state == marotte.RunPending {
				e = c
				break
			}
		}
		switch {
		case e == nil:
			return nil
		case e.kind == marotte.RunKindRetry && overtaken(e, f, l.wireSeq):
			l.remove(e)
		default:
			return e
		}
	}
}

func (l *runList) take(e *runEntry, at int) {
	l.remove(e)
	l.insert(at, e)
	e.state = marotte.RunStarting
	if e.kind == marotte.RunKindRevert {
		e.state = marotte.RunRunning
		l.dropResume()
	}
	l.fx.launch = append(l.fx.launch, e)
}

// answerSteerWaits answers each submitter whose row is not taken by its row's first outcome as a steer: steered once
// kiro-cli holds it, queued once it waits on the list as anything but a steer in flight (marotte.RunEntryView). A
// taken row's submitter waits on its launch.
func (l *runList) answerSteerWaits() {
	var view *marotte.RunView
	for _, e := range l.entries {
		if !e.submitter || e.launch == nil || e.launch.closed || e.live() || e.state == marotte.RunOffered {
			continue
		}
		if e.state == marotte.RunInKAS {
			answerSteered(e)
			continue
		}
		if view == nil {
			v := marotte.RunViewOf(l.project(), 0)
			view = &v
		}
		if !steerInFlight(view, e.id) && !l.doorSteerBound(e) {
			e.launch.finish("", nil, true, answerQueued(e))
		}
	}
}

// doorSteerBound reports a door entry the running turn takes as a steer (the user's twin is
// marotte.RunEntryView.SteerInFlight).
func (l *runList) doorSteerBound(e *runEntry) bool {
	return !e.byUser() && l.steersOnceSettled(l.project(), e.id)
}

// steersOnceSettled reports that marotte.Deliver steers id from entries now, or will once the two holds that settle by
// themselves have: the head's prompt written and the one steer offered answered.
func (l *runList) steersOnceSettled(entries []marotte.RunEntry, id string) bool {
	for i := range entries {
		if entries[i].State == marotte.RunOffered {
			entries[i].State = marotte.RunInKAS
		}
	}
	h := l.headFacts()
	h.Prompted = true
	dl := marotte.Deliver(entries, h)
	return dl.Kind == marotte.DeliverSteer && dl.IDs[0] == id
}

// answerSteered answers e's submitter steered. A steer owes no composer settle, and e taken later as a turn of its own
// claims none, since the composer may hold the user's next words by then.
func answerSteered(e *runEntry) {
	e.submitter, e.opts.composer = false, false
	e.launch.finishSteered()
}

func steerInFlight(v *marotte.RunView, id string) bool {
	for i := range v.Entries {
		if v.Entries[i].ID == id {
			return v.Entries[i].SteerInFlight
		}
	}
	return false
}

// submitAnswer is submit's outcome: taken, refused, steering (the submitter waits on the entry's steer), or none of
// them (the entry waits on the list).
type submitAnswer struct {
	refused  takeAnswer
	taken    bool
	steering bool
}

// submit inserts a sent message as a pending row and runs advance at the POST's intent stamp for that entry only. The
// user's row goes ahead of the paused rows and releases the reader's hold (releaseHold); a caller's row goes last,
// behind the user's paused rows, and paused while the queue is (queuePaused). The caller answers an id already on the
// list and never submits it.
func (l *runList) submit(e *runEntry, f *orderFacts, intentStamp uint64, waits bool) submitAnswer {
	before := l.fx.changed
	e.state, e.submitter, e.launch = marotte.RunPending, waits, newLaunchWait()
	if !waits {
		e.opts.composer = false
	}
	if e.byUser() {
		l.releaseHold(true)
		l.insertPending(e)
	} else {
		if by, paused := l.queuePaused(); paused {
			e.state, e.pausedBy, e.want = marotte.RunPaused, by, marotte.RunWantPrompt
		}
		l.insert(len(l.entries), e)
	}
	l.intent, l.intentStamp, l.intentRefused = e, intentStamp, takeGranted
	l.advance(f)
	l.intent = nil
	switch {
	case e.state == marotte.RunStarting || e.state == marotte.RunRunning:
		return submitAnswer{taken: true}
	case l.intentRefused == takeClosed || l.intentRefused == takeGone:
		l.remove(e)
		l.fx.changed = before
		return submitAnswer{refused: l.intentRefused}
	case e.submitter:
		return submitAnswer{steering: true}
	}
	answerQueued(e)
	return submitAnswer{}
}

// answerQueued is the one section that answers e's submitter queued: it ends the wait and claims the composer
// settle, reporting whether the settle is owed.
func answerQueued(e *runEntry) bool {
	settle := e.submitter && e.opts.composer
	e.submitter, e.opts.composer = false, false
	return settle
}

// doorPlace is where a notify-door send went.
type doorPlace uint8

const (
	// doorPlaced: the send is a new entry (submit's answer says where).
	doorPlaced doorPlace = iota
	// doorReplaced: the caller's waiting entry took the send's text and keeps its id and place.
	doorReplaced
	// doorWithKAS: the caller's earlier entry is with kiro-cli as a steer, so the send adds nothing.
	doorWithKAS
	// doorTooBig: the send is past the draft cap and would wait behind a running turn; nothing changed.
	doorTooBig
)

// doorSend places a notify-door send: one waiting entry per caller, pending or paused, since a caller's later wake
// supersedes its earlier one, and none beside one kiro-cli holds unread, which the agent reads anyway. A waiting entry
// is persisted with the list, so one paused or behind a running turn keeps the draft cap a user's queued row has.
func (l *runList) doorSend(e *runEntry, f *orderFacts, intentStamp uint64, waits bool) (submitAnswer, doorPlace) {
	big := len(e.row.Text) > marotte.MaxDraftBytes
	for _, have := range l.entries[l.liveLen():] {
		if !sameWakeCaller(have, e) {
			continue
		}
		waiting := have.state == marotte.RunPending || have.state == marotte.RunPaused
		switch {
		case waiting && big && (have.state == marotte.RunPaused || turnRuns(l.project())):
			return submitAnswer{}, doorTooBig
		case waiting:
			have.row.Text = e.row.Text
			l.touch()
			return submitAnswer{}, doorReplaced
		case have.inFlight():
			return submitAnswer{}, doorWithKAS
		}
	}
	if e.want == "" {
		e.want = marotte.RunWantEither
	}
	_, paused := l.queuePaused()
	if big && (paused || l.waitsBehindTurn(e)) {
		return submitAnswer{}, doorTooBig
	}
	return l.submit(e, f, intentStamp, waits), doorPlaced
}

// waitsBehindTurn reports a door entry e that, appended now, would wait behind a running turn rather than be steered
// into it.
func (l *runList) waitsBehindTurn(e *runEntry) bool {
	entries := l.project()
	if !turnRuns(entries) {
		return false
	}
	probe := marotte.RunEntry{
		ID: e.id, Kind: marotte.RunKindRow, State: marotte.RunPending, Origin: e.origin, Row: e.row, Want: e.want,
	}
	return !l.steersOnceSettled(append(entries, probe), e.id)
}

// claimComposer is the open's claim on the composer settle: true for the one caller that settles.
func (l *runList) claimComposer(id string) bool {
	_, e := l.find(id)
	if e == nil || !e.opts.composer {
		return false
	}
	e.opts.composer = false
	return true
}

// idleAnswer is takeIdle's answer.
type idleAnswer uint8

const (
	idleGranted idleAnswer = iota
	// idleBusy refuses at once: a turn runs, or kiro-cli's state admits none.
	idleBusy
	// idleWait: the caller waits on lc.changed and asks again.
	idleWait
)

// takeIdle places a revert or a tangent summary, which run now or not at all.
func (l *runList) takeIdle(e *runEntry, f *orderFacts, intentStamp uint64) idleAnswer {
	if _, have := l.find(e.id); have != nil {
		if have.live() {
			return idleGranted
		}
		return idleWait
	}
	n := l.liveLen()
	for _, h := range l.entries[:n] {
		if h.kind.TurnKind() && (h.state == marotte.RunRunning || h.state == marotte.RunStopping) &&
			(h != l.beside() || e.kind == marotte.RunKindRevert) {
			return idleBusy
		}
	}
	if !f.admit {
		return idleBusy
	}
	switch {
	case l.beside() != nil:
		l.insert(1, e)
	case n > 0:
		return idleWait
	default:
		l.insert(0, e)
	}
	e.state = marotte.RunPending
	l.intent, l.intentStamp = e, intentStamp
	l.advance(f)
	l.intent = nil
	if e.live() {
		return idleGranted
	}
	return idleWait
}

// idleExpired is takeIdle's deadline, one section: a granted entry stays its caller's, an ungranted one goes.
func (l *runList) idleExpired(e *runEntry) bool {
	if e.live() {
		return true
	}
	l.remove(e)
	return false
}

// sendNow moves a row to the front of the pending entries, behind a pending compaction there, and names the entry
// the press stops: stop's entry only while it is the lone live turn, starting or running (hasStop false names
// RunView.Cancel's target under the same rule). A named entry marks the press for the stop the caller then makes
// (readerStop).
func (l *runList) sendNow(id, stop string, hasStop bool) (*runEntry, error) {
	_, e := l.find(id)
	switch {
	case e == nil || e.kind != marotte.RunKindRow || !e.byUser():
		return nil, errRowNotQueued
	case e.state == marotte.RunHeld:
		return nil, errRowHeld
	case e.live(), e.inFlight():
		return nil, errRowSending
	}
	l.remove(e)
	e.state, e.pausedBy, e.pausedBefore, e.next = marotte.RunPending, 0, false, true
	l.insert(l.frontOfPending(true), e)
	l.readerTookOver()
	l.releaseHold(true)
	if !hasStop {
		stop = marotte.CancelTarget(l.project())
	}
	if l.liveLen() != 1 {
		return nil, nil
	}
	h := l.entries[0]
	if h.id != stop || !h.kind.TurnKind() || h.runEntry() ||
		(h.state != marotte.RunStarting && h.state != marotte.RunRunning) {
		return nil, nil
	}
	l.pressed = true
	return h, nil
}

// clearNext is the undo of a send-now whose stop was not delivered: the row keeps its place.
func (l *runList) clearNext(id string) {
	if _, e := l.find(id); e != nil && e.next {
		e.next = false
		l.touch()
	}
}

// leadSteer marks the user's steer handed to kiro-cli under id as send-now's, so the turn end that returns it unread
// makes it the next prompt's lead (marotte.Deliver), and marks the press for the stop that follows (readerStop); it
// reports whether one was marked.
func (l *runList) leadSteer(id string) bool {
	_, e := l.find(id)
	if e == nil || !e.inFlight() || !e.byUser() {
		return false
	}
	e.next, l.pressed = true, true
	l.touch()
	return true
}

// unqueue removes a pending, paused or held row. A steer kiro-cli holds answers errRowInKAS: only a clear of its
// buffer takes it back.
func (l *runList) unqueue(id string) error {
	_, e := l.find(id)
	switch {
	case e == nil || e.row == nil:
		return errRowNotQueued
	case e.live(), e.state == marotte.RunOffered:
		return errRowSending
	case e.state == marotte.RunInKAS:
		return errRowInKAS
	}
	l.remove(e)
	return nil
}

// requestCompaction joins the chat's one compaction, or inserts one at the front of the pending entries.
func (l *runList) requestCompaction(origin marotte.RunEntryOrigin, forTurn string) (e *runEntry, joined bool) {
	if c := l.compaction(); c != nil {
		return c, true
	}
	e = &runEntry{
		id: l.mint("compact-"), kind: marotte.RunKindCompaction, origin: origin, state: marotte.RunPending,
		forTurn: forTurn, done: newCompactWait(),
	}
	l.insert(l.frontOfPending(false), e)
	return e, false
}

// crossing records a threshold reading at the point for turnID: false when that turn already crossed or the reader
// declined its compaction.
func (l *runList) crossing(turnID string) bool {
	if turnID == l.crossed || turnID == l.declined {
		return false
	}
	l.crossed = turnID
	return true
}

// cancelCompaction withdraws the compaction with that id: a pending one goes, a running one is marked cancelled
// (no requeue, no toast). A stale id changes nothing.
func (l *runList) cancelCompaction(id string) {
	_, e := l.find(id)
	if e == nil || e.kind != marotte.RunKindCompaction {
		return
	}
	if e.origin == marotte.RunFromFailsafe {
		l.dropResume()
	}
	if e.state == marotte.RunPending {
		l.declined = e.forTurn
		e.done.finish(false, false, errCompactCancelled)
		l.remove(e)
		return
	}
	e.cancelled = true
}

var errCompactCancelled = errors.New("the compaction was cancelled")

// compactStep is what a compaction's start does next.
type compactStep uint8

const (
	// compactAbort: the compaction is no longer this start's (put back or gone); write nothing.
	compactAbort compactStep = iota
	// compactCloseFold: close the claimed fold turn, then wait for its entry to end.
	compactCloseFold
	// compactWaitFold: the fold turn's close tail still runs.
	compactWaitFold
	// compactWrite: the compaction is running; write the call.
	compactWrite
)

// compactionStart is one section of a compaction's start (closing a fold turn ahead of it, then the write),
// re-checking its conditions.
func (l *runList) compactionStart(id string, f *orderFacts) (compactStep, *runEntry) {
	i, e := l.find(id)
	if e == nil || e.state != marotte.RunStarting {
		return compactAbort, nil
	}
	ok := f.sessionCall && f.stops == 0
	if i == 1 {
		fold := l.entries[0]
		switch {
		case fold.state == marotte.RunClosing:
			return compactWaitFold, fold
		case !ok || !fold.yields || !fold.wire() || fold.state != marotte.RunRunning:
			l.putBack(e, marotte.RunPending, 0)
			return compactAbort, nil
		}
		fold.state, fold.steerable = marotte.RunClosing, false
		l.touch()
		return compactCloseFold, fold
	}
	if i != 0 || l.liveLen() != 1 || !ok {
		l.putBack(e, marotte.RunPending, 0)
		return compactAbort, nil
	}
	e.state = marotte.RunRunning
	l.touch()
	return compactWrite, nil
}

// kasReportedActive notes that kiro-cli ran beside a running compaction call, so its bare refusal is requeued.
func (l *runList) kasReportedActive() {
	if c := l.compaction(); c != nil && c.state == marotte.RunRunning {
		c.sawTurn = true
	}
}

// compactionEnded settles the compaction call with that id by the facts the list recorded, never by a refusal
// reason (KAS answers every refusal as a bare {success: false}). A failure no /compact waits on is toasted (fx.toast).
func (l *runList) compactionEnded(id string, accepted bool, err error) {
	_, e := l.find(id)
	if e == nil || e.kind != marotte.RunKindCompaction || e.state != marotte.RunRunning {
		return
	}
	failsafe := e.origin == marotte.RunFromFailsafe
	switch {
	case accepted || e.cancelled:
		e.done.finish(accepted, false, nil)
		l.remove(e)
		l.failsafeHeld = l.failsafeHeld || (failsafe && accepted && !e.cancelled)
	case err == nil && e.sawTurn:
		e.sawTurn = false
		if e.waiter {
			e.done.finish(false, true, nil)
			e.done, e.waiter = newCompactWait(), false
		}
		l.remove(e)
		e.state = marotte.RunPending
		l.insert(l.frontOfPending(false), e)
	default:
		l.compactionNotRun(e, err, failsafe)
	}
}

// compactionNotRun removes a compaction call that failed or was refused. A failsafe compaction's resume goes with it,
// and its failure is toasted even when a /compact waits on it, since the toast says the stopped turn was not continued.
func (l *runList) compactionNotRun(e *runEntry, err error, failsafe bool) {
	e.done.finish(false, false, err)
	l.remove(e)
	if failsafe {
		l.dropResume()
	}
	if (!e.waiter || failsafe) && !errors.Is(err, marotte.ErrChatClosedDuringCall) {
		l.fx.toast = append(l.fx.toast, e)
	}
}

// openFacts name one turn open for turnOpened.
type openFacts struct {
	turnID string
	// admission is the reservation token a prompt-class open presents; zero for a wire open.
	admission uint64
	seq       uint64
	wire      bool
	bracketed bool
}

// turnOpened places a turn open by identity: a prompt-class open on the starting entry its token names, a wire open
// by replace-then-place. A prompt-class open no take names (a wiring error the caller logs) is listed as a live entry
// anyway, so nothing is taken beside a turn the list did not start.
func (l *runList) turnOpened(o openFacts) bool {
	if !o.wire {
		e := l.byToken(o.admission)
		if e == nil || e.state != marotte.RunStarting {
			l.insert(l.liveLen(), &runEntry{
				id: o.turnID, turnID: o.turnID, kind: marotte.RunKindTurn, origin: marotte.RunFromPrompt,
				state: marotte.RunStarting, released: true, steerable: true,
			})
			return false
		}
		e.turnID = o.turnID
		switch e.kind {
		case marotte.RunKindResume:
			l.resumed++
		case marotte.RunKindRow:
			l.resumed = 0
		}
		l.touch()
		return true
	}
	return l.wireOpened(o)
}

// wireOpened places a wire turn in two steps: it replaces a closing or run entry, then goes behind a running
// compaction call, ahead of a marotte turn or first, putting back a compaction taken but not written.
func (l *runList) wireOpened(o openFacts) bool {
	for _, h := range l.entries[:l.liveLen()] {
		if h.wire() && h.state != marotte.RunClosing {
			return false
		}
	}
	for _, h := range slices.Clone(l.entries[:l.liveLen()]) {
		if h.state == marotte.RunClosing || h.runEntry() {
			l.remove(h)
		}
	}
	l.wireSeq = o.seq
	l.insert(l.wireSlot(), &runEntry{
		id: o.turnID, turnID: o.turnID, kind: marotte.RunKindTurn, origin: marotte.RunFromWire,
		state: marotte.RunRunning, released: true, yields: !o.bracketed, steerable: o.bracketed,
	})
	return true
}

// wireSlot is where a wire open goes once step 1 replaced what it replaces: index 1 behind a running compaction or
// revert (which learns kiro-cli ran beside it), else first; a compaction taken but not written is put back.
func (l *runList) wireSlot() int {
	at := 0
	for _, h := range slices.Clone(l.entries[:l.liveLen()]) {
		switch {
		case h.kind == marotte.RunKindCompaction && h.state == marotte.RunStarting:
			l.putBack(h, marotte.RunPending, 0)
		case h.kind == marotte.RunKindCompaction || h.kind == marotte.RunKindRevert:
			at = 1
			if h.kind == marotte.RunKindCompaction {
				h.sawTurn = true
			}
		}
	}
	return at
}

// turnSent marks the taken entry whose turn that is running and steerable before its session/prompt is written, so a
// send in that window steers and the header already says running.
func (l *runList) turnSent(turnID string) {
	if _, e := l.byTurn(turnID); e != nil && e.state == marotte.RunStarting {
		e.state, e.steerable = marotte.RunRunning, true
		l.touch()
	}
}

// turnPrompted marks the entry whose turn that is as having its session/prompt written: kiro-cli files a steer only
// in a turn it holds, and a door entry it refused before may be offered into this one.
func (l *runList) turnPrompted(turnID string) {
	if _, e := l.byTurn(turnID); e != nil && e.live() {
		e.prompted = true
		l.steerRefused = nil
	}
}

// steerFate is how one offer of a row as a steer turned out, as the steer record learned it.
type steerFate uint8

const (
	fateNone steerFate = iota
	// fateInKAS: kiro-cli queued it, or its answer was lost while kiro-cli may hold it.
	fateInKAS
	// fateRead: the agent read it.
	fateRead
	// fateBack: kiro-cli refused it, or no turn could take it.
	fateBack
	// fateUnread: a turn end left it unread.
	fateUnread
	// fateGone: the reader removed it from the dock.
	fateGone
	// fateCleared: a clear of the user's took it out of kiro-cli's buffer, unread, and resent none of its words.
	fateCleared
)

// final reports a fate after which the steer record holds nothing of the offer.
func (f steerFate) final() bool { return f != fateNone && f != fateInKAS }

// entryFate is the fate of entry id's offer.
type entryFate struct {
	id    string
	offer uint64
	fate  steerFate
}

// steerFates applies the fates the steer record reported, each only to the offer it names: an entry put back and
// offered again since is not moved by its earlier offer's fate.
func (l *runList) steerFates(fs []entryFate) {
	for _, f := range fs {
		_, e := l.find(f.id)
		if e == nil || e.offer != f.offer {
			continue
		}
		switch f.fate {
		case fateInKAS:
			l.steerQueued(f.id)
		case fateRead, fateGone:
			l.steerRead(f.id)
		case fateBack:
			l.steerBack(f.id)
		case fateUnread:
			l.turnEnded([]string{f.id})
		case fateCleared:
			l.steerCleared(f.id)
		case fateNone:
		}
	}
}

// steerQueued is kiro-cli's acceptance of an offered steer into its buffer.
func (l *runList) steerQueued(id string) {
	if _, e := l.find(id); e != nil && e.state == marotte.RunOffered {
		e.state = marotte.RunInKAS
		l.touch()
	}
}

// steerBack returns a steer kiro-cli refused or cleared to pending at its place. The user's waits for a turn of its
// own, as a send with no turn to steer into does; a door entry is not offered again until a turn's prompt is written
// or a turn closes clean, so a full buffer is not retried in a loop.
func (l *runList) steerBack(id string) {
	_, e := l.find(id)
	if e == nil || !e.inFlight() {
		return
	}
	l.returned(e)
	if e.byUser() {
		e.want = marotte.RunWantPrompt
	} else {
		l.steerRefused = append(l.steerRefused, e.id)
	}
}

// returned puts a steer kiro-cli gave back on the list at its place: paused when the reader's stop stamped it
// (holdReturns) or holds the queue, or when the reader's send released its stamp (afterSend), else pending.
func (l *runList) returned(e *runEntry) {
	e.state = marotte.RunPending
	if e.pausedBy == 0 && l.held() {
		e.pausedBy, _ = l.queuePaused()
	}
	if e.pausedBy != 0 || e.afterSend {
		e.state, e.want = marotte.RunPaused, marotte.RunWantPrompt
	}
	e.afterSend = false
	l.touch()
}

// steerCleared returns a steer a clear took out of kiro-cli's buffer (the user's, or a reloaded session's post-load
// clear) to pending, still steerable, a door entry behind every waiting user entry: kiro-cli never read it, and its
// words are never resent inside the user's.
func (l *runList) steerCleared(id string) {
	_, e := l.find(id)
	if e == nil || !e.inFlight() {
		return
	}
	l.returned(e)
	l.doorsBehindUsers()
}

// steerRead takes a steer off the list once the agent read it or the reader removed it from the dock.
func (l *runList) steerRead(id string) {
	if _, e := l.find(id); e != nil && e.inFlight() {
		if e.submitter && e.launch != nil {
			answerSteered(e)
		}
		l.remove(e)
	}
}

// turnEnded returns the steers a turn end left unread to pending at their places, marked Unread (one sender's run of
// them is one prompt), the user's as turns of their own. Every waiting door entry then goes behind every waiting user
// entry: a wake was delivered once or not at all, and its caller sends again while it stays unread.
func (l *runList) turnEnded(unread []string) {
	for _, id := range unread {
		_, e := l.find(id)
		if e == nil || !e.inFlight() {
			continue
		}
		e.unread = true
		l.returned(e)
		if e.byUser() {
			e.want = marotte.RunWantPrompt
		}
	}
	l.unreadBeforeResume()
	l.doorsBehindUsers()
}

// unreadBeforeResume moves the user's Unread entries standing behind the failsafe's waiting resume to just ahead of
// it, in their order: they were written for the task the resume continues.
func (l *runList) unreadBeforeResume() {
	r := l.resume()
	if r == nil {
		return
	}
	at, _ := l.find(r.id)
	var moved []*runEntry
	for _, e := range slices.Clone(l.entries[at+1:]) {
		if e.byUser() && e.unread && e.state == marotte.RunPending {
			moved = append(moved, e)
			l.remove(e)
		}
	}
	for i, e := range moved {
		l.insert(at+i, e)
	}
}

// doorsBehindUsers moves the pending or paused door entries ahead of the last waiting user entry to just behind it,
// keeping both senders' own orders.
func (l *runList) doorsBehindUsers() {
	n := l.liveLen()
	last := -1
	for i := n; i < len(l.entries); i++ {
		if e := l.entries[i]; e.kind == marotte.RunKindRow && e.byUser() && (e.state == marotte.RunPending ||
			e.state == marotte.RunPaused || e.inFlight()) {
			last = i
		}
	}
	var doors []*runEntry
	rest := l.entries[:n:n]
	for i := n; i < len(l.entries); i++ {
		e := l.entries[i]
		if i < last && !e.byUser() && e.kind == marotte.RunKindRow &&
			(e.state == marotte.RunPending || e.state == marotte.RunPaused) {
			doors = append(doors, e)
			continue
		}
		rest = append(rest, e)
		if i == last {
			rest = append(rest, doors...)
		}
	}
	if len(doors) > 0 {
		l.entries = rest
		l.touch()
	}
}

// listClose are one turn close's facts for turnClosed.
type listClose struct {
	turnID string
	// retryID is the message id a retry would take.
	retryID string
	// compact is the turn-end compaction the policy asks for ("" for none): turn_end or context_limit.
	compact marotte.RunEntryOrigin
	seq     uint64
	clean   bool
	// completed: the turn's own outcome graded clean, an unattended close not counted (clean counts it).
	completed bool
	// handoff: marotte closed a fold-opened turn to send its next call (closerPromptHandoff).
	handoff bool
	// emptyRetry: the closed turn ended clean on the wire with nothing emitted.
	emptyRetry bool
}

// turnClosed acts on the entry whose turn closed, wherever it sits.
func (l *runList) turnClosed(c listClose, f *orderFacts) {
	i, e := l.byTurn(c.turnID)
	if e == nil || !e.live() {
		return
	}
	n := l.liveLen()
	stopped := e.state == marotte.RunStopping
	e.state, e.steerable, e.clean = marotte.RunClosing, false, c.clean
	l.touch()
	finished := stopped && e.fsStopped && c.completed
	switch {
	case finished:
		l.withdrawFailsafe(e.stopBy)
	case stopped && e.fsStopped && e.fsNoResume:
		l.fx.notices = append(l.fx.notices, resumeLimitNotice)
	}
	if c.clean {
		l.steerRefused = nil
	}
	if n == 2 && l.closedBeside(i, e, c, stopped) {
		return
	}
	retry := retryOwed(e, c, f)
	l.stopped = stopped && !finished
	if !c.handoff {
		l.pauseRule(c.clean || retry, l.stopped)
	}
	l.afterHeadClose(e, c, retry)
}

// retryOwed reports an empty turn of a retry-marked prompt that no later turn or stop overtook: its re-send goes after
// every row queued before it, so its close unpauses them as a clean close does.
func retryOwed(e *runEntry, c listClose, f *orderFacts) bool {
	return e.retry && c.emptyRetry && f.nextSeq <= c.seq && (f.stopSeq == 0 || f.stopSeq < c.seq) &&
		e.row != nil && c.retryID != ""
}

// afterHeadClose inserts what the chat's last close owes: the turn-end compaction the policy asks for, and the
// re-send of an empty turn (retry), after the rows already queued.
func (l *runList) afterHeadClose(e *runEntry, c listClose, retry bool) {
	if c.compact != "" && l.compaction() == nil && l.declined != c.turnID {
		l.insert(l.frontOfPending(false), &runEntry{
			id: l.mint("compact-"), kind: marotte.RunKindCompaction, origin: c.compact,
			state: marotte.RunPending, forTurn: c.turnID, done: newCompactWait(),
		})
	}
	if retry {
		row := *e.row
		l.insertPending(&runEntry{
			id: c.retryID, kind: marotte.RunKindRetry, origin: marotte.RunFromRetry, state: marotte.RunPending,
			row: &row, afterSeq: c.seq, replaces: c.turnID,
		})
	}
}

// closedBeside is turnClosed's arms for a two-entry prefix, reporting whether one applied: a wire turn closing while
// the turn taken beside it is live goes at once; the second entry's close, and a fold turn's ahead of its
// compaction, happen in place and decide nothing, since the chat's next close (or the compaction) does.
func (l *runList) closedBeside(i int, e *runEntry, c listClose, stopped bool) bool {
	other := l.entries[1-i]
	switch {
	case i == 0 && e.wire() && !other.kiroCLI() && other.kind.TurnKind() &&
		(other.state == marotte.RunStarting || other.state == marotte.RunRunning):
		l.remove(e)
		l.stopped = stopped
		if !c.handoff {
			l.pauseRule(c.clean, stopped)
		}
		return true
	case i == 1, other.kind == marotte.RunKindCompaction:
		return true
	}
	return false
}

// pauseRule is a head close's effect on the rows waiting: a stopped turn's close leaves them as its stop paused them,
// even when it ended clean (a superseded wire turn, or a reply that finished before the cancel landed), so they go
// only after the next turn; a clean close unpauses them unless the reader's stop holds the queue (held).
func (l *runList) pauseRule(clean, stopped bool) {
	switch {
	case stopped:
	case clean:
		if !l.held() {
			l.unpauseRows()
		}
	default:
		l.pauseRows(0)
	}
}

// endsChanged applies the steer queue's pending-end count read at seq, dropping a read older than one applied: a held
// close's take waits while an end is owed.
func (l *runList) endsChanged(seq uint64, n int) {
	if seq < l.endsSeq {
		return
	}
	l.endsSeq = seq
	l.endsOwed = n > 0
}

// tailEnded is a close tail's end, naming the closing entry it ran for. A stand-down ends that entry outright.
func (l *runList) tailEnded(id string, standDown bool) {
	_, e := l.find(id)
	if e == nil || e.state != marotte.RunClosing {
		return
	}
	e.tailDone = true
	if standDown {
		l.remove(e)
		return
	}
	l.endIfDone(e)
}

// reservationReleased records a take's reservation given back. A starting entry whose turn never opened only
// records it: launched, which follows, puts it back or removes it.
func (l *runList) reservationReleased(token uint64) {
	e := l.byToken(token)
	if e == nil {
		return
	}
	e.released = true
	if e.state == marotte.RunStarting && e.turnID == "" {
		return
	}
	l.endIfDone(e)
}

// launchRefusal is why a take's launch did not open its turn.
type launchRefusal uint8

const (
	launchOpened launchRefusal = iota
	// launchFailed: any other error before the open: the chat closed or was deleted since the take's stamp, or
	// kiro-cli refused the launch.
	launchFailed
	// launchWithdrawn: a stop withdrew the take (command.ErrTakeWithdrawn).
	launchWithdrawn
)

// launched is the launch result of the take token names. Only a refusal before the open changes the list, and only
// while that take is the entry's (a stop can withdraw a take and the entry be taken again). It reports whether the
// row went from the list because its submitter still waits (the POST answers the refusal; the client keeps the
// draft).
func (l *runList) launched(id string, token uint64, turnID string, refusal launchRefusal, err error) (removed bool) {
	_, e := l.find(id)
	if e == nil {
		return false
	}
	if refusal == launchOpened {
		if e.launch != nil {
			e.launch.finish(turnID, err, false, false)
		}
		return false
	}
	if e.state != marotte.RunStarting || e.turnID != "" || e.res.Token != token {
		return false
	}
	switch {
	case e.kind == marotte.RunKindResume:
		l.remove(e)
		if refusal != launchWithdrawn && !l.ended {
			l.fx.notices = append(l.fx.notices, resumeRefusedNotice)
		}
	case e.origin == marotte.RunFromSummary, e.kind == marotte.RunKindRetry:
		l.remove(e)
	case e.submitter && refusal != launchWithdrawn:
		e.submitter, e.opts.composer = false, false
		l.remove(e)
		removed = true
	default:
		l.requeue(e, 0)
	}
	if e.launch != nil {
		e.launch.finish("", err, false, false)
	}
	return removed
}

// stopArm is how the glue carries out a stop stopHead applied.
type stopArm uint8

const (
	// stopNothing: the target names no entry a stop may stop.
	stopNothing stopArm = iota
	// stopRunning: a running turn (or a wire turn and the sent prompt beside it): one session/cancel.
	stopRunning
	// stopRun: kiro-cli's run with no turn here: one session/cancel; the rows are paused as for a turn.
	stopRun
	// stopWithdraw: a take whose turn has not opened: give back res, write nothing.
	stopWithdraw
	// stopUnsent: an opened, unsent turn: mark it stopped so its send gate refuses it.
	stopUnsent
	// stopSession: a plain stop on an idle list: the session's open asks are answered and one session/cancel is
	// written, as kiro-cli may still be working on something no entry names; no entry changes.
	stopSession
)

// stopPlan is what stopHead changed and what the glue owes.
type stopPlan struct {
	entry *runEntry
	res   command.Reservation
	arm   stopArm
}

// stopHead stops the live entry target names, or with target "" the one RunView.Cancel names (the session itself
// on an idle list). It never stops a compaction, a stopping or closing entry, or anything not live.
func (l *runList) stopHead(target string, token uint64) stopPlan {
	if target == "" && l.liveLen() == 0 {
		return stopPlan{arm: stopSession}
	}
	i, e := l.stopTarget(target)
	if e == nil {
		return stopPlan{}
	}
	mark := func(x *runEntry) {
		x.prior, x.stopBy, x.state = x.state, token, marotte.RunStopping
		l.touch()
	}
	switch {
	case e.runEntry():
		mark(e)
		l.pauseRows(token)
		return stopPlan{entry: e, arm: stopRun}
	case e.state == marotte.RunStarting && e.turnID == "":
		res := e.res
		l.pauseRows(token)
		if e.kind == marotte.RunKindResume {
			l.remove(e)
		} else {
			l.requeue(e, token)
		}
		return stopPlan{entry: e, res: res, arm: stopWithdraw}
	case e.state == marotte.RunStarting:
		mark(e)
		l.pauseRows(token)
		l.requeueUnsent(e, token)
		return stopPlan{entry: e, arm: stopUnsent}
	}
	l.stopRunningAt(i, token, mark)
	return stopPlan{entry: e, arm: stopRunning}
}

// readerStop is every stop the reader makes (Cancel, the composer's stop, a send-now press's stop): readerTookOver,
// then stopHead, then, when the stop paused the queue, holdReturns. A press (sendNow or leadSteer marked its lead) is
// the reader's send, so what it paused waits only for the lead's clean close (releaseHold).
// The failsafe's stop holds no return: the steers kiro-cli gives back go ahead of its resume (unreadBeforeResume).
func (l *runList) readerStop(target string, token uint64) stopPlan {
	press := l.pressed
	l.pressed = false
	l.readerTookOver()
	plan := l.stopHead(target, token)
	if l.pausing == token {
		l.holdReturns(token, press)
	}
	if press {
		l.releaseHold(true)
	}
	return plan
}

// readerTookOver withdraws the waiting resume and makes a turn the failsafe is stopping the reader's stop, whose close
// leaves the rows paused (pauseRule) and tells nothing of the resume limit. The reader stopped the work or sent the
// next message, so marotte does not continue it; the withdrawal holds when the cancel is not delivered, as a later
// stop drops a retry (overtaken).
func (l *runList) readerTookOver() {
	l.dropResume()
	for _, e := range l.entries[:l.liveLen()] {
		e.fsStopped = false
	}
}

// stopTarget is the live starting or running turn stopHead's target names, with its index, or nil.
func (l *runList) stopTarget(target string) (int, *runEntry) {
	n := l.liveLen()
	if target == "" {
		target = marotte.CancelTarget(l.project())
	}
	i, e := l.find(target)
	if e == nil || i >= n || !e.kind.TurnKind() || (e.state != marotte.RunStarting && e.state != marotte.RunRunning) {
		return i, nil
	}
	return i, e
}

// stopRunningAt marks the running entry at i stopping. With kiro-cli's wire turn beside a sent, unbound prompt, one
// session/cancel stops both turns, so the other is marked too and the stop pauses the rows whichever was named; a stop
// of the head, or of kiro-cli's turn beside a compaction or revert head, pauses them too.
func (l *runList) stopRunningAt(i int, token uint64, mark func(*runEntry)) {
	mark(l.entries[i])
	both := l.liveLen() == 2 && l.entries[0].wire() && l.entries[1].kind.TurnKind() && !l.entries[1].kiroCLI()
	if both {
		if other := l.entries[1-i]; other.state == marotte.RunRunning {
			mark(other)
		}
	}
	if i == 0 || both || l.entries[0].kind == marotte.RunKindCompaction || l.entries[0].kind == marotte.RunKindRevert {
		l.pauseRows(token)
	}
}

// stopUndone restores what the stop with that token marked, when its session/cancel was not delivered: entries
// still stopping whose turn is still open (open reports it) go back to their prior state, rows it paused that are
// still paused go back to pending, and steers it stamped (holdReturns) are unstamped. A failsafe stop's own listing is
// withdrawn (withdrawFailsafe).
func (l *runList) stopUndone(token uint64, open func(turnID string) bool) {
	if token == 0 {
		return
	}
	for _, e := range l.entries {
		switch {
		case e.stopBy == token && e.state == marotte.RunStopping && (e.runEntry() || open(e.turnID)):
			e.state, e.stopBy, e.prior, e.fsStopped = e.prior, 0, "", false
			l.touch()
		case e.pausedBy == token && e.state == marotte.RunPaused:
			if !e.pausedBefore {
				e.state = marotte.RunPending
			}
			e.pausedBy, e.pausedBefore = 0, false
			l.touch()
		case e.pausedBy == token && e.inFlight():
			e.pausedBy = 0
		}
	}
	if l.pausing == token {
		l.pausing = 0
	}
	l.withdrawFailsafe(token)
}

// endLife is the section that ends the life: every waiter is answered with the chat's close, and the list the next
// life starts from (rows only, the waited-on takes left out, an unopened take's joined rows kept) is returned for the
// life's last write, with the steers kiro-cli was handed, which that list leaves out (marotte.SanitizeRun).
func (l *runList) endLife() (next, steers []marotte.RunEntry) {
	l.ended = true
	var keep []marotte.RunEntry
	for _, e := range l.entries {
		if e.done != nil {
			e.done.finish(false, false, marotte.ErrChatClosedDuringCall)
		}
		if e.launch != nil && e.submitter && e.turnID == "" {
			e.submitter, e.opts.composer = false, false
			e.launch.finish("", marotte.ErrChatClosedDuringCall, false, false)
			continue
		}
		keep = append(keep, l.projectOne(e))
		if e.turnID == "" {
			for _, j := range e.joined {
				keep = append(keep, l.projectOne(j))
			}
		}
	}
	return marotte.SanitizeRun(keep, false), handedSteers(keep)
}

// handedSteers is the entries kiro-cli was handed as steers: offered, or in its buffer.
func handedSteers(entries []marotte.RunEntry) []marotte.RunEntry {
	var out []marotte.RunEntry
	for _, e := range entries {
		if e.Row != nil && (e.State == marotte.RunOffered || e.State == marotte.RunInKAS) {
			out = append(out, e)
		}
	}
	return out
}

func (l *runList) projectOne(e *runEntry) marotte.RunEntry {
	return marotte.RunEntry{
		Row: e.row, ID: e.id, Kind: e.kind, State: e.state, Origin: e.origin, Turn: e.turnID, Next: e.next,
		Steerable: e.steerable, Yields: e.yields, Admits: e.admits, Want: e.want, Unread: e.unread,
		StopPaused: l.stopPaused(e),
	}
}

// stopPaused reports a row the reader's stop paused, which waits for the reader's send or Resume. The failsafe's stop
// pauses rows until its resume's clean close, so a row it paused is not one while that resume is listed; once the
// reader withdraws the resume (readerTookOver), nothing but the reader sends the row.
func (l *runList) stopPaused(e *runEntry) bool {
	if e.state != marotte.RunPaused || e.pausedBy == 0 {
		return false
	}
	return !slices.ContainsFunc(l.entries, func(x *runEntry) bool {
		return x.kind == marotte.RunKindResume && x.fsToken == e.pausedBy
	})
}

// loadedStop is the pausedBy of a stop-paused row no stop's undo may unpause: one the stored list marks stop-paused,
// in the life that loads it, or one that joined once its stop's turn was gone (queuePaused). No stop has this token.
const loadedStop = ^uint64(0)

// load seeds a new life's list from the stored one (SanitizeRun ran on it) and the stored version. A row a stop
// paused stays paused by it, so the queue stays paused for a row an outside caller sends (queuePaused).
func (l *runList) load(entries, stored []marotte.RunEntry, version uint64) {
	l.entries = nil
	for i := range entries {
		re := entries[i]
		e := &runEntry{
			id: re.ID, kind: re.Kind, state: re.State, origin: re.Origin, row: re.Row, next: re.Next,
			want: re.Want, unread: re.Unread,
		}
		if re.StopPaused && re.State == marotte.RunPaused {
			e.pausedBy = loadedStop
		}
		l.entries = append(l.entries, e)
	}
	l.stored, l.version = stored, version
}
