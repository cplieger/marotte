package agent

// A step whose turn stays open and sends nothing: flagged for a chat-launched run, stopped for a parentless one.

import (
	"encoding/json"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
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
	runTree{t: t, h: h, id: silentRunID}.start(stepNodeType, "a")
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
func fileStepAsk(t *testing.T, h *Runtime, kind marotte.EventType) marotte.ServerEvent {
	t.Helper()
	return runTree{t: t, h: h, id: silentRunID}.ask(kind, "a")
}

// A step waiting on a person's answer is not hung, whoever launched the run.
func TestSilentStep_AnOpenAskIsNotSilence(t *testing.T) {
	kinds := []marotte.EventType{marotte.EventPermissionNeeded, marotte.EventUserInputNeeded, marotte.EventElicitationNeeded}
	for name, launch := range silenceLaunches {
		for _, kind := range kinds {
			t.Run(name+" "+string(kind), func(t *testing.T) {
				fp := newRunOutcomePush()
				h, br, deadline := silentStep(t, launch, 2*time.Hour, WithPush(fp))
				fileStepAsk(t, h, kind)

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

// The answer to a long-open ask restarts the count: past an idle window after it the step is not flagged, an hour
// after it it is.
func TestSilentStep_AnAnswerRestartsTheCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fp := newRunOutcomePush()
		h, br, _ := silentStep(t, agentLaunch(), 2*time.Hour, WithPush(fp))
		defer shutdownHub(t, h)
		ask := fileStepAsk(t, h, marotte.EventPermissionNeeded)
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		runTree{t: t, h: h, id: silentRunID}.answer(ask)

		time.Sleep(runIdleWindow + 5*time.Minute)
		synctest.Wait()
		joinInflight(t, h)
		wantRunning(t, h, br)
		if got := h.runs.silentSince(silentRunID); !got.IsZero() {
			t.Errorf("silentSince = %v 20 minutes after the answer, want the count restarted", got)
		}
		if n := len(runOutcomePushes(fp)); n != 0 {
			t.Errorf("20 minutes after the answer %d notifications went out, want none", n)
		}

		time.Sleep(runSilenceBound - runIdleWindow)
		synctest.Wait()
		joinInflight(t, h)
		if h.runs.silentSince(silentRunID).IsZero() {
			t.Error("an hour of silence after the answer left no flag")
		}
	})
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

// runTree drives a run's node tree through KAS's own frames on the run's host session.
type runTree struct {
	t  *testing.T
	h  *Runtime
	id string
}

func (r runTree) frame(method string, params map[string]any) {
	r.t.Helper()
	params["workflowId"] = r.id
	host := runChatID(r.id)
	r.h.dispatch(r.t.Context(), host, r.h.originOf(host), runNotif(method, params))
}

func (r runTree) node(path []string) map[string]any {
	return map[string]any{"nodeId": path[len(path)-1], "nodePath": append([]string{r.id}, path...)}
}

// start opens the node at path, below the run, as a KAS node of type typ.
func (r runTree) start(typ string, path ...string) {
	r.t.Helper()
	params := r.node(path)
	params["type"] = typ
	r.frame(methodWFNodeStart, params)
}

func (r runTree) complete(path ...string) {
	r.t.Helper()
	params := r.node(path)
	params["status"] = "completed"
	r.frame(methodWFNodeComplete, params)
}

// park is KAS parking the step at path for a person (parkAwaitingUser): node_paused alone, the run still running.
func (r runTree) park(path ...string) {
	r.t.Helper()
	params := r.node(path)
	params["reason"] = askPauseReason
	r.frame(methodWFNodePaused, params)
}

// ask files an open ask of the step node, as its session's request would.
func (r runTree) ask(kind marotte.EventType, node string) marotte.ServerEvent {
	r.t.Helper()
	var payload any
	switch kind {
	case marotte.EventUserInputNeeded:
		payload = marotte.UserInputNeededPayload{RunID: r.id, NodeID: node}
	case marotte.EventElicitationNeeded:
		payload = marotte.ElicitationNeededPayload{RunID: r.id, NodeID: node}
	default:
		payload = marotte.PermissionNeededPayload{RunID: r.id, NodeID: node}
	}
	evt, _ := r.h.bus.PendingPermsAdd(r.t.Context(), 1, marotte.NewEvent(kind, runChatID(r.id), payload), nil)
	return evt
}

// answer is a person allowing the permission ask evt.
func (r runTree) answer(evt marotte.ServerEvent) {
	r.t.Helper()
	reply, ok := r.h.bus.TakePendingPerm(runChatID(r.id), requestIDOf(r.t, evt), marotte.DecisionKindPermission, marotte.SettledByUser)
	if !ok {
		r.t.Fatal("Setup: the ask could not be taken")
	}
	if _, err := reply.Respond(r.t.Context(), marotte.PermissionOutcomeSelected("allow")); err != nil {
		r.t.Fatalf("Setup: answering the ask: %v", err)
	}
}

// expiryVerdict is what an idle window's expiry did to a run.
type expiryVerdict int

const (
	// verdictKept is refilled, neither flagged nor stopped.
	verdictKept expiryVerdict = iota
	// verdictSilent is an hour of silence on a counting step: flagged when a chat launched the run, stopped otherwise.
	verdictSilent
	// verdictStalled is no work open: stopped whoever launched the run.
	verdictStalled
)

// The run is silent only when a step that is running, open, asks nobody and is no watch sent nothing for an hour; any
// frame from any node, an answer included, restarts the count.
func TestSilence_OnePredicateOverTheRunTree(t *testing.T) {
	var answered marotte.ServerEvent
	for _, tc := range []struct {
		arrange func(r runTree)
		// then runs after the silence is staged.
		then    func(r runTree)
		name    string
		quiet   time.Duration
		verdict expiryVerdict
	}{
		{
			name: "a running step silent 59 minutes", quiet: 59 * time.Minute, verdict: verdictKept,
			arrange: func(r runTree) { r.start(stepNodeType, "a") },
		},
		{
			name: "a running step silent an hour", quiet: time.Hour, verdict: verdictSilent,
			arrange: func(r runTree) { r.start(stepNodeType, "a") },
		},
		{
			name: "an ask open on the step", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start(stepNodeType, "a")
				r.ask(marotte.EventPermissionNeeded, "a")
			},
		},
		{
			name: "an ask answered after two hours open", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start(stepNodeType, "a")
				answered = r.ask(marotte.EventPermissionNeeded, "a")
			},
			then: func(r runTree) { r.answer(answered) },
		},
		{
			name: "an ask on a step whose content frame opened its turn", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.h.runs.RunFoldTarget(r.t.Context(), &translate.RunStep{
					RunID: r.id, NodePath: workflow.PathKey([]string{r.id, "a"}), SessionID: "sess_a", NodeID: "a",
				}, runChatID(r.id))
				r.ask(marotte.EventPermissionNeeded, "a")
			},
		},
		{
			name: "a watch", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start("repeat", "loop")
				r.start("watch", "loop", "iter-0", "mail")
			},
		},
		{
			name: "only a container open", quiet: 2 * time.Hour, verdict: verdictStalled,
			arrange: func(r runTree) {
				r.start("repeat", "loop")
				r.start(stepNodeType, "loop", "iter-0", "a")
				r.complete("loop", "iter-0", "a")
			},
		},
		{
			name: "a step KAS parked for a person", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start(stepNodeType, "a")
				r.park("a")
			},
		},
		{
			name: "a turn running on a parked step", quiet: 2 * time.Hour, verdict: verdictSilent,
			arrange: func(r runTree) {
				r.start("parallel", "p")
				r.start(stepNodeType, "p", "a")
				r.park("p", "a")
				r.h.runs.RunTurnStart(r.id, workflow.PathKey([]string{r.id, "p", "a"}))
			},
		},
		{
			name: "a parked step whose wake turn ended", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start("parallel", "p")
				r.start(stepNodeType, "p", "a")
				r.start("watch", "p", "mail")
				r.park("p", "a")
				key := workflow.PathKey([]string{r.id, "p", "a"})
				r.h.runs.RunTurnStart(r.id, key)
				r.h.runs.RunStopReason(r.id, key, marotte.StopReasonEndTurn)
			},
		},
		{
			name: "a parallel branch parked on a person beside a running silent one", quiet: time.Hour, verdict: verdictSilent,
			arrange: func(r runTree) {
				r.start("parallel", "p")
				r.start(stepNodeType, "p", "a")
				r.start(stepNodeType, "p", "b")
				r.park("p", "a")
			},
		},
		{
			name: "a parallel branch asking a person beside a running silent one", quiet: time.Hour, verdict: verdictSilent,
			arrange: func(r runTree) {
				r.start("parallel", "p")
				r.start(stepNodeType, "p", "a")
				r.start(stepNodeType, "p", "b")
				r.ask(marotte.EventUserInputNeeded, "a")
			},
		},
		{
			name: "a parallel branch parked on a person beside an hourly watch", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start("parallel", "p")
				r.start(stepNodeType, "p", "a")
				r.park("p", "a")
				r.start("watch", "p", "mail")
			},
		},
		{
			name: "a frame from a sibling", quiet: 2 * time.Hour, verdict: verdictKept,
			arrange: func(r runTree) {
				r.start("parallel", "p")
				r.start(stepNodeType, "p", "a")
				r.start(stepNodeType, "p", "b")
			},
			then: func(r runTree) { r.h.runs.RunMadeProgress(r.id) },
		},
	} {
		for origin, launch := range silenceLaunches {
			t.Run(tc.name+" "+origin, func(t *testing.T) {
				fp := newRunOutcomePush()
				h, _, br := newTestHub(WithPush(fp))
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowCancel:  json.RawMessage(`{}`),
					methodKiroWorkflowInspect: inspectReply(t, silentRunID, marotte.RunStatusRunning, ""),
				}
				h.insertBridgeNow(runChatID(silentRunID), testBridge(br, bridgeReady))
				h.runs.log = newRunLog(t.TempDir())
				tree := runTree{t: t, h: h, id: silentRunID}
				tc.arrange(tree)
				deadline := stagedExpiry(t, h.runs, silentRunID, launch)
				stageSilence(h.runs, silentRunID, tc.quiet)
				if tc.then != nil {
					tc.then(tree)
				}

				h.runs.cancelExpired(silentRunID, deadline)
				joinInflight(t, h)

				wantVerdict(t, h, br, fp, tc.verdict, launch.origin == runlease.OriginAgent)
			})
		}
	}
}

func wantVerdict(t *testing.T, h *Runtime, br *fakeBridge, fp *runOutcomePush, v expiryVerdict, chatLaunched bool) {
	t.Helper()
	var pushed int
	for _, p := range runOutcomePushes(fp) {
		if p.body == silencePushBody {
			pushed++
		}
	}
	flagged := !h.runs.silentSince(silentRunID).IsZero()
	switch {
	case v == verdictKept, v == verdictSilent && chatLaunched:
		wantRunning(t, h, br)
		wantPushes := 0
		if v == verdictSilent {
			wantPushes = 1
		}
		if flagged != (v == verdictSilent) || pushed != wantPushes {
			t.Errorf("flagged = %t with %d silence pushes, want flagged = %t with %d", flagged, pushed, v == verdictSilent, wantPushes)
		}
	default:
		if got := h.runs.endReason(silentRunID); got != runEndStalled {
			t.Errorf("endReason = %q, want %q", got, runEndStalled)
		}
		if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
			t.Errorf("no cancel went out: %v", br.callLog())
		}
		if flagged || pushed != 0 {
			t.Errorf("a stopped run was flagged = %t with %d silence pushes, want neither", flagged, pushed)
		}
	}
}
