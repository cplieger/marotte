package rule

import "time"

// maxScanDays bounds the day-by-day scan for an at trigger's next slot: a monthly day clamps into every month, so
// any valid trigger has a slot within two months.
const maxScanDays = 62

// NextDue answers a time trigger's next evaluation in now's zone, the server's; lastTurnEndedAt is
// Chat.LastTurnEndedAt and live says the Bot has a live turn. A due time already passed reads as now. A kind with no
// schedule, and an idle trigger while live (the turn's end moves it), answer the zero time.
func NextDue(t Trigger, st *TriggerState, lastTurnEndedAt int64, live bool, now time.Time) time.Time {
	due, ok, afterTurn := nextTime(t, st, lastTurnEndedAt, live, now.Location())
	switch {
	case afterTurn, !ok:
		return time.Time{}
	case due.Before(now):
		return now
	}
	return due
}

// DueAt is a time trigger's due time as the record alone places it, with no live turn and no clock: a due time
// already passed stays in the past, so a reader compares it with its own now. A kind with no schedule answers false.
func DueAt(t Trigger, st *TriggerState, lastTurnEndedAt int64, loc *time.Location) (time.Time, bool) {
	due, ok, _ := nextTime(t, st, lastTurnEndedAt, false, loc)
	return due, ok
}

// DueSlot answers the slot an evaluation at now is for, when one is due: an every trigger's anchor plus its
// interval, an at trigger's newest slot after its anchor: one evaluation, of the newest missed slot.
func DueSlot(t Trigger, st *TriggerState, lastTurnEndedAt int64, live bool, now time.Time) (Slot, bool) {
	due, ok, _ := nextTime(t, st, lastTurnEndedAt, live, now.Location())
	if !ok || due.After(now) {
		return Slot{}, false
	}
	if t.Kind == KindAt {
		for {
			next, found := nextSlot(t.At, due)
			if !found || next.After(now) {
				break
			}
			due = next
		}
	}
	return Slot{DueAt: due, LateBy: now.Sub(due)}, true
}

// Evaluated moves the trigger's anchor for an evaluation at now, fire or skip: to the evaluation's instant, never
// to the due time it was for.
func Evaluated(st *TriggerState, now time.Time) {
	st.LastAt = now.UnixMilli()
}

// nextTime is the first due time after the trigger's anchor, unclamped; afterTurn says an idle trigger waits for
// the live turn's end.
func nextTime(t Trigger, st *TriggerState, lastTurnEndedAt int64, live bool, loc *time.Location) (due time.Time, ok, afterTurn bool) {
	anchor := st.LastAt
	if anchor == 0 {
		anchor = st.ArmedAt
	}
	switch {
	case t.Kind == KindEvery && t.Every != nil && t.Every.Anchor == AnchorIdle:
		if live {
			return time.Time{}, false, true
		}
		from := max(lastTurnEndedAt, st.LastAt, st.ArmedAt)
		return time.UnixMilli(from).In(loc).Add(time.Duration(t.Every.Minutes) * time.Minute), true, false
	case t.Kind == KindEvery && t.Every != nil:
		return time.UnixMilli(anchor).In(loc).Add(time.Duration(t.Every.Minutes) * time.Minute), true, false
	case t.Kind == KindAt && t.At != nil:
		due, ok = nextSlot(t.At, time.UnixMilli(anchor).In(loc))
		return due, ok, false
	}
	return time.Time{}, false, false
}

// nextSlot is the at trigger's first slot strictly after after, in after's zone.
func nextSlot(a *AtTrigger, after time.Time) (time.Time, bool) {
	clock, ok := parseClock(a.Time)
	if !ok {
		return time.Time{}, false
	}
	y, m, d := after.Date()
	for i := range maxScanDays {
		// Noon keeps the date arithmetic clear of any DST shift.
		day := time.Date(y, m, d+i, 12, 0, 0, 0, after.Location())
		if !slotDay(a, day) {
			continue
		}
		if slot := wallTime(day, clock); slot.After(after) {
			return slot, true
		}
	}
	return time.Time{}, false
}

func slotDay(a *AtTrigger, day time.Time) bool {
	switch a.Days {
	case DaysDaily:
		return true
	case DaysWeekdays:
		for _, w := range a.Weekdays {
			if time.Weekday(w) == day.Weekday() {
				return true
			}
		}
	case DaysMonthly:
		last := time.Date(day.Year(), day.Month()+1, 0, 12, 0, 0, 0, day.Location()).Day()
		want := a.MonthDay
		if want == MonthDayLast || want > last {
			want = last
		}
		return day.Day() == want
	}
	return false
}

// wallTime is the instant of minutes after midnight on day's date. A missing spring-forward time normalises as
// time.Date does; a repeated fall-back time is its first instant, since time.Date may answer the second and a slot
// fires once, at the first.
func wallTime(day time.Time, minutes int) time.Time {
	t := time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, day.Location())
	_, before := t.Add(-3 * time.Hour).Zone()
	_, at := t.Zone()
	if before > at {
		earlier := t.Add(-time.Duration(before-at) * time.Second)
		if earlier.Day() == t.Day() && earlier.Hour() == t.Hour() && earlier.Minute() == t.Minute() {
			return earlier
		}
	}
	return t
}
