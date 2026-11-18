package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// lanesRunLanes are tests/perf/synth/gen.py's lanes: each lane's iterations at x1 and its status. x1 is about 1,000
// nodes and a 1 MB inspect reply; xN multiplies every lane's iterations.
var lanesRunLanes = []struct {
	prefix string
	iters  int
	status string
}{
	{"core", 24, "completed"},
	{"ts", 20, "completed"},
	{"media", 10, "running"},
	{"search", 10, "running"},
	{"platform", 5, "paused"},
	{"api", 1, "paused"},
	{"web", 1, "paused"},
	{"web2", 1, "paused"},
	{"libs", 6, "completed"},
	{"auth-gates", 14, "paused"},
	{"final", 1, "paused"},
}

const lanesRunOutText = "The patch adds a retry to the fetch client, bounded at three attempts, and a unit test for " +
	"every error arm. Checks: the statedb tests pass with -race, golangci-lint reports 0 issues, " +
	"and deadset reports 0 findings at CI's pins. "

// lanesRunContent is gen.py's --content default: the entries a step turn holds between its open and its answer.
const lanesRunContent = 12

// lanesRun is gen.py's run at a scale: the inspect reply the engine answers, the run log marotte keeps for it (a
// turn per started node), and every step's path key in the order the run started them.
type lanesRun struct {
	inspect json.RawMessage
	log     []marotte.Entry
	steps   []string
}

type lanesGen struct {
	rng   *rand.Rand
	wid   string
	log   []marotte.Entry
	steps []string
	turnN int
	clock int64
}

type genNode = map[string]any

func (g *lanesGen) entry(id, turn string, kind marotte.EntryKind, payload any, seq uint64) {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	g.clock += 100
	g.log = append(g.log, marotte.Entry{ID: id, Turn: turn, Kind: kind, Payload: raw, Seq: seq, Ts: g.clock})
}

type genTurn struct {
	id  string
	seq uint64
}

func (g *lanesGen) turn(key string, step bool) genTurn {
	g.turnN++
	t := genTurn{id: fmt.Sprintf("t-syn%08d", g.turnN)}
	g.entry(t.id, t.id, marotte.EntryKindTurnOpen,
		map[string]any{"source": "workflow_step", "run": g.wid, "node_path": key, "n": g.turnN}, 0)
	if !step {
		return t
	}
	for i := range lanesRunContent {
		t.seq++
		call := fmt.Sprintf("toolu_syn_%08d_%03d", g.turnN, i)
		path := fmt.Sprintf("/workspace/subflux/internal/x%03d.go", i)
		switch i % 3 {
		case 0:
			g.entry(fmt.Sprintf("%s-%d-say", t.id, i), t.id, marotte.EntryKindThinking,
				map[string]string{"text": "Checking the lane state before the change.\n\n"}, t.seq)
		case 1:
			g.entry(call, t.id, marotte.EntryKindToolCall, map[string]any{
				"id": call, "title": "Read File", "kind": "read", "status": "pending",
				"input": map[string]string{"path": path}, "locations": []map[string]string{{"path": path}},
			}, t.seq)
		default:
			g.entry(marotte.ToolResultID(call), t.id, marotte.EntryKindToolResult, map[string]any{
				"title": "Read File", "kind": "read", "status": "completed",
				"output": "package x\n" + strings.Repeat("const a = 1\n", 60),
			}, t.seq)
		}
	}
	t.seq++
	g.entry(t.id+"-out-say", t.id, marotte.EntryKindText, map[string]string{"text": lanesRunOutText}, t.seq)
	return t
}

// genStep is a step node's agent and model; a node with none is a container.
type genStep struct{ agent, model string }

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

func (g *lanesGen) node(id, typ string, path []string, it int, branch string, step *genStep) (genNode, genTurn) {
	g.clock += 80
	n := genNode{"nodeId": id, "type": typ, "status": "running", "startedAt": iso(g.clock)}
	if it >= 0 {
		n["iteration"] = it
	}
	if branch != "" {
		n["branchId"] = branch
	}
	if step != nil {
		n["agentName"], n["modelId"], n["effortLevel"] = step.agent, step.model, "high"
		n["sessionId"] = fmt.Sprintf("sess_%08x-0000-4000-8000-%012x", g.rng.Uint32(), g.rng.Uint64()>>16)
	} else {
		n["children"] = []genNode{}
	}
	key := workflow.PathKey(append([]string{g.wid}, path...))
	if step != nil {
		g.steps = append(g.steps, key)
	}
	return n, g.turn(key, step != nil)
}

func (g *lanesGen) end(n genNode, opened genTurn, status string) {
	n["status"] = status
	if status != "completed" && status != "failed" {
		return
	}
	g.clock += 60
	n["endedAt"] = iso(g.clock)
	if n["type"] == "step" {
		out := strings.Repeat(lanesRunOutText, 8)
		n["capturedOutput"] = out[:min(len(out), 900+g.rng.IntN(1200))] + "\n\nUNIT-READY"
		n["completionSignal"], n["completionSignalSource"] = "success", "send_message"
		n["credits"] = []map[string]any{{"unit": "credit", "value": 1 + 8*g.rng.Float64()}}
	}
	g.entry(opened.id+":e1", opened.id, marotte.EntryKindTurnClose, map[string]any{
		"outcome": "completed", "stop_reason_raw": "end_turn", "credits": 3.1, "elapsed_ms": 120000,
	}, opened.seq+1)
}

func appendChild(parent, child genNode) {
	parent["children"] = append(parent["children"].([]genNode), child)
}

func (g *lanesGen) iteration(prefix, laneID string, k int, last bool, laneStatus string) genNode {
	base := []string{"lanes-c", laneID, fmt.Sprintf("iter-%d", k)}
	openTail := last && laneStatus != "completed"
	it, itOpen := g.node(fmt.Sprintf("%s#%d", laneID, k), "sequence", base, k, "", nil)
	loopPath := append(append([]string{}, base...), prefix+"-build-loop")
	loop, loopOpen := g.node(prefix+"-build-loop", "repeat", loopPath, k, "", nil)
	rounds := 1 + g.rng.IntN(4)
	for j := range rounds {
		jp := append(append([]string{}, loopPath...), fmt.Sprintf("iter-%d", j))
		seq, seqOpen := g.node(fmt.Sprintf("%s-build-loop#%d", prefix, j), "sequence", jp, j, "", nil)
		s, sOpen := g.node(prefix+"-build", "step", append(append([]string{}, jp...), prefix+"-build"), j, "", &genStep{"builder", "model-a"})
		stopHere := openTail && j == rounds-1 && laneStatus == "running" && g.rng.Float64() < 0.5
		status := "completed"
		if stopHere {
			status = "running"
		}
		g.end(s, sOpen, status)
		appendChild(seq, s)
		g.end(seq, seqOpen, status)
		appendChild(loop, seq)
		if stopHere {
			appendChild(it, loop)
			return it
		}
	}
	g.end(loop, loopOpen, "completed")
	appendChild(it, loop)
	reviewsPath := append(append([]string{}, base...), prefix+"-reviews")
	par, parOpen := g.node(prefix+"-reviews", "parallel", reviewsPath, k, "", nil)
	for _, rev := range []genStep{{"reviewer-a", "model-a"}, {"reviewer-b", "model-c"}} {
		id := prefix + "-review-" + rev.agent[len("reviewer-"):]
		s, sOpen := g.node(id, "step", append(append([]string{}, reviewsPath...), id), k, id, &rev)
		g.end(s, sOpen, "completed")
		appendChild(par, s)
	}
	g.end(par, parOpen, "completed")
	appendChild(it, par)
	gate, gateOpen := g.node(prefix+"-gate", "step", append(append([]string{}, base...), prefix+"-gate"), k, "", &genStep{"review-gate", "model-a"})
	gateStatus := "completed"
	if openTail {
		gateStatus = "running"
		if laneStatus == "paused" {
			gateStatus = "paused"
		}
	}
	g.end(gate, gateOpen, gateStatus)
	appendChild(it, gate)
	itStatus := "completed"
	if openTail {
		itStatus = gateStatus
	}
	g.end(it, itOpen, itStatus)
	return it
}

func buildLanesRun(wid string, scale int) lanesRun {
	g := &lanesGen{rng: rand.New(rand.NewPCG(7, uint64(scale))), wid: wid, clock: 1_700_000_000_000}
	root := genNode{"nodeId": wid, "type": "sequence", "status": "running", "startedAt": iso(g.clock), "children": []genNode{}}
	par, _ := g.node("lanes-c", "parallel", []string{"lanes-c"}, -1, "", nil)
	appendChild(root, par)
	for _, lane := range lanesRunLanes {
		laneID := "lane-" + lane.prefix
		n, opened := g.node(laneID, "repeat", []string{"lanes-c", laneID}, -1, laneID, nil)
		count := max(1, lane.iters*scale)
		for k := range count {
			appendChild(n, g.iteration(lane.prefix, laneID, k, k == count-1, lane.status))
		}
		g.end(n, opened, lane.status)
		appendChild(par, n)
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": wid,
		"state": map[string]any{
			"workflowId": wid, "workflowName": "lanes-synth", "status": "running", "root": root,
			"inputs": map[string]any{}, "artifacts": map[string]any{}, "capturedOutputs": map[string]any{},
			"runLabel": fmt.Sprintf("lanes-synth-x%d", scale), "planRevision": 0,
		},
		"nodePlan": []any{},
	})
	if err != nil {
		panic(err)
	}
	return lanesRun{inspect: raw, log: g.log, steps: g.steps}
}

// lanesRuns builds each scale's run once for every test that reads it: the x10 run is 10,000 nodes.
var lanesRuns = map[int]func() lanesRun{
	1:  sync.OnceValue(func() lanesRun { return buildLanesRun("wf_1", 1) }),
	10: sync.OnceValue(func() lanesRun { return buildLanesRun("wf_1", 10) }),
}

// writeRunLog writes run's log where a runLog over cfgDir reads workflow wid's, in one write as gen.py does.
func writeRunLog(t *testing.T, cfgDir, wid string, run lanesRun) {
	t.Helper()
	dir := filepath.Join(cfgDir, runLogDir, wid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range run.log {
		if err := enc.Encode(&run.log[i]); err != nil {
			t.Fatalf("Setup: encode entry %d: %v", i, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, chat.EntriesFileName), buf.Bytes(), 0o600); err != nil {
		t.Fatalf("Setup: write the run log: %v", err)
	}
}
