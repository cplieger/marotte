package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// An empty sessionID is the chat's own frame; a sessionID plus workflow marker is a step's.
func configOptionFrame(t *testing.T, running, sessionID string, workflow bool) *marotte.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(marotte.ACPUpdateConfigOption),
		"configOptions": []any{
			map[string]any{
				"id":           "effortLevel",
				"currentValue": running,
				"options": []any{
					map[string]any{"value": "high", "name": "high"},
					map[string]any{"value": "max", "name": "max"},
				},
			},
		},
	}
	if workflow {
		update["_meta"] = map[string]any{"kiro": map[string]any{
			"workflow": map[string]any{"workflowId": "wf1", "nodeId": "n1", "type": "step"},
		}}
	}
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    mustJSON(t, update),
	})
	return &marotte.RPCResponse{Method: marotte.MethodSessionUpdate, Params: params}
}

func healEffortFixture(t *testing.T, chose string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.Effort = chose
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	return h, br
}

// settleHeals joins the repair goroutine through the runtime's shutdown, so "nothing was
// applied" is a fact rather than a race the test won.
func settleHeals(t *testing.T, h *Runtime) {
	t.Helper()
	shutdownHub(t, h)
}

// TestHealEffort_RepairsALevelTheSessionMovedOnItsOwn covers the spawning turn, which repairEffort cannot reach.
func TestHealEffort_RepairsALevelTheSessionMovedOnItsOwn(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort applied = %q, want %q", got, "max")
	}
	if got := br.lastObservedEffort(); got != "high" {
		t.Errorf("observed level = %q, want %q — the bridge must be told what the session reports, or its differs-only cache skips the repair", got, "high")
	}
}

// TestHealEffort_RepairsOncePerBridge pins the latch; it is claimed synchronously, so spending it in the fixture proves the second frame did nothing.
func TestHealEffort_RepairsOncePerBridge(t *testing.T) {
	h, br := healEffortFixture(t, "max")
	sb := h.coord.bridgeFor("c1")
	if !sb.claimEffortHeal() {
		t.Fatal("fixture: the latch was already spent")
	}

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("applied %q on a spent latch; the repair must run once per bridge", got)
	}
	if got := br.lastObservedEffort(); got != "high" {
		t.Errorf("observed level = %q, want %q — the OBSERVATION is not latched, only the repair", got, "high")
	}
}

// TestHealEffort_SendsNothingWhenThereIsNothingToRepair covers the three distinct "nothing" cases.
func TestHealEffort_SendsNothingWhenThereIsNothingToRepair(t *testing.T) {
	tests := map[string]struct {
		chose   string
		running string
	}{
		"the session already runs at the chosen level": {chose: "max", running: "max"},
		"the chat chose nothing and has no seed":       {chose: "", running: "high"},
		"the session reports no level at all":          {chose: "max", running: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			h, br := healEffortFixture(t, test.chose)

			h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, test.running, "", false))
			settleHeals(t, h)

			if got := br.lastEffort(); got != "" {
				t.Errorf("applied %q, want no call", got)
			}
		})
	}
}

// TestHealEffort_IgnoresAWorkflowStepsFrame pins that a step's level never repairs the chat's.
func TestHealEffort_IgnoresAWorkflowStepsFrame(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "sess-step", true))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("applied %q from a step's frame, want no call", got)
	}
	if got := br.lastObservedEffort(); got != "" {
		t.Errorf("observed %q from a step's frame; a step's level is not this session's", got)
	}
}
