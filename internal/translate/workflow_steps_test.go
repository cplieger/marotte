package translate

// A step's frames pass through this package and their attribution is what identifies
// them; the run's idle window is the host's, so these tests assert what the host is
// TOLD rather than what it does about it. There is deliberately no per-step tool-call
// cap: a count measures work, not runaway.

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestReportStepProgress_EveryStepFrameNamesTheRun pins the ruling that replaced the
// per-step tool-call cap: a productive step is bounded by the run's idle window and the
// backstop alone, so three hundred consecutive frames tell the host three hundred times
// that the run is working, each naming the run. RunBoundsAccess carries no stop verb, so
// a cancel is unrepresentable here; what a count could still do is stop REPORTING.
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

// TestReportStepProgress_OnlyAStepFrameIsProgress: the chat's own frames and a SUBAGENT's
// name no run whose window could be refilled. A subagent's frames are the dangerous half:
// they arrive on a chat that may well have a live run, and crediting them would keep a
// genuinely wedged run alive on a different agent's work. A step the registry could not
// place has no run to credit either.
func TestReportStepProgress_OnlyAStepFrameIsProgress(t *testing.T) {
	tests := []struct {
		name string
		attr FrameAttribution
	}{
		{name: "the_chats_own_frame", attr: FrameAttribution{SessionID: "sess_chat"}},
		{name: "a_subagents_frame", attr: FrameAttribution{SubSessionID: "sub-1", SessionID: "sub-1"}},
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

// A step's ask is attributed by registry lookup: the run id is what lets a run tab render
// an ask that arrived elsewhere, the node id is what makes the card say who is asking. A
// frame with no session id is not a step and stamps empty strings, so a miss is not an error.
func TestStepRef_AttributesAnAskToItsRun(t *testing.T) {
	tests := []struct {
		name       string
		sessionID  string
		wantRunID  string
		wantNodeID string
	}{
		{name: "a_registered_step_session", sessionID: "sess_step", wantRunID: "wf_1", wantNodeID: "build"},
		{name: "no_session_id_at_all", sessionID: "", wantRunID: "", wantNodeID: ""},
		{name: "a_session_nothing_announced", sessionID: "sess_stranger", wantRunID: "", wantNodeID: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(rolesOf(deps))
			tr.RecordStepSession("sess_step", "wf_1", "build", "build")

			id := int64(7)
			tr.HandlePermissionRequest(t.Context(), "c1", &marotte.RPCResponse{
				ID: &id,
				Params: mustJSON(t, map[string]any{
					"sessionId": tc.sessionID,
					"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Read a file", "kind": "read"},
					"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
				}),
			})

			got, ok := findPermissionNeeded(t, events)
			if !ok {
				t.Fatal("no permission_needed event broadcast")
			}
			if got.RunID != tc.wantRunID {
				t.Errorf("permission_needed RunID for sessionId %q = %q, want %q", tc.sessionID, got.RunID, tc.wantRunID)
			}
			if got.NodeID != tc.wantNodeID {
				t.Errorf("permission_needed NodeID for sessionId %q = %q, want %q", tc.sessionID, got.NodeID, tc.wantNodeID)
			}
		})
	}
}
