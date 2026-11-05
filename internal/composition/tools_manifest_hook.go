package composition

import (
	"fmt"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/toolbelt/v3"
)

const toolsManifestName = "tools.json"

// toolsManifestSaveHook is the editor's check-and-converge for a save of tools.json: it refuses a
// document the engine's own parse rejects, and after a save lands it converges the engine on the
// file (toolsSlot.manifestChanged). The agent's file tools never reach tools.json, because no
// workspace or additional directory may cover the config directory (validateConfig, settings.AgentDirs).
func toolsManifestSaveHook(tools *toolsSlot) filebrowse.Option {
	return filebrowse.WithSaveHook(tools.manifest, filebrowse.SaveHook{
		Check: func(content []byte) error {
			if _, err := toolbelt.ParseManifest(content); err != nil {
				return fmt.Errorf("tools.json was not saved: %w", err)
			}
			return nil
		},
		Saved: tools.manifestChanged,
	})
}
