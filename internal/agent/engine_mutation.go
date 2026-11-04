package agent

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v4"
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
	verb    string
}

// engineReply is a finished call: write answers the request, applied says the change landed, err is a failure
// to log when no request is left to answer.
type engineReply struct {
	err     error
	write   func(w http.ResponseWriter)
	applied bool
}

// answerEngineMutation runs m.call off the request and answers with its reply, or with 504 still_applying once
// clientAnswerBudget passes first. m.changed runs when the call applied the change or answered after the 504,
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
	timer := time.NewTimer(clientAnswerBudget)
	defer timer.Stop()
	select {
	case rep := <-done:
		rep.write(w)
		return
	case <-timer.C:
	case <-r.Context().Done():
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
	slog.Warn("an engine change has not answered in time; telling the client it is still applying", "verb", m.verb)
	webhttp.WriteJSONStatus(w, http.StatusGatewayTimeout, httpreply.ErrorJSONWithCode(
		"kiro-cli has not answered yet and may still apply the change; the list updates when it does",
		stillApplyingCode,
	))
}
