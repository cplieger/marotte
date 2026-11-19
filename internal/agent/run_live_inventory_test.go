package agent

// The live-runs inventory names a parentless run with no chat for as long as marotte holds its lease: running,
// paused or waiting for an answer it stays, and it leaves only when the lease is released.

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// launchParentless starts wf_1 through POST /api/runs, the launch with no chat behind it.
func launchParentless(t *testing.T) *Runtime {
	t.Helper()
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_1")
	rec := httptest.NewRecorder()
	h.runRoutes.handleLaunch(rec, launchReq(t))
	if rec.Code/100 != 2 {
		t.Fatalf("POST /api/runs = %d %s, want a launched run", rec.Code, rec.Body.String())
	}
	return h
}

// onRunLane delivers a lifecycle frame as KAS sends it for a parentless run: on the run's own host session.
func onRunLane(t *testing.T, h *Runtime, method string, params map[string]any) {
	t.Helper()
	h.translateACPEvent(marotte.RunHostChatID("wf_1"), h.originOf(marotte.RunHostChatID("wf_1")), runNotif(method, params))
}

// liveRows reads GET /api/runs/live, checking every row carries its start and leaving it out of the rows returned.
func liveRows(t *testing.T, h *Runtime) []marotte.LiveRun {
	t.Helper()
	rows := getLiveRuns(t, h.runRoutes).Runs
	for i := range rows {
		if rows[i].StartedAt <= 0 {
			t.Errorf("row %+v carries no start", rows[i])
		}
		rows[i].StartedAt = 0
	}
	return rows
}

func TestLiveRunInventory_ListsAParentlessRunWithNoChatWhileItRunsPausesOrWaits(t *testing.T) {
	h := launchParentless(t)
	running := []marotte.LiveRun{{WorkflowID: "wf_1", ChatID: "", Executing: true}}
	parked := []marotte.LiveRun{{WorkflowID: "wf_1", ChatID: "", Executing: false}}

	if got := liveRows(t, h); !slices.Equal(got, running) {
		t.Errorf("launched: rows = %+v, want %+v", got, running)
	}

	onRunLane(t, h, methodWFNodePaused, map[string]any{
		"workflowId": "wf_1", "nodeId": "ask", "nodePath": []any{"ask"}, "reason": "waiting_for_input",
	})
	if got := liveRows(t, h); !slices.Equal(got, running) {
		t.Errorf("a step waiting for an answer: rows = %+v, want %+v", got, running)
	}

	onRunLane(t, h, methodWFPaused, map[string]any{"workflowId": "wf_1", "pauseReason": "user"})
	if got := liveRows(t, h); !slices.Equal(got, parked) {
		t.Errorf("paused by a person: rows = %+v, want %+v", got, parked)
	}

	onRunLane(t, h, methodWFRunStart, map[string]any{"workflowId": "wf_1", "workflowName": "publish"})
	if got := liveRows(t, h); !slices.Equal(got, running) {
		t.Errorf("resumed: rows = %+v, want %+v", got, running)
	}
}

// A run's launching chat is on its row, so only a parentless run's row has none.
func TestLiveRunInventory_AnAgentsRunIsListedUnderItsLaunchingChat(t *testing.T) {
	h, _, _ := newTestHub()

	h.translateACPEvent("c1", h.originOf("c1"),
		runNotif(methodWFRunStart, map[string]any{"workflowId": "wf_2", "workflowName": "publish"}))

	want := []marotte.LiveRun{{WorkflowID: "wf_2", ChatID: "c1", Executing: true}}
	if got := liveRows(t, h); !slices.Equal(got, want) {
		t.Errorf("rows = %+v, want %+v", got, want)
	}
}

func TestLiveRunInventory_AParentlessRunLeavesAtItsTerminalRunComplete(t *testing.T) {
	for _, status := range []string{"completed", "failed", "aborted", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			h := launchParentless(t)

			onRunLane(t, h, methodWFRunComplete, map[string]any{"workflowId": "wf_1", "status": status})

			if got := liveRows(t, h); len(got) != 0 {
				t.Errorf("rows = %+v after a %s run_complete, want none", got, status)
			}
		})
	}
}

// An onMaxIterations pause ends in a run_complete that is not terminal: the run is resumable, so it stays.
func TestLiveRunInventory_AParentlessRunStaysAtAPausedRunComplete(t *testing.T) {
	h := launchParentless(t)

	onRunLane(t, h, methodWFRunComplete, map[string]any{"workflowId": "wf_1", "status": "paused"})

	got := liveRows(t, h)
	if len(got) != 1 || got[0].WorkflowID != "wf_1" || got[0].ChatID != "" {
		t.Errorf("rows = %+v, want one row for wf_1 with no chat", got)
	}
}

// KAS's run list is the other ending: a terminal status there, or six hours unlisted, releases the run.
func TestLiveRunInventory_AParentlessRunLeavesWhenKASListsItOverOrStopsListingIt(t *testing.T) {
	now := time.Now()

	t.Run("listed with a terminal status", func(t *testing.T) {
		h := launchParentless(t)
		h.runs.reconcileLeasePresence(t.Context(), map[string]marotte.RunStatus{"wf_1": marotte.RunStatusCompleted}, now, false)
		if got := liveRows(t, h); len(got) != 0 {
			t.Errorf("rows = %+v, want none", got)
		}
	})
	t.Run("unlisted for six hours", func(t *testing.T) {
		h := launchParentless(t)
		h.runs.setFirstAbsentAt(t.Context(), "wf_1", now.Add(-6*time.Hour))
		h.runs.reconcileLeasePresence(t.Context(), map[string]marotte.RunStatus{}, now, false)
		if got := liveRows(t, h); len(got) != 0 {
			t.Errorf("rows = %+v, want none", got)
		}
	})
	t.Run("unlisted for under six hours", func(t *testing.T) {
		h := launchParentless(t)
		h.runs.setFirstAbsentAt(t.Context(), "wf_1", now.Add(-5*time.Hour))
		h.runs.reconcileLeasePresence(t.Context(), map[string]marotte.RunStatus{}, now, false)
		want := []marotte.LiveRun{{WorkflowID: "wf_1", ChatID: "", Executing: true}}
		if got := liveRows(t, h); !slices.Equal(got, want) {
			t.Errorf("rows = %+v, want %+v", got, want)
		}
	})
}

// liveRunFramesSince decodes the live_run frames after since, checking each is workspace-wide and each live one carries
// its start, which it leaves out of the payloads returned.
func liveRunFramesSince(t *testing.T, h *Runtime, since uint64) []marotte.LiveRunPayload {
	t.Helper()
	var out []marotte.LiveRunPayload
	for i, e := range eventsSince(h, since, marotte.EventLiveRun) {
		var msg struct {
			ChatID  string                 `json:"chat_id"`
			Payload marotte.LiveRunPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("decode live_run frame %d: %v", i, err)
		}
		if msg.ChatID != "" || e.Event.Topic != "" {
			t.Errorf("live_run frame %d went to chat %q topic %q, want every page", i, msg.ChatID, e.Event.Topic)
		}
		if msg.Payload.Live && msg.Payload.StartedAt <= 0 {
			t.Errorf("live_run frame %d carries no start: %+v", i, msg.Payload)
		}
		msg.Payload.StartedAt = 0
		out = append(out, msg.Payload)
	}
	return out
}

// Every page learns each change of a parentless run's row from a pushed frame, with no read of its own.
func TestLiveRunInventory_PushesEachChangeOfAParentlessRunsRow(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_1")
	start := h.bus.fanout.Position().Head
	rec := httptest.NewRecorder()
	h.runRoutes.handleLaunch(rec, launchReq(t))
	if rec.Code/100 != 2 {
		t.Fatalf("POST /api/runs = %d %s, want a launched run", rec.Code, rec.Body.String())
	}
	launched := liveRunFramesSince(t, h, start)
	if len(launched) == 0 || launched[len(launched)-1] != (marotte.LiveRunPayload{WorkflowID: "wf_1", Live: true, Executing: true}) {
		t.Fatalf("launch frames = %+v, want the last one live and executing with no chat", launched)
	}

	steps := []struct {
		name   string
		method string
		params map[string]any
		want   marotte.LiveRunPayload
	}{
		{
			name: "paused", method: methodWFPaused,
			params: map[string]any{"workflowId": "wf_1", "pauseReason": "user"},
			want:   marotte.LiveRunPayload{WorkflowID: "wf_1", Live: true},
		},
		{
			name: "resumed", method: methodWFRunStart,
			params: map[string]any{"workflowId": "wf_1", "workflowName": "publish"},
			want:   marotte.LiveRunPayload{WorkflowID: "wf_1", Live: true, Executing: true},
		},
		{
			name: "completed", method: methodWFRunComplete,
			params: map[string]any{"workflowId": "wf_1", "status": "completed"},
			want:   marotte.LiveRunPayload{WorkflowID: "wf_1"},
		},
	}
	for _, s := range steps {
		before := h.bus.fanout.Position().Head
		onRunLane(t, h, s.method, s.params)
		if got := liveRunFramesSince(t, h, before); !slices.Equal(got, []marotte.LiveRunPayload{s.want}) {
			t.Errorf("%s: frames = %+v, want exactly %+v", s.name, got, s.want)
		}
	}
}

// A row says when marotte put its run on the wire, so every page orders runs started at different times alike.
func TestLiveRunInventory_ARowCarriesWhenItsRunWentOnTheWire(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_1")
	start := h.bus.fanout.Position().Head
	before := time.Now().UnixMilli()
	rec := httptest.NewRecorder()
	h.runRoutes.handleLaunch(rec, launchReq(t))
	after := time.Now().UnixMilli()
	if rec.Code/100 != 2 {
		t.Fatalf("POST /api/runs = %d %s, want a launched run", rec.Code, rec.Body.String())
	}

	rows := getLiveRuns(t, h.runRoutes).Runs
	if len(rows) != 1 || rows[0].StartedAt < before || rows[0].StartedAt > after {
		t.Fatalf("rows = %+v, want one started between %d and %d", rows, before, after)
	}
	frames := eventsSince(h, start, marotte.EventLiveRun)
	if len(frames) == 0 {
		t.Fatal("the launch pushed no live_run frame")
	}
	var last struct {
		Payload marotte.LiveRunPayload `json:"payload"`
	}
	if err := json.Unmarshal(frames[len(frames)-1].Event.Data, &last); err != nil {
		t.Fatalf("decode live_run frame: %v", err)
	}
	if last.Payload.StartedAt != rows[0].StartedAt {
		t.Errorf("the frame says it started at %d and the route %d", last.Payload.StartedAt, rows[0].StartedAt)
	}
}
