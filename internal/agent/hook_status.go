package agent

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/workspace"
)

// kiroSettingsPath returns kiro-cli's settings file (cli.json, where `kiro-cli settings` persists
// every key), via workspace.KiroHome() so both agree on the location.
func kiroSettingsPath() string {
	if workspace.KiroHome() == ".kiro" {
		// Neither KIRO_HOME nor HOME is set: a relative path would read from CWD. Empty means defaults.
		return ""
	}
	return workspace.KiroSettingsPath("cli.json")
}

// cachedBoolField reads a JSON boolean, costing one os.Stat once warm. Staleness checks mtime,
// inode and size: kiro-cli publishes cli.json by rename, so no single signal catches every rewrite.
type cachedBoolField struct {
	id         atomicfile.FileIdentity
	path       string
	key        string
	size       int64
	mu         sync.Mutex
	defaultVal bool
	value      bool
}

func newCachedBoolField(path, key string, defaultVal bool) *cachedBoolField {
	return &cachedBoolField{path: path, key: key, defaultVal: defaultVal, value: defaultVal}
}

func (c *cachedBoolField) get() bool {
	if c.path == "" {
		return c.defaultVal
	}

	// No lock across the read: this runs per tool call. Two concurrent misses both reading is benign.
	info, err := os.Stat(c.path)
	if err != nil {
		return c.defaultVal
	}

	c.mu.Lock()
	id, size, cached := c.id, c.size, c.value
	c.mu.Unlock()
	if id.Matches(info) && info.Size() == size {
		return cached
	}

	data, err := os.ReadFile(c.path) // #nosec G304 -- fixed path
	if err != nil {
		return c.defaultVal
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return c.defaultVal
	}
	parsed := c.defaultVal
	if v, ok := raw[c.key]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil {
			parsed = b
		}
	}

	// A file that changed again is stale in the safe direction: the next call re-reads.
	c.mu.Lock()
	c.value, c.id, c.size = parsed, atomicfile.Identify(info), info.Size()
	c.mu.Unlock()
	return parsed
}

// hookStatusCache caches hooks.showStatus from ~/.kiro/settings/cli.json.
type hookStatusCache struct {
	field *cachedBoolField
}

func newHookStatusCache(path string) *hookStatusCache {
	return &hookStatusCache{field: newCachedBoolField(path, "hooks.showStatus", true)}
}

// IsHookStatusEnabled reads kiro-cli's hooks.showStatus setting, true on any error or when unset,
// matching kiro-cli. marotte's own config uses different keys and holds no such entry.
func (c *hookStatusCache) IsHookStatusEnabled() bool {
	return c.field.get()
}
