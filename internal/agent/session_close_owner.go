package agent

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// The chat's lifecycle is the one owner of "may this chat's session close now". Every claimant of the session is a
// hold (holdKind) acquired and released under lc.mu, a reload or a release is a request (askClose), and the request is
// granted in the section that releases the last hold (decideLocked, run by every wakeLocked).

// holdKind names one claimant of a chat's session; blockerLocked reads each from its own record.
type holdKind uint8

const (
	holdAdmission holdKind = iota // a reservation
	holdTurn                      // an own or pending turn record
	holdKAS                       // KAS reports a run, or a closer's KAS hold stands
	holdLease                     // a running session call
	holdTail                      // a turn's close tail until its drain's pick (lc.tails)
	holdDoor                      // a session door in progress
	holdOpen                      // a caller using the entry its door returned (lc.opens)
	holdStep                      // a run step turn the chat's session hosts (lc.steps)
	holdWake                      // the wake a terminal run_complete owes the chat (lc.wakes)
	holdKinds
)

// blockerLocked is the first hold that refuses a close, holdKinds for none. opener is the reservation token of the
// prompt whose own door asks (0 for none): its reservation and its unsent turn do not refuse. Caller holds mu.
func (lc *chatLifecycle) blockerLocked(opener uint64) holdKind {
	switch {
	case lc.reserved && (opener == 0 || lc.admission != opener):
		return holdAdmission
	case turnRefuses(lc.own, opener) || turnRefuses(lc.pending, opener):
		return holdTurn
	case lc.kas.state != kasIdle:
		return holdKAS
	case lc.work[workSession] > 0:
		return holdLease
	case len(lc.tails) > 0:
		return holdTail
	case lc.work[workDoor] > 0:
		return holdDoor
	case len(lc.opens) > 0:
		return holdOpen
	case len(lc.steps) > 0:
		return holdStep
	case len(lc.wakes) > 0:
		return holdWake
	}
	return holdKinds
}

// turnRefuses reports whether turn record t refuses a close asked at opener's door: any record but the opener's own
// unsent prompt, which no session has seen.
func turnRefuses(t *activeTurn, opener uint64) bool {
	return t != nil && (opener == 0 || t.admission != opener || !t.awaitsSend())
}

// closePurpose is a bit set of why a close was asked.
type closePurpose uint8

const (
	closeReload  closePurpose = 1 << iota // the entry's door settings moved (markReload)
	closeRelease                          // nothing keeps the chat resident
	closeDead                             // the session already ended; nothing it hosts can be cut
)

// closeAsk is the chat's one pending close: the entry it names and every purpose that asked for it.
type closeAsk struct {
	sb  *sharedBridge
	why closePurpose
}

// closeGrant is the chat's one granted close, set in the section that decided it.
type closeGrant struct {
	sb *sharedBridge
	// done is closed once, when the grant ends; an open on sb waits on it, then asks again.
	done chan struct{}
	// why is what the closer re-checks; a request on the grant ORs in here until frozen, then into late, which
	// endClose asks again when the close withdrew.
	why, late closePurpose
	// frozen is set when the closer takes the grant (nextClose); confirmed once its re-checks passed (confirmClose).
	// Only a confirmed close refuses a lease: an unconfirmed one may still be withdrawn.
	frozen, confirmed bool
	// inline is a door's own grant, which that door closes, confirmed at grant; the closer leaves it alone.
	inline bool
}

// askVerdict is askClose's answer.
type askVerdict uint8

const (
	askPending askVerdict = iota // the request is the pending close, or joined it
	askGranted                   // the door's own grant: the caller closes inline, then calls endClose
	askClosing                   // a close is granted on the entry: wait on done, then ask again
	askEnded                     // the life ended; its teardown owns the session's end
)

type askAnswer struct {
	grant *closeGrant
	done  <-chan struct{}
	// release ends the open hold a door's askPending or askEnded answer took (openOrReload); nil on any other answer.
	release func()
	// startCloser is true only for the request that moved lc.closer from false to true; that caller starts it.
	startCloser bool
	verdict     askVerdict
}

// askClose requests a close of sb for why. door is true for a request made at a session door, whose opener is the
// asking prompt's reservation token (0 for a non-prompt door). A door's dead-session close, or its reload no hold
// refuses, is the door's own grant; every other request becomes or joins the one pending close.
func (lc *chatLifecycle) askClose(sb *sharedBridge, why closePurpose, opener uint64, door bool) askAnswer {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.askCloseLocked(sb, why, opener, door)
}

func (lc *chatLifecycle) askCloseLocked(sb *sharedBridge, why closePurpose, opener uint64, door bool) askAnswer {
	switch g := lc.closing; {
	case lc.ended:
		return askAnswer{verdict: askEnded}
	case g != nil && g.sb == sb:
		if g.frozen {
			g.late |= why
		} else {
			g.why |= why
		}
		return askAnswer{verdict: askClosing, done: g.done}
	case g == nil && door && (why&closeDead != 0 || lc.blockerLocked(opener) == holdKinds):
		g = &closeGrant{sb: sb, done: make(chan struct{}), why: why, confirmed: true, inline: true}
		if lc.ask != nil && lc.ask.sb == sb {
			g.why |= lc.ask.why
		}
		lc.ask = nil
		lc.closing = g
		lc.wakeLocked()
		return askAnswer{verdict: askGranted, grant: g}
	}
	if lc.ask == nil || lc.ask.sb != sb {
		lc.ask = &closeAsk{sb: sb, why: why}
	} else {
		lc.ask.why |= why
	}
	lc.decideLocked()
	return askAnswer{verdict: askPending, startCloser: lc.claimCloserLocked()}
}

// doorReads is what a session door reads for its close decision before it takes mu: sb.mu and the tab store are
// never read under it.
type doorReads struct {
	dead       bool // the entry's session ended
	configHeld bool // config.json does not read, so a marked entry keeps its session
	kept       bool // the chat stays resident (keptResident), so only a door reloads it
}

// openOrReload is a session door's one section on sb, its open entry: a close granted on sb, or a marked entry whose
// door is still in progress, answers askClosing with the channel to wait on. A dead session's close is the door's own
// grant; a marked entry's reload while config.json reads is asked with opener's exemption, and for a kept chat only
// as the door's own grant. When the door uses sb (askPending, askEnded) the answer carries the open hold taken in
// this section, so no close is granted between the decision and the caller's use.
func (lc *chatLifecycle) openOrReload(sb *sharedBridge, opener uint64, reads doorReads) askAnswer {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	a := lc.doorAskLocked(sb, opener, reads)
	if a.verdict == askPending || a.verdict == askEnded {
		a.release = lc.holdOpenLocked(opener)
	}
	return a
}

// holdOpenOn is the open hold of a door that keeps sb after its own close was refused: it waits on done when a close
// was granted on sb meanwhile, since no hold is taken on a closing entry.
func (lc *chatLifecycle) holdOpenOn(sb *sharedBridge, opener uint64) (release func(), done <-chan struct{}) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if g := lc.closing; g != nil && g.sb == sb {
		return nil, g.done
	}
	return lc.holdOpenLocked(opener), nil
}

func (lc *chatLifecycle) doorAskLocked(sb *sharedBridge, opener uint64, reads doorReads) askAnswer {
	switch {
	case lc.ended:
		return askAnswer{verdict: askEnded}
	case lc.closing != nil && lc.closing.sb == sb:
		return askAnswer{verdict: askClosing, done: lc.closing.done}
	case reads.dead:
		return lc.askCloseLocked(sb, closeDead, 0, true)
	case lc.reloadFor == sb && !reads.configHeld:
		blocker := lc.blockerLocked(opener)
		// The entry's own door is still finishing after it settled: wait for it, as a door reloads inline once the
		// door before it is done, rather than leave the reload to the closer.
		if lc.closing == nil && blocker == holdDoor {
			return askAnswer{verdict: askClosing, done: lc.changed}
		}
		// The closer never closes a kept chat, so a pending reload would only be granted and withdrawn.
		if reads.kept && (lc.closing != nil || blocker != holdKinds) {
			return askAnswer{verdict: askPending}
		}
		return lc.askCloseLocked(sb, closeReload, opener, true)
	}
	return askAnswer{verdict: askPending}
}

// reloadMarked reports whether sb is marked for a reload (markReload).
func (lc *chatLifecycle) reloadMarked(sb *sharedBridge) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.reloadFor == sb
}

// closerRefused undoes a closer start the runtime refused (its shutdown began): no closer will run what is owed, so
// the pending ask and a granted close it would have run go, and an open waiting on that grant asks again.
func (lc *chatLifecycle) closerRefused() {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.closer = false
	lc.ask = nil
	lc.dropUntakenGrantLocked()
	lc.wakeLocked()
}

// dropUntakenGrantLocked ends a granted close no closer took and no door runs inline: once no closer will run (the
// life's end, the closer's return or refused start), nothing else ends it, and an open waits on its done. Caller
// holds mu and wakes.
func (lc *chatLifecycle) dropUntakenGrantLocked() {
	if g := lc.closing; g != nil && !g.frozen && !g.inline {
		lc.closing = nil
		close(g.done)
	}
}

// claimCloserLocked marks the closer running when a close is owed it and none runs, reporting whether the caller
// must start it. An inline grant's door owes it instead, through endClose. Caller holds mu.
func (lc *chatLifecycle) claimCloserLocked() bool {
	inline := lc.closing != nil && lc.closing.inline
	if lc.ended || lc.closer || inline || (lc.ask == nil && lc.closing == nil) {
		return false
	}
	lc.closer = true
	return true
}

// decideLocked grants the pending close when no hold refuses it. A pending ask is decided with opener 0: a door's
// exemption applies only in its own section. Caller holds mu.
func (lc *chatLifecycle) decideLocked() {
	if lc.ask == nil || lc.closing != nil || lc.ended || lc.blockerLocked(0) != holdKinds {
		return
	}
	lc.closing = &closeGrant{sb: lc.ask.sb, done: make(chan struct{}), why: lc.ask.why}
	lc.ask = nil
}

// heldSinceGrantLocked reports a hold KAS's own work can start while a close is granted and not yet confirmed (a
// wire turn, its close tail, KAS's state, a hosted step, an owed wake), and a session call, which only a confirmed
// close refuses. None of them stands at grant time, since blockerLocked refuses on each. An open or a door on the
// entry waits on done, and a reservation taken meanwhile belongs to the next generation. Caller holds mu.
func (lc *chatLifecycle) heldSinceGrantLocked() bool {
	return (lc.own != nil && lc.own.Source == marotte.TurnSourceWireTurnStart) || len(lc.tails) > 0 ||
		lc.kas.state != kasIdle || len(lc.steps) > 0 || len(lc.wakes) > 0 || lc.work[workSession] > 0
}

// nextClose is one wait of the chat's closer: the granted close it runs next with the purposes it froze, or false
// when it returns (no close owed, a door's inline grant, or the life's end), having cleared lc.closer. It waits with
// mu dropped, since wakeLocked closes lc.changed under mu.
func (lc *chatLifecycle) nextClose() (*closeGrant, closePurpose, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for {
		g := lc.closing
		switch {
		case lc.ended, g != nil && g.inline, g == nil && lc.ask == nil:
			lc.closer = false
			return nil, 0, false
		case g != nil && !g.frozen:
			g.frozen = true
			return g, g.why, true
		}
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
			lc.mu.Lock()
		case <-lc.ctx.Done():
			// The runtime's end cancels the life's context before any teardown ends the life.
			lc.mu.Lock()
			lc.ask = nil
			lc.closer = false
			lc.dropUntakenGrantLocked()
			lc.wakeLocked()
			return nil, 0, false
		}
	}
}

// confirmClose is the closer's last look before it closes for g, once its re-checks passed: from a true answer every
// lease on g's entry is refused. A hold started since the grant (heldSinceGrantLocked) puts g back as the pending
// ask, answering false, so the close waits for that hold too.
func (lc *chatLifecycle) confirmClose(g *closeGrant) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.closing != g {
		return false
	}
	if !lc.heldSinceGrantLocked() {
		g.confirmed = true
		return true
	}
	lc.closing = nil
	why := g.why | g.late
	if lc.ask == nil {
		lc.ask = &closeAsk{sb: g.sb, why: why}
	} else if lc.ask.sb == g.sb {
		lc.ask.why |= why
	}
	close(g.done)
	lc.wakeLocked()
	return false
}

// endClose ends grant g, closed reporting whether its session closed; a call for a grant that is no longer the
// chat's (a second call, a grant put back) does nothing. A close that withdrew asks its late purposes again. It
// answers true when a close is still owed and no closer runs, having marked one running: the caller starts it.
func (lc *chatLifecycle) endClose(g *closeGrant, closed bool) (startCloser bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.closing != g {
		return false
	}
	lc.closing = nil
	if closed && lc.reloadFor == g.sb {
		lc.reloadFor = nil
	}
	if !closed && g.late != 0 && lc.ask == nil {
		lc.ask = &closeAsk{sb: g.sb, why: g.late}
	}
	lc.decideLocked()
	startCloser = lc.claimCloserLocked()
	close(g.done)
	lc.wakeLocked()
	return startCloser
}

// closingSession reports whether a confirmed close is on sb, so the end of sb's session is that close's.
func (lc *chatLifecycle) closingSession(sb *sharedBridge) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.closingLocked(sb)
}

// closingLocked reports whether a confirmed close is on sb. Caller holds mu.
func (lc *chatLifecycle) closingLocked(sb *sharedBridge) bool {
	return lc.closing != nil && lc.closing.sb == sb && lc.closing.confirmed
}

// closeOwner is the lifecycle that decides when sb, chatID's entry, may close: the life its leases are counted on. A
// run chat's entry has none: releaseEndedHost owns it.
func (*turnRegistry) closeOwner(chatID marotte.ChatID, sb *sharedBridge) (*chatLifecycle, bool) {
	if sb == nil || isRunChat(chatID) {
		return nil, false
	}
	lc, ok := sb.life.(*chatLifecycle)
	return lc, ok
}

// markReload marks sb, an entry of this life, for a reload (closeReload), reporting whether it was not marked yet.
// The mark is not a request: an idle chat keeps its session until its next door or close.
func (lc *chatLifecycle) markReload(sb *sharedBridge) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.ended || lc.reloadFor == sb {
		return false
	}
	lc.reloadFor = sb
	return true
}

// holdOpenLocked takes an open hold for the caller of a door whose opener token is opener (0 for a non-prompt door),
// answering its idempotent release. releaseReservation of opener ends it too, so a prompt's door hold never outlives
// its admission. Caller holds mu.
func (lc *chatLifecycle) holdOpenLocked(opener uint64) func() {
	if lc.ended {
		return func() {}
	}
	lc.openSeq++
	id := lc.openSeq
	if lc.opens == nil {
		lc.opens = make(map[uint64]uint64)
	}
	lc.opens[id] = opener
	var once sync.Once
	return func() {
		once.Do(func() {
			lc.mu.Lock()
			defer lc.mu.Unlock()
			if _, held := lc.opens[id]; held {
				delete(lc.opens, id)
				lc.wakeLocked()
			}
		})
	}
}

// endOpensLocked ends every open hold a door took with opener's token. Caller holds mu and wakes.
func (lc *chatLifecycle) endOpensLocked(opener uint64) {
	if opener == 0 {
		return
	}
	maps.DeleteFunc(lc.opens, func(_, o uint64) bool { return o == opener })
}

// beginTail counts a turn's close tail on lc and takes its hold, answering the life's context and the idempotent done
// that ends both in one section; false once the life ended.
func (r *turnRegistry) beginTail(lc *chatLifecycle) (lifeCtx context.Context, done func(), ok bool) {
	token := r.tailSeq.Add(1)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lifeCtx, done, ok = lc.beginLocked(workTail, nil, func() { delete(lc.tails, token) })
	if !ok {
		return nil, done, false
	}
	if lc.tails == nil {
		lc.tails = make(map[uint64]struct{})
	}
	lc.tails[token] = struct{}{}
	return lifeCtx, done, true
}

// holdStep records that host's session hosts the run step turn turnID, until endStep. A run chat's session is its
// run's, so it takes none. Lock order runLog.mu -> r.mu -> lc.mu.
func (r *turnRegistry) holdStep(host marotte.ChatID, turnID string) {
	if isRunChat(host) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.steps == nil {
		r.steps = make(map[marotte.ChatID]map[string]struct{})
	}
	if r.steps[host] == nil {
		r.steps[host] = make(map[string]struct{})
	}
	r.steps[host][turnID] = struct{}{}
	lc, ok := r.chats[host]
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.ended {
		return
	}
	if lc.steps == nil {
		lc.steps = make(map[string]struct{})
	}
	lc.steps[turnID] = struct{}{}
	lc.wakeLocked()
}

// endStep ends the hold holdStep took for turnID; a turn it never saw ends nothing.
func (r *turnRegistry) endStep(host marotte.ChatID, turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if set := r.steps[host]; set != nil {
		delete(set, turnID)
		if len(set) == 0 {
			delete(r.steps, host)
		}
	}
	lc, ok := r.chats[host]
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if _, held := lc.steps[turnID]; held {
		delete(lc.steps, turnID)
		lc.wakeLocked()
	}
}

// holdWake keeps chatID's session for the wake KAS owes it after workflowID's terminal run_complete: KAS sends the
// wake after the frame, and with no resident session its frames would reach the engine lane. The hold ends when a
// turn KAS opens on the chat (takeWakeLocked), its life ends or wakeHoldBound passes. A run chat takes none.
func (r *turnRegistry) holdWake(chatID marotte.ChatID, workflowID string) {
	if isRunChat(chatID) {
		return
	}
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.ended {
		return
	}
	if old := lc.wakes[workflowID]; old != nil {
		old.Stop()
	}
	if lc.wakes == nil {
		lc.wakes = make(map[string]*time.Timer)
	}
	var bound *time.Timer
	bound = time.AfterFunc(wakeHoldBound, func() {
		lc.mu.Lock()
		defer lc.mu.Unlock()
		if lc.wakes[workflowID] == bound {
			delete(lc.wakes, workflowID)
			lc.wakeLocked()
		}
	})
	lc.wakes[workflowID] = bound
	lc.wakeLocked()
}

// takeWakeLocked ends one wake hold, any of them, for a turn KAS opened: that turn, including a continuation that
// delivers a buffered wake, takes over the session in this section. A turn marotte opened takes none, since it may end
// unsent while KAS, still idle, starts the wake. A wake the running turn reads opens no turn, so its hold ends at
// wakeHoldBound. KAS's activity takes none, since holdKAS refuses while it lasts. Caller holds mu.
func (lc *chatLifecycle) takeWakeLocked() {
	for workflowID, bound := range lc.wakes {
		bound.Stop()
		delete(lc.wakes, workflowID)
		lc.wakeLocked()
		return
	}
}

// endWakesLocked ends every wake hold when the life ends. Caller holds mu.
func (lc *chatLifecycle) endWakesLocked() {
	if len(lc.wakes) == 0 {
		return
	}
	for _, bound := range lc.wakes {
		bound.Stop()
	}
	clear(lc.wakes)
	lc.wakeLocked()
}
