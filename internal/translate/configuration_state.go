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

// agentDefaultsLayer holds KAS's own helper agents (the wf-* step agents, Explore): subagents a
// workflow names, never a mode a chat can pick.
const agentDefaultsLayer = "kiro-agent-defaults"

// workspaceConfiguration is _kiro/configuration/getState's answer to a workspace observer,
// measured on kiro-cli 2.29.1, read for its agent resources only.
type workspaceConfiguration struct {
	Layers []struct {
		ID string `json:"id"`
	} `json:"layers"`
	Resources []struct {
		Type  string      `json:"type"`
		Items []agentItem `json:"items"`
	} `json:"resources"`
}

type agentItem struct {
	Layer string `json:"layer"`
	Value struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"value"`
}

// DecodeCustomAgents reads the custom agents a workspace getState answer reports, as modes with
// Source "workspace" for the workspace layer and "global" for any other. KAS reports every
// statement of a colliding id as active, so one id keeps the statement of the latest layer in the
// frame's own layer order, and within a layer its last. ok is false for an undecodable frame.
func DecodeCustomAgents(raw json.RawMessage) ([]marotte.SessionMode, bool) {
	var c workspaceConfiguration
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	rank := make(map[string]int, len(c.Layers))
	for i, l := range c.Layers {
		rank[l.ID] = i
	}
	d := agentSet{at: map[string]int{}, won: map[string]int{}, out: []marotte.SessionMode{}}
	for i := range c.Resources {
		if c.Resources[i].Type != "agent" {
			continue
		}
		for j := range c.Resources[i].Items {
			if m, ok := customAgentMode(&c.Resources[i].Items[j]); ok {
				d.keep(&m, rank[c.Resources[i].Items[j].Layer])
			}
		}
	}
	return d.out, true
}

// customAgentMode is the mode one agent statement names; ok is false for KAS's own helper agents
// and an item with no id.
func customAgentMode(it *agentItem) (marotte.SessionMode, bool) {
	id := displayText(it.Value.ID)
	if id == "" || it.Layer == agentDefaultsLayer {
		return marotte.SessionMode{}, false
	}
	source := "global"
	if it.Layer == "workspace" {
		source = "workspace"
	}
	return marotte.SessionMode{
		ID:          id,
		Name:        displayText(it.Value.Name),
		Description: displayText(it.Value.Description),
		Source:      source,
	}, true
}

// agentSet holds one mode per id in first-seen order; won is the layer rank its statement came from.
type agentSet struct {
	at  map[string]int
	won map[string]int
	out []marotte.SessionMode
}

func (d *agentSet) keep(m *marotte.SessionMode, rank int) {
	j, seen := d.at[m.ID]
	switch {
	case !seen:
		d.at[m.ID] = len(d.out)
		d.won[m.ID] = rank
		d.out = append(d.out, *m)
	case rank >= d.won[m.ID]:
		d.won[m.ID] = rank
		d.out[j] = *m
	}
}

// configurationStanding is a workspace getState answer read for its non-active items.
type configurationStanding struct {
	Layers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"layers"`
	Resources []struct {
		Type  string         `json:"type"`
		Items []standingItem `json:"items"`
	} `json:"resources"`
}

// standingItem is one getState item, the fields the standing read uses.
type standingItem struct {
	ReadFrom struct {
		URI string `json:"uri"`
	} `json:"readFrom"`
	Standing struct {
		Kind      string `json:"kind"`
		DecidedBy string `json:"decidedBy"`
		Compose   string `json:"compose"`
		Setting   string `json:"setting"`
	} `json:"standing"`
	Value struct {
		URI    string `json:"uri"`
		Server string `json:"server"`
	} `json:"value"`
}

// mcpServerResource is the resource type of an MCP server, the one type joined by name.
const mcpServerResource = "mcp-server"

// DecodeConfigurationStanding reads the items kiro-cli loaded but does not use out of a workspace getState answer:
// a displaced or disabled item, or one of a kind this build does not know, kept as it came. A file-backed item is
// keyed by DocKeyFromAbs of the file it was read from, an MCP server by its name; an item with neither key is
// dropped, since it joins no row. ok is false for an undecodable frame.
func DecodeConfigurationStanding(raw json.RawMessage) ([]marotte.ConfigStanding, bool) {
	var c configurationStanding
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	names := make(map[string]string, len(c.Layers))
	for _, l := range c.Layers {
		names[l.ID] = displayText(l.Name)
	}
	out := []marotte.ConfigStanding{}
	for i := range c.Resources {
		res := &c.Resources[i]
		for j := range res.Items {
			it := &res.Items[j]
			if it.Standing.Kind == "" || it.Standing.Kind == "active" {
				continue
			}
			key := standingKey(res.Type, it)
			if key == "" {
				continue
			}
			out = append(out, marotte.ConfigStanding{
				Type:          displayText(res.Type),
				Key:           key,
				Kind:          displayText(it.Standing.Kind),
				DecidedBy:     displayText(it.Standing.DecidedBy),
				DecidedByName: names[it.Standing.DecidedBy],
				Compose:       displayText(it.Standing.Compose),
				Setting:       displayText(it.Standing.Setting),
			})
		}
	}
	return out, true
}

// standingKey names an item the way the docs and MCP rows key it: an MCP server by its name, a file by its doc key
// (read-from location first). "" means the item names neither.
func standingKey(resourceType string, it *standingItem) string {
	if resourceType == mcpServerResource {
		if it.Value.Server == "" {
			return ""
		}
		return displayText(it.Value.Server)
	}
	if key, ok := docKeyFromFileURI(it.ReadFrom.URI); ok {
		return key
	}
	key, _ := docKeyFromFileURI(it.Value.URI)
	return key
}

// agentLoadFrame is _kiro/customAgent/config_error's params, or not_found's skipped member.
type agentLoadFrame struct {
	Path       string `json:"path"`
	ReasonCode string `json:"reasonCode"`
	Error      string `json:"error"`
}

// DecodeAgentLoadError reads the agent file and the error of a _kiro/customAgent/config_error frame, or of a
// not_found frame's skipped agent. ok is false for any other frame, and for one naming no path.
func DecodeAgentLoadError(method string, params json.RawMessage) (path, reasonCode, text string, ok bool) {
	var f agentLoadFrame
	switch method {
	case "_kiro/customAgent/config_error":
		if json.Unmarshal(params, &f) != nil {
			return "", "", "", false
		}
	case "_kiro/customAgent/not_found":
		var p struct {
			Skipped *agentLoadFrame `json:"skipped"`
		}
		if json.Unmarshal(params, &p) != nil || p.Skipped == nil {
			return "", "", "", false
		}
		f = *p.Skipped
	default:
		return "", "", "", false
	}
	if f.Path == "" {
		return "", "", "", false
	}
	return f.Path, displayText(f.ReasonCode), displayText(f.Error), true
}
