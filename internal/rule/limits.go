package rule

import (
	"slices"
	"time"
)

// NextDelivery answers when rules[i]'s next prompt, from trigger ("" for Run now), may start, and the pace that
// holds it: the rule's MinGap, the trigger's MaxPerHour and MaxPerDay, and BotMaxPerHour over every rule of the Bot.
// The latest wins; now with LimitNone when nothing holds. An event over a limit waits, it is never skipped.
func NextDelivery(rules []Rule, i int, trigger string, now time.Time) (time.Time, Limit) {
	until, limit := now, LimitNone
	hold := func(t time.Time, l Limit) {
		if t.After(until) {
			until, limit = t, l
		}
	}
	r := &rules[i]
	if len(r.Status.Sent) > 0 {
		hold(time.UnixMilli(slices.Max(r.Status.Sent)).Add(MinGap), LimitGap)
	}
	if lim := triggerLimits(&r.Spec, trigger); lim != nil {
		st := r.Status.Triggers[trigger]
		if t, ok := capUntil(st.Sent, lim.MaxPerHour, now); ok {
			hold(t, LimitHour)
		}
		if lim.MaxPerDay > 0 && st.Day != nil && st.Day.Date == now.Format(time.DateOnly) && st.Day.N >= lim.MaxPerDay {
			y, m, d := now.Date()
			hold(time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()), LimitDay)
		}
	}
	var all []int64
	for j := range rules {
		all = append(all, rules[j].Status.Sent...)
	}
	if t, ok := capUntil(all, BotMaxPerHour, now); ok {
		hold(t, LimitBot)
	}
	return until.In(now.Location()), limit
}

// capUntil answers when the deliveries in the rolling hour before now fall under limit, if they are at it.
func capUntil(sent []int64, limit int, now time.Time) (time.Time, bool) {
	in := inHour(sent, now)
	if limit < 1 || len(in) < limit {
		return time.Time{}, false
	}
	slices.Sort(in)
	return time.UnixMilli(in[len(in)-limit]).Add(time.Hour), true
}

// inHour keeps the times in (now - 1 h, now], in their order.
func inHour(sent []int64, now time.Time) []int64 {
	from := now.Add(-time.Hour).UnixMilli()
	out := make([]int64, 0, len(sent))
	for _, s := range sent {
		if s > from {
			out = append(out, s)
		}
	}
	return out
}

func triggerLimits(spec *Spec, trigger string) *TriggerLimits {
	if trigger == "" {
		return nil
	}
	for i := range spec.Triggers {
		if spec.Triggers[i].ID == trigger {
			return spec.Triggers[i].Limits
		}
	}
	return nil
}

// RecordSent counts a delivery at now for every pace: the rule's Sent and, for a trigger's event, the trigger's
// Sent (at most MaxPerHour, the newest) and its local day's count.
func RecordSent(st *Status, trigger string, now time.Time) {
	at := now.UnixMilli()
	st.Sent = append(inHour(st.Sent, now), at)
	if trigger == "" {
		return
	}
	if st.Triggers == nil {
		st.Triggers = map[string]TriggerState{}
	}
	ts := st.Triggers[trigger]
	sent := append(inHour(ts.Sent, now), at)
	ts.Sent = sent[max(0, len(sent)-maxPerHour):]
	today := now.Format(time.DateOnly)
	n := 1
	if ts.Day != nil && ts.Day.Date == today {
		n = ts.Day.N + 1
	}
	ts.Day = &DayCount{Date: today, N: n}
	st.Triggers[trigger] = ts
}
