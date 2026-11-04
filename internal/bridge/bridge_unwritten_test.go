package bridge

import (
	"errors"
	"io"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// fullThenFailWriter takes every byte and still reports an error, which io.Writer permits.
type fullThenFailWriter struct{}

func (fullThenFailWriter) Write(p []byte) (int, error) { return len(p), errors.New("write boom") }
func (fullThenFailWriter) Close() error                { return nil }

// A request whose frame did not reach kiro-cli whole is reported unwritten beside the engine's end; one whose every
// byte went out may have been taken and is not.
func TestCall_AnEngineEndBeforeTheWholeFrameIsUnwritten(t *testing.T) {
	cases := map[string]struct {
		w         io.WriteCloser
		unwritten bool
	}{
		"a write that fails at once":              {&captureWriter{failErr: errors.New("write boom")}, true},
		"a short write":                           {shortWriter{}, true},
		"a write that took every byte and failed": {fullThenFailWriter{}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := NewEngine("/nonexistent", "/work")
			b.stdin.Store(&stdinPipe{w: c.w})

			_, err := b.Call(t.Context(), "test/method", nil)

			if !errors.Is(err, marotte.ErrBridgeExited) {
				t.Errorf("Call = %v, want the engine's end", err)
			}
			if got := errors.Is(err, marotte.ErrNotWritten); got != c.unwritten {
				t.Errorf("Call = %v, reads unwritten = %v, want %v", err, got, c.unwritten)
			}
			waitReaped(t, b, "a stdin write failed")
		})
	}
}

// A call on a session the engine's end closed, and a call after the read loop exited, never wrote a byte.
func TestCallAt_AfterTheEngineEndedIsUnwritten(t *testing.T) {
	t.Run("on a session the end closed", func(t *testing.T) {
		e, s := engineWithSession()
		s.state = stateClosed
		e.readerExited.Store(true)

		_, _, err := s.CallAt(t.Context(), "test/method", nil)

		if !errors.Is(err, marotte.ErrBridgeExited) || !errors.Is(err, marotte.ErrNotWritten) {
			t.Errorf("CallAt = %v, want the engine's end, unwritten", err)
		}
	})
	t.Run("on the engine after its read loop exited", func(t *testing.T) {
		b := NewEngine("/nonexistent", "/work")
		b.stdin.Store(&stdinPipe{w: &captureWriter{}})
		b.readerExited.Store(true)

		_, err := b.Call(t.Context(), "test/method", nil)

		if !errors.Is(err, marotte.ErrNotWritten) {
			t.Errorf("Call = %v, want unwritten", err)
		}
	})
}
