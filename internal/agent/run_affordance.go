package agent

// What may be done to one run: one server-side table over KAS's status and the run's pause, decided from one
// inspect read so the verbs and the state a client shows come from the same answer. Retry is offered on
// agent-parented runs too: kiro-cli's restore pass skips an aborted run, so nothing else recovers it.

import (
	"context"
	"encoding/json"
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

// askingVerbs is the paused row while every paused step asks, or while that is unknown: a known ask
// re-parks on resume, and an unreadable tree cannot prove that resume will not re-park.
var askingVerbs = []string{verbCancel}

// askState is whether a run's read has every paused step waiting on a person (workflow.PausedOnAsks).
type askState int

const (
	// asksNone is a read where some paused leaf waits on nobody, or where none is paused.
	asksNone askState = iota
	asksAll
	// asksUnread is a read whose tree did not decode, so whether anyone asks is unknown.
	asksUnread
)

// asksOf is the ask state of one decoded reply; a not-OK reply is one whose tree did not decode.
func asksOf(tree workflow.Inspect) askState {
	switch {
	case !tree.OK:
		return asksUnread
	case workflow.PausedOnAsks(tree):
		return asksAll
	default:
		return asksNone
	}
}

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
	// PauseNodeID is the repeat a maxIterations pause stopped at, "" otherwise; the
	// extend and finish_loop verbs address it.
	PauseNodeID string
	// status is the gate's read of the run's status, the ending Retry records for a run whose end marotte missed.
	status marotte.RunStatus
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
	// asks is the ask state of the run's newest read.
	asks askState
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
	case paused && f.asks != asksNone:
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

// runHead is the part of an inspect state the verbs are decided from. KAS's `workflow/list` row derives
// its pausePending, pauseKind and pauseNodeId from these same fields, and costs a read of every run.
type runHead struct {
	Status       string
	PauseKind    string
	PauseNodeID  string
	PausePending bool
}

// headOf decodes the state's head from a raw inspect reply. Only the status can fail it: a pause field of
// another shape leaves its fact unset, so a kiro-cli drift there still offers the status's verbs (Cancel).
func headOf(raw json.RawMessage) (runHead, error) {
	var res struct {
		State struct {
			Status       string          `json:"status"`
			PausePending json.RawMessage `json:"pausePending"`
			Pause        json.RawMessage `json:"pause"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return runHead{}, err
	}
	st := &res.State
	head := runHead{Status: st.Status}
	switch string(st.PausePending) {
	case "", "null", "false":
	default:
		head.PausePending = true
	}
	var pause struct {
		Kind   string `json:"kind"`
		NodeID string `json:"nodeId"`
	}
	if len(st.Pause) > 0 && json.Unmarshal(st.Pause, &pause) == nil {
		head.PauseKind, head.PauseNodeID = pause.Kind, pause.NodeID
	}
	return head, nil
}

// affordance answers what may be done from one inspect read's head and the run's ask cell as its newest
// run read left it (handleRun refreshes it through openRun just before).
func (rs *Runs) affordance(workflowID string, head *runHead) *runAffordance {
	f := runFacts{
		status: head.Status, pausePending: head.PausePending, asks: rs.cellAsks(workflowID),
		pauseKind: head.PauseKind, pauseNodeID: head.PauseNodeID,
	}
	aff := affordanceOf(&f)
	aff.status = marotte.RunStatus(head.Status)
	return &aff
}

// controls is the affordance as the wire answers it, on the run read and on GET /api/runs/{id}/controls.
// Verbs is never null, so an unknown status reads as nothing offered.
func (rs *Runs) controls(workflowID string, head *runHead) marotte.RunControlsResponse {
	aff := rs.affordance(workflowID, head)
	return marotte.RunControlsResponse{
		Verbs: append([]string{}, aff.Verbs...), Refused: aff.Refused, PauseNodeID: aff.PauseNodeID,
	}
}

// cellAsks is the ask state of the run's ask cell: asksNone with no cell or before its first read, and
// asksUnread while its newest read failed.
func (rs *Runs) cellAsks(workflowID string) askState {
	c := rs.asks.get(workflowID)
	if c == nil {
		return asksNone
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.readFailed:
		return asksUnread
	case workflow.PausedOnAsks(c.reply):
		return asksAll
	default:
		return asksNone
	}
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
	scan := rs.chats.SessionOwners(ctx)
	if owners := scan.Owners.Of(sessionID); len(owners) > 0 {
		return owners[0], scan.Complete
	}
	return "", scan.Complete
}
