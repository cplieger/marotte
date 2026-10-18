package bridge

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestWriteFrame_EveryWriteErrorIsBridgeExitedAndReaps pins that a plain write error is engine-scoped too: a stdin
// that refused one write cannot be trusted with the frames after it, so the error reads as the engine's exit and the
// engine ends.
func TestWriteFrame_EveryWriteErrorIsBridgeExitedAndReaps(t *testing.T) {
	sentinel := errors.New("write boom")
	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: &captureWriter{failErr: sentinel}})

	err := b.Respond(t.Context(), 1, map[string]string{}, nil)
	if !errors.Is(err, marotte.ErrBridgeExited) {
		t.Errorf("Respond through a failing writer = %v, want it to read as ErrBridgeExited", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Respond through a failing writer = %v, want the underlying write error kept", err)
	}
	waitReaped(t, b, "a stdin write failed")
	if got := b.Exit().Reason; got != exitWriteFailed {
		t.Errorf("exit reason = %q, want %q", got, exitWriteFailed)
	}
}

// shortenWriteStall replaces the shipped per-chunk stall bound for one test, so removing the timer fails the
// assertion. Callers must not run in parallel with anything that writes a frame.
func shortenWriteStall(t *testing.T, d time.Duration) {
	t.Helper()
	orig := writeStall
	writeStall = d
	t.Cleanup(func() { writeStall = orig })
}

// waitReaped waits until the engine's done channel closes, bounded.
func waitReaped(t *testing.T, b *Engine, what string) {
	t.Helper()
	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: the engine was never reaped", what)
	}
}

// TestWriteFrame_ReapsTheEngineWhenStdinStopsDraining pins that a chunk with no progress is bounded and that expiry
// ends the engine: it leaves a partial frame on the wire, an unrecoverable desync.
func TestWriteFrame_ReapsTheEngineWhenStdinStopsDraining(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// pr is never drained: the wedge under test.
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})
	shortenWriteStall(t, 50*time.Millisecond)

	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: pw})
	// Larger than the pipe buffer, so the write cannot complete.
	err = b.writeFrame(make([]byte, 256*1024))
	if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.Is(err, marotte.ErrBridgeExited) {
		t.Fatalf("writeFrame against an undrained pipe err = %v, want a stall that reads as ErrBridgeExited", err)
	}
	waitReaped(t, b, "a write stalled, leaving a partial frame on the wire")
	if got := b.Exit().Reason; got != ExitStdinStalled {
		t.Errorf("exit reason = %q, want %q", got, ExitStdinStalled)
	}
}

// TestWriteFrame_ASlowButDrainingReaderIsNotReaped pins that the bound is per chunk of progress, not per frame: a
// reader that keeps draining, however slowly, keeps the engine.
func TestWriteFrame_ASlowButDrainingReaderIsNotReaped(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 300*time.Millisecond)
	read := make(chan int, 1)
	go func() {
		buf := make([]byte, 32<<10)
		total := 0
		for total < 1<<20 {
			// Each read frees room for less than a whole frame, well inside the stall bound.
			time.Sleep(20 * time.Millisecond)
			n, rErr := pr.Read(buf)
			total += n
			if rErr != nil {
				break
			}
		}
		read <- total
	}()

	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: pw})
	start := time.Now()
	if err := b.writeFrame(make([]byte, 1<<20)); err != nil {
		t.Fatalf("writeFrame to a slow but draining reader: %v", err)
	}
	if elapsed := time.Since(start); elapsed < writeStall {
		t.Fatalf("Setup: the frame took %v, want longer than the %v stall bound for the case to mean anything", elapsed, writeStall)
	}
	select {
	case <-b.done:
		t.Error("a reader that kept draining was reaped")
	default:
	}
	<-read
}

// TestWriteFrame_StallAfterProgressStillReaps pins that progress earlier in a frame does not excuse a later chunk that
// makes none.
func TestWriteFrame_StallAfterProgressStillReaps(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 100*time.Millisecond)
	go func() {
		// Drain one chunk's worth, then stop reading for good.
		_, _ = io.ReadFull(pr, make([]byte, writeChunk))
	}()

	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: pw})
	if err := b.writeFrame(make([]byte, 1<<20)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("writeFrame that stalls after progress = %v, want a stall", err)
	}
	waitReaped(t, b, "a write stalled after making progress")
}

// shortWriter reports fewer bytes than given with a nil error, which io.Writer permits, leaving a partial frame.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func (shortWriter) Close() error                { return nil }

// TestWriteFrame_ReapsTheEngineOnAShortWrite pins that a short write ends the engine too.
func TestWriteFrame_ReapsTheEngineOnAShortWrite(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: shortWriter{}})

	if err := b.writeFrame([]byte("hello\n")); !errors.Is(err, marotte.ErrBridgeExited) {
		t.Fatalf("writeFrame on a short write = %v, want ErrBridgeExited; a truncated frame desyncs kiro-cli's scanner", err)
	}
	waitReaped(t, b, "a short write left a partial frame on the wire")
}

// TestWriteFrame_WritesThroughAWriterWithNoDeadline pins that the stall bound applies only where the writer supports
// one; the fixtures are not files.
func TestWriteFrame_WritesThroughAWriterWithNoDeadline(t *testing.T) {
	shortenWriteStall(t, time.Nanosecond)
	w := &captureWriter{}
	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: w})

	if err := b.writeFrame([]byte("hello\n")); err != nil {
		t.Fatalf("writeFrame through a deadline-less writer: %v", err)
	}
	if !w.wrote() {
		t.Errorf("writeFrame wrote nothing through a deadline-less writer")
	}
}

// A request's write commit runs under the write lock, so no other frame can go out between its verdict and the
// request's own frame, and a refusal is the call's answer with nothing written and nothing left pending.
func TestCallAt_ACommitRefusalWritesNothing(t *testing.T) {
	e, s := engineWithSession()
	w := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: w})
	refusal := errors.New("stopped before the write")
	underLock := false
	// Bounded, so a refused request that was written anyway stops waiting for its answer.
	bounded, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ctx := marotte.WithWriteCommit(bounded, func() error {
		if e.writeMu.TryLock() {
			e.writeMu.Unlock()
		} else {
			underLock = true
		}
		return refusal
	})

	_, _, err := s.CallAt(ctx, marotte.MethodPrompt, map[string]any{})
	if !errors.Is(err, refusal) {
		t.Errorf("CallAt = %v, want the commit's refusal", err)
	}
	if !underLock {
		t.Error("the commit ran outside the write lock, so another frame could go out between its verdict and the write")
	}
	if w.wrote() {
		t.Error("a refused request was written")
	}
	e.pendingMu.Lock()
	pending := len(e.pending)
	e.pendingMu.Unlock()
	if pending != 0 {
		t.Errorf("pending requests = %d after a refused write, want 0", pending)
	}
}

// A commit that passes lets the request's frame out.
func TestCallAt_APassingCommitWritesTheRequest(t *testing.T) {
	e, s := engineWithSession()
	w := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: w})
	ctx, cancel := context.WithCancel(marotte.WithWriteCommit(t.Context(), func() error { return nil }))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = s.CallAt(ctx, marotte.MethodPrompt, map[string]any{})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !w.wrote() {
		if time.Now().After(deadline) {
			t.Fatal("a request whose commit passed was never written")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}
