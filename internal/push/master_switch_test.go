package push

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// seedConfig writes a config.json into a fresh dir and returns it. An empty body
// leaves the volume with no file at all, which is the fresh-install state.
func seedConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}
	return dir
}

// prefsOf snapshots the service's live preference map.
func prefsOf(s *Service) map[marotte.PushKind]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[marotte.PushKind]bool, len(s.prefs))
	maps.Copy(out, s.prefs)
	return out
}

// The state below is exactly what turning the master off produces: the client
// PATCHes that one key, so the per-kind keys keep their values, and absent means
// each kind's own default (two of the three ON).
func TestLoadPreferences_HonoursTheMasterSwitch(t *testing.T) {
	s := New(t.Context(), seedConfig(t, `{"notifications_enabled":false}`), testSubject)
	t.Cleanup(s.Close)

	for kind, on := range prefsOf(s) {
		if on {
			t.Errorf("%s is on with the master switch off; the resolved toggles ignore the master switch", kind)
		}
	}
}

// TestLoadPreferences_OnlyAnExplicitFalseRefuses is the polarity half: it stops
// the master switch silencing every workspace that has never opened Settings. This key's default is OFF ("the reader has not opted in") while each
// kind carries its own declared default, so an absent master is not a decision —
// and a value that will not parse is not a request for silence either.
func TestLoadPreferences_OnlyAnExplicitFalseRefuses(t *testing.T) {
	// agent_finished is the probe: settings.DefaultNotifyAgentFinished is on, so a
	// reading of "off" here can only have come from the master arm.
	for name, body := range map[string]string{
		"no file at all":           "",
		"empty document":           `{}`,
		"master explicitly on":     `{"notifications_enabled":true}`,
		"master an unparsed value": `{"notifications_enabled":"maybe"}`,
		"a different key off":      `{"notify_pr_status":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := New(t.Context(), seedConfig(t, body), testSubject)
			t.Cleanup(s.Close)

			if !prefsOf(s)[marotte.PushKindAgentFinished] {
				t.Errorf("agent_finished is off under %s; only an explicit false may refuse", body)
			}
		})
	}
}
