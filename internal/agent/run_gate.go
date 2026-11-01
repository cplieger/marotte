package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/keylock"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// KAS fails a step permanently on a refused credential ask unless a pause of its run was requested first, so run
// work dispatched while the vault cannot answer turns a state the user can fix into a terminal failure.
var (
	errRunsNeedSignIn = command.StatusErrorReason(http.StatusConflict, "sign_in_required",
		errors.New("sign in to Kiro first: runs wait until marotte can answer the agent's sign-in"))
	errRunsProviderDown = command.StatusErrorReason(http.StatusServiceUnavailable, "identity_provider_unreachable",
		errors.New("the Kiro sign-in service is unreachable: runs wait until it answers"))
)

// The reasons marotte gives KAS when it pauses a running run while the gate is closed. KAS attributes every
// reasoned pause to the user and keeps the reason as the run's stopReason, which is the gate pause's hold.
const (
	signInHoldReason   = "sign in to Kiro to continue; marotte resumes the run when you do"
	providerHoldReason = "the Kiro sign-in service is unreachable; marotte resumes the run when it answers"
	// manualResumeReason replaces a pause's promise of an automatic resume once marotte declines one
	// (parkForUser); it licenses none.
	manualResumeReason = "resume it to continue"
)

func isHoldReason(reason string) bool {
	return reason == signInHoldReason || reason == providerHoldReason
}

// sweepRetryBase is the first gap between the closing edge's sweeps (reconcileClosed, pauseBeforeRefusal),
// releaseRetryBase between resumeHeld's and findHolds' tries; each gap doubles. Vars for tests.
var (
	sweepRetryBase   = time.Second
	releaseRetryBase = 2 * time.Second
	// sweepRetryCap bounds the gap between a held refusal's sweeps, which never stop, and between findHolds' tries.
	sweepRetryCap = 30 * time.Second
)

const (
	maxSweepAttempts   = 6
	maxReleaseAttempts = 5
)

// runGate holds run work while the engine's sign-in cannot be answered. Every admission re-reads the vault, but the
// vault can close while admitted work is still being written: each closing edge starts an epoch, and work that
// returns in a later epoch than its admission's sends the sweep again (runAdmission.settle). The zero value is open.
//
// Every hold, by the matrix TestGateHold_EveryCell/<row>/<cell> runs in full, rendered from its reference model
// (holdModel), which skips a combination only with the reason it cannot happen, or the cell it drives the same as and
// why. Rows: a chat-parented, manual or scheduled run's gate pause, which KAS holds as the run's stopReason, or its
// heal, which heldHeals holds; a gate pause's heal is held there too when the gate closes again before its release
// admits it. Cells: where the hold lands; what happens while it is held; a restart at its checkpoint, which keeps KAS's
// state and loses heldHeals and the heal budget, or the engine's replacement there, which keeps them; the gate crossing
// the hold or its release; a fault in the reads that form the hold or release it; a fault in the release's writes, a
// resume or, once the heal budget is spent, the park for the user; for a chat-parented run, the orders production
// leaves open between a sign-in's reload mark and the gate's opening, and between a reload's rehydrate and a closing
// sweep; whether the runtime boots signed in, so no reload mark is pending as a chat's gate pause forms. Each item is
// the outcome of the cell that differs from the plain one in one value, or in the late mark and a signed-in boot: the
// hold at the checkpoint, before which nothing resumes the run, what a fresh runtime finds there, and the end.
// holdCellText gives every cell's steps. A person's pause or resume of a held run is runTransitions'.
//
//   - The plain cell (lands before the edge; held untouched; no restart; no crossing; reads answer; writes answer; the
//     chat's reload mark and rehydrate come first). A chat-parented run's gate pause, a manual run's gate pause, a
//     scheduled run's gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends
//     running after 1 resume. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for
//     the restart and its heal held, persisted paused for the restart, ends running after 1 resume.
//   - Lands after the edge. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate pause:
//     held running with the gate's pause pending, persisted running with the gate's pause pending, ends running after 1
//     resume. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for the restart,
//     persisted paused for the restart, ends running after 1 resume.
//   - Budget spent while held. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate
//     pause: held paused with the gate's reason, persisted paused with the gate's reason, ends paused for a person to
//     resume after 0 resumes. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for
//     the restart and its heal held, persisted paused for the restart, ends paused for a person to resume after 0
//     resumes.
//   - Cancelled while held. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate pause:
//     held cancelled, persisted cancelled, ends cancelled after 0 resumes. A chat-parented run's heal, a manual run's
//     heal, a scheduled run's heal: held cancelled and its heal held, persisted cancelled, ends cancelled after 0
//     resumes.
//   - Gone while held. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate pause: held
//     gone, persisted gone, ends gone after 0 resumes. A chat-parented run's heal, a manual run's heal, a scheduled
//     run's heal: held gone and its heal held, persisted gone, ends gone after 0 resumes.
//   - Deleted while held. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate pause:
//     held gone, persisted gone, ends gone after 0 resumes. A chat-parented run's heal, a manual run's heal, a
//     scheduled run's heal: held gone and its heal held, persisted gone, ends gone after 0 resumes.
//   - A restart, the gate closed. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate
//     pause: held paused with the gate's reason, a restart finds it paused with the gate's reason with no heal held,
//     ends running after 1 resume. A chat-parented run's heal: held paused for the restart and its heal held, a restart
//     finds it paused for the restart with no heal held, ends running after 1 resume. A manual run's heal, a scheduled
//     run's heal: held paused for the restart and its heal held, a restart finds it paused for the restart with no heal
//     held, ends cancelled after 0 resumes.
//   - A restart, the gate open. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate
//     pause: held paused with the gate's reason, a restart finds it paused with the gate's reason with no heal held,
//     ends running after 1 resume. A chat-parented run's heal: held paused for the restart and its heal held, a restart
//     finds it paused for the restart with no heal held, ends running after 1 resume. A manual run's heal, a scheduled
//     run's heal: held paused for the restart and its heal held, a restart finds it paused for the restart with no heal
//     held, ends cancelled after 0 resumes.
//   - The engine replaced at the checkpoint. A chat-parented run's gate pause, a manual run's gate pause, a scheduled
//     run's gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain cell.
//   - The gate opens before the hold forms. A chat-parented run's gate pause, a manual run's gate pause, a scheduled
//     run's gate pause: held running, persisted none, ends running after 0 resumes. A chat-parented run's heal, a
//     manual run's heal, a scheduled run's heal: held running, persisted none, ends running after 1 resume.
//   - The gate closes again at the release's first read of the hold. A chat-parented run's gate pause, a manual run's
//     gate pause, a scheduled run's gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's
//     heal: as in the plain cell.
//   - The gate closes again before the release's heal admits. A chat-parented run's gate pause, a manual run's gate
//     pause, a scheduled run's gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as
//     in the plain cell.
//   - The release's first resume write is lost, and the gate closes and opens again while it waits to retry. A
//     chat-parented run's gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends
//     running after 2 resumes. A manual run's gate pause, a scheduled run's gate pause, a manual run's heal, a
//     scheduled run's heal: as in the plain cell. A chat-parented run's heal: held paused for the restart and its heal
//     held, persisted paused for the restart, ends running after 2 resumes.
//   - The gate closes and opens again while the release's first resume write is in flight. A chat-parented run's gate
//     pause, a manual run's gate pause, a scheduled run's gate pause, a chat-parented run's heal, a manual run's heal,
//     a scheduled run's heal: as in the plain cell.
//   - The gate closes while the release's first resume write is in flight, and opens again once the release ended and
//     the closing sweep paused the run again. A chat-parented run's gate pause, a manual run's gate pause, a scheduled
//     run's gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends running after
//     2 resumes. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for the restart
//     and its heal held, persisted paused for the restart, ends running after 2 resumes.
//   - The gate closes while the release's first resume write is in flight, and opens again once the closing sweep
//     paused the run again, the release still live. A chat-parented run's gate pause, a manual run's gate pause, a
//     scheduled run's gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends
//     running after 2 resumes. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for
//     the restart and its heal held, persisted paused for the restart, ends running after 2 resumes.
//   - The hold's first read fails once. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain cell.
//   - The hold's reads keep failing. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends running after 1
//     resume, with an ERROR. A chat-parented run's heal: held paused for the restart, persisted paused for the restart,
//     ends running after 1 resume, with an ERROR. A manual run's heal, a scheduled run's heal: held paused for the
//     restart, persisted paused for the restart, ends paused for the restart after 0 resumes, with an ERROR.
//   - The heal entry's own read fails once. A chat-parented run's gate pause, a manual run's gate pause, a scheduled
//     run's gate pause: cannot happen, a gate pause has no heal entry: it forms in the closing sweep, whose read the
//     hold's read values fail. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain
//     cell.
//   - The release's first read fails once. A chat-parented run's gate pause, a manual run's gate pause, a scheduled
//     run's gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain cell.
//   - The heal's first read fails once. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain cell.
//   - The finder's reads keep failing. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends running after 1
//     resume, with an ERROR. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for
//     the restart and its heal held, persisted paused for the restart, ends running after 1 resume, with an ERROR.
//   - The heal's reads keep failing. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause: held paused with the gate's reason, persisted paused with the gate's reason, ends running after 1
//     resume, with an ERROR. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for
//     the restart and its heal held, persisted paused for the restart, ends running after 1 resume, with an ERROR.
//   - Every read keeps failing. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's gate
//     pause: held paused with the gate's reason, persisted paused with the gate's reason, ends running after 1 resume,
//     with an ERROR. A chat-parented run's heal, a manual run's heal, a scheduled run's heal: held paused for the
//     restart and its heal held, persisted paused for the restart, ends running after 1 resume, with an ERROR.
//   - The release's write is lost once. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain cell.
//   - Every release write is lost. A chat-parented run's gate pause: held paused with the gate's reason, persisted
//     paused with the gate's reason, ends paused for a person to resume after 0 resumes, with an ERROR. A manual run's
//     gate pause, a scheduled run's gate pause: held paused with the gate's reason, persisted paused with the gate's
//     reason, ends running after 1 resume, with an ERROR. A chat-parented run's heal: held paused for the restart and
//     its heal held, persisted paused for the restart, ends paused for a person to resume after 0 resumes, with an
//     ERROR. A manual run's heal, a scheduled run's heal: held paused for the restart and its heal held, persisted
//     paused for the restart, ends running after 1 resume, with an ERROR.
//   - The release write's reply is lost. A chat-parented run's gate pause, a manual run's gate pause, a scheduled run's
//     gate pause, a chat-parented run's heal, a manual run's heal, a scheduled run's heal: as in the plain cell.
//   - The gate opens and the release writes before the sign-in marks the chat for its reload. A chat-parented run's
//     gate pause: cannot happen, no sign-in opens the gate onto a held release's resume write with its own reload mark
//     the only one pending: an earlier sign-in's mark still waits on the chat's session for the run's first resume
//     write (with a signed-out boot, the sign-in before the lapse, whose mark the run's open step turn keeps from an
//     idle open (claimReload), or an earlier opening edge whose release wrote no resume), or no held release writes a
//     resume in the edge's round, nor in that of the pending pause's frame. A manual run's gate pause, a scheduled
//     run's gate pause, a manual run's heal, a scheduled run's heal: cannot happen, only a chat's session is marked for
//     its reload at a sign-in (markSessionsForReload), and a reload's rehydrate sweeps only a chat's runs; a manual or
//     scheduled run's host session is never marked. A chat-parented run's heal: as in the plain cell.
//   - The closing sweep pauses the resumed run before the chat's rehydrate reads it. A chat-parented run's gate pause,
//     a chat-parented run's heal: cannot happen, no closing sweep pauses the run again while a chat's rehydrate waits
//     to read it: only a held release's resume write that reloads the chat's session and lands at the closed gate
//     starts both. A manual run's gate pause, a scheduled run's gate pause, a manual run's heal, a scheduled run's
//     heal: cannot happen, only a chat's session is marked for its reload at a sign-in (markSessionsForReload), and a
//     reload's rehydrate sweeps only a chat's runs; a manual or scheduled run's host session is never marked.
//   - The runtime boots signed in, so no sign-in marks the chat's session for its reload before the gate closes. A
//     chat-parented run's gate pause: as in the plain cell. A manual run's gate pause, a scheduled run's gate pause, a
//     manual run's heal, a scheduled run's heal: as with a signed-out boot, since only a chat's session is marked for
//     its reload at a sign-in (markSessionsForReload), and the sign-in a signed-out boot needs before a gate pause's
//     lapse opens the gate onto a running run, which no finder reads (pausedRuns). A chat-parented run's heal: as with
//     a signed-out boot, since a heal's hold forms at a gate closed before any sign-in, and a boot that loads a sign-in
//     marks no session (Runtime.onSignInChange marks on an edge), so no reload mark is pending at its first opening
//     edge either way.
//   - The gate opens and the release writes before the sign-in marks the chat for its reload, the runtime booted signed
//     in. A chat-parented run's gate pause: as in the plain cell. A manual run's gate pause, a scheduled run's gate
//     pause, a manual run's heal, a scheduled run's heal: cannot happen, only a chat's session is marked for its reload
//     at a sign-in (markSessionsForReload), and a reload's rehydrate sweeps only a chat's runs; a manual or scheduled
//     run's host session is never marked. A chat-parented run's heal: as with a signed-out boot, since a heal's hold
//     forms at a gate closed before any sign-in, and a boot that loads a sign-in marks no session
//     (Runtime.onSignInChange marks on an edge), so no reload mark is pending at its first opening edge either way.
type runGate struct {
	// read is the vault's Answerability for the engine's backend; nil keeps the gate open, since then no vault
	// answers the engine (none is wired, or an API key authenticates it).
	read func() signin.Answerability
	// releases is each held run's latest release (resumeHeld): at most one live per run, which serves every opening
	// edge that comes while it runs, and one per edge, so every try for the run's hold shares one heal of its budget.
	releases map[string]releaseOwner
	// heldHeals are the automatic resumes the closed gate held, of any run kind, by launching chat ("" when unknown,
	// as for a gate pause whose resume the gate closed again before). Only admitOrHold adds a hold and only
	// Runs.transition drops one (dropHold), both under the run's resume lock.
	heldHeals map[string]marotte.ChatID
	// moved closes at the next change of state; nil until moves asks for it.
	moved chan struct{}
	state signin.Answerability
	// epoch counts closing edges: a driving write admitted in an earlier epoch crossed one.
	epoch uint64
	// opens counts opening edges.
	opens uint64
	mu    sync.Mutex
}

// wire installs read and takes its first reading; before any run work.
func (g *runGate) wire(read func() signin.Answerability) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.read = read
	if read != nil {
		g.state = read()
	}
}

func (g *runGate) openLocked() bool { return g.read == nil || g.state == signin.Answerable }

// gateMove is one reading of the gate: from and to its states, the opening edges counted by then, and on an opening
// edge a copy of heldHeals.
type gateMove struct {
	released map[string]marotte.ChatID
	from     signin.Answerability
	to       signin.Answerability
	opening  uint64
}

func (m gateMove) opened() bool {
	return m.from != signin.Answerable && m.to == signin.Answerable
}

func (m gateMove) closed() bool {
	return m.from == signin.Answerable && m.to != signin.Answerable
}

func (g *runGate) refreshLocked() gateMove {
	m := gateMove{from: g.state, to: g.state, opening: g.opens}
	if g.read == nil {
		return m
	}
	m.to = g.read()
	g.state = m.to
	if m.to != m.from && g.moved != nil {
		close(g.moved)
		g.moved = nil
	}
	switch {
	case m.closed():
		g.epoch++
	case m.opened():
		g.opens++
		m.opening = g.opens
		m.released = maps.Clone(g.heldHeals)
		// An ended release refuses no claim of this edge or a later one.
		maps.DeleteFunc(g.releases, func(_ string, o releaseOwner) bool { return !o.live })
	}
	return m
}

func (g *runGate) refresh() gateMove {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refreshLocked()
}

// moves is a channel closed at the gate's next change of state, as a read of the vault finds it.
func (g *runGate) moves() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.moved == nil {
		g.moved = make(chan struct{})
	}
	return g.moved
}

// now reads the gate: whether run work may proceed.
func (g *runGate) now() (open bool, m gateMove) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m = g.refreshLocked()
	return g.openLocked(), m
}

// admit admits one driving write in the current epoch, or refuses it while the gate is closed.
func (g *runGate) admit() (epoch uint64, m gateMove, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m = g.refreshLocked()
	if !g.openLocked() {
		return 0, m, g.refusalLocked()
	}
	return g.epoch, m, nil
}

// crossed reports whether a closing edge passed since epoch's admission.
func (g *runGate) crossed(epoch uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.epoch != epoch
}

// admitOrHold is admit for one automatic resume, adding workflowID to heldHeals instead of refusing it. An unknown
// chat keeps the one an earlier hold recorded.
func (g *runGate) admitOrHold(workflowID string, chatID marotte.ChatID) (epoch uint64, held bool, m gateMove) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m = g.refreshLocked()
	if g.openLocked() {
		return g.epoch, false, m
	}
	if g.heldHeals == nil {
		g.heldHeals = make(map[string]marotte.ChatID)
	}
	if chatID == "" {
		chatID = g.heldHeals[workflowID]
	}
	g.heldHeals[workflowID] = chatID
	return g.epoch, true, m
}

// dropHold drops workflowID's hold.
func (g *runGate) dropHold(workflowID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.heldHeals, workflowID)
}

func (g *runGate) refusalLocked() error {
	switch {
	case g.openLocked():
		return nil
	case g.state == signin.ProviderUnreachable:
		return errRunsProviderDown
	default:
		return errRunsNeedSignIn
	}
}

func holdReasonFor(a signin.Answerability) string {
	if a == signin.ProviderUnreachable {
		return providerHoldReason
	}
	return signInHoldReason
}

// gateSweep pauses the running runs while the gate is closed. kick is a coalescing signal to its loop, started on
// the first kick; turns serializes sweeps under one key, abandonably, so a run listed running by one is paused
// before the next lists it and a refused ask's sweep never waits past its sweepBudget.
type gateSweep struct {
	kick  chan struct{}
	turns *keylock.Set[string]
	once  sync.Once
}

// sweepBudget bounds one sweep, the loop's or a refused ask's.
const sweepBudget = 30 * time.Second

// gateOpen reads the gate, applying any edge the read found.
func (rs *Runs) gateOpen() bool {
	open, m := rs.gate.now()
	rs.applyGateMove(m)
	return open
}

// admitMode is what admitRunWork does with run work while the gate is closed.
type admitMode int

const (
	// refuseClosed answers the gate's refusal (errRunsNeedSignIn or errRunsProviderDown).
	refuseClosed admitMode = iota
	// holdClosed adds the run to the gate's heldHeals and answers errRunHeld.
	holdClosed
)

// errRunHeld answers a holdClosed admission the closed gate held.
var errRunHeld = errors.New("the run is held until the agent's sign-in can be answered")

// runAdmission is one piece of run work admitRunWork let through, and the only proof of it the engine's run-work
// guard accepts (guardRunWork); the zero value admits nothing.
type runAdmission struct {
	admittedBy *Runs
	admittedIn uint64
}

// admitRunWork is the one admission of run work, every write that makes a run's step do model work: a launch, every
// resume, a retry, a repeat's verbs, a step's prompt or answer, and a step's steer. The engine refuses each such
// write whose context the admission is not bound to (runAdmission.bind). A caller settles the admission once its
// writes returned.
func (rs *Runs) admitRunWork(mode admitMode, workflowID string, chatID marotte.ChatID) (runAdmission, error) {
	if mode == holdClosed {
		epoch, held, m := rs.gate.admitOrHold(workflowID, chatID)
		rs.applyGateMove(m)
		if held {
			slog.Info("a paused run is held until the agent's sign-in can be answered",
				"workflow_id", workflowID, "chat_id", chatID)
			return runAdmission{}, errRunHeld
		}
		return runAdmission{admittedBy: rs, admittedIn: epoch}, nil
	}
	epoch, m, err := rs.gate.admit()
	rs.applyGateMove(m)
	if err != nil {
		return runAdmission{}, err
	}
	return runAdmission{admittedBy: rs, admittedIn: epoch}, nil
}

// settle reconciles run work a closing edge crossed between its admission and its writes' return: the closing
// edge's sweep may have listed the run before the write landed, so the sweep runs again.
func (a runAdmission) settle(workflowID string) {
	if a.admittedBy != nil && a.admittedBy.gate.crossed(a.admittedIn) {
		slog.Info("sign-in gate: run work landed after the gate closed; pausing the running runs again",
			"workflow_id", workflowID)
		a.admittedBy.kickSweep()
	}
}

func (a runAdmission) admitted() bool { return a.admittedBy != nil }

// keepHold acts on a gate pause a finder read (actKeepHold): it starts the run's resume while the gate is open and
// only logs while it is closed.
func (rs *Runs) keepHold(workflowID string) {
	open, m := rs.gate.now()
	rs.applyGateMove(m)
	if !open {
		slog.Info("a paused run is held until the agent's sign-in can be answered",
			"workflow_id", workflowID, "answerability", m.to.String())
		return
	}
	rs.startRelease(workflowID, "", m.opening)
}

// maxFindAttempts bounds findHolds' retries of a read that fails.
const maxFindAttempts = 6

// findHolds reads each run for a gate pause, through the transition (gateFind): the named runs, or with none every
// run KAS lists paused. A failed read, of a run or of the list, is retried from releaseRetryBase, doubling up to
// sweepRetryCap, maxFindAttempts times in all, then logged as an ERROR.
func (rs *Runs) findHolds(ids ...string) {
	listed := len(ids) > 0
	for attempt := 1; ; attempt++ {
		ctx, cancel := rs.lifecycle.derivedContext()
		var err error
		if !listed {
			ids, err = rs.pausedRuns(ctx)
			listed = err == nil
		}
		if listed {
			ids, err = rs.findPass(ctx, ids)
		}
		cancel()
		if err == nil {
			return
		}
		if attempt >= maxFindAttempts {
			slog.Error("sign-in gate: a paused run could not be read for the gate's pause; the next sign-in or restart reads it again",
				"workflow_ids", ids, "attempts", attempt, "error", err)
			return
		}
		slog.Warn("sign-in gate: a paused run could not be read for the gate's pause; trying again",
			"attempt", attempt, "error", err)
		if !sleepCtx(rs.lifecycle.shutdownCtx, min(releaseRetryBase<<(attempt-1), sweepRetryCap)) {
			return
		}
	}
}

// findHoldsAsync is findHolds off the caller's path.
func (rs *Runs) findHoldsAsync(ids ...string) {
	rs.lifecycle.goUnlessDraining(func() { rs.findHolds(ids...) })
}

// pausedRuns lists the runs KAS reads paused.
func (rs *Runs) pausedRuns(ctx context.Context) ([]string, error) {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("the run list: %w", err)
	}
	var ids []string
	for i := range runs {
		if marotte.RunStatus(runs[i].Status) == marotte.RunStatusPaused {
			ids = append(ids, runs[i].WorkflowID)
		}
	}
	return ids, nil
}

// findPass is one read of each run for findHolds, returning the runs whose read failed.
func (rs *Runs) findPass(ctx context.Context, ids []string) (failed []string, err error) {
	var errs []error
	for _, id := range ids {
		if _, tErr := rs.transition(ctx, id, &runRequest{want: gateFind}); tErr != nil {
			failed = append(failed, id)
			errs = append(errs, fmt.Errorf("reading %s: %w", id, tErr))
		}
	}
	return failed, errors.Join(errs...)
}

// gateMoved applies a move of the vault's Answerability.
func (rs *Runs) gateMoved() { rs.applyGateMove(rs.gate.refresh()) }

// applyGateMove acts on the edge one read of the gate found, which no other read finds; runGate's doc owns what an
// edge does to a hold.
func (rs *Runs) applyGateMove(m gateMove) {
	switch {
	case m.opened():
		slog.Info("the agent's sign-in can be answered again; resuming the held runs", "runs", len(m.released))
		for _, wf := range slices.Sorted(maps.Keys(m.released)) {
			rs.startRelease(wf, m.released[wf], m.opening)
		}
		rs.findHoldsAsync()
	case m.closed():
		slog.Warn("the agent's sign-in cannot be answered; pausing the running runs", "answerability", m.to.String())
		rs.kickSweep()
	}
}

// releaseOwner is a run's latest release: the opening edge it serves, and whether it still runs.
type releaseOwner struct {
	opening uint64
	live    bool
}

// claimRelease takes workflowID's release for opening: false while one is live, which serves every later edge
// itself (resumeHeld), or once one served opening or a later edge.
func (g *runGate) claimRelease(workflowID string, opening uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if o, ok := g.releases[workflowID]; ok && (o.live || o.opening >= opening) {
		return false
	}
	if g.releases == nil {
		g.releases = make(map[string]releaseOwner)
	}
	g.releases[workflowID] = releaseOwner{opening: opening, live: true}
	return true
}

// servesLater reports whether an opening edge came after the one workflowID's release serves, and if so makes it
// serve the latest.
func (g *runGate) servesLater(workflowID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.serveLaterLocked(workflowID)
}

func (g *runGate) serveLaterLocked(workflowID string) bool {
	o := g.releases[workflowID]
	if g.opens <= o.opening {
		return false
	}
	o.opening = g.opens
	g.releases[workflowID] = o
	return true
}

// endRelease ends workflowID's live release, unless retry is set and an opening edge came after the one it serves:
// it then serves the latest edge and reports true. claimRelease refuses that edge's claim while the release is live,
// before this adopts the edge and after, because this release serves it.
func (g *runGate) endRelease(workflowID string, retry bool) (again bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if retry && g.serveLaterLocked(workflowID) {
		return true
	}
	o := g.releases[workflowID]
	o.live = false
	g.releases[workflowID] = o
	return false
}

// startRelease starts workflowID's release on the opening edge opening, unless the gate refuses it (claimRelease).
func (rs *Runs) startRelease(workflowID string, chatID marotte.ChatID, opening uint64) {
	rs.lifecycle.goClaimed(func() func() {
		if !rs.gate.claimRelease(workflowID, opening) {
			return nil
		}
		return func() { rs.resumeHeld(workflowID, chatID) }
	})
}

// resumeHeld is a held run's release, its automatic resume (healResume): tried again from releaseRetryBase while it
// fails at the open gate, maxReleaseAttempts times in all, then logged as an ERROR, and after a try that met the
// closed gate once the gate opens. An opening edge later than the one it serves cuts a wait short and starts the
// tries afresh, and so does one that came before its last try ended (runGate.endRelease). Every try for one pause
// shares one heal of the run's budget.
func (rs *Runs) resumeHeld(workflowID string, chatID marotte.ChatID) {
	charged := false
	for attempt := 1; ; attempt++ {
		hctx, cancel := rs.lifecycle.derivedContext()
		var out resumeOutcome
		out, charged = rs.retryResume(hctx, chatID, workflowID, charged)
		cancel()
		if hook := releaseTryHook.Load(); hook != nil {
			(*hook)(rs, workflowID, out)
		}
		fresh, again := rs.afterReleaseTry(workflowID, chatID, out, attempt)
		if !again {
			return
		}
		if fresh {
			attempt = 0
		}
		if out == resumeWritten || out == resumeSkipped {
			// The try ended the pause its heal was charged for, so the later edge's pause claims its own.
			charged = false
		}
	}
}

// afterReleaseTry waits for a release's next try after attempt ended in out: again false once the release ended,
// fresh when the next try starts the tries afresh.
func (rs *Runs) afterReleaseTry(workflowID string, chatID marotte.ChatID, out resumeOutcome, attempt int) (fresh, again bool) {
	switch {
	case out == resumeHeld, out == resumeFailed && !rs.gateOpen():
		fresh, again = true, rs.awaitOpen(workflowID)
	case out == resumeFailed && attempt < maxReleaseAttempts:
		fresh, again = rs.awaitRetry(workflowID, releaseRetryBase<<(attempt-1))
	default:
		if out == resumeFailed {
			slog.Error("sign-in gate: a held run could not be resumed; it stays held for the next sign-in",
				"workflow_id", workflowID, "chat_id", chatID, "attempts", attempt)
		}
		again = rs.gate.endRelease(workflowID, true)
		return again, again
	}
	if !again {
		// Only shutdown ends a wait, and leaves no edge to serve.
		rs.gate.endRelease(workflowID, false)
	}
	return fresh, again
}

// awaitOpen waits until the gate opens, then serves the latest edge; false at shutdown.
func (rs *Runs) awaitOpen(workflowID string) bool {
	for moved := rs.gate.moves(); !rs.gateOpen(); moved = rs.gate.moves() {
		select {
		case <-moved:
		case <-rs.lifecycle.shutdownCtx.Done():
			return false
		}
	}
	rs.gate.servesLater(workflowID)
	return true
}

// releaseTryHook, when set, runs after each try of a release, before the release decides on its next; tests only.
var releaseTryHook atomic.Pointer[func(rs *Runs, workflowID string, out resumeOutcome)]

// releaseWaitHook, when set, gives the wait before a release's next try in place of its backoff gap; tests only.
var releaseWaitHook atomic.Pointer[func(rs *Runs, workflowID string, gap time.Duration) time.Duration]

// awaitRetry waits gap before a release's next try: woken when an opening edge later than the one it serves cuts
// the wait short, which it then serves; again false at shutdown.
func (rs *Runs) awaitRetry(workflowID string, gap time.Duration) (woken, again bool) {
	if hook := releaseWaitHook.Load(); hook != nil {
		gap = (*hook)(rs, workflowID, gap)
	}
	t := time.NewTimer(gap)
	defer t.Stop()
	for {
		moved := rs.gate.moves()
		if rs.gate.servesLater(workflowID) {
			return true, true
		}
		select {
		case <-t.C:
			return false, true
		case <-moved:
		case <-rs.lifecycle.shutdownCtx.Done():
			return false, false
		}
	}
}

func (rs *Runs) kickSweep() {
	rs.sweep.once.Do(func() {
		rs.sweep.kick = make(chan struct{}, 1)
		rs.lifecycle.goLoopUnlessDraining(rs.sweepLoop)
	})
	select {
	case rs.sweep.kick <- struct{}{}:
	default:
	}
}

func (rs *Runs) sweepLoop() {
	ctx := rs.lifecycle.shutdownCtx
	for {
		select {
		case <-ctx.Done():
			return
		case <-rs.sweep.kick:
		}
		rs.reconcileClosed(ctx)
	}
}

// reconcileClosed sweeps until every running run is paused or the gate opens, retrying a failed sweep; past
// maxSweepAttempts it logs an ERROR and waits for the next kick.
func (rs *Runs) reconcileClosed(ctx context.Context) {
	for attempt := 1; ; attempt++ {
		if rs.gateOpen() {
			return
		}
		sctx, cancel := context.WithTimeout(ctx, sweepBudget)
		err := rs.sweepPause(sctx)
		cancel()
		if err == nil || errors.Is(err, errGateOpened) {
			return
		}
		if attempt >= maxSweepAttempts {
			slog.Error("sign-in gate: a running run could not be paused; its next credential ask waits until it is",
				"attempts", attempt, "error", err)
			return
		}
		slog.Warn("sign-in gate: not every running run could be paused; trying again", "attempt", attempt, "error", err)
		if !sleepCtx(ctx, sweepRetryBase<<(attempt-1)) {
			return
		}
	}
}

// errGateOpened ends a sweep the opening edge overtook: the runs left are running on an answerable sign-in.
var errGateOpened = errors.New("the sign-in gate opened")

// sweepPause pauses every listed running run with no pause pending (KAS parks a step on a refused ask once any pause
// was requested), holding each: nil once each was paused, errGateOpened when the gate opened first.
func (rs *Runs) sweepPause(ctx context.Context) error {
	release, err := rs.sweep.turns.Lock(ctx, "")
	if err != nil {
		return err
	}
	defer release()
	runs, err := rs.listRaw(ctx)
	if err != nil {
		return fmt.Errorf("the run list: %w", err)
	}
	var errs []error
	for i := range runs {
		r := &runs[i]
		if marotte.RunStatus(r.Status) != marotte.RunStatusRunning || r.PausePending {
			continue
		}
		pErr := rs.pauseHeld(ctx, r.WorkflowID)
		if errors.Is(pErr, errGateOpened) {
			return pErr
		}
		if pErr != nil {
			errs = append(errs, fmt.Errorf("pausing %s: %w", r.WorkflowID, pErr))
		}
	}
	return errors.Join(errs...)
}

// pauseHeld holds and pauses one running run, after any resume admitted before the gate closed (runTransitions
// decides it): nil once paused or when it needs no pause, errGateOpened when the gate opened first, and an error to
// retry otherwise.
func (rs *Runs) pauseHeld(ctx context.Context, workflowID string) error {
	_, err := rs.transition(ctx, workflowID, &runRequest{want: gatePause})
	return err
}

// pauseBeforeRefusal holds a refused credential ask, while the gate is closed, until every running run's pause was
// accepted, so the asking step parks instead of failing (the var block's premise); a failed sweep is retried with
// no limit, since answering early fails the step for good, and an engine draining for a restart is waited out, since
// its runs still run. It reports whether the gate is open, so the caller asks the vault again; false once the
// refusal may be written, at ctx's end, or when no engine is live, since one starting or ended runs no run.
func (rs *Runs) pauseBeforeRefusal(ctx context.Context) (gateOpen bool) {
	for attempt := 1; ; {
		moved := rs.gate.moves()
		if rs.gateOpen() {
			return true
		}
		drain, err := rs.refusalSweep(ctx)
		switch {
		case drain != nil:
			slog.Info("sign-in gate: a credential refusal waits out the engine's restart")
			select {
			case <-drain.ready:
			case <-drain.retired:
			case <-moved:
			case <-ctx.Done():
				return false
			}
			continue
		case err == nil, errors.Is(err, errNoLiveEngine), ctx.Err() != nil:
			return false
		case errors.Is(err, errGateOpened):
			return true
		case attempt == maxSweepAttempts:
			slog.Error("sign-in gate: a credential refusal is held until every running run is paused",
				"attempts", attempt, "error", err)
		default:
			slog.Warn("sign-in gate: not every running run was paused before a credential refusal; trying again",
				"attempt", attempt, "error", err)
		}
		if !waitOut(ctx, min(sweepRetryBase<<min(attempt-1, maxSweepAttempts), sweepRetryCap), moved) {
			return false
		}
		attempt++
	}
}

// waitOut waits d, or until wake closes; false at ctx's end.
func waitOut(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-wake:
	case <-ctx.Done():
		return false
	}
	return true
}

// errNoLiveEngine ends a refusal's sweep when no engine is live.
var errNoLiveEngine = errors.New("no engine is live")

// refusalSweep is one sweep of pauseBeforeRefusal, within sweepBudget, or the drain of an engine restarting.
func (rs *Runs) refusalSweep(ctx context.Context) (*engineDrain, error) {
	release, drain := rs.engine.tryDoorOrDrain()
	switch {
	case drain != nil:
		return drain, nil
	case release == nil:
		return nil, errNoLiveEngine
	}
	defer release()
	sctx, cancel := context.WithTimeout(ctx, sweepBudget)
	defer cancel()
	return nil, rs.sweepPause(sctx)
}
