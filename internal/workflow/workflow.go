// Package workflow decodes KAS's workflow-run wire shapes. It holds no run state:
// `_kiro/workflow/inspect` returns it whole and the read endpoints pass it through. What
// remains is which ACP session executed a given step, plus KAS's `-32603` error unwrapping.
//
// `inspect` has no stepSessions array: `state.root` holds `sessionId`, `agentName`,
// `iteration` and `branchId` ON each step node, so the tree IS the join.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// node is one node of a run's state tree; a step node is a leaf and the only kind with a
// session. Other fields stay on the wire, passed through by the read endpoint.
type node struct {
	// Iteration is the pass of an enclosing `repeat`; a pointer because pass 0 is the first.
	Iteration        *int             `json:"iteration"`
	NodeID           string           `json:"nodeId"`
	Type             string           `json:"type"`
	SessionID        string           `json:"sessionId"`
	Status           string           `json:"status"`
	AgentName        string           `json:"agentName"`
	CompletionSignal string           `json:"completionSignal"`
	StartedAt        string           `json:"startedAt"`
	EndedAt          string           `json:"endedAt"`
	Children         []node           `json:"children"`
	Attention        []attentionEntry `json:"attention"`
}

// attentionEntry is one of KAS's open waits on a node: a step's park (markWaitingForUser) carries no
// toolCallId, and a permission or input request its session holds carries the request's.
type attentionEntry struct {
	Kind       string `json:"kind"`
	Since      string `json:"since"`
	ToolCallID string `json:"toolCallId"`
}

// State is the run state `inspect` returns, decoded only as far as step identification
// needs.
type State struct {
	Root            *node  `json:"root"`
	WorkflowID      string `json:"workflowId"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parentSessionId"`
	RunLabel        string `json:"runLabel"`
	WorkflowName    string `json:"workflowName"`
	// PauseReason is KAS's sentence for the run's latest park, whichever step parked.
	PauseReason string `json:"pauseReason"`
	CreatedAt   string `json:"createdAt"`
}

// UpdatedAt is the run's last activity as `_kiro/workflow/list` derives its row's updatedAt from this same state: the
// root's end, else its start, else the state's creation; "" when none is recorded.
func (s *State) UpdatedAt() string {
	if s.Root != nil && s.Root.EndedAt != "" {
		return s.Root.EndedAt
	}
	if s.Root != nil && s.Root.StartedAt != "" {
		return s.Root.StartedAt
	}
	return s.CreatedAt
}

// InspectResult is `_kiro/workflow/inspect`'s reply, decoded to the parts
// marotte reads. `nodePlan` rides through undecoded — see the package comment.
type InspectResult struct {
	State      *State `json:"state"`
	WorkflowID string `json:"workflowId"`
	// PendingSteps is the queued replace_remaining plan, which KAS answers from its persisted
	// pending-step-update.json even for a run it has not loaded. It stays raw so a shape change in
	// this advisory key never fails the state's decode.
	PendingSteps json.RawMessage `json:"pendingSteps"`
}

// StepSession names one step node, its ACP session and its PATH. The path addresses one
// EXECUTION (a repeat's iterations share a node id), so callers naming a step key on it.
type StepSession struct {
	NodeID    string
	SessionID string
	// Status is KAS's node status word, verbatim.
	Status string
	// Path is the node path from the root to this step in KAS's WIRE spelling (see pathSegment).
	Path []string
}

// PathKey is a node path's one string identity: the run log's turn key, the step-transcript
// address and the client's row key (run-node-key.ts, pinned by testdata/node_path_key.json). A
// node id is free-form, so a separator join would collide ["a/b","c"] with ["a","b/c"].
// It is an equality key only: a path over keyenc.MaxComponentBytes keys by a hash no split reads
// back, so a reader needing the segments or the node id carries them beside the key.
func PathKey(path []string) string {
	return keyenc.Join(path...)
}

// StepSessions walks a run's state tree depth-first and returns every step with a session,
// in declaration order (a `parallel` node's branches are not time-ordered).
func StepSessions(s *State) []StepSession {
	var out []StepSession
	for _, st := range Steps(s) {
		if st.SessionID != "" {
			out = append(out, st)
		}
	}
	return out
}

// Steps returns EVERY step node in the same order, run or not, so a caller can tell "no
// such step" from "not started".
func Steps(s *State) []StepSession {
	var out []StepSession
	for _, v := range nodes(s) {
		if v.n.Type == "step" {
			out = append(out, StepSession{NodeID: v.n.NodeID, SessionID: v.n.SessionID, Status: v.n.Status, Path: v.path})
		}
	}
	return out
}

// pathSegment is what KAS calls this node in a node PATH. A repeat's iteration container
// is `<repeatId>#<n>` in the tree and `iter-<n>` on the wire, derived from its
// `iteration`; with none it falls back to the node id. Siblings holding the same
// translation: translate.runNodePath and run-node-key.ts.
func pathSegment(n, parent *node) string {
	if parent != nil && parent.Type == "repeat" && n.Iteration != nil {
		return "iter-" + strconv.Itoa(*n.Iteration)
	}
	return n.NodeID
}

// The refusal KAS's run-claim gate throws as a plain Error: a -32603 whose `error.data.details` is the sentence
// (acp-server.js acquireRunOwnership and ensureRunOwnership, kiro-cli 2.27.0), shared by the load and mutate variants.
const justClaimedMarker = "was just claimed by another process"

// ErrJustClaimed means another claim on the run is in flight right now.
var ErrJustClaimed = errors.New("another claim on this run is in flight")

// Classify types a `_kiro/workflow/*` RPC failure AT THE BOUNDARY, wrapping its sentinel
// with the original still unwrappable. It reads `error.data`, never the message chain:
// RPCError.Error() for this shape is the literal "Internal error".
func Classify(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(rpcerr.Details(err), justClaimedMarker) {
		return fmt.Errorf("%w: %w", ErrJustClaimed, err)
	}
	return err
}
