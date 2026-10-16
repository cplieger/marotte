package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

// TestSessionStartTimeout_KeepsEngineAndCoTenants: a session/new past its budget fails that door only; a co-tenant's
// in-flight prompt still completes, and the engine is the same one.
func TestSessionStartTimeout_KeepsEngineAndCoTenants(t *testing.T) {
	_ = capture.Default(t)
	k := newFakeKAS(t)
	tenant := k.openedSession(nil)
	shortenBudgets(t)

	prompted := make(chan error, 1)
	go func() {
		_, err := tenant.Call(context.Background(), marotte.MethodPrompt, map[string]any{"sessionId": tenant.SessionID()})
		prompted <- err
	}()
	prompt := k.nextMethod(marotte.MethodPrompt)
	opened := make(chan error, 1)
	go func() {
		_, err := k.e.NewSession(context.Background(), &marotte.SessionOpts{}, nil)
		opened <- err
	}()
	k.nextMethod(methodSessionNew) // never answered
	if err := <-opened; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an unanswered session/new = %v, want its budget's expiry", err)
	}
	k.reply(prompt.ID, map[string]any{"stopReason": "end_turn"})
	if err := <-prompted; err != nil {
		t.Errorf("the co-tenant's prompt failed after another session's door expired: %v", err)
	}
	select {
	case <-k.e.Done():
		t.Error("a session door's expiry ended the engine")
	default:
	}
}

// abandonedNew starts a NewSession whose ctx the test cancels once the request is written, and returns the request.
func abandonedNew(t *testing.T, k *fakeKAS) wireFrame {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := k.e.NewSession(ctx, &marotte.SessionOpts{}, nil)
		done <- err
	}()
	f := k.nextMethod(methodSessionNew)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("an abandoned NewSession = %v, want its caller's cancellation", err)
	}
	return f
}

func reapRecorder(k *fakeKAS) <-chan marotte.SessionID {
	ch := make(chan marotte.SessionID, 4)
	k.e.reapSession = func(id marotte.SessionID) { ch <- id }
	return ch
}

func TestAbandonedSessionNew_LateReplyIsClosedAndReaped(t *testing.T) {
	k := newFakeKAS(t)
	reaped := reapRecorder(k)
	f := abandonedNew(t, k)
	minted := f.overrideID()
	k.reply(f.ID, map[string]any{"sessionId": minted})
	closeFrame := k.nextMethod(methodSessionClose)
	if got := closeFrame.param(marotte.KeySessionID); got != string(minted) {
		t.Errorf("the late reply's session/close names %q, want the minted %q", got, minted)
	}
	k.reply(closeFrame.ID, map[string]any{})
	select {
	case id := <-reaped:
		if id != minted {
			t.Errorf("reaped %q, want the minted id", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session KAS created for an abandoned door was never reaped")
	}
	k.e.lanesMu.Lock()
	_, still := k.e.sessions[minted]
	k.e.lanesMu.Unlock()
	if still {
		t.Error("the abandoned door's lane stayed registered")
	}
}

func TestAbandonedSessionNew_LateReplyWithADifferentIDClosesThatID(t *testing.T) {
	logs := capture.Default(t)
	k := newFakeKAS(t)
	reaped := reapRecorder(k)
	f := abandonedNew(t, k)
	const kasMinted = "sess_00000000-0000-4000-8000-0000000000aa"
	k.reply(f.ID, map[string]any{"sessionId": kasMinted})
	closeFrame := k.nextMethod(methodSessionClose)
	if got := closeFrame.param(marotte.KeySessionID); got != kasMinted {
		t.Errorf("session/close names %q, want the id KAS created, %q", got, kasMinted)
	}
	k.reply(closeFrame.ID, map[string]any{})
	select {
	case id := <-reaped:
		if id != kasMinted {
			t.Errorf("reaped %q, want %q", id, kasMinted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session KAS minted itself was never reaped")
	}
	if logs.CountExact("engine: kiro-cli ignored the session id marotte sent, so a session setting failed its schema") != 1 {
		t.Error("the ignored override was not logged")
	}
}

func TestAbandonedSessionNew_LateErrorReplyClosesNothing(t *testing.T) {
	k := newFakeKAS(t)
	f := abandonedNew(t, k)
	k.replyError(f.ID, -32600, "Session already exists")
	if f, quiet := k.quiet(300 * time.Millisecond); !quiet {
		t.Errorf("a late error reply wrote %s; it created nothing to close", f.raw)
	}
	k.e.lanesMu.Lock()
	_, still := k.e.sessions[f.overrideID()]
	k.e.lanesMu.Unlock()
	if still {
		t.Error("the abandoned door's lane stayed registered")
	}
}

func TestAbandonedSessionLoad_LateReplyIsClosedUnlessSuperseded(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(map[bool]string{false: "alone", true: "superseded"}[superseded], func(t *testing.T) {
			k := newFakeKAS(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := k.e.LoadSession(ctx, &marotte.SessionOpts{SessionID: "sess_load"}, nil)
				done <- err
			}()
			first := k.nextMethod(methodSessionLoad)
			cancel()
			<-done
			if superseded {
				go func() {
					_, _ = k.e.LoadSession(context.Background(), &marotte.SessionOpts{SessionID: "sess_load"}, nil)
				}()
				k.nextMethod(methodSessionLoad)
			}
			k.reply(first.ID, map[string]any{})
			f, quiet := k.quiet(300 * time.Millisecond)
			closed := !quiet && f.Method == methodSessionClose
			if closed == superseded {
				t.Errorf("late load reply with superseded=%v: wrote close=%v (%s)", superseded, closed, f.raw)
			}
		})
	}
}

// TestAbandonedSessionLoad_ANewLoadWaitsForTheLateClose: a load for the id started while the abandoned load's close is
// in flight writes nothing until that close settles, so the close never lands on the newer session.
func TestAbandonedSessionLoad_ANewLoadWaitsForTheLateClose(t *testing.T) {
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
	k.reply(first.ID, map[string]any{})
	closeFrame := k.nextMethod(methodSessionClose)
	opened := make(chan error, 1)
	go func() {
		_, err := k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "sess_load"}, nil)
		opened <- err
	}()
	if f, quiet := k.quiet(200 * time.Millisecond); !quiet {
		t.Fatalf("a load wrote %s while the abandoned load's close was unsettled", f.raw)
	}
	k.reply(closeFrame.ID, map[string]any{})
	again := k.nextMethod(methodSessionLoad)
	k.reply(again.ID, map[string]any{})
	k.answerUntilErr(opened)
}

// TestAbandonedSessionLoad_ALateReplyBeforeTheDoorFailsIsClosed: callAt gives up on the load before LoadSession's
// failDoor takes the handle off the table, so a reply in that gap finds the dead door's own handle, which must not
// read as a newer one.
func TestAbandonedSessionLoad_ALateReplyBeforeTheDoorFailsIsClosed(t *testing.T) {
	k := newFakeKAS(t)
	s, err := k.e.newSessionHandle(t.Context(), "sess_load", &marotte.SessionOpts{SessionID: "sess_load"})
	if err != nil {
		t.Fatalf("Setup: newSessionHandle = %v", err)
	}
	k.e.lateSessionLoad(s)(&marotte.RPCResponse{Result: json.RawMessage(`{}`)})
	closeFrame := k.nextMethod(methodSessionClose)
	if got := closeFrame.param(marotte.KeySessionID); got != "sess_load" {
		t.Errorf("session/close names %q, want sess_load", got)
	}
	s.failDoor("load", context.Canceled)
	opened := make(chan error, 1)
	go func() {
		_, err := k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "sess_load"}, nil)
		opened <- err
	}()
	if f, quiet := k.quiet(200 * time.Millisecond); !quiet {
		t.Fatalf("a load wrote %s while the abandoned load's close was unsettled", f.raw)
	}
	k.reply(closeFrame.ID, map[string]any{})
	again := k.nextMethod(methodSessionLoad)
	k.reply(again.ID, map[string]any{})
	k.answerUntilErr(opened)
}

func TestEngineExit_ClosesEveryLaneAndFailsEveryPending(t *testing.T) {
	_ = capture.Default(t)
	k := newFakeKAS(t)
	a, b := k.openedSession(nil), k.openedSession(nil)
	failed := make(chan error, 2)
	for _, s := range []*Session{a, b} {
		go func() {
			_, err := s.Call(context.Background(), marotte.MethodPrompt, map[string]any{"sessionId": s.SessionID()})
			failed <- err
		}()
		k.nextMethod(marotte.MethodPrompt)
	}
	_ = k.outW.Close() // the engine ends itself
	for range 2 {
		if err := <-failed; !errors.Is(err, marotte.ErrBridgeExited) {
			t.Errorf("a pending prompt at the engine's end = %v, want ErrBridgeExited", err)
		}
	}
	for _, s := range []*Session{a, b} {
		select {
		case <-s.closeDone:
		case <-time.After(2 * time.Second):
			t.Errorf("session %s did not close with the engine", s.SessionID())
		}
		drain(s.NotifCh(), 2*time.Second)
	}
	drain(k.e.EngineNotifCh(), 2*time.Second)
	if got := k.e.Exit(); got.Reason != ExitEnded || !got.EndedItself() {
		t.Errorf("Exit() = %+v, want an engine that ended itself", got)
	}
}

func TestEngineExit_OnExitRunsBeforeAnyPendingFailsOrLaneCloses(t *testing.T) {
	_ = capture.Default(t)
	var (
		k         *fakeKAS
		s         *Session
		sawOpen   bool
		sawWaiter bool
		hooked    = make(chan ExitInfo, 1)
	)
	k = newFakeKAS(t, WithExitHook(func(info ExitInfo) {
		sawOpen = s.Open()
		k.e.pendingMu.Lock()
		sawWaiter = len(k.e.pending) == 1
		k.e.pendingMu.Unlock()
		hooked <- info
	}))
	s = k.openedSession(nil)
	go func() { _, _ = s.Call(context.Background(), marotte.MethodPrompt, nil) }()
	k.nextMethod(marotte.MethodPrompt)
	_ = k.outW.Close()
	info := <-hooked
	if !sawOpen || !sawWaiter {
		t.Errorf("the exit hook saw open=%v pending=%v, want the session still open and its prompt still pending", sawOpen, sawWaiter)
	}
	if info.Reason != ExitEnded {
		t.Errorf("hook reason = %q, want %q", info.Reason, ExitEnded)
	}
}

// TestStart_ARequestBeforeTheInitializeResultReachesTheEngineLane pins the startup order: the engine lane and the read
// loop run before initialize is written, so a host callback KAS sends around initialize is consumable at once, and an
// answered one lets initialize finish.
func TestStart_ARequestBeforeTheInitializeResultReachesTheEngineLane(t *testing.T) {
	k := newFakeKAS(t)
	initialized := make(chan error, 1)
	go func() { initialized <- k.e.initialize(context.Background()) }()
	init := k.nextMethod(methodInitialize)
	k.request(41, "_kiro/auth/getAccessToken", map[string]any{})
	select {
	case n := <-k.e.EngineNotifCh():
		if n.Msg.Method != "_kiro/auth/getAccessToken" {
			t.Fatalf("the engine lane got %q first", n.Msg.Method)
		}
		_ = k.e.Respond(context.Background(), *n.Msg.ID, nil, &marotte.RPCError{Code: -32000, Message: "unauthenticated"})
	case <-time.After(2 * time.Second):
		t.Fatal("a host callback sent before the initialize result never reached the engine lane")
	}
	if f := k.next(); f.ID == nil || *f.ID != 41 {
		t.Fatalf("the callback was not answered before initialize completed; wrote %s", f.raw)
	}
	k.reply(init.ID, map[string]any{})
	if err := <-initialized; err != nil {
		t.Errorf("initialize after the callback was answered = %v", err)
	}
}

func TestEngine_StderrFlagsTheExitRecord(t *testing.T) {
	_ = capture.Default(t)
	for name, tc := range map[string]struct {
		line          string
		heap, timeout bool
	}{
		"a heap-limit death":  {line: "FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory", heap: true},
		"the relay's timeout": {line: "error: timed out writing to the v3 engine's stdin", timeout: true},
		"anything else":       {line: "warn: something happened"},
	} {
		t.Run(name, func(t *testing.T) {
			e := NewEngine("/nonexistent", "/work")
			e.lifecycleCtx = context.Background()
			e.forwardStderr(strings.NewReader(tc.line + "\n"))
			got := e.Exit()
			if got.JSHeapOOM != tc.heap || got.RelayWriteTimeout != tc.timeout {
				t.Errorf("Exit() = %+v, want js_heap_oom=%v relay_write_timeout=%v", got, tc.heap, tc.timeout)
			}
		})
	}
}

func TestSidecarExitOf_NamesTheCodeOrTheSignal(t *testing.T) {
	exitOne := exec.Command("/bin/sh", "-c", "exit 1")
	_ = exitOne.Run()
	if got := sidecarExitOf(exitOne.ProcessState); got != "exit_code=1" {
		t.Errorf("sidecarExitOf(exit 1) = %q, want exit_code=1", got)
	}
	killed := exec.Command("/bin/sh", "-c", "kill -KILL $$")
	_ = killed.Run()
	if got := sidecarExitOf(killed.ProcessState); got != "signal=killed" {
		t.Errorf("sidecarExitOf(SIGKILL) = %q, want signal=killed", got)
	}
	if got := sidecarExitOf(nil); got != "" {
		t.Errorf("sidecarExitOf(nil) = %q, want empty", got)
	}
}

// TestEngineStop_RecordsAStopNotAnEnd: Stop labels the end as marotte's, whatever the read loop sees after.
func TestEngineStop_RecordsAStopNotAnEnd(t *testing.T) {
	k := newFakeKAS(t)
	k.e.Stop()
	if got := k.e.Exit(); got.Reason != ExitStopped || got.EndedItself() {
		t.Errorf("Exit() after Stop = %+v, want reason stopped and not ended itself", got)
	}
}
