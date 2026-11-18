package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
	"github.com/cplieger/marotte/internal/translate"
)

// readCosts is what each run and session read costs the server per request at x1 and x10 (tests/perf/synth's
// sizes). A change that moves one by design rewrites its entry in the same diff.
var readCosts = map[string]testsupport.Cost{
	"step transcript x1":   {Allocs: 101, Bytes: 98484},
	"step transcript x10":  {Allocs: 103, Bytes: 99030},
	"since read x1":        {Allocs: 130, Bytes: 48188},
	"since read x10":       {Allocs: 130, Bytes: 48264},
	"glance read x1":       {Allocs: 37461, Bytes: 4304759},
	"glance read x10":      {Allocs: 384012, Bytes: 43973557},
	"ask core refresh x1":  {Allocs: 68313, Bytes: 13142528},
	"ask core refresh x10": {Allocs: 686190, Bytes: 137888984},
	"session listing x1":   {Allocs: 3047, Bytes: 720140},
	"session listing x10":  {Allocs: 28448, Bytes: 6571324},
}

func holdReadCost(t *testing.T, op string, got testsupport.Cost) {
	t.Helper()
	testsupport.HoldCost(t, op, readCosts[op], got)
}

// startStepTurn opens a run-log turn on the step at path and tells the run's revision about it, as a step's first
// frame does.
func startStepTurn(t *testing.T, h *Runtime, path string) {
	t.Helper()
	_, opened, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path}, "c1")
	if err != nil {
		t.Fatalf("open the step turn of %s: %v", path, err)
	}
	evt := marotte.NewEvent(marotte.EventTurnOpened, "", marotte.TurnOpenedPayload{WorkflowID: "wf_1", Entry: *opened})
	h.runs.noteRunEvent(&evt)
	joinInflight(h)
}

// serveRun answers GET /api/runs/wf_1?query from the run routes and fails t unless it is a 200; it decodes nothing,
// so a measured call costs only the server's work.
func serveRun(t *testing.T, h *Runtime, query string) {
	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1?"+query, http.NoBody)
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/wf_1?%s = %d: %.200s", query, rec.Code, rec.Body.String())
	}
}

// heldRev is the revision the run's cell holds.
func heldRev(h *Runtime) uint64 {
	c := h.runs.cellFor("wf_1")
	c.revision.mu.Lock()
	defer c.revision.mu.Unlock()
	return c.revision.rev
}

// A step's transcript is read from that step's turns, so it costs the same on the x10 run as on the x1 run.
func TestReadCost_StepTranscriptCostsTheStepNotTheRun(t *testing.T) {
	testsupport.RequireCostBuild(t)
	costs := map[int]testsupport.Cost{}
	for _, scale := range []int{1, 10} {
		op := fmt.Sprintf("step transcript x%d", scale)
		t.Run(op, func(t *testing.T) {
			run := lanesRuns[scale]()
			cfg := t.TempDir()
			writeRunLog(t, cfg, "wf_1", run)
			r := newRunLog(cfg)
			costs[scale] = testsupport.MeasureCost(201, func() {
				if _, _, _, found, err := r.stepTurns(t.Context(), "wf_1", run.steps[0]); err != nil || !found {
					t.Fatalf("stepTurns(%s) = found %v, err %v; want the step's turns", run.steps[0], found, err)
				}
			})
			holdReadCost(t, op, costs[scale])
		})
	}
	testsupport.HoldCost(t, "step transcript x10 against x1", costs[1], costs[10])
}

func TestReadCost_RunReadsHoldTheirBudgets(t *testing.T) {
	testsupport.RequireCostBuild(t)
	for _, scale := range []int{1, 10} {
		run := lanesRuns[scale]()
		h, _ := hostedLeasedRun(t, run.inspect)
		h.runs.log = newRunLog(t.TempDir())
		rev := revOf(t, h)
		startStepTurn(t, h, run.steps[0])
		if heldRev(h) <= rev {
			t.Fatalf("Setup: a step's first frame left the run at revision %d", rev)
		}

		op := fmt.Sprintf("since read x%d", scale)
		t.Run(op, func(t *testing.T) {
			since := "since=" + strconv.FormatUint(rev, 10)
			holdReadCost(t, op, testsupport.MeasureCost(201, func() { serveRun(t, h, since) }))
		})

		// The client reads the glance again on every run_rev, and each is a revision the cache has not shaped.
		op = fmt.Sprintf("glance read x%d", scale)
		t.Run(op, func(t *testing.T) {
			next := 1
			holdReadCost(t, op, testsupport.MeasureCostAfter(testsupport.CostRuns(scale, 21, 3),
				func() {
					before := heldRev(h)
					startStepTurn(t, h, run.steps[next])
					next++
					if heldRev(h) <= before {
						t.Fatalf("Setup: a step's first frame left the run at revision %d", before)
					}
				},
				func() { serveRun(t, h, "view=glance") }))
		})

		op = fmt.Sprintf("ask core refresh x%d", scale)
		t.Run(op, func(t *testing.T) {
			c := h.runs.cellFor("wf_1")
			holdReadCost(t, op, testsupport.MeasureCost(testsupport.CostRuns(scale, 3, 1), func() {
				if _, err := h.runs.refreshCell(t.Context(), c); err != nil {
					t.Fatalf("refreshCell: %v", err)
				}
			}))
		})
	}
}

// historySessions seeds tests/perf/synth's History at scale: 150 sessions, 30 of them owned by a chat (the rows
// History lists), and 21 runs per x1, and answers session/list and the run list with them.
func historySessions(t *testing.T, scale int) *Runtime {
	t.Helper()
	h, cs, br := newTestHub()
	type row struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
		UpdatedAt string `json:"updatedAt"`
	}
	sessions := make([]row, 0, 150*scale)
	for i := range 150 * scale {
		id := fmt.Sprintf("sess_%08d-0000-4000-8000-000000000000", i)
		sessions = append(sessions, row{id, fmt.Sprintf("Session %d", i), fmt.Sprintf("2026-11-%02dT10:00:00Z", 1+i%9)})
		if i >= 30*scale {
			continue
		}
		if _, err := cs.Mutate(t.Context(), marotte.ChatID(fmt.Sprintf("c-hist%08d", i)), func(c *marotte.Chat, _ bool) bool {
			c.Name = fmt.Sprintf("Chat %d", i)
			c.RecordSession(id)
			return true
		}); err != nil {
			t.Fatalf("Setup: seed chat %d: %v", i, err)
		}
	}
	runs := make([]map[string]string, 0, 20*scale+1)
	for i := range 20*scale + 1 {
		runs = append(runs, map[string]string{
			"workflowId": fmt.Sprintf("wf_h%015x", i), "name": fmt.Sprintf("history run %d", i),
			"workflowName": fmt.Sprintf("recipe-%02d", i%7), "status": "completed",
			"createdAt": "2026-11-10T10:00:00.000Z", "updatedAt": "2026-11-10T10:00:00.000Z",
			"parentSessionId": sessions[i%len(sessions)].SessionID,
		})
	}
	for method, body := range map[string]any{
		marotte.MethodSessionList: map[string]any{"sessions": sessions},
		methodKiroWorkflowList:    map[string]any{"runs": runs},
	} {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		br.setCallResult(method, raw)
	}
	return h
}

func TestReadCost_SessionListingHoldsItsBudget(t *testing.T) {
	testsupport.RequireCostBuild(t)
	for _, scale := range []int{1, 10} {
		op := fmt.Sprintf("session listing x%d", scale)
		t.Run(op, func(t *testing.T) {
			h := historySessions(t, scale)
			rec := httptest.NewRecorder()
			h.handleSessionList(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", http.NoBody))
			var listed marotte.SessionListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
				t.Fatalf("Setup: decode the listing: %v", err)
			}
			if len(listed.Sessions) != 30*scale || len(listed.Runs) != 20*scale+1 {
				t.Fatalf("Setup: the listing holds %d sessions and %d runs, want %d and %d",
					len(listed.Sessions), len(listed.Runs), 30*scale, 20*scale+1)
			}
			holdReadCost(t, op, testsupport.MeasureCost(testsupport.CostRuns(scale, 51, 21), func() {
				rec := httptest.NewRecorder()
				h.handleSessionList(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", http.NoBody))
				if rec.Code != http.StatusOK {
					t.Fatalf("GET /api/sessions = %d", rec.Code)
				}
			}))
		})
	}
}
