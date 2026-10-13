package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Inspect is one decoded `_kiro/workflow/inspect` reply. OK is false for a reply with no readable
// state, which a reader must treat as "unknown", never as "nothing is waiting".
type Inspect struct {
	State *State
	OK    bool
}

// ErrOtherRun means an inspect reply described a run other than the one asked for.
var ErrOtherRun = errors.New("the inspect reply names another run")

// DecodeInspect decodes the reply to an inspect of workflowID; the returned State always names
// workflowID. A syntax error, a type error in any decoded field, a missing state, or a reply naming
// another run, none, or two disagreeing ones answers a not-OK Inspect and the reason.
func DecodeInspect(workflowID string, raw json.RawMessage) (Inspect, error) {
	var res InspectResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return Inspect{}, fmt.Errorf("decode inspect: %w", err)
	}
	if res.State == nil {
		return Inspect{}, errors.New("decode inspect: the reply has no state")
	}
	// KAS names the run on the reply and on its state; each it names must be the run asked for.
	for _, got := range []string{res.State.WorkflowID, res.WorkflowID} {
		if got != "" && got != workflowID {
			return Inspect{}, fmt.Errorf("%w: asked %q, got %q", ErrOtherRun, workflowID, got)
		}
	}
	if res.State.WorkflowID == "" && res.WorkflowID == "" {
		return Inspect{}, fmt.Errorf("%w: asked %q, the reply names none", ErrOtherRun, workflowID)
	}
	// Every ask's identity is built from the state's id.
	res.State.WorkflowID = workflowID
	return Inspect{State: res.State, OK: true}, nil
}

const (
	statusRunning = "running"
	statusPaused  = "paused"
	// needInput is KAS's completionSignal for a step waiting on a person.
	needInput = "need_input"
)

// StatusUpdateTarget mirrors KAS's `update` resolver: the first running step node in pre-order,
// else the first paused one. False when the run holds neither.
func StatusUpdateTarget(s *State) (StepSession, bool) {
	var paused *StepSession
	for _, st := range Steps(s) {
		switch st.Status {
		case statusRunning:
			return st, true
		case statusPaused:
			if paused == nil {
				paused = &st
			}
		}
	}
	if paused == nil {
		return StepSession{}, false
	}
	return *paused, true
}

// visit is one node of a state tree with its wire path.
type visit struct {
	n    *node
	path []string
}

// nodes walks every node of a run's tree in pre-order, the order Steps uses.
func nodes(s *State) []visit {
	if s == nil || s.Root == nil {
		return nil
	}
	var out []visit
	var walkAll func(n, parent *node, trail []string)
	walkAll = func(n, parent *node, trail []string) {
		path := make([]string, 0, len(trail)+1)
		path = append(path, trail...)
		path = append(path, pathSegment(n, parent))
		out = append(out, visit{n: n, path: path})
		for i := range n.Children {
			walkAll(&n.Children[i], n, path)
		}
	}
	walkAll(s.Root, nil, nil)
	return out
}

// waits reports whether a node satisfies W1, the one wait predicate: paused with a session, and
// either signalled need_input or, with no signal, awaiting user input. A running node with the
// signal is a turn still in progress, and an aborted one a cancelled run.
func (n *node) waits() bool {
	if n.Status != statusPaused || n.SessionID == "" {
		return false
	}
	switch n.CompletionSignal {
	case needInput:
		return true
	case "":
		return n.AwaitingUserInput != nil && *n.AwaitingUserInput
	}
	return false
}
