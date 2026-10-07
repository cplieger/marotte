package composition

import (
	"fmt"
	"log/slog"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/toolbelt/v3"
)

const toolsManifestName = "tools.json"

// toolsManifestSaveHook refuses an editor save of tools.json that the engine's own parse rejects,
// because toolbelt.New refuses that file at the next start and the boot is fatal. A valid save
// converges the tools. A nil engine (the degraded root) keeps the check and skips the converge.
func toolsManifestSaveHook(engine *toolbelt.Engine) filebrowse.SaveHook {
	return filebrowse.SaveHook{
		Check: func(content []byte) error {
			if _, err := toolbelt.ParseManifest(content); err != nil {
				return fmt.Errorf("tools.json was not saved: %w", err)
			}
			return nil
		},
		Saved: func() {
			if engine == nil {
				return
			}
			// Missing, not Full: a hand edit asks for what it changed, never an update pass.
			// Reconcile coalesces past a full queue, so this fails only during shutdown, when
			// the boot reconcile of the next start converges instead.
			if _, _, err := engine.Reconcile(toolbelt.ReconcileMissing); err != nil {
				slog.Warn("tools: reconcile after a tools.json save not enqueued",
					"error", logsafe.Field(err.Error()))
			}
		},
	}
}
