package agent

// Every terminal step of the per-chat turn lifecycle goes through finalizeTurn.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// turnState is the per-chat turn state machine.
type turnState int

const (
	turnIdle turnState = iota
	turnOpen
	// turnFinalizing IS the exclusion: an operation that finds it WAITS, so a
	// finalize's persistence and broadcast need no held lock.
	turnFinalizing
)

// errTurnSlotHeld is OpenTurn's refusal when the slot the source needs is held: a
// local_shell over an open turn, or a prompt while one is still owed its bracket.
var errTurnSlotHeld = errors.New("the chat's turn slot is held")

// activeTurn is everything true of one turn, whoever opened it. ID, Seq, Chat, Opened, Source, Log and done are
// written once at open and read lock-free; the rest are guarded by the owning chatLifecycle's mutex. Log guards itself.
type activeTurn struct {
	// Log is this turn's accumulator over the chat's entry log, so one turn's open
	// lane cannot extend the next turn's entry.
	Log *turnlog.Turn
	// lc is the owning lifecycle, carried rather than re-resolved: a forgotten chat's lookup mints a fresh one with
	// another mutex and wake channel.
	lc *chatLifecycle
	// done is closed once, at finalize.
	done      chan struct{}
	reapTimer *time.Timer
	// graceTimer is a stop's budget on this turn's call (armCallGraceLocked).
	graceTimer *time.Timer
	// cancel trips this turn's own prompt call, from StartTurn until EndPromptCall; nil for a turn with none (KAS's,
	// a local shell).
	cancel context.CancelCauseFunc
	// Opened is when the turn began, and the only source of its elapsed time.
	Opened time.Time
	// reapChainAt is when this turn's compaction-reap chain began; only a tool started at or after it extends the budget.
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
	// statusDesc is the description declared during this turn, the agent_finished push body. On the turn: the chat's copy outlives turns.
	statusDesc string
	// result is immutable once done is closed.
	result marotte.TurnResult
	// Seq is the chat's open sequence, stamped at open and never persisted: the
	// registry's own order, distinct from the transcript's n.
	Seq uint64
	// NeedSeq is the read-loop position of the session/prompt response a local settle must wait for; zero means none waiting.
	NeedSeq      uint64
	reapArmedSeq uint64
	reapArmedGen uint64
	// reapArmID names the newest reap arm; an expiry carrying an older id is a stale
	// timer whose Stop lost the race and must not act.
	reapArmID uint64
	// admission is the reservation a prompt-class turn opened under (openLocked), zero for none.
	admission uint64
	// sendStops is the chat's stopCount when the gate let this turn's prompt through; the gate refuses a later attempt
	// once it moved, so it stays the first attempt's, and each attempt's write commit refuses once it moves.
	sendStops uint64
	// idleArms counts KAS's idle reports that armed this turn's idle close (WireTurnActivity); an expiry carrying an
	// older count, or arriving after kasIdle cleared, must not act.
	idleArms uint64
	// holds is how many completion handles are outstanding, and what bounds
	// retention: a result a waiter still holds a handle for is never evicted.
	holds  int
	Source marotte.TurnOpenSource
	// acked is whether a wire turn_start bound to this turn, provisional for an acknowledgeable source; reviseLocked re-targets it.
	acked bool
	// bracketed is whether a wire turn_start opened this wire turn, so its turn_end is owed; a fold opens one with no
	// bracket, which may never close, and admission must not refuse a prompt beside it (bracketedWireLocked).
	bracketed bool
	// kasIdle is whether KAS's newest activity report since this turn opened said no turn runs.
	kasIdle bool
	// sent is whether the send gate let this prompt-class turn's session/prompt go at least once (markSentLocked); a
	// write commit may still refuse each frame (sendCommitLocked).
	sent bool
	// stopped is whether the reader's stop keeps this unsent prompt from being sent (ArmStopGrace).
	stopped bool
	// finalizing is whether a closer holds this turn's claim: a second claim and a
	// fold both treat a finalizing turn as absent.
	finalizing bool
}

type chatLifecycle struct {
	// own is the chat's own open turn that frames fold into; at most one.
	own *activeTurn
	// pending is the prompt-class turn awaiting its wire bracket, usually own
	// itself. One rather than a queue: admission serializes prompt-class sources.
	pending *activeTurn
	// call is the sent prompt whose session/prompt is unanswered, from its send to EndPromptCall; it outlives the
	// turn when a later turn_start closed it. One: the bridge slot serializes calls.
	call *activeTurn
	// stops holds the send hold of each stop in progress, by token (requestStop).
	stops map[uint64]struct{}
	// retained holds finalized turns whose handles are not all released, so a waiter can read a result after the chat moved on.
	retained map[string]*activeTurn
	// fwdExits holds, per forward generation still running, the channel its
	// goroutine closes on exit.
	fwdExits map[uint64]chan struct{}
	// changed is closed and replaced on every state change, under mu. A channel, not a Cond, which would re-take the
	// lock the finalize needs.
	changed chan struct{}
	// restoreSession is the session whose writes are KAS applying or rolling back reviewed changes
	// (expectRestores), "" for none.
	restoreSession string
	// kas is KAS's execution state for the chat's session; only kasStepLocked changes it.
	kas kasExec
	// nextSeq is the open sequence the newest record took; openLocked increments it
	// before assigning, so the next record takes nextSeq+1.
	nextSeq uint64
	// epoch names this lifecycle among the chat's; a fence from another epoch never holds. Re-minted at teardown so a surviving drain opens nothing.
	epoch uint64
	// stopSeq is nextSeq when a stop was last requested: every turn at or below it was open then. Zero is no stop.
	stopSeq uint64
	// admission names the held reservation, re-minted by each reserveLocked; stoppedAdmission is one a stop kept
	// unsent before its turn opened (ArmStopGrace), which openLocked passes to the turn opened under it.
	admission        uint64
	stoppedAdmission uint64
	// observedSeq is the read loop position the FOLDER has reached. Advanced for
	// every frame consumed, not only the ones that touch a turn — see observe.
	observedSeq uint64
	// fwdGen counts the forward goroutines attached for this chat: a new bridge
	// restarts its sequence at zero, so positions compare only within a generation.
	fwdGen uint64
	// reservedSource is who holds the admission slot while reserved; the reservation is not a Turn.
	reservedSource marotte.TurnOpenSource
	// forwardGone is whether the attached forward goroutine has exited, so a settle
	// waiting on a position that can no longer advance stops waiting.
	forwardGone bool
	// inFrame is whether the forward goroutine is consuming a frame (beginFrame to observe).
	inFrame bool
	// reserved is whether the admission slot is held.
	reserved bool
	state    turnState
	// stopCount counts requestStop calls; atomic so a send's write commit reads it under the engine's write lock
	// without mu (sendCommitLocked).
	stopCount atomic.Uint64
	mu        sync.Mutex
	// switchMu is held by every writer of pending_model (the switch's admission, the idle or
	// turn-close apply, the session open's fold): otherwise two appliers read one pick and apply it twice,
	// or a landing falls between an admission's read and its park. Held across the bridge call.
	switchMu sync.Mutex
}

// turnRegistry holds one lifecycle per chat. Lock order registry.mu -> lifecycle.mu -> chat store: opens and
// closers append under the lifecycle mutex, so log and registry agree.
type turnRegistry struct {
	chats map[marotte.ChatID]*chatLifecycle
	// onIdle runs, with no lock held, each time a chat leaves its last open turn or admission: an engine restart
	// pending on quiescence re-checks there, and so does the chat's release check. Set once at construction.
	onIdle func(marotte.ChatID)
	// claimHook runs just before a named claim's section and each send-gate section; nil in production. A test sets
	// it before driving the frames, closers or prompt that read it.
	claimHook func()
	// openHook runs at the top of OpenTurn, before its section; nil in production, set as claimHook is.
	openHook func()
	epochs   atomic.Uint64
	// stopTokens mints each stop's send-hold token.
	stopTokens atomic.Uint64
	mu         sync.Mutex
}

func newTurnRegistry() *turnRegistry {
	return &turnRegistry{chats: make(map[marotte.ChatID]*chatLifecycle)}
}

func (r *turnRegistry) lifecycleFor(chatID marotte.ChatID) *chatLifecycle {
	r.mu.Lock()
	defer r.mu.Unlock()
	lc, ok := r.chats[chatID]
	if !ok {
		lc = &chatLifecycle{changed: make(chan struct{}), epoch: r.epochs.Add(1)}
		r.chats[chatID] = lc
	}
	return lc
}

// fenceHoldsLocked reports whether no turn opened on this lifecycle after the one
// the fence names. The zero fence is unfenced. Caller holds mu.
func (lc *chatLifecycle) fenceHoldsLocked(f command.TurnFence) bool {
	return f.Epoch == 0 || (lc.epoch == f.Epoch && lc.nextSeq <= f.Seq)
}

func fenceOf(t *activeTurn) command.TurnFence {
	t.lc.mu.Lock()
	defer t.lc.mu.Unlock()
	return command.TurnFence{Epoch: t.lc.epoch, Seq: t.Seq}
}

// refence re-mints the chat's lifecycle epoch, so every earlier fence stops holding.
func (r *turnRegistry) refence(chatID marotte.ChatID) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	lc.epoch = r.epochs.Add(1)
	lc.wakeLocked()
	lc.mu.Unlock()
}

// lookup returns the chat's lifecycle without creating one; on a read path lifecycleFor would leak one per opened chat.
func (r *turnRegistry) lookup(chatID marotte.ChatID) (*chatLifecycle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lc, ok := r.chats[chatID]
	return lc, ok
}

// forget drops a chat's lifecycle. An open turn keeps the dropped one through its own lc, so an in-flight finalize
// publishes where its waiters park.
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
func (lc *chatLifecycle) setStateLocked(s turnState) {
	lc.state = s
	lc.wakeLocked()
}

// awaitNotFinalizing blocks while the own turn finalizes and returns with mu held, or false (released) when ctx
// died. The next own turn is unobservable until the previous one's effects completed.
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

// openLocked records a turn whose turn_open the caller just appended: prompt-class to pending (and own when own is
// nil), every other source to own. Caller holds mu and checked the slot.
func (lc *chatLifecycle) openLocked(chatID marotte.ChatID, opened *marotte.Entry, source marotte.TurnOpenSource, model string, log *turnlog.Turn) *activeTurn {
	lc.nextSeq++
	lc.restoreSession = ""
	t := &activeTurn{
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
		if lc.reserved {
			t.admission = lc.admission
			t.stopped = lc.stoppedAdmission == lc.admission
		}
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

// awaitsSend reports a prompt-class turn whose session/prompt has not gone out: KAS cannot be running it, so no
// wire frame binds, folds into or ends it, and KAS's frames open a wire turn of their own.
func (t *activeTurn) awaitsSend() bool { return t.Source.Acknowledgeable() && !t.sent }

// sendStep is the send gate's verdict on an admitted prompt's turn (markSentLocked).
type sendStep int

const (
	sendWait      sendStep = iota // KAS reports a run, a frame is mid-fold, a stop holds sends, or another turn holds own
	sendNow                       // t owns the chat and is marked sent
	sendCloseFold                 // own was a fold-opened wire turn, now claimed; the caller closes it, then asks again
	sendStopped                   // the reader's stop keeps t unsent, or keeps a later attempt of it unwritten
)

// markSentLocked lets t's send through only once t owns the chat (caller holds mu, t live and unclaimed), since t's
// run may stream before its turn_start and those frames fold into own. By own's state, while KAS admits a
// session/prompt, no frame is being consumed (inFrame: it may be folding into own, or parked behind a close and due
// before the send) and no stop's session/cancel is still to go out (stops), a later stop being the write commit's:
//   - nil or t: t takes own and is let through.
//   - a fold-opened wire turn: no turn_end is owed for it, so it is claimed here, in the section that read the
//     verdict's state, and returned for the caller to close (sendCloseFold).
//   - anything else, or a turn another closer claimed: wait for its close.
func (lc *chatLifecycle) markSentLocked(t *activeTurn) (sendStep, *activeTurn) {
	o := lc.own
	switch {
	case t.stopped, t.sent && lc.stopCount.Load() != t.sendStops:
		return sendStopped, nil
	case !lc.kas.may().admit, lc.inFrame, len(lc.stops) > 0:
		return sendWait, nil
	case o == nil || o == t:
		t.sendStops = lc.stopCount.Load()
		t.sent = true
		lc.call = t
		if o == nil {
			lc.own = t
			lc.setStateLocked(turnOpen)
		}
		return sendNow, nil
	// The source check is defensive: t's reservation keeps every other source from opening.
	case o.Source == marotte.TurnSourceWireTurnStart && !o.bracketed && lc.claimIfLocked(o, nil):
		return sendCloseFold, o
	default:
		return sendWait, nil
	}
}

// slotFreeLocked reports whether source may open now: local_shell needs own free, prompt-class a pending turn
// not owed its bracket. Caller holds mu.
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

// claimBound claims, for a turn_end, the chat's own open turn unless it is a prompt its turn_start has not bound
// (not acked); false then, when none is open, or when it is already claimed. First-wins. An end cannot be safely
// attributed to an unacknowledged prompt: claiming it could retire the prompt's words before KAS reads them, and
// the prompt's own session/prompt response stays its closer.
func (r *turnRegistry) claimBound(ctx context.Context, chatID marotte.ChatID) (*activeTurn, bool) {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil, false
	}
	defer lc.mu.Unlock()
	t := lc.own
	if t == nil || !t.acked || !lc.claimIfLocked(t, nil) {
		return nil, false
	}
	return t, true
}

// claimRule is what a named claim checks beyond liveness, and what it starts.
type claimRule struct {
	// valid is the closer's own predicate, evaluated in the claim's section (claimIfLocked); nil for none.
	valid func(lc *chatLifecycle, t *activeTurn) bool
	// graceClose reports the claimed turn's close as marotte's cancel grace (claimTurn).
	graceClose func(t *activeTurn) bool
}

// claimTurn claims one named turn for finalizing, false when not live or when rule's predicate no longer holds.
// Id-scoped: a closer armed for turn N is harmless after N+1 opened, and reaches a pending turn that is no longer
// own. When rule's graceClose reports the claimed turn's close as marotte's cancel grace, the KAS hold starts in the
// same section, whatever KAS last reported: the claim lifts bracketedWireLocked's refusal, and KAS's report of the
// cancelled run may still be in flight. kasTransitions owns what the hold then permits.
func (r *turnRegistry) claimTurn(ctx context.Context, chatID marotte.ChatID, turnID string, rule *claimRule) (*activeTurn, bool) {
	if r.claimHook != nil {
		r.claimHook()
	}
	// lookup: a timer closer outliving forget must not mint an empty lifecycle for the chat.
	lc, ok := r.lookup(chatID)
	if !ok || !lc.awaitNotFinalizing(ctx) {
		return nil, false
	}
	defer lc.mu.Unlock()
	if rule == nil {
		rule = &claimRule{}
	}
	t := lc.liveLocked(turnID)
	if t == nil || !lc.claimIfLocked(t, rule.valid) {
		return nil, false
	}
	if rule.graceClose != nil && rule.graceClose(t) {
		lc.kasStepLocked(kasGraceClaim, t.Seq)
	}
	return t, true
}

// claimIfLocked claims t when it is unclaimed and valid (nil for none) holds, in one section with the reads valid
// makes: every closer decides and claims here, so no frame between its decision and its claim can stale the
// decision. Caller holds mu, t live.
func (lc *chatLifecycle) claimIfLocked(t *activeTurn, valid func(*chatLifecycle, *activeTurn) bool) bool {
	if t.finalizing || (valid != nil && !valid(lc, t)) {
		return false
	}
	lc.stopReapLocked(t)
	t.finalizing = true
	if lc.own == t {
		lc.setStateLocked(turnFinalizing)
	} else {
		lc.wakeLocked()
	}
	return true
}

// liveLocked finds an OPEN record by id, own or pending, nil otherwise. Caller
// holds mu.
func (lc *chatLifecycle) liveLocked(turnID string) *activeTurn {
	if lc.own != nil && lc.own.ID == turnID {
		return lc.own
	}
	if lc.pending != nil && lc.pending.ID == turnID {
		return lc.pending
	}
	return nil
}

// finish publishes a claimed turn's result and returns the chat to idle: result stored and done closed before the
// state moves. It publishes on the turn's own lifecycle, which a forgotten chat would otherwise strand.
func (r *turnRegistry) finish(t *activeTurn, result marotte.TurnResult) {
	lc := t.lc
	lc.mu.Lock()
	idle := lc.finishLocked(t, result)
	lc.mu.Unlock()
	if idle {
		r.idle(t.Chat)
	}
}

// idle reports a chat that left its last open turn or admission.
func (r *turnRegistry) idle(chatID marotte.ChatID) {
	if r.onIdle != nil {
		r.onIdle(chatID)
	}
}

// finishLocked is finish under mu, reporting whether the chat is now idle.
func (lc *chatLifecycle) finishLocked(t *activeTurn, result marotte.TurnResult) bool {
	result.Turn = t.ID
	t.result = result
	close(t.done)
	if lc.own == t {
		lc.own = nil
	}
	// Or the next prompt's turn_start binds to this dead turn.
	if lc.pending == t {
		lc.pending = nil
	}
	if t.holds > 0 {
		if lc.retained == nil {
			lc.retained = make(map[string]*activeTurn)
		}
		lc.retained[t.ID] = t
	}
	// A pending turn from a revised binding is still owed a bracket.
	if lc.own != nil || lc.pending != nil {
		lc.setStateLocked(turnOpen)
		return false
	}
	lc.setStateLocked(turnIdle)
	return !lc.liveLockedState()
}

// turnLocked finds the record for one id, open or retained. Caller holds mu.
func (lc *chatLifecycle) turnLocked(turnID string) *activeTurn {
	if t := lc.liveLocked(turnID); t != nil {
		return t
	}
	return lc.retained[turnID]
}

// finish drops open ones.
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

// await blocks until the named turn finalized and returns its result, or ErrNoSuchTurn. done is read under mu
// and waited on with mu released, or the waiter blocks its own finalize.
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

// awaitBound blocks until the named turn's turn_bind is on disk (true) or the turn finalized without one (false),
// or ErrNoSuchTurn.
func (r *turnRegistry) awaitBound(ctx context.Context, chatID marotte.ChatID, turnID string) (bool, error) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	t := lc.turnLocked(turnID)
	lc.mu.Unlock()
	if t == nil {
		return false, marotte.ErrNoSuchTurn
	}
	var bound <-chan struct{}
	if t.Log != nil {
		bound = t.Log.Bound()
	}
	select {
	case <-bound:
		return true, nil
	case <-t.done:
		// The bind precedes the turn_end that finalizes, but both can be ready here.
		select {
		case <-bound:
			return true, nil
		default:
			return false, nil
		}
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// openedAfter reports whether an own turn opened after the named one, the empty-turn gate's structural clause.
// It compares the open sequence, never n; steps open nothing here.
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

// expectRestores marks session's writes on chatID as KAS putting files back until the chat's next
// turn opens. KAS raises a turn approval once the turn's own writes are done, and refuses a rewind
// mid-turn, so until then the session's writes are KAS applying that review or that revert. A run
// chat opens no turns that would end the window, so it is never marked.
func (r *turnRegistry) expectRestores(chatID marotte.ChatID, session string) {
	r.markRestores(chatID, session, true)
}

// expectRevertRestores is expectRestores for a rewind, declined while a turn is open: KAS refuses
// that revert, so a mark would cover the open turn's own writes.
func (r *turnRegistry) expectRevertRestores(chatID marotte.ChatID, session string) {
	r.markRestores(chatID, session, false)
}

func (r *turnRegistry) markRestores(chatID marotte.ChatID, session string, overOpenTurn bool) {
	if session == "" || workflowIDOf(chatID) != "" {
		return
	}
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !overOpenTurn && (lc.own != nil || lc.pending != nil) {
		return
	}
	lc.restoreSession = session
}

func (r *turnRegistry) restoring(chatID marotte.ChatID, session string) bool {
	if session == "" {
		return false
	}
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.restoreSession == session
}

// stoppedAfter reports whether a stop was requested after the named turn opened,
// open or retained. A stop from an earlier turn is inert by construction.
func (r *turnRegistry) stoppedAfter(chatID marotte.ChatID, turnID string) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.stoppedAfterLocked(turnID)
}

// stoppedAfterLocked is stoppedAfter's predicate. Caller holds mu.
func (lc *chatLifecycle) stoppedAfterLocked(turnID string) bool {
	t := lc.turnLocked(turnID)
	return t != nil && t.Seq <= lc.stopSeq
}

// turnByID reports the live turn the id names, or false when the chat holds none
// under it or it is finalizing.
func (r *turnRegistry) turnByID(chatID marotte.ChatID, turnID string) (*activeTurn, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil || t.finalizing {
		return nil, false
	}
	return t, true
}

// holdsTurn answers the chat whose own or pending slot holds the turn id, for the digest's live_turn lookup (the ref names no chat).
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

// currentTurn reports which turn the chat's activity belongs to (own, open or finalizing): a finalizing turn
// still spawned its processes.
func (r *turnRegistry) currentTurn(chatID marotte.ChatID) (string, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil {
		return "", false
	}
	return lc.own.ID, true
}

// live reports whether the reader must treat the chat as running: own, pending, or a held admission slot.
// Finalizing counts. Through lookup: an HTTP read path.
func (r *turnRegistry) live(chatID marotte.ChatID) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.liveLockedState()
}

// kasAllowsSessionCall reports whether KAS's state lets a model switch reach the session now (kasPermits). Through
// lookup: a command's read path.
func (r *turnRegistry) kasAllowsSessionCall(chatID marotte.ChatID) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return true
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.kas.may().sessionCall
}

// kasStepLocked applies one input to the chat's KAS state, waking every waiter when it changed. Caller holds mu.
func (lc *chatLifecycle) kasStepLocked(in kasInput, seq uint64) (drain bool, rows []string) {
	changed, drain, rows := lc.kas.step(in, seq)
	if changed {
		lc.wakeLocked()
	}
	return drain, rows
}

// liveLockedState is live's predicate, spelled once. Caller holds mu.
func (lc *chatLifecycle) liveLockedState() bool {
	return lc.own != nil || lc.pending != nil || lc.reserved
}

func (r *turnRegistry) openTurnIDs(chatID marotte.ChatID) []*activeTurn {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	out := make([]*activeTurn, 0, 2)
	if lc.own != nil {
		out = append(out, lc.own)
	}
	if lc.pending != nil && lc.pending != lc.own {
		out = append(out, lc.pending)
	}
	return out
}

// ownTurns returns every chat's own open turn, so a connect replay reads the turn, not the prompt slot. Lock order registry.mu -> lifecycle.mu.
func (r *turnRegistry) ownTurns() map[marotte.ChatID]*activeTurn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[marotte.ChatID]*activeTurn, len(r.chats))
	for id, lc := range r.chats {
		lc.mu.Lock()
		if lc.own != nil {
			out[id] = lc.own
		}
		lc.mu.Unlock()
	}
	return out
}

// busyChatIDs is every chat with a turn in flight, the only population a stale-`thinking` retraction spares; live's predicate.
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

// interrupt records why the named turn was interrupted, first-wins and id-scoped.
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

// interruptTurn claims cause on the named live, unclaimed turn and returns the turn's own prompt cancel (nil when
// it has none), false when the turn is gone, finalizing or already interrupted.
func (r *turnRegistry) interruptTurn(chatID marotte.ChatID, turnID string, cause marotte.InterruptCause) (context.CancelCauseFunc, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil {
		return nil, false
	}
	return lc.interruptLocked(t, cause)
}

// interruptLocked is interruptTurn's claim on a turn the caller already validated, in the caller's section: the
// cancel it returns is t's own, so calling it after t closed cannot reach the turn that replaced it. Caller holds mu.
func (*chatLifecycle) interruptLocked(t *activeTurn, cause marotte.InterruptCause) (context.CancelCauseFunc, bool) {
	if t.finalizing || t.Interrupt != "" {
		return nil, false
	}
	t.Interrupt = cause
	return t.cancel, true
}

// interruptCause reads a claimed turn's cause under the turn's own lifecycle, the writer's mutex.
func (*turnRegistry) interruptCause(t *activeTurn) marotte.InterruptCause {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return t.Interrupt
}

// stageStatusDescription records a declared description on the own turn via lookup (never minting a lifecycle).
// An empty one is dropped, so the discharge frame cannot wipe it.
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
func (*turnRegistry) statusDescription(t *activeTurn) string {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return t.statusDesc
}
