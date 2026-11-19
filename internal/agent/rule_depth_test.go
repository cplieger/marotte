package agent

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ruleengine"
)

// depthHub is a runtime over a ready bridge for c1 with a real rule engine wired, its loop not running: the depth
// taps need none.
func depthHub(t *testing.T) (*Runtime, *ruleengine.Engine) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	eng := ruleengine.New(&ruleengine.Deps{WakeHold: wakeHoldBound})
	h.SetRuleEngine(eng)
	return h, eng
}

func depthCaller(depth int) *marotte.Caller {
	return &marotte.Caller{Kind: marotte.CallerKindRule, Label: "Tidy", Rule: "r1", Event: "every:t1:1", Depth: depth}
}

// openRuleTurn opens c1's turn from a rule's prompt at depth and answers its id.
func openRuleTurn(t *testing.T, h *Runtime, depth int) string {
	t.Helper()
	id, _ := h.stagePromptTurnFrom(t, "c1", depthCaller(depth), nil)
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	return id
}

func closeDepthTurn(t *testing.T, h *Runtime, id string) {
	t.Helper()
	h.translateACPEvent("c1", h.originOf("c1"), newTurnEndMsg("end_turn"))
	h.coord.releaseTurn("c1", id)
	waitFor(t, func() bool { return !h.TurnLive("c1") })
}

func startDepthRun(t *testing.T, h *Runtime, workflowID string) {
	t.Helper()
	h.runs.observeStart(t.Context(), "c1", runNotif(methodWFRunStart, map[string]any{
		"workflowId": workflowID, "workflowName": "publish",
	}))
}

func leaseDepthOf(t *testing.T, h *Runtime, workflowID string) int {
	t.Helper()
	l, held := h.runs.lease(workflowID)
	if !held {
		t.Fatalf("run_start of %s granted no lease", workflowID)
	}
	return l.Depth
}

func TestLeaseDepth_ARunTakesItsLaunchingTurnsDepth(t *testing.T) {
	h, _ := depthHub(t)
	openRuleTurn(t, h, 2)
	startDepthRun(t, h, "wf_live")
	if got := leaseDepthOf(t, h, "wf_live"); got != 2 {
		t.Errorf("Lease.Depth = %d, want 2", got)
	}
}

// KAS sends run_start after the launching turn may have closed.
func TestLeaseDepth_ARunStartAfterTheTurnClosedTakesTheClosedTurnsDepth(t *testing.T) {
	h, _ := depthHub(t)
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_late")
	if got := leaseDepthOf(t, h, "wf_late"); got != 1 {
		t.Errorf("Lease.Depth = %d, want 1", got)
	}
}

// A restart keeps no closed turn's depth, and neither does a new life of the chat's session.
func TestLeaseDepth_ARunStartInANewSessionIsDepthZero(t *testing.T) {
	h, _ := depthHub(t)
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	h.coord.turns.endLife("c1")
	startDepthRun(t, h, "wf_new")
	if got := leaseDepthOf(t, h, "wf_new"); got != 0 {
		t.Errorf("Lease.Depth = %d, want 0", got)
	}
}

func TestLeaseDepth_AUsersTurnLaunchesAtDepthZero(t *testing.T) {
	h, _ := depthHub(t)
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	startDepthRun(t, h, "wf_user")
	if got := leaseDepthOf(t, h, "wf_user"); got != 0 {
		t.Errorf("Lease.Depth = %d, want 0", got)
	}
}

func TestLeaseDepth_NoEngineGrantsDepthZero(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	h.insertBridgeNow("c1", testBridge(br, bridgeReady))
	openRuleTurn(t, h, 2)
	startDepthRun(t, h, "wf_bare")
	if got := leaseDepthOf(t, h, "wf_bare"); got != 0 {
		t.Errorf("Lease.Depth = %d, want 0", got)
	}
}

// The run's end keeps its lease depth for the wake KAS owes the chat, so the wake turn is one deeper.
func TestWakeDepth_TheWakeTurnAfterAnAutomaticRunIsOneDeeper(t *testing.T) {
	h, eng := depthHub(t)
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_auto")
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_auto", "status": "completed",
	}))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
	waitFor(t, func() bool { return h.TurnLive("c1") })
	if got := eng.LaunchDepth("c1"); got != 2 {
		t.Errorf("wake turn depth = %d, want 2", got)
	}
}

func TestWakeDepth_TheWakeTurnAfterAUsersRunIsDepthZero(t *testing.T) {
	h, eng := depthHub(t)
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	startDepthRun(t, h, "wf_user")
	closeDepthTurn(t, h, id)
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_user", "status": "completed",
	}))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
	waitFor(t, func() bool { return h.TurnLive("c1") })
	if got := eng.LaunchDepth("c1"); got != 0 {
		t.Errorf("wake turn depth = %d, want 0", got)
	}
}

// A run that ends after its parent's session ended owes that life no wake, so a person's run in the reopened chat
// wakes a turn at depth 0, not one deeper than the old automatic run.
func TestWakeDepth_ARunEndingAfterItsParentsSessionEndedDeepensNoLaterWake(t *testing.T) {
	h, eng := depthHub(t)
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_auto")
	if got := leaseDepthOf(t, h, "wf_auto"); got != 1 {
		t.Fatalf("Lease.Depth = %d, want 1", got)
	}
	h.coord.turns.endLife("c1")
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_auto", "status": "completed",
	}))
	h.coord.turns.forget("c1")
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	startDepthRun(t, h, "wf_user")
	closeDepthTurn(t, h, id)
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_user", "status": "completed",
	}))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
	waitFor(t, func() bool { return h.TurnLive("c1") })
	if got := eng.LaunchDepth("c1"); got != 0 {
		t.Errorf("wake turn depth = %d, want 0", got)
	}
}

// endingTaps ends the chat's life from inside RunEnded's caller: a close landing between holdWake's ended check and
// the depth record, which holds it off only while the record runs under the lifecycle's lock.
type endingTaps struct {
	*ruleengine.Engine
	end   func()
	ended chan struct{}
	fired atomic.Bool
}

func (e *endingTaps) RunEnded(parent marotte.ChatID, workflowID string, depth int) {
	if e.fired.Swap(true) {
		e.Engine.RunEnded(parent, workflowID, depth)
		return
	}
	go func() {
		e.end()
		close(e.ended)
	}()
	select {
	case <-e.ended:
	case <-time.After(200 * time.Millisecond):
	}
	e.Engine.RunEnded(parent, workflowID, depth)
}

func TestWakeDepth_ASessionEndingAsTheRunEndsDeepensNoLaterWake(t *testing.T) {
	h, eng := depthHub(t)
	taps := &endingTaps{Engine: eng, end: func() { h.coord.turns.endLife("c1") }, ended: make(chan struct{})}
	h.coord.turns.rules = taps
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_auto")
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_auto", "status": "completed",
	}))
	<-taps.ended
	h.coord.turns.forget("c1")
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	startDepthRun(t, h, "wf_user")
	closeDepthTurn(t, h, id)
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_user", "status": "completed",
	}))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
	waitFor(t, func() bool { return h.TurnLive("c1") })
	if got := eng.LaunchDepth("c1"); got != 0 {
		t.Errorf("wake turn depth = %d, want 0", got)
	}
}

// retryDepthRun is a person's Retry of wf_auto, an agent-launched run from c1, after a rule turn at depth 1 closed.
func retryDepthRun(t *testing.T, h *Runtime, keepLease bool) {
	t.Helper()
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_auto")
	if got := leaseDepthOf(t, h, "wf_auto"); got != 1 {
		t.Fatalf("Setup: Lease.Depth = %d, want 1", got)
	}
	if !keepLease {
		h.runs.forgetBounds(t.Context(), "wf_auto")
	}
	h.runs.leaseForRetry(t.Context(), "wf_auto", &runOrigin{recipe: "publish", parent: runParent{chat: "c1"}}, nil,
		&runAffordance{}, nil)
}

// A person's Retry is a person's launch: its run's wake turn is depth 0 whether the old lease was released or held.
func TestLeaseDepth_APersonsRetryIsDepthZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		held bool
	}{{name: "lease_released"}, {name: "lease_held", held: true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, eng := depthHub(t)
			retryDepthRun(t, h, tc.held)
			if got := leaseDepthOf(t, h, "wf_auto"); got != 0 {
				t.Errorf("Lease.Depth = %d, want 0", got)
			}
			h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_auto", "status": "completed",
			}))
			h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
			waitFor(t, func() bool { return h.TurnLive("c1") })
			if got := eng.LaunchDepth("c1"); got != 0 {
				t.Errorf("wake turn depth = %d, want 0", got)
			}
		})
	}
}
