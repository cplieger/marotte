package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
)

// writtenExit is the engine ending with the request on the wire: KAS may have taken it, and a run it took survives
// the restart paused (KAS's initialize sweep reconciles a running run to paused).
func writtenExit() error {
	return &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true}
}

// unwrittenExit is the engine ending before the request's frame reached KAS.
func unwrittenExit() error {
	return &marotte.TransportError{
		Err: fmt.Errorf("%w: %w", marotte.ErrBridgeExited, marotte.ErrNotWritten), Retryable: true,
	}
}

func TestVerdictOf_AnEngineEndIsUnconfirmedOnlyForAWrittenRequest(t *testing.T) {
	cases := map[string]struct {
		err  error
		want kasVerdict
	}{
		"an end after the write":       {writtenExit(), kasUnconfirmed},
		"an end before the write":      {unwrittenExit(), kasRefused},
		"a write the engine fenced":    {fmt.Errorf("write: %w", marotte.ErrNotWritten), kasRefused},
		"an undecoded answer":          {undecodedReply(), kasUnconfirmed},
		"a refusal KAS itself sends":   {&marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "Parse error"}, kasRefused},
		"an answer read and succeeded": {nil, kasTook},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := verdictOf(c.err); got != c.want {
				t.Errorf("verdictOf(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// An invoke the engine ended under keeps the run when it was written, since the run survives paused, and gives it
// back when it never reached KAS.
func TestLaunch_AnEngineEndUnderInvokeKeepsTheRunOnlyIfWritten(t *testing.T) {
	t.Run("written, on the launch route", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_1")
		br.setCallErr(methodKiroWorkflowInvoke, writtenExit())

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
			t.Error("the run's deadline was not armed")
		}
	})
	t.Run("unwritten, on a schedule", func(t *testing.T) {
		h, br, st, entry := nightlyLaunch(t)
		br.setCallErr(methodKiroWorkflowInvoke, unwrittenExit())

		id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{},
			time.Now().Add(time.Hour))
		if id != "wf_1" || err == nil {
			t.Errorf("LaunchScheduled = %q, %v; want wf_1 failed", id, err)
		}
		if _, held := h.runs.lease("wf_1"); held {
			t.Error("the lease of a run whose start never reached KAS survived")
		}
		if row := st.List()[0]; row.LastStatus != schedule.StatusFailed {
			t.Errorf("row = %+v, want failed", row)
		}
	})
}

// A workflow/new the engine ended under may have created a run only if it was written.
func TestLaunch_AnEngineEndUnderNewSendsTheReaderToHistoryOnlyIfWritten(t *testing.T) {
	cases := map[string]struct {
		err     error
		history bool
	}{
		"written":   {writtenExit(), true},
		"unwritten": {unwrittenExit(), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_1")
			br.setCallErr(methodKiroWorkflowNew, c.err)

			rec := httptest.NewRecorder()
			h.runRoutes.handleLaunch(rec, launchReq(t))

			if got := strings.Contains(rec.Body.String(), "History"); got != c.history {
				t.Errorf("POST /api/runs = %d %s, sends to History = %v, want %v",
					rec.Code, rec.Body.String(), got, c.history)
			}
		})
	}
}

// A retry the engine ended under follows launch's rule: written keeps the minted lease and re-arms, unwritten gives
// it back.
func TestHandleRetry_AnEngineEndKeepsTheLeaseOnlyIfWritten(t *testing.T) {
	t.Run("written", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true, "final-verify")
		br.setCallErr(methodKiroWorkflowRetry, writtenExit())

		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))

		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Refresh") {
			t.Errorf("the retry = %d %s, want %d sending the reader to a refresh",
				rec.Code, rec.Body.String(), http.StatusBadGateway)
		}
		if _, held := h.runs.lease("wf_1"); !held {
			t.Error("the lease the retry minted was released, so a retry KAS took runs with no deadline")
		}
		if !h.runs.bounded("wf_1") {
			t.Error("the run was not re-armed")
		}
	})
	t.Run("unwritten", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true, "final-verify")
		br.setCallErr(methodKiroWorkflowRetry, unwrittenExit())

		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))

		if strings.Contains(rec.Body.String(), "Refresh") {
			t.Errorf("the retry = %d %s, want no claim that KAS may have taken it", rec.Code, rec.Body.String())
		}
		if _, held := h.runs.lease("wf_1"); held {
			t.Error("the lease minted for a retry that never reached KAS was not given back")
		}
	})
}

// A write the engine ended before is settled at once, so it never holds the run in the interrupted set.
func TestWrite_AnUnwrittenRequestLeavesNoMutation(t *testing.T) {
	h, br := seedChatParentedRun(t, true, "final-verify")
	br.setCallErr(methodKiroWorkflowRetry, unwrittenExit())
	host, err := h.runs.ensureResident(t.Context(), "wf_1")
	if err != nil {
		t.Fatalf("Setup: ensureResident: %v", err)
	}
	waitMutationsEmpty(t, h)

	_, _ = host.call(t.Context(), h.runs, "wf_1", methodKiroWorkflowRetry, map[string]any{keyWorkflowID: "wf_1"})

	if !h.runs.mutations.empty() {
		t.Error("a request the engine never received is held as a mutation that may have taken")
	}
}

func waitMutationsEmpty(t *testing.T, h *Runtime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !h.runs.mutations.empty() {
		if time.Now().After(deadline) {
			t.Fatal("Setup: the run's mutations never settled")
		}
		time.Sleep(time.Millisecond)
	}
}
