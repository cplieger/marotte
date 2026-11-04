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

// A prompt's write commit runs under the write lock, so no other frame can go out between its verdict and the
// prompt's own frame, and a refusal is the call's answer with nothing written and nothing left pending.
func TestPromptAt_ACommitRefusalWritesNothing(t *testing.T) {
	e, s := engineWithSession()
	w := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: w})
	refusal := errors.New("stopped before the write")
	underLock := false
	// Bounded, so a refused request that was written anyway stops waiting for its answer.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	commit := func() error {
		if e.writeMu.TryLock() {
			e.writeMu.Unlock()
		} else {
			underLock = true
		}
		return refusal
	}

	_, _, err := s.PromptAt(ctx, map[string]any{}, commit)
	if !errors.Is(err, refusal) {
		t.Errorf("PromptAt = %v, want the commit's refusal", err)
	}
	if !underLock {
		t.Error("the commit ran outside the write lock, so another frame could go out between its verdict and the write")
	}
	if w.wrote() {
		t.Error("a refused prompt was written")
	}
	e.pendingMu.Lock()
	pending := len(e.pending)
	e.pendingMu.Unlock()
	if pending != 0 {
		t.Errorf("pending requests = %d after a refused write, want 0", pending)
	}
}

// A commit that passes lets the prompt's frame out.
func TestPromptAt_APassingCommitWritesThePrompt(t *testing.T) {
	e, s := engineWithSession()
	w := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: w})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = s.PromptAt(ctx, map[string]any{}, func() error { return nil })
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !w.wrote() {
		if time.Now().After(deadline) {
			t.Fatal("a prompt whose commit passed was never written")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

// A plain call cannot write a session/prompt, so no prompt reaches the wire without PromptAt's commit.
func TestCallAt_RefusesASessionPrompt(t *testing.T) {
	e, s := engineWithSession()
	w := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: w})
	// Bounded, so a prompt written anyway stops waiting for its answer.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, _, err := s.CallAt(ctx, marotte.MethodPrompt, map[string]any{}); !errors.Is(err, errPromptNotACall) {
		t.Errorf("Session.CallAt(session/prompt) = %v, want %v", err, errPromptNotACall)
	}
	if _, _, err := e.CallAt(ctx, marotte.MethodPrompt, map[string]any{}); !errors.Is(err, errPromptNotACall) {
		t.Errorf("Engine.CallAt(session/prompt) = %v, want %v", err, errPromptNotACall)
	}
	if w.wrote() {
		t.Error("a session/prompt asked of a plain call was written")
	}
}

// shortenStallProbe replaces the shipped first-sample delay and livelock ceiling for one test. Callers must not run in
// parallel with anything that writes a frame.
func shortenStallProbe(t *testing.T, after, ceiling time.Duration) {
	t.Helper()
	origAfter, origCeiling := stallProbeAfter, writeStallCeiling
	stallProbeAfter, writeStallCeiling = after, ceiling
	t.Cleanup(func() { stallProbeAfter, writeStallCeiling = origAfter, origCeiling })
}

// writeInBackground runs writeFrame on its own goroutine, so a write that never returns fails the test instead of
// hanging it.
func writeInBackground(b *Engine, n int) <-chan error {
	done := make(chan error, 1)
	go func() { done <- b.writeFrame(make([]byte, n)) }()
	return done
}

func awaitWrite(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("writeFrame did not return within %v", within)
		return nil
	}
}

// A busy engine reads nothing while its event loop is blocked but keeps burning CPU; the write waits it out.
func TestWriteFrame_ABusyEngineIsWaitedOut(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 150*time.Millisecond)
	shortenStallProbe(t, 20*time.Millisecond, 10*time.Second)
	b := NewEngine("/nonexistent", "/work")
	b.cpuTicks = advancingCPU()
	b.stdin.Store(&stdinPipe{w: pw})
	drainAt := 3 * writeStall
	go func() {
		time.Sleep(drainAt)
		_, _ = io.Copy(io.Discard, pr)
	}()

	if err := awaitWrite(t, writeInBackground(b, 256*1024), 5*time.Second); err != nil {
		t.Fatalf("writeFrame to an engine whose CPU kept advancing = %v, want it waited out", err)
	}
	select {
	case <-b.done:
		t.Errorf("an engine that kept burning CPU was reaped as %q", b.Exit().Reason)
	default:
	}
}

// A reader that stopped draining at flat CPU is wedged and still ends the engine.
func TestWriteFrame_ReapsAFlatCPUEngine(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 100*time.Millisecond)
	shortenStallProbe(t, 20*time.Millisecond, 10*time.Second)
	b := NewEngine("/nonexistent", "/work")
	b.cpuTicks = func() (uint64, bool) { return 42, true }
	b.stdin.Store(&stdinPipe{w: pw})

	err = awaitWrite(t, writeInBackground(b, 256*1024), 5*time.Second)
	if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.Is(err, marotte.ErrBridgeExited) {
		t.Fatalf("writeFrame against a wedged reader at flat CPU = %v, want a stall that reads as ErrBridgeExited", err)
	}
	waitReaped(t, b, "a wedged reader at flat CPU")
	if got := b.Exit().Reason; got != ExitStdinStalled {
		t.Errorf("exit reason = %q, want %q", got, ExitStdinStalled)
	}
}

// No CPU evidence buys no time: a probe that cannot read /proc reaps as a flat engine does.
func TestWriteFrame_ReapsWhenTheCPUProbeCannotRead(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 100*time.Millisecond)
	shortenStallProbe(t, 20*time.Millisecond, 10*time.Second)
	b := NewEngine("/nonexistent", "/work")
	var calls uint64
	b.cpuTicks = func() (uint64, bool) { calls++; return calls, false }
	b.stdin.Store(&stdinPipe{w: pw})

	err = awaitWrite(t, writeInBackground(b, 256*1024), 5*time.Second)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("writeFrame with an unreadable CPU probe = %v, want a stall", err)
	}
	waitReaped(t, b, "a stalled write whose CPU probe could not read")
	if got := b.Exit().Reason; got != ExitStdinStalled {
		t.Errorf("exit reason = %q, want %q", got, ExitStdinStalled)
	}
}

// An engine that burns CPU but never drains is a livelock, reaped at the ceiling rather than waited on forever.
func TestWriteFrame_ABusyEngineIsReapedAtTheCeiling(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 100*time.Millisecond)
	shortenStallProbe(t, 20*time.Millisecond, 600*time.Millisecond)
	b := NewEngine("/nonexistent", "/work")
	b.cpuTicks = advancingCPU()
	b.stdin.Store(&stdinPipe{w: pw})

	start := time.Now()
	err = awaitWrite(t, writeInBackground(b, 256*1024), 5*time.Second)
	elapsed := time.Since(start)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("writeFrame to a never-draining engine with CPU advancing = %v, want a stall at the ceiling", err)
	}
	if elapsed < writeStallCeiling {
		t.Errorf("the write was reaped after %v, want it waited out to the %v ceiling while CPU advanced", elapsed, writeStallCeiling)
	}
	waitReaped(t, b, "a livelocked engine at the ceiling")
	if got := b.Exit().Reason; got != ExitStdinStalled {
		t.Errorf("exit reason = %q, want %q", got, ExitStdinStalled)
	}
}

// Bytes moving inside a chunk's window is progress, even when the whole chunk takes longer than writeStall and the
// CPU reads flat.
func TestWriteFrame_PartialChunkProgressIsNotAStall(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 100*time.Millisecond)
	shortenStallProbe(t, 20*time.Millisecond, 10*time.Second)
	go func() {
		buf := make([]byte, 8<<10)
		for {
			// A chunk needs eight of these reads, about 400 ms, four stall windows.
			time.Sleep(50 * time.Millisecond)
			if _, rErr := pr.Read(buf); rErr != nil {
				return
			}
		}
	}()
	b := NewEngine("/nonexistent", "/work")
	b.cpuTicks = func() (uint64, bool) { return 42, true }
	b.stdin.Store(&stdinPipe{w: pw})

	if err := awaitWrite(t, writeInBackground(b, 256*1024), 10*time.Second); err != nil {
		t.Fatalf("writeFrame to a reader draining part of a chunk in each window = %v, want no stall", err)
	}
	select {
	case <-b.done:
		t.Errorf("a reader that drained inside every window was reaped as %q", b.Exit().Reason)
	default:
	}
}

// A frame read while stdin does not move means node's event loop runs and the relay is the stuck reader, so node's
// CPU buys no time and the write is reaped at writeStall, not the ceiling.
func TestWriteFrame_AReaderStuckWhileFramesFlowIsReaped(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	shortenWriteStall(t, 100*time.Millisecond)
	shortenStallProbe(t, 20*time.Millisecond, 10*time.Second)
	b := NewEngine("/nonexistent", "/work")
	b.cpuTicks = advancingCPU()
	b.stdin.Store(&stdinPipe{w: pw})
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				b.noteFrame(1)
			}
		}
	}()

	err = awaitWrite(t, writeInBackground(b, 256*1024), 3*time.Second)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("writeFrame to a stuck reader while frames flowed = %v, want a stall", err)
	}
	waitReaped(t, b, "a stuck reader while frames flowed")
	if got := b.Exit().Reason; got != ExitStdinStalled {
		t.Errorf("exit reason = %q, want %q", got, ExitStdinStalled)
	}
}

// The shipped ceiling outlasts the longest event-loop stall measured (81.6 s, kirodotdev/Kiro#12261) and holds writeMu
// no longer than any other engine-silence bound waits.
func TestWriteStallCeiling_WaitsOutTheMeasuredStallAndNoLonger(t *testing.T) {
	const measured = 81600 * time.Millisecond
	if writeStallCeiling <= measured {
		t.Errorf("writeStallCeiling = %v, want above the %v stall", writeStallCeiling, measured)
	}
	if writeStallCeiling > stallExtendCeiling {
		t.Errorf("writeStallCeiling = %v, want at most stallExtendCeiling %v", writeStallCeiling, stallExtendCeiling)
	}
}
