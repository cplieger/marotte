package agent

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
)

// clientAnswerBudget is how long a mutating handler waits on the engine before it answers still_applying: below
// clientRequestBudget, so the browser reads the answer instead of aborting. A var for tests.
var clientAnswerBudget = clientRequestBudget - 5*time.Second

// stillApplyingCode marks a 504 for a change the engine has not answered yet and may still apply.
const stillApplyingCode = "still_applying"

// engineMutation is one user-visible change made through an engine call.
type engineMutation struct {
	// call makes the change on ctx, detached from the request, and returns the answer it earned.
	call func(ctx context.Context) engineReply
	// changed broadcasts the refetch signal that settles the UI; nil sends none.
	changed func()
	// sent closes once call is committed to writing the change, with nothing left before the write that can block.
	// Until it does, nothing can apply, so the budget never answers still_applying and the call's own answer is
	// awaited; nil means call writes at once.
	sent <-chan struct{}
	verb string
}

// engineReply is a finished call: write answers the request, applied says the change landed, err is a failure
// to log when no request is left to answer.
type engineReply struct {
	err     error
	write   func(w http.ResponseWriter)
	applied bool
}

// answerEngineMutation runs m.call off the request and answers with its reply, or with 504 still_applying once
// clientAnswerBudget passes first after m.sent closed. m.changed runs when the call applied the change or answered after the 504,
// whatever it answered, so a list the UI re-reads settles the outcome either way.
func (lt *lifetime) answerEngineMutation(w http.ResponseWriter, r *http.Request, m engineMutation) {
	var (
		mu        sync.Mutex
		abandoned bool
	)
	done := make(chan engineReply, 1)
	ctx, cancel := lt.TurnContext(r.Context())
	if !lt.goUnlessDraining(func() {
		defer cancel()
		rep := m.call(ctx)
		// Under mu with the abandon, so a reply is either written by the handler or settled here, never neither.
		mu.Lock()
		late := abandoned
		done <- rep
		mu.Unlock()
		switch {
		case late && rep.err != nil:
			slog.Warn("an engine change failed after its request was told it is still applying",
				"verb", m.verb, "applied", rep.applied, "error", rep.err)
		case late:
			slog.Info("an engine change answered after its request was told it is still applying",
				"verb", m.verb, "applied", rep.applied)
		}
		if m.changed != nil && (rep.applied || late) {
			m.changed()
		}
	}) {
		cancel()
		writeShuttingDown(w)
		return
	}
	rep, answered, left := awaitReply(r.Context(), done, m.sent)
	if answered {
		rep.write(w)
		return
	}
	mu.Lock()
	select {
	case rep := <-done:
		mu.Unlock()
		rep.write(w)
		return
	default:
	}
	abandoned = true
	mu.Unlock()
	if left {
		slog.Info("the client left before an engine change answered; it may still apply", "verb", m.verb)
	} else {
		slog.Warn("an engine change has not answered in time; telling the client it is still applying", "verb", m.verb)
	}
	httpserver.WriteJSONStatus(w, http.StatusGatewayTimeout, httpreply.ErrorJSONWithCode(
		"kiro-cli has not answered yet and may still apply the change; the list updates when it does",
		stillApplyingCode,
	))
}

// awaitReply waits for the call's reply until clientAnswerBudget has passed with the change written (sent closed or
// nil), or until the client leaves (left); answered is false when the reply did not come first.
func awaitReply(ctx context.Context, done <-chan engineReply, sent <-chan struct{}) (rep engineReply, answered, left bool) {
	timer := time.NewTimer(clientAnswerBudget)
	defer timer.Stop()
	budget, expired := timer.C, false
	for {
		select {
		case rep := <-done:
			return rep, true, false
		case <-ctx.Done():
			return engineReply{}, false, true
		case <-budget:
			budget, expired = nil, true
		case <-sent:
			sent = nil
		}
		if expired && sent == nil {
			return engineReply{}, false, false
		}
	}
}
