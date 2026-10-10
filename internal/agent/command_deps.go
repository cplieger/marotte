package agent

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// bridgeRole adapts the coordinator to command.bridgeAccess, checking for a nil *sharedBridge,
// which would become a non-nil interface.
type bridgeRole struct{ coord *bridgeCoordinator }

// Bridge returns the active bridge for a chat, or nil.
func (b bridgeRole) Bridge(chatID marotte.ChatID) command.Bridge {
	sb := b.coord.bridgeFor(chatID)
	if sb == nil {
		return nil
	}
	return sb
}

// OpenBridge ensures a bridge exists for the chat (typed-nil checked).
func (b bridgeRole) OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (command.Bridge, error) {
	sb, err := b.coord.openBridge(ctx, chatID, model)
	if err != nil || sb == nil {
		return nil, err
	}
	return sb, nil
}

// CloseBridge closes every turn the bridge hosted with outcome, then stops it.
func (b bridgeRole) CloseBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	b.coord.closeBridge(ctx, chatID, outcome)
}

// BridgeLive reports a bridge for the chat that is past its spawn.
func (b bridgeRole) BridgeLive(chatID marotte.ChatID) bool { return b.coord.bridgeLive(chatID) }

// DeleteChatStateByChain deletes a chat's runs and KAS sessions from a captured chain. Order is
// the contract: runs before sessions (KAS refuses a session owning a live run), sessions
// before the bridge closes, Reap last.
func (rt *Runtime) DeleteChatStateByChain(ctx context.Context, chatID marotte.ChatID, sessionChain []string, cause command.RunStopCause) {
	stop := runStop{}
	switch cause {
	case command.RunStopTabClosed:
		stop = userStop(stopWhyTabClosed)
	case command.RunStopChatDeleted:
		stop = userStop(stopWhyChatDeleted)
	}
	rt.beginSteerTeardown(ctx, chatID, false)
	rt.runs.deleteForSessions(ctx, chatID, sessionChain, stop)
	rt.deleteSessions(ctx, chatID, sessionChain)
	rt.cleanupChatState(ctx, chatID)
	rt.reapSessions(sessionChain)
}

// CloseChatState tears down in-memory state without touching the KAS session, so a reopen session/loads it.
func (rt *Runtime) CloseChatState(ctx context.Context, chatID marotte.ChatID) {
	rt.beginSteerTeardown(ctx, chatID, true)
	rt.runs.cancelForChat(ctx, chatID, userStop(stopWhyTabClosed))
	rt.cleanupChatState(ctx, chatID)
}

// DischargeWaiting ends a chat's retained waiting_on_user claim and broadcasts the clear.
func (rt *Runtime) DischargeWaiting(ctx context.Context, chatID marotte.ChatID) {
	if !rt.bus.chatStatus.clearWaiting(chatID) {
		return
	}
	rt.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventChatStatus, chatID, marotte.ChatStatusPayload{}))
}

// OpenTurn appends the turn_open and creates the registry record together; an answering source
// discharges the waiting claim.
func (rt *Runtime) OpenTurn(ctx context.Context, chatID marotte.ChatID, open command.TurnOpen) (string, error) {
	id, err := rt.coord.OpenTurn(ctx, chatID, open)
	if err == nil && open.Source.UserAnswered() {
		rt.DischargeWaiting(ctx, chatID)
	}
	return id, err
}

// StartTurn stamps the model and credit baseline onto the turn, with the bridge live, just before the call.
func (rt *Runtime) StartTurn(ctx context.Context, chatID marotte.ChatID, turnID string) bool {
	return rt.coord.StartTurn(ctx, chatID, turnID)
}

// ReserveTurnForPrompt takes the chat's admission slot for a prompt, waiting up to wait; a refusal keys on the holder's source.
func (rt *Runtime) ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration) command.AdmissionOutcome {
	return rt.coord.ReserveTurnForPrompt(ctx, chatID, wait)
}

// TryReserveTurn takes the chat's admission slot iff it is free.
func (rt *Runtime) TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	return rt.coord.TryReserveTurn(chatID, source)
}

// TryReserveIdleTurn takes the chat's admission slot iff no turn is open and nothing holds it.
func (rt *Runtime) TryReserveIdleTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	return rt.coord.TryReserveIdleTurn(chatID, source)
}

// TryReserveTurnFenced takes the chat's admission slot iff it is free and no turn
// opened after the fenced one.
func (rt *Runtime) TryReserveTurnFenced(chatID marotte.ChatID, source marotte.TurnOpenSource, fence command.TurnFence) bool {
	return rt.coord.TryReserveTurnFenced(chatID, source, fence)
}

// PromptHolder names the prompt-class turn holding the chat's admission.
func (rt *Runtime) PromptHolder(chatID marotte.ChatID) (string, bool) {
	return rt.coord.PromptHolder(chatID)
}

// ReleaseTurnReservation frees the chat's admission slot and wakes waiters.
func (rt *Runtime) ReleaseTurnReservation(chatID marotte.ChatID) {
	rt.coord.ReleaseTurnReservation(chatID)
}

// AwaitTurn blocks until the named turn has finalized and reports what it did.
func (rt *Runtime) AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	return rt.coord.AwaitTurn(ctx, chatID, turnID)
}

// AwaitTurnBound blocks until KAS binds the named turn's prompt or the turn finalizes unbound.
func (rt *Runtime) AwaitTurnBound(ctx context.Context, chatID marotte.ChatID, turnID string) (bool, error) {
	return rt.coord.AwaitTurnBound(ctx, chatID, turnID)
}

// ReleaseTurn gives up the completion handle OpenTurn issued.
func (rt *Runtime) ReleaseTurn(chatID marotte.ChatID, turnID string) {
	rt.coord.ReleaseTurn(chatID, turnID)
}

// SettleTurnOnResponse closes the turn on its settling response once the queue behind it is consumed, unless turn_end got there first.
func (rt *Runtime) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	rt.coord.SettleTurnOnResponse(ctx, chatID, turnID, seq, resp)
}

// TurnOpenedAfter reports whether any own turn on the chat opened after turnID.
func (rt *Runtime) TurnOpenedAfter(chatID marotte.ChatID, turnID string) bool {
	return rt.coord.TurnOpenedAfter(chatID, turnID)
}

// StopRequestedAfter reports whether a stop was requested after turnID opened.
func (rt *Runtime) StopRequestedAfter(chatID marotte.ChatID, turnID string) bool {
	return rt.coord.StopRequestedAfter(chatID, turnID)
}

// RequestStop records the reader's stop on the chat's turn sequence.
func (rt *Runtime) RequestStop(chatID marotte.ChatID) {
	rt.coord.RequestStop(chatID)
}

// AdmissionHolderSource reports who holds the admission slot: the open turn, else the reservation.
func (rt *Runtime) AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return rt.coord.AdmissionHolderSource(chatID)
}

// FinalizeLocalShellTurn closes a `!cmd` turn, appending its one text entry first.
func (rt *Runtime) FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, turnID, output string) {
	rt.coord.FinalizeLocalShellTurn(ctx, chatID, turnID, output)
}

// PreSendCompact runs the compaction policy's before-a-send trigger.
func (rt *Runtime) PreSendCompact(ctx context.Context, chatID marotte.ChatID) {
	rt.coord.autoCompact.preSend(ctx, chatID)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string, kind marotte.FailureKind, seq uint64) {
	rt.coord.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason, kind, seq)
}

var _ command.SessionRenamer = (*Runtime)(nil)

// RenameSession renames a session no chat bridge holds, through the utility session.
func (rt *Runtime) RenameSession(ctx context.Context, sessionID, title string) error {
	raw, err := rt.utility.get().session.renameSessionRaw(ctx, sessionID, title)
	if err != nil {
		return err
	}
	return command.RenameResultOutcome(raw)
}
