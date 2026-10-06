package agent

// KAS's ignore evaluators deny before any allow rule is consulted, so marotte runs no
// matcher: it only sends the list, over a connection-scope notification.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// ignorePushTimeout is a wedged-pipe ceiling for one Notify; bridge.Notify carries no deadline.
const ignorePushTimeout = 10 * time.Second

// readAgentIgnoreFiles resolves the list sent to KAS and reports whether the settings
// document could be read. An absent key is a successful read meaning the floor.
func readAgentIgnoreFiles(ctx context.Context, configDir string) (files []string, ok bool) {
	list, _, err := settings.FieldStrict[[]string](ctx, configDir, settings.KeyAgentIgnoreFiles)
	if err != nil {
		slog.Warn("agent ignore files: settings unreadable; the list marotte could send is a guess",
			"key", settings.KeyAgentIgnoreFiles, "error", err)
		return []string{settings.AgentIgnoreFloor}, false
	}
	return settings.AgentIgnoreList(list), true
}

// spawnIgnoreFiles resolves StartOpts.IgnoreFiles for every spawn site. An unreadable
// document yields the FLOOR: sending nothing would leave `.kiroignore` unenforced for the session.
func spawnIgnoreFiles(ctx context.Context, configDir string) []string {
	files, _ := readAgentIgnoreFiles(ctx, configDir)
	return files
}

// PushAgentIgnoreFiles fans the ignore list out to every live bridge after a settings
// write; KAS applies it hot. Per-bridge failures are logged. On an unreadable document it
// sends NOTHING and reports it: an empty list would CLEAR enforcement in KAS.
func (rt *Runtime) PushAgentIgnoreFiles(ctx context.Context) {
	bridges := rt.bridge.mgr.all()
	if len(bridges) == 0 {
		return
	}
	files, ok := readAgentIgnoreFiles(ctx, rt.lifecycle.configDir)
	if !ok {
		slog.Error("agent ignore files not pushed; kiro-cli keeps enforcing the previous list",
			"key", settings.KeyAgentIgnoreFiles, "bridges", len(bridges))
		rt.Broadcast(ctx, marotte.NewEvent(marotte.EventPolicyError, "", marotte.PolicyErrorPayload{
			Errors: []marotte.PolicyErrorItem{{
				Source:  settings.Filename,
				Message: "The agent ignore file list could not be read, so kiro-cli keeps enforcing the previous one.",
			}},
		}))
		return
	}

	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for chatID, sb := range bridges {
		wg.Go(func() {
			if err := sb.Notify(cctx, marotte.MethodPolicyIgnoreFilesChanged, map[string]any{
				marotte.ParamIgnoreFiles: files,
			}); err != nil {
				slog.Warn("agent ignore files: bridge notify failed",
					"chat_id", chatID, "files", files, "error", err)
			}
		})
	}
	wg.Wait()
}
