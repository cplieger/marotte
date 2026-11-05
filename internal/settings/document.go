package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// Document is one generation of config.json, parsed once. Every value decoded from it comes from the same bytes, with
// no I/O, so readers that take a Document cannot mix two readings. The zero Document is the absent document.
type Document struct {
	raw map[string]json.RawMessage
}

// ErrUnparseable marks a Read whose generation was read but does not parse as a JSON object; an oversized file is
// truncated at maxBytes and lands here too.
var ErrUnparseable = errors.New("settings: config.json does not parse")

// Read reads config.json once and parses it once. gen is the generation the bytes were stored under; an absent file is
// one generation while it stays absent. err is nil for a readable document and wraps ErrUnparseable, with that
// generation's gen, for one that does not parse; any other err is a read error (a stat or open failure, a non-regular
// file, ctx ended) and gen is 0.
func Read(ctx context.Context, configDir string) (doc Document, gen uint64, err error) {
	g := readGeneration(ctx, configDir)
	if g.err != nil {
		return Document{}, 0, g.err
	}
	raw, err := parsedOf(configDir, g)
	if err != nil {
		return Document{}, g.gen, fmt.Errorf("%w: %w", ErrUnparseable, err)
	}
	return Document{raw: raw}, g.gen, nil
}

// Lookup decodes key from d: found reports the key was present and decoded; err wraps errKeyParse for a present key
// that does not decode into T.
func Lookup[T any](d Document, key string) (value T, found bool, err error) {
	var zero T
	v, ok := d.raw[key]
	if !ok {
		return zero, false, nil
	}
	var out T
	if uErr := json.Unmarshal(v, &out); uErr != nil {
		return zero, false, fmt.Errorf("%w %s: %w", errKeyParse, key, uErr)
	}
	return out, true, nil
}

// LookupOr is Lookup with a default: an absent key reads as def, and a key that does not decode reads as def and logs
// "settings: parse <key>" at Warn.
func LookupOr[T any](d Document, key string, def T) T {
	v, ok, err := Lookup[T](d, key)
	if err != nil {
		slog.Warn("settings: parse "+key, "error", err)
	}
	if !ok {
		return def
	}
	return v
}

// Into decodes key into out and reports success, logging a key that does not decode.
func (d Document) Into(key string, out any) bool {
	v, ok := d.raw[key]
	if !ok {
		return false
	}
	if err := json.Unmarshal(v, out); err != nil {
		slog.Warn("settings: parse "+key, "error", err)
		return false
	}
	return true
}

// DebugLogsIn is DebugLogs over d: anything but a stored true is info.
func DebugLogsIn(d Document) bool {
	var on bool
	return d.Into(KeyDebugLogs, &on) && on
}

// HighThroughputIn reports whether the next engine starts with the high throughput launcher; an absent or invalid
// value is the default.
func HighThroughputIn(d Document) bool {
	return LookupOr(d, KeyHighThroughput, defaultHighThroughput)
}

// MCPWaitForReadyIn is MCPWaitForReady over d: anything but a stored true is off.
func MCPWaitForReadyIn(d Document) bool {
	return LookupOr(d, KeyMCPWaitForReady, false)
}
