package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A heal losing KAS's claim to another verb is not a failed heal: on one engine KAS queues a second claim and refuses
// it only once the first overstayed its claim's TTL.
func TestResumeIfInterrupted_LosingTheClaimToAnotherResumeIsNotAFailure(t *testing.T) {
	logs := captureLogs(t)
	h, _, engine := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	addressableStep(t, h, engine, "wf_1", "review")
	engine.setCallResult(methodKiroWorkflowInspect,
		inspectReply(t, "wf_1", marotte.RunStatusPaused, stalePauseReason))
	host := newFakeBridge()
	host.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowResume: {
		Code: -32603, Message: "Internal error",
		Data: json.RawMessage(`{"details":"Workflow 'wf_1' was just claimed by another process; ` +
			`refusing to load it here. Retry if that process does not end up driving it."}`),
	}}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: host, state: bridgeIdle})

	h.runs.resumeIfInterrupted(t.Context(), runChatID("wf_1"), "wf_1")

	if !slices.Contains(host.callLog(), methodKiroWorkflowResume) {
		t.Fatalf("the heal sent no resume, so its outcome is untested: %v", host.callLog())
	}
	if out := logs.String(); strings.Contains(out, `"msg":"rehydrate: resume failed"`) ||
		!strings.Contains(out, `"msg":"rehydrate: another verb held this run's claim past its TTL`) {
		t.Errorf("logs = %s, want the lost claim reported as another verb's, not a failure", out)
	}
}
