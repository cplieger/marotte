package agent

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// stepWorkflowInspect is a running run whose one step runs on sess_step, carrying the workflow id KAS's
// own state does: the step registry is keyed on it.
func stepWorkflowInspect(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"workflowId": "wf_1", "status": marotte.RunStatusRunning,
			"root": map[string]any{
				"nodeId": "root", "type": "sequence", "status": marotte.RunNodeStatusRunning,
				"children": []any{map[string]any{
					"nodeId": "review", "type": stepNodeType, "status": marotte.RunNodeStatusRunning,
					"sessionId": "sess_step",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

// stepSteeringMsg is a steering frame on the step's own session. KAS composes it with no workflow meta,
// so only the step registry can attribute it.
func stepSteeringMsg(kind, steerID, text string) *marotte.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": map[string]any{"kind": kind, "messageId": steerID, "content": text}},
	})
	params, _ := json.Marshal(map[string]any{"sessionId": "sess_step", "update": json.RawMessage(update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

// A message is the first request after a restart when the browser kept its target, so the inspect it
// sends from must attribute the step's frames by itself: with no GET before it, KAS's steering frames
// still fold as the step's, on either carrier.
func TestStepMessage_AColdRegistryStillAttributesTheStepsSteeringFrames(t *testing.T) {
	for _, carrier := range []string{"run_bridge", "launching_chat"} {
		t.Run(carrier, func(t *testing.T) {
			h, cs, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			h.runs.log = newRunLog(t.TempDir())
			br.setCallResult(methodKiroWorkflowInspect, stepWorkflowInspect(t))
			br.setCallResult(marotte.MethodSessionSteer, json.RawMessage(`{"queued":true}`))
			frameChat := runChatID("wf_1")
			if carrier == "run_bridge" {
				h.bridge.mgr.insert(frameChat, &sharedBridge{bridge: br, state: bridgeIdle})
			} else {
				frameChat = "c1"
				br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
					"workflowId": "wf_1", "name": "publish", "status": "running", "parentSessionId": "sess_owned",
				}))
				if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.RecordSession("sess_owned")
					return true
				}); err != nil {
					t.Fatalf("Setup: seeding the chat: %s", err)
				}
				if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
					t.Fatalf("Setup: opening the chat's bridge: %s", err)
				}
			}
			h.runs.RunNodeStart(t.Context(), reviewStep("sess_step"), "")
			mux := http.NewServeMux()
			(&runRoutes{runs: h.Runs()}).register(mux)

			rec := postStep(t, mux, "/api/runs/wf_1/steps/"+url.PathEscape(reviewPath)+"/message",
				marotte.RunStepMessageRequest{Text: "check the logs", MessageID: "m-1"})
			if rec.Code != http.StatusOK {
				t.Fatalf("Setup: POST message = %d %s, want 200", rec.Code, rec.Body.String())
			}
			for _, kind := range []string{"steering_queued", "steering_injected"} {
				h.translateACPEvent(frameChat, h.originOf(frameChat), stepSteeringMsg(kind, "steer-m-1", "check the logs"))
			}

			got := stepSteersIn(t, h)
			want := []marotte.EntrySteer{{Text: "check the logs", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("run log steers = %+v, want %+v: KAS's read is the step's", got, want)
			}
			if rows := h.bus.steers.list("c1"); len(rows) != 0 {
				t.Errorf("the launching chat holds %d steer rows, want none: the frames are the step's", len(rows))
			}
			if carrier == "run_bridge" {
				return
			}
			for _, e := range logOf(t, cs, "c1") {
				if e.Kind == marotte.EntryKindSteer {
					t.Errorf("the launching chat's log holds steer %s, want none", e.Payload)
				}
			}
		})
	}
}
