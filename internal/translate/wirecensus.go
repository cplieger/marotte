package translate

// A runtime census of the `_meta.kiro` fields KAS sends and marotte drops: encoding/json
// discards an unknown field silently (a misread `modeId` once lost every agent mode
// change). internal/kascap covers the client-to-agent direction statically. The probe
// never materializes a value, is bounded per frame and per process (latching off at the
// cap), and cannot fail a turn. Always on: it is ~1% of the hottest path, and the fields
// worth finding ride rare frames a sample budget would miss.

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/cplieger/runesafe/v2"
)

// maxCensusObjectBytes skips the probe for an oversized `_meta.kiro` object, so a block
// with many keys cannot turn one frame into that many map inserts.
const maxCensusObjectBytes = 64 << 10

// maxCensusKeys bounds the distinct (name, type) pairs held for the process's life (both
// halves are backend-controlled); reaching it disables the probe.
const maxCensusKeys = 128

// maxCensusNameBytes bounds one reported field name, untrusted text on a logfmt line.
const maxCensusNameBytes = 64

// censusLedger remembers which (name, type) pairs were reported, so each novel shape is
// reported once. Mutex-guarded because each bridge decodes on its own goroutine; a plain
// map because the cap needs an exact len().
type censusLedger struct {
	// reported leads: fieldalignment wants the pointer-bearing field before the mutex.
	reported map[string]struct{}
	mu       sync.Mutex
	off      bool
}

var census = &censusLedger{reported: make(map[string]struct{}, maxCensusKeys)}

// claim reports whether name is novel, recording it if so. It also answers false
// once the ledger is full, which is what latches the probe off.
func (l *censusLedger) claim(name string) (novel, full bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.off {
		return false, false
	}
	if _, seen := l.reported[name]; seen {
		return false, false
	}
	if len(l.reported) >= maxCensusKeys {
		l.off = true
		return false, true
	}
	l.reported[name] = struct{}{}
	return true, false
}

// disabled reports whether the ledger has latched off, so the hot path can skip
// the decode entirely rather than paying for it and discarding the result.
func (l *censusLedger) disabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.off
}

// cached because censusMeta runs on every frame. The map is shared: read-only.
func knownKeys(t reflect.Type) map[string]struct{} {
	if cached, ok := knownKeyCache.Load(t); ok {
		if set, isSet := cached.(map[string]struct{}); isSet {
			return set
		}
	}
	derived := knownKeysOf(t)
	knownKeyCache.Store(t, derived)
	return derived
}

// knownKeyCache memoizes knownKeysOf per type: read-mostly with a fixed key set, the
// shape sync.Map is for.
var knownKeyCache sync.Map

// knownKeysOf returns the lowercased JSON member names a struct type consumes, including embedded
// and untagged nested structs. Derived from the tags so it cannot drift; lowercased because
// encoding/json matches members case-insensitively.
func knownKeysOf(t reflect.Type) map[string]struct{} {
	out := make(map[string]struct{})
	collectKnownKeys(t, out, 0)
	return out
}

// maxCensusDepth bounds the tag walk so a recursive type cannot spin.
const maxCensusDepth = 8

func collectKnownKeys(t reflect.Type, out map[string]struct{}, depth int) {
	if depth > maxCensusDepth {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
			continue
		case name != "":
			out[strings.ToLower(name)] = struct{}{}
		default:
			// No tag: encoding/json matches the FIELD name, and promotes an embedded struct's members.
			if f.Anonymous {
				collectKnownKeys(f.Type, out, depth+1)
				continue
			}
			out[strings.ToLower(f.Name)] = struct{}{}
		}
	}
}

// jsonKindOf names a raw value's JSON type from its first byte, all the probe reads, so a
// field's contents cannot leak into the log.
func jsonKindOf(raw json.RawMessage) string {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return "object"
		case '[':
			return "array"
		case '"':
			return "string"
		case 't', 'f':
			return "bool"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	return "empty"
}

// censusMeta reports, once per process, every member of one `_meta.kiro` object the given
// wire type does not consume. declined lists members skipped on purpose, without which
// the probe would fire on its first frame and mute itself.
func censusMeta(label string, raw json.RawMessage, target reflect.Type, declined ...string) {
	if len(raw) == 0 || len(raw) > maxCensusObjectBytes || census.disabled() {
		return
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		// Not an object, or malformed: the caller's own decode reports that.
		return
	}
	known := knownKeys(target)
	for name, value := range members {
		if _, ok := known[strings.ToLower(name)]; ok {
			continue
		}
		if declines(declined, name) {
			continue
		}
		kind := jsonKindOf(value)
		key := label + "." + strings.ToLower(name) + ":" + kind
		novel, full := census.claim(key)
		if full {
			slog.Warn("wire census: field budget spent, probe disabled for this process",
				"limit", maxCensusKeys)
			return
		}
		if !novel {
			continue
		}
		slog.Warn("wire census: UNKNOWN _meta.kiro field, dropped — KAS may have added one",
			"frame", label,
			"field", runesafe.SanitizeSingleLineBounded(name, maxCensusNameBytes),
			"type", kind)
	}
}

// declines reports whether name is one the caller deliberately does not read. Scanned, not
// merged into the cached set, because that set is SHARED by every caller.
func declines(declined []string, name string) bool {
	for _, d := range declined {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

// censusMeteringUnit reports a metering unit label marotte does not sum. A value rather
// than a name, because `unit` is a known field: only its label shows a new billing
// dimension. A low-cardinality vocabulary word, still sanitized and capped.
func censusMeteringUnit(unit string) {
	if unit == "" || unit == meteringUnitCredit || census.disabled() {
		return
	}
	safe := runesafe.SanitizeSingleLineBounded(unit, maxCensusNameBytes)
	novel, full := census.claim("meteringUsage.unit=" + safe)
	if full {
		slog.Warn("wire census: field budget spent, probe disabled for this process",
			"limit", maxCensusKeys)
		return
	}
	if !novel {
		return
	}
	slog.Warn("wire census: UNKNOWN metering unit, not counted as spend — "+
		"KAS may have added a billing dimension", "unit", safe)
}
