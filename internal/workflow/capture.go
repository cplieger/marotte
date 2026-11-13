package workflow

import (
	"encoding/json"
	"fmt"
)

// captureNode is a state node decoded only as far as finding one node's captured output needs.
type captureNode struct {
	Iteration      *int            `json:"iteration"`
	CapturedOutput json.RawMessage `json:"capturedOutput"`
	NodeID         string          `json:"nodeId"`
	Type           string          `json:"type"`
	Children       []captureNode   `json:"children"`
}

// CaptureAt is the `capturedOutput` of the node at pathKey (a PathKey) in an inspect reply: nil
// when that node captured nothing, found false when no node has that path. An error means the
// reply does not decode.
func CaptureAt(raw json.RawMessage, pathKey string) (capture *string, found bool, err error) {
	var res struct {
		State *struct {
			Root *captureNode `json:"root"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, false, fmt.Errorf("decode inspect: %w", err)
	}
	if res.State == nil || res.State.Root == nil {
		return nil, false, nil
	}
	n := res.State.Root.find(pathKey, "", nil)
	if n == nil {
		return nil, false, nil
	}
	if len(n.CapturedOutput) == 0 || string(n.CapturedOutput) == "null" {
		return nil, true, nil
	}
	var text string
	if err := json.Unmarshal(n.CapturedOutput, &text); err != nil {
		return nil, true, fmt.Errorf("decode capturedOutput: %w", err)
	}
	return &text, true, nil
}

// find is the node at pathKey in n's subtree, trail being n's ancestors' segments.
func (n *captureNode) find(pathKey, parentType string, trail []string) *captureNode {
	path := make([]string, 0, len(trail)+1)
	path = append(path, trail...)
	path = append(path, segmentOf(n.NodeID, n.Iteration, parentType))
	if PathKey(path) == pathKey {
		return n
	}
	for i := range n.Children {
		if hit := n.Children[i].find(pathKey, n.Type, path); hit != nil {
			return hit
		}
	}
	return nil
}
