package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func wantStopParams(t *testing.T, verb string, params map[string]any, byUser bool, reason string) {
	t.Helper()
	if params == nil {
		t.Fatalf("no %s call went out", verb)
	}
	initiator, hasInitiator := params["initiator"]
	gotReason, hasReason := params["reason"]
	switch {
	case !byUser && (hasInitiator || hasReason):
		t.Errorf("%s params = %v, want no initiator and no reason for a stop nobody asked for", verb, params)
	case byUser && initiator != "user":
		t.Errorf("%s initiator = %v, want \"user\"", verb, initiator)
	case byUser && reason == "" && hasReason:
		t.Errorf("%s reason = %v, want none for the stop button itself", verb, gotReason)
	case byUser && reason != "" && gotReason != reason:
		t.Errorf("%s reason = %v, want %q", verb, gotReason, reason)
	}
}

func TestCancel_TellsKASTheUserAskedForIt(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	leased(t, h.runs, "wf_1")

	if err := h.runs.cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	wantStopParams(t, "cancel", br.paramsFor(methodKiroWorkflowCancel), true, "")
}

func TestCancelRun_NamesTheRewindAsTheReason(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	leased(t, h.runs, "wf_1")

	if err := h.runs.CancelRun(t.Context(), "wf_1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	wantStopParams(t, "cancel", br.paramsFor(methodKiroWorkflowCancel), true, stopWhyRewound)
}

func TestBoundCancel_AttributesNoOne(t *testing.T) {
	const id = "wf_1"
	h, br := askFixture(t, id, runChatID(id))
	deadline := stagedExpiry(t, h.runs, id, manualLaunch())

	h.runs.cancelExpired(id, deadline)

	wantStopParams(t, "cancel", br.paramsFor(methodKiroWorkflowCancel), false, "")
}

func TestRetryTermination_KeepsTheUsersAttribution(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	setCancelRetryDelay(h.runs, time.Millisecond)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	awaitCalls(t, br, methodKiroWorkflowCancel, 2, "the refused cancel was never re-attempted")

	wantStopParams(t, "re-attempted cancel", br.paramsFor(methodKiroWorkflowCancel), true, "")
}

func TestPause_TellsKASTheUserAskedForIt(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowPause: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.pause(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	wantStopParams(t, "pause", br.paramsFor(methodKiroWorkflowPause), true, "")
}

func TestResume_IsNotAttributed(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowResume: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.resume(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	wantStopParams(t, "resume", br.paramsFor(methodKiroWorkflowResume), false, "")
}

func TestDeleteChatStateByChain_AttributesByCause(t *testing.T) {
	for name, tc := range map[string]struct {
		reason string
		cause  command.RunStopCause
		byUser bool
	}{
		"a tab close is the user's":     {cause: command.RunStopTabClosed, byUser: true, reason: stopWhyTabClosed},
		"a retention purge is no one's": {cause: command.RunStopRetention},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, rig := newDeleteRig(t, kasRuns(t, map[string]any{
				"workflowId": "wf_live", "name": "r", "status": "running", "parentSessionId": "sess_cur",
			}))
			insertChatBridge(t, h, rig, "c1", "sess_cur")

			h.DeleteChatStateByChain(t.Context(), "c1", []string{"sess_cur"}, tc.cause)

			deletes := rig.of(methodKiroWorkflowDelete)
			if len(deletes) != 1 {
				t.Fatalf("workflow delete went out %d times, want 1", len(deletes))
			}
			wantStopParams(t, "delete", deletes[0].params, tc.byUser, tc.reason)
		})
	}
}

func TestDeleteChatStateByChain_NamesTheChatDeletion(t *testing.T) {
	h, cs, rig := newDeleteRig(t, kasRuns(t, map[string]any{
		"workflowId": "wf_live", "name": "r", "status": "running", "parentSessionId": "sess_cur",
	}))
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess_cur")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	insertChatBridge(t, h, rig, "c1", "sess_cur")

	h.DeleteChatStateByChain(t.Context(), "c1", []string{"sess_cur"}, command.RunStopChatDeleted)

	deletes := rig.of(methodKiroWorkflowDelete)
	if len(deletes) != 1 {
		t.Fatalf("workflow delete went out %d times, want 1", len(deletes))
	}
	wantStopParams(t, "delete", deletes[0].params, true, stopWhyChatDeleted)
}

func TestCloseChatState_NamesTheTabClose(t *testing.T) {
	h, cs, rig := newDeleteRig(t, kasRuns(t, map[string]any{
		"workflowId": "wf_live", "name": "r", "status": "running", "parentSessionId": "sess_cur",
	}))
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess_cur")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	insertChatBridge(t, h, rig, "c1", "sess_cur")

	h.CloseChatState(t.Context(), "c1")

	cancels := rig.of(methodKiroWorkflowCancel)
	if len(cancels) != 1 {
		t.Fatalf("workflow cancel went out %d times, want 1", len(cancels))
	}
	wantStopParams(t, "cancel", cancels[0].params, true, stopWhyTabClosed)
}

func inspectUserParked(t *testing.T, state map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"workflowId": "wf_1", "state": state})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

func TestPausePredicates_SpareAUserPark(t *testing.T) {
	for name, state := range map[string]map[string]any{
		"a landed user pause": {
			"status": "paused", "pauseReason": stalePauseReason, "stopInitiator": "user",
		},
		"a pending user pause": {
			"status": "paused", "pauseReason": stalePauseReason,
			"pausePending": map[string]any{"initiator": "user"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: inspectUserParked(t, state),
			}
			if h.runs.restartPaused(t.Context(), "wf_1") {
				t.Error("restartPaused accepted a run the user parked, so the orphan sweep would cancel it")
			}
			if h.runs.involuntarilyPaused(t.Context(), "wf_1") {
				t.Error("involuntarilyPaused accepted a run the user parked, so marotte would resume it unasked")
			}
		})
	}

	t.Run("a pause another initiator stamped is still the sweep's", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: inspectUserParked(t, map[string]any{
				"status": "paused", "pauseReason": stalePauseReason, "stopInitiator": "agent",
			}),
		}
		if !h.runs.restartPaused(t.Context(), "wf_1") {
			t.Error("restartPaused refused a restart-paused run nobody parked")
		}
	})
}

func TestAffordance_APendingPauseWithholdsPause(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "publish", "status": "running", "pausePending": true,
		}),
	}
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	aff := h.runs.affordance(t.Context(), "wf_1", "running")

	for _, v := range aff.Verbs {
		if v == verbPause {
			t.Fatalf("verbs = %v, want Pause withheld while a pause is pending", aff.Verbs)
		}
	}
	if got := aff.Refused[verbPause]; got != pausePendingRefusal {
		t.Errorf("Pause refusal = %q, want %q", got, pausePendingRefusal)
	}
}

func TestLaunch_OnlyAScheduledRunSendsALabel(t *testing.T) {
	t.Run("scheduled", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_sched")
		if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", "sched-1", time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("LaunchScheduled: %v", err)
		}
		if got := br.paramsFor(methodKiroWorkflowNew)["runLabel"]; got != "publish · scheduled" {
			t.Errorf("runLabel = %v, want %q", got, "publish · scheduled")
		}
	})
	t.Run("manual", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_manual")
		if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		if got, ok := br.paramsFor(methodKiroWorkflowNew)["runLabel"]; ok {
			t.Errorf("runLabel = %v on a manual launch, want none", got)
		}
	})
}

func TestRunLabelFor_FitsKASsRule(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain":                {"publish · scheduled", "publish · scheduled"},
		"double quote":         {`say "hi"`, "say 'hi'"},
		"newline and tab":      {"a\nb\tc", "a b c"},
		"line separator":       {"a\u2028b", "a b"},
		"surrounding space":    {"  a  ", "a"},
		"only control":         {"\n\t", ""},
		"cut at 100 units":     {strings.Repeat("a", 120), strings.Repeat("a", 100)},
		"surrogate not split":  {strings.Repeat("a", 99) + "😀", strings.Repeat("a", 99)},
		"surrogate fits":       {strings.Repeat("a", 98) + "😀", strings.Repeat("a", 98) + "😀"},
		"cut then trim spaces": {strings.Repeat("a", 99) + " b", strings.Repeat("a", 99)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runLabelFor(tc.in); got != tc.want {
				t.Errorf("runLabelFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
