package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// inspectWithCaptures is a running run of `steps` finished steps, each with a captured output of
// `size` characters over several lines.
func inspectWithCaptures(t *testing.T, workflowID string, steps, size int) json.RawMessage {
	t.Helper()
	line := strings.Repeat("x", 99) + "\n"
	capture := strings.Repeat(line, size/len(line)+1)[:size]
	children := make([]any, 0, steps)
	for i := range steps {
		children = append(children, map[string]any{
			"nodeId": fmt.Sprintf("s%d", i), "type": "step", "status": "completed",
			"sessionId": fmt.Sprintf("sess-%d", i), "capturedOutput": capture,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"workflowId": workflowID, "status": "running", "inputs": map[string]string{"repo": "marotte"},
			"root": map[string]any{"nodeId": workflowID, "type": "sequence", "status": "running", "children": children},
		},
		"nodePlan": map[string]any{"nodeId": workflowID, "type": "sequence"},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

func runViewReq(id, view string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"?view="+view, nil)
	req.SetPathValue("id", id)
	return req
}

// The run read serves each step's capture summary, not its whole captured output, which only the
// step read carries.
func TestHandleRun_ServesACaptureSummaryPerNode(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithCaptures(t, "wf_1", 3, 5000))

	reply, body := getRun(t, h, "wf_1")

	var state struct {
		Root struct {
			Children []map[string]json.RawMessage `json:"children"`
		} `json:"root"`
	}
	if err := json.Unmarshal(reply.State, &state); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	if len(state.Root.Children) != 3 {
		t.Fatalf("children = %d, want 3: %s", len(state.Root.Children), body)
	}
	for i, node := range state.Root.Children {
		if _, ok := node["capturedOutput"]; ok {
			t.Errorf("node %d carries its whole capturedOutput", i)
		}
		var summary string
		if err := json.Unmarshal(node["captureSummary"], &summary); err != nil || summary == "" {
			t.Errorf("node %d captureSummary = %s, want the collapsed row's text", i, node["captureSummary"])
		}
		if len(summary) > captureSummaryLimit {
			t.Errorf("node %d captureSummary is %d characters, want at most %d", i, len(summary), captureSummaryLimit)
		}
		var has bool
		if err := json.Unmarshal(node["hasCapture"], &has); err != nil || !has {
			t.Errorf("node %d hasCapture = %s, want true", i, node["hasCapture"])
		}
	}
}

// The capture read is where a step's whole captured output comes from: the result pane of the
// shown step draws it, and the run read carries only its summary.
func TestStepCapture_AnswersTheStepsWholeCapture(t *testing.T) {
	capture := strings.Repeat("a long report line\n", 300)
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "wf_1", "type": "sequence", "status": "running", "children": []any{
				map[string]any{"nodeId": "build", "type": "step", "status": "completed", "sessionId": "sess-b", "capturedOutput": capture},
				map[string]any{"nodeId": "quiet", "type": "step", "status": "completed", "sessionId": "sess-q"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := hostedLeasedRun(t, raw)
	read := func(nodeID string) *httptest.ResponseRecorder {
		path := workflow.PathKey([]string{"wf_1", nodeID})
		req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/steps/x/capture", nil)
		req.SetPathValue("id", "wf_1")
		req.SetPathValue("path", path)
		rec := httptest.NewRecorder()
		h.runRoutes.handleStepCapture(rec, req)
		return rec
	}

	t.Run("a step that captured", func(t *testing.T) {
		rec := read("build")
		if rec.Code != http.StatusOK {
			t.Fatalf("capture read = %d: %s", rec.Code, rec.Body.String())
		}
		var out marotte.RunStepCapture
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.CapturedOutput == nil || *out.CapturedOutput != capture {
			t.Errorf("captured_output = %v, want the step's whole %d characters", out.CapturedOutput, len(capture))
		}
	})
	t.Run("a step that captured nothing", func(t *testing.T) {
		rec := read("quiet")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "captured_output") {
			t.Errorf("capture read = %d %s, want 200 with no captured_output", rec.Code, rec.Body.String())
		}
	})
	t.Run("a path the run does not name", func(t *testing.T) {
		if rec := read("other"); rec.Code != http.StatusNotFound {
			t.Errorf("capture read = %d, want 404", rec.Code)
		}
	})
}

// A summary is what the collapsed row shows: the first three lines (two shown, the third so the
// clamp marks the cut where the whole capture puts it), cut on a rune at the limit.
func TestCaptureSummary(t *testing.T) {
	long := strings.Repeat("é", 300)
	cases := []struct {
		name, in, want string
	}{
		{name: "one line", in: "done", want: "done"},
		{name: "three lines and more", in: "a\nb\nc\nd\ne", want: "a\nb\nc"},
		{name: "a trailing newline", in: "a\nb\n", want: "a\nb\n"},
		{name: "a line past the limit", in: long, want: long[:captureSummaryLimit]},
		{name: "a rune across the limit", in: "x" + long, want: ("x" + long)[:captureSummaryLimit-1]},
		{name: "empty", in: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := captureSummary(c.in); got != c.want {
				t.Errorf("captureSummary(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// A watch's capture is the structured record its row decodes (its subtitle and facts), so the run
// read keeps it whole.
func TestHandleRun_KeepsAWatchsCapture(t *testing.T) {
	capture := `{"status":"exited","exitCode":0,"outputTail":"ok 1\nok 2"}`
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "wf_1", "type": "sequence", "status": "running", "children": []any{
				map[string]any{"nodeId": "wait", "type": "watch", "status": "completed", "capturedOutput": capture},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowInspect, raw)

	reply, _ := getRun(t, h, "wf_1")

	var state struct {
		Root struct {
			Children []struct {
				CapturedOutput string `json:"capturedOutput"`
			} `json:"children"`
		} `json:"root"`
	}
	if err := json.Unmarshal(reply.State, &state); err != nil || len(state.Root.Children) != 1 {
		t.Fatalf("decoding state: %v", err)
	}
	if state.Root.Children[0].CapturedOutput != capture {
		t.Errorf("the watch's capturedOutput = %q, want %q", state.Root.Children[0].CapturedOutput, capture)
	}
}

// The header view is what a tab dot, a chat tab's mark and a steer note's label read: the run's
// status and name with no node tree, so its size is the same for any run.
func TestHandleRun_TheHeaderViewCarriesNoTree(t *testing.T) {
	header := func(steps int) string {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectWithCaptures(t, "wf_1", steps, 200))
		rec := httptest.NewRecorder()
		h.runRoutes.handleRun(rec, runViewReq("wf_1", "header"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET ?view=header = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	small, large := header(10), header(10_000)

	var reply map[string]json.RawMessage
	if err := json.Unmarshal([]byte(large), &reply); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nodePlan", "step_starts", "step_ends"} {
		if _, ok := reply[key]; ok {
			t.Errorf("the header view carries %s", key)
		}
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(reply["state"], &state); err != nil {
		t.Fatalf("decoding state: %v (%s)", err, large)
	}
	if _, ok := state["root"]; ok {
		t.Error("the header view carries the node tree")
	}
	if string(state["status"]) != `"running"` {
		t.Errorf("state.status = %s, want running", state["status"])
	}
	if len(small) != len(large) {
		t.Errorf("the header is %d bytes for 10 nodes and %d for 10,000; want the same", len(small), len(large))
	}
}
