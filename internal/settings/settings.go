// Package settings is the one reader for <configDir>/config.json: Field (typed key), FieldInto
// (pointer target), FieldStrict (unreadable kept apart from absent, for a caller gating a
// destructive action) and Read (one parsed generation for several keys), all through one
// freshness-checked cache capped at maxBytes.
//
// There is no exported raw-bytes reader: the cache's slice is shared by every caller, so an
// exported accessor would let any caller corrupt every other's settings. For the same reason a
// Document exposes decoders only, never the parsed map the cache shares.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/cplieger/atomicfile/v4"
	"golang.org/x/sync/singleflight"
)

// maxBytes caps config.json reads, matching the HTTP path's webhttp.MaxJSONBody.
const maxBytes = 1 << 20

const filename = "config.json"

// Filename is the basename of marotte's config file (not kiro-cli's cli.json).
const Filename = filename

// Staleness is three legs, atomicfile.FileIdentity (mtime AND os.SameFile) plus size: neither pair
// alone catches both a same-length rename within one tick and a different-length in-place rewrite.
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
	// committed is the stamp Update last wrote, under writeMu.
	committed stamp
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

// generation is one read of the cache: the bytes and the generation number they were stored
// under, so a parse of them is never tagged with a generation a later store bumped to.
type generation struct {
	err  error
	data []byte
	gen  uint64
}

func (c *cache) load() generation {
	v, _, _ := c.sfGroup.Do("load", func() (any, error) {
		return c.reload(), nil
	})
	//nolint:errcheck // sfGroup.Do's closure always returns a `generation` value.
	return v.(generation)
}

// reload is the body of the singleflight slot: resolve the path, take the mtime/size fast
// path, otherwise read and cache.
func (c *cache) reload() generation {
	// Absolute because atomicfile.OpenRegular requires it.
	path, err := filepath.Abs(filepath.Join(c.configDir, filename))
	if err != nil {
		return generation{err: err}
	}
	// os.Stat never blocks on a FIFO, so the fast path stays one syscall.
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return generation{gen: c.forget()}
		}
		return generation{err: statErr}
	}
	if cached, ok := c.hit(info); ok {
		return cached
	}
	data, readInfo, err := readRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return generation{gen: c.forget()}
		}
		return generation{err: err}
	}
	// readInfo, not the stat above: the identity must describe the generation these bytes came
	// from, which a concurrent publish may already have replaced.
	return generation{data: data, gen: c.store(data, readInfo)}
}

// readRegular reads path under maxBytes, refusing anything but a regular file (and a final
// symlink), and returns the FileInfo of the descriptor read. OpenRegular because os.Open
// blocks forever on a FIFO, which inside the singleflight slot would wedge every reader.
func readRegular(path string) (data []byte, info os.FileInfo, err error) {
	f, info, err := atomicfile.OpenRegular(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	data, err = io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// A zero identity reports Changed.
func (c *cache) hit(info os.FileInfo) (generation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id.Changed(info) || info.Size() != c.size {
		return generation{}, false
	}
	return generation{data: c.data, gen: c.gen}, true
}

func (c *cache) store(data []byte, info os.FileInfo) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.id = atomicfile.Identify(info)
	c.size = info.Size()
	c.gen++
	return c.gen
}

// forget drops the cached bytes for a vanished file. The generation moves only when the cache
// held one, so an absent file reads as one generation for as long as it stays absent.
func (c *cache) forget() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil || c.id != (atomicfile.FileIdentity{}) {
		c.data = nil
		c.id = atomicfile.FileIdentity{}
		c.size = 0
		c.gen++
	}
	return c.gen
}

func readGeneration(ctx context.Context, configDir string) generation {
	if configDir == "" {
		return generation{}
	}
	if err := ctx.Err(); err != nil {
		return generation{err: err}
	}
	return getCache(configDir).load()
}

// errKeyParse marks the one FieldStrict failure about a single key: present, and not decoding
// into the caller's type.
var errKeyParse = errors.New("settings: parse")

// FieldStrict is Field with unreadability kept apart from absence: found reports the key was
// PRESENT and decoded; err reports config.json exists and could not be read or parsed.
func FieldStrict[T any](ctx context.Context, configDir, key string) (value T, found bool, err error) {
	d, _, err := Read(ctx, configDir)
	if err != nil {
		var zero T
		return zero, false, err
	}
	return Lookup[T](d, key)
}

// readStopped reports a read that ended with its caller's ctx, which says nothing about the document.
func readStopped(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Field reads config.json and decodes the named key into T, returning the zero value and
// false when the file is missing, the key is absent, parsing fails (logged at Warn
// naming the key) or ctx has ended (not logged). It wraps FieldStrict for callers whose
// fallback is a display default.
func Field[T any](ctx context.Context, configDir, key string) (T, bool) {
	out, ok, err := FieldStrict[T](ctx, configDir, key)
	switch {
	case errors.Is(err, errKeyParse):
		slog.Warn("settings: parse "+key, "error", err)
	case err != nil && !readStopped(err):
		slog.Warn("settings: read config.json for "+key, "error", err)
	}
	return out, ok
}

// FieldOr is Field for a reader with a default: an absent key, or one that does not decode into T,
// reads as def. readable is false only when config.json exists and could not be read or parsed, or
// ctx ended first, so a live push can tell "the document says def" from "there is no answer".
func FieldOr[T any](ctx context.Context, configDir, key string, def T) (value T, readable bool) {
	out, ok, err := FieldStrict[T](ctx, configDir, key)
	switch {
	case errors.Is(err, errKeyParse):
		slog.Warn("settings: parse "+key, "error", err)
	case readStopped(err):
		return def, false
	case err != nil:
		slog.Warn("settings: read config.json for "+key, "error", err)
		return def, false
	}
	if !ok {
		return def, true
	}
	return out, true
}

// FieldInto is the pointer-target variant of Field: it decodes the named key into out and
// reports success; like Field, it logs no failure that is ctx's.
func FieldInto(ctx context.Context, configDir, key string, out any) bool {
	d, _, err := Read(ctx, configDir)
	if err != nil {
		if !readStopped(err) {
			slog.Warn("settings: read config.json for "+key, "error", err)
		}
		return false
	}
	return d.Into(key, out)
}

func parsedOf(configDir string, g generation) (map[string]json.RawMessage, error) {
	if g.err != nil {
		return nil, g.err
	}
	if g.data == nil {
		return nil, nil
	}
	c := getCache(configDir)
	c.mu.Lock()
	if c.parsed != nil && c.parsedGen == g.gen {
		m := c.parsed
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(g.data, &raw); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.parsed = raw
	c.parsedGen = g.gen
	c.mu.Unlock()
	return raw, nil
}
