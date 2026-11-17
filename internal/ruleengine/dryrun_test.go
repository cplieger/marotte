package ruleengine

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// dryRunAt builds a Test of a rule firing from an every trigger at now on a Bot holding saved.
func dryRunAt(t *testing.T, now time.Time, spec rule.Spec, savedIx int, saved ...rule.Rule) *DryRun {
	t.Helper()
	data, err := rule.SampleEvent(spec.Triggers[0], map[string]any{"trigger": "every", "rule": spec.Name})
	if err != nil {
		t.Fatal(err)
	}
	return &DryRun{
		Now: now, Chat: &marotte.Chat{Bot: &marotte.Bot{StartedAt: 1, Rules: saved}},
		Trigger: spec.Triggers[0].ID, Data: data, Spec: spec, Saved: savedIx, Ready: true,
	}
}

// sentTimes is n deliveries spread over the minutes before now.
func sentTimes(now time.Time, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = now.Add(-time.Duration(n-i) * 50 * time.Second).UnixMilli()
	}
	return out
}

// A fired test waits on what delivery checks first: the Bot's own turn, then the latest pace, then the agent
// engine's start; the record is read and never changed.
func TestTest_SaysWhatHoldsAFiredPromptInDeliverysOrder(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	limited := specOf("Tidy", nil, everyT("T1", 15, rule.AnchorLast))
	limited.Triggers[0].Limits = &rule.TriggerLimits{Hold: rule.HoldFIFO, MaxPerHour: 2, HoldCap: 10}
	hourly := armedRule("R1", limited, 1)
	hourly.Status.Triggers["T1"] = rule.TriggerState{ArmedAt: 1, Sent: []int64{now.Add(-50 * time.Minute).UnixMilli(), now.Add(-10 * time.Minute).UnixMilli()}}

	t.Run("the Bot's turn comes before a pace", func(t *testing.T) {
		d := dryRunAt(t, now, limited, 0, hourly)
		d.Live = true
		got, err := Test(d)
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != marotte.RuleTestFire || got.Wait != (marotte.RuleTestWait{Reason: marotte.RuleWaitBotBusy}) {
			t.Errorf("outcome %q wait %+v, want fire waiting on the turn", got.Outcome, got.Wait)
		}
	})
	t.Run("the trigger's hourly limit", func(t *testing.T) {
		got, err := Test(dryRunAt(t, now, limited, 0, hourly))
		if err != nil {
			t.Fatal(err)
		}
		want := marotte.RuleTestWait{Reason: marotte.RuleWaitHour, Until: now.Add(10 * time.Minute).UnixMilli()}
		if got.Wait != want {
			t.Errorf("wait %+v, want %+v: the oldest of the two leaves the hour then", got.Wait, want)
		}
	})
	t.Run("the Bot's ceiling counts every other rule beside a new one", func(t *testing.T) {
		other := armedRule("R2", specOf("Other", nil, everyT("T2", 15, rule.AnchorLast)), 1)
		other.Status.Sent = sentTimes(now, rule.BotMaxPerHour)
		fresh := specOf("New", nil, everyT("new:a", 15, rule.AnchorLast))
		got, err := Test(dryRunAt(t, now, fresh, -1, other))
		if err != nil {
			t.Fatal(err)
		}
		want := marotte.RuleTestWait{Reason: marotte.RuleWaitBot, Until: time.UnixMilli(other.Status.Sent[0]).Add(time.Hour).UnixMilli()}
		if got.Wait != want {
			t.Errorf("wait %+v, want %+v", got.Wait, want)
		}
	})
	t.Run("the agent engine not started", func(t *testing.T) {
		d := dryRunAt(t, now, specOf("Tidy", nil, everyT("T1", 15, rule.AnchorLast)), -1)
		d.Ready = false
		got, err := Test(d)
		if err != nil {
			t.Fatal(err)
		}
		if got.Wait != (marotte.RuleTestWait{Reason: marotte.RuleWaitNotReady}) {
			t.Errorf("wait %+v, want not_ready", got.Wait)
		}
	})
	t.Run("a skip waits on nothing", func(t *testing.T) {
		d := dryRunAt(t, now, specOf("Tidy", leafCond(rule.Leaf{Kind: rule.LeafBotWaiting}), everyT("T1", 15, rule.AnchorLast)), 0, hourly)
		got, err := Test(d)
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != marotte.RuleTestSkip || got.Wait != (marotte.RuleTestWait{}) || got.Reason == "" {
			t.Errorf("outcome %q reason %q wait %+v, want a skip with its reason and no wait", got.Outcome, got.Reason, got.Wait)
		}
		if len(hourly.Status.Triggers["T1"].Sent) != 2 || d.Chat.Bot.Rules[0].Spec.Triggers[0].Limits == nil {
			t.Error("the Bot's record moved, want it read only")
		}
	})
}
