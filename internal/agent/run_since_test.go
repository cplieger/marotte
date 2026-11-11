package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// lanesInspect is a running run of two lanes, each a sequence of build and review; `build` is
// lane-a's build status.
func lanesInspect(t *testing.T, build string) json.RawMessage {
	t.Helper()
	lane := func(id, buildStatus string) map[string]any {
		return map[string]any{"nodeId": id, "type": "sequence", "status": "running", "children": []any{
			map[string]any{"nodeId": "build", "type": "step", "status": buildStatus, "sessionId": "sess-" + id},
			map[string]any{"nodeId": "review", "type": "step", "status": "pending"},
		}}
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "root", "type": "parallel", "status": "running", "children": []any{
				lane("lane-a", build), lane("lane-b", "running"),
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// sinceReply is GET /api/runs/{id}?since=<rev>: the run's revision, the changed node records by
// path, and the run-log extras that moved.
type sinceReply struct {
	State    json.RawMessage               `json:"state"`
	StepEnds map[string]marotte.RunStepEnd `json:"step_ends"`
	Nodes    []struct {
		Node json.RawMessage `json:"node"`
		Path string          `json:"path"`
	} `json:"nodes"`
	Rev uint64 `json:"rev"`
}

func getRunAt(t *testing.T, h *Runtime, query string) (sinceReply, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1?"+query, nil)
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/wf_1?%s = %d: %s", query, rec.Code, rec.Body.String())
	}
	var out sinceReply
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return out, rec.Body.String()
}

func revOf(t *testing.T, h *Runtime) uint64 {
	t.Helper()
	whole, body := getRunAt(t, h, "")
	if whole.Rev == 0 {
		t.Fatalf("the run read carries no revision: %s", body)
	}
	return whole.Rev
}

// A changed step reaches a client as its own record and its containers', not as the whole run.
func TestHandleRun_SinceAnswersTheChangedStepAndItsContainers(t *testing.T) {
	h, br := hostedLeasedRun(t, lanesInspect(t, "running"))
	rev := revOf(t, h)
	br.setCallResult(methodKiroWorkflowInspect, lanesInspect(t, "completed"))
	readNow(t, h, "wf_1")

	got, body := getRunAt(t, h, "since="+strconv.FormatUint(rev, 10))

	if got.State != nil {
		t.Fatalf("?since answered the whole run: %.300s", body)
	}
	if got.Rev <= rev {
		t.Errorf("rev = %d, want past %d", got.Rev, rev)
	}
	paths := make([]string, 0, len(got.Nodes))
	for _, n := range got.Nodes {
		paths = append(paths, n.Path)
	}
	slices.Sort(paths)
	want := []string{
		workflow.PathKey([]string{"root"}),
		workflow.PathKey([]string{"root", "lane-a"}),
		workflow.PathKey([]string{"root", "lane-a", "build"}),
	}
	slices.Sort(want)
	if !slices.Equal(paths, want) {
		t.Errorf("records = %v, want the step and its two containers %v", paths, want)
	}
}

// A grade marotte records is part of the run read, so it moves the revision even when KAS's
// state did not change, and ?since carries it.
func TestHandleRun_AGradeAdvancesTheRevision(t *testing.T) {
	h, _ := hostedLeasedRun(t, lanesInspect(t, "completed"))
	h.runs.log = newRunLog(t.TempDir())
	path := []string{"root", "lane-a", "build"}
	build := workflow.PathKey(path)
	if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: build}, "c1"); err != nil {
		t.Fatal(err)
	}
	rev := revOf(t, h)
	h.runs.log.stopReason("wf_1", build, marotte.StopReasonToolUse)
	h.runs.RunNodeComplete(t.Context(), "wf_1", path, "completed", "")
	joinInflight(t, h)

	got, body := getRunAt(t, h, "since="+strconv.FormatUint(rev, 10))

	if got.Rev <= rev {
		t.Errorf("rev = %d after a grade, want past %d: %.300s", got.Rev, rev, body)
	}
	if _, ok := got.StepEnds[build]; !ok {
		t.Errorf("step_ends = %v, want the graded step's row", got.StepEnds)
	}
}

// The header view and a watch's capture read answer from the run's held read: neither asks the
// engine again.
func TestHandleRun_TheHeaderAndAWatchReadAnswerFromTheHeldRead(t *testing.T) {
	reply, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "root", "type": "sequence", "status": "running", "children": []any{
				map[string]any{"nodeId": "wait", "type": "watch", "status": "completed", "capturedOutput": "done"},
				map[string]any{"nodeId": "build", "type": "step", "status": "running", "sessionId": "sess-b"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, br := hostedLeasedRun(t, reply)
	_, wholeBody := getRun(t, h, "wf_1")
	var wholeControls struct {
		Controls json.RawMessage `json:"controls"`
	}
	if err := json.Unmarshal([]byte(wholeBody), &wholeControls); err != nil {
		t.Fatal(err)
	}
	br.forgetCalls()

	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, runViewReq("wf_1", "header"))
	var header struct {
		Controls json.RawMessage `json:"controls"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &header); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/steps/x/capture", nil)
	req.SetPathValue("id", "wf_1")
	req.SetPathValue("path", workflow.PathKey([]string{"root", "wait"}))
	capRec := httptest.NewRecorder()
	h.runRoutes.handleStepCapture(capRec, req)

	if br.called(methodKiroWorkflowInspect) {
		t.Error("the header view or the watch's capture read asked the engine again")
	}
	var capture marotte.RunStepCapture
	if err := json.Unmarshal(capRec.Body.Bytes(), &capture); err != nil || capture.CapturedOutput == nil ||
		*capture.CapturedOutput != "done" {
		t.Errorf("capture read = %s, want the watch's capture", capRec.Body.String())
	}
	if string(header.Controls) != string(wholeControls.Controls) || len(header.Controls) == 0 {
		t.Errorf("header controls = %s, want the whole read's %s", header.Controls, wholeControls.Controls)
	}
}

// A run's lifecycle frame makes the server re-read the run once and tell every client the new
// revision, so no client has to read the run to learn that it moved.
func TestRunRev_AFramePublishesTheNextRevision(t *testing.T) {
	h, br := hostedLeasedRun(t, lanesInspect(t, "running"))
	rev := revOf(t, h)
	br.setCallResult(methodKiroWorkflowInspect, lanesInspect(t, "completed"))

	h.bus.Broadcast(t.Context(), marotte.NewEvent(marotte.EventRunProgress, "", marotte.RunProgressPayload{
		WorkflowID: "wf_1", Kind: marotte.RunProgressNodeComplete,
	}))

	var got marotte.RunRevPayload
	waitFor(t, func() bool {
		for _, e := range eventsOfType(h, marotte.EventRunRev) {
			if json.Unmarshal(e.Payload, &got) == nil && got.Rev > rev {
				return true
			}
		}
		return false
	})
	if got.WorkflowID != "wf_1" || got.Status != "running" {
		t.Errorf("run_rev = %+v, want wf_1 running", got)
	}
	since, body := getRunAt(t, h, "since="+strconv.FormatUint(rev, 10))
	if since.Rev != got.Rev || len(since.Nodes) == 0 {
		t.Errorf("?since after run_rev %d = rev %d with %d records: %.300s", got.Rev, since.Rev, len(since.Nodes), body)
	}
}

// A revision the server does not hold (another process's, or one past the ring) answers the run whole.
func TestHandleRun_SinceAnUnknownRevisionAnswersWhole(t *testing.T) {
	h, _ := hostedLeasedRun(t, lanesInspect(t, "running"))
	rev := revOf(t, h)

	got, body := getRunAt(t, h, "since="+strconv.FormatUint(rev-1, 10))

	if got.State == nil || got.Nodes != nil || got.Rev != rev {
		t.Errorf("?since=<unknown> = %.300s, want the whole read at rev %d", body, rev)
	}
}

// A node the run no longer holds is named removed, and its parent comes with it.
func TestHandleRun_SinceNamesARemovedNode(t *testing.T) {
	h, br := hostedLeasedRun(t, lanesInspect(t, "running"))
	rev := revOf(t, h)
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "root", "type": "parallel", "status": "running", "children": []any{
				map[string]any{"nodeId": "lane-a", "type": "sequence", "status": "running"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	br.setCallResult(methodKiroWorkflowInspect, raw)
	readNow(t, h, "wf_1")

	var got struct {
		Removed []string `json:"removed"`
		Nodes   []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	}
	_, body := getRunAt(t, h, "since="+strconv.FormatUint(rev, 10))
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	removed := slices.Sorted(slices.Values(got.Removed))
	want := []string{
		workflow.PathKey([]string{"root", "lane-a", "build"}),
		workflow.PathKey([]string{"root", "lane-a", "review"}),
		workflow.PathKey([]string{"root", "lane-b"}),
		workflow.PathKey([]string{"root", "lane-b", "build"}),
		workflow.PathKey([]string{"root", "lane-b", "review"}),
	}
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if !slices.ContainsFunc(got.Nodes, func(n struct {
		Path string `json:"path"`
	},
	) bool {
		return n.Path == workflow.PathKey([]string{"root"})
	}) {
		t.Errorf("records = %v, want the removed nodes' parent", got.Nodes)
	}
}
