package rule

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const (
	sentenceNotOp       = "Use the item's own negative, such as “is off” or “does not match”"
	sentenceTriggeredBy = "“Triggered by” only leads a branch"
	sentenceBotTooLarge = `This Bot's rules could hold more waiting events than a Bot can store. Lower "keep up to" on a trigger.`
)

func validSpec() Spec {
	return Spec{
		Name: "Check in", Prompt: "Look at the open pull requests.", Enabled: true,
		Triggers: []Trigger{everyTrigger("new:a", 60)},
	}
}

func eventSpec(triggers ...Trigger) Spec {
	s := validSpec()
	s.Triggers = triggers
	return s
}

func fieldsOf(errs []FieldError) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Field
	}
	return out
}

// wantOnly asserts errs is exactly one error, at path, with a sentence.
func wantOnly(t *testing.T, errs []FieldError, path string) FieldError {
	t.Helper()
	if len(errs) != 1 || errs[0].Field != path {
		t.Fatalf("errors at %q, want exactly one at %q (%v)", fieldsOf(errs), path, errs)
	}
	if strings.TrimSpace(errs[0].Error) == "" {
		t.Fatalf("the error at %q has no sentence", path)
	}
	return errs[0]
}

func wantNone(t *testing.T, errs []FieldError) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("errors %v, want none", errs)
	}
}

func TestValidate_AcceptsEachBuiltKindAtItsBounds(t *testing.T) {
	cases := map[string]Trigger{
		"every at the floor":       everyTrigger("new:a", MinEveryMinutes),
		"every at a week":          everyTrigger("new:a", MaxEveryMinutes),
		"every with no anchor":     {ID: "new:a", Kind: KindEvery, Every: &EveryTrigger{Minutes: 60}},
		"every idle":               {ID: "new:a", Kind: KindEvery, Every: &EveryTrigger{Minutes: 60, Anchor: AnchorIdle}},
		"at daily":                 atTrigger("new:a", AtTrigger{Days: DaysDaily, Time: "09:00"}),
		"at midnight":              atTrigger("new:a", AtTrigger{Days: DaysDaily, Time: "00:00"}),
		"at the last minute":       atTrigger("new:a", AtTrigger{Days: DaysDaily, Time: "23:59"}),
		"at weekdays":              atTrigger("new:a", AtTrigger{Days: DaysWeekdays, Weekdays: []int{0, 1, 6}, Time: "14:00"}),
		"at monthly on the 31st":   atTrigger("new:a", AtTrigger{Days: DaysMonthly, MonthDay: 31, Time: "18:00"}),
		"at monthly on the 1st":    atTrigger("new:a", AtTrigger{Days: DaysMonthly, MonthDay: 1, Time: "18:00"}),
		"at monthly last day":      atTrigger("new:a", AtTrigger{Days: DaysMonthly, MonthDay: MonthDayLast, Time: "18:00"}),
		"a trigger with no id yet": everyTrigger("", 60),
	}
	for name, trig := range cases {
		t.Run(name, func(t *testing.T) {
			wantNone(t, Validate(eventSpec(trig), ValidateContext{}))
		})
	}
}

func TestValidate_RefusesTextOutOfBounds(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Spec)
		path string
	}{
		{"an empty name", func(s *Spec) { s.Name = "" }, "name"},
		{"a blank name", func(s *Spec) { s.Name = "  \t" }, "name"},
		{"a name of 81 characters", func(s *Spec) { s.Name = strings.Repeat("é", 81) }, "name"},
		{"a name on two lines", func(s *Spec) { s.Name = "Check\nin" }, "name"},
		{"a name with a control character", func(s *Spec) { s.Name = "Check\x07in" }, "name"},
		{"a name that is not UTF-8", func(s *Spec) { s.Name = "Check \xff" }, "name"},
		{"an empty prompt", func(s *Spec) { s.Prompt = "" }, "prompt"},
		{"a prompt past 8 KiB", func(s *Spec) { s.Prompt = strings.Repeat("a", maxPromptBytes+1) }, "prompt"},
		{"a prompt with a control character", func(s *Spec) { s.Prompt = "Look\x1b[2J" }, "prompt"},
		{"a prompt that is not UTF-8", func(s *Spec) { s.Prompt = "Look \xc3" }, "prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			tc.edit(&s)
			wantOnly(t, Validate(s, ValidateContext{}), tc.path)
		})
	}
}

func TestValidate_AcceptsTextAtItsBounds(t *testing.T) {
	s := validSpec()
	s.Name = strings.Repeat("é", maxNameRunes)
	s.Prompt = strings.Repeat("a", maxPromptBytes-2) + "\n\t"
	wantNone(t, Validate(s, ValidateContext{}))
	s.Prompt = "x"
	wantNone(t, Validate(s, ValidateContext{}))
}

func TestValidate_RefusesATriggerCountOutsideOneToEight(t *testing.T) {
	s := validSpec()
	s.Triggers = nil
	wantOnly(t, Validate(s, ValidateContext{}), "triggers")
	s.Triggers = make([]Trigger, maxTriggers+1)
	for i := range s.Triggers {
		s.Triggers[i] = everyTrigger("", 60)
	}
	wantOnly(t, Validate(s, ValidateContext{}), "triggers")
	s.Triggers = s.Triggers[:maxTriggers]
	wantNone(t, Validate(s, ValidateContext{}))
}

func TestValidate_RefusesATriggerOutsideItsKindsBounds(t *testing.T) {
	every := func(e EveryTrigger) Trigger { return Trigger{ID: "new:a", Kind: KindEvery, Every: &e} }
	cases := []struct {
		name string
		trig Trigger
		path string
	}{
		{"an unknown kind", Trigger{ID: "new:a", Kind: "cron"}, "triggers[0].kind"},
		{"a kind not built yet", Trigger{ID: "new:a", Kind: "pr_opened"}, "triggers[0].kind"},
		{"every with no every field", Trigger{ID: "new:a", Kind: KindEvery}, "triggers[0].every"},
		{"at with no at field", Trigger{ID: "new:a", Kind: KindAt}, "triggers[0].at"},
		{"every carrying an at field", Trigger{
			ID: "new:a", Kind: KindEvery, Every: &EveryTrigger{Minutes: 60},
			At: &AtTrigger{Days: DaysDaily, Time: "09:00"},
		}, "triggers[0].at"},
		{"at carrying an every field", Trigger{
			ID: "new:a", Kind: KindAt, At: &AtTrigger{Days: DaysDaily, Time: "09:00"},
			Every: &EveryTrigger{Minutes: 60},
		}, "triggers[0].every"},
		{"every below 5 minutes", every(EveryTrigger{Minutes: MinEveryMinutes - 1}), "triggers[0].every.minutes"},
		{"every past a week", every(EveryTrigger{Minutes: MaxEveryMinutes + 1}), "triggers[0].every.minutes"},
		{"every with an unknown anchor", every(EveryTrigger{Minutes: 60, Anchor: "midnight"}), "triggers[0].every.anchor"},
		{"at with an unknown days", atTrigger("new:a", AtTrigger{Days: "yearly", Time: "09:00"}), "triggers[0].at.days"},
		{"at at 24:00", atTrigger("new:a", AtTrigger{Days: DaysDaily, Time: "24:00"}), "triggers[0].at.time"},
		{"at at 09:60", atTrigger("new:a", AtTrigger{Days: DaysDaily, Time: "09:60"}), "triggers[0].at.time"},
		{"at at 9:00", atTrigger("new:a", AtTrigger{Days: DaysDaily, Time: "9:00"}), "triggers[0].at.time"},
		{"at with no time", atTrigger("new:a", AtTrigger{Days: DaysDaily}), "triggers[0].at.time"},
		{"at weekdays with none", atTrigger("new:a", AtTrigger{Days: DaysWeekdays, Time: "09:00"}), "triggers[0].at.weekdays"},
		{"at weekdays with day 7", atTrigger("new:a", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1, 7}, Time: "09:00"}), "triggers[0].at.weekdays"},
		{"at weekdays with day -1", atTrigger("new:a", AtTrigger{Days: DaysWeekdays, Weekdays: []int{-1}, Time: "09:00"}), "triggers[0].at.weekdays"},
		{"at weekdays twice the same day", atTrigger("new:a", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1, 1}, Time: "09:00"}), "triggers[0].at.weekdays"},
		{"at daily with weekdays", atTrigger("new:a", AtTrigger{Days: DaysDaily, Weekdays: []int{1}, Time: "09:00"}), "triggers[0].at.weekdays"},
		{"at weekdays with a month day", atTrigger("new:a", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1}, MonthDay: 3, Time: "09:00"}), "triggers[0].at.month_day"},
		{"at monthly on day 0", atTrigger("new:a", AtTrigger{Days: DaysMonthly, Time: "09:00"}), "triggers[0].at.month_day"},
		{"at monthly on day 32", atTrigger("new:a", AtTrigger{Days: DaysMonthly, MonthDay: 32, Time: "09:00"}), "triggers[0].at.month_day"},
		{"at monthly on day -2", atTrigger("new:a", AtTrigger{Days: DaysMonthly, MonthDay: -2, Time: "09:00"}), "triggers[0].at.month_day"},
		{"at monthly with weekdays", atTrigger("new:a", AtTrigger{Days: DaysMonthly, MonthDay: 1, Weekdays: []int{1}, Time: "09:00"}), "triggers[0].at.weekdays"},
		{"every carrying limits", Trigger{
			ID: "new:a", Kind: KindEvery, Every: &EveryTrigger{Minutes: 60},
			Limits: &TriggerLimits{MaxPerHour: 12, HoldCap: 10},
		}, "triggers[0].limits"},
		{"at carrying limits", Trigger{
			ID: "new:a", Kind: KindAt, At: &AtTrigger{Days: DaysDaily, Time: "09:00"},
			Limits: &TriggerLimits{MaxPerHour: 12, HoldCap: 10},
		}, "triggers[0].limits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantOnly(t, Validate(eventSpec(tc.trig), ValidateContext{}), tc.path)
		})
	}
}

func TestValidate_AnEventTriggerNeedsLimitsInRange(t *testing.T) {
	withTestEventKind(t)
	lim := func(edit func(*TriggerLimits)) Trigger {
		tr := eventTrigger("new:a", 10)
		edit(tr.Limits)
		return tr
	}
	cases := []struct {
		name string
		trig Trigger
		path string
	}{
		{"no limits", Trigger{ID: "new:a", Kind: testEventKind}, "triggers[0].limits"},
		{"max per hour 0", lim(func(l *TriggerLimits) { l.MaxPerHour = 0 }), "triggers[0].limits.max_per_hour"},
		{"max per hour 61", lim(func(l *TriggerLimits) { l.MaxPerHour = maxPerHour + 1 }), "triggers[0].limits.max_per_hour"},
		{"max per day -1", lim(func(l *TriggerLimits) { l.MaxPerDay = -1 }), "triggers[0].limits.max_per_day"},
		{"max per day 1441", lim(func(l *TriggerLimits) { l.MaxPerDay = maxPerDay + 1 }), "triggers[0].limits.max_per_day"},
		{"hold cap 0", lim(func(l *TriggerLimits) { l.HoldCap = 0 }), "triggers[0].limits.hold_cap"},
		{"hold cap 26", lim(func(l *TriggerLimits) { l.HoldCap = maxHoldCap + 1 }), "triggers[0].limits.hold_cap"},
		{"an unknown hold", lim(func(l *TriggerLimits) { l.Hold = "oldest" }), "triggers[0].limits.hold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantOnly(t, Validate(eventSpec(tc.trig), ValidateContext{}), tc.path)
		})
	}
	t.Run("every bound itself", func(t *testing.T) {
		for _, l := range []TriggerLimits{
			{MaxPerHour: minPerHour, HoldCap: minHoldCap},
			{MaxPerHour: maxPerHour, MaxPerDay: maxPerDay, HoldCap: maxHoldCap, Hold: holdNewest},
			{MaxPerHour: 12, MaxPerDay: 1, HoldCap: 10, Hold: HoldFIFO},
		} {
			wantNone(t, Validate(eventSpec(Trigger{ID: "new:a", Kind: testEventKind, Limits: &l}), ValidateContext{}))
		}
	})
}

func TestValidate_TriggerIDsAreTheStoredRulesOrPlaceholders(t *testing.T) {
	kept := ValidateContext{Kept: map[string]bool{"T1": true}}
	cases := []struct {
		name string
		ids  []string
		ctx  ValidateContext
		path string
	}{
		{"an id on a new rule", []string{"T1"}, ValidateContext{}, "triggers[0].id"},
		{"an id the kept rule never had", []string{"T9"}, kept, "triggers[0].id"},
		{"an empty placeholder", []string{NewIDPrefix}, ValidateContext{}, "triggers[0].id"},
		{"a placeholder past 16 characters", []string{NewIDPrefix + strings.Repeat("a", 17)}, ValidateContext{}, "triggers[0].id"},
		{"a placeholder with a space", []string{NewIDPrefix + "a b"}, ValidateContext{}, "triggers[0].id"},
		{"one id twice", []string{"new:a", "new:a"}, ValidateContext{}, "triggers[1].id"},
		{"a kept id twice", []string{"T1", "T1"}, kept, "triggers[1].id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			s.Triggers = nil
			for _, id := range tc.ids {
				s.Triggers = append(s.Triggers, everyTrigger(id, 60))
			}
			wantOnly(t, Validate(s, tc.ctx), tc.path)
		})
	}
	t.Run("a kept id and a placeholder", func(t *testing.T) {
		s := eventSpec(everyTrigger("T1", 60), everyTrigger(NewIDPrefix+"b-2_x", 30), everyTrigger("", 15))
		wantNone(t, Validate(s, kept))
	})
}

func TestValidate_RefusesANotGroupWithTheNegativeSentence(t *testing.T) {
	s := validSpec()
	c := all(busy(), Cond{Op: "not", Children: []Cond{busy()}})
	s.Condition = &c
	e := wantOnly(t, Validate(s, ValidateContext{}), "condition.children[1].op")
	if e.Error != sentenceNotOp {
		t.Errorf("error %q, want %q", e.Error, sentenceNotOp)
	}
}

func TestValidate_RefusesAMalformedNode(t *testing.T) {
	cases := []struct {
		name string
		cond Cond
		path string
	}{
		{"an unknown join", Cond{Op: "xor", Children: busyN(2)}, "condition.op"},
		{"a group and a leaf at once", Cond{Op: OpAll, Children: busyN(1), Leaf: &Leaf{Kind: LeafBotBusy}}, "condition.leaf"},
		{"neither a group nor a leaf", Cond{}, "condition"},
		{"a leaf with children", Cond{Leaf: &Leaf{Kind: LeafBotBusy}, Children: busyN(1)}, "condition.children"},
		{"an empty group", all(), "condition.children"},
		{"a group of 13", all(busyN(maxGroupChildren + 1)...), "condition.children"},
		{"an empty nested group", all(busy(), anyOf()), "condition.children[1].children"},
		{"an unknown leaf kind", leaf(Leaf{Kind: "moon_phase"}), "condition.leaf.kind"},
		{"a leaf kind not built yet", leaf(Leaf{Kind: "in_overage"}), "condition.leaf.kind"},
		{"a leaf with another kind's payload", leaf(Leaf{Kind: LeafBotBusy, Rule: &LastRanLeaf{Minutes: 60}}), "condition.leaf.rule"},
		{"rule_last_ran with no payload", leaf(Leaf{Kind: LeafRuleLastRan}), "condition.leaf.rule"},
		{"rule_last_ran below 5 minutes", leaf(Leaf{Kind: LeafRuleLastRan, Rule: &LastRanLeaf{Minutes: minLastRanMinutes - 1}}), "condition.leaf.rule.minutes"},
		{"rule_last_ran past 31 days", leaf(Leaf{Kind: LeafRuleLastRan, Rule: &LastRanLeaf{Minutes: maxLastRanMinutes + 1}}), "condition.leaf.rule.minutes"},
		{"time_window with no weekday", leaf(Leaf{Kind: leafTimeWindow, Time: &TimeLeaf{From: "08:00", To: "20:00"}}), "condition.leaf.time.weekdays"},
		{"time_window with day 7", leaf(Leaf{Kind: leafTimeWindow, Time: &TimeLeaf{Weekdays: []int{7}, From: "08:00", To: "20:00"}}), "condition.leaf.time.weekdays"},
		{"time_window with a bad from", leaf(Leaf{Kind: leafTimeWindow, Time: &TimeLeaf{Weekdays: []int{1}, From: "8", To: "20:00"}}), "condition.leaf.time.from"},
		{"time_window with a bad to", leaf(Leaf{Kind: leafTimeWindow, Time: &TimeLeaf{Weekdays: []int{1}, From: "08:00", To: "20:61"}}), "condition.leaf.time.to"},
		{"time_window of no length", leaf(Leaf{Kind: leafTimeWindow, Time: &TimeLeaf{Weekdays: []int{1}, From: "08:00", To: "08:00"}}), "condition.leaf.time.to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			s.Condition = &tc.cond
			wantOnly(t, Validate(s, ValidateContext{}), tc.path)
		})
	}
	t.Run("every built leaf at its bounds", func(t *testing.T) {
		s := validSpec()
		c := all(
			busy(), leaf(Leaf{Kind: LeafBotWaiting, Not: true}),
			leaf(Leaf{Kind: LeafRuleLastRan, Rule: &LastRanLeaf{Minutes: minLastRanMinutes}}),
			leaf(Leaf{Kind: LeafRuleLastRan, Not: true, Rule: &LastRanLeaf{Minutes: maxLastRanMinutes}}),
			leaf(Leaf{Kind: leafTimeWindow, Time: &TimeLeaf{Weekdays: []int{0, 6}, From: "20:00", To: "08:00"}}),
			field("due_at", fieldIsSet, ""),
		)
		s.Condition = &c
		wantNone(t, Validate(s, ValidateContext{}))
	})
}

func TestValidate_CountsLeavesAndDepthAsStored(t *testing.T) {
	two := eventSpec(everyTrigger("new:a", 60), everyTrigger("new:b", 30))
	cases := []struct {
		name string
		spec Spec
		cond Cond
		path string // "" validates
	}{
		{"24 leaves", validSpec(), all(all(busyN(12)...), all(busyN(12)...)), ""},
		{"25 leaves", validSpec(), all(all(busyN(12)...), all(busyN(12)...), busy()), "condition.children[2]"},
		{"groups to depth 4", validSpec(), all(all(all(all(all(busy()))))), ""},
		{"a group at depth 5", validSpec(), all(all(all(all(all(all(busy())))))), "condition.children[0].children[0].children[0].children[0].children[0]"},
		{
			"two branches with groups at depths 2, 3 and 4", two,
			anyOf(
				all(by("new:a"), all(busy(), all(busy(), all(busy())))),
				all(by("new:b"), anyOf(busy(), all(busy(), anyOf(busy())))),
			), "",
		},
		{
			"a branch's group at depth 5", two,
			anyOf(
				all(by("new:a"), busy()),
				all(by("new:b"), all(all(all(all(busy()))))),
			), "condition.children[1].children[1].children[0].children[0].children[0]",
		},
		{
			"triggered_by leaves count toward 24", two,
			anyOf(all(append([]Cond{by("new:a")}, busyN(11)...)...), all(append(append([]Cond{by("new:b")}, busyN(10)...), all(busy()))...)), "",
		},
		{
			"the 25th leaf with triggered_by counted", two,
			anyOf(all(append([]Cond{by("new:a")}, busyN(11)...)...), all(append(append([]Cond{by("new:b")}, busyN(10)...), all(busy(), busy()))...)),
			"condition.children[1].children[11].children[1]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spec
			s.Condition = &tc.cond
			errs := Validate(s, ValidateContext{})
			if tc.path == "" {
				wantNone(t, errs)
				return
			}
			wantOnly(t, errs, tc.path)
		})
	}
}

func TestValidate_TriggeredByOnlyLeadsABranch(t *testing.T) {
	three := eventSpec(everyTrigger("new:a", 60), everyTrigger("new:b", 30), everyTrigger("new:c", 15))
	two := eventSpec(everyTrigger("new:a", 60), everyTrigger("new:b", 30))
	cases := []struct {
		name     string
		spec     Spec
		cond     Cond
		path     string
		sentence string
	}{
		{"as the root", two, by("new:a"), "condition", sentenceTriggeredBy},
		{"in an all root", two, all(by("new:a"), busy()), "condition.children[0]", sentenceTriggeredBy},
		{"in a nested group", two, anyOf(all(by("new:a"), all(by("new:b"))), all(by("new:b"))), "condition.children[0].children[1].children[0]", sentenceTriggeredBy},
		{"not first in its branch", two, anyOf(all(busy(), by("new:a")), all(by("new:b"))), "condition.children[0].children[1]", sentenceTriggeredBy},
		{"beside a child that is not a branch", two, anyOf(all(by("new:a"), busy()), busy()), "condition.children[0].children[0]", sentenceTriggeredBy},
		{"branches that overlap", two, anyOf(all(by("new:a", "new:b")), all(by("new:b"))), "condition.children[1].children[0].leaf.by.triggers[0]", sentenceTriggeredBy},
		{"branches that miss a trigger", three, anyOf(all(by("new:a")), all(by("new:b"))), "condition", sentenceTriggeredBy},
		{"naming an absent trigger", two, anyOf(all(by("new:a")), all(by("new:b", "new:z"))), "condition.children[1].children[0].leaf.by.triggers[1]", ""},
		{"naming no trigger", two, anyOf(all(by("new:a", "new:b")), all(by())), "condition.children[1].children[0].leaf.by.triggers", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spec
			s.Condition = &tc.cond
			e := wantOnly(t, Validate(s, ValidateContext{}), tc.path)
			if tc.sentence != "" && e.Error != tc.sentence {
				t.Errorf("error %q, want %q", e.Error, tc.sentence)
			}
		})
	}
	t.Run("the canonical forms validate", func(t *testing.T) {
		for _, c := range []Cond{
			anyOf(all(by("new:a"), busy()), all(by("new:b", "new:c"))),
			anyOf(all(by("new:a", "new:b", "new:c"), busy())),
			anyOf(all(by("new:c")), all(by("new:a")), all(by("new:b"), anyOf(busy(), busy()))),
		} {
			s := three
			s.Condition = &c
			wantNone(t, Validate(s, ValidateContext{}))
		}
	})
}

func TestValidate_EventFieldLeaves(t *testing.T) {
	withTestEventKind(t)
	values := func(n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strings.Repeat("v", size-1) + string(rune('a'+i%26))
		}
		return out
	}
	oneOf := func(vs []string) Cond {
		return leaf(Leaf{Kind: leafEventField, Event: &EventLeaf{Field: "author", Op: fieldOneOf, Values: vs}})
	}
	scoped := func(name, trig string) Cond {
		return leaf(Leaf{Kind: leafEventField, Not: true, Event: &EventLeaf{Field: name, Op: fieldIs, Value: "renovate[bot]", Trigger: trig}})
	}
	both := eventSpec(eventTrigger("new:e", 10), everyTrigger("new:t", 60))
	cases := []struct {
		name string
		spec Spec
		cond Cond
		path string // "" validates
	}{
		{"one_of with 50 values of 256 bytes", both, oneOf(values(maxOneOfValues, maxValueBytes)), ""},
		{"one_of with 51 values", both, oneOf(values(maxOneOfValues+1, 8)), "condition.leaf.event.values"},
		{"one_of with a value past 256 bytes", both, oneOf([]string{"a", strings.Repeat("v", maxValueBytes+1)}), "condition.leaf.event.values[1]"},
		{"one_of with no values", both, oneOf(nil), "condition.leaf.event.values"},
		{"one_of with a value", both, leaf(Leaf{Kind: leafEventField, Event: &EventLeaf{Field: "author", Op: fieldOneOf, Value: "a", Values: []string{"b"}}}), "condition.leaf.event.value"},
		{"is with values", both, leaf(Leaf{Kind: leafEventField, Event: &EventLeaf{Field: "author", Op: fieldIs, Value: "a", Values: []string{"b"}}}), "condition.leaf.event.values"},
		{"is with a value past 256 bytes", both, field("author", fieldIs, strings.Repeat("v", maxValueBytes+1)), "condition.leaf.event.value"},
		{"is_set with a value", both, field("author", fieldIsSet, "x"), "condition.leaf.event.value"},
		{"an unknown operator", both, field("author", "near", "x"), "condition.leaf.event.op"},
		{"above with a word", both, field("number", fieldAbove, "many"), "condition.leaf.event.value"},
		{"below with a number", both, field("number", fieldBelow, "12.5"), ""},
		{"a field no trigger has", both, field("branch", fieldIs, "main"), "condition.leaf.event.field"},
		{"a field only the time trigger has", both, field("due_at", fieldIsSet, ""), ""},
		{"a common field", both, field("trigger_name", fieldContains, "PR"), ""},
		{"above on a list field", both, field("labels", fieldAbove, "3"), "condition.leaf.event.op"},
		{"below on a list field", both, field("labels", fieldBelow, "3"), "condition.leaf.event.op"},
		{"matches on a list field", both, field("labels", fieldMatches, "report:*"), ""},
		{"scoped to its trigger", both, scoped("author", "new:e"), ""},
		{"scoped to a trigger without the field", both, scoped("author", "new:t"), "condition.leaf.event.field"},
		{"scoped to an absent trigger", both, scoped("author", "new:z"), "condition.leaf.event.trigger"},
		{"no field", both, field("", fieldIsSet, ""), "condition.leaf.event.field"},
		{"no payload", both, leaf(Leaf{Kind: leafEventField}), "condition.leaf.event"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spec
			s.Condition = &tc.cond
			errs := Validate(s, ValidateContext{})
			if tc.path == "" {
				wantNone(t, errs)
				return
			}
			wantOnly(t, errs, tc.path)
		})
	}
}

func TestValidate_AnswersEveryErrorAtOnce(t *testing.T) {
	s := validSpec()
	s.Name = ""
	s.Triggers = append(s.Triggers, everyTrigger("new:b", 2), atTrigger("new:c", AtTrigger{Days: DaysDaily, Time: "25:00"}))
	c := all(busy(), Cond{Op: "not", Children: busyN(1)})
	s.Condition = &c
	got := fieldsOf(Validate(s, ValidateContext{}))
	want := []string{"name", "triggers[1].every.minutes", "triggers[2].at.time", "condition.children[1].op"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("errors at %q, missing %q", got, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("errors at %q, want exactly %q", got, want)
	}
}

func TestDecode_RefusesServerAndUnknownFieldsAtTheirPaths(t *testing.T) {
	const good = `{"name":"Check in","prompt":"Look.","enabled":true,"triggers":[{"id":"new:a","kind":"every","every":{"minutes":60,"anchor":"last"}}]}`
	t.Run("a valid spec decodes", func(t *testing.T) {
		got, errs := Decode([]byte(good))
		wantNone(t, errs)
		if got.Name != "Check in" || got.Prompt != "Look." || !got.Enabled || len(got.Triggers) != 1 ||
			got.Triggers[0].Every == nil || got.Triggers[0].Every.Minutes != 60 || got.Triggers[0].ID != "new:a" {
			t.Errorf("decoded %+v", got)
		}
	})
	cases := []struct {
		name   string
		body   string
		paths  []string
		server bool
	}{
		{"a status", `{"name":"x","prompt":"y","triggers":[],"status":{"dropped":3}}`, []string{"status"}, true},
		{"an id", `{"id":"r1","name":"x","prompt":"y","triggers":[]}`, []string{"id"}, true},
		{"a hook id on a trigger", `{"name":"x","prompt":"y","triggers":[{"kind":"every","every":{"minutes":60}},{"kind":"every","hook_id":"h","every":{"minutes":60}}]}`, []string{"triggers[1].hook_id"}, true},
		{"a secret on a trigger", `{"name":"x","prompt":"y","triggers":[{"kind":"every","secret":"s","every":{"minutes":60}}]}`, []string{"triggers[0].secret"}, true},
		{"a typo in a kind's field", `{"name":"x","prompt":"y","triggers":[{"kind":"every","every":{"minute":60}}]}`, []string{"triggers[0].every.minute"}, false},
		{"the gap that left the limits", `{"name":"x","prompt":"y","triggers":[{"kind":"x","limits":{"max_per_hour":1,"hold_cap":1,"min_gap_min":5}}]}`, []string{"triggers[0].limits.min_gap_min"}, false},
		{"an op on rule_last_ran", `{"name":"x","prompt":"y","triggers":[],"condition":{"leaf":{"kind":"rule_last_ran","rule":{"minutes":60,"op":"above"}}}}`, []string{"condition.leaf.rule.op"}, false},
		{"a field deep in the tree", `{"name":"x","prompt":"y","triggers":[],"condition":{"op":"all","children":[{"leaf":{"kind":"bot_busy"}},{"leaf":{"kind":"bot_busy","colour":"red"}}]}}`, []string{"condition.children[1].leaf.colour"}, false},
		{"two at once", `{"name":"x","prompt":"y","triggers":[{"kind":"every","every":{"minutes":60,"phase":1}}],"status":{}}`, []string{"status", "triggers[0].every.phase"}, true},
		{"a wrong type", `{"name":"x","prompt":"y","triggers":[{"kind":"every","every":{"minutes":"60"}}]}`, []string{"triggers[0].every.minutes"}, false},
		{"a list where an object goes", `{"name":"x","prompt":"y","triggers":[{"kind":"every","every":[60]}]}`, []string{"triggers[0].every"}, false},
		{"not JSON", `{"name":`, []string{""}, false},
		{"not an object", `[1]`, []string{""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := Decode([]byte(tc.body))
			if got := fieldsOf(errs); !slices.Equal(got, tc.paths) {
				t.Fatalf("errors at %q, want %q (%v)", got, tc.paths, errs)
			}
			if tc.server && !strings.Contains(errs[0].Error, "server") {
				t.Errorf("error %q does not say the server writes the field", errs[0].Error)
			}
			for _, e := range errs {
				if strings.TrimSpace(e.Error) == "" {
					t.Errorf("the error at %q has no sentence", e.Field)
				}
			}
		})
	}
}

func TestDecode_RoundTripsWhatItDecodes(t *testing.T) {
	s := eventSpec(
		Trigger{ID: "T1", Kind: KindEvery, Every: &EveryTrigger{Minutes: 90, Anchor: AnchorIdle}},
		atTrigger("new:b", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1, 5}, Time: "09:30"}),
	)
	c := anyOf(
		all(by("T1"), leaf(Leaf{Kind: leafTimeWindow, Not: true, Time: &TimeLeaf{Weekdays: []int{0}, From: "12:00", To: "23:59"}})),
		all(by("new:b"), leaf(Leaf{Kind: leafEventField, Event: &EventLeaf{Field: "due_at", Op: fieldOneOf, Values: []string{"x", "y"}, Trigger: "new:b"}})),
	)
	s.Condition = &c
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got, errs := Decode(data)
	wantNone(t, errs)
	again, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("round trip\n got %s\nwant %s", again, data)
	}
}

func TestValidateBot_RefusesMoreThanTwentyRules(t *testing.T) {
	specs := make([]Spec, MaxRules+1)
	for i := range specs {
		specs[i] = validSpec()
	}
	wantOnly(t, ValidateBot(sent(specs...)), "rules")
	wantNone(t, ValidateBot(sent(specs[:MaxRules]...)))
	wantOnly(t, ValidateBot(append(sent(specs[:MaxRules]...), nil)), "rules")
}

// sent is specs as a save sends them, every one decoded.
func sent(specs ...Spec) []*Spec {
	out := make([]*Spec, len(specs))
	for i := range specs {
		out[i] = &specs[i]
	}
	return out
}

func TestValidateBot_BoundsTheWorstCaseSize(t *testing.T) {
	withTestEventKind(t)
	oneTrigger := func(caps ...int) []Spec {
		out := make([]Spec, len(caps))
		for i, c := range caps {
			out[i] = eventSpec(eventTrigger("new:a", c))
		}
		return out
	}
	repeat := func(n, c int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = c
		}
		return out
	}
	t.Run("twenty one-trigger rules at the default cap fit", func(t *testing.T) {
		// 20 × (10 × 12 + 12 + 22 + 29) KiB + 12 KiB = 3,672 KiB, under 4 MiB with these specs.
		wantNone(t, ValidateBot(sent(oneTrigger(repeat(20, 10)...)...)))
	})
	t.Run("twenty at cap 12 do not, named at the largest cap", func(t *testing.T) {
		// 20 × (144 + 63) KiB + 12 KiB = 4,152 KiB.
		caps := repeat(20, 12)
		caps[7] = 13
		e := wantOnly(t, ValidateBot(sent(oneTrigger(caps...)...)), "rules[7].triggers[0].limits.hold_cap")
		if e.Error != sentenceBotTooLarge {
			t.Errorf("error %q, want %q", e.Error, sentenceBotTooLarge)
		}
	})
	t.Run("a tie names the first", func(t *testing.T) {
		wantOnly(t, ValidateBot(sent(oneTrigger(repeat(20, 12)...)...)), "rules[0].triggers[0].limits.hold_cap")
	})
	t.Run("a rule that did not decode keeps every later rule's index", func(t *testing.T) {
		specs := sent(oneTrigger(repeat(20, 13)...)...)
		specs[0] = nil
		wantOnly(t, ValidateBot(specs), "rules[1].triggers[0].limits.hold_cap")
	})
	t.Run("a rule with several event triggers counts each cap", func(t *testing.T) {
		// Each rule: 7 × (288 + 29) + (300 + 29) + 12 + 22 = 2,582 KiB; two rules and the Bot's 12 KiB pass 4 MiB.
		rule := func() Spec {
			var ts []Trigger
			for i := range 8 {
				c := 24
				if i == 5 {
					c = 25
				}
				ts = append(ts, eventTrigger(NewIDPrefix+string(rune('a'+i)), c))
			}
			return eventSpec(ts...)
		}
		wantOnly(t, ValidateBot(sent(rule(), rule())), "rules[0].triggers[5].limits.hold_cap")
		wantNone(t, ValidateBot(sent(rule())))
	})
	t.Run("time triggers alone can pass the bound", func(t *testing.T) {
		// 20 × (8 × (12 + 29) + 12 + 22) KiB + 12 KiB = 7,252 KiB, with no hold cap to lower.
		specs := make([]Spec, MaxRules)
		for i := range specs {
			var ts []Trigger
			for range maxTriggers {
				ts = append(ts, everyTrigger("", 60))
			}
			specs[i] = eventSpec(ts...)
		}
		e := wantOnly(t, ValidateBot(sent(specs...)), "rules")
		if e.Error == sentenceBotTooLarge {
			t.Errorf("the refusal points at a hold cap no rule has: %q", e.Error)
		}
	})
}

func TestRearms_ComparesATriggerWithoutItsLimits(t *testing.T) {
	withTestEventKind(t)
	stored := eventTrigger("T1", 10)
	limits := eventTrigger("T1", 20)
	limits.Limits.MaxPerHour = 30
	limits.Limits.Hold = holdNewest
	if Rearms(&stored, &limits) {
		t.Error("a limits-only change re-arms the trigger; it must keep its ArmedAt and cursor")
	}
	every := everyTrigger("T2", 60)
	same := everyTrigger("T2", 60)
	if Rearms(&every, &same) {
		t.Error("an unchanged trigger re-arms")
	}
	longer := everyTrigger("T2", 90)
	if !Rearms(&every, &longer) {
		t.Error("a changed interval keeps its arming")
	}
	idle := everyTrigger("T2", 60)
	idle.Every.Anchor = AnchorIdle
	if !Rearms(&every, &idle) {
		t.Error("a changed anchor keeps its arming")
	}
	at := atTrigger("T2", AtTrigger{Days: DaysDaily, Time: "09:00"})
	if !Rearms(&every, &at) {
		t.Error("a changed kind keeps its arming")
	}
}
