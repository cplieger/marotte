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

// TestStateUpdatedAt_FollowsKASsListDerivation pins the order KAS's `_kiro/workflow/list` builds a row's updatedAt
// in from the state inspect returns: `root.endedAt ?? root.startedAt ?? createdAt`.
func TestStateUpdatedAt_FollowsKASsListDerivation(t *testing.T) {
	t.Parallel()
	const created, started, ended = "2026-11-01T10:00:00.000Z", "2026-11-01T10:00:01.000Z", "2026-11-01T11:00:00.000Z"
	for name, tc := range map[string]struct {
		state string
		want  string
	}{
		"an ended run":        {`{"createdAt":"` + created + `","root":{"startedAt":"` + started + `","endedAt":"` + ended + `"}}`, ended},
		"a run never ended":   {`{"createdAt":"` + created + `","root":{"startedAt":"` + started + `"}}`, started},
		"a root with no time": {`{"createdAt":"` + created + `","root":{"nodeId":"wf_1"}}`, created},
		"no root":             {`{"createdAt":"` + created + `"}`, created},
		"no time at all":      {`{"root":{"nodeId":"wf_1"}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var s State
			if err := json.Unmarshal([]byte(tc.state), &s); err != nil {
				t.Fatal(err)
			}
			if got := s.UpdatedAt(); got != tc.want {
				t.Errorf("UpdatedAt() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStepRecord_Ended pins which node statuses are final: a running, paused or pending node can still complete.
func TestStepRecord_Ended(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status string
		ended  bool
	}{
		{"", false},
		{"running", false},
		{"paused", false},
		{"pending", false},
		{"completed", true},
		{"failed", true},
		{"cancelled", true},
		{"aborted", true},
	} {
		if got := (StepRecord{Status: tc.status}).Ended(); got != tc.ended {
			t.Errorf("StepRecord{Status: %q}.Ended() = %v, want %v", tc.status, got, tc.ended)
		}
	}
}
