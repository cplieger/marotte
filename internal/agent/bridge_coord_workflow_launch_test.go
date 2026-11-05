package agent

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// workflowLaunchFrame is a config_option_update carrying only workflowLaunch at value, as KAS sends on a switch into a
// mode that offers it; a sessionID plus workflow marker makes it a step's.
func workflowLaunchFrame(t *testing.T, value, sessionID string, workflow bool) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []any{
			map[string]any{"id": marotte.ConfigOptionWorkflowLaunch, "type": "select", "currentValue": value},
		},
	}
	if workflow {
		update["_meta"] = map[string]any{"kiro": map[string]any{
			"workflow": map[string]any{"workflowId": "wf1", "nodeId": "n1", "type": "step"},
		}}
	}
	params := mustJSON(t, map[string]any{"sessionId": sessionID, "update": mustJSON(t, update)})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

func (b *fakeBridge) workflowLaunchHealsSeen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.workflowLaunchHeals)
}

func TestConfigOptionUpdate_HealsWorkflowLaunchOnTheChatsOwnFrame(t *testing.T) {
	h, br := healEffortFixture(t, "")

	h.handleSessionUpdate(t.Context(), "c1", workflowLaunchFrame(t, marotte.WorkflowLaunchWhenAsked, "", false))
	settleHeals(t, h)

	if got := br.workflowLaunchHealsSeen(); !slices.Equal(got, []string{marotte.WorkflowLaunchWhenAsked}) {
		t.Errorf("HealWorkflowLaunch calls = %q, want [%q]", got, marotte.WorkflowLaunchWhenAsked)
	}
}

func TestConfigOptionUpdate_LeavesWorkflowLaunchAloneOnAStepsFrame(t *testing.T) {
	h, br := healEffortFixture(t, "")

	h.handleSessionUpdate(t.Context(), "c1", workflowLaunchFrame(t, marotte.WorkflowLaunchWhenAsked, "sess-step", true))
	settleHeals(t, h)

	if got := br.workflowLaunchHealsSeen(); len(got) != 0 {
		t.Errorf("HealWorkflowLaunch calls from a step's frame = %q, want none", got)
	}
}

func TestConfigOptionUpdate_HealsNoWorkflowLaunchTheFrameDoesNotOffer(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.workflowLaunchHealsSeen(); len(got) != 0 {
		t.Errorf("HealWorkflowLaunch calls from a frame without the option = %q, want none", got)
	}
}
