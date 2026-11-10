package agent

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func TestSettingsWritten_AProfileKeyAlsoBroadcastsPermissionsChanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		want bool
	}{
		{name: "security_profile among the keys", keys: []string{settings.KeyTheme, settings.KeySecurityProfile}, want: true},
		{name: "nil keys, either side unparseable", keys: nil, want: true},
		{name: "theme alone", keys: []string{settings.KeyTheme}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := reopenFixture(t)
			since := h.bus.fanout.Position().Head

			h.SettingsWritten(t.Context(), tc.keys)

			got := slices.Contains(broadcastTypes(h, since), marotte.EventPermissionsChanged)
			if got != tc.want {
				t.Errorf("SettingsWritten(%v) broadcast %s = %v, want %v", tc.keys, marotte.EventPermissionsChanged, got, tc.want)
			}
		})
	}
}
