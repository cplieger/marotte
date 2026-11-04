package agent

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// inspectHead is an inspect reply for wf_1 whose state carries `extra` beside its status.
func inspectHead(t *testing.T, status marotte.RunStatus, extra map[string]any) json.RawMessage {
	t.Helper()
	state := map[string]any{"workflowId": "wf_1", "status": status}
	maps.Copy(state, extra)
	raw, err := json.Marshal(map[string]any{"workflowId": "wf_1", "state": state})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

// headCases are the run states whose verbs KAS's state alone decides: the status, a pause asked for and
// not landed (`pausePending`), and a loop paused at its cap (`pause`).
var headCases = map[string]struct {
	extra   map[string]any
	status  marotte.RunStatus
	verbs   []string
	refused string
	node    string
}{
	"a paused run offers Resume": {
		status: marotte.RunStatusPaused, verbs: []string{verbResume, verbCancel},
	},
	"a running run whose pause has not landed refuses a second Pause": {
		status: marotte.RunStatusRunning, verbs: []string{verbCancel}, refused: pausePendingRefusal,
		extra: map[string]any{"pausePending": map[string]any{"initiator": "user", "requestedAt": "2026-11-03T15:13:03Z"}},
	},
	"a loop paused at its cap offers its own verbs": {
		status: marotte.RunStatusPaused, verbs: maxIterationsVerbs, node: "loop",
		extra: map[string]any{"pause": map[string]any{"kind": pauseKindMaxIterations, "nodeId": "loop"}},
	},
}

// blockedListHub is a hub whose `workflow/list` never answers until the test ends: an engine busy enough
// that reading every run takes longer than the page waits (the live run read 26-30 s per list).
func blockedListHub(t *testing.T, inspect json.RawMessage) *Runtime {
	t.Helper()
	h, _, br := newTestHub()
	release := make(chan struct{})
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspect,
		methodKiroWorkflowList:    parentlessRunList("wf_1"),
	}
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowList: release}
	t.Cleanup(func() {
		close(release)
		shutdownHub(t, h)
	})
	return h
}

// serve runs one handler with a deadline, failing when it waits past it.
func serve(t *testing.T, handle func(http.ResponseWriter, *http.Request), path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handle(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("GET %s waited past 5s on a blocked workflow/list: the verbs must come from the run's own inspect read", path)
	}
	return rec
}

func checkControls(t *testing.T, got marotte.RunControlsResponse, verbs []string, refused, node string) {
	t.Helper()
	if !slices.Equal(got.Verbs, verbs) {
		t.Errorf("verbs = %v, want %v", got.Verbs, verbs)
	}
	if got.Refused[verbPause] != refused {
		t.Errorf("refused pause = %q, want %q", got.Refused[verbPause], refused)
	}
	if got.PauseNodeID != node {
		t.Errorf("pause_node_id = %q, want %q", got.PauseNodeID, node)
	}
}

// TestHandleControls_DecidesFromTheInspectReadAlone pins that the controls answer reads the run's own state,
// never the workspace's run list.
func TestHandleControls_DecidesFromTheInspectReadAlone(t *testing.T) {
	for name, tc := range headCases {
		t.Run(name, func(t *testing.T) {
			h := blockedListHub(t, inspectHead(t, tc.status, tc.extra))
			rec := serve(t, h.runRoutes.handleControls, "/api/runs/wf_1/controls")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET controls = %d: %s", rec.Code, rec.Body.String())
			}
			var got marotte.RunControlsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding the controls reply: %s", err)
			}
			checkControls(t, got, tc.verbs, tc.refused, tc.node)
		})
	}
}

// TestHandleControls_APauseFieldOfAnotherShapeKeepsTheStatusVerbs pins that a kiro-cli release changing a
// pause field's type costs only that fact: the status still decides the verbs, Cancel included.
func TestHandleControls_APauseFieldOfAnotherShapeKeepsTheStatusVerbs(t *testing.T) {
	for name, tc := range map[string]struct {
		extra   map[string]any
		status  marotte.RunStatus
		verbs   []string
		refused string
	}{
		"a pausePending sent as a bool still reads as a pending pause": {
			status: marotte.RunStatusRunning, verbs: []string{verbCancel}, refused: pausePendingRefusal,
			extra: map[string]any{"pausePending": true},
		},
		"a pausePending sent as false reads as none": {
			status: marotte.RunStatusRunning, verbs: []string{verbPause, verbCancel},
			extra: map[string]any{"pausePending": false},
		},
		"a pause sent as a string leaves the plain paused verbs": {
			status: marotte.RunStatusPaused, verbs: []string{verbResume, verbCancel},
			extra: map[string]any{"pause": pauseKindMaxIterations},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := blockedListHub(t, inspectHead(t, tc.status, tc.extra))
			rec := serve(t, h.runRoutes.handleControls, "/api/runs/wf_1/controls")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET controls = %d: %s", rec.Code, rec.Body.String())
			}
			var got marotte.RunControlsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding the controls reply: %s", err)
			}
			checkControls(t, got, tc.verbs, tc.refused, "")
		})
	}
}

// TestHandleRun_CarriesTheControlsOfItsOwnRead pins that the run read states its verbs beside the state they
// were decided from, so a client never shows a paused run with the verbs of the running one.
func TestHandleRun_CarriesTheControlsOfItsOwnRead(t *testing.T) {
	for name, tc := range headCases {
		t.Run(name, func(t *testing.T) {
			h := blockedListHub(t, inspectHead(t, tc.status, tc.extra))
			rec := serve(t, h.runRoutes.handleRun, "/api/runs/wf_1")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET run = %d: %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Controls *marotte.RunControlsResponse `json:"controls"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding the run reply: %s", err)
			}
			if got.Controls == nil {
				t.Fatalf("the run read carries no controls: %s", rec.Body.String())
			}
			checkControls(t, *got.Controls, tc.verbs, tc.refused, tc.node)
		})
	}
}
