package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// stepEndCase is one row of testdata/step_end_cases.json, the table the run tab's endable flag
// (run-exec-source.ts) is held to as well.
type stepEndCase struct {
	ID   string `json:"id"`
	Run  string `json:"run"`
	Node string `json:"node"`
	Type string `json:"type"`
	// Sibling is another step's status beside the target, Twin another execution of the target's node id.
	Sibling string `json:"sibling"`
	Twin    string `json:"twin"`
	Asked   bool   `json:"asked"`
	Endable bool   `json:"endable"`
}

// targetPath is the path of the step stepEndTree puts under its root.
var targetPath = workflow.PathKey([]string{"root", "target"})

// stepEndTree is wf_1's inspect reply for c: the target under a parallel root, a sibling beside it, and its twin in a
// branch of its own.
func stepEndTree(t *testing.T, c *stepEndCase) json.RawMessage {
	t.Helper()
	target := map[string]any{"nodeId": "target", "type": c.Type, "status": c.Node}
	if c.Node != "pending" {
		target["sessionId"] = "sess_target"
	}
	if c.Asked {
		target["completionSignal"] = "need_input"
		target["attention"] = []any{map[string]any{"kind": "question", "since": "2026-11-01T09:58:33.463Z"}}
	}
	kids := []any{target}
	if c.Sibling != "" {
		kids = append(kids, map[string]any{"nodeId": "sibling", "type": stepNodeType, "status": c.Sibling, "sessionId": "sess_sibling"})
	}
	if c.Twin != "" {
		kids = append(kids, map[string]any{"nodeId": "branch", "type": "sequence", "status": c.Twin, "children": []any{
			map[string]any{"nodeId": "target", "type": stepNodeType, "status": c.Twin, "sessionId": "sess_twin"},
		}})
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": c.Run,
			"root": map[string]any{"nodeId": "root", "type": "parallel", "status": c.Run, "children": kids},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %v", err)
	}
	return raw
}

// endHub is wf_1 hosted and leased, with its cell, inspect answering reply and every update taken.
func endHub(t *testing.T, reply json.RawMessage) (*Runtime, *fakeBridge) {
	t.Helper()
	h, br, _ := askCoreHub(t, "wf_1", reply)
	br.setCallResult(methodKiroWorkflowUpdate, json.RawMessage(`{"workflowId":"wf_1","updated":true}`))
	return h, br
}

func TestEndStep_TheAvailabilityTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/step_end_cases.json")
	if err != nil {
		t.Fatalf("Setup: reading testdata/step_end_cases.json: %v", err)
	}
	var cases []stepEndCase
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("Setup: testdata/step_end_cases.json = %d cases, err %v", len(cases), err)
	}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			h, br := endHub(t, stepEndTree(t, &c))
			err := h.runs.endStep(t.Context(), "wf_1", targetPath, runStepCompleted)
			sent := countCalls(br, methodKiroWorkflowUpdate)
			if c.Endable && (err != nil || sent != 1) {
				t.Errorf("end = %v with %d updates sent, want one update", err, sent)
			}
			if !c.Endable && (err == nil || sent != 0) {
				t.Errorf("end = %v with %d updates sent, want a refusal and none", err, sent)
			}
		})
	}
}

func TestEndStepSendsUpdateStatusWithNodeID(t *testing.T) {
	for _, status := range []string{runStepCompleted, runStepFailed} {
		t.Run(status, func(t *testing.T) {
			h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "paused", Node: "paused", Type: stepNodeType}))
			if err := h.runs.endStep(t.Context(), "wf_1", targetPath, status); err != nil {
				t.Fatalf("end = %v, want nil", err)
			}
			params := br.paramsFor(methodKiroWorkflowUpdate)
			for key, want := range map[string]any{
				keyWorkflowID: "wf_1", "action": updateStatusAction, "status": status, "nodeId": "target",
			} {
				if params[key] != want {
					t.Errorf("update params[%q] = %v, want %v", key, params[key], want)
				}
			}
		})
	}
}

func TestEndStepRefusesUnpausedStep(t *testing.T) {
	for _, node := range []string{"running", "completed"} {
		t.Run(node, func(t *testing.T) {
			h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "running", Node: node, Type: stepNodeType}))
			err := h.runs.endStep(t.Context(), "wf_1", targetPath, runStepCompleted)
			if got := personAnswer(err); got != string(marotte.RunStepEndNotPaused) {
				t.Errorf("end = %q, want %s", got, marotte.RunStepEndNotPaused)
			}
			if n := countCalls(br, methodKiroWorkflowUpdate); n != 0 {
				t.Errorf("updates sent = %d, want none", n)
			}
		})
	}
}

func TestEndStepRefusesAPathTheRunDoesNotHold(t *testing.T) {
	h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "paused", Node: "paused", Type: stepNodeType}))
	err := h.runs.endStep(t.Context(), "wf_1", workflow.PathKey([]string{"root", "other"}), runStepCompleted)
	if err == nil || countCalls(br, methodKiroWorkflowUpdate) != 0 {
		t.Errorf("end of an unknown path = %v with %d updates, want a refusal and none", err,
			countCalls(br, methodKiroWorkflowUpdate))
	}
}

func TestEndStepRefusesDuplicatePausedNodeID(t *testing.T) {
	h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "paused", Node: "paused", Type: stepNodeType, Twin: "paused"}))
	err := h.runs.endStep(t.Context(), "wf_1", targetPath, runStepCompleted)
	if got := personAnswer(err); got != string(marotte.RunStepEndUnnamed) {
		t.Errorf("end = %q, want %s", got, marotte.RunStepEndUnnamed)
	}
	if n := countCalls(br, methodKiroWorkflowUpdate); n != 0 {
		t.Errorf("updates sent = %d, want none: KAS would end the first paused execution by id", n)
	}
}

func TestEndStepRunsWhileASiblingRuns(t *testing.T) {
	h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "running", Node: "paused", Type: stepNodeType, Sibling: "running"}))
	if err := h.runs.endStep(t.Context(), "wf_1", targetPath, runStepFailed); err != nil {
		t.Fatalf("end = %v, want nil", err)
	}
	if n := countCalls(br, methodKiroWorkflowUpdate); n != 1 {
		t.Errorf("updates sent = %d, want 1", n)
	}
}

func TestEndStepMapsKASDecline(t *testing.T) {
	h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "running", Node: "paused", Type: stepNodeType}))
	const decline = "Cannot update node 'target': step 'target' is running, not paused; only a paused step can be ended with a nodeId."
	br.setCallResult(methodKiroWorkflowUpdate, json.RawMessage(`{"workflowId":"wf_1","updated":false,"message":"`+decline+`"}`))
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)

	rec := postStep(t, mux, "/api/runs/wf_1/steps/"+url.PathEscape(targetPath)+"/end", map[string]string{"status": "completed"})

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "is running, not paused") {
		t.Errorf("body = %s, want KAS's sentence", rec.Body.String())
	}
}

func TestEndStep_TheRouteRefusesAStatusKASRefuses(t *testing.T) {
	for _, body := range []string{`{"status":"running"}`, `{}`} {
		t.Run(body, func(t *testing.T) {
			h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "paused", Node: "paused", Type: stepNodeType}))
			mux := http.NewServeMux()
			(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
			var raw json.RawMessage = []byte(body)
			rec := postStep(t, mux, "/api/runs/wf_1/steps/"+url.PathEscape(targetPath)+"/end", raw)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if n := countCalls(br, methodKiroWorkflowUpdate); n != 0 {
				t.Errorf("updates sent = %d, want none", n)
			}
		})
	}
}

// An end goes out behind marotte's own sends to the run: refused while an answer is in flight or a stop is queued.
func TestEndStep_WaitsOnTheRunsOwnSends(t *testing.T) {
	for _, tc := range []struct {
		hold func(ctx context.Context, h *Runtime, c *askCell)
		name string
		want string
	}{
		{
			name: "an answer in flight", want: string(marotte.RunAnswerPending),
			hold: func(ctx context.Context, h *Runtime, c *askCell) { h.runs.registerPending(ctx, c) },
		},
		{
			name: "a stop queued", want: string(marotte.RunAskRefusedStopping),
			hold: func(ctx context.Context, h *Runtime, c *askCell) { h.runs.setStopping(ctx, c, true) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "paused", Node: "paused", Type: stepNodeType}))
			tc.hold(t.Context(), h, h.runs.cellFor("wf_1"))
			err := h.runs.endStep(t.Context(), "wf_1", targetPath, runStepCompleted)
			if got := personAnswer(err); got != tc.want {
				t.Errorf("end = %q, want %s", got, tc.want)
			}
			if n := countCalls(br, methodKiroWorkflowUpdate); n != 0 {
				t.Errorf("updates sent = %d, want none", n)
			}
		})
	}
}

// A resume that reaches the run while a person's end of its step is between its read and its write reads the run
// after the end: the two never interleave under the run's resume lock.
func TestEndStep_AResumeWaitsForTheEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br := endHub(t, stepEndTree(t, &stepEndCase{Run: "paused", Node: "paused", Type: stepNodeType}))
		br.setCallResult(methodKiroWorkflowResume, json.RawMessage(`{}`))
		release, reads := heldTransition(t, h, personEnd)

		var endErr, resumeErr error
		endDone, resumeDone := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(endDone)
			endErr = h.runs.endStep(t.Context(), "wf_1", targetPath, runStepCompleted)
		}()
		synctest.Wait()
		go func() { defer close(resumeDone); resumeErr = h.runs.resume(t.Context(), "wf_1", callerPage) }()
		synctest.Wait()
		if n := reads(); n != 1 {
			t.Errorf("reads while the end is held between its read and its write = %d, want its own alone", n)
		}
		release()
		<-endDone
		<-resumeDone
		if endErr != nil || resumeErr != nil {
			t.Fatalf("end = %v, resume = %v, want both nil", endErr, resumeErr)
		}
		if got := stateWrites(br.callLog()); len(got) != 2 || got[0] != methodKiroWorkflowUpdate {
			t.Errorf("writes = %v, want the end's update before the resume", got)
		}
	})
}
