package command

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A client that stops waiting is told the change is still applying, and the change keeps running on a context its
// cancel does not reach.
func TestDetachedChange_AClientThatLeavesDoesNotCancelTheChange(t *testing.T) {
	c := detachedChange{lifecycle: newBenchDeps(), answerBudget: time.Minute}
	started := make(chan struct{})
	proceed := make(chan struct{})
	ctxErr := make(chan error, 1)
	h := c.wrap(marotte.CmdSetMode, func(ctx context.Context, _ *marotte.ClientCommand) (any, error) {
		close(started)
		<-proceed
		ctxErr <- ctx.Err()
		return nil, nil
	})
	reqCtx, leave := context.WithCancel(t.Context())
	answered := make(chan error, 1)
	go func() {
		_, err := h(reqCtx, &marotte.ClientCommand{Type: marotte.CmdSetMode, ChatID: "c1"})
		answered <- err
	}()
	<-started
	leave()
	select {
	case err := <-answered:
		if status, reason, _ := StatusOf(err); status != http.StatusGatewayTimeout || reason != reasonStillApplying {
			t.Errorf("answer after the client left = %v (%d %q), want 504 %s", err, status, reason, reasonStillApplying)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wrapped handler did not answer once the client left")
	}
	close(proceed)
	if err := <-ctxErr; err != nil {
		t.Errorf("the change's context ended with the client's request: %v", err)
	}
}

// A change another command applies through Dispatcher.Run runs on its caller's context and answers its own outcome.
func TestDispatcherRun_AChangeRunsInline(t *testing.T) {
	d := New()
	d.changes = detachedChange{lifecycle: newBenchDeps(), answerBudget: time.Minute}
	// A cancelled caller tells the two apart: a detached change's context outlives it.
	callerCtx, cancel := context.WithCancel(t.Context())
	cancel()
	refused := StatusError(http.StatusBadGateway, context.DeadlineExceeded)
	var ranOn error
	d.RegisterChange(marotte.CmdSetEffort, func(ctx context.Context, _ *marotte.ClientCommand) (any, error) {
		ranOn = ctx.Err()
		return nil, refused
	})
	if err := d.Run(callerCtx, &marotte.ClientCommand{Type: marotte.CmdSetEffort}); !errors.Is(err, refused) {
		t.Errorf("Run = %v, want the change's own outcome %v", err, refused)
	}
	if !errors.Is(ranOn, context.Canceled) {
		t.Errorf("the change's context err = %v, want its caller's cancel", ranOn)
	}
}

// Unwired, the handler runs on the request as it is.
func TestDetachedChange_UnwiredRunsInline(t *testing.T) {
	type key struct{}
	reqCtx := context.WithValue(t.Context(), key{}, "request")
	for name, c := range map[string]detachedChange{
		"no lifecycle": {answerBudget: time.Minute},
		"no budget":    {lifecycle: newBenchDeps()},
	} {
		t.Run(name, func(t *testing.T) {
			var got context.Context
			h := c.wrap(marotte.CmdSetMode, func(ctx context.Context, _ *marotte.ClientCommand) (any, error) {
				got = ctx
				return nil, nil
			})
			if _, err := h(reqCtx, &marotte.ClientCommand{}); err != nil {
				t.Fatalf("inline handler = %v", err)
			}
			if got != reqCtx {
				t.Error("an unwired change ran on another context than its request's")
			}
		})
	}
}
