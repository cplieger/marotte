package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/trigger"
)

// classes reports which of the trigger error table's two marks err carries.
func classes(err error) (retryable, blocked bool) {
	return errors.Is(err, trigger.ErrLaunchRetryable), errors.Is(err, trigger.ErrLaunchBlocked)
}

func TestLaunchClassTable(t *testing.T) {
	t.Run("an unknown recipe is blocked", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		h.runs.confirmer = &fakeConfirmer{}
		launchableRecipe(br, "wf_1")
		id, err := h.runs.LaunchTriggered(t.Context(), "bundled://gone", "rule-1", testTriggerKey,
			testTriggerCoalesce, nil, marotte.RunDefaults{})
		if r, b := classes(err); id != "" || r || !b || !errors.Is(err, errUnknownRecipe) {
			t.Errorf("LaunchTriggered = %q, %v; want no id, errUnknownRecipe marked blocked", id, err)
		}
	})
	t.Run("a refused pick is blocked", func(t *testing.T) {
		h, _, _ := runDefaultsHub(t)
		h.runs.confirmer = &fakeConfirmer{}
		_, err := launchTriggered(t, h, nil, marotte.RunDefaults{Model: "model-e-9"})
		if r, b := classes(err); r || !b || !errors.Is(err, errDefaultsRefused) {
			t.Errorf("LaunchTriggered error = %v, want errDefaultsRefused marked blocked", err)
		}
	})
	t.Run("an uncheckable pick is retryable", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		h.runs.confirmer = &fakeConfirmer{}
		launchableRecipe(br, "wf_1")
		_, err := launchTriggered(t, h, nil, marotte.RunDefaults{Model: "model-a"})
		if r, b := classes(err); !r || b || !errors.Is(err, errDefaultsUncheckable) {
			t.Errorf("LaunchTriggered error = %v, want errDefaultsUncheckable marked retryable", err)
		}
	})
	t.Run("a drain is retryable", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		h.runs.confirmer = &fakeConfirmer{}
		launchableRecipe(br, "wf_1")
		beginDraining(h)
		_, err := launchTriggered(t, h, nil, marotte.RunDefaults{})
		if r, b := classes(err); !r || b || !errors.Is(err, errRuntimeDraining) {
			t.Errorf("LaunchTriggered error = %v, want errRuntimeDraining marked retryable", err)
		}
	})
	t.Run("an unreadable run list is retryable", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		h.runs.confirmer = &fakeConfirmer{}
		launchableRecipe(br, "wf_1")
		br.mu.Lock()
		br.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowList: {Code: -32603, Message: "Internal error"}}
		br.mu.Unlock()
		id, err := launchTriggered(t, h, nil, marotte.RunDefaults{})
		if r, b := classes(err); id != "" || !r || b || !errors.Is(err, errRunListUnavailable) {
			t.Errorf("LaunchTriggered = %q, %v; want no id, errRunListUnavailable marked retryable", id, err)
		}
		if n := wireCount(br, methodKiroWorkflowNew); n != 0 {
			t.Errorf("workflow/new sent %d times, want none with the run list unread", n)
		}
	})
	t.Run("a busy recipe keeps its BusyError and is retryable", func(t *testing.T) {
		h, _, br := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		h.runs.confirmer = &fakeConfirmer{}
		launchableRecipe(br, "wf_1")
		br.mu.Lock()
		br.callResults[methodKiroWorkflowList] = []byte(
			`{"runs":[{"workflowId":"wf_9","workflowName":"publish","status":"running"}]}`,
		)
		br.mu.Unlock()
		_, err := launchTriggered(t, h, nil, marotte.RunDefaults{})
		busy, ok := errors.AsType[*schedule.BusyError](err)
		if !ok || busy.WorkflowID != "wf_9" {
			t.Errorf("LaunchTriggered error = %v, want a BusyError naming wf_9", err)
		}
	})
	for _, c := range []struct {
		err       error
		name      string
		retryable bool
		blocked   bool
	}{
		{name: "the sign-in gate", err: errRunsNeedSignIn, retryable: true},
		{name: "the identity provider down", err: errRunsProviderDown, retryable: true},
		{name: "an engine that did not answer", err: errEngineSlow, retryable: true},
		{name: "a recipe list still loading", err: errRecipeListSlow, retryable: true},
		{name: "a host session that did not start", err: errRunHostStart, retryable: true},
		{name: "a host session already taken", err: errHostSessionTaken, retryable: true},
		{name: "an unlisted error", err: errors.New("something new")},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := classLaunch(c.err)
			if r, b := classes(got); r != c.retryable || b != c.blocked || !errors.Is(got, c.err) {
				t.Errorf("classLaunch(%v) = %v: retryable %v, blocked %v; want %v, %v and the error kept",
					c.err, got, r, b, c.retryable, c.blocked)
			}
		})
	}
	t.Run("the sign-in gate also reads as no run can start", func(t *testing.T) {
		if err := classLaunch(errRunsNeedSignIn); !errors.Is(err, schedule.ErrNoRunCanStart) {
			t.Errorf("classLaunch = %v, want schedule.ErrNoRunCanStart", err)
		}
	})
	t.Run("a recipe list deadline is slow, not a run list fault", func(t *testing.T) {
		err := classifyRecipeListErr(context.DeadlineExceeded)
		if !errors.Is(err, errRecipeListSlow) || errors.Is(err, errRunListUnavailable) {
			t.Errorf("classifyRecipeListErr = %v, want errRecipeListSlow and not errRunListUnavailable", err)
		}
	})
}

func TestLaunchClassKeepsTheErrorsText(t *testing.T) {
	err := classLaunch(errUnknownRecipe)
	if err.Error() != errUnknownRecipe.Error() {
		t.Errorf("classLaunch text = %q, want %q: the rule's row shows the refusal's own sentence", err, errUnknownRecipe)
	}
}

// A required input the rule leaves empty is no class of its own: marotte cannot see which inputs a recipe requires,
// so the launch is decided by KAS's answer, replayed here from the probe of the pinned kiro-cli.
func TestMissingInputLaunchesAsProbed(t *testing.T) {
	var fx struct {
		Error   *marotte.RPCError `json:"error"`
		Created bool              `json:"created"`
	}
	readKASProbe(t, "missing-input.json", &fx)
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.confirmer = &fakeConfirmer{}
	launchableRecipe(br, "wf_1")
	if fx.Error != nil {
		br.mu.Lock()
		br.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowNew: fx.Error}
		br.mu.Unlock()
	}
	id, err := launchTriggered(t, h, map[string]string{"wait": "off"}, marotte.RunDefaults{})
	switch {
	case fx.Created:
		if id != "wf_1" || err != nil {
			t.Errorf("LaunchTriggered = %q, %v; want wf_1 launched, as KAS accepted the run", id, err)
		}
	case fx.Error != nil:
		rpc, ok := errors.AsType[*marotte.RPCError](err)
		if id != "" || !ok || rpc.Code != fx.Error.Code {
			t.Errorf("LaunchTriggered = %q, %v; want no id and KAS's %d refusal", id, err, fx.Error.Code)
		}
	default:
		t.Fatal("missing-input.json records neither a created run nor an error")
	}
}
