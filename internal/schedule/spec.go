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

// freq is how often a schedule recurs.
type freq string

// The five recurrence frequencies the UI offers.
const (
	FreqMinutely freq = "minutely" // every Interval minutes, phased by Minute
	freqHourly   freq = "hourly"   // every Interval hours, at Minute past
	FreqDaily    freq = "daily"    // every day at Hour:Minute
	freqWeekly   freq = "weekly"   // on each Weekday at Hour:Minute
	freqMonthly  freq = "monthly"  // on MonthDay at Hour:Minute
)

// lastDay in MonthDay means "the last day of the month", the explicit answer to
// months that have no 29th/30th/31st.
const lastDay = -1

// The bounds on FreqMinutely's Interval (an hour or more is freqHourly's). The floor is
// derived from the runner: below it the 1-minute ticker makes every sweep due and missGrace
// cannot tell a fireable slot from the next. minRunBudget moves with this constant.
// Mirrored in static-src/schedule-types.ts (INTERVAL_BOUNDS); change both together.
const (
	minMinuteInterval = 5
	maxMinuteInterval = 59
)

const minutesPerDay = 24 * 60

// maxScanDays bounds the forward search, generously, so even a never-matching spec ends.
const maxScanDays = 400

// errNoOccurrence means the spec matches no day within the scan window. A
// validated spec cannot produce this.
var errNoOccurrence = errors.New("schedule has no next occurrence")

// Spec is one recurrence rule. Only the fields its freq uses are read, so a
// stored spec keeps the others at zero.
type Spec struct {
	Freq freq `json:"freq"`
	// Weekdays are the days freqWeekly fires on, as time.Weekday (0=Sunday).
	Weekdays []int `json:"weekdays,omitempty"`
	// Interval is the step in its freq's unit: hours for FreqHourly (1-24), minutes for
	// FreqMinutely (minMinuteInterval..maxMinuteInterval). Anchored to local midnight, so a step
	// that does not divide the day has a short final gap (every 5 hours: 00,05,10,15,20, midnight).
	Interval int `json:"interval,omitempty"`
	// MonthDay is the day freqMonthly fires on: 1-31, or LastDay. A value past
	// the end of a short month is CLAMPED to that month's last day rather than
	// skipping the month, so "day 31" still fires in February.
	MonthDay int `json:"month_day,omitempty"`
	// Hour and Minute are the local time of day. freqHourly ignores Hour and uses Minute as
	// the offset past each stepped hour; FreqMinutely uses Minute % Interval as the PHASE, so the
	// chosen minute is always a fire time.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}

// validate reports whether the spec is well-formed. Called on every write so a
// stored schedule can be trusted by the runner.
func (s Spec) validate() error {
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
	case freqHourly:
		if s.Interval < 1 || s.Interval > 24 {
			return fmt.Errorf("hourly interval %d out of range 1-24", s.Interval)
		}
		return nil
	case FreqDaily:
		return s.validateHour()
	case freqWeekly:
		return s.validateWeekly()
	case freqMonthly:
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
	if s.MonthDay != lastDay && (s.MonthDay < 1 || s.MonthDay > 31) {
		return fmt.Errorf("month day %d out of range 1-31 (or %d for last)", s.MonthDay, lastDay)
	}
	return s.validateHour()
}

func (s Spec) validateHour() error {
	if s.Hour < 0 || s.Hour > 23 {
		return fmt.Errorf("hour %d out of range 0-23", s.Hour)
	}
	return nil
}

// nextRun returns the first occurrence strictly after `after`, in after's location. It
// scans forward a day at a time, so all calendar arithmetic (month lengths, leap years,
// DST) is delegated to time.Date.
func nextRun(s Spec, after time.Time) (time.Time, error) {
	if err := s.validate(); err != nil {
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
	return time.Time{}, errNoOccurrence
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func (s Spec) matchesDay(day time.Time) bool {
	switch s.Freq {
	case FreqMinutely, freqHourly, FreqDaily:
		return true
	case freqWeekly:
		return slices.Contains(s.Weekdays, int(day.Weekday()))
	case freqMonthly:
		return day.Day() == s.monthDayIn(day)
	default:
		return false
	}
}

// monthDayIn resolves MonthDay against the length of the given day's month:
// lastDay and any overshoot both become that month's final day.
func (s Spec) monthDayIn(day time.Time) int {
	last := daysInMonth(day)
	if s.MonthDay == lastDay || s.MonthDay > last {
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

// The switch is EXHAUSTIVE: a fallback would silently degrade an unhandled frequency to daily. DST
// is delegated to time.Date; on spring-forward the list is not monotonic, which is safe because the
// out-of-order slots are already past when nextRun reaches them.
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
	case freqHourly:
		out := make([]time.Time, 0, 24/s.Interval+1)
		for h := 0; h < 24; h += s.Interval {
			out = append(out, at(h, s.Minute))
		}
		return out
	case FreqDaily, freqWeekly, freqMonthly:
		return []time.Time{at(s.Hour, s.Minute)}
	default:
		// Unreachable through the store (Put validates). Empty, not a daily fallback: nextRun then
		// reports errNoOccurrence and the runner says so.
		return nil
	}
}
