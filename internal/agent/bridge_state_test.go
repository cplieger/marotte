package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
)

// A short grace: it is armed against a specific turn generation.
const testGrace = 20 * time.Millisecond

// newPromptingBridge returns a bridge holding the prompt slot with a registered prompt context.
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

// TestCancelPromptCall_CarriesNoCause pins that a wire interrupt keeps context.Canceled, so the turn concludes `interrupted`.
func TestCancelPromptCall_CarriesNoCause(t *testing.T) {
	sb, ctx, _ := newPromptingBridge(t)
	if !sb.cancelPromptCall() {
		t.Fatalf("cancelling an in-flight prompt must report true")
	}
	<-ctx.Done()
	if got := context.Cause(ctx); got != context.Canceled {
		t.Errorf("context.Cause = %v, want context.Canceled: an interrupt must carry no "+
			"sentinel, or it would conclude cancelled", got)
	}
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

// TestCancelPromptCall_TripsTheInFlightCall is the bridge half of an interruption; the cause is
// the turn record's (TestTurnRegistry_InterruptIsFirstWinsPerEpoch).
func TestCancelPromptCall_TripsTheInFlightCall(t *testing.T) {
	sb, ctx, _ := newPromptingBridge(t)

	if !sb.cancelPromptCall() {
		t.Fatal("cancelling an in-flight prompt call must be taken")
	}

	select {
	case <-ctx.Done():
	default:
		t.Error("the prompt context is still live; the blocked Call never returns and the slot stays held")
	}
}

// TestCancelPromptCall_RefusesWithNoCallInFlight pins that a late sentinel must not cancel a turn the user just started.
func TestCancelPromptCall_RefusesWithNoCallInFlight(t *testing.T) {
	t.Run("idle bridge", func(t *testing.T) {
		sb := &sharedBridge{}
		if sb.cancelPromptCall() {
			t.Error("an idle bridge accepted a cancel")
		}
	})

	t.Run("prompting but no prompt call registered", func(t *testing.T) {
		sb := &sharedBridge{}
		if !sb.tryAcquireForPrompt() {
			t.Fatal("fresh bridge must be acquirable")
		}
		// Between TryAcquireForPrompt and BeginPromptCall: a turn, nothing to cancel yet.
		if sb.cancelPromptCall() {
			t.Error("a cancel was taken with no prompt context to trip")
		}
	})

	t.Run("after the turn ended", func(t *testing.T) {
		sb, _, _ := newPromptingBridge(t)
		sb.EndPromptCall()
		if sb.cancelPromptCall() {
			t.Error("a cancel was taken after the prompt call ended")
		}
	})
}
