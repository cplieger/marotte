package agent

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/settings"
)

// documentReadAttempts bounds the re-reads of a config.json that answers a read error; a file being replaced or
// re-permissioned settles in one.
const documentReadAttempts = 3

// readSnapshot reads one parsed generation of config.json, the caller's cancellation dropped: an already-cancelled
// caller would be an unordered read failure and would hold restarts and session reloads. A document that does not
// parse answers its generation, readable false; a read error on each of documentReadAttempts reads answers gen 0,
// readable false: a read with no order among others.
func readSnapshot(ctx context.Context, configDir string) (doc settings.Document, gen uint64, readable bool) {
	ctx = context.WithoutCancel(ctx)
	for range documentReadAttempts {
		d, g, err := settings.Read(ctx, configDir)
		switch {
		case err == nil:
			return d, g, true
		case errors.Is(err, settings.ErrUnparseable):
			// A re-read of the same generation answers the same bytes.
			return settings.Document{}, g, false
		}
	}
	return settings.Document{}, 0, false
}

// configDocument is every config.json value an engine spawn, a restart decision or a session door reads.
type configDocument struct {
	session sessionDocument
	engine  engineDocument
}

// clone answers a copy sharing no slice with d.
func (d *configDocument) clone() configDocument {
	c := *d
	c.engine.ignoreFiles = slices.Clone(d.engine.ignoreFiles)
	c.session.dirEntries = slices.Clone(d.session.dirEntries)
	return c
}

// readConfigDocument decodes every value of a configDocument from one parsed generation.
func readConfigDocument(d settings.Document) configDocument {
	files, ignoreErr := readAgentIgnoreFiles(d)
	dirs, _, dirsErr := settings.Lookup[[]string](d, settings.KeyAdditionalDirectories)
	enabled, pct := autoCompactionPolicy(d)
	return configDocument{
		engine: engineDocument{
			features:          engineFeatures(d),
			knowledge:         knowledgeEnabled(d),
			heapMiB:           engineHeapMiB(d),
			debugLogs:         settings.DebugLogsIn(d),
			throughput:        settings.HighThroughputIn(d),
			ignoreFiles:       files,
			ignoreInvalid:     ignoreErr != nil,
			contentCollection: settings.LookupOr(d, settings.KeyContentCollectionEnabled, settings.DefaultContentCollectionEnabled),
		},
		session: sessionDocument{
			profile:            securityProfileID(d),
			dirEntries:         dirs,
			dirsInvalid:        dirsErr != nil,
			memory:             memoryPreference(d),
			features:           sessionFeatures(d),
			autoCompactEnabled: enabled,
			autoCompactPct:     pct,
		},
	}
}

// defaultConfigDocument is what an absent config.json gives, from the same readers.
var defaultConfigDocument = sync.OnceValue(func() configDocument { return readConfigDocument(settings.Document{}) })

// docSource says where a read's values come from. The zero value is a readable read, so a hand-built spec is one.
type docSource uint8

const (
	fromDocument     docSource = iota // config.json reads; the values are its newest generation's
	fromLastReadable                  // config.json cannot be read; the values of the newest readable generation
	fromDefaults                      // config.json cannot be read and no read since boot could; the readers' defaults
)

// lastReadable owns the answer to "is config.json readable" and the values a spawn, a restart decision and a session
// door read from it. Ready at its zero value.
type lastReadable struct {
	v          configDocument // the newest readable generation's values, valid when ok
	gen        uint64         // that generation
	seen       uint64         // the newest generation any ordered read answered for
	mu         sync.Mutex
	ok         bool
	unreadable bool // the verdict of the newest answer
}

// read is readSnapshot through settle. It decodes a generation only the first time one arrives, so a key that does not
// decode logs once per generation, not at every read.
func (l *lastReadable) read(ctx context.Context, configDir string, fields func(settings.Document) configDocument) (configDocument, docSource) {
	doc, gen, readable := readSnapshot(ctx, configDir)
	var v *configDocument
	if readable && !l.decoded(gen) {
		d := fields(doc)
		v = &d
	}
	return l.settle(v, gen, readable)
}

// decoded reports whether gen is the cached readable generation.
func (l *lastReadable) decoded(gen uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ok && l.gen == gen
}

// settle records one read and answers what its caller must use: never a generation older than one already answered.
// v is nil for a readable generation decoded already.
func (l *lastReadable) settle(v *configDocument, gen uint64, readable bool) (configDocument, docSource) {
	l.mu.Lock()
	flipped := l.record(v, gen, readable)
	doc, src := l.answer()
	held, everRead := l.unreadable, l.ok
	l.mu.Unlock()
	switch {
	case !flipped:
	case !held:
		slog.Info("config.json reads again; waiting engine restarts and session reloads continue", "generation", gen)
	case everRead:
		slog.Warn("config.json cannot be read; marotte keeps its last readable settings, and engine restarts and session "+
			"reloads wait until it reads again", "generation", gen)
	default:
		slog.Warn("config.json cannot be read and none was read since marotte started; marotte runs on the defaults, and "+
			"engine restarts and session reloads wait until it reads", "generation", gen)
	}
	return doc, src
}

// record moves the cache by one read; it reports whether the verdict flipped. l.mu must be held. An older read
// finishing late changes nothing, so no answer goes back a generation.
func (l *lastReadable) record(v *configDocument, gen uint64, readable bool) (flipped bool) {
	was := l.unreadable
	switch {
	case readable && gen >= l.seen:
		// A nil v found l.gen == gen in decoded, and l.gen never falls while gen >= l.seen holds, so gen is still cached.
		if v != nil {
			l.v = *v
		}
		l.gen, l.ok, l.seen, l.unreadable = gen, true, gen, false
	case !readable && gen != 0 && gen > l.seen:
		l.seen, l.unreadable = gen, true
	case !readable && gen == 0:
		l.unreadable = true
	}
	return was != l.unreadable
}

// answer is the cache state's answer, never the arrival's, so the source is fromDocument exactly while the verdict is
// readable. l.mu must be held.
func (l *lastReadable) answer() (configDocument, docSource) {
	switch {
	case !l.unreadable:
		return l.v.clone(), fromDocument
	case l.ok:
		return l.v.clone(), fromLastReadable
	}
	d := defaultConfigDocument()
	return d.clone(), fromDefaults
}

// held reports the newest answer's verdict without reading: true while config.json cannot be read.
func (l *lastReadable) held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unreadable
}
