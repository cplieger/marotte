package forges

import (
	"context"
	"sync"
)

// idLocks is the per-connection lock every change takes, held across that
// change's own I/O. An entry lives only while someone holds or waits on it.
type idLocks struct {
	held map[string]*idLock
	mu   sync.Mutex
}

// idLock is a cancellable mutex: its channel holds one token while locked.
type idLock struct {
	ch   chan struct{}
	refs int
}

// lock takes id's lock, waiting no longer than ctx allows: a waiter whose ctx
// ends answers its error and never holds the lock. unlock releases it once.
func (l *idLocks) lock(ctx context.Context, id string) (unlock func(), err error) {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*idLock{}
	}
	e := l.held[id]
	if e == nil {
		e = &idLock{ch: make(chan struct{}, 1)}
		l.held[id] = e
	}
	e.refs++
	l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		l.release(id, e)
		return nil, err
	}
	select {
	case e.ch <- struct{}{}:
	case <-ctx.Done():
		l.release(id, e)
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-e.ch
			l.release(id, e)
		})
	}, nil
}

func (l *idLocks) release(id string, e *idLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(l.held, id)
	}
}
