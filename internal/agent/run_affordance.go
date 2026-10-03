package agent

// What may be done to one run, and why not for the rest.
//
// ONE table, and it is the server's: the client's status→verbs map could not see
// the third input, whether anything in this process still holds the run, so it
// could draw a button whose only possible outcome was a refusal. Retry is offered
// on an agent-parented run too — kiro-cli's own restore pass considers only a
// `running` or `paused` run, so an aborted one has no recovery path in either
// product and withholding retry left it unreachable.

import (
	"context"
	"slices"
	"strconv"

	"github.com/cplieger/marotte/internal/marotte"
)

// The run-control verb names. Named because this table, the route handlers and
// the client's label map all key on the same words.
const (
	verbPause  = "pause"
	verbResume = "resume"
	verbCancel = "cancel"
	verbRetry  = "retry"
)

// runStatusVerbs maps KAS's WorkflowStatusSchema to the verbs each status accepts,
// mirroring KAS itself: retry throws for anything non-terminal, pause sets a flag
// that means nothing once a run has stopped, resume re-drives a paused one. Cancel
// is on both live statuses and neither terminal one. An UNKNOWN status is absent
// rather than mapped to an empty list, so a future KAS status degrades to a
// read-only view instead of a wrong control.
var runStatusVerbs = map[marotte.RunStatus][]string{
	marotte.RunStatusRunning: {verbPause, verbCancel},
	marotte.RunStatusPaused:  {verbResume, verbCancel},
	// A completed run is a record: nothing to retry, nothing to stop.
	marotte.RunStatusCompleted: {},
	// Retry resets the failed and aborted nodes plus their ancestors, so completed
	// work survives — unlike relaunching, which starts at step one.
	marotte.RunStatusFailed:  {verbRetry},
	marotte.RunStatusAborted: {verbRetry},
	// A cancel writes its target status verbatim, so `cancelled` is reachable from
	// another client of the workspace and is a record like `completed`.
	marotte.RunStatusCancelled: {},
}

// hostedOnlyVerbs need the process that holds the run's registry entry: KAS's pause
// reaches `registry.require`, which throws for a run not in the live in-memory
// registry. Resume and Retry are not here, because a verb that finds nothing holding
// the run makes its launching session live first (acquireHost).
var hostedOnlyVerbs = []string{verbPause}

// runAffordance answers what may be done to one run.
type runAffordance struct {
	// Refused maps a verb this run does not offer to one sentence for the reader,
	// and carries only a verb whose absence would otherwise be unexplained.
	Refused map[string]string
	// origin is the gate's read of where the run came from, which Retry routes on
	// rather than reading again: two reads can disagree.
	origin runOrigin
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
	// parentName is that chat's display name, for the refusal sentence. Empty for
	// an unnamed chat, which the sentence then omits rather than quoting nothing.
	parentName string
	// hosted reports whether some live bridge in this process holds the run's
	// registry entry.
	hosted bool
}

// affordanceOf answers what may be done to one run. Pure, so the table is
// testable over (status × parent × hosted) without a bridge or an RPC.
func affordanceOf(f runFacts) runAffordance {
	byStatus, known := runStatusVerbs[marotte.RunStatus(f.status)]
	if !known {
		return runAffordance{}
	}
	out := runAffordance{Verbs: make([]string, 0, len(byStatus))}
	for _, verb := range byStatus {
		if slices.Contains(hostedOnlyVerbs, verb) && !f.hosted {
			if out.Refused == nil {
				out.Refused = map[string]string{}
			}
			out.Refused[verb] = notHostedRefusal(f.parentChat, f.parentName)
			continue
		}
		out.Verbs = append(out.Verbs, verb)
	}
	return out
}

// notHostedRefusal is the sentence Pause gets when nothing in this process holds the
// run. It names the launching chat when there is one, because that is the reader's
// remedy: opening that chat respawns its bridge. A parentless run has no such door,
// so its sentence says which verb still works.
func notHostedRefusal(parentChat marotte.ChatID, parentName string) string {
	if parentChat != "" {
		return "This run is driven by an agent in " + chatLabel(parentChat, parentName) +
			", and that conversation is not open here, so it cannot be paused from this page. " +
			"Open that chat to bring the run back within reach."
	}
	return "This run has no live engine on this server, so it cannot be paused from here. " +
		"Cancel still works."
}

// chatLabel names a chat for a sentence a person reads: its name when it has one,
// its id otherwise, because quoting an empty name names nothing.
func chatLabel(chatID marotte.ChatID, name string) string {
	if name == "" {
		return "chat " + strconv.Quote(string(chatID))
	}
	return strconv.Quote(name)
}

// affordance resolves the facts and answers what may be done to the run.
//
// status is passed in rather than read here: every caller has already made that
// `inspect` call, and one decision must rest on one status read, not two that can
// disagree. Costs ONE out-of-process read, originOf's `workflow/list`, which
// carries both the parent session and the recipe.
func (rs *Runs) affordance(ctx context.Context, workflowID, status string) *runAffordance {
	o := rs.originOf(ctx, workflowID)
	f := runFacts{status: status, parentChat: o.parent.chat, parentName: o.chatName}
	f.hosted = rs.bridges.get(runChatID(workflowID)) != nil ||
		(f.parentChat != "" && rs.bridges.get(f.parentChat) != nil)
	aff := affordanceOf(f)
	aff.origin = o
	return &aff
}

// chatForSession resolves a run's parent SESSION to the chat that owns it, from the
// chat store alone, and reports whether the scan read every chat: an unowned answer
// from an incomplete scan proves nothing.
//
// Matched against the whole session CHAIN, not the current id: a chat changes
// session on a failed session/load, a model-switch fallback and empty-turn
// recovery. Unlike hostBridgeChat this does NOT require a live bridge — a closed
// chat is exactly when the reader needs to be told which one to open.
func (rs *Runs) chatForSession(
	ctx context.Context, sessionID string,
) (chatID marotte.ChatID, name string, complete bool) {
	if sessionID == "" {
		return "", "", true
	}
	// Indexed: marotte.ChatHeader is 304 bytes, which gocritic's rangeValCopy flags.
	headers, complete := rs.chats.ListComplete(ctx)
	for i := range headers {
		if slices.Contains(headers[i].SessionChain(), sessionID) {
			return marotte.ChatID(headers[i].ID), headers[i].Name, complete
		}
	}
	return "", "", complete
}
