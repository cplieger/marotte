package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// A short grace: it is armed against a specific turn generation.
const testGrace = 20 * time.Millisecond

func newPromptingBridge(t *testing.T) (*sharedBridge, context.Context, uint64) {
	t.Helper()
	sb := &sharedBridge{}
	if !sb.tryAcquireForPrompt() {
		t.Fatalf("fresh bridge must be acquirable")
	}
	// Not t.Context(): cancel runs from t.Cleanup, after t.Context() is already done.
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	gen := sb.BeginPromptCall(cancel)
	return sb, ctx, gen
}

// TestArmCancelGrace_UnblocksAnUnackedCancel pins that KAS never answers, so marotte cancels the prompt itself.
func TestArmCancelGrace_UnblocksAnUnackedCancel(t *testing.T) {
	// The bubble makes the elapsed time exactly the grace, so an early or late arm fails.
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		start := time.Now()
		if !sb.ArmCancelGrace(gen, testGrace) {
			t.Fatalf("arming against an in-flight prompt must succeed")
		}
		<-ctx.Done()
		if elapsed := time.Since(start); elapsed != testGrace {
			t.Errorf("prompt context cancelled after %v, want exactly the armed grace %v",
				elapsed, testGrace)
		}
	})
}

// TestArmCancelGrace_AckedCancelLeavesContextAlone pins that a turn ending within the grace disarms the timer.
func TestArmCancelGrace_AckedCancelLeavesContextAlone(t *testing.T) {
	// The bubble makes "the timer did not fire" exact rather than probabilistic.
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		sb.ArmCancelGrace(gen, testGrace)
		sb.releaseAfterPrompt() // KAS answered; the prompt handler's defer runs.

		synctest.Sleep(4 * testGrace)
		if ctx.Err() != nil {
			t.Errorf("context cancelled after the turn already ended: %v", ctx.Err())
		}
	})
}

// TestShouldTripCancelGrace_RefusesANewerTurn pins the generation guard at the decision: a
// running timer func can outlive its turn.
func TestShouldTripCancelGrace_RefusesANewerTurn(t *testing.T) {
	sb, _, gen := newPromptingBridge(t)

	// Turn 1 ends, turn 2 starts.
	sb.releaseAfterPrompt()
	if !sb.tryAcquireForPrompt() {
		t.Fatalf("bridge must be acquirable again after release")
	}
	_, nextCancel := context.WithCancelCause(t.Context())
	defer nextCancel(nil)
	nextGen := sb.BeginPromptCall(nextCancel)
	if nextGen == gen {
		t.Fatalf("a new turn must get a new generation (got %d twice)", gen)
	}

	if _, ok := sb.shouldTripCancelGrace(gen); ok {
		t.Errorf("turn %d's grace must not apply to turn %d", gen, nextGen)
	}
	// The budget armed for the current turn still applies.
	if _, ok := sb.shouldTripCancelGrace(nextGen); !ok {
		t.Errorf("turn %d's own grace must still apply", nextGen)
	}
}

// TestArmCancelGrace_ExpiryCarriesTheGraceCause pins that ctx.Err() is context.Canceled either way, so
// the cause sentinel decides `cancelled` over `interrupted`.
func TestArmCancelGrace_ExpiryCarriesTheGraceCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		if !sb.ArmCancelGrace(gen, testGrace) {
			t.Fatalf("arming against an in-flight prompt must succeed")
		}
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), command.ErrCancelGraceExpired) {
			t.Errorf("context.Cause = %v, want %v: without it the failure site cannot "+
				"tell an expired grace from a shutdown", context.Cause(ctx), command.ErrCancelGraceExpired)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("ctx.Err() = %v, want context.Canceled: the cause is a SEPARATE "+
				"channel and every errors.Is(err, context.Canceled) site must keep matching", ctx.Err())
		}
	})
}

// TestShouldTripCancelGrace_RefusesAFinishedTurn covers the ordinary ack.
func TestShouldTripCancelGrace_RefusesAFinishedTurn(t *testing.T) {
	sb, _, gen := newPromptingBridge(t)
	sb.releaseAfterPrompt()
	if _, ok := sb.shouldTripCancelGrace(gen); ok {
		t.Errorf("grace must not apply to a turn that already ended")
	}
}

// TestArmCancelGrace_RefusesWhenNoPromptInFlight keeps an idle chat from arming a timer.
func TestArmCancelGrace_RefusesWhenNoPromptInFlight(t *testing.T) {
	sb := &sharedBridge{}
	if sb.ArmCancelGrace(sb.PromptGeneration(), testGrace) {
		t.Errorf("arming on an idle bridge must report false")
	}

	// Prompting, but no prompt context registered.
	if !sb.tryAcquireForPrompt() {
		t.Fatalf("acquire failed")
	}
	if sb.ArmCancelGrace(sb.PromptGeneration(), testGrace) {
		t.Errorf("arming with no registered prompt cancel must report false")
	}
}

// TestEndPromptCall_DisarmsTheTimer covers the prompt handler's own defer path.
func TestEndPromptCall_DisarmsTheTimer(t *testing.T) {
	// Same bubble-based negative assertion as above.
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		sb.ArmCancelGrace(gen, testGrace)
		sb.EndPromptCall()

		synctest.Sleep(4 * testGrace)
		if ctx.Err() != nil {
			t.Errorf("context cancelled after EndPromptCall disarmed the budget: %v", ctx.Err())
		}
	})
}

// A record still in its door holds no session: every forward refuses rather than dereferencing it, and the command
// layer sees no bridge.
func TestSharedBridge_BeforeItsDoorAttachesEveryForwardRefuses(t *testing.T) {
	sb := &sharedBridge{state: bridgeStarting, settled: newSessionSettle()}
	ctx := t.Context()
	if _, err := sb.Call(ctx, marotte.MethodPrompt, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Call before attach = %v, want ErrSessionClosed", err)
	}
	if _, _, err := sb.CallAt(ctx, marotte.MethodPrompt, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("CallAt before attach = %v, want ErrSessionClosed", err)
	}
	if err := sb.Notify(ctx, marotte.MethodCancel, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Notify before attach = %v, want ErrSessionClosed", err)
	}
	if err := sb.Respond(ctx, 1, nil, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Respond before attach = %v, want ErrSessionClosed", err)
	}
	if id := sb.SessionID(); id != "" {
		t.Errorf("SessionID before attach = %q, want empty", id)
	}

	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.bridge.mgr.insert("c1", sb)
	if got := (bridgeRole{coord: h.coord}).Bridge("c1"); got != nil {
		t.Errorf("Bridge for a record in its door = %v, want nil", got)
	}
	sb.attach(newFakeBridge())
	if got := (bridgeRole{coord: h.coord}).Bridge("c1"); got == nil {
		t.Error("Bridge once the door attached its session = nil, want the record")
	}
}
