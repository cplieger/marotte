package rule

import (
	"slices"
	"strconv"
	"strings"
)

// The words placeholders are formatted with; the catalogue publishes them so the client formats the same way.
var words = Words{
	Days:       []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	DaysPlural: []string{"Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"},
	Minute:     "min", Hour: "h", Day: "d", Weekdays: "weekdays", EveryDay: "every day", LastDay: "the last day",
}

// fillTemplate replaces each {name} in template; pairs alternate a name and its value.
func fillTemplate(template string, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		template = strings.ReplaceAll(template, "{"+pairs[i]+"}", pairs[i+1])
	}
	return template
}

// formatMinutes writes a duration in days, hours and minutes, leaving out each zero unit: "1 h 30 min", "2 d".
func formatMinutes(m int) string {
	if m <= 0 {
		return "0 " + words.Minute
	}
	var parts []string
	for _, u := range []struct {
		word string
		size int
	}{{words.Day, 24 * 60}, {words.Hour, 60}, {words.Minute, 1}} {
		if n := m / u.size; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+u.word)
			m -= n * u.size
		}
	}
	return strings.Join(parts, " ")
}

// mondayFirst orders a weekday set Monday to Sunday with no repeats.
func mondayFirst(days []int) []int {
	var has [7]bool
	for _, d := range days {
		if d >= 0 && d < 7 {
			has[d] = true
		}
	}
	var out []int
	for i := 1; i <= 7; i++ {
		if has[i%7] {
			out = append(out, i%7)
		}
	}
	return out
}

// daysPhrase is {days}: "every day" for all seven, else "on " and the days, a run of three or more as
// "Mon to Fri", the rest listed: "on Sat, Sun", "on Mon to Wed, Fri".
func daysPhrase(days []int) string {
	set := mondayFirst(days)
	if len(set) == 7 {
		return words.EveryDay
	}
	var parts []string
	for i := 0; i < len(set); {
		j := i
		for j+1 < len(set) && (set[j]+1)%7 == set[j+1] {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, words.Days[set[i]]+" to "+words.Days[set[j]])
		} else {
			for k := i; k <= j; k++ {
				parts = append(parts, words.Days[set[k]])
			}
		}
		i = j + 1
	}
	return "on " + strings.Join(parts, ", ")
}

// weekdaysPhrase is {weekdays}, an at trigger's recurrence: "every day", "weekdays" for Monday to Friday,
// "Mondays" for one day, else the set as daysPhrase reads it.
func weekdaysPhrase(days []int) string {
	set := mondayFirst(days)
	switch {
	case len(set) == 1:
		return words.DaysPlural[set[0]]
	case slices.Equal(set, []int{1, 2, 3, 4, 5}):
		return words.Weekdays
	}
	return daysPhrase(days)
}

// monthDayPhrase is {month_day}: "the last day", "the 1st", "the 22nd".
func monthDayPhrase(day int) string {
	if day == MonthDayLast {
		return words.LastDay
	}
	suffix := "th"
	if day%100 < 11 || day%100 > 13 {
		switch day % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return "the " + strconv.Itoa(day) + suffix
}

// triggerPhrase is a trigger as the editor's summary says it ("every 3 h", "Mondays at 14:00"), from the template
// the catalogue serves for its variant.
func triggerPhrase(t Trigger) string {
	def := kinds[t.Kind]
	switch {
	case t.Kind == KindEvery && t.Every != nil:
		anchor := t.Every.Anchor
		if anchor == "" {
			anchor = AnchorLast
		}
		return fillTemplate(def.phrases[string(anchor)], "minutes", formatMinutes(t.Every.Minutes))
	case t.Kind == KindAt && t.At != nil:
		return fillTemplate(def.phrases[string(t.At.Days)], "time", t.At.Time,
			paramWeekdays, weekdaysPhrase(t.At.Weekdays), "month_day", monthDayPhrase(t.At.MonthDay))
	}
	return string(t.Kind)
}

// EventPhrase is how a mark names the source of an event from trigger: the trigger's phrase, "Run now" for "", or
// the id itself for a trigger the spec no longer holds.
func EventPhrase(spec *Spec, trigger string) string {
	if trigger == "" {
		return manualPhrase
	}
	for i := range spec.Triggers {
		if spec.Triggers[i].ID == trigger {
			return triggerPhrase(spec.Triggers[i])
		}
	}
	return trigger
}
