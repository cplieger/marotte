package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func interactionResolvedFrame(t *testing.T, toolCallID, outcome string) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind":                "interaction_resolved",
		"toolCallId":          toolCallID,
		"outcome":             outcome,
		"interactionResolved": map[string]any{"toolCallId": toolCallID, "outcome": outcome},
	}}})
}

// An ask KAS withdrew is retired on every attribution, a subagent's and a step's
// included, because the ask is filed under the chat whoever raised it.
func TestHandleSessionInfoUpdate_InteractionResolvedCancelledWithdrawsTheAsk(t *testing.T) {
	tests := []struct {
		name    string
		attr    FrameAttribution
		outcome string
		want    []string
	}{
		{name: "the chat's own ask, cancelled", outcome: "cancelled", want: []string{"c1/tc-1"}},
		{name: "a subagent's ask, cancelled", attr: FrameAttribution{Subagent: true, SessionID: "sub"}, outcome: "cancelled", want: []string{"c1/tc-1"}},
		{name: "a step's ask, cancelled", attr: FrameAttribution{Step: true, RunID: "wf"}, outcome: "cancelled", want: []string{"c1/tc-1"}},
		{name: "an answer marotte sent", outcome: "selected", want: nil},
		{name: "a dismissed question", outcome: "dismissed", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				interactionResolvedFrame(t, "tc-1", tt.outcome), tt.attr)
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
		interactionResolvedFrame(t, "tc-2", "cancelled"), FrameAttribution{Step: true, RunID: "wf_1"})
	if want := []string{"run:wf_1/tc-2"}; !slices.Equal(deps.withdrawn, want) {
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
