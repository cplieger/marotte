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

// compactPhase is where a compaction run is; a run ends in exactly one of the last three, or goes back to queued
// once when KAS refused it under a turn it opened.
type compactPhase uint8

const (
	compactQueued compactPhase = iota
	compactRunning
	compactDone
	compactRefused
	compactCancelled
)

// compactRun is one compaction, from its request or its claim at a turn end to its end. Its fields are a.mu's.
type compactRun struct {
	// call is the run's latest call, nil until its first claim.
	call  *compactCall
	phase compactPhase
	// shown is true for a run that was queued: the holding area shows it until it ends.
	shown bool
}

// compactCall is one call made for run. Its outcome is written before done closes and never after, so a joiner reads
// it after done with no lock.
type compactCall struct {
	run  *compactRun
	done chan struct{}
	err  error
	// requeued: KAS refused the call under a turn it opened, and a queued request now stands for it.
	requeued bool
	accepted bool
}

type autoCompactState struct {
	queued  *compactRun
	running *compactRun
	// crossed is the turn whose reading reached the point and queued the compaction, so that turn queues one.
	crossed string
	// declined is the turn whose queued compaction the reader cancelled, so that turn's end compacts nothing unasked.
	// Keyed by turn id, so no lifecycle edge has to reset it: the next turn has another id.
	declined string
}

type autoCompactor struct {
	chats map[marotte.ChatID]*autoCompactState
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

// queueTurn is the chat's turn state inside the queue's section.
type queueTurn struct {
	// id is the turn a request belongs to (turnRegistry.compactTurn's), "" for none.
	id string
	// live: a turn is open or reserved. holds: a close's KAS hold stands, and its release runs a request. open: the
	// chat is not closing, so a request may be written.
	live, holds, open bool
}

// queueable reports that a request now waits for a turn end instead of running.
func (t queueTurn) queueable() bool { return t.open && (t.live || t.holds) }

// compactQueue is the section every compaction decision is made in, the one a turn opens and closes in, and the
// row on the chat record that shows the queued compaction on every page.
type compactQueue interface {
	// section runs fn once in that section with the turn state and the row's writer, which is idempotent. With no
	// lifecycle fn gets no turn. Only a ctx ending while a turn finalizes stops fn from running.
	section(ctx context.Context, chatID marotte.ChatID, fn func(t queueTurn, row func(on bool) error) error) error
}

func newAutoCompactor(bc *bridgeCoordinator, policy func(context.Context) (enabled bool, pct int)) *autoCompactor {
	return &autoCompactor{
		chats:  map[marotte.ChatID]*autoCompactState{},
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

// within runs fn on the chat's state in the queue's section with a.mu held, so a cancel lands wholly before or
// wholly after every other decision. Lock order: the section, a.mu, the store.
func (a *autoCompactor) within(ctx context.Context, chatID marotte.ChatID, fn func(t queueTurn, st *autoCompactState, row func(bool) error) error) error {
	return a.queue.section(ctx, chatID, func(t queueTurn, row func(bool) error) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		return fn(t, a.stateLocked(chatID), row)
	})
}

// showsLocked reports whether the holding area shows a compaction: one queued, or a queued one still running.
func (st *autoCompactState) showsLocked() bool {
	return st.queued != nil || st.running != nil && st.running.shown && st.running.phase == compactRunning
}

// enqueueLocked queues a request unless one is queued. The row is written first, so a failed write queues nothing.
func (st *autoCompactState) enqueueLocked(row func(bool) error) error {
	if st.queued != nil {
		return nil
	}
	if err := row(true); err != nil {
		return err
	}
	st.queued = &compactRun{phase: compactQueued, shown: true}
	return nil
}

// claimLocked starts run, the queued request or nil for a new one, as the call in flight. Caller saw none running.
func (st *autoCompactState) claimLocked(run *compactRun) *compactCall {
	if run == nil {
		run = &compactRun{}
	}
	if st.queued == run {
		st.queued = nil
	}
	run.phase, run.call = compactRunning, &compactCall{run: run, done: make(chan struct{})}
	st.running = run
	return run.call
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
	queued := false
	err := a.within(ctx, chatID, func(t queueTurn, st *autoCompactState, row func(bool) error) error {
		if !t.queueable() || sameTurn(st.crossed, t.id) || sameTurn(st.declined, t.id) {
			return nil
		}
		st.crossed, queued = t.id, true
		return st.enqueueLocked(row)
	})
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
// for the context's size, unless the reader cancelled that turn's (turn is the closed turn's id). It joins a call
// already in flight instead of making a second. Reports whether a compaction ran or was joined, which afterRelease
// takes.
func (a *autoCompactor) afterTurn(ctx context.Context, chatID marotte.ChatID, turn string, contextLimit bool) bool {
	if a == nil {
		return false
	}
	b := a.bridgeOf(chatID)
	if b == nil {
		return false
	}
	u := a.usage(ctx, chatID)
	enabled, point := a.policy(ctx)
	atPoint := enabled && (contextLimit || u.HasRealData && u.ContextPct >= float64(point))
	var (
		c       *compactCall
		mine    bool
		trigger = "turn end"
	)
	err := a.within(ctx, chatID, func(t queueTurn, st *autoCompactState, _ func(bool) error) error {
		switch {
		case t.live, st.queued == nil && (!atPoint || sameTurn(st.declined, turn)):
		case st.running != nil:
			c = st.running.call
		default:
			if st.queued != nil {
				trigger = "queued"
			}
			c, mine = st.claimLocked(st.queued), true
		}
		return nil
	})
	if err != nil {
		slog.Warn("compaction: the turn end could not read the queue", "chat_id", chatID, "error", err)
		return false
	}
	return a.runOrJoin(ctx, chatID, b, c, mine, u.ContextPct, trigger)
}

// afterRelease takes a request written under the close's KAS hold after afterTurn ran: run now when the close ran
// none, else satisfied by the one it ran. A turn open by then owns the request.
func (a *autoCompactor) afterRelease(ctx context.Context, chatID marotte.ChatID, ran bool) {
	if a == nil {
		return
	}
	b := a.bridgeOf(chatID)
	var (
		c    *compactCall
		mine bool
	)
	err := a.within(ctx, chatID, func(t queueTurn, st *autoCompactState, row func(bool) error) error {
		switch {
		case t.live || st.queued == nil:
		case ran:
			st.queued.phase, st.queued = compactDone, nil
			return row(st.showsLocked())
		case st.running != nil:
			c = st.running.call
		case b != nil:
			c, mine = st.claimLocked(st.queued), true
		}
		return nil
	})
	if err != nil {
		slog.Warn("queued compaction: the request could not be settled", "chat_id", chatID, "error", err)
		return
	}
	a.runOrJoin(ctx, chatID, b, c, mine, a.usage(ctx, chatID).ContextPct, "queued")
}

// runOrJoin makes a claimed call (a turn end's: a refusal is reported) or waits for the one it joined; false with
// no call.
func (a *autoCompactor) runOrJoin(ctx context.Context, chatID marotte.ChatID, b *sharedBridge, c *compactCall, mine bool, atPct float64, trigger string) bool {
	switch {
	case c == nil:
		return false
	case mine:
		a.call(ctx, chatID, b, c, atPct, trigger, true)
	default:
		a.await(ctx, chatID, c)
	}
	return true
}

// awaitRunning holds a prompt until a running compaction is settled, so the prompt does not abort it.
func (a *autoCompactor) awaitRunning(ctx context.Context, chatID marotte.ChatID) {
	if a == nil {
		return
	}
	a.mu.Lock()
	var running *compactCall
	if st := a.chats[chatID]; st != nil && st.running != nil {
		running = st.running.call
	}
	a.mu.Unlock()
	if running != nil {
		a.await(ctx, chatID, running)
	}
}

// request is the reader's compaction: queued while a turn runs, else run now, joining a running one, and taking a
// request left queued on the idle chat. A run KAS refused because a turn opened meanwhile is queued behind that turn,
// unless the reader cancelled it.
func (a *autoCompactor) request(ctx context.Context, chatID marotte.ChatID) (command.CompactOutcome, error) {
	b := a.bridgeOf(chatID)
	var (
		c            *compactCall
		mine, queued bool
	)
	err := a.within(ctx, chatID, func(t queueTurn, st *autoCompactState, row func(bool) error) error {
		switch {
		case t.queueable():
			queued = true
			return st.enqueueLocked(row)
		case b == nil:
		case st.running != nil:
			c = st.running.call
		default:
			c, mine = st.claimLocked(st.queued), true
		}
		return nil
	})
	switch {
	case queued || err != nil:
		return command.CompactQueued, err
	case c == nil:
		return command.CompactNoSession, nil
	case mine:
		a.call(ctx, chatID, b, c, a.usage(ctx, chatID).ContextPct, "request", false)
	case !a.await(ctx, chatID, c):
		return command.CompactRefused, ctx.Err()
	}
	switch {
	case c.err != nil:
		return command.CompactRefused, c.err
	case c.accepted:
		return command.CompactAccepted, nil
	case c.requeued:
		return command.CompactQueued, nil
	}
	return command.CompactRefused, nil
}

// cancel withdraws the queued compaction and the call in flight: neither runs nor comes back, and the turn the request
// belongs to (the open one, else the one whose close may still run its end) compacts nothing unasked. A later press
// is a new request.
func (a *autoCompactor) cancel(ctx context.Context, chatID marotte.ChatID) error {
	return a.within(ctx, chatID, func(t queueTurn, st *autoCompactState, row func(bool) error) error {
		if t.id != "" {
			st.declined = t.id
		}
		if st.queued != nil {
			st.queued.phase, st.queued = compactCancelled, nil
		}
		if st.running != nil && st.running.phase == compactRunning {
			st.running.phase = compactCancelled
		}
		return row(st.showsLocked())
	})
}

// await holds the caller until the call is settled, bounded by compactCallBudget; false when the wait ended first.
func (*autoCompactor) await(ctx context.Context, chatID marotte.ChatID, c *compactCall) bool {
	wait, cancel := context.WithTimeout(ctx, compactCallBudget)
	defer cancel()
	select {
	case <-c.done:
		return true
	case <-wait.Done():
		slog.Warn("stopped waiting for the running compaction", "chat_id", chatID, "error", wait.Err())
		return false
	}
}

// call makes c and settles it; report toasts a shown request that did not run. An accepted call waits for the frames
// before its reply, so a prompt sent next opens after the summary.
func (a *autoCompactor) call(ctx context.Context, chatID marotte.ChatID, b *sharedBridge, c *compactCall, atPct float64, trigger string, report bool) {
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
	c.accepted, c.err = accepted, err

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
	a.settle(ctx, chatID, c, report)
}

// settle ends c in the queue's section, once: a later settle finds c done and does nothing, and done closes there,
// so a joiner returns after it. An accepted call satisfies the request queued during it; one KAS refused under a turn
// it opened is queued for that turn's end unless the reader cancelled it; any other end drops the queue, with a toast
// (report) when the holding area showed it.
func (a *autoCompactor) settle(ctx context.Context, chatID marotte.ChatID, c *compactCall, report bool) {
	toast, requeued := false, false
	err := a.queue.section(context.WithoutCancel(ctx), chatID, func(t queueTurn, row func(bool) error) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		select {
		case <-c.done:
			return nil
		default:
		}
		defer close(c.done)
		st := a.chats[chatID]
		if st == nil || st.running != c.run {
			return nil
		}
		st.running = nil
		toast = st.endLocked(c, t.open && t.live) && report
		requeued = c.requeued
		return row(st.showsLocked())
	})
	if err != nil {
		slog.Warn("queued compaction: the request could not be settled", "chat_id", chatID, "error", err)
	}
	if requeued {
		slog.Info("compaction refused under a starting turn; it runs at that turn's end", "chat_id", chatID)
	}
	if toast && a.failed != nil {
		a.failed(ctx, chatID, queuedCompactFailed)
	}
}

// endLocked moves c's run to where c's outcome leaves it, reporting whether a shown request ended without running.
func (st *autoCompactState) endLocked(c *compactCall, turnLive bool) bool {
	run := c.run
	switch {
	case c.accepted:
		if run.phase == compactRunning {
			run.phase = compactDone
		}
		if st.queued != nil {
			st.queued.phase, st.queued = compactDone, nil
		}
	case run.phase == compactCancelled:
	case c.err == nil && turnLive:
		c.requeued = true
		if st.queued == nil {
			run.phase, run.shown, st.queued = compactQueued, true, run
		} else {
			run.phase = compactRefused
		}
	default:
		dropped := st.queued != nil
		run.phase = compactRefused
		if dropped {
			st.queued.phase, st.queued = compactRefused, nil
		}
		return (run.shown || dropped) && !errors.Is(c.err, marotte.ErrChatClosedDuringCall)
	}
	return false
}

func (a *autoCompactor) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.budget != nil {
		return a.budget(ctx, compactCallBudget)
	}
	return context.WithTimeout(ctx, compactCallBudget)
}

// recordCompactQueue keeps the queued compaction on the chat record, which carries it to every page's dock.
type recordCompactQueue struct{ bc *bridgeCoordinator }

// section runs fn in the chat's lifecycle section, as AppendIfLive writes, so a close sees a request or its writer
// sees no turn.
func (q recordCompactQueue) section(ctx context.Context, chatID marotte.ChatID, fn func(t queueTurn, row func(bool) error) error) error {
	row := func(on bool) error { return q.setRow(ctx, chatID, on) }
	err := q.bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		v, _ := q.bc.turns.gates.admit(chatID, lc.stamp)
		return fn(queueTurn{
			id:    lc.compactTurnLocked(),
			live:  lc.liveLockedState(),
			holds: lc.kas.may().holdRows,
			open:  !lc.ended && v == gateAdmitted,
		}, row)
	})
	if errors.Is(err, errNoLife) {
		return fn(queueTurn{}, row)
	}
	return err
}

func (q recordCompactQueue) setRow(ctx context.Context, chatID marotte.ChatID, on bool) error {
	_, err := q.bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || (c.CompactPending != nil) == on {
			return false
		}
		c.CompactPending = nil
		if on {
			c.CompactPending = &marotte.PendingCompaction{RequestedAt: time.Now().UnixMilli()}
		}
		return true
	})
	return err
}
