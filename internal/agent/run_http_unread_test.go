package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A run control whose answer arrived and could not be read may have taken effect: 502 sending the reader to a
// refresh, never KAS's refusal (409) or marotte's own fault (500).
func TestControlHandler_AnAnswerThatCouldNotBeReadIsNotARefusal(t *testing.T) {
	unread := map[string]error{
		"an undecoded answer": undecodedReply(),
		"an oversize answer":  &marotte.TransportError{Err: marotte.ErrFrameTooLarge},
	}
	verbs := map[string]struct {
		handle func(*runRoutes, http.ResponseWriter, *http.Request)
		method string
		status marotte.RunStatus
	}{
		"pause":  {(*runRoutes).handlePause, methodKiroWorkflowPause, "running"},
		"resume": {(*runRoutes).handleResume, methodKiroWorkflowResume, "paused"},
	}
	for vName, v := range verbs {
		for eName, callErr := range unread {
			t.Run(vName+" with "+eName, func(t *testing.T) {
				h, _, br := newTestHub()
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
					methodKiroWorkflowInspect: inspectReply(t, "wf_1", v.status, ""),
				}
				h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
				br.setCallErr(v.method, callErr)
				req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/"+vName, nil)
				req.SetPathValue("id", "wf_1")
				rec := httptest.NewRecorder()

				v.handle(h.runRoutes, rec, req)

				if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Refresh") {
					t.Errorf("%s = %d %s, want %d sending the reader to a refresh",
						vName, rec.Code, rec.Body.String(), http.StatusBadGateway)
				}
			})
		}
	}
}

// A resume whose answer could not be read may have re-driven the run, so the run gets its deadline as a resume
// that took does; a resume KAS refused leaves the run unbounded.
func TestResume_AnAnswerThatCouldNotBeReadArmsTheDeadline(t *testing.T) {
	cases := map[string]struct {
		arm     func(br *fakeBridge)
		bounded bool
	}{
		"an undecoded answer": {func(br *fakeBridge) { br.setCallErr(methodKiroWorkflowResume, undecodedReply()) }, true},
		"an oversize answer": {func(br *fakeBridge) {
			br.setCallErr(methodKiroWorkflowResume, &marotte.TransportError{Err: marotte.ErrFrameTooLarge})
		}, true},
		"a parse error KAS itself sends": {func(br *fakeBridge) {
			br.setCallRPCErr(methodKiroWorkflowResume, &marotte.RPCError{Code: marotte.RPCCodeParseError, Message: "Parse error"})
		}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, br := heldRun(t, "wf_1", "publish")
			br.mu.Lock()
			br.callResults[methodKiroWorkflowList] = json.RawMessage(`{"runs":[]}`)
			br.callResults[methodKiroWorkflowInspect] = inspectReply(t, "wf_1", marotte.RunStatusPaused, "")
			br.mu.Unlock()
			c.arm(br)
			req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/resume", nil)
			req.SetPathValue("id", "wf_1")

			h.runRoutes.handleResume(httptest.NewRecorder(), req)

			if got := h.runs.bounded("wf_1"); got != c.bounded {
				t.Errorf("bounded after the resume = %v, want %v", got, c.bounded)
			}
		})
	}
}
