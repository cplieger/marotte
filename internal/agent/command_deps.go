package agent

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// bridgeRole adapts the coordinator to command.bridgeAccess, checking for a nil *sharedBridge,
// which would become a non-nil interface.
type bridgeRole struct{ coord *bridgeCoordinator }

// Bridge returns the chat's bridge once its door has attached a session, or nil: a record still
// in its door has no session to address, so a command sees no bridge there.
func (b bridgeRole) Bridge(chatID marotte.ChatID) command.Bridge {
	sb := b.coord.bridgeFor(chatID)
	if sb == nil || !sb.hasSession() {
		return nil
	}
	return sb
}

// OpenBridge ensures a bridge exists for the chat (typed-nil checked), with the caller's open hold on it.
func (b bridgeRole) OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (command.Bridge, func(), error) {
	sb, release, err := b.coord.openBridge(ctx, chatID, model)
	if err != nil || sb == nil {
		return nil, release, err
	}
	return sb, release, nil
}

// OpenBridgeAt is OpenBridge for the holder of res.
func (b bridgeRole) OpenBridgeAt(
	ctx context.Context, chatID marotte.ChatID, model string, res command.Reservation,
) (command.Bridge, func(), error) {
	sb, release, err := b.coord.openBridgeAt(ctx, chatID, model, res)
	if err != nil || sb == nil {
		return nil, release, err
	}
	return sb, release, nil
}

// RefreshForRetry abandons the empty turn's session as one lease on its entry (bridgeCoordinator.refreshForRetry).
func (b bridgeRole) RefreshForRetry(ctx context.Context, chatID marotte.ChatID, sb command.Bridge) error {
	entry, ok := sb.(*sharedBridge)
	if !ok {
		return marotte.ErrSessionClosed
	}
	return b.coord.refreshForRetry(ctx, chatID, entry)
}

// BridgeLive reports a bridge for the chat that is past its session open.
func (b bridgeRole) BridgeLive(chatID marotte.ChatID) bool { return b.coord.bridgeLive(chatID) }

// CallBudget is the engine owner's stall-aware budget (engineOwner.stallAware).
func (b bridgeRole) CallBudget(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return b.coord.engine.stallAware(ctx, d)
}

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
	rt.AwaitSessionLeases(ctx, chatID)
	rt.runs.deleteForSessions(ctx, chatID, sessionChain, stop)
	rt.deleteSessions(ctx, chatID, sessionChain)
	rt.cleanupChatState(ctx, chatID)
	rt.reapSessions(sessionChain)
}

// CloseChatState tears down in-memory state without touching the KAS session, so a reopen session/loads it, and
// stops the chat's runs under stop, a person's close's stop hold, when it is set (Runs.stopChatRuns).
func (rt *Runtime) CloseChatState(ctx context.Context, chatID marotte.ChatID, stop *chat.Claim) {
	rt.beginSteerTeardown(ctx, chatID, true)
	if stop != nil {
		rt.runs.stopChatRuns(ctx, chatID, stop)
	}
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
	id, err := rt.coord.openTurn(ctx, chatID, open)
	if err == nil && open.UserAnswered() {
		rt.DischargeWaiting(ctx, chatID)
	}
	return id, err
}

// StartTurn stamps the model and credit baseline onto the turn, with the bridge live, just before the call.
func (rt *Runtime) StartTurn(ctx context.Context, turn command.TurnRef, cancel context.CancelCauseFunc) bool {
	return rt.coord.startTurn(ctx, turn, cancel)
}

// EndPromptCall forgets the turn's call once it returned.
func (rt *Runtime) EndPromptCall(turn command.TurnRef) {
	rt.coord.endPromptCall(turn)
}

// LifeStamp is the life gate's position now.
func (rt *Runtime) LifeStamp() uint64 { return rt.coord.LifeStamp() }

// AwaitNoClose waits until no close of chatID is held.
func (rt *Runtime) AwaitNoClose(ctx context.Context, chatID marotte.ChatID) (bool, error) {
	return rt.coord.AwaitNoClose(ctx, chatID)
}

// ReserveTurnForPrompt takes the chat's admission slot for a prompt, waiting up to wait; a refusal keys on the holder's source.
func (rt *Runtime) ReserveTurnForPrompt(
	ctx context.Context, chatID marotte.ChatID, wait time.Duration, stamp uint64, messageID string,
) (command.AdmissionOutcome, command.Reservation) {
	return rt.coord.ReserveTurnForPrompt(ctx, chatID, wait, stamp, messageID)
}

// TryReserveIdleTurn takes the chat's admission slot iff no turn is open and nothing holds it.
func (rt *Runtime) TryReserveIdleTurn(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	return rt.coord.TryReserveIdleTurn(chatID, source, stamp)
}

// ReserveIdleTurn takes the chat's admission slot once no turn holds it, waiting up to wait for a transient holder.
func (rt *Runtime) ReserveIdleTurn(
	ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, wait time.Duration,
) (command.IdleAdmission, command.Reservation) {
	return rt.coord.ReserveIdleTurn(ctx, chatID, source, stamp, wait)
}

// ReserveTurn takes the chat's admission slot, waiting up to wait for a holder that is not a running turn.
func (rt *Runtime) ReserveTurn(
	ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64, wait time.Duration,
) (command.IdleAdmission, command.Reservation) {
	return rt.coord.ReserveTurn(ctx, chatID, source, stamp, wait)
}

// TryReserveDrain is a close's drain picking its prompt (bridgeCoordinator.TryReserveDrain).
func (rt *Runtime) TryReserveDrain(chatID marotte.ChatID, cl command.CloseFacts) (command.Reservation, bool) {
	return rt.coord.TryReserveDrain(chatID, cl)
}

// PromptHolder names the prompt-class turn holding the chat's admission.
func (rt *Runtime) PromptHolder(chatID marotte.ChatID) (string, bool) {
	return rt.coord.PromptHolder(chatID)
}

// ReleaseTurnReservation frees the chat's admission slot and wakes waiters.
func (rt *Runtime) ReleaseTurnReservation(res command.Reservation) {
	rt.coord.ReleaseTurnReservation(res)
}

// AwaitTurn blocks until the named turn has finalized and reports what it did.
func (rt *Runtime) AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	return rt.coord.awaitTurn(ctx, chatID, turnID)
}

// AwaitTurnBound blocks until KAS binds the named turn's prompt or the turn finalizes unbound.
func (rt *Runtime) AwaitTurnBound(ctx context.Context, chatID marotte.ChatID, turnID string) (bool, error) {
	return rt.coord.awaitTurnBound(ctx, chatID, turnID)
}

// ReleaseTurn gives up the completion handle OpenTurn issued.
func (rt *Runtime) ReleaseTurn(chatID marotte.ChatID, turnID string) {
	rt.coord.releaseTurn(chatID, turnID)
}

// SettleTurnOnResponse closes the turn on its settling response once the queue behind it is consumed, unless turn_end got there first.
func (rt *Runtime) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	rt.coord.settleTurnOnResponse(ctx, chatID, turnID, seq, resp)
}

// TurnOpenedAfter reports whether any own turn on the chat opened after turnID.
func (rt *Runtime) TurnOpenedAfter(chatID marotte.ChatID, turnID string) bool {
	return rt.coord.turnOpenedAfter(chatID, turnID)
}

// TurnIsOpen reports whether turnID is an open, non-finalizing turn of the chat's live lifecycle.
func (rt *Runtime) TurnIsOpen(chatID marotte.ChatID, turnID string) bool {
	return rt.coord.turns.isOpen(chatID, turnID)
}

// StopRequestedAfter reports whether a stop was requested after turnID opened.
func (rt *Runtime) StopRequestedAfter(chatID marotte.ChatID, turnID string) bool {
	return rt.coord.stopRequestedAfter(chatID, turnID)
}

// RequestStop records a stop for cause and captures the turns it targets.
func (rt *Runtime) RequestStop(chatID marotte.ChatID, cause marotte.TurnStopCause) *command.TurnStop {
	return rt.coord.RequestStop(chatID, cause)
}

// ReleaseStop lifts the stop's hold on the chat's sends.
func (rt *Runtime) ReleaseStop(stop *command.TurnStop) {
	rt.coord.ReleaseStop(stop)
}

// ArmStopGrace backstops the stop's captured targets with the grace budget d.
func (rt *Runtime) ArmStopGrace(stop *command.TurnStop, d time.Duration) bool {
	return rt.coord.ArmStopGrace(stop, d)
}

// AdmissionHolderSource reports who holds the admission slot: the open turn, else the reservation.
func (rt *Runtime) AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return rt.coord.AdmissionHolderSource(chatID)
}

// AwaitCompaction holds a prompt until the chat's running compaction returns.
func (rt *Runtime) AwaitCompaction(ctx context.Context, chatID marotte.ChatID) {
	rt.coord.autoCompact.awaitRunning(ctx, chatID)
}

// RequestCompact is the reader's compaction, owned by the chat's compactor.
func (rt *Runtime) RequestCompact(ctx context.Context, chatID marotte.ChatID) (command.CompactOutcome, error) {
	return rt.coord.autoCompact.request(ctx, chatID)
}

// CancelCompact drops the chat's queued compaction.
func (rt *Runtime) CancelCompact(ctx context.Context, chatID marotte.ChatID) error {
	return rt.coord.autoCompact.cancel(ctx, chatID)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string, kind marotte.FailureKind, seq uint64) {
	rt.coord.abandonInFlightTurn(ctx, chatID, turnID, stop, reason, kind, seq)
}

// ProviderUnreachable returns the vault's reason while the identity provider gives the engine's account no usable
// renewal.
func (rt *Runtime) ProviderUnreachable() (string, bool) { return rt.signin.providerDown() }

var _ command.SessionRenamer = (*Runtime)(nil)

// RenameSession renames a session, resident or not, with an engine call.
func (rt *Runtime) RenameSession(ctx context.Context, sessionID, title string) error {
	raw, err := rt.engine.renameSessionRaw(ctx, sessionID, title)
	if err != nil {
		return err
	}
	return command.RenameResultOutcome(raw)
}
