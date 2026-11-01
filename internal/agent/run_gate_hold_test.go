package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
	"github.com/cplieger/slogx/capture"
)

// The dimensions of the hold matrix. TestGateHold_EveryCell runs their full cross-product, and holdModel says what
// each combination does, or why it cannot happen.
type (
	runKind    int
	holdForm   int
	landing    int
	heldEvent  int
	restartAt  int
	crossing   int
	readFault  int
	writeFault int
	raceOrder  int
	bootSign   int
)

const (
	kindChat runKind = iota
	kindManual
	kindScheduled
)

const (
	// formGate is the gate's own pause of a running run; formHeal is an automatic resume of a run a restart paused.
	formGate holdForm = iota
	formHeal
)

const (
	landBefore landing = iota
	landAfter
)

const (
	heldNone heldEvent = iota
	heldBudget
	heldCancelled
	heldGone
	heldDeleted
)

const (
	restartNone restartAt = iota
	restartClosed
	restartOpen
	// restartEngine replaces only the engine: marotte keeps its memory, the hold's heal and the heal budget too.
	restartEngine
)

const (
	crossNone crossing = iota
	// crossOpensBeforeHold opens the gate between the hold's read and its pause or admission.
	crossOpensBeforeHold
	// crossClosesAtFind closes the gate again at the release's first read of the hold, crossClosesAtHeal at its
	// heal's first read, before the heal's admission.
	crossClosesAtFind
	crossClosesAtHeal
	// crossFlapsAtRetry loses a held release's first resume write, then closes and opens the gate while the release
	// waits to retry it.
	crossFlapsAtRetry
	// The in-flight crossings hold a held release's first resume write before KAS takes it while the gate closes:
	// crossFlapsInFlight opens the gate again before the write lands, crossRepausedEnded once the write landed, the
	// release ended and the closing sweep paused the run again, crossRepausedLive at the same point with the release
	// still live after its try.
	crossFlapsInFlight
	crossRepausedEnded
	crossRepausedLive
)

// The orders a chat-parented cell's production leaves open. orderFirst: a sign-in marks the chat's session for its
// reload (Runtime.onSignInChange) before the gate reads the sign-in, and the rehydrate a release's resume write
// reloads reads the run before a closing sweep pauses it again. orderMarkLate: the gate opens and the release it
// starts writes its first resume before the mark lands, which the vault leaves unordered (OnChange's listeners run
// after Answerability shows the sign-in). orderSweepFirst: the closing sweep pauses the run again before that
// rehydrate reads it.
const (
	orderFirst raceOrder = iota
	orderMarkLate
	orderSweepFirst
)

const (
	// bootSignedOut: the cell's first runtime boots with no sign-in, so a gate pause's lapse follows the sign-in that
	// opened the gate, whose reload mark a chat's open step turn keeps on its session (claimReload).
	bootSignedOut bootSign = iota
	// bootSignedIn: it boots on a loaded sign-in, which marks no session (Runtime.onSignInChange marks on an edge).
	bootSignedIn
)

const (
	readNone readFault = iota
	// readHoldOnce fails once the read that forms the hold, the closing sweep's of a gate pause or the heal entry's of
	// a heal; readHoldEvery fails each one until its tries are spent. Both act before the checkpoint.
	readHoldOnce
	readHoldEvery
	// readEntryOnce fails once, held there, the heal entry's read before its heal's: a chat's rehydrate's run list, or
	// for a parentless run, which the engine ends under while KAS still lists it running, the read after the resume
	// that skipped it (resumeParentless), after which KAS lists it paused.
	readEntryOnce
	// readFirstOnce fails the release's first read once, readHealOnce its heal's first read.
	readFirstOnce
	readHealOnce
	// readFinderEvery, readHealEvery and readEvery fail every read of the finder (of any run it lists, a held heal's
	// too), of the heal, or of both, until the failing readers settle.
	readFinderEvery
	readHealEvery
	readEvery
)

const (
	writeNone writeFault = iota
	// A release write is a resume, or once the heal budget is spent the park for the user. writeLostOnce loses the
	// first one before KAS takes it; writeLostEvery loses every one until the writers settle; writeReplyLost loses the
	// first one's reply after KAS took it.
	writeLostOnce
	writeLostEvery
	writeReplyLost
)

var (
	kindNames    = []string{kindChat: "chat-parented", kindManual: "manual", kindScheduled: "scheduled"}
	formNames    = []string{formGate: "gate pause", formHeal: "heal"}
	landNames    = []string{landBefore: "lands before the edge", landAfter: "lands after the edge"}
	heldNames    = []string{"held untouched", "budget spent while held", "cancelled while held", "gone while held", "deleted while held"}
	restartNames = []string{
		restartNone: "no restart", restartClosed: "a restart, the gate closed", restartOpen: "a restart, the gate open",
		restartEngine: "the engine replaced at the checkpoint",
	}
	crossNames = []string{
		"no crossing", "the gate opens before the hold forms", "the gate closes again at the release's first read of the hold",
		"the gate closes again before the release's heal admits",
		"the release's first resume write is lost, and the gate closes and opens again while it waits to retry",
		"the gate closes and opens again while the release's first resume write is in flight",
		"the gate closes while the release's first resume write is in flight, and opens again once the release " +
			"ended and the closing sweep paused the run again",
		"the gate closes while the release's first resume write is in flight, and opens again once the closing " +
			"sweep paused the run again, the release still live",
	}
	orderNames = []string{
		"the chat's reload mark and rehydrate come first",
		"the gate opens and the release writes before the sign-in marks the chat for its reload",
		"the closing sweep pauses the resumed run before the chat's rehydrate reads it",
	}
	// A signed-out boot is every cell's unless its name says otherwise.
	bootNames = []string{
		"", "the runtime boots signed in, so no sign-in marks the chat's session for its reload before the gate closes",
	}
	readNames = []string{
		"reads answer", "the hold's first read fails once", "the hold's reads keep failing",
		"the heal entry's own read fails once", "the release's first read fails once",
		"the heal's first read fails once",
		"the finder's reads keep failing", "the heal's reads keep failing", "every read keeps failing",
	}
	writeNames = []string{
		"writes answer", "the release's write is lost once", "every release write is lost", "the release write's reply is lost",
	}
)

// holdCombo is one cell of the matrix.
type holdCombo struct {
	kind    runKind
	form    holdForm
	land    landing
	held    heldEvent
	restart restartAt
	cross   crossing
	read    readFault
	write   writeFault
	order   raceOrder
	boot    bootSign
}

func (c holdCombo) gate() bool { return c.form == formGate }
func (c holdCombo) chat() bool { return c.kind == kindChat }

// inFlight is whether the crossing holds the release's first resume write in flight.
func (c crossing) inFlight() bool {
	return c == crossFlapsInFlight || c == crossRepausedEnded || c == crossRepausedLive
}

// takesWrite is whether the crossing acts on the release's first resume write, as each write fault does.
func (c crossing) takesWrite() bool { return c == crossFlapsAtRetry || c.inFlight() }

// holdRead is whether the cell's read fault acts as the hold forms; releaseRead whether it acts on the release.
func (c holdCombo) holdRead() bool {
	return c.read == readHoldOnce || c.read == readHoldEvery || c.read == readEntryOnce
}
func (c holdCombo) releaseRead() bool { return c.read != readNone && !c.holdRead() }

// failsFinder is whether a read fault fails every read of the finder.
func failsFinder(r readFault) bool { return r == readFinderEvery || r == readEvery }

// rowName is the cell's row: its hold and run kind.
func (c holdCombo) rowName() string {
	if c.gate() {
		return "a " + kindNames[c.kind] + " run's gate pause"
	}
	return "a " + kindNames[c.kind] + " run's heal"
}

func (c holdCombo) name() string {
	parts := []string{
		landNames[c.land], heldNames[c.held], restartNames[c.restart], crossNames[c.cross], readNames[c.read],
		writeNames[c.write], orderNames[c.order],
	}
	if c.boot != bootSignedOut {
		parts = append(parts, bootNames[c.boot])
	}
	return strings.Join(parts, "; ")
}

// everyCombo is the full cross-product, row by row.
func everyCombo() []holdCombo {
	var out []holdCombo
	for form := range holdForm(len(formNames)) {
		for kind := range runKind(len(kindNames)) {
			for land := range landing(len(landNames)) {
				for held := range heldEvent(len(heldNames)) {
					for restart := range restartAt(len(restartNames)) {
						for cross := range crossing(len(crossNames)) {
							for read := range readFault(len(readNames)) {
								for write := range writeFault(len(writeNames)) {
									for order := range raceOrder(len(orderNames)) {
										for boot := range bootSign(len(bootNames)) {
											out = append(out, holdCombo{kind, form, land, held, restart, cross, read, write, order, boot})
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// kasHold is how KAS reads the run: the part of a hold a restart keeps.
type kasHold int

const (
	kasNone kasHold = iota
	kasGateReason
	kasRestartPause
	kasPausePending
	kasHoldRunning
	kasCancelled
	kasGone
	// kasParked is marotte's park for the user (parkForUser), a person's pause.
	kasParked
	kasOther
)

func (h kasHold) String() string {
	switch h {
	case kasNone:
		return "nothing"
	case kasGateReason:
		return "paused with the gate's reason"
	case kasRestartPause:
		return "paused for the restart"
	case kasPausePending:
		return "running with the gate's pause pending"
	case kasHoldRunning:
		return "running"
	case kasCancelled:
		return "cancelled"
	case kasGone:
		return "gone"
	case kasParked:
		return "paused for a person to resume"
	}
	return "in a state no cell names"
}

func (h kasHold) holds() bool { return h == kasGateReason || h == kasRestartPause }

func (h kasHold) ended() bool { return h == kasCancelled || h == kasGone }

// listedPaused is whether KAS lists the run paused, so a finder of every paused run (pausedRuns) reads it.
func (h kasHold) listedPaused() bool {
	return h == kasGateReason || h == kasRestartPause || h == kasParked || h == kasOther
}

// holdState is a run's hold at one point of a cell: how KAS reads the run, and whether the gate holds a heal of it.
type holdState struct {
	kas  kasHold
	heal bool
}

func (s holdState) String() string {
	if s.heal {
		return s.kas.String() + " and its heal held"
	}
	return s.kas.String()
}

// holdEvent is what a step of a cell drives.
type holdEvent int

const (
	evHold holdEvent = iota
	// evHoldRead and evHoldDead close the gate under the running run, whose closing sweep's read then fails.
	evHoldRead
	evHoldDead
	evHoldOpens
	evHeal
	evHealOpens
	evBudget
	evCancel
	evGone
	evDelete
	evRestartClosed
	evRestartOpen
	evEngineEnd
	evSignIn
	evSignInAgain
	evPendAcrossEdge
	evLandFrame
	evHealAfter
	evRetry
	evRehydrate
)

// holdStep is one step of a cell: the event it drives and the run's state after it, until the next step's event.
type holdStep struct {
	text string
	// loud are the ERRORs the step's failing readers log, which it waits for before the readers settle.
	loud []string
	want holdState
	ev   holdEvent
	// persisted is KAS's read of the run as a restart's new runtime starts, the hold's heal not kept; kasNone
	// off a restart.
	persisted kasHold
	resumes   int32
	// blocked: the step ends with a read or a next try held, until the next step's retry.
	blocked bool
	// settleRead and settleWrite: the step waits out the failing readers or writers, then ends their fault.
	settleRead  bool
	settleWrite bool
	// arm: the faults and the release's crossing start after this step, the hold's checkpoint.
	arm bool
	// asks: the run's next credential ask (pauseBeforeRefusal) starts the closing sweep that pauses it.
	asks bool
	// finderFirst: the step's heal and orphan sweep read the run only after the finder listed it.
	finderFirst bool
	// crosses: the gate closes again under the step's release, which the step waits for.
	crosses bool
	// flaps: the step's held release loses its first resume write, and the step closes and opens the gate while it
	// waits to retry; flapPauses: the closing edge's sweep pauses the run a rehydrate resumed in that wait.
	flaps      bool
	flapPauses bool
	// failsEntry: the step's heal entry's own read fails once (readEntryOnce), held there.
	failsEntry bool
	// markLate: the step's sign-in, or with the last step's holdsMark that one's, marks the chat for its reload only
	// once its release wrote its first resume.
	markLate bool
	// holdsMark: the step's sign-in's reload mark is still held as the step ends, for the next step's markLate.
	holdsMark bool
	// inFlight: the step's held release's first resume write is held in flight under this crossing; rehydrates: that
	// write reloads the chat's session, whose rehydrate reads the run once the write landed.
	inFlight   crossing
	rehydrates bool
	// servesLive: the release live after the last step's try serves this step's opening edge.
	servesLive bool
	// chainTry: the last step's failed sweep try is tried again in this one by its chain (retryResumeLater), which
	// the step waits for, since a try that only holds the run again changes nothing it could see.
	chainTry bool
	// engineSweeps: the next engine's resume of the runs the last one interrupted sweeps the run, which the step waits
	// for, as it may only hold the run again.
	engineSweeps bool
	// finderSettles: a restart's chat rehydrate starts once the boot's finder spent its failing reads.
	finderSettles bool
}

// The ERRORs a cell's failing readers log.
const (
	findSpentLog       = "a paused run could not be read for the gate's pause; the next sign-in or restart reads it again"
	sweepSpentLog      = "a running run could not be paused; its next credential ask waits until it is"
	releaseSpentLog    = "a held run could not be resumed"
	resweepSpentLog    = "the chat's runs could not be listed or resumed by any sweep"
	parentlessSpentLog = "an interrupted run could not be resumed by any try"
)

// healer is the release path that resumes a heal: none, the gate's held heal (resumeHeld's tries, one heal of the
// budget for them all), or a rehydrate's or a parentless resume's tries, each claiming a heal.
type healer int

const (
	healerNone healer = iota
	healerHeld
	healerSweep
)

// releasers are the paths one event starts: the finder's chain (its read, keepHold, then resumeHeld) and a healer.
type releasers struct {
	healer healer
	finder bool
}

// holdSim is holdModel's state while it walks a cell.
type holdSim struct {
	steps   []holdStep
	cur     holdStep
	c       holdCombo
	kas     kasHold
	claims  int
	resumes int32
	read    readFault
	write   writeFault
	cross   crossing
	entry   bool
	open    bool
	// reload: a chat's session is marked for its reload (signsIn); freshMark: the current step's sign-in made the
	// only mark pending; live: a release live after its try serves the next opening edge (crossRepausedLive);
	// chainNext: the next step is a sweep chain's later try after the last step's failed one; sweepNext: the next
	// step's engine end resumes the run through a sweep; finderEarly: the round's dead finder settles in its first
	// step, finderMoved once a block ended that step.
	reload, freshMark, live, chainNext, sweepNext, finderEarly, finderMoved bool
	// edgeMark: the opening edge of steps[edgeStep], a pending gate pause's, made the only reload mark pending, and
	// no restart or engine end has taken it up since.
	edgeMark bool
	edgeStep int
	// fired records whether each armed fault, crossing and race order acted.
	readFired, writeFired, crossFired, orderFired bool
}

// holdModel is the reference model of the hold matrix: the steps a cell drives, each with the state the cell must
// reach, or why the matrix skips the combination: it cannot happen, or it drives as its twin.
func holdModel(c holdCombo) (steps []holdStep, impossible string) {
	if why := c.ruledOut(); why != "" {
		return nil, why
	}
	if _, why := c.twin(); why != "" {
		return nil, why
	}
	s := &holdSim{c: c}
	s.walk()
	switch {
	case c.read != readNone && !s.readFired:
		return nil, "no read the fault names comes while it is armed: findHolds reads only the runs KAS lists paused " +
			"(pausedRuns), a chat's sweep only the paused runs it lists (resumeChainRuns), resumeHeld only a held heal, " +
			"and a heal entry's own read comes only at a chat's rehydrate (resumeChainRuns' list), which an engine's " +
			"end brings only for a run still leased (resumeInterrupted), or for a parentless run only while KAS lists " +
			"it running for an ended engine (resumeParentless)"
	case c.write != writeNone && !s.writeFired:
		return nil, "no release write comes while its fault is armed: a heal writes only once its read finds a pause " +
			"it resumes (runTransitions' healResume row)"
	case c.cross.takesWrite() && !s.crossFired:
		return nil, "no held release (resumeHeld) writes the run's resume at an open gate with no sweep of its round " +
			"healing the run too, so no release write is lost or held in flight: a chat's rehydrate or a parentless " +
			"run's tries heal it, it parks for a person or ends first, or failing reads leave no hold for a release"
	case c.order == orderSweepFirst && !s.orderFired:
		return nil, "no closing sweep pauses the run again while a chat's rehydrate waits to read it: only a held " +
			"release's resume write that reloads the chat's session and lands at the closed gate starts both"
	case c.order == orderMarkLate && !s.orderFired:
		return nil, "no sign-in opens the gate onto a held release's resume write with its own reload mark the only " +
			"one pending: an earlier sign-in's mark still waits on the chat's session for the run's first resume " +
			"write (with a signed-out boot, the sign-in before the lapse, whose mark the run's open step turn keeps " +
			"from an idle open (claimReload), or an earlier opening edge whose release wrote no resume), or no held " +
			"release writes a resume in the edge's round, nor in that of the pending pause's frame"
	case c.cross != crossNone && !s.crossFired:
		return nil, "no release reads the hold at an open gate, or none of its reads answers before its tries are " +
			"spent (resumeHeld, retryResumeLater), so the gate has no read to cross"
	}
	return s.steps, ""
}

// ruledOut is why the dimensions alone exclude the combination, "" when they do not.
func (c holdCombo) ruledOut() string {
	switch {
	case c.land == landAfter && c.cross == crossOpensBeforeHold:
		return "a hold landing after the opening edge finds the gate already open, so it cannot open before the hold"
	case c.cross == crossOpensBeforeHold && (c.held != heldNone || c.restart != restartNone || c.releaseRead() ||
		c.write != writeNone):
		return "the gate opening before the hold forms leaves no hold for the later events: holdPause writes no " +
			"pause once the gate reads open (errGateOpened), and admitOrHold admits the heal"
	case c.read == readEntryOnce && c.gate():
		return "a gate pause has no heal entry: it forms in the closing sweep, whose read the hold's read values fail"
	case c.cross.takesWrite() && c.write != writeNone:
		return "the crossing loses or holds the release's first resume write itself, the first write each write " +
			"fault acts on"
	case c.order != orderFirst && !c.chat():
		return "only a chat's session is marked for its reload at a sign-in (markSessionsForReload), and a reload's " +
			"rehydrate sweeps only a chat's runs; a manual or scheduled run's host session is never marked"
	case !c.gate() && c.land == landAfter && (c.read == readHoldOnce || c.read == readHoldEvery):
		return "a heal after the opening edge forms no hold, since admitOrHold admits it at the open gate, so its " +
			"first read is the release's"
	}
	return ""
}

// twin is the cell with a signed-out boot that c drives the same as, and why, when c's boot changes nothing the
// cell drives; "" when it does.
func (c holdCombo) twin() (holdCombo, string) {
	if c.boot == bootSignedOut || (c.chat() && c.gate()) {
		return c, ""
	}
	t := c
	t.boot = bootSignedOut
	if !c.chat() {
		return t, "only a chat's session is marked for its reload at a sign-in (markSessionsForReload), and the " +
			"sign-in a signed-out boot needs before a gate pause's lapse opens the gate onto a running run, which no " +
			"finder reads (pausedRuns)"
	}
	return t, "a heal's hold forms at a gate closed before any sign-in, and a boot that loads a sign-in marks no " +
		"session (Runtime.onSignInChange marks on an edge), so no reload mark is pending at its first opening edge " +
		"either way"
}

func (s *holdSim) state() holdState { return holdState{kas: s.kas, heal: s.entry} }

// begin starts the next step.
func (s *holdSim) begin(ev holdEvent, text string) {
	s.cur = holdStep{ev: ev, text: text, chainTry: s.chainNext, engineSweeps: s.sweepNext}
	s.chainNext, s.sweepNext = false, false
	signs := s.signsIn(ev)
	s.freshMark = signs && s.c.chat() && !s.reload
	if signs && s.c.chat() {
		s.reload = true
	}
}

// end closes the current step at the simulated state.
func (s *holdSim) end() {
	s.cur.want, s.cur.resumes = s.state(), s.resumes
	s.steps = append(s.steps, s.cur)
}

// block closes the current step held at a read, at the simulated state, and starts the retry's step.
func (s *holdSim) block(note string) {
	if s.finderEarly {
		// The boot's finder spent its failing reads before the rehydrate this step holds started.
		s.finderEarly, s.finderMoved = false, true
		s.cur.loud = append(s.cur.loud, findSpentLog)
		s.cur.settleRead = true
		s.cur.text += "; the finder's failing reads settle first"
	}
	s.cur.text += "; " + note
	s.cur.blocked = true
	s.end()
	s.begin(evRetry, "the held try goes on")
}

func (s *holdSim) healEntryText() string {
	if s.c.chat() {
		return "its chat's rehydrate"
	}
	return "the engine's restart"
}

// holdSpentLog is the ERROR the hold's readers log once every read failed: the closing sweep's, a chat's sweeps', or
// a parentless run's resume tries'.
func (s *holdSim) holdSpentLog() string {
	switch {
	case s.c.gate():
		return sweepSpentLog
	case s.c.chat():
		return resweepSpentLog
	}
	return parentlessSpentLog
}

// holdReads is the steps the hold's read fault adds as the hold forms: its first read fails, held there, or every
// read fails until its tries are spent. It returns the event that then forms the hold (ev, or the held read's retry),
// whether the run's next credential ask starts the gate pause's sweep, and whether a hold still forms.
func (s *holdSim) holdReads(ev holdEvent) (next holdEvent, asks, forms bool) {
	entry, what := evHeal, s.healEntryText()+" reads it"
	if s.c.gate() {
		entry, what = evHoldRead, "the gate closes, and its sweep reads it"
	}
	switch s.read {
	case readHoldOnce:
		s.read, s.readFired = readNone, true
		s.begin(entry, what+", and the read fails, held there")
		s.cur.blocked = true
		s.end()
		if s.c.gate() {
			return ev, false, true
		}
		s.chainNext = true
		return evRetry, false, true
	case readEntryOnce:
		s.entryRead(entry, "")
		return evRetry, false, true
	case readHoldEvery:
		s.read, s.readFired = readNone, true
		if s.c.gate() {
			entry = evHoldDead
		}
		s.begin(entry, what+", and every read fails until its tries are spent: ERROR")
		s.cur.settleRead = true
		s.cur.loud = []string{s.holdSpentLog()}
		s.end()
		return ev, s.c.gate(), s.c.gate()
	}
	return ev, false, true
}

// entryRead is the step the readEntryOnce fault adds at a heal's entry ev, held at the failed read; lead, when set,
// names the event that brings the entry.
func (s *holdSim) entryRead(ev holdEvent, lead string) {
	s.read, s.readFired = readNone, true
	text := "its chat's rehydrate lists the chat's runs, and the list fails, held there"
	if !s.c.chat() {
		text = "the engine ends while KAS still lists it running, so its resume skips it, and the read after that " +
			"fails, held there, and KAS then lists it paused for the restart"
	}
	if lead != "" {
		text = lead + "; " + text
	}
	s.begin(ev, text)
	s.cur.blocked, s.cur.failsEntry = true, true
	s.end()
	s.chainNext = true
}

// chatEntryRead is whether a chat's rehydrate at a restart or an engine's end lists the runs while the readEntryOnce
// fault is still armed: the restart's runtime reopens the chat, and the next engine reopens it while its run is
// leased, which a delete ends (teardownDeleted).
func (s *holdSim) chatEntryRead(engine bool) bool {
	return s.read == readEntryOnce && s.c.chat() && (!engine || s.c.held != heldDeleted)
}

// entryText names the step that forms a heal's hold: its entry, or the retry of a read the fault held.
func (s *holdSim) entryText(ev holdEvent) string {
	if ev == evRetry {
		return "the next try"
	}
	return s.healEntryText()
}

func (s *holdSim) walk() {
	c := s.c
	if c.holdRead() {
		s.read = c.read
	}
	if c.land == landAfter {
		s.walkAfter()
		return
	}
	s.kas = kasRestartPause
	if c.gate() {
		s.kas = kasHoldRunning
	}
	switch {
	case c.cross == crossOpensBeforeHold && c.gate():
		_, asks, _ := s.holdReads(evHoldOpens)
		s.crossFired = true
		s.begin(evHoldOpens, "the gate opens again between the closing sweep's read and its pause, which pauses nothing")
		s.cur.asks = asks
		s.end()
		return
	case c.cross == crossOpensBeforeHold:
		ev, _, forms := s.holdReads(evHealOpens)
		if !forms {
			return
		}
		s.crossFired = true
		s.kas, s.resumes, s.claims = kasHoldRunning, 1, 1
		s.begin(ev, s.entryText(ev)+" reads it with the gate closed, which opens before the heal's admission")
		s.end()
		return
	case c.gate():
		_, asks, _ := s.holdReads(evHold)
		s.kas = kasGateReason
		s.begin(evHold, "the gate's sweep pauses it with the reason, and its paused frame's finder reads it held")
		if asks {
			s.cur.asks = true
			s.cur.text = "its step's next credential ask starts the sweep that pauses it with the reason, and its " +
				"paused frame's finder reads it held"
		}
		s.end()
	default:
		if ev, _, forms := s.holdReads(evHeal); forms {
			s.entry = true
			s.begin(ev, s.entryText(ev)+" holds its heal")
			s.end()
		}
	}
	s.heldWhile()
	s.arm()
	if c.restart != restartNone {
		s.restartAndBoot()
	}
	s.loop()
}

// heldWhile is the cell's event at the hold's checkpoint.
func (s *holdSim) heldWhile() {
	switch s.c.held {
	case heldBudget:
		s.claims = maxAutoHeals
		s.begin(evBudget, "its heal budget is spent")
	case heldCancelled:
		s.kas = kasCancelled
		s.begin(evCancel, "it is cancelled")
	case heldGone:
		s.kas = kasGone
		s.begin(evGone, "KAS forgets it")
	case heldDeleted:
		s.kas = kasGone
		s.begin(evDelete, "it is deleted")
	default:
		return
	}
	s.end()
}

// arm starts the release's faults and crossing at the hold's checkpoint, the step just closed.
func (s *holdSim) arm() {
	s.steps[len(s.steps)-1].arm = true
	if !s.c.holdRead() {
		s.read = s.c.read
	}
	s.write = s.c.write
	if s.c.cross != crossOpensBeforeHold {
		s.cross = s.c.cross
	}
}

func (s *holdSim) walkAfter() {
	c := s.c
	if c.gate() {
		s.kas = kasHoldRunning
		_, asks, _ := s.holdReads(evPendAcrossEdge)
		s.kas, s.open = kasPausePending, true
		s.begin(evPendAcrossEdge, "the gate's pause is asked and still pending at the opening edge, whose finder "+
			"reads only paused runs")
		s.cur.asks = asks
		// With a signed-out boot the step's lapse signs in before its edge does (holdCell.lapse).
		s.edgeMark, s.edgeStep = s.freshMark && c.boot == bootSignedIn, len(s.steps)
		s.end()
	} else {
		s.kas, s.open = kasRestartPause, true
		s.begin(evSignIn, "the opening edge, before anything holds its heal")
		s.end()
	}
	s.heldWhile()
	s.arm()
	if c.restart != restartNone {
		s.restartAndBoot()
	}
	switch {
	case s.done():
	case c.gate() && s.kas == kasPausePending:
		s.kas = kasGateReason
		s.round(evLandFrame, "the pause lands, and its frame's finder reads it held", releasers{finder: true})
	case !c.gate() && c.restart == restartNone:
		ev := evHealAfter
		if s.read == readEntryOnce && s.kas == kasRestartPause {
			s.entryRead(ev, "")
			ev = evRetry
		}
		s.round(ev, s.entryText(ev)+", the gate open", releasers{healer: healerSweep})
	}
	s.loop()
}

// restartAndBoot replaces the process at the hold's checkpoint: KAS keeps the run's state, and the heal's hold and
// the heal budget, which marotte keeps in memory, are lost. The new runtime's boot reads the run.
func (s *holdSim) restartAndBoot() {
	if s.c.restart == restartEngine {
		s.engineEnd()
		return
	}
	persisted := s.kas
	s.entry, s.claims, s.reload, s.edgeMark = false, 0, false, false
	s.open = s.c.restart == restartOpen
	ev, text := evRestartClosed, "a restart, the gate closed at boot"
	if s.open {
		ev, text = evRestartOpen, "a restart, the gate open at boot"
	}
	if !s.c.gate() && !s.c.chat() && s.kas == kasRestartPause {
		s.kas = kasCancelled
		s.begin(ev, text+": the orphan sweep cancels it, as a run its process left paused")
		if failsFinder(s.read) {
			s.read, s.readFired = readNone, true
			s.cur.finderFirst, s.cur.settleRead = true, true
			s.cur.loud = []string{findSpentLog}
			s.cur.text += "; the boot's finder lists it first, and its reads fail until its tries are spent: ERROR"
		}
		s.cur.persisted = persisted
		s.end()
		return
	}
	r := releasers{finder: s.c.gate() && s.kas == kasGateReason}
	if s.c.chat() && s.kas.holds() {
		r.healer = healerSweep
		text += ", its chat's rehydrate too"
	}
	s.roundAfterEntry(ev, text, r, s.chatEntryRead(false))
	for i := len(s.steps) - 1; i >= 0; i-- {
		if s.steps[i].ev == ev {
			s.steps[i].persisted = persisted
			break
		}
	}
}

// engineEnd ends the engine at the hold's checkpoint, KAS's state and marotte's memory kept: the next engine resumes
// the runs the last one counted executing (Runtime.resumeInterrupted), a chat-parented run, whose step is open,
// through its chat's rehydrate, which reads it only while KAS lists it paused, and a bounded one, a parentless heal
// row's, through resumeParentless, which reads it while its lease stands.
func (s *holdSim) engineEnd() {
	// The next engine loads the chat's session anew, which takes up a sign-in's mark for its reload.
	s.reload, s.edgeMark = false, false
	var r releasers
	if (s.c.chat() && s.kas.holds()) || (!s.c.gate() && !s.c.chat() && s.c.held != heldDeleted) {
		r.healer = healerSweep
	}
	s.sweepNext = r.healer == healerSweep && !s.chatEntryRead(true)
	s.roundAfterEntry(evEngineEnd, "the engine ends, and the next one resumes the runs it interrupted", r,
		s.chatEntryRead(true))
}

// roundAfterEntry is round(ev, text, r), first held at the chat's rehydrate's failed list when entryFails; its sweep
// after orphanResumeDelay (resumeInterruptedRuns) is then the round.
func (s *holdSim) roundAfterEntry(ev holdEvent, text string, r releasers, entryFails bool) {
	if entryFails {
		s.entryRead(ev, text)
		ev, text = evRetry, "its chat's rehydrate sweeps again"
	}
	s.round(ev, text, r)
}

func (s *holdSim) done() bool {
	return s.kas == kasHoldRunning || s.kas == kasParked || (s.kas.ended() && !s.entry)
}

// loop drives the event that releases the run next until it ends, is resumed or parked, or nothing releases it.
func (s *holdSim) loop() {
	for n := 0; !s.done(); n++ {
		if n > 8 {
			panic(fmt.Sprintf("holdModel: %s never settles", s.c.name()))
		}
		// The finder of every paused run releases any run's gate pause, a heal row's too once the gate paused it again.
		edge := releasers{finder: s.kas == kasGateReason}
		if s.entry || s.live {
			edge.healer = healerHeld
		}
		switch {
		case !s.open:
			s.open = true
			s.round(evSignIn, "the opening edge", edge)
		case edge.finder || edge.healer != healerNone:
			s.round(evSignInAgain, "the next opening edge", edge)
		case s.c.chat() && s.kas.holds():
			s.round(evRehydrate, "its chat's next rehydrate", releasers{healer: healerSweep})
		default:
			return
		}
	}
}

// findsEveryPaused is whether ev's runtime runs the finder of every paused run (findHolds with no ids): an opening
// edge's (applyGateMove) or a boot's (BootSweep).
func findsEveryPaused(ev holdEvent) bool {
	switch ev {
	case evSignIn, evSignInAgain, evRestartClosed, evRestartOpen:
		return true
	}
	return false
}

// round is one event's release of the run by r, under the armed faults and crossing.
func (s *holdSim) round(ev holdEvent, text string, r releasers) {
	s.begin(ev, text)
	s.cur.servesLive = s.live
	hold := s.kas.holds()
	// The finder of every paused run reads a heal's run too, and releases nothing of it.
	healListed := !s.c.gate() && findsEveryPaused(ev) && s.kas.listedPaused()
	finderDead := (r.finder || healListed) && failsFinder(s.read)
	if finderDead && (r.healer == healerHeld ||
		(r.healer == healerSweep && s.c.chat() && (ev == evRestartOpen || ev == evRestartClosed))) {
		// The cell lets a held release try again, or starts a restart's chat rehydrate, once the dead finder spent its
		// failing reads.
		s.cur.finderSettles, s.finderEarly = true, true
	}
	if finderDead && (healListed || r.healer != healerNone) {
		// Else a heal resuming it first leaves the finder's list reading it running, with nothing to read.
		s.cur.finderFirst = true
		s.cur.text += "; the finder lists it before its heal reads it"
	}
	healDead := s.read == readHealEvery || s.read == readEvery
	// A finder alone reaches a heal read only through keepHold at an open gate, and not when the gate closes again
	// at its own read first.
	finderHealRead := r.finder && !finderDead && s.open && hold && s.cross != crossClosesAtFind
	healReadComes := r.healer != healerNone || finderHealRead
	if (r.finder || r.healer != healerNone) && (s.read == readFirstOnce || (s.read == readHealOnce && healReadComes)) {
		s.read, s.readFired = readNone, true
		// A sweep's failed first try is tried again by its chain (retryResumeLater), in the next step.
		s.chainNext = r.healer == healerSweep && !r.finder
		s.block("its first read fails, held there")
	}
	finderReads := r.finder && !finderDead
	healerReads := r.healer != healerNone && !healDead
	finderHeals := finderReads && s.open && hold && !healDead
	deadRead := finderDead || (healDead && healReadComes)
	if deadRead {
		s.readFired = true
	}
	var loud []string
	switch {
	case s.open && hold && s.cross == crossClosesAtFind && (finderReads || healerReads),
		s.open && hold && s.cross == crossClosesAtHeal && (healerReads || finderHeals):
		closer := s.cross
		s.cross, s.crossFired, s.open = crossNone, true, false
		s.cur.crosses = true
		s.entry = s.entry || healerReads || closer == crossClosesAtHeal
		s.cur.text += "; the gate closes again before the release admits it, so it is held again"
		loud = s.deadLoud(r, finderDead, healDead, false)
	case !s.open:
		// A heal's read holds a pause it would resume, and drops the hold of a run it reads ended or gone.
		if healerReads {
			s.entry = hold
		}
		loud = s.deadLoud(r, finderDead, healDead, false)
	case hold && (healerReads || finderHeals):
		loud = s.release(r, finderHeals, healerReads)
	case hold:
		loud = s.deadLoud(r, finderDead, healDead, true)
	case healerReads:
		s.entry = false
		s.cur.text += "; its heal reads it " + s.kas.String() + " and drops the hold"
	default:
		loud = s.deadLoud(r, finderDead, healDead, true)
	}
	if s.open {
		s.live = false
	}
	// A block moved the dead finder's ERROR to the round's first step (block); else it is this one.
	moved := s.finderMoved
	s.finderEarly, s.finderMoved = false, false
	if moved {
		deadRead = healDead && healReadComes
	}
	if finderDead && !moved && !slices.Contains(loud, findSpentLog) {
		loud = append(loud, findSpentLog)
	}
	if deadRead {
		s.cur.settleRead = true
		s.cur.text += "; its failing reads settle"
	}
	s.cur.loud = append(s.cur.loud, loud...)
	if len(s.cur.loud) > 0 {
		s.cur.text += ": ERROR"
	}
	s.end()
	if deadRead {
		s.read = readNone
	}
}

// signsIn is whether ev commits a sign-in, which marks a chat's session for its reload (Runtime.onSignInChange): the
// next resume write of the chat's run reloads it, and the reload's rehydrate sweeps the run too.
func (s *holdSim) signsIn(ev holdEvent) bool {
	switch ev {
	case evHold, evHoldRead, evHoldDead:
		// The lapse signs in first only at a closed gate (holdCell.lapse).
		return s.c.boot == bootSignedOut
	case evHoldOpens, evHealOpens, evSignIn, evSignInAgain, evPendAcrossEdge:
		return true
	}
	return false
}

// deadLoud is the ERRORs the dead heal readers of a round that resumes nothing log: a sweep's always, and
// resumeHeld's while the gate stays open (it stops once the gate closes).
func (s *holdSim) deadLoud(r releasers, finderDead, healDead, open bool) []string {
	if !healDead {
		return nil
	}
	switch {
	case r.healer == healerSweep && s.c.chat():
		return []string{resweepSpentLog}
	case r.healer == healerSweep:
		return []string{parentlessSpentLog}
	case open && (r.healer == healerHeld || (r.finder && !finderDead)):
		return []string{releaseSpentLog}
	}
	return nil
}

// spentLoud is the ERRORs a round's writers log once every release write is lost: resumeHeld's when held, and a
// sweep's, a chat's or a parentless run's, when swept.
func (s *holdSim) spentLoud(held, swept bool) []string {
	var loud []string
	if held {
		loud = append(loud, releaseSpentLog)
	}
	switch {
	case swept && s.c.chat():
		loud = append(loud, resweepSpentLog)
	case swept:
		loud = append(loud, parentlessSpentLog)
	}
	return loud
}

// release is a round at an open gate whose live readers can resume the held run: finderHeals is the finder's chain
// (keepHold, then resumeHeld) reaching its heal, healerHeals the healer's own heal.
func (s *holdSim) release(r releasers, finderHeals, healerHeals bool) []string {
	if s.claims >= maxAutoHeals {
		return s.park(finderHeals || (healerHeals && r.healer == healerHeld), healerHeals && r.healer == healerSweep)
	}
	// The edge's release of a held heal and its finder's are one release (releases).
	loops := 0
	if finderHeals || (healerHeals && r.healer == healerHeld) {
		loops = 1
	}
	// The pending pause's frame comes after its opening edge, whose mark the vault leaves unordered with it too.
	edgeLate := s.freshMark && (s.cur.ev == evSignIn || s.cur.ev == evSignInAgain)
	frameLate := s.edgeMark && s.cur.ev == evLandFrame
	late := s.c.order == orderMarkLate && loops > 0 && (edgeLate || frameLate)
	switch {
	case late && frameLate:
		s.orderFired, s.cur.markLate, s.steps[s.edgeStep].holdsMark = true, true, true
		s.steps[s.edgeStep].text += "; its sign-in's reload mark is held"
		s.cur.text += "; its release writes the run's resume before the opening edge's sign-in marks the chat for " +
			"its reload"
	case late:
		s.orderFired, s.cur.markLate = true, true
		s.cur.text += "; the gate opens and its release writes the run's resume before the sign-in marks the chat " +
			"for its reload"
	}
	// The first resume write of a chat's run since a sign-in reloads the chat's session, whose rehydrate sweeps too; a
	// mark that lands after the release's first write reloads the session at the next one.
	firstSweeps := (healerHeals && r.healer == healerSweep) || (s.c.chat() && s.reload && !late)
	sweeps := (healerHeals && r.healer == healerSweep) || (s.c.chat() && s.reload)
	// A chat sweep's own first write reloads the session, so the reload's rehydrate is a second sweep and chain.
	twoSweeps := healerHeals && r.healer == healerSweep && s.c.chat() && s.reload
	if s.c.chat() {
		s.reload = false
	}
	// The crossing's write must be the held release's: a sweep healing the run in the same round may write first.
	if held := loops > 0 && (!healerHeals || r.healer != healerSweep); held && s.cross == crossFlapsAtRetry {
		s.flap(firstSweeps)
		return nil
	} else if held && s.cross.inFlight() {
		s.inFlightWrite(s.c.chat() && firstSweeps)
		return nil
	}
	switch s.write {
	case writeLostOnce:
		s.write, s.writeFired = writeNone, true
		s.claims++
		s.block("its resume write is lost, and the next try is held at its read")
	case writeReplyLost:
		s.write, s.writeFired = writeNone, true
		s.kas, s.resumes, s.claims = kasHoldRunning, s.resumes+1, s.claims+1
		if r.healer == healerSweep && s.c.chat() && !finderHeals {
			// A chat's sweep lists its runs before it reads one, and the list reads this one running.
			s.cur.text += "; KAS takes its resume write and the reply is lost, and the sweep's next try lists it running"
			s.entry = false
			return nil
		}
		s.block("KAS takes its resume write and the reply is lost, and the next try is held at its read")
		s.entry = false
		return nil
	case writeLostEvery:
		s.writeFired, s.cur.settleWrite, s.write = true, true, writeNone
		if sweeps {
			// A sweep's tries outnumber the heals left, so they spend the budget, and their parks are lost too.
			s.claims = maxAutoHeals
			s.cur.text += "; every release write is lost, and the sweep's tries spend the heal budget and lose their parks"
			loud := s.spentLoud(loops > 0, true)
			if twoSweeps {
				loud = append(loud, resweepSpentLog)
			}
			return loud
		}
		s.claims += loops
		s.cur.text += "; every resume write is lost until its tries are spent"
		return []string{releaseSpentLog}
	}
	s.kas, s.entry = kasHoldRunning, false
	s.resumes++
	s.claims++
	return nil
}

// flap is a release under crossFlapsAtRetry: its first resume write is lost, the gate closes and opens again while it
// waits to retry, and the opening edge wakes it to resume the run. When sweeps, the chat's rehydrate its lost
// write reloaded resumes the run in that wait, and the closing edge's sweep pauses it with the gate's reason first.
func (s *holdSim) flap(sweeps bool) {
	s.cross, s.crossFired = crossNone, true
	s.cur.flaps, s.cur.flapPauses = true, sweeps
	s.cur.text += "; its resume write is lost, and the gate closes and opens again while it waits to retry"
	if sweeps {
		s.resumes++
		s.cur.text += "; the chat's rehydrate resumes it in that wait, and the closing edge's sweep pauses it with the " +
			"gate's reason"
	}
	s.cur.text += "; the opening edge wakes the release, which resumes it"
	s.kas, s.entry = kasHoldRunning, false
	s.resumes++
	s.claims++
}

// inFlightWrite is a release under an in-flight crossing: its first resume write is held before KAS takes it while
// the gate closes, and the closing sweep, which lists the run still paused, pauses nothing. The write lands at the
// open gate (crossFlapsInFlight), so the release's next try, which serves the later edge, reads the run running; or
// at the closed gate, so the sweep its admission's crossed epoch starts (runAdmission.settle) pauses the run with the
// gate's reason, the gate still closed, the release ended (crossRepausedEnded) or live, to serve the next edge itself
// (crossRepausedLive). rehydrates: the write reloaded a chat's session, whose rehydrate reads the run once the write
// landed, before that pause (orderFirst) or after it, holding its heal (orderSweepFirst).
func (s *holdSim) inFlightWrite(rehydrates bool) {
	cross := s.cross
	s.cross, s.crossFired = crossNone, true
	s.cur.inFlight, s.cur.rehydrates = cross, rehydrates
	s.kas, s.entry = kasHoldRunning, false
	s.resumes++
	s.claims++
	if cross == crossFlapsInFlight {
		s.cur.text += "; its resume write is in flight while the gate closes and opens again, and lands at the open gate"
		return
	}
	s.kas, s.open, s.live = kasGateReason, false, cross == crossRepausedLive
	s.cur.text += "; its resume write is in flight as the gate closes, and lands at the closed gate, and the closing " +
		"sweep pauses the run with the gate's reason"
	if s.live {
		s.cur.text += ", the release still live"
	} else {
		s.cur.text += ", the release ended"
	}
	if rehydrates && s.c.order == orderSweepFirst {
		s.orderFired, s.entry = true, true
		s.cur.text += "; the chat's rehydrate then reads that pause and holds its heal"
	}
}

// park is a release that finds the heal budget spent and parks the run for the user (parkForUser): held is whether
// resumeHeld writes the park, swept whether a sweep does.
func (s *holdSim) park(held, swept bool) []string {
	s.cur.text += "; its heal finds the budget spent"
	switch s.write {
	case writeLostOnce:
		s.write, s.writeFired = writeNone, true
		s.block("its park write is lost, and the next try is held at its read")
	case writeReplyLost:
		s.write, s.writeFired = writeNone, true
		s.kas = kasParked
		s.block("KAS takes its park write and the reply is lost, and the next try is held at its read")
	case writeLostEvery:
		s.writeFired, s.cur.settleWrite, s.write = true, true, writeNone
		s.cur.text += "; every park write is lost until its tries are spent"
		return s.spentLoud(held, swept)
	}
	s.kas, s.entry = kasParked, false
	s.cur.text += "; it parks it for the user"
	return nil
}

// holdSummary is runGate's doc, rendered from holdModel: the lead, then per dimension value the outcome of the cell
// that differs from the plain cell in that value alone, the rows with one outcome named together.
func holdSummary() string {
	var b strings.Builder
	wrapDoc(&b, gateHoldLead, "", "")
	b.WriteString("\n")
	type variant struct {
		set   func(*holdCombo)
		label string
	}
	variants := []variant{{label: "The plain cell (" + holdCombo{}.name() + ")", set: func(*holdCombo) {}}}
	add := func(names []string, set func(*holdCombo, int)) {
		for v := 1; v < len(names); v++ {
			variants = append(variants, variant{
				label: strings.ToUpper(names[v][:1]) + names[v][1:], set: func(c *holdCombo) { set(c, v) },
			})
		}
	}
	add(landNames, func(c *holdCombo, v int) { c.land = landing(v) })
	add(heldNames, func(c *holdCombo, v int) { c.held = heldEvent(v) })
	add(restartNames, func(c *holdCombo, v int) { c.restart = restartAt(v) })
	add(crossNames, func(c *holdCombo, v int) { c.cross = crossing(v) })
	add(readNames, func(c *holdCombo, v int) { c.read = readFault(v) })
	add(writeNames, func(c *holdCombo, v int) { c.write = writeFault(v) })
	add(orderNames, func(c *holdCombo, v int) { c.order = raceOrder(v) })
	add(bootNames, func(c *holdCombo, v int) { c.boot = bootSign(v) })
	// A signed-in boot decides whether the late mark is the only one pending.
	variants = append(variants, variant{
		label: strings.ToUpper(orderNames[orderMarkLate][:1]) + orderNames[orderMarkLate][1:] +
			", the runtime booted signed in",
		set: func(c *holdCombo) { c.order, c.boot = orderMarkLate, bootSignedIn },
	})
	plain := map[string]string{}
	for _, c := range holdRowCombos() {
		plain[c.rowName()] = holdOutcome(c)
	}
	for i, v := range variants {
		var outcomes []string
		rows := map[string][]string{}
		for _, c := range holdRowCombos() {
			v.set(&c)
			o := holdOutcome(c)
			if i > 0 && o == plain[c.rowName()] {
				o = "as in the plain cell"
			}
			if _, ok := rows[o]; !ok {
				outcomes = append(outcomes, o)
			}
			rows[o] = append(rows[o], c.rowName())
		}
		item := v.label + "."
		for _, o := range outcomes {
			names := strings.Join(rows[o], ", ")
			item += " " + strings.ToUpper(names[:1]) + names[1:] + ": " + o + "."
		}
		wrapDoc(&b, item, "  - ", "    ")
	}
	return b.String()
}

// holdRowCombos is the plain cell of each row.
func holdRowCombos() []holdCombo {
	var out []holdCombo
	for form := range holdForm(len(formNames)) {
		for kind := range runKind(len(kindNames)) {
			out = append(out, holdCombo{kind: kind, form: form})
		}
	}
	return out
}

// holdOutcome is a cell's outcome in brief: how it is held at its checkpoint and what a restart there finds, how it
// ends and after how many resumes, and whether a reader gives up with an ERROR; or why it cannot happen, or why it
// drives as its twin.
func holdOutcome(c holdCombo) string {
	if _, why := c.twin(); why != "" && c.ruledOut() == "" {
		return "as with a signed-out boot, since " + why
	}
	steps, why := holdModel(c)
	if why != "" {
		return "cannot happen, " + why
	}
	parts := []string{"held " + steps[holdCheckpoint(steps)].want.String(), holdPersisted(steps)}
	loud := slices.ContainsFunc(steps, func(s holdStep) bool { return len(s.loud) > 0 })
	last := steps[len(steps)-1]
	parts = append(parts, fmt.Sprintf("ends %s after %s", last.want, resumeCount(last.resumes)))
	if loud {
		parts = append(parts, "with an ERROR")
	}
	return strings.Join(parts, ", ")
}

func resumeCount(n int32) string {
	if n == 1 {
		return "1 resume"
	}
	return fmt.Sprintf("%d resumes", n)
}

// holdCheckpoint is the index of the step the cell's faults and crossing start after, the first step when none does.
func holdCheckpoint(steps []holdStep) int {
	for i := range steps {
		if steps[i].arm {
			return i
		}
	}
	return 0
}

// holdPersisted is what a fresh runtime finds at the cell's checkpoint: a restart cell's own assertion, or for any
// other cell KAS's state there, which the cell asserts and the restart cells show a fresh runtime reads.
func holdPersisted(steps []holdStep) string {
	for i := range steps {
		if steps[i].persisted != kasNone {
			return "a restart finds it " + steps[i].persisted.String() + " with no heal held"
		}
	}
	if steps[holdCheckpoint(steps)].arm {
		return "persisted " + steps[holdCheckpoint(steps)].want.kas.String()
	}
	return "persisted none"
}

// holdCellText is a cell's steps, or why it cannot happen, or the twin it runs as.
func holdCellText(c holdCombo) string {
	if twin, why := c.twin(); why != "" && c.ruledOut() == "" {
		return "runs as " + twin.name() + ", since " + why + "."
	}
	steps, why := holdModel(c)
	if why != "" {
		return "cannot happen: " + why + "."
	}
	parts := make([]string, 0, len(steps))
	for i := range steps {
		parts = append(parts, holdStepText(&steps[i]))
	}
	return strings.Join(parts, "; then ") + "; " + holdPersisted(steps) + "."
}

func holdStepText(s *holdStep) string {
	text := fmt.Sprintf("%s: %s, resumes %d", s.text, s.want, s.resumes)
	if s.blocked {
		text += ", until the held try goes on"
	}
	return text
}

// gateHoldLead opens runGate's doc's matrix.
const gateHoldLead = "Every hold, by the matrix TestGateHold_EveryCell/<row>/<cell> runs in full, rendered from its " +
	"reference model (holdModel), which skips a combination only with the reason it cannot happen, or the cell it " +
	"drives the same as and why. Rows: a chat-parented, manual or scheduled run's gate pause, which KAS holds as the " +
	"run's stopReason, or its heal, which heldHeals holds; a gate pause's heal is held there too when the gate closes " +
	"again before its release admits it. Cells: where the hold lands; what happens while it is held; a restart at its " +
	"checkpoint, which keeps KAS's state and loses heldHeals and the heal budget, or the engine's replacement there, " +
	"which keeps them; the gate crossing the hold or its release; a fault in the reads that form the hold or release " +
	"it; a fault in the release's writes, a resume or, once the heal budget is spent, the park for the user; for a " +
	"chat-parented run, the orders production leaves open between a sign-in's reload mark and the gate's opening, " +
	"and between a reload's rehydrate and a closing sweep; whether the runtime boots signed in, so no reload mark is " +
	"pending as a chat's gate pause forms. Each item is the outcome of the cell that differs from the plain one in " +
	"one value, or in the late mark and a signed-in boot: the hold at the checkpoint, before which nothing resumes the " +
	"run, what a fresh runtime finds there, and the end. holdCellText gives every cell's steps. A person's pause or " +
	"resume of a held run is runTransitions'."

// holdTable is every cell of the matrix, one per line, as GATE_HOLD_TABLE renders it.
func holdTable() string {
	var b strings.Builder
	for _, c := range everyCombo() {
		fmt.Fprintf(&b, "- %s / %s: %s\n", c.rowName(), c.name(), holdCellText(c))
	}
	return b.String()
}

// docWidth is a doc comment line's width after its "// ".
const docWidth = 117

// wrapDoc writes text to b in lines of at most docWidth, the first prefixed by first and the rest by rest.
func wrapDoc(b *strings.Builder, text, first, rest string) {
	line := first
	for w := range strings.FieldsSeq(text) {
		switch {
		case line == first || line == rest:
			line += w
		case len(line)+1+len(w) > docWidth:
			b.WriteString(line + "\n")
			line = rest + w
		default:
			line += " " + w
		}
	}
	b.WriteString(line + "\n")
}

// runGate's doc comment ends with holdSummary. With GATE_HOLD_TABLE set it writes the summary there, and every cell
// to the same name with ".cells" added.
func TestGateHold_TheTableIsRunGatesDoc(t *testing.T) {
	want := holdSummary()
	if out := os.Getenv("GATE_HOLD_TABLE"); out != "" {
		if err := os.WriteFile(out, []byte(want), 0o600); err != nil {
			t.Fatalf("write the summary: %v", err)
		}
		if err := os.WriteFile(out+".cells", []byte(holdTable()), 0o600); err != nil {
			t.Fatalf("write the cells: %v", err)
		}
	}
	f, err := parser.ParseFile(token.NewFileSet(), "run_gate.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse run_gate.go: %v", err)
	}
	var doc string
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE && g.Specs[0].(*ast.TypeSpec).Name.Name == "runGate" {
			doc = g.Doc.Text()
		}
	}
	if !strings.HasSuffix(doc, want) {
		t.Errorf("runGate's doc does not end with holdSummary; GATE_HOLD_TABLE=<file> go test "+
			"-run '^TestGateHold_TheTableIsRunGatesDoc$' ./internal/agent/ writes it. Want:\n%s", want)
	}
}

// kasRun is KAS's one run for a hold cell: its list row, inspect, pause, resume and cancel answered from one state,
// with the release writes' armed fault.
type kasRun struct {
	br            *fakeBridge
	onWriteFault  func(writeFault)
	id            string
	parent        string
	status        marotte.RunStatus
	pauseReason   string
	stopReason    string
	pendingReason string
	write         writeFault
	resumes       atomic.Int32
	pauses        atomic.Int32
	// finderLists, sweepLists and closingLists count the lists of the finder of every paused run (pausedRuns), of a
	// chat's sweep (resumeChainRuns) and of the closing sweep (sweepPause).
	finderLists  atomic.Int32
	sweepLists   atomic.Int32
	closingLists atomic.Int32
	// onEntryRead holds a heal entry's failing read (failsEntryRead).
	onEntryRead func()
	mu          sync.Mutex
	landLater   bool
	gone        bool
	// staleOwner: KAS lists the run running for an engine that ended, until the read after its skipped resume.
	staleOwner bool
	// failEntryList: the next first sweep of a chat's rehydrate fails its list.
	failEntryList bool
	// flapWrite: the next resume write of a held release (resumeHeld) is lost before KAS takes it (crossFlapsAtRetry),
	// and onFlapWrite runs.
	flapWrite   bool
	onFlapWrite func()
	// writeHold holds the next resume write of a held release before KAS takes it, until its retried closes;
	// listHolds the next run list a lister asks, by lister, until theirs closes.
	writeHold *holdBlock
	listHolds map[string]*holdBlock
	// handles are the sessions answering from this run: the engine's lane and one per door (door).
	handles []*fakeBridge
	// lastCall is when the runtime last called the run's KAS, in Unix nanoseconds.
	lastCall atomic.Int64
	// resumeWrites counts the resume writes sent, before any fault or hold acts on one.
	resumeWrites atomic.Int32
	// chainLists counts a chat's sweep chain's later lists (retryResumeLater), chainListedPaused whether the last
	// one listed the run paused, so that try reads it.
	chainLists        atomic.Int32
	chainListedPaused bool
	// sweepListedPaused: the last list of a chat's sweep listed the run paused.
	sweepListedPaused bool
}

func (k *kasRun) runningState() {
	k.status, k.pauseReason, k.stopReason = marotte.RunStatusRunning, "", ""
}

func (k *kasRun) restartPausedState() {
	k.status, k.pauseReason, k.stopReason = marotte.RunStatusPaused, stalePauseReason, ""
}

// attach makes br, the engine's lane, answer from k, as a restart's new engine reads the KAS state the last one left.
func (k *kasRun) attach(br *fakeBridge) {
	k.mu.Lock()
	k.br = br
	k.mu.Unlock()
	k.answerOn(br)
}

func (k *kasRun) answerOn(br *fakeBridge) {
	k.mu.Lock()
	k.handles = append(k.handles, br)
	gone := k.gone
	k.mu.Unlock()
	br.mu.Lock()
	br.onCallFails = k.answer
	br.mu.Unlock()
	if gone {
		br.setCallRPCErr(methodKiroWorkflowInspect, workflowNotFoundErr())
	}
}

// door is the session one door opens: a handle of its own over this run's KAS state, as the engine mints one per
// door, so a closed generation's late exit finds a different handle registered.
func (k *kasRun) door() engineSession {
	b := newFakeBridge()
	b.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	k.answerOn(b)
	return b
}

// detach stops every handle answering from k, as a stopped runtime's engine answers no more.
func (k *kasRun) detach() {
	k.mu.Lock()
	handles := k.handles
	k.handles = nil
	k.mu.Unlock()
	for _, b := range handles {
		b.mu.Lock()
		b.onCallFails = nil
		b.mu.Unlock()
	}
}

func workflowNotFoundErr() *marotte.RPCError {
	return &marotte.RPCError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`)}
}

// armWrite fails the release writes as w says, from the next one on; writeNone ends the fault.
func (k *kasRun) armWrite(w writeFault) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.write = w
}

func (k *kasRun) reads() kasHold {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case k.gone:
		return kasGone
	case k.pendingReason != "":
		return kasPausePending
	case k.status == marotte.RunStatusCancelled:
		return kasCancelled
	case k.status == marotte.RunStatusRunning && k.stopReason == "":
		return kasHoldRunning
	case k.status == marotte.RunStatusPaused && isHoldReason(k.stopReason):
		return kasGateReason
	case k.status == marotte.RunStatusPaused && k.stopReason == manualResumeReason:
		return kasParked
	case k.status == marotte.RunStatusPaused && k.pauseReason == stalePauseReason && k.stopReason == "":
		return kasRestartPause
	}
	return kasOther
}

func (k *kasRun) answer(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
	k.lastCall.Store(time.Now().UnixNano())
	if k.failsEntryRead(method) {
		return nil, nil, true, errHoldReadFault
	}
	if method == methodKiroWorkflowResume {
		k.resumeWrites.Add(1)
		if b := k.takeWriteHold(); b != nil {
			close(b.reached)
			<-b.retried
		}
	}
	if method == methodKiroWorkflowList {
		if b := k.takeListHold(lister()); b != nil {
			close(b.reached)
			<-b.retried
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	switch method {
	case methodKiroWorkflowList:
		switch lister() {
		case "pausedRuns":
			k.finderLists.Add(1)
		case "resumeChainRuns":
			k.sweepLists.Add(1)
			k.sweepListedPaused = !k.gone && k.status == marotte.RunStatusPaused
			if retryOf() == retryChat {
				k.chainListedPaused = !k.gone && k.status == marotte.RunStatusPaused
				k.chainLists.Add(1)
			}
		case "sweepPause":
			k.closingLists.Add(1)
		}
		return k.listLocked(), nil, true, nil
	case methodKiroWorkflowInspect:
		return k.inspectLocked(), nil, true, nil
	case methodKiroWorkflowPause:
		reason, _ := params["reason"].(string)
		if reason == manualResumeReason {
			if takes, err := k.releaseFaultLocked(); err != nil {
				if takes {
					k.pauses.Add(1)
					k.pendingReason = reason
					k.landLocked()
				}
				return nil, nil, true, err
			}
		}
		k.pauses.Add(1)
		k.pendingReason = reason
		if !k.landLater {
			k.landLocked()
		}
		return json.RawMessage(`{"paused":true}`), nil, true, nil
	case methodKiroWorkflowResume:
		if k.flapWrite && innermostOf(".(*Runs).resumeHeld") != "" {
			k.flapWrite = false
			k.onFlapWrite()
			return nil, nil, true, errHoldFault
		}
		takes, err := k.releaseFaultLocked()
		if takes {
			k.resumes.Add(1)
			k.status, k.pauseReason, k.stopReason, k.pendingReason = marotte.RunStatusRunning, "", "", ""
		}
		if err != nil {
			return nil, nil, true, err
		}
		return json.RawMessage(`{}`), nil, true, nil
	case methodKiroWorkflowCancel:
		k.status, k.pendingReason = marotte.RunStatusCancelled, ""
		return json.RawMessage(`{}`), nil, true, nil
	}
	return nil, nil, false, nil
}

// takeWriteHold is the armed write hold when this call is a held release's resume write (resumeHeld), which then
// disarms it.
func (k *kasRun) takeWriteHold() *holdBlock {
	if innermostOf(".(*Runs).resumeHeld") == "" {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	b := k.writeHold
	k.writeHold = nil
	return b
}

// holdList holds the next run list by asks, a lister's name, until b lets it go.
func (k *kasRun) holdList(asks string, b *holdBlock) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.listHolds == nil {
		k.listHolds = map[string]*holdBlock{}
	}
	k.listHolds[asks] = b
}

func (k *kasRun) takeListHold(asks string) *holdBlock {
	k.mu.Lock()
	defer k.mu.Unlock()
	b := k.listHolds[asks]
	delete(k.listHolds, asks)
	return b
}

// releaseFaultLocked applies the armed write fault to one release write: whether KAS takes the write, and the
// transport's failure, if any, that its caller then reads.
func (k *kasRun) releaseFaultLocked() (takes bool, err error) {
	w := k.write
	if w == writeNone {
		return true, nil
	}
	if w != writeLostEvery {
		k.write = writeNone
	}
	k.onWriteFault(w)
	return w == writeReplyLost, errHoldFault
}

// failsEntryRead is whether this call is a heal entry's read that its armed fault fails, once: a chat's rehydrate's
// first list, or resumeParentless's read after the resume it skipped while KAS listed the run running for an ended
// engine, after which KAS lists the run paused for the restart, as it does once the run's owner is stale.
func (k *kasRun) failsEntryRead(method string) bool {
	var fails bool
	switch method {
	case methodKiroWorkflowList:
		if lister() != "resumeChainRuns" || retryOf() != retryNone {
			return false
		}
		k.mu.Lock()
		fails, k.failEntryList = k.failEntryList, false
		k.mu.Unlock()
	case methodKiroWorkflowInspect:
		if innermostOf(".(*Runtime).resumeParentless", ".(*Runtime).resumeParentless.func1") != ".(*Runtime).resumeParentless" {
			return false
		}
		k.mu.Lock()
		fails, k.staleOwner = k.staleOwner, false
		if fails {
			k.restartPausedState()
		}
		k.mu.Unlock()
	}
	if fails {
		k.onEntryRead()
	}
	return fails
}

// callStack is the calling goroutine's function names, innermost first.
func callStack() []string {
	pcs := make([]uintptr, 128)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	var out []string
	for {
		f, more := frames.Next()
		out = append(out, f.Function)
		if !more {
			return out
		}
	}
}

// innermostOf is the first of suffixes that ends a function name on the calling goroutine's stack, innermost frame
// first, "" when none does.
func innermostOf(suffixes ...string) string {
	for _, fn := range callStack() {
		for _, s := range suffixes {
			if strings.HasSuffix(fn, s) {
				return s
			}
		}
	}
	return ""
}

// lister names the Runs method whose run list the calling goroutine asks, "" for any other.
func lister() string {
	return strings.TrimPrefix(innermostOf(".(*Runs).pausedRuns", ".(*Runs).resumeChainRuns", ".(*Runs).sweepPause"),
		".(*Runs).")
}

// retryOf is the retryResumeLater chain whose later try the calling goroutine runs: retryChat a chat's re-sweep,
// retryParentless a parentless run's next try, retryNone a caller's own first try or any other reader.
func retryOf() retryChain {
	stack := callStack()
	on := func(sub string) bool {
		return slices.ContainsFunc(stack, func(fn string) bool { return strings.Contains(fn, sub) })
	}
	switch {
	case !on(".(*Runs).retryResumeLater"):
		return retryNone
	case on(".(*Runs).resumeInterruptedRuns"):
		return retryChat
	case on(".(*Runtime).resumeParentless"):
		return retryParentless
	}
	return retryNone
}

// retryChain is a retryResumeLater caller, by the ERROR its spent chain logs.
type retryChain int

const (
	retryNone retryChain = iota - 1
	retryChat
	retryParentless
)

// retrySpentLogs is each chain's spent ERROR, by retryChain.
var retrySpentLogs = [...]string{retryChat: resweepSpentLog, retryParentless: parentlessSpentLog}

func (k *kasRun) listLocked() json.RawMessage {
	runs := []map[string]any{}
	if !k.gone {
		runs = append(runs, map[string]any{
			"workflowId": k.id, "workflowName": "publish", "status": k.status, "parentSessionId": k.parent,
			"pausePending": k.pendingReason != "",
		})
	}
	return kasJSON(map[string]any{"runs": runs})
}

func (k *kasRun) inspectLocked() json.RawMessage {
	st := map[string]any{"status": k.status, "pauseReason": k.pauseReason}
	if k.pendingReason != "" {
		st["pausePending"] = map[string]any{"initiator": initiatorUser}
	}
	if k.stopReason != "" {
		st["stopInitiator"], st["stopReason"] = initiatorUser, k.stopReason
	}
	return kasJSON(map[string]any{"workflowId": k.id, "state": st})
}

func (k *kasRun) landLocked() {
	k.status, k.pauseReason, k.stopReason, k.pendingReason =
		marotte.RunStatusPaused, "Workflow paused during node 'review'.", k.pendingReason, ""
}

func (k *kasRun) set(fn func(*kasRun)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn(k)
}

// vanish is KAS deleting the run: the list drops it and its inspect answers workflow not found, on every handle.
func (k *kasRun) vanish() {
	k.mu.Lock()
	k.gone = true
	handles := slices.Clone(k.handles)
	k.mu.Unlock()
	for _, b := range handles {
		b.setCallRPCErr(methodKiroWorkflowInspect, workflowNotFoundErr())
	}
}

func kasJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

var errHoldFault = errors.New("the engine did not answer")

// holdLogs routes the log records that name a cell's run, chat or session to that cell, so cells run side by side.
type holdLogs struct {
	sinks *sync.Map
	attrs []slog.Attr
}

// holdNamed finds the cell names in a record's attribute, an error's text included.
var holdNamed = regexp.MustCompile(`\b(?:wf|chat|sess)_hold_\d+\b`)

// cellRecord is one routed record: its message and its attributes, key=value.
type cellRecord struct {
	msg   string
	attrs []string
}

// cellLog is one cell's routed records.
type cellLog struct {
	errors []cellRecord
	infos  []string
	// all is every record, with its level and attributes, for a failed cell's report.
	all []string
	mu  sync.Mutex
}

func (l *cellLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.all, "\n")
}

// infoSince is whether a record below ERROR from the from'th on holds msg in its message.
func (l *cellLog) infoSince(msg string, from int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.ContainsFunc(l.infos[min(from, len(l.infos)):], func(m string) bool { return strings.Contains(m, msg) })
}

func (l *cellLog) infoCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.infos)
}

// errorCount is how many ERRORs hold msg in their message.
func (l *cellLog) errorCount(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.errors {
		if strings.Contains(r.msg, msg) {
			n++
		}
	}
	return n
}

// errorsLacking is the attributes of each ERROR whose message holds msg and whose attributes lack attr.
func (l *cellLog) errorsLacking(msg, attr string) [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out [][]string
	for _, r := range l.errors {
		if strings.Contains(r.msg, msg) && !slices.Contains(r.attrs, attr) {
			out = append(out, r.attrs)
		}
	}
	return out
}

var _ slog.Handler = (*holdLogs)(nil)

func (*holdLogs) Enabled(_ context.Context, level slog.Level) bool { return level >= slog.LevelInfo }

func (h *holdLogs) Handle(_ context.Context, rec slog.Record) error {
	var keys, attrs []string
	visit := func(a slog.Attr) bool {
		text := a.String()
		attrs = append(attrs, text)
		keys = append(keys, holdNamed.FindAllString(text, -1)...)
		return true
	}
	for _, a := range h.attrs {
		visit(a)
	}
	rec.Attrs(visit)
	seen := map[*cellLog]bool{}
	for _, key := range keys {
		if v, ok := h.sinks.Load(key); ok && !seen[v.(*cellLog)] {
			l := v.(*cellLog)
			seen[l] = true
			l.mu.Lock()
			l.all = append(l.all, rec.Level.String()+" "+rec.Message+" "+strings.Join(attrs, " "))
			if rec.Level >= slog.LevelError {
				l.errors = append(l.errors, cellRecord{msg: rec.Message, attrs: attrs})
			} else {
				l.infos = append(l.infos, rec.Message)
			}
			l.mu.Unlock()
		}
	}
	return nil
}

func (h *holdLogs) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &holdLogs{sinks: h.sinks, attrs: slices.Concat(h.attrs, attrs)}
}

func (h *holdLogs) WithGroup(string) slog.Handler { return h }

// spentCounts is the try count each exhausted reader's ERROR reports, as its attribute.
var spentCounts = map[string]string{
	findSpentLog:       fmt.Sprintf("attempts=%d", maxFindAttempts),
	sweepSpentLog:      fmt.Sprintf("attempts=%d", maxSweepAttempts),
	releaseSpentLog:    fmt.Sprintf("attempts=%d", maxReleaseAttempts),
	resweepSpentLog:    fmt.Sprintf("sweeps=%d", maxResweeps),
	parentlessSpentLog: fmt.Sprintf("tries=%d", maxResweeps),
}

// holdBlock is a read a cell holds in its transition until the cell's next step lets it go on.
type holdBlock struct {
	reached chan struct{}
	retried chan struct{}
	release func()
}

// cellFaults are a cell's armed faults and crossing, which the transition hook of each of its runtimes reads.
type cellFaults struct {
	lastHit   time.Time
	cell      atomic.Pointer[holdCell]
	hits      int
	blocks    chan *holdBlock
	all       []*holdBlock
	mu        sync.Mutex
	read      readFault
	cross     crossing
	gateRow   bool
	blockNext bool
	// done: the cell's runtime is stopping, so no read is held.
	done bool
	// finderFirst holds a heal's and the orphan sweep's reads until the finder lists the run past finderBase lists.
	finderFirst bool
	finderBase  int32
	// finderRead closes at the next finder's read of the run; finds counts them.
	finderRead chan struct{}
	finds      int
	// crossed is set once the gate closed again under a release.
	crossed atomic.Bool
	// laterTries counts the heal reads of each retryResumeLater chain's later tries, by retryChain.
	laterTries [2]int
	// flapGap: the held release lost the crossing's write, so its next wait to retry is the flap's (releaseWait);
	// gapReached closes as it starts.
	flapGap    bool
	gapReached chan struct{}
	// tryHold, when set, holds the next release try that wrote the run's resume after it ended, the release still
	// live (crossRepausedLive).
	tryHold *holdBlock
	// markWait, when set, holds a release's next wait to retry until a late sign-in's reload mark landed, so its next
	// write reloads the chat's session (orderMarkLate); finderWait, for the step, each wait until the dead finder
	// spent its failing reads.
	markWait   func()
	finderWait func()
	// rehydrateRead closes at the next heal read of a chat's sweep (resumeChainRuns).
	rehydrateRead chan struct{}
	// heldReads counts the transition reads held in the hook now, each under its run's resume lock, wentOn the held
	// reads let go that went on; healReads every heal read the hook saw.
	heldReads atomic.Int32
	wentOn    atomic.Int32
	healReads int
}

func (f *cellFaults) healReadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healReads
}

// newHold is a hold the cell's step lets go of, and the runtime's stop too.
func (f *cellFaults) newHold() *holdBlock {
	b := &holdBlock{reached: make(chan struct{}), retried: make(chan struct{})}
	b.release = sync.OnceFunc(func() { close(b.retried) })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		b.release()
	}
	f.all = append(f.all, b)
	return b
}

// releaseTried is a runtime's release-try hook: it holds the armed try that wrote the resume.
func (f *cellFaults) releaseTried(out resumeOutcome) {
	if out != resumeWritten {
		return
	}
	f.mu.Lock()
	b := f.tryHold
	f.tryHold = nil
	f.mu.Unlock()
	if b != nil {
		close(b.reached)
		<-b.retried
	}
}

var (
	releaseTriesHooked sync.Once
	releaseTries       sync.Map
)

// onReleaseTry installs fn as h's release-try hook (releaseTryHook) for the test, and returns its uninstall.
func onReleaseTry(t *testing.T, h *Runtime, fn func(out resumeOutcome)) (unhook func()) {
	t.Helper()
	releaseTriesHooked.Do(func() {
		hook := func(rs *Runs, _ string, out resumeOutcome) {
			if fn, ok := releaseTries.Load(rs); ok {
				fn.(func(resumeOutcome))(out)
			}
		}
		releaseTryHook.Store(&hook)
	})
	runs := h.runs
	releaseTries.Store(runs, fn)
	unhook = func() { releaseTries.Delete(runs) }
	t.Cleanup(unhook)
	return unhook
}

// flapWait is how long a flapped release waits to retry: longer than any step waits, so only the opening edge's
// wake ends it in time.
const flapWait = time.Hour

// releaseWait is a runtime's release-wait hook: the wait after the crossing's lost write is flapWait, every other
// release's wait its backoff gap; an armed markWait and finderWait run first.
func (f *cellFaults) releaseWait(gap time.Duration) time.Duration {
	f.mu.Lock()
	mark, finder := f.markWait, f.finderWait
	f.markWait = nil
	if f.flapGap {
		f.flapGap, gap = false, flapWait
		close(f.gapReached)
	}
	f.mu.Unlock()
	if mark != nil {
		mark()
	}
	if finder != nil {
		finder()
	}
	return gap
}

// onFlapWrite records the crossing's lost write.
func (f *cellFaults) onFlapWrite() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flapGap = true
}

var (
	releaseWaitsHooked sync.Once
	releaseWaits       sync.Map
)

// onReleaseWait installs fn as h's release-wait hook (releaseWaitHook) for the test, and returns its uninstall.
func onReleaseWait(t *testing.T, h *Runtime, fn func(gap time.Duration) time.Duration) (unhook func()) {
	t.Helper()
	releaseWaitsHooked.Do(func() {
		hook := func(rs *Runs, _ string, gap time.Duration) time.Duration {
			if fn, ok := releaseWaits.Load(rs); ok {
				return fn.(func(time.Duration) time.Duration)(gap)
			}
			return gap
		}
		releaseWaitHook.Store(&hook)
	})
	runs := h.runs
	releaseWaits.Store(runs, fn)
	unhook = func() { releaseWaits.Delete(runs) }
	t.Cleanup(unhook)
	return unhook
}

func (f *cellFaults) hitLocked() {
	f.lastHit = time.Now()
	f.hits++
}

// blockLocked holds the calling transition until the cell's step lets it go on.
func (f *cellFaults) blockLocked() *holdBlock {
	b := &holdBlock{reached: make(chan struct{}), retried: make(chan struct{})}
	b.release = sync.OnceFunc(func() { close(b.retried) })
	f.all = append(f.all, b)
	f.blocks <- b
	return b
}

// releaseAll lets every held read go on and holds none after, as a runtime stops.
func (f *cellFaults) releaseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done, f.blockNext = true, false
	for _, b := range f.all {
		b.release()
	}
}

var errHoldReadFault = errors.New("the engine did not answer the inspect")

// holdsAt is whether want is the read that forms the cell's hold: the closing sweep's of a gate pause, the heal
// entry's of a heal.
func (f *cellFaults) holdsAt(want runWant) bool {
	if f.gateRow {
		return want == gatePause
	}
	return want == healResume
}

// awaitFinderFirst waits, for a heal's or the orphan sweep's read, until the finder listed the run, when the step
// orders it first.
func (f *cellFaults) awaitFinderFirst(want runWant) {
	if want != healResume && want != orphanCancel {
		return
	}
	f.mu.Lock()
	first, base := f.finderFirst, f.finderBase
	f.mu.Unlock()
	if !first {
		return
	}
	cell := f.cell.Load()
	for deadline := time.Now().Add(5 * time.Second); cell.k.finderLists.Load() <= base; {
		if time.Now().After(deadline) {
			cell.t.Errorf("a heal's or the orphan sweep's read waited 5 s for the finder to list the run first " +
				"(lister names pausedRuns by its call stack)")
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// failEntryRead holds a heal entry's failing read until the cell's next step.
func (f *cellFaults) failEntryRead() {
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	_ = f.failOnceLocked()
}

// failOnceLocked fails the calling read once, held until the cell's step lets it go on.
func (f *cellFaults) failOnceLocked() error {
	f.read = readNone
	f.hitLocked()
	b := f.blockLocked()
	f.mu.Unlock()
	close(b.reached)
	f.heldReads.Add(1)
	<-b.retried
	f.heldReads.Add(-1)
	f.wentOn.Add(1)
	return errHoldReadFault
}

// hook is a runtime's transition hook: it fails the hold's reads as it forms, opens the gate before the hold forms,
// fails and holds the release's reads, holds the try after a failed write, and closes the gate again as the release
// reads the hold.
func (f *cellFaults) hook(rs *Runs) func(want runWant, seen runSeen) error {
	return func(want runWant, seen runSeen) error {
		f.awaitFinderFirst(want)
		retry := retryNone
		if want == healResume {
			retry = retryOf()
		}
		f.mu.Lock()
		if retry != retryNone {
			f.laterTries[retry]++
		}
		if want == gateFind {
			f.finds++
		}
		if want == healResume {
			f.healReads++
		}
		if want == gateFind && f.finderRead != nil {
			close(f.finderRead)
			f.finderRead = nil
		}
		if want == healResume && f.rehydrateRead != nil && innermostOf(".(*Runs).resumeChainRuns") != "" {
			close(f.rehydrateRead)
			f.rehydrateRead = nil
		}
		switch {
		case f.holdsAt(want) && f.read == readHoldEvery:
			f.hitLocked()
			f.mu.Unlock()
			return errHoldReadFault
		case f.holdsAt(want) && f.read == readHoldOnce && !f.done:
			return f.failOnceLocked()
		}
		if f.cross == crossOpensBeforeHold &&
			((f.gateRow && want == gatePause && seen == seenRunning) || (!f.gateRow && want == healResume && seen == seenRestartPaused)) {
			f.cross = crossNone
			f.mu.Unlock()
			f.cell.Load().signInNow()
			return nil
		}
		if want == gateFind && failsFinder(f.read) {
			f.hitLocked()
			f.mu.Unlock()
			return errHoldReadFault
		}
		if want != healResume && (!f.gateRow || want != gateFind) {
			f.mu.Unlock()
			return nil
		}
		switch {
		case want == healResume && (f.read == readEvery || f.read == readHealEvery):
			f.hitLocked()
			f.mu.Unlock()
			return errHoldReadFault
		case f.done:
			f.mu.Unlock()
			return nil
		case f.read == readFirstOnce || (f.read == readHealOnce && want == healResume):
			return f.failOnceLocked()
		case f.blockNext:
			f.blockNext = false
			b := f.blockLocked()
			f.mu.Unlock()
			close(b.reached)
			f.heldReads.Add(1)
			<-b.retried
			f.heldReads.Add(-1)
			f.wentOn.Add(1)
			return nil
		}
		cross := f.cross
		holds := seen == seenGatePaused || seen == seenRestartPaused
		fire := holds && (cross == crossClosesAtFind || (cross == crossClosesAtHeal && want == healResume))
		if fire {
			f.cross = crossNone
		}
		f.mu.Unlock()
		if fire && rs.gateOpen() {
			f.cell.Load().signOutNow()
			f.crossed.Store(true)
		} else if fire {
			f.mu.Lock()
			f.cross = cross
			f.mu.Unlock()
		}
		return nil
	}
}

// awaitFinder returns a wait for the next finder's read of the run, or no wait when wanted is false.
func (f *cellFaults) awaitFinder(wanted bool) func() {
	if !wanted {
		return func() {}
	}
	read := make(chan struct{})
	f.mu.Lock()
	f.finderRead = read
	f.mu.Unlock()
	return func() {
		select {
		case <-read:
		case <-time.After(5 * time.Second):
			f.cell.Load().t.Errorf("a step waited 5 s for the finder's read of the run")
		}
	}
}

// onWriteFault records a release write failing; a once fault holds the next release read.
func (f *cellFaults) onWriteFault(w writeFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hitLocked()
	if w != writeLostEvery {
		f.blockNext = true
	}
}

// holdCell is one runtime of a cell: the gate's vault, KAS's run, and the cell's faults.
type holdCell struct {
	*gateRun
	t       *testing.T
	k       *kasRun
	f       *cellFaults
	logs    *cellLog
	c       holdCombo
	stop    func()
	unhook  func()
	id      string
	chat    marotte.ChatID
	session string
	block   *holdBlock
	prevEv  holdEvent
	// stepInfos is the count of records below ERROR as the current step began, after which awaitInfo looks.
	stepInfos int
	// lapsed: a step closed the gate under the running run, so the next one does not.
	lapsed bool
	// liveTry holds a release live after its try, for the next step's opening edge (crossRepausedLive).
	liveTry *holdBlock
	// heldMark is the reload mark a step's sign-in left held for the next step (holdStep.holdsMark).
	heldMark *lateMark
	// onBoot arms a restart step's holds once the stopped runtime let go of the last ones.
	onBoot func()
	// markHold, when set, holds the vault's delivery of the next sign-in before the runtime's listener marks the
	// chat's session for its reload; marks counts the sign-ins delivered past that listener.
	markHold atomic.Pointer[holdBlock]
	marks    atomic.Int32
	view     gateView
}

// gateView stands between a cell's gate and its vault: while held, the gate reads the state it held at, as when no
// reader has read the vault since a sign-in committed.
type gateView struct {
	at   signin.Answerability
	mu   sync.Mutex
	held bool
}

func (v *gateView) read(vault func() signin.Answerability) signin.Answerability {
	v.mu.Lock()
	held, at := v.held, v.at
	v.mu.Unlock()
	if held {
		return at
	}
	return vault()
}

func (v *gateView) hold(at signin.Answerability) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.held, v.at = true, at
}

func (v *gateView) let() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.held = false
}

// watchMarks puts the cell between the vault and the runtime's sign-in listener, and the gate's read behind its view:
// before for markHold, after for marks. pre is true while the runtime's listeners are not yet registered.
func (c *holdCell) watchMarks(vault *signin.Vault, pre bool) {
	if pre {
		vault.OnChange(func(ch signin.Change) {
			if ch.To != signin.StatusSignedIn {
				return
			}
			if b := c.markHold.Swap(nil); b != nil {
				close(b.reached)
				<-b.retried
			}
		})
		return
	}
	vault.OnChange(func(ch signin.Change) {
		if ch.To == signin.StatusSignedIn && ch.From != signin.StatusSignedIn {
			c.marks.Add(1)
		}
	})
	g := &c.h.runs.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	read := g.read
	c.view.let()
	g.read = func() signin.Answerability { return c.view.read(read) }
}

// holdCellSeq numbers the cells, whose runs and chats are named apart so their logs route apart.
var holdCellSeq atomic.Int64

// newHoldCell boots a cell's first runtime over a fresh KAS run, signed in as the cell's boot says.
func newHoldCell(t *testing.T, router *holdLogs, c holdCombo) *holdCell {
	t.Helper()
	n := holdCellSeq.Add(1)
	cell := &holdCell{
		t: t, c: c, id: fmt.Sprintf("wf_hold_%d", n), chat: marotte.ChatID(fmt.Sprintf("chat_hold_%d", n)),
		session: fmt.Sprintf("sess_hold_%d", n), logs: &cellLog{},
		f: &cellFaults{gateRow: c.gate(), blocks: make(chan *holdBlock, 16)},
	}
	if c.cross == crossOpensBeforeHold {
		cell.f.cross = crossOpensBeforeHold
	}
	if c.holdRead() {
		cell.f.read = c.read
	}
	for _, key := range []string{cell.id, string(cell.chat), cell.session} {
		router.sinks.Store(key, cell.logs)
	}
	t.Cleanup(func() {
		for _, key := range []string{cell.id, string(cell.chat), cell.session} {
			router.sinks.Delete(key)
		}
	})
	k := &kasRun{
		id: cell.id, parent: testLaunchSession, onWriteFault: cell.f.onWriteFault, onEntryRead: cell.f.failEntryRead,
		onFlapWrite: cell.f.onFlapWrite,
	}
	if c.chat() {
		k.parent = cell.session
	}
	if c.gate() {
		k.runningState()
		k.landLater = c.land == landAfter
	} else {
		k.restartPausedState()
	}
	cell.k = k
	t.Cleanup(cell.f.releaseAll)
	cell.boot(t, c.boot == bootSignedIn)
	return cell
}

// boot starts a runtime for the cell over its KAS run, the vault answerable at boot when signedIn.
func (c *holdCell) boot(t *testing.T, signedIn bool) {
	t.Helper()
	dir := t.TempDir()
	if signedIn {
		if err := os.WriteFile(filepath.Join(dir, "signin.json"), []byte(signedInAtBoot), 0o600); err != nil {
			t.Fatalf("Setup: write signin.json: %v", err)
		}
	}
	b := newGateBackend()
	vault := openVault(t, dir, b)
	c.watchMarks(vault, true)
	var (
		h  *Runtime
		br *fakeBridge
	)
	if c.c.chat() {
		h, br = agentRunOnChatAs(t, c.id, c.chat, c.session, c.k.door().(*fakeBridge), WithSignIn(vault, gateBackendID))
		h.runs.grantLease(t.Context(), c.id, "publish",
			launchOrigin{origin: runlease.OriginAgent, chatID: string(c.chat)}, hostSec())
		h.runs.recordParentSession(t.Context(), c.id, c.session)
	} else {
		h, _, br = newTestHub(WithSignIn(vault, gateBackendID))
		origin := runlease.OriginManual
		if c.c.kind == kindScheduled {
			origin = runlease.OriginScheduled
		}
		l := runlease.Lease{
			WorkflowID: c.id, Recipe: "publish", Origin: origin, ParentSessionID: testLaunchSession,
			Host: &runlease.HostSecurity{Presets: []string{}},
		}
		if !c.c.gate() {
			// Bounded, so an engine end counts the run as interrupted and the next engine resumes it.
			l.Deadline = time.Now().Add(time.Hour)
		}
		leaseRun(t, h, l)
	}
	c.stop = sync.OnceFunc(func() {
		c.f.releaseAll()
		shutdownHub(t, h)
	})
	t.Cleanup(c.stop)
	if open := h.runs.gateOpen(); open != signedIn {
		t.Fatalf("Setup: the gate reads open %t at boot, want %t", open, signedIn)
	}
	c.gateRun = &gateRun{h: h, br: br, vault: vault, b: b}
	c.f.cell.Store(c)
	c.k.attach(br)
	fakeSourceOf(t, h).setFactory(c.k.door)
	c.watchMarks(vault, false)
	unhookReads, unhookWaits := onTransitionReadErr(t, h, c.f.hook(h.runs)), onReleaseWait(t, h, c.f.releaseWait)
	unhookTries := onReleaseTry(t, h, c.f.releaseTried)
	c.unhook = func() {
		unhookReads()
		unhookWaits()
		unhookTries()
	}
}

// agentRunOnChatAs is agentRunOnChat for the run id hosted by chat's session, resident as first on the engine's first
// generation, which ends it with the generation.
func agentRunOnChatAs(
	t *testing.T, id string, chat marotte.ChatID, session string, first *fakeBridge, opts ...Option,
) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub(opts...)
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	first.sessionID = session
	e := fakeSourceOf(t, h).current()
	e.mu.Lock()
	e.sessions = append(e.sessions, first)
	e.mu.Unlock()
	h.bridge.mgr.insert(chat, &sharedBridge{bridge: first, state: bridgeIdle})
	h.runs.useLog(newRunLog(t.TempDir()))
	h.coord.runs = h.runs.log
	if _, err := cs.Mutate(t.Context(), chat, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession(session)
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	step := &translate.RunStep{RunID: id, NodePath: workflow.PathKey([]string{id, "review"}), SessionID: "step-session-a"}
	if _, _, err := h.runs.log.open(t.Context(), step, chat); err != nil {
		t.Fatalf("open the step: %v", err)
	}
	return h, br
}

var signedInAtBoot = `{"backends":{"test":{"data":{}}}}`

// signInNow and signOutNow move the gate from inside a transition's hook, off the test's goroutine.
func (c *holdCell) signInNow() {
	c.b.set(true, true, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.vault.StartSignIn(ctx, gateBackendID, signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
		c.t.Errorf("StartSignIn: %v", err)
		return
	}
	c.awaitGate(true)
}

func (c *holdCell) signOutNow() {
	if err := c.vault.SignOut(gateBackendID); err != nil {
		c.t.Errorf("SignOut: %v", err)
		return
	}
	c.awaitGate(false)
}

func (c *holdCell) awaitGate(open bool) {
	for deadline := time.Now().Add(5 * time.Second); c.h.runs.gateOpen() != open; {
		if time.Now().After(deadline) {
			c.t.Errorf("the gate never read open %t", open)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// signInStep signs in, as gateRun.signIn does, unless the gate closes again under the release the sign-in starts. A
// chat-parented cell orders the sign-in's reload mark against the gate's opening: with late, the gate opens and the
// release writes the run's resume while the mark is held; without, the gate reads the sign-in only after the mark.
func (c *holdCell) signInStep(t *testing.T, late bool, afterOpen func()) {
	t.Helper()
	if m := c.openGate(t, late, afterOpen); m != nil {
		c.landMark(t, m)
	}
}

// lateMark is a sign-in's reload mark held until a release writes the run's resume: the hold, and the marks and
// resume writes counted before the sign-in.
type lateMark struct {
	hold          *holdBlock
	marks, writes int32
}

// openGate is signInStep up to the gate's opening, which returns the mark it holds with late.
func (c *holdCell) openGate(t *testing.T, late bool, afterOpen func()) *lateMark {
	t.Helper()
	marks, writes := c.marks.Load(), c.k.resumeWrites.Load()
	// Only a crossing under this sign-in's release cuts its waits short.
	c.f.crossed.Store(false)
	var mark *holdBlock
	switch {
	case !c.c.chat():
	case late:
		mark = c.f.newHold()
		c.markHold.Store(mark)
		c.f.mu.Lock()
		c.f.markWait = func() {
			for deadline := time.Now().Add(5 * time.Second); c.marks.Load() <= marks && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
		}
		c.f.mu.Unlock()
	default:
		c.h.runs.gate.mu.Lock()
		at := c.h.runs.gate.state
		c.h.runs.gate.mu.Unlock()
		c.view.hold(at)
	}
	if _, err := c.vault.StartSignIn(t.Context(), gateBackendID, signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
		t.Fatalf("StartSignIn: %v", err)
	}
	waitUntil(t, "the sign-in to commit", func() bool {
		return c.f.crossed.Load() || c.vault.Answerability(gateBackendID) == signin.Answerable
	})
	if c.c.chat() && !late {
		waitUntil(t, "the sign-in's reload mark", func() bool { return c.marks.Load() > marks })
		c.view.let()
	}
	// The gate's own listener may have read the held view, or wait behind the held mark, so this read applies the edge.
	c.h.runs.gateOpen()
	waitUntil(t, "the gate to open", func() bool { return c.f.crossed.Load() || c.h.runs.gateOpen() })
	if afterOpen != nil {
		afterOpen()
	}
	if mark == nil {
		return nil
	}
	return &lateMark{hold: mark, marks: marks, writes: writes}
}

// landMark lets m's mark land once a release wrote the run's resume after its sign-in.
func (c *holdCell) landMark(t *testing.T, m *lateMark) {
	t.Helper()
	waitUntil(t, "the release's resume write before the reload mark", func() bool {
		return c.k.resumeWrites.Load() > m.writes
	})
	m.hold.release()
	waitUntil(t, "the sign-in's reload mark", func() bool { return c.marks.Load() > m.marks })
	c.f.mu.Lock()
	c.f.markWait = nil
	c.f.mu.Unlock()
}

// signOut closes the gate, as the gate reads it, so the next sign-in is an opening edge.
func (c *holdCell) signOut(t *testing.T) {
	t.Helper()
	if err := c.vault.SignOut(gateBackendID); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	waitUntil(t, "the gate to close", func() bool { return !c.h.runs.gateOpen() })
}

// lapse rejects the next renewal under the running run, so the gate closes and its sweep reads the run; a closed
// gate is opened by a sign-in first.
func (c *holdCell) lapse(t *testing.T) {
	t.Helper()
	if !c.h.runs.gateOpen() {
		c.signInStep(t, false, nil)
	}
	c.b.set(false, false, func() error { return signin.ErrRejected })
	if rerr := c.askVault(t.Context(), t); rerr == nil {
		t.Fatal("Setup: a rejected renewal was answered")
	}
	c.lapsed = true
}

// formHold carries out a gate pause's start: the lapse unless a step before closed the gate, the held read's retry,
// and the run's next credential ask when the step says so.
func (c *holdCell) formHold(t *testing.T, s *holdStep) {
	t.Helper()
	if !c.lapsed {
		c.lapse(t)
	}
	c.releaseBlock()
	if s.asks {
		runs := c.h.runs
		c.goAsync(func(ctx context.Context) { runs.pauseBeforeRefusal(ctx) })
	}
}

// releaseBlock lets the held read go on, and waits until it has, so a later wait does not count it as still held.
func (c *holdCell) releaseBlock() {
	if c.block == nil {
		return
	}
	held, went := c.f.heldReads.Load(), c.f.wentOn.Load()
	c.block.release()
	c.block = nil
	for deadline := time.Now().Add(5 * time.Second); held > 0 && c.f.wentOn.Load() <= went; {
		if time.Now().After(deadline) {
			c.t.Error("a held read never went on once let go")
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *holdCell) frame() {
	chat := c.chat
	if !c.c.chat() {
		chat = ""
	}
	heal := c.h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	heal(c.h.lifecycle.shutdownCtx, chat, runNotif(methodWFPaused, map[string]any{
		"workflowId": c.id, "initiator": initiatorUser, "initiatorReason": signInHoldReason,
	}))
}

// goAsync runs fn off the test's goroutine, as a read in it may be held by the cell, and on the runtime's lifetime,
// as production runs its sweeps, so the runtime's Shutdown waits for it and nothing it starts outlives that wait.
func (c *holdCell) goAsync(fn func(ctx context.Context)) {
	lt := c.h.lifecycle
	if !lt.goUnlessDraining(func() {
		ctx, cancel := lt.derivedContext()
		defer cancel()
		fn(ctx)
	}) {
		c.t.Error("a step's work started on a stopping runtime")
	}
}

func (c *holdCell) rehydrate() {
	runs := c.h.runs
	c.goAsync(func(ctx context.Context) { runs.resumeInterruptedRuns(ctx, c.chat) })
}

// engineRestart ends the engine, so the next one resumes the runs it interrupted (Runtime.resumeInterrupted).
func (c *holdCell) engineRestart(t *testing.T) {
	t.Helper()
	_, gen := c.h.engine.current()
	fakeSourceOf(t, c.h).current().crash()
	waitReady(t, c.h.engine, gen+1)
}

// armEntryList sets whether the next first list of a chat's rehydrate fails, as the step's failsEntry says.
func (c *holdCell) armEntryList(s *holdStep) {
	c.k.set(func(k *kasRun) { k.failEntryList = s.failsEntry })
}

// restart ends the runtime and boots a new one over the same KAS run, the gate open at boot for evRestartOpen, and
// checks what the restart kept, s.persisted.
func (c *holdCell) restart(t *testing.T, s *holdStep) {
	t.Helper()
	signedIn, persisted := s.ev == evRestartOpen, s.persisted
	c.unhook()
	c.k.detach()
	c.stop()
	c.f.mu.Lock()
	c.f.done = false
	c.f.mu.Unlock()
	if c.onBoot != nil {
		c.onBoot()
		c.onBoot = nil
	}
	c.boot(t, signedIn)
	if got := c.k.reads(); got != persisted {
		t.Errorf("a restart's new runtime finds the run %v, want %v", got, persisted)
	}
	if heldByTheGate(c.h.runs, c.id) {
		t.Error("a restart's new runtime holds the run's heal before its boot")
	}
	runs, chat, gate := c.h.runs, c.chat, c.c.gate()
	found := c.f.awaitFinder(gate && persisted == kasGateReason)
	swept := c.k.sweepLists.Load()
	if c.c.chat() {
		c.armEntryList(s)
	}
	spent := c.logs.errorCount(findSpentLog)
	c.goAsync(func(ctx context.Context) {
		runs.BootSweep(ctx)
		if c.c.chat() {
			// Only after the boot's finder read the run: a rehydrate writing first would leave the finder's list
			// reading it running, with no read for a lost reply's fault to hold.
			found()
			for deadline := time.Now().Add(5 * time.Second); s.finderSettles && c.logs.errorCount(findSpentLog) <= spent; {
				if time.Now().After(deadline) {
					c.t.Error("the boot's finder never spent its failing reads")
					break
				}
				time.Sleep(time.Millisecond)
			}
			runs.resumeInterruptedRuns(ctx, chat)
		}
	})
	if c.c.chat() && persisted == kasPausePending {
		// The rehydrate lists the run running and sweeps once more, which must list it before its pause lands.
		waitUntil(t, "the rehydrate's two sweeps", func() bool { return c.k.sweepLists.Load() >= swept+2 })
	}
	if gate && !signedIn && persisted == kasGateReason && (c.c.read == readNone || c.c.holdRead()) {
		c.awaitInfo(t, "a paused run is held until the agent's sign-in can be answered")
	}
}

func (c *holdCell) awaitInfo(t *testing.T, msg string) {
	t.Helper()
	waitUntil(t, msg, func() bool { return c.logs.infoSince(msg, c.stepInfos) })
}

// drive carries out one step's event.
func (c *holdCell) drive(t *testing.T, s *holdStep) {
	t.Helper()
	switch s.ev {
	case evHold:
		c.formHold(t, s)
		waitUntil(t, "the gate's pause", func() bool { return c.k.pauses.Load() == 1 })
		c.b.set(true, true, nil)
		c.frame()
		c.awaitInfo(t, "a paused run is held until the agent's sign-in can be answered")
	case evHoldRead, evHoldDead:
		c.lapse(t)
	case evHoldOpens:
		c.formHold(t, s)
		waitUntil(t, "the gate to open again", func() bool {
			c.f.mu.Lock()
			defer c.f.mu.Unlock()
			return c.f.cross == crossNone
		})
		c.awaitGate(true)
	case evHeal, evHealOpens, evHealAfter:
		if c.c.chat() {
			c.armEntryList(s)
			c.rehydrate()
			break
		}
		if s.failsEntry {
			c.k.set(func(k *kasRun) {
				k.runningState()
				k.staleOwner = true
			})
		}
		c.engineRestart(t)
	case evBudget:
		for range maxAutoHeals {
			c.h.runs.claimHeal(c.id)
		}
	case evCancel:
		c.k.set(func(k *kasRun) { k.status, k.pendingReason = marotte.RunStatusCancelled, "" })
	case evGone:
		c.k.vanish()
	case evDelete:
		c.k.vanish()
		c.h.runs.teardownDeleted(t.Context(), c.id)
	case evRestartClosed, evRestartOpen:
		c.restart(t, s)
	case evEngineEnd:
		pending, swept := c.c.chat() && c.k.reads() == kasPausePending, c.k.sweepLists.Load()
		reads := c.f.healReadCount()
		if c.c.chat() {
			// The next engine loads every session anew; the cell's first chat session has no forward to see the crash
			// end it, so it is marked for that reload, whose rehydrate sweeps its runs.
			c.h.markSessionsForReload("engine end")
			c.armEntryList(s)
		}
		c.engineRestart(t)
		if pending {
			// As at a restart: both sweeps list the run before its pause lands.
			waitUntil(t, "the rehydrate's two sweeps", func() bool { return c.k.sweepLists.Load() >= swept+2 })
		}
		if s.engineSweeps {
			c.awaitEngineSweep(t, swept, reads)
		}
	case evSignIn:
		c.signInStep(t, s.markLate, c.liveServes(s))
	case evSignInAgain:
		c.signOut(t)
		c.signInStep(t, s.markLate, c.liveServes(s))
	case evPendAcrossEdge:
		c.formHold(t, s)
		// The sweep's pause write has returned, so a finder's read from here on is the opening edge's or later.
		c.awaitInfo(t, "sign-in gate: paused a running run until the agent's sign-in can be answered")
		c.f.mu.Lock()
		c.f.finds = 0
		c.f.mu.Unlock()
		c.b.set(true, true, nil)
		c.heldMark = c.openGate(t, s.holdsMark, nil)
	case evLandFrame:
		c.f.mu.Lock()
		finds := c.f.finds
		c.f.mu.Unlock()
		if c.prevEv == evPendAcrossEdge && finds != 0 {
			t.Errorf("the run with its pause pending was read %d times by a finder after the opening edge, want 0", finds)
		}
		c.k.set(func(k *kasRun) {
			k.landLater = false
			k.landLocked()
		})
		c.frame()
		if s.markLate {
			c.landMark(t, c.heldMark)
			c.heldMark = nil
		}
	case evRetry:
		c.releaseBlock()
	case evRehydrate:
		c.rehydrate()
	}
}

// awaitEngineSweep waits for the next engine's sweep of the run: a chat's rehydrate's list from lists' count, and the
// sweep's read of the run from reads' count when it reads one, or a read the hook holds ahead of it, then for its
// transition to end.
func (c *holdCell) awaitEngineSweep(t *testing.T, lists int32, reads int) {
	t.Helper()
	if c.c.chat() {
		waitUntil(t, "the chat's rehydrate's list", func() bool { return c.k.sweepLists.Load() > lists })
		var paused bool
		c.k.set(func(k *kasRun) { paused = k.sweepListedPaused })
		if !paused {
			return
		}
	}
	waitUntil(t, "the next engine's read of the run", func() bool {
		return c.f.healReadCount() > reads || c.f.heldReads.Load() > 0
	})
	c.awaitTransitionEnd(t)
}

// awaitTransitionEnd waits for the run's resume lock to be free; a read the hook holds keeps that lock past the
// transition the step waits for, which then ended or waits behind it.
func (c *holdCell) awaitTransitionEnd(t *testing.T) {
	t.Helper()
	waitUntil(t, "the run's transition to end", func() bool {
		return c.f.heldReads.Load() > 0 || resumeLockFree(c.h.runs, c.id)
	})
}

// awaitChainTry waits for the cell's sweep chain's later try: a chat's list from lists' count, and the try's read of
// the run from later's count when it reads one, or a read the hook holds ahead of it, then for the try's transition
// to end.
func (c *holdCell) awaitChainTry(t *testing.T, later [2]int, lists int32) {
	t.Helper()
	chain := retryParentless
	if c.c.chat() {
		chain = retryChat
		waitUntil(t, "the chat's sweep chain's later list", func() bool { return c.k.chainLists.Load() > lists })
		var paused bool
		c.k.set(func(k *kasRun) { paused = k.chainListedPaused })
		if !paused {
			return
		}
	}
	waitUntil(t, "the sweep chain's later try", func() bool {
		c.f.mu.Lock()
		defer c.f.mu.Unlock()
		return c.f.laterTries[chain] > later[chain] || c.f.heldReads.Load() > 0
	})
	c.awaitTransitionEnd(t)
}

// armFlap loses the step's held release's next resume write (crossFlapsAtRetry).
func (c *holdCell) armFlap() {
	c.f.mu.Lock()
	c.f.gapReached = make(chan struct{})
	c.f.mu.Unlock()
	c.k.set(func(k *kasRun) { k.flapWrite = true })
}

// flap closes and opens the gate while the held release waits to retry its lost write, once the step's other calls
// settled. With s.flapPauses a rehydrate resumes the run in that wait first, and the gate opens again only once the
// closing edge's sweep paused it.
func (c *holdCell) flap(t *testing.T, s *holdStep) {
	t.Helper()
	c.f.mu.Lock()
	reached := c.f.gapReached
	c.f.mu.Unlock()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the held release never waited to retry its lost resume write")
	}
	if s.flapPauses {
		awaitResumes(t, &c.k.resumes, s.resumes-1)
	}
	c.signOut(t)
	if s.flapPauses {
		waitUntil(t, "the closing edge's pause of the resumed run", func() bool { return c.k.reads() == kasGateReason })
	}
	c.signInStep(t, false, nil)
}

// inFlightHolds are an in-flight step's holds: the release's resume write, the try that wrote it (crossRepausedLive),
// and with orderSweepFirst the rehydrate's run list; and the chat's sweep lists before the step.
type inFlightHolds struct {
	write, try, rehydrate *holdBlock
	sweeps                int32
}

// armInFlight holds the step's held release's next resume write in flight, with crossRepausedLive the try that wrote
// it once it ended, and with orderSweepFirst the run list of the rehydrate that write's reload starts.
func (c *holdCell) armInFlight(s *holdStep) *inFlightHolds {
	h := &inFlightHolds{write: c.f.newHold(), sweeps: c.k.sweepLists.Load()}
	c.k.set(func(k *kasRun) { k.writeHold = h.write })
	if s.inFlight == crossRepausedLive {
		h.try = c.f.newHold()
		c.f.mu.Lock()
		c.f.tryHold = h.try
		c.f.mu.Unlock()
	}
	if s.rehydrates && c.c.order == orderSweepFirst {
		h.rehydrate = c.f.newHold()
		c.k.holdList("resumeChainRuns", h.rehydrate)
	}
	return h
}

// inFlight closes the gate while the held release's resume write is in flight, then opens it again before the write
// lands (crossFlapsInFlight), or lets the write land at the closed gate and waits for the closing sweep's pause of the
// run, the release ended or held live for the next step's edge. A chat's rehydrate reads the run as the cell's order
// says: before that pause, its list taken while the write was in flight and the sweep's list held until its read, or
// after it, its list held until the pause.
func (c *holdCell) inFlight(t *testing.T, s *holdStep, h *inFlightHolds) {
	t.Helper()
	select {
	case <-h.write.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the held release never wrote the run's resume")
	}
	closing := c.k.closingLists.Load()
	c.signOut(t)
	if s.inFlight == crossFlapsInFlight {
		c.signInStep(t, false, nil)
		h.write.release()
		return
	}
	var sweep *holdBlock
	if s.rehydrates && h.rehydrate == nil {
		waitUntil(t, "the rehydrate's list while the write is in flight", func() bool {
			return c.k.sweepLists.Load() > h.sweeps
		})
		waitUntil(t, "the closing edge's sweep's list", func() bool { return c.k.closingLists.Load() > closing })
		read := make(chan struct{})
		c.f.mu.Lock()
		c.f.rehydrateRead = read
		c.f.mu.Unlock()
		sweep = c.f.newHold()
		c.k.holdList("sweepPause", sweep)
		defer func() {
			select {
			case <-read:
			case <-time.After(5 * time.Second):
				t.Error("the chat's rehydrate never read the run once the write landed")
			}
			sweep.release()
			c.awaitPaused(t)
		}()
	}
	h.write.release()
	if h.try != nil {
		select {
		case <-h.try.reached:
		case <-time.After(5 * time.Second):
			t.Fatal("the release's try never ended after its resume write")
		}
		c.liveTry = h.try
	} else {
		waitUntil(t, "the release to end", func() bool {
			c.h.runs.gate.mu.Lock()
			defer c.h.runs.gate.mu.Unlock()
			return !c.h.runs.gate.releases[c.id].live
		})
	}
	if sweep != nil {
		return
	}
	c.awaitPaused(t)
	if h.rehydrate != nil {
		h.rehydrate.release()
		waitUntil(t, "the rehydrate to hold the paused run's heal", func() bool { return heldByTheGate(c.h.runs, c.id) })
	}
}

func (c *holdCell) awaitPaused(t *testing.T) {
	t.Helper()
	waitUntil(t, "the closing sweep's pause of the resumed run", func() bool { return c.k.reads() == kasGateReason })
}

// liveServes is the step's wait after its opening edge when s.servesLive: letLive's.
func (c *holdCell) liveServes(s *holdStep) func() {
	if !s.servesLive {
		return nil
	}
	return c.letLive(c.f.awaitFinder(true))
}

// letLive lets the release held live after its try serve this step's opening edge, once the edge's finder read the
// run, whose claim the live release refuses.
func (c *holdCell) letLive(found func()) func() {
	try := c.liveTry
	c.liveTry = nil
	return func() {
		found()
		try.release()
	}
}

// arm starts the cell's release faults and its release's crossing.
func (c *holdCell) arm() {
	c.f.mu.Lock()
	if !c.c.holdRead() {
		c.f.read = c.c.read
	}
	if c.c.cross != crossOpensBeforeHold {
		c.f.cross = c.c.cross
	}
	c.f.mu.Unlock()
	c.k.armWrite(c.c.write)
}

// stepQuiet is how long the run's KAS must go uncalled before the next step: a step's checks pass once its end state
// is reached, while a reader the step started (an opening edge's finder, a release) may still be on its way, and one
// landing in the next step would act on that step's gate.
const stepQuiet = 50 * time.Millisecond

// faultQuiet is how long a fault must go unhit, once the step's end is reached, for its readers or writers to have
// settled: longer than any of their retries' gaps under fastGateRetries and the test's orphanResumeDelay.
const faultQuiet = 150 * time.Millisecond

// settle waits for the step's ERRORs, each reporting its reader's whole tries, for its faults to act, and for the
// step's end state, then until the faults go unhit, checks each spent retryResumeLater chain made its tries, and ends
// the faults. hits is the count of faults hit before the step, errs the count of each of its ERRORs, later the
// chains' later tries.
func (c *holdCell) settle(t *testing.T, s *holdStep, hits int, errs map[string]int, later [2]int) {
	t.Helper()
	for _, msg := range s.loud {
		// Each listing is one reader's ERROR: two chat sweep chains log it twice.
		n := errs[msg] + countOf(s.loud, msg)
		waitUntil(t, "the ERROR "+msg, func() bool { return c.logs.errorCount(msg) >= n })
		if lacking := c.logs.errorsLacking(msg, spentCounts[msg]); len(lacking) > 0 {
			t.Errorf("the ERROR %q reports %v, want %s", msg, lacking, spentCounts[msg])
		}
	}
	// The fault ends only at the step's end, which wantHold then reports if it never comes.
	for deadline := time.Now().Add(5 * time.Second); c.holdNow() != s.want && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	waitUntil(t, "the failing calls to settle", func() bool {
		c.f.mu.Lock()
		defer c.f.mu.Unlock()
		return c.f.hits > hits && time.Since(c.f.lastHit) > faultQuiet
	})
	c.f.mu.Lock()
	tries := c.f.laterTries
	c.f.mu.Unlock()
	for chain, msg := range retrySpentLogs {
		if !slices.Contains(s.loud, msg) {
			continue
		}
		// Each spent chain tries maxResweeps times in all: its caller's first try, then the later ones counted here.
		spent := c.logs.errorCount(msg) - errs[msg]
		if got, want := tries[chain]-later[chain], spent*(maxResweeps-1); got != want {
			t.Errorf("spent chains: %d logged %q after %d later tries, want %d", spent, msg, got, want)
		}
	}
	if s.settleRead {
		c.f.mu.Lock()
		c.f.read = readNone
		c.f.mu.Unlock()
	}
	if s.settleWrite {
		c.k.armWrite(writeNone)
	}
}

func countOf(list []string, v string) int {
	n := 0
	for _, x := range list {
		if x == v {
			n++
		}
	}
	return n
}

func (c *holdCell) holdNow() holdState {
	return holdState{kas: c.k.reads(), heal: heldByTheGate(c.h.runs, c.id)}
}

// wantHold waits for the run to reach want, failing the cell if it does not.
func (c *holdCell) wantHold(t *testing.T, at string, want holdState) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); c.holdNow() != want && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if g := c.holdNow(); g != want {
		t.Errorf("%s: the run is %v, want %v", at, g, want)
	}
}

// orderFinder makes the step's heal and orphan sweep read the run only after the finder listed it, or ends that.
func (c *holdCell) orderFinder(first bool) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.finderFirst, c.f.finderBase = first, c.k.finderLists.Load()
}

// run drives the cell's steps, checking the state after each.
func (c *holdCell) run(t *testing.T, steps []holdStep) {
	t.Helper()
	var at string
	defer func() {
		if t.Failed() {
			t.Logf("%s failed; the cell's log:\n%s", at, c.logs)
		}
	}()
	for i := range steps {
		s := &steps[i]
		at = fmt.Sprintf("step %d (%s)", i+1, s.text)
		waitUntil(t, "the previous step's calls to settle", func() bool {
			return time.Since(time.Unix(0, c.k.lastCall.Load())) > stepQuiet
		})
		c.f.mu.Lock()
		hits, later := c.f.hits, c.f.laterTries
		c.f.mu.Unlock()
		lists := c.k.chainLists.Load()
		errs := map[string]int{}
		for _, msg := range s.loud {
			errs[msg] = c.logs.errorCount(msg)
		}
		c.stepInfos = c.logs.infoCount()
		var rehydrateList *holdBlock
		if s.finderSettles {
			spent := c.logs.errorCount(findSpentLog)
			settled := func() {
				for deadline := time.Now().Add(5 * time.Second); c.logs.errorCount(findSpentLog) <= spent &&
					time.Now().Before(deadline); {
					time.Sleep(time.Millisecond)
				}
			}
			c.f.mu.Lock()
			c.f.finderWait = settled
			c.f.mu.Unlock()
			if c.c.chat() && s.inFlight == crossNone {
				// A chat's reload rehydrate, which a release's write may start, lists only then too; an in-flight
				// step orders that rehydrate itself (inFlight).
				rehydrateList = c.f.newHold()
				c.k.holdList("resumeChainRuns", rehydrateList)
				go func() {
					settled()
					rehydrateList.release()
				}()
			}
		}
		if s.finderFirst {
			c.orderFinder(true)
		}
		if s.flaps {
			c.armFlap()
		}
		var inFlight *inFlightHolds
		if s.inFlight != crossNone {
			// A restart's stop lets go of every hold, so a restart step's are armed for its new runtime.
			arm := func() { inFlight = c.armInFlight(s) }
			if s.ev == evRestartClosed || s.ev == evRestartOpen {
				c.onBoot = arm
			} else {
				arm()
			}
		}
		c.drive(t, s)
		c.prevEv = s.ev
		if s.blocked {
			select {
			case c.block = <-c.f.blocks:
				<-c.block.reached
			case <-time.After(5 * time.Second):
				t.Fatalf("step %d (%s): no read was held", i+1, s.text)
			}
		}
		if s.chainTry {
			c.awaitChainTry(t, later, lists)
		}
		if s.flaps {
			c.flap(t, s)
		}
		if inFlight != nil {
			c.inFlight(t, s, inFlight)
		}
		if s.crosses {
			waitUntil(t, "the gate to close again under the release", c.f.crossed.Load)
		}
		if s.settleRead || s.settleWrite || len(s.loud) > 0 {
			c.settle(t, s, hits, errs, later)
		}
		c.wantHold(t, fmt.Sprintf("step %d (%s)", i+1, s.text), s.want)
		awaitResumes(t, &c.k.resumes, s.resumes)
		if t.Failed() {
			t.FailNow()
		}
		if s.finderFirst {
			c.orderFinder(false)
		}
		c.f.mu.Lock()
		c.f.finderWait = nil
		c.f.mu.Unlock()
		if rehydrateList != nil && c.k.takeListHold("resumeChainRuns") != nil {
			rehydrateList.release()
		}
		if !s.blocked {
			// A try the model holds no read of may still read later, which a fault left armed would hold.
			c.f.mu.Lock()
			c.f.blockNext = false
			c.f.mu.Unlock()
		}
		if s.arm {
			c.arm()
		}
	}
}

// Each door of a cell's chat opens a session handle of its own over the cell's one KAS run, as the engine mints one
// per door: the closed generation's late exit then finds its replacement registered and leaves it, where one handle
// shared by every door would remove the replacement (removeIfBridge) and open a further generation.
func TestGateHold_EachDoorOpensItsOwnHandleOverTheCellsRun(t *testing.T) {
	c := newHoldCell(t, &holdLogs{sinks: &sync.Map{}}, holdCombo{kind: kindChat, form: formGate})
	reopen := func() *sharedBridge {
		t.Helper()
		c.h.markSessionsForReload("a test's reload")
		sb, err := c.h.coord.openBridge(t.Context(), c.chat, "")
		if err != nil {
			t.Fatalf("Setup: reopen the chat: %v", err)
		}
		return sb
	}
	// Both generations open through the cell's doors, so each holds its session as forwardAt is handed it.
	first := reopen().current()
	sb := reopen()
	second := sb.current()
	if second == first {
		t.Error("the reload's door handed out the closed generation's session handle")
	}

	if c.h.bridge.mgr.removeIfBridge(c.chat, first) {
		t.Error("the closed generation's late exit removed its replacement")
	}
	if got := c.h.bridge.mgr.get(c.chat); got != sb {
		t.Errorf("the chat's registered session is %p after the late exit, want the replacement %p", got, sb)
	}
	resp, err := second.Call(t.Context(), methodKiroWorkflowInspect, map[string]any{keyWorkflowID: c.id})
	if err != nil || resp.Error != nil || !strings.Contains(string(resp.Result), c.id) {
		t.Errorf("the replacement's inspect = %v, %v; want the cell's run %s from its one KAS state", resp, err, c.id)
	}
}

// holdCellsAtOnce bounds the cells running side by side.
const holdCellsAtOnce = 48

// Every cell of the hold matrix, each through the real entry points, held, persisted and resumed as holdModel says.
// The cells run side by side, each on its own runtime, its logs routed by its run's, chat's and session's names;
// serial with every other test, since it swaps the global logger.
func TestGateHold_EveryCell(t *testing.T) {
	fastGateRetries(t)
	prevDelay := orphanResumeDelay
	orphanResumeDelay = 20 * time.Millisecond
	t.Cleanup(func() { orphanResumeDelay = prevDelay })
	router := &holdLogs{sinks: &sync.Map{}}
	capture.SwapDefault(t, slog.New(router))
	byRow := map[string][]holdCombo{}
	var rows []string
	for _, c := range everyCombo() {
		if _, why := holdModel(c); why != "" {
			continue
		}
		if _, ok := byRow[c.rowName()]; !ok {
			rows = append(rows, c.rowName())
		}
		byRow[c.rowName()] = append(byRow[c.rowName()], c)
	}
	sem := make(chan struct{}, holdCellsAtOnce)
	for _, row := range rows {
		t.Run(row, func(t *testing.T) {
			var wg sync.WaitGroup
			for _, c := range byRow[row] {
				sem <- struct{}{}
				wg.Go(func() {
					defer func() { <-sem }()
					t.Run(c.name(), func(t *testing.T) {
						steps, _ := holdModel(c)
						newHoldCell(t, router, c).run(t, steps)
					})
				})
			}
			wg.Wait()
		})
	}
}
