package agent

// A turn ends by its own end, its bridge's death, or a marotte crash; the first two close here, the third is
// internal/chat's store-open closer.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/runesafe/v2"
)

// turnCloser names the local step ending a turn rather than a stop reason.
type turnCloser int

const (
	closerPromptResponse turnCloser = iota
	// closerPromptFailure is the prompt call failing before the turn could end.
	closerPromptFailure
	closerLocalShell
	// closerBridgeDeath is the bridge's stream ending, or a deliberate stop, with a turn open.
	closerBridgeDeath
	// closerWireEnd is the engine's own turn_end bracket, the ONLY closer setting WireEnded.
	closerWireEnd
	// closerBracketLost is a turn_start arriving while a bracketed own turn is open: its turn_end was lost.
	closerBracketLost
)

// deathInterruptCause is a turn's public sentence when the bridge's stream ended mid-turn. It claims only what was
// observed: nothing reads the exit status, so a closed pipe is not an exit.
const deathInterruptCause = "The connection to the agent closed before the turn finished."

// "type continue" is the remedy.
const shutdownInterruptCause marotte.InterruptCause = "The server restarted while this turn was running. Send the prompt again, or type continue."

// maxReasonBytes bounds a persisted failure reason, as rpcerr.Text does.
const maxReasonBytes = 2048

// reasonFor settles what a close says: the closer's cause, else the outcome's default, else nothing. Sanitized and
// capped: every non-constant source is upstream text.
func reasonFor(o marotte.TurnOutcome, supplied string) string {
	reason := supplied
	if reason == "" {
		reason = marotte.DefaultFailureReason(o)
	}
	if reason == "" {
		return ""
	}
	capped, _ := runesafe.SanitizeSingleLineCapped(sanitize.Output(reason), maxReasonBytes, "...")
	return capped
}

type turnClose struct {
	// Resp is the session/prompt response, on closerPromptResponse only.
	Resp *marotte.RPCResponse
	// Reason is the user-facing account; empty leaves the outcome's default sentence.
	Reason string
	// Kind is the classified failure, on closerPromptFailure only.
	Kind marotte.FailureKind
	// Turn names the ONE turn an id-scoped closer may end. Empty only with Own.
	Turn string
	// Stop is what the close concludes: the wire's on closerWireEnd, the command layer's (`interrupted` or
	// `cancelled` only) on closerPromptFailure and closerBridgeDeath.
	Stop marotte.StopReason
	// Seq is the reply's read-loop position on the prompt closers, waited for before closing; zero skips the wait.
	Seq uint64
	// Own spells "close the chat's own turn", for a closer describing the chat rather than an id it holds.
	Own    bool
	Closer turnCloser
}

// finalizeTurn claims one turn and runs the turn end rule: claim, effects, publish, with mu held for none of the
// effects, so racing closers produce one set (the second loses the claim).
func (bc *bridgeCoordinator) finalizeTurn(ctx context.Context, chatID marotte.ChatID, tc *turnClose) {
	// The wait precedes the claim: the claim makes folds wait, so the settle would block on itself. A failure's wait
	// only orders the close after a queued turn_end; a dead ctx or gone forward still closes.
	if tc.Seq > 0 && !bc.turns.awaitPosition(ctx, chatID, tc.Turn, tc.Seq) && tc.Closer != closerPromptFailure {
		return
	}
	t, won := bc.claimForCloser(ctx, chatID, tc)
	if !won {
		return
	}
	// Detached here, below awaitPosition, whose only timed escape is ctx.Done(); both doors hand a shutdown-cancelled context.
	ctx = durable.Context(ctx)
	stop, reason, kind := tc.Stop, tc.Reason, tc.Kind
	switch tc.Closer {
	case closerPromptResponse:
		stop = extractStopReason(tc.Resp)
	case closerLocalShell:
		stop = marotte.StopReasonEndTurn
	case closerBracketLost:
		stop = marotte.StopReasonUnknown
	case closerPromptFailure, closerBridgeDeath:
		// A cause claimed on the turn beats the closer's own account.
		if cause := bc.turns.interruptCause(t); cause != "" {
			reason, kind = string(cause), ""
		}
	case closerWireEnd:
	}
	if reason == "" && kind == "" {
		reason, kind = engineAccount(t, stop)
	}
	result, outcome := bc.closeTurn(ctx, t, stop, reason, kind, tc.Closer)
	cf := closeFacts{outcome: outcome, fence: fenceOf(t)}
	if tc.Closer == closerBridgeDeath {
		cf.exit = bc.turns.forwardExit(t.Chat)
	}
	bc.turns.finish(t, result)
	bc.publishClose(ctx, t, cf)
}

func (bc *bridgeCoordinator) publishClose(ctx context.Context, t *activeTurn, cf closeFacts) {
	if bc.onTurnClosed != nil {
		bc.onTurnClosed(t.Chat, t.ID)
	}
	bc.autoCompact.noteTurnClosed(t.Chat)
	// Its own goroutine, on inflight so a drained prompt joins a held count instead of racing Shutdown's Wait.
	after := func() { bc.afterTurnClose(ctx, t.Chat, cf) }
	if bc.lifecycle == nil || !bc.lifecycle.goUnlessDraining(after) {
		go after()
	}
}

// engineAccount is a broken turn's sentence when the closer supplied none: the engine's latched display_error,
// through the prompt-failure remedy table. A clean close ignores it, and so does a stop that classifies itself, as
// the replay projection does.
func engineAccount(t *activeTurn, stop marotte.StopReason) (string, marotte.FailureKind) {
	e := t.Log.EngineError()
	c := marotte.ConcludeStopReason(stop)
	if e == nil || c.FailureKind != "" || marotte.SeverityOf(c.Outcome) != marotte.TurnSeverityBroken {
		return "", ""
	}
	m := rpcerr.Mapped{ErrorType: e.ErrorType, RetryErrorType: e.RetryErrorType}
	var kind marotte.FailureKind
	if m.ErrorType == rpcerr.ContextWindowExceededError {
		kind = marotte.FailureKindContextLimit
	}
	return rpcerr.Account(m, e.Message), kind
}

// closeFacts is what a won close hands its pipeline: the outcome, the fence naming the turn, and a death's forwarder exit.
type closeFacts struct {
	exit    <-chan struct{}
	outcome marotte.TurnOutcome
	fence   command.TurnFence
}

// afterTurnClose runs the between-turns follow-ups in order: steer ends, a model switch (it changes the
// compaction's window), the compaction (a queued prompt would abort it upstream), then the drain graded by the
// close's outcome. Nothing once shutdown began.
func (bc *bridgeCoordinator) afterTurnClose(ctx context.Context, chatID marotte.ChatID, cf closeFacts) {
	var ends command.EndFacts
	if bc.resolveEnds != nil && !bc.draining() {
		// A death drains its forwarder here, lock-free.
		if cf.exit != nil {
			waitForwarder(cf.exit)
		}
		// So a holder the resolve sees is another turn, not this close's prompt.
		bc.settleAfterClose(bc.lifecycle.shutdownCtx, chatID, command.AdmissionWait)
		ends = bc.resolveEnds(ctx, chatID, cf.fence)
	}
	if bc.applyPendingModel != nil {
		bc.applyPendingModel(ctx, chatID)
	}
	bc.autoCompact.afterTurn(ctx, chatID)
	if bc.drainAfterClose == nil || bc.draining() ||
		!bc.settleAfterClose(bc.lifecycle.shutdownCtx, chatID, command.AdmissionWait) {
		return
	}
	bc.drainAfterClose(ctx, chatID, command.CloseFacts{Outcome: cf.outcome, Fence: cf.fence}, ends)
}

func (bc *bridgeCoordinator) draining() bool {
	return bc.lifecycle != nil && bc.lifecycle.draining.Load()
}

// waitForwarder waits for a dead bridge's forward goroutine, bounded like the
// resolver's own wait.
func waitForwarder(exit <-chan struct{}) {
	timer := time.NewTimer(command.ResendBridgeWait)
	defer timer.Stop()
	select {
	case <-exit:
	case <-timer.C:
	}
}

// claimForCloser claims the turn a closer ends: an id that turn, Own the chat's own. Neither opens a turn, so a late
// bracket makes no phantom; an empty id closes nothing.
func (bc *bridgeCoordinator) claimForCloser(ctx context.Context, chatID marotte.ChatID, tc *turnClose) (*activeTurn, bool) {
	if tc.Own {
		return bc.turns.claimOwn(ctx, chatID)
	}
	if tc.Turn == "" {
		slog.Warn("a turn closer named no turn, so it closed nothing",
			"chat_id", chatID, "closer", tc.Closer)
		return nil, false
	}
	t, won := bc.turns.claimTurn(ctx, chatID, tc.Turn)
	if !won {
		// Two closers racing one fault (turn_end and the prompt response): the first won.
		slog.Debug("a turn closer lost its claim", "chat_id", chatID, "closer", tc.Closer, "turn", tc.Turn)
	}
	return t, won
}

// closeTurn runs the turn end rule on a claimed turn: the accumulator seals lanes, aborts unsettled calls and
// appends the turn_close with its aggregate; sealed entries are announced, the header counters follow, and the
// push fires. It returns the outcome the turn_close states.
func (bc *bridgeCoordinator) closeTurn(ctx context.Context, t *activeTurn, stop marotte.StopReason, reason string, kind marotte.FailureKind, closer turnCloser) (marotte.TurnResult, marotte.TurnOutcome) {
	chatID := t.Chat
	c := bc.concludeStop(chatID, stop, reason, kind)
	statusDesc := bc.turns.statusDescription(t)
	sealed, err := t.Log.Close(ctx, c)
	translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
	if bc.steerTurnEnded != nil {
		end := command.SteerTurnEnd{TurnID: t.ID, Source: t.Source, TurnSeq: t.Seq, BridgeDeath: closer == closerBridgeDeath}
		if end.BridgeDeath {
			end.Exit = bc.turns.forwardExit(chatID)
		}
		bc.steerTurnEnded(chatID, end)
	}
	switch {
	case errors.Is(err, turnlog.ErrClosed):
		// Another closer wrote the turn_close first; the record must still finish.
		slog.Warn("a turn closer found the turn_close already on disk", "chat_id", chatID, "turn", t.ID, "closer", closer)
	case err != nil:
		slog.Error("close a turn in the entry log", "chat_id", chatID, "turn", t.ID, "closer", closer, "error", err)
	}
	if err := bc.chatStore.WriteCounters(ctx, chatID); err != nil {
		slog.Warn("turn closed but the header's counters did not follow", "chat_id", chatID, "turn", t.ID, "error", err)
	}
	// The push reads the turn_close's outcome, so a silent end_turn graded `empty` is not a clean finish.
	concluded := c.WithContent(t.Log.Emitted())
	bc.pushTurnOutcome(ctx, chatID, concluded, statusDesc)
	return marotte.TurnResult{
		Stop:           stop,
		Interrupt:      bc.turns.interruptCause(t),
		Reason:         c.Reason,
		EmittedNothing: !t.Log.Emitted(),
		WireEnded:      closer == closerWireEnd,
	}, concluded.Outcome
}

type broadcastFunc func(ctx context.Context, e marotte.ServerEvent)

// Broadcast publishes e.
func (f broadcastFunc) Broadcast(ctx context.Context, e marotte.ServerEvent) { f(ctx, e) }

// A closer's own reason or kind replaces the stop's account as a pair. An unmapped stop logs once per value.
func (bc *bridgeCoordinator) concludeStop(
	chatID marotte.ChatID,
	stop marotte.StopReason,
	reason string,
	kind marotte.FailureKind,
) marotte.TurnConclusion {
	c := marotte.ConcludeStopReason(stop)
	if reason != "" || kind != "" {
		c.Reason, c.FailureKind = reason, kind
	}
	c.EmptyIfSilent = stop == marotte.StopReasonEndTurn
	if !c.Known {
		if _, seen := bc.unknownStops.LoadOrStore(stop, struct{}{}); !seen {
			slog.Warn("a turn ended on a stop reason marotte does not map",
				"chat_id", chatID, "stop_reason", stop)
		}
	}
	c.Reason = reasonFor(c.Outcome, c.Reason)
	if c.Reason == "" && marotte.SeverityOf(c.Outcome) == marotte.TurnSeverityBroken {
		// Unreachable while DefaultFailureReason covers every broken outcome; logged since the cost is a bodiless red card.
		slog.Warn("a broken turn closed with no reason to show",
			"chat_id", chatID, "outcome", c.Outcome, "stop_reason", stop)
	}
	return c
}

// pushTurnOutcome sends a finished turn's notification, reading the severity so it never claims success. On the
// detached context: the push fans out on its own goroutine.
func (bc *bridgeCoordinator) pushTurnOutcome(
	ctx context.Context,
	chatID marotte.ChatID,
	c marotte.TurnConclusion,
	statusDesc string,
) {
	// The turn ended but a run it launched is still on the wire (`run_workflow` returns at creation); the run's
	// own `run_outcome` push covers it.
	if bc.chatHasLiveRun != nil && bc.chatHasLiveRun(chatID) {
		slog.Debug("withholding agent_finished: a run this chat launched is still live",
			"chat_id", chatID, "outcome", c.Outcome)
		return
	}
	switch marotte.SeverityOf(c.Outcome) {
	case marotte.TurnSeverityClean:
		n := notice.TurnFinished(bc.NoticeTarget(ctx, chatID, ""), statusDesc)
		bc.Notify(ctx, chatID, &n)
	case marotte.TurnSeverityBroken:
		n := notice.TurnFailed(bc.NoticeTarget(ctx, chatID, ""), c.Reason)
		bc.Notify(ctx, chatID, &n)
	case marotte.TurnSeverityStopped, marotte.TurnSeverityRunning:
		// A cancel was asked for and an unreadable end says nothing, so neither pushes; `running` cannot close.
	}
}
