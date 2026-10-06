package agent

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// runStatuses reads the run-status vocabulary off the fixture internal/marotte and internal/kascap share.
func runStatuses(t *testing.T) []marotte.RunStatus {
	t.Helper()
	raw, err := os.ReadFile("../marotte/testdata/run_statuses.json")
	if err != nil {
		t.Fatalf("read run-status contract: %v", err)
	}
	var contract struct {
		Runs []struct {
			Status marotte.RunStatus `json:"status"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode run-status contract: %v", err)
	}
	out := make([]marotte.RunStatus, 0, len(contract.Runs))
	for _, row := range contract.Runs {
		out = append(out, row.Status)
	}
	return out
}

// TestRunVerbGates pins each verb's `from` list; the rationale is run_affordance.go's. Cancel stays unrestricted: it is the tab-close gesture.
func TestRunVerbGates(t *testing.T) {
	all := runStatuses(t)

	cases := map[string]struct {
		verb  runVerb
		legal []marotte.RunStatus
	}{
		"pause is live-only":     {runVerbPause, []marotte.RunStatus{marotte.RunStatusRunning}},
		"resume is paused-only":  {runVerbResume, []marotte.RunStatus{marotte.RunStatusPaused}},
		"cancel is unrestricted": {runVerbCancel, all},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, status := range all {
				want := slices.Contains(tc.legal, status)
				// An empty `from` is unrestricted: the handler skips the pre-check.
				got := len(tc.verb.from) == 0 || slices.Contains(tc.verb.from, status)
				if got != want {
					t.Errorf("%s from %q: got legal=%v, want %v", tc.verb.name, status, got, want)
				}
			}
		})
	}
}

// TestRunVerbsAreWired pins that a verb with no issuer 200s doing nothing, one with no name logs as "", and a
// restricted cancel errors on tab close.
func TestRunVerbsAreWired(t *testing.T) {
	for _, verb := range []runVerb{runVerbCancel, runVerbPause, runVerbResume, runVerbDelete} {
		if verb.name == "" {
			t.Error("a run verb has no name; its 409 and its log line would both be blank")
		}
		if verb.issue == nil {
			t.Errorf("run verb %q has no issuer: the route would answer ok without calling KAS", verb.name)
		}
	}
	// Cancel is the tab-close gesture (KAS is idempotent on a terminal run); delete is the only way out of History.
	for _, verb := range []runVerb{runVerbCancel, runVerbDelete} {
		if len(verb.from) != 0 {
			t.Errorf("run verb %q is gated; it must reach a run from any status", verb.name)
		}
	}
}

// TestHostBridge_ReachesAnAgentLaunchedRunThroughItsChat pins that KAS parents an agent run on the launching chat's
// session, so that bridge hosts it. A dead chat or the utility bridge cannot execute the run.
func TestHostBridge_ReachesAnAgentLaunchedRunThroughItsChat(t *testing.T) {
	setup := func(t *testing.T, parentSession string, sessions ...string) (*Runtime, *fakeBridge) {
		t.Helper()
		h, cs, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_1", "status": "paused", "parentSessionId": parentSession,
			}),
			methodKiroWorkflowPause: json.RawMessage(`{"paused":true}`),
		}
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "A"
			for _, s := range sessions {
				c.RecordSession(s)
			}
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		return h, br
	}

	t.Run("the launching chat's live bridge hosts the run", func(t *testing.T) {
		h, _ := setup(t, "sess_owned", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if _, sb := h.runs.hostBridgeChat(t.Context(), "wf_1"); sb == nil {
			t.Fatal("hostBridge = nil for a run parented on a chat with a live bridge; " +
				"every pause and resume on an agent-launched run would answer 409")
		}
	})

	// A run launched before a session change is parented on a retired id.
	t.Run("a run parented on a RETIRED session in the chain still resolves", func(t *testing.T) {
		h, _ := setup(t, "sess_old", "sess_old", "sess_current")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if _, sb := h.runs.hostBridgeChat(t.Context(), "wf_1"); sb == nil {
			t.Fatal("hostBridge = nil for a run parented on a session the chat has since retired")
		}
	})

	t.Run("a chat with no live bridge is no carrier", func(t *testing.T) {
		// Seeded but never opened: neither a nil call nor the toolless utility bridge is acceptable.
		h, _ := setup(t, "sess_owned", "sess_owned")
		if _, sb := h.runs.hostBridgeChat(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge returned a bridge for a chat that has none")
		}
	})

	t.Run("a PARENTLESS run resolves to nothing", func(t *testing.T) {
		h, _ := setup(t, "", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if _, sb := h.runs.hostBridgeChat(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge matched a parentless run to a chat; an empty parent session " +
				"must never match a chat's chain")
		}
	})

	t.Run("a session no open chat owns resolves to nothing", func(t *testing.T) {
		h, _ := setup(t, "sess_stranger", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if _, sb := h.runs.hostBridgeChat(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge matched a run parented on a session this chat does not own")
		}
	})

	// The RPC reaches KAS on the chat's own bridge, with no re-host.
	t.Run("Pause reaches KAS through the chat's bridge", func(t *testing.T) {
		h, br := setup(t, "sess_owned", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if err := h.runs.Pause(t.Context(), "wf_1"); err != nil {
			t.Fatalf("Pause(agent-launched run) = %v, want nil", err)
		}
		if !slices.Contains(br.callLog(), methodKiroWorkflowPause) {
			t.Error("Pause returned nil without issuing the RPC")
		}
	})

	t.Run("an unreadable run inventory refuses rather than guessing", func(t *testing.T) {
		h, cs, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowList: errRecipeBusy}
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if _, sb := h.runs.hostBridgeChat(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge returned a bridge while the run inventory was unreadable")
		}
	})
}

// pausedFrame builds `_kiro/workflow/paused` with only `{workflowId, pauseReason}`, KAS's shape for an unclassified pause.
func pausedFrame(t *testing.T, workflowID, reason string) *marotte.RPCResponse {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"workflowId": workflowID, "pauseReason": reason,
	})
	if err != nil {
		t.Fatalf("marshal paused frame: %v", err)
	}
	return &marotte.RPCResponse{Params: params}
}

// pausedFrameWithDetail builds KAS's classified pause frame. A raw map, so a renamed JSON tag cannot move both sides.
func pausedFrameWithDetail(t *testing.T, workflowID, reason, class, code string) *marotte.RPCResponse {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"workflowId":  workflowID,
		"pauseReason": reason,
		"pauseDetail": map[string]any{
			"class": class, "code": code, "occurredAt": "2026-09-04T12:03:43.000Z",
		},
	})
	if err != nil {
		t.Fatalf("marshal paused frame: %v", err)
	}
	return &marotte.RPCResponse{Params: params}
}

// pausedDriftedDetail builds a pause frame whose `pauseDetail` is not an object, the drift the heal must survive.
func pausedDriftedDetail(t *testing.T, workflowID, reason string) *marotte.RPCResponse {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"workflowId": workflowID, "pauseReason": reason,
		"pauseDetail": "transient-error",
	})
	if err != nil {
		t.Fatalf("marshal paused frame: %v", err)
	}
	return &marotte.RPCResponse{Params: params}
}

// inspectDriftedDetail is the same drift on the inspect reply, so both gates see it.
func inspectDriftedDetail(t *testing.T, workflowID, reason string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"status":      marotte.RunStatusPaused,
			"pauseReason": reason,
			"pauseDetail": "transient-error",
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// TestHealPaused_ResumesAnInvoluntaryPauseTheMomentKASReportsIt pins that a transient pause on a live bridge has no
// rehydrate trigger, but `_kiro/workflow/paused` arrives at once with the reason.
func TestHealPaused_ResumesAnInvoluntaryPauseTheMomentKASReportsIt(t *testing.T) {
	const transient = "Transient connection error (EAI_AGAIN); the run is paused and can be resumed."

	// Milliseconds instead of the 5s backoff. Restored by Cleanup: a defer skips a subtest's failure path.
	fastHeal := func(t *testing.T) {
		t.Helper()
		prev := healBaseDelay
		healBaseDelay = time.Millisecond
		t.Cleanup(func() { healBaseDelay = prev })
	}

	// The callback's re-read must agree with the frame.
	seedReply := func(t *testing.T, reply json.RawMessage) (*Runtime, *fakeBridge) {
		t.Helper()
		h, cs, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: reply,
			methodKiroWorkflowResume:  json.RawMessage(`{}`),
		}
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		return h, br
	}

	// The ordinary shape, for the cases whose whole subject is the reason.
	seed := func(t *testing.T, reason string) (*Runtime, *fakeBridge) {
		t.Helper()
		return seedReply(t, inspectPaused(t, "wf_1", reason))
	}

	// Deadline-bounded poll rather than a sleep: it fails closed with a diagnostic
	// and cannot flake into a false pass.
	waitForResume := func(t *testing.T, br *fakeBridge) bool {
		t.Helper()
		stop := time.Now().Add(3 * time.Second)
		for !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
			if time.Now().After(stop) {
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}

	t.Run("a transient network pause is resumed", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, transient)
		var forwarded bool
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {
			forwarded = true
		})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", transient))
		if !forwarded {
			t.Error("the frame was not forwarded; the client must render the pause before " +
				"anything undoes it")
		}
		if !waitForResume(t, br) {
			t.Fatalf("no resume was issued for an involuntary pause; calls were %v", br.callLog())
		}
	})

	// A parallel branch's reason goes to a state copy and the run gets a wrapper sentence, so only the
	// detail heals it. End to end: a predicate table cannot see a dropped wire field.
	t.Run("a transient fault inside a parallel branch is resumed", func(t *testing.T) {
		const wrapper = "Parallel 'phase1' is waiting on branch 'live-verify' " +
			"(branch paused on transient error EAI_AGAIN)."
		fastHeal(t)
		h, br := seedReply(t, inspectPausedWithDetail(t, "wf_1", wrapper, transientDetail()))
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "c1", pausedFrameWithDetail(t, "wf_1", wrapper, "transient-error", "EAI_AGAIN"))
		if !waitForResume(t, br) {
			t.Fatalf("no resume was issued for a transient fault parked inside a parallel "+
				"branch; this is the wedge the detail arm exists to reach. calls were %v",
				br.callLog())
		}
	})

	// A drifted detail must pass both the frame gate and the inspect guard; only the branch population is lost.
	t.Run("a detail whose wire shape drifted still heals off the reason", func(t *testing.T) {
		fastHeal(t)
		h, br := seedReply(t, inspectDriftedDetail(t, "wf_1", interruptedPauseReason))
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "c1", pausedDriftedDetail(t, "wf_1", interruptedPauseReason))
		if !waitForResume(t, br) {
			t.Fatalf("no resume was issued for an interrupted step whose pauseDetail arrived in "+
				"a shape marotte does not decode; discarding the whole frame there breaks the "+
				"heal for EVERY pause, not just the branch ones. calls were %v", br.callLog())
		}
	})

	// A plain step's interruption heals off its prose with no detail; dropping the reason arms would strand it.
	t.Run("a plain-step interruption with no detail is still resumed", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, interruptedPauseReason)
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", interruptedPauseReason))
		if !waitForResume(t, br) {
			t.Fatalf("no resume was issued for an interruption, which carries NO pauseDetail; "+
				"the reason arms cover that population. calls were %v", br.callLog())
		}
	})

	for name, reason := range map[string]string{
		"a step waiting on a human": "Step requested user input via send_message.",
		"a policy stop":             "Repeat 'implement' reached maxIterations.",
		"a recorded failure":        "Run failed: the reviewer never approved",
	} {
		t.Run(name+" is left alone", func(t *testing.T) {
			fastHeal(t)
			h, br := seed(t, reason)
			heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
			heal(t.Context(), "c1", pausedFrame(t, "wf_1", reason))
			// Give a scheduled heal every chance to fire.
			time.Sleep(50 * time.Millisecond)
			if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
				t.Errorf("resumed a run paused for %q; that overrides a decision somebody made", reason)
			}
			// Assert the budget: the callback's re-read refuses this fixture too.
			if attempt, _ := h.runs.claimHeal("wf_1"); attempt != 1 {
				t.Errorf("attempt number = %d, want 1; healPaused spent a heal on a run paused "+
					"for %q instead of declining at the reason gate", attempt, reason)
			}
		})
	}

	// A run that left `paused` within the backoff must not be re-driven.
	t.Run("a run that left the paused state inside the backoff is not resumed", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, transient)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "aborted", transient))
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", transient))
		time.Sleep(50 * time.Millisecond)
		if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
			t.Error("resumed a run KAS now reports aborted")
		}
	})

	// observePaused is silent on success, so a decline's line, and the reason it names, is the only trace.
	t.Run("a declined pause names the reason it refused", func(t *testing.T) {
		const parked = "Step requested user input via send_message."
		fastHeal(t)
		logs := captureLogs(t)
		h, _ := seed(t, parked)
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", parked))
		out := logs.String()
		if !strings.Contains(out, parked) {
			t.Errorf("the declined pause reason is absent from the log; a decline nothing "+
				"records is the silence that made this defect an investigation.\nlogs:\n%s", out)
		}
		if !strings.Contains(out, `"workflow_id":"wf_1"`) {
			t.Errorf("the declined run is not named, so the line cannot be attributed to a "+
				"run.\nlogs:\n%s", out)
		}
	})

	// A line on every pause would bury the declines.
	t.Run("a pause that IS resumed does not log a decline", func(t *testing.T) {
		fastHeal(t)
		logs := captureLogs(t)
		h, br := seed(t, transient)
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", transient))
		if !waitForResume(t, br) {
			t.Fatalf("no resume was issued, so this case is not exercising the accepted path; "+
				"calls were %v", br.callLog())
		}
		if strings.Contains(logs.String(), "left alone") {
			t.Errorf("a resumed pause logged a decline; the line must mark the refused path "+
				"only.\nlogs:\n%s", logs.String())
		}
	})

	t.Run("a frame with no chat id or no run id does nothing", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, transient)
		heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		heal(t.Context(), "", pausedFrame(t, "wf_1", transient))
		heal(t.Context(), "c1", pausedFrame(t, "", transient))
		time.Sleep(50 * time.Millisecond)
		if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
			t.Error("a frame missing its addressing still reached a resume")
		}
		// The outcome has a second guard, so assert nothing was claimed.
		if attempt, _ := h.runs.claimHeal("wf_1"); attempt != 1 {
			t.Errorf("attempt number = %d, want 1; a frame with no chat id spent a heal", attempt)
		}
	})
}

// TestHealBudget_BoundsThePauseHealLoopAndProgressRefillsIt pins that the budget stops a heal/pause loop and progress refills it.
func TestHealBudget_BoundsThePauseHealLoopAndProgressRefillsIt(t *testing.T) {
	t.Run("three attempts, then the run is left paused", func(t *testing.T) {
		h, _, _ := newTestHub()
		for i := 1; i <= maxAutoHeals; i++ {
			attempt, ok := h.runs.claimHeal("wf_1")
			if !ok {
				t.Fatalf("claimHeal attempt %d = refused, want granted", i)
			}
			if attempt != i {
				t.Errorf("claimHeal attempt number = %d, want %d; the backoff is computed from it", attempt, i)
			}
		}
		if _, ok := h.runs.claimHeal("wf_1"); ok {
			t.Errorf("claimHeal granted a %dth attempt; the pause-heal loop is unbounded", maxAutoHeals+1)
		}
	})

	t.Run("a completed node gives the whole budget back", func(t *testing.T) {
		h, _, _ := newTestHub()
		for range maxAutoHeals {
			if _, ok := h.runs.claimHeal("wf_1"); !ok {
				t.Fatal("setup: the budget was refused before it was spent")
			}
		}
		progress := h.runs.healProgress(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
		progress(t.Context(), "c1", pausedFrame(t, "wf_1", ""))

		attempt, ok := h.runs.claimHeal("wf_1")
		if !ok {
			t.Error("a run that made progress got no heal budget back, so one blip writes it " +
				"off for the rest of the process")
		}
		if attempt != 1 {
			t.Errorf("attempt number after progress = %d, want 1 (a full reset)", attempt)
		}
	})

	t.Run("the budget is per run", func(t *testing.T) {
		h, _, _ := newTestHub()
		for range maxAutoHeals {
			if _, ok := h.runs.claimHeal("wf_1"); !ok {
				t.Fatal("setup: the budget was refused before it was spent")
			}
		}
		if _, ok := h.runs.claimHeal("wf_2"); !ok {
			t.Error("one run exhausting its budget denied another run's first attempt")
		}
	})

	t.Run("a run ending clears its counter", func(t *testing.T) {
		h, _, _ := newTestHub()
		if _, ok := h.runs.claimHeal("wf_1"); !ok {
			t.Fatal("setup: the first attempt was refused")
		}
		h.runs.forgetBounds(t.Context(), "wf_1")
		if attempt, _ := h.runs.claimHeal("wf_1"); attempt != 1 {
			t.Errorf("attempt number after the run ended = %d, want 1; a workflow id KAS reuses "+
				"would inherit a spent budget", attempt)
		}
	})
}
