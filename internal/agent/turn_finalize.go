package agent

// A turn has three terminal causes: its own end, the death of the bridge that
// hosted it, and a marotte crash. The first two close here; the third is the
// store-open closer in internal/chat.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/runesafe/v2"
)

// turnCloser names the local step ending a turn rather than a stop reason.
type turnCloser int

const (
	// closerPromptResponse is the session/prompt response settling.
	closerPromptResponse turnCloser = iota
	// closerPromptFailure is the prompt call failing before the turn could end.
	closerPromptFailure
	// closerLocalShell is a `!cmd` turn marotte ran itself.
	closerLocalShell
	// closerBridgeDeath is the bridge's frame stream ending, or a deliberate stop,
	// with a turn still open.
	closerBridgeDeath
	// closerWireEnd is the engine's own turn_end bracket, the ONLY closer setting WireEnded.
	closerWireEnd
	// closerBracketLost is a wire turn_start arriving while a bracketed own turn is
	// still open: its turn_end was lost and the next bracket is the evidence.
	closerBracketLost
)

// deathInterruptCause is what a turn says when the bridge's frame stream ended
// mid-turn. PUBLIC PROSE, and it claims only what marotte observed: nothing on
// this path reads the process's exit status, so a closed pipe must not be
// reported as an exit.
const deathInterruptCause = "The connection to the agent closed before the turn finished."

// maxReasonBytes bounds a persisted failure reason, matching rpcerr.Text: the usual
// source is upstream text, and a transcript row is no place for a wall of it.
const maxReasonBytes = 2048

// reasonFor settles what a close SAYS: the cause the closer supplied, else the
// outcome's own default sentence, else nothing for a turn that ended cleanly.
// Sanitized and capped here because every source but the local constants is
// untrusted upstream text.
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

// turnClose is what a closer knows about the stop it is reporting.
type turnClose struct {
	// Resp is the session/prompt response, on closerPromptResponse only.
	Resp *marotte.RPCResponse
	// Reason is the user-facing account of the stop. Empty leaves the outcome's
	// default sentence to speak, rather than inventing wording.
	Reason string
	// Turn names the ONE turn an id-scoped closer may end. Empty only with Own.
	Turn string
	// Stop is the stop the close concludes: the wire's own on closerWireEnd, the
	// command layer's decision on closerPromptFailure and closerBridgeDeath, where
	// the only legal values are `interrupted` and `cancelled`.
	Stop marotte.StopReason
	// Seq is the read loop position the response arrived at, on closerPromptResponse
	// only: the settle waits for the folder to reach it. Zero skips the wait.
	Seq uint64
	// Own is the EXPLICIT spelling of "close the chat's own turn", for a closer that
	// describes the CHAT rather than a turn it holds an id for.
	Own    bool
	Closer turnCloser
}

// finalizeTurn claims one turn and runs the turn end rule on it. Claim first,
// effects second, publish third, with the mutex held for none of the effects, so
// two closers racing one turn produce one set of effects: the second loses the claim.
func (bc *BridgeCoordinator) finalizeTurn(ctx context.Context, chatID marotte.ChatID, tc *turnClose) {
	// The wait comes BEFORE the claim: claiming first puts the chat in turnFinalizing,
	// where a fold waits, so the settle would block on a folder it had blocked itself.
	if tc.Seq > 0 && !bc.turns.awaitPosition(ctx, chatID, tc.Turn, tc.Seq) {
		return
	}
	t, won := bc.claimForCloser(ctx, chatID, tc)
	if !won {
		return
	}
	// Detached HERE and not at the top, because awaitPosition above has ctx.Done() as
	// its only timed escape. Below here the effects are durability, and both doors into
	// this function are handed a shutdown-cancelled context by construction.
	ctx = durable.Context(ctx)
	stop, reason := tc.Stop, tc.Reason
	switch tc.Closer {
	case closerPromptResponse:
		stop = extractStopReason(tc.Resp)
	case closerLocalShell:
		stop = marotte.StopReasonEndTurn
	case closerBracketLost:
		stop = marotte.StopReasonUnknown
	case closerPromptFailure, closerBridgeDeath:
		// A cause claimed on the turn (the tool-use filter, a user cancel) beats the
		// closer's own account.
		if cause := bc.turns.interruptCause(t); cause != "" {
			reason = string(cause)
		}
	case closerWireEnd:
	}
	result := bc.closeTurn(ctx, t, stop, reason, tc.Closer)
	bc.turns.finish(t, result)
	// Published by the closer that WON: a loser would advance a boundary it did not cross.
	if bc.onTurnClosed != nil {
		bc.onTurnClosed(t.Chat, t.ID)
	}
	// The pending_model idle arm runs on its own goroutine: it re-reads the idle
	// predicate under the lifecycle mutex and applies a switch through the bridge,
	// neither of which belongs inside a closer.
	if bc.applyPendingModel != nil {
		go bc.applyPendingModel(ctx, t.Chat)
	}
}

// claimForCloser claims the turn a closer is ending: an ID claims exactly that
// turn, Own the chat's own open turn. Neither OPENS a turn to close it, so a
// bracket for an already-closed turn makes no phantom. An EMPTY id closes
// nothing, so a prompt failure cannot claim a turn OpenTurn never appended.
func (bc *BridgeCoordinator) claimForCloser(ctx context.Context, chatID marotte.ChatID, tc *turnClose) (*Turn, bool) {
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
		// The ordinary outcome of two closers racing one fault: KAS's turn_end and the
		// prompt response are one cause, and the first to arrive closed the turn.
		slog.Debug("a turn closer lost its claim", "chat_id", chatID, "closer", tc.Closer, "turn", tc.Turn)
	}
	return t, won
}

// closeTurn runs the turn end rule on a claimed turn: the accumulator seals every
// lane, aborts every unsettled call and appends the turn_close carrying its
// aggregate; each sealed entry is announced, the header's turn_count and
// last_turn_outcome follow from the index, and the off-screen push fires. The
// footer's credits and elapsed are the accumulator's own, fed by Meter, so nothing
// here reads the chat record for them.
func (bc *BridgeCoordinator) closeTurn(ctx context.Context, t *Turn, stop marotte.StopReason, reason string, closer turnCloser) marotte.TurnResult {
	chatID := t.Chat
	c := bc.concludeStop(chatID, stop, reason)
	statusDesc := bc.turns.statusDescription(t)
	sealed, err := t.Log.Close(ctx, c)
	translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
	if bc.steerTurnEnded != nil {
		end := command.SteerTurnEnd{TurnID: t.ID, Source: t.Source, BridgeDeath: closer == closerBridgeDeath}
		if end.BridgeDeath {
			end.Exit = bc.turns.forwardExit(chatID)
		}
		bc.steerTurnEnded(chatID, end)
	}
	switch {
	case errors.Is(err, turnlog.ErrClosed):
		// The store-open closer or a between-turns synthesis wrote the turn_close
		// first; the registry record still has to finish, so this is not an error.
		slog.Warn("a turn closer found the turn_close already on disk", "chat_id", chatID, "turn", t.ID, "closer", closer)
	case err != nil:
		slog.Error("close a turn in the entry log", "chat_id", chatID, "turn", t.ID, "closer", closer, "error", err)
	}
	if err := bc.chatStore.WriteCounters(ctx, chatID); err != nil {
		slog.Warn("turn closed but the header's counters did not follow", "chat_id", chatID, "turn", t.ID, "error", err)
	}
	// The push reads the outcome the turn_close carries, so a silent end_turn
	// graded `empty` by the seal is not announced as a clean finish.
	bc.pushTurnOutcome(ctx, chatID, c.WithContent(t.Log.Emitted()), statusDesc)
	return marotte.TurnResult{
		Stop:           stop,
		Interrupt:      bc.turns.interruptCause(t),
		EmittedNothing: !t.Log.Emitted(),
		WireEnded:      closer == closerWireEnd,
	}
}

// broadcastFunc adapts the coordinator's broadcast closure to the translator's
// Broadcaster role, so the sealed-entry announcement has one owner.
type broadcastFunc func(ctx context.Context, e marotte.ServerEvent)

// Broadcast publishes e.
func (f broadcastFunc) Broadcast(ctx context.Context, e marotte.ServerEvent) { f(ctx, e) }

// concludeStop grades the stop and settles what the close SAYS: the reason travels ON
// the conclusion, so the turn_close stamps it from one field. An unmapped stop logs
// ONCE per distinct value, so an unseen wire is discoverable without a line per turn.
func (bc *BridgeCoordinator) concludeStop(
	chatID marotte.ChatID,
	stop marotte.StopReason,
	reason string,
) marotte.TurnConclusion {
	c := marotte.ConcludeStopReason(stop)
	c.EmptyIfSilent = stop == marotte.StopReasonEndTurn
	if !c.Known {
		if _, seen := bc.unknownStops.LoadOrStore(stop, struct{}{}); !seen {
			slog.Warn("a turn ended on a stop reason marotte does not map",
				"chat_id", chatID, "stop_reason", stop)
		}
	}
	c.Reason = reasonFor(c.Outcome, reason)
	if c.Reason == "" && marotte.SeverityOf(c.Outcome) == marotte.TurnSeverityBroken {
		// Unreachable while DefaultFailureReason covers every broken outcome; logged
		// rather than asserted because being wrong shows a red turn card with no body.
		slog.Warn("a broken turn closed with no reason to show",
			"chat_id", chatID, "outcome", c.Outcome, "stop_reason", stop)
	}
	return c
}

// pushTurnOutcome sends the off-screen notification a finished turn earns, reading the
// SEVERITY so it cannot claim success over a failure. The client half
// (static-src/handlers/turn.ts) reads the same table the shared severity fixture pins, so
// no string is authored here. On finalizeTurn's DETACHED context deliberately — the push
// fans out on its own goroutine, and runPromptTurn defers a cancel of the caller's.
func (bc *BridgeCoordinator) pushTurnOutcome(
	ctx context.Context,
	chatID marotte.ChatID,
	c marotte.TurnConclusion,
	statusDesc string,
) {
	// The chat's TURN ended; its WORK has not, if a run this chat launched is still on
	// the wire. `run_workflow` returns as soon as the run is created, so the launching
	// turn concludes cleanly while the run carries on, and a push saying the agent
	// finished would tell an off-screen reader the opposite of the truth. The run's
	// own terminal transition pushes `run_outcome` (`notifyRunOutcome`), so this side
	// defers rather than re-firing.
	if bc.chatHasLiveRun != nil && bc.chatHasLiveRun(chatID) {
		slog.Debug("withholding agent_finished: a run this chat launched is still live",
			"chat_id", chatID, "outcome", c.Outcome)
		return
	}
	switch marotte.SeverityOf(c.Outcome) {
	case marotte.TurnSeverityClean:
		bc.NotifyPush(ctx, agentFinishedBodyFrom(statusDesc), marotte.PushKindAgentFinished, chatID)
	case marotte.TurnSeverityBroken:
		bc.NotifyPush(ctx, marotte.DefaultFailureReason(c.Outcome), marotte.PushKindAgentFinished, chatID)
	case marotte.TurnSeverityStopped, marotte.TurnSeverityRunning:
		// A cancel is what the reader asked for and an unreadable end reports nothing,
		// so neither earns an off-screen notification. `running` cannot reach a close.
	}
}
