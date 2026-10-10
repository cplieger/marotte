package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// In-memory state only; durable state is the delete grade's.
func (rt *Runtime) cleanupChatState(ctx context.Context, chatID marotte.ChatID) {
	// The chat's runs end with it, so its step asks are answerable by nobody.
	rt.runs.asks.clearChat(chatID)
	// waiting_on_user outlives ClearAtTurnEnd, so clear it here.
	rt.bus.chatStatus.clear(chatID)
	rt.beginSteerTeardown(ctx, chatID, false)
	rt.coord.closeBridge(ctx, chatID, marotte.TurnOutcomeCancelled)
	rt.agentTerms.killForChat(chatID)
	rt.coord.turns.forget(chatID)
	rt.coord.autoCompact.forget(chatID)
	rt.lines.Clear(chatID)
	// The ledger and the record are separate; neither clears the other.
	rt.steerLedger.ForgetChat(chatID)
	rt.bus.steers.endTeardown(chatID)
}

// BeginChatTeardown is command.chatTeardown's first step, before its cancel, so that turn end resends nothing.
func (rt *Runtime) BeginChatTeardown(chatID marotte.ChatID, keep bool) {
	rt.beginSteerTeardown(rt.lifecycle.shutdownCtx, chatID, keep)
}

// The lifecycle is re-fenced first and the record marked under the steer lock; on a close an unread
// row no opened resend carries leaves its cancel note.
func (rt *Runtime) beginSteerTeardown(ctx context.Context, chatID marotte.ChatID, notes bool) {
	rt.coord.turns.refence(chatID)
	if rt.steerQueue.locks != nil && !rt.bus.steers.gone(chatID) {
		lockCtx, cancel := context.WithTimeout(ctx, command.ResendBridgeWait)
		unlock, err := rt.steerQueue.LockSteerOps(lockCtx, chatID)
		cancel()
		if err != nil {
			slog.Warn("chat teardown: a steer operation still held the chat; marking it gone anyway", "chat_id", chatID)
		} else {
			defer unlock()
		}
	}
	unread := rt.bus.steers.beginTeardown(chatID, rt.coord.turns.forwardExit(chatID))
	if !notes || len(unread) == 0 {
		return
	}
	resent := rt.resentKeys(durable.Context(ctx), chatID)
	for _, p := range unread {
		if resent[p.SteerID] {
			continue
		}
		rt.coord.recordSteer(durable.Context(ctx), chatID, p.SteerID, &marotte.EntrySteer{
			Text: p.Text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
		})
	}
}

// sessionDeleteTimeout bounds one session/delete: KAS quiesces an active turn first.
const sessionDeleteTimeout = 30 * time.Second

// deleteSessions sends session/delete for every id in the chain on the chat's live bridge, else
// the utility session. A refusal Warns; Reap is the fallback.
func (rt *Runtime) deleteSessions(ctx context.Context, chatID marotte.ChatID, chain []string) {
	sb := rt.bridge.mgr.get(chatID)
	if sb != nil && !sb.startedPastSpawn() {
		sb = nil
	}
	for _, id := range chain {
		if id == "" {
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, sessionDeleteTimeout)
		var err error
		if sb != nil {
			err = runCallErr(sb.Call(dctx, marotte.MethodSessionDelete, map[string]any{marotte.KeySessionID: id}))
		} else {
			// Raw params: scopedParams would inject the utility's own session id.
			_, err = rt.utility.get().session.rawCall(dctx, "session delete", marotte.MethodSessionDelete,
				callerParams(map[string]any{marotte.KeySessionID: id}))
		}
		cancel()
		if err != nil {
			slog.Warn("delete: session delete failed; the reaper removes it", "session_id", id, "chat_id", chatID, "error", rpcerr.Text(err))
		}
	}
}

// reapSessions removes each session's on-disk KAS state, after deleteSessions.
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
		if _, step := chatID.StepSession(); step {
			rt.runs.noteStepSteer(durable.Context(ctx), chatID, steerID, steer)
			return
		}
		rt.coord.recordSteer(durable.Context(ctx), chatID, steerID, steer)
	}
	recs.stepEvent = rt.runs.steers.event
	recs.stepJob = rt.runStepSteerJob
	recs.promptHeld = func(chatID marotte.ChatID) bool {
		source, held := rt.coord.AdmissionHolderSource(chatID)
		return held && source.PromptClass()
	}
}
