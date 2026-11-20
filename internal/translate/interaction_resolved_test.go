package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// interactionResolvedFrame is the session_info_update KAS sends when it closes an ask, its askId on
// the nested block's own _meta.kiro; an empty askID sends none.
func interactionResolvedFrame(t *testing.T, toolCallID, askID, outcome string) json.RawMessage {
	t.Helper()
	resolved := map[string]any{"toolCallId": toolCallID, "outcome": outcome}
	if askID != "" {
		resolved["_meta"] = map[string]any{"kiro": map[string]any{"askId": askID, "askKind": "tool_call_approval"}}
	}
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind":                "interaction_resolved",
		"toolCallId":          toolCallID,
		"outcome":             outcome,
		"interactionResolved": resolved,
	}}})
}

// An ask KAS closed unanswered is retired on every attribution, a step's included, because the ask is
// filed under the chat whoever raised it.
func TestHandleSessionInfoUpdate_InteractionResolvedCancelledWithdrawsTheAsk(t *testing.T) {
	tests := []struct {
		name    string
		attr    FrameAttribution
		askID   string
		outcome string
		want    []string
	}{
		{name: "the chat's own ask, cancelled", askID: "pr-1", outcome: "cancelled", want: []string{"c1/pr-1"}},
		{name: "a step's ask, cancelled", attr: FrameAttribution{Step: true, RunID: "wf"}, askID: "pr-1", outcome: "cancelled", want: []string{"c1/pr-1"}},
		{name: "the chat's own ask, rule-decided", askID: "pr-1", outcome: "rule_decided", want: []string{"c1/pr-1"}},
		{name: "a step's ask, rule-decided", attr: FrameAttribution{Step: true, RunID: "wf"}, askID: "pr-1", outcome: "rule_decided", want: []string{"c1/pr-1"}},
		{name: "a close naming no ask", outcome: "cancelled", want: nil},
		{name: "an answer marotte sent", askID: "pr-1", outcome: "selected", want: nil},
		{name: "a dismissed question", askID: "pr-1", outcome: "dismissed", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				interactionResolvedFrame(t, "tc-1", tt.askID, tt.outcome), tt.attr)
			if !slices.Equal(deps.withdrawn, tt.want) {
				t.Errorf("HandleSessionInfoUpdate(interaction_resolved %s) withdrew %v, want %v",
					tt.outcome, deps.withdrawn, tt.want)
			}
		})
	}
}

// A parentless run's ask is filed under its run:<id> key, and its bridge's frames
// take the step path.
func TestHandleStepInfoUpdate_InteractionResolvedWithdrawsTheRunsAsk(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleStepInfoUpdate(t.Context(), "run:wf_1",
		interactionResolvedFrame(t, "tc-2", "pr-2", "cancelled"), FrameAttribution{Step: true, RunID: "wf_1"})
	if want := []string{"run:wf_1/pr-2"}; !slices.Equal(deps.withdrawn, want) {
		t.Errorf("HandleStepInfoUpdate withdrew %v, want %v", deps.withdrawn, want)
	}
}

// The approval card's context: the paths, the round, and a watch command's run,
// which also attributes the ask so the run's teardown retires it.
func TestHandlePermissionRequest_DecodesLocationsRoundAndWatch(t *testing.T) {
	deps, events := newEventCaptureDeps()
	roles := rolesOf(deps)
	roles.WorkDir = "/workspace"
	tr := New(roles)

	id := int64(7)
	tr.HandlePermissionRequest(t.Context(), "c1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
		"sessionId": "sess_parent",
		"toolCall": map[string]any{
			"toolCallId": "tc-9",
			"title":      "gh pr checks",
			"kind":       "execute",
			"locations":  []map[string]any{{"path": "/workspace/a/b.go"}, {"path": ""}},
		},
		"options": []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		"_meta": map[string]any{"kiro": map[string]any{
			"consentRound":  2,
			"workflowWatch": map[string]any{"workflowId": "wf_1", "nodeId": "watch"},
		}},
	})})

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if want := []string{"a/b.go"}; !slices.Equal(got.Locations, want) {
		t.Errorf("Locations = %v, want %v", got.Locations, want)
	}
	if got.ConsentRound != 2 {
		t.Errorf("ConsentRound = %d, want 2", got.ConsentRound)
	}
	if got.Watch == nil || *got.Watch != (marotte.PermissionWatch{WorkflowID: "wf_1", NodeID: "watch"}) {
		t.Errorf("Watch = %+v, want {wf_1 watch}", got.Watch)
	}
	if got.RunID != "wf_1" || got.NodeID != "watch" {
		t.Errorf("RunID/NodeID = %q/%q, want wf_1/watch (from the watch marker)", got.RunID, got.NodeID)
	}
}

// An ask with none of the three carries none of them.
func TestHandlePermissionRequest_NoWatchNoRoundNoLocations(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(8)
	tr.HandlePermissionRequest(t.Context(), "c1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
		"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tc", "title": "t"},
		"options": []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
	})})
	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.Watch != nil || got.ConsentRound != 0 || got.Locations != nil || got.RunID != "" {
		t.Errorf("payload = %+v, want no watch, round, locations or run", got)
	}
}

// A step's ask names its run and node through its requester, so it is attributed with the step
// registry cold (no node_start seen, as after a restart).
func TestHandlePermissionRequest_AttributesAStepAskByItsRequester(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(9)
	tr.HandlePermissionRequest(t.Context(), "c1", nopOrigin{}, &marotte.RPCResponse{ID: &id, Params: mustJSON(t, map[string]any{
		"sessionId": "sess_step",
		"toolCall":  map[string]any{"toolCallId": "tc", "title": "t"},
		"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		"_meta": map[string]any{"kiro": map[string]any{
			"askId": "pr-1", "askKind": "tool_call_approval",
			"requester": map[string]any{
				"sessionId": "sess_step", "launchingSessionId": "sess_parent", "rootSessionId": "sess_parent",
				"workflow": map[string]any{"workflowId": "wf_1", "nodeId": "n1", "nodePath": []string{"seq", "n1"}},
			},
		}},
	})})
	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.RunID != "wf_1" || got.NodeID != "n1" {
		t.Errorf("RunID/NodeID = %q/%q, want wf_1/n1 from the requester", got.RunID, got.NodeID)
	}
	if got.Watch != nil {
		t.Errorf("Watch = %+v, want nil for an ask no watch raised", got.Watch)
	}
}
