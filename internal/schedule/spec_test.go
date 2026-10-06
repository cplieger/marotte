package schedule

import (
	"testing"
	"time"
	// Embedded tzdata keeps the DST tests running on a host with no zoneinfo; test binary only.
	_ "time/tzdata"
)

// at builds a local time, the same location NextRun works in.
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local)
}

func TestNextRun(t *testing.T) {
	tests := []struct {
		name  string
		spec  Spec
		after time.Time
		want  time.Time
	}{
		{
			name:  "daily later today",
			spec:  Spec{Freq: FreqDaily, Hour: 2, Minute: 30},
			after: at(2026, time.August, 4, 1, 0),
			want:  at(2026, time.August, 4, 2, 30),
		},
		{
			name:  "daily rolls to tomorrow once passed",
			spec:  Spec{Freq: FreqDaily, Hour: 2, Minute: 30},
			after: at(2026, time.August, 4, 2, 30),
			want:  at(2026, time.August, 5, 2, 30),
		},
		{
			name:  "hourly steps from local midnight",
			spec:  Spec{Freq: FreqHourly, Interval: 6, Minute: 15},
			after: at(2026, time.August, 4, 7, 0),
			want:  at(2026, time.August, 4, 12, 15),
		},
		{
			// A step of 15 phased at :07 fires 07,22,37,52 past each hour.
			name:  "minutely steps from local midnight with the chosen offset",
			spec:  Spec{Freq: FreqMinutely, Interval: 15, Minute: 7},
			after: at(2026, time.August, 4, 9, 30),
			want:  at(2026, time.August, 4, 9, 37),
		},
		{
			// The picked minute is always a fire time: 37 is 2 steps of 15 past phase 7.
			name:  "minutely fires at the minute the user picked",
			spec:  Spec{Freq: FreqMinutely, Interval: 15, Minute: 37},
			after: at(2026, time.August, 4, 9, 30),
			want:  at(2026, time.August, 4, 9, 37),
		},
		{
			name:  "minutely with a zero offset is on the step",
			spec:  Spec{Freq: FreqMinutely, Interval: 30, Minute: 0},
			after: at(2026, time.August, 4, 9, 5),
			want:  at(2026, time.August, 4, 9, 30),
		},
		{
			name:  "minutely wraps to the next day past the last slot",
			spec:  Spec{Freq: FreqMinutely, Interval: 30, Minute: 0},
			after: at(2026, time.August, 4, 23, 45),
			want:  at(2026, time.August, 5, 0, 0),
		},
		{
			// A step not dividing 1440 has a short final gap: the walk restarts at local midnight.
			name:  "minutely step that does not divide the day has a short final gap",
			spec:  Spec{Freq: FreqMinutely, Interval: 50, Minute: 0},
			after: at(2026, time.August, 4, 23, 30),
			want:  at(2026, time.August, 5, 0, 0),
		},
		{
			// The walk stops at the day, so the next fire is the next day's PHASE, not the midnight
			// the step points at.
			name:  "minutely step landing exactly on the end of the day keeps its phase",
			spec:  Spec{Freq: FreqMinutely, Interval: 50, Minute: 40},
			after: at(2026, time.August, 4, 23, 30),
			want:  at(2026, time.August, 5, 0, 40),
		},
		{
			// The walk is over minutes of the DAY: phase 20, step 45 gives 00:20, 01:05, ... 10:05.
			name:  "minutely straddles the hour when the step does not divide 60",
			spec:  Spec{Freq: FreqMinutely, Interval: 45, Minute: 20},
			after: at(2026, time.August, 4, 9, 30),
			want:  at(2026, time.August, 4, 10, 5),
		},
		{
			name:  "hourly wraps to midnight after the last step",
			spec:  Spec{Freq: FreqHourly, Interval: 6, Minute: 15},
			after: at(2026, time.August, 4, 18, 30),
			want:  at(2026, time.August, 5, 0, 15),
		},
		{
			name:  "hourly step that does not divide 24 has a short final gap",
			spec:  Spec{Freq: FreqHourly, Interval: 5, Minute: 0},
			after: at(2026, time.August, 4, 20, 30),
			want:  at(2026, time.August, 5, 0, 0),
		},
		{
			name:  "weekly picks the next listed weekday",
			spec:  Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Monday)}, Hour: 9},
			after: at(2026, time.August, 4, 10, 0), // a Tuesday
			want:  at(2026, time.August, 10, 9, 0), // the following Monday
		},
		{
			name:  "weekly with several days takes the soonest",
			spec:  Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Monday), int(time.Wednesday)}, Hour: 9},
			after: at(2026, time.August, 4, 10, 0), // Tuesday
			want:  at(2026, time.August, 5, 9, 0),  // Wednesday
		},
		{
			name:  "weekly same day but earlier hour still fires today",
			spec:  Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Tuesday)}, Hour: 23},
			after: at(2026, time.August, 4, 10, 0),
			want:  at(2026, time.August, 4, 23, 0),
		},
		{
			name:  "monthly this month",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 15, Hour: 3},
			after: at(2026, time.August, 4, 10, 0),
			want:  at(2026, time.August, 15, 3, 0),
		},
		{
			name:  "monthly rolls to next month once passed",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 1, Hour: 3},
			after: at(2026, time.August, 4, 10, 0),
			want:  at(2026, time.September, 1, 3, 0),
		},
		{
			name:  "day 31 clamps to the end of a 30-day month",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 31, Hour: 3},
			after: at(2026, time.September, 1, 0, 0),
			want:  at(2026, time.September, 30, 3, 0),
		},
		{
			name:  "day 31 clamps to the end of February",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 31, Hour: 3},
			after: at(2026, time.February, 1, 0, 0),
			want:  at(2026, time.February, 28, 3, 0),
		},
		{
			name:  "day 30 clamps to a leap February's 29th",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 30, Hour: 3},
			after: at(2028, time.February, 1, 0, 0),
			want:  at(2028, time.February, 29, 3, 0),
		},
		{
			name:  "last day of a 31-day month",
			spec:  Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 3},
			after: at(2026, time.August, 1, 0, 0),
			want:  at(2026, time.August, 31, 3, 0),
		},
		{
			name:  "last day of a leap February",
			spec:  Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 3},
			after: at(2028, time.February, 10, 0, 0),
			want:  at(2028, time.February, 29, 3, 0),
		},
		{
			name:  "monthly crosses a year boundary",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 5, Hour: 3},
			after: at(2026, time.December, 20, 0, 0),
			want:  at(2027, time.January, 5, 3, 0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextRun(tt.spec, tt.after)
			if err != nil {
				t.Fatalf("NextRun: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("NextRun(%v) = %v, want %v", tt.after, got, tt.want)
			}
		})
	}
}

// TestNextRun_IsStrictlyAfter pins that a fired due time is not returned again, or a
// schedule re-fires in a loop for the whole minute.
func TestNextRun_IsStrictlyAfter(t *testing.T) {
	spec := Spec{Freq: FreqDaily, Hour: 2, Minute: 30}
	exact := at(2026, time.August, 4, 2, 30)
	got, err := NextRun(spec, exact)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !got.After(exact) {
		t.Errorf("NextRun must be strictly after its argument: got %v for %v", got, exact)
	}
}

// TestNextRun_Idempotent guards against drift: feeding a result back in must
// yield the following slot, never the same one.
func TestNextRun_Idempotent(t *testing.T) {
	spec := Spec{Freq: FreqHourly, Interval: 8, Minute: 0}
	cur := at(2026, time.August, 4, 3, 0)
	seen := map[time.Time]bool{}
	for range 10 {
		next, err := NextRun(spec, cur)
		if err != nil {
			t.Fatalf("NextRun: %v", err)
		}
		if seen[next] {
			t.Fatalf("NextRun repeated %v", next)
		}
		seen[next] = true
		if !next.After(cur) {
			t.Fatalf("NextRun went backwards: %v after %v", next, cur)
		}
		cur = next
	}
}

// TestNextRun_MinutelyAcrossADSTBoundary walks a DST-transition day slot by slot and
// asserts the walk never repeats an instant, goes backwards or skips the day. The
// spring-forward slot list is unsorted (time.Date normalizes the missing hour backward);
// NextRun stays correct only because that block is already past.
func TestNextRun_MinutelyAcrossADSTBoundary(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	const step = 15
	cases := []struct {
		name string
		day  time.Time
		// wantSlots is the count over the local day: 92 on a 23-hour day (the missing hour's slots
		// collapse onto fired instants), 96 on a 25-hour day.
		wantSlots int
		// wantInHour1 is how many slots fall in the local 01:00 hour: four either way.
		wantInHour1 int
		wantMaxGap  time.Duration
	}{
		{"spring forward, 23 hours", time.Date(2026, time.March, 8, 0, 0, 0, 0, loc), 92, 4, step * time.Minute},
		// The one real gap: 01:45 EDT to 02:00 EST is 1h15m, the repeated hour traversed once.
		{"fall back, 25 hours", time.Date(2026, time.November, 1, 0, 0, 0, 0, loc), 96, 4, 75 * time.Minute},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			spec := Spec{Freq: FreqMinutely, Interval: step}
			end := tt.day.AddDate(0, 0, 1)
			cur := tt.day.Add(-time.Second) // just before midnight's own slot
			var slots []time.Time
			var maxGap time.Duration
			for cur.Before(end) {
				next, err := NextRun(spec, cur)
				if err != nil {
					t.Fatalf("NextRun after %v: %v", cur, err)
				}
				if !next.After(cur) {
					t.Fatalf("NextRun went backwards or repeated: %v after %v", next, cur)
				}
				if !next.Before(end) {
					break
				}
				if len(slots) > 0 {
					if gap := next.Sub(slots[len(slots)-1]); gap > maxGap {
						maxGap = gap
					}
				}
				slots = append(slots, next)
				cur = next
			}
			if len(slots) != tt.wantSlots {
				t.Errorf("got %d slots on the day, want %d", len(slots), tt.wantSlots)
			}
			inHour1 := 0
			for _, s := range slots {
				if s.Hour() == 1 {
					inHour1++
				}
			}
			if inHour1 != tt.wantInHour1 {
				t.Errorf("got %d slots in the 01:00 hour, want %d", inHour1, tt.wantInHour1)
			}
			if maxGap != tt.wantMaxGap {
				t.Errorf("largest gap = %v, want %v", maxGap, tt.wantMaxGap)
			}
			// The day is never skipped: the first slot is local midnight.
			if len(slots) > 0 && !slots[0].Equal(tt.day) {
				t.Errorf("first slot = %v, want local midnight %v", slots[0], tt.day)
			}
		})
	}
}

// TestNextRun_MinutelySkipsTheMissingHour asserts the accepted spring-forward consequence:
// no slot reports the nonexistent local 02:00 hour.
func TestNextRun_MinutelySkipsTheMissingHour(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	spec := Spec{Freq: FreqMinutely, Interval: 15}
	cur := time.Date(2026, time.March, 8, 1, 0, 0, 0, loc)
	end := time.Date(2026, time.March, 8, 4, 0, 0, 0, loc)
	for cur.Before(end) {
		next, err := NextRun(spec, cur)
		if err != nil {
			t.Fatalf("NextRun: %v", err)
		}
		if next.Hour() == 2 {
			t.Fatalf("NextRun returned %v, an hour the local clock skips", next)
		}
		cur = next
	}
	// The transition is crossed in one step of the walk, not stalled on.
	got, err := NextRun(spec, time.Date(2026, time.March, 8, 1, 50, 0, 0, loc))
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if want := time.Date(2026, time.March, 8, 3, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("across the gap: got %v, want %v", got, want)
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		ok   bool
	}{
		{"valid daily", Spec{Freq: FreqDaily, Hour: 2, Minute: 30}, true},
		{"valid hourly", Spec{Freq: FreqHourly, Interval: 6}, true},
		{"valid minutely", Spec{Freq: FreqMinutely, Interval: 15, Minute: 7}, true},
		{"valid weekly", Spec{Freq: FreqWeekly, Weekdays: []int{0}, Hour: 9}, true},
		{"valid monthly last day", Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 9}, true},
		{"unknown freq", Spec{Freq: "yearly", Hour: 1}, false},
		{"empty freq", Spec{Hour: 1}, false},
		{"hour too high", Spec{Freq: FreqDaily, Hour: 24}, false},
		{"hour negative", Spec{Freq: FreqDaily, Hour: -1}, false},
		{"minute too high", Spec{Freq: FreqDaily, Minute: 60}, false},
		// Every range in the error messages is inclusive at both ends.
		{"midnight, the bottom of the hour range", Spec{Freq: FreqDaily, Hour: 0, Minute: 30}, true},
		{"the last minute of the hour", Spec{Freq: FreqDaily, Hour: 2, Minute: 59}, true},
		{"hourly interval zero", Spec{Freq: FreqHourly, Interval: 0}, false},
		{"hourly interval too high", Spec{Freq: FreqHourly, Interval: 25}, false},
		{"hourly at the floor, every hour", Spec{Freq: FreqHourly, Interval: 1}, true},
		{"hourly at the ceiling, once a day", Spec{Freq: FreqHourly, Interval: 24}, true},
		// The floor (see minMinuteInterval).
		{"minutely every minute is refused", Spec{Freq: FreqMinutely, Interval: 1}, false},
		{"minutely just under the floor", Spec{Freq: FreqMinutely, Interval: minMinuteInterval - 1}, false},
		{"minutely at the floor", Spec{Freq: FreqMinutely, Interval: minMinuteInterval}, true},
		{"minutely interval zero", Spec{Freq: FreqMinutely, Interval: 0}, false},
		{"minutely at the ceiling", Spec{Freq: FreqMinutely, Interval: maxMinuteInterval}, true},
		// An hour or more is the hourly frequency's.
		{"minutely an hour is refused", Spec{Freq: FreqMinutely, Interval: 60}, false},
		{"minutely offset out of range", Spec{Freq: FreqMinutely, Interval: 15, Minute: 60}, false},
		{"weekly with no days", Spec{Freq: FreqWeekly, Hour: 9}, false},
		{"weekday out of range", Spec{Freq: FreqWeekly, Weekdays: []int{7}, Hour: 9}, false},
		{"Saturday, the top of the weekday range", Spec{Freq: FreqWeekly, Weekdays: []int{6}, Hour: 9}, true},
		{"month day zero", Spec{Freq: FreqMonthly, MonthDay: 0, Hour: 9}, false},
		{"month day too high", Spec{Freq: FreqMonthly, MonthDay: 32, Hour: 9}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if tt.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tt.ok && err == nil {
				t.Errorf("expected an error, got none")
			}
		})
	}
}

// TestNextRun_RejectsAnInvalidSpec keeps a bad stored spec from silently
// producing a fire time.
func TestNextRun_RejectsAnInvalidSpec(t *testing.T) {
	if _, err := NextRun(Spec{Freq: "nope"}, time.Now()); err == nil {
		t.Errorf("an invalid spec must not yield a next run")
	}
}

// FuzzNextRun asserts for any valid spec that the result is strictly in the future and
// stable when fed back in: a wrong time is the bug that matters, not a panic.
func FuzzNextRun(f *testing.F) {
	f.Add(0, 6, 2, 30, 15, 1)
	f.Add(3, 1, 23, 59, 31, 64)
	// A minutely seed, so the frequency's arithmetic is in the seed corpus.
	f.Add(4, 15, 0, 7, 1, 0)
	f.Add(4, 50, 0, 59, 1, 0)
	f.Fuzz(func(t *testing.T, freqSel, interval, hour, minute, monthDay, weekdayBits int) {
		freqs := []Freq{FreqHourly, FreqDaily, FreqWeekly, FreqMonthly, FreqMinutely}
		if freqSel < 0 {
			freqSel = -freqSel
		}
		if weekdayBits < 0 {
			weekdayBits = -weekdayBits
		}
		var wd []int
		for d := range 7 {
			if weekdayBits&(1<<d) != 0 {
				wd = append(wd, d)
			}
		}
		s := Spec{
			Freq:     freqs[freqSel%len(freqs)],
			Interval: interval,
			Hour:     hour,
			Minute:   minute,
			MonthDay: monthDay,
			Weekdays: wd,
		}
		if s.Validate() != nil {
			t.Skip("spec does not validate")
		}
		base := at(2026, time.August, 4, 12, 0)
		first, err := NextRun(s, base)
		if err != nil {
			t.Fatalf("a validated spec must have a next run: %v", err)
		}
		if !first.After(base) {
			t.Errorf("next run %v is not after %v", first, base)
		}
		second, err := NextRun(s, first)
		if err != nil {
			t.Fatalf("second NextRun: %v", err)
		}
		if !second.After(first) {
			t.Errorf("next run did not advance: %v then %v", first, second)
		}
	})
}
