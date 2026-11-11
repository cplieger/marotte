package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// repeatInspect is a running run whose one lane repeat has run `passes` passes of a build step;
// the last pass's build is running.
func repeatInspect(t *testing.T, passes int) json.RawMessage {
	t.Helper()
	kids := make([]any, 0, passes)
	for i := range passes {
		status := "completed"
		if i == passes-1 {
			status = "running"
		}
		kids = append(kids, map[string]any{
			"nodeId": fmt.Sprintf("lane#%d", i), "type": "sequence", "iteration": i, "status": status,
			"children": []any{map[string]any{
				"nodeId": "build", "type": "step", "status": status, "sessionId": fmt.Sprintf("sess-%d", i),
				"startedAt": "2026-01-01T10:00:00Z", "capturedOutput": "built",
			}},
		})
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "wf_1", "type": "sequence", "status": "running", "children": []any{
				map[string]any{"nodeId": "lane", "type": "repeat", "status": "running", "children": kids},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type glanceReply struct {
	State struct {
		Root struct {
			Children []struct {
				Children []struct {
					NodeID string `json:"nodeId"`
				} `json:"children"`
				EarlierPasses *struct {
					Count int `json:"count"`
				} `json:"earlierPasses"`
			} `json:"children"`
		} `json:"root"`
	} `json:"state"`
	StepStarts map[string]marotte.RunStepStart `json:"step_starts"`
	Controls   json.RawMessage                 `json:"controls"`
	Rev        uint64                          `json:"rev"`
}

func getGlance(t *testing.T, h *Runtime) (glanceReply, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, runViewReq("wf_1", "glance"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?view=glance = %d: %s", rec.Code, rec.Body.String())
	}
	var out glanceReply
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the glance: %v", err)
	}
	return out, rec.Body.Len()
}

// The glance is what a run bar and a run card read: each repeat through its latest pass, with
// the passes before it counted, so its size follows the plan's width and not the run's history.
func TestHandleRun_TheGlanceViewKeepsEachRepeatsLatestPass(t *testing.T) {
	glance := func(passes int) (glanceReply, int) {
		h, _ := hostedLeasedRun(t, repeatInspect(t, passes))
		h.runs.log = newRunLog(t.TempDir())
		for i := range passes {
			path := workflow.PathKey([]string{"wf_1", "lane", fmt.Sprintf("iter-%d", i), "build"})
			if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1"); err != nil {
				t.Fatal(err)
			}
		}
		return getGlance(t, h)
	}

	small, smallBytes := glance(10)
	large, largeBytes := glance(1000)

	if len(large.State.Root.Children) != 1 {
		t.Fatalf("root children = %d, want the lane", len(large.State.Root.Children))
	}
	lane := large.State.Root.Children[0]
	if len(lane.Children) != 1 || lane.Children[0].NodeID != "lane#999" {
		t.Errorf("the lane holds %d passes, want only lane#999", len(lane.Children))
	}
	if lane.EarlierPasses == nil || lane.EarlierPasses.Count != 999 {
		t.Errorf("earlierPasses = %+v, want 999 passes counted", lane.EarlierPasses)
	}
	latest := workflow.PathKey([]string{"wf_1", "lane", "iter-999", "build"})
	if _, ok := large.StepStarts[latest]; !ok || len(large.StepStarts) != 1 {
		t.Errorf("step_starts holds %d rows, want only the latest pass's build", len(large.StepStarts))
	}
	if large.Rev == 0 || len(large.Controls) == 0 {
		t.Errorf("the glance lacks the read's rev or controls")
	}
	if p := small.State.Root.Children[0].EarlierPasses; p == nil || p.Count != 9 {
		t.Errorf("10 passes: earlierPasses = %+v, want 9 counted", p)
	}
	// The pass number's two more digits in a name and the count are all that differ.
	if diff := largeBytes - smallBytes; diff < 0 || diff > 16 {
		t.Errorf("the glance is %d bytes at 10 passes and %d at 1,000; want the same within the digits", smallBytes, largeBytes)
	}
}

// A glance of a run whose read is held answers from that read, as the header view does: a
// revision's glance costs no engine read, and it is the glance a fresh read gives.
func TestHandleRun_AWarmGlanceAnswersFromTheHeldRead(t *testing.T) {
	h, br := hostedLeasedRun(t, repeatInspect(t, 20))
	h.runs.log = newRunLog(t.TempDir())
	cold := httptest.NewRecorder()
	h.runRoutes.handleRun(cold, runViewReq("wf_1", "glance"))
	br.forgetCalls()

	warm := httptest.NewRecorder()
	h.runRoutes.handleRun(warm, runViewReq("wf_1", "glance"))

	if br.called(methodKiroWorkflowInspect) {
		t.Error("a warm glance asked the engine again")
	}
	if warm.Code != http.StatusOK || warm.Body.String() != cold.Body.String() {
		t.Errorf("warm glance = %d %.300s, want the fresh read's %.300s", warm.Code, warm.Body.String(), cold.Body.String())
	}
}
