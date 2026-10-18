package agent

// The position the folder has reached in the read-loop sequence: a response goes straight to the waiting Call while
// notifications queue for Forward, so a turn settled on its response alone can leave turn_end unread (EWD687a).

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// forwardAttachment is one forward goroutine's generation and the closer of its exit channel.
type forwardAttachment struct {
	exited func()
	gen    uint64
}

// attachForward resets the observed position for a new forward goroutine and returns its attachment. A new bridge
// restarts its sequence at zero; the generation lets a straggling forward from the old bridge be ignored. The closer
// is bound here, not when the goroutine starts: a teardown that captured the exit channel may forget the lifecycle
// before the goroutine runs, and a closer looked up then would never release its drain wait.
func (r *turnRegistry) attachForward(chatID marotte.ChatID) forwardAttachment {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.fwdGen++
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
	return forwardAttachment{gen: gen, exited: func() {
		close(exit)
		lc.mu.Lock()
		delete(lc.fwdExits, gen)
		lc.mu.Unlock()
	}}
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
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if gen == lc.fwdGen {
		lc.inFrame = true
	}
}

// Called for every frame Forward consumes, not every fold: many frames touch no turn, and a
// fold-bounded position could park a settle forever.
func (r *turnRegistry) observe(chatID marotte.ChatID, gen, seq uint64) {
	lc := r.lifecycleFor(chatID)
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
	lc := r.lifecycleFor(chatID)
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

// awaitPosition parks until the folder has consumed everything before this turn's response, reporting whether it
// got there. It holds no lifecycle mutex and claims nothing: claiming enters turnFinalizing, where a fold waits. It
// does not stop when the awaited turn finalizes, because the wait also orders the empty-turn gate's later-turn
// check; returning early let a re-prompt duplicate work and spend.
func (r *turnRegistry) awaitPosition(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	gen := lc.fwdGen
	if t := lc.turnLocked(turnID); t != nil {
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
