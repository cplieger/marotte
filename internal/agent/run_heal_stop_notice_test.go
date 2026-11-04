package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
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

// When marotte stops resuming a throttled run, the agent that launched it is told in its own session, as a warning
// notification its next turn reads, naming the run so it can resume or end it.
func TestHealStop_TellsTheParentSession(t *testing.T) {
	g := newThrottleRig(t, nil)
	g.spend(t)
	waitUntil(t, "the parent's notice", func() bool { return len(g.stopNotices()) > 0 })

	notices := g.stopNotices()
	if len(notices) != 1 {
		t.Fatalf("the stop wrote %d notices to the parent, want one: %v", len(notices), notices)
	}
	if got := fmt.Sprint(notices[0][marotte.KeySessionID]); got != parkSession {
		t.Errorf("the notice went to session %q, want the launching session %q", got, parkSession)
	}
	msg, _ := notices[0]["message"].(string)
	for _, want := range []string{gateRunID, "publish", "throttling"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the parent's notice %q does not name %q", msg, want)
		}
	}
}

// The user hears of the stop through a push of the "Workflow runs" kind, opening the run.
func TestHealStop_PushesUnderWorkflowRuns(t *testing.T) {
	fp := newRunOutcomePush()
	g := newThrottleRig(t, nil, WithPush(fp))
	g.spend(t)
	joinInflight(t, g.h)

	pushes := runOutcomePushes(fp)
	want := runPushSent{
		title: "publish", body: "Workflow paused · marotte stopped resuming it after repeated model throttling",
		kind: marotte.PushKindRunOutcome, subject: marotte.RunSubject(gateRunID),
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
// when its next throttles spend the budget again, that stop is told too. A pause read while the stop stands tells
// nobody twice.
func TestHealStop_AThrottleAfterAResumedStopIsToldAgain(t *testing.T) {
	fp := newRunOutcomePush()
	g := newThrottleRig(t, nil, WithPush(fp))
	g.spend(t)
	waitUntil(t, "the first notice", func() bool { return len(g.stopNotices()) == 1 })

	g.throttled(t)
	joinInflight(t, g.h)
	if n := len(g.stopNotices()); n != 1 {
		t.Fatalf("a pause read while the stop stands wrote %d notices, want the first alone", n)
	}

	g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, gateRunID, marotte.RunStatusRunning, ""))
	g.h.runs.observeStart(t.Context(), parkChat, runNotif(methodWFRunStart, map[string]any{"workflowId": gateRunID}))
	if got := autoResumeStoppedAt(t, g.h); got != "" {
		t.Fatalf("a resume KAS ran left auto_resume_stopped_at = %q", got)
	}
	base := g.resumes.Load()
	for i := range int32(maxAutoHeals) {
		g.throttled(t)
		waitUntil(t, "the automatic resume", func() bool { return g.resumes.Load() == base+i+1 })
	}
	g.throttled(t)
	waitUntil(t, "the second notice", func() bool { return len(g.stopNotices()) == 2 })
	joinInflight(t, g.h)
	if n := len(runOutcomePushes(fp)); n != 2 {
		t.Errorf("two stops pushed %d run notifications, want two", n)
	}
}
