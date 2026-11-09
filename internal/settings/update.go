package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/memlimit"
	"github.com/cplieger/marotte/internal/nodeopts"
	"github.com/cplieger/marotte/internal/ownedfile"
)

// ErrUnreadable marks a failure about the STORED document: config.json could not be read or
// parsed, or was refused and has not been read again, so the remedy is the file itself.
var ErrUnreadable = errors.New("settings: stored document unreadable")

// errNoLaterStamp refuses a write when the stored version and this process's last one are both
// at maxStamp, so no version a page orders after them is left.
var errNoLaterStamp = fmt.Errorf("settings: no version above settings_epoch and settings_revision %d is left; "+
	"remove both keys from config.json and restart marotte", uint64(maxStamp))

// Update applies fn to the stored document and writes it back atomically under ONE per-
// configDir lock across read, merge and write, so concurrent writers drop no key. It merges
// over what a reader reads (ownedfile.Merging): an unreadable document refuses (ErrUnreadable),
// since merging over an empty map would destroy every key fn does not name, and so does a held
// absence whose last adopted read was refused. Each write stamps the document with the next
// version (cache.next), whatever fn put there, and refuses when none is left; a refusal writes
// nothing. publish, when not nil, gets the write's settings_updated event before the lock is
// released, so events leave in version order. The map fn edited, as written, is returned.
func Update(ctx context.Context, configDir string, fn func(doc map[string]json.RawMessage) error, publish func(context.Context, marotte.ServerEvent)) (map[string]json.RawMessage, error) {
	if configDir == "" {
		return nil, errors.New("settings: no config dir")
	}
	c := getCache(configDir)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	path, err := filepath.Abs(filepath.Join(configDir, filename))
	if err != nil {
		return nil, err
	}
	s, err := c.storedBytes(path, ownedfile.Merging)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	next, ok := c.next(stampOf(s.doc), time.Now())
	if !ok {
		return nil, errNoLaterStamp
	}
	if mErr := fn(s.doc); mErr != nil {
		return nil, mErr
	}
	written, err := c.writeLocked(ctx, path, s.doc, next)
	if err != nil {
		return nil, err
	}
	c.fold(s.base, written)
	if publish != nil {
		publish(ctx, next.event())
	}
	return s.doc, nil
}

// writeLocked stamps doc with next and writes it as config.json: every write of the file goes
// through it. writeMu must be held.
func (c *cache) writeLocked(ctx context.Context, path string, doc map[string]json.RawMessage, next stamp) ([]byte, error) {
	next.write(doc)
	written, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	written = append(written, '\n')
	res, err := filesafe.WriteFile(ctx, path, written, filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700))
	if err != nil {
		return nil, err
	}
	if !res.Durable {
		slog.Warn("settings: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	c.committed = next
	c.raise(next)
	c.gate.Reset()
	c.forget()
	return written, nil
}

// fold moves the applied record to a write's bytes when the write built on it. A write over a
// document the record has not adopted leaves it, so the next Poll still reports the outside
// change the write folded in. writeMu must be held.
func (c *cache) fold(base ownedfile.Read, written []byte) {
	if a := c.appliedRecord(); a.set && a.read != base {
		return
	}
	c.setApplied(ownedfile.Written(written), written)
}

// validateCompactionPatch refuses a patch whose compaction keys the UI cannot
// produce: auto_compaction_enabled must be a bool and auto_compact_pct a
// ValidAutoCompactPct integer. A patch that carries neither key passes.
func validateCompactionPatch(patch map[string]json.RawMessage) error {
	if raw, ok := patch[KeyAutoCompactionEnabled]; ok {
		var b bool
		if decodeInto(&b, raw) != nil {
			return fmt.Errorf("%s must be true or false", KeyAutoCompactionEnabled)
		}
	}
	if raw, ok := patch[KeyAutoCompactPct]; ok {
		var pct int
		if decodeAutoCompactPct(&pct, raw) != nil {
			return errAutoCompactPctRange
		}
	}
	return nil
}

// storedRead is one uncached read of config.json for a writer: the bytes found on disk (nil
// unless a regular file), the read the document was built from, and the document, which its
// caller owns.
type storedRead struct {
	doc  map[string]json.RawMessage
	disk []byte
	base ownedfile.Read
}

// storedBytes reads config.json once and answers by rule (ownedfile.Reading for a reader,
// Merging for a merge write), with the read cache's answers: the applied document while a read
// is held, an empty map for an absence, and an error for refused bytes. disk is set on error too.
func (c *cache) storedBytes(path string, rule func(r, applied ownedfile.Read, have bool) ownedfile.Source) (storedRead, error) {
	r, data, _, readErr := classify(path)
	a := c.appliedRecord()
	s := storedRead{disk: data, base: r}
	switch rule(r, a.read, a.set) {
	case ownedfile.Disk:
		doc, err := parseObject(data)
		s.doc = doc
		return s, err
	case ownedfile.Applied:
		doc, err := parseObject(a.data)
		s.doc, s.base = doc, a.read
		return s, err
	case ownedfile.Seed:
		s.doc = map[string]json.RawMessage{}
		return s, nil
	}
	switch {
	case data != nil:
		return s, readErr
	case a.data != nil && a.read.Kind == ownedfile.Unreadable:
		_, err := parseObject(a.data)
		return s, fmt.Errorf("%s holds no document, and its last adopted read was refused: %w", path, err)
	case readErr == nil:
		return s, fmt.Errorf("%s is absent and its last adopted read was refused", path)
	}
	return s, readErr
}

// Stored reads configDir's config.json uncached, for a caller that needs the whole document: the
// settings GET and Announce. It answers as Read does: the applied document while an absent or
// unreadable read is held, an empty map for an adopted absence, an error for refused bytes. A FIFO
// is refused without blocking, since under Update's lock it would wedge every later write.
func Stored(configDir string) (map[string]json.RawMessage, error) {
	if configDir == "" {
		return nil, errors.New("settings: no config dir")
	}
	path, err := filepath.Abs(filepath.Join(configDir, filename))
	if err != nil {
		return nil, err
	}
	s, err := getCache(configDir).storedBytes(path, ownedfile.Reading)
	if err != nil {
		return nil, err
	}
	return s.doc, nil
}

// The bounds of KeyEngineHeapMiB's nonzero values, and the headroom a container limit keeps beyond the heap for the
// engine's native memory.
const (
	minEngineHeapMiB      = 1024
	maxEngineHeapMiB      = 65536
	engineHeapHeadroomMiB = 512
)

// nodeOptionsEnv and containerLimit are what the heap check composes with; vars for tests.
var (
	nodeOptionsEnv = func() string { return os.Getenv("NODE_OPTIONS") }
	containerLimit = memlimit.Limit
)

// validateEngineHeapMiB refuses a heap limit outside its bounds, one a container limit cannot hold beside the engine's
// native memory, and a nonzero one marotte's own NODE_OPTIONS would make unappliable.
func validateEngineHeapMiB(raw json.RawMessage, nodeOptions string, limit func() (int64, string)) error {
	var n int
	if decodeInto(&n, raw) != nil {
		return fmt.Errorf("%s must be a whole number of MiB", KeyEngineHeapMiB)
	}
	if n == 0 {
		return nil
	}
	upper := maxEngineHeapMiB
	if bytes, _ := limit(); bytes > 0 {
		upper = min(upper, int(bytes>>20)-engineHeapHeadroomMiB)
	}
	if n < minEngineHeapMiB || n > upper {
		return fmt.Errorf("%s must be 0 (kiro-cli's default) or from %d to %d MiB", KeyEngineHeapMiB, minEngineHeapMiB, upper)
	}
	if _, err := nodeopts.WithHeapLimit(nodeOptions, n); err != nil {
		return fmt.Errorf("marotte's NODE_OPTIONS environment variable is malformed (%w), so the memory limit cannot be applied. "+
			"Fix the variable, or keep the limit at its default", err)
	}
	return nil
}
