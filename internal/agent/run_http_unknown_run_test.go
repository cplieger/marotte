package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// unknownRunMux is the run routes over an engine that answers every per-run workflow method as KAS does for an id
// it has no run for.
func unknownRunMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h, _, br := newTestHub()
	for _, m := range []string{
		methodKiroWorkflowInspect, methodKiroWorkflowCancel, methodKiroWorkflowPause, methodKiroWorkflowResume,
		methodKiroWorkflowRetry, methodKiroWorkflowDelete, methodKiroWorkflowUpdate,
	} {
		br.setCallRPCErr(m, workflowNotFoundErr())
	}
	br.setCallResult(methodKiroWorkflowList, kasRuns(t))
	mux := http.NewServeMux()
	(&runRoutes{runs: h.runs, epoch: testEpoch}).register(mux)
	return mux
}

// A client stops on 404 and retries a 5xx, so an id KAS has no run for is 404 on every verb class: the ones the
// affordance gate reads first, the status-gated ones, the ones KAS answers itself, the step verbs and the reads.
func TestRunVerbs_AnswerNotFoundForAnUnknownRun(t *testing.T) {
	cases := []struct {
		name, method, target, body string
	}{
		{"retry", http.MethodPost, "/api/runs/wf_1/retry", ``},
		{"extend", http.MethodPost, "/api/runs/wf_1/extend", `{"node_id":"loop","iterations":1}`},
		{"finish-loop", http.MethodPost, "/api/runs/wf_1/finish-loop", `{"node_id":"loop"}`},
		{"pause", http.MethodPost, "/api/runs/wf_1/pause", ``},
		{"resume", http.MethodPost, "/api/runs/wf_1/resume", ``},
		{"cancel", http.MethodPost, "/api/runs/wf_1/cancel", ``},
		{"delete", http.MethodDelete, "/api/runs/wf_1", ``},
		{"controls", http.MethodGet, "/api/runs/wf_1/controls", ``},
		{"step message", http.MethodPost, "/api/runs/wf_1/steps/review/message", `{"text":"hi","message_id":"m1"}`},
		{"read", http.MethodGet, "/api/runs/wf_1", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := unknownRunMux(t)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body)))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d %s, want 404", tc.method, tc.target, rec.Code, rec.Body.String())
			}
			if got := errorText(t, rec); got != "run not found" {
				t.Errorf("%s %s error = %q, want %q", tc.method, tc.target, got, "run not found")
			}
		})
	}
}

func TestWorkflowNotFound_MatchesOnlyAConfirmedAbsence(t *testing.T) {
	incomplete := workflowNotFoundErr()
	incomplete.Data = json.RawMessage(`{"details":"x","errorType":"WorkflowNotFoundError","searchIncomplete":true}`)
	cases := []struct {
		err  error
		name string
		want bool
	}{
		{name: "KAS has no run by that id", err: workflowNotFoundErr(), want: true},
		{name: "wrapped", err: fmt.Errorf("workflow inspect call: %w", workflowNotFoundErr()), want: true},
		// KAS could not search every relayed session, so the run may still exist there.
		{name: "an incomplete search", err: incomplete, want: false},
		{name: "another refusal", err: &marotte.RPCError{
			Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"session not idle"}`),
		}, want: false},
		{name: "a transport failure", err: errors.New("bridge exited"), want: false},
		{name: "no error", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowNotFound(tc.err); got != tc.want {
				t.Errorf("workflowNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
