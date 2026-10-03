package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// cleanupChatState tears down every in-memory bookkeeping entry for a chat.
// reapDurable is true for a permanent delete (destroys checkpoint history and
// KAS session state); false for the archive path, which must stay reversible.
func (rt *Runtime) cleanupChatState(ctx context.Context, chatID marotte.ChatID, reapDurable bool) {
	rt.bus.ClearPendingPermsForChat(chatID)
	// A workflow step's question keyed to this chat's dock. The chat going away
	// also cancels the runs its sessions launched, so such an ask is answerable by
	// nobody afterwards and replaying it would put a card on a conversation that
	// no longer exists.
	rt.runs.asks.ClearChat(chatID)
	// waiting_on_user survives ClearAtTurnEnd past turn end; the chat going away
	// must clear it too, or a reconnect replays a status for a chat that's gone.
	rt.bus.chatStatus.Clear(chatID)
	rt.beginSteerTeardown(ctx, chatID, false)
	rt.coord.CloseBridge(ctx, chatID, marotte.TurnOutcomeCancelled)
	rt.agentTerms.KillForChat(chatID)
	rt.coord.turns.forget(chatID)
	if reapDurable {
		rt.reapChatSession(ctx, chatID)
	}
	rt.lines.Clear(chatID)
	// The ledger holds whose WORDS an id carried, the record which rows are held;
	// neither clears the other.
	rt.steerLedger.ForgetChat(chatID)
	rt.bus.steers.EndTeardown(chatID)
}

// BeginChatTeardown is command.ChatTeardown's first step, before the teardown's
// cancel: the turn end that cancel causes then resends nothing.
func (rt *Runtime) BeginChatTeardown(chatID marotte.ChatID, keep bool) {
	rt.beginSteerTeardown(rt.lifecycle.shutdownCtx, chatID, keep)
}

// beginSteerTeardown marks the chat's steer record gone and captures its forward
// exit before the registry forgets it. On a close, where the record survives, an
// unread row leaves the note it would have had at a cancel.
func (rt *Runtime) beginSteerTeardown(ctx context.Context, chatID marotte.ChatID, notes bool) {
	unread := rt.bus.steers.BeginTeardown(chatID, rt.coord.turns.forwardExit(chatID))
	if !notes {
		return
	}
	for _, p := range unread {
		rt.coord.recordSteer(durable.Context(ctx), chatID, p.SteerID, &marotte.EntrySteer{
			Text: p.Text, Origin: p.Origin, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
		})
	}
}

// reapChatSession removes the chat's on-disk KAS session state on permanent
// delete, reaping the whole session CHAIN rather than just the current id —
// a chat that changed session (failed load, model-switch fallback) leaves
// state under every id it held.
func (rt *Runtime) reapChatSession(ctx context.Context, chatID marotte.ChatID) {
	c, ok := rt.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	rt.reapSessions(c.SessionChain())
}

// reapSessions removes each session's on-disk KAS state from a captured
// chain, for callers whose chat record is already gone.
func (rt *Runtime) reapSessions(chain []string) {
	if rt.sessionReaper == nil {
		return
	}
	for _, id := range chain {
		rt.sessionReaper.Reap(id)
	}
}

func (rt *Runtime) wireSteerRecords() {
	recs := rt.bus.steers
	recs.broadcast = func(e marotte.ServerEvent) { rt.bus.Broadcast(context.Background(), e) }
	recs.note = func(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
		rt.coord.recordSteer(durable.Context(ctx), chatID, steerID, steer)
	}
	recs.promptHeld = func(chatID marotte.ChatID) bool {
		source, held := rt.coord.AdmissionHolderSource(chatID)
		return held && source.PromptClass()
	}
}
