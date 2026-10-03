package agent

import (
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// replayPendingSteers emits the steer record's frames, so a reconnecting legacy
// client's dock comes back holding the messages the model has not read yet.
func (rt *Runtime) replayPendingSteers(writeFn func(marotte.ServerEvent) error) error {
	events := rt.bus.steers.List("")
	for _, evt := range events {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	if len(events) > 0 {
		// The counterpart to the client's own gap line: together they say whether an
		// emptied dock was force-emptied by a gap and refilled here, or emptied
		// because the turn ended with the steer unread.
		slog.Debug("SSE connect steer replay", "steers", len(events))
	}
	return nil
}
