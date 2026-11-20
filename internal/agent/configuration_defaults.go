package agent

import (
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

type kiroDefaultKey struct {
	kas string
	// builtin is KAS's coded default, the answer when no layer states the key.
	builtin string
}

// kiroDefaultKeys is every setting whose Default GET /api/settings resolves. A key joins only
// when marotte withholds it while unset AND KAS's behaviour reads it from the configuration store
// (at 2.28.0 infraSafety's reader does not, so cloudformation_safety_check stays out).
var kiroDefaultKeys = map[string]kiroDefaultKey{
	settings.KeyWorkValidation: {kas: "validation", builtin: settings.FeatureOff},
}

type kiroDefaultsCache struct {
	stated    map[string]marotte.KiroDefault
	fallbacks []marotte.ModelFallback
	memory    translate.DeviceMemoryView
	mu        sync.Mutex
	known     bool
}

func (st *Settings) cacheConfigurationState(raw json.RawMessage) {
	stated, ok := translate.DecodeConfigurationState(raw)
	if !ok {
		return
	}
	fallbacks, _ := translate.DecodeModelFallbacks(raw)
	memory, _ := translate.DecodeDeviceMemoryView(raw)
	before, beforeFallbacks, beforeMemory := st.KiroDefaults(), st.ModelFallbacks(), st.DeviceMemory()
	st.kiroDefaults.mu.Lock()
	st.kiroDefaults.stated, st.kiroDefaults.fallbacks, st.kiroDefaults.memory = stated, fallbacks, memory
	st.kiroDefaults.known = true
	st.kiroDefaults.mu.Unlock()
	changed := !maps.Equal(before, st.KiroDefaults()) || !slices.Equal(beforeFallbacks, fallbacks) ||
		!sameDeviceMemory(beforeMemory, st.DeviceMemory())
	if changed {
		st.announceSettings()
	}
	// Every view, not only a changed one: a restarted engine with Model fallback just turned on reports the same rows.
	st.seedModelFallbacksSoon()
}

// announceSettings tells every tab to refetch GET /api/settings.
func (st *Settings) announceSettings() {
	if st.broadcast != nil && st.lifecycle != nil {
		settings.Announce(st.lifecycle.shutdownCtx, st.lifecycle.configDir, st.broadcast)
	}
}

// DeviceMemory answers kiro-cli's device Memory choice; nil until the engine has reported a view.
func (st *Settings) DeviceMemory() *marotte.DeviceMemory {
	st.kiroDefaults.mu.Lock()
	defer st.kiroDefaults.mu.Unlock()
	if !st.kiroDefaults.known {
		return nil
	}
	m := st.kiroDefaults.memory.Choice()
	return &m
}

// deviceMemoryView is a copy of the cached Memory rows.
func (st *Settings) deviceMemoryView() translate.DeviceMemoryView {
	st.kiroDefaults.mu.Lock()
	defer st.kiroDefaults.mu.Unlock()
	return st.kiroDefaults.memory
}

// storeDeviceMemoryRow puts a row _kiro/configuration/set answered in the cached view, so GET /api/settings answers it
// before KAS pushes the next view.
func (st *Settings) storeDeviceMemoryRow(row json.RawMessage) {
	st.kiroDefaults.mu.Lock()
	defer st.kiroDefaults.mu.Unlock()
	st.kiroDefaults.memory.Store(row)
}

func sameDeviceMemory(a, b *marotte.DeviceMemory) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Mode != b.Mode || (a.Lock == nil) != (b.Lock == nil) {
		return false
	}
	return a.Lock == nil || *a.Lock == *b.Lock
}

// ModelFallbacks answers kiro-cli's fallback for each model it serves, sorted by model; empty until the engine has
// reported a view.
func (st *Settings) ModelFallbacks() []marotte.ModelFallback {
	st.kiroDefaults.mu.Lock()
	defer st.kiroDefaults.mu.Unlock()
	return append([]marotte.ModelFallback{}, st.kiroDefaults.fallbacks...)
}

// KiroDefaults answers what each kiroDefaultKeys setting resolves to in kiro-cli, keyed by the
// marotte setting key; empty until the engine has reported a view. The caller drops the
// keys the user has set.
func (st *Settings) KiroDefaults() map[string]marotte.KiroDefault {
	st.kiroDefaults.mu.Lock()
	defer st.kiroDefaults.mu.Unlock()
	out := make(map[string]marotte.KiroDefault, len(kiroDefaultKeys))
	if !st.kiroDefaults.known {
		return out
	}
	for key, k := range kiroDefaultKeys {
		d, stated := st.kiroDefaults.stated[k.kas]
		if !stated {
			d = marotte.KiroDefault{Value: k.builtin}
		}
		out[key] = d
	}
	return out
}
