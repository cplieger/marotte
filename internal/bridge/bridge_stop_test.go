package bridge

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A Start that fails before any process leaves no read loop, so Stop closes NotifCh and a forward loop still ends.
func TestStop_ClosesTheStreamOfABridgeThatNeverStarted(t *testing.T) {
	for name, opts := range map[string]*marotte.StartOpts{
		"no kiro-cli installed":     {Lifetime: t.Context()},
		"an invalid session id":     {Lifetime: t.Context(), SessionID: "not a session id"},
		"no lifetime for the spawn": {},
	} {
		t.Run(name, func(t *testing.T) {
			b := New("", t.TempDir())
			if err := b.Start(t.Context(), opts); err == nil {
				t.Fatal("Setup: Start succeeded with no kiro-cli to run")
			}
			b.Stop()
			select {
			case _, open := <-b.NotifCh():
				if open {
					t.Error("NotifCh delivered a frame from a bridge that never started")
				}
			case <-time.After(time.Second):
				t.Error("NotifCh is still open after Stop, so a consumer ranging over it never ends")
			}
		})
	}
}

type callAtResult struct {
	err error
	seq uint64
}

func callAtAsync(t *testing.T, b *Bridge) <-chan callAtResult {
	t.Helper()
	done := make(chan callAtResult, 1)
	go func() {
		_, seq, err := b.CallAt(t.Context(), marotte.MethodPrompt, map[string]any{})
		done <- callAtResult{err: err, seq: seq}
	}()
	return done
}

func awaitCallAt(t *testing.T, done <-chan callAtResult) callAtResult {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("CallAt did not return")
		return callAtResult{}
	}
}

// Stop ends the process while its read loop still holds three delivered frames' worth of position: the request
// written before it settles at that position, so a caller can order the frames against the failure.
func TestCallAt_StopAfterTheWriteSettlesAtTheDeliveredPosition(t *testing.T) {
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdout pipe: %v", err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = outR.Close(); _ = inR.Close() })
	b := New("/unused", "/work")
	b.stdout = newFrameReader(bufio.NewReaderSize(outR, stdoutBufSize))
	b.stdin.Store(&stdinPipe{w: inW})
	go b.readLoop()
	frame := `{"jsonrpc":"2.0","method":"session/update","params":{}}` + "\n"
	if _, err := outW.WriteString(strings.Repeat(frame, 3)); err != nil {
		t.Fatalf("Setup: write notifications: %v", err)
	}
	for i := range 3 {
		select {
		case <-b.NotifCh():
		case <-time.After(2 * time.Second):
			t.Fatalf("Setup: notification %d never reached NotifCh", i+1)
		}
	}

	done := callAtAsync(t, b)
	readFrame(t, inR)
	b.Stop()
	// What the reap does to a live process: its stdout ends.
	_ = outW.Close()
	got := awaitCallAt(t, done)

	if !errors.Is(got.err, marotte.ErrBridgeExited) {
		t.Fatalf("CallAt written before Stop: error = %v, want ErrBridgeExited", got.err)
	}
	if got.seq != 3 {
		t.Errorf("CallAt written before Stop at delivered position 3: position = %d, want 3", got.seq)
	}
}

// With no read loop to drain, Stop settles the written request itself, at the same position.
func TestCallAt_StopWithNoReadLoopSettlesAtTheDeliveredPosition(t *testing.T) {
	b := New("/unused", "/work")
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = inR.Close() })
	b.stdin.Store(&stdinPipe{w: inW})
	b.deliveredSeq = 3

	done := callAtAsync(t, b)
	readFrame(t, inR)
	b.Stop()
	got := awaitCallAt(t, done)

	if !errors.Is(got.err, marotte.ErrBridgeExited) {
		t.Fatalf("CallAt written before Stop: error = %v, want ErrBridgeExited", got.err)
	}
	if got.seq != 3 {
		t.Errorf("CallAt written before Stop at delivered position 3: position = %d, want 3", got.seq)
	}
}

// Once the read loop has failed its pending requests, nothing can settle a new one, so it is refused unwritten
// rather than reported as written and lost.
func TestCallAt_OnABridgeWhoseReadLoopExitedRefusesWithoutWriting(t *testing.T) {
	b := New("/unused", "/work")
	b.stdout = newFrameReader(bufio.NewReaderSize(strings.NewReader(""), stdoutBufSize))
	b.readLoop()
	b.Stop()
	// A stdin that still takes bytes: the window before the reap closes it.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = inR.Close() })
	b.stdin.Store(&stdinPipe{w: inW})

	got := awaitCallAt(t, callAtAsync(t, b))
	_ = inW.Close()
	wrote, readErr := io.ReadAll(inR)

	if got.err == nil || errors.Is(got.err, marotte.ErrBridgeExited) {
		t.Errorf("CallAt after the read loop exited: error = %v, want a refusal that does not claim a write", got.err)
	}
	if readErr != nil || len(wrote) != 0 {
		t.Errorf("CallAt after the read loop exited wrote %q (read error %v), want nothing", wrote, readErr)
	}
}

// A response after the read loop exited would reach no agent, so it is refused unwritten and reported as the
// bridge's exit.
func TestRespond_OnABridgeWhoseReadLoopExitedRefusesWithoutWriting(t *testing.T) {
	b := New("/unused", "/work")
	b.stdout = newFrameReader(bufio.NewReaderSize(strings.NewReader(""), stdoutBufSize))
	b.readLoop()
	b.Stop()
	// A stdin that still takes bytes: the window before the reap closes it.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = inR.Close() })
	b.stdin.Store(&stdinPipe{w: inW})

	got := b.Respond(t.Context(), 7, map[string]string{}, nil)
	_ = inW.Close()
	wrote, readErr := io.ReadAll(inR)

	if !errors.Is(got, marotte.ErrBridgeExited) {
		t.Errorf("Respond after the read loop exited: error = %v, want ErrBridgeExited", got)
	}
	if readErr != nil || len(wrote) != 0 {
		t.Errorf("Respond after the read loop exited wrote %q (read error %v), want nothing", wrote, readErr)
	}
}

// The exit drain waits out a response already being written, so a response is wholly written before the close or
// refused after it, never reported written once the read loop is gone.
func TestClosePending_WaitsOutAResponseBeingWritten(t *testing.T) {
	b := New("/unused", "/work")
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = inR.Close(); _ = inW.Close() })
	b.stdin.Store(&stdinPipe{w: inW})
	// Far larger than a pipe's buffer, so the write is still in flight after its first byte is read.
	big := strings.Repeat("x", 1<<20)
	responded := make(chan error, 1)
	go func() { responded <- b.Respond(t.Context(), 7, big, nil) }()
	if _, err := io.ReadFull(inR, make([]byte, 1)); err != nil {
		t.Fatalf("Setup: reading the response's first byte: %v", err)
	}

	closed := make(chan struct{})
	go func() { b.closePending(); close(closed) }()
	deadline := time.Now().Add(10 * time.Second)
	for !parkedOnMutexIn("(*Bridge).closePending") {
		select {
		case <-closed:
			t.Fatal("closePending returned while a response was mid-write, want it to wait for the write")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("closePending neither returned nor waited for the response's write within 10s")
		}
		time.Sleep(time.Millisecond)
	}
	go func() { _, _ = io.Copy(io.Discard, inR) }()

	if err := <-responded; err != nil {
		t.Errorf("Respond begun before the close = %v, want nil: it was wholly written", err)
	}
	<-closed
}

// The response fence is published before the exit drain contends for the pending-request lock, so a response begun
// after the read loop exited is refused unwritten even while that drain is blocked.
func TestRespond_AfterReadLoopExitIsRefusedWhileTheDrainWaitsOnPendingRequests(t *testing.T) {
	b := New("/unused", "/work")
	b.stdout = newFrameReader(bufio.NewReaderSize(strings.NewReader(""), stdoutBufSize))
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = inR.Close(); _ = inW.Close() })
	// The read loop's exit reaps the bridge; a stdin that reap cannot close leaves the fence the only refusal.
	b.stdin.Store(&stdinPipe{w: unclosableWriter{inW}})

	b.pendingMu.Lock()
	exited := make(chan struct{})
	go func() { b.readLoop(); close(exited) }()
	deadline := time.Now().Add(10 * time.Second)
	for !parkedOnMutexIn("(*Bridge).closePending") {
		if time.Now().After(deadline) {
			b.pendingMu.Unlock()
			t.Fatal("Setup: the read loop's exit drain did not reach the held pending-request lock within 10s")
		}
		time.Sleep(time.Millisecond)
	}

	got := b.Respond(t.Context(), 7, map[string]string{}, nil)
	_ = inW.Close()
	wrote, readErr := io.ReadAll(inR)
	b.pendingMu.Unlock()
	<-exited
	b.Stop()

	if !errors.Is(got, marotte.ErrBridgeExited) {
		t.Errorf("Respond after the read loop exited, drain blocked on pendingMu: error = %v, want ErrBridgeExited", got)
	}
	if readErr != nil || len(wrote) != 0 {
		t.Errorf("Respond after the read loop exited, drain blocked on pendingMu: wrote %q (read error %v), want nothing",
			wrote, readErr)
	}
}

// A request begun while the exit drain waits out an earlier response is refused unwritten: the fence closes every
// outbound frame at once, so no request reaches a stdin the read loop no longer answers.
func TestCallAt_DuringTheExitDrainsWaitOnAResponseIsRefusedWithoutWriting(t *testing.T) {
	b := New("/unused", "/work")
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Setup: stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = inR.Close(); _ = inW.Close() })
	b.stdin.Store(&stdinPipe{w: inW})
	// Far larger than a pipe's buffer, so the response holds writeMu until the test drains the pipe.
	responded := make(chan error, 1)
	go func() { responded <- b.Respond(t.Context(), 7, strings.Repeat("x", 1<<20), nil) }()
	if _, err := io.ReadFull(inR, make([]byte, 1)); err != nil {
		t.Fatalf("Setup: reading the response's first byte: %v", err)
	}
	closed := make(chan struct{})
	go func() { b.closePending(); close(closed) }()
	awaitParkedOnMutex(t, "(*Bridge).closePending")

	called := callAtAsync(t, b)
	awaitParkedOnMutex(t, "(*Bridge).CallAt")
	var wrote bytes.Buffer
	copied := make(chan error, 1)
	go func() { _, cErr := io.Copy(&wrote, inR); copied <- cErr }()
	if err := <-responded; err != nil {
		t.Errorf("Respond begun before the drain = %v, want nil: it was wholly written", err)
	}
	<-closed
	got := awaitCallAt(t, called)
	_ = inW.Close()
	if cErr := <-copied; cErr != nil {
		t.Fatalf("reading stdin: %v", cErr)
	}

	if got.err == nil || errors.Is(got.err, marotte.ErrBridgeExited) {
		t.Errorf("CallAt during the exit drain's wait: error = %v, want a refusal that does not claim a write", got.err)
	}
	if bytes.Contains(wrote.Bytes(), []byte(marotte.MethodPrompt)) {
		t.Errorf("CallAt during the exit drain's wait wrote its request after the read loop exited; stdin tail %q",
			wrote.Bytes()[max(0, wrote.Len()-200):])
	}
}

func awaitParkedOnMutex(t *testing.T, fn string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !parkedOnMutexIn(fn); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("Setup: no goroutine in %s parked on a mutex within 10s", fn)
		}
	}
}

type unclosableWriter struct{ io.Writer }

func (unclosableWriter) Close() error { return nil }

// parkedOnMutexIn reports whether some goroutine is parked acquiring a sync.Mutex with fn on its stack: the
// positive signal that a contended lock, not the scheduler, is what holds it.
func parkedOnMutexIn(fn string) bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "[sync.Mutex.Lock") && strings.Contains(g, fn) {
			return true
		}
	}
	return false
}
