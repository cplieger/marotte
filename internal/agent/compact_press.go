package agent

// The reader's Compact is one _kiro/session/compact per press, sent at once and never queued or retried. kiro-cli
// compacts inside a turn by itself, at the threshold every chat door sends (chatSessionOpts).

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// compactCallBudget bounds one compaction call. Above KAS's 300 s _kiro/session/compact bound, so it only covers a
// wedged bridge.
const compactCallBudget = 310 * time.Second

// compactRefusedDetail is the toast for KAS's bare {success: false}, which names no cause. KAS discards a finished
// summary when the session's history moved while it ran (kirodotdev/Kiro#12483).
const compactRefusedDetail = "The compaction did not run: the agent was busy, new messages arrived while kiro-cli " +
	"was summarizing, the summary failed, or a PreCompact hook blocked it."

// compactCallFailedDetail is the toast for a call that ended in an error; the error itself goes to the log only.
const compactCallFailedDetail = "The compaction did not run: the call to kiro-cli failed or timed out."

// compactCall is a chat's running press, on its chatLifecycle (compactPress) under lc.mu.
type compactCall struct {
	// reported: kiro-cli's own summarization failure frame told the reader of this call's outcome.
	reported bool
}

type compactPresses struct {
	turns *turnRegistry
	// bridgeOf is the chat's live entry, nil for none; the call is one lease on it.
	bridgeOf func(marotte.ChatID) *sharedBridge
	compact  func(context.Context, command.CompactBridge) (bool, uint64, error)
	// budget bounds one call, extended through an engine stall; folded waits until the frames before a reply's lane
	// position are folded.
	budget func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	folded func(ctx context.Context, chatID marotte.ChatID, seq uint64) bool
	// setMark writes Chat.Compacting from the chat's life, which the header projects (ChatHeader.Compacting).
	setMark func(ctx context.Context, lc *chatLifecycle)
	toast   func(ctx context.Context, chatID marotte.ChatID, detail string)
	spawn   func(func()) bool
}

func newCompactPresses(bc *bridgeCoordinator) *compactPresses {
	return &compactPresses{
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
		compact: command.Compact,
		budget:  bc.engine.stallAware,
		folded:  bc.turns.awaitPosition,
		setMark: bc.setMark,
		toast: func(ctx context.Context, chatID marotte.ChatID, detail string) {
			bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
				Code: marotte.ErrCodeCompactionFailed, Message: detail,
			}))
		},
		spawn: bc.goUnlessDraining,
	}
}

// request is the reader's press: it sends the chat's one call unless one runs, and answers without waiting for it.
func (p *compactPresses) request(ctx context.Context, chatID marotte.ChatID) (command.CompactOutcome, error) {
	b := p.bridgeOf(chatID)
	if b == nil {
		return command.CompactNoSession, nil
	}
	lc, err := p.turns.lifecycleAt(ctx, chatID, p.turns.gates.stamp())
	if err != nil {
		return command.CompactNoSession, err
	}
	lc.mu.Lock()
	switch {
	case lc.ended:
		lc.mu.Unlock()
		return command.CompactNoSession, marotte.ErrChatClosedDuringCall
	case lc.compactPress != nil, lc.summaryRunning:
		// KAS answers a press during its own summary with a bare {success: false} before that summary lands.
		lc.mu.Unlock()
		return command.CompactRunning, nil
	}
	call := &compactCall{}
	lc.compactPress = call
	lc.mu.Unlock()
	p.setMark(ctx, lc)
	if !p.spawn(func() { p.run(lc, b, call) }) {
		p.end(lc, call)
		return command.CompactNoSession, errRuntimeDraining
	}
	return command.CompactStarted, nil
}

// run makes the press's call on the chat's life and tells the reader of a call that did not run, once: kiro-cli's
// own failure report is that account when it came (ObserveCompactionFailed), and an accepted call's is kiro-cli's
// compaction entry.
func (p *compactPresses) run(lc *chatLifecycle, b *sharedBridge, call *compactCall) {
	accepted, err := p.call(lc.ctx, lc.chatID, b)
	reported := p.end(lc, call)
	attrs := []any{"chat_id", lc.chatID}
	switch {
	case accepted:
		slog.Info("compaction accepted", attrs...)
	case errors.Is(err, marotte.ErrChatClosedDuringCall), lc.ctx.Err() != nil:
		slog.Debug("compaction stood down: the chat closed", append(attrs, "error", err)...)
	case reported:
		slog.Warn("compaction did not run", append(attrs, "reason", "kiro-cli reported the summary failed")...)
	case err != nil:
		slog.Warn("compaction did not run", append(attrs, "reason", err.Error())...)
		p.toast(lc.ctx, lc.chatID, compactCallFailedDetail)
	default:
		slog.Warn("compaction did not run", append(attrs, "reason", "refused: kiro-cli answered success false")...)
		p.toast(lc.ctx, lc.chatID, compactRefusedDetail)
	}
}

// end frees the chat for the next press and rewrites the mark. It reports whether kiro-cli reported the call's
// failure itself.
func (p *compactPresses) end(lc *chatLifecycle, call *compactCall) (reported bool) {
	lc.mu.Lock()
	if lc.compactPress == call {
		lc.compactPress = nil
	}
	reported = call.reported
	lc.mu.Unlock()
	p.setMark(lc.ctx, lc)
	return reported
}

// setMark writes Chat.Compacting from lc's state as it stands under markMu: each change is followed by a setMark, so
// the last write holds the last state whatever order two writers ran in.
func (bc *bridgeCoordinator) setMark(ctx context.Context, lc *chatLifecycle) {
	lc.markMu.Lock()
	defer lc.markMu.Unlock()
	lc.mu.Lock()
	on := lc.compactPress != nil || lc.summaryRunning
	lc.mu.Unlock()
	bc.markCompacting(ctx, lc.chatID, on)
}

// markCompacting writes Chat.Compacting detached: the clear runs once the chat's life ended the call too.
func (bc *bridgeCoordinator) markCompacting(ctx context.Context, chatID marotte.ChatID, on bool) {
	if _, err := bc.chatStore.Mutate(durable.Context(ctx), chatID, func(c *marotte.Chat, ok bool) bool {
		changed := ok && c.Compacting != on
		c.Compacting = on
		return changed
	}); err != nil {
		slog.Warn("compaction: the header's running mark was not written", "chat_id", chatID, "error", err)
	}
}

// ObserveCompactionFailed hears kiro-cli report a summary failed: a press running on the chat leaves the account to
// that report.
func (bc *bridgeCoordinator) ObserveCompactionFailed(chatID marotte.ChatID) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.compactPress != nil {
		lc.compactPress.reported = true
	}
}

// ObserveSummaryRunning marks kiro-cli's own summary in the header between its running frame and its outcome.
func (bc *bridgeCoordinator) ObserveSummaryRunning(ctx context.Context, chatID marotte.ChatID, running bool) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		bc.markCompacting(ctx, chatID, running)
		return
	}
	lc.mu.Lock()
	lc.summaryRunning = running
	lc.mu.Unlock()
	bc.setMark(ctx, lc)
}

// closeSummaryMark ends kiro-cli's summary when its turn closed with no outcome frame: a set life flag is cleared, and
// otherwise a header still reading Compacting (a lost write) is cleared, through setMark when the chat has a life, so
// a running press keeps its mark.
func (bc *bridgeCoordinator) closeSummaryMark(ctx context.Context, chatID marotte.ChatID) {
	if lc, ok := bc.turns.lookup(chatID); ok {
		lc.mu.Lock()
		running := lc.summaryRunning
		lc.mu.Unlock()
		if running {
			bc.ObserveSummaryRunning(ctx, chatID, false)
			return
		}
	}
	if c, ok := bc.chatStore.Get(ctx, chatID); ok && c.Compacting {
		bc.ObserveSummaryRunning(ctx, chatID, false)
	}
}

// call makes one compaction call. A call KAS answered waits for the frames before its reply, so kiro-cli's own
// failure report is heard before the outcome is settled.
func (p *compactPresses) call(ctx context.Context, chatID marotte.ChatID, b *sharedBridge) (bool, error) {
	callCtx, cancel := p.budget(ctx, compactCallBudget)
	defer cancel()
	var (
		accepted bool
		seq      uint64
	)
	err := b.use(callCtx, func(ctx context.Context, s engineSession) error {
		var cErr error
		accepted, seq, cErr = p.compact(ctx, s)
		return cErr
	})
	if err == nil && !p.folded(callCtx, chatID, seq) {
		slog.Warn("compaction: the frames before its reply never folded", "chat_id", chatID)
	}
	return accepted, err
}
