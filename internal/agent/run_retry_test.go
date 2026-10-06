package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// retryReply builds `_kiro/workflow/retry`'s own reply shape:
// `{workflowId, status, retriedNodeIds[]}`.
func retryReply(t *testing.T, workflowID, status string, nodes ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID, "status": status, "retriedNodeIds": nodes,
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the retry reply: %s", err)
	}
	return raw
}

// retryReq builds POST /api/runs/{id}/retry with the path value set.
func retryReq(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+id+"/retry", bytes.NewReader(nil))
	req.SetPathValue("id", id)
	return req
}

// seedChatParentedRun stages one aborted run parented on a chat's session, the chat's bridge optionally live, KAS answering inspect, list and retry.
func seedChatParentedRun(t *testing.T, openChat bool, nodes ...string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "publish",
			"status": "aborted", "parentSessionId": "sess_owned",
		}),
		methodKiroWorkflowRetry: retryReply(t, "wf_1", "running", nodes...),
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Findings cleanup"
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}
	if openChat {
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: opening the chat's bridge: %s", err)
		}
	}
	return h, br
}

// gateAnswer resolves the retry route's real affordance and checks both threaded facts.
func gateAnswer(t *testing.T, h *Runtime, workflowID string) *runAffordance {
	t.Helper()
	aff := h.runs.affordance(t.Context(), workflowID, "aborted")
	if aff.origin.parent.chat != "c1" {
		t.Fatalf("Setup: the gate resolved parent %q, want the launching chat c1", aff.origin.parent.chat)
	}
	if aff.origin.recipe != "publish" {
		t.Fatalf("Setup: the gate resolved recipe %q, want publish off KAS's run list", aff.origin.recipe)
	}
	return aff
}

// TestHandleRetry_AnswersTheOutcomeRatherThanOk pins that the reply carries KAS's status and reset node ids.
func TestHandleRetry_AnswersTheOutcomeRatherThanOk(t *testing.T) {
	for name, nodes := range map[string][]string{
		"five nodes reset": {"phase-c-loop", "phase-d-loop", "final-verify", "plan", "setup"},
		"one node reset":   {"final-verify"},
		"NOTHING reset":    {},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := seedChatParentedRun(t, true, nodes...)
			rec := httptest.NewRecorder()
			h.runRoutes.handleRetry(rec, retryReq("wf_1"))
			if rec.Code != http.StatusOK {
				t.Fatalf("POST retry = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var got marotte.RunRetriedResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding the retry reply: %s", err)
			}
			if got.Status != "running" {
				t.Errorf("status = %q, want %q", got.Status, "running")
			}
			if !slices.Equal(got.RetriedNodeIDs, nodes) {
				t.Errorf("retried_node_ids = %v, want %v; the reply IS the outcome report, and "+
					"a caller that cannot count the reset nodes cannot tell a retry from a no-op",
					got.RetriedNodeIDs, nodes)
			}
			// An `ok` alongside would leave the ambiguity for old readers.
			if strings.Contains(rec.Body.String(), `"ok"`) {
				t.Errorf("the reply still carries `ok`: %s", rec.Body.String())
			}
		})
	}
}

// TestRetry_AddressesTheRunsRealHost pins that keying on runChatID alone would spawn a second engine for a chat run whose process lives.
func TestRetry_AddressesTheRunsRealHost(t *testing.T) {
	h, br := seedChatParentedRun(t, true, "final-verify")
	aff := gateAnswer(t, h, "wf_1")
	starts := br.startCount()

	out, err := h.runs.Retry(t.Context(), "wf_1", aff)
	if err != nil {
		t.Fatalf("Retry on a chat-parented run = %v, want nil", err)
	}
	if len(out.RetriedNodeIDs) != 1 {
		t.Errorf("outcome = %+v, want the one node KAS reported", out)
	}
	calls := br.callLog()
	if !slices.Contains(calls, methodKiroWorkflowRetry) {
		t.Fatalf("the verb never reached KAS; calls were %v", calls)
	}
	if got := br.startCount() - starts; got != 0 {
		t.Errorf("%d processes started for a run whose launching chat's process is alive", got)
	}
	if sb := h.bridge.mgr.get(runChatID("wf_1")); sb != nil {
		t.Error("a bridge was registered under the run's synthetic id for a run its launching " +
			"chat already hosts")
	}
}

// TestRetry_ReachesAnUnhostedChatRunThroughItsChat pins that retried on the launching chat's bridge.
func TestRetry_ReachesAnUnhostedChatRunThroughItsChat(t *testing.T) {
	// Nothing here holds the run.
	h, br := seedChatParentedRun(t, false, "phase-c-loop")

	if _, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1")); err != nil {
		t.Fatalf("Retry on a run nothing hosts = %v, want nil", err)
	}
	calls := br.callLog()
	if !slices.Contains(calls, methodKiroWorkflowRetry) {
		t.Fatalf("the retry never reached KAS; calls were %v", calls)
	}
	// Through the chat, whose session load carries the presets.
	if h.bridge.mgr.get("c1") == nil {
		t.Error("the launching chat's bridge was not opened, so its session is not live")
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("a run bridge was registered for a chat-parented run")
	}
}

// TestRetry_ARefusedRetryOnAReHostedRunLeavesNothingBehind pins that no stray subprocess or lease; the chat's bridge stays.
func TestRetry_ARefusedRetryOnAReHostedRunLeavesNothingBehind(t *testing.T) {
	h, br := seedChatParentedRun(t, false)
	br.setCallRPCErr(methodKiroWorkflowRetry,
		&marotte.RPCError{Code: -32603, Message: "Workflow wf_1 not found on disk"})

	if _, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1")); err == nil {
		t.Fatal("Retry = nil for a retry KAS refused")
	}
	if sb := h.bridge.mgr.get(runChatID("wf_1")); sb != nil {
		t.Error("a run bridge was left registered for a run that never re-drove")
	}
	if h.bridge.mgr.get("c1") == nil {
		t.Error("a failed retry closed the chat's bridge, which belongs to the conversation")
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the lease minted for the attempt was not given back")
	}
}

// TestRetry_AnInBandRefusalIsAFailure pins that KAS refuses in band, so the reply's `error` must fail the verb.
func TestRetry_AnInBandRefusalIsAFailure(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallRPCErr(methodKiroWorkflowRetry,
		&marotte.RPCError{Code: -32603, Message: "Cannot retry a completed workflow"})

	out, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
	if err == nil {
		t.Fatalf("Retry = (%+v, nil) for a refusal KAS answered in band; the run was not "+
			"re-driven and its previous terminal reason is still the truth about it", out)
	}
	if !isRPCRefusal(err) {
		t.Errorf("Retry error = %v, want a JSON-RPC refusal the route can classify as a 409", err)
	}
}

// TestHandleRetry_ForwardsKASsOwnSentence pins that a constant "internal error" hides the reason.
func TestHandleRetry_ForwardsKASsOwnSentence(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallRPCErr(methodKiroWorkflowRetry, &marotte.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data:    json.RawMessage(`{"details":"Workflow wf_1 is not registered. Load or create it first."}`),
	})

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("a KAS refusal = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not registered") {
		t.Errorf("the refusal body = %s, want KAS's own sentence", body)
	}
	if strings.Contains(body, "internal error") {
		t.Errorf("the refusal body = %s, want the diagnostic rather than the constant", body)
	}
}

// TestHandleRetry_AnEngineThatWillNotStartAnswersInsideTheClientsWindow pins that the browser aborts at 30s, so the
// verb answers 503 first rather than letting the client tear down its fresh bridge.
func TestHandleRetry_AnEngineThatWillNotStartAnswersInsideTheClientsWindow(t *testing.T) {
	if retryTimeout >= clientRequestBudget {
		t.Errorf("retryTimeout = %v, want it below the client's %v request budget, or the "+
			"browser aborts the handoff mid-flight", retryTimeout, clientRequestBudget)
	}

	h, br := seedChatParentedRun(t, false)
	// Start the utility bridge first, or the gate parks the status read and the test hangs.
	if _, err := h.runs.rawInspect(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Setup: warming the utility bridge: %s", err)
	}
	// A never-returning Start stands in for a cold runtime unpack.
	br.setStartGate(make(chan struct{}))
	prev := retryTimeout
	retryTimeout = time.Millisecond
	t.Cleanup(func() { retryTimeout = prev })
	before := len(br.callLog())

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a retry whose engine never started = %d, want %d: %s",
			rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Try again") {
		t.Errorf("the 503 body = %s, want it to name the remedy", rec.Body.String())
	}
	if slices.Contains(br.callLog()[before:], methodKiroWorkflowRetry) {
		t.Error("the verb was issued on a bridge that never started")
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the abandoned attempt kept the lease it minted")
	}
}

// TestHandleRetry_RefusesWhatTheAffordanceRefuses pins that the gate is server-side.
func TestHandleRetry_RefusesWhatTheAffordanceRefuses(t *testing.T) {
	t.Run("a completed run is refused, naming its status", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "completed", ""))
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("retry on a completed run = %d, want %d", rec.Code, http.StatusConflict)
		}
		if !strings.Contains(rec.Body.String(), "completed") {
			t.Errorf("the refusal = %s, want it to name the status", rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
			t.Error("the verb reached KAS for a status it refuses")
		}
	})

	// An aborted chat-parented run must stay retryable.
	t.Run("an aborted CHAT-PARENTED run is accepted", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true, "phase-c-loop")
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("retry on an aborted chat-parented run = %d, want %d: %s",
				rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	// The two unusable-status arms answer differently.
	t.Run("a status read that FAILS is a 500", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallErr(methodKiroWorkflowInspect, errors.New("no such workflow"))
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("an unreadable status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})

	t.Run("a run KAS does not know is a 404", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		// No workflow verb: "" status, not a fault.
		br.setCallErr(methodKiroWorkflowInspect, workflow.ErrUnknownMethod)
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("retry on an unknown run = %d, want %d: %s",
				rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
			t.Error("the verb was issued for a run that has no status")
		}
	})
}

// TestRetry_AnUnreadableOutcomeIsNotReportedAsAFailedRetry pins that KAS accepted the verb, so keep the lease and
// bridge and re-arm.
func TestRetry_AnUnreadableOutcomeIsNotReportedAsAFailedRetry(t *testing.T) {
	// One reply per usable-outcome-free shape.
	unreadable := map[string]json.RawMessage{
		"a reply that is not an object": json.RawMessage(`"retried"`),
		"a reply with no outcome":       json.RawMessage(``),
		"a reply about another run":     retryReply(t, "wf_other", "running", "plan"),
	}

	for name, reply := range unreadable {
		t.Run(name+", on the run's own host", func(t *testing.T) {
			// One fake serves the runtime, so this is the chat's own.
			h, br := seedChatParentedRun(t, true)
			h.runs.claimTermination("wf_1")
			h.runs.recordEnd("wf_1", runEndOverran)
			br.setCallResult(methodKiroWorkflowRetry, reply)

			_, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
			if !errors.Is(err, errRetryOutcomeUnreadable) {
				t.Fatalf("Retry = %v, want errRetryOutcomeUnreadable: the verb LANDED, so "+
					"telling the reader to retry would ask for the work twice", err)
			}
			// The run may be executing, so the old termination must go.
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("the run still reads %q, so its row renders as aborted while it may "+
					"be running", got)
			}
			if !h.runs.bounded("wf_1") {
				t.Error("the run carries no deadline, so nothing bounds work that may be running")
			}
		})

		t.Run(name+", on a re-hosted run", func(t *testing.T) {
			h, br := seedChatParentedRun(t, false)
			br.setCallResult(methodKiroWorkflowRetry, reply)

			_, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
			if !errors.Is(err, errRetryOutcomeUnreadable) {
				t.Fatalf("Retry = %v, want errRetryOutcomeUnreadable", err)
			}
			if h.bridge.mgr.get("c1") == nil {
				t.Error("the chat bridge that just started re-driving the run was closed because " +
					"its reply could not be parsed")
			}
			if _, held := h.runs.lease("wf_1"); !held {
				t.Error("the lease was released for a run that may be executing, so the wall " +
					"clock no longer bounds it and its recipe reads as free")
			}
		})
	}
}

// The answer must not be a "try again" 500.
func TestHandleRetry_AnUnreadableOutcomeTellsTheReaderToRefresh(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowRetry, json.RawMessage(`"retried"`))

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("an unreadable outcome = %d, want %d: %s",
			rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Refresh") {
		t.Errorf("the body = %s, want the remedy that is actually the reader's; a retry that "+
			"landed must not be reported as one to repeat", body)
	}
	if strings.Contains(body, "internal error") {
		t.Errorf("the body = %s, want the diagnostic rather than the constant", body)
	}
}

// TestRetry_ReadsTheParentTheGateResolved pins that the verb uses the gate's answer, since a second read can lose
// the parent and re-host a live run. Asserted as a mechanism: background listing makes counts unstable.
func TestRetry_ReadsTheParentTheGateResolved(t *testing.T) {
	h, br := seedChatParentedRun(t, true, "final-verify")
	// Resolved while the inventory carries the parent.
	aff := gateAnswer(t, h, "wf_1")
	// A later read: same run, no parent session.
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
		"workflowId": "wf_1", "name": "publish", "status": "aborted",
	}))

	starts := br.startCount()
	if _, err := h.runs.Retry(t.Context(), "wf_1", aff); err != nil {
		t.Fatalf("Retry = %v, want nil: the verb re-asked and got a different answer", err)
	}
	if got := br.startCount() - starts; got != 0 {
		t.Errorf("%d processes started although the gate had already resolved the run's live host", got)
	}
	if sb := h.bridge.mgr.get(runChatID("wf_1")); sb != nil {
		t.Error("a second engine was registered under the run's synthetic id for a run its " +
			"launching chat already hosts")
	}
}

func TestHandleRetry_RejectsNonPOSTAndAMissingID(t *testing.T) {
	h, _ := seedChatParentedRun(t, true)
	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/retry", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	rec = httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, httptest.NewRequest(http.MethodPost, "/api/runs//retry", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no id = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
