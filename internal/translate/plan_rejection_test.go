package translate

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
)

const kasRejectionID = `["creator-1","tc_7",1]`

// rejectionBlock is KAS's workflowPlanRejection value as buildWorkflowPlanRejection writes it.
func rejectionBlock(reason string) map[string]any {
	b := map[string]any{
		"id": kasRejectionID, "optionKind": "reject_once",
		"workflowPlan": map[string]any{
			"workflowId": "wf_1", "nodeId": "create-workflow", "kind": "launch",
			"steps": []map[string]any{{"id": "build", "type": "step"}},
		},
		"origin": map[string]any{"sessionId": "creator-1"},
	}
	if reason != "" {
		b["rejectionReason"] = reason
	}
	return b
}

func deliverRejection(t *testing.T, deps *baseDeps, block map[string]any, attr FrameAttribution) {
	t.Helper()
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1", mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": map[string]any{"workflowPlanRejection": block}},
	}), attr)
}

func TestPlanRejection_ReachesTheLaunchingChat(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	deliverRejection(t, deps, rejectionBlock("split step 2"), FrameAttribution{SessionID: "parent-1"})

	want := []planRejectionCall{{chat: "c1", runID: "wf_1", kasID: kasRejectionID, reason: "split step 2"}}
	if len(deps.planRejections) != 1 || deps.planRejections[0] != want[0] {
		t.Errorf("rejections = %+v, want %+v", deps.planRejections, want)
	}
}

func TestPlanRejection_CensusIsQuiet(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	captureSlog(t, &logbuf)
	deps, _ := newEventCaptureDeps()
	deliverRejection(t, deps, rejectionBlock("split step 2"), FrameAttribution{SessionID: "parent-1"})
	if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
		t.Errorf("a plan rejection reported a member marotte declines: %s", out)
	}
}

func TestPlanRejection_AStepFrameTellsNoChat(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	deliverRejection(t, deps, rejectionBlock("split step 2"),
		FrameAttribution{SessionID: "step-1", RunID: "wf_0", NodePath: "wf_0/plan", NodeID: "plan", Step: true})
	if len(deps.planRejections) != 0 {
		t.Errorf("a step's frame reached the chat: %+v", deps.planRejections)
	}
}

func TestPlanRejection_NoRunNoNote(t *testing.T) {
	for name, edit := range map[string]func(b map[string]any){
		"no run": func(b map[string]any) { delete(b["workflowPlan"].(map[string]any), "workflowId") },
		"no id":  func(b map[string]any) { delete(b, "id") },
	} {
		t.Run(name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			b := rejectionBlock("split step 2")
			edit(b)
			deliverRejection(t, deps, b, FrameAttribution{SessionID: "parent-1"})
			if len(deps.planRejections) != 0 {
				t.Errorf("rejections = %+v, want none", deps.planRejections)
			}
		})
	}
}

func TestPlanRejection_TheReasonIsDefusedAndCapped(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	reason := "keep\nthe line\u202e" + strings.Repeat("é", 1200)
	deliverRejection(t, deps, rejectionBlock(reason), FrameAttribution{SessionID: "parent-1"})

	if len(deps.planRejections) != 1 {
		t.Fatalf("rejections = %+v, want one", deps.planRejections)
	}
	got := deps.planRejections[0].reason
	if strings.ContainsRune(got, '\u202e') || !strings.HasPrefix(got, "keep\nthe line ") {
		t.Errorf("reason = %q, want the Bidi override spaced out and the newline kept", got[:min(len(got), 40)])
	}
	if n := utf8.RuneCountInString(got); n > marotte.MaxRejectionReasonRunes {
		t.Errorf("reason has %d runes, want at most %d", n, marotte.MaxRejectionReasonRunes)
	}
}
