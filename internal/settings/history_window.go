package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultHistoryWindowHours sits under every retention that keeps a chat (the least is one day), so
// the default never needs the Days kept cap.
const (
	DefaultHistoryWindowHours = 24
	DefaultHistoryWindow      = DefaultHistoryWindowHours * time.Hour
)

// historyWindowChoices are whole days above a day, so every Days kept cap lands on a choice. The
// longest must not pass tabs.RecentHold, or a widen would reach closes the tab store already dropped.
var historyWindowChoices = []int{1, 6, 12, 24, 48, 72, 168}

// HistoryWindowChoices returns the hours history_window_hours accepts, ascending.
func HistoryWindowChoices() []int {
	return slices.Clone(historyWindowChoices)
}

// ValidHistoryWindowHours reports whether h is one of the choices.
func ValidHistoryWindowHours(h int) bool {
	return slices.Contains(historyWindowChoices, h)
}

// HistoryWindow answers how long a closed chat stays in the sidebar's History: the stored hours,
// capped at Days kept when that is above 0. A config.json that cannot be read, or an hours value
// off the choices, reads as DefaultHistoryWindow.
func HistoryWindow(ctx context.Context, configDir string) time.Duration {
	d, _, err := Read(ctx, configDir)
	if err != nil {
		return DefaultHistoryWindow
	}
	hours, found, err := Lookup[int](d, KeyHistoryWindowHours)
	if err != nil || !found || !ValidHistoryWindowHours(hours) {
		hours = DefaultHistoryWindowHours
	}
	days, found, err := Lookup[int](d, KeyChatRetentionDays)
	if err != nil || !found {
		days = DefaultChatRetentionDays
	}
	return historyWindowFor(hours, days)
}

// historyWindowFor caps hours at days. At days 0 no close is listed, so there is nothing to cap.
func historyWindowFor(hours, days int) time.Duration {
	if days > 0 {
		hours = min(hours, days*24)
	}
	return time.Duration(hours) * time.Hour
}

// validateHistoryWindowPatch refuses a history_window_hours off the choices. A value above Days
// kept is accepted: it is stored and capped where it is used, so raising Days kept restores it.
func validateHistoryWindowPatch(patch map[string]json.RawMessage) error {
	raw, ok := patch[KeyHistoryWindowHours]
	if !ok {
		return nil
	}
	var h int
	if decodeChecked(&h, raw, KeyHistoryWindowHours, ValidHistoryWindowHours) != nil {
		return errHistoryWindowChoice
	}
	return nil
}

var errHistoryWindowChoice = fmt.Errorf("%s must be one of %s", KeyHistoryWindowHours, choiceList(historyWindowChoices))

// choiceList spells ns as "1, 6 or 12".
func choiceList(ns []int) string {
	words := make([]string, len(ns))
	for i, n := range ns {
		words[i] = strconv.Itoa(n)
	}
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}
