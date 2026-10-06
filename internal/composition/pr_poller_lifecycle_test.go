package composition

// Production calls Build with context.Background(), so the poller must stop through App.Shutdown,
// the real shutdown owner.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunBackground_StopWaitsForTheGoroutine is the mechanism: stop cancels, and
// it does not return until the loop has. The wait is the part that matters — an
// unwaited cancel lets a sweep already inside a forge subprocess keep running
// after Shutdown returned, which is the leak in a different costume.
func TestRunBackground_StopWaitsForTheGoroutine(t *testing.T) {
	var exited atomic.Bool
	started := make(chan struct{})
	stop := runBackground(context.Background(), "test loop", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		exited.Store(true)
	})
	<-started
	if exited.Load() {
		t.Fatal("the loop exited before it was asked to")
	}
	stop()
	if !exited.Load() {
		t.Error("stop returned while the loop was still running: the cancel is not waited on")
	}
}

// TestApp_ShutdownStopsThePRStatusPoller drives App.Shutdown — the function
// production calls — rather than a cancel the test created, so it proves
// App.Shutdown owns the poller's cancel.
func TestApp_ShutdownStopsThePRStatusPoller(t *testing.T) {
	var exited atomic.Bool
	started := make(chan struct{})
	app := &App{
		stopPRPoller: runBackground(context.Background(), "pr status poller",
			func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				exited.Store(true)
			}),
	}
	<-started

	app.Shutdown()

	if !exited.Load() {
		t.Error("the PR-status poller was still running after App.Shutdown returned; " +
			"it would keep consulting the push service the same Shutdown just closed")
	}
}

// TestApp_ShutdownSurvivesAnAbsentPoller keeps the nil tolerance honest in the
// direction that matters: a degraded Build (tools is already nil on the
// root-integrity path) must still get its ordered teardown rather than a panic.
func TestApp_ShutdownSurvivesAnAbsentPoller(t *testing.T) {
	(&App{}).Shutdown()
}
