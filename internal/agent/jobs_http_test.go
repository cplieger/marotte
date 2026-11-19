package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	return jobRequestWithBody(t, h, method, target, http.NoBody)
}

func jobRequestWithBody(t *testing.T, h *Runtime, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.registerJobRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, body))
	return rec
}

// seedRunningJob lists job 7 as running in c1's session.
func seedRunningJob(t *testing.T, h *Runtime, br *fakeBridge) {
	t.Helper()
	h.translator.HandleJobUpdate(t.Context(), "c1", &marotte.RPCResponse{
		Method: methodKiroJobUpdate,
		Params: json.RawMessage(`{"sessionId":"` + string(br.SessionID()) + `","id":"7","kind":"shell","description":"sleep 60","status":"running"}`),
	})
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
	t.Run("unknown to kiro-cli", func(t *testing.T) {
		h, br := jobsHub(t)
		seedRunningJob(t, h, br)
		br.mu.Lock()
		br.callRPCErrs = map[string]*marotte.RPCError{methodKiroJobOutput: {Code: rpcInvalidParams, Message: "the session has no job '7'"}}
		br.mu.Unlock()

		rec := jobRequest(t, h, http.MethodGet, "/api/chats/c1/jobs/7/output")

		if rec.Code != http.StatusConflict || errorOf(t, rec) != "the session has no job '7'" {
			t.Errorf("answer = %d %s, want 409 with kiro-cli's message", rec.Code, rec.Body.String())
		}
		if got := h.translator.RunningJobs(); len(got) != 0 {
			t.Errorf("running = %+v, want the job dropped", got)
		}
	})
	t.Run("not a number", func(t *testing.T) {
		h, br := jobsHub(t)

		rec := jobRequest(t, h, http.MethodGet, "/api/chats/c1/jobs/7/output?lines=many")

		if rec.Code != http.StatusBadRequest || slices.Contains(br.callLog(), methodKiroJobOutput) {
			t.Errorf("answer = %d, calls %v; want 400 and no call", rec.Code, br.callLog())
		}
	})
}

// Stop answers 202 and leaves the end to job_update. kiro-cli answers wasRunning false both for a job that already
// ended and for one it no longer holds: either is a 409 that drops the row.
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
		seedRunningJob(t, h, br)
		br.setCallResult(methodKiroJobCancel, json.RawMessage(`{"jobs":[{"jobId":"7","wasRunning":false,"confirmed":false}]}`))

		rec := jobRequest(t, h, http.MethodPost, "/api/chats/c1/jobs/7/cancel")

		if rec.Code != http.StatusConflict || errorOf(t, rec) != "The job had already ended." {
			t.Errorf("answer = %d %s, want 409", rec.Code, rec.Body.String())
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

// Move to background sends the card's call on the chat's session and answers kiro-cli's outcome: moved, ended and
// not_movable are answers, move_failed or an answer outside them is a 502, and a refusal is a 409 with its text that
// drops no job row, since the move names no job.
func TestJobsAPI_MoveSendsTheCallAndAnswersKiroCLIsOutcome(t *testing.T) {
	const target = "/api/chats/c1/jobs"
	move := func(t *testing.T, h *Runtime, body string) *httptest.ResponseRecorder {
		t.Helper()
		return jobRequestWithBody(t, h, http.MethodPost, target, strings.NewReader(body))
	}
	answer := func(t *testing.T, rec *httptest.ResponseRecorder) marotte.BackgroundJobMove {
		t.Helper()
		var out marotte.BackgroundJobMove
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body %q: %v", rec.Body.String(), err)
		}
		return out
	}

	t.Run("moved", func(t *testing.T) {
		h, br := jobsHub(t)
		br.setCallResult(methodKiroJobMove, json.RawMessage(`{"outcome":"moved","jobId":"7"}`))

		rec := move(t, h, `{"tool_call_id":"tc-1"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if got := answer(t, rec); got != (marotte.BackgroundJobMove{Outcome: marotte.BackgroundJobMoved, JobID: "7"}) {
			t.Errorf("body = %+v, want moved as job 7", got)
		}
		if p := br.paramsFor(methodKiroJobMove); p["toolCallId"] != "tc-1" || fmt.Sprint(p[marotte.KeySessionID]) != string(br.SessionID()) {
			t.Errorf("params = %v, want call tc-1 on the chat's session %q", p, br.SessionID())
		}
	})
	for _, outcome := range []marotte.BackgroundJobMoveOutcome{marotte.BackgroundJobMoveEnded, marotte.BackgroundJobNotMovable} {
		t.Run(string(outcome), func(t *testing.T) {
			h, br := jobsHub(t)
			br.setCallResult(methodKiroJobMove, json.RawMessage(`{"outcome":"`+string(outcome)+`"}`))

			rec := move(t, h, `{"tool_call_id":"tc-1"}`)

			if rec.Code != http.StatusOK {
				t.Fatalf("answer = %d %s, want 200", rec.Code, rec.Body.String())
			}
			if got := answer(t, rec); got != (marotte.BackgroundJobMove{Outcome: outcome}) {
				t.Errorf("body = %+v, want %s", got, outcome)
			}
		})
	}
	for name, result := range map[string]string{
		"move_failed":         `{"outcome":"move_failed"}`,
		"unknown outcome":     `{"outcome":"teleported","jobId":"7"}`,
		"moved without jobId": `{"outcome":"moved"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, br := jobsHub(t)
			br.setCallResult(methodKiroJobMove, json.RawMessage(result))

			rec := move(t, h, `{"tool_call_id":"tc-1"}`)

			if rec.Code != http.StatusBadGateway || errorOf(t, rec) != "kiro-cli could not move the command to the background." {
				t.Errorf("answer = %d %s, want 502", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("jobs not enabled", func(t *testing.T) {
		h, br := jobsHub(t)
		seedRunningJob(t, h, br)
		br.mu.Lock()
		br.callRPCErrs = map[string]*marotte.RPCError{methodKiroJobMove: {Code: rpcInvalidParams, Message: "Background jobs are not enabled for this session."}}
		br.mu.Unlock()

		rec := move(t, h, `{"tool_call_id":"7"}`)

		if rec.Code != http.StatusConflict || errorOf(t, rec) != "Background jobs are not enabled for this session." {
			t.Errorf("answer = %d %s, want 409 with kiro-cli's message", rec.Code, rec.Body.String())
		}
		if got := h.translator.RunningJobs()["c1"].Jobs; len(got) != 1 {
			t.Errorf("running = %+v, want job 7 still listed", got)
		}
	})
	t.Run("no session", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })

		rec := move(t, h, `{"tool_call_id":"tc-1"}`)

		if rec.Code != http.StatusConflict || slices.Contains(br.callLog(), methodKiroJobMove) {
			t.Errorf("answer = %d, calls %v; want 409 and no call", rec.Code, br.callLog())
		}
	})
	for name, id := range map[string]string{
		"empty":             "",
		"control character": "tc1",
		"untrimmed":         " tc-1",
		"257 bytes":         strings.Repeat("a", 257),
	} {
		t.Run("bad tool_call_id "+name, func(t *testing.T) {
			h, br := jobsHub(t)
			raw, err := json.Marshal(marotte.BackgroundJobMoveRequest{ToolCallID: id})
			if err != nil {
				t.Fatal(err)
			}

			rec := move(t, h, string(raw))

			if rec.Code != http.StatusBadRequest || slices.Contains(br.callLog(), methodKiroJobMove) {
				t.Errorf("answer = %d, calls %v; want 400 and no call", rec.Code, br.callLog())
			}
		})
	}
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
