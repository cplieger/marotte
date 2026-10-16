package agent

// KAS's ignore evaluators deny before any allow rule is consulted, so marotte runs no
// matcher: it only sends the list, over a connection-scope notification.

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// bridge.Notify carries no deadline.
const ignorePushTimeout = 10 * time.Second

// readAgentIgnoreFiles resolves the list sent to KAS and reports whether the settings
// document could be read. An absent key is a successful read meaning the floor.
func readAgentIgnoreFiles(ctx context.Context, configDir string) (files []string, ok bool) {
	list, _, err := settings.FieldStrict[[]string](ctx, configDir, settings.KeyAgentIgnoreFiles)
	if err != nil {
		return []string{settings.AgentIgnoreFloor}, false
	}
	return settings.AgentIgnoreList(list), true
}

// spawnIgnoreFiles resolves EngineOpts.IgnoreFiles for the engine spawn. An unreadable
// document yields the FLOOR: sending nothing would leave `.kiroignore` unenforced for the session.
func spawnIgnoreFiles(ctx context.Context, configDir string) []string {
	files, ok := readAgentIgnoreFiles(ctx, configDir)
	if !ok {
		slog.Warn("agent ignore files: settings unreadable; this spawn enforces only the floor",
			"key", settings.KeyAgentIgnoreFiles)
	}
	return files
}

// reportIgnoreFilesUnreadable tells the operator and the client what is enforced while the list cannot be read: the
// running engine keeps its previous list, because an empty push would CLEAR enforcement in KAS, and an engine spawned
// meanwhile gets only the floor (spawnIgnoreFiles). It reports whether it did; with no engine up there is nothing to
// report.
func (rt *Runtime) reportIgnoreFilesUnreadable(ctx context.Context) bool {
	if h, _ := rt.engine.current(); h == nil {
		return false
	}
	slog.Error("agent ignore files not pushed; the agent engine keeps its previous list, and an engine started before the file is fixed enforces only the floor",
		"key", settings.KeyAgentIgnoreFiles, "floor", settings.AgentIgnoreFloor)
	rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPolicyError, "", marotte.PolicyErrorPayload{
		Errors: []marotte.PolicyErrorItem{{
			Source: settings.Filename,
			Message: "The agent ignore file list could not be read. The agent engine keeps enforcing its previous list; " +
				"if it restarts before the file is fixed it enforces only " + settings.AgentIgnoreFloor + ".",
		}},
	}))
	return true
}
