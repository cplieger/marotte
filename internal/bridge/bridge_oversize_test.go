package bridge

// An oversize stdout frame is surfaced and the session survives; what it fails is decided by what its head names.

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

func pendingCall(e *Engine, id int64, sessionID marotte.SessionID) chan pendingReply {
	ch := make(chan pendingReply, 1)
	e.pendingMu.Lock()
	e.pending[id] = &pendingEntry{ch: ch, lane: e.engineLane, sessionID: sessionID}
	e.pendingMu.Unlock()
	return ch
}

// oversize is a frame past the cap whose head is head: KAS writes id and method before params.
func oversize(head string) string {
	return head + strings.Repeat("x", scannerLineCap+16) + "\"}}\n"
}

// liveReadLoop runs e's read loop over a pipe the test writes, so the loop stays up until the test closes it.
func liveReadLoop(t *testing.T) (*Engine, *io.PipeWriter) {
	t.Helper()
	pr, pw := io.Pipe()
	e := readLoopEngine(pr)
	ended := make(chan struct{})
	go func() {
		e.readLoop()
		close(ended)
	}()
	t.Cleanup(func() {
		_ = pw.Close()
		<-ended
	})
	return e, pw
}

func writeFrames(t *testing.T, w io.Writer, frames ...string) {
	t.Helper()
	for _, f := range frames {
		if _, err := io.WriteString(w, f); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
}

// settled waits until the read loop has consumed everything before a marker notification on the engine lane.
func drainEngineLane(t *testing.T, e *Engine, w io.Writer) {
	t.Helper()
	writeFrames(t, w, `{"jsonrpc":"2.0","method":"_test/marker","params":{}}`+"\n")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-e.EngineNotifCh():
			if n.Msg != nil && n.Msg.Method == "_test/marker" {
				return
			}
		case <-deadline:
			t.Fatal("the read loop never reached the marker frame")
		}
	}
}

func noReply(t *testing.T, ch chan pendingReply, what string) {
	t.Helper()
	select {
	case got := <-ch:
		t.Errorf("%s was failed with %#v; the dropped frame did not concern it", what, got.resp)
	default:
	}
}

func gotReply(t *testing.T, ch chan pendingReply, want *marotte.RPCResponse, what string) {
	t.Helper()
	select {
	case got := <-ch:
		if got.resp != want {
			t.Errorf("%s got %#v, want %#v", what, got.resp, want)
		}
	default:
		t.Errorf("%s was left waiting; Call has no deadline, so it would hang forever", what)
	}
}

func TestReadLoop_OversizeResponseFailsOnlyItsRequest(t *testing.T) {
	_ = capture.Default(t)
	e, w := liveReadLoop(t)
	mine, other := pendingCall(e, 7, ""), pendingCall(e, 8, "")
	writeFrames(t, w, oversize(`{"jsonrpc":"2.0","id":7,"result":{"text":"`))
	drainEngineLane(t, e, w)
	gotReply(t, mine, frameTooLargeResp, "the request the dropped response answered")
	noReply(t, other, "another pending request")
}

func TestReadLoop_OversizeResponseForCancelledCallFailsNothing(t *testing.T) {
	c := capture.Default(t)
	e, w := liveReadLoop(t)
	other := pendingCall(e, 8, "")
	writeFrames(t, w, oversize(`{"jsonrpc":"2.0","id":7,"result":{"text":"`))
	drainEngineLane(t, e, w)
	noReply(t, other, "a request the dropped response did not answer")
	if !c.HasAttr("frame exceeds the size cap", "kind", "response") {
		t.Error("the dropped response was not logged with kind=response")
	}
}

func TestReadLoop_OversizeNotificationFailsOnlyItsSession(t *testing.T) {
	_ = capture.Default(t)
	e, w := liveReadLoop(t)
	s := newTestSession()
	s.engine = e
	e.lanesMu.Lock()
	e.sessions[s.id] = s
	e.lanesMu.Unlock()
	mine, other := pendingCall(e, 7, s.id), pendingCall(e, 8, "sess-other")
	writeFrames(t, w, oversize(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"`+string(s.id)+`","update":{"text":"`))
	drainEngineLane(t, e, w)
	gotReply(t, mine, frameTooLargeResp, "the session's own request")
	noReply(t, other, "a co-tenant session's request")
}

// stepRouter answers every session as a step of run wf_1.
type stepRouter struct{}

func (stepRouter) Route(*FrameHead) marotte.SessionID { return "" }

//deadset:ignore DS1103 -- reportDroppedFrame reaches it through its stepRunner type assertion
func (stepRouter) StepRun(marotte.SessionID) (string, bool) { return "wf_1", true }

func TestReadLoop_OversizeStepNotificationFailsNothing(t *testing.T) {
	c := capture.Default(t)
	e, w := liveReadLoop(t)
	e.router = stepRouter{}
	pending := pendingCall(e, 7, "sess-chat")
	writeFrames(t, w, oversize(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-step","update":{"text":"`))
	drainEngineLane(t, e, w)
	noReply(t, pending, "a chat's request, for a step's dropped frame")
	if !c.HasAttr("frame exceeds the size cap", "run_id", "wf_1") {
		t.Error("the dropped step frame's line does not name its run")
	}
}

// TestReadLoop_OversizeNotificationForAnUnknownSessionFailsNothing: a session id marotte holds no request on is still
// attributable, so no co-tenant's request fails for it.
func TestReadLoop_OversizeNotificationForAnUnknownSessionFailsNothing(t *testing.T) {
	_ = capture.Default(t)
	e, w := liveReadLoop(t)
	pending := pendingCall(e, 7, "sess-chat")
	writeFrames(t, w, oversize(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-gone","update":{"text":"`))
	drainEngineLane(t, e, w)
	noReply(t, pending, "a chat's request, for an unknown session's dropped frame")
}

func TestReadLoop_OversizeUnattributedFailsAllPending(t *testing.T) {
	_ = capture.Default(t)
	for name, head := range map[string]string{
		"no head":                        "",
		"a notification with no session": `{"jsonrpc":"2.0","method":"_kiro/x","params":{"data":"`,
	} {
		t.Run(name, func(t *testing.T) {
			e, w := liveReadLoop(t)
			a, b := pendingCall(e, 7, "sess-a"), pendingCall(e, 8, "")
			writeFrames(t, w, oversize(head))
			drainEngineLane(t, e, w)
			gotReply(t, a, frameTooLargeResp, "pending request a")
			gotReply(t, b, frameTooLargeResp, "pending request b")
		})
	}
}

func TestReadLoop_OversizeRequestIsAnsweredOnceByItsID(t *testing.T) {
	_ = capture.Default(t)
	e, w := liveReadLoop(t)
	out := &captureWriter{}
	e.stdin.Store(&stdinPipe{w: out})
	pending := pendingCall(e, 7, "")
	writeFrames(t, w, oversize(`{"jsonrpc":"2.0","id":41,"method":"fs/write_text_file","params":{"sessionId":"sess-a","content":"`))
	drainEngineLane(t, e, w)
	noReply(t, pending, "a pending request, for a dropped KAS request")
	deadline := time.Now().Add(2 * time.Second)
	for !out.wrote() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	out.mu.Lock()
	written := out.buf.String()
	out.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(written), "\n")
	if len(lines) != 1 {
		t.Fatalf("wrote %d frames, want exactly one answer:\n%s", len(lines), written)
	}
	var got struct {
		Error *marotte.RPCErrorOut `json:"error"`
		ID    int64                `json:"id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("decode the answer %q: %v", lines[0], err)
	}
	if got.ID != 41 || got.Error == nil || got.Error.Code != marotte.RPCCodeInternal {
		t.Errorf("answer = %s, want a -32603 error for request 41", lines[0])
	}
}

// The frame after an oversize one still reaches dispatch: the turn dies, not the session.
func TestReadLoop_ResumesDispatchAfterAnOversizeFrame(t *testing.T) {
	_ = capture.Default(t)
	e, w := liveReadLoop(t)
	writeFrames(t, w, oversize(""), `{"jsonrpc":"2.0","method":"session/update","params":{}}`+"\n")
	select {
	case n := <-e.EngineNotifCh():
		if n.Msg == nil || n.Msg.Method != marotte.MethodSessionUpdate {
			t.Fatalf("frame after the oversize one = %#v, want session/update", n.Msg)
		}
		if n.Seq != 1 {
			t.Errorf("stamped position = %d, want 1: the DROPPED frame is not delivered, so it takes no position", n.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the frame after an oversize one never reached dispatch; the stream did not resynchronise")
	}
}

// Call returns a non-retryable transport error carrying marotte.ErrFrameTooLarge: a retry would re-run an expensive
// turn into the same payload. promptFailureReason shows the wording.
func TestCall_FrameTooLargeIsNonRetryableAndNamed(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	b.stdin.Store(&stdinPipe{w: &captureWriter{}})

	done := make(chan error, 1)
	go func() {
		_, err := b.Call(t.Context(), marotte.MethodPrompt, nil)
		done <- err
	}()
	waitPending(t, b, 1)
	b.failPending(frameTooLargeResp, matchAll)

	select {
	case err := <-done:
		if !errors.Is(err, marotte.ErrFrameTooLarge) {
			t.Fatalf("Call error = %v, want marotte.ErrFrameTooLarge", err)
		}
		if errors.Is(err, marotte.ErrBridgeExited) {
			t.Error("a dropped frame must not read as a dead engine: the process and the session are still alive")
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

// An unterminated blob ends the engine, with its own log line.
func TestReadLoop_ExhaustedDrainReapsWithItsOwnLogLine(t *testing.T) {
	c := capture.Default(t)
	b := readLoopEngine(&endlessReader{b: 'q'})

	b.readLoop()

	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not end the engine after the drain budget was exhausted")
	}
	if c.CountExact("ACP read: a single frame never terminated within the drain budget; reaping the agent engine") == 0 {
		t.Error("the exhausted-drain case did not get its own log line")
	}
	if got := b.Exit().Reason; got != exitDrainExhausted {
		t.Errorf("exit reason = %q, want %q", got, exitDrainExhausted)
	}
}
