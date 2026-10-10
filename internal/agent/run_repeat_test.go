package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestAffordance_AMaxIterationsPauseOffersExtendAndFinishNotResume(t *testing.T) {
	cases := []struct {
		name  string
		facts runFacts
		want  []string
		node  string
	}{
		{
			name: "a repeat at its cap",
			facts: runFacts{
				status: "paused", hosted: true,
				pauseKind: pauseKindMaxIterations, pauseNodeID: "loop",
			},
			want: []string{verbExtend, verbFinishLoop, verbCancel},
			node: "loop",
		},
		{
			name:  "an ordinary pause",
			facts: runFacts{status: "paused", hosted: true},
			want:  []string{verbResume, verbCancel},
		},
		{
			// The pause kind is stale once the run moves on.
			name: "a running run that once paused at a cap",
			facts: runFacts{
				status: "running", hosted: true,
				pauseKind: pauseKindMaxIterations, pauseNodeID: "loop",
			},
			want: []string{verbPause, verbCancel},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := affordanceOf(&tc.facts)
			if !slices.Equal(got.Verbs, tc.want) {
				t.Errorf("affordanceOf(%+v).Verbs = %v, want %v", tc.facts, got.Verbs, tc.want)
			}
			if got.PauseNodeID != tc.node {
				t.Errorf("affordanceOf(%+v).PauseNodeID = %q, want %q", tc.facts, got.PauseNodeID, tc.node)
			}
		})
	}
}

// Both repeat verbs load an unregistered run themselves, so a pre-restart pause keeps them.
func TestAffordance_AMaxIterationsPauseIsOfferedUnhosted(t *testing.T) {
	got := affordanceOf(&runFacts{
		status: "paused", pauseKind: pauseKindMaxIterations, pauseNodeID: "loop",
	})
	want := []string{verbExtend, verbFinishLoop, verbCancel}
	if !slices.Equal(got.Verbs, want) {
		t.Errorf("unhosted maxIterations pause Verbs = %v, want %v", got.Verbs, want)
	}
}

// An offered verb needs a route, or its button 404s.
func TestMaxIterationsVerbsHaveRoutes(t *testing.T) {
	h, _, _ := newTestHub()
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: h.Epoch}).register(mux)
	paths := map[string]string{verbExtend: "/api/runs/wf_1/extend", verbFinishLoop: "/api/runs/wf_1/finish-loop"}
	for _, verb := range maxIterationsVerbs {
		if verb == verbCancel {
			continue
		}
		path, ok := paths[verb]
		if !ok {
			t.Errorf("maxIterationsVerbs offers %q and the test names no route for it", verb)
			continue
		}
		req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("POST %s has no route, so %q cannot be issued", path, verb)
		}
	}
}

func TestExtendRepeat_SendsNodeAndCountOnResume(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.extendRepeat(t.Context(), "wf_1", "loop", 5); err != nil {
		t.Fatalf("ExtendRepeat = %v, want nil", err)
	}
	params := br.paramsFor(methodKiroWorkflowResume)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", methodKiroWorkflowResume, br.callLog())
	}
	ext, ok := params["extendRepeat"].(map[string]any)
	if !ok {
		t.Fatalf(`resume params["extendRepeat"] = %v, want an object`, params["extendRepeat"])
	}
	if ext["nodeId"] != "loop" {
		t.Errorf(`extendRepeat.nodeId = %v, want "loop"`, ext["nodeId"])
	}
	if ext["additionalIterations"] != 5 {
		t.Errorf("extendRepeat.additionalIterations = %v, want 5", ext["additionalIterations"])
	}
}

func TestExtendRepeat_RefusesACountOutsideTheCap(t *testing.T) {
	for _, n := range []int{0, -1, maxExtendIterations + 1} {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		if err := h.runs.extendRepeat(t.Context(), "wf_1", "loop", n); err == nil {
			t.Errorf("ExtendRepeat(n=%d) = nil, want an error", n)
		}
		if br.paramsFor(methodKiroWorkflowResume) != nil {
			t.Errorf("ExtendRepeat(n=%d) sent a resume, want none", n)
		}
	}
}

func TestFinishRepeat_SendsNodeIdAndCompleted(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.finishRepeat(t.Context(), "wf_1", "loop"); err != nil {
		t.Fatalf("FinishRepeat = %v, want nil", err)
	}
	params := br.paramsFor(methodKiroWorkflowUpdate)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", methodKiroWorkflowUpdate, br.callLog())
	}
	for key, want := range map[string]any{
		keyWorkflowID: "wf_1", "action": updateStatusAction, "status": runStepCompleted, "nodeId": "loop",
	} {
		if params[key] != want {
			t.Errorf("update params[%q] = %v, want %v", key, params[key], want)
		}
	}
}

func TestFinishRepeat_AnUpdatedFalseIsARefusal(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowUpdate: json.RawMessage(
			`{"workflowId":"wf_1","updated":false,"message":"Cannot update node 'loop': it is not a paused repeat."}`,
		),
	}

	err := h.runs.finishRepeat(t.Context(), "wf_1", "loop")
	if !errors.Is(err, errStepStatusRefused) {
		t.Fatalf("FinishRepeat = %v, want errStepStatusRefused", err)
	}
	if !strings.Contains(err.Error(), "not a paused repeat") {
		t.Errorf("FinishRepeat error = %q, want KAS's reason", err)
	}
}

// TestSetStepStatus_NeverSendsNodeId pins that KAS refuses a nodeId on an ordinary step write.
func TestSetStepStatus_NeverSendsNodeId(t *testing.T) {
	h, _, br := newTestHub()
	addressableStep(t, h, br, "wf_1", "review")
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.setStepStatus(t.Context(), "wf_1", "review", runStepCompleted); err != nil {
		t.Fatalf("SetStepStatus = %v, want nil", err)
	}
	if got, ok := br.paramsFor(methodKiroWorkflowUpdate)["nodeId"]; ok {
		t.Errorf(`SetStepStatus params["nodeId"] = %v, want absent`, got)
	}
}

func maxIterationsHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusPaused, "Repeat 'loop' reached maxIterations."),
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "nightly", "status": "paused",
			"pauseKind": "maxIterations", "pauseNodeId": "loop",
		}),
	}
	return h, br
}

func TestHandleControls_CarriesThePausedRepeat(t *testing.T) {
	h, _ := maxIterationsHub(t)
	rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/controls", http.NoBody)
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	rr.handleControls(rec, req)

	var got marotte.RunControlsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode controls %s: %v", rec.Body.String(), err)
	}
	if got.PauseNodeID != "loop" {
		t.Errorf("controls pause_node_id = %q, want %q", got.PauseNodeID, "loop")
	}
	if want := []string{verbExtend, verbFinishLoop, verbCancel}; !slices.Equal(got.Verbs, want) {
		t.Errorf("controls verbs = %v, want %v", got.Verbs, want)
	}
}

func TestHandleExtend_ARefusalIsA409WithKASsSentence(t *testing.T) {
	h, br := maxIterationsHub(t)
	br.callErrs = map[string]error{methodKiroWorkflowResume: &marotte.RPCError{
		Code: -32603, Message: "Repeat 'loop' has no definition in the workflow plan.",
	}}
	rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/extend",
		strings.NewReader(`{"node_id":"loop","iterations":3}`))
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	rr.handleExtend(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("extend status = %d, want %d (body %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no definition in the workflow plan") {
		t.Errorf("extend body = %s, want KAS's sentence", rec.Body.String())
	}
}

func TestHandleExtend_RefusedOnAnOrdinaryPause(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusPaused, ""),
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "nightly", "status": "paused",
		}),
	}
	rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/extend",
		strings.NewReader(`{"node_id":"loop","iterations":3}`))
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	rr.handleExtend(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("extend on an ordinary pause status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if br.paramsFor(methodKiroWorkflowResume) != nil {
		t.Error("extend on an ordinary pause sent a resume, want none")
	}
}

func TestHandleExtend_ACountOutsideTheCapIsA400BeforeAnyCall(t *testing.T) {
	for _, n := range []int{0, 1001} {
		h, br := maxIterationsHub(t)
		rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
		body := `{"node_id":"loop","iterations":` + strconv.Itoa(n) + `}`
		req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/extend", strings.NewReader(body))
		req.SetPathValue("id", "wf_1")
		rec := httptest.NewRecorder()

		rr.handleExtend(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("extend iterations=%d status = %d, want %d", n, rec.Code, http.StatusBadRequest)
		}
		if len(br.callLog()) != 0 {
			t.Errorf("extend iterations=%d made calls %v, want none", n, br.callLog())
		}
	}
}

func TestHandleFinishLoop_AnUpdatedFalseIsA409WithKASsSentence(t *testing.T) {
	h, br := maxIterationsHub(t)
	br.callResults[methodKiroWorkflowUpdate] = json.RawMessage(
		`{"workflowId":"wf_1","updated":false,"message":"Cannot update node 'loop': it is not a paused repeat."}`,
	)
	rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/finish-loop",
		strings.NewReader(`{"node_id":"loop"}`))
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	rr.handleFinishLoop(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("finish-loop status = %d, want %d (body %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not a paused repeat") {
		t.Errorf("finish-loop body = %s, want KAS's sentence", rec.Body.String())
	}
}
