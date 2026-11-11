package agent

// A chat's compaction is an entry of its run list (run_list.go): the reader's button, a turn whose context reached
// the point, or a turn's end inserts one, and the list's taker starts it once nothing runs ahead of it. KAS's own
// compaction is off on every chat session.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// compactCallBudget bounds one compaction call. Above KAS's 300 s _kiro/session/compact bound, so it only covers a
// wedged bridge. A var for tests.
var compactCallBudget = 310 * time.Second

// compactFailed is the toast for a compaction the dock showed that KAS refused or that failed.
const compactFailed = "The compaction did not run: the agent was busy, or the summary failed or the chat changed before it finished"

// compactHandoffReason is the account of a fold-opened turn a compaction's start closed to make its call.
const compactHandoffReason = "The agent's turn had no start marker, so marotte closed it to compact the context."

type autoCompactor struct {
	turns *turnRegistry
	// bridgeOf is the chat's live entry, nil for none; every compaction call is one lease on it.
	bridgeOf func(marotte.ChatID) *sharedBridge
	usage    func(context.Context, marotte.ChatID) marotte.Usage
	policy   func(context.Context) (enabled bool, pct int)
	compact  func(context.Context, command.CompactBridge) (bool, uint64, error)
	// budget bounds one call, extended through an engine stall; folded waits until the frames before a reply's lane
	// position are folded. Both nil in tests.
	budget func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	folded func(ctx context.Context, chatID marotte.ChatID, seq uint64) bool
	// failed tells the chat's pages that a compaction the dock showed did not run.
	failed func(ctx context.Context, chatID marotte.ChatID, detail string)
	// asksOpen reports an ask the chat's own turn is parked on (a permission, a question, an elicitation).
	asksOpen func(marotte.ChatID) bool
	// carryOutStop carries out the failsafe's stop as a Cancel does (command.Dispatcher.CarryOutStop).
	carryOutStop func(context.Context, marotte.ChatID, command.RunStop) error
	// spawn runs the failsafe's stop off the read loop that reported the reading.
	spawn func(func())
}

func newAutoCompactor(
	bc *bridgeCoordinator, policy func(context.Context) (enabled bool, pct int),
	asksOpen func(marotte.ChatID) bool, carryOutStop func(context.Context, marotte.ChatID, command.RunStop) error,
) *autoCompactor {
	return &autoCompactor{
		turns: bc.turns,
		bridgeOf: func(chatID marotte.ChatID) *sharedBridge {
			if !bc.bridgeLive(chatID) {
				return nil
			}
			if sb := bc.bridgeFor(chatID); sb != nil && sb.hasSession() {
				return sb
			}
			return nil
		},
		usage: func(ctx context.Context, chatID marotte.ChatID) marotte.Usage {
			if chat, ok := bc.chatStore.Get(ctx, chatID); ok {
				return chat.Usage
			}
			return marotte.Usage{}
		},
		policy:  policy,
		compact: command.Compact,
		budget:  bc.engine.stallAware,
		folded:  bc.turns.awaitPosition,
		failed: func(ctx context.Context, chatID marotte.ChatID, detail string) {
			bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
				Code: marotte.ErrCodeCompactionFailed, Message: detail,
			}))
		},
		asksOpen:     asksOpen,
		carryOutStop: carryOutStop,
		spawn:        bc.spawnRun,
	}
}

// ObserveContextUsage hands each live context reading KAS reported to the chat's compactor.
func (bc *bridgeCoordinator) ObserveContextUsage(ctx context.Context, chatID marotte.ChatID, pct float64) {
	bc.autoCompact.noteUsage(ctx, chatID, pct)
}

// ObserveToolCall hands each tool call the chat's own agent opened to the chat's compactor.
func (bc *bridgeCoordinator) ObserveToolCall(chatID marotte.ChatID) {
	bc.autoCompact.noteToolCall(chatID)
}

// noteToolCall records that the chat's running turn went on past its last reading.
func (a *autoCompactor) noteToolCall(chatID marotte.ChatID) {
	if a == nil {
		return
	}
	lc, ok := a.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	lc.toolCallTurn = lc.compactTurnLocked()
	lc.mu.Unlock()
}

// noteUsage lists the running turn's compaction the first time a reading inside it reaches the point, once per turn
// and never after the reader cancelled that turn's (runList.crossing). A reading at compactionFailsafePct stops the
// turn to compact and resume it (runList.failsafe) when the response it measures called a tool, so the turn goes on;
// while an ask is open the stop is owed until the asks settle (askSettled) or a later response's reading replaces it.
// One under the point re-arms the failsafe.
func (a *autoCompactor) noteUsage(ctx context.Context, chatID marotte.ChatID, pct float64) {
	if a == nil {
		return
	}
	lc, ok := a.turns.lookup(chatID)
	if !ok {
		return
	}
	enabled, point := a.policy(ctx)
	lc.mu.Lock()
	goesOn := lc.toolCallTurn
	lc.toolCallTurn = ""
	// Only a new response replaces an owed stop: KAS sends each response's reading twice (the plain frame, then its
	// breakdown twin), and no model call runs while an ask is open, so a tool-free reading then is the twin.
	if goesOn != "" {
		lc.run.failsafeOwed = ""
	}
	if !compactsLocked(lc, enabled) {
		lc.mu.Unlock()
		return
	}
	if pct < float64(point) {
		lc.run.usageUnderThePoint()
		lc.mu.Unlock()
		return
	}
	turn := lc.compactTurnLocked()
	listCrossingLocked(lc, turn, pct)
	var rs command.RunStop
	if pct >= compactionFailsafePct && goesOn == turn {
		// Read under lc.mu, as fireOwed reads it: an ask settling between this reading and the owed mark would
		// otherwise find nothing owed, and the turn would run on to the limit.
		if a.asksOpen(chatID) {
			lc.run.failsafeOwed = turn
		} else {
			rs = a.failsafeLocked(lc, turn)
		}
	}
	lc.wakeOnChangeLocked()
	lc.mu.Unlock()
	a.carryOutFailsafe(lc, rs, "turn", turn, "context_pct", pct)
}

// askSettled fires the stop a reading owed while the chat's turn waited on an ask, once the chat session's last
// open ask has settled (answered, denied, withdrawn or ended): the response's next model call is the one that would
// overflow, so the stop cannot wait for another reading. KAS can open a tool's ask before its response's reading
// (processChunkStream dispatches a finished tool block inside the stream loop and reports usage after it).
func (a *autoCompactor) askSettled(chatID marotte.ChatID) {
	if a == nil {
		return
	}
	a.spawn(func() { a.fireOwed(chatID) })
}

func (a *autoCompactor) fireOwed(chatID marotte.ChatID) {
	lc, ok := a.turns.lookup(chatID)
	if !ok {
		return
	}
	enabled, _ := a.policy(lc.ctx)
	lc.mu.Lock()
	turn := lc.run.failsafeOwed
	var rs command.RunStop
	if turn != "" && compactsLocked(lc, enabled) && !a.asksOpen(chatID) {
		lc.run.failsafeOwed = ""
		rs = a.failsafeLocked(lc, turn)
	}
	lc.wakeOnChangeLocked()
	lc.mu.Unlock()
	a.carryOutFailsafe(lc, rs, "turn", turn, "owed", true)
}

// carryOutFailsafe carries out a fired failsafe's stop off the caller's goroutine.
func (a *autoCompactor) carryOutFailsafe(lc *chatLifecycle, rs command.RunStop, attrs ...any) {
	if rs.Arm != command.StopCancel {
		return
	}
	slog.Warn("compaction failsafe: stopping the turn to compact it", append([]any{"chat_id", lc.chatID}, attrs...)...)
	a.spawn(func() { a.stopForCompaction(lc, rs) })
}

// compactsLocked reports whether a reading may compact lc's chat now: the policy is on and a turn is live.
// Caller holds lc.mu.
func compactsLocked(lc *chatLifecycle, enabled bool) bool {
	return enabled && !lc.ended && lc.liveLockedState()
}

// listCrossingLocked lists turn's compaction when a reading inside it reaches the point for the first time.
// Caller holds lc.mu.
func listCrossingLocked(lc *chatLifecycle, turn string, pct float64) {
	if lc.run.liveLen() == 0 || !lc.run.crossing(turn) {
		return
	}
	if _, joined := lc.run.requestCompaction(marotte.RunFromThreshold, turn); !joined {
		slog.Info("auto-compaction listed: the context reached the point", "chat_id", lc.chatID, "context_pct", pct)
	}
}

// failsafeLocked fires the failsafe in turn, answering the stop to carry out (StopNothing when it did not fire).
// Caller holds lc.mu.
func (a *autoCompactor) failsafeLocked(lc *chatLifecycle, turn string) command.RunStop {
	token := a.turns.stopTokens.Add(1)
	plan := lc.run.failsafe(failsafeFacts{turnID: turn, resumeID: "m-" + newMessageID(), token: token})
	if plan.arm != stopRunning {
		return command.RunStop{}
	}
	rs, _ := a.turns.carryStopLocked(lc, plan, token, marotte.TurnStopCompaction)
	return rs
}

// stopForCompaction carries out the failsafe's stop. A cancel not delivered was undone (runList.stopUndone), so the
// turn runs on and compacts at its end.
func (a *autoCompactor) stopForCompaction(lc *chatLifecycle, rs command.RunStop) {
	if err := a.carryOutStop(lc.ctx, lc.chatID, rs); err != nil {
		slog.Warn("compaction failsafe: the turn could not be stopped; it compacts at its end",
			"chat_id", lc.chatID, "error", err)
	}
}

// turnEndCompaction is the compaction a closing turn's end asks for: the model refused the turn for the context's
// size, or the reading is at the point; "" for none or with the policy off.
func (a *autoCompactor) turnEndCompaction(ctx context.Context, chatID marotte.ChatID, contextLimit bool) marotte.RunEntryOrigin {
	if a == nil || a.bridgeOf(chatID) == nil {
		return ""
	}
	enabled, point := a.policy(ctx)
	switch {
	case !enabled:
		return ""
	case contextLimit:
		return marotte.RunFromContextLimit
	}
	if u := a.usage(ctx, chatID); u.HasRealData && u.ContextPct >= float64(point) {
		return marotte.RunFromTurnEnd
	}
	return ""
}

// request is the reader's compaction: it joins the chat's compaction, or lists one. One behind anything live, or
// one the taker could not start now, answers queued at once; one that started (or a running one it joined) is waited
// for up to compactCallBudget, so an idle press answers once the summary commits.
func (a *autoCompactor) request(ctx context.Context, chatID marotte.ChatID) (command.CompactOutcome, error) {
	if a.bridgeOf(chatID) == nil {
		return command.CompactNoSession, nil
	}
	lc, err := a.turns.lifecycleAt(ctx, chatID, a.turns.gates.stamp())
	if err != nil {
		return command.CompactRefused, err
	}
	lc.mu.Lock()
	if lc.ended {
		lc.mu.Unlock()
		return command.CompactRefused, marotte.ErrChatClosedDuringCall
	}
	e, _ := lc.run.requestCompaction(marotte.RunFromRequest, lc.compactTurnLocked())
	lc.wakeLocked()
	if !e.live() {
		lc.mu.Unlock()
		return command.CompactQueued, nil
	}
	e.waiter = true
	done := e.done
	lc.mu.Unlock()
	wait, cancel := context.WithTimeout(ctx, compactCallBudget)
	defer cancel()
	select {
	case <-done.ch:
	case <-wait.Done():
		lc.mu.Lock()
		if !done.closed {
			e.waiter = false
		}
		lc.mu.Unlock()
		slog.Warn("stopped waiting for the running compaction", "chat_id", chatID, "error", wait.Err())
		return command.CompactRefused, wait.Err()
	}
	switch {
	case done.err != nil:
		return command.CompactRefused, done.err
	case done.accepted:
		return command.CompactAccepted, nil
	case done.queued:
		return command.CompactQueued, nil
	}
	return command.CompactRefused, nil
}

// cancel withdraws the chat's compaction entry with that id ("" names the chat's one): a pending one goes and its
// turn compacts nothing unasked, a running one is not stopped but never comes back queued or toasts.
func (a *autoCompactor) cancel(_ context.Context, chatID marotte.ChatID, id string) error {
	lc, ok := a.turns.lookup(chatID)
	if !ok {
		return nil
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if id == "" {
		if c := lc.run.compaction(); c != nil {
			id = c.id
		}
	}
	lc.run.cancelCompaction(id)
	lc.wakeLocked()
	return nil
}

// testHookAfterCompactionReturn runs once a compaction's call has returned, before its end is applied: the window
// where a request racing the compaction's end lands.
var testHookAfterCompactionReturn = func(marotte.ChatID) {}

// testHookBeforeCompactionStart runs once a compaction is taken, before its start's first section: the window where
// a request racing the compaction's start lands.
var testHookBeforeCompactionStart = func(marotte.ChatID) {}

// runCompaction is a taken compaction's start (runList.compactionStart): behind a fold-opened turn it closes that
// turn first and waits for its entry to end, then makes the call and applies its end (runList.compactionEnded).
// Every check is made under lc.mu and every wait with it released.
func (bc *bridgeCoordinator) runCompaction(lc *chatLifecycle, id string) {
	testHookBeforeCompactionStart(lc.chatID)
	if !bc.compactionReady(lc, id) {
		return
	}
	a := bc.autoCompact
	var (
		accepted bool
		err      = marotte.ErrSessionClosed
	)
	if b := a.bridgeOf(lc.chatID); b != nil {
		accepted, err = a.call(lc.ctx, lc.chatID, b, a.usage(lc.ctx, lc.chatID).ContextPct)
	}
	testHookAfterCompactionReturn(lc.chatID)
	lc.mu.Lock()
	lc.run.compactionEnded(id, accepted, err)
	lc.wakeLocked()
	lc.mu.Unlock()
}

// compactionReady runs the compaction's start until its call may be written, reporting false when the start
// abandoned it or the life ended.
func (bc *bridgeCoordinator) compactionReady(lc *chatLifecycle, id string) bool {
	for {
		lc.mu.Lock()
		f := lc.orderFactsLocked()
		step, fold := lc.run.compactionStart(id, &f)
		var claimed *activeTurn
		if step == compactCloseFold {
			if t := lc.liveLocked(fold.turnID); t != nil && lc.claimIfLocked(t, nil) {
				claimed = t
			}
		}
		lc.wakeOnChangeLocked()
		changed := lc.changed
		lc.mu.Unlock()
		switch step {
		case compactAbort:
			return false
		case compactWrite:
			return true
		case compactCloseFold:
			if claimed != nil {
				bc.finalizeTurn(lc.ctx, lc.chatID, &turnClose{
					Closer: closerPromptHandoff, Stop: marotte.StopReasonUnknown, Reason: compactHandoffReason,
					Turn: claimed.ID, Claimed: claimed,
				})
			}
		case compactWaitFold:
			select {
			case <-changed:
			case <-lc.ctx.Done():
				return false
			}
		}
	}
}

// call makes one compaction call. An accepted call waits for the frames before its reply, so a prompt sent next
// opens after the summary.
func (a *autoCompactor) call(ctx context.Context, chatID marotte.ChatID, b *sharedBridge, atPct float64) (bool, error) {
	callCtx, cancel := a.callContext(ctx)
	defer cancel()
	var (
		accepted bool
		seq      uint64
	)
	err := b.use(callCtx, func(ctx context.Context, s engineSession) error {
		var cErr error
		accepted, seq, cErr = a.compact(ctx, s)
		return cErr
	})
	if err == nil && accepted && a.folded != nil && !a.folded(callCtx, chatID, seq) {
		slog.Warn("compaction: the frames before its reply never folded", "chat_id", chatID)
	}
	switch {
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		slog.Debug("compaction stood down: the chat closed", "chat_id", chatID)
	case err != nil:
		slog.Warn("compaction failed", "chat_id", chatID, "context_pct", atPct, "error", err)
	case !accepted:
		slog.Info("compaction refused", "chat_id", chatID)
	default:
		slog.Info("compaction accepted", "chat_id", chatID, "context_pct", atPct)
	}
	return accepted, err
}

func (a *autoCompactor) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.budget != nil {
		return a.budget(ctx, compactCallBudget)
	}
	return context.WithTimeout(ctx, compactCallBudget)
}
