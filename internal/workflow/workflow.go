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
	"slices"
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
	// The lenient fields, kept raw so a drifted type never fails the reply: the ask core keeps its last asks on a
	// failed decode. Read through signalSource and continuations; Unreadable reports one that does not parse.
	RawSignalSource  json.RawMessage `json:"completionSignalSource"`
	RawContinuations json.RawMessage `json:"continuationAttempts"`
	RawAwaiting      json.RawMessage `json:"awaitingUserInput"`
}

// signalSource is the node's completionSignalSource, "" when absent or unparsed.
func (n *node) signalSource() string {
	v, _ := rawAs[string](n.RawSignalSource)
	return v
}

// continuations is the node's continuationAttempts, 0 when absent or unparsed.
func (n *node) continuations() int {
	v, _ := rawAs[int](n.RawContinuations)
	return v
}

// rawAs parses raw as T; ok is false when raw is absent, null, or does not parse.
func rawAs[T any](raw json.RawMessage) (v T, ok bool) {
	if len(raw) == 0 || string(raw) == jsonNull {
		return v, false
	}
	return v, json.Unmarshal(raw, &v) == nil
}

// unparsed reports raw present and not parsing as T.
func unparsed[T any](raw json.RawMessage) bool {
	_, ok := rawAs[T](raw)
	return !ok && len(raw) > 0 && string(raw) != jsonNull
}

// jsonNull is JSON's null literal, which KAS sends for an absent value.
const jsonNull = "null"

// lenient decodes raw into dst, recording name in bad when raw is present and does not parse into dst's type.
func lenient[T any](raw json.RawMessage, dst *T, name string, bad *[]string) {
	if len(raw) == 0 || string(raw) == jsonNull {
		return
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		*bad = append(*bad, name+"="+string(raw))
		return
	}
	*dst = v
}

// RunPause is the run-level pause record of kiro-cli's state (3.5.0 schema): pauseDetail, pause, pausePending,
// stopInitiator and stopReason, each decoded on its own.
type RunPause struct {
	DetailClass      string
	DetailCode       string
	DetailAt         string
	PendingInitiator string
	PendingReason    string
	StopInitiator    string
	StopReason       string
	// CapKind is pause.kind ("maxIterations" for a repeat at its cap).
	CapKind   string
	HasDetail bool
	// Unreadable is set when any of the five fields was present and did not parse.
	Unreadable bool
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
	PauseReason string   `json:"pauseReason"`
	CreatedAt   string   `json:"createdAt"`
	Pause       RunPause `json:"-"`
	unreadable  []string
}

// UnmarshalJSON decodes the state and its run-level pause record leniently (node's raw fields say why).
func (s *State) UnmarshalJSON(b []byte) error {
	type plain State
	var w struct {
		plain
		PauseDetail   json.RawMessage `json:"pauseDetail"`
		PausePending  json.RawMessage `json:"pausePending"`
		Pause         json.RawMessage `json:"pause"`
		StopInitiator json.RawMessage `json:"stopInitiator"`
		StopReason    json.RawMessage `json:"stopReason"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = State(w.plain)
	s.unreadable = nil
	var detail struct {
		Class      string `json:"class"`
		Code       string `json:"code"`
		OccurredAt string `json:"occurredAt"`
	}
	var pending struct {
		Initiator string `json:"initiator"`
		Reason    string `json:"reason"`
	}
	var capped struct {
		Kind string `json:"kind"`
	}
	lenient(w.PauseDetail, &detail, "pauseDetail", &s.unreadable)
	lenient(w.PausePending, &pending, "pausePending", &s.unreadable)
	lenient(w.Pause, &capped, "pause", &s.unreadable)
	p := RunPause{
		DetailClass: detail.Class, DetailCode: detail.Code, DetailAt: detail.OccurredAt,
		PendingInitiator: pending.Initiator, PendingReason: pending.Reason, CapKind: capped.Kind,
		HasDetail: detail.Class != "",
	}
	lenient(w.StopInitiator, &p.StopInitiator, "stopInitiator", &s.unreadable)
	lenient(w.StopReason, &p.StopReason, "stopReason", &s.unreadable)
	p.Unreadable = len(s.unreadable) > 0
	s.Pause = p
	return nil
}

// Unreadable names every lenient field of s, run-level or on a node, that was present and did not parse, with
// its raw value, for a Debug log; nil when every one parsed.
func Unreadable(s *State) []string {
	if s == nil {
		return nil
	}
	out := slices.Clone(s.unreadable)
	var walk func(n *node)
	walk = func(n *node) {
		if unparsed[string](n.RawSignalSource) {
			out = append(out, "node "+n.NodeID+" completionSignalSource="+string(n.RawSignalSource))
		}
		if unparsed[int](n.RawContinuations) {
			out = append(out, "node "+n.NodeID+" continuationAttempts="+string(n.RawContinuations))
		}
		if unparsed[bool](n.RawAwaiting) {
			out = append(out, "node "+n.NodeID+" awaitingUserInput="+string(n.RawAwaiting))
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	if s.Root != nil {
		walk(s.Root)
	}
	return out
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
	// CompletionSignal is the signal KAS recorded on the node, verbatim; "" for none.
	CompletionSignal string
	// Path is the node path from the root to this step in KAS's WIRE spelling (see pathSegment).
	Path []string
}

// EndSignalled reports whether KAS recorded a success or an error signal on the step.
func (s *StepSession) EndSignalled() bool {
	return s.CompletionSignal == signalSuccess || s.CompletionSignal == signalError
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
		if v.n.Type == nodeTypeStep {
			out = append(out, StepSession{
				NodeID: v.n.NodeID, SessionID: v.n.SessionID, Status: v.n.Status, CompletionSignal: v.n.CompletionSignal, Path: v.path,
			})
		}
	}
	return out
}

// pathSegment is what KAS calls this node in a node PATH. A repeat's iteration container
// is `<repeatId>#<n>` in the tree and `iter-<n>` on the wire, derived from its
// `iteration`; with none it falls back to the node id. Siblings holding the same
// translation: translate.runNodePath and run-node-key.ts.
func pathSegment(n, parent *node) string {
	parentType := ""
	if parent != nil {
		parentType = parent.Type
	}
	return segmentOf(n.NodeID, n.Iteration, parentType)
}

// nodeTypeRepeat is KAS's node type of a loop, whose children are its passes.
const nodeTypeRepeat = "repeat"

// segmentOf is pathSegment over a node's own fields and its parent's type ("" for the root).
func segmentOf(nodeID string, iteration *int, parentType string) string {
	if parentType == nodeTypeRepeat && iteration != nil {
		return "iter-" + strconv.Itoa(*iteration)
	}
	return nodeID
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
