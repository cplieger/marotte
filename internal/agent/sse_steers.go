package agent

import (
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// replayPendingSteers emits the steer record's frames, so a reconnecting legacy client's dock keeps the unread messages.
func (rt *Runtime) replayPendingSteers(writeFn func(marotte.ServerEvent) error) error {
	events := rt.bus.steers.list("")
	for _, evt := range events {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	if len(events) > 0 {
		// With the client's gap line, this tells a gap-emptied dock from one emptied by the turn ending.
		slog.Debug("SSE connect steer replay", "steers", len(events))
	}
	return nil
}
