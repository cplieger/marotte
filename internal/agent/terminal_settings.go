package agent

import (
	"context"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// PushTerminalSettings sends the shell-tool default timeout to every live chat and run bridge after a settings
// write; KAS holds it per process. A spawning bridge refuses the push and reads it itself
// (StartOpts.TerminalTimeout). Failures are logged.
func (rt *Runtime) PushTerminalSettings(ctx context.Context) {
	bridges := rt.bridge.mgr.all()
	if len(bridges) == 0 {
		return
	}
	params := marotte.TerminalSettingsParams(terminalCommandTimeoutMs(ctx, rt.lifecycle.configDir))

	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for chatID, sb := range bridges {
		wg.Go(func() {
			if err := sb.Notify(cctx, marotte.MethodTerminalSettingsChanged, params); err != nil {
				slog.Warn("terminal settings: bridge notify failed",
					"chat_id", chatID, "key", settings.KeyTerminalCommandTimeoutMs, "error", err)
			}
		})
	}
	wg.Wait()
}
