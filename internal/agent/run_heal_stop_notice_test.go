package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// stopNotices are the steers marotte wrote that KAS files as a warning notification: the parent agent's copy of the
// stop.
func (g *throttleRig) stopNotices() []map[string]any {
	var out []map[string]any
	for _, p := range g.steerParams() {
		if msg, _ := p["message"].(string); strings.HasPrefix(msg, "[notification/warning] ") {
			out = append(out, p)
		}
	}
	return out
}

// runOutcomePushes drains fp's sent pushes of the run_outcome kind.
func runOutcomePushes(fp *runOutcomePush) []runPushSent {
	var out []runPushSent
	for {
		select {
		case got := <-fp.sent:
			if got.kind == marotte.PushKindRunOutcome {
				out = append(out, got)
			}
		default:
			return out
		}
	}
}

// onClock puts the rig's ledger, model gate and run log on a test clock, and the ledger's timers on the returned host,
// and has every turn on the launching chat's session answer with words, as the engine's read loop would fold them, so
// a wake's turn completes.
func (g *throttleRig) onClock() *attnHost {
	clk := &attnClock{t: time.Now()}
	host := attnOnClock(g.h, clk)
	g.h.runs.log.setClock(clk.now)
	g.br.mu.Lock()
	prev := g.br.onCall
	g.br.mu.Unlock()
	g.br.setOnCall(func(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == marotte.MethodPrompt && fmt.Sprint(p[marotte.KeySessionID]) == parkSession {
			g.h.translateACPEvent(parkChat, g.h.originOf(parkChat), newSessionChunkMsg(parkSession, "On it."))
			return json.RawMessage(`{"stopReason":"end_turn"}`), nil, true
		}
		return prev(method, p)
	})
	return host
}

// stopRead waits until the ledger reads the run as marotte's stopped resumes.
func (g *throttleRig) stopRead(t *testing.T) {
	t.Helper()
	waitUntil(t, "the ledger's read of the stop", func() bool {
		h, ok := ledgerHealth(g.h, gateRunID)
		return ok && h.State == workflow.HealthEnginePaused && strings.Contains(h.Why, "stopped resuming")
	})
}

// deliver runs the ledger past its coalescing window, and the timers then due, and answers the wake turns c1 then
// holds.
func (g *throttleRig) deliver(t *testing.T, host *attnHost) int {
	t.Helper()
	host.clk.advance(attentionCoalesce + time.Second)
	g.h.runs.attention.tick()
	host.fireDue()
	joinInflight(g.h)
	return wakesOf(t, g.h.chatStore.(*testChatStore), parkChat)
}

// When marotte stops resuming a throttled run, the chat that launched it is woken with a turn naming the run and the
// stop; no notice is steered into its session.
func TestHealStop_WakesTheLaunchingChat(t *testing.T) {
	t.Parallel()
	g := newThrottleRig(t, nil)
	clk := g.onClock()
	g.spend(t)
	g.stopRead(t)
	if n := g.deliver(t, clk); n != 1 {
		t.Fatalf("the stop woke the launching chat %d times, want once", n)
	}
	wake := nextPrompt(t, g.h.chatStore.(*testChatStore), parkChat, 0)
	for _, want := range []string{gateRunID, "publish", "stopped resuming"} {
		if !strings.Contains(wake.Text, want) {
			t.Errorf("the wake %q does not name %q", wake.Text, want)
		}
	}
	if n := g.stopNotices(); len(n) != 0 {
		t.Errorf("the stop steered %d warning notices into the launching session, want none: %v", len(n), n)
	}
}

// The user hears of the stop through a push of the "Workflow runs" kind, opening the run.
func TestHealStop_PushesUnderWorkflowRuns(t *testing.T) {
	t.Parallel()
	fp := newRunOutcomePush()
	g := newThrottleRig(t, nil, WithPush(fp))
	g.spend(t)
	joinInflight(g.h)

	pushes := runOutcomePushes(fp)
	want := runPushSent{
		title: "publish", body: "Workflow paused · marotte stopped resuming it after repeated model throttling",
		chat: "c1", kind: marotte.PushKindRunOutcome, subject: marotte.RunSubject(gateRunID),
	}
	if len(pushes) != 1 || pushes[0] != want {
		t.Errorf("the stop pushed %+v, want exactly %+v", pushes, want)
	}
	// The heal that stopped has returned and canceled its context; the push's POST must not be bound to it.
	if n := fp.canceledSends(); n != 0 {
		t.Errorf("%d pushes were sent under a context canceled once the heal returned", n)
	}
}

// A run resumed after the stop (its parent's resume reaches marotte only as KAS's run_start) is healed again, and
// when its next throttles spend the budget again, that stop wakes the chat too. A pause read while the stop stands
// wakes nobody twice.
func TestHealStop_AThrottleAfterAResumedStopIsToldAgain(t *testing.T) {
	t.Parallel()
	fp := newRunOutcomePush()
	g := newThrottleRig(t, nil, WithPush(fp))
	clk := g.onClock()
	g.spend(t)
	g.stopRead(t)
	if n := g.deliver(t, clk); n != 1 {
		t.Fatalf("the first stop woke the chat %d times, want once", n)
	}

	g.throttled(t)
	joinInflight(g.h)
	if n := g.deliver(t, clk); n != 1 {
		t.Fatalf("a pause read while the stop stands woke the chat %d times in all, want the first alone", n)
	}

	g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, gateRunID, marotte.RunStatusRunning, ""))
	g.h.runs.observeStart(t.Context(), parkChat, runNotif(methodWFRunStart, map[string]any{"workflowId": gateRunID}))
	if got := autoResumeStoppedAt(t, g.h); got != "" {
		t.Fatalf("a resume KAS ran left auto_resume_stopped_at = %q", got)
	}
	readNow(t, g.h, gateRunID)
	base := g.resumes.Load()
	for i := range int32(maxAutoHeals) {
		g.throttled(t)
		waitUntil(t, "the automatic resume", func() bool { return g.resumes.Load() == base+i+1 })
	}
	g.throttled(t)
	waitUntil(t, "the second stop recorded", func() bool { return autoResumeStoppedAt(t, g.h) != "" })
	g.stopRead(t)
	if n := g.deliver(t, clk); n != 2 {
		t.Errorf("two stops woke the chat %d times, want twice", n)
	}
	if n := len(runOutcomePushes(fp)); n != 2 {
		t.Errorf("two stops pushed %d run notifications, want two", n)
	}
}
