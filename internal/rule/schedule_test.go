package rule

import (
	"testing"
	"time"
	// Embedded tzdata keeps the DST cases running on a host with no zoneinfo; test binary only.
	_ "time/tzdata"
)

// cest is Paris's summer offset, beside eval_test.go's cet, so a DST case names which of a repeated hour it means.
var cest = time.FixedZone("CEST", 7200)

// paris is the container's server zone, where 2026's clocks go forward on 29 March and back on 25 October.
var paris = mustZone("Europe/Paris")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func wall(y int, m time.Month, d, hour, minute int) time.Time {
	return time.Date(y, m, d, hour, minute, 0, 0, paris)
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func idleTrigger(id string, minutes int) Trigger {
	return Trigger{ID: id, Kind: KindEvery, Every: &EveryTrigger{Minutes: minutes, Anchor: AnchorIdle}}
}

func daily(clock string) Trigger { return atTrigger("t1", AtTrigger{Days: DaysDaily, Time: clock}) }

func monthly(day int, clock string) Trigger {
	return atTrigger("t1", AtTrigger{Days: DaysMonthly, MonthDay: day, Time: clock})
}

func armed(at time.Time) TriggerState { return TriggerState{ArmedAt: ms(at)} }

func ranAt(armedAt, lastAt time.Time) TriggerState {
	return TriggerState{ArmedAt: ms(armedAt), LastAt: ms(lastAt)}
}

func wantDue(t *testing.T, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("NextDue = %v, want %v", got, want)
	}
}

func TestNextDue_EveryLastCountsFromTheLastEvaluation(t *testing.T) {
	nine := wall(2026, 11, 16, 9, 0)
	noon := wall(2026, 11, 16, 12, 0)
	cases := []struct {
		name string
		trig Trigger
		st   TriggerState
		now  time.Time
		live bool
		want time.Time
	}{
		{"from LastAt, not the later turn end", everyTrigger("t1", 180), ranAt(nine, noon), noon.Add(time.Minute), false, wall(2026, 11, 16, 15, 0)},
		{"from ArmedAt before the first evaluation", everyTrigger("t1", 180), armed(nine), nine.Add(time.Minute), false, noon},
		{"an empty anchor reads as last", Trigger{ID: "t1", Kind: KindEvery, Every: &EveryTrigger{Minutes: 30}}, ranAt(nine, noon), noon, false, wall(2026, 11, 16, 12, 30)},
		{"a live turn does not hold it", everyTrigger("t1", 180), ranAt(nine, noon), noon.Add(time.Minute), true, wall(2026, 11, 16, 15, 0)},
		{"a passed due time reads as now", everyTrigger("t1", 180), ranAt(nine, nine), noon.Add(time.Minute), false, noon.Add(time.Minute)},
		{"elapsed time across the clocks going back", everyTrigger("t1", 60), ranAt(wall(2026, 10, 25, 1, 0), time.Date(2026, 10, 25, 2, 30, 0, 0, cest)), time.Date(2026, 10, 25, 2, 30, 0, 0, cest), false, time.Date(2026, 10, 25, 2, 30, 0, 0, cet)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ended := ms(wall(2026, 11, 16, 14, 0))
			wantDue(t, NextDue(c.trig, &c.st, ended, c.live, c.now), c.want)
		})
	}
}

func TestNextDue_EveryIdleCountsFromTheLatestOfTurnEndEvaluationAndArming(t *testing.T) {
	eight, nine, ten := wall(2026, 11, 16, 8, 0), wall(2026, 11, 16, 9, 0), wall(2026, 11, 16, 10, 0)
	now := wall(2026, 11, 16, 10, 5)
	cases := []struct {
		name  string
		st    TriggerState
		ended time.Time
		want  time.Time
	}{
		{"the turn end is latest", ranAt(eight, nine), ten, wall(2026, 11, 16, 12, 0)},
		{"the last evaluation is latest", ranAt(eight, ten), nine, wall(2026, 11, 16, 12, 0)},
		{"the arming is latest", armed(ten), nine, wall(2026, 11, 16, 12, 0)},
		{"no turn ever ended", ranAt(eight, nine), time.UnixMilli(0), wall(2026, 11, 16, 11, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantDue(t, NextDue(idleTrigger("t1", 120), &c.st, ms(c.ended), false, now), c.want)
		})
	}
	t.Run("a live turn answers no time until it ends", func(t *testing.T) {
		st := ranAt(eight, nine)
		if got := NextDue(idleTrigger("t1", 120), &st, ms(eight), true, now); !got.IsZero() {
			t.Errorf("NextDue = %v during a live turn, want the zero time", got)
		}
	})
}

func TestNextDue_AtIsTheFirstSlotAfterItsAnchor(t *testing.T) {
	mon := wall(2026, 11, 16, 8, 0)
	cases := []struct {
		name string
		trig Trigger
		st   TriggerState
		now  time.Time
		want time.Time
	}{
		{"daily, later today", daily("09:00"), armed(mon), mon, wall(2026, 11, 16, 9, 0)},
		{"daily, earlier today goes to tomorrow", daily("07:30"), armed(mon), mon, wall(2026, 11, 17, 7, 30)},
		{"daily, a slot at the anchor itself is past", daily("08:00"), armed(mon), mon, wall(2026, 11, 17, 8, 0)},
		{"daily, from LastAt", daily("09:00"), ranAt(mon, wall(2026, 11, 16, 9, 0)), wall(2026, 11, 16, 9, 1), wall(2026, 11, 17, 9, 0)},
		{"weekdays, Monday and Wednesday from a Thursday", atTrigger("t1", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1, 3}, Time: "09:00"}), armed(wall(2026, 11, 19, 8, 0)), wall(2026, 11, 19, 8, 0), wall(2026, 11, 23, 9, 0)},
		{"weekdays, Sunday is 0", atTrigger("t1", AtTrigger{Days: DaysWeekdays, Weekdays: []int{0}, Time: "18:00"}), armed(mon), mon, wall(2026, 11, 22, 18, 0)},
		{"weekdays, today when still ahead", atTrigger("t1", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1, 5}, Time: "14:00"}), armed(mon), mon, wall(2026, 11, 16, 14, 0)},
		{"monthly, the 15th", monthly(15, "06:00"), armed(mon), mon, wall(2026, 12, 15, 6, 0)},
		{"monthly, the 31st clamps to 30 April", monthly(31, "18:00"), armed(wall(2026, 4, 2, 8, 0)), wall(2026, 4, 2, 8, 0), wall(2026, 4, 30, 18, 0)},
		{"monthly, the 31st clamps to 28 February", monthly(31, "18:00"), armed(wall(2027, 2, 1, 8, 0)), wall(2027, 2, 1, 8, 0), wall(2027, 2, 28, 18, 0)},
		{"monthly, the last day", monthly(MonthDayLast, "18:00"), armed(mon), mon, wall(2026, 11, 30, 18, 0)},
		{"monthly, the last day of a 31-day month", monthly(MonthDayLast, "18:00"), armed(wall(2026, 12, 1, 8, 0)), wall(2026, 12, 1, 8, 0), wall(2026, 12, 31, 18, 0)},
		{"monthly, the 29th in a leap February", monthly(29, "12:00"), armed(wall(2028, 2, 1, 8, 0)), wall(2028, 2, 1, 8, 0), wall(2028, 2, 29, 12, 0)},
		{"monthly, the 29th in a common February", monthly(29, "12:00"), armed(wall(2027, 2, 1, 8, 0)), wall(2027, 2, 1, 8, 0), wall(2027, 2, 28, 12, 0)},
		{"monthly, the 30th in a leap February", monthly(30, "12:00"), armed(wall(2028, 2, 1, 8, 0)), wall(2028, 2, 1, 8, 0), wall(2028, 2, 29, 12, 0)},
		{"monthly, the last day of a leap February", monthly(MonthDayLast, "12:00"), armed(wall(2028, 2, 1, 8, 0)), wall(2028, 2, 1, 8, 0), wall(2028, 2, 29, 12, 0)},
		{"monthly, the clamped day passed goes to next month", monthly(31, "18:00"), ranAt(wall(2026, 4, 2, 8, 0), wall(2026, 4, 30, 18, 0)), wall(2026, 4, 30, 18, 1), wall(2026, 5, 31, 18, 0)},
		{"a passed slot reads as now", daily("09:00"), armed(mon), wall(2026, 11, 16, 9, 30), wall(2026, 11, 16, 9, 30)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantDue(t, NextDue(c.trig, &c.st, 0, false, c.now), c.want)
		})
	}
}

// TestNextDue_AtAcrossDaylightSaving passes every now in paris: the schedule reads the server's zone from now.
func TestNextDue_AtAcrossDaylightSaving(t *testing.T) {
	t.Run("a missing spring-forward slot resolves as time.Date normalises it", func(t *testing.T) {
		st := ranAt(wall(2026, 3, 27, 8, 0), wall(2026, 3, 28, 2, 30))
		wantDue(t, NextDue(daily("02:30"), &st, 0, false, wall(2026, 3, 28, 2, 31)), time.Date(2026, 3, 29, 3, 30, 0, 0, cest))
	})
	t.Run("the day after spring-forward is back at the wall-clock time", func(t *testing.T) {
		st := ranAt(wall(2026, 3, 27, 8, 0), time.Date(2026, 3, 29, 3, 30, 0, 0, cest))
		wantDue(t, NextDue(daily("02:30"), &st, 0, false, time.Date(2026, 3, 29, 3, 31, 0, 0, cest).In(paris)), time.Date(2026, 3, 30, 2, 30, 0, 0, cest))
	})
	first := time.Date(2026, 10, 25, 2, 30, 0, 0, cest)
	second := time.Date(2026, 10, 25, 2, 30, 0, 0, cet)
	t.Run("a fall-back slot is the first 02:30", func(t *testing.T) {
		st := ranAt(wall(2026, 10, 23, 8, 0), wall(2026, 10, 24, 2, 30))
		wantDue(t, NextDue(daily("02:30"), &st, 0, false, wall(2026, 10, 24, 2, 31)), first)
	})
	t.Run("a fall-back slot fires once", func(t *testing.T) {
		st := ranAt(wall(2026, 10, 23, 8, 0), first)
		wantDue(t, NextDue(daily("02:30"), &st, 0, false, first.Add(time.Minute).In(paris)), time.Date(2026, 10, 26, 2, 30, 0, 0, cet))
		if slot, ok := DueSlot(daily("02:30"), &st, 0, false, second.Add(time.Minute).In(paris)); ok {
			t.Errorf("DueSlot at the second 02:30 = %v, want nothing due", slot.DueAt)
		}
	})
}

func TestNextDue_HasNoTimeForAnEventTrigger(t *testing.T) {
	st := armed(wall(2026, 11, 16, 8, 0))
	got := NextDue(eventTrigger("t1", 10), &st, 0, false, wall(2026, 11, 16, 9, 0))
	if !got.IsZero() {
		t.Errorf("NextDue = %v, want the zero time", got)
	}
	if _, ok := DueSlot(eventTrigger("t1", 10), &st, 0, false, wall(2026, 11, 16, 9, 0)); ok {
		t.Error("DueSlot of an event trigger is due, want never")
	}
}

func TestDueSlot_IsDueOnlyOnceTheDueTimeIsReached(t *testing.T) {
	nine := wall(2026, 11, 16, 9, 0)
	cases := []struct {
		name  string
		trig  Trigger
		st    TriggerState
		ended time.Time
		live  bool
		now   time.Time
		due   bool
		want  time.Time
	}{
		{"every, a minute early", everyTrigger("t1", 30), armed(nine), nine, false, wall(2026, 11, 16, 9, 29), false, time.Time{}},
		{"every, a millisecond early", everyTrigger("t1", 30), armed(nine), nine, false, wall(2026, 11, 16, 9, 30).Add(-time.Millisecond), false, time.Time{}},
		{"every, on time", everyTrigger("t1", 30), armed(nine), nine, false, wall(2026, 11, 16, 9, 30), true, wall(2026, 11, 16, 9, 30)},
		{"at, a millisecond early", daily("10:00"), armed(nine), nine, false, wall(2026, 11, 16, 10, 0).Add(-time.Millisecond), false, time.Time{}},
		{"idle, during a live turn", idleTrigger("t1", 30), armed(nine), nine, true, wall(2026, 11, 16, 11, 0), false, time.Time{}},
		{"idle, a turn end moved it", idleTrigger("t1", 30), armed(nine), wall(2026, 11, 16, 9, 20), false, wall(2026, 11, 16, 9, 40), false, time.Time{}},
		{"idle, idle long enough", idleTrigger("t1", 30), armed(nine), wall(2026, 11, 16, 9, 20), false, wall(2026, 11, 16, 9, 50), true, wall(2026, 11, 16, 9, 50)},
		{"at, a slot at the arming is not due", daily("09:00"), armed(nine), nine, false, wall(2026, 11, 16, 9, 30), false, time.Time{}},
		{"at, on time", daily("10:00"), armed(nine), nine, false, wall(2026, 11, 16, 10, 0), true, wall(2026, 11, 16, 10, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slot, ok := DueSlot(c.trig, &c.st, ms(c.ended), c.live, c.now)
			if ok != c.due {
				t.Fatalf("DueSlot due = %v, want %v", ok, c.due)
			}
			if !ok {
				return
			}
			if !slot.DueAt.Equal(c.want) || slot.LateBy != 0 {
				t.Errorf("DueSlot = %v late by %v, want %v late by 0", slot.DueAt, slot.LateBy, c.want)
			}
		})
	}
}

func TestDueSlot_AnOverdueTriggerGivesOneEvaluationThenItsNextAfterNow(t *testing.T) {
	cases := []struct {
		name     string
		trig     Trigger
		st       TriggerState
		now      time.Time
		wantDue  time.Time
		wantLate time.Duration
		wantNext time.Time
	}{
		{
			name: "every 15 min after a 3-hour stop", trig: everyTrigger("t1", 15),
			st:  ranAt(wall(2026, 11, 16, 8, 0), wall(2026, 11, 16, 10, 0)),
			now: wall(2026, 11, 16, 13, 0), wantDue: wall(2026, 11, 16, 10, 15), wantLate: 165 * time.Minute,
			wantNext: wall(2026, 11, 16, 13, 15),
		},
		{
			name: "Monday 14:00, marotte down from 13:50 to 14:20", trig: atTrigger("t1", AtTrigger{Days: DaysWeekdays, Weekdays: []int{1}, Time: "14:00"}),
			st:  ranAt(wall(2026, 11, 2, 8, 0), wall(2026, 11, 9, 14, 0)),
			now: wall(2026, 11, 16, 14, 20), wantDue: wall(2026, 11, 16, 14, 0), wantLate: 20 * time.Minute,
			wantNext: wall(2026, 11, 23, 14, 0),
		},
		{
			name: "daily 09:00 with three slots missed, for the newest", trig: daily("09:00"),
			st:  ranAt(wall(2026, 11, 2, 8, 0), wall(2026, 11, 16, 9, 0)),
			now: wall(2026, 11, 19, 10, 0), wantDue: wall(2026, 11, 19, 9, 0), wantLate: time.Hour,
			wantNext: wall(2026, 11, 20, 9, 0),
		},
		{
			name: "monthly last day, two months missed, for the newest", trig: monthly(MonthDayLast, "18:00"),
			st:  ranAt(wall(2026, 8, 1, 8, 0), wall(2026, 8, 31, 18, 0)),
			now: wall(2026, 11, 2, 18, 0), wantDue: wall(2026, 10, 31, 18, 0), wantLate: 48 * time.Hour,
			wantNext: wall(2026, 11, 30, 18, 0),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slot, ok := DueSlot(c.trig, &c.st, 0, false, c.now)
			if !ok {
				t.Fatal("DueSlot is not due, want one evaluation")
			}
			if !slot.DueAt.Equal(c.wantDue) || slot.LateBy != c.wantLate {
				t.Errorf("DueSlot = %v late by %v, want %v late by %v", slot.DueAt, slot.LateBy, c.wantDue, c.wantLate)
			}
			after := c.st
			Evaluated(&after, c.now)
			if slot, ok := DueSlot(c.trig, &after, 0, false, c.now.Add(time.Minute)); ok {
				t.Errorf("DueSlot a minute after the evaluation = %v, want nothing due", slot.DueAt)
			}
			wantDue(t, NextDue(c.trig, &after, 0, false, c.now.Add(time.Minute)), c.wantNext)
		})
	}
}

func TestEvaluated_AnchorsAtTheEvaluationInstantAndKeepsTheRest(t *testing.T) {
	st := TriggerState{ArmedAt: 100, LastAt: 200, Sent: []int64{150}, Cursor: []byte(`{"k":1}`), Day: &DayCount{Date: "2026-11-16", N: 2}}
	now := wall(2026, 11, 16, 13, 0)
	got := st
	Evaluated(&got, now)
	if got.LastAt != ms(now) {
		t.Errorf("LastAt = %d, want the evaluation instant %d", got.LastAt, ms(now))
	}
	if got.ArmedAt != 100 || len(got.Sent) != 1 || got.Sent[0] != 150 || string(got.Cursor) != `{"k":1}` || got.Day == nil || got.Day.N != 2 {
		t.Errorf("Evaluated changed more than LastAt: %+v", got)
	}
}

// TestSchedule_ASkipMovesTheAnchorAsAFireDoes walks "every 3 h, if this Bot is not working", armed at 09:00,
// through a fire, a skip and a fire.
func TestSchedule_ASkipMovesTheAnchorAsAFireDoes(t *testing.T) {
	trig := everyTrigger("t1", 180)
	cond := not(busy())
	st := armed(wall(2026, 11, 16, 9, 0))
	rows := []struct {
		at     time.Time
		busy   bool
		fires  bool
		reason string
		next   time.Time
	}{
		{wall(2026, 11, 16, 12, 0), false, true, "", wall(2026, 11, 16, 15, 0)},
		{wall(2026, 11, 16, 15, 0), true, false, "this Bot is working", wall(2026, 11, 16, 18, 0)},
		{wall(2026, 11, 16, 18, 0), false, true, "", wall(2026, 11, 16, 21, 0)},
	}
	for _, row := range rows {
		if slot, ok := DueSlot(trig, &st, 0, false, row.at.Add(-time.Minute)); ok {
			t.Fatalf("DueSlot a minute before %v = %v, want nothing due", row.at, slot.DueAt)
		}
		slot, ok := DueSlot(trig, &st, 0, false, row.at)
		if !ok || !slot.DueAt.Equal(row.at) {
			t.Fatalf("DueSlot at %v = %v (due %v), want due on time", row.at, slot.DueAt, ok)
		}
		res := Eval(&cond, &Snapshot{Now: row.at, Trigger: "t1", BotBusy: row.busy, Event: Event{Fields: map[string]any{}}})
		if (res.Value == True) != row.fires || res.Reason != row.reason {
			t.Errorf("at %v: Eval = %v %q, want fires %v %q", row.at, res.Value, res.Reason, row.fires, row.reason)
		}
		Evaluated(&st, row.at)
		wantDue(t, NextDue(trig, &st, 0, false, row.at), row.next)
	}
}
