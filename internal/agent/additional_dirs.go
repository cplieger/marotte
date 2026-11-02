package agent

import (
	"context"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/workspace"
)

func (lt *lifetime) agentDirs() settings.AgentDirs {
	return settings.AgentDirs{Workspace: lt.workDir, Config: lt.configDir}
}

// grantedDirs is the additional directories agents may reach at this moment, read per call so a
// removal revokes access at once.
func (lt *lifetime) grantedDirs(ctx context.Context) []string {
	granted, _, _ := lt.agentDirs().Granted(ctx)
	return granted
}

// sessionDirs is grantedDirs for a session door, logging each stored entry it does not grant.
func (lt *lifetime) sessionDirs(ctx context.Context) []string {
	granted, refused, err := lt.agentDirs().Granted(ctx)
	if err != nil {
		slog.Warn("additional directories not granted to agents: config unreadable",
			"key", settings.KeyAdditionalDirectories, "error", logsafe.Field(err.Error()))
	}
	for _, entry := range slices.Sorted(maps.Keys(refused)) {
		slog.Warn("additional directory not granted to agents", "key", settings.KeyAdditionalDirectories,
			"error", logsafe.Field(refused[entry]))
	}
	return granted
}

// resolveCwd is a shell command's working directory: p resolved into the workspace or a granted
// additional directory, symlinks followed. A verdict only, since the kernel resolves the path
// again at chdir.
func (lt *lifetime) resolveCwd(ctx context.Context, p string) (string, error) {
	root, rel, err := workspace.ConfineAnyAbs(append([]string{lt.workDir}, lt.grantedDirs(ctx)...), p)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, rel), nil
}
