package agent

import (
	"context"
	"errors"
	"sync"
)

// errReplaySuperseded settles a load's gate when a re-load replaced its projection unswapped.
var errReplaySuperseded = errors.New("a later session/load superseded this replay before it merged")

// sessionSettle is a once-settled outcome: a bridge generation's readiness, closed once the chat's
// log holds KAS's account of the session (publishGeneration), or one replay's merge. A nil one is
// settled: nothing it hosts was ever replayed.
type sessionSettle struct {
	done chan struct{}
	err  error
	once sync.Once
}

func newSessionSettle() *sessionSettle {
	return &sessionSettle{done: make(chan struct{})}
}

func (s *sessionSettle) settle(err error) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.err = err
		close(s.done)
	})
}

func (s *sessionSettle) wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-s.done:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
