package agent

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// bridgeRole adapts the bridge coordinator to command.BridgeAccess. A named type
// rather than methods on Runtime: the coordinator returns *sharedBridge, and a nil
// one assigned to command.Bridge is a NON-nil interface, so every method checks.
type bridgeRole struct{ coord *BridgeCoordinator }

// Bridge returns the active bridge for a chat, or nil.
func (b bridgeRole) Bridge(chatID marotte.ChatID) command.Bridge {
	sb := b.coord.Bridge(chatID)
	if sb == nil {
		return nil
	}
	return sb
}

// OpenBridge ensures a bridge exists for the chat. The nil check is the typed-nil
// trap bridgeRole's own doc names.
func (b bridgeRole) OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (command.Bridge, error) {
	sb, err := b.coord.OpenBridge(ctx, chatID, model)
	if err != nil || sb == nil {
		return nil, err
	}
	return sb, nil
}

// AwaitReplayAdopted waits for a session/load replay this chat may have in flight to
// be adopted into the record. A non-nil error means do not rewrite the transcript.
func (b bridgeRole) AwaitReplayAdopted(ctx context.Context, chatID marotte.ChatID) error {
	return b.coord.AwaitReplayAdopted(ctx, chatID)
}

// CloseBridge tears down the bridge for a chat.
func (b bridgeRole) CloseBridge(chatID marotte.ChatID) { b.coord.CloseBridge(chatID) }

// DeleteChatState tears down all in-memory state for a chat being permanently deleted,
// cancelling its runs and reaping its durable KAS session.
func (rt *Runtime) DeleteChatState(ctx context.Context, chatID marotte.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, true)
}

// DeleteChatStateByChain is DeleteChatState for a chat whose record is already gone:
// the close escalation deletes the record inside its commit, so the session chain is
// captured beforehand rather than re-read from a record that would silently no-op.
func (rt *Runtime) DeleteChatStateByChain(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
	rt.runs.CancelForSessions(ctx, chatID, sessionChain)
	rt.cleanupChatState(ctx, chatID, false)
	rt.reapSessions(sessionChain)
}

// CloseChatState tears down a chat's in-memory state WITHOUT touching its durable KAS
// session, so the record survives and reopening it session/loads the history back.
func (rt *Runtime) CloseChatState(ctx context.Context, chatID marotte.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, false)
}

// DischargeWaiting ends a chat's retained waiting_on_user claim: the user has
// answered, so the claim is false. Only that claim — a status the running turn
// declared belongs to that turn. Broadcast as well as cleared, or a second connected
// device paints the dot until its next message_chunk.
func (rt *Runtime) DischargeWaiting(ctx context.Context, chatID marotte.ChatID) {
	if !rt.bus.chatStatus.ClearWaiting(chatID) {
		return
	}
	rt.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventChatStatus, chatID, marotte.ChatStatusPayload{}))
}

// StartTurn opens the chat's turn at bridge-ready, immediately before the call that
// drives it, returning the epoch the caller holds a completion handle on.
func (rt *Runtime) StartTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource) marotte.TurnEpoch {
	if source.UserAnswered() {
		rt.DischargeWaiting(ctx, chatID)
	}
	return rt.coord.StartTurn(ctx, chatID, source)
}

// ReserveTurnForPrompt takes the chat's admission slot for a prompt, waiting up to
// wait while it is held, and keys a refusal on the holder's source.
func (rt *Runtime) ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration) command.AdmissionOutcome {
	return rt.coord.ReserveTurnForPrompt(ctx, chatID, wait)
}

// TryReserveTurn takes the chat's admission slot iff it is free.
func (rt *Runtime) TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	return rt.coord.TryReserveTurn(chatID, source)
}

// ReleaseTurnReservation frees the chat's admission slot and wakes waiters.
func (rt *Runtime) ReleaseTurnReservation(chatID marotte.ChatID) {
	rt.coord.ReleaseTurnReservation(chatID)
}

// AwaitTurn blocks until the named turn has finalized and reports what it did.
func (rt *Runtime) AwaitTurn(ctx context.Context, chatID marotte.ChatID, epoch marotte.TurnEpoch) (marotte.TurnResult, error) {
	return rt.coord.AwaitTurn(ctx, chatID, epoch)
}

// ReleaseTurn gives up the completion handle StartTurn issued.
func (rt *Runtime) ReleaseTurn(chatID marotte.ChatID, epoch marotte.TurnEpoch) {
	rt.coord.ReleaseTurn(chatID, epoch)
}

// SettleTurnOnResponse closes the turn on the response that settled it, once everything
// queued behind that response is consumed and only if the wire's turn_end did not.
func (rt *Runtime) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, epoch marotte.TurnEpoch, seq uint64, resp *marotte.RPCResponse) {
	rt.coord.SettleTurnOnResponse(ctx, chatID, epoch, seq, resp)
}

// TurnOpenedAfter reports whether any turn on the chat opened after epoch.
func (rt *Runtime) TurnOpenedAfter(chatID marotte.ChatID, epoch marotte.TurnEpoch) bool {
	return rt.coord.TurnOpenedAfter(chatID, epoch)
}

// AdmissionHolderSource reports who holds the chat's admission slot: the open turn
// when one is open, else the reservation.
func (rt *Runtime) AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return rt.coord.AdmissionHolderSource(chatID)
}

// FinalizeLocalShellTurn closes a `!cmd` turn marotte ran itself.
func (rt *Runtime) FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, epoch marotte.TurnEpoch) {
	rt.coord.FinalizeLocalShellTurn(ctx, chatID, epoch)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, epoch marotte.TurnEpoch, stop marotte.StopReason, reason string) {
	rt.coord.AbandonInFlightTurn(ctx, chatID, epoch, stop, reason)
}
