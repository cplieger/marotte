package agent

// What may be done to one run. One server-side table: only the server knows whether this process
// still holds the run. Retry is offered on agent-parented runs too: kiro-cli's restore pass skips
// an aborted run, so nothing else recovers it.

import (
	"context"
	"slices"
	"strconv"

	"github.com/cplieger/marotte/internal/marotte"
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

// maxIterationsVerbs is the paused row for a repeat at its cap. Neither new verb is
// hosted-only: both KAS handlers load an unregistered run from disk.
var maxIterationsVerbs = []string{verbExtend, verbFinishLoop, verbCancel}

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

// hostedOnlyVerbs need the process holding the run's registry entry: KAS's pause throws for a run
// not registered in memory. Resume and Retry make the launching session live first (acquireHost).
var hostedOnlyVerbs = []string{verbPause}

// runAffordance answers what may be done to one run.
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

// permits reports whether the run offers this verb.
func (a *runAffordance) permits(verb string) bool {
	return slices.Contains(a.Verbs, verb)
}

// refusal is the sentence for a verb this run does not offer, or "" when the
// affordance has nothing to add to the run's own status.
func (a *runAffordance) refusal(verb string) string {
	return a.Refused[verb]
}

// runFacts is what an affordance is decided from: the status the caller read, the
// chat owning the run's parent session, and hosting off the bridge map.
type runFacts struct {
	// status is the run's own status, as `inspect` reports it.
	status string
	// parentChat is the chat whose agent launched the run, "" when parentless.
	parentChat marotte.ChatID
	// parentName is that chat's display name, for the refusal sentence; empty for an unnamed chat.
	parentName  string
	pauseKind   string
	pauseNodeID string
	// hosted reports whether a live bridge in this process holds the run's registry entry.
	hosted bool
	// pausePending is a pause already asked for, which a second Pause cannot add to.
	pausePending bool
}

// affordanceOf answers what may be done to one run. Pure, so the table tests over (status × parent × hosted).
func affordanceOf(f *runFacts) runAffordance {
	byStatus, known := runStatusVerbs[marotte.RunStatus(f.status)]
	if !known {
		return runAffordance{}
	}
	out := runAffordance{Verbs: make([]string, 0, len(byStatus))}
	if marotte.RunStatus(f.status) == marotte.RunStatusPaused &&
		f.pauseKind == pauseKindMaxIterations && f.pauseNodeID != "" {
		out.PauseNodeID = f.pauseNodeID
		byStatus = maxIterationsVerbs
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
	switch {
	case verb == verbPause && f.pausePending:
		return pausePendingRefusal
	case slices.Contains(hostedOnlyVerbs, verb) && !f.hosted:
		return notHostedRefusal(f.parentChat, f.parentName)
	}
	return ""
}

const pausePendingRefusal = "A pause is already requested; the run stops at the end of its current step."

// notHostedRefusal is Pause's sentence when nothing here holds the run: it names the launching
// chat (opening it respawns the bridge), or for a parentless run the verb that still works.
func notHostedRefusal(parentChat marotte.ChatID, parentName string) string {
	if parentChat != "" {
		return "This run is driven by an agent in " + chatLabel(parentChat, parentName) +
			", and that conversation is not open here, so it cannot be paused from this page. " +
			"Open that chat to bring the run back within reach."
	}
	return "This run has no live engine on this server, so it cannot be paused from here. " +
		"Cancel still works."
}

// chatLabel names a chat for a sentence: its name, else its id.
func chatLabel(chatID marotte.ChatID, name string) string {
	if name == "" {
		return "chat " + strconv.Quote(string(chatID))
	}
	return strconv.Quote(name)
}

// affordance resolves the facts and answers what may be done. status is passed in so one decision
// rests on one `inspect` read; it costs one `workflow/list` (originOf).
func (rs *Runs) affordance(ctx context.Context, workflowID, status string) *runAffordance {
	o := rs.originOf(ctx, workflowID)
	f := runFacts{
		status: status, parentChat: o.parent.chat, parentName: o.chatName,
		pauseKind: o.pauseKind, pauseNodeID: o.pauseNodeID, pausePending: o.pausePending,
	}
	f.hosted = rs.bridges.get(runChatID(workflowID)) != nil ||
		(f.parentChat != "" && rs.bridges.get(f.parentChat) != nil)
	aff := affordanceOf(&f)
	aff.origin = o
	return &aff
}

// chatForSession resolves a run's parent session to its chat from the store alone, reporting
// whether every chat was read. Matched against the whole session chain, and no live bridge
// required: a closed chat is exactly when the reader needs its name.
func (rs *Runs) chatForSession(
	ctx context.Context, sessionID string,
) (chatID marotte.ChatID, name string, complete bool) {
	if sessionID == "" {
		return "", "", true
	}
	// Indexed: marotte.ChatHeader is 304 bytes (gocritic rangeValCopy).
	headers, complete := rs.chats.ListComplete(ctx)
	for i := range headers {
		if slices.Contains(headers[i].SessionChain(), sessionID) {
			return marotte.ChatID(headers[i].ID), headers[i].Name, complete
		}
	}
	return "", "", complete
}
