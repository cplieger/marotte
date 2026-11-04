package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func TestChatLife_BeginWorkIsCountedUntilTheEndAndRefusedAfter(t *testing.T) {
	r := newTurnRegistry()
	lc := r.testLifecycle("c1")
	ctx, done, ok := lc.beginWork(workTail)
	if !ok || ctx == nil {
		t.Fatalf("beginWork before the end: ok %v ctx %v, want admitted with the life's context", ok, ctx)
	}
	if ctx.Err() != nil {
		t.Errorf("the life's context is already done: %v", ctx.Err())
	}
	lc.mu.Lock()
	n := lc.work[workTail]
	lc.mu.Unlock()
	if n != 1 {
		t.Errorf("tail count %d, want 1", n)
	}
	r.endLife("c1")
	if _, _, ok := lc.beginWork(workDoor); ok {
		t.Error("beginWork after the end was admitted")
	}
	select {
	case <-lc.quiet:
		t.Error("quiet closed while a tail still ran")
	default:
	}
	done()
	done()
	select {
	case <-lc.quiet:
	case <-time.After(time.Second):
		t.Fatal("quiet did not close at the last done after the end")
	}
	lc.mu.Lock()
	n = lc.work[workTail]
	lc.mu.Unlock()
	if n != 0 {
		t.Errorf("a second done uncounted again: tail count %d", n)
	}
}

func TestChatLife_EndLifeCancelsRemintsAndIsIdempotent(t *testing.T) {
	r := newTurnRegistry()
	lc := r.testLifecycle("c1")
	ctx, done, _ := lc.beginWork(workDoor)
	defer done()
	lc.mu.Lock()
	fence := command.TurnFence{Epoch: lc.epoch, Seq: lc.nextSeq}
	lc.mu.Unlock()
	got, ok := r.endLife("c1")
	if !ok || got != lc {
		t.Fatalf("endLife: lc %p ok %v, want the chat's lifecycle", got, ok)
	}
	if !errors.Is(context.Cause(ctx), marotte.ErrChatClosedDuringCall) {
		t.Errorf("the life's context cause %v, want ErrChatClosedDuringCall", context.Cause(ctx))
	}
	lc.mu.Lock()
	holds := lc.fenceHoldsLocked(fence)
	epoch := lc.epoch
	lc.mu.Unlock()
	if holds {
		t.Error("a fence from before the end still holds")
	}
	again, ok := r.endLife("c1")
	if !ok || again != lc {
		t.Errorf("a second endLife: lc %p ok %v, want the same lifecycle", again, ok)
	}
	lc.mu.Lock()
	if lc.epoch != epoch {
		t.Error("a second endLife re-minted the epoch")
	}
	lc.mu.Unlock()
	if !lc.endedNow() {
		t.Error("endedNow is false after the end")
	}
}

func TestChatLife_QuietClosesAtOnceWhenNoWorkRuns(t *testing.T) {
	r := newTurnRegistry()
	lc := r.testLifecycle("c1")
	r.endLife("c1")
	select {
	case <-lc.quiet:
	case <-time.After(time.Second):
		t.Fatal("quiet stayed open on an ended life with no work")
	}
}

func TestChatLife_ForgetEndsTheLife(t *testing.T) {
	r := newTurnRegistry()
	lc := r.testLifecycle("c1")
	ctx, done, _ := lc.beginWork(workForward)
	done()
	r.forget("c1")
	if !lc.endedNow() || ctx.Err() == nil {
		t.Errorf("forget: ended %v ctx %v, want ended and cancelled", lc.endedNow(), ctx.Err())
	}
	if _, ok := r.lookup("c1"); ok {
		t.Error("the lifecycle is still registered after forget")
	}
}

func TestChatLife_AwaitQuietWarnsOnceAndKeepsWaiting(t *testing.T) {
	logs := captureLogs(t)
	r := newTurnRegistry()
	lc := r.testLifecycle("c1")
	_, done, _ := lc.beginWork(workSweep)
	r.endLife("c1")
	returned := make(chan struct{})
	go func() {
		lc.awaitQuiet(t.Context(), 10*time.Millisecond)
		close(returned)
	}()
	time.Sleep(60 * time.Millisecond)
	select {
	case <-returned:
		t.Fatal("awaitQuiet returned while a sweep ran")
	default:
	}
	done()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("awaitQuiet did not return at the last done")
	}
	out := logs.String()
	if n := strings.Count(out, "a closed chat's background work has not stopped"); n != 1 {
		t.Errorf("WARN logged %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, `"sweeps":1`) {
		t.Errorf("the WARN does not name the running sweep:\n%s", out)
	}
}

func TestChatLife_AnEndedLifeAdmitsNoReservationAndIsNeverHandedOut(t *testing.T) {
	r := newTurnRegistry()
	r.testLifecycle("c1")
	h := r.gates.hold("c1")
	h.commit()
	r.endLife("c1")
	if _, ok := r.tryReserve("c1", marotte.TurnSourcePrompt, r.gates.stamp()); ok {
		t.Error("an ended life admitted a reservation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := r.lifecycleAt(ctx, "c1", r.gates.stamp()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("lifecycleAt on an ended life during its hold: err %v, want it to wait for the hold", err)
	}
	h.release()
}

// A run chat is never held, so its forward attaches with no stamp and no gate; a chat id there is a wiring error.
func TestLifecycleForRun_RefusesAChatID(t *testing.T) {
	t.Run("a chat id panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("lifecycleForRun took a chat id")
			}
		}()
		newTurnRegistry().lifecycleForRun("c1")
	})
	t.Run("a run chat attaches past any gate", func(t *testing.T) {
		r := newTurnRegistry()
		run := marotte.ChatID(runChatPrefix + "wf1")
		h := r.gates.hold(run)
		h.commit()
		got := make(chan forwardAttachment, 1)
		go func() { got <- r.attachRunForward(run) }()
		select {
		case att := <-got:
			if att.gen == 0 {
				t.Error("the run forward got no generation")
			}
			if lc, ok := r.lookup(run); !ok || lc.endedNow() {
				t.Errorf("after the attach the run chat's lifecycle is present %v, want present and live", ok)
			}
			att.exited()
			att.done()
		case <-time.After(time.Second):
			t.Fatal("the run forward waited on a gate")
		}
		h.release()
	})
}

// A reply for a turn of a life that was replaced waits on nothing of the new life: the new forward's position says
// nothing about the old session's frames.
func TestAwaitClosePosition_ATurnOfAnotherLifeWaitsOnNothing(t *testing.T) {
	r := newTurnRegistry()
	r.testLifecycle("c1")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if r.awaitClosePosition(ctx, "c1", "t-old-life", 5) {
		t.Error("the wait reached a position the new life never observed")
	}
	if ctx.Err() != nil {
		t.Error("the wait parked on the new life's forward until its deadline")
	}
}
