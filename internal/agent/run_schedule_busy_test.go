package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
)

// A scheduled launch the engine did not answer before the run was created is the engine being busy, which the runner
// retries until the next slot; once workflow/new is sent a run may exist, so a failure there is never retried.
func TestLaunchScheduled_TellsABusyEngineFromAFailure(t *testing.T) {
	deadline := fmt.Errorf("engine call: %w", context.DeadlineExceeded)
	notReady := fmt.Errorf("%w: the agent engine is not ready", marotte.ErrBridgeExited)
	cases := []struct {
		fail   map[string]error
		name   string
		manual bool
		busy   bool
	}{
		{name: "the recipe listing ran out of time", fail: map[string]error{methodKiroWorkflowListRecipes: deadline}, busy: true},
		{name: "the run list ran out of time", fail: map[string]error{methodKiroWorkflowList: deadline}, busy: true},
		{name: "the engine was not ready", fail: map[string]error{methodKiroWorkflowList: notReady}, busy: true},
		{name: "workflow new ran out of time", fail: map[string]error{methodKiroWorkflowNew: deadline}},
		{name: "the run list refused", fail: map[string]error{methodKiroWorkflowList: errors.New("kas gone")}},
		{name: "a manual launch", fail: map[string]error{methodKiroWorkflowListRecipes: deadline}, manual: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_new")
			br.callErrs = tc.fail
			var err error
			if tc.manual {
				_, _, err = h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
			} else {
				_, _, err = h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "s1"},
					nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
			}
			if err == nil {
				t.Fatal("the launch succeeded")
			}
			if got := errors.Is(err, schedule.ErrEngineBusy); got != tc.busy {
				t.Errorf("errors.Is(%v, ErrEngineBusy) = %v, want %v", err, got, tc.busy)
			}
		})
	}

	// The runner's own context ending is marotte stopping, not the engine being slow.
	t.Run("the caller's own deadline", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_new")
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		_, _, err := h.runs.LaunchScheduled(ctx, "bundled://publish", launchrule.Row{ID: "s1"}, nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
		if err == nil || errors.Is(err, schedule.ErrEngineBusy) {
			t.Errorf("LaunchScheduled = %v, want a failure that is not the engine's", err)
		}
	})
}

// The row's wait reaches the client as status.waiting_for, and only while there is one.
func TestLaunchRuleView_CarriesTheWaitOnTheEngine(t *testing.T) {
	due := time.Date(2026, time.August, 4, 2, 0, 0, 0, time.UTC)
	r := launchrule.Rule{
		Source: "bundled://nightly", Enabled: true,
		When: launchrule.When{Clock: &launchrule.ClockSpec{Freq: launchrule.FreqDaily, Hour: 2}},
	}
	waitOf := func(st launchrule.State) any {
		raw, err := json.Marshal(launchRuleViewOf(&r, &st, nil))
		if err != nil {
			t.Fatalf("encode the view: %v", err)
		}
		var v struct {
			Status map[string]any `json:"status"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return v.Status["waiting_for"]
	}
	if got := waitOf(launchrule.State{}); got != nil {
		t.Errorf("waiting_for = %v on a row with no wait", got)
	}
	if got := waitOf(launchrule.State{Status: launchrule.RuleStatus{WaitingFor: due}}); got != due.Format(time.RFC3339) {
		t.Errorf("waiting_for = %v, want %v", got, due.Format(time.RFC3339))
	}
}
