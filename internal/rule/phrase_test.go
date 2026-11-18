package rule

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func everyAnchored(minutes int, anchor Anchor) Trigger {
	t := everyTrigger("t", minutes)
	t.Every.Anchor = anchor
	return t
}

func TestTriggerPhrase_ReadsEachTimeTriggerAsTheEditorSaysIt(t *testing.T) {
	weekly := func(time string, days ...int) Trigger {
		return atTrigger("t", AtTrigger{Days: DaysWeekdays, Weekdays: days, Time: time})
	}
	monthly := func(day int) Trigger {
		return atTrigger("t", AtTrigger{Days: DaysMonthly, MonthDay: day, Time: "18:00"})
	}
	cases := []struct {
		name string
		trig Trigger
		want string
	}{
		{"every, hours", everyAnchored(180, AnchorLast), "every 3 h"},
		{"every, no anchor reads as last", everyAnchored(180, ""), "every 3 h"},
		{"every, idle", everyAnchored(180, AnchorIdle), "3 h after the Bot's last turn"},
		{"every, minutes", everyAnchored(5, AnchorLast), "every 5 min"},
		{"every, hours and minutes", everyAnchored(90, AnchorLast), "every 1 h 30 min"},
		{"every, days", everyAnchored(2880, AnchorLast), "every 2 d"},
		{"every, days and hours", everyAnchored(2160, AnchorLast), "every 1 d 12 h"},
		{"every, the longest", everyAnchored(10080, AnchorIdle), "7 d after the Bot's last turn"},
		{"daily", atTrigger("t", AtTrigger{Days: DaysDaily, Time: "09:00"}), "every day at 09:00"},
		{"Monday to Friday", weekly("09:00", 1, 2, 3, 4, 5), "weekdays at 09:00"},
		{"one weekday", weekly("14:00", 1), "Mondays at 14:00"},
		{"Sunday", weekly("14:00", 0), "Sundays at 14:00"},
		{"all seven days", weekly("09:00", 0, 1, 2, 3, 4, 5, 6), "every day at 09:00"},
		{"a run of three", weekly("09:00", 2, 3, 4), "on Tue to Thu at 09:00"},
		{"a run across Sunday", weekly("09:00", 5, 6, 0), "on Fri to Sun at 09:00"},
		{"the weekend", weekly("10:00", 6, 0), "on Sat, Sun at 10:00"},
		{"scattered days", weekly("08:00", 5, 1, 3), "on Mon, Wed, Fri at 08:00"},
		{"the last day", monthly(MonthDayLast), "on the last day of the month at 18:00"},
		{"the 1st", monthly(1), "on the 1st of the month at 18:00"},
		{"the 2nd", monthly(2), "on the 2nd of the month at 18:00"},
		{"the 3rd", monthly(3), "on the 3rd of the month at 18:00"},
		{"the 11th", monthly(11), "on the 11th of the month at 18:00"},
		{"the 12th", monthly(12), "on the 12th of the month at 18:00"},
		{"the 13th", monthly(13), "on the 13th of the month at 18:00"},
		{"the 15th", monthly(15), "on the 15th of the month at 18:00"},
		{"the 21st", monthly(21), "on the 21st of the month at 18:00"},
		{"the 22nd", monthly(22), "on the 22nd of the month at 18:00"},
		{"the 23rd", monthly(23), "on the 23rd of the month at 18:00"},
		{"the 31st", monthly(31), "on the 31st of the month at 18:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := triggerPhrase(tc.trig); got != tc.want {
				t.Errorf("triggerPhrase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCatalog_ListsTheBuiltKindsWithTheirBoundsAndPhrases(t *testing.T) {
	c := NewCatalog()
	var kinds []TriggerKind
	for _, k := range c.Kinds {
		kinds = append(kinds, k.Kind)
	}
	if want := []TriggerKind{KindEvery, KindAt}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	every, atKind := c.Kinds[0], c.Kinds[1]
	t.Run("every", func(t *testing.T) {
		if every.Group != "Time" || every.Label != "Every few minutes or hours" || !every.Time {
			t.Errorf("group %q label %q time %v", every.Group, every.Label, every.Time)
		}
		if !slices.Equal(every.Fields, []string{"due_at", "anchor"}) || len(every.Lists) != 0 {
			t.Errorf("fields %v lists %v, want [due_at anchor] and none", every.Fields, every.Lists)
		}
		want := map[string]string{"last": "every {minutes}", "idle": "{minutes} after the Bot's last turn"}
		if !maps.Equal(every.Phrases, want) {
			t.Errorf("phrases %v, want %v", every.Phrases, want)
		}
		wantParams := []Param{
			{Name: "minutes", Min: 5, Max: 10080},
			{Name: "anchor", Choices: []string{"last", "idle"}},
		}
		checkParams(t, every.Params, wantParams)
	})
	t.Run("at", func(t *testing.T) {
		if atKind.Group != "Time" || atKind.Label != "At a time of day" || !atKind.Time {
			t.Errorf("group %q label %q time %v", atKind.Group, atKind.Label, atKind.Time)
		}
		if !slices.Equal(atKind.Fields, []string{"due_at"}) || len(atKind.Lists) != 0 {
			t.Errorf("fields %v lists %v, want [due_at] and none", atKind.Fields, atKind.Lists)
		}
		want := map[string]string{
			"daily": "every day at {time}", "weekdays": "{weekdays} at {time}", "monthly": "on {month_day} of the month at {time}",
		}
		if !maps.Equal(atKind.Phrases, want) {
			t.Errorf("phrases %v, want %v", atKind.Phrases, want)
		}
		wantParams := []Param{
			{Name: "days", Choices: []string{"daily", "weekdays", "monthly"}},
			{Name: "time"},
			{Name: "weekdays", Min: 0, Max: 6},
			{Name: "month_day", Min: 1, Max: 31, Named: map[string]int{"last": -1}},
		}
		checkParams(t, atKind.Params, wantParams)
	})
	t.Run("the common fields", func(t *testing.T) {
		if want := []string{"event_id", "trigger", "trigger_name", "rule", "at", "late_by"}; !slices.Equal(c.Common, want) {
			t.Errorf("common %v, want %v", c.Common, want)
		}
	})
}

func checkParams(t *testing.T, got, want []Param) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("params %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Name != w.Name || g.Min != w.Min || g.Max != w.Max || !slices.Equal(g.Choices, w.Choices) || !maps.Equal(g.Named, w.Named) {
			t.Errorf("param %d is %+v, want %+v", i, g, w)
		}
	}
}

func TestCatalog_SamplesAreEventsOfTheirKind(t *testing.T) {
	c := NewCatalog()
	if len(c.Kinds) == 0 {
		t.Fatal("the catalogue lists no kind")
	}
	for _, k := range c.Kinds {
		t.Run(string(k.Kind), func(t *testing.T) {
			var sample map[string]any
			if err := json.Unmarshal(k.Sample, &sample); err != nil {
				t.Fatalf("the sample is no JSON object: %v", err)
			}
			keys := slices.Sorted(maps.Keys(sample))
			want := slices.Sorted(slices.Values(slices.Concat(c.Common, k.Fields)))
			if !slices.Equal(keys, want) {
				t.Errorf("sample fields %v, want %v", keys, want)
			}
			if sample["trigger"] != string(k.Kind) {
				t.Errorf("sample trigger %v, want %s", sample["trigger"], k.Kind)
			}
			if _, err := SampleEvent(Trigger{Kind: k.Kind}, sample); err != nil {
				t.Errorf("the sample is not an event: %v", err)
			}
		})
	}
}

func TestCatalog_ListsTheBuiltStateItems(t *testing.T) {
	c := NewCatalog()
	var leaves []LeafKind
	for _, l := range c.Leaves {
		leaves = append(leaves, l.Kind)
	}
	if want := []LeafKind{LeafBotBusy, LeafBotWaiting, leafTimeWindow, leafEventField, LeafRuleLastRan}; !slices.Equal(leaves, want) {
		t.Fatalf("leaves %v, want %v (triggered_by is the flow's, never an item)", leaves, want)
	}
	want := map[LeafKind]struct {
		group   string
		phrases map[string]string
		params  []Param
	}{
		LeafBotBusy: {"This Bot", map[string]string{"is": "this Bot is working", "not": "this Bot is not working"}, nil},
		LeafBotWaiting: {"This Bot", map[string]string{
			"is": "this Bot is waiting on you", "not": "this Bot is not waiting on you",
		}, nil},
		leafTimeWindow: {"Time", map[string]string{
			"is": "the time is between {from} and {to} {days}", "not": "the time is not between {from} and {to} {days}",
		}, []Param{{Name: "from"}, {Name: "to"}, {Name: "weekdays", Min: 0, Max: 6}}},
		leafEventField: {"What happened", map[string]string{
			"is": "the event's {field} {op}", "not": "the event's {field} {op}", "unknown": "the event's {field} could not be read",
		}, []Param{
			{Name: "field"},
			{Name: "op", Choices: []string{"is", "one_of", "contains", "starts_with", "matches", "above", "below", "is_set"}},
			{Name: "value", Max: 256},
			{Name: "values", Max: 50},
		}},
		LeafRuleLastRan: {"This rule", map[string]string{
			"is": "this rule last ran more than {minutes} ago", "not": "this rule ran in the last {minutes}", "never": "this rule has never run",
		}, []Param{{Name: "minutes", Min: 5, Max: 44640}}},
	}
	for _, l := range c.Leaves {
		t.Run(string(l.Kind), func(t *testing.T) {
			w := want[l.Kind]
			if l.Group != w.group {
				t.Errorf("group %q, want %q", l.Group, w.group)
			}
			if !maps.Equal(l.Phrases, w.phrases) {
				t.Errorf("phrases %v, want %v", l.Phrases, w.phrases)
			}
			checkParams(t, l.Params, w.params)
		})
	}
}

func TestCatalog_ListsEachOperatorWithItsNegative(t *testing.T) {
	want := []OpEntry{
		{Op: fieldIs, Phrase: "is {value}", Negative: "is not {value}", Value: true, List: true},
		{Op: fieldOneOf, Phrase: "is one of {values}", Negative: "is not one of {values}", List: true},
		{Op: fieldContains, Phrase: "contains {value}", Negative: "does not contain {value}", Value: true, List: true},
		{Op: fieldStartsWith, Phrase: "starts with {value}", Negative: "does not start with {value}", Value: true, List: true},
		{Op: fieldMatches, Phrase: "matches {value}", Negative: "does not match {value}", Value: true, List: true},
		{Op: fieldAbove, Phrase: "is above {value}", Negative: "is at most {value}", Value: true},
		{Op: fieldBelow, Phrase: "is below {value}", Negative: "is at least {value}", Value: true},
		{Op: fieldIsSet, Phrase: "is set", Negative: "is not set", List: true},
	}
	if got := NewCatalog().Ops; !slices.Equal(got, want) {
		t.Errorf("ops %+v, want %+v", got, want)
	}
}

func TestCatalog_PublishesTheBoundsAndTheWords(t *testing.T) {
	c := NewCatalog()
	if want := (Bounds{
		MaxRules: 20, MaxTriggers: 8, MaxNameRunes: 80, MaxPromptBytes: 8192, MaxLeaves: 24,
		MaxGroupDepth: 4, MaxGroupChildren: 12, MaxOneOfValues: 50, MaxValueBytes: 256,
	}); c.Rule != want {
		t.Errorf("rule bounds %+v, want %+v", c.Rule, want)
	}
	l := c.Limits
	if !slices.Equal(l.Holds, []HoldPolicy{HoldFIFO, holdNewest}) {
		t.Errorf("holds %v, want [fifo newest]", l.Holds)
	}
	for _, b := range []struct {
		name      string
		got, want int
	}{
		{"min per hour", l.MinPerHour, 1},
		{"max per hour", l.MaxPerHour, 60},
		{"default per hour", l.DefaultPerHour, 12},
		{"max per day", l.MaxPerDay, 1440},
		{"min hold cap", l.MinHoldCap, 1},
		{"max hold cap", l.MaxHoldCap, 25},
		{"default hold cap", l.DefaultHoldCap, 10},
		{"gap minutes", l.GapMinutes, 1},
		{"Bot max per hour", l.BotMaxPerHour, 60},
	} {
		if b.got != b.want {
			t.Errorf("%s %d, want %d", b.name, b.got, b.want)
		}
	}
	want := Words{
		Days:       []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
		DaysPlural: []string{"Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"},
		Minute:     "min", Hour: "h", Day: "d", Weekdays: "weekdays", EveryDay: "every day", LastDay: "the last day",
	}
	if !reflect.DeepEqual(c.Words, want) {
		t.Errorf("words %+v, want %+v", c.Words, want)
	}
}

// fill fills a template's placeholders as the Catalog's documentation says the client does.
func fill(template string, values map[string]string) string {
	for k, v := range values {
		template = strings.ReplaceAll(template, "{"+k+"}", v)
	}
	return template
}

func TestCatalog_TemplatesAreTheServersOwnPhrases(t *testing.T) {
	c := NewCatalog()
	if len(c.Kinds) < 2 {
		t.Fatalf("kinds %v, want every and at", c.Kinds)
	}
	every, atKind := c.Kinds[0], c.Kinds[1]
	cases := []struct {
		trig     Trigger
		template string
		values   map[string]string
	}{
		{everyAnchored(90, AnchorLast), every.Phrases["last"], map[string]string{"minutes": "1 h 30 min"}},
		{everyAnchored(180, AnchorIdle), every.Phrases["idle"], map[string]string{"minutes": "3 h"}},
		{atTrigger("t", AtTrigger{Days: DaysDaily, Time: "07:30"}), atKind.Phrases["daily"], map[string]string{"time": "07:30"}},
		{
			atTrigger("t", AtTrigger{Days: DaysWeekdays, Weekdays: []int{3}, Time: "07:30"}), atKind.Phrases["weekdays"],
			map[string]string{"weekdays": "Wednesdays", "time": "07:30"},
		},
		{
			atTrigger("t", AtTrigger{Days: DaysMonthly, MonthDay: MonthDayLast, Time: "07:30"}), atKind.Phrases["monthly"],
			map[string]string{"month_day": "the last day", "time": "07:30"},
		},
	}
	for _, tc := range cases {
		want := fill(tc.template, tc.values)
		if got := triggerPhrase(tc.trig); got != want {
			t.Errorf("triggerPhrase = %q, the catalogue's template reads %q", got, want)
		}
	}
	reasons := []struct {
		cond  Cond
		snap  func(*Snapshot)
		leaf  LeafKind
		key   string
		value map[string]string
	}{
		{busy(), nil, LeafBotBusy, "not", nil},
		{not(waiting()), func(s *Snapshot) { s.BotWaiting = true }, LeafBotWaiting, "is", nil},
		{lastRan(360), func(s *Snapshot) { s.LastFire = s.Now.Add(-time.Minute).UnixMilli() }, LeafRuleLastRan, "not", map[string]string{"minutes": "6 h"}},
		{not(lastRan(360)), nil, LeafRuleLastRan, "never", nil},
		{
			window("08:00", "09:00", 1, 2, 3, 4, 5), nil, leafTimeWindow, "not",
			map[string]string{"from": "08:00", "to": "09:00", "days": "on Mon to Fri"},
		},
	}
	for _, tc := range reasons {
		s := snap()
		if tc.snap != nil {
			tc.snap(&s)
		}
		var template string
		for _, l := range c.Leaves {
			if l.Kind == tc.leaf {
				template = l.Phrases[tc.key]
			}
		}
		if got, want := eval(tc.cond, s).Reason, fill(template, tc.value); got != want {
			t.Errorf("%s skip reason %q, the catalogue's %s template reads %q", tc.leaf, got, tc.key, want)
		}
	}
}

func TestNewCatalog_AnswersACopy(t *testing.T) {
	a := NewCatalog()
	if len(a.Kinds) == 0 || len(a.Kinds[0].Fields) == 0 || a.Kinds[0].Phrases == nil || len(a.Leaves) == 0 || a.Leaves[0].Phrases == nil {
		t.Fatalf("an empty catalogue: %+v", a)
	}
	a.Kinds[0].Phrases["last"] = "changed"
	a.Kinds[0].Fields[0] = "changed"
	a.Leaves[0].Phrases["is"] = "changed"
	b := NewCatalog()
	if b.Kinds[0].Phrases["last"] == "changed" || b.Kinds[0].Fields[0] == "changed" || b.Leaves[0].Phrases["is"] == "changed" {
		t.Error("a change to one catalogue reached the next")
	}
}

func TestEventPhrase(t *testing.T) {
	spec := Spec{Triggers: []Trigger{{ID: "t1", Kind: KindEvery, Every: &EveryTrigger{Minutes: 90}}}}
	for _, c := range []struct{ trigger, want string }{
		{trigger: "t1", want: "every 1 h 30 min"},
		{trigger: "", want: "Run now"},
		{trigger: "gone", want: "gone"},
	} {
		if got := EventPhrase(&spec, c.trigger); got != c.want {
			t.Errorf("EventPhrase(%q) = %q, want %q", c.trigger, got, c.want)
		}
	}
}
