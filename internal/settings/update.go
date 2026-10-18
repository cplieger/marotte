package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/memlimit"
	"github.com/cplieger/marotte/internal/nodeopts"
)

// ErrUnreadable marks the Update failure about the STORED document: config.json exists and
// could not be read or parsed, so the remedy is the file itself.
var ErrUnreadable = errors.New("settings: stored document unreadable")

// Update applies fn to the stored document and writes it back atomically under ONE per-
// configDir lock across read, merge and write, so concurrent writers drop no key. An
// unreadable document refuses (ErrUnreadable): merging over an empty map would destroy every
// key fn does not name. An absent file is ordinary. Each write stamps the document with the next
// version (cache.next), whatever fn put there; a refusal writes nothing. publish, when not nil,
// gets the write's settings_updated event before the lock is released, so events leave in version
// order. The merged document is returned.
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
	doc, err := readDocument(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	next := c.next(stampOf(doc), time.Now())
	if mErr := fn(doc); mErr != nil {
		return nil, mErr
	}
	next.write(doc)
	pretty, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	res, err := atomicfile.WriteFile(ctx, path, append(pretty, '\n'),
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700))
	if err != nil {
		return nil, err
	}
	if !res.Durable {
		slog.Warn("settings: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	c.committed = next
	c.forget()
	if publish != nil {
		publish(ctx, next.event())
	}
	return doc, nil
}

// ValidateCompactionPatch refuses a patch whose compaction keys the UI cannot
// produce: auto_compaction_enabled must be a bool and auto_compact_pct an
// integer in 50..90 in steps of 5. A patch that carries neither key passes.
func ValidateCompactionPatch(patch map[string]json.RawMessage) error {
	if raw, ok := patch[KeyAutoCompactionEnabled]; ok {
		var b bool
		if decodeInto(&b, raw) != nil {
			return fmt.Errorf("%s must be true or false", KeyAutoCompactionEnabled)
		}
	}
	if raw, ok := patch[KeyAutoCompactPct]; ok {
		var pct int
		if decodeAutoCompactPct(&pct, raw) != nil {
			return fmt.Errorf("%s must be a whole number from 50 to 90 in steps of 5", KeyAutoCompactPct)
		}
	}
	return nil
}

// An absent file is an empty map, every other fault an error. readRegular refuses a FIFO, which
// under Update's lock would wedge every later write.
func readDocument(path string) (map[string]json.RawMessage, error) {
	data, info, err := readRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("settings: %s is %d bytes, over the %d-byte cap", path, info.Size(), maxBytes)
	}
	doc := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	// A top-level `null` parses into a NIL map with no error, and maps.Copy onto nil panics.
	if doc == nil {
		return nil, fmt.Errorf("settings: %s contains a top-level null, not an object", path)
	}
	return doc, nil
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
