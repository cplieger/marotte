package marotte

import (
	"encoding/json"
	"testing"
)

func TestChatThinkingIsOff_ReportThenChoiceThenModelDefault(t *testing.T) {
	tests := []struct {
		name       string
		choice     string
		active     string
		defaultOff bool
		want       bool
	}{
		{name: "choice off, no report", choice: ThinkingOff, want: true},
		{name: "choice on, no report", choice: ThinkingOn, want: false},
		{name: "no choice, no report, model default on", want: false},
		{name: "no choice, no report, model default off", defaultOff: true, want: true},
		{name: "choice on beats a default-off model", choice: ThinkingOn, defaultOff: true, want: false},
		{name: "a report of on beats a default-off model", active: ThinkingOn, defaultOff: true, want: false},
		{name: "choice off, session reports on", choice: ThinkingOff, active: ThinkingOn, want: false},
		{name: "choice on, session reports off", choice: ThinkingOn, active: ThinkingOff, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Chat{Thinking: tc.choice, ThinkingActive: tc.active}
			if got := c.ThinkingIsOff(tc.defaultOff); got != tc.want {
				t.Errorf("Chat{Thinking: %q, ThinkingActive: %q}.ThinkingIsOff(%v) = %v, want %v",
					tc.choice, tc.active, tc.defaultOff, got, tc.want)
			}
		})
	}
}

func TestCapEffortForThinkingOff_LowersOnlyTheTiersKASCaps(t *testing.T) {
	five := []SessionEffortLevel{{ID: "low"}, {ID: "medium"}, {ID: "high"}, {ID: "xhigh"}, {ID: "max"}}
	tests := []struct {
		name   string
		level  string
		levels []SessionEffortLevel
		want   string
	}{
		{name: "max caps to high", level: "max", levels: five, want: "high"},
		{name: "xhigh caps to high", level: "xhigh", levels: five, want: "high"},
		{name: "high is untouched", level: "high", levels: five, want: "high"},
		{name: "low is untouched", level: "low", levels: five, want: "low"},
		{
			name: "a shorter vocabulary caps to its own top", level: "max",
			levels: []SessionEffortLevel{{ID: "low"}, {ID: "medium"}, {ID: "max"}}, want: "medium",
		},
		{
			name: "no lower tier leaves the level", level: "max",
			levels: []SessionEffortLevel{{ID: "xhigh"}, {ID: "max"}}, want: "max",
		},
		{name: "no vocabulary leaves the level", level: "max", want: "max"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapEffortForThinkingOff(tc.level, tc.levels); got != tc.want {
				t.Errorf("CapEffortForThinkingOff(%q, %v) = %q, want %q", tc.level, tc.levels, got, tc.want)
			}
		})
	}
}

func TestModelChoiceMetaThinkingDefaultOff_AbsentMeansOn(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: `{"kiro":{}}`, want: false},
		{raw: `{"kiro":{"defaultThinkingEnabled":true}}`, want: false},
		{raw: `{"kiro":{"defaultThinkingEnabled":false}}`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			var m ModelChoiceMeta
			if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tc.raw, err)
			}
			if got := m.ThinkingDefaultOff(); got != tc.want {
				t.Errorf("ModelChoiceMeta(%s).ThinkingDefaultOff() = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
