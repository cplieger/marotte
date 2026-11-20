package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// jobsHub is a hub with chat c1's session open on br.
func jobsHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: OpenBridge: %v", err)
	}
	return h, br
}

func jobRequest(t *testing.T, h *Runtime, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.registerJobRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody))
	return rec
}

// Output reads the job's lines on the chat's own session; lines is clamped to kiro-cli's 1..1000 before the call.
func TestJobsAPI_OutputClampsLinesAndCallsTheChatsSession(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{query: "", want: 100},
		{query: "?lines=0", want: 1},
		{query: "?lines=-5", want: 1},
		{query: "?lines=250", want: 250},
		{query: "?lines=5000", want: 1000},
	} {
		t.Run("lines"+tc.query, func(t *testing.T) {
			h, br := jobsHub(t)
			br.setCallResult(methodKiroJobOutput, json.RawMessage(`{"status":"running","lines":["a","b\u001b[31m"],"gap":{"at":3}}`))

			rec := jobRequest(t, h, http.MethodGet, "/api/chats/c1/jobs/7/output"+tc.query)

			if rec.Code != http.StatusOK {
				t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body.String())
			}
			p := br.paramsFor(methodKiroJobOutput)
			if p["jobId"] != "7" || fmt.Sprint(p["lines"]) != fmt.Sprint(tc.want) || fmt.Sprint(p[marotte.KeySessionID]) != string(br.SessionID()) {
				t.Errorf("params = %v, want job 7, %v lines, on the chat's session %q", p, tc.want, br.SessionID())
			}
			var out marotte.BackgroundJobOutput
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != marotte.BackgroundJobRunning || !slices.Equal(out.Lines, []string{"a", "b"}) || !out.Gap {
				t.Errorf("body = %+v, want running, the lines with the escape stripped, and the gap", out)
			}
		})
	}
	t.Run("not a number", func(t *testing.T) {
		h, br := jobsHub(t)

		rec := jobRequest(t, h, http.MethodGet, "/api/chats/c1/jobs/7/output?lines=many")

		if rec.Code != http.StatusBadRequest || slices.Contains(br.callLog(), methodKiroJobOutput) {
			t.Errorf("answer = %d, calls %v; want 400 and no call", rec.Code, br.callLog())
		}
	})
}

// Stop answers 202 and leaves the end to job_update; a job already ended is a 409, and a job kiro-cli does not hold
// is refused with its message and leaves the running list.
func TestJobsAPI_CancelSendsTheJobAndReportsKiroCLIsAnswer(t *testing.T) {
	t.Run("stopping", func(t *testing.T) {
		h, br := jobsHub(t)
		br.setCallResult(methodKiroJobCancel, json.RawMessage(`{"jobs":[{"jobId":"7","wasRunning":true,"confirmed":true}]}`))

		rec := jobRequest(t, h, http.MethodPost, "/api/chats/c1/jobs/7/cancel")

		if rec.Code != http.StatusAccepted {
			t.Errorf("answer = %d %s, want 202", rec.Code, rec.Body.String())
		}
		if p := br.paramsFor(methodKiroJobCancel); p["jobId"] != "7" || fmt.Sprint(p[marotte.KeySessionID]) != string(br.SessionID()) {
			t.Errorf("params = %v, want job 7 on the chat's session", p)
		}
	})
	t.Run("already ended", func(t *testing.T) {
		h, br := jobsHub(t)
		br.setCallResult(methodKiroJobCancel, json.RawMessage(`{"jobs":[{"jobId":"7","wasRunning":false,"confirmed":true}]}`))

		rec := jobRequest(t, h, http.MethodPost, "/api/chats/c1/jobs/7/cancel")

		if rec.Code != http.StatusConflict || errorOf(t, rec) != "The job had already ended." {
			t.Errorf("answer = %d %s, want 409", rec.Code, rec.Body.String())
		}
	})
	t.Run("unknown to kiro-cli", func(t *testing.T) {
		h, br := jobsHub(t)
		h.translator.HandleJobUpdate(t.Context(), "c1", &marotte.RPCResponse{
			Method: methodKiroJobUpdate,
			Params: json.RawMessage(`{"sessionId":"` + string(br.SessionID()) + `","id":"7","kind":"shell","description":"sleep 60","status":"running"}`),
		})
		br.mu.Lock()
		br.callRPCErrs = map[string]*marotte.RPCError{methodKiroJobCancel: {Code: rpcInvalidParams, Message: "the session has no job '7'"}}
		br.mu.Unlock()

		rec := jobRequest(t, h, http.MethodPost, "/api/chats/c1/jobs/7/cancel")

		if rec.Code != http.StatusConflict || errorOf(t, rec) != "the session has no job '7'" {
			t.Errorf("answer = %d %s, want 409 with kiro-cli's message", rec.Code, rec.Body.String())
		}
		if got := h.translator.RunningJobs(); len(got) != 0 {
			t.Errorf("running = %+v, want the job dropped", got)
		}
	})
	t.Run("no session", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })

		rec := jobRequest(t, h, http.MethodPost, "/api/chats/c1/jobs/7/cancel")

		if rec.Code != http.StatusConflict || slices.Contains(br.callLog(), methodKiroJobCancel) {
			t.Errorf("answer = %d, calls %v; want 409 and no call", rec.Code, br.callLog())
		}
	})
}

// The list serves a chat's running jobs only while the session they run in is still open for the chat.
func TestJobsAPI_ListsTheRunningJobsOfEachChatsSession(t *testing.T) {
	h, br := jobsHub(t)
	update := func(session, id string) {
		t.Helper()
		h.translator.HandleJobUpdate(t.Context(), "c1", &marotte.RPCResponse{
			Method: methodKiroJobUpdate,
			Params: json.RawMessage(`{"sessionId":"` + session + `","id":"` + id + `","kind":"shell","description":"d","status":"running"}`),
		})
	}
	list := func() marotte.BackgroundJobsView {
		rec := jobRequest(t, h, http.MethodGet, "/api/jobs")
		var v marotte.BackgroundJobsView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("GET /api/jobs = %d %q: %v", rec.Code, rec.Body.String(), err)
		}
		return v
	}

	update(string(br.SessionID()), "7")
	if v := list(); len(v.Jobs) != 1 || v.Jobs[0].ID != "7" || v.Jobs[0].ChatID != "c1" {
		t.Errorf("jobs = %+v, want c1's job 7", v.Jobs)
	}

	h.coord.closeBridge(t.Context(), "c1", marotte.TurnOutcomeCancelled)
	if v := list(); len(v.Jobs) != 0 {
		t.Errorf("jobs = %+v, want none: they ran in a session the chat no longer holds", v.Jobs)
	}
}
