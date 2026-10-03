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

// CloseBridge closes every turn the bridge hosted with outcome, then stops it.
func (b bridgeRole) CloseBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	b.coord.CloseBridge(ctx, chatID, outcome)
}

// BridgeLive reports a bridge for the chat that is past its spawn.
func (b bridgeRole) BridgeLive(chatID marotte.ChatID) bool { return b.coord.bridgeLive(chatID) }

// DeleteChatState tears down all in-memory state for a chat being permanently deleted,
// cancelling its runs and reaping its durable KAS session.
func (rt *Runtime) DeleteChatState(ctx context.Context, chatID marotte.ChatID) {
	rt.beginSteerTeardown(ctx, chatID, false)
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, true)
}

// DeleteChatStateByChain is DeleteChatState for a chat whose record is already gone:
// the close escalation deletes the record inside its commit, so the session chain is
// captured beforehand rather than re-read from a record that would silently no-op.
func (rt *Runtime) DeleteChatStateByChain(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
	rt.beginSteerTeardown(ctx, chatID, false)
	rt.runs.CancelForSessions(ctx, chatID, sessionChain)
	rt.cleanupChatState(ctx, chatID, false)
	rt.reapSessions(sessionChain)
}

// CloseChatState tears down a chat's in-memory state WITHOUT touching its durable KAS
// session, so the record survives and reopening it session/loads the history back.
func (rt *Runtime) CloseChatState(ctx context.Context, chatID marotte.ChatID) {
	rt.beginSteerTeardown(ctx, chatID, true)
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

// OpenTurn appends the turn_open at admission and creates the turn's registry
// record in one operation; a source that answers the agent discharges the chat's
// retained waiting_on_user claim, because the turn_open is the answer.
func (rt *Runtime) OpenTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, prompt *marotte.EntryPrompt, init func(*marotte.Chat)) (string, error) {
	id, err := rt.coord.OpenTurn(ctx, chatID, source, prompt, init)
	if err == nil && source.UserAnswered() {
		rt.DischargeWaiting(ctx, chatID)
	}
	return id, err
}

// StartTurn stamps the model and the credit baseline onto the turn the id names,
// with the bridge live, immediately before the call that drives it.
func (rt *Runtime) StartTurn(ctx context.Context, chatID marotte.ChatID, turnID string) bool {
	return rt.coord.StartTurn(ctx, chatID, turnID)
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
func (rt *Runtime) AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	return rt.coord.AwaitTurn(ctx, chatID, turnID)
}

// ReleaseTurn gives up the completion handle OpenTurn issued.
func (rt *Runtime) ReleaseTurn(chatID marotte.ChatID, turnID string) {
	rt.coord.ReleaseTurn(chatID, turnID)
}

// SettleTurnOnResponse closes the turn on the response that settled it, once everything
// queued behind that response is consumed and only if the wire's turn_end did not.
func (rt *Runtime) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	rt.coord.SettleTurnOnResponse(ctx, chatID, turnID, seq, resp)
}

// TurnOpenedAfter reports whether any own turn on the chat opened after turnID.
func (rt *Runtime) TurnOpenedAfter(chatID marotte.ChatID, turnID string) bool {
	return rt.coord.TurnOpenedAfter(chatID, turnID)
}

// AdmissionHolderSource reports who holds the chat's admission slot: the open turn
// when one is open, else the reservation.
func (rt *Runtime) AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return rt.coord.AdmissionHolderSource(chatID)
}

// FinalizeLocalShellTurn closes a `!cmd` turn marotte ran itself, appending its
// one text entry first.
func (rt *Runtime) FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, turnID, output string) {
	rt.coord.FinalizeLocalShellTurn(ctx, chatID, turnID, output)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string) {
	rt.coord.AbandonInFlightTurn(ctx, chatID, turnID, stop, reason)
}
