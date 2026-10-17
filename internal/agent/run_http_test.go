package agent

import (
	"bytes"
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

// TestHandleRun_CarriesTheRunsStepEnds pins `step_ends`: KAS grades a step its iteration limit stopped `completed`
// with no signal, so only the run log's close can tell the reader the step did not finish.
func TestHandleRun_CarriesTheRunsStepEnds(t *testing.T) {
	t.Run("a step kiro-cli stopped is named with its reason", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, "wf_1", "build", "completed", ""))
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

// hostedLeasedRun is a leased parentless run with its host session resident, answering inspect with reply.
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

	t.Run("a run with no resident session is loaded from its lease and answers 200", func(t *testing.T) {
		h, _, br := newTestHub()
		leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession, Host: hostSec()})
		br.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		askID := markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step")
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", askID, "the main branch"))
		if rec.Code != http.StatusOK {
			t.Errorf("a run with no resident session = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowList) {
			t.Error("the answer read the run list, which a one-run verb never needs")
		}
	})

	// A failed load must answer 500, not echo an internal path.
	t.Run("a failed load answers a generic 500", func(t *testing.T) {
		h, _, br := newTestHub()
		leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession, Host: hostSec()})
		br.loadErr = errors.New("fork/exec: no such file or directory")

		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a failed load = %d, want %d: %s",
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

// TestHandleResume_AQueuedClaimIsWaitedForNotRefused pins kiro-cli 2.31.0's same-process claim queue: KAS holds a
// verb behind the run's current claim holder and answers it once the holder releases, so the verb waits for that
// reply and answers 200; only an overstayed holder's refusal is the 409 above.
func TestHandleResume_AQueuedClaimIsWaitedForNotRefused(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", ""),
	}
	queued := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowResume: queued}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/resume", nil)
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runRoutes.handleResume(rec, req)
	}()
	waitForCall(t, br, methodKiroWorkflowResume)
	select {
	case <-done:
		t.Fatalf("the resume answered %d while KAS still held its claim in the queue", rec.Code)
	case <-time.After(20 * time.Millisecond):
	}
	close(queued)
	<-done
	if rec.Code != http.StatusOK {
		t.Errorf("a resume KAS queued then ran = %d %s, want 200", rec.Code, rec.Body.String())
	}
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
		t.Fatalf("Setup: the pre-send read: %s", err)
	}
}
