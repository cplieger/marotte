package agent

// What may be done to one run: one server-side table over KAS's status and the run's pause. Retry is offered on
// agent-parented runs too: kiro-cli's restore pass skips an aborted run, so nothing else recovers it.

import (
	"context"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// The run-control verb names, shared by this table, the routes and the client's label map.
const (
	verbPause  = "pause"
	verbResume = "resume"
	verbCancel = "cancel"
	verbRetry  = "retry"
	// verbExtend and verbFinishLoop replace resume on a repeat paused at its cap, where resume re-parks.
	verbExtend     = "extend"
	verbFinishLoop = "finish_loop"
)

const pauseKindMaxIterations = "maxIterations"

// maxIterationsVerbs is the paused row for a repeat at its cap, where resume re-parks.
var maxIterationsVerbs = []string{verbExtend, verbFinishLoop, verbCancel}

// askingVerbs is the paused row while every paused step asks: KAS's resume leaves each step's
// need_input signal and re-parks it at once, so only an answer moves the run on.
var askingVerbs = []string{verbCancel}

// runStatusVerbs maps KAS's WorkflowStatusSchema to accepted verbs, mirroring KAS. An unknown
// status is absent, so a future status degrades to read-only.
var runStatusVerbs = map[marotte.RunStatus][]string{
	marotte.RunStatusRunning: {verbPause, verbCancel},
	marotte.RunStatusPaused:  {verbResume, verbCancel},
	// A completed run is a record.
	marotte.RunStatusCompleted: {},
	// Retry resets failed and aborted nodes plus ancestors, keeping completed work.
	marotte.RunStatusFailed:  {verbRetry},
	marotte.RunStatusAborted: {verbRetry},
	// A cancel writes its target verbatim, so `cancelled` can come from another client.
	marotte.RunStatusCancelled: {},
}

type runAffordance struct {
	// Refused maps an unoffered verb to one sentence, only where its absence needs explaining.
	Refused map[string]string
	// origin is the gate's read of the run's origin, which Retry reuses: two reads can disagree.
	origin runOrigin
	// PauseNodeID is the repeat a maxIterations pause stopped at, "" otherwise; the
	// extend and finish_loop verbs address it.
	PauseNodeID string
	// Verbs are the offered controls, in the order a row presents them.
	Verbs []string
}

func (a *runAffordance) permits(verb string) bool {
	return slices.Contains(a.Verbs, verb)
}

// refusal is the sentence for a verb this run does not offer, or "" when the
// affordance has nothing to add to the run's own status.
func (a *runAffordance) refusal(verb string) string {
	return a.Refused[verb]
}

// runFacts is what an affordance is decided from: the status the caller read and the run's pause.
type runFacts struct {
	// status is the run's own status, as `inspect` reports it.
	status      string
	pauseKind   string
	pauseNodeID string
	// pausePending is a pause already asked for, which a second Pause cannot add to.
	pausePending bool
	// asking is workflow.PausedOnAsks of the run's newest read.
	asking bool
}

// Pure, so the table tests over the status and the pause.
func affordanceOf(f *runFacts) runAffordance {
	byStatus, known := runStatusVerbs[marotte.RunStatus(f.status)]
	if !known {
		return runAffordance{}
	}
	out := runAffordance{Verbs: make([]string, 0, len(byStatus))}
	paused := marotte.RunStatus(f.status) == marotte.RunStatusPaused
	switch {
	case paused && f.pauseKind == pauseKindMaxIterations && f.pauseNodeID != "":
		out.PauseNodeID = f.pauseNodeID
		byStatus = maxIterationsVerbs
	case paused && f.asking:
		byStatus = askingVerbs
	}
	for _, verb := range byStatus {
		why := f.refusal(verb)
		if why == "" {
			out.Verbs = append(out.Verbs, verb)
			continue
		}
		if out.Refused == nil {
			out.Refused = map[string]string{}
		}
		out.Refused[verb] = why
	}
	return out
}

// refusal is the sentence a withheld verb gets, "" when the verb is offered.
func (f *runFacts) refusal(verb string) string {
	if verb == verbPause && f.pausePending {
		return pausePendingRefusal
	}
	return ""
}

const pausePendingRefusal = "A pause is already requested; the run stops at the end of its current step."

// affordance resolves the facts and answers what may be done. status is passed in so one decision
// rests on one `inspect` read; it costs one `workflow/list` (originOf).
func (rs *Runs) affordance(ctx context.Context, workflowID, status string) *runAffordance {
	o := rs.originOf(ctx, workflowID)
	f := runFacts{
		status: status, pauseKind: o.pauseKind, pauseNodeID: o.pauseNodeID, pausePending: o.pausePending,
	}
	f.asking = rs.pausedOnAsks(workflowID)
	aff := affordanceOf(&f)
	aff.origin = o
	return &aff
}

// pausedOnAsks reads the run's ask cell: false with no cell, or before its first read.
func (rs *Runs) pausedOnAsks(workflowID string) bool {
	c := rs.asks.get(workflowID)
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return workflow.PausedOnAsks(c.reply)
}

// chatForSession resolves a run's parent session to its chat from the store alone, reporting
// whether every chat was read. Matched against the whole session chain, and no resident session
// required.
func (rs *Runs) chatForSession(
	ctx context.Context, sessionID string,
) (chatID marotte.ChatID, complete bool) {
	if sessionID == "" {
		return "", true
	}
	// Indexed: marotte.ChatHeader is 304 bytes (gocritic rangeValCopy).
	headers, complete := rs.chats.ListComplete(ctx)
	for i := range headers {
		if slices.Contains(headers[i].SessionChain(), sessionID) {
			return marotte.ChatID(headers[i].ID), complete
		}
	}
	return "", complete
}
