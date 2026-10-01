package agent

// The agent ignore list: which ignore FILES KAS enforces for a bridge, resolved
// per spawn and pushed to every live bridge when the setting changes.
//
// marotte runs no matcher of its own here. KAS's ignore evaluators sit at the top
// of its filesystem policy check and return deny BEFORE any rule is consulted, so
// no allow rule at any scope can override them; the list is the whole of marotte's
// side, and the one door for it is a connection-scope notification.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// ignorePushTimeout bounds one bridge's ignore-list notification. A Notify is a
// stdin write rather than a round trip, so this is a wedged-pipe ceiling and not
// a work budget; bridge.Notify carries no deadline of its own.
const ignorePushTimeout = 10 * time.Second

// readAgentIgnoreFiles resolves the list marotte sends KAS and reports whether the
// settings document could be READ at all. The two callers need that apart: a fresh
// bridge has no prior value, so it takes the floor; a live bridge already has one,
// so it must be left alone.
//
// An ABSENT key is a successful read of a document that says nothing, which is the
// default (an empty user list) and therefore the floor.
func readAgentIgnoreFiles(ctx context.Context, configDir string) (files []string, ok bool) {
	list, _, err := settings.FieldStrict[[]string](ctx, configDir, settings.KeyAgentIgnoreFiles)
	if err != nil {
		slog.Warn("agent ignore files: settings unreadable; the list marotte could send is a guess",
			"key", settings.KeyAgentIgnoreFiles, "error", err)
		return []string{settings.AgentIgnoreFloor}, false
	}
	return settings.AgentIgnoreList(list), true
}

// spawnIgnoreFiles resolves StartOpts.IgnoreFiles for a bridge starting fresh.
//
// One resolver for every spawn site, so no bridge can disagree about what the
// agent may read — the shape securityPresets already holds for the policy
// presets. On an unreadable document it answers the FLOOR rather than nothing: a
// fresh connection has no prior value for KAS to keep, so sending nothing would
// leave `.kiroignore` unenforced for that session's whole life.
func spawnIgnoreFiles(ctx context.Context, configDir string) []string {
	files, _ := readAgentIgnoreFiles(ctx, configDir)
	return files
}

// PushAgentIgnoreFiles fans the ignore list out to every live bridge, for a
// settings write that touched it. The value is CONNECTION-scope in KAS and hot —
// it is pushed into each live session's policy engine with no session restart — so
// this is what makes an edit reach the chats already open.
//
// Per-bridge failures are logged and not fatal, one shared timeout, the shape
// mcpRegistry.reconnectServer already holds for a live fan-out. Notify rather than
// Call: KAS answers this notification with nothing.
//
// On an unreadable settings document it sends NOTHING and reports the document in
// the panel. Every live bridge already holds a list KAS is enforcing, and the two
// alternatives are both worse than silence: a list assembled from a document that
// could not be read is a guess, and an empty list CLEARS enforcement outright.
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
