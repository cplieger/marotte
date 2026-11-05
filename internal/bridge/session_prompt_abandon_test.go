package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// abandonWrittenPrompt writes a session/prompt and gives up on it once KAS has read it, as the cancel grace does.
func abandonWrittenPrompt(t *testing.T, k *fakeKAS, s *Session) wireFrame {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, _, err := s.PromptAt(ctx, map[string]any{marotte.KeySessionID: s.SessionID()}, nil)
		done <- err
	}()
	f := k.nextMethod(marotte.MethodPrompt)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Setup: the abandoned PromptAt = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Setup: PromptAt did not return after its ctx ended")
	}
	return f
}

type abandonedWait struct {
	seq      uint64
	answered bool
}

func awaitAbandonedAsync(ctx context.Context, s *Session) <-chan abandonedWait {
	out := make(chan abandonedWait, 1)
	go func() {
		seq, answered := s.AwaitAbandonedPrompt(ctx)
		out <- abandonedWait{seq: seq, answered: answered}
	}()
	return out
}

// The answer to a prompt its caller abandoned is the fence for the frames its execution still streams: it reaches
// the session at its own lane position, after every frame KAS wrote before it.
func TestPromptAtAbandonedAnswerReachesSession(t *testing.T) {
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	f := abandonWrittenPrompt(t, k, s)
	waited := awaitAbandonedAsync(t.Context(), s)

	for range 2 {
		k.notify(marotte.MethodSessionUpdate, map[string]any{"sessionId": s.SessionID()})
	}
	var last uint64
	for i := range 2 {
		select {
		case n := <-s.NotifCh():
			last = n.Seq
		case <-time.After(2 * time.Second):
			t.Fatalf("Setup: trailing frame %d never reached the session's lane", i+1)
		}
	}
	k.reply(f.ID, map[string]any{"stopReason": "cancelled"})

	select {
	case got := <-waited:
		if !got.answered {
			t.Fatal("AwaitAbandonedPrompt reported no answer, though KAS answered the abandoned prompt")
		}
		if got.seq != last {
			t.Errorf("the answer's lane position = %d, want %d: the position after the frames KAS wrote first", got.seq, last)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitAbandonedPrompt did not return after KAS answered the abandoned prompt")
	}
	if _, answered := s.AwaitAbandonedPrompt(t.Context()); answered {
		t.Error("a second AwaitAbandonedPrompt reported the same answer again; the record must be spent")
	}
}

// A prompt never written abandons nothing, so the wait returns at once rather than at its deadline.
func TestPromptAtCancelledBeforeWritePublishesNoRecord(t *testing.T) {
	refusal := errors.New("refused by the send gate")
	for name, call := range map[string]func(*Session) error{
		"ctx already ended": func(s *Session) error {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, _, err := s.PromptAt(ctx, map[string]any{}, nil)
			return err
		},
		"commit refused the write": func(s *Session) error {
			_, _, err := s.PromptAt(t.Context(), map[string]any{}, func() error { return refusal })
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			k := newFakeKAS(t)
			s := k.openedSession(nil)
			if err := call(s); err == nil {
				t.Fatal("Setup: the unwritten PromptAt succeeded")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			start := time.Now()
			if _, answered := s.AwaitAbandonedPrompt(ctx); answered {
				t.Error("AwaitAbandonedPrompt reported an answer for a prompt never written")
			}
			if waited := time.Since(start); waited > time.Second {
				t.Errorf("AwaitAbandonedPrompt waited %v for a prompt never written, want an immediate return", waited)
			}
		})
	}
}

// The engine's exit drops an abandoned entry unanswered, so the wait must end with it, not at its ctx deadline.
func TestAbandonedPromptWaitEndsOnEngineExit(t *testing.T) {
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	abandonWrittenPrompt(t, k, s)
	waited := awaitAbandonedAsync(t.Context(), s)

	k.e.Stop()

	select {
	case got := <-waited:
		if got.answered {
			t.Error("AwaitAbandonedPrompt reported an answer from an engine that exited without one")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitAbandonedPrompt still waits after the engine exited")
	}
}
