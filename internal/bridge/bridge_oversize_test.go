package bridge

// An oversize stdout frame is surfaced and the session survives; once it ended the scan and killed the session.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// pendingCall registers one pending request id and returns its channel, as Call does.
func pendingCall(b *Bridge, id int64) chan pendingReply {
	ch := make(chan pendingReply, 1)
	b.pendingMu.Lock()
	b.pending[id] = ch
	b.pendingMu.Unlock()
	return ch
}

// A dropped frame could be any response, so every pending request fails rather than waiting forever; the prompt
// path then finalizes the turn and tells the user.
func TestReadLoop_OversizeFrameFailsPendingCalls(t *testing.T) {
	c := capture.Default(t)
	huge := strings.Repeat("x", scannerLineCap+16)
	b := readLoopBridge(strings.NewReader(huge + "\n"))
	ch := pendingCall(b, 7)

	b.readLoop()

	select {
	case got := <-ch:
		if got.resp != frameTooLargeResp {
			t.Fatalf("pending call got %#v, want the frameTooLargeResp sentinel", got.resp)
		}
	default:
		t.Fatal("pending call was left waiting after an oversize frame; Call has no deadline, so it would hang forever")
	}
	if c.CountExact("ACP read: frame exceeds the size cap; dropped it and failed the pending requests") == 0 {
		t.Error("the dropped frame was not logged; the loss has to be visible somewhere")
	}
}

// The frame after an oversize one still reaches dispatch: the turn dies, not the session.
func TestReadLoop_ResumesDispatchAfterAnOversizeFrame(t *testing.T) {
	_ = capture.Default(t)
	huge := strings.Repeat("x", scannerLineCap+16)
	b := readLoopBridge(strings.NewReader(
		huge + "\n" + `{"jsonrpc":"2.0","method":"session/update","params":{}}` + "\n",
	))
	b.notifCh = make(chan marotte.Notification, 4)

	b.readLoop()

	select {
	case n := <-b.notifCh:
		if n.Msg == nil || n.Msg.Method != marotte.MethodSessionUpdate {
			t.Fatalf("notification after the oversize frame = %#v, want session/update", n.Msg)
		}
		if n.Seq != 1 {
			t.Errorf("stamped sequence = %d, want 1: the DROPPED frame is not delivered, so it takes no sequence", n.Seq)
		}
	default:
		t.Fatal("the frame after an oversize one never reached dispatch; the stream did not resynchronise")
	}
}

// Call returns a non-retryable transport error carrying marotte.ErrFrameTooLarge: a retry would re-run an expensive
// turn into the same payload. promptFailureReason shows the wording.
func TestCall_FrameTooLargeIsNonRetryableAndNamed(t *testing.T) {
	b := New("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: &captureWriter{}})

	done := make(chan error, 1)
	go func() {
		_, err := b.Call(t.Context(), marotte.MethodPrompt, nil)
		done <- err
	}()
	waitPending(t, b, 1)
	b.failPending(frameTooLargeResp)

	select {
	case err := <-done:
		if !errors.Is(err, marotte.ErrFrameTooLarge) {
			t.Fatalf("Call error = %v, want marotte.ErrFrameTooLarge", err)
		}
		if errors.Is(err, marotte.ErrBridgeExited) {
			t.Error("a dropped frame must not read as a dead bridge: the process and the session are still alive")
		}
		te, ok := errors.AsType[*marotte.TransportError](err)
		if !ok {
			t.Fatalf("Call error %T, want *marotte.TransportError", err)
		}
		if te.Retryable {
			t.Error("frame-too-large was marked retryable; the same prompt reproduces the same oversize payload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after the pending answer")
	}
}

// An unterminated blob reaps the bridge, with its own log line.
func TestReadLoop_ExhaustedDrainReapsWithItsOwnLogLine(t *testing.T) {
	c := capture.Default(t)
	b := readLoopBridge(&endlessReader{b: 'q'})

	b.readLoop()

	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not reap the bridge after the drain budget was exhausted")
	}
	if c.CountExact("ACP read: a single frame never terminated within the drain budget; reaping bridge") == 0 {
		t.Error("the exhausted-drain case did not get its own log line")
	}
}
