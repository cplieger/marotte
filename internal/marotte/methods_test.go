package marotte

import (
	"reflect"
	"testing"
)

func TestTerminalSettingsParams_OmitsTheTimeoutWhenUnset(t *testing.T) {
	tests := []struct {
		ms   int
		want map[string]any
	}{
		{ms: 0, want: map[string]any{"terminal": map[string]any{"enabled": false}}},
		{ms: 30000, want: map[string]any{"terminal": map[string]any{"enabled": true, "commandTimeoutMs": 30000}}},
	}
	for _, tc := range tests {
		if got := TerminalSettingsParams(tc.ms); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("TerminalSettingsParams(%d) = %v, want %v", tc.ms, got, tc.want)
		}
	}
}
