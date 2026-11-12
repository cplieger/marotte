package agent

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// wideRunInspect is a running run of lanes parallel sequences of steps steps each.
func wideRunInspect(t testing.TB, lanes, steps int) json.RawMessage {
	t.Helper()
	kids := make([]any, 0, lanes)
	for l := range lanes {
		seq := make([]any, 0, steps)
		for s := range steps {
			seq = append(seq, map[string]any{"nodeId": fmt.Sprintf("s%d", s), "type": "step", "status": "completed"})
		}
		kids = append(kids, map[string]any{
			"nodeId": fmt.Sprintf("lane-%d", l), "type": "sequence", "status": "running", "children": seq,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": "running",
			"root": map[string]any{"nodeId": "root", "type": "parallel", "status": "running", "children": kids},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// bytesPer is the heap the process allocates per call of f, over n calls.
func bytesPer(n int, f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

// liveCost is what the server allocates for a held run of lanes×100 steps, per step turn frame it
// observes and per ?since read a client makes at the current revision.
func liveCost(t *testing.T, lanes int) (perFrame, perSince uint64) {
	t.Helper()
	h, _ := hostedLeasedRun(t, wideRunInspect(t, lanes, 100))
	h.runs.log = newRunLog(t.TempDir())
	path := workflow.PathKey([]string{"root", "lane-0", "s0"})
	_, opened, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	rev := revOf(t, h)
	evt := marotte.NewEvent(marotte.EventTurnOpened, "", marotte.TurnOpenedPayload{WorkflowID: "wf_1", Entry: *opened})
	perFrame = bytesPer(20, func() {
		h.runs.noteRunEvent(&evt)
		joinInflight(t, h)
	})
	since := "since=" + strconv.FormatUint(rev, 10)
	perSince = bytesPer(20, func() { getRunAt(t, h, since) })
	return perFrame, perSince
}

// A step turn's frame and a client's ?since cost what changed: ten times the nodes cost about the same.
func TestRunRev_LiveWorkDoesNotGrowWithTheRun(t *testing.T) {
	frame1k, since1k := liveCost(t, 10)
	frame10k, since10k := liveCost(t, 100)
	t.Logf("per frame %d B at 1,000 nodes, %d B at 10,000; per ?since %d B, %d B", frame1k, frame10k, since1k, since10k)
	const slack = 32 << 10
	if frame10k > 2*frame1k+slack {
		t.Errorf("a step frame on 10,000 nodes allocates %d B against %d B on 1,000: the revise walks the run", frame10k, frame1k)
	}
	if since10k > 2*since1k+slack {
		t.Errorf("a ?since on 10,000 nodes allocates %d B against %d B on 1,000: the read walks the run", since10k, since1k)
	}
}

// A step turn opening moves that step's start row and its node into the next revision, with no engine read.
func TestRunRev_AStepFrameRevisesThatStep(t *testing.T) {
	h, br := hostedLeasedRun(t, wideRunInspect(t, 2, 3))
	h.runs.log = newRunLog(t.TempDir())
	rev := revOf(t, h)
	path := workflow.PathKey([]string{"root", "lane-1", "s2"})
	_, opened, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	br.forgetCalls()

	h.bus.Broadcast(t.Context(), marotte.NewEvent(marotte.EventTurnOpened, "", marotte.TurnOpenedPayload{
		WorkflowID: "wf_1", Entry: *opened,
	}))
	joinInflight(t, h)

	var got struct {
		StepStarts map[string]marotte.RunStepStart `json:"step_starts"`
		Nodes      []struct {
			Path string `json:"path"`
		} `json:"nodes"`
		Rev uint64 `json:"rev"`
	}
	_, body := getRunAt(t, h, "since="+strconv.FormatUint(rev, 10))
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if br.called(methodKiroWorkflowInspect) {
		t.Error("a step frame read the engine")
	}
	if got.Rev <= rev {
		t.Errorf("rev = %d, want past %d: %.300s", got.Rev, rev, body)
	}
	if s, ok := got.StepStarts[path]; !ok || s.Ended || s.Turn != opened.Turn {
		t.Errorf("step_starts = %+v, want %s in flight on turn %s", got.StepStarts, path, opened.Turn)
	}
	if len(got.StepStarts) != 1 {
		t.Errorf("step_starts = %+v, want only the opened step's row", got.StepStarts)
	}
	found := false
	for _, n := range got.Nodes {
		found = found || n.Path == path
	}
	if !found {
		t.Errorf("nodes = %+v, want the opened step's record", got.Nodes)
	}
}
