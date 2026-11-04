package agent

// The per-chat admission slot, reserved synchronously before any bridge exists, so a prompt is admitted or refused
// before its turn_open. OpenTurn then makes the Turn and StartTurn stamps model and baseline. A wire turn holds no reservation.

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// reserveLocked takes lc's admission slot for a caller whose intent began at stamp, answering the reservation (a
// zero Token when not taken) and, when the life gate refused, its verdict with the hold to wait on. An ended life
// answers gateClosedSince. Every reservation sends a session/prompt or opens a turn, so none is taken beside a
// bracketed wire turn (bracketedWireLocked) or while KAS's state admits none (kasPermits). Caller holds lc.mu, so a
// close's hold either precedes this check or follows the reservation, which the close's impact read then sees.
func (r *turnRegistry) reserveLocked(
	lc *chatLifecycle, source marotte.TurnOpenSource, stamp uint64, messageID string,
) (command.Reservation, gateVerdict, <-chan struct{}) {
	if lc.ended {
		return command.Reservation{}, gateClosedSince, nil
	}
	if v, idle := r.gates.admit(lc.chatID, stamp); v != gateAdmitted {
		return command.Reservation{}, v, idle
	}
	if lc.reserved || !lc.kas.may().admit || lc.bracketedWireLocked() {
		return command.Reservation{}, gateAdmitted, nil
	}
	lc.reserved = true
	lc.reservedSource = source
	lc.admission = r.admissions.Add(1)
	lc.admissionMessage = messageID
	lc.reservedForRow, lc.admissionOpened = false, false
	return command.Reservation{Chat: lc.chatID, Token: lc.admission, Stamp: lc.stamp}, gateAdmitted, nil
}

// holderSourceLocked names the admission holder: the bracket-owed prompt turn, else the reservation, else own. A
// prompt admitted beside a wire turn must read as Busy, and a bare wire turn still shows. Caller holds mu.
func (lc *chatLifecycle) holderSourceLocked() (marotte.TurnOpenSource, bool) {
	if lc.pending != nil {
		return lc.pending.Source, true
	}
	if lc.reserved {
		return lc.reservedSource, true
	}
	if lc.own != nil {
		return lc.own.Source, true
	}
	return 0, false
}

// tryReserve takes the slot iff free and the life gate admits stamp, never waiting (recovery retry, merge).
func (r *turnRegistry) tryReserve(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	lc, ok := r.tryLifecycleAt(chatID, stamp)
	if !ok {
		return command.Reservation{}, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	res, _, _ := r.reserveLocked(lc, source, stamp, "")
	return res, res.Token != 0
}

// tryReserveFenced takes the slot iff free and the fence holds, never waiting (a close's drain). The fence names a
// life that existed, so a missing lifecycle refuses, and a held close refuses.
func (r *turnRegistry) tryReserveFenced(chatID marotte.ChatID, source marotte.TurnOpenSource, fence command.TurnFence) (command.Reservation, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return command.Reservation{}, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !lc.fenceHoldsLocked(fence) {
		return command.Reservation{}, false
	}
	res, _, _ := r.reserveLocked(lc, source, lc.stamp, "")
	lc.reservedForRow = res.Token != 0
	return res, res.Token != 0
}

// tryReserveIdle takes the slot iff nothing holds admission, an open wire turn included, in one acquisition.
func (r *turnRegistry) tryReserveIdle(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	lc, ok := r.tryLifecycleAt(chatID, stamp)
	if !ok {
		return command.Reservation{}, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if _, held := lc.holderSourceLocked(); held {
		return command.Reservation{}, false
	}
	res, _, _ := r.reserveLocked(lc, source, stamp, "")
	return res, res.Token != 0
}

// promptHolder names the prompt-class turn holding admission: the bracket-owed one, else a non-finalizing own one.
func (r *turnRegistry) promptHolder(chatID marotte.ChatID) (string, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return "", false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	switch {
	case lc.pending != nil && !lc.pending.finalizing:
		return lc.pending.ID, true
	case lc.own != nil && !lc.own.finalizing && lc.own.Source.PromptClass():
		return lc.own.ID, true
	}
	return "", false
}

// releaseReservation frees the admission slot res holds and wakes every parked waiter, of which at most one
// acquires. A slot another reservation holds is left alone.
func (r *turnRegistry) releaseReservation(res command.Reservation) {
	lc, ok := r.lookup(res.Chat)
	if !ok {
		return
	}
	lc.mu.Lock()
	if !lc.reserved || lc.admission != res.Token {
		lc.mu.Unlock()
		return
	}
	lc.reserved = false
	lc.reservedSource = 0
	lc.admissionMessage = ""
	lc.reservedForRow, lc.admissionOpened = false, false
	lc.wakeLocked()
	idle := !lc.liveLockedState()
	lc.mu.Unlock()
	if idle {
		r.idle(res.Chat)
	}
}

// bracketedWireLocked reports an open wire turn a prompt must not reach: KAS aborts its running turn on any client
// session/prompt. A fold-opened wire turn is left out: no turn_end is owed for it, so it may never close, and
// refusing prompts beside it would strand the chat. Caller holds mu.
func (lc *chatLifecycle) bracketedWireLocked() bool {
	t := lc.own
	return t != nil && !t.finalizing && t.bracketed && t.Source == marotte.TurnSourceWireTurnStart
}

// promptAdmission is one look at a prompt's admission: the reservation taken, or why not and what to wait on.
type promptAdmission struct {
	// changed is the lifecycle's next-change channel, gate the close hold's idle channel; either may be nil.
	changed, gate <-chan struct{}
	res           command.Reservation
	busy, closed  bool
}

// reserveOrHolder tries a prompt's reservation at stamp and on refusal reports whether a steerable holder or a close
// refused it, with the channels to park on, in one acquisition, so a release between try and park cannot strand the
// waiter.
func (r *turnRegistry) reserveOrHolder(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, messageID string) promptAdmission {
	lc, gate, err := r.admitLife(chatID, stamp)
	switch {
	case err != nil:
		return promptAdmission{closed: true}
	case lc == nil:
		return promptAdmission{gate: gate}
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	res, v, idle := r.reserveLocked(lc, source, stamp, messageID)
	switch {
	case res.Token != 0:
		return promptAdmission{res: res}
	case v == gateClosedSince:
		return promptAdmission{closed: true}
	case v == gateWait:
		return promptAdmission{gate: idle, changed: lc.changed}
	}
	holder, held := lc.holderSourceLocked()
	return promptAdmission{busy: lc.kas.may().steerHolder && held && holder.SteerableHolder(), changed: lc.changed}
}

// admissionHolder reports the holder's source (open turn, else reservation), false when neither.
func (r *turnRegistry) admissionHolder(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return 0, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.holderSourceLocked()
}

// TryReserveTurn takes the admission slot iff free and the life gate admits stamp, never waiting.
func (bc *bridgeCoordinator) TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	return bc.turns.tryReserve(chatID, source, stamp)
}

// TryReserveIdleTurn takes the admission slot iff no turn is open and nothing holds it, never waiting.
func (bc *bridgeCoordinator) TryReserveIdleTurn(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	return bc.turns.tryReserveIdle(chatID, source, stamp)
}

// TryReserveTurnFenced is TryReserveTurn for a close's drain, refused once a turn
// opened after the closed one. The open checks the fence again where it appends.
func (bc *bridgeCoordinator) TryReserveTurnFenced(chatID marotte.ChatID, source marotte.TurnOpenSource, fence command.TurnFence) (command.Reservation, bool) {
	return bc.turns.tryReserveFenced(chatID, source, fence)
}

// LifeStamp is the life gate's position now, the stamp a caller takes where its intent toward a chat begins.
func (bc *bridgeCoordinator) LifeStamp() uint64 { return bc.turns.gates.stamp() }

// AwaitNoClose waits until no close of chatID is held (chatGates.awaitNoClose).
func (bc *bridgeCoordinator) AwaitNoClose(ctx context.Context, chatID marotte.ChatID) (bool, error) {
	return bc.turns.gates.awaitNoClose(ctx, chatID)
}

// PromptHolder names the prompt-class turn holding the chat's admission.
func (bc *bridgeCoordinator) PromptHolder(chatID marotte.ChatID) (string, bool) {
	return bc.turns.promptHolder(chatID)
}

// ReleaseTurnReservation frees the admission slot res took, waking every waiter.
func (bc *bridgeCoordinator) ReleaseTurnReservation(res command.Reservation) {
	bc.turns.releaseReservation(res)
}

// AdmissionHolderSource reports the holder: the open turn's source (a wire turn holds no reservation), else the
// reservation's. Satisfies command.turnOutcomeAccess.
func (bc *bridgeCoordinator) AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return bc.turns.admissionHolder(chatID)
}

// ReserveTurnForPrompt takes the slot for a prompt whose intent began at stamp, waiting up to wait; one waiter
// acquires per wake. A steerable holder (a prompt-class turn, or a wire turn KAS bracketed) answers Busy at once; a
// close committed since stamp answers Closed; a revert holder, a held close, a KAS hold or a run KAS reports with no
// steerable holder parks the waiter and answers Starting at the budget. A dead ctx answers Starting.
func (bc *bridgeCoordinator) ReserveTurnForPrompt(
	ctx context.Context, chatID marotte.ChatID, wait time.Duration, stamp uint64, messageID string,
) (command.AdmissionOutcome, command.Reservation) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		a := bc.turns.reserveOrHolder(chatID, marotte.TurnSourcePrompt, stamp, messageID)
		if out, done := a.outcome(); done {
			return out, a.res
		}
		select {
		case <-a.changed:
		case <-a.gate:
		case <-timer.C:
			return bc.expiredAdmission(chatID, stamp, messageID)
		case <-ctx.Done():
			return command.AdmissionStarting, command.Reservation{}
		}
	}
}

// outcome is the answer a look decides, false when the caller parks.
func (a *promptAdmission) outcome() (command.AdmissionOutcome, bool) {
	switch {
	case a.res.Token != 0:
		return command.AdmissionAcquired, true
	case a.closed:
		return command.AdmissionClosed, true
	case a.busy:
		return command.AdmissionBusy, true
	}
	return command.AdmissionStarting, false
}

// defaultIdleEndGrace is how long a wire turn waits for its turn_end once KAS reports no turn running. KAS 2.31
// sends that report just before the turn's turn_completion and turn_end, so only a lost end outlasts it.
const defaultIdleEndGrace = 10 * time.Second

// WireTurnActivity takes KAS's report of whether the chat's session runs a turn into the chat's KAS state (kasExec).
// While KAS reports none, an open bracketed wire turn closes after the grace unless its turn_end or a report of a
// running turn comes first. No local silence closes it: KAS's activity counts its executions, so a slow model call,
// a retry or a compaction keeps the turn open.
func (bc *bridgeCoordinator) WireTurnActivity(chatID marotte.ChatID, active bool) {
	t, arm, ok := bc.turns.noteKASActivity(chatID, active)
	if !ok {
		return
	}
	grace := cmp.Or(bc.idleEndGrace, defaultIdleEndGrace)
	// Refused once Shutdown began, as armWireGrace's.
	bc.lifecycle.afterUnlessDraining(grace, func() {
		closed := bc.finalizeTurn(context.Background(), chatID, &turnClose{
			Closer: closerKASIdle, Stop: marotte.StopReasonUnknown, Reason: kasIdleReason, Turn: t.ID,
			Valid: func(lc *chatLifecycle, c *activeTurn) bool { return lc.idleSinceLocked(c, arm) },
		})
		if closed {
			slog.Warn("the agent went idle without ending its turn; closed it", "chat_id", chatID, "turn", t.ID, "grace", grace)
		}
	})
}

// noteKASActivity applies KAS's report to the chat's KAS state and to the open bracketed wire turn, returning the arm
// an idle close keys on when this report starts an idle stretch.
func (r *turnRegistry) noteKASActivity(chatID marotte.ChatID, active bool) (*activeTurn, uint64, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, 0, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	in := kasReportIdle
	if active {
		in = kasReportActive
	}
	lc.kasStepLocked(in, 0)
	if !lc.bracketedWireLocked() {
		return nil, 0, false
	}
	t := lc.own
	if active || t.kasIdle {
		t.kasIdle = !active
		return nil, 0, false
	}
	t.kasIdle = true
	t.idleArms++
	return t, t.idleArms, true
}

// kasHoldWarnAfter is when a closer still waiting for KAS to go idle is logged. Elapsed time never ends the wait
// (KAS's idle report, the session ending or shutdown do): a send beside a still-running execution is the loss the
// wait prevents, while a refused prompt hands its text back.
const kasHoldWarnAfter = 15 * time.Minute

// awaitKASIdle waits, after the fenced turn closed, until KAS's state permits session calls (kasPermits), so the
// closer's model switch, compaction and drain are not queued beside a run KAS still reports, such as the cancelled
// one or KAS's re-wake of the notifications a cancel cleared, where KAS streams only one of them (its adapter owns
// the last execution). False when a turn opened meanwhile, whose own close drains, when the life ended (its end
// re-mints the epoch, so the fence fails), or when ctx ended.
func (lc *chatLifecycle) awaitKASIdle(ctx context.Context, fence command.TurnFence) bool {
	warn := time.NewTimer(kasHoldWarnAfter)
	defer warn.Stop()
	for {
		lc.mu.Lock()
		callable, changed := lc.kas.may().sessionCall, lc.changed
		superseded := lc.own != nil || lc.pending != nil || !lc.fenceHoldsLocked(fence)
		lc.mu.Unlock()
		switch {
		case superseded:
			return false
		case callable:
			return true
		}
		select {
		case <-changed:
		case <-warn.C:
			slog.Warn("kiro-cli still reports the session busy after a turn closed; the chat's prompts wait for it",
				"chat_id", lc.chatID, "waited", kasHoldWarnAfter)
		case <-ctx.Done():
			return false
		}
	}
}

// errPromptTurnClosed fails a prompt whose turn a closer took while its send waited; the chat's close answers
// marotte.ErrChatClosedDuringCall instead, which owes no failure frame.
var errPromptTurnClosed = errors.New("this prompt's turn closed before it was sent, so it was not sent; send it again")

// AwaitPromptSend holds turnID's session/prompt until the prompt owns the chat while KAS admits it (markSentLocked),
// then lets it go. No frame before the send is the prompt's (awaitsSend), so a notification re-wake keeps a turn of
// its own: a bracketed one is waited out to its close, and a fold-opened one is claimed by the same section that
// read KAS's state and closed here, unless its late turn_start claimed it first (bindPending), which leaves a
// bracketed turn to wait out. It fails the prompt unsent only when its turn closed meanwhile (marotte.ErrChatClosedDuringCall
// when the chat's close took it), ctx ended, or the reader's stop kept it unsent (command.ErrPromptStopped). Once it lets the send go it answers the write's commit
// (sendCommitLocked).
func (bc *bridgeCoordinator) AwaitPromptSend(ctx context.Context, chatID marotte.ChatID, turnID string) (func() error, error) {
	for {
		fold, commit, err := bc.turns.awaitPromptSend(ctx, chatID, turnID)
		if err != nil || fold == nil {
			return commit, err
		}
		slog.Info("closing the agent's unbracketed turn so an admitted prompt can be sent", "chat_id", chatID, "turn", fold.ID)
		bc.finalizeTurn(ctx, chatID, &turnClose{
			Closer: closerPromptHandoff, Stop: marotte.StopReasonUnknown, Reason: promptHandoffReason, Turn: fold.ID, Claimed: fold,
		})
	}
}

// awaitPromptSend is AwaitPromptSend's wait, answering the fold-opened turn it claimed for the caller to close before
// the prompt can own the chat, or the write's commit once the send may go. A run KAS starts while the send is on the
// wire is still not seen: KAS offers no execution token to make the handoff atomic, so its turn_start binds
// provisionally (ReviseTurnBinding).
func (r *turnRegistry) awaitPromptSend(ctx context.Context, chatID marotte.ChatID, turnID string) (*activeTurn, func() error, error) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, nil, nil
	}
	warn := time.NewTimer(kasHoldWarnAfter)
	defer warn.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if r.claimHook != nil {
			r.claimHook()
		}
		step, fold, commit, changed, err := lc.sendAttempt(turnID)
		if err != nil {
			return nil, nil, err
		}
		switch step {
		case sendNow:
			return nil, commit, nil
		case sendCloseFold:
			return fold, nil, nil
		case sendStopped:
			return nil, nil, command.ErrPromptStopped
		case sendWait:
		}
		select {
		case <-changed:
		case <-warn.C:
			slog.Warn("the chat's admitted prompt still waits for kiro-cli's run or the open turn to end",
				"chat_id", chatID, "waited", kasHoldWarnAfter)
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// sendAttempt is one pass of awaitPromptSend's gate on turnID, answering the channel to wait on for sendWait. A turn
// gone from an ended life is the close's casualty (marotte.ErrChatClosedDuringCall); one another closer took is
// errPromptTurnClosed.
func (lc *chatLifecycle) sendAttempt(
	turnID string,
) (step sendStep, fold *activeTurn, commit func() error, changed <-chan struct{}, err error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil || t.finalizing {
		if lc.ended {
			return 0, nil, nil, nil, marotte.ErrChatClosedDuringCall
		}
		return 0, nil, nil, nil, errPromptTurnClosed
	}
	step, fold = lc.markSentLocked(t)
	if step == sendNow {
		commit = lc.sendCommitLocked(t)
	}
	return step, fold, commit, lc.changed, nil
}

// sendCommitLocked is the write commit of an attempt markSentLocked just let t's send go for: a stop requested since
// t's first attempt was let through (sendStops) refuses the frame with command.ErrPromptStopped, and a stop requested
// once it passed writes its session/cancel behind the frame, since both take the engine's write lock. Caller holds mu.
func (lc *chatLifecycle) sendCommitLocked(t *activeTurn) func() error {
	base := t.sendStops
	return func() error {
		if lc.stopCount.Load() != base {
			return command.ErrPromptStopped
		}
		return nil
	}
}

// kasStep applies a closer's input for the turn at seq: a release answers whether the closer may drain, with the
// rows queued under its hold.
func (lc *chatLifecycle) kasStep(in kasInput, seq uint64) (drain bool, rows []string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.kasStepLocked(in, seq)
}

// unbracketedPrompt reports a prompt turn no wire turn_start bound: kiro-cli never streamed its run.
func (t *activeTurn) unbracketedPrompt() bool {
	t.lc.mu.Lock()
	defer t.lc.mu.Unlock()
	return t.Source.Acknowledgeable() && !t.acked
}

// idleSinceLocked reports whether t is still the open bracketed wire turn and KAS has reported no running turn since
// arm. Caller holds mu.
func (lc *chatLifecycle) idleSinceLocked(t *activeTurn, arm uint64) bool {
	return lc.own == t && lc.bracketedWireLocked() && t.kasIdle && t.idleArms == arm
}

// One last try first.
func (bc *bridgeCoordinator) expiredAdmission(chatID marotte.ChatID, stamp uint64, messageID string) (command.AdmissionOutcome, command.Reservation) {
	a := bc.turns.reserveOrHolder(chatID, marotte.TurnSourcePrompt, stamp, messageID)
	out, _ := a.outcome()
	return out, a.res
}

// The manager registers before Start, so presence is not liveness.
func (bc *bridgeCoordinator) bridgeLive(chatID marotte.ChatID) bool {
	sb := bc.bridge.mgr.get(chatID)
	return sb != nil && sb.startedPastOpen()
}
