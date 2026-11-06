package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
)

func undecodedReply() error {
	return fmt.Errorf("%w: %w", marotte.ErrReplyUndecoded,
		&marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "marotte could not decode kiro-cli's answer"})
}

func launchReq(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/runs",
		strings.NewReader(`{"source":"bundled://publish"}`))
}

// An invoke whose answer arrived unread may have started the run, so the run keeps its lease and its deadline,
// the schedule row is not marked failed, and the launch answers 502.
func TestLaunch_AnInvokeWhoseAnswerWasUnreadKeepsItsLease(t *testing.T) {
	unread := map[string]error{
		"an undecoded answer": undecodedReply(),
		"an oversize answer":  &marotte.TransportError{Err: marotte.ErrFrameTooLarge},
	}
	for name, callErr := range unread {
		t.Run(name+" on the launch route", func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_1")
			br.setCallErr(methodKiroWorkflowInvoke, callErr)

			rec := httptest.NewRecorder()
			h.runRoutes.handleLaunch(rec, launchReq(t))

			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "History") {
				t.Errorf("POST /api/runs = %d %s, want %d sending the reader to History",
					rec.Code, rec.Body.String(), http.StatusBadGateway)
			}
			if _, held := h.runs.lease("wf_1"); !held {
				t.Error("the lease was released, so a run KAS may have started runs with no envelope")
			}
			if !h.runs.bounded("wf_1") {
				t.Error("the run's deadline was not armed, so nothing bounds a run KAS may have started")
			}
		})
		t.Run(name+" on a schedule", func(t *testing.T) {
			h, br, st, entry := nightlyLaunch(t)
			br.setCallErr(methodKiroWorkflowInvoke, callErr)

			id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{},
				time.Now().Add(time.Hour))
			// The runner records a slot failed on any error (schedule.Runner.settle), so a run that may be running
			// is reported launched.
			if id != "wf_1" || err != nil {
				t.Errorf("LaunchScheduled = %q, %v; want wf_1 launched", id, err)
			}
			if _, held := h.runs.lease("wf_1"); !held {
				t.Error("the scheduled run's lease was released")
			}
			if row := st.Clocks()[0]; row.LastRunID != "wf_1" || row.LastStatus == launchrule.StatusFailed {
				t.Errorf("row = %+v, want wf_1 not recorded failed", row)
			}
		})
	}

	t.Run("a parse error KAS itself sends", func(t *testing.T) {
		h, br, st, entry := nightlyLaunch(t)
		br.setCallRPCErr(methodKiroWorkflowInvoke,
			&marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "Parse error"})

		id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{},
			time.Now().Add(time.Hour))
		if id != "wf_1" || err == nil || errors.Is(err, errLaunchUnconfirmed) {
			t.Errorf("LaunchScheduled = %q, %v; want wf_1 refused", id, err)
		}
		if _, held := h.runs.lease("wf_1"); held {
			t.Error("the lease of a run KAS refused to start survived")
		}
		if row := st.Clocks()[0]; row.LastStatus != launchrule.StatusFailed {
			t.Errorf("row = %+v, want failed", row)
		}
	})

	t.Run("a parse error KAS itself sends on the launch route", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_1")
		br.setCallRPCErr(methodKiroWorkflowInvoke,
			&marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "Parse error"})

		rec := httptest.NewRecorder()
		h.runRoutes.handleLaunch(rec, launchReq(t))

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/runs = %d %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})
}

// A workflow/new whose answer was unread, or named no run, may have created one: 502 sending the reader to History,
// and a schedule records the slot failed rather than relaunching into a second run.
func TestLaunch_ANewWhoseAnswerWasUnreadSendsTheReaderToHistory(t *testing.T) {
	cases := map[string]func(br *fakeBridge){
		"an undecoded answer": func(br *fakeBridge) { br.setCallErr(methodKiroWorkflowNew, undecodedReply()) },
		"an oversize answer": func(br *fakeBridge) {
			br.setCallErr(methodKiroWorkflowNew, &marotte.TransportError{Err: marotte.ErrFrameTooLarge})
		},
		"an answer naming no run": func(br *fakeBridge) {
			br.mu.Lock()
			br.callResults[methodKiroWorkflowNew] = json.RawMessage(`{}`)
			br.mu.Unlock()
		},
	}
	for name, arm := range cases {
		t.Run(name+" on the launch route", func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_1")
			arm(br)

			rec := httptest.NewRecorder()
			h.runRoutes.handleLaunch(rec, launchReq(t))

			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "History") {
				t.Errorf("POST /api/runs = %d %s, want %d sending the reader to History",
					rec.Code, rec.Body.String(), http.StatusBadGateway)
			}
		})
		t.Run(name+" on a schedule", func(t *testing.T) {
			h, br, _, entry := nightlyLaunch(t)
			arm(br)

			id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{},
				time.Now().Add(time.Hour))
			if id != "" || !errors.Is(err, errLaunchNewUnconfirmed) || errors.Is(err, schedule.ErrEngineBusy) {
				t.Errorf("LaunchScheduled = %q, %v; want no id, errLaunchNewUnconfirmed and no engine-busy retry", id, err)
			}
		})
	}
}
