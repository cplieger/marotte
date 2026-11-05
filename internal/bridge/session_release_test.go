package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/slogx/capture"
)

const (
	heldMsg     = "a closed session is still held by the agent engine; restarting the agent engine from Settings releases it"
	releasedMsg = "a held session was released"
)

// shortenRelease makes a released session re-send and report quickly. Not parallel.
func shortenRelease(t *testing.T, budget time.Duration, retry []time.Duration, leak time.Duration) {
	t.Helper()
	origBudget, origRetry, origLeak := closeBudget, releaseRetry, releaseLeakAfter
	closeBudget, releaseRetry, releaseLeakAfter = budget, retry, leak
	t.Cleanup(func() { closeBudget, releaseRetry, releaseLeakAfter = origBudget, origRetry, origLeak })
}

// awaitSettled fails unless s reaches closed within 2 s.
func awaitSettled(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the session never settled closed")
	}
}

// A released session's late refusal does not reopen it: its caller let go, so the close is re-sent instead.
func TestSessionRelease_ALateRefusalNeverReopens(t *testing.T) {
	shortenRelease(t, 100*time.Millisecond, []time.Duration{300 * time.Millisecond}, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	s.Release()
	first := k.nextMethod(methodSessionClose)
	if f, quiet := k.quiet(150 * time.Millisecond); !quiet {
		t.Fatalf("wrote %s inside the first close's budget", f.raw)
	}
	k.replyError(first.ID, -32000, "could not confirm the remote turn stopped")
	k.quiet(50 * time.Millisecond)
	if s.Open() {
		t.Fatal("a late refusal reopened a released session")
	}
	retry := k.nextMethod(methodSessionClose)
	k.reply(retry.ID, map[string]any{})
	awaitSettled(t, s)
}

// A holder's Close whose budget is spent lets go of the session too, so a late refusal does not reopen it.
func TestSessionClose_AnUnconfirmedCloseNeverReopens(t *testing.T) {
	shortenRelease(t, 100*time.Millisecond, []time.Duration{300 * time.Millisecond}, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	first := k.nextMethod(methodSessionClose)
	if err := <-closed; !errors.Is(err, marotte.ErrSessionClosing) {
		t.Fatalf("an unconfirmed Close = %v, want ErrSessionClosing", err)
	}
	k.replyError(first.ID, -32000, "refused late")
	k.quiet(50 * time.Millisecond)
	if s.Open() {
		t.Fatal("a late refusal reopened a session whose close budget was spent")
	}
	k.reply(k.nextMethod(methodSessionClose).ID, map[string]any{})
	awaitSettled(t, s)
}

// A released session is re-sent its close on the schedule until kiro-cli confirms it.
func TestSessionRelease_RetriesUntilTheEngineConfirms(t *testing.T) {
	shortenRelease(t, 50*time.Millisecond, []time.Duration{20 * time.Millisecond}, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	s.Release()
	for range 3 {
		k.nextMethod(methodSessionClose) // no reply
	}
	k.reply(k.nextMethod(methodSessionClose).ID, map[string]any{})
	awaitSettled(t, s)
}

// A release kiro-cli never confirms is reported once, and the engine's end settles it with one INFO.
func TestSessionRelease_AHeldSessionIsReportedOnceAndEndsWithTheEngine(t *testing.T) {
	logs := capture.Default(t)
	shortenRelease(t, 50*time.Millisecond, []time.Duration{50 * time.Millisecond}, 300*time.Millisecond)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	s.Release()
	eventually(t, "the held session was reported", func() bool { return logs.CountExact(heldMsg) > 0 })
	if got, _ := logs.AttrValueExact(heldMsg, "last_error"); got != "no reply" {
		t.Errorf("last_error = %q, want %q", got, "no reply")
	}
	time.Sleep(300 * time.Millisecond) // a second leak window passes
	if n := logs.CountExact(heldMsg); n != 1 {
		t.Errorf("the held session was reported %d times, want once", n)
	}
	k.e.Stop()
	awaitSettled(t, s)
	eventually(t, "the engine's end logged the release", func() bool { return logs.CountExact(releasedMsg) == 1 })
}

// A refusal is reported by the same one ERROR as a silence, and a door waiting on the id fails then.
func TestSessionRelease_ARefusalIsReportedOnceLikeASilence(t *testing.T) {
	logs := capture.Default(t)
	shortenRelease(t, 50*time.Millisecond, []time.Duration{30 * time.Millisecond}, 300*time.Millisecond)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	stop := make(chan struct{})
	defer close(stop)
	go func() { // refuse every close
		for {
			select {
			case f, ok := <-k.frames:
				if !ok {
					return
				}
				if f.Method == methodSessionClose {
					k.replyError(f.ID, -32000, "close refused")
				}
			case <-stop:
				return
			}
		}
	}()
	s.Release()
	door := make(chan error, 1)
	go func() {
		_, err := k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: string(s.SessionID())}, nil)
		door <- err
	}()
	select {
	case err := <-door:
		if !errors.Is(err, marotte.ErrSessionClosing) {
			t.Errorf("a door waiting on a held session = %v, want ErrSessionClosing", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the door waiting on a refused release never returned")
	}
	if s.Open() {
		t.Error("a refused release reopened the session")
	}
	if n := logs.CountExact(heldMsg); n != 1 {
		t.Errorf("the refused release was reported %d times, want once", n)
	}
	if got, _ := logs.AttrValueExact(heldMsg, "last_error"); got == "no reply" || got == "" {
		t.Errorf("last_error = %q, want the refusal", got)
	}
}

// Release closes the lane at the call: the forward gets what was queued and its channel ends, and later frames for the
// id reach the engine lane.
func TestSessionRelease_ClosesTheLaneAtTheCall(t *testing.T) {
	shortenRelease(t, time.Hour, nil, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	update := func(tag string) map[string]any {
		return map[string]any{marotte.KeySessionID: s.SessionID(), "tag": tag}
	}
	k.notify(marotte.MethodSessionUpdate, update("queued"))
	ping := make(chan error, 1)
	go func() { _, err := k.e.Call(context.Background(), "_probe/ping", nil); ping <- err }()
	k.reply(k.nextMethod("_probe/ping").ID, map[string]any{})
	if err := <-ping; err != nil {
		t.Fatalf("Setup: round trip: %v", err)
	}
	s.Release()
	got := drain(s.NotifCh(), time.Second)
	if len(got) != 1 {
		t.Fatalf("the lane delivered %d frames after Release, want the one queued", len(got))
	}
	select {
	case _, open := <-s.NotifCh():
		if open {
			t.Fatal("the lane delivered a frame after its queue")
		}
	case <-time.After(time.Second):
		t.Fatal("the lane did not end at Release")
	}
	k.notify(marotte.MethodSessionUpdate, update("late"))
	k.request(9, marotte.MethodRequestPermission, map[string]any{marotte.KeySessionID: s.SessionID()})
	for _, want := range []string{marotte.MethodSessionUpdate, marotte.MethodRequestPermission} {
		select {
		case n := <-k.e.EngineNotifCh():
			if n.Msg.Method != want {
				t.Errorf("the engine lane got %q, want %q", n.Msg.Method, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s for a released session did not reach the engine lane", want)
		}
	}
}

// A Release during a holder's Close wins: the Close's in-budget refusal does not reopen the session.
func TestSessionRelease_DuringAHoldersCloseNeverReopens(t *testing.T) {
	shortenRelease(t, time.Hour, []time.Duration{50 * time.Millisecond}, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	first := k.nextMethod(methodSessionClose)
	s.Release()
	k.replyError(first.ID, -32000, "refused")
	if err := <-closed; err == nil {
		t.Error("the holder's refused Close returned nil")
	}
	if s.Open() {
		t.Fatal("the holder's in-budget refusal reopened a released session")
	}
	if !s.Released() {
		t.Error("Released() = false after Release")
	}
	k.reply(k.nextMethod(methodSessionClose).ID, map[string]any{})
	awaitSettled(t, s)
}

// A Close whose caller left lets go of the session: the lane closes and a later refusal does not reopen it.
func TestSessionClose_ACallersContextEndTurnsIntoARelease(t *testing.T) {
	shortenRelease(t, time.Hour, []time.Duration{50 * time.Millisecond}, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	ctx, cancel := context.WithCancel(t.Context())
	closed := make(chan error, 1)
	go func() { closed <- s.Close(ctx) }()
	first := k.nextMethod(methodSessionClose)
	cancel()
	if err := <-closed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with a cancelled caller = %v, want its cancellation", err)
	}
	if !s.Released() {
		t.Error("Released() = false after the caller left")
	}
	select {
	case _, open := <-s.NotifCh():
		if open {
			t.Error("the lane delivered a frame")
		}
	case <-time.After(time.Second):
		t.Error("the lane stayed open after the caller left")
	}
	k.replyError(first.ID, -32000, "refused")
	// The retry is written after the refusal is handled; a fixed quiet window could swallow it.
	retry := k.nextMethod(methodSessionClose)
	if s.Open() {
		t.Fatal("a refusal reopened a session whose caller left")
	}
	k.reply(retry.ID, map[string]any{})
	awaitSettled(t, s)
}

// A door for an id whose released predecessor is never confirmed fails when the predecessor is declared held, not
// before and not at its own deadline.
func TestSessionDoor_AHeldPredecessorFailsTheDoorWhenTheLoopDeclaresIt(t *testing.T) {
	_ = capture.Default(t)
	shortenRelease(t, 50*time.Millisecond, []time.Duration{50 * time.Millisecond}, 300*time.Millisecond)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	start := time.Now()
	s.Release()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err := k.e.LoadSession(ctx, &marotte.SessionOpts{SessionID: string(s.SessionID())}, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("door = %v, want ErrSessionClosing", err)
	}
	if elapsed < 250*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("the door returned after %v, want at the held verdict (300ms)", elapsed)
	}
}

// A slow close is waited for however long it takes, and the door then opens with no error.
func TestSessionDoor_ASlowCloseOpensOnceItSettles(t *testing.T) {
	shortenRelease(t, 100*time.Millisecond, []time.Duration{100 * time.Millisecond}, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	s.Release()
	k.nextMethod(methodSessionClose) // no reply
	door := make(chan error, 1)
	go func() {
		_, err := k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: string(s.SessionID())}, nil)
		door <- err
	}()
	k.nextMethod(methodSessionClose) // no reply
	k.reply(k.nextMethod(methodSessionClose).ID, map[string]any{})
	load := k.nextMethod(methodSessionLoad)
	k.reply(load.ID, map[string]any{})
	k.answerUntilErr(door)
}

// A door whose own ctx ends while the predecessor still closes answers its own timeout, not the restart remedy.
func TestSessionDoor_ACallerTimeoutDuringASlowCloseAnswersItsOwnTimeout(t *testing.T) {
	shortenRelease(t, time.Hour, nil, time.Hour)
	k := newFakeKAS(t)
	s := k.openedSession(nil)
	s.Release()
	k.nextMethod(methodSessionClose)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := k.e.LoadSession(ctx, &marotte.SessionOpts{SessionID: string(s.SessionID())}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("door = %v, want its own deadline", err)
	}
	if errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("door = %v, which names the restart remedy for a close that is only slow", err)
	}
}

// abandonedLoad starts a LoadSession of id and cancels it once session/load is written.
func abandonedLoad(t *testing.T, k *fakeKAS, id string) wireFrame {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := k.e.LoadSession(ctx, &marotte.SessionOpts{SessionID: id}, nil)
		done <- err
	}()
	f := k.nextMethod(methodSessionLoad)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("an abandoned load did not return at once")
	}
	return f
}

// An abandoned load keeps its handle until kiro-cli answers, so a load of the same id waits for it.
func TestSessionDoor_AnAbandonedLoadKeepsItsHandleUntilKiroAnswers(t *testing.T) {
	t.Run("answered", func(t *testing.T) {
		k := newFakeKAS(t)
		first := abandonedLoad(t, k, "sess_load")
		again := make(chan error, 1)
		go func() {
			_, err := k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "sess_load"}, nil)
			again <- err
		}()
		if f, quiet := k.quiet(200 * time.Millisecond); !quiet {
			t.Fatalf("a second load wrote %s while the first was unanswered", f.raw)
		}
		k.reply(first.ID, map[string]any{})
		closeFrame := k.nextMethod(methodSessionClose)
		if f, quiet := k.quiet(100 * time.Millisecond); !quiet {
			t.Fatalf("a second load wrote %s before the first's close settled", f.raw)
		}
		k.reply(closeFrame.ID, map[string]any{})
		k.reply(k.nextMethod(methodSessionLoad).ID, map[string]any{})
		k.answerUntilErr(again)
	})
	t.Run("refused", func(t *testing.T) {
		k := newFakeKAS(t)
		first := abandonedLoad(t, k, "sess_load")
		again := make(chan error, 1)
		go func() {
			_, err := k.e.LoadSession(t.Context(), &marotte.SessionOpts{SessionID: "sess_load"}, nil)
			again <- err
		}()
		k.replyError(first.ID, -32603, "no such session")
		if f := k.next(); f.Method != methodSessionLoad {
			t.Fatalf("after the first load's refusal the engine wrote %s, want the second load", f.raw)
		} else {
			k.reply(f.ID, map[string]any{})
		}
		k.answerUntilErr(again)
	})
}

// An abandoned session/new keeps its handle as released from the give-up, and its late result is closed and reaped.
func TestAbandonedSessionNew_KeepsItsHandleAndIsReleased(t *testing.T) {
	k := newFakeKAS(t)
	reaped := reapRecorder(k)
	f := abandonedNew(t, k)
	id := f.overrideID()
	waitCtx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := k.e.newSessionHandle(waitCtx, id, &marotte.SessionOpts{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a door for the abandoned id = %v, want it to wait on the kept handle", err)
	}
	k.reply(f.ID, map[string]any{"sessionId": id})
	closeFrame := k.nextMethod(methodSessionClose)
	if got := closeFrame.param(marotte.KeySessionID); got != string(id) {
		t.Errorf("session/close names %q, want %q", got, id)
	}
	k.reply(closeFrame.ID, map[string]any{})
	select {
	case got := <-reaped:
		if got != id {
			t.Errorf("reaped %q, want %q", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the abandoned session was never reaped")
	}
	s, err := k.e.newSessionHandle(t.Context(), id, &marotte.SessionOpts{})
	if err != nil {
		t.Fatalf("a door for the id after the settle = %v", err)
	}
	s.closed()
}

// A kept handle kiro-cli never answers is declared held at the clock, failing a door waiting on it then; a later
// answer is closed and logged as released, and an error answer before the clock reports nothing.
func TestSessionDoor_AnAbandonedLoadNeverAnsweredIsDeclaredHeld(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		logs := capture.Default(t)
		shortenRelease(t, time.Hour, nil, 300*time.Millisecond)
		k := newFakeKAS(t)
		first := abandonedLoad(t, k, "sess_load")
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()
		_, err := k.e.LoadSession(ctx, &marotte.SessionOpts{SessionID: "sess_load"}, nil)
		if !errors.Is(err, marotte.ErrSessionClosing) {
			t.Errorf("a door waiting on a never-answered load = %v, want ErrSessionClosing", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("the door returned after %v, want at the held verdict", elapsed)
		}
		if n := logs.CountExact(heldMsg); n != 1 {
			t.Fatalf("held reported %d times, want once", n)
		}
		if got, _ := logs.AttrValueExact(heldMsg, "last_error"); got != "no reply to session/load" {
			t.Errorf("last_error = %q, want %q", got, "no reply to session/load")
		}
		k.reply(first.ID, map[string]any{})
		k.reply(k.nextMethod(methodSessionClose).ID, map[string]any{})
		eventually(t, "the late settle logged the release", func() bool { return logs.CountExact(releasedMsg) == 1 })
		if n := logs.CountExact(heldMsg); n != 1 {
			t.Errorf("held reported %d times after the late answer, want once", n)
		}
	})
	t.Run("new", func(t *testing.T) {
		logs := capture.Default(t)
		shortenRelease(t, time.Hour, nil, 200*time.Millisecond)
		k := newFakeKAS(t)
		abandonedNew(t, k)
		eventually(t, "the abandoned new was reported", func() bool { return logs.CountExact(heldMsg) == 1 })
		if got, _ := logs.AttrValueExact(heldMsg, "last_error"); got != "no reply to session/new" {
			t.Errorf("last_error = %q, want %q", got, "no reply to session/new")
		}
	})
	t.Run("refused before the clock", func(t *testing.T) {
		logs := capture.Default(t)
		shortenRelease(t, time.Hour, nil, 300*time.Millisecond)
		k := newFakeKAS(t)
		first := abandonedLoad(t, k, "sess_load")
		k.replyError(first.ID, -32603, "no such session")
		if f, quiet := k.quiet(500 * time.Millisecond); !quiet {
			t.Errorf("an error answer wrote %s; it created nothing to close", f.raw)
		}
		if logs.CountExact(heldMsg) != 0 {
			t.Error("a load kiro-cli refused was reported held")
		}
	})
}
