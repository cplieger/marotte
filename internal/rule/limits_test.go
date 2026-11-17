package rule

import (
	"slices"
	"testing"
	"time"
)

// pacedRule has an event trigger "e" (12 an hour, 3 a day) and a time trigger "t".
func pacedRule() Rule {
	e := eventTrigger("e", 10)
	e.Limits.MaxPerDay = 3
	return Rule{ID: "r1", Spec: Spec{Name: "Paced", Prompt: "Go.", Enabled: true, Triggers: []Trigger{e, everyTrigger("t", 60)}}}
}

func msAgo(now time.Time, d time.Duration) int64 { return now.Add(-d).UnixMilli() }

// spread is n delivery times, the oldest first ago before now and the rest a minute apart after it.
func spread(now time.Time, n int, first time.Duration) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = msAgo(now, first-time.Duration(i)*time.Minute)
	}
	return out
}

func withTrigger(r Rule, id string, st TriggerState) Rule {
	if r.Status.Triggers == nil {
		r.Status.Triggers = map[string]TriggerState{}
	}
	r.Status.Triggers[id] = st
	return r
}

func TestNextDelivery_EachPaceHoldsUntilItsOwnTime(t *testing.T) {
	now := at(16, 10, 0)
	today := now.Format(time.DateOnly)
	cases := []struct {
		name    string
		rule    Rule
		trigger string
		at      time.Time
		limit   Limit
	}{
		{"nothing sent", pacedRule(), "e", now, LimitNone},
		{"the gap holds for the rest of a minute", func() Rule {
			r := pacedRule()
			r.Status.Sent = []int64{msAgo(now, 20*time.Second)}
			return r
		}(), "e", now.Add(40 * time.Second), LimitGap},
		{"a delivery exactly a minute ago leaves the gap", func() Rule {
			r := pacedRule()
			r.Status.Sent = []int64{msAgo(now, time.Minute)}
			return r
		}(), "e", now, LimitNone},
		{"the gap paces a time trigger", func() Rule {
			r := pacedRule()
			r.Status.Sent = []int64{msAgo(now, 30*time.Second)}
			return r
		}(), "t", now.Add(30 * time.Second), LimitGap},
		{"the gap paces Run now", func() Rule {
			r := pacedRule()
			r.Status.Sent = []int64{msAgo(now, 30*time.Second)}
			return r
		}(), "", now.Add(30 * time.Second), LimitGap},
		{
			"12 in the hour hold until the oldest leaves it", withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 12, 59*time.Minute)}),
			"e", now.Add(time.Minute), LimitHour,
		},
		{
			"11 in the hour let it through", withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 11, 59*time.Minute)}),
			"e", now, LimitNone,
		},
		{
			"a delivery exactly an hour ago no longer counts", withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 12, time.Hour)}),
			"e", now, LimitNone,
		},
		{"a lowered cap counts from the delivery that brings the hour under it", func() Rule {
			r := withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 5, 50*time.Minute)})
			r.Spec.Triggers[0].Limits.MaxPerHour = 2
			return r
		}(), "e", now.Add(13 * time.Minute), LimitHour},
		{
			"the hourly cap is the trigger's own", withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 12, 59*time.Minute)}),
			"t", now, LimitNone,
		},
		{
			"Run now has no trigger cap", withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 12, 59*time.Minute)}),
			"", now, LimitNone,
		},
		{
			"3 today hold until the next local midnight", withTrigger(pacedRule(), "e", TriggerState{Day: &DayCount{Date: today, N: 3}}),
			"e", time.Date(2026, 11, 17, 0, 0, 0, 0, cet), LimitDay,
		},
		{
			"3 yesterday count nothing today", withTrigger(pacedRule(), "e", TriggerState{Day: &DayCount{Date: "2026-11-15", N: 3}}),
			"e", now, LimitNone,
		},
		{"no daily cap", func() Rule {
			r := withTrigger(pacedRule(), "e", TriggerState{Day: &DayCount{Date: today, N: 500}})
			r.Spec.Triggers[0].Limits.MaxPerDay = 0
			return r
		}(), "e", now, LimitNone},
		{"the later of two paces holds", func() Rule {
			r := withTrigger(pacedRule(), "e", TriggerState{Sent: spread(now, 12, 59*time.Minute)})
			r.Status.Sent = []int64{msAgo(now, 30*time.Second)}
			return r
		}(), "e", now.Add(time.Minute), LimitHour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, limit := NextDelivery([]Rule{tc.rule}, 0, tc.trigger, now)
			if !got.Equal(tc.at) || limit != tc.limit {
				t.Errorf("NextDelivery = %v %v, want %v %v", got, limit, tc.at, tc.limit)
			}
		})
	}
}

func TestNextDelivery_TheBotCeilingCountsEveryRule(t *testing.T) {
	now := at(16, 10, 0)
	rules := func(perRule ...int) []Rule {
		out := make([]Rule, len(perRule))
		for i, n := range perRule {
			out[i] = pacedRule()
			out[i].ID = string(rune('a' + i))
			// The rule's own deliveries, oldest first, 2 to 58 minutes ago, never inside the gap.
			for j := range n {
				out[i].Status.Sent = append(out[i].Status.Sent, msAgo(now, time.Duration(58-j)*time.Minute+time.Duration(i)*time.Second))
			}
		}
		return out
	}
	t.Run("59 over three rules let a fourth rule through", func(t *testing.T) {
		rs := append(rules(20, 20, 19), pacedRule())
		if got, limit := NextDelivery(rs, 3, "e", now); !got.Equal(now) || limit != LimitNone {
			t.Errorf("NextDelivery = %v %v, want now", got, limit)
		}
	})
	t.Run("60 over three rules hold a fourth rule until the oldest leaves the hour", func(t *testing.T) {
		rs := append(rules(20, 20, 20), pacedRule())
		oldest := slices.Min(slices.Concat(rs[0].Status.Sent, rs[1].Status.Sent, rs[2].Status.Sent))
		want := time.UnixMilli(oldest).Add(time.Hour)
		got, limit := NextDelivery(rs, 3, "e", now)
		if !got.Equal(want) || limit != LimitBot {
			t.Errorf("NextDelivery = %v %v, want %v %v", got, limit, want, LimitBot)
		}
	})
	t.Run("the ceiling holds Run now too", func(t *testing.T) {
		rs := append(rules(30, 30), pacedRule())
		if _, limit := NextDelivery(rs, 2, "", now); limit != LimitBot {
			t.Errorf("limit %v, want %v", limit, LimitBot)
		}
	})
	t.Run("a delivery an hour old leaves the ceiling", func(t *testing.T) {
		rs := rules(30, 30)
		rs[0].Status.Sent[0] = msAgo(now, time.Hour)
		rs = append(rs, pacedRule())
		if got, limit := NextDelivery(rs, 2, "e", now); !got.Equal(now) || limit != LimitNone {
			t.Errorf("NextDelivery = %v %v, want now", got, limit)
		}
	})
}

func TestRecordSent_CountsTheDeliveryForEveryPace(t *testing.T) {
	now := at(16, 10, 0)
	today := now.Format(time.DateOnly)
	t.Run("an event trigger's delivery", func(t *testing.T) {
		st := Status{
			Sent:     []int64{msAgo(now, 2*time.Hour), msAgo(now, time.Hour), msAgo(now, 30*time.Minute)},
			Triggers: map[string]TriggerState{"e": {ArmedAt: 5, Sent: []int64{msAgo(now, 61*time.Minute), msAgo(now, 10*time.Minute)}, Day: &DayCount{Date: today, N: 2}}},
		}
		RecordSent(&st, "e", now)
		if want := []int64{msAgo(now, 30*time.Minute), now.UnixMilli()}; !slices.Equal(st.Sent, want) {
			t.Errorf("rule sent %v, want the last hour's %v", st.Sent, want)
		}
		ts := st.Triggers["e"]
		if want := []int64{msAgo(now, 10*time.Minute), now.UnixMilli()}; !slices.Equal(ts.Sent, want) {
			t.Errorf("trigger sent %v, want the last hour's %v", ts.Sent, want)
		}
		if ts.Day == nil || *ts.Day != (DayCount{Date: today, N: 3}) {
			t.Errorf("day %+v, want %s 3", ts.Day, today)
		}
		if ts.ArmedAt != 5 {
			t.Errorf("armed at %d, want the state's other fields kept", ts.ArmedAt)
		}
	})
	t.Run("the first delivery of a new day", func(t *testing.T) {
		st := Status{Triggers: map[string]TriggerState{"e": {Day: &DayCount{Date: "2026-11-15", N: 9}}}}
		RecordSent(&st, "e", now)
		if d := st.Triggers["e"].Day; d == nil || *d != (DayCount{Date: today, N: 1}) {
			t.Errorf("day %+v, want %s 1", d, today)
		}
	})
	t.Run("a trigger with no state yet", func(t *testing.T) {
		var st Status
		RecordSent(&st, "e", now)
		if ts := st.Triggers["e"]; !slices.Equal(ts.Sent, []int64{now.UnixMilli()}) || ts.Day == nil || ts.Day.N != 1 {
			t.Errorf("trigger state %+v, want one delivery now", ts)
		}
	})
	t.Run("Run now counts for the rule alone", func(t *testing.T) {
		st := Status{Triggers: map[string]TriggerState{"e": {ArmedAt: 5}}}
		RecordSent(&st, "", now)
		if !slices.Equal(st.Sent, []int64{now.UnixMilli()}) {
			t.Errorf("rule sent %v, want now", st.Sent)
		}
		if len(st.Triggers) != 1 || st.Triggers["e"].Sent != nil || st.Triggers["e"].Day != nil {
			t.Errorf("triggers %+v, want them untouched", st.Triggers)
		}
	})
	t.Run("a trigger keeps at most 60 deliveries", func(t *testing.T) {
		sent := make([]int64, 0, maxPerHour)
		for i := range maxPerHour {
			sent = append(sent, msAgo(now, time.Duration(59*60-i)*time.Second))
		}
		st := Status{Triggers: map[string]TriggerState{"e": {Sent: sent}}}
		RecordSent(&st, "e", now)
		got := st.Triggers["e"].Sent
		if len(got) != maxPerHour || got[len(got)-1] != now.UnixMilli() || got[0] != sent[1] {
			t.Errorf("trigger keeps %d deliveries from %d, want the newest %d", len(got), got[0], maxPerHour)
		}
	})
}
