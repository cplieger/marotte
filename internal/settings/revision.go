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

// maxStamp bounds both fields to JavaScript's Number.MAX_SAFE_INTEGER: pages compare them as
// numbers, and a larger value rounds onto its neighbour.
const maxStamp = 1<<53 - 1

// decodeStampField decodes one stamp field, refusing a value above maxStamp.
func decodeStampField(dst *uint64, raw json.RawMessage) error {
	var v uint64
	if err := decodeInto(&v, raw); err != nil {
		return err
	}
	if v > maxStamp {
		return fmt.Errorf("settings: version field %d is above %d", v, uint64(maxStamp))
	}
	*dst = v
	return nil
}

// stampOf reads doc's KeySettingsEpoch and KeySettingsRevision; an absent or malformed value,
// one above maxStamp included, is 0.
func stampOf(doc map[string]json.RawMessage) stamp {
	var s stamp
	if raw, ok := doc[KeySettingsEpoch]; ok {
		_ = decodeStampField(&s.epoch, raw)
	}
	if raw, ok := doc[KeySettingsRevision]; ok {
		_ = decodeStampField(&s.revision, raw)
	}
	return s
}

// next is the stamp a write over a document stamped stored commits at now, or false when no later
// stamp fits under maxStamp. It continues the count only over the stamp this process last wrote
// (c.committed). Otherwise, as on a process's first write or over a file replaced since, it starts
// an epoch above both, in milliseconds: a count read back from a restored copy could repeat one a
// page holds. With the epoch saturated by a hand-edited stamp, the count continues above both.
func (c *cache) next(stored stamp, now time.Time) (stamp, bool) {
	if stored.epoch != 0 && stored.revision != 0 && stored == c.committed {
		return stored.successor()
	}
	if epoch := max(uint64(max(now.UnixMilli(), 0)), stored.epoch+1, c.committed.epoch+1); epoch <= maxStamp {
		return stamp{epoch: epoch, revision: 1}, true
	}
	if stored.below(c.committed) {
		return c.committed.successor()
	}
	return stored.successor()
}

// successor is the next stamp after s within maxStamp: the next revision, else the next epoch's first.
func (s stamp) successor() (stamp, bool) {
	switch {
	case s.revision < maxStamp:
		return stamp{epoch: s.epoch, revision: s.revision + 1}, true
	case s.epoch < maxStamp:
		return stamp{epoch: s.epoch + 1, revision: 1}, true
	}
	return stamp{}, false
}

func (s stamp) below(than stamp) bool {
	return s.epoch < than.epoch || (s.epoch == than.epoch && s.revision < than.revision)
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
// Update's lock, so no write's event leaves between its read and its publish. With no readable
// document the stamp is 0, as a settings read answers then.
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
