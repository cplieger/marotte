package translate

import (
	"encoding/json"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

const configurationSettingKRN = "krn:::setting/"

// configurationState is _kiro/configuration/state's params, measured on kiro-cli 2.28.0: the view
// it composes for, KAS's layer list (ids and display names), and one statement per setting a layer
// states. An unstated setting has no statement.
type configurationState struct {
	View struct {
		Observer string `json:"observer"`
	} `json:"view"`
	Layers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"layers"`
	Settings []struct {
		KRN           string          `json:"krn"`
		Value         json.RawMessage `json:"value"`
		Contributions []struct {
			Layer  string `json:"layer"`
			Stated struct {
				Used bool `json:"used"`
			} `json:"stated"`
		} `json:"contributions"`
	} `json:"settings"`
}

// DecodeConfigurationState reads a CONNECTION view into the resolved value and winning layer of
// each stated setting, keyed by KAS's setting key. ok is false for a session view or an
// undecodable frame; a decoded view replaces the previous one whole. A non-string value is kept as
// its JSON text, and the layer's name comes from the frame's own layer list.
func DecodeConfigurationState(raw json.RawMessage) (map[string]marotte.KiroDefault, bool) {
	var s configurationState
	if json.Unmarshal(raw, &s) != nil || s.View.Observer != "connection" {
		return nil, false
	}
	names := make(map[string]string, len(s.Layers))
	for _, l := range s.Layers {
		names[l.ID] = displayText(l.Name)
	}
	out := make(map[string]marotte.KiroDefault, len(s.Settings))
	for i := range s.Settings {
		st := &s.Settings[i]
		key, isSetting := strings.CutPrefix(st.KRN, configurationSettingKRN)
		if !isSetting || key == "" || len(st.Value) == 0 {
			continue
		}
		// The last used contribution is the one the compose kept.
		layer := ""
		for _, c := range st.Contributions {
			if c.Stated.Used {
				layer = c.Layer
			}
		}
		out[key] = marotte.KiroDefault{
			Value:     configurationValue(st.Value),
			Layer:     displayText(layer),
			LayerName: names[layer],
		}
	}
	return out, true
}

func configurationValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return displayText(s)
	}
	return displayText(string(raw))
}
