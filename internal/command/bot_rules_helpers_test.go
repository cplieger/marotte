package command

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

// fakeRuleEngine records every Reindex and RunNow call and answers RunNow from its script.
type fakeRuleEngine struct {
	err       error
	reindexed []marotte.ChatID
	runs      []runNowCall
	answer    RunNowAnswer
	mu        sync.Mutex
}

type runNowCall struct {
	chat marotte.ChatID
	rule string
	op   string
}

func (f *fakeRuleEngine) Reindex(chatID marotte.ChatID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reindexed = append(f.reindexed, chatID)
}

func (f *fakeRuleEngine) RunNow(_ context.Context, chatID marotte.ChatID, ruleID, opID string) (RunNowAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, runNowCall{chat: chatID, rule: ruleID, op: opID})
	return f.answer, f.err
}

func (f *fakeRuleEngine) reindexes() []marotte.ChatID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reindexed)
}

var (
	ruleIDShape    = regexp.MustCompile(`^[a-z2-7]{26}$`)
	triggerIDShape = regexp.MustCompile(`^[a-z2-7]{13}$`)
)

// everyRule is an enabled rule named name with one every trigger of minutes, id tid ("" or "new:<ref>" for a new one).
func everyRule(name, tid string, minutes int) rule.Spec {
	return rule.Spec{
		Name: name, Prompt: "Look at the open pull requests.", Enabled: true,
		Triggers: []rule.Trigger{{ID: tid, Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: minutes}}},
	}
}

func ruleSave(t *testing.T, ruleID string, spec rule.Spec) marotte.BotRuleSave {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return marotte.BotRuleSave{RuleID: ruleID, Spec: raw}
}

// storedRule is a saved rule r-<name> whose one every trigger t-<name> of 60 minutes was armed at armedAt and last
// evaluated at lastAt.
func storedRule(name string, armedAt, lastAt int64) rule.Rule {
	tid := "t-" + name
	return rule.Rule{
		ID:   "r-" + name,
		Spec: everyRule(name, tid, 60),
		Status: rule.Status{
			Triggers: map[string]rule.TriggerState{tid: {ArmedAt: armedAt, LastAt: lastAt}},
		},
	}
}

// queueOf is one event of each kind a disarm meets: a time trigger's stage-new and fired events, a Run now event
// waiting, and a fired event whose prompt Notify already queued on the chat's run order.
func queueOf(tid string) []rule.QueuedEvent {
	return []rule.QueuedEvent{
		{Stage: rule.StageNew, Trigger: tid, EventID: "every:" + tid + ":1", Data: json.RawMessage(`{}`)},
		{Stage: rule.StageFired, Trigger: tid, EventID: "every:" + tid + ":2", Data: json.RawMessage(`{}`)},
		{Stage: rule.StageFired, EventID: "manual:op-press", Data: json.RawMessage(`{}`)},
		{Stage: rule.StageFired, Trigger: tid, EventID: "every:" + tid + ":3", Data: json.RawMessage(`{}`), Queued: true, MessageID: "m-q"},
	}
}

func eventIDs(q []rule.QueuedEvent) []string {
	out := make([]string, 0, len(q))
	for i := range q {
		out = append(out, q[i].EventID)
	}
	return out
}

// armedWindow fails t unless st was armed afresh between before and after: a new generation above was, and no
// anchor or cursor carried over, so nothing fires for the time it was not armed.
func armedWindow(t *testing.T, label string, st rule.TriggerState, was int64, before, after time.Time) {
	t.Helper()
	if st.ArmedAt <= was || st.ArmedAt < before.UnixMilli() || st.ArmedAt > after.UnixMilli()+1 {
		t.Errorf("%s: armed_at = %d, want a new generation above %d within [%d, %d]",
			label, st.ArmedAt, was, before.UnixMilli(), after.UnixMilli()+1)
	}
	if st.LastAt != 0 || st.Cursor != nil {
		t.Errorf("%s: last_at %d cursor %s, want both reset by the arming", label, st.LastAt, st.Cursor)
	}
}

// ruleOf is the stored rule id of c's Bot, failing t when it has none.
func ruleOf(t *testing.T, c *marotte.Chat, id string) *rule.Rule {
	t.Helper()
	for i := range c.Bot.Rules {
		if c.Bot.Rules[i].ID == id {
			return &c.Bot.Rules[i]
		}
	}
	t.Fatalf("the Bot has no rule %q: %+v", id, c.Bot.Rules)
	return nil
}

func ruleIDsOf(c *marotte.Chat) []string {
	out := make([]string, 0, len(c.Bot.Rules))
	for i := range c.Bot.Rules {
		out = append(out, c.Bot.Rules[i].ID)
	}
	return out
}
