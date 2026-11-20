package translate

// These tests assert what the host is TOLD; the run's idle window is the host's. There is
// deliberately no per-step tool-call cap: a count measures work, not runaway.

import (
	"slices"
	"testing"
)

// TestReportStepProgress_EveryStepFrameNamesTheRun pins that every step frame reports the
// run as working; runBoundsAccess has no stop verb, so a cancel is unrepresentable here.
func TestReportStepProgress_EveryStepFrameNamesTheRun(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	attr := FrameAttribution{SessionID: "sess_step", RunID: "wf_1", NodePath: "wf/step-a", Step: true}

	for range 300 {
		tr.ReportStepProgress(attr)
	}

	if len(deps.runProgress) != 300 {
		t.Errorf("progress reports = %d, want one per frame, the 201st included", len(deps.runProgress))
	}
	if got := slices.Compact(slices.Clone(deps.runProgress)); !slices.Equal(got, []string{"wf_1"}) {
		t.Errorf("progress named %v, want the run wf_1 alone", got)
	}
}

// TestReportStepProgress_OnlyAStepFrameIsProgress pins that a chat's frames credit no run: its
// work would keep a wedged run alive. An unplaced step credits none.
func TestReportStepProgress_OnlyAStepFrameIsProgress(t *testing.T) {
	tests := []struct {
		name string
		attr FrameAttribution
	}{
		{name: "the_chats_own_frame", attr: FrameAttribution{SessionID: "sess_chat"}},
		{name: "a_step_with_no_run", attr: FrameAttribution{SessionID: "sess_step", Step: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			tr.ReportStepProgress(tc.attr)
			if len(deps.runProgress) != 0 {
				t.Errorf("ReportStepProgress(%+v) reported progress for %v, want none", tc.attr, deps.runProgress)
			}
		})
	}
}
