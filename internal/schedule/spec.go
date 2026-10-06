// Package schedule computes when a recurring workflow run is next due.
//
// The shape is a subset of RFC 5545's recurrence rule rather than a cron expression: it
// maps one-to-one onto the UI's choices and needs no parser. Everything is LOCAL time, by
// decision: there is no per-schedule timezone.
package schedule

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Freq is how often a schedule recurs.
type Freq string

// The five recurrence frequencies the UI offers.
const (
	FreqMinutely Freq = "minutely" // every Interval minutes, phased by Minute
	FreqHourly   Freq = "hourly"   // every Interval hours, at Minute past
	FreqDaily    Freq = "daily"    // every day at Hour:Minute
	FreqWeekly   Freq = "weekly"   // on each Weekday at Hour:Minute
	FreqMonthly  Freq = "monthly"  // on MonthDay at Hour:Minute
)

// LastDay in MonthDay means "the last day of the month", the explicit answer to
// months that have no 29th/30th/31st.
const LastDay = -1

// The bounds on FreqMinutely's Interval (an hour or more is FreqHourly's). The floor is
// derived from the runner: below it the 1-minute ticker makes every sweep due and MissGrace
// cannot tell a fireable slot from the next. minRunBudget moves with this constant.
// Mirrored in static-src/schedule-types.ts (INTERVAL_BOUNDS); change both together.
const (
	minMinuteInterval = 5
	maxMinuteInterval = 59
)

// minutesPerDay bounds the minute walk in timesOn.
const minutesPerDay = 24 * 60

// maxScanDays bounds the forward search, generously, so even a never-matching spec ends.
const maxScanDays = 400

// ErrNoOccurrence means the spec matches no day within the scan window. A
// validated spec cannot produce this.
var ErrNoOccurrence = errors.New("schedule has no next occurrence")

// Spec is one recurrence rule. Only the fields its Freq uses are read, so a
// stored spec keeps the others at zero.
type Spec struct {
	Freq Freq `json:"freq"`
	// Weekdays are the days FreqWeekly fires on, as time.Weekday (0=Sunday).
	Weekdays []int `json:"weekdays,omitempty"`
	// Interval is the step in its Freq's unit: hours for FreqHourly (1-24), minutes for
	// FreqMinutely (minMinuteInterval..maxMinuteInterval). Anchored to local midnight, so a step
	// that does not divide the day has a short final gap (every 5 hours: 00,05,10,15,20, midnight).
	Interval int `json:"interval,omitempty"`
	// MonthDay is the day FreqMonthly fires on: 1-31, or LastDay. A value past
	// the end of a short month is CLAMPED to that month's last day rather than
	// skipping the month, so "day 31" still fires in February.
	MonthDay int `json:"month_day,omitempty"`
	// Hour and Minute are the local time of day. FreqHourly ignores Hour and uses Minute as
	// the offset past each stepped hour; FreqMinutely uses Minute % Interval as the PHASE, so the
	// chosen minute is always a fire time.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}

// Validate reports whether the spec is well-formed. Called on every write so a
// stored schedule can be trusted by the runner.
func (s Spec) Validate() error {
	if s.Minute < 0 || s.Minute > 59 {
		return fmt.Errorf("minute %d out of range 0-59", s.Minute)
	}
	switch s.Freq {
	case FreqMinutely:
		// The one gate the runner trusts: Put validates every write. The form mirrors it.
		if s.Interval < minMinuteInterval || s.Interval > maxMinuteInterval {
			return fmt.Errorf("minute interval %d out of range %d-%d",
				s.Interval, minMinuteInterval, maxMinuteInterval)
		}
		return nil
	case FreqHourly:
		if s.Interval < 1 || s.Interval > 24 {
			return fmt.Errorf("hourly interval %d out of range 1-24", s.Interval)
		}
		return nil
	case FreqDaily:
		return s.validateHour()
	case FreqWeekly:
		return s.validateWeekly()
	case FreqMonthly:
		return s.validateMonthly()
	default:
		return fmt.Errorf("unknown frequency %q", s.Freq)
	}
}

func (s Spec) validateWeekly() error {
	if len(s.Weekdays) == 0 {
		return errors.New("weekly schedule needs at least one weekday")
	}
	for _, d := range s.Weekdays {
		if d < 0 || d > 6 {
			return fmt.Errorf("weekday %d out of range 0-6", d)
		}
	}
	return s.validateHour()
}

func (s Spec) validateMonthly() error {
	if s.MonthDay != LastDay && (s.MonthDay < 1 || s.MonthDay > 31) {
		return fmt.Errorf("month day %d out of range 1-31 (or %d for last)", s.MonthDay, LastDay)
	}
	return s.validateHour()
}

func (s Spec) validateHour() error {
	if s.Hour < 0 || s.Hour > 23 {
		return fmt.Errorf("hour %d out of range 0-23", s.Hour)
	}
	return nil
}

// NextRun returns the first occurrence strictly after `after`, in after's location. It
// scans forward a day at a time, so all calendar arithmetic (month lengths, leap years,
// DST) is delegated to time.Date.
func NextRun(s Spec, after time.Time) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	day := startOfDay(after)
	for i := range maxScanDays {
		d := day.AddDate(0, 0, i)
		if !s.matchesDay(d) {
			continue
		}
		for _, t := range s.timesOn(d) {
			if t.After(after) {
				return t, nil
			}
		}
	}
	return time.Time{}, ErrNoOccurrence
}

// startOfDay is local midnight on t's date.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// matchesDay reports whether the spec fires at all on the given day.
func (s Spec) matchesDay(day time.Time) bool {
	switch s.Freq {
	case FreqMinutely, FreqHourly, FreqDaily:
		return true
	case FreqWeekly:
		return slices.Contains(s.Weekdays, int(day.Weekday()))
	case FreqMonthly:
		return day.Day() == s.monthDayIn(day)
	default:
		return false
	}
}

// monthDayIn resolves MonthDay against the length of the given day's month:
// LastDay and any overshoot both become that month's final day.
func (s Spec) monthDayIn(day time.Time) int {
	last := daysInMonth(day)
	if s.MonthDay == LastDay || s.MonthDay > last {
		return last
	}
	return s.MonthDay
}

// daysInMonth uses time.Date's normalization: day 0 of the NEXT month is the
// last day of this one, which is correct for February in a leap year without
// knowing anything about leap years.
func daysInMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// timesOn returns the fire times on a day the spec matches, in order. The switch is
// EXHAUSTIVE: a fallback would silently degrade an unhandled frequency to daily. DST is
// delegated to time.Date; on spring-forward the list is not monotonic, which is safe
// because the out-of-order slots are already past when NextRun reaches them.
func (s Spec) timesOn(day time.Time) []time.Time {
	at := func(h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
	}
	switch s.Freq {
	case FreqMinutely:
		out := make([]time.Time, 0, minutesPerDay/s.Interval+1)
		for md := s.Minute % s.Interval; md < minutesPerDay; md += s.Interval {
			out = append(out, at(md/60, md%60))
		}
		return out
	case FreqHourly:
		out := make([]time.Time, 0, 24/s.Interval+1)
		for h := 0; h < 24; h += s.Interval {
			out = append(out, at(h, s.Minute))
		}
		return out
	case FreqDaily, FreqWeekly, FreqMonthly:
		return []time.Time{at(s.Hour, s.Minute)}
	default:
		// Unreachable through the store (Put validates). Empty, not a daily fallback: NextRun then
		// reports ErrNoOccurrence and the runner says so.
		return nil
	}
}
