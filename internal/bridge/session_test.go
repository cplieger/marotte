package bridge

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestSessionHasNoStop pins that no session handle can end the engine: a session's own errors stay its own.
func TestSessionHasNoStop(t *testing.T) {
	typ := reflect.TypeFor[*Session]()
	engine := reflect.TypeFor[*Engine]()
	for m := range typ.Methods() {
		if strings.Contains(m.Name, "Stop") || strings.Contains(m.Name, "Kill") {
			t.Errorf("*Session has method %s; a session must have no way to stop the engine", m.Name)
		}
		for out := range m.Type.Outs() {
			if out == engine {
				t.Errorf("*Session.%s returns *Engine; a session must not hand out the engine", m.Name)
			}
		}
	}
}

// shortenCloseBudget makes a close with no reply give up quickly. Not parallel.
func shortenCloseBudget(t *testing.T, budget time.Duration, retries ...time.Duration) {
	t.Helper()
	origBudget, origRetries := closeBudget, closeRetries
	closeBudget, closeRetries = budget, retries
	t.Cleanup(func() { closeBudget, closeRetries = origBudget, origRetries })
}

func TestSessionState_ClosingRefusesCallsButStillResponds(t *testing.T) {
	shortenCloseBudget(t, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	go func() { _ = s.Close(context.Background()) }()
	closeFrame := k.nextMethod(methodSessionClose)

	if _, _, err := s.PromptAt(t.Context(), nil, nil); !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("PromptAt on a closing session = %v, want ErrSessionClosing", err)
	}
	if err := s.Notify(t.Context(), marotte.MethodCancel, nil); !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("Notify on a closing session = %v, want ErrSessionClosing", err)
	}
	if err := s.Respond(t.Context(), 7, map[string]any{}, nil); err != nil {
		t.Errorf("Respond on a closing session = %v, want it written: KAS's quiesce may ask", err)
	}
	if f := k.next(); f.ID == nil || *f.ID != 7 {
		t.Errorf("the response during the close was not written; got %s", f.raw)
	}
	k.reply(closeFrame.ID, map[string]any{})
}

func TestSessionClose_EngineGoneBeforeTheWriteEndsClosed(t *testing.T) {
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	k.e.Stop()
	if err := s.Close(t.Context()); err != nil && !errors.Is(err, marotte.ErrBridgeExited) {
		t.Errorf("Close on a stopped engine = %v, want nil or ErrBridgeExited", err)
	}
	if s.Open() {
		t.Error("a session of a stopped engine reports open")
	}
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Error("a session of a stopped engine never reached closed")
	}
}

// TestSessionClose_CallerCancelBeforeTheWriteStillCloses pins that a caller who gives up while the close waits on the
// write lock abandons nothing: the close runs under the engine's lifetime and lands.
func TestSessionClose_CallerCancelBeforeTheWriteStillCloses(t *testing.T) {
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	k.e.writeMu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	closed := make(chan error, 1)
	go func() { closed <- s.Close(ctx) }()
	cancel()
	if err := <-closed; !errors.Is(err, context.Canceled) {
		t.Errorf("Close with a cancelled caller = %v, want the caller's cancellation", err)
	}
	k.e.writeMu.Unlock()
	closeFrame := k.nextMethod(methodSessionClose)
	k.reply(closeFrame.ID, map[string]any{})
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("a close whose caller left before the write never reached closed")
	}
}

func TestSessionClose_CallerCancelAfterTheWrite_LateReplyFinalizesClosed(t *testing.T) {
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	ctx, cancel := context.WithCancel(t.Context())
	closed := make(chan error, 1)
	go func() { closed <- s.Close(ctx) }()
	closeFrame := k.nextMethod(methodSessionClose)
	cancel()
	<-closed
	k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": s.SessionID(), "during": "close"})
	select {
	case n := <-s.NotifCh():
		if n.Msg.Method != marotte.MethodSessionUpdate {
			t.Errorf("the closing session's lane got %q", n.Msg.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a frame for a closing session did not reach its lane; the lane must stay registered until the reply")
	}
	k.reply(closeFrame.ID, map[string]any{})
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the late close reply did not finalize the session closed")
	}
}

func TestSessionClose_RefusedCloseReturnsToOpen(t *testing.T) {
	for _, code := range []int{-32603, -32000, -32001, -32602} {
		t.Run(strconv.Itoa(-code), func(t *testing.T) {
			k := newFakeKAS(t)
			s := k.openedSession(nil)
			closed := make(chan error, 1)
			go func() { closed <- s.Close(t.Context()) }()
			k.replyError(k.nextMethod(methodSessionClose).ID, code, "refused")
			if err := <-closed; err == nil {
				t.Fatal("a refused close returned nil")
			}
			if !s.Open() {
				t.Errorf("after a %d refusal the session is not open; KAS may still hold it live", code)
			}
		})
	}
}

// TestSessionClose_AResultEndsClosedAndARetryIsIdempotent: KAS answers {} to every close, so a close retried after a
// lost reply ends closed too.
func TestSessionClose_AResultEndsClosedAndARetryIsIdempotent(t *testing.T) {
	shortenCloseBudget(t, 100*time.Millisecond, 50*time.Millisecond)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	k.nextMethod(methodSessionClose) // the reply is lost
	if err := <-closed; !errors.Is(err, marotte.ErrSessionClosing) {
		t.Fatalf("a close with no reply = %v, want ErrSessionClosing", err)
	}
	retry := k.nextMethod(methodSessionClose)
	if got := retry.param(marotte.KeySessionID); got != string(s.SessionID()) {
		t.Errorf("the retried close names %q, want %q", got, s.SessionID())
	}
	k.reply(retry.ID, map[string]any{})
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the retried close's {} did not end the session closed")
	}
}

func TestSessionClose_NoReplyStaysClosingUntilEngineExit(t *testing.T) {
	shortenCloseBudget(t, 100*time.Millisecond)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	if err := s.Close(context.Background()); !errors.Is(err, marotte.ErrSessionClosing) {
		t.Fatalf("a close with no reply = %v, want ErrSessionClosing", err)
	}
	select {
	case <-s.closeDone:
		t.Fatal("an unconfirmed close ended the session; it stays closing until a reply or the engine's end")
	case <-time.After(150 * time.Millisecond):
	}
	if _, _, err := s.PromptAt(t.Context(), nil, nil); !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("PromptAt on an unconfirmed close = %v, want ErrSessionClosing", err)
	}
	k.e.Stop()
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the engine's end did not close a session whose close was unconfirmed")
	}
}
