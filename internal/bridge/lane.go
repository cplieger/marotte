package bridge

import (
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// Lane bounds. A lane is unbounded below laneHardCap so a slow consumer never blocks the read loop or stalls a
// co-tenant; laneWarnAt logs the backlog once per episode. Vars for tests.
var (
	laneWarnAt  = 4096
	laneHardCap = 65536
)

// lane is one consumer's ordered frame queue: a session's, or the engine's. The read loop is its only producer, so
// order within a lane is pipe order. A pump moves frames from the queue to out; every frame is held by exactly one of
// the consumer (sent on out), the pump's hand-back slot, or the queue, because the pop and the take both run under mu.
type lane struct {
	out      chan marotte.Notification
	wake     chan struct{}
	stop     chan struct{}
	pumpDone chan struct{}
	handBack *marotte.Notification
	queue    []marotte.Notification
	mu       sync.Mutex
	// seq counts frames appended; the read loop alone writes it, and a reply's position is read on that goroutine too.
	seq     uint64
	closed  bool
	backlog bool
}

func newLane() *lane {
	l := &lane{
		out:      make(chan marotte.Notification),
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		pumpDone: make(chan struct{}),
	}
	go l.pump()
	return l
}

// appendResult is what one append did.
type appendResult int

const (
	appended appendResult = iota
	appendedBacklog
	refusedClosed
	refusedFull
)

// push appends msg unless the lane is closed or at laneHardCap. Read loop only.
func (l *lane) push(msg *marotte.RPCResponse) appendResult {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return refusedClosed
	}
	if len(l.queue) >= laneHardCap {
		l.mu.Unlock()
		return refusedFull
	}
	l.seq++
	l.queue = append(l.queue, marotte.Notification{Msg: msg, Seq: l.seq})
	result := appended
	switch n := len(l.queue); {
	case n >= laneWarnAt && !l.backlog:
		l.backlog = true
		result = appendedBacklog
	case n < laneWarnAt/2:
		l.backlog = false
	}
	l.mu.Unlock()
	l.signal()
	return result
}

func (l *lane) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// position is the number of frames appended so far. Read loop only.
func (l *lane) position() uint64 { return l.seq }

func (l *lane) pump() {
	defer close(l.pumpDone)
	defer close(l.out)
	for {
		l.mu.Lock()
		if len(l.queue) > 0 {
			f := l.queue[0]
			l.queue[0] = marotte.Notification{}
			l.queue = l.queue[1:]
			l.mu.Unlock()
			select {
			case l.out <- f:
				continue
			case <-l.stop:
				l.mu.Lock()
				l.handBack = &f
				l.mu.Unlock()
				return
			}
		}
		if l.closed {
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		select {
		case <-l.wake:
		case <-l.stop:
			return
		}
	}
}

// close refuses later appends and lets the pump hand the consumer every queued frame before out closes.
func (l *lane) close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.signal()
}

// detach refuses later appends, takes the queue and stops the pump without delivering it. It never waits on the pump.
func (l *lane) detach() []marotte.Notification {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	taken := l.queue
	l.queue = nil
	l.mu.Unlock()
	close(l.stop)
	return taken
}

// handedBack waits for the pump to stop after detach and returns the frame it held, if any.
func (l *lane) handedBack() *marotte.Notification {
	<-l.pumpDone
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handBack
}
