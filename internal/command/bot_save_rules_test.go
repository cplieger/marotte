package command

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/testsupport"
)

// mutateCounter counts the record writes a command makes.
type mutateCounter struct {
	chatStore
	n atomic.Int32
}

func (m *mutateCounter) Mutate(ctx context.Context, id marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	m.n.Add(1)
	return m.chatStore.Mutate(ctx, id, fn)
}

// newRuleRoles is newBotRoles with a recording rule engine.
func newRuleRoles(t *testing.T, store chatStore) (*botRoles, *fakeRuleEngine) {
	t.Helper()
	roles, _ := newBotRoles(t, store)
	eng := &fakeRuleEngine{}
	roles.rules = eng
	return roles, eng
}

func stringsOf(t *testing.T, v any) []string {
	t.Helper()
	s, ok := v.([]string)
	if !ok {
		t.Fatalf("answer = %T %v, want a []string", v, v)
	}
	return s
}

func triggerIDsOf(t *testing.T, v any) [][]string {
	t.Helper()
	s, ok := v.([][]string)
	if !ok {
		t.Fatalf("answer trigger_ids = %T %v, want a [][]string", v, v)
	}
	return s
}

func TestCmdBotSave_RefusesEveryInvalidRuleAtItsPathAndSavesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{
		Goal: "Watch.", StartedAt: 1, SpecVersion: 2,
		Rules: []rule.Rule{storedRule("kept", 1, 2)},
	})
	roles, eng := newRuleRoles(t, store)
	bad := everyRule("Weekly #[[a:b]]", "", 1)
	p := &marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", BaseVersion: 2, OpID: "op-1", Rules: []marotte.BotRuleSave{
		ruleSave(t, "", everyRule("Fine", "", 60)),
		ruleSave(t, "", bad),
		{Spec: json.RawMessage(`{"name":"X","prompt":"p","enabled":true,"triggers":[{"kind":"every","every":{"minutes":60}}],"bogus":1}`)},
	}}

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", p))

	if statusOf(err) != http.StatusBadRequest || reasonOf(err) != reasonBotInvalid {
		t.Fatalf("bot_save = %d %q, want 400 %s (%v)", statusOf(err), reasonOf(err), reasonBotInvalid, err)
	}
	got := fieldsOf(err)
	slices.Sort(got)
	if want := []string{"rules[1].name", "rules[1].triggers[0].every.minutes", "rules[2].bogus"}; !slices.Equal(got, want) {
		t.Errorf("fields = %v, want %v: every error of every rule, each at its path", got, want)
	}
	c := botOf(t, store, "c-bot")
	if ids := ruleIDsOf(c); !slices.Equal(ids, []string{"r-kept"}) || c.Bot.SpecVersion != 2 {
		t.Errorf("rules %v version %d, want the stored rule and version 2 kept", ids, c.Bot.SpecVersion)
	}
	if n := len(eng.reindexes()); n != 0 {
		t.Errorf("a refused save reindexed %d times", n)
	}
}

func TestCmdBotSave_RefusesMoreThanTwentyRulesAtRules(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	roles, _ := newRuleRoles(t, store)
	rules := make([]marotte.BotRuleSave, rule.MaxRules+1)
	for i := range rules {
		rules[i] = ruleSave(t, "", everyRule("Rule", "", 60))
	}

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot",
		&marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", OpID: "op-1", Rules: rules}))

	if statusOf(err) != http.StatusBadRequest || !slices.Equal(fieldsOf(err), []string{"rules"}) {
		t.Errorf("bot_save of 21 rules = %d at %v, want 400 at rules", statusOf(err), fieldsOf(err))
	}
	if c := botOf(t, store, "c-bot"); len(c.Bot.Rules) != 0 {
		t.Errorf("a refused save stored %d rules", len(c.Bot.Rules))
	}
}

// A save past 20 rules is refused at rules even when one of them does not decode, beside that rule's own error.
func TestCmdBotSave_CountsTheRulesAsSentBesideAnInvalidOne(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	roles, _ := newRuleRoles(t, store)
	rules := make([]marotte.BotRuleSave, rule.MaxRules+1)
	rules[0] = marotte.BotRuleSave{Spec: json.RawMessage(`{"name":"X","prompt":"p","enabled":true,"triggers":[{"kind":"every","every":{"minutes":60}}],"bogus":1}`)}
	for i := 1; i < len(rules); i++ {
		rules[i] = ruleSave(t, "", everyRule("Rule", "", 60))
	}

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot",
		&marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", OpID: "op-1", Rules: rules}))

	got := fieldsOf(err)
	slices.Sort(got)
	if want := []string{"rules", "rules[0].bogus"}; statusOf(err) != http.StatusBadRequest || !slices.Equal(got, want) {
		t.Errorf("bot_save = %d at %v, want 400 at %v", statusOf(err), got, want)
	}
}

func TestCmdBotSave_ANewRuleNeedsAnOpID(t *testing.T) {
	for name, op := range map[string]string{"none": "", "not an identifier": "op 1"} {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
			roles, _ := newRuleRoles(t, store)

			_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
				Name: "Watcher", Goal: "Watch.", OpID: op, Rules: []marotte.BotRuleSave{ruleSave(t, "", everyRule("New", "", 60))},
			}))

			if statusOf(err) != http.StatusBadRequest || !slices.Equal(fieldsOf(err), []string{"op_id"}) {
				t.Errorf("bot_save = %d at %v, want 400 at op_id", statusOf(err), fieldsOf(err))
			}
		})
	}
}

func TestCmdBotSave_AKeptRuleNeedsNoOpID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, Rules: []rule.Rule{storedRule("kept", 1, 2)}})
	roles, _ := newRuleRoles(t, store)

	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.",
		Rules: []marotte.BotRuleSave{ruleSave(t, "r-kept", everyRule("kept", "t-kept", 60))},
	})
}

func TestCmdBotSave_RefusesARuleIDTheBotDoesNotHoldOrSendsTwice(t *testing.T) {
	cases := []struct {
		name  string
		rules func(t *testing.T) []marotte.BotRuleSave
		want  string
	}{
		{name: "an unknown id", want: "rules[0].rule_id", rules: func(t *testing.T) []marotte.BotRuleSave {
			return []marotte.BotRuleSave{ruleSave(t, "r-gone", everyRule("gone", "", 60))}
		}},
		{name: "one id twice", want: "rules[1].rule_id", rules: func(t *testing.T) []marotte.BotRuleSave {
			return []marotte.BotRuleSave{
				ruleSave(t, "r-kept", everyRule("kept", "t-kept", 60)), ruleSave(t, "r-kept", everyRule("kept", "t-kept", 60)),
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, Rules: []rule.Rule{storedRule("kept", 1, 2)}})
			roles, _ := newRuleRoles(t, store)

			_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot",
				&marotte.BotSaveCommand{Name: "Watcher", Goal: "Watch.", OpID: "op-1", Rules: tc.rules(t)}))

			if statusOf(err) != http.StatusBadRequest || !slices.Equal(fieldsOf(err), []string{tc.want}) {
				t.Errorf("bot_save = %d at %v, want 400 at %s", statusOf(err), fieldsOf(err), tc.want)
			}
		})
	}
}

func TestCmdBotSave_MintsIDsWritesOnceAndRewritesEveryPlaceholder(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	counter := &mutateCounter{chatStore: store}
	roles, eng := newRuleRoles(t, counter)
	by := func(tid string) rule.Cond {
		return rule.Cond{Leaf: &rule.Leaf{Kind: rule.LeafTriggeredBy, By: &rule.ByLeaf{Triggers: []string{tid}}}}
	}
	two := rule.Spec{
		Name: "Two", Prompt: "Look.", Enabled: true,
		Triggers: []rule.Trigger{
			{ID: "new:a", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 60}},
			{ID: "new:b", Kind: rule.KindAt, At: &rule.AtTrigger{Days: rule.DaysDaily, Time: "09:00"}},
		},
		Condition: &rule.Cond{Op: rule.OpAny, Children: []rule.Cond{
			{Op: rule.OpAll, Children: []rule.Cond{by("new:a"), {Leaf: &rule.Leaf{Kind: rule.LeafBotBusy, Not: true}}}},
			{Op: rule.OpAll, Children: []rule.Cond{by("new:b"), {Leaf: &rule.Leaf{Kind: rule.LeafBotWaiting, Not: true}}}},
		}},
	}

	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.", OpID: "op-1",
		Rules: []marotte.BotRuleSave{ruleSave(t, "", two), ruleSave(t, "", everyRule("One", "", 30))},
	})

	ruleIDs := stringsOf(t, body["rule_ids"])
	triggerIDs := triggerIDsOf(t, body["trigger_ids"])
	if len(ruleIDs) != 2 || len(triggerIDs) != 2 || len(triggerIDs[0]) != 2 || len(triggerIDs[1]) != 1 {
		t.Fatalf("answer rule_ids %v trigger_ids %v, want 2 rules of 2 and 1 triggers", ruleIDs, triggerIDs)
	}
	minted := append(slices.Clone(ruleIDs), slices.Concat(triggerIDs...)...)
	for _, id := range ruleIDs {
		if !ruleIDShape.MatchString(id) {
			t.Errorf("rule id %q, want 16 random bytes in the id vocabulary's lowercase base32", id)
		}
	}
	for _, id := range slices.Concat(triggerIDs...) {
		if !triggerIDShape.MatchString(id) {
			t.Errorf("trigger id %q, want 8 random bytes in the id vocabulary's lowercase base32", id)
		}
	}
	if s := slices.Clone(minted); len(slices.Compact(slices.Sorted(slices.Values(s)))) != len(minted) {
		t.Errorf("minted ids %v, want every one distinct", minted)
	}
	c := botOf(t, store, "c-bot")
	if got := ruleIDsOf(c); !slices.Equal(got, ruleIDs) {
		t.Fatalf("stored rules %v, want the answered ids in the sent order %v", got, ruleIDs)
	}
	stored := c.Bot.Rules[0].Spec
	if stored.Triggers[0].ID != triggerIDs[0][0] || stored.Triggers[1].ID != triggerIDs[0][1] {
		t.Errorf("stored trigger ids %q %q, want the answered %v", stored.Triggers[0].ID, stored.Triggers[1].ID, triggerIDs[0])
	}
	branches := stored.Condition.Children
	if got := branches[0].Children[0].Leaf.By.Triggers; !slices.Equal(got, []string{triggerIDs[0][0]}) {
		t.Errorf("the first branch leads with %v, want the minted %q", got, triggerIDs[0][0])
	}
	if got := branches[1].Children[0].Leaf.By.Triggers; !slices.Equal(got, []string{triggerIDs[0][1]}) {
		t.Errorf("the second branch leads with %v, want the minted %q", got, triggerIDs[0][1])
	}
	raw, err := json.Marshal(c.Bot.Rules)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), rule.NewIDPrefix) {
		t.Errorf("the stored rules still name a placeholder: %s", raw)
	}
	if c.Bot.SpecVersion != 1 || body["spec_version"] != 1 {
		t.Errorf("spec_version %d answered %v, want 1", c.Bot.SpecVersion, body["spec_version"])
	}
	if n := counter.n.Load(); n != 1 {
		t.Errorf("the save wrote the record %d times, want one Mutate", n)
	}
	if got := eng.reindexes(); !slices.Equal(got, []marotte.ChatID{"c-bot"}) {
		t.Errorf("reindexed %v, want c-bot once after the write", got)
	}
}

func TestCmdBotSave_AReplayedOpIDAnswersTheSameIDsAndSavesOnce(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1})
	roles, _ := newRuleRoles(t, store)
	p := &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.", OpID: "op-1",
		Rules: []marotte.BotRuleSave{ruleSave(t, "", everyRule("One", "", 30))},
	}

	first := saveBot(t, roles, "c-bot", p)
	again := saveBot(t, roles, "c-bot", p)

	if a, b := stringsOf(t, first["rule_ids"]), stringsOf(t, again["rule_ids"]); !slices.Equal(a, b) {
		t.Errorf("replayed rule_ids %v, want the first answer's %v", b, a)
	}
	a, b := triggerIDsOf(t, first["trigger_ids"]), triggerIDsOf(t, again["trigger_ids"])
	if !slices.EqualFunc(a, b, slices.Equal[[]string]) {
		t.Errorf("replayed trigger_ids %v, want the first answer's %v", b, a)
	}
	c := botOf(t, store, "c-bot")
	if len(c.Bot.Rules) != 1 || c.Bot.SpecVersion != 1 || again["spec_version"] != 1 {
		t.Errorf("after the replay %d rules, version %d answered %v; want one rule saved once at version 1",
			len(c.Bot.Rules), c.Bot.SpecVersion, again["spec_version"])
	}
}

func TestCmdBotSave_AStaleBaseSavesNoRule(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, SpecVersion: 3, Rules: []rule.Rule{storedRule("kept", 1, 2)}})
	roles, eng := newRuleRoles(t, store)

	_, err := cmdBotSave(t.Context(), roles, botSaveReq(t, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.", BaseVersion: 2, OpID: "op-1",
		Rules: []marotte.BotRuleSave{ruleSave(t, "", everyRule("New", "", 30))},
	}))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonBotChanged {
		t.Errorf("bot_save = %d %q, want 409 bot_changed", statusOf(err), reasonOf(err))
	}
	if c := botOf(t, store, "c-bot"); !slices.Equal(ruleIDsOf(c), []string{"r-kept"}) || c.Bot.SpecVersion != 3 {
		t.Errorf("rules %v version %d, want the record unchanged", ruleIDsOf(c), c.Bot.SpecVersion)
	}
	if n := len(eng.reindexes()); n != 0 {
		t.Errorf("a refused save reindexed %d times", n)
	}
}

func TestCmdBotSave_AKeptRuleKeepsItsStatusAndAnAbsentOneGoesWithItsQueue(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	kept := storedRule("kept", 1_000, 5_000)
	kept.Status.LastFire = &rule.Mark{At: 5_000, Trigger: "t-kept", EventID: "every:t-kept:5000", Summary: "Every hour"}
	kept.Status.Queue = queueOf("t-kept")[1:2]
	gone := storedRule("gone", 1_000, 4_000)
	gone.Status.Queue = queueOf("t-gone")[:2]
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, Rules: []rule.Rule{kept, gone}})
	roles, _ := newRuleRoles(t, store)
	edited := everyRule("kept", "t-kept", 60)
	edited.Prompt = "Look at the open issues too."

	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.",
		Rules: []marotte.BotRuleSave{ruleSave(t, "r-kept", edited)},
	})

	c := botOf(t, store, "c-bot")
	if got := ruleIDsOf(c); !slices.Equal(got, []string{"r-kept"}) {
		t.Fatalf("rules %v, want only the kept rule: the absent one is deleted", got)
	}
	r := ruleOf(t, c, "r-kept")
	if r.Spec.Prompt != edited.Prompt {
		t.Errorf("prompt %q, want the edit", r.Spec.Prompt)
	}
	if r.Status.LastFire == nil || r.Status.LastFire.EventID != "every:t-kept:5000" ||
		!slices.Equal(eventIDs(r.Status.Queue), []string{"every:t-kept:2"}) {
		t.Errorf("status = %+v, want the kept rule's marks and queue kept", r.Status)
	}
	if st := r.Status.Triggers["t-kept"]; st.ArmedAt != 1_000 || st.LastAt != 5_000 {
		t.Errorf("trigger state %+v, want armed_at 1000 and last_at 5000 kept: a prompt edit keeps the anchor", st)
	}
	if got := stringsOf(t, body["rule_ids"]); !slices.Equal(got, []string{"r-kept"}) {
		t.Errorf("answer rule_ids %v, want [r-kept]", got)
	}
}

func TestCmdBotSave_RearmsAChangedTriggerAndKeepsAnUnchangedOne(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	r := rule.Rule{
		ID: "r-two",
		Spec: rule.Spec{Name: "Two", Prompt: "Look.", Enabled: true, Triggers: []rule.Trigger{
			{ID: "t-a", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 60}},
			{ID: "t-b", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 30}},
		}},
		Status: rule.Status{Triggers: map[string]rule.TriggerState{
			"t-a": {ArmedAt: 1_000, LastAt: 5_000}, "t-b": {ArmedAt: 1_000, LastAt: 6_000},
		}},
	}
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, Rules: []rule.Rule{r}})
	roles, _ := newRuleRoles(t, store)
	spec := r.Spec
	spec.Triggers = []rule.Trigger{
		{ID: "t-a", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 90}},
		{ID: "t-b", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 30}},
		{ID: "new:c", Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: 15}},
	}

	before := time.Now()
	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.", OpID: "op-1",
		Rules: []marotte.BotRuleSave{ruleSave(t, "r-two", spec)},
	})
	after := time.Now()

	st := ruleOf(t, botOf(t, store, "c-bot"), "r-two").Status.Triggers
	armedWindow(t, "the changed trigger", st["t-a"], 1_000, before, after)
	if got := st["t-b"]; got.ArmedAt != 1_000 || got.LastAt != 6_000 {
		t.Errorf("the unchanged trigger's state %+v, want armed_at 1000 and last_at 6000 kept", got)
	}
	added := triggerIDsOf(t, body["trigger_ids"])[0][2]
	armedWindow(t, "the added trigger", st[added], 0, before, after)
}

func TestCmdBotSave_ArmsOnlyWhatCanRun(t *testing.T) {
	cases := []struct {
		bot     marotte.Bot
		name    string
		enabled bool
		armed   bool
	}{
		{name: "a started Bot", bot: marotte.Bot{StartedAt: 1}, enabled: true, armed: true},
		{name: "a disabled rule", bot: marotte.Bot{StartedAt: 1}},
		{name: "a draft", bot: marotte.Bot{}, enabled: true},
		{name: "a paused Bot", bot: marotte.Bot{StartedAt: 1, Paused: true}, enabled: true},
		{name: "a closed Bot", bot: marotte.Bot{StartedAt: 1, ClosedAt: 2}, enabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			b := tc.bot
			b.Goal = "Watch."
			seedBot(t, store, "c-bot", "Watcher", &b)
			roles, _ := newRuleRoles(t, store)
			spec := everyRule("New", "", 30)
			spec.Enabled = tc.enabled

			before := time.Now()
			body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
				Name: "Watcher", Goal: "Watch.", OpID: "op-1",
				Rules: []marotte.BotRuleSave{ruleSave(t, "", spec)},
			})
			after := time.Now()

			c := botOf(t, store, "c-bot")
			tid := triggerIDsOf(t, body["trigger_ids"])[0][0]
			st := c.Bot.Rules[0].Status.Triggers[tid]
			if tc.armed {
				armedWindow(t, "the new trigger", st, 0, before, after)
				return
			}
			if st.ArmedAt != 0 {
				t.Errorf("armed_at = %d, want 0: nothing is armed until the rule can run", st.ArmedAt)
			}
		})
	}
}

func TestCmdBotSave_StartArmsEveryEnabledRuleWithOneStamp(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "New Bot", &marotte.Bot{Rules: []rule.Rule{storedRule("starter", 0, 0)}})
	roles, eng := newRuleRoles(t, store)
	off := everyRule("Off", "", 30)
	off.Enabled = false

	before := time.Now()
	body := saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.", Start: true, OpID: "op-1",
		Rules: []marotte.BotRuleSave{
			ruleSave(t, "r-starter", everyRule("starter", "t-starter", 60)),
			ruleSave(t, "", everyRule("Also", "", 45)),
			ruleSave(t, "", off),
		},
	})
	after := time.Now()

	c := botOf(t, store, "c-bot")
	if c.Bot.StartedAt == 0 {
		t.Fatal("the Bot did not start")
	}
	tids := triggerIDsOf(t, body["trigger_ids"])
	starter := c.Bot.Rules[0].Status.Triggers["t-starter"]
	also := c.Bot.Rules[1].Status.Triggers[tids[1][0]]
	armedWindow(t, "the kept starter rule", starter, 0, before, after)
	armedWindow(t, "the new enabled rule", also, 0, before, after)
	if starter.ArmedAt != also.ArmedAt {
		t.Errorf("armed_at %d and %d, want one stamp for every rule the start arms", starter.ArmedAt, also.ArmedAt)
	}
	if st := c.Bot.Rules[2].Status.Triggers[tids[2][0]]; st.ArmedAt != 0 {
		t.Errorf("the disabled rule's armed_at = %d, want 0", st.ArmedAt)
	}
	if h := c.Header(); h.Bot == nil || h.Bot.NextAt != also.ArmedAt+45*time.Minute.Milliseconds() || h.Bot.NextRule != "Also" {
		t.Errorf("header = %+v, want next_at the earliest armed slot, the 45-minute rule's first, 45 min after the start", h.Bot)
	}
	if got := eng.reindexes(); !slices.Equal(got, []marotte.ChatID{"c-bot"}) {
		t.Errorf("reindexed %v, want c-bot once", got)
	}
}

func TestCmdBotSave_EnablingRearmsAndDisablingDropsTheQueue(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	off := storedRule("off", 1_000, 2_000)
	off.Spec.Enabled = false
	on := storedRule("on", 1_000, 3_000)
	on.Status.Queue = queueOf("t-on")
	on.Status.Dropped = 1
	seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, Rules: []rule.Rule{off, on}})
	roles, _ := newRuleRoles(t, store)
	enable, disable := off.Spec, on.Spec
	enable.Enabled, disable.Enabled = true, false

	before := time.Now()
	saveBot(t, roles, "c-bot", &marotte.BotSaveCommand{
		Name: "Watcher", Goal: "Watch.",
		Rules: []marotte.BotRuleSave{ruleSave(t, "r-off", enable), ruleSave(t, "r-on", disable)},
	})
	after := time.Now()

	c := botOf(t, store, "c-bot")
	armedWindow(t, "the enabled rule", ruleOf(t, c, "r-off").Status.Triggers["t-off"], 1_000, before, after)
	disabled := ruleOf(t, c, "r-on").Status
	if got := eventIDs(disabled.Queue); !slices.Equal(got, []string{"every:t-on:3"}) || disabled.Dropped != 4 {
		t.Errorf("the disabled rule's queue %v dropped %d, want only the event already queued in the chat kept and 3 more dropped",
			got, disabled.Dropped)
	}
}

// rules is the whole list when present and untouched when absent, as supervised is: a save of the Bot's other
// fields alone keeps every rule with its status.
func TestCmdBotSave_ASaveWithNoRulesKeepsThemAndAnEmptyListDeletesThem(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    []string
	}{
		"absent": {payload: `{"name":"Watcher","goal":"New goal","memory":false,"base_version":0}`, want: []string{"r-kept"}},
		"empty":  {payload: `{"name":"Watcher","goal":"New goal","memory":false,"base_version":0,"rules":[]}`, want: []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "Watcher", &marotte.Bot{Goal: "Watch.", StartedAt: 1, Rules: []rule.Rule{storedRule("kept", 1_000, 5_000)}})
			roles, _ := newRuleRoles(t, store)

			if _, err := cmdBotSave(t.Context(), roles, &marotte.ClientCommand{
				Type: marotte.CmdBotSave, ChatID: "c-bot", Payload: json.RawMessage(tc.payload),
			}); err != nil {
				t.Fatalf("bot_save = %d %v, want 200", statusOf(err), err)
			}

			c := botOf(t, store, "c-bot")
			if got := ruleIDsOf(c); !slices.Equal(got, tc.want) || c.Bot.Goal != "New goal" {
				t.Errorf("rules %v goal %q, want %v and the new goal", got, c.Bot.Goal, tc.want)
			}
			if name == "absent" {
				if st := ruleOf(t, c, "r-kept").Status.Triggers["t-kept"]; st.ArmedAt != 1_000 || st.LastAt != 5_000 {
					t.Errorf("trigger state %+v, want it kept", st)
				}
			}
		})
	}
}
