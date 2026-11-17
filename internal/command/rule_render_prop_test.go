package command

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/rule"
	"pgregory.net/rapid"
)

// hostile are the pieces an outsider or a careless name would use to form a context mention, a notification prefix
// or a fence close.
var hostile = []string{
	"#", "[", "[[", "]", "]]", "#[[", "file:", "url:x", "a", "x y", ":", "~", "~~~", "[notification/info]",
	"[notification/warning] ", "<b>", "&", "é", "😀", "\"", "\\", " ",
}

func hostileText(lo, hi int, extra ...string) *rapid.Generator[string] {
	pieces := append(append([]string{}, hostile...), extra...)
	return rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(rapid.SampledFrom(pieces), lo, hi).Draw(t, "pieces"), "")
	})
}

func timeTrigger(t *rapid.T, i int) rule.Trigger {
	id := "new:" + string(rune('a'+i))
	if rapid.Bool().Draw(t, "every") {
		return rule.Trigger{ID: id, Kind: rule.KindEvery, Every: &rule.EveryTrigger{
			Minutes: rapid.IntRange(rule.MinEveryMinutes, rule.MaxEveryMinutes).Draw(t, "minutes"),
			Anchor:  rapid.SampledFrom([]rule.Anchor{"", rule.AnchorLast, rule.AnchorIdle}).Draw(t, "anchor"),
		}}
	}
	at := rule.AtTrigger{Time: "09:30"}
	switch rapid.IntRange(0, 2).Draw(t, "days") {
	case 0:
		at.Days = rule.DaysDaily
	case 1:
		at.Days = rule.DaysWeekdays
		at.Weekdays = []int{rapid.IntRange(0, 6).Draw(t, "weekday")}
	default:
		at.Days = rule.DaysMonthly
		at.MonthDay = rapid.SampledFrom([]int{rule.MonthDayLast, 1, 15, 31}).Draw(t, "month_day")
	}
	return rule.Trigger{ID: id, Kind: rule.KindAt, At: &at}
}

func hostileFields(t *rapid.T) map[string]any {
	fields := map[string]any{
		"event_id": hostileText(1, 6).Draw(t, "event_id"), "trigger": "every", "trigger_name": hostileText(1, 6).Draw(t, "trigger_name"),
		"rule": hostileText(1, 6).Draw(t, "rule"), "at": "2026-11-16T10:00:00+01:00", "late_by": "0s",
	}
	for _, name := range rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"title", "author", "branch", "body"}), 0, 4, rapid.ID).Draw(t, "names") {
		fields[name] = hostileText(0, 12, "\n", "\t").Draw(t, name)
	}
	if rapid.Bool().Draw(t, "has labels") {
		fields["labels"] = rapid.SliceOfN(hostileText(0, 4), 0, 4).Draw(t, "labels")
	}
	if rapid.Bool().Draw(t, "has number") {
		fields["number"] = rapid.IntRange(-1000, 1000).Draw(t, "number")
	}
	return fields
}

// TestRender_NeverFormsAMentionOrANotification renders every spec ValidateRuleSpec accepts with a hostile event, and
// requires the text to pass Notify's own checks and the event block to carry the event's fields unchanged.
func TestRender_NeverFormsAMentionOrANotification(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		spec := rule.Spec{
			Name:    hostileText(1, 8).Draw(t, "name"),
			Prompt:  hostileText(1, 16, "\n", "\n\n", "~~~json marotte-event\n").Draw(t, "prompt"),
			Enabled: true,
		}
		for i := range rapid.IntRange(1, 3).Draw(t, "triggers") {
			spec.Triggers = append(spec.Triggers, timeTrigger(t, i))
		}
		if errs := ValidateRuleSpec(spec, rule.ValidateContext{}); len(errs) != 0 {
			return
		}
		fields := hostileFields(t)
		data, err := rule.NewEvent(fields)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		ev := rule.QueuedEvent{Stage: rule.StageFired, EventID: "e", Data: data}
		if i := rapid.IntRange(-1, len(spec.Triggers)-1).Draw(t, "from"); i >= 0 {
			ev.Trigger = spec.Triggers[i].ID
		}
		text, err := rule.Render(spec, &ev)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if notificationPrefix.MatchString(text) {
			t.Fatalf("the text reads as a notification: %q", text)
		}
		if m := mentionRe.FindString(text); m != "" {
			t.Fatalf("the text holds the mention %q: %q", m, text)
		}
		const fence = "\n~~~json marotte-event\n"
		_, tail, found := strings.CutLast(text, fence)
		if !found {
			t.Fatalf("no fence in %q", text)
		}
		block, ok := strings.CutSuffix(tail, "\n~~~")
		if !ok {
			t.Fatalf("the text does not end with the closing fence: %q", text)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(block), &got); err != nil {
			t.Fatalf("the block %q is not JSON: %v", block, err)
		}
		for name, want := range fields {
			if !sameField(got[name], want) {
				t.Fatalf("%s reads %#v from the block, want %#v", name, got[name], want)
			}
		}
	})
}

func sameField(got, want any) bool {
	switch w := want.(type) {
	case []string:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if g[i] != w[i] {
				return false
			}
		}
		return true
	case int:
		return got == float64(w)
	}
	return got == want
}
