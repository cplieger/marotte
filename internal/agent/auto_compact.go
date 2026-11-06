package agent

// A chat has one compaction path: the queued compaction (Chat.CompactPending), run at a turn's end before any queued
// prompt. The reader's button queues one while a turn runs, or runs it at once on an idle chat; a turn whose context
// reaches the point queues one the same way. KAS's own compaction is off on every chat session.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// compactCallBudget bounds one compaction call. Above KAS's 300 s _kiro/session/compact bound, so it only covers a
// wedged bridge. A var for tests.
var compactCallBudget = 310 * time.Second

// queuedCompactFailed is the toast for a queued compaction KAS refused or that failed.
const queuedCompactFailed = "The queued compaction did not run: the agent was busy, or the summary failed or the chat changed before it finished"

// compactRun is one compaction call; accepted and err are written before done closes.
type compactRun struct {
	done chan struct{}
	err  error
	// cancels is the chat's cancel count when the run was claimed; a higher count means the reader withdrew it.
	cancels  uint64
	accepted bool
}

type autoCompactState struct {
	inflight *compactRun
	// crossed is the turn whose reading reached the point and queued the compaction, so that turn queues one.
	crossed string
	// declined is the turn whose queued compaction the reader cancelled, so that turn's end compacts nothing unasked.
	// Keyed by turn id, so no lifecycle edge has to reset it: the next turn has another id.
	declined string
	// cancels counts the reader's cancels, whatever turn each named, so a run tells a cancel made during it.
	cancels uint64
}

type autoCompactor struct {
	chats map[marotte.ChatID]*autoCompactState
	live  func(marotte.ChatID) bool
	// turnOf is turnRegistry.compactTurn, read only to skip a reading whose turn already settled; the queue's section
	// names the turn every decision is made for.
	turnOf func(marotte.ChatID) (string, bool)
	// bridgeOf is the chat's live entry, nil for none; every compaction call is one lease on it.
	bridgeOf func(marotte.ChatID) *sharedBridge
	usage    func(context.Context, marotte.ChatID) marotte.Usage
	policy   func(context.Context) (enabled bool, pct int)
	compact  func(context.Context, command.CompactBridge) (bool, uint64, error)
	// budget bounds one call, extended through an engine stall; folded waits until the frames before a reply's lane
	// position are folded. Both nil in tests.
	budget func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	folded func(ctx context.Context, chatID marotte.ChatID, seq uint64) bool
	queue  compactQueue
	// failed tells the chat's pages that a queued compaction did not run.
	failed func(ctx context.Context, chatID marotte.ChatID, detail string)
	mu     sync.Mutex
}

// compactQueue is Chat.CompactPending as the compactor reads and settles it. markIfLive and withdraw each decide and
// write in one section with the turn the request belongs to, so a cancel and a crossing never interleave.
type compactQueue interface {
	// markIfLive sets the request while a turn runs or a close's KAS hold stands, so that close runs it; false (and
	// nothing written) on an idle chat. admit, when set, gets the turn and may refuse a new request; a request
	// already set stays and answers true.
	markIfLive(ctx context.Context, chatID marotte.ChatID, admit func(turn string) bool) (bool, error)
	pending(ctx context.Context, chatID marotte.ChatID) bool
	clear(ctx context.Context, chatID marotte.ChatID) error
	// clearIfIdle clears the request unless a turn is live, whose end it then waits for.
	clearIfIdle(ctx context.Context, chatID marotte.ChatID) error
	// withdraw hands decline the turn the request belongs to, then clears the request.
	withdraw(ctx context.Context, chatID marotte.ChatID, decline func(turn string)) error
}

func newAutoCompactor(bc *bridgeCoordinator, policy func(context.Context) (enabled bool, pct int)) *autoCompactor {
	return &autoCompactor{
		chats:  map[marotte.ChatID]*autoCompactState{},
		live:   bc.turns.live,
		turnOf: bc.turns.compactTurn,
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
		queue:   recordCompactQueue{bc: bc},
		failed: func(ctx context.Context, chatID marotte.ChatID, detail string) {
			bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
				Code: marotte.ErrCodeCompactionFailed, Message: detail,
			}))
		},
	}
}

// stateLocked returns the chat's state, creating it. a.mu must be held.
func (a *autoCompactor) stateLocked(chatID marotte.ChatID) *autoCompactState {
	st := a.chats[chatID]
	if st == nil {
		st = &autoCompactState{}
		a.chats[chatID] = st
	}
	return st
}

// ObserveContextUsage hands each context reading KAS reported to the chat's compactor.
func (bc *bridgeCoordinator) ObserveContextUsage(ctx context.Context, chatID marotte.ChatID, pct float64) {
	bc.autoCompact.noteUsage(ctx, chatID, pct)
}

// noteUsage queues the turn's compaction the first time a reading inside a turn reaches the point, once per turn and
// never after the reader cancelled this turn's.
func (a *autoCompactor) noteUsage(ctx context.Context, chatID marotte.ChatID, pct float64) {
	if a == nil {
		return
	}
	turn, live := a.turnOf(chatID)
	if !live {
		return
	}
	if enabled, point := a.policy(ctx); !enabled || pct < float64(point) || a.settled(chatID, turn) {
		return
	}
	queued, err := a.queue.markIfLive(ctx, chatID, func(turn string) bool { return a.cross(chatID, turn) })
	switch {
	case err != nil:
		slog.Warn("auto-compaction: the compaction could not be queued; the turn's end compacts at the point anyway",
			"chat_id", chatID, "context_pct", pct, "error", err)
	case queued:
		slog.Info("auto-compaction queued: the context reached the point", "chat_id", chatID, "context_pct", pct)
	}
}

// sameTurn reports whether a recorded turn is this one; "" names no turn, so the zero state matches none.
func sameTurn(recorded, turn string) bool { return recorded != "" && recorded == turn }

// settled reports that the turn already crossed or the reader cancelled its compaction.
func (a *autoCompactor) settled(chatID marotte.ChatID, turn string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.stateLocked(chatID)
	return sameTurn(st.crossed, turn) || sameTurn(st.declined, turn)
}

// cross records the turn's crossing, false when it is settled.
func (a *autoCompactor) cross(chatID marotte.ChatID, turn string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.stateLocked(chatID)
	if sameTurn(st.crossed, turn) || sameTurn(st.declined, turn) {
		return false
	}
	st.crossed = turn
	return true
}

// declined reports that the reader cancelled the turn's compaction.
func (a *autoCompactor) declined(chatID marotte.ChatID, turn string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return sameTurn(a.stateLocked(chatID).declined, turn)
}

func (a *autoCompactor) forget(chatID marotte.ChatID) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.chats, chatID)
}

// afterTurn runs at most one compaction at a turn's end, on an idle chat (a compaction beside a running turn is
// aborted upstream): the queued one, else the policy's when the reading is at the point or the model refused the turn
// for the context's size, unless the reader cancelled that turn's (turn is the closed turn's id). A refusal under a
// starting turn queues it for that turn's end. Reports whether a compaction ran or was joined, which afterRelease takes.
func (a *autoCompactor) afterTurn(ctx context.Context, chatID marotte.ChatID, turn string, contextLimit bool) bool {
	if a == nil || a.live(chatID) {
		return false
	}
	b := a.bridgeOf(chatID)
	if b == nil {
		return false
	}
	u := a.usage(ctx, chatID)
	if a.queue.pending(ctx, chatID) {
		return a.runOnce(ctx, chatID, b, u.ContextPct, "queued")
	}
	// After the queue read: withdraw records the cancel before it clears the request, so a cleared one is seen here.
	enabled, point := a.policy(ctx)
	if a.declined(chatID, turn) || !enabled || !contextLimit && (!u.HasRealData || u.ContextPct < float64(point)) {
		return false
	}
	return a.runOnce(ctx, chatID, b, u.ContextPct, "turn end")
}

// afterRelease takes a request written under the close's KAS hold after afterTurn read the queue: run now when the
// close ran none, else satisfied by the one it ran. A turn open by then owns the request.
func (a *autoCompactor) afterRelease(ctx context.Context, chatID marotte.ChatID, ran bool) {
	if a == nil || a.live(chatID) || !a.queue.pending(ctx, chatID) {
		return
	}
	if ran {
		if err := a.queue.clearIfIdle(ctx, chatID); err != nil {
			slog.Warn("queued compaction: the request could not be cleared", "chat_id", chatID, "error", err)
		}
		return
	}
	if b := a.bridgeOf(chatID); b != nil {
		a.runOnce(ctx, chatID, b, a.usage(ctx, chatID).ContextPct, "queued")
	}
}

// runOnce runs a turn end's compaction, or joins the one already running, then settles the queue. Always true: a
// compaction stands for this turn end either way.
func (a *autoCompactor) runOnce(ctx context.Context, chatID marotte.ChatID, b *sharedBridge, atPct float64, trigger string) bool {
	a.mu.Lock()
	st := a.stateLocked(chatID)
	joined := st.inflight
	run := st.claimLocked()
	a.mu.Unlock()
	if run == nil {
		if a.await(ctx, chatID, joined) {
			a.settle(ctx, chatID, joined)
		}
		return true
	}
	a.run(ctx, chatID, st, run, b, atPct, trigger)
	a.settle(ctx, chatID, run)
	return true
}

// settle ends a turn end's compaction: queued again when a turn opened under it (KAS aborts a compaction a starting
// turn meets) unless the reader cancelled since the run was claimed, else the request cleared, a press made during the
// run included, with a toast when it did not run. A press after the cancel is a row already there, which the re-queue
// keeps. A run is claimed only on an idle chat, so any cancel naming a turn opened under it counts as one since.
func (a *autoCompactor) settle(ctx context.Context, chatID marotte.ChatID, run *compactRun) {
	if !run.accepted && run.err == nil && a.live(chatID) {
		admit := func(string) bool { return a.uncancelled(chatID, run) }
		if requeued, err := a.queue.markIfLive(ctx, chatID, admit); err == nil && requeued {
			slog.Info("compaction refused under a starting turn; it runs at that turn's end", "chat_id", chatID)
			return
		}
	}
	if !a.queue.pending(ctx, chatID) {
		return
	}
	if err := a.queue.clear(ctx, chatID); err != nil {
		slog.Warn("queued compaction: the request could not be cleared", "chat_id", chatID, "error", err)
	}
	if !run.accepted && !errors.Is(run.err, marotte.ErrChatClosedDuringCall) && a.failed != nil {
		a.failed(ctx, chatID, queuedCompactFailed)
	}
}

// awaitRunning holds a prompt until a running compaction returns, so the prompt does not abort it.
func (a *autoCompactor) awaitRunning(ctx context.Context, chatID marotte.ChatID) {
	if a == nil {
		return
	}
	a.mu.Lock()
	var running *compactRun
	if st := a.chats[chatID]; st != nil {
		running = st.inflight
	}
	a.mu.Unlock()
	if running != nil {
		a.await(ctx, chatID, running)
	}
}

// request is the reader's compaction: queued on the record while a turn runs, else run now, joining a running one.
// A run KAS refused because a turn opened meanwhile is queued behind that turn, unless the reader cancelled since.
func (a *autoCompactor) request(ctx context.Context, chatID marotte.ChatID) (command.CompactOutcome, error) {
	if queued, err := a.queue.markIfLive(ctx, chatID, nil); err != nil || queued {
		return command.CompactQueued, err
	}
	b := a.bridgeOf(chatID)
	if b == nil {
		return command.CompactNoSession, nil
	}
	u := a.usage(ctx, chatID)
	a.mu.Lock()
	st := a.stateLocked(chatID)
	run, mine := st.inflight, false
	if run == nil {
		run, mine = st.claimLocked(), true
	}
	a.mu.Unlock()
	if mine {
		a.run(ctx, chatID, st, run, b, u.ContextPct, "request")
	} else if !a.await(ctx, chatID, run) {
		return command.CompactRefused, ctx.Err()
	}
	switch {
	case run.err != nil:
		return command.CompactRefused, run.err
	case run.accepted:
		// It stands for a request left on the record.
		if err := a.queue.clear(ctx, chatID); err != nil {
			slog.Warn("queued compaction: the request could not be cleared", "chat_id", chatID, "error", err)
		}
		return command.CompactAccepted, nil
	}
	uncancelled := func(string) bool { return a.uncancelled(chatID, run) }
	if queued, err := a.queue.markIfLive(ctx, chatID, uncancelled); err != nil || queued {
		return command.CompactQueued, err
	}
	return command.CompactRefused, nil
}

// cancel drops the queued compaction; the turn it belongs to (the open one, else the one whose close may still be
// running its end) then compacts nothing unasked, a run in flight is not queued again, and no other turn is touched.
// A later press still runs.
func (a *autoCompactor) cancel(ctx context.Context, chatID marotte.ChatID) error {
	return a.queue.withdraw(ctx, chatID, func(turn string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		st := a.stateLocked(chatID)
		st.cancels++
		if turn != "" {
			st.declined = turn
		}
	})
}

// uncancelled reports that the reader made no cancel since the run was claimed. A run whose state forget dropped
// meets a fresh count and is treated as cancelled unless none was ever made.
func (a *autoCompactor) uncancelled(chatID marotte.ChatID, run *compactRun) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stateLocked(chatID).cancels == run.cancels
}

// claimLocked marks a compaction in flight and returns its handle, nil while one runs. a.mu must be held.
func (st *autoCompactState) claimLocked() *compactRun {
	if st.inflight != nil {
		return nil
	}
	st.inflight = &compactRun{done: make(chan struct{}), cancels: st.cancels}
	return st.inflight
}

// await holds the caller until the running compaction returns, bounded by compactCallBudget; false when the wait
// ended first.
func (*autoCompactor) await(ctx context.Context, chatID marotte.ChatID, running *compactRun) bool {
	wait, cancel := context.WithTimeout(ctx, compactCallBudget)
	defer cancel()
	select {
	case <-running.done:
		return true
	case <-wait.Done():
		slog.Warn("stopped waiting for the running compaction", "chat_id", chatID, "error", wait.Err())
		return false
	}
}

// run makes the call and records its outcome. An accepted call waits for the frames before its reply, so a prompt
// sent next opens after the summary.
func (a *autoCompactor) run(ctx context.Context, chatID marotte.ChatID, st *autoCompactState, run *compactRun, b *sharedBridge, atPct float64, trigger string) {
	callCtx, cancel := a.callContext(ctx)
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
	cancel()

	a.mu.Lock()
	if cur := a.chats[chatID]; cur == st {
		st.inflight = nil
	}
	run.accepted, run.err = accepted, err
	a.mu.Unlock()
	close(run.done)

	switch {
	case errors.Is(err, marotte.ErrChatClosedDuringCall):
		slog.Debug("compaction stood down: the chat closed", "chat_id", chatID, "trigger", trigger)
	case err != nil:
		slog.Warn("compaction failed", "chat_id", chatID, "trigger", trigger, "context_pct", atPct, "error", err)
	case !accepted:
		slog.Info("compaction refused", "chat_id", chatID, "trigger", trigger)
	default:
		slog.Info("compaction accepted", "chat_id", chatID, "trigger", trigger, "context_pct", atPct)
	}
}

func (a *autoCompactor) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.budget != nil {
		return a.budget(ctx, compactCallBudget)
	}
	return context.WithTimeout(ctx, compactCallBudget)
}

// recordCompactQueue keeps the queued compaction on the chat record, which carries it to every page's dock.
type recordCompactQueue struct{ bc *bridgeCoordinator }

// markIfLive writes the request in the lifecycle section, as AppendIfLive does, so the close sees it or the caller
// sees no turn. Lock order: the lifecycle, then admit's a.mu (released), then the store.
func (q recordCompactQueue) markIfLive(ctx context.Context, chatID marotte.ChatID, admit func(turn string) bool) (bool, error) {
	queued := false
	err := q.bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if v, _ := q.bc.turns.gates.admit(chatID, lc.stamp); lc.ended || v != gateAdmitted {
			return nil
		}
		if !lc.liveLockedState() && !lc.kas.may().holdRows {
			return nil
		}
		admitted := admit == nil || admit(lc.compactTurnLocked())
		_, err := q.bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
			if !exists || c.CompactPending != nil {
				queued = true
				return false
			}
			if queued = admitted; !admitted {
				return false
			}
			c.CompactPending = &marotte.PendingCompaction{RequestedAt: time.Now().UnixMilli()}
			return true
		})
		return err
	})
	if errors.Is(err, errNoLife) {
		return false, nil
	}
	return queued, err
}

// withdraw runs decline and the clear in the lifecycle section markIfLive writes in; with no lifecycle there is no
// turn to decline, only the request to clear.
func (q recordCompactQueue) withdraw(ctx context.Context, chatID marotte.ChatID, decline func(turn string)) error {
	err := q.bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		decline(lc.compactTurnLocked())
		return q.clear(ctx, chatID)
	})
	if errors.Is(err, errNoLife) {
		return q.clear(ctx, chatID)
	}
	return err
}

func (q recordCompactQueue) pending(ctx context.Context, chatID marotte.ChatID) bool {
	c, ok := q.bc.chatStore.Get(ctx, chatID)
	return ok && c.CompactPending != nil
}

func (q recordCompactQueue) clear(ctx context.Context, chatID marotte.ChatID) error {
	_, err := q.bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || c.CompactPending == nil {
			return false
		}
		c.CompactPending = nil
		return true
	})
	return err
}

func (q recordCompactQueue) clearIfIdle(ctx context.Context, chatID marotte.ChatID) error {
	err := q.bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if lc.liveLockedState() {
			return nil
		}
		return q.clear(ctx, chatID)
	})
	if errors.Is(err, errNoLife) {
		return q.clear(ctx, chatID)
	}
	return err
}
