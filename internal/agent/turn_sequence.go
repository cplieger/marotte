package agent

// The position the folder has reached in the read-loop sequence: a response goes straight to the waiting Call while
// notifications queue for Forward, so a turn settled on its response alone can leave turn_end unread (EWD687a).

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// forwardAttachment is one forward goroutine's generation, the closer of its exit channel, and the done of its
// count on the life, which the forward calls as its last step.
type forwardAttachment struct {
	exited func()
	done   func()
	gen    uint64
}

// attachForward attaches a new forward goroutine to chatID's life for a door whose intent began at stamp: refused
// with marotte.ErrChatClosedDuringCall once a close committed since stamp or the life ended, and waiting (bounded by
// ctx) while a close is held.
func (r *turnRegistry) attachForward(ctx context.Context, chatID marotte.ChatID, stamp uint64) (forwardAttachment, error) {
	lc, err := r.lifecycleAt(ctx, chatID, stamp)
	if err != nil {
		return forwardAttachment{}, err
	}
	return r.attachOn(lc)
}

// attachRunForward is attachForward for a run chat, which no close holds.
func (r *turnRegistry) attachRunForward(chatID marotte.ChatID) forwardAttachment {
	att, err := r.attachOn(r.lifecycleForRun(chatID))
	if err != nil {
		// A run chat's life ends only at its forget, which no attach precedes.
		panic("attachRunForward: " + err.Error())
	}
	return att
}

// attachOn resets the observed position for a new forward goroutine and returns its attachment. A new bridge
// restarts its sequence at zero; the registry-wide generation lets a straggling forward from another bridge be
// ignored. The closer is bound here, not when the goroutine starts: a teardown that captured the exit channel may
// forget the lifecycle before the goroutine runs, and a closer looked up then would never release its drain wait.
func (r *turnRegistry) attachOn(lc *chatLifecycle) (forwardAttachment, error) {
	_, done, ok := lc.beginWork(workForward)
	if !ok {
		return forwardAttachment{}, marotte.ErrChatClosedDuringCall
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.fwdGen = r.fwdGens.Add(1)
	gen := lc.fwdGen
	lc.observedSeq = 0
	lc.forwardGone = false
	lc.inFrame = false
	lc.kasStepLocked(kasForwardReset, 0)
	if lc.fwdExits == nil {
		lc.fwdExits = make(map[uint64]chan struct{})
	}
	exit := make(chan struct{})
	lc.fwdExits[gen] = exit
	lc.wakeLocked()
	return forwardAttachment{gen: gen, done: done, exited: func() {
		close(exit)
		lc.mu.Lock()
		delete(lc.fwdExits, gen)
		lc.mu.Unlock()
	}}, nil
}

// forwardExit answers a channel the current forward goroutine closes on exit, nil when none is attached.
func (r *turnRegistry) forwardExit(chatID marotte.ChatID) <-chan struct{} {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.fwdExits[lc.fwdGen]
}

// beginFrame marks the frame Forward is consuming until its observe, so the send gate decides only between frames.
func (r *turnRegistry) beginFrame(chatID marotte.ChatID, gen uint64) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if gen == lc.fwdGen {
		lc.inFrame = true
	}
}

// Called for every frame Forward consumes, not every fold: many frames touch no turn, and a
// fold-bounded position could park a settle forever.
func (r *turnRegistry) observe(chatID marotte.ChatID, gen, seq uint64) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if gen != lc.fwdGen || (seq <= lc.observedSeq && !lc.inFrame) {
		return
	}
	lc.inFrame = false
	lc.observedSeq = max(lc.observedSeq, seq)
	lc.wakeLocked()
}

// sealPosition records that the forward goroutine has exited. Waiters defer rather than close: the bridge-death
// closer and every teardown's own closer own the turn.
func (r *turnRegistry) sealPosition(chatID marotte.ChatID, gen uint64) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if gen != lc.fwdGen {
		return
	}
	lc.forwardGone = true
	lc.inFrame = false
	lc.kasStepLocked(kasForwardReset, 0)
	lc.wakeLocked()
}

// awaitPosition parks until chatID's folder has consumed everything before seq, reporting whether it got there. It
// holds no lifecycle mutex and claims nothing: claiming enters turnFinalizing, where a fold waits. A chat with no
// lifecycle has no forward to wait for.
func (r *turnRegistry) awaitPosition(ctx context.Context, chatID marotte.ChatID, seq uint64) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	return lc.awaitSeq(ctx, nil, seq)
}

// awaitClosePosition is awaitPosition for a closer of turnID, parked on the life that turn belongs to: open, or among
// its newest closes. A turn id belongs to one life, so a reply landing after its life was forgotten or replaced waits
// on nothing of another life and answers false.
func (r *turnRegistry) awaitClosePosition(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64) bool {
	if turnID == "" {
		return r.awaitPosition(ctx, chatID, seq)
	}
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	t := lc.turnLocked(turnID)
	mine := t != nil || lc.closedRecentlyLocked(turnID)
	lc.mu.Unlock()
	if !mine {
		return false
	}
	return lc.awaitSeq(ctx, t, seq)
}

// awaitSeq is the wait of awaitPosition on lc, recording seq on t when it names one. It does not stop when t
// finalizes, because the wait also orders the empty-turn gate's later-turn check; returning early let a re-prompt
// duplicate work and spend.
func (lc *chatLifecycle) awaitSeq(ctx context.Context, t *activeTurn, seq uint64) bool {
	lc.mu.Lock()
	gen := lc.fwdGen
	if t != nil {
		t.NeedSeq = seq
	}
	for {
		switch {
		case lc.observedSeq >= seq:
			lc.mu.Unlock()
			return true
		case lc.forwardGone, lc.fwdGen != gen:
			lc.mu.Unlock()
			return false
		}
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
		lc.mu.Lock()
	}
}
