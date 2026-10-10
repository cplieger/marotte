package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/cplieger/atomicfile/v4"
)

// ErrUnreadable marks the Update failure about the STORED document: config.json exists and
// could not be read or parsed, so the remedy is the file itself.
var ErrUnreadable = errors.New("settings: stored document unreadable")

// Update applies fn to the stored document and writes it back atomically under ONE per-
// configDir lock across read, merge and write, so concurrent writers drop no key. An
// unreadable document refuses (ErrUnreadable): merging over an empty map would destroy every
// key fn does not name. An absent file is ordinary. The merged document is returned.
func Update(ctx context.Context, configDir string, fn func(doc map[string]json.RawMessage) error) (map[string]json.RawMessage, error) {
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
	if mErr := fn(doc); mErr != nil {
		return nil, mErr
	}
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
	c.forget()
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
