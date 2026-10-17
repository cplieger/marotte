package agent

import (
	"encoding/json"
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
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey(build), SessionID: "sess_b", Path: build}, "c1")

	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 3})
	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: "applied"})

	_, body := getRun(t, h, "wf_1")
	want := `"plan_update":{"after":"loop","outcome":"applied","pending":3}`
	if !strings.Contains(body, want) {
		t.Errorf("GET /api/runs/wf_1 = %s, want it to carry %s", body, want)
	}
}

// inspectQueued is a running wf_1's inspect reply with n queued top-level steps.
func inspectQueued(t *testing.T, n int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId":   "wf_1",
		"state":        map[string]any{"status": "running"},
		"pendingSteps": make([]struct{}, n),
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

func TestRunPlanUpdate_AQueuedPlanKASKeptShowsWithNoFrame(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, inspectQueued(t, 2))

	_, body := getRun(t, h, "wf_1")
	want := `"plan_update":{"outcome":"queued","pending":2}`
	if !strings.Contains(body, want) {
		t.Errorf("GET /api/runs/wf_1 = %s, want it to carry %s", body, want)
	}
}

func TestRunPlanUpdate_AQueuedRevisionKASDroppedIsNotShown(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))

	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 3})

	_, body := getRun(t, h, "wf_1")
	if strings.Contains(body, `"plan_update"`) {
		t.Errorf("GET /api/runs/wf_1 = %s, want no plan_update for a revision KAS no longer holds", body)
	}
}

func TestRunPlanUpdate_AnUnreadableCountServesMemory(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect,
		json.RawMessage(`{"workflowId":"wf_1","state":{"status":"running"},"pendingSteps":{"steps":2}}`))

	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 3})

	_, body := getRun(t, h, "wf_1")
	want := `"plan_update":{"outcome":"queued","pending":3}`
	if !strings.Contains(body, want) {
		t.Errorf("GET /api/runs/wf_1 = %s, want it to carry %s", body, want)
	}
}

func TestRunPlanUpdate_TheReplayedFrameKeepsTheStampedStep(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, inspectQueued(t, 3))
	h.runs.log = newRunLog(t.TempDir())
	build := []string{"wf_1", "loop", "iter-0", "build"}
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: workflow.PathKey(build), SessionID: "sess_b", Path: build}, "c1")
	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 3})
	h.runs.RunNodeComplete(t.Context(), "wf_1", build, "paused", "")

	h.runs.RunPlanUpdate(t.Context(), "c1", "wf_1", marotte.RunPlanUpdate{Outcome: marotte.RunPlanQueued, Pending: 3})

	_, body := getRun(t, h, "wf_1")
	want := `"plan_update":{"after":"loop","outcome":"queued","pending":3}`
	if !strings.Contains(body, want) {
		t.Errorf("GET /api/runs/wf_1 = %s, want it to carry %s", body, want)
	}
}

func TestRunPlanUpdate_ARejectionTellsTheLaunchingChat(t *testing.T) {
	const chatID = marotte.ChatID("c1")
	h, cs, _ := runLoggingHub(t)
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
