package agent

// The reader's stop acts on the turns that ran when it was asked for, captured in one section, never on whatever
// holds the chat once its session/cancel has gone out.

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func refOf(t *activeTurn) command.TurnRef { return command.TurnRef{Chat: t.Chat, Turn: t.ID} }

// requestStop records the stop on the open sequence, holds the chat's sends until releaseStop, and captures its
// targets. An unsent prompt is the target only when nothing else runs: own is none, closing or the prompt itself,
// and KAS reports no run, so the send gate would send it next. Behind a running turn it waits, and goes once that
// turn ends. No lifecycle, no stop.
func (r *turnRegistry) requestStop(chatID marotte.ChatID) *command.TurnStop {
	stop := &command.TurnStop{Chat: chatID}
	lc, ok := r.lookup(chatID)
	if !ok {
		return stop
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.stopSeq = lc.nextSeq
	stop.Token = r.stopTokens.Add(1)
	if lc.stops == nil {
		lc.stops = make(map[uint64]struct{})
	}
	lc.stops[stop.Token] = struct{}{}
	o, u := lc.own, lc.pending
	if o != nil {
		stop.Own = refOf(o)
		stop.Wire = o.Source == marotte.TurnSourceWireTurnStart && !o.finalizing
	}
	if c := lc.call; c != nil {
		stop.Call = refOf(c)
	}
	if u != nil && u.awaitsSend() && !u.finalizing && (o == nil || o == u || o.finalizing) && lc.kas.may().admit {
		stop.Next = refOf(u)
	}
	return stop
}

// releaseStop lifts one stop's send hold. A lifecycle forgotten meanwhile took the hold with it.
func (r *turnRegistry) releaseStop(stop *command.TurnStop) {
	lc, ok := r.lookup(stop.Chat)
	if !ok || stop.Token == 0 {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if _, held := lc.stops[stop.Token]; held {
		delete(lc.stops, stop.Token)
		lc.wakeLocked()
	}
}

// RequestStop records the reader's stop and captures what it targets (requestStop).
func (bc *bridgeCoordinator) RequestStop(chatID marotte.ChatID) *command.TurnStop {
	return bc.turns.requestStop(chatID)
}

// ReleaseStop lifts the stop's hold on the chat's sends.
func (bc *bridgeCoordinator) ReleaseStop(stop *command.TurnStop) {
	bc.turns.releaseStop(stop)
}

// ArmStopGrace acts on each captured target still live, in one section: Next is kept unsent, a call KAS has not
// answered within d is cancelled with command.ErrCancelGraceExpired, so the prompt-failure path closes its turn, and
// an open turn KAS started closes as cancelled at d unless its turn_end comes first, so a lost bracket, or a
// fold-opened turn no turn_end is owed for, cannot hold the chat's sends.
func (bc *bridgeCoordinator) ArmStopGrace(stop *command.TurnStop, d time.Duration) bool {
	lc, ok := bc.turns.lookup(stop.Chat)
	if !ok {
		return false
	}
	lc.mu.Lock()
	acted := false
	if t := lc.liveLocked(stop.Next.Turn); stop.Next.Named() && t != nil && t.awaitsSend() && !t.finalizing {
		t.stopped = true
		lc.wakeLocked()
		acted = true
	}
	if c := lc.call; stop.Call.Named() && c != nil && c.ID == stop.Call.Turn && c.cancel != nil {
		lc.armCallGraceLocked(c, d)
		acted = true
	}
	wire := ""
	if t := lc.liveLocked(stop.Own.Turn); stop.Wire && t != nil && !t.finalizing {
		wire = t.ID
		acted = true
	}
	lc.mu.Unlock()
	if wire != "" {
		bc.armWireGrace(stop.Chat, wire, d)
	}
	return acted
}

// armCallGraceLocked cancels t's call at d unless it ended first (EndPromptCall). Caller holds mu.
func (lc *chatLifecycle) armCallGraceLocked(t *activeTurn, d time.Duration) {
	if t.graceTimer != nil {
		t.graceTimer.Stop()
	}
	t.graceTimer = time.AfterFunc(d, func() {
		lc.mu.Lock()
		cancel := t.cancel
		lc.mu.Unlock()
		if cancel == nil {
			return
		}
		slog.Warn("cancel unacked within grace; cancelled the prompt's call", "chat_id", t.Chat, "turn", t.ID, "grace", d)
		cancel(command.ErrCancelGraceExpired)
	})
}

// armWireGrace closes the named turn KAS started as cancelled at d; the close names the turn, so it never reaches a
// later one. Refused once Shutdown began: it closes its own turns, with its own account.
func (bc *bridgeCoordinator) armWireGrace(chatID marotte.ChatID, turnID string, d time.Duration) {
	bc.lifecycle.afterUnlessDraining(d, func() {
		if bc.finalizeTurn(context.Background(), chatID, &turnClose{Closer: closerCancelGrace, Stop: marotte.StopReasonCancelled, Turn: turnID}) {
			slog.Warn("cancel unacked within grace; closed the agent's turn", "chat_id", chatID, "turn", turnID, "grace", d)
		}
	})
}

// EndPromptCall forgets the named turn's call once it returned: its cancel, its grace and its hold on lc.call.
func (bc *bridgeCoordinator) EndPromptCall(turn command.TurnRef) {
	lc, ok := bc.turns.lookup(turn.Chat)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(turn.Turn)
	if lc.call != nil && lc.call.ID == turn.Turn {
		t = lc.call
		lc.call = nil
	}
	if t == nil {
		return
	}
	t.cancel = nil
	if t.graceTimer != nil {
		t.graceTimer.Stop()
		t.graceTimer = nil
	}
}
