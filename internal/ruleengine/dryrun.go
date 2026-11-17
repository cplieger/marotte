package ruleengine

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// DryRun is one Test this rule: a spec as the editor holds it, saved or not, and one event of one of its triggers.
type DryRun struct {
	Now time.Time
	// Chat is the Bot's record; DryRun reads it and changes nothing.
	Chat *marotte.Chat
	// Trigger is the id of the spec's trigger the event comes from.
	Trigger string
	// Data is the event, as rule.NewEvent or rule.SampleEvent bound it.
	Data json.RawMessage
	Spec rule.Spec
	// Saved is the index of the stored rule the spec edits, -1 for a new rule.
	Saved int
	// Live, Waiting and Ready are the Bot's turn, its wait on the user, and the agent engine's readiness.
	Live, Waiting, Ready bool
}

var waitReasons = map[rule.Limit]marotte.RuleWaitReason{
	rule.LimitGap: marotte.RuleWaitGap, rule.LimitHour: marotte.RuleWaitHour,
	rule.LimitDay: marotte.RuleWaitDay, rule.LimitBot: marotte.RuleWaitBot,
}

// Test evaluates d as the engine would and answers the outcome, each condition item's value, the hold a fired prompt
// would wait on, in the order delivery checks them, and the exact text, sending and writing nothing.
func Test(d *DryRun) (marotte.RuleTestAnswer, error) {
	rules := slices.Clone(d.Chat.Bot.Rules)
	tested := rule.Rule{Spec: d.Spec}
	ix := d.Saved
	if ix >= 0 {
		tested.Status = rules[ix].Status
		rules[ix] = tested
	} else {
		ix = len(rules)
		rules = append(rules, tested)
	}
	ev := rule.QueuedEvent{Stage: rule.StageFired, Trigger: d.Trigger, EventID: "test", Data: d.Data, Depth: 1}
	busy := d.Live || chatBusy(d.Chat)
	s, err := snapshot(&tested, &ev, d.Now, busy, d.Waiting)
	if err != nil {
		return marotte.RuleTestAnswer{}, err
	}
	res, err := safeEval(d.Spec.Condition, &s)
	if err != nil {
		return marotte.RuleTestAnswer{}, err
	}
	text, err := rule.Render(d.Spec, &ev)
	if err != nil {
		return marotte.RuleTestAnswer{}, err
	}
	out := marotte.RuleTestAnswer{Outcome: marotte.RuleTestFire, Text: text, Leaves: leafValues(d.Spec.Condition, &s)}
	if res.Value != rule.True {
		out.Outcome, out.Reason = marotte.RuleTestSkip, res.Reason
		return out, nil
	}
	until, limit := rule.NextDelivery(rules, ix, d.Trigger, d.Now)
	switch {
	case busy:
		out.Wait = marotte.RuleTestWait{Reason: marotte.RuleWaitBotBusy}
	case limit != rule.LimitNone:
		out.Wait = marotte.RuleTestWait{Reason: waitReasons[limit], Until: until.UnixMilli()}
	case !d.Ready:
		out.Wait = marotte.RuleTestWait{Reason: marotte.RuleWaitNotReady}
	}
	return out, nil
}

var truthValues = map[rule.Truth]marotte.RuleTestValue{
	rule.True: marotte.RuleTestTrue, rule.False: marotte.RuleTestFalse, rule.Unknown: marotte.RuleTestUnknown,
}

func leafValues(c *rule.Cond, s *rule.Snapshot) []marotte.RuleTestLeaf {
	leaves := rule.Leaves(c, s)
	out := make([]marotte.RuleTestLeaf, len(leaves))
	for i, l := range leaves {
		out[i] = marotte.RuleTestLeaf{Path: l.Path, Value: truthValues[l.Value]}
	}
	return out
}
