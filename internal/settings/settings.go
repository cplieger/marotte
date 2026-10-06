// Package settings is the one reader for <configDir>/config.json: Field (typed key), FieldInto
// (pointer target) and FieldStrict (unreadable kept apart from absent, for a caller gating a
// destructive action), all through one freshness-checked cache capped at MaxBytes.
//
// There is no exported raw-bytes reader: the cache's slice is shared by every caller, so an
// exported accessor would let any caller corrupt every other's settings.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/cplieger/atomicfile/v4"
	"golang.org/x/sync/singleflight"
)

// MaxBytes caps config.json reads, matching the HTTP path's webhttp.MaxJSONBody.
const MaxBytes = 1 << 20

// filename is the canonical settings file name.
const filename = "config.json"

// Filename is the basename of marotte's config file (not kiro-cli's cli.json).
const Filename = filename

// cache provides freshness-checked caching for config.json reads. Staleness is three legs,
// atomicfile.FileIdentity (mtime AND os.SameFile) plus size: neither pair alone catches both
// a same-length rename within one tick and a different-length in-place rewrite.
type cache struct {
	id        atomicfile.FileIdentity
	sfGroup   singleflight.Group
	parsed    map[string]json.RawMessage
	configDir string
	data      []byte
	parsedGen uint64
	gen       uint64
	size      int64
	mu        sync.Mutex
	// writeMu serializes Update's read-modify-write for this configDir. Separate
	// from mu, which guards the cached bytes for the length of one field access
	// and is taken inside the window this one holds.
	writeMu sync.Mutex
}

var (
	globalCacheMu sync.Mutex
	globalCaches  = map[string]*cache{}
)

func getCache(configDir string) *cache {
	globalCacheMu.Lock()
	defer globalCacheMu.Unlock()
	c, ok := globalCaches[configDir]
	if !ok {
		c = &cache{configDir: configDir}
		globalCaches[configDir] = c
	}
	return c
}

type result struct {
	err  error
	data []byte
}

func (c *cache) load() ([]byte, error) {
	v, _, _ := c.sfGroup.Do("load", func() (any, error) {
		data, err := c.reload()
		return result{data: data, err: err}, nil
	})
	//nolint:errcheck // sfGroup.Do's closure always returns a `result` value.
	r := v.(result)
	return r.data, r.err
}

// reload is the body of the singleflight slot: resolve the path, take the mtime/size fast
// path, otherwise read and cache.
func (c *cache) reload() ([]byte, error) {
	// Absolute because atomicfile.OpenRegular requires it.
	path, err := filepath.Abs(filepath.Join(c.configDir, filename))
	if err != nil {
		return nil, err
	}
	// os.Stat never blocks on a FIFO, so the fast path stays one syscall.
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			c.forget()
			return nil, nil
		}
		return nil, statErr
	}
	if cached, ok := c.hit(info); ok {
		return cached, nil
	}
	data, readInfo, err := readRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.forget()
			return nil, nil
		}
		return nil, err
	}
	// readInfo, not the stat above: the identity must describe the generation these bytes came
	// from, which a concurrent publish may already have replaced.
	c.store(data, readInfo)
	return data, nil
}

// readRegular reads path under MaxBytes, refusing anything but a regular file (and a final
// symlink), and returns the FileInfo of the descriptor read. OpenRegular because os.Open
// blocks forever on a FIFO, which inside the singleflight slot would wedge every reader.
func readRegular(path string) (data []byte, info os.FileInfo, err error) {
	f, info, err := atomicfile.OpenRegular(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	data, err = io.ReadAll(io.LimitReader(f, MaxBytes))
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// hit reports the cached bytes when info matches what they were read from; a zero identity
// reports Changed.
func (c *cache) hit(info os.FileInfo) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id.Changed(info) || info.Size() != c.size {
		return nil, false
	}
	return c.data, true
}

// store records freshly read bytes under the identity they were read at.
func (c *cache) store(data []byte, info os.FileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.id = atomicfile.Identify(info)
	c.size = info.Size()
	c.gen++
}

// forget drops the cached bytes for a vanished file and bumps the generation so parsedMap
// re-derives.
func (c *cache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = nil
	c.id = atomicfile.FileIdentity{}
	c.size = 0
	c.gen++
}

// readBytes returns the raw config.json content for configDir, cached; (nil, nil) when the
// file is missing or configDir is empty. UNEXPORTED: the slice IS the shared cache's own.
func readBytes(ctx context.Context, configDir string) ([]byte, error) {
	if configDir == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return getCache(configDir).load()
}

// errKeyParse marks the one FieldStrict failure about a single key: present, and not decoding
// into the caller's type.
var errKeyParse = errors.New("settings: parse")

// FieldStrict is Field with unreadability kept apart from absence: found reports the key was
// PRESENT and decoded; err reports config.json exists and could not be read or parsed.
func FieldStrict[T any](ctx context.Context, configDir, key string) (value T, found bool, err error) {
	var zero T
	raw, err := parsedMap(ctx, configDir)
	if err != nil {
		return zero, false, err
	}
	if raw == nil {
		return zero, false, nil // no config.json: ABSENT, and intentional
	}
	v, ok := raw[key]
	if !ok {
		return zero, false, nil // key absent: ABSENT, and intentional
	}
	var out T
	if uErr := json.Unmarshal(v, &out); uErr != nil {
		return zero, false, fmt.Errorf("%w %s: %w", errKeyParse, key, uErr)
	}
	return out, true, nil
}

// Field reads config.json and decodes the named key into T, returning the zero value and
// false when the file is missing, the key is absent, or parsing fails (logged at Warn
// naming the key). It wraps FieldStrict for callers whose fallback is a display default.
func Field[T any](ctx context.Context, configDir, key string) (T, bool) {
	out, ok, err := FieldStrict[T](ctx, configDir, key)
	switch {
	case errors.Is(err, errKeyParse):
		slog.Warn("settings: parse "+key, "error", err)
	case err != nil:
		slog.Warn("settings: read config.json for "+key, "error", err)
	}
	return out, ok
}

// FieldInto is the pointer-target variant of Field: it decodes the named key into out and
// reports success.
func FieldInto(ctx context.Context, configDir, key string, out any) bool {
	raw, err := parsedMap(ctx, configDir)
	if err != nil {
		slog.Warn("settings: read config.json for "+key, "error", err)
		return false
	}
	if raw == nil {
		return false
	}
	v, ok := raw[key]
	if !ok {
		return false
	}
	if err := json.Unmarshal(v, out); err != nil {
		slog.Warn("settings: parse "+key, "error", err)
		return false
	}
	return true
}

// parsedMap returns the cached parsed map[string]json.RawMessage for configDir, invalidated
// when the bytes change.
func parsedMap(ctx context.Context, configDir string) (map[string]json.RawMessage, error) {
	data, err := readBytes(ctx, configDir)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	c := getCache(configDir)
	c.mu.Lock()
	if c.parsed != nil && c.parsedGen == c.gen {
		m := c.parsed
		c.mu.Unlock()
		return m, nil
	}
	curGen := c.gen
	c.mu.Unlock()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.parsed = raw
	c.parsedGen = curGen
	c.mu.Unlock()
	return raw, nil
}
