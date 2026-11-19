// Package ruleengine runs every Bot's rules: it evaluates their triggers as they come due, holds what fired in each
// rule's queue on the Bot's record, and delivers one prompt at a time to a Bot that is free. Every rule write goes
// through the chat store's Mutate; the pure arithmetic is internal/rule's.
package ruleengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
)

// tick is the longest the loop sleeps: a clock jump, a suspended container or a gate that opened is noticed within
// it, and a write fault is retried at it.
const tick = time.Minute

// Store is the chat store as the engine reads and writes a Bot's record and its log.
type Store interface {
	Get(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, bool)
	Mutate(ctx context.Context, chatID marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	// BotIDs answers every chat with a bot block, and false until the store's first complete scan.
	BotIDs() ([]marotte.ChatID, bool)
	All(ctx context.Context, chatID marotte.ChatID) ([]marotte.Entry, error)
}

// Notifier places a rule's prompt in a Bot's chat and takes a held row back off its run list. Notify answers once the
// prompt is on the chat's run list, with the id of the run entry holding it.
type Notifier interface {
	Notify(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller,
		opts command.NotifyOptions) (string, error)
	Unqueue(ctx context.Context, chatID marotte.ChatID, messageID string) error
}

// Deps is what New builds an engine from; every field but WakeHold and Logger is required.
type Deps struct {
	Store    Store
	Notifier Notifier
	// Enter holds the runtime's drain gate for one delivery, false while it drains.
	Enter func() (release func(), ok bool)
	// Ready reports the agent engine ready to take a prompt.
	Ready func() bool
	// TurnLive reports a chat's turn live, admitted or held; Waiting its waiting_on_user claim.
	TurnLive func(marotte.ChatID) bool
	Waiting  func(marotte.ChatID) bool
	// NotDelivered tells the user that the rule named ruleName had its prompt dropped after its last try, with the
	// last refusal, once the drop is written; nil tells no one.
	NotDelivered func(ctx context.Context, chatID marotte.ChatID, ruleName string, cause error)
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// WakeHold is how long a run's end keeps its lease depth for the wake turn KAS owes its parent chat; zero keeps
	// none, so every wake turn reads as another prompt-less turn.
	WakeHold time.Duration
}

// Status is what the engine holds in memory about one Bot, for its setup.
type Status struct {
	// WriteFailingSince is when the engine stopped on a record write that failed; zero while writes land.
	WriteFailingSince time.Time
	// CeilingUntil is when the Bot falls back under BotMaxPerHour, while a prompt waits on it; else zero.
	CeilingUntil time.Time
}

// openedPrompt is a rule's event whose prompt opened a turn, as the turn-open tap saw it.
type openedPrompt struct {
	rule, event string
}

// pendingWrite is an outcome write owed for an act already done on the chat: a write fault retries it as it was,
// since acting again would deliver the prompt twice or read a row taken back as discarded. done runs once it lands.
type pendingWrite struct {
	apply func(c *marotte.Chat) bool
	done  func()
	chat  marotte.ChatID
}

var (
	// errStopped marks a record write that failed: the loop stops until a retry lands.
	errStopped  = errors.New("rule engine write failed")
	errRuleGone = errors.New("this Bot has no such rule; reload its setup")
)

// Engine is the rule engine. Its taps and commands may be called from any goroutine; Run owns the loop. mu guards
// dirty, known, opened, depths, ceiling and failingSince; Run's goroutine alone reads and writes every other field.
type Engine struct {
	deps  Deps
	log   *slog.Logger
	wake  chan struct{}
	dirty map[marotte.ChatID]bool
	// known is every chat indexed or reindexed and not yet forgotten, so the turn-end tap skips any other.
	known   map[marotte.ChatID]bool
	opened  map[marotte.ChatID][]openedPrompt
	depths  map[marotte.ChatID]*chatDepths
	ceiling map[marotte.ChatID]time.Time
	index   map[marotte.ChatID]bool
	due     map[marotte.ChatID]time.Time
	settled map[marotte.ChatID]bool
	retryAt map[marotte.ChatID]map[string]time.Time
	waiting map[marotte.ChatID]int
	// failingSince is when the current write fault began; zero while writes land.
	failingSince time.Time
	pending      []pendingWrite
	mu           sync.Mutex
	indexed      bool
	ready        bool
	faulting     bool
}

// New builds an engine; Run starts it.
func New(deps *Deps) *Engine {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		deps: *deps, log: log, wake: make(chan struct{}, 1),
		dirty: map[marotte.ChatID]bool{}, known: map[marotte.ChatID]bool{}, opened: map[marotte.ChatID][]openedPrompt{},
		depths: map[marotte.ChatID]*chatDepths{}, ceiling: map[marotte.ChatID]time.Time{},
		index: map[marotte.ChatID]bool{}, due: map[marotte.ChatID]time.Time{}, settled: map[marotte.ChatID]bool{},
		retryAt: map[marotte.ChatID]map[string]time.Time{}, waiting: map[marotte.ChatID]int{},
	}
}

// Run evaluates and delivers until ctx ends, and answers nil then: what waits stays on disk for the next process.
// Nothing is evaluated before the store's first complete scan and the agent engine's first ready.
func (e *Engine) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		next := e.step(ctx)
		timer.Reset(time.Until(next))
		wake := e.wake
		if e.faulting {
			wake = nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Reindex reads chatID again on the loop's next pass.
func (e *Engine) Reindex(chatID marotte.ChatID) {
	e.mu.Lock()
	e.dirty[chatID], e.known[chatID] = true, true
	e.mu.Unlock()
	e.signal()
}

// TurnOpened is the turn-open tap. It records the turn's chain depth and answers, for a wake turn (one that took the
// wake hold an ended run left), the waking run's lease depth. A turn whose prompt a rule's event sent also settles
// that event's queued prompt. It never waits on the loop, so a full inbox loses no depth.
func (e *Engine) TurnOpened(chatID marotte.ChatID, turnID string, source marotte.TurnOpenSource, caller *marotte.Caller,
	wake bool,
) (leaseDepth int) {
	now := time.Now()
	settles := caller != nil && caller.Kind == marotte.CallerKindRule && caller.Event != ""
	e.mu.Lock()
	leaseDepth = e.depthsLocked(chatID).openDepthLocked(turnID, source, caller, wake, now)
	if settles {
		e.opened[chatID] = append(e.opened[chatID], openedPrompt{rule: caller.Rule, event: caller.Event})
		e.dirty[chatID] = true
	}
	e.mu.Unlock()
	if settles {
		e.signal()
	}
	return leaseDepth
}

// TurnClosed is the turn-end tap, called once the turn's close and LastTurnEndedAt are on disk: the Bot may be free,
// and the turn's depth becomes the chat's last closed one. A chat the engine does not know as a Bot wakes no pass.
func (e *Engine) TurnClosed(chatID marotte.ChatID, turnID string, _ *marotte.Caller) {
	e.mu.Lock()
	if d := e.depths[chatID]; d != nil {
		d.closeDepthLocked(turnID)
	}
	known := e.known[chatID]
	if known {
		e.dirty[chatID] = true
	}
	e.mu.Unlock()
	if known {
		e.signal()
	}
}

// Status answers what the engine holds in memory about chatID.
func (e *Engine) Status(chatID marotte.ChatID) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Status{WriteFailingSince: e.failingSince, CeilingUntil: e.ceiling[chatID]}
}

func (e *Engine) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) takeDirty() map[marotte.ChatID]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.dirty
	e.dirty = map[marotte.ChatID]bool{}
	for id := range d {
		e.known[id] = true
	}
	return d
}

func (e *Engine) takeOpened(chatID marotte.ChatID) []openedPrompt {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.opened[chatID]
	delete(e.opened, chatID)
	return o
}

// step is one pass of the loop; it answers when the next one is owed.
func (e *Engine) step(ctx context.Context) time.Time {
	now := time.Now()
	if !e.open() || (e.faulting && !e.retryPending(ctx)) {
		return now.Add(tick)
	}
	if !e.pass(ctx, now) {
		return now.Add(tick)
	}
	if e.faulting {
		e.resume()
	}
	obs.RuleQueueEvents(sum(e.waiting))
	next := now.Add(tick)
	for _, at := range e.due {
		if at.Before(next) {
			next = at
		}
	}
	return next
}

// open reports the loop's gates open: the index built from the store's first complete scan, and the agent engine
// ready once since the process started.
func (e *Engine) open() bool {
	if !e.indexed {
		ids, complete := e.deps.Store.BotIDs()
		if !complete {
			return false
		}
		e.mu.Lock()
		for _, id := range ids {
			e.index[id], e.known[id] = true, true
		}
		e.mu.Unlock()
		e.indexed = true
	}
	if !e.ready {
		e.ready = e.deps.Ready()
	}
	return e.ready
}

// pass processes every Bot that was reindexed, is due, or is not yet settled in this process, and reports whether
// it ran to the end; a fault or a drain stops it with what it had not reached still owed.
func (e *Engine) pass(ctx context.Context, now time.Time) bool {
	dirty := e.takeDirty()
	for id := range dirty {
		e.index[id] = true
	}
	ids := slices.Sorted(maps.Keys(e.index))
	for i, id := range ids {
		if !dirty[id] && !e.owed(id, now) {
			continue
		}
		next, err := e.process(ctx, id, now)
		if err != nil {
			e.owe(ids[i:], dirty)
			e.due[id] = now
			return false
		}
		if next.IsZero() {
			delete(e.due, id)
		} else {
			e.due[id] = next
		}
	}
	return true
}

// owe marks again the reindexed Bots a stopped pass did not finish, so the next pass takes them.
func (e *Engine) owe(ids []marotte.ChatID, dirty map[marotte.ChatID]bool) {
	for _, id := range ids {
		if dirty[id] {
			e.Reindex(id)
		}
	}
}

// owed reports chatID owed a pass at now: not yet settled in this process, or due.
func (e *Engine) owed(chatID marotte.ChatID, now time.Time) bool {
	at, ok := e.due[chatID]
	return !e.settled[chatID] || (ok && !at.After(now))
}

func sum(m map[marotte.ChatID]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// forget drops chatID from the index: it is gone, or no longer holds a rule.
func (e *Engine) forget(chatID marotte.ChatID) {
	delete(e.index, chatID)
	delete(e.due, chatID)
	delete(e.settled, chatID)
	delete(e.retryAt, chatID)
	delete(e.waiting, chatID)
	e.mu.Lock()
	delete(e.known, chatID)
	delete(e.ceiling, chatID)
	delete(e.opened, chatID)
	e.mu.Unlock()
}

// process settles, evaluates and delivers chatID's rules at now, and answers when chatID next needs a pass, zero
// for never until a tap or a command reindexes it. An error is a write fault or a drain: the pass stops.
func (e *Engine) process(ctx context.Context, chatID marotte.ChatID, now time.Time) (time.Time, error) {
	c, ok := e.deps.Store.Get(ctx, chatID)
	if !ok || c.Bot == nil || len(c.Bot.Rules) == 0 {
		e.forget(chatID)
		return time.Time{}, nil
	}
	c, err := e.settle(ctx, chatID, c, now)
	if err != nil || c == nil {
		return e.stopped(chatID, err)
	}
	if c, err = e.evaluateDue(ctx, chatID, c, now); err != nil || c == nil {
		return e.stopped(chatID, err)
	}
	deliverAt, err := e.deliver(ctx, chatID, c, now)
	if err != nil {
		return e.stopped(chatID, err)
	}
	if c, ok = e.deps.Store.Get(ctx, chatID); !ok || c.Bot == nil {
		e.forget(chatID)
		return time.Time{}, nil
	}
	e.waiting[chatID] = waitingEvents(c)
	return earliest(deliverAt, e.nextDue(chatID, c, now), nextSettle(c, now)), nil
}

// stopped is process's answer for a stage that ended it: a fault or drain stops the pass, a chat gone is forgotten,
// and anything else (a held row the boot could not take back) retries at the next tick.
func (e *Engine) stopped(chatID marotte.ChatID, err error) (time.Time, error) {
	switch {
	case errors.Is(err, errStopped), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return time.Time{}, err
	case err != nil:
		return time.Now().Add(tick), nil
	}
	e.forget(chatID)
	return time.Time{}, nil
}

func earliest(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if !t.IsZero() && (out.IsZero() || t.Before(out)) {
			out = t
		}
	}
	return out
}

func waitingEvents(c *marotte.Chat) int {
	n := 0
	for i := range c.Bot.Rules {
		n += len(c.Bot.Rules[i].Status.Queue)
	}
	return n
}

// write applies to chatID's record under the store's lock and answers the record as it stands after, nil when the
// chat or its bot block is gone. apply reports whether it changed anything; a change moves RulesVersion.
func (e *Engine) write(ctx context.Context, chatID marotte.ChatID, apply func(c *marotte.Chat) bool) (*marotte.Chat, error) {
	var seen *marotte.Chat
	_, err := e.deps.Store.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || c.Bot == nil {
			return false
		}
		seen = c
		if !apply(c) {
			return false
		}
		c.Bot.RulesVersion++
		return true
	})
	switch {
	case err == nil:
		return seen, nil
	case errors.Is(err, chat.ErrTombstoned):
		return nil, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	e.fault(chatID, err)
	return nil, fmt.Errorf("%w: %w", errStopped, err)
}

// writeAfterSend is write for an outcome owed by an act already done on the chat (a prompt handed to it, a row taken
// back): a drain does not cancel it, and on a fault it is kept and retried as it is, before the loop runs again.
// done, when set, runs once it lands.
func (e *Engine) writeAfterSend(ctx context.Context, chatID marotte.ChatID, apply func(c *marotte.Chat) bool, done func()) (*marotte.Chat, error) {
	after, err := e.write(context.WithoutCancel(ctx), chatID, apply)
	switch {
	case errors.Is(err, errStopped):
		e.pending = append(e.pending, pendingWrite{chat: chatID, apply: apply, done: done})
	case err == nil && done != nil:
		done()
	}
	return after, err
}

// retryPending retries the outcome writes a fault left owed, in order, and reports whether all landed.
func (e *Engine) retryPending(ctx context.Context) bool {
	for len(e.pending) > 0 {
		p := e.pending[0]
		if _, err := e.write(context.WithoutCancel(ctx), p.chat, p.apply); err != nil {
			return false
		}
		e.pending = e.pending[1:]
		if p.done != nil {
			p.done()
		}
	}
	return true
}

// fault stops the loop on a record write that failed, loudly once: the ERROR line, the gauge and the setup's
// banner. Run retries every tick until a pass lands.
func (e *Engine) fault(chatID marotte.ChatID, err error) {
	if e.faulting {
		return
	}
	e.faulting = true
	e.mu.Lock()
	e.failingSince = time.Now()
	e.mu.Unlock()
	obs.RuleEngineWriteFailing(true)
	e.log.Error("rule engine could not write a Bot's record", "chat_id", chatID, "error", err)
}

func (e *Engine) resume() {
	e.faulting = false
	e.mu.Lock()
	e.failingSince = time.Time{}
	e.mu.Unlock()
	obs.RuleEngineWriteFailing(false)
	e.log.Info("rule engine resumed")
}
