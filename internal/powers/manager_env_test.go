package powers

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// kiro-cli replaces its own executable on exit unless KIRO_NO_AUTO_UPDATE=1 or its HOME's app.disableAutoupdates is
// set, so a powers run carries the variable under an `env -i` parent and over an overlay opting back in.
func TestManager_TheCLIRunsWithSelfUpdateOff(t *testing.T) {
	cases := map[string]func() []string{
		"no overlay":                   noOverlay,
		"an overlay that opts back in": func() []string { return []string{"KIRO_NO_AUTO_UPDATE=0"} },
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			testsupport.EmptyEnv(t)
			cli, seen := testsupport.EnvRecorder(t, "KIRO_NO_AUTO_UPDATE")
			m := NewManager(nil, &recordingWriter{}, func() string { return cli }, env, filepath.Join(t.TempDir(), "installed"))
			if err := m.Uninstall(t.Context(), "postman", fixedMode(ServersActive)); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if got := seen(); got != "1" {
				t.Errorf("kiro-cli saw KIRO_NO_AUTO_UPDATE %q, want 1", got)
			}
		})
	}
}
