package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// stepRouteCase is one row of testdata/step_routes.json, the address oracle static-src/actions/runs.ts's
// step actions are held to as well.
type stepRouteCase struct {
	ID         string   `json:"id"`
	Desc       string   `json:"desc"`
	WorkflowID string   `json:"workflow_id"`
	NodePath   []string `json:"node_path"`
	StepURL    string   `json:"step_url"`
}

func stepRouteCases(t *testing.T) []stepRouteCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/step_routes.json")
	if err != nil {
		t.Fatalf("Setup: reading testdata/step_routes.json: %v", err)
	}
	var cases []stepRouteCase
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("Setup: testdata/step_routes.json = %d cases, err %v", len(cases), err)
	}
	return cases
}

func stepRouteHub(t *testing.T, tc stepRouteCase) (*Runtime, *fakeBridge, *http.ServeMux) {
	t.Helper()
	if len(tc.NodePath) != 2 {
		t.Fatalf("Setup: node path %q is not root, step", tc.NodePath)
	}
	rootID, stepID := tc.NodePath[0], tc.NodePath[1]
	h, _, br := newTestHub()
	h.runs.log = newRunLog(t.TempDir())
	h.bridge.mgr.insert(runChatID(tc.WorkflowID), &sharedBridge{bridge: br, state: bridgeIdle})
	br.setCallResult(methodKiroWorkflowInspect,
		stepTreeInspect(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning, rootID, stepID, "sess_step"))
	br.setCallResult(marotte.MethodSessionSteer, json.RawMessage(`{"queued":true}`))
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: tc.WorkflowID, NodePath: workflow.PathKey(tc.NodePath), NodeID: stepID, SessionID: "sess_step"}, "")
	mux := http.NewServeMux()
	(&runRoutes{runs: h.Runs()}).register(mux)
	return h, br, mux
}

func postStep(t *testing.T, mux *http.ServeMux, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Setup: marshalling the body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, reader))
	return rec
}

// releaseSteerClear answers the next _session/steer/clear with steerIDs and moves the carrier's read loop
// past its reply, the barrier a remove or a clear awaits.
func releaseSteerClear(t *testing.T, h *Runtime, br *fakeBridge, workflowID string, seq uint64, steerIDs ...string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"messageIds": steerIDs})
	if err != nil {
		t.Fatalf("Setup: marshalling the clear reply: %v", err)
	}
	br.setCallResult(marotte.MethodSessionSteerClear, raw)
	gen := h.coord.turns.attachForward(runChatID(workflowID))
	br.mu.Lock()
	br.deliveredSeq = seq
	br.mu.Unlock()
	h.coord.turns.observe(runChatID(workflowID), gen, seq)
}

// Every step verb reaches the execution the client's URL names, whatever the run's id and the node path's
// root are, through the routes the server registers.
func TestStepRoutes_ReachTheStepTheClientAddresses(t *testing.T) {
	for _, tc := range stepRouteCases(t) {
		t.Run(tc.ID, func(t *testing.T) {
			h, br, mux := stepRouteHub(t, tc)

			rec := postStep(t, mux, tc.StepURL+"/message", marotte.RunStepMessageRequest{Text: "hi", MessageID: "m-1"})
			var out marotte.RunStepMessageResponse
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil ||
				out.Verb != marotte.RunStepMessageSteer || out.SteerID != "steer-m-1" {
				t.Fatalf("%s (%s) POST %s/message = %d %s, want 200 steering steer-m-1",
					tc.ID, tc.Desc, tc.StepURL, rec.Code, rec.Body.String())
			}
			if got := br.paramsFor(marotte.MethodSessionSteer)["sessionId"]; got != marotte.SessionID("sess_step") {
				t.Errorf("%s: _session/steer sessionId = %v, want sess_step", tc.ID, got)
			}

			releaseSteerClear(t, h, br, tc.WorkflowID, 3, "steer-m-1")
			rec = postStep(t, mux, tc.StepURL+"/steer-remove", marotte.RunStepSteerRequest{SteerID: "steer-m-1"})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":"steer-m-1"`) {
				t.Errorf("%s: POST %s/steer-remove = %d %s, want 200 deleting steer-m-1",
					tc.ID, tc.StepURL, rec.Code, rec.Body.String())
			}

			if rec := postStep(t, mux, tc.StepURL+"/message", marotte.RunStepMessageRequest{Text: "again", MessageID: "m-2"}); rec.Code != http.StatusOK {
				t.Fatalf("Setup: %s second message = %d %s", tc.ID, rec.Code, rec.Body.String())
			}
			releaseSteerClear(t, h, br, tc.WorkflowID, 6, "steer-m-2")
			rec = postStep(t, mux, tc.StepURL+"/steer-clear", nil)
			if rec.Code != http.StatusOK {
				t.Errorf("%s: POST %s/steer-clear = %d %s, want 200", tc.ID, tc.StepURL, rec.Code, rec.Body.String())
			}

			get := httptest.NewRecorder()
			mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, tc.StepURL, nil))
			if get.Code != http.StatusOK {
				t.Errorf("%s: GET %s = %d %s, want 200", tc.ID, tc.StepURL, get.Code, get.Body.String())
			}
		})
	}
}

// A node path is a step of the run only when the run's own tree holds it.
func TestStepRoutes_APathTheRunsTreeDoesNotHoldIsUnknown(t *testing.T) {
	tc := stepRouteCases(t)[0]
	_, br, mux := stepRouteHub(t, tc)

	rec := postStep(t, mux, "/api/runs/"+tc.WorkflowID+"/steps/wf_other%3Acoder/message",
		marotte.RunStepMessageRequest{Text: "hi", MessageID: "m-1"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST a foreign path's message = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if params := br.paramsFor(marotte.MethodSessionSteer); params != nil {
		t.Errorf("_session/steer params = %v, want nothing sent", params)
	}
	for _, verb := range []string{"steer-remove", "steer-clear"} {
		rec := postStep(t, mux, "/api/runs/"+tc.WorkflowID+"/steps/wf_other%3Acoder/"+verb,
			marotte.RunStepSteerRequest{SteerID: "steer-x"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST a foreign path's %s = %d %s, want 404", verb, rec.Code, rec.Body.String())
		}
	}
}
