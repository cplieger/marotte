package settings

import (
	"context"
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

// revisionOf reads doc's KeySettingsRevision; an absent or malformed value is 0, so the next
// write restarts the count at 1.
func revisionOf(doc map[string]json.RawMessage) uint64 {
	var n uint64
	if raw, ok := doc[KeySettingsRevision]; !ok || decodeInto(&n, raw) != nil {
		return 0
	}
	return n
}

// Revision is the stored KeySettingsRevision, 0 when config.json is absent, unreadable or has
// none.
func Revision(ctx context.Context, configDir string) uint64 {
	n, _ := Field[uint64](ctx, configDir, KeySettingsRevision)
	return n
}

// UpdatedEvent is the settings_updated event, carrying the revision stored when it is built.
func UpdatedEvent(ctx context.Context, configDir string) marotte.ServerEvent {
	return marotte.NewEvent(marotte.EventSettingsUpdated, "",
		marotte.SettingsUpdatedPayload{SettingsRevision: Revision(ctx, configDir)})
}
