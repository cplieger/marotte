package composition

import (
	"fmt"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/toolbelt/v3"
)

const toolsManifestName = "tools.json"

// toolsManifestSaveHook is the one check-and-converge for a write of tools.json, an editor save
// and an agent's file-tool write alike: it refuses a document the engine's own parse rejects,
// and after any write lands it converges the engine on the file (toolsSlot.manifestChanged).
func toolsManifestSaveHook(tools *toolsSlot) filebrowse.SaveHook {
	return filebrowse.SaveHook{
		Check: func(content []byte) error {
			if _, err := toolbelt.ParseManifest(content); err != nil {
				return fmt.Errorf("tools.json was not saved: %w", err)
			}
			return nil
		},
		Saved: tools.manifestChanged,
	}
}

type agentWriteHooks interface {
	SetWriteHook(path string, hook agent.WriteHook)
}

// wireToolsManifestHook registers toolsManifestSaveHook on the agent's file tools and returns it
// as the editor's option, so the two writers of tools.json share one hook.
func wireToolsManifestHook(tools *toolsSlot, agentWrites agentWriteHooks) filebrowse.Option {
	hook := toolsManifestSaveHook(tools)
	agentWrites.SetWriteHook(tools.manifest, agent.WriteHook{Check: hook.Check, Saved: hook.Saved})
	return filebrowse.WithSaveHook(tools.manifest, hook)
}
