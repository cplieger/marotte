package agent

// Every terminal step of the per-chat turn lifecycle goes through finalizeTurn.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// TurnState is the per-chat turn state machine.
type TurnState int

const (
	turnIdle TurnState = iota
	turnOpen
	// turnFinalizing IS the exclusion: an operation that finds it WAITS, so a
	// finalize's persistence and broadcast need no held lock.
	turnFinalizing
)

// ErrTurnSlotHeld is OpenTurn's refusal when the slot the source needs is held: a
// local_shell over an open turn, or a prompt while one is still owed its bracket.
var ErrTurnSlotHeld = errors.New("the chat's turn slot is held")

// Turn is everything true of one turn, for its whole life, whoever opened it.
// ID, Seq, Chat, Opened, Source, Log and done are written once at open and read
// without the lock; every other field is guarded by the owning chatLifecycle's
// mutex. Log guards its own state (turnlog.Turn), so a reader on another
// goroutine calls it without lc.mu.
type Turn struct {
	// Log is this turn's accumulator over the chat's entry log, so one turn's open
	// lane cannot extend the next turn's entry.
	Log *turnlog.Turn
	// lc is the lifecycle that OWNS this turn, carried rather than re-resolved from
	// Chat: a forgotten chat's lookup mints a FRESH lifecycle, so re-resolving hands
	// an operation a different mutex and a different wake channel.
	lc *chatLifecycle
	// done is closed once, at finalize.
	done      chan struct{}
	reapTimer *time.Timer
	// Opened is when the turn began, and the only source of its elapsed time.
	Opened time.Time
	// reapChainAt is when this turn's compaction-reap chain began. A tool in
	// flight extends the budget only if it started at or after it, so a status
	// the failed compaction left behind cannot extend it forever.
	reapChainAt time.Time
	// opened is the turn_open entry as written, which the turn_opened frame carries.
	opened *marotte.Entry
	// ID is the turn_open entry's id: every handle on the turn.
	ID string
	// Model is the model answering, stamped at StartTurn for a prompt and at open
	// for every other source.
	Model string
	Chat  marotte.ChatID
	// Interrupt is the first cause claimed for this turn.
	Interrupt marotte.InterruptCause
	// statusDesc is the description the agent declared DURING this turn, which is what
	// the agent_finished push body says. On the turn rather than the chat, because the
	// chat's copy outlives its turn on purpose and so cannot answer for one.
	statusDesc string
	// result is immutable once done is closed.
	result marotte.TurnResult
	// Seq is the chat's open sequence, stamped at open and never persisted: the
	// registry's own order, distinct from the transcript's n.
	Seq uint64
	// NeedSeq is the read loop position a LOCAL settle of this turn must wait for:
	// where the session/prompt response arrived. Zero means no settle is waiting.
	NeedSeq      uint64
	reapArmedSeq uint64
	reapArmedGen uint64
	// reapArmID names the newest reap arm; an expiry carrying an older id is a stale
	// timer whose Stop lost the race and must not act.
	reapArmID uint64
	// needGen is the forward generation NeedSeq belongs to: a position minted
	// against one bridge means nothing against the next.
	needGen uint64
	// holds is how many completion handles are outstanding, and what bounds
	// retention: a result a waiter still holds a handle for is never evicted.
	holds  int
	Source marotte.TurnOpenSource
	// acked is whether a wire turn_start has bound to this turn. Provisional for an
	// acknowledgeable source, since the bracket cannot tell a prompted turn from an
	// agent-initiated one; reviseLocked re-targets it.
	acked bool
	// finalizing is whether a closer holds this turn's claim: a second claim and a
	// fold both treat a finalizing turn as absent.
	finalizing bool
}

// chatLifecycle is one chat's turn state machine.
type chatLifecycle struct {
	// own is the chat's own open turn that frames fold into; at most one.
	own *Turn
	// pending is the prompt-class turn awaiting its wire bracket, usually own
	// itself. One rather than a queue: admission serializes prompt-class sources.
	pending *Turn
	// retained holds the FINALIZED turns whose handles have not all been released,
	// so a waiter can still read a result after the chat has moved on. Several
	// handles can be outstanding at once, hence a map.
	retained map[string]*Turn
	// changed is closed and REPLACED on EVERY state change, under mu. A channel
	// rather than a Cond, which re-acquires the mutex to return, so a parked waiter
	// would hold the lock the finalize needs.
	changed chan struct{}
	// nextSeq is the open sequence the next record takes.
	nextSeq uint64
	// observedSeq is the read loop position the FOLDER has reached. Advanced for
	// every frame consumed, not only the ones that touch a turn — see observe.
	observedSeq uint64
	// fwdGen counts the forward goroutines attached for this chat: a new bridge
	// restarts its sequence at zero, so positions compare only within a generation.
	fwdGen uint64
	// reservedSource is who holds the admission slot, meaningful only while reserved
	// is true. The reservation is NOT a Turn: it is the bare per-chat admission taken
	// before any bridge exists.
	reservedSource marotte.TurnOpenSource
	// forwardGone is whether the attached forward goroutine has exited, so a settle
	// waiting on a position that can no longer advance stops waiting.
	forwardGone bool
	// reserved is whether the admission slot is held.
	reserved bool
	state    TurnState
	mu       sync.Mutex
}

// turnRegistry holds one lifecycle per chat. Lock order is registry.mu ->
// lifecycle.mu -> the chat store's lock: OpenTurn and every closer append under
// the lifecycle mutex, so the log and the registry never disagree about which
// turns exist.
type turnRegistry struct {
	chats map[marotte.ChatID]*chatLifecycle
	mu    sync.Mutex
}

func newTurnRegistry() *turnRegistry {
	return &turnRegistry{chats: make(map[marotte.ChatID]*chatLifecycle)}
}

// lifecycleFor returns the chat's lifecycle, creating it on first use.
func (r *turnRegistry) lifecycleFor(chatID marotte.ChatID) *chatLifecycle {
	r.mu.Lock()
	defer r.mu.Unlock()
	lc, ok := r.chats[chatID]
	if !ok {
		lc = &chatLifecycle{changed: make(chan struct{})}
		r.chats[chatID] = lc
	}
	return lc
}

// lookup returns the chat's lifecycle WITHOUT creating one, reporting false when
// the chat has none. The read-only twin of lifecycleFor: an entry leaves the map
// only through forget, so a predicate on an HTTP read path would otherwise leave a
// lifecycle and its `changed` channel behind for every chat merely opened.
func (r *turnRegistry) lookup(chatID marotte.ChatID) (*chatLifecycle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lc, ok := r.chats[chatID]
	return lc, ok
}

// forget drops a chat's lifecycle. A turn already open keeps the dropped one, since
// every operation holding a *Turn goes through that turn's own lc, so an in-flight
// finalize still publishes where its waiters are parked.
func (r *turnRegistry) forget(chatID marotte.ChatID) {
	r.mu.Lock()
	delete(r.chats, chatID)
	r.mu.Unlock()
}

// wakeLocked wakes every waiter without moving the state. Caller holds mu.
func (lc *chatLifecycle) wakeLocked() {
	close(lc.changed)
	lc.changed = make(chan struct{})
}

// setStateLocked moves the state and wakes every waiter. Caller holds mu.
func (lc *chatLifecycle) setStateLocked(s TurnState) {
	lc.state = s
	lc.wakeLocked()
}

// awaitNotFinalizing blocks while the chat's own turn is finalizing and returns
// with the mutex HELD, or reports false when ctx died first (mutex released). The
// next own turn is not observable as open until the previous one's effects
// completed.
func (lc *chatLifecycle) awaitNotFinalizing(ctx context.Context) bool {
	lc.mu.Lock()
	for lc.state == turnFinalizing {
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
		lc.mu.Lock()
	}
	return true
}

// openLocked creates the record for a turn whose turn_open the caller has just
// appended, in the slot its source names: a prompt-class source goes to pending
// and to own when own is nil; every other source goes to own. Caller holds mu and
// has established that the slot the source needs is free.
func (lc *chatLifecycle) openLocked(chatID marotte.ChatID, opened *marotte.Entry, source marotte.TurnOpenSource, model string, log *turnlog.Turn) *Turn {
	lc.nextSeq++
	t := &Turn{
		Opened: time.Now(),
		Model:  model,
		Chat:   chatID,
		opened: opened,
		ID:     opened.ID,
		Seq:    lc.nextSeq,
		Log:    log,
		Source: source,
		done:   make(chan struct{}),
		lc:     lc,
	}
	if source.Acknowledgeable() {
		lc.pending = t
		if lc.own == nil {
			lc.own = t
		}
	} else {
		lc.own = t
	}
	lc.setStateLocked(turnOpen)
	return t
}

// slotFreeLocked reports whether a turn of source may open now: local_shell is
// refused while own is held, a prompt-class source while a pending turn is still
// owed its bracket. Caller holds mu.
func (lc *chatLifecycle) slotFreeLocked(source marotte.TurnOpenSource) bool {
	switch {
	case source == marotte.TurnSourceLocalShell:
		return lc.own == nil
	case source.Acknowledgeable():
		return lc.pending == nil
	default:
		return lc.own == nil
	}
}

// claimOwn claims the chat's OWN open turn for finalizing, reporting false when the
// chat has none or it is already claimed. First-wins, so two closers racing one
// turn produce one set of effects.
func (r *turnRegistry) claimOwn(ctx context.Context, chatID marotte.ChatID) (*Turn, bool) {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil, false
	}
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing {
		return nil, false
	}
	return lc.claimLocked(lc.own), true
}

// claimTurn claims ONE named turn for finalizing, reporting false when that id is
// not live on this chat. Id-scoped, so a closer armed for turn N is harmless once
// turn N+1 has opened, and it still reaches a pending turn that is no longer own.
func (r *turnRegistry) claimTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (*Turn, bool) {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil, false
	}
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil || t.finalizing {
		return nil, false
	}
	return lc.claimLocked(t), true
}

// liveLocked finds an OPEN record by id, own or pending, nil otherwise. Caller
// holds mu.
func (lc *chatLifecycle) liveLocked(turnID string) *Turn {
	if lc.own != nil && lc.own.ID == turnID {
		return lc.own
	}
	if lc.pending != nil && lc.pending.ID == turnID {
		return lc.pending
	}
	return nil
}

// claimLocked marks t finalizing and, when t is own, moves the chat into
// turnFinalizing so the next own open waits. Caller holds mu and has established
// that t is live and unclaimed.
func (lc *chatLifecycle) claimLocked(t *Turn) *Turn {
	lc.stopReapLocked(t)
	t.finalizing = true
	if lc.own == t {
		lc.setStateLocked(turnFinalizing)
	} else {
		lc.wakeLocked()
	}
	return t
}

// finish publishes a claimed turn's result and returns the chat to idle.
//
// Ordering is the contract: the result is stored and done closed before the state
// moves, so a waiter woken by the transition always finds it. It publishes on the
// turn's OWN lifecycle, since a chat forgotten mid-finalize would be handed a fresh
// one here and leave every parked waiter in turnFinalizing forever.
func (r *turnRegistry) finish(t *Turn, result marotte.TurnResult) {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	result.Turn = t.ID
	t.result = result
	close(t.done)
	if lc.own == t {
		lc.own = nil
	}
	// Or the NEXT prompt's turn_start binds to this dead turn.
	if lc.pending == t {
		lc.pending = nil
	}
	if t.holds > 0 {
		if lc.retained == nil {
			lc.retained = make(map[string]*Turn)
		}
		lc.retained[t.ID] = t
	}
	// A pending turn left over from a revised binding is still owed a bracket, so
	// the chat is not idle just because own closed.
	if lc.own != nil || lc.pending != nil {
		lc.setStateLocked(turnOpen)
		return
	}
	lc.setStateLocked(turnIdle)
}

// turnLocked finds the record for one id, open or retained. Caller holds mu.
func (lc *chatLifecycle) turnLocked(turnID string) *Turn {
	if t := lc.liveLocked(turnID); t != nil {
		return t
	}
	return lc.retained[turnID]
}

// release gives up one completion handle, dropping a finalized record once its
// last handle goes. An OPEN turn's record is dropped by finish, not here.
func (r *turnRegistry) release(chatID marotte.ChatID, turnID string) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(turnID)
	if t == nil {
		return
	}
	t.holds--
	if t.holds <= 0 {
		delete(lc.retained, turnID)
	}
}

// await blocks until the named turn has finalized and returns its result, or
// reports ErrNoSuchTurn for an id this chat has no record of. The done channel is
// taken under the mutex and waited on with it RELEASED, or the waiter blocks the
// finalize it waits for; close-before-receive publishes finish's write.
func (r *turnRegistry) await(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	t := lc.turnLocked(turnID)
	lc.mu.Unlock()
	if t == nil {
		return marotte.TurnResult{}, marotte.ErrNoSuchTurn
	}
	select {
	case <-t.done:
		return t.result, nil
	case <-ctx.Done():
		return marotte.TurnResult{}, ctx.Err()
	}
}

// openedAfter reports whether any own turn on this chat was opened after the named
// one: the STRUCTURAL clause of the empty-turn gate, since a mis-bound pending turn
// satisfies every other clause. It compares the open sequence, never n: a step
// opens nothing on the chat, so the gate answers on the chat's own turns alone.
func (r *turnRegistry) openedAfter(chatID marotte.ChatID, turnID string) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(turnID)
	if t == nil {
		return false
	}
	return lc.nextSeq > t.Seq
}

// turnByID reports the live turn the id names, or false when the chat holds none
// under it or it is finalizing.
func (r *turnRegistry) turnByID(chatID marotte.ChatID, turnID string) (*Turn, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil || t.finalizing {
		return nil, false
	}
	return t, true
}

// holdsTurn answers the chat whose own or pending slot holds the turn id: the
// digest's live_turn lookup, which has a turn id and no chat. Through every
// lifecycle, because the ref names no chat; the registry is small.
func (r *turnRegistry) holdsTurn(turnID string) (marotte.ChatID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for chatID, lc := range r.chats {
		lc.mu.Lock()
		held := (lc.own != nil && lc.own.ID == turnID) || (lc.pending != nil && lc.pending.ID == turnID)
		lc.mu.Unlock()
		if held {
			return chatID, true
		}
	}
	return "", false
}

// ownTurn reports the chat's own open turn, or false when none is open or it is
// finalizing: this one is asked by a caller about to ACT on the turn, and a claimed
// turn's effects are already running.
func (r *turnRegistry) ownTurn(chatID marotte.ChatID) (*Turn, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing {
		return nil, false
	}
	return lc.own, true
}

// currentTurn reports which turn a chat's activity belongs to right now — own,
// open or finalizing — and false when the chat holds none. Wider than ownTurn on
// purpose: a turn whose effects are still running is still the turn that spawned a
// process.
func (r *turnRegistry) currentTurn(chatID marotte.ChatID) (string, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil {
		return "", false
	}
	return lc.own.ID, true
}

// live reports whether the chat has a turn the reader must treat as running: an
// own turn, a pending turn, or a held admission slot. turnFinalizing counts, exactly
// the state a client told the chat is idle would then get a turn_closed for. Through
// lookup: this is an HTTP read path.
func (r *turnRegistry) live(chatID marotte.ChatID) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.liveLockedState()
}

// liveLockedState is live's predicate, spelled once. Caller holds mu.
func (lc *chatLifecycle) liveLockedState() bool {
	return lc.own != nil || lc.pending != nil || lc.reserved
}

// openTurnIDs reports the id and newest sealed seq of every open turn on the chat,
// own and a distinct pending alike, for the digest's live_turn stamps. Through
// lookup for live's reason.
func (r *turnRegistry) openTurnIDs(chatID marotte.ChatID) []*Turn {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	out := make([]*Turn, 0, 2)
	if lc.own != nil {
		out = append(out, lc.own)
	}
	if lc.pending != nil && lc.pending != lc.own {
		out = append(out, lc.pending)
	}
	return out
}

// ownTurns returns the own open turn of every chat that has one, so a connect
// replay reads the turn rather than the prompt slot, which is empty for every turn
// marotte did not prompt. Lock order is registry.mu -> lifecycle.mu.
func (r *turnRegistry) ownTurns() map[marotte.ChatID]*Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[marotte.ChatID]*Turn, len(r.chats))
	for id, lc := range r.chats {
		lc.mu.Lock()
		if lc.own != nil {
			out[id] = lc.own
		}
		lc.mu.Unlock()
	}
	return out
}

// busyChatIDs is every chat with a turn in flight, which is the only population a
// client's stale-`thinking` retraction may be withheld from. The predicate is live's,
// so the handshake and the transcript GET cannot disagree.
func (r *turnRegistry) busyChatIDs() []marotte.ChatID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]marotte.ChatID, 0, len(r.chats))
	for id, lc := range r.chats {
		lc.mu.Lock()
		busy := lc.liveLockedState()
		lc.mu.Unlock()
		if busy {
			out = append(out, id)
		}
	}
	return out
}

// interrupt records why the named turn was interrupted, first cause wins. Id-scoped
// so a cause cannot land on a turn it did not describe; first-wins so a user cancel
// and the tool-use filter firing in one window do not relabel each other.
func (r *turnRegistry) interrupt(chatID marotte.ChatID, turnID string, cause marotte.InterruptCause) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil || t.Interrupt != "" {
		return false
	}
	t.Interrupt = cause
	return true
}

// interruptCause reads a claimed turn's cause, under the turn's OWN lifecycle:
// re-resolving one by chat id would read the field under a different mutex than the
// writer's.
func (r *turnRegistry) interruptCause(t *Turn) marotte.InterruptCause {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return t.Interrupt
}

// stageStatusDescription records a declared description on the chat's own turn.
// Uses lookup rather than lifecycleFor: a declaration for a chat with no lifecycle
// must not mint one. An empty description is not a declaration and is dropped, so
// the discharge's own frame cannot wipe a turn's words.
func (r *turnRegistry) stageStatusDescription(chatID marotte.ChatID, desc string) {
	if desc == "" {
		return
	}
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own != nil {
		lc.own.statusDesc = desc
	}
}

// statusDescription reads a claimed turn's staged description, under the turn's OWN
// lifecycle for interruptCause's reason.
func (r *turnRegistry) statusDescription(t *Turn) string {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return t.statusDesc
}
