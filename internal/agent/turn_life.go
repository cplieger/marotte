package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A chatLifecycle is the chat's life. Every goroutine that works for the life (its session door, its forward, each
// turn-close tail, each later rehydrate sweep, each session lease) is started through beginWork, which counts it and
// refuses once the life ended. A close ends the life at its commit (ChatHold.Commit); the teardown then waits until
// the count is zero (awaitQuiet) before it forgets any of the chat's state or releases the gate hold. With the gate
// admitting no new life while a hold stands, no work of one life runs while another life of the chat exists.

// workKind names one kind of the life's work, counted apart so a stuck teardown names what it waits for.
type workKind uint8

const (
	workDoor workKind = iota
	workForward
	workTail
	workSweep
	workSession
	workKinds
)

var workKindNames = [workKinds]string{"doors", "forwards", "tails", "sweeps", "sessions"}

// newLifecycle builds a lifecycle for chatID admitted at stamp, its context a child of the registry's lifetime.
func (r *turnRegistry) newLifecycle(chatID marotte.ChatID, stamp uint64) *chatLifecycle {
	parent := r.lifeParent
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	return &chatLifecycle{
		changed: make(chan struct{}),
		quiet:   make(chan struct{}),
		epoch:   r.epochs.Add(1),
		chatID:  chatID,
		stamp:   stamp,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// beginWork counts one goroutine of the life's work of kind, answering the life's context and the idempotent done
// the goroutine calls when it returns; false once the life ended, when the caller starts nothing.
func (lc *chatLifecycle) beginWork(kind workKind) (context.Context, func(), bool) {
	return lc.begin(kind, nil)
}

// beginLease is beginWork for one session lease, whose own context cut cancels: the life's end calls cut before
// it returns, so no check on the lease's context passes once a close committed. A lease's context derives from its
// caller's, and context.AfterFunc on the life's would cut it later, on another goroutine.
func (lc *chatLifecycle) beginLease(cut context.CancelCauseFunc) (context.Context, func(), bool) {
	return lc.begin(workSession, cut)
}

func (lc *chatLifecycle) begin(kind workKind, cut context.CancelCauseFunc) (context.Context, func(), bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.ended {
		return nil, func() {}, false
	}
	lc.work[kind]++
	var token uint64
	if cut != nil {
		if lc.leaseCuts == nil {
			lc.leaseCuts = make(map[uint64]context.CancelCauseFunc)
		}
		lc.leaseSeq++
		token = lc.leaseSeq
		lc.leaseCuts[token] = cut
	}
	var once sync.Once
	return lc.ctx, func() {
		once.Do(func() {
			lc.mu.Lock()
			lc.work[kind]--
			delete(lc.leaseCuts, token)
			lc.quietIfDoneLocked()
			lc.wakeLocked()
			lc.mu.Unlock()
		})
	}, true
}

// endLifeLocked ends the life: every fence taken before it stops holding, its context and every running lease's are
// cancelled with marotte.ErrChatClosedDuringCall, and no new work starts. Idempotent. Caller holds mu.
func (lc *chatLifecycle) endLifeLocked(epochs func() uint64) {
	if lc.ended {
		return
	}
	lc.ended = true
	lc.epoch = epochs()
	lc.cancel(marotte.ErrChatClosedDuringCall)
	for _, cut := range lc.leaseCuts {
		cut(marotte.ErrChatClosedDuringCall)
	}
	lc.quietIfDoneLocked()
	lc.wakeLocked()
}

// quietIfDoneLocked closes quiet once the life ended and none of its work runs. Caller holds mu.
func (lc *chatLifecycle) quietIfDoneLocked() {
	if !lc.ended || lc.workingLocked() {
		return
	}
	select {
	case <-lc.quiet:
	default:
		close(lc.quiet)
	}
}

func (lc *chatLifecycle) workingLocked() bool {
	for _, n := range lc.work {
		if n > 0 {
			return true
		}
	}
	return false
}

// endedNow reports whether the life ended; a tail checks it before each step.
func (lc *chatLifecycle) endedNow() bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.ended
}

// mintEpoch names a new lifecycle epoch.
func (r *turnRegistry) mintEpoch() uint64 { return r.epochs.Add(1) }

// endLife ends chatID's life (endLifeLocked) and returns its lifecycle, false when the chat has none. Its first
// caller for a close is the commit; a teardown's later call finds the same ended lifecycle.
func (r *turnRegistry) endLife(chatID marotte.ChatID) (*chatLifecycle, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, false
	}
	lc.mu.Lock()
	lc.endLifeLocked(r.mintEpoch)
	lc.mu.Unlock()
	return lc, true
}

// awaitQuiet waits until every work of the ended life returned, or ctx ends. At warnAfter it logs one WARN naming
// what still runs and keeps waiting: a worker that does not return is a defect, and the chat stays closed until it
// does.
func (lc *chatLifecycle) awaitQuiet(ctx context.Context, warnAfter time.Duration) {
	lc.awaitWorkDone(ctx, warnAfter, func() bool {
		select {
		case <-lc.quiet:
			return true
		default:
			return false
		}
	})
}

// awaitWork is awaitQuiet for one kind of work, which the teardown waits for before its own cancel.
func (lc *chatLifecycle) awaitWork(ctx context.Context, kind workKind, warnAfter time.Duration) {
	lc.awaitWorkDone(ctx, warnAfter, func() bool {
		lc.mu.Lock()
		defer lc.mu.Unlock()
		return lc.work[kind] == 0
	})
}

func (lc *chatLifecycle) awaitWorkDone(ctx context.Context, warnAfter time.Duration, done func() bool) {
	warn := time.NewTimer(warnAfter)
	defer warn.Stop()
	for {
		lc.mu.Lock()
		changed := lc.changed
		lc.mu.Unlock()
		if done() {
			return
		}
		select {
		case <-changed:
		case <-warn.C:
			lc.warnStuck()
		case <-ctx.Done():
			return
		}
	}
}

func (lc *chatLifecycle) warnStuck() {
	lc.mu.Lock()
	attrs := make([]any, 0, 2+2*len(lc.work))
	attrs = append(attrs, "chat_id", lc.chatID)
	for k, n := range lc.work {
		attrs = append(attrs, workKindNames[k], n)
	}
	lc.mu.Unlock()
	slog.Warn("a closed chat's background work has not stopped; the chat stays closed until it does", attrs...)
}
