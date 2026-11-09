package composition

import (
	"context"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// settingsReactor is what a config.json owner tells after a write: every page, and the runtime's after-write rule.
type settingsReactor interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
	SettingsWritten(ctx context.Context, keys []string)
}

var _ settingsReactor = (*agent.Runtime)(nil)

// ownedFileDeps are the owners the file browser routes an owned file's writes through.
type ownedFileDeps struct {
	Settings settingsReactor
	Tools    *toolsSlot
	Dirs     settings.AgentDirs
}

// ownedFileOptions registers every file marotte owns with the file browser.
func ownedFileOptions(d ownedFileDeps) []filebrowse.Option {
	return []filebrowse.Option{toolsManifestOwner(d.Tools)}
}
