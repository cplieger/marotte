package settings

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/tabs"
)

func TestHistoryWindow_DefaultsTo24Hours(t *testing.T) {
	d := EffectiveDefaults()
	if d.HistoryWindowHours != 24 {
		t.Errorf("EffectiveDefaults().HistoryWindowHours = %d, want 24", d.HistoryWindowHours)
	}
	if want := []int{1, 6, 12, 24, 48, 72, 168}; !slices.Equal(d.HistoryWindowChoices, want) {
		t.Errorf("EffectiveDefaults().HistoryWindowChoices = %v, want %v", d.HistoryWindowChoices, want)
	}
	if got := HistoryWindow(t.Context(), t.TempDir()); got != 24*time.Hour {
		t.Errorf("HistoryWindow with no config.json = %v, want 24h", got)
	}
}

func TestHistoryWindow_StoredValuesAndBounds(t *testing.T) {
	tests := []struct {
		stored string
		want   int
		reject bool
	}{
		{stored: `1`, want: 1},
		{stored: `6`, want: 6},
		{stored: `12`, want: 12},
		{stored: `24`, want: 24},
		{stored: `48`, want: 48},
		{stored: `72`, want: 72},
		{stored: `168`, want: 168},
		{stored: `0`, want: 24, reject: true},
		{stored: `2`, want: 24, reject: true},
		{stored: `169`, want: 24, reject: true},
		{stored: `-1`, want: 24, reject: true},
		{stored: `2.5`, want: 24, reject: true},
		{stored: `"24"`, want: 24, reject: true},
		{stored: `null`, want: 24, reject: true},
	}
	for _, tc := range tests {
		t.Run(tc.stored, func(t *testing.T) {
			doc := map[string]json.RawMessage{KeyHistoryWindowHours: json.RawMessage(tc.stored)}
			got, rejected := EffectiveFrom(doc)
			if got.HistoryWindowHours != tc.want {
				t.Errorf("EffectiveFrom(%s).HistoryWindowHours = %d, want %d", tc.stored, got.HistoryWindowHours, tc.want)
			}
			if gotReject := slices.Equal(rejected, []string{KeyHistoryWindowHours}); gotReject != tc.reject {
				t.Errorf("EffectiveFrom(%s) rejected = %v, want %s rejected %v", tc.stored, rejected, KeyHistoryWindowHours, tc.reject)
			}
			dir := t.TempDir()
			writeSettings(t, dir, []byte(fmt.Sprintf(`{%q:%s}`, KeyHistoryWindowHours, tc.stored)))
			if got := HistoryWindow(t.Context(), dir); got != time.Duration(tc.want)*time.Hour {
				t.Errorf("HistoryWindow over %s = %v, want %dh", tc.stored, got, tc.want)
			}
			if err := validateHistoryWindowPatch(doc); (err != nil) != tc.reject {
				t.Errorf("validateHistoryWindowPatch(%s) = %v, want error %v", tc.stored, err, tc.reject)
			}
		})
	}
	t.Run("a patch without the key", func(t *testing.T) {
		if err := validateHistoryWindowPatch(map[string]json.RawMessage{KeyTheme: json.RawMessage(`"dark"`)}); err != nil {
			t.Errorf("validateHistoryWindowPatch(no window key) = %v, want nil", err)
		}
	})
	t.Run("the refusal names every choice", func(t *testing.T) {
		err := validateHistoryWindowPatch(map[string]json.RawMessage{KeyHistoryWindowHours: json.RawMessage(`5`)})
		const want = "history_window_hours must be one of 1, 6, 12, 24, 48, 72 or 168"
		if err == nil || err.Error() != want {
			t.Errorf("validateHistoryWindowPatch(5) = %v, want %q", err, want)
		}
	})
}

func TestHistoryWindow_CappedByChatRetention(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		want   time.Duration
	}{
		{name: "a week under keep forever", stored: `{"history_window_hours":168,"chat_retention_days":-1}`, want: 168 * time.Hour},
		{name: "a week under seven days", stored: `{"history_window_hours":168,"chat_retention_days":7}`, want: 168 * time.Hour},
		{name: "a week under two days", stored: `{"history_window_hours":168,"chat_retention_days":2}`, want: 48 * time.Hour},
		{name: "two days under one day", stored: `{"history_window_hours":48,"chat_retention_days":1}`, want: 24 * time.Hour},
		{name: "an hour under one day", stored: `{"history_window_hours":1,"chat_retention_days":1}`, want: time.Hour},
		{name: "a week under the default retention", stored: `{"history_window_hours":168}`, want: 168 * time.Hour},
		{name: "no cap while a close deletes", stored: `{"history_window_hours":72,"chat_retention_days":0}`, want: 72 * time.Hour},
		{name: "the default under one day", stored: `{"chat_retention_days":1}`, want: 24 * time.Hour},
		{name: "an unreadable file", stored: `{`, want: 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSettings(t, dir, []byte(tc.stored))
			if got := HistoryWindow(t.Context(), dir); got != tc.want {
				t.Errorf("HistoryWindow over %s = %v, want %v", tc.stored, got, tc.want)
			}
		})
	}
}

// The tab store holds a close for tabs.RecentHold, so a longer choice would widen onto closes it already dropped.
func TestHistoryWindow_TheLongestChoiceFitsTheTabStoresHold(t *testing.T) {
	if longest := time.Duration(slices.Max(EffectiveDefaults().HistoryWindowChoices)) * time.Hour; longest > tabs.RecentHold {
		t.Errorf("the longest history window choice is %v, past the tab store's hold of %v", longest, tabs.RecentHold)
	}
}
