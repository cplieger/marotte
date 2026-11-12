package agent

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The run read's summary pass yields valid JSON with no step capture left under state.root, and a
// second pass changes nothing.
func FuzzSummarizedRead_idempotent(f *testing.F) {
	f.Add([]byte(`{"workflowId":"wf","state":{"status":"running","root":{"nodeId":"wf","type":"sequence","children":[` +
		`{"capturedOutput":"a\nb\nc\nd","nodeId":"s","type":"step"},` +
		`{"nodeId":"w","type":"watch","capturedOutput":"{\"status\":\"exited\"}"},` +
		`{"nodeId":"r","type":"repeat","children":[{"nodeId":"r#0","iteration":0,"type":"sequence","children":[` +
		`{"nodeId":"b","type":"step","capturedOutput":"\u00e9\u00e9"}]}]}]}},"nodePlan":{"capturedOutput":"kept"}}`))
	f.Add([]byte(`{"state":null}`))
	f.Add([]byte(`{"state":{"root":{"type":"step","capturedOutput":7,"children":"none"}}}`))
	f.Add([]byte(`[1,2]`))
	f.Fuzz(func(t *testing.T, in []byte) {
		if !json.Valid(in) {
			return
		}
		out, err := summarizedRead(in)
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("summarizedRead(%q) = %q, not JSON", in, out)
		}
		again, err := summarizedRead(out)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("a second pass over %q gave %q (%v)", out, again, err)
		}
		var reply struct {
			State struct {
				Root json.RawMessage `json:"root"`
			} `json:"state"`
		}
		if json.Unmarshal(out, &reply) == nil {
			assertNoStepCapture(t, reply.State.Root)
		}
	})
}

// assertNoStepCapture fails for a node under raw, other than a watch, carrying a string capture.
func assertNoStepCapture(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var n struct {
		CapturedOutput json.RawMessage   `json:"capturedOutput"`
		Type           string            `json:"type"`
		Children       []json.RawMessage `json:"children"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return
	}
	if n.Type != "watch" && len(n.CapturedOutput) > 0 && n.CapturedOutput[0] == '"' {
		t.Fatalf("a %q node kept its capture: %s", n.Type, raw)
	}
	for _, c := range n.Children {
		assertNoStepCapture(t, c)
	}
}
