package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// stamp orders the stored document's versions, compared epoch first: revision counts Update's
// writes within one epoch, and only a later epoch counts again from 1.
type stamp struct {
	epoch    uint64
	revision uint64
}

// stampOf reads doc's KeySettingsEpoch and KeySettingsRevision; an absent or malformed value is 0.
func stampOf(doc map[string]json.RawMessage) stamp {
	var s stamp
	if raw, ok := doc[KeySettingsEpoch]; ok {
		_ = decodeInto(&s.epoch, raw)
	}
	if raw, ok := doc[KeySettingsRevision]; ok {
		_ = decodeInto(&s.revision, raw)
	}
	return s
}

// next is the stamp a write over a document stamped stored commits at now. It continues the count
// only over the stamp this process last wrote (c.committed). Otherwise, as on a process's first
// write or over a file replaced since, it starts an epoch above both, in milliseconds so a
// JavaScript number holds it: a count read back from a restored copy could repeat one a page holds.
func (c *cache) next(stored stamp, now time.Time) stamp {
	if stored.epoch != 0 && stored.revision != 0 && stored == c.committed {
		return stamp{epoch: stored.epoch, revision: stored.revision + 1}
	}
	epoch := max(uint64(max(now.UnixMilli(), 0)), stored.epoch+1, c.committed.epoch+1)
	return stamp{epoch: epoch, revision: 1}
}

func (s stamp) write(doc map[string]json.RawMessage) {
	doc[KeySettingsEpoch] = json.RawMessage(strconv.FormatUint(s.epoch, 10))
	doc[KeySettingsRevision] = json.RawMessage(strconv.FormatUint(s.revision, 10))
}

func (s stamp) event() marotte.ServerEvent {
	return marotte.NewEvent(marotte.EventSettingsUpdated, "",
		marotte.SettingsUpdatedPayload{SettingsEpoch: s.epoch, SettingsRevision: s.revision})
}

// Announce publishes a settings_updated event stamped with the stored document's version, for a
// change to the effective settings that writes nothing, such as a kiro-cli default. It holds
// Update's lock, so the event leaves in order with every write's own. With no readable document
// the stamp is 0, as a settings read answers then.
func Announce(ctx context.Context, configDir string, publish func(context.Context, marotte.ServerEvent)) {
	if configDir == "" {
		publish(ctx, stamp{}.event())
		return
	}
	c := getCache(configDir)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	publish(ctx, readStamp(configDir).event())
}

func readStamp(configDir string) stamp {
	path, err := filepath.Abs(filepath.Join(configDir, filename))
	if err == nil {
		var doc map[string]json.RawMessage
		if doc, err = readDocument(path); err == nil {
			return stampOf(doc)
		}
	}
	slog.Warn("settings: change announced without a version", "error", fmt.Errorf("%w: %w", ErrUnreadable, err))
	return stamp{}
}
