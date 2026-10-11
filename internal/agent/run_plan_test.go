package agent

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

func TestRunPlanUpdate_TheRunReadCarriesTheRevisionThroughItsOutcome(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))
	h.runs.log = newRunLog(t.TempDir())
	build := []string{"wf_1", "loop", "iter-0", "build"}
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey(build), NodeID: "build", SessionID: "sess_b", Path: build}, "c1")

	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 3})
	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: "applied"})

	_, body := getRun(t, h, "wf_1")
	want := `"plan_update":{"after":"loop","outcome":"applied","pending":3}`
	if !strings.Contains(body, want) {
		t.Errorf("GET /api/runs/wf_1 = %s, want it to carry %s", body, want)
	}
}

func TestRunPlanUpdate_ARejectionTellsTheLaunchingChat(t *testing.T) {
	const chatID = marotte.ChatID("c1")
	h, cs := runLoggingHub(t)
	seedChat(t, cs, chatID)

	h.runs.RunPlanUpdate(t.Context(), chatID, "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 1})
	h.runs.RunPlanUpdate(t.Context(), chatID, "wf_1",
		marotte.RunPlanUpdate{Outcome: marotte.RunPlanRejected, Reason: "step 'x' names no agent"})
	h.runs.RunPlanUpdate(t.Context(), runChatID("wf_2"), "wf_2",
		marotte.RunPlanUpdate{Outcome: marotte.RunPlanRejected, Reason: "no chat to tell"})

	steers := chatSteers(t, cs, chatID)
	if len(steers) != 1 {
		t.Fatalf("got %d notes, want one", len(steers))
	}
	s := steers[0]
	if s.Text != "Plan update rejected: step 'x' names no agent" || s.Severity != "error" || s.OriginRun != "wf_1" {
		t.Errorf("note = %+v, want KAS's reason at error severity from wf_1", s)
	}
}
