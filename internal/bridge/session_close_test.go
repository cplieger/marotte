package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

type doorResult struct {
	s   *Session
	err error
}

// openingSession starts a NewSession and returns its handle while session/new is unanswered.
func openingSession(t *testing.T, k *fakeKAS) (s *Session, newFrame wireFrame, done <-chan doorResult) {
	t.Helper()
	attached := make(chan *Session, 1)
	out := make(chan doorResult, 1)
	go func() {
		s, err := k.e.NewSession(context.Background(), &marotte.SessionOpts{Role: marotte.SessionRoleChat},
			func(s *Session) error { attached <- s; return nil })
		out <- doorResult{s, err}
	}()
	newFrame = k.nextMethod(methodSessionNew)
	return <-attached, newFrame, out
}

// awaitCloseAsked waits until a Close has reached s.
func awaitCloseAsked(t *testing.T, s *Session) {
	t.Helper()
	eventually(t, "the Close reached the opening session", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeAsked
	})
}

// An opening session has nothing of the chat's on the wire, so a cancel is told so rather than "closed".
func TestSessionNotify_DuringTheDoorSaysOpening(t *testing.T) {
	k := newFakeKAS(t)
	s, newFrame, done := openingSession(t, k)

	err := s.Notify(t.Context(), marotte.MethodCancel, map[string]any{marotte.KeySessionID: s.SessionID()})
	if !errors.Is(err, marotte.ErrSessionOpening) {
		t.Errorf("Notify during the door = %v, want ErrSessionOpening", err)
	}
	if errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Notify during the door = %v, which reads as a closed session", err)
	}
	k.reply(newFrame.ID, map[string]any{"sessionId": newFrame.overrideID()})
	k.answerUntilErr(doorErrs(done))
}

// doorErrs adapts a door's result to answerUntilErr.
func doorErrs(done <-chan doorResult) <-chan error {
	errs := make(chan error, 1)
	go func() { errs <- (<-done).err }()
	return errs
}

// A close asked during the door writes nothing until session/new is answered, then closes what KAS created once, and
// the door hands out no session.
func TestSessionClose_DuringTheDoorWaitsForItsAnswer(t *testing.T) {
	k := newFakeKAS(t)
	s, newFrame, done := openingSession(t, k)
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()

	if f, quiet := k.quiet(100 * time.Millisecond); !quiet {
		t.Fatalf("the close wrote %s before session/new was answered", f.raw)
	}
	awaitCloseAsked(t, s)
	k.reply(newFrame.ID, map[string]any{"sessionId": newFrame.overrideID()})
	var closeFrame wireFrame
	for closeFrame.ID == nil {
		switch f := k.next(); {
		case f.Method == methodSessionClose:
			closeFrame = f
		case f.ID != nil && f.Method != "":
			k.reply(f.ID, map[string]any{})
		}
	}
	if got := closeFrame.param(marotte.KeySessionID); got != string(s.SessionID()) {
		t.Errorf("session/close names %q, want %q", got, s.SessionID())
	}
	k.reply(closeFrame.ID, map[string]any{})

	select {
	case r := <-done:
		if r.s != nil || !errors.Is(r.err, marotte.ErrChatClosedDuringCall) {
			t.Errorf("door = (%v, %v), want no session and ErrChatClosedDuringCall", r.s, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the door did not return after its close")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close = %v, want nil once the close was confirmed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the confirmed close")
	}
	if s.Open() {
		t.Error("the session reads open after its close")
	}
	if f, quiet := k.quiet(100 * time.Millisecond); !quiet && f.Method == methodSessionClose {
		t.Errorf("a second session/close was written: %s", f.raw)
	}
}

// A door that fails while a close waits on it created nothing to close: the close settles with no session/close.
func TestSessionClose_DuringADoorThatFailsWritesNoClose(t *testing.T) {
	k := newFakeKAS(t)
	s, newFrame, done := openingSession(t, k)
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	if f, quiet := k.quiet(100 * time.Millisecond); !quiet {
		t.Fatalf("the close wrote %s during the door", f.raw)
	}
	awaitCloseAsked(t, s)

	k.replyError(newFrame.ID, -32603, "refused")
	if r := <-done; r.err == nil {
		t.Fatal("a refused session/new opened a session")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close = %v, want nil: the failed door left nothing open", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the door failed")
	}
	if f, quiet := k.quiet(100 * time.Millisecond); !quiet && f.Method == methodSessionClose {
		t.Errorf("session/close was written for a door that created nothing: %s", f.raw)
	}
}

// An attach that refuses ends the door before anything is written: no frame, no handle, the attach's error.
func TestSessionDoor_AnAttachErrorWritesNothing(t *testing.T) {
	refusal := errors.New("the chat closed")
	for _, verb := range []string{"new", "load"} {
		t.Run(verb, func(t *testing.T) {
			k := newFakeKAS(t)
			attach := func(*Session) error { return refusal }
			var err error
			if verb == "new" {
				_, err = k.e.NewSession(t.Context(), &marotte.SessionOpts{}, attach)
			} else {
				_, err = k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "sess_load"}, attach)
			}
			if !errors.Is(err, refusal) {
				t.Errorf("door with a refusing attach = %v, want the attach's error", err)
			}
			if f, quiet := k.quiet(150 * time.Millisecond); !quiet {
				t.Errorf("a refused attach let the door write %s", f.raw)
			}
			k.e.lanesMu.Lock()
			n := len(k.e.sessions)
			k.e.lanesMu.Unlock()
			if n != 0 {
				t.Errorf("a refused attach left %d handles in the table", n)
			}
		})
	}
}

// eventually polls cond for up to 2 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
