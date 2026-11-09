package composition

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/toolbelt/v3"
)

const toolsManifestName = "tools.json"

// toolsManifestOwner registers the editor's writer of tools.json: it refuses a document the
// engine's own parse rejects, and after a save lands it converges the engine on the file
// (toolsSlot.manifestChanged). The agent's file tools never reach tools.json, because no
// workspace or additional directory may cover the config directory (validateConfig, settings.AgentDirs).
func toolsManifestOwner(tools *toolsSlot) filebrowse.Option {
	return filebrowse.WithOwner(tools.manifest, toolsManifestWriter{tools: tools})
}

type toolsManifestWriter struct{ tools *toolsSlot }

var _ filebrowse.Owner = toolsManifestWriter{}

func (o toolsManifestWriter) Replace(ctx context.Context, content []byte, check func([]byte) error) ([]byte, error) {
	if _, err := toolbelt.ParseManifest(content); err != nil {
		return nil, filebrowse.SaveRefusalError{Err: fmt.Errorf("tools.json was not saved: %w", err)}
	}
	if check != nil {
		current, err := os.ReadFile(o.tools.manifest)
		if errors.Is(err, fs.ErrNotExist) {
			current = nil
		} else if err != nil {
			return nil, err
		}
		if err := check(current); err != nil {
			return nil, err
		}
	}
	if _, err := filesafe.WriteFile(ctx, o.tools.manifest, content, filesafe.WithMode(0o644)); err != nil {
		return nil, err
	}
	o.tools.manifestChanged()
	return content, nil
}
