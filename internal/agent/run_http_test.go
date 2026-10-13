package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// TestHandleRun_RejectsNonGET pins that the surface is read-only.
func TestHandleRun_RejectsNonGET(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, httptest.NewRequest(http.MethodPost, "/api/runs/wf_1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/runs/{id} = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleRun_RejectsAMissingID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	// A hand-built request can reach this: 400 before calling KAS.
	h.runRoutes.handleRun(rec, httptest.NewRequest(http.MethodGet, "/api/runs/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET with no id = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func runReq(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id, nil)
	req.SetPathValue("id", id)
	return req
}

// `state` stays raw so a spliced key cannot hide a lost tree.
type runReply struct {
	State         json.RawMessage  `json:"state"`
	OpenAsks      []marotte.RunAsk `json:"open_asks"`
	AsksReachable bool             `json:"asks_reachable"`
}

// getRun returns the decoded reply and the bytes: `[]` versus `null` shows only in bytes.
func getRun(t *testing.T, h *Runtime, id string) (runReply, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, runReq(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/%s = %d, want %d: %s", id, rec.Code, http.StatusOK, rec.Body.String())
	}
	var out runReply
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the run reply: %s", err)
	}
	return out, rec.Body.String()
}

// TestHandleRun_CarriesTheRunsStepEnds pins `step_ends`: KAS grades a step its iteration limit stopped `completed`,
// so only the run log's close can tell the reader the step did not finish.
func TestHandleRun_CarriesTheRunsStepEnds(t *testing.T) {
	t.Run("a step kiro-cli stopped is named with its reason", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))
		h.runs.log = newRunLog(t.TempDir())
		build := workflow.PathKey([]string{"wf_1", "build"})
		if _, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: build}, "c1"); err != nil {
			t.Fatal(err)
		}
		h.runs.log.stopReason("wf_1", build, marotte.StopReasonToolUse)
		if _, _, err := h.runs.log.closeNode(t.Context(), "wf_1", build, "completed", ""); err != nil {
			t.Fatal(err)
		}

		_, body := getRun(t, h, "wf_1")

		var reply struct {
			StepEnds map[string]marotte.RunStepEnd `json:"step_ends"`
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatalf("decoding the run reply: %s", err)
		}
		want := marotte.RunStepEnd{
			Outcome:       marotte.TurnOutcomeFailed,
			FailureReason: marotte.ModelCallLimitStepReason,
			FailureKind:   marotte.FailureKindModelCallLimit,
		}
		if got := reply.StepEnds[build]; got != want || len(reply.StepEnds) != 1 {
			t.Errorf("step_ends = %+v, want only %s = %+v: %s", reply.StepEnds, build, want, body)
		}
	})

	t.Run("a run with no broken step carries an empty object rather than null", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))

		_, body := getRun(t, h, "wf_1")

		if !strings.Contains(body, `"step_ends":{}`) {
			t.Errorf("the body = %s, want `\"step_ends\":{}`", body)
		}
	})
}

// TestHandleRun_CarriesTheRunsStepStarts pins `step_starts`: KAS restamps a resumed node's startedAt, so only the
// run log's turn_open can tell the reader when the step began.
func TestHandleRun_CarriesTheRunsStepStarts(t *testing.T) {
	t.Run("an open step is named with its turn_open's time", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))
		h.runs.log = newRunLog(t.TempDir())
		build := workflow.PathKey([]string{"wf_1", "build"})
		_, opened, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: build}, "c1")
		if err != nil {
			t.Fatal(err)
		}

		_, body := getRun(t, h, "wf_1")

		var reply struct {
			StepStarts map[string]marotte.RunStepStart `json:"step_starts"`
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatalf("decoding the run reply: %s", err)
		}
		want := marotte.RunStepStart{StartedAt: time.UnixMilli(opened.Ts).UTC().Format(time.RFC3339Nano)}
		if got := reply.StepStarts[build]; got != want || len(reply.StepStarts) != 1 {
			t.Errorf("step_starts = %+v, want only %s = %+v: %s", reply.StepStarts, build, want, body)
		}
	})

	t.Run("a run with no log carries an empty object rather than null", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))

		_, body := getRun(t, h, "wf_1")

		if !strings.Contains(body, `"step_starts":{}`) {
			t.Errorf("the body = %s, want `\"step_starts\":{}`", body)
		}
	})
}

// hostedLeasedRun is a leased parentless run with its carrier live, answering inspect with reply.
func hostedLeasedRun(t *testing.T, reply json.RawMessage) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.setCallResult(methodKiroWorkflowInspect, reply)
	return h, br
}

// TestHandleRun_CarriesTheRunsOpenAsks pins `open_asks` and `asks_reachable`, so an agent handed a
// deferral can find the question and its ask id, and can tell "nothing waits" from "unknown".
func TestHandleRun_CarriesTheRunsOpenAsks(t *testing.T) {
	t.Run("a parked step is an ask with its id, and the passthrough survives", func(t *testing.T) {
		h, _ := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))

		got, body := getRun(t, h, "wf_1")

		if len(got.OpenAsks) != 1 || !got.AsksReachable {
			t.Fatalf("open_asks = %+v reachable %v, want the run's one ask read: %s", got.OpenAsks, got.AsksReachable, body)
		}
		a := got.OpenAsks[0]
		if a.AskID != markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step") {
			t.Errorf("ask_id = %q, want the position plus this boot's placeholder marker", a.AskID)
		}
		if a.NodeID != "review" || a.StepSessionID != "sess_step" || a.TextSource != marotte.RunAskTextUnseen {
			t.Errorf("ask = %+v, want review on sess_step, its text unseen", a)
		}
		var state struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(got.State, &state); err != nil || state.Status != "paused" {
			t.Errorf("KAS's own `state` did not survive the splice: %v %q: %s", err, state.Status, body)
		}
		if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || !p.Reachable {
			t.Errorf("the published snapshot = %+v, want the same ask every surface shows", p)
		}
	})

	t.Run("a run with no ask carries an empty list rather than null", func(t *testing.T) {
		h, _ := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusRunning,
			askStepNode{id: "review", status: "running", session: "sess_step"}))

		got, body := getRun(t, h, "wf_1")

		if len(got.OpenAsks) != 0 || !got.AsksReachable {
			t.Fatalf("open_asks = %+v reachable %v, want none, read", got.OpenAsks, got.AsksReachable)
		}
		if !strings.Contains(body, `"open_asks":[]`) {
			t.Errorf("the body = %s, want `\"open_asks\":[]`", body)
		}
	})

	t.Run("an unleased live run says its asks are unknown", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))

		got, body := getRun(t, h, "wf_1")

		if len(got.OpenAsks) != 0 || got.AsksReachable {
			t.Errorf("open_asks = %+v reachable %v, want none and unreachable for a run marotte does not answer for: %s",
				got.OpenAsks, got.AsksReachable, body)
		}
	})

	t.Run("an unleased run that ended asks nothing", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "completed", ""))

		if got, _ := getRun(t, h, "wf_1"); !got.AsksReachable {
			t.Error("asks_reachable = false for an ended run, want true: an ended run asks nothing")
		}
	})

	t.Run("a leased run no host can be opened for is unreachable on both halves", func(t *testing.T) {
		h, _, br := newTestHub()
		leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
		h.runs.cellFor("wf_1")
		br.loadErr = errors.New("the launch session cannot be loaded")

		rec := httptest.NewRecorder()
		h.runRoutes.handleRun(rec, runReq("wf_1"))

		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"asks_reachable":false`) {
			t.Errorf("GET = %d %s, want 502 naming the asks unreachable", rec.Code, rec.Body.String())
		}
		if p := lastAsksOf(t, h, "wf_1"); p.Reachable {
			t.Error("the published snapshot reads reachable after a failed host start")
		}
	})
}

// TestHandleRun_GradesAFailedReadThreeWays pins that the client stops on 404 and retries otherwise, so each misgrade has a cost.
func TestHandleRun_GradesAFailedReadThreeWays(t *testing.T) {
	tests := []struct {
		name string
		arm  func(br *fakeBridge)
		want int
	}{
		{
			// KAS resolved the id and refused: the answer will not change.
			name: "the engine answered ABOUT the run",
			arm: func(br *fakeBridge) {
				br.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{
					Code: -32603, Message: "Internal error",
					Data: json.RawMessage(`{"details":"workflow not found"}`),
				})
			},
			want: http.StatusNotFound,
		},
		{
			// An engine without workflow verbs never will describe a run.
			name: "the engine has no workflow verb",
			arm: func(br *fakeBridge) {
				br.setCallErr(methodKiroWorkflowInspect, workflow.ErrUnknownMethod)
			},
			want: http.StatusServiceUnavailable,
		},
		{
			// Nothing reached the engine, so nothing was learned.
			name: "the read never reached the engine",
			arm: func(br *fakeBridge) {
				br.setCallErr(methodKiroWorkflowInspect, errors.New("bridge exited"))
			},
			want: http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, br := seedChatParentedRun(t, true)
			tc.arm(br)
			rec := httptest.NewRecorder()

			h.runRoutes.handleRun(rec, runReq("wf_1"))

			if rec.Code != tc.want {
				t.Errorf("GET /api/runs/wf_1 with %s = %d, want %d: %s",
					tc.name, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// testEpoch stands in for the hub epoch on a runRoutes built without a runtime.
func testEpoch() string { return "test-epoch" }

func getLiveRuns(t *testing.T, rr *runRoutes) marotte.LiveRunsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	rr.handleLiveRuns(rec, httptest.NewRequest(http.MethodGet, "/api/runs/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/live = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var out marotte.LiveRunsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode live-runs reply: %v", err)
	}
	return out
}

func TestHandleLiveRuns_RejectsNonGET(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.runRoutes.handleLiveRuns(rec, httptest.NewRequest(http.MethodPost, "/api/runs/live", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/runs/live = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleLiveRuns_ProjectsEveryLiveLeaseWithItsChat pins that chat-parented runs carry their chat, parentless none, served without KAS.
func TestHandleLiveRuns_ProjectsEveryLiveLeaseWithItsChat(t *testing.T) {
	h, _, br := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_manual", "workflowName": "nightly",
	}))
	calls := len(br.callLog())

	out := getLiveRuns(t, h.runRoutes)

	want := map[string]string{"wf_agent": "c-live", "wf_manual": ""}
	if len(out.Runs) != len(want) {
		t.Fatalf("GET /api/runs/live returned %d rows, want %d: %+v", len(out.Runs), len(want), out.Runs)
	}
	for _, r := range out.Runs {
		wantChat, ok := want[r.WorkflowID]
		if !ok {
			t.Errorf("unexpected row %+v", r)
			continue
		}
		if r.ChatID != wantChat {
			t.Errorf("chat_id for %s = %q, want %q", r.WorkflowID, r.ChatID, wantChat)
		}
	}
	if got := len(br.callLog()); got != calls {
		t.Errorf("the endpoint put %d call(s) on the wire; the projection must be presence-based",
			got-calls)
	}
}

// TestHandleLiveRuns_ATerminalRunLeavesTheProjection pins that the terminal frame releases the lease.
func TestHandleLiveRuns_ATerminalRunLeavesTheProjection(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	if out := getLiveRuns(t, h.runRoutes); len(out.Runs) != 1 {
		t.Fatalf("the live run is not in the projection: %+v", out.Runs)
	}

	h.runs.observeComplete(t.Context(), "c-live", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_agent", "status": "completed",
	}))

	if out := getLiveRuns(t, h.runRoutes); len(out.Runs) != 0 {
		t.Errorf("a terminal run is still in the projection, so its chat can never be evicted: %+v",
			out.Runs)
	}
}

// /api/runs/live projects the run for the chat's eviction exemption; History attributes it so the row nests the run's tab.
func TestHandleLiveRuns_AndHistoryBothCarryAChatParentedRun(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))

	if out := getLiveRuns(t, h.runRoutes); len(out.Runs) != 1 || out.Runs[0].ChatID != "c-live" {
		t.Fatalf("the chat-parented run is not projected with its chat: %+v", out.Runs)
	}

	rows := h.runs.toWire(
		map[string]marotte.ChatID{"sess-1": "c-live"},
		[]kasWorkflowRun{
			{WorkflowID: "wf_agent", Name: "publish", Status: "running", ParentSessionID: "sess-1"},
			{WorkflowID: "wf_manual", Name: "nightly", Status: "completed"},
		},
	)
	if len(rows) != 2 {
		t.Fatalf("History listed %d rows, want both runs: %+v", len(rows), rows)
	}
	byID := map[string]string{}
	for i := range rows {
		byID[rows[i].WorkflowID] = rows[i].ParentChatID
	}
	if got, ok := byID["wf_agent"]; !ok || got != "c-live" {
		t.Errorf("History's chat-parented row carries parent_chat_id %q (present=%v), want %q: "+
			"the row's door nests the run's tab under that chat", got, ok, "c-live")
	}
	if got, ok := byID["wf_manual"]; !ok || got != "" {
		t.Errorf("History's parentless row carries parent_chat_id %q (present=%v), want empty",
			got, ok)
	}
}

// TestHandleLiveRuns_ServesPersistedLeasesAcrossARestart pins that a paused run emits nothing, so the persisted lease
// must paint the dot. Not executing: NewStore parks every loaded deadline.
func TestHandleLiveRuns_ServesPersistedLeasesAcrossARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	before, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := before.Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_agent", Recipe: "publish", ChatID: "c-live", Origin: runlease.OriginAgent,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The restart: a fresh store over the same directory.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	out := getLiveRuns(t, &runRoutes{runs: &Runs{leases: reopened}, epoch: testEpoch})

	if len(out.Runs) != 1 || out.Runs[0].WorkflowID != "wf_agent" || out.Runs[0].ChatID != "c-live" {
		t.Fatalf("the projection after a restart = %+v, want the persisted lease with its chat",
			out.Runs)
	}
	if out.Runs[0].Executing {
		t.Error("a restart-surviving lease reports executing; NewStore parks every loaded " +
			"deadline, and the bridge that carried this run's frames died with the process")
	}
}

// TestHandleLiveRuns_ExecutingFollowsTheLeasesOwnClock pins that armed on start and resume, parked on pause, so a
// parked run's eviction exemption lapses while its row survives.
func TestHandleLiveRuns_ExecutingFollowsTheLeasesOwnClock(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))

	out := getLiveRuns(t, h.runRoutes)
	if len(out.Runs) != 1 || !out.Runs[0].Executing {
		t.Fatalf("a run that just started is not projected as executing: %+v", out.Runs)
	}

	// The run-level pause frame parks the deadline.
	h.runs.observePaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})(
		t.Context(), "c-live", runNotif(methodWFPaused, map[string]any{"workflowId": "wf_agent"}),
	)

	out = getLiveRuns(t, h.runRoutes)
	if len(out.Runs) != 1 {
		t.Fatalf("a paused run left the projection, so its tab loses its dot and its parent: %+v",
			out.Runs)
	}
	if out.Runs[0].Executing {
		t.Error("a parked run still reports executing, so its chat's whole message window " +
			"stays pinned for the life of the page")
	}
}

// TestHandleLiveRuns_APreUpgradeLeaseRowProjectsWithNoChat pins that a version-1 file loads with an empty chat_id.
func TestHandleLiveRuns_APreUpgradeLeaseRowProjectsWithNoChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"version":1,"leases":[{"started_at":"2026-08-01T03:00:00Z",` +
		`"workflow_id":"wf_old","recipe":"nightly","origin":"scheduled","unattended":true}]}`
	if err := os.WriteFile(filepath.Join(dir, runlease.FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore over a pre-upgrade file: %v", err)
	}

	out := getLiveRuns(t, &runRoutes{runs: &Runs{leases: st}, epoch: testEpoch})

	if len(out.Runs) != 1 || out.Runs[0].WorkflowID != "wf_old" {
		t.Fatalf("the pre-upgrade lease is not projected: %+v", out.Runs)
	}
	if out.Runs[0].ChatID != "" {
		t.Errorf("chat_id = %q for a pre-upgrade row, want empty (no chat to exempt)",
			out.Runs[0].ChatID)
	}
}

// TestHandleControls pins the affordance route's envelope: the client cannot answer this from SSE state alone.
func TestHandleControls(t *testing.T) {
	controlsReq := func(id string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"/controls", nil)
		req.SetPathValue("id", id)
		return req
	}

	t.Run("it refuses a non-GET", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/controls", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("it refuses a missing id", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, httptest.NewRequest(http.MethodGet, "/api/runs//controls", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("no id = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	// Retry is offered with the parent chat, for the step-transcript note too.
	t.Run("an aborted chat-parented run offers retry and names its parent chat", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, controlsReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET controls = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var got marotte.RunControlsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding the controls reply: %s", err)
		}
		if !slices.Contains(got.Verbs, "retry") {
			t.Errorf("verbs = %v, want retry offered", got.Verbs)
		}
		if got.ParentChatID != "c1" {
			t.Errorf("parent_chat_id = %q, want c1; the run page reads it to say where a step's "+
				"live transcript went", got.ParentChatID)
		}
	})

	t.Run("a live run whose engine is gone carries the refusal sentence", func(t *testing.T) {
		// Chat closed: nothing here holds the run.
		h, br := seedChatParentedRun(t, false)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, controlsReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET controls = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var got marotte.RunControlsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding the controls reply: %s", err)
		}
		if slices.Contains(got.Verbs, "pause") {
			t.Errorf("verbs = %v, want pause withheld from a run nothing hosts", got.Verbs)
		}
		if !strings.Contains(got.Refused["pause"], "Findings cleanup") {
			t.Errorf("refused[pause] = %q, want the sentence to name the chat to open",
				got.Refused["pause"])
		}
	})

	t.Run("an unreadable run is a 404 rather than an empty row", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		// rr.status reports "" for an engine without workflow verbs.
		br.setCallErr(methodKiroWorkflowInspect, workflow.ErrUnknownMethod)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, controlsReq("wf_1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET controls for an unreadable run = %d, want %d: %s",
				rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})
}

func answerReq(t *testing.T, id, askID, text string) *http.Request {
	t.Helper()
	body, err := json.Marshal(marotte.RunAnswerRequest{AskID: askID, Text: text})
	if err != nil {
		t.Fatalf("Setup: marshalling the answer body: %s", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/runs/"+id+"/answer", bytes.NewReader(body))
	req.SetPathValue("id", id)
	return req
}

// TestHandleAnswer pins the guards and the coded refusals.
func TestHandleAnswer(t *testing.T) {
	t.Run("it refuses a non-POST", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/answer", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("it refuses a missing id", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, httptest.NewRequest(http.MethodPost, "/api/runs//answer", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("no id = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("an empty answer is a 400", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "  "))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("an empty answer = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	// A TUI-launched run has no lease, so marotte answers for none of its steps.
	t.Run("an unleased run's ask is gone", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: parentlessRunList("wf_1")}
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))
		if rec.Code != http.StatusConflict || reasonOf(t, rec.Body.Bytes()) != string(marotte.RunAskRefusedGone) {
			t.Fatalf("an unleased run = %d %s, want 409 ask_gone", rec.Code, rec.Body.String())
		}
		if slices.Contains(br.callLog(), marotte.MethodPrompt) {
			t.Error("an answer to a run with no cell was sent")
		}
	})

	// The answering request is refused on KAS's own state, though its own pending holds every ask.
	t.Run("a run still moving is a retryable 409", func(t *testing.T) {
		h, br := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusRunning, parkedStep("review", "sess_step")))
		askID := markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step")

		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", askID, "the main branch"))

		if rec.Code != http.StatusConflict || reasonOf(t, rec.Body.Bytes()) != string(marotte.RunAskRefusedSettling) {
			t.Fatalf("a settling run = %d %s, want 409 ask_settling", rec.Code, rec.Body.String())
		}
		if slices.Contains(br.callLog(), marotte.MethodPrompt) {
			t.Error("the answer was sent into a live loop, where KAS runs it as an ordinary turn")
		}
		if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || p.Asks[0].Blocked != marotte.RunAskSettling {
			t.Errorf("after the refusal the run publishes %+v, want the ask back to settling, no longer pending", p.Asks)
		}
	})

	// A park drops the process, so an unhosted run must re-host rather than refuse.
	t.Run("a run with no bridge is re-hosted from its lease and answers 200", func(t *testing.T) {
		h, _, br := newTestHub()
		leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
		br.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		askID := markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step")
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", askID, "the main branch"))
		if rec.Code != http.StatusOK {
			t.Errorf("an unhosted run = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowList) {
			t.Error("the answer read the run list, which can stamp the utility as the run's owner")
		}
	})

	// A failed spawn must answer 500, not echo an internal path.
	t.Run("a failed spawn answers a generic 500", func(t *testing.T) {
		h, _, br := newTestHub()
		leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
		br.loadErr = errors.New("fork/exec: no such file or directory")

		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a failed spawn = %d, want %d: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "fork/exec") {
			t.Errorf("the body = %s, want a generic sentinel", rec.Body.String())
		}
	})

	t.Run("the happy path answers 200", func(t *testing.T) {
		h, _ := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		askID := markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step")
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", askID, "the main branch"))
		if rec.Code != http.StatusOK {
			t.Fatalf("the happy path = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

func pauseReq(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+id+"/pause", nil)
	req.SetPathValue("id", id)
	return req
}

// TestControlHandler_ForwardsKASsOwnRefusal pins that a hosted run's refused verb answers 409 with KAS's reason.
func TestControlHandler_ForwardsKASsOwnRefusal(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	// This process holds the run, so pause is offered.
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	// KAS refuses with -32603 and the reason in `error.data`, hence rpcerr.Text.
	br.callRPCErrs = map[string]*marotte.RPCError{
		methodKiroWorkflowPause: {
			Code:    -32603,
			Message: "Internal error",
			Data:    json.RawMessage(`{"details":"Workflow 'wf_1' is not registered"}`),
		},
	}

	rec := httptest.NewRecorder()
	h.runRoutes.handlePause(rec, pauseReq("wf_1"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("a refused pause = %d, want %d: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not registered") {
		t.Errorf("the body = %s, want KAS's own reason; without it the reader is told "+
			"only that the verb failed and the reason reaches the log alone",
			rec.Body.String())
	}
}

// TestHandleResume_AClaimInFlightIsReaderText pins that KAS's "another process" refusal becomes plain words.
func TestHandleResume_AClaimInFlightIsReaderText(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", ""),
	}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.callRPCErrs = map[string]*marotte.RPCError{
		methodKiroWorkflowResume: {
			Code:    -32603,
			Message: "Internal error",
			Data: json.RawMessage(`{"details":"Workflow 'wf_1' was just claimed by another process; ` +
				`refusing to load it here. Retry if that process does not end up driving it."}`),
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/resume", nil)
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	h.runRoutes.handleResume(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("a resume refused for a claim in flight = %d, want %d: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the body %s: %v", rec.Body.String(), err)
	}
	if body.Error != runClaimInFlightText {
		t.Errorf("the body's error = %q, want %q", body.Error, runClaimInFlightText)
	}
}

// TestHandleStepStatus_SplitsAValidationRefusalFromAStartFailure pins that a start failure is 500, a KAS refusal 400.
func TestHandleStepStatus_SplitsAValidationRefusalFromAStartFailure(t *testing.T) {
	post := func(t *testing.T, h *Runtime, nodeID, status string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{"node_id": nodeID, "status": status})
		if err != nil {
			t.Fatalf("Setup: marshalling the step-status body: %s", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/step", bytes.NewReader(body))
		req.SetPathValue("id", "wf_1")
		rec := httptest.NewRecorder()
		h.runRoutes.handleStepStatus(rec, req)
		return rec
	}

	t.Run("an unknown status is a 400 naming the allowlist", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := post(t, h, "review", "paused")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("an unknown status = %d, want %d: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), runStepCompleted) {
			t.Errorf("the body = %s, want the accepted statuses", rec.Body.String())
		}
	})

	t.Run("a failed spawn answers a generic 500", func(t *testing.T) {
		h, _, br := newTestHub()
		// The pre-send read must land to reach the spawn failure.
		addressableStep(t, h, br, "wf_1", "review")
		br.startErr = errors.New("fork/exec: no such file or directory")

		rec := post(t, h, "review", runStepCompleted)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a failed spawn = %d, want %d: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "fork/exec") {
			t.Errorf("the body = %s, want a generic sentinel", rec.Body.String())
		}
	})
}

// Every node carries `type`: KAS's resolver considers `step` only.
func stepTargetInspect(t *testing.T, workflowID, nodeID string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"status": string(marotte.RunStatusPaused),
			"root": map[string]any{
				"nodeId": "root", "type": "sequence", "status": string(marotte.RunStatusPaused),
				"children": []any{map[string]any{
					"nodeId": nodeID, "type": stepNodeType, "status": string(marotte.RunStatusPaused),
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

func addressableStep(t *testing.T, h *Runtime, br *fakeBridge, workflowID, nodeID string) {
	t.Helper()
	br.setCallResult(methodKiroWorkflowInspect, stepTargetInspect(t, workflowID, nodeID))
	br.setCallResult(methodKiroWorkflowList, parentlessRunList(workflowID))
	if _, err := h.runs.rawInspect(t.Context(), workflowID); err != nil {
		t.Fatalf("Setup: warming the utility session: %s", err)
	}
}

// TestSetStepStatus pins the allowlist and the bridge resolution.
func TestSetStepStatus(t *testing.T) {
	t.Run("it accepts running as the continue-without-answering verb", func(t *testing.T) {
		h, br := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))

		if err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: runStepRunning}); err != nil {
			t.Fatalf("SetStepStatus(running) = %v, want nil", err)
		}
		if br.paramsFor(methodKiroWorkflowUpdate)["status"] != runStepRunning {
			t.Errorf("update params = %v, want status running", br.paramsFor(methodKiroWorkflowUpdate))
		}
		// Until the step's turn_start a read can still show the node awaiting input.
		if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || p.Asks[0].Blocked != marotte.RunAskPending {
			t.Errorf("after Continue the run publishes %+v, want its ask held as pending", p.Asks)
		}
		h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
		if h.runs.asks.get("wf_1").pendingOf() != nil {
			t.Error("the step's turn_start left Continue's pending set")
		}
	})

	t.Run("continuing a run with no lease is mistargeted", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		if err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: runStepRunning}); !errors.Is(err, errStepStatusMistargeted) {
			t.Errorf("SetStepStatus(running) on an unleased run = %v, want errStepStatusMistargeted", err)
		}
	})

	t.Run("an unknown status is still refused", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		if err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: "paused"}); err == nil {
			t.Error("SetStepStatus(paused) = nil, want a refusal")
		}
	})

	t.Run("a chat-parented run resolves the launching chat's bridge", func(t *testing.T) {
		h, cs, br := newTestHub()
		// An agent-launched run has no bridge of its own; resolving the chat's bridge avoids a re-host.
		cs.seed(t, "c1", func(c *marotte.Chat) { c.RecordSession("sess_parent") })
		// A live chat bridge holds the chat's current session.
		br.sessionID = "sess_parent"
		h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: json.RawMessage(
				`{"runs":[{"workflowId":"wf_1","name":"publish","status":"paused","parentSessionId":"sess_parent"}]}`,
			),
			methodKiroWorkflowInspect: stepTargetInspect(t, "wf_1", "review"),
		}

		if err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: runStepCompleted}); err != nil {
			t.Errorf("SetStepStatus on an agent-launched run = %v, want nil", err)
		}
	})
}

// Two executions of one step share its node id and path, as a fan-out's sessions do, and KAS's
// update targets the first: a Continue goes only from that execution's card.
func TestSetStepStatus_AContinueAtAFanOutPathGoesOnlyFromTheTargetsCard(t *testing.T) {
	for name, tc := range map[string]struct {
		pick    func(target, other workflow.Ask) string
		wantErr error
		node    string
	}{
		"the target's card under another step's node id": {
			pick: func(target, _ workflow.Ask) string { return target.ID }, node: "plan", wantErr: errStepStatusMistargeted,
		},
		"the target's card":               {pick: func(target, _ workflow.Ask) string { return target.ID }},
		"the other execution's card":      {pick: func(_, other workflow.Ask) string { return other.ID }, wantErr: errStepStatusMistargeted},
		"no card named":                   {pick: func(_, _ workflow.Ask) string { return "" }, wantErr: errAskUnnamed},
		"an older question of the target": {pick: func(target, _ workflow.Ask) string { return keyenc.Join(target.Position, "older") }, wantErr: errAskStale},
		"a question nothing asks now":     {pick: func(target, _ workflow.Ask) string { return keyenc.Join(target.Position+"-gone", "m") }, wantErr: errAskGone},
	} {
		t.Run(name, func(t *testing.T) {
			h, br := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused,
				parkedStep("review", "sess_a"), parkedStep("review", "sess_b")))
			asks := readNow(t, h, "wf_1").asks
			if len(asks) != 2 || asks[0].Position == asks[1].Position || asks[0].StepSessionID != "sess_a" {
				t.Fatalf("Setup: asks = %+v, want sess_a's then sess_b's", asks)
			}

			err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{
				NodeID: cmp.Or(tc.node, "review"), Status: runStepRunning, AskID: tc.pick(asks[0], asks[1]),
			})

			sent := slices.Contains(br.callLog(), methodKiroWorkflowUpdate)
			if tc.wantErr == nil {
				if err != nil || !sent {
					t.Errorf("Continue = %v, update sent %v; want the target continued", err, sent)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Continue = %v, want %v", err, tc.wantErr)
			}
			if sent {
				t.Error("the refused Continue sent update, which advances the target execution")
			}
		})
	}
}

// TestHandleStepStatus_AFanOutContinueCarriesItsCardsAskID posts the card's ask_id; without it
// two asks at one node read ambiguous.
func TestHandleStepStatus_AFanOutContinueCarriesItsCardsAskID(t *testing.T) {
	h, br := hostedLeasedRun(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused,
		parkedStep("review", "sess_a"), parkedStep("review", "sess_b")))
	asks := readNow(t, h, "wf_1").asks
	if len(asks) != 2 || asks[0].StepSessionID != "sess_a" {
		t.Fatalf("Setup: asks = %+v, want sess_a's then sess_b's", asks)
	}
	body, err := json.Marshal(map[string]string{"node_id": "review", "status": runStepRunning, "ask_id": asks[0].ID})
	if err != nil {
		t.Fatalf("Setup: marshalling the step-status body: %s", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/step", bytes.NewReader(body))
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	h.runRoutes.handleStepStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("POST /step with the target's ask_id = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if n := countCalls(br, methodKiroWorkflowUpdate); n != 1 {
		t.Errorf("update sent %d times, want once for the named execution", n)
	}
}

// TestSetStepStatus_WithholdsAMistargetedWrite pins that the verb carries no node id, so with two paused branches
// KAS would mark the first while the card names the signal-bearing second.
func TestSetStepStatus_WithholdsAMistargetedWrite(t *testing.T) {
	// `plan` carries the signal and `verify` comes first; `type` on every node.
	branched := func(t *testing.T) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"workflowId": "wf_1",
			"state": map[string]any{
				"status": string(marotte.RunStatusPaused),
				"root": map[string]any{
					"nodeId": "fan", "type": "parallel", "status": string(marotte.RunStatusPaused),
					"children": []any{
						map[string]any{
							"nodeId": "verify", "type": stepNodeType, "status": string(marotte.RunStatusPaused),
						},
						map[string]any{
							"nodeId": "plan", "type": stepNodeType, "status": string(marotte.RunStatusPaused),
							"completionSignal": "need_input",
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("Setup: marshalling the inspect reply: %s", err)
		}
		return raw
	}

	cases := []struct {
		name string
		// reply is the fake's state tree; nil when readErr drives the case.
		reply   func(*testing.T) json.RawMessage
		readErr error
		nodeID  string
		wantErr error
	}{{
		// The defect: KAS would mark a different paused branch.
		name:    "the signal-bearing branch is not the node KAS would mark",
		reply:   branched,
		nodeID:  "plan",
		wantErr: errStepStatusMistargeted,
	}, {
		// Naming the node KAS picks still goes through.
		name:   "the node KAS would mark is sent",
		reply:  branched,
		nodeID: "verify",
	}, {
		name: "a run with no running or paused step is withheld",
		reply: func(t *testing.T) json.RawMessage {
			t.Helper()
			return json.RawMessage(`{"workflowId":"wf_1","state":{"status":"completed",` +
				`"root":{"nodeId":"root","type":"sequence","status":"completed",` +
				`"children":[{"nodeId":"review","type":"step","status":"completed"}]}}}`)
		},
		nodeID:  "review",
		wantErr: errStepStatusMistargeted,
	}, {
		// A running step outranks every paused one in KAS's resolver.
		name: "a running step outranks the parked node being named",
		reply: func(t *testing.T) json.RawMessage {
			t.Helper()
			return json.RawMessage(`{"workflowId":"wf_1","state":{"status":"running",` +
				`"root":{"nodeId":"root","type":"sequence","status":"running","children":[` +
				`{"nodeId":"plan","type":"step","status":"paused"},` +
				`{"nodeId":"build","type":"step","status":"running"}]}}}`)
		},
		nodeID:  "plan",
		wantErr: errStepStatusMistargeted,
	}, {
		// Fail closed, unlike answerAddress: here a wrong guess is an irreversible write.
		name: "an undecodable state is withheld rather than sent",
		reply: func(t *testing.T) json.RawMessage {
			t.Helper()
			return json.RawMessage(`{"workflowId":"wf_1"}`)
		},
		nodeID:  "review",
		wantErr: errStepStatusUnreadable,
	}, {
		// The failed-call shape must withhold too.
		name:    "a failed read is withheld rather than sent",
		readErr: errors.New("bridge exited"),
		nodeID:  "review",
		wantErr: errStepStatusUnreadable,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			if tc.readErr != nil {
				br.callErrs = map[string]error{methodKiroWorkflowInspect: tc.readErr}
			} else {
				br.setCallResult(methodKiroWorkflowInspect, tc.reply(t))
			}
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

			err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: tc.nodeID, Status: runStepCompleted})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("SetStepStatus(%q) = %v, want nil", tc.nodeID, err)
				}
				if br.paramsFor(methodKiroWorkflowUpdate) == nil {
					t.Errorf("no %s call, calls were %v: the addressable node must still "+
						"be sent, or the guard refuses every parallel run",
						methodKiroWorkflowUpdate, br.callLog())
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SetStepStatus(%q) = %v, want %v", tc.nodeID, err, tc.wantErr)
			}
			// A run state, answered 409.
			if !errors.Is(err, errStepStatusRefused) {
				t.Errorf("SetStepStatus(%q) = %v, want it to wrap errStepStatusRefused",
					tc.nodeID, err)
			}
			if params := br.paramsFor(methodKiroWorkflowUpdate); params != nil {
				t.Errorf("%s was called with %v, want no call at all: the write is "+
					"WITHHELD, and KAS marking the wrong node is what that prevents",
					methodKiroWorkflowUpdate, params)
			}
		})
	}
}

// TestSetStepStatus_ParamsAreFlat pins both fields at the top level, no `update` object and no node id; the nested shape threw.
func TestSetStepStatus_ParamsAreFlat(t *testing.T) {
	h, _, br := newTestHub()
	addressableStep(t, h, br, "wf_1", "review")
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: runStepCompleted}); err != nil {
		t.Fatalf("SetStepStatus = %v, want nil", err)
	}
	params := br.paramsFor(methodKiroWorkflowUpdate)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", methodKiroWorkflowUpdate, br.callLog())
	}
	if got := params[keyWorkflowID]; got != "wf_1" {
		t.Errorf("params[%q] = %v, want wf_1", keyWorkflowID, got)
	}
	if got := params["action"]; got != "update_status" {
		t.Errorf(`params["action"] = %v, want update_status: the schema declares it `+
			`required, so an absent value is a shape the verb accepts only by accident`, got)
	}
	if got := params["status"]; got != runStepCompleted {
		t.Errorf(`params["status"] = %v, want %q at the TOP level`, got, runStepCompleted)
	}
	if got, ok := params["update"]; ok {
		t.Errorf(`params["update"] = %v, want absent: nesting the fields is what made `+
			`the verb throw on "`+"`status` is required for action `update_status`"+`"`, got)
	}
	if got, ok := params["nodeId"]; ok {
		t.Errorf(`params["nodeId"] = %v, want absent: the verb takes no node id — it `+
			`targets the run's own current running-or-paused step`, got)
	}
}

// TestSetStepStatus_ReadsTheReply pins that KAS declines with a 200.
func TestSetStepStatus_ReadsTheReply(t *testing.T) {
	// Absent `updated` is no claim and reads as taken.
	cases := []struct {
		name    string
		reply   string
		wantErr bool
		wantMsg string
	}{
		{
			name:  "a taken update",
			reply: `{"workflowId":"wf_1","updated":true,"queued":false,"message":"Step marked completed; the workflow will advance."}`,
		},
		{
			// A queued update lands at turn end, so it was taken.
			name:  "a queued update",
			reply: `{"workflowId":"wf_1","updated":true,"queued":true,"message":"Marked completed; the step finalizes when its current turn ends."}`,
		},
		{
			name:    "a terminal run is declined",
			reply:   `{"workflowId":"wf_1","updated":false,"queued":false,"message":"Cannot update step status: the workflow has already finished (status: completed)."}`,
			wantErr: true,
			wantMsg: "the workflow has already finished",
		},
		{
			name:    "a run with no current step is declined",
			reply:   `{"workflowId":"wf_1","updated":false,"queued":false,"message":"No current step to update: the workflow has no running or paused step."}`,
			wantErr: true,
			wantMsg: "no running or paused step",
		},
		{
			name:  "a reply that states nothing reads as taken",
			reply: `{"workflowId":"wf_1"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowUpdate:  json.RawMessage(tc.reply),
				methodKiroWorkflowInspect: stepTargetInspect(t, "wf_1", "review"),
			}

			err := h.runs.setStepStatus(t.Context(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: runStepCompleted})
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("SetStepStatus = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, errStepStatusRefused) {
				t.Fatalf("SetStepStatus = %v, want errStepStatusRefused", err)
			}
			// KAS's sentence names which decline happened.
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want it to carry %q", err, tc.wantMsg)
			}
		})
	}
}

// TestHandleStepStatus_DeclineIsAConflict pins that a run state is 409, not 400.
func TestHandleStepStatus_DeclineIsAConflict(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowUpdate: json.RawMessage(
			`{"workflowId":"wf_1","updated":false,"queued":false,` +
				`"message":"No current step to update: the workflow has no running or paused step."}`,
		),
		methodKiroWorkflowInspect: stepTargetInspect(t, "wf_1", "review"),
	}
	rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/step",
		strings.NewReader(`{"node_id":"review","status":"completed"}`))
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	rr.handleStepStatus(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "no running or paused step") {
		t.Errorf("body = %s, want KAS's own reason", rec.Body.String())
	}
}
