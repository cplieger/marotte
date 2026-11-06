package workflow

import (
	"encoding/json"
	"testing"
)

// laneFixture: a sequence root running a parallel and then wrap-up; the parallel's first branch is a
// sequence of setup, a nested sequence holding gate then check, and publish; build is its other branch.
const laneFixture = `{"workflowId":"wf_1","state":{"workflowId":"wf_1","status":"running","root":
{"nodeId":"root","type":"sequence","status":"running","children":[
 {"nodeId":"fan","type":"parallel","status":"running","children":[
  {"nodeId":"branch","type":"sequence","status":"paused","children":[
   {"nodeId":"setup","type":"step","status":"completed"},
   {"nodeId":"inner","type":"sequence","status":"paused","children":[
    {"nodeId":"gate","type":"step","status":"paused","sessionId":"s1"},
    {"nodeId":"check","type":"step","status":"pending"}]},
   {"nodeId":"publish","type":"step","status":"pending"}]},
  {"nodeId":"build","type":"step","status":"running","sessionId":"s2"}]},
 {"nodeId":"wrap-up","type":"step","status":"pending"}]}}}`

func TestLane_PassedReadsTheBranchBelowTheNearestParallel(t *testing.T) {
	t.Parallel()
	in, err := DecodeInspect("wf_1", json.RawMessage(laneFixture))
	if err != nil {
		t.Fatalf("Setup: decode: %v", err)
	}
	lane := LaneOf(in.State, PathKey([]string{"root", "fan", "branch", "inner", "gate"}))
	for _, tc := range []struct {
		name string
		path []string
		want bool
	}{
		{name: "the_next_step_of_its_sequence", path: []string{"root", "fan", "branch", "inner", "check"}, want: true},
		{name: "the_next_step_of_the_enclosing_sequence", path: []string{"root", "fan", "branch", "publish"}, want: true},
		{name: "itself", path: []string{"root", "fan", "branch", "inner", "gate"}},
		{name: "its_own_container", path: []string{"root", "fan", "branch", "inner"}},
		{name: "an_earlier_step_of_the_branch", path: []string{"root", "fan", "branch", "setup"}},
		{name: "the_other_branch", path: []string{"root", "fan", "build"}},
		{name: "past_the_join", path: []string{"root", "wrap-up"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lane.Passed(tc.path); got != tc.want {
				t.Errorf("Passed(%v) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
	if LaneOf(in.State, PathKey([]string{"root", "missing"})).Passed([]string{"root", "fan", "branch", "publish"}) {
		t.Error("the lane of a step the run does not hold passed a node")
	}
}
