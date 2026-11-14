package settings

import (
	"context"
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// KeyInterruptMode is what a message sent while a turn runs does, for every chat: the composer's default half and
// Enter, and a send that names no mode of its own (the notify route, an API caller). Default steer.
const KeyInterruptMode = "interrupt_mode"

const defaultInterruptMode = marotte.InterruptSteer

func decodeInterruptMode(dst *marotte.InterruptMode, raw json.RawMessage) error {
	return decodeChecked(dst, raw, KeyInterruptMode, marotte.InterruptMode.Valid)
}

// InterruptModeIn is the instance's interrupt mode in configDir: steer over an absent, unreadable or invalid value.
func InterruptModeIn(ctx context.Context, configDir string) marotte.InterruptMode {
	var m marotte.InterruptMode
	if !FieldInto(ctx, configDir, KeyInterruptMode, &m) || !m.Valid() {
		return defaultInterruptMode
	}
	return m
}
