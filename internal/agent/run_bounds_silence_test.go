package agent

// A step whose turn stays open and sends nothing: flagged for a chat-launched run, stopped for a parentless one.

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const silentRunID = "wf_1"

const silencePushBody = "No activity for 1 hour · a step is still running"

// silentStep is a run with one open step whose last frame was ago, its idle window just expired.
func silentStep(t *testing.T, launch launchOrigin, ago time.Duration, opts ...Option) (*Runtime, *fakeBridge, time.Time) {
	t.Helper()
	h, _, br := newTestHub(opts...)
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.insertBridgeNow(runChatID(silentRunID), testBridge(br, bridgeReady))
	h.runs.log = newRunLog(t.TempDir())
	openStep(t, h, silentRunID, []string{silentRunID, "a"}, "step-session-a")
	deadline := stagedExpiry(t, h.runs, silentRunID, launch)
	stageSilence(h.runs, silentRunID, ago)
	return h, br, deadline
}

func stageSilence(rs *Runs, id string, ago time.Duration) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.bounds.lastFrame[id] = time.Now().Add(-ago)
}

// expireAgain stages the run's deadline as just passed and fires it.
func expireAgain(t *testing.T, rs *Runs, id string) {
	t.Helper()
	deadline := time.Now().Add(-time.Second)
	if err := rs.leaseStore().SetDeadline(t.Context(), id, deadline); err != nil {
		t.Fatalf("stage an expired deadline: %v", err)
	}
	rs.cancelExpired(id, deadline)
}

func wantRunning(t *testing.T, h *Runtime, br *fakeBridge) {
	t.Helper()
	if got := h.runs.endReason(silentRunID); got != "" {
		t.Errorf("endReason = %q, want the run still running", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("a cancel went out for a run that should keep running: %v", br.callLog())
	}
	if l, held := h.runs.lease(silentRunID); !held || !l.Bounded() {
		t.Errorf("lease held = %t, bounded = %t; want the run kept under its idle window", held, l.Bounded())
	}
}

func TestSilentStep_FiftyNineMinutesIsNotFlagged(t *testing.T) {
	for name, launch := range map[string]launchOrigin{"chat-launched": agentLaunch(), "parentless": manualLaunch()} {
		t.Run(name, func(t *testing.T) {
			fp := newRunOutcomePush()
			h, br, deadline := silentStep(t, launch, 59*time.Minute, WithPush(fp))

			h.runs.cancelExpired(silentRunID, deadline)
			joinInflight(t, h)

			wantRunning(t, h, br)
			if got := h.runs.silentSince(silentRunID); !got.IsZero() {
				t.Errorf("silentSince = %v, want no flag at 59 minutes", got)
			}
			if n := len(runOutcomePushes(fp)); n != 0 {
				t.Errorf("59 minutes of silence pushed %d notifications, want none", n)
			}
			// The refill stops at the hour, so the flag lands on time, not up to a window late.
			l, _ := h.runs.lease(silentRunID)
			if budget := time.Until(l.Deadline); budget > 90*time.Second {
				t.Errorf("the refill granted %v, want it to end at the hour of silence (about 1m)", budget.Round(time.Second))
			}
		})
	}
}

func TestSilentStep_AnHourFlagsAChatLaunchedRunAndKeepsItRunning(t *testing.T) {
	fp := newRunOutcomePush()
	h, br, deadline := silentStep(t, agentLaunch(), time.Hour, WithPush(fp))

	h.runs.cancelExpired(silentRunID, deadline)
	joinInflight(t, h)

	wantRunning(t, h, br)
	if l, _ := h.runs.lease(silentRunID); time.Until(l.Deadline) < runIdleWindow-time.Second {
		t.Errorf("the flagged run got %v of budget, want a full idle window", time.Until(l.Deadline).Round(time.Second))
	}
	if h.runs.silentSince(silentRunID).IsZero() {
		t.Fatal("an hour of silence on an open step left no flag")
	}
	pushes := runOutcomePushes(fp)
	want := runPushSent{
		title: "publish", body: silencePushBody,
		kind: marotte.PushKindRunOutcome, subject: marotte.RunSubject(silentRunID),
	}
	if len(pushes) != 1 || pushes[0] != want {
		t.Errorf("the flag pushed %+v, want exactly %+v", pushes, want)
	}
	if got := servedSilentSince(t, h, br); got == "" {
		t.Error("the run read carries no silent_since for a flagged run")
	}

	expireAgain(t, h.runs, silentRunID)
	joinInflight(t, h)
	wantRunning(t, h, br)
	if n := len(runOutcomePushes(fp)); n != 0 {
		t.Errorf("a second expiry in the same silence pushed %d more notifications, want none", n)
	}
}

func TestSilentStep_AFrameClearsTheFlagAndANewSilencePushesAgain(t *testing.T) {
	fp := newRunOutcomePush()
	h, br, deadline := silentStep(t, agentLaunch(), time.Hour, WithPush(fp))
	h.runs.cancelExpired(silentRunID, deadline)
	joinInflight(t, h)
	if n := len(runOutcomePushes(fp)); n != 1 {
		t.Fatalf("Setup: the flag pushed %d notifications, want one", n)
	}

	h.runs.RunMadeProgress(silentRunID)

	if got := h.runs.silentSince(silentRunID); !got.IsZero() {
		t.Errorf("silentSince = %v after a frame, want the flag cleared", got)
	}
	if got := servedSilentSince(t, h, br); got != "" {
		t.Errorf("the run read still carries silent_since = %q after a frame", got)
	}

	stageSilence(h.runs, silentRunID, time.Hour)
	expireAgain(t, h.runs, silentRunID)
	joinInflight(t, h)
	if n := len(runOutcomePushes(fp)); n != 1 {
		t.Errorf("a second silence pushed %d notifications, want one", n)
	}
}

// A lifecycle frame, a watch's idle poll among them, is activity too: a polling watch is never flagged.
func TestSilentStep_AWatchPollIsAFrame(t *testing.T) {
	h, _, deadline := silentStep(t, manualLaunch(), time.Hour)
	host := runChatID(silentRunID)
	h.dispatch(t.Context(), host, h.originOf(host), runNotif(methodWFWatchPoll, map[string]any{
		"workflowId": silentRunID, "nodeId": "a", "nodePath": []string{silentRunID, "a"}, "outcome": "idle",
	}))

	h.runs.cancelExpired(silentRunID, deadline)

	if got := h.runs.endReason(silentRunID); got != "" {
		t.Errorf("endReason = %q, want a polling watch kept running", got)
	}
}

// A run no chat launched has nobody to tell, so its hour of silence ends it as the idle window's stall does.
func TestSilentStep_AnHourStopsAParentlessRunAsStalled(t *testing.T) {
	fp := newRunOutcomePush()
	h, br, deadline := silentStep(t, manualLaunch(), time.Hour, WithPush(fp))

	h.runs.cancelExpired(silentRunID, deadline)
	joinInflight(t, h)

	if got := h.runs.endReason(silentRunID); got != runEndStalled {
		t.Errorf("endReason = %q, want %q", got, runEndStalled)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for a parentless run silent for an hour: %v", br.callLog())
	}
	if !h.runs.silentSince(silentRunID).IsZero() {
		t.Error("a stopped run was flagged as silent and still running")
	}
}

var silenceLaunches = map[string]launchOrigin{"chat-launched": agentLaunch(), "run-button": manualLaunch()}

// fileStepAsk files an open ask of the silent step, as the step's request would.
func fileStepAsk(h *Runtime, kind marotte.EventType) marotte.ServerEvent {
	var payload any
	switch kind {
	case marotte.EventUserInputNeeded:
		payload = marotte.UserInputNeededPayload{RunID: silentRunID, NodeID: "a"}
	case marotte.EventElicitationNeeded:
		payload = marotte.ElicitationNeededPayload{RunID: silentRunID, NodeID: "a"}
	default:
		payload = marotte.PermissionNeededPayload{RunID: silentRunID, NodeID: "a"}
	}
	return h.bus.pendingPerms.add(1, marotte.NewEvent(kind, runChatID(silentRunID), payload), nil)
}

// A step waiting on a person's answer is not hung, whoever launched the run.
func TestSilentStep_AnOpenAskIsNotSilence(t *testing.T) {
	kinds := []marotte.EventType{marotte.EventPermissionNeeded, marotte.EventUserInputNeeded, marotte.EventElicitationNeeded}
	for name, launch := range silenceLaunches {
		for _, kind := range kinds {
			t.Run(name+" "+string(kind), func(t *testing.T) {
				fp := newRunOutcomePush()
				h, br, deadline := silentStep(t, launch, 2*time.Hour, WithPush(fp))
				fileStepAsk(h, kind)

				h.runs.cancelExpired(silentRunID, deadline)
				joinInflight(t, h)

				wantRunning(t, h, br)
				if got := h.runs.silentSince(silentRunID); !got.IsZero() {
					t.Errorf("silentSince = %v, want no flag while the step waits on an answer", got)
				}
				if n := len(runOutcomePushes(fp)); n != 0 {
					t.Errorf("a step waiting on an answer pushed %d notifications, want none", n)
				}
			})
		}
	}
}

// The answer restarts the count: a full hour from it passes before the step is flagged.
func TestSilentStep_AnAnswerRestartsTheCount(t *testing.T) {
	fp := newRunOutcomePush()
	h, br, deadline := silentStep(t, agentLaunch(), 2*time.Hour, WithPush(fp))
	ask := fileStepAsk(h, marotte.EventPermissionNeeded)
	h.runs.cancelExpired(silentRunID, deadline)
	joinInflight(t, h)

	reply, ok := h.bus.TakePendingPerm(runChatID(silentRunID), requestIDOf(t, ask), marotte.DecisionKindPermission, marotte.SettledByUser)
	if !ok {
		t.Fatal("Setup: the ask could not be taken")
	}
	if _, err := reply.Respond(t.Context(), marotte.PermissionOutcomeSelected("allow")); err != nil {
		t.Fatalf("Setup: answering the ask: %v", err)
	}

	expireAgain(t, h.runs, silentRunID)
	joinInflight(t, h)
	wantRunning(t, h, br)
	if got := h.runs.silentSince(silentRunID); !got.IsZero() {
		t.Errorf("silentSince = %v right after the answer, want the count restarted", got)
	}
	if n := len(runOutcomePushes(fp)); n != 0 {
		t.Errorf("the expiry right after the answer pushed %d notifications, want none", n)
	}

	stageSilence(h.runs, silentRunID, time.Hour)
	expireAgain(t, h.runs, silentRunID)
	joinInflight(t, h)
	if h.runs.silentSince(silentRunID).IsZero() {
		t.Error("an hour of silence after the answer left no flag")
	}
}

// A paused step has no timer running, so no length of pause flags or stops it.
func TestSilentStep_APausedStepIsNotSilence(t *testing.T) {
	for name, launch := range silenceLaunches {
		t.Run(name, func(t *testing.T) {
			fp := newRunOutcomePush()
			h, br, deadline := silentStep(t, launch, 2*time.Hour, WithPush(fp))
			host := runChatID(silentRunID)
			h.dispatch(t.Context(), host, h.originOf(host), runNotif(methodWFNodePaused, map[string]any{
				"workflowId": silentRunID, "nodeId": "a", "nodePath": []string{silentRunID, "a"}, "reason": "waiting for an answer",
			}))
			h.dispatch(t.Context(), host, h.originOf(host), runNotif(methodWFPaused, map[string]any{
				"workflowId": silentRunID, "pauseReason": "waiting for an answer",
			}))
			stageSilence(h.runs, silentRunID, 2*time.Hour)

			h.runs.cancelExpired(silentRunID, deadline)
			joinInflight(t, h)

			if got := h.runs.endReason(silentRunID); got != "" {
				t.Errorf("endReason = %q, want a paused step kept", got)
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("a cancel went out for a paused step: %v", br.callLog())
			}
			if l, held := h.runs.lease(silentRunID); !held || l.Bounded() {
				t.Errorf("lease held = %t, bounded = %t; want the paused run held with no deadline", held, l.Bounded())
			}
			if got := h.runs.silentSince(silentRunID); !got.IsZero() {
				t.Errorf("silentSince = %v, want a paused step never flagged", got)
			}
			for _, p := range runOutcomePushes(fp) {
				if p.body == silencePushBody {
					t.Errorf("a paused step got the silence push %+v", p)
				}
			}
		})
	}
}

// A watch's bound is its own idleTimeoutSec: an hourly poll inside a repeat is neither flagged nor stopped.
func TestSilentStep_AnHourlyWatchIsNeitherFlaggedNorStopped(t *testing.T) {
	for name, launch := range silenceLaunches {
		t.Run(name, func(t *testing.T) {
			fp := newRunOutcomePush()
			h, _, br := newTestHub(WithPush(fp))
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
			h.insertBridgeNow(runChatID(silentRunID), testBridge(br, bridgeReady))
			h.runs.log = newRunLog(t.TempDir())
			host := runChatID(silentRunID)
			watch := []string{silentRunID, "watching", "iter-0", "mail"}
			frame := func(method string, params map[string]any) {
				params["workflowId"] = silentRunID
				h.dispatch(t.Context(), host, h.originOf(host), runNotif(method, params))
			}
			frame(methodWFNodeStart, map[string]any{"nodeId": "watching", "nodePath": []string{silentRunID, "watching"}, "type": "repeat"})
			frame(methodWFNodeStart, map[string]any{"nodeId": "mail", "nodePath": watch, "type": "watch"})
			stagedExpiry(t, h.runs, silentRunID, launch)

			for range 3 {
				stageSilence(h.runs, silentRunID, time.Hour)
				expireAgain(t, h.runs, silentRunID)
				joinInflight(t, h)
				frame(methodWFWatchPoll, map[string]any{"nodeId": "mail", "nodePath": watch, "outcome": "idle"})
			}

			wantRunning(t, h, br)
			if got := h.runs.silentSince(silentRunID); !got.IsZero() {
				t.Errorf("silentSince = %v, want a watch never flagged", got)
			}
			if n := len(runOutcomePushes(fp)); n != 0 {
				t.Errorf("an hourly watch pushed %d notifications, want none", n)
			}
		})
	}
}

func servedSilentSince(t *testing.T, h *Runtime, br *fakeBridge) string {
	t.Helper()
	br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, silentRunID, marotte.RunStatusRunning, ""))
	_, body := getRun(t, h, silentRunID)
	var reply struct {
		SilentSince string `json:"silent_since"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("decoding the run reply: %s", err)
	}
	return reply.SilentSince
}
