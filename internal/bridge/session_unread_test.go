package bridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// unreadReplies are the two answers that reach marotte and cannot be read: KAS may have acted on the request.
var unreadReplies = map[string]func(id int64) string{
	"an undecodable answer": func(id int64) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", id, tooDeep)
	},
	"an oversize answer": func(id int64) string {
		return oversize(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"text":"`, id))
	},
}

func (k *fakeKAS) sendRaw(frame string) {
	k.t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := k.outW.Write([]byte(frame)); err != nil {
		k.t.Fatalf("write frame to the engine: %v", err)
	}
}

// A close whose answer arrived unread may have closed the session, so it is neither reopened as refused nor taken
// as closed: it stays closing, and the re-sent close (KAS answers {} to every close) settles it.
func TestSessionClose_AnUnreadAnswerStaysClosingUntilTheRetrySettlesIt(t *testing.T) {
	for name, frame := range unreadReplies {
		t.Run(name, func(t *testing.T) {
			_ = capture.Default(t)
			shortenCloseBudget(t, time.Second, 50*time.Millisecond)
			k := newFakeKAS(t)
			s := k.openedSession(nil)
			closed := make(chan error, 1)
			go func() { closed <- s.Close(t.Context()) }()
			k.sendRaw(frame(*k.nextMethod(methodSessionClose).ID))
			err := <-closed
			if !errors.Is(err, marotte.ErrSessionClosing) {
				t.Errorf("Close with %s = %v, want ErrSessionClosing", name, err)
			}
			if _, refused := errors.AsType[*marotte.RPCError](err); refused {
				t.Errorf("Close with %s = %v, which reads as kiro-cli refusing the close", name, err)
			}
			if s.Open() {
				t.Fatalf("after %s the session is open again, as if kiro-cli had refused the close", name)
			}
			retry := k.nextMethod(methodSessionClose)
			k.reply(retry.ID, map[string]any{})
			select {
			case <-s.closeDone:
			case <-time.After(2 * time.Second):
				t.Fatal("the re-sent close's {} did not end the session closed")
			}
		})
	}
}

// An abandoned session/new whose late answer arrived unread may have created the session, so the minted id is closed
// and reaped as for a late answer that was read.
func TestAbandonedSessionNew_AnUnreadLateAnswerClosesTheMintedSession(t *testing.T) {
	for name, frame := range unreadReplies {
		t.Run(name, func(t *testing.T) {
			_ = capture.Default(t)
			k := newFakeKAS(t)
			reaped := reapRecorder(k)
			f := abandonedNew(t, k)
			minted := f.overrideID()
			k.sendRaw(frame(*f.ID))
			closeFrame := k.nextMethod(methodSessionClose)
			if got := closeFrame.param(marotte.KeySessionID); got != string(minted) {
				t.Errorf("session/close names %q, want the minted %q", got, minted)
			}
			k.reply(closeFrame.ID, map[string]any{})
			select {
			case id := <-reaped:
				if id != minted {
					t.Errorf("reaped %q, want the minted id", id)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the session kiro-cli may have created for an abandoned door was never reaped")
			}
		})
	}
}

// An abandoned session/load whose late answer arrived unread may have loaded the session, so it is closed.
func TestAbandonedSessionLoad_AnUnreadLateAnswerIsClosed(t *testing.T) {
	for name, frame := range unreadReplies {
		t.Run(name, func(t *testing.T) {
			_ = capture.Default(t)
			k := newFakeKAS(t)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := k.e.LoadSession(ctx, &marotte.SessionOpts{SessionID: "sess_load"}, nil)
				done <- err
			}()
			first := k.nextMethod(methodSessionLoad)
			cancel()
			<-done
			k.sendRaw(frame(*first.ID))
			f, quiet := k.quiet(time.Second)
			if quiet || f.Method != methodSessionClose || f.param(marotte.KeySessionID) != "sess_load" {
				t.Errorf("after %s to an abandoned load, wrote %q (quiet=%v), want a session/close of sess_load",
					name, f.raw, quiet)
			}
		})
	}
}

// A late close answer that arrived unread settles nothing: the session stays closing for the re-send, never reopened.
func TestSessionClose_AnUnreadLateAnswerDoesNotReopen(t *testing.T) {
	for name, frame := range unreadReplies {
		t.Run(name, func(t *testing.T) {
			_ = capture.Default(t)
			shortenCloseBudget(t, 100*time.Millisecond, time.Hour)
			k := newFakeKAS(t)
			s := k.openedSession(nil)
			if err := s.Close(t.Context()); !errors.Is(err, marotte.ErrSessionClosing) {
				t.Fatalf("Setup: a close with no reply = %v, want ErrSessionClosing", err)
			}
			first := k.nextMethod(methodSessionClose)
			k.sendRaw(frame(*first.ID))
			if f, quiet := k.quiet(300 * time.Millisecond); !quiet {
				t.Errorf("an unread late close answer wrote %s", f.raw)
			}
			if s.Open() {
				t.Errorf("after %s to a late close the session is open, as if kiro-cli had refused it", name)
			}
		})
	}
}
