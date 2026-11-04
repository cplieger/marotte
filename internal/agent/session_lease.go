package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/cplieger/marotte/internal/marotte"
)

// A chat's session is reachable only through a lease on its life: every engine call on it is one use, which the life
// counts, refuses once the chat's close committed and cuts at the commit, so a teardown that waited for the leases
// (AwaitSessionLeases) is the only caller left when it cancels and releases. This file is the only code that reads
// or writes sharedBridge.leased; session_lease_test.go fails the build on any other access.

// leaseLife is the life an entry's leases are counted on: the chat's lifecycle, or runHostLife for a run host.
type leaseLife interface {
	beginWork(kind workKind) (ctx context.Context, done func(), ok bool)
}

// runHostLife is a run host's life, which no close ends: it always admits, on the runtime's lifetime.
type runHostLife struct{ ctx context.Context }

func (l runHostLife) beginWork(workKind) (context.Context, func(), bool) {
	return l.ctx, func() {}, true
}

// newSharedBridge is the one constructor of an entry, whose leases are counted on life.
func newSharedBridge(life leaseLife) *sharedBridge {
	if life == nil {
		panic("newSharedBridge: an entry needs the life its leases are counted on")
	}
	return &sharedBridge{state: bridgeStarting, settled: newSessionSettle(), life: life}
}

// use runs fn on the entry's session as one lease of its life. fn's context is also cancelled, with the life's cause,
// when the life ends, and an error fn returns then wraps marotte.ErrChatClosedDuringCall; a lease the life refuses
// runs nothing and answers that error. fn must not keep the session past its return. An entry with no session yet
// answers marotte.ErrSessionClosed.
func (sb *sharedBridge) use(ctx context.Context, fn func(ctx context.Context, s engineSession) error) error {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	if s == nil {
		return marotte.ErrSessionClosed
	}
	lifeCtx, done, ok := sb.life.beginWork(workSession)
	if !ok {
		return marotte.ErrChatClosedDuringCall
	}
	defer done()
	child, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(lifeCtx, func() { cancel(context.Cause(lifeCtx)) })
	defer stop()
	return closeCut(child, fn(child, s))
}

// closeCut is err from work on ctx, wrapping marotte.ErrChatClosedDuringCall when the chat's close cut ctx, so a
// call the close ended reads as the close whatever the call itself answered.
func closeCut(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), marotte.ErrChatClosedDuringCall) &&
		!errors.Is(err, marotte.ErrChatClosedDuringCall) {
		return fmt.Errorf("%w: %w", marotte.ErrChatClosedDuringCall, err)
	}
	return err
}

// useRun runs a run's verb on the entry's session. Run work outlives the chat's life, so it is neither counted nor
// cut by a close; the session's own release refuses it.
func (sb *sharedBridge) useRun(ctx context.Context, fn func(ctx context.Context, s engineSession) error) error {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	if s == nil {
		return marotte.ErrSessionClosed
	}
	return fn(ctx, s)
}

// responder is the entry's session as an answerer of kiro-cli's own requests, nil while it has none.
func (sb *sharedBridge) responder() acpResponder {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.leased == nil {
		return nil
	}
	return sb.leased
}

// closeOwned hands the entry's session to fn to end it. Its callers own the session's end: the teardown, once the
// life ended and its leases returned, and the closers of an entry no life holds any more (a failed or self-ended
// generation, a run host's end). An in-life release is a call on a lease's own session instead.
func (sb *sharedBridge) closeOwned(fn func(s engineSession)) {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	if s != nil {
		fn(s)
	}
}

// attach makes s the session sb holds, before its door writes.
func (sb *sharedBridge) attach(s engineSession) {
	sb.mu.Lock()
	sb.leased = s
	sb.mu.Unlock()
}

// holds reports whether s is the entry's session.
func (sb *sharedBridge) holds(s acpResponder) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.leased != nil && acpResponder(sb.leased) == s
}

// hasSession reports whether a door attached a session.
func (sb *sharedBridge) hasSession() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.leased != nil
}

// open reports whether the entry's session is open.
func (sb *sharedBridge) open() bool {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	return s != nil && s.Open()
}

// endedOnItsOwn reports whether session, ending now, is sb's and nobody owns its end.
func (sb *sharedBridge) endedOnItsOwn(session engineSession) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.leased == session && sb.state != bridgeStarting && sb.closeClaim == nil
}

// sessionDeadLocked is the reload or release claim's test of the session: due when no session is attached or it is no
// longer open. Caller holds sb.mu.
func (sb *sharedBridge) sessionDeadLocked() bool {
	return sb.leased == nil || !sb.leased.Open()
}

// SessionID is the entry's session id, "" while it has none.
func (sb *sharedBridge) SessionID() marotte.SessionID {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	if s == nil {
		return ""
	}
	return s.SessionID()
}

// autoCompactionDisabled reports the disableAutoCompaction value KAS froze for the entry's session.
func (sb *sharedBridge) autoCompactionDisabled() bool {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	return s != nil && s.AutoCompactionDisabled()
}

// observeEffort and observeThinking record a level the session reported.
func (sb *sharedBridge) observeEffort(level string) {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	if s != nil {
		s.ObserveEffort(level)
	}
}

func (sb *sharedBridge) observeThinking(value string) {
	sb.mu.Lock()
	s := sb.leased
	sb.mu.Unlock()
	if s != nil {
		s.ObserveThinking(value)
	}
}

// Call, CallAt, PromptAt and Notify are the command layer's calls on the entry, each one lease.

func (sb *sharedBridge) Call(ctx context.Context, method string, params any) (resp *marotte.RPCResponse, err error) {
	err = sb.use(ctx, func(ctx context.Context, s engineSession) error {
		sb.beforeCall(method, s.SessionID())
		var cErr error
		resp, cErr = s.Call(ctx, method, params)
		return cErr
	})
	return resp, err
}

func (sb *sharedBridge) CallAt(ctx context.Context, method string, params any) (resp *marotte.RPCResponse, seq uint64, err error) {
	err = sb.use(ctx, func(ctx context.Context, s engineSession) error {
		sb.beforeCall(method, s.SessionID())
		var cErr error
		resp, seq, cErr = s.CallAt(ctx, method, params)
		return cErr
	})
	return resp, seq, err
}

func (sb *sharedBridge) PromptAt(ctx context.Context, params any, commit func() error) (resp *marotte.RPCResponse, seq uint64, err error) {
	err = sb.use(ctx, func(ctx context.Context, s engineSession) error {
		var cErr error
		resp, seq, cErr = s.PromptAt(ctx, params, commit)
		return cErr
	})
	return resp, seq, err
}

func (sb *sharedBridge) Notify(ctx context.Context, method string, params any) error {
	return sb.use(ctx, func(ctx context.Context, s engineSession) error {
		return s.Notify(ctx, method, params)
	})
}

// Respond answers a request kiro-cli sent on the entry's session.
func (sb *sharedBridge) Respond(ctx context.Context, requestID int64, result any, err error) error {
	r := sb.responder()
	if r == nil {
		return marotte.ErrSessionClosed
	}
	return r.Respond(ctx, requestID, result, err)
}
