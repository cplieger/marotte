package bridge

import (
	"testing"

	"github.com/cplieger/marotte/internal/testsupport"
)

// kiro-cli replaces its own executable on exit unless KIRO_NO_AUTO_UPDATE=1 or its HOME's app.disableAutoupdates is
// set, so the engine carries the variable whatever HOME and parent environment it starts under.
func TestProcessEnv_TurnsOffKiroCLISelfUpdate(t *testing.T) {
	t.Run("an env -i parent and no overlay, as the unpinned runtime starts it", func(t *testing.T) {
		testsupport.EmptyEnv(t)
		e := NewEngine("kiro-cli", t.TempDir())
		e.lifecycleCtx = t.Context()
		if v, ok := envValue(e.processEnv(), "KIRO_NO_AUTO_UPDATE"); v != "1" {
			t.Errorf("KIRO_NO_AUTO_UPDATE = %q (set %v), want 1", v, ok)
		}
	})
	t.Run("an inherited and an overlaid opt-out", func(t *testing.T) {
		t.Setenv("KIRO_NO_AUTO_UPDATE", "0")
		e := NewEngine("kiro-cli", t.TempDir(), WithEnv([]string{"KIRO_NO_AUTO_UPDATE=0"}))
		e.lifecycleCtx = t.Context()
		if v, ok := envValue(e.processEnv(), "KIRO_NO_AUTO_UPDATE"); v != "1" {
			t.Errorf("KIRO_NO_AUTO_UPDATE = %q (set %v), want 1", v, ok)
		}
	})
}
