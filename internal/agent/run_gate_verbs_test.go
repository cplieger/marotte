package agent

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/signin"
)

// fakeAnswerability is the vault's Answerability for a runtime with no vault: set moves it as the vault's edge does.
type fakeAnswerability struct{ a atomic.Int32 }

func wireGate(h *Runtime, a signin.Answerability) *fakeAnswerability {
	f := &fakeAnswerability{}
	f.a.Store(int32(a))
	h.runs.gate.wire(func() signin.Answerability { return signin.Answerability(f.a.Load()) })
	return f
}

func (f *fakeAnswerability) set(h *Runtime, a signin.Answerability) {
	f.a.Store(int32(a))
	h.runs.gateMoved()
}

// runWorkSent lists the run-work writes in calls: the verbs that start or continue a step's model work.
func runWorkSent(calls []string) []string {
	return slices.DeleteFunc(slices.Clone(calls), func(m string) bool {
		switch m {
		case methodKiroWorkflowInvoke, methodKiroWorkflowResume, methodKiroWorkflowRetry, methodKiroWorkflowUpdate,
			marotte.MethodPrompt, marotte.MethodSessionSteer:
			return false
		}
		return true
	})
}

type gateOutcome int

const (
	gateRefuses gateOutcome = iota + 1
	gateHolds
	gateLetsThrough
)

// closedGateVerb is one run verb sent while the sign-in cannot be answered. route names the HTTP route or entry
// point that reaches it; wrote is the method a verb the gate lets through writes.
type closedGateVerb struct {
	hub   func(t *testing.T) (*Runtime, *fakeBridge)
	call  func(ctx context.Context, t *testing.T, h *Runtime) error
	name  string
	route string
	wrote string
	want  gateOutcome
}

func runningStepHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	return messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
}

func pausedStepHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	return messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
}

func restartPausedHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, br := pausedStepHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", stalePauseReason))
	return h, br
}

// steeredStepHub is runningStepHub with one unread row the step holds, sent while the gate was open.
func steeredStepHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, br := runningStepHub(t)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-7", ""); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}
	br.setCallResult(marotte.MethodSessionSteerClear, json.RawMessage(`{"messageIds":["steer-m-7"]}`))
	gen := h.coord.turns.attachForward(runChatID("wf_1")).gen
	br.mu.Lock()
	br.deliveredSeq = 3
	br.mu.Unlock()
	h.coord.turns.observe(runChatID("wf_1"), gen, 3)
	br.mu.Lock()
	br.calls = nil
	br.mu.Unlock()
	return h, br
}

var closedGateVerbs = []closedGateVerb{
	{
		name: "a launch", route: "POST /api/runs", want: gateRefuses,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			_, _, err := h.runs.launch(ctx, "bundled://demo", nil, marotte.RunDefaults{})
			return err
		},
	},
	{
		name: "a scheduled launch", route: "schedule", want: gateRefuses,
		call: func(ctx context.Context, t *testing.T, h *Runtime) error {
			_, _, err := h.runs.LaunchScheduled(ctx, "bundled://demo", "s1", marotte.RunDefaults{}, time.Time{})
			if !errors.Is(err, schedule.ErrNoRunCanStart) {
				t.Errorf("LaunchScheduled = %v, want it held by the schedule runner", err)
			}
			return err
		},
	},
	{
		name: "a user resume", route: "POST /api/runs/{id}/resume", want: gateRefuses, hub: pausedStepHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.resume(ctx, "wf_1", callerPage)
		},
	},
	{
		name: "a retry", route: "POST /api/runs/{id}/retry", want: gateRefuses,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			_, err := h.runs.retry(ctx, "wf_1", &runAffordance{})
			return err
		},
	},
	{
		name: "a repeat's extension", route: "POST /api/runs/{id}/extend", want: gateRefuses, hub: pausedStepHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.extendRepeat(ctx, "wf_1", "loop", 2, callerPage)
		},
	},
	{
		name: "a repeat's finish", route: "POST /api/runs/{id}/finish-loop", want: gateRefuses, hub: pausedStepHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.finishRepeat(ctx, "wf_1", "loop")
		},
	},
	{
		name: "an answer", route: "POST /api/runs/{id}/answer", want: gateRefuses, hub: askHub,
		call: func(ctx context.Context, t *testing.T, h *Runtime) error {
			return h.runs.answerInput(ctx, "wf_1", readNow(t, h, "wf_1").asks[0].ID, "the main branch")
		},
	},
	{
		name: "a running step's steer", route: "POST /api/runs/{id}/steps/{path}/{verb}", want: gateRefuses,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			_, err := h.runs.messageStep(ctx, "wf_1", reviewPath, "check the logs", "m-1", "")
			return err
		},
	},
	{
		name: "a paused step's prompt", route: "POST /api/runs/{id}/steps/{path}/{verb}", want: gateRefuses,
		hub: pausedStepHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			_, err := h.runs.messageStep(ctx, "wf_1", reviewPath, "carry on", "m-1", "")
			return err
		},
	},
	{
		name: "an asking step's answer", route: "POST /api/runs/{id}/steps/{path}/{verb}", want: gateRefuses,
		hub: askHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			_, err := h.runs.messageStep(ctx, "wf_1", reviewPath, "the main branch", "m-1", "")
			return err
		},
	},
	{
		name: "a step steer's removal, which resubmits the rows it keeps", route: "POST /api/runs/{id}/steps/{path}/{verb}",
		want: gateRefuses, hub: steeredStepHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.removeStepSteer(ctx, "wf_1", reviewPath, "steer-m-7")
		},
	},
	{
		name: "a step's steers cleared", route: "POST /api/runs/{id}/steps/{path}/{verb}", want: gateLetsThrough,
		hub: steeredStepHub, wrote: marotte.MethodSessionSteerClear,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.clearStepSteers(ctx, "wf_1", reviewPath)
		},
	},
	{
		name: "an automatic resume", route: "automatic resume", want: gateHolds, hub: restartPausedHub,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			if h.runs.resumeIfInterrupted(ctx, parkChat, "wf_1") == resumeHeld {
				return errRunHeld
			}
			return nil
		},
	},
	{
		name: "a pause", route: "POST /api/runs/{id}/pause", want: gateLetsThrough, wrote: methodKiroWorkflowPause,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.pause(ctx, "wf_1", callerPage)
		},
	},
	{
		name: "a cancel", route: "POST /api/runs/{id}/cancel", want: gateLetsThrough, wrote: methodKiroWorkflowCancel,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error {
			return h.runs.cancel(ctx, "wf_1", callerPage)
		},
	},
	{
		name: "a delete", route: "DELETE /api/runs/{id}", want: gateLetsThrough, wrote: methodKiroWorkflowDelete,
		call: func(ctx context.Context, _ *testing.T, h *Runtime) error { return h.runs.delete(ctx, "wf_1") },
	},
}

// Every run verb a person or marotte can send while the sign-in cannot be answered: run work is refused unwritten
// or held for the sign-in, and a stop goes through.
func TestSignInGate_EveryRunVerbWhileClosed(t *testing.T) {
	for _, tc := range closedGateVerbs {
		t.Run(tc.name, func(t *testing.T) {
			hub := tc.hub
			if hub == nil {
				hub = runningStepHub
			}
			h, br := hub(t)
			wireGate(h, signin.NeedsSignIn)
			err := tc.call(t.Context(), t, h)
			switch tc.want {
			case gateRefuses:
				if !errors.Is(err, errRunsNeedSignIn) {
					t.Errorf("%s = %v, want the sign-in refusal", tc.name, err)
				}
			case gateHolds:
				if !errors.Is(err, errRunHeld) {
					t.Errorf("%s = %v, want the run held", tc.name, err)
				}
			case gateLetsThrough:
				if errors.Is(err, errRunsNeedSignIn) || errors.Is(err, errRunHeld) {
					t.Errorf("%s = %v, want it let through", tc.name, err)
				}
				if !slices.Contains(br.callLog(), tc.wrote) {
					t.Errorf("calls = %v, want %s written", br.callLog(), tc.wrote)
				}
			}
			if sent := runWorkSent(br.callLog()); len(sent) != 0 {
				t.Errorf("run work written = %v, want none", sent)
			}
		})
	}
}

// The table covers every route that writes to a run.
func TestSignInGate_EveryRunVerbRouteIsInTheTable(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "run_routes.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse run_routes.go: %v", err)
	}
	var routes []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !slices.Contains([]string{"HandleFunc", "Handle", "write"}, calleeName(call)) || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		if p, uErr := strconv.Unquote(lit.Value); uErr == nil && !strings.HasPrefix(p, "GET ") {
			routes = append(routes, p)
		}
		return true
	})
	if len(routes) < 8 {
		t.Fatalf("found %d writing routes %v, want the run verbs", len(routes), routes)
	}
	for _, r := range routes {
		if !slices.ContainsFunc(closedGateVerbs, func(v closedGateVerb) bool { return v.route == r }) {
			t.Errorf("route %s has no row in closedGateVerbs", r)
		}
	}
}

// A steer refused while the gate is closed leaves no row in the step's dock, so the user's text goes back to the
// composer; the same message is steered once the sign-in answers again.
func TestSignInGate_AStepSteerIsRefusedBeforeItsRowIsRouted(t *testing.T) {
	for _, tc := range []struct {
		state signin.Answerability
		want  error
	}{
		{state: signin.NeedsSignIn, want: errRunsNeedSignIn},
		{state: signin.ProviderUnreachable, want: errRunsProviderDown},
	} {
		t.Run(tc.state.String(), func(t *testing.T) {
			h, br := runningStepHub(t)
			gate := wireGate(h, tc.state)

			_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-1", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("MessageStep = %v, want %v", err, tc.want)
			}
			if rows := h.bus.steers.list(""); len(rows) != 0 {
				t.Errorf("dock rows = %d, want none for a refused steer", len(rows))
			}

			gate.set(h, signin.Answerable)
			if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-1", ""); err != nil {
				t.Fatalf("MessageStep once answerable = %v", err)
			}
			if n := countCalls(br, marotte.MethodSessionSteer); n != 1 {
				t.Errorf("steers written = %d, want 1", n)
			}
		})
	}
}

// A step's steer target built with no admission sends no steer: the engine's guard refuses it unwritten.
func TestStepSteer_AnUnadmittedTargetWritesNoSteer(t *testing.T) {
	h, br := runningStepHub(t)
	lease, err := h.runs.life.admit("wf_1")
	if err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	defer lease.end()
	tgt := h.runs.steers.steerTarget(lease, runAdmission{}, "sess_step")

	_, err = tgt.Caller.Call(t.Context(), marotte.MethodSessionSteer, map[string]any{"message": "x"})
	if !errors.Is(err, errRunWorkUnadmitted) {
		t.Errorf("steer = %v, want errRunWorkUnadmitted", err)
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 0 {
		t.Errorf("steers written = %d, want 0", n)
	}
}

// stepKey is messageHub's step steering buffer.
var stepKey = marotte.StepSteerKey("sess_step")

// waitingRow leaves one unread row waiting in the step's open channel, the state a flush job sends from.
func waitingRow(t *testing.T, h *Runtime) {
	t.Helper()
	if !h.runs.steers.recs.step(stepKey, false, func(rec *steerRecord, _ *steerFx) {
		rec.channel = chanOpen
		rec.rows = append(rec.rows, dockRow{key: "steer-q", text: "and the tests", seq: rec.nextSeq, state: rowWaiting})
		rec.nextSeq++
	}) {
		t.Fatal("Setup: the step has no steering record")
	}
}

func waitingRows(h *Runtime) int {
	n := 0
	h.runs.steers.recs.step(stepKey, false, func(rec *steerRecord, _ *steerFx) { n = len(rec.unownedWaiting()) })
	return n
}

// A step's queued flush while the gate is closed sends nothing and keeps its rows waiting; the sign-in sends them.
func TestSignInGate_AStepsQueuedFlushWaitsForTheSignIn(t *testing.T) {
	h, br := runningStepHub(t)
	waitingRow(t, h)
	gate := wireGate(h, signin.NeedsSignIn)

	h.runStepSteerJob(command.SteerJob{Chat: stepKey})
	time.Sleep(quietWindow)
	if n := countCalls(br, marotte.MethodSessionSteer); n != 0 {
		t.Errorf("steers written while closed = %d, want 0", n)
	}
	if n := waitingRows(h); n != 1 {
		t.Errorf("waiting rows = %d, want the row kept for the flush", n)
	}

	gate.set(h, signin.Answerable)
	waitUntil(t, "the flush once answerable", func() bool { return countCalls(br, marotte.MethodSessionSteer) == 1 })
}

// Deleting a row KAS never held sends nothing, so the closed gate lets it through.
func TestSignInGate_ARemovalThatSendsNothingIsLetThrough(t *testing.T) {
	h, br := runningStepHub(t)
	waitingRow(t, h)
	wireGate(h, signin.NeedsSignIn)

	if err := h.runs.removeStepSteer(t.Context(), "wf_1", reviewPath, "steer-q"); err != nil {
		t.Fatalf("removeStepSteer = %v, want the row deleted", err)
	}
	if n := waitingRows(h); n != 0 {
		t.Errorf("waiting rows = %d, want 0", n)
	}
	if calls := br.callLog(); len(calls) != 0 {
		t.Errorf("calls = %v, want none", calls)
	}
}

// A removal of a row KAS holds that the queue refuses before any clear sends nothing either: the closed gate leaves
// the queue's own refusal standing.
func TestSignInGate_ARemovalTheQueueRefusesKeepsItsReason(t *testing.T) {
	cases := []struct {
		setup func(rec *steerRecord)
		name  string
		want  string
	}{
		{
			name: "agent rows are waiting",
			setup: func(rec *steerRecord) {
				rec.others = append(rec.others, marotte.SteerQueuedPayload{SteerID: "steer-agent"})
			},
			want: command.SteerRefuseAgentRows,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, br := steeredStepHub(t)
			if !h.runs.steers.recs.step(stepKey, false, func(rec *steerRecord, _ *steerFx) { tc.setup(rec) }) {
				t.Fatal("Setup: the step has no steering record")
			}
			wireGate(h, signin.NeedsSignIn)

			err := h.runs.removeStepSteer(t.Context(), "wf_1", reviewPath, "steer-m-7")
			if _, reason, _ := command.StatusOf(err); reason != tc.want {
				t.Errorf("removeStepSteer = %v (reason %q), want the queue's %q", err, reason, tc.want)
			}
			if calls := br.callLog(); len(calls) != 0 {
				t.Errorf("calls = %v, want none", calls)
			}
		})
	}
}

// A queued flush waiting on a closed gate ends when its step's run ends, with no gate move: it holds no in-flight
// slot for the rest of the outage.
func TestSignInGate_AWaitingFlushEndsWithItsStep(t *testing.T) {
	h, br := runningStepHub(t)
	waitingRow(t, h)
	wireGate(h, signin.NeedsSignIn)
	h.lifecycle.inflight.Wait()

	h.runStepSteerJob(command.SteerJob{Chat: stepKey})
	ended := make(chan struct{})
	go func() {
		h.lifecycle.inflight.Wait()
		close(ended)
	}()
	select {
	case <-ended:
		t.Fatal("Setup: the flush ended before its step did, want it waiting on the gate")
	case <-time.After(quietWindow):
	}

	h.runs.closeRun(t.Context(), "wf_1", string(marotte.RunStatusCancelled))
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the flush still waits after its step's run ended")
	}
	if n := countCalls(br, marotte.MethodSessionSteer); n != 0 {
		t.Errorf("steers written = %d, want 0", n)
	}
}

// A steer admitted before the gate closed and answered after it sweeps the running runs again, since the closing
// edge's sweep may have read the run before the steer reached it.
func TestSignInGate_AStepSteerCrossingTheClosingEdgeSweepsAgain(t *testing.T) {
	h, br := runningStepHub(t)
	gate := wireGate(h, signin.Answerable)
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{"workflowId": "wf_1", "status": "paused"}))
	release := make(chan struct{})
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodSessionSteer {
			<-release
		}
		return nil, nil, false
	}
	br.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := h.runs.messageStep(context.Background(), "wf_1", reviewPath, "check the logs", "m-1", "")
		done <- err
	}()
	waitUntil(t, "the steer to be written", func() bool { return slices.Contains(br.callLog(), marotte.MethodSessionSteer) })
	gate.set(h, signin.NeedsSignIn)
	waitUntil(t, "the closing edge's sweep", func() bool { return countCalls(br, methodKiroWorkflowList) == 1 })
	time.Sleep(quietWindow)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the admitted steer = %v, want it written", err)
	}
	waitUntil(t, "the steer's own sweep", func() bool { return countCalls(br, methodKiroWorkflowList) == 2 })
}

// A run KAS parks again once its heal budget is spent is stamped for the user off the pause frame itself.
func TestHealBudget_ASpentRunsNextPauseIsParkedForTheUser(t *testing.T) {
	h, br := agentRunOnChat(t, gateRunID)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.grantLease(t.Context(), gateRunID, "publish", agentLaunch(), hostSec())
	h.runs.recordParentSession(t.Context(), gateRunID, parkSession)
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, gateRunID, stalePauseReason))
	g := &gateRun{h: h, br: br}
	verbs := g.watchVerbs(t)
	for range maxAutoHeals {
		h.runs.claimHeal(gateRunID)
	}

	heal := h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	heal(t.Context(), parkChat, runNotif(methodWFPaused, map[string]any{"workflowId": gateRunID, "pauseReason": stalePauseReason}))
	waitUntil(t, "the run's pause stamped for the user", func() bool { return len(verbs.pauseParams()) == 1 })
	if p := verbs.pauseParams()[0]; p["reason"] != manualResumeReason {
		t.Errorf("pause reason = %v, want %q", p["reason"], manualResumeReason)
	}
	awaitResumes(t, &verbs.resumes, 0)
}
