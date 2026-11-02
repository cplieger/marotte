package agent

import (
	"encoding/json"
	"testing"
)

// The rows are trimmed from a kiro-cli 2.29.1 session/list: the step row carries
// `_meta.kiro.workflow`, the parent row does not.
func TestStepSessionOf_ReadsTheStepRow(t *testing.T) {
	h, _, br := newTestHub()
	br.setCallResult("session/list", json.RawMessage(`{"sessions":[
		{"sessionId":"sess_parent","title":"New Session","_meta":{"kiro":{"status":"waiting_on_user"}}},
		{"sessionId":"sess_step","title":"askloop · ask","_meta":{"kiro":{
			"workflow":{"workflowId":"wf_1","workflowName":"askloop","nodeId":"ask","nodePath":["wf_1","ask"],"type":"step"},
			"serverSession":{"role":"workflow-step","ownerSessionId":"sess_parent"}}}}]}`))

	if wf, found, err := h.StepSessionOf(t.Context(), "sess_step"); err != nil || !found || wf != "wf_1" {
		t.Errorf("StepSessionOf(step) = %q, %v, %v; want wf_1, found", wf, found, err)
	}
	if _, found, err := h.StepSessionOf(t.Context(), "sess_parent"); err != nil || found {
		t.Errorf("StepSessionOf(parent) = found %v, %v; want a chat's session not a step's", found, err)
	}
}
