package workflow

import (
	"encoding/json"
	"testing"
)

// TestStepRecords_CarriesKASsCompletionSignal pins the lookup a step's end is graded by: each step under its PathKey,
// with the node's status and completion signal as KAS sent them.
func TestStepRecords_CarriesKASsCompletionSignal(t *testing.T) {
	t.Parallel()
	var res InspectResult
	if err := json.Unmarshal([]byte(`{"workflowId":"wf_1","state":{"workflowId":"wf_1","root":
		{"nodeId":"wf_1","type":"sequence","status":"running","children":[
			{"nodeId":"create-workflow","type":"step","status":"completed","sessionId":"s1",
			 "completionSignal":"success","completionSignalSource":"send_message","capturePinned":true},
			{"nodeId":"build","type":"step","status":"completed","sessionId":"s2"},
			{"nodeId":"ask","type":"step","status":"paused","sessionId":"s3","completionSignal":"need_input"}
		]}}}`), &res); err != nil {
		t.Fatal(err)
	}
	got := StepRecords(res.State)
	for _, tc := range []struct {
		node      string
		signal    string
		completed bool
		succeeded bool
	}{
		{"create-workflow", "success", true, true},
		{"build", "", true, false},
		{"ask", "need_input", false, false},
	} {
		st, ok := got[PathKey([]string{"wf_1", tc.node})]
		if !ok {
			t.Errorf("StepRecords has no %s: %+v", tc.node, got)
			continue
		}
		if st.CompletionSignal != tc.signal || st.Completed() != tc.completed || st.Succeeded() != tc.succeeded {
			t.Errorf("%s = signal %q, completed %v, succeeded %v; want %q, %v, %v",
				tc.node, st.CompletionSignal, st.Completed(), st.Succeeded(), tc.signal, tc.completed, tc.succeeded)
		}
	}
	if len(got) != 3 {
		t.Errorf("StepRecords holds %d steps, want 3", len(got))
	}
	if StepRecords(nil) != nil {
		t.Error("StepRecords(nil) is not nil")
	}
}
