// Package workflow decodes KAS's workflow-run wire shapes. It holds no run state:
// `_kiro/workflow/inspect` returns it whole and the read endpoints pass it through. What
// remains is which ACP session executed a given step, plus KAS's `-32603` error unwrapping.
//
// `inspect` has no stepSessions array: `state.root` holds `sessionId`, `agentName`,
// `iteration` and `branchId` ON each step node, so the tree IS the join.
package workflow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// Node is one node of a run's state tree; a step node is a leaf and the only kind with a
// session. Other fields stay on the wire, passed through by the read endpoint.
type Node struct {
	// Iteration is the pass of an enclosing `repeat`; a pointer because pass 0 is the first.
	Iteration *int   `json:"iteration"`
	NodeID    string `json:"nodeId"`
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Children  []Node `json:"children"`
}

// State is the run state `inspect` returns, decoded only as far as step identification
// needs.
type State struct {
	Root       *Node  `json:"root"`
	WorkflowID string `json:"workflowId"`
}

// InspectResult is `_kiro/workflow/inspect`'s reply, decoded to the one part
// marotte reads. `nodePlan` rides through undecoded — see the package comment.
type InspectResult struct {
	State *State `json:"state"`
}

// StepSession names one step node, its ACP session and its PATH. The path addresses one
// EXECUTION (a repeat's iterations share a node id), so callers naming a step key on it.
type StepSession struct {
	NodeID    string
	SessionID string
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
	if s == nil || s.Root == nil {
		return nil
	}
	var out []StepSession
	walk(s.Root, nil, nil, &out)
	return out
}

// walk visits the tree depth-first. Each level allocates its path at EXACT capacity, or a
// child's append writes into a sibling's backing array.
func walk(n, parent *Node, trail []string, out *[]StepSession) {
	path := make([]string, 0, len(trail)+1)
	path = append(path, trail...)
	path = append(path, pathSegment(n, parent))
	if n.Type == "step" {
		*out = append(*out, StepSession{NodeID: n.NodeID, SessionID: n.SessionID, Path: path})
	}
	for i := range n.Children {
		walk(&n.Children[i], n, path, out)
	}
}

// pathSegment is what KAS calls this node in a node PATH. A repeat's iteration container
// is `<repeatId>#<n>` in the tree and `iter-<n>` on the wire, derived from its
// `iteration`; with none it falls back to the node id. Siblings holding the same
// translation: translate.runNodePath and run-store.ts.
func pathSegment(n, parent *Node) string {
	if parent != nil && parent.Type == "repeat" && n.Iteration != nil {
		return "iter-" + strconv.Itoa(*n.Iteration)
	}
	return n.NodeID
}

// unknownMethodMarker is how KAS reports an unregistered `_kiro/workflow/*` name: a -32603
// whose `error.data.details` holds this text, which no registered method produces.
const unknownMethodMarker = "has no persistence classification"

// ErrUnknownMethod means the verb does not exist on this KAS build: a permanent capability
// answer, unlike a transient failure. Callers ask errors.Is.
var ErrUnknownMethod = errors.New("workflow verb not registered on this kiro-cli build")

// The two refusals KAS's run-ownership gate throws as plain Errors: a -32603 whose
// `error.data.details` is the sentence (acp-server.js acquireRunOwnership and
// ensureRunOwnership, kiro-cli 2.27.0). Each marker is shared by the load and mutate variants.
const (
	ownedElsewhereMarker = "appears to be running in another process"
	justClaimedMarker    = "was just claimed by another process"
)

// ErrOwnedElsewhere means another process's ownership stamp names the run and KAS does
// not judge that stamp stale.
var ErrOwnedElsewhere = errors.New("another process holds this run")

// ErrJustClaimed means another claim on the run is in flight right now.
var ErrJustClaimed = errors.New("another claim on this run is in flight")

// Classify types a `_kiro/workflow/*` RPC failure AT THE BOUNDARY, wrapping its sentinel
// with the original still unwrappable. It reads `error.data`, never the message chain:
// RPCError.Error() for this shape is the literal "Internal error".
func Classify(err error) error {
	if err == nil {
		return nil
	}
	details := rpcerr.Details(err)
	for _, c := range []struct {
		sentinel error
		marker   string
	}{
		{ErrUnknownMethod, unknownMethodMarker},
		{ErrOwnedElsewhere, ownedElsewhereMarker},
		{ErrJustClaimed, justClaimedMarker},
	} {
		if strings.Contains(details, c.marker) {
			return fmt.Errorf("%w: %w", c.sentinel, err)
		}
	}
	return err
}
