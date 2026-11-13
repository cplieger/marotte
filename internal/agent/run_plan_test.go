package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
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
	if s.Text != "Plan update could not be applied: step 'x' names no agent" || s.Severity != "error" || s.OriginRun != "wf_1" {
		t.Errorf("note = %+v, want KAS's reason at error severity from wf_1", s)
	}
}

const kasRejectionID = `["wf_1","create-workflow","tc_7"]`

func wantRejectionID(kasID string) string {
	sum := sha256.Sum256([]byte(kasID))
	return "notify-wf_1:plan-rejection:" + hex.EncodeToString(sum[:8])
}

func TestRunPlanRejection_NotesTheLaunchingChat(t *testing.T) {
	for _, tc := range []struct{ name, kasID, reason, want string }{
		{"with a reason", kasRejectionID, "split step 2", "Plan edits requested: split step 2"},
		{"without one", `["wf_1","create-workflow","tc_8"]`, "", "Plan rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const chatID = marotte.ChatID("c1")
			h, cs, _ := runLoggingHub(t)
			seedChat(t, cs, chatID)

			h.runs.RunPlanRejection(t.Context(), chatID, "wf_1", tc.kasID, tc.reason)

			byID := steerEntries(t, cs, chatID)
			got := byID[wantRejectionID(tc.kasID)]
			if len(byID) != 1 || len(got) != 1 {
				t.Fatalf("notes = %+v, want one under %q", byID, wantRejectionID(tc.kasID))
			}
			s := got[0]
			if s.Text != tc.want || s.Severity != "" || s.OriginRun != "wf_1" || s.Origin != marotte.SteerOriginAgent {
				t.Errorf("note = %+v, want %q from wf_1, agent origin, no severity", s, tc.want)
			}
		})
	}
}

// A run chat cannot be stored, so a steer filed for one surfaces only as recordSteer's warning.
func TestRunPlanRejection_ARunChatTellsNoOne(t *testing.T) {
	h, _, _ := runLoggingHub(t)
	logs := captureLogs(t)

	h.runs.RunPlanRejection(t.Context(), runChatID("wf_1"), "wf_1", kasRejectionID, "split step 2")

	if out := logs.String(); strings.Contains(out, "steer was not recorded") {
		t.Errorf("a run chat was sent a note: %s", out)
	}
}

// The id is KAS's rejection id hashed, never a clock, so every Runs names one rejection alike.
func TestRunPlanRejection_OneRejectionOneID(t *testing.T) {
	const chatID = marotte.ChatID("c1")
	ids := make([][]string, 2)
	for i := range ids {
		h, cs, _ := runLoggingHub(t)
		seedChat(t, cs, chatID)
		h.runs.RunPlanRejection(t.Context(), chatID, "wf_1", kasRejectionID, "split step 2")
		ids[i] = slices.Collect(maps.Keys(steerEntries(t, cs, chatID)))
	}
	want := []string{wantRejectionID(kasRejectionID)}
	if !slices.Equal(ids[0], want) || !slices.Equal(ids[1], want) {
		t.Errorf("note ids = %v, want %v from each", ids, want)
	}
}
