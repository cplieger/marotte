package schedule

import (
	"testing"
	"time"
)

// TestNextRunFromNeverResolvesIntoThePast pins that a floored answer is in the future for
// every frequency, whatever the anchor: a week-old anchor would otherwise name a past slot.
func TestNextRunFromNeverResolvesIntoThePast(t *testing.T) {
	now := at(2026, time.August, 12, 14, 37)
	tests := []struct {
		name string
		spec Spec
	}{
		{"hourly", Spec{Freq: FreqHourly, Interval: 6, Minute: 15}},
		{"daily", Spec{Freq: FreqDaily, Hour: 2, Minute: 30}},
		{"weekly", Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Monday)}, Hour: 9}},
		{"monthly", Spec{Freq: FreqMonthly, MonthDay: 1, Hour: 3}},
		{"monthly on the last day", Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, staleBy := range []time.Duration{
				time.Hour,
				48 * time.Hour,
				7 * 24 * time.Hour,
				90 * 24 * time.Hour,
			} {
				anchor := now.Add(-staleBy)
				got, err := NextRunFrom(tt.spec, anchor, now)
				if err != nil {
					t.Errorf("stale by %s: NextRunFrom: %v", staleBy, err)
					continue
				}
				if !got.After(now) {
					t.Errorf("stale by %s: got %s, which is not after now (%s)", staleBy, got, now)
				}
				// The floored answer is the first slot after now, not one further out.
				want, err := NextRun(tt.spec, now)
				if err != nil {
					t.Fatalf("NextRun: %v", err)
				}
				if !got.Equal(want) {
					t.Errorf("stale by %s: got %s, want the first slot after now %s", staleBy, got, want)
				}
			}
		})
	}
}

// TestNextRunFromKeepsTheAnchorAsOrigin pins that a floor before the anchor cannot drag a
// just-fired schedule back to an earlier slot.
func TestNextRunFromKeepsTheAnchorAsOrigin(t *testing.T) {
	spec := Spec{Freq: FreqDaily, Hour: 2, Minute: 0}
	anchor := at(2026, time.August, 12, 3, 0) // fired late, past today's slot
	floor := at(2026, time.August, 12, 1, 0)  // before the anchor

	got, err := NextRunFrom(spec, anchor, floor)
	if err != nil {
		t.Fatalf("NextRunFrom: %v", err)
	}
	if want := at(2026, time.August, 13, 2, 0); !got.Equal(want) {
		t.Errorf("got %s, want %s (the slot after the anchor, not after the floor)", got, want)
	}
}

// TestNextRunFromZeroFloorIsTheRawSlot pins that a zero floor returns a past slot: sweep's
// fire and skip branches both read it.
func TestNextRunFromZeroFloorIsTheRawSlot(t *testing.T) {
	spec := Spec{Freq: FreqDaily, Hour: 2, Minute: 0}
	anchor := at(2026, time.August, 5, 2, 0)
	wantDue := at(2026, time.August, 6, 2, 0) // long past by any real "now"

	got, err := NextRunFrom(spec, anchor, time.Time{})
	if err != nil {
		t.Fatalf("NextRunFrom: %v", err)
	}
	if !got.Equal(wantDue) {
		t.Errorf("got %s, want the unfloored slot %s", got, wantDue)
	}
	raw, err := NextRun(spec, anchor)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !got.Equal(raw) {
		t.Errorf("zero floor changed the runner's answer: got %s, NextRun says %s", got, raw)
	}
}

// TestNextRunFromPassesValidationErrorsThrough pins that an invalid spec's error is passed
// through rather than becoming a zero time rendered as a real date.
func TestNextRunFromPassesValidationErrorsThrough(t *testing.T) {
	now := at(2026, time.August, 12, 14, 0)
	if _, err := NextRunFrom(Spec{Freq: "yearly"}, now, now); err == nil {
		t.Error("expected an error for an unknown frequency")
	}
	if _, err := NextRunFrom(Spec{Freq: FreqWeekly, Hour: 9}, now, now); err == nil {
		t.Error("expected an error for a weekly spec with no weekdays")
	}
}
