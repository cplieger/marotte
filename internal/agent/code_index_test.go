package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Frames in the shape KAS's CodeIndexStatusReporter sends (notification, storeStatus, buildStatus).
const (
	indexBuildingParse = `{"sessionId":"sess_a","workspaceRoot":"/tmp/work","store":{"state":"none"},` +
		`"build":{"buildId":"b1","analysisScope":"structural-only","state":"running","phase":"parse","message":"Parsing 40 of 120 files"}}`
	indexReady = `{"sessionId":"sess_a","workspaceRoot":"/tmp/work","store":{"state":"ready","analysisScope":"structural-only",` +
		`"summary":{"files":1234,"functions":5678}},"build":{"buildId":"b1","analysisScope":"structural-only","state":"succeeded","phase":"save","message":"Saved"}}`
	indexOtherRoot = `{"sessionId":"sess_b","workspaceRoot":"/srv/other","store":{"state":"none"}}`
)

func indexStatusFrame(params string) *marotte.RPCResponse {
	return &marotte.RPCResponse{Method: methodKiroCodeIndexStatus, Params: json.RawMessage(params)}
}

func codeIndexView(t *testing.T, h *Runtime) marotte.CodeIndexView {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleCodeIndex(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/code-index", http.NoBody))
	var v marotte.CodeIndexView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("GET /api/code-index = %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

// Every lane delivers its own copy of a broadcast, so a repeat announces nothing, and a lane running behind cannot
// bring back a build that already ended.
func TestCodeIndexStatus_KeepsTheLatestPerRootAndBroadcasts(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, f := range []string{indexBuildingParse, indexReady, indexReady, indexBuildingParse, indexOtherRoot} {
		h.translateACPEvent("c1", h.originOf("c1"), indexStatusFrame(f))
	}

	v := codeIndexView(t, h)
	if v.Root != "/tmp/work" {
		t.Errorf("root = %q, want the work dir /tmp/work", v.Root)
	}
	if len(v.Statuses) != 2 {
		t.Fatalf("statuses = %+v, want one per root", v.Statuses)
	}
	if other := v.Statuses[0]; other.WorkspaceRoot != "/srv/other" || other.Store.State != "none" || other.Build != nil {
		t.Errorf("first status = %+v, want /srv/other with no index and no build", other)
	}
	work := v.Statuses[1]
	if work.WorkspaceRoot != "/tmp/work" || work.Store.State != "ready" || work.Store.Summary == nil ||
		*work.Store.Summary != (marotte.CodeIndexSummary{Files: 1234, Functions: 5678}) {
		t.Errorf("work dir status = %+v, want ready with 1234 files and 5678 functions", work)
	}
	if b := work.Build; b == nil || b.BuildID != "b1" || b.State != "succeeded" || b.Phase != "save" || b.Message != "Saved" {
		t.Errorf("work dir build = %+v, want b1 succeeded at save", work.Build)
	}
	if n := len(eventsOfType(h, marotte.EventCodeIndexStatus)); n != 3 {
		t.Errorf("code_index_status events = %d, want 3: building, ready, the other root", n)
	}
}

// codeIndexBuildStatus is a /tmp/work status carrying one build.
func codeIndexBuildStatus(id, state, phase string) *marotte.CodeIndexStatusPayload {
	return &marotte.CodeIndexStatusPayload{
		WorkspaceRoot: "/tmp/work", Store: marotte.CodeIndexStore{State: "none"},
		Build: &marotte.CodeIndexBuild{BuildID: id, State: state, Phase: phase, Message: state + " " + phase},
	}
}

// codeIndexStoreStatus is a /tmp/work status carrying no build, which KAS sends while its process has none.
func codeIndexStoreStatus() *marotte.CodeIndexStatusPayload {
	return &marotte.CodeIndexStatusPayload{WorkspaceRoot: "/tmp/work", Store: marotte.CodeIndexStore{State: "ready"}}
}

func codeIndexFrameText(f *marotte.CodeIndexStatusPayload) string {
	if f.Build == nil {
		return "no build"
	}
	return f.Build.BuildID + " " + f.Build.State + " " + f.Build.Phase
}

// codeIndexFrame is one frame and the startedAt KAS sent with its build, zero when it sent none.
type codeIndexFrame struct {
	started time.Time
	st      *marotte.CodeIndexStatusPayload
}

func unstarted(st *marotte.CodeIndexStatusPayload) codeIndexFrame { return codeIndexFrame{st: st} }

// Every lane delivers its own copy of KAS's ordered sequence, so a lagging lane's copy of an ended build, of an
// earlier state of the shown one, or of a build that started before the shown one is refused, and the view converges
// on the latest build whichever lane's copy marotte saw first.
func TestCodeIndexStatus_RefusesAnEarlierPhaseAndAnEndedBuild(t *testing.T) {
	t1 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	at := func(when time.Time, st *marotte.CodeIndexStatusPayload) codeIndexFrame {
		return codeIndexFrame{started: when, st: st}
	}
	for _, tc := range []struct {
		name   string
		frames []codeIndexFrame
		want   []bool
		shown  *marotte.CodeIndexBuild
	}{{
		name: "late same-build phase",
		frames: []codeIndexFrame{
			unstarted(codeIndexBuildStatus("b1", "queued", "")), at(t1, codeIndexBuildStatus("b1", "running", "parse")),
			at(t1, codeIndexBuildStatus("b1", "running", "scan")), unstarted(codeIndexBuildStatus("b1", "queued", "")),
			at(t1, codeIndexBuildStatus("b1", "running", "graph")),
		},
		want:  []bool{true, true, false, false, true},
		shown: codeIndexBuildStatus("b1", "running", "graph").Build,
	}, {
		name: "late older build",
		frames: []codeIndexFrame{
			at(t1, codeIndexBuildStatus("b1", "running", "parse")), at(t1, codeIndexBuildStatus("b1", "succeeded", "save")),
			unstarted(codeIndexBuildStatus("b2", "queued", "")),
			at(t1, codeIndexBuildStatus("b1", "succeeded", "save")), at(t1, codeIndexBuildStatus("b1", "running", "save")),
			at(t2, codeIndexBuildStatus("b2", "running", "scan")),
		},
		want:  []bool{true, true, true, false, false, true},
		shown: codeIndexBuildStatus("b2", "running", "scan").Build,
	}, {
		name: "newer build seen first",
		frames: []codeIndexFrame{
			unstarted(codeIndexBuildStatus("b2", "queued", "")), at(t1, codeIndexBuildStatus("b1", "running", "parse")),
			at(t1, codeIndexBuildStatus("b1", "failed", "parse")), at(t2, codeIndexBuildStatus("b2", "running", "scan")),
			at(t2, codeIndexBuildStatus("b2", "succeeded", "save")),
		},
		want:  []bool{true, true, true, true, true},
		shown: codeIndexBuildStatus("b2", "succeeded", "save").Build,
	}, {
		name: "newer build ended before a lagging lane's older build",
		frames: []codeIndexFrame{
			at(t2, codeIndexBuildStatus("b2", "succeeded", "save")), at(t1, codeIndexBuildStatus("b1", "running", "parse")),
			at(t1, codeIndexBuildStatus("b1", "succeeded", "save")), at(t2, codeIndexBuildStatus("b2", "running", "scan")),
			at(t2, codeIndexBuildStatus("b2", "succeeded", "save")),
		},
		want:  []bool{true, false, false, false, false},
		shown: codeIndexBuildStatus("b2", "succeeded", "save").Build,
	}, {
		name: "ended build stays ended",
		frames: []codeIndexFrame{
			at(t1, codeIndexBuildStatus("b1", "running", "save")), at(t1, codeIndexBuildStatus("b1", "canceled", "save")),
			at(t1, codeIndexBuildStatus("b1", "running", "save")),
		},
		want:  []bool{true, true, false},
		shown: codeIndexBuildStatus("b1", "canceled", "save").Build,
	}, {
		name: "build-less frame after a finished build",
		frames: []codeIndexFrame{
			at(t1, codeIndexBuildStatus("b1", "succeeded", "save")), unstarted(codeIndexStoreStatus()),
		},
		want:  []bool{true, false},
		shown: codeIndexBuildStatus("b1", "succeeded", "save").Build,
	}, {
		name: "build-less frame while a build runs",
		frames: []codeIndexFrame{
			at(t2, codeIndexBuildStatus("b2", "running", "scan")), unstarted(codeIndexStoreStatus()),
			at(t2, codeIndexBuildStatus("b2", "succeeded", "save")), unstarted(codeIndexStoreStatus()),
		},
		want:  []bool{true, false, true, false},
		shown: codeIndexBuildStatus("b2", "succeeded", "save").Build,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var c codeIndexStatuses
			for i, f := range tc.frames {
				if got := c.set(f.st, f.started); got != tc.want[i] {
					t.Errorf("set(frame %d: %s) = %v, want %v", i, codeIndexFrameText(f.st), got, tc.want[i])
				}
			}
			v := c.view()
			if len(v) != 1 {
				t.Fatalf("view = %+v, want one root", v)
			}
			if got := v[0].Build; (got == nil) != (tc.shown == nil) || (got != nil && *got != *tc.shown) {
				t.Errorf("shown build = %+v, want %+v", got, tc.shown)
			}
		})
	}
}

// KAS's startedAt orders two builds on the wire, so a lagging lane's older build cannot replace a newer one.
func TestCodeIndexStatus_OrdersBuildsByTheirStartedAt(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, f := range []string{
		`{"workspaceRoot":"/tmp/work","store":{"state":"ready"},"build":{"buildId":"b2","state":"succeeded","phase":"save","startedAt":"2026-11-19T10:01:00.000Z"}}`,
		`{"workspaceRoot":"/tmp/work","store":{"state":"none"},"build":{"buildId":"b1","state":"running","phase":"parse","startedAt":"2026-11-19T10:00:00.000Z"}}`,
	} {
		h.translateACPEvent("c1", h.originOf("c1"), indexStatusFrame(f))
	}
	if v := codeIndexView(t, h); len(v.Statuses) != 1 || v.Statuses[0].Build == nil || v.Statuses[0].Build.BuildID != "b2" {
		t.Errorf("statuses = %+v, want b2, the later-started build", v.Statuses)
	}
}

// An engine that ended mid-build takes its in-memory status with it, so the next engine's build-less replay shows.
func TestCodeIndexStatus_ResetLetsTheNextEngineReplayShow(t *testing.T) {
	var c codeIndexStatuses
	c.set(codeIndexBuildStatus("b1", "running", "scan"), time.Time{})
	c.reset()
	if !c.set(codeIndexStoreStatus(), time.Time{}) {
		t.Error("set(no build) after reset = false, want true")
	}
	if v := c.view(); len(v) != 1 || v[0].Build != nil || v[0].Store.State != "ready" {
		t.Errorf("view = %+v, want the ready store with no build", v)
	}
}

func TestCodeIndexStatus_ArrivesFromARunHostSession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })

	h.dispatch(t.Context(), marotte.RunHostChatID("wf_1"), br, indexStatusFrame(indexBuildingParse))

	v := codeIndexView(t, h)
	if len(v.Statuses) != 1 || v.Statuses[0].Build == nil || v.Statuses[0].Build.Phase != "parse" {
		t.Errorf("statuses = %+v, want the run host session's building status", v.Statuses)
	}
}

// A build started from Settings reports through the observer pass that started it.
func TestCodeIndexStatus_ArrivesFromAnObserverPass(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	o := oneObserver(t, h)

	err := h.observe(t.Context(), func(context.Context, engineSession) error {
		o.deliver(indexStatusFrame(indexBuildingParse))
		for deadline := time.Now().Add(5 * time.Second); len(codeIndexView(t, h).Statuses) == 0; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				return errors.New("the observer's indexStatus was not kept within 5s")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := codeIndexView(t, h); len(v.Statuses) != 1 || v.Statuses[0].Build == nil || v.Statuses[0].Build.Phase != "parse" {
		t.Errorf("statuses = %+v, want the observer pass's building status", v.Statuses)
	}
}

func postCodeIndex(t *testing.T, path, body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler(rec, req)
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out.Error
}

// Build sends no analysisScope, so kiro-cli's own default decides.
func TestCodeIndexBuild_CallsStartBuildOnAnObserverPass(t *testing.T) {
	t.Run("started", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		o := oneObserver(t, h)
		o.callResults = map[string]json.RawMessage{
			methodKiroCodeIndexStartBuild: json.RawMessage(`{"buildId":"b2","workspaceRoot":"/tmp/work","analysisScope":"structural-only","status":"started"}`),
		}

		rec := postCodeIndex(t, "/api/code-index/build", "", h.handleCodeIndexBuild)

		if rec.Code != http.StatusNoContent {
			t.Errorf("answer = %d %s, want 204", rec.Code, rec.Body.String())
		}
		p := o.paramsFor(methodKiroCodeIndexStartBuild)
		if p == nil {
			t.Fatalf("startBuild was not called on the observer; calls were %v", o.callLog())
		}
		if p[marotte.KeySessionID] != o.SessionID() {
			t.Errorf("sessionId = %v, want the observer's %q", p[marotte.KeySessionID], o.SessionID())
		}
		if _, ok := p["analysisScope"]; ok {
			t.Errorf("params = %v, want no analysisScope", p)
		}
	})
	t.Run("refused by kiro-cli", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		o := oneObserver(t, h)
		o.callRPCErrs = map[string]*marotte.RPCError{methodKiroCodeIndexStartBuild: {
			Code: -32000, Message: "The code index is not enabled.",
			Data: json.RawMessage(`{"errorType":"CodeIntelligenceNotEnabledError","retryErrorType":"CLIENT_ERROR"}`),
		}}

		rec := postCodeIndex(t, "/api/code-index/build", "", h.handleCodeIndexBuild)

		if rec.Code != http.StatusConflict || errorOf(t, rec) != "The code index is not enabled." {
			t.Errorf("answer = %d %s, want 409 with kiro-cli's message", rec.Code, rec.Body.String())
		}
	})
	t.Run("no answer", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		o := oneObserver(t, h)
		o.callErrs = map[string]error{methodKiroCodeIndexStartBuild: errors.New("kas gone")}

		rec := postCodeIndex(t, "/api/code-index/build", "", h.handleCodeIndexBuild)

		if rec.Code != http.StatusBadGateway || errorOf(t, rec) != "kiro-cli did not start the build." {
			t.Errorf("answer = %d %s, want 502", rec.Code, rec.Body.String())
		}
	})
}

func TestCodeIndexCancel_PassesTheBuildID(t *testing.T) {
	for _, tc := range []struct {
		name, body, kas string
		want            int
		wantError       string
		called          bool
	}{
		{name: "canceled", body: `{"build_id":"b2"}`, kas: `{"status":"canceled"}`, want: http.StatusNoContent, called: true},
		{
			name: "nothing running", body: `{"build_id":"b2"}`, kas: `{"status":"not-running"}`, want: http.StatusConflict,
			wantError: "No index build is running.", called: true,
		},
		{name: "no build named", body: `{}`, want: http.StatusBadRequest, wantError: "build_id names the build to cancel."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			o := oneObserver(t, h)
			o.callResults = map[string]json.RawMessage{methodKiroCodeIndexCancelBuild: json.RawMessage(tc.kas)}

			rec := postCodeIndex(t, "/api/code-index/cancel", tc.body, h.handleCodeIndexCancel)

			if rec.Code != tc.want {
				t.Errorf("answer = %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.wantError != "" && errorOf(t, rec) != tc.wantError {
				t.Errorf("error = %q, want %q", errorOf(t, rec), tc.wantError)
			}
			if called := slices.Contains(o.callLog(), methodKiroCodeIndexCancelBuild); called != tc.called {
				t.Fatalf("cancelBuild called = %v, want %v; calls were %v", called, tc.called, o.callLog())
			}
			if tc.called && o.paramsFor(methodKiroCodeIndexCancelBuild)["buildId"] != "b2" {
				t.Errorf("params = %v, want buildId b2", o.paramsFor(methodKiroCodeIndexCancelBuild))
			}
		})
	}
}
