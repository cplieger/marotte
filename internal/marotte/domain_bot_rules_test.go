package marotte

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/rule"
)

// noon is a June day's 12:00 in the server's zone, clear of any DST change, so an at trigger's slot reads the same
// in every zone the suite runs in.
var noon = time.Date(2026, 6, 10, 12, 0, 0, 0, time.Local)

func at(d time.Duration) int64 { return noon.Add(d).UnixMilli() }

func everyLast(id string, minutes int) rule.Trigger {
	return rule.Trigger{ID: id, Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: minutes, Anchor: rule.AnchorLast}}
}

func everyIdle(id string, minutes int) rule.Trigger {
	return rule.Trigger{ID: id, Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: minutes, Anchor: rule.AnchorIdle}}
}

func atDaily(id, hhmm string) rule.Trigger {
	return rule.Trigger{ID: id, Kind: rule.KindAt, At: &rule.AtTrigger{Days: rule.DaysDaily, Time: hhmm}}
}

// armed is an enabled rule whose every trigger was armed at armedAt.
func armed(id, name string, armedAt int64, triggers ...rule.Trigger) rule.Rule {
	r := rule.Rule{ID: id, Spec: rule.Spec{Name: name, Prompt: "Go.", Enabled: true, Triggers: triggers}}
	r.Status.Triggers = map[string]rule.TriggerState{}
	for _, t := range triggers {
		r.Status.Triggers[t.ID] = rule.TriggerState{ArmedAt: armedAt}
	}
	return r
}

func activeBot(rules ...rule.Rule) *Bot {
	return &Bot{Goal: "g", StartedAt: at(-time.Hour), Rules: rules}
}

func botHeader(t *testing.T, c *Chat) BotSummary {
	t.Helper()
	h := c.Header()
	if h.Bot == nil {
		t.Fatal("Header().Bot = nil for a Bot's chat")
	}
	return *h.Bot
}

// The summary's schedule facts: NextAt over every armed time trigger, NextTimedAt over the ones a turn does not
// postpone (every from the last evaluation, at), IdleMinutes over the ones counted from a turn's end.
func TestChatHeader_StatesTheBotsNextRule(t *testing.T) {
	cases := []struct {
		name  string
		chat  Chat
		want  BotSummary
		state BotState
	}{
		{
			name: "one interval from its arming",
			chat: Chat{Bot: activeBot(armed("r1", "Heartbeat", at(0), everyLast("t1", 30)))},
			want: BotSummary{NextAt: at(30 * time.Minute), NextRule: "Heartbeat", NextTimedAt: at(30 * time.Minute), NextTimedRule: "Heartbeat", Rules: 1},
		},
		{
			name: "an interval from its last evaluation",
			chat: Chat{Bot: activeBot(func() rule.Rule {
				r := armed("r1", "Heartbeat", at(0), everyLast("t1", 30))
				r.Status.Triggers["t1"] = rule.TriggerState{ArmedAt: at(0), LastAt: at(45 * time.Minute)}
				return r
			}())},
			want: BotSummary{NextAt: at(75 * time.Minute), NextRule: "Heartbeat", NextTimedAt: at(75 * time.Minute), NextTimedRule: "Heartbeat", Rules: 1},
		},
		{
			name: "an overdue trigger states its due time, not the write's",
			chat: Chat{Bot: activeBot(armed("r1", "Heartbeat", at(-3*time.Hour), everyLast("t1", 30)))},
			want: BotSummary{NextAt: at(-150 * time.Minute), NextRule: "Heartbeat", NextTimedAt: at(-150 * time.Minute), NextTimedRule: "Heartbeat", Rules: 1},
		},
		{
			name: "an idle trigger counts from the last turn's end",
			chat: Chat{LastTurnEndedAt: at(10 * time.Minute), Bot: activeBot(armed("r1", "Check in", at(0), everyIdle("t1", 120)))},
			want: BotSummary{NextAt: at(130 * time.Minute), NextRule: "Check in", IdleMinutes: 120, IdleRule: "Check in", Rules: 1},
		},
		{
			name: "an idle trigger of 120 minutes beside an at trigger",
			chat: Chat{LastTurnEndedAt: at(10 * time.Minute), Bot: activeBot(
				armed("r1", "Check in", at(0), everyIdle("t1", 120)),
				armed("r2", "Daily", at(0), atDaily("t2", "16:00")),
			)},
			want: BotSummary{
				NextAt: at(130 * time.Minute), NextRule: "Check in",
				NextTimedAt: at(4 * time.Hour), NextTimedRule: "Daily",
				IdleMinutes: 120, IdleRule: "Check in", Rules: 2,
			},
		},
		{
			name: "two idle triggers: the next names the earlier, the idle the shorter",
			chat: Chat{Bot: activeBot(
				armed("r1", "Long", at(0), everyIdle("t1", 120)),
				armed("r2", "Short", at(100*time.Minute), everyIdle("t2", 30)),
			)},
			want: BotSummary{NextAt: at(120 * time.Minute), NextRule: "Long", IdleMinutes: 30, IdleRule: "Short", Rules: 2},
		},
		{
			name: "ties go to the first in rule order",
			chat: Chat{Bot: activeBot(
				armed("r1", "First", at(0), everyIdle("t1", 60)),
				armed("r2", "Second", at(0), everyIdle("t2", 60)),
			)},
			want: BotSummary{NextAt: at(time.Hour), NextRule: "First", IdleMinutes: 60, IdleRule: "First", Rules: 2},
		},
		{
			name: "the earliest of one rule's triggers",
			chat: Chat{Bot: activeBot(armed("r1", "Both", at(0), everyLast("t1", 90), atDaily("t2", "13:00")))},
			want: BotSummary{NextAt: at(time.Hour), NextRule: "Both", NextTimedAt: at(time.Hour), NextTimedRule: "Both", Rules: 1},
		},
		{
			name: "a disabled rule is neither counted nor scheduled",
			chat: Chat{Bot: activeBot(
				func() rule.Rule { r := armed("r1", "Off", at(0), everyLast("t1", 5)); r.Spec.Enabled = false; return r }(),
				armed("r2", "On", at(0), everyLast("t2", 60)),
			)},
			want: BotSummary{NextAt: at(time.Hour), NextRule: "On", NextTimedAt: at(time.Hour), NextTimedRule: "On", Rules: 1},
		},
		{
			name: "a trigger never armed is not scheduled",
			chat: Chat{Bot: activeBot(rule.Rule{ID: "r1", Spec: rule.Spec{Name: "Unarmed", Prompt: "Go.", Enabled: true, Triggers: []rule.Trigger{everyLast("t1", 30)}}})},
			want: BotSummary{Rules: 1},
		},
		{
			name: "a paused Bot has no next time",
			chat: Chat{Bot: func() *Bot {
				b := activeBot(armed("r1", "Heartbeat", at(0), everyIdle("t1", 30)))
				b.Paused = true
				return b
			}()},
			want:  BotSummary{Rules: 1},
			state: botStatePaused,
		},
		{
			name: "a closed Bot has no next time",
			chat: Chat{Bot: func() *Bot {
				b := activeBot(armed("r1", "Heartbeat", at(0), everyIdle("t1", 30)))
				b.ClosedAt = at(time.Minute)
				return b
			}()},
			want:  BotSummary{Rules: 1},
			state: botStateClosed,
		},
		{
			name:  "a draft has no next time",
			chat:  Chat{Bot: &Bot{Rules: []rule.Rule{armed("r1", "Check in", at(0), everyIdle("t1", 120))}}},
			want:  BotSummary{Rules: 1},
			state: botStateDraft,
		},
		{
			name: "a failed delivery asks for attention",
			chat: Chat{Bot: activeBot(func() rule.Rule {
				r := armed("r1", "Heartbeat", at(0), everyLast("t1", 30))
				r.Status.LastFailure = &rule.Mark{At: at(time.Minute), Trigger: "t1", EventID: "e1", Reason: "the Bot's queue is full"}
				return r
			}())},
			want: BotSummary{NextAt: at(30 * time.Minute), NextRule: "Heartbeat", NextTimedAt: at(30 * time.Minute), NextTimedRule: "Heartbeat", Rules: 1, Attention: botAttentionRuleFailed},
		},
		{
			name: "the rules version is the record's",
			chat: Chat{Bot: func() *Bot { b := activeBot(); b.RulesVersion = 7; return b }()},
			want: BotSummary{RulesVersion: 7},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.chat
			c.ID = "c1"
			want := tc.want
			want.State = tc.state
			if want.State == "" {
				want.State = botStateActive
			}
			if got := botHeader(t, &c); !reflect.DeepEqual(got, want) {
				t.Errorf("Header().Bot =\n%+v, want\n%+v", got, want)
			}
		})
	}
}

// The summary's wire names are what the sidebar and the Bot pill read; the counts are always present, so the client
// never invents a default for them.
func TestChatHeader_TheBotsRuleFactsOnTheWire(t *testing.T) {
	c := &Chat{ID: "c1", LastTurnEndedAt: at(10 * time.Minute), Bot: activeBot(
		armed("r1", "Check in", at(0), everyIdle("t1", 120)),
		armed("r2", "Daily", at(0), atDaily("t2", "16:00")),
	)}
	c.Bot.RulesVersion = 3
	c.Bot.Rules[0].Status.LastFailure = &rule.Mark{At: at(time.Minute), Trigger: "t1", EventID: "e1"}
	raw, err := json.Marshal(c.Header().Bot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keyed map[string]any
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	want := map[string]any{
		"state": "active", "next_at": float64(at(130 * time.Minute)), "next_rule": "Check in",
		"next_timed_at": float64(at(4 * time.Hour)), "next_timed_rule": "Daily",
		"idle_minutes": float64(120), "idle_rule": "Check in", "rules": float64(2),
		"attention": "rule_failed", "rules_version": float64(3),
	}
	if !reflect.DeepEqual(keyed, want) {
		t.Errorf("summary JSON = %s, want %v", raw, want)
	}

	raw, err = json.Marshal((&Chat{ID: "c2", Bot: &Bot{}}).Header().Bot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	keyed = nil
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if want := map[string]any{"state": "draft", "idle_minutes": float64(0), "rules": float64(0), "rules_version": float64(0)}; !reflect.DeepEqual(keyed, want) {
		t.Errorf("a draft with no rules = %s, want %v", raw, want)
	}
}

// A Bot's rules live in its chat.json, the spec the user wrote beside the status the engine keeps, so a reload loses
// neither and a client body's spec never reaches the status.
func TestChat_ABotsRulesRoundTripThroughChatJSON(t *testing.T) {
	r := armed("r1", "Weekly questions", at(0), everyLast("t1", 30), atDaily("t2", "14:00"))
	r.Spec.Condition = &rule.Cond{Op: rule.OpAll, Children: []rule.Cond{{Leaf: &rule.Leaf{Kind: rule.LeafBotBusy, Not: true}}}}
	r.Status.LastFire = &rule.Mark{At: at(time.Minute), Trigger: "t1", EventID: "every:t1:1", Summary: "every 30 min"}
	r.Status.Sent = []int64{at(time.Minute)}
	r.Status.Queue = []rule.QueuedEvent{{Stage: rule.StageFired, Trigger: "t2", EventID: "at:t2:2", Data: json.RawMessage(`{"block":{}}`), SeenAt: at(2 * time.Minute), Depth: 1}}
	r.Status.Dropped = 2
	bot := Bot{Goal: "g", StartedAt: at(-time.Hour), SpecVersion: 4, RulesVersion: 9, Rules: []rule.Rule{r}}
	raw, err := json.Marshal(&Chat{ID: "c1", Bot: &bot, LastTurnEndedAt: at(5 * time.Minute)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"rules":[{"id":"r1","spec":{`, `"status":{`, `"rules_version":9`, `"last_turn_ended_at":`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Errorf("chat.json lacks %s: %s", key, raw)
		}
	}
	var stored struct {
		Bot struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(stored.Bot.Rules) != 1 {
		t.Fatalf("chat.json holds %d rules, want 1: %s", len(stored.Bot.Rules), raw)
	}
	if spec := stored.Bot.Rules[0]["spec"]; bytes.Contains(spec, []byte(`"queue"`)) || bytes.Contains(spec, []byte(`"last_fire"`)) {
		t.Errorf("the stored spec carries status: %s", spec)
	}
	var got Chat
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if got.LastTurnEndedAt != at(5*time.Minute) {
		t.Errorf("LastTurnEndedAt = %d, want %d", got.LastTurnEndedAt, at(5*time.Minute))
	}
	if got.Bot == nil || !reflect.DeepEqual(*got.Bot, bot) {
		t.Errorf("reloaded bot =\n%+v, want\n%+v", got.Bot, bot)
	}
}
