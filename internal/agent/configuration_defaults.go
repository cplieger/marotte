package agent

import (
	"encoding/json"
	"maps"
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
	stated map[string]marotte.KiroDefault
	mu     sync.Mutex
	known  bool
}

func (st *Settings) cacheConfigurationState(raw json.RawMessage) {
	stated, ok := translate.DecodeConfigurationState(raw)
	if !ok {
		return
	}
	before := st.KiroDefaults()
	st.kiroDefaults.mu.Lock()
	st.kiroDefaults.stated, st.kiroDefaults.known = stated, true
	st.kiroDefaults.mu.Unlock()
	if !maps.Equal(before, st.KiroDefaults()) && st.broadcast != nil && st.lifecycle != nil {
		st.broadcast(st.lifecycle.shutdownCtx, marotte.NewEvent(marotte.EventSettingsUpdated, "", marotte.SettingsUpdatedPayload{}))
	}
}

// KiroDefaults answers what each kiroDefaultKeys setting resolves to in kiro-cli, keyed by the
// marotte setting key; empty until the utility bridge has reported a view. The caller drops the
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
