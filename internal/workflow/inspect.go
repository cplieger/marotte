package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Inspect is one decoded `_kiro/workflow/inspect` reply. OK is false for a reply with no readable
// state, which a reader must treat as "unknown", never as "nothing is waiting". Pending counts the
// top-level steps of the queued plan revision: 0 when none is queued, -1 when the reply's
// pendingSteps is not an array.
type Inspect struct {
	State   *State
	Pending int
	OK      bool
}

// errOtherRun means an inspect reply described a run other than the one asked for.
var errOtherRun = errors.New("the inspect reply names another run")

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
			return Inspect{}, fmt.Errorf("%w: asked %q, got %q", errOtherRun, workflowID, got)
		}
	}
	if res.State.WorkflowID == "" && res.WorkflowID == "" {
		return Inspect{}, fmt.Errorf("%w: asked %q, the reply names none", errOtherRun, workflowID)
	}
	// Every ask's identity is built from the state's id.
	res.State.WorkflowID = workflowID
	return Inspect{State: res.State, Pending: pendingCount(res.PendingSteps), OK: true}, nil
}

func pendingCount(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var steps []json.RawMessage
	if err := json.Unmarshal(raw, &steps); err != nil {
		return -1
	}
	return len(steps)
}

const (
	statusRunning   = "running"
	statusPaused    = "paused"
	statusCompleted = "completed"
	// needInput is KAS's completionSignal for a step waiting on a person.
	needInput     = "need_input"
	signalSuccess = "success"
)

// visit is one node of a state tree with its wire path.
type visit struct {
	n    *node
	path []string
}

// nodes walks every node of a run's tree in pre-order. Each level allocates its path at EXACT
// capacity, or a child's append writes into a sibling's backing array.
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

// StepRecord is KAS's own record of how one step node stands: its status and completion signal, verbatim.
type StepRecord struct {
	Status string
	// CompletionSignal is "" when the step signalled nothing.
	CompletionSignal string
}

// Succeeded reports whether KAS completed the step on a success signal: a send_message success, or
// update_workflow's recorded handoff (writeHandoffCompletion, kiro-cli 2.32.0).
func (r StepRecord) Succeeded() bool {
	return r.Status == statusCompleted && r.CompletionSignal == signalSuccess
}

// Completed reports whether KAS's node status is `completed`, signalled or not.
func (r StepRecord) Completed() bool {
	return r.Status == statusCompleted
}

// StepRecords indexes every step node of a run's tree by its PathKey; nil for a state with no tree.
func StepRecords(s *State) map[string]StepRecord {
	all := nodes(s)
	if all == nil {
		return nil
	}
	out := make(map[string]StepRecord)
	for _, v := range all {
		if v.n.Type == "step" {
			out[PathKey(v.path)] = StepRecord{Status: v.n.Status, CompletionSignal: v.n.CompletionSignal}
		}
	}
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
