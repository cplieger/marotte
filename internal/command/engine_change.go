package command

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// engineChangeBudget bounds one session change's engine call on a kiro-cli that is not stalled, as the agent's
// launchTimeout bounds a run launch on the same engine; CallBudget extends it through a stall.
const engineChangeBudget = 120 * time.Second

// reasonStillApplying marks a 504 for a change kiro-cli has not answered yet and may still apply.
const reasonStillApplying = "still_applying"

var errStillApplying = errors.New("kiro-cli has not answered yet and may still apply the change; the chat updates when it does")

// detachedChange runs a command whose engine call can outlast its request.
type detachedChange struct {
	lifecycle lifecycleAccess
	// answerBudget is how long the request waits before it answers still_applying.
	answerBudget time.Duration
}

type inProcessKey struct{}

// inProcess marks ctx as a command applied inside another's handler (Dispatcher.Run).
func inProcess(ctx context.Context) context.Context {
	return context.WithValue(ctx, inProcessKey{}, true)
}

// wrap runs h off the request, so a client that stops waiting cannot cancel an engine call kiro-cli may still
// apply, and answers h's outcome or, once answerBudget passes first, 504 still_applying. h's late outcome is its own:
// on success it persists and broadcasts as on time. A nil lifecycle or a zero budget runs h inline, as does an
// in-process call, whose caller needs the outcome to apply its own.
func (c detachedChange) wrap(verb marotte.CommandType, h Handler) Handler {
	if c.lifecycle == nil || c.answerBudget <= 0 {
		return h
	}
	return func(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
		if ctx.Value(inProcessKey{}) != nil {
			return h(ctx, cmd)
		}
		type outcome struct {
			body any
			err  error
		}
		var (
			mu        sync.Mutex
			abandoned bool
		)
		done := make(chan outcome, 1)
		// Before the answer, so a shutdown waits for the change.
		c.lifecycle.InflightAdd(1)
		dctx, cancel := c.lifecycle.TurnContext(ctx)
		go func() {
			defer c.lifecycle.InflightDone()
			defer cancel()
			body, err := h(dctx, cmd)
			// Under mu with the abandon, so an outcome is answered or logged here, never neither.
			mu.Lock()
			late := abandoned
			done <- outcome{body: body, err: err}
			mu.Unlock()
			switch {
			case late && err != nil:
				slog.Warn("a session change failed after its request was told it is still applying",
					"verb", verb, "chat", cmd.ChatID, keyError, err)
			case late:
				slog.Info("a session change answered after its request was told it is still applying",
					"verb", verb, "chat", cmd.ChatID)
			}
		}()
		timer := time.NewTimer(c.answerBudget)
		defer timer.Stop()
		left := false
		select {
		case o := <-done:
			return o.body, o.err
		case <-timer.C:
		case <-ctx.Done():
			left = true
		}
		mu.Lock()
		defer mu.Unlock()
		select {
		case o := <-done:
			return o.body, o.err
		default:
		}
		abandoned = true
		if left {
			slog.Info("the client left before a session change answered; it may still apply",
				"verb", verb, "chat", cmd.ChatID)
		} else {
			slog.Warn("a session change has not answered in time; telling the client it is still applying",
				"verb", verb, "chat", cmd.ChatID)
		}
		return nil, StatusErrorReason(http.StatusGatewayTimeout, reasonStillApplying, errStillApplying)
	}
}
