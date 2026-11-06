package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

const tripwireLine = "a signalled paused step's lane moved on while another step still ran: " +
	"kirodotdev/Kiro#12281 may be fixed; verify before removing marotte's workarounds for it"

// The laned run: a parallel holding a repeat whose sixth pass runs gate then after, beside build.
var (
	gateSegments  = []string{"root", "lane", "iter-6", "gate"}
	gatePath      = workflow.PathKey(gateSegments)
	afterSegments = []string{"root", "lane", "iter-6", "after"}
	nextPass      = []string{"root", "lane", "iter-7"}
	nextPassGate  = []string{"root", "lane", "iter-7", "gate"}
	buildSegments = []string{"root", "build"}
)

// firedCount is how many lane advances w saw while a sibling still ran.
func firedCount(w *signalTripwire) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// laneInspect is an inspect reply of the laned run with gate and build at the given statuses.
func laneInspect(t *testing.T, run marotte.RunStatus, gate, signal, build string) json.RawMessage {
	t.Helper()
	gateNode := map[string]any{"nodeId": "gate", "type": stepNodeType, "status": gate, "sessionId": "sess_gate", "iteration": 6}
	if signal != "" {
		gateNode["completionSignal"] = signal
	}
	pass := map[string]any{"nodeId": "lane#6", "type": "sequence", "status": "paused", "iteration": 6, "children": []any{
		gateNode,
		map[string]any{"nodeId": "after", "type": stepNodeType, "status": "pending", "iteration": 6},
	}}
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{"workflowId": "wf_1", "status": run, "root": map[string]any{
			"nodeId": "root", "type": "parallel", "status": string(run), "children": []any{
				map[string]any{"nodeId": "lane", "type": "repeat", "status": "paused", "children": []any{pass}},
				map[string]any{"nodeId": "build", "type": stepNodeType, "status": build, "sessionId": "sess_build"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %v", err)
	}
	return raw
}

// tripRuns is a run registry whose frame hooks feed its tripwire, as KAS's frames do.
func tripRuns(t *testing.T) *Runs {
	t.Helper()
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusPaused)
	return h.runs
}

func started(t *testing.T, rs *Runs, path []string) {
	t.Helper()
	rs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey(path), Path: path}, "")
}

func completed(t *testing.T, rs *Runs, path []string) {
	t.Helper()
	rs.RunNodeComplete(t.Context(), "wf_1", path, "completed", "")
}

func laneState(t *testing.T, run marotte.RunStatus, gate, signal, build string) *workflow.State {
	t.Helper()
	in, err := workflow.DecodeInspect("wf_1", laneInspect(t, run, gate, signal, build))
	if err != nil {
		t.Fatalf("Setup: decoding the inspect reply: %v", err)
	}
	return in.State
}

// A signalled paused step seen beside a running sibling: only its lane moving on while that sibling
// never stopped trips, which KAS 2.37.1 never does. Its own end or resume moves no lane.
func TestSignalTripwire_TripsOnlyOnALaneAdvanceBesideARunningSibling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal string
		events func(rs *Runs)
		want   int
	}{
		{name: "next_step_of_the_pass", signal: "success", want: 1, events: func(rs *Runs) {
			started(t, rs, afterSegments)
		}},
		{name: "next_pass", signal: "success", want: 1, events: func(rs *Runs) {
			started(t, rs, nextPass)
		}},
		{name: "a_step_of_the_next_pass", signal: "error", want: 1, events: func(rs *Runs) {
			started(t, rs, nextPassGate)
		}},
		{name: "advance_after_the_step_ended", signal: "success", want: 1, events: func(rs *Runs) {
			completed(t, rs, gateSegments)
			started(t, rs, afterSegments)
		}},
		{name: "a_client_end", signal: "success", events: func(rs *Runs) {
			completed(t, rs, gateSegments)
		}},
		{name: "a_resume", signal: "success", events: func(rs *Runs) {
			started(t, rs, gateSegments)
		}},
		{name: "advance_after_a_resume", signal: "success", events: func(rs *Runs) {
			started(t, rs, gateSegments)
			started(t, rs, afterSegments)
		}},
		{name: "advance_once_the_sibling_stopped", signal: "success", events: func(rs *Runs) {
			completed(t, rs, buildSegments)
			started(t, rs, afterSegments)
		}},
		{name: "unsignalled_advance", events: func(rs *Runs) {
			started(t, rs, afterSegments)
		}},
		{name: "question_advance", signal: "need_input", events: func(rs *Runs) {
			started(t, rs, afterSegments)
		}},
		{name: "a_node_of_another_lane", signal: "success", events: func(rs *Runs) {
			started(t, rs, buildSegments)
		}},
		{name: "the_enclosing_repeat", signal: "success", events: func(rs *Runs) {
			started(t, rs, []string{"root", "lane"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := tripRuns(t)
			rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", tc.signal, "running"))
			tc.events(rs)
			if got := firedCount(&rs.signals); got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

// An inspect read never trips: an end or a resume it shows is what a client's verb also produces.
func TestSignalTripwire_AReadShowingTheStepEndedOrResumedIsNoEvidence(t *testing.T) {
	for _, step := range []string{"completed", "failed", "running"} {
		t.Run(step, func(t *testing.T) {
			rs := tripRuns(t)
			rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "running"))
			rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, step, "", "running"))
			if got := firedCount(&rs.signals); got != 0 {
				t.Errorf("count = %d, want 0", got)
			}
		})
	}
}

// KAS clears a step's signal when it completes it, so the signal seen while it was paused is what counts.
func TestSignalTripwire_TheSignalSeenWhilePausedOutlivesTheEnd(t *testing.T) {
	rs := tripRuns(t)
	rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "need_input", "running"))
	rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "running"))
	rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "completed", "", "running"))
	started(t, rs, afterSegments)
	if got := firedCount(&rs.signals); got != 1 {
		t.Errorf("count = %d, want 1: the answer's success was recorded while the step stayed paused", got)
	}
}

func TestSignalTripwire_ASignalSeenWithNoRunningSiblingIsNotWatched(t *testing.T) {
	rs := tripRuns(t)
	rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "completed"))
	rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "running"))
	started(t, rs, afterSegments)
	if got := firedCount(&rs.signals); got != 0 {
		t.Errorf("count = %d, want 0: the sibling started after the signal was seen", got)
	}
}

// A step that is itself a parallel's branch has no lane past it short of the join, which waits for
// every sibling.
func TestSignalTripwire_ABranchStepHasNoLane(t *testing.T) {
	rs := tripRuns(t)
	in, err := workflow.DecodeInspect("wf_1", inspectAsk(t, "wf_1", marotte.RunStatusRunning,
		askStepNode{id: "review", status: "paused", session: "sess_step", signal: "success"},
		askStepNode{id: "build", status: "running", session: "sess_build"}))
	if err != nil {
		t.Fatalf("Setup: decoding the inspect reply: %v", err)
	}
	rs.signals.observe("wf_1", in.State)
	started(t, rs, []string{"root", "other"})
	if got := firedCount(&rs.signals); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// A run pause stops every sibling: no lane advance after it is a candidate.
func TestSignalTripwire_APauseIsNoEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(rs *Runs)
	}{
		{name: "run_paused", stop: func(rs *Runs) { rs.signals.runStopped("wf_1") }},
		{name: "paused_run_read", stop: func(rs *Runs) {
			rs.signals.observe("wf_1", laneState(t, marotte.RunStatusPaused, "paused", "success", "running"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := tripRuns(t)
			rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "running"))
			tc.stop(rs)
			started(t, rs, afterSegments)
			if got := firedCount(&rs.signals); got != 0 {
				t.Errorf("count = %d, want 0", got)
			}
		})
	}
}

// One INFO line per process, the count carried on every later sighting at DEBUG.
func TestSignalTripwire_LogsOnceAndCountsEverySighting(t *testing.T) {
	logs := captureLogs(t)
	rs := tripRuns(t)
	for range 2 {
		rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "running"))
		started(t, rs, afterSegments)
	}
	out := logs.String()
	if n := strings.Count(out, tripwireLine); n != 1 {
		t.Errorf("tripwire INFO lines = %d, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, `"level":"INFO"`) || !strings.Contains(out, `"workflow_id":"wf_1"`) || !strings.Contains(out, `"node_id":"gate"`) {
		t.Errorf("logs = %s, want an INFO line naming the run and the node", out)
	}
	if !strings.Contains(out, `"count":2`) {
		t.Errorf("logs = %s, want the second sighting counted at DEBUG", out)
	}
	if got := firedCount(&rs.signals); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
}

// The Runs hooks feed it: an inspect read watches, the frames decide. A parent agent's update_status
// reaches marotte as a bare node_complete and moves no lane.
func TestRuns_TheTripwireReadsInspectsAndFrames(t *testing.T) {
	gateStart := &translate.RunStep{RunID: "wf_1", NodePath: gatePath, SessionID: "sess_gate", Path: gateSegments}
	afterStart := &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey(afterSegments), SessionID: "sess_after", Path: afterSegments}
	for _, tc := range []struct {
		name   string
		frames func(rs *Runs)
		want   int
	}{
		{name: "a_parents_update_status", frames: func(rs *Runs) {
			rs.RunNodeComplete(t.Context(), "wf_1", gateSegments, "completed", "")
		}},
		{name: "a_resume", frames: func(rs *Runs) {
			rs.RunNodeStart(t.Context(), gateStart, "")
		}},
		{name: "the_lane_moved_on_beside_a_running_sibling", want: 1, frames: func(rs *Runs) {
			rs.RunNodeComplete(t.Context(), "wf_1", gateSegments, "completed", "")
			rs.RunNodeStart(t.Context(), afterStart, "")
		}},
		{name: "sibling_paused_first", frames: func(rs *Runs) {
			rs.RunNodePaused(t.Context(), "wf_1", buildSegments)
			rs.RunNodeComplete(t.Context(), "wf_1", gateSegments, "completed", "")
			rs.RunNodeStart(t.Context(), afterStart, "")
		}},
		{name: "sibling_completed_first", frames: func(rs *Runs) {
			rs.RunNodeComplete(t.Context(), "wf_1", buildSegments, "completed", "")
			rs.RunNodeStart(t.Context(), afterStart, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusPaused)
			br.setCallResult(methodKiroWorkflowInspect, laneInspect(t, marotte.RunStatusRunning, "paused", "success", "running"))
			if _, err := h.runs.rawInspect(t.Context(), "wf_1"); err != nil {
				t.Fatalf("Setup: rawInspect: %v", err)
			}
			tc.frames(h.runs)
			if got := firedCount(&h.runs.signals); got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRuns_ARunCompleteFramePausingTheRunClearsTheWatch(t *testing.T) {
	rs := &Runs{translate: &recordingRunTranslator{}}
	rs.signals.observe("wf_1", laneState(t, marotte.RunStatusRunning, "paused", "success", "running"))
	rs.observeComplete(t.Context(), "c-1", runNotif(methodWFRunComplete, map[string]any{"workflowId": "wf_1", "status": "paused"}))
	rs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey(afterSegments), Path: afterSegments}, "")
	if got := firedCount(&rs.signals); got != 0 {
		t.Errorf("count = %d, want 0: a paused run stopped every sibling", got)
	}
}

// Mark step done applies at once while a sibling runs and moves no lane: it is marotte's own end.
func TestEndStep_APersonsEndIsNoTripwireEvidence(t *testing.T) {
	h, _ := endHub(t, laneInspect(t, marotte.RunStatusRunning, "paused", "success", "running"))
	if err := h.runs.endStep(t.Context(), "wf_1", gatePath, runStepCompleted); err != nil {
		t.Fatalf("Setup: endStep = %v", err)
	}
	h.runs.RunNodeComplete(t.Context(), "wf_1", gateSegments, "completed", "")
	if got := firedCount(&h.runs.signals); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}
