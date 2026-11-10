// Package settings is the one reader and writer of <configDir>/config.json: Field (typed key), FieldInto
// (pointer target), FieldStrict (unreadable kept apart from absent, for a caller gating a
// destructive action) and Read (one parsed generation for several keys), all through one
// freshness-checked cache capped at maxBytes; Stored reads the whole document uncached, as a map
// its caller owns. Update, Replace and Poll are its writers. Every reader accepts one leading
// UTF-8 byte order mark, and an absent or unreadable read is answered by internal/ownedfile's rule.
//
// There is no exported raw-bytes reader: the cache's slice is shared by every caller, so an
// exported accessor would let any caller corrupt every other's settings. For the same reason a
// Document exposes decoders only, never the parsed map the cache shares.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/ownedfile"
	"github.com/cplieger/marotte/internal/userjson"
	"golang.org/x/sync/singleflight"
)

// maxBytes caps config.json reads, matching the HTTP path's httpserver.MaxJSONBody.
const maxBytes = 1 << 20

const filename = "config.json"

// Filename is the basename of marotte's config file (not kiro-cli's cli.json).
const Filename = filename

// errNotObject refuses bytes that are not one JSON object of at most maxBytes.
var errNotObject = errors.New("settings: config.json is not one JSON object")

// parseObject is the one rule for what config.json's bytes may hold: at most maxBytes, one BOM
// stripped, a JSON object. A top-level null unmarshals into a nil map with no error, so it is
// refused here and no reader sees it as an empty document.
func parseObject(data []byte) (map[string]json.RawMessage, error) {
	if len(data) > maxBytes {
		return nil, fmt.Errorf("%w: over the %d-byte cap", errNotObject, maxBytes)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(userjson.Body(data), &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: a top-level null", errNotObject)
	}
	return doc, nil
}

func acceptObject(data []byte) error {
	_, err := parseObject(data)
	return err
}

// classify is every read of config.json; a var so a test can fail a read as a broken mount does.
var classify = func(path string) (r ownedfile.Read, data []byte, info os.FileInfo, err error) {
	return ownedfile.Classify(path, ownedfile.Options{Limit: maxBytes}, acceptObject)
}

// applied is the document the owner last wrote or adopted: readers answer it while a read is
// held. gen is a generation no other byte set is answered under.
type applied struct {
	read ownedfile.Read
	data []byte
	gen  uint64
	set  bool
}

// Staleness is three legs, filesafe.FileIdentity (mtime AND os.SameFile) plus size: neither pair
// alone catches both a same-length rename within one tick and a different-length in-place rewrite.
type cache struct {
	sfGroup   singleflight.Group
	id        filesafe.FileIdentity
	parsed    map[string]json.RawMessage
	configDir string
	data      []byte
	// gate holds an absent or unreadable Poll read, under writeMu.
	gate    ownedfile.Gate
	applied applied
	// high is the highest stamp this process wrote, published or applied, and committed the
	// stamp it last wrote; both under writeMu.
	high      stamp
	committed stamp
	parsedGen uint64
	size      int64
	// gen counts generations; dataGen is the one data is answered under.
	dataGen uint64
	gen     uint64
	mu      sync.Mutex
	// writeMu serializes every write and Poll for this configDir. Separate from mu, which guards
	// the cached bytes and the applied record for the length of one access and is taken inside
	// the window this one holds; every move of applied but the first-read seed holds both.
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
// path, otherwise classify the read and answer it by ownedfile.Reading.
func (c *cache) reload() generation {
	// Absolute because filesafe.OpenRegularNoFollow requires it.
	path, err := filepath.Abs(filepath.Join(c.configDir, filename))
	if err != nil {
		return generation{err: err}
	}
	// os.Stat never blocks on a FIFO, so the fast path stays one syscall.
	if info, statErr := os.Stat(path); statErr == nil {
		if cached, ok := c.hit(info); ok {
			return cached
		}
	}
	r, data, info, readErr := classify(path)
	c.mu.Lock()
	first := !c.applied.set
	a := c.applied
	c.mu.Unlock()
	if readErr == nil && r.Kind == ownedfile.Absent {
		readErr = fmt.Errorf("%s is absent and its last adopted read was refused", path)
	}
	src := ownedfile.Reading(r, a.read, a.set)
	g := c.answer(src, data, info, readErr, a)
	if first {
		c.seedApplied(r, data, g.gen)
	}
	return g
}

// answer is reload's generation for src. Refused is always an error to the reader: bytes the
// owner refuses are stored, so parsedOf refuses them; a read with none over a refused applied
// record serves that record; neither having bytes is an ErrUnreadable at generation 0.
func (c *cache) answer(src ownedfile.Source, data []byte, info os.FileInfo, readErr error, a applied) generation {
	switch {
	case src == ownedfile.Disk || (src == ownedfile.Refused && data != nil):
		// info, not a stat of the path: the identity must describe the generation these bytes
		// came from, which a concurrent publish may already have replaced.
		return generation{data: data, gen: c.store(data, info)}
	case src == ownedfile.Seed:
		return generation{gen: c.forget()}
	case src == ownedfile.Applied || a.data != nil && a.read.Kind == ownedfile.Unreadable:
		return generation{data: a.data, gen: a.gen}
	}
	return generation{err: fmt.Errorf("%w: %w", ErrUnreadable, readErr)}
}

// seedApplied records a process's first read of config.json as applied (a first read is adopted
// as it is). It takes mu, not writeMu: a read runs inside Update's window.
func (c *cache) seedApplied(r ownedfile.Read, data []byte, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.applied.set {
		return
	}
	c.applied = applied{read: r, data: data, gen: gen, set: true}
}

// setApplied moves the applied record under a fresh generation. writeMu must be held.
func (c *cache) setApplied(r ownedfile.Read, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.applied = applied{read: r, data: data, gen: c.gen, set: true}
}

func (c *cache) appliedRecord() applied {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applied
}

// A zero identity matches nothing.
func (c *cache) hit(info os.FileInfo) (generation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.id.Matches(info) || info.Size() != c.size {
		return generation{}, false
	}
	return generation{data: c.data, gen: c.dataGen}, true
}

func (c *cache) store(data []byte, info os.FileInfo) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.id = filesafe.Identify(info)
	c.size = info.Size()
	c.gen++
	c.dataGen = c.gen
	return c.dataGen
}

// forget drops the cached bytes so the next read goes to disk, and answers the generation of no
// bytes. The generation moves only when the cache held bytes, so an absent file reads as one
// generation for as long as it stays absent.
func (c *cache) forget() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil || c.id.Recorded() {
		c.data = nil
		c.id = filesafe.FileIdentity{}
		c.size = 0
		c.gen++
		c.dataGen = c.gen
	}
	return c.dataGen
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
// PRESENT and decoded; err reports config.json could not be read, or was refused and has not
// been read again.
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

// fieldOr is Field for a reader with a default: an absent key, or one that does not decode into T,
// reads as def. readable is false only when config.json exists and could not be read or parsed, or
// ctx ended first, so a live push can tell "the document says def" from "there is no answer".
func fieldOr[T any](ctx context.Context, configDir, key string, def T) (value T, readable bool) {
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

	// Parsed here, not in reload: the cache holds the file's exact bytes.
	raw, err := parseObject(g.data)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.parsed = raw
	c.parsedGen = g.gen
	c.mu.Unlock()
	return raw, nil
}
