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

// defaultHistoryWindowHours sits under every retention that keeps a chat (the least is one day), so
// the default never needs the Days kept cap.
const (
	defaultHistoryWindowHours = 24
	DefaultHistoryWindow      = defaultHistoryWindowHours * time.Hour
)

// The longest choice must not pass tabs.RecentHold, or a widen would reach closes the tab store
// already dropped.
var historyWindowChoices = []int{1, 6, 12, 24, 48, 72, 168}

func validHistoryWindowHours(h int) bool {
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
	if err != nil || !found || !validHistoryWindowHours(hours) {
		hours = defaultHistoryWindowHours
	}
	days, found, err := Lookup[int](d, KeyChatRetentionDays)
	if err != nil || !found {
		days = DefaultChatRetentionDays
	}
	return historyWindowFor(hours, days)
}

// historyWindowFor caps hours at days when days is above 0. Days 0 caps nothing: the client hides the
// section there, and the held closes must come back when Days kept rises again.
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
	if decodeChecked(&h, raw, KeyHistoryWindowHours, validHistoryWindowHours) != nil {
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
