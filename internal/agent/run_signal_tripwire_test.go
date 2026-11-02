package agent

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// tripwireCause is the INFO line's verdict: the lifecycle frames carry no actor, so an end or a resume
// another client sent reads the same as KAS no longer waiting.
const tripwireCause = "another client may have done it, or kirodotdev/Kiro#11733 may be fixed; verify before removing the done-pending label"

const tripwireLine = "a signalled paused step ended before the run settled; " + tripwireCause

var buildPath = workflow.PathKey([]string{"root", "build"})

// firedCount is how many early resumes and applications w saw.
func firedCount(w *signalTripwire) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

func tripState(t *testing.T, run marotte.RunStatus, steps ...askStepNode) *workflow.State {
	t.Helper()
	in, err := workflow.DecodeInspect("wf_1", inspectAsk(t, "wf_1", run, steps...))
	if err != nil {
		t.Fatalf("Setup: decoding the inspect reply: %v", err)
	}
	return in.State
}

func tripReview(status, signal string) askStepNode {
	return askStepNode{id: "review", status: status, session: "sess_step", signal: signal}
}

func tripBuild(status string) askStepNode {
	return askStepNode{id: "build", status: status, session: "sess_build"}
}

// A signalled paused step seen beside a running sibling, then a later read: the tripwire fires only
// when the step ended while that sibling never stopped, which KAS 2.32.0 never does.
func TestSignalTripwire_FiresOnlyWhileTheSiblingStillRuns(t *testing.T) {
	for _, tc := range []struct {
		name, signal, sibling, step string
		want                        int
	}{
		{name: "success_applied_beside_a_running_sibling", signal: "success", sibling: "running", step: "completed", want: 1},
		{name: "error_applied_beside_a_running_sibling", signal: "error", sibling: "running", step: "failed", want: 1},
		{name: "applied_once_the_sibling_finished", signal: "success", sibling: "completed", step: "completed"},
		{name: "unsignalled_end_beside_a_running_sibling", sibling: "running", step: "completed"},
		{name: "need_input_end_beside_a_running_sibling", signal: "need_input", sibling: "running", step: "completed"},
		{name: "unsignalled_resume_beside_a_running_sibling", sibling: "running", step: "running", want: 1},
		{name: "still_paused", signal: "success", sibling: "running", step: "paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w signalTripwire
			w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", tc.signal), tripBuild("running")))
			w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview(tc.step, tc.signal), tripBuild(tc.sibling)))
			if got := firedCount(&w); got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSignalTripwire_ASignalRecordedAfterThePauseCounts(t *testing.T) {
	var w signalTripwire
	w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "need_input"), tripBuild("running")))
	w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
	w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("completed", "success"), tripBuild("running")))
	if got := firedCount(&w); got != 1 {
		t.Errorf("count = %d, want 1: the answer's success was recorded while the step stayed paused", got)
	}
}

func TestSignalTripwire_ASignalSeenWithNoRunningSiblingIsNotWatched(t *testing.T) {
	var w signalTripwire
	w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("completed")))
	w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("completed", "success"), tripBuild("running")))
	if got := firedCount(&w); got != 0 {
		t.Errorf("count = %d, want 0: the sibling started after the signal was seen", got)
	}
}

// Frames tell only whether a sibling still runs: a sibling's stop frame before the step's completion
// is KAS 2.32.0's order, and a completion or restart with the sibling still running is a candidate
// to verify, since the frames carry no actor.
func TestSignalTripwire_FramesTrackWhetherASiblingStillRuns(t *testing.T) {
	t.Run("sibling_stopped_first", func(t *testing.T) {
		var w signalTripwire
		w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
		w.nodeStopped("wf_1", buildPath, "paused")
		w.nodeStopped("wf_1", reviewPath, "completed")
		if got := firedCount(&w); got != 0 {
			t.Errorf("count = %d, want 0", got)
		}
	})
	t.Run("step_completed_first", func(t *testing.T) {
		var w signalTripwire
		w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
		w.nodeStopped("wf_1", reviewPath, "completed")
		if got := firedCount(&w); got != 1 {
			t.Errorf("count = %d, want 1", got)
		}
	})
	t.Run("unsignalled_step_completed_first", func(t *testing.T) {
		var w signalTripwire
		w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", ""), tripBuild("running")))
		w.nodeStopped("wf_1", reviewPath, "completed")
		if got := firedCount(&w); got != 0 {
			t.Errorf("count = %d, want 0: an end with no recorded signal is a person's or the parent's", got)
		}
	})
	t.Run("step_restarted", func(t *testing.T) {
		var w signalTripwire
		w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", ""), tripBuild("running")))
		w.nodeStarted("wf_1", reviewPath)
		if got := firedCount(&w); got != 1 {
			t.Errorf("count = %d, want 1", got)
		}
	})
}

// A run pause stops every sibling, and marotte's own end applies at once: neither is a candidate.
func TestSignalTripwire_APauseOrAPersonsEndIsNoEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(w *signalTripwire)
	}{
		{name: "run_paused", stop: func(w *signalTripwire) { w.runStopped("wf_1") }},
		{name: "marked_done", stop: func(w *signalTripwire) { w.forget("wf_1", reviewPath) }},
		{name: "paused_run_read", stop: func(w *signalTripwire) {
			w.observe("wf_1", tripState(t, marotte.RunStatusPaused, tripReview("paused", "success"), tripBuild("running")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w signalTripwire
			w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
			tc.stop(&w)
			w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("completed", "success"), tripBuild("running")))
			if got := firedCount(&w); got != 0 {
				t.Errorf("count = %d, want 0", got)
			}
		})
	}
}

// One INFO line per process, the count carried on every later sighting at DEBUG.
func TestSignalTripwire_LogsOnceAndCountsEverySighting(t *testing.T) {
	logs := captureLogs(t)
	var w signalTripwire
	for range 2 {
		w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
		w.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("completed", "success"), tripBuild("running")))
	}
	out := logs.String()
	if n := strings.Count(out, tripwireLine); n != 1 {
		t.Errorf("tripwire INFO lines = %d, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, `"level":"INFO"`) || !strings.Contains(out, `"workflow_id":"wf_1"`) || !strings.Contains(out, `"node_id":"review"`) {
		t.Errorf("logs = %s, want an INFO line naming the run and the node", out)
	}
	if !strings.Contains(out, `"count":2`) {
		t.Errorf("logs = %s, want the second sighting counted at DEBUG", out)
	}
	if got := firedCount(&w); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
}

// The Runs hooks feed it: an inspect read watches, the frames decide.
func TestRuns_TheTripwireReadsInspectsAndFrames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames func(rs *Runs)
		want   int
	}{
		{name: "step_completed_beside_a_running_sibling", want: 1, frames: func(rs *Runs) {
			rs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
		}},
		{name: "sibling_paused_first", frames: func(rs *Runs) {
			rs.RunNodePaused(t.Context(), "wf_1", []string{"root", "build"})
			rs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
		}},
		{name: "step_restarted_beside_a_running_sibling", want: 1, frames: func(rs *Runs) {
			rs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: reviewPath, SessionID: "sess_step", Path: reviewSegments}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusPaused)
			br.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusRunning,
				tripReview("paused", "success"), tripBuild("running")))
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
	rs.signals.observe("wf_1", tripState(t, marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
	rs.observeComplete(t.Context(), "c-1", runNotif(methodWFRunComplete, map[string]any{"workflowId": "wf_1", "status": "paused"}))
	rs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
	if got := firedCount(&rs.signals); got != 0 {
		t.Errorf("count = %d, want 0: a paused run stopped every sibling", got)
	}
}

// Mark step done applies at once while a sibling runs; it is marotte's own end, not KAS's fix.
func TestEndStep_APersonsEndIsNoTripwireEvidence(t *testing.T) {
	h, _ := endHub(t, inspectAsk(t, "wf_1", marotte.RunStatusRunning, tripReview("paused", "success"), tripBuild("running")))
	if err := h.runs.endStep(t.Context(), "wf_1", reviewPath, runStepCompleted); err != nil {
		t.Fatalf("Setup: endStep = %v", err)
	}
	h.runs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
	if got := firedCount(&h.runs.signals); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// A parent agent's update_status on a signalled paused step reaches marotte as a bare node_complete,
// as KAS's own early apply would, and a client's answer restarts a step as KAS's early resume would:
// the line names both causes and asserts neither.
func TestRuns_TheTripwireLineLeavesTheCauseOpen(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		frame      func(rs *Runs)
	}{
		{name: "another_clients_end", want: tripwireLine, frame: func(rs *Runs) {
			rs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")
		}},
		{name: "a_resume", want: "a paused step resumed before the run settled; " + tripwireCause, frame: func(rs *Runs) {
			rs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: reviewPath, SessionID: "sess_step", Path: reviewSegments}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusPaused)
			br.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusRunning,
				tripReview("paused", "success"), tripBuild("running")))
			if _, err := h.runs.rawInspect(t.Context(), "wf_1"); err != nil {
				t.Fatalf("Setup: rawInspect: %v", err)
			}
			tc.frame(h.runs)
			out := logs.String()
			if !strings.Contains(out, `"msg":"`+tc.want+`"`) {
				t.Errorf("logs = %s, want the INFO line %q", out, tc.want)
			}
			if strings.Contains(out, "can go") {
				t.Errorf("logs = %s, want no line asserting the label can go", out)
			}
		})
	}
}
