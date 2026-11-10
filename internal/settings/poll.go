package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ownedfile"
)

// Poll checks config.json for an outside write, under Update's lock. A read ownedfile.Gate holds
// records, writes and publishes nothing. An acting valid read reports the keys that differ from
// the applied document; a settled absence writes the seed (the stamp keys alone) and reports every
// key but the stamp keys the applied document held, or stays held and is retried when the seed
// cannot be written; a settled unreadable read reports nil keys. ok means there is something to
// apply, and publish has had the change's settings_updated event by then. A document stamped
// below the highest stamp this process wrote, published or applied is first rewritten above it.
// err is a context end or a path that does not resolve.
func Poll(ctx context.Context, configDir string, publish func(context.Context, marotte.ServerEvent)) (changed []string, ok bool, err error) {
	if configDir == "" {
		return nil, false, errors.New("settings: no config dir")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	path, err := filepath.Abs(filepath.Join(configDir, filename))
	if err != nil {
		return nil, false, err
	}
	c := getCache(configDir)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	r, data, _, readErr := classify(path)
	if !c.gate.Observe(r) {
		return nil, false, nil
	}
	a := c.appliedRecord()
	if !a.set {
		c.setApplied(r, data)
		return nil, false, nil
	}
	switch {
	case r.Kind == ownedfile.Valid:
		changed, ok = c.pollValid(ctx, path, r, data, a, publish)
		return changed, ok, nil
	case r == a.read:
		return nil, false, nil
	case r.Kind == ownedfile.Absent:
		changed, ok = c.pollAbsent(ctx, path, a, publish)
		return changed, ok, nil
	}
	c.setApplied(r, data)
	c.forget()
	slog.Warn("settings: config.json cannot be read; it stays refused until it reads again", "path", path,
		"error", fmt.Errorf("%w: %w", ErrUnreadable, readErr))
	publish(ctx, stamp{}.event())
	return nil, true, nil
}

// appliedDoc is a's document for a diff: nil when a was refused, empty for an absence. A valid
// one raises c.high to its stamp, the version boot and every page fetched before a write read.
func (c *cache) appliedDoc(a applied) map[string]json.RawMessage {
	switch a.read.Kind {
	case ownedfile.Valid:
		doc, _ := parseObject(a.data)
		c.raise(stampOf(doc))
		return doc
	case ownedfile.Absent:
		return map[string]json.RawMessage{}
	}
	return nil
}

func (c *cache) pollValid(ctx context.Context, path string, r ownedfile.Read, data []byte, a applied,
	publish func(context.Context, marotte.ServerEvent),
) (changed []string, ok bool) {
	before := c.appliedDoc(a)
	doc, _ := parseObject(data)
	if before != nil {
		changed = changedKeys(before, doc)
	}
	ok = len(changed) > 0 || before == nil
	moved := r != a.read
	if moved {
		c.setApplied(r, data)
	}
	disk := stampOf(doc)
	if disk.below(c.high) {
		// A page holding c.high refuses every read below it, so the document is rewritten above.
		next, has := c.next(disk, time.Now())
		werr := errNoLaterStamp
		if has {
			written, err := c.writeLocked(ctx, path, doc, next)
			if err == nil {
				c.fold(r, written)
				publish(ctx, next.event())
				return changed, ok
			}
			werr = err
		}
		slog.Warn("settings: config.json was written below the version pages hold and could not be rewritten above it",
			"path", path, "error", werr)
	}
	if moved {
		c.forget()
	}
	if ok {
		c.raise(disk)
		publish(ctx, disk.event())
	}
	return changed, ok
}

// pollAbsent adopts a settled absence: the seed, the stamp keys alone, is written and applied.
// It answers every key but the stamp keys the applied document held, nil when that was refused.
// ok is false when the seed cannot be written: nothing is adopted, so the next poll retries.
func (c *cache) pollAbsent(ctx context.Context, path string, a applied,
	publish func(context.Context, marotte.ServerEvent),
) (keys []string, ok bool) {
	before := c.appliedDoc(a)
	next, has := c.next(stamp{}, time.Now())
	werr := errNoLaterStamp
	if has {
		written, err := c.writeLocked(ctx, path, map[string]json.RawMessage{}, next)
		if err == nil {
			if before != nil {
				keys = changedKeys(before, map[string]json.RawMessage{})
			}
			c.setApplied(ownedfile.Written(written), written)
			publish(ctx, next.event())
			return keys, true
		}
		werr = err
	}
	slog.Warn("settings: config.json was removed and its defaults could not be written back; retrying at the next poll",
		"path", path, "error", werr)
	return nil, false
}

// changedKeys answers, sorted and never nil, every key but the stamp keys whose compacted value
// differs between a and b or that one side lacks.
func changedKeys(a, b map[string]json.RawMessage) []string {
	keys := []string{}
	for k := range maps.Keys(a) {
		if _, inB := b[k]; !inB || !sameValue(a[k], b[k]) {
			keys = append(keys, k)
		}
	}
	for k := range maps.Keys(b) {
		if _, inA := a[k]; !inA {
			keys = append(keys, k)
		}
	}
	keys = slices.DeleteFunc(keys, func(k string) bool { return k == KeySettingsEpoch || k == KeySettingsRevision })
	slices.Sort(keys)
	return keys
}

func sameValue(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
