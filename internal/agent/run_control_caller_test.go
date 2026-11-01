package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestRunControls_AttributeACallerOutsideThePage drives each attributed REST control through its handler, once as
// marotte's page sends it (fetch metadata, or only Origin over plain HTTP) and once as an agent's curl does (neither),
// and reads what KAS was told.
func TestRunControls_AttributeACallerOutsideThePage(t *testing.T) {
	hosted := func(t *testing.T, status marotte.RunStatus, result string) (*Runtime, *fakeBridge) {
		t.Helper()
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", status, ""),
			result:                    json.RawMessage(`{}`),
		}
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		return h, br
	}
	verbs := []struct {
		setup  func(t *testing.T) (*Runtime, *fakeBridge)
		handle func(rr *runRoutes, w http.ResponseWriter, r *http.Request)
		name   string
		path   string
		body   string
		method string
		// pageAttributed says the page's own request names the user: a pause or stop does, a resume does not.
		pageAttributed bool
	}{
		{
			name: "pause", path: "/api/runs/wf_1/pause", method: methodKiroWorkflowPause, pageAttributed: true,
			setup: func(t *testing.T) (*Runtime, *fakeBridge) {
				return hosted(t, marotte.RunStatusRunning, methodKiroWorkflowPause)
			},
			handle: (*runRoutes).handlePause,
		},
		{
			name: "resume", path: "/api/runs/wf_1/resume", method: methodKiroWorkflowResume,
			setup: func(t *testing.T) (*Runtime, *fakeBridge) {
				return hosted(t, marotte.RunStatusPaused, methodKiroWorkflowResume)
			},
			handle: (*runRoutes).handleResume,
		},
		{
			name: "cancel", path: "/api/runs/wf_1/cancel", method: methodKiroWorkflowCancel, pageAttributed: true,
			setup: func(t *testing.T) (*Runtime, *fakeBridge) {
				h, br := hosted(t, marotte.RunStatusRunning, methodKiroWorkflowCancel)
				leased(t, h.runs, "wf_1")
				return h, br
			},
			handle: (*runRoutes).handleCancel,
		},
		{
			name: "extend", path: "/api/runs/wf_1/extend", body: `{"node_id":"loop","iterations":3}`,
			method: methodKiroWorkflowResume,
			setup:  maxIterationsHub,
			handle: (*runRoutes).handleExtend,
		},
	}
	for _, v := range verbs {
		for _, caller := range []struct {
			name, site, origin string
			api                bool
		}{
			{name: "from the page", site: "same-origin"},
			// A plain-HTTP LAN origin gets no fetch metadata, only Origin.
			{name: "from the page over plain HTTP", origin: "http://192.0.2.10:9847"},
			{name: "from outside the page", api: true},
		} {
			t.Run(v.name+" "+caller.name, func(t *testing.T) {
				h, br := v.setup(t)
				rr := &runRoutes{runs: h.runs, epoch: h.Epoch}
				var body io.Reader = http.NoBody
				if v.body != "" {
					body = strings.NewReader(v.body)
				}
				req := httptest.NewRequest(http.MethodPost, v.path, body)
				req.SetPathValue("id", "wf_1")
				if caller.site != "" {
					req.Header.Set("Sec-Fetch-Site", caller.site)
				}
				if caller.origin != "" {
					req.Header.Set("Origin", caller.origin)
				}
				rec := httptest.NewRecorder()

				v.handle(rr, rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("%s = %d, want 200: %s", v.name, rec.Code, rec.Body.String())
				}
				params := br.paramsFor(v.method)
				if caller.api {
					wantStopParams(t, v.name, params, true, apiCallerReason)
				} else {
					wantStopParams(t, v.name, params, v.pageAttributed, "")
				}
				if v.body != "" && params["extendRepeat"] == nil {
					t.Errorf("%s params = %v, want the extension kept beside the attribution", v.name, params)
				}
			})
		}
	}
}
