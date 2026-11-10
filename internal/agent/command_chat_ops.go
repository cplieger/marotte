package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/runlease"
)

// cleanupChatState drops the chat's in-memory state; durable state is the delete grade's. Order is the contract:
// the life is ended and the bridge released and drained before anything is forgotten, and nothing is forgotten
// while the life's work runs (chatLifecycle owns that rule).
func (rt *Runtime) cleanupChatState(ctx context.Context, chatID marotte.ChatID) {
	// waiting_on_user outlives ClearAtTurnEnd, so clear it here.
	rt.bus.chatStatus.clear(chatID)
	lc, lived := rt.beginSteerTeardown(ctx, chatID, false)
	rt.teardownStep("begun")
	rt.coord.closeBridge(ctx, chatID, marotte.TurnOutcomeCancelled)
	rt.teardownStep("bridge closed")
	if lived {
		lc.awaitQuiet(rt.lifecycle.shutdownCtx, budgetLeft(ctx))
	}
	rt.teardownStep("quiet")
	rt.agentTerms.killForChat(chatID)
	rt.coord.turns.forget(chatID)
	rt.lines.Clear(chatID)
	// The ledger and the record are separate; neither clears the other.
	rt.steerLedger.ForgetChat(chatID)
	rt.teardownStep("forgotten")
	rt.bus.steers.endTeardown(chatID)
}

// teardownStep runs the test hook between cleanupChatState's steps; nil in production.
func (rt *Runtime) teardownStep(step string) {
	if hook := rt.teardownHook.Load(); hook != nil {
		(*hook)(step)
	}
}

// lifeWarnAfter is when a teardown with no deadline names the life's work it still waits for.
const lifeWarnAfter = time.Minute

// budgetLeft is what remains of the job's budget, after which a wait for the life's work logs its WARN and goes on
// waiting.
func budgetLeft(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return max(time.Until(deadline), 0)
	}
	return lifeWarnAfter
}

// chatHold is command.ChatHold over the turn registry's life gate.
type chatHold struct {
	hold   *gateHold
	turns  *turnRegistry
	chatID marotte.ChatID
}

// Commit records the close at the gate and ends the chat's life in the same critical section of the commit.
func (h chatHold) Commit() {
	if h.hold.commit() {
		h.turns.endLife(h.chatID)
	}
}

func (h chatHold) Release() { h.hold.release() }

// HoldChat holds chatID's life gate for a close being decided (chatGates owns the contract).
func (rt *Runtime) HoldChat(chatID marotte.ChatID) command.ChatHold {
	return chatHold{hold: rt.coord.turns.gates.hold(chatID), turns: rt.coord.turns, chatID: chatID}
}

// CloseImpact reads what closing each chat stops, from in-memory and cached state only (command.closeImpactReader).
func (rt *Runtime) CloseImpact(ctx context.Context, chatIDs []marotte.ChatID) map[marotte.ChatID]command.CloseWork {
	var leases []runlease.Lease
	if rt.runs != nil {
		leases = rt.runs.leaseStore().List()
	}
	out := make(map[marotte.ChatID]command.CloseWork, len(chatIDs))
	for _, chatID := range chatIDs {
		out[chatID] = command.CloseWork{
			Turns:  rt.coord.turns.closeWork(chatID),
			Asks:   rt.bus.pendingPerms.idsForChat(chatID),
			Runs:   chatLiveRuns(leases, chatID),
			Steers: rt.bus.steers.unreadKeys(chatID),
			Queued: rt.coord.queuedPromptsPending(ctx, chatID),
		}
	}
	return out
}

// AwaitSessionLeases waits until every lease on the chat's session admitted before its close committed has
// returned; the commit cut each, so the teardown's own cancel and release are the only session access after it.
func (rt *Runtime) AwaitSessionLeases(ctx context.Context, chatID marotte.ChatID) {
	if lc, ok := rt.coord.turns.lookup(chatID); ok {
		lc.awaitWork(rt.lifecycle.shutdownCtx, workSession, budgetLeft(ctx))
	}
}

// TeardownCancel sends the chat's session/cancel as the teardown's closer, after AwaitSessionLeases. A chat with no
// session has nothing to cancel.
func (rt *Runtime) TeardownCancel(ctx context.Context, chatID marotte.ChatID) error {
	sb := rt.coord.bridgeFor(chatID)
	if sb == nil {
		return nil
	}
	var err error
	sb.closeOwned(func(s engineSession) {
		err = s.Notify(ctx, marotte.MethodCancel, map[string]any{marotte.KeySessionID: s.SessionID()})
	})
	return err
}

// BeginChatTeardown is command.chatTeardown's first step, before its cancel, so that turn end resends nothing.
func (rt *Runtime) BeginChatTeardown(chatID marotte.ChatID, keep bool) {
	rt.beginSteerTeardown(rt.lifecycle.shutdownCtx, chatID, keep)
}

// beginSteerTeardown ends the chat's life (the commit already did; this is idempotent) and marks the steer record
// gone under the steer lock; on a close an unread row no opened resend carries leaves its cancel note. It returns
// the ended lifecycle, false when the chat has none.
func (rt *Runtime) beginSteerTeardown(ctx context.Context, chatID marotte.ChatID, notes bool) (*chatLifecycle, bool) {
	lc, lived := rt.coord.turns.endLife(chatID)
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
		return lc, lived
	}
	resent := rt.resentKeys(durable.Context(ctx), chatID)
	for i := range unread {
		p := &unread[i]
		if resent[p.SteerID] {
			continue
		}
		rt.coord.recordSteer(durable.Context(ctx), chatID, p.SteerID, &marotte.EntrySteer{
			Text: p.Text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
			Caller: p.Caller,
		})
	}
	return lc, lived
}

// sessionDeleteTimeout bounds one session/delete, extended through an engine stall (engineOwner.stallAware): KAS
// quiesces an active turn first. A var for tests.
var sessionDeleteTimeout = 30 * time.Second

// deleteSessions sends an engine-level session/delete for every id in the chain. A refusal or a timeout Warns; Reap is
// the fallback.
func (rt *Runtime) deleteSessions(ctx context.Context, chatID marotte.ChatID, chain []string) {
	for _, id := range chain {
		if id == "" {
			continue
		}
		dctx, cancel := rt.engine.stallAware(ctx, sessionDeleteTimeout)
		_, err := rt.engine.rawCall(dctx, "session delete", marotte.MethodSessionDelete,
			map[string]any{marotte.KeySessionID: id})
		late := errors.Is(dctx.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case err != nil && late:
			slog.Warn("delete: kiro-cli did not answer session/delete in time and may still apply it; the reaper removes its files either way",
				"session_id", id, "chat_id", chatID)
		case err != nil:
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
