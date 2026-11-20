package translate

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

const configurationSettingKRN = "krn:::setting/"

// observerConnection is the view observer of the connection-wide rows, as against one session's.
const observerConnection = "connection"

// composeLocked is the compose mode of a row a configuration layer locks.
const composeLocked = "locked"

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
	if json.Unmarshal(raw, &s) != nil || s.View.Observer != observerConnection {
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

// The two device rows of kiro-cli's Memory choice, and their values (KAS's memoryMode enum, memoryReflection on/off).
const (
	memoryModeKRN       = configurationSettingKRN + "memoryMode"
	memoryReflectionKRN = configurationSettingKRN + "memoryReflection"
	memoryModeDisabled  = "disabled"
	memoryModeReadOnly  = "read_only"
	memoryModeReadWrite = "read_write"
	memoryReflectionOff = "off"
)

// deviceMemoryRow is one composed setting row, the fields the Memory reduction reads.
type deviceMemoryRow struct {
	KRN        string          `json:"krn"`
	Value      json.RawMessage `json:"value"`
	Compose    string          `json:"compose"`
	Assertions []struct {
		Layer string `json:"layer"`
	} `json:"assertions"`
}

// SettingLock names the layer that locks a composed row: the last assertion's, with its display name from names
// (the layer id when it has none). nil unless compose is locked.
func SettingLock(compose string, assertionLayers []string, names map[string]string) *marotte.SettingLock {
	if compose != composeLocked || len(assertionLayers) == 0 {
		return nil
	}
	raw := assertionLayers[len(assertionLayers)-1]
	layer, name := displayText(raw), displayText(names[raw])
	if name == "" {
		name = layer
	}
	return &marotte.SettingLock{Layer: layer, Name: name}
}

func (r *deviceMemoryRow) lock(names map[string]string) *marotte.SettingLock {
	layers := make([]string, len(r.Assertions))
	for i, a := range r.Assertions {
		layers[i] = a.Layer
	}
	return SettingLock(r.Compose, layers, names)
}

func (r *deviceMemoryRow) text() (string, bool) {
	var v string
	if r == nil || json.Unmarshal(r.Value, &v) != nil {
		return "", false
	}
	return v, true
}

// DeviceMemoryView is a CONNECTION view's two Memory rows, raw as composed (nil when the view lists none), and its
// layers' display names.
type DeviceMemoryView struct {
	Names      map[string]string
	Mode       json.RawMessage
	Reflection json.RawMessage
}

// DecodeDeviceMemoryView reads the Memory rows of a CONNECTION view; ok is false for a session view or an
// undecodable frame.
func DecodeDeviceMemoryView(raw json.RawMessage) (DeviceMemoryView, bool) {
	var s struct {
		View struct {
			Observer string `json:"observer"`
		} `json:"view"`
		Layers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"layers"`
		Settings []json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(raw, &s) != nil || s.View.Observer != observerConnection {
		return DeviceMemoryView{}, false
	}
	v := DeviceMemoryView{Names: make(map[string]string, len(s.Layers))}
	for _, l := range s.Layers {
		v.Names[l.ID] = l.Name
	}
	for _, row := range s.Settings {
		v.Store(row)
	}
	return v, true
}

// Store puts a composed row, as a view lists it or _kiro/configuration/set answers it, in its slot; ok is false for
// any other setting or an undecodable row.
func (v *DeviceMemoryView) Store(row json.RawMessage) bool {
	var r deviceMemoryRow
	if json.Unmarshal(row, &r) != nil {
		return false
	}
	switch r.KRN {
	case memoryModeKRN:
		v.Mode = row
	case memoryReflectionKRN:
		v.Reflection = row
	default:
		return false
	}
	return true
}

// Values are the two rows' composed values, KAS's own defaults (disabled, off) for an unstated one.
func (v *DeviceMemoryView) Values() (mode, reflection string) {
	mode, ok := decodeRow(v.Mode).text()
	if !ok {
		mode = memoryModeDisabled
	}
	reflection, ok = decodeRow(v.Reflection).text()
	if !ok {
		reflection = memoryReflectionOff
	}
	return mode, reflection
}

// Choice reduces the two rows as kiro-cli's terminal UI reduces them (mode ?? disabled, reflection ?? off), so a view
// stating neither reads off. A mode outside KAS's three is kept as sent. Lock names the layer locking either row,
// the mode's first.
func (v *DeviceMemoryView) Choice() marotte.DeviceMemory {
	mode, reflection := v.Values()
	out := marotte.DeviceMemory{}
	switch {
	case mode == memoryModeDisabled:
		out.Mode = marotte.DeviceMemoryOff
	case mode == memoryModeReadOnly:
		out.Mode = marotte.DeviceMemoryReadOnly
	case mode == memoryModeReadWrite && reflection == "on":
		out.Mode = marotte.DeviceMemoryLearn
	case mode == memoryModeReadWrite:
		out.Mode = marotte.DeviceMemoryReadWrite
	default:
		out.Mode = displayText(mode)
	}
	if r := decodeRow(v.Mode); r != nil {
		out.Lock = r.lock(v.Names)
	}
	if r := decodeRow(v.Reflection); out.Lock == nil && r != nil {
		out.Lock = r.lock(v.Names)
	}
	return out
}

func decodeRow(raw json.RawMessage) *deviceMemoryRow {
	var r deviceMemoryRow
	if len(raw) == 0 || json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return &r
}

// DeviceMemoryRows maps a Memory choice onto the two rows KAS stores it in: the memoryMode value, then the
// memoryReflection one. ok is false for a mode outside the four.
func DeviceMemoryRows(mode string) (modeValue, reflectionValue string, ok bool) {
	switch mode {
	case marotte.DeviceMemoryOff:
		return memoryModeDisabled, memoryReflectionOff, true
	case marotte.DeviceMemoryReadOnly:
		return memoryModeReadOnly, memoryReflectionOff, true
	case marotte.DeviceMemoryReadWrite:
		return memoryModeReadWrite, memoryReflectionOff, true
	case marotte.DeviceMemoryLearn:
		return memoryModeReadWrite, "on", true
	}
	return "", "", false
}

// modelFallbackKRN prefixes the one modelFallback row KAS states per served model.
const modelFallbackKRN = configurationSettingKRN + "modelFallback/"

// fallbackSetting is one modelFallback row: a chain of model ids per layer, composed into Value.
type fallbackSetting struct {
	KRN           string                 `json:"krn"`
	Compose       string                 `json:"compose"`
	Value         []string               `json:"value"`
	Contributions []fallbackContribution `json:"contributions"`
}

type fallbackContribution struct {
	Stated *struct {
		Value []string `json:"value"`
		Used  bool     `json:"used"`
	} `json:"stated"`
	Layer string `json:"layer"`
}

// layers names the last stated contribution's layer the compose used, and the first target of the last
// vendor-layer statement ("" when it states an empty chain).
func (st *fallbackSetting) layers() (usedLayer, vendorDefault string) {
	for _, c := range st.Contributions {
		if c.Stated == nil {
			continue
		}
		if c.Stated.Used {
			usedLayer = c.Layer
		}
		if slices.Contains(vendorFallbackLayers, c.Layer) {
			vendorDefault = ""
			if len(c.Stated.Value) > 0 {
				vendorDefault = displayText(c.Stated.Value[0])
			}
		}
	}
	return usedLayer, vendorDefault
}

// hasEmptyID reports a chain, composed or stated, holding an empty model id: kiro-cli's terminal UI refuses that
// row.
func (st *fallbackSetting) hasEmptyID() bool {
	if slices.Contains(st.Value, "") {
		return true
	}
	return slices.ContainsFunc(st.Contributions, func(c fallbackContribution) bool {
		return c.Stated != nil && slices.Contains(c.Stated.Value, "")
	})
}

// vendorFallbackLayers state Kiro's own chains; a chain composed from any other layer is the account's or an
// administrator's choice. kiro-cli's terminal UI reads the same two.
var vendorFallbackLayers = []string{"kiro-service", agentDefaultsLayer}

// DecodeModelFallbacks reads a CONNECTION view's modelFallback rows, sorted by model, reduced as kiro-cli's terminal
// UI reduces them. A row whose chain is not a list of model ids is dropped. ok is false for a session view or an
// undecodable frame.
func DecodeModelFallbacks(raw json.RawMessage) ([]marotte.ModelFallback, bool) {
	var s struct {
		View struct {
			Observer string `json:"observer"`
		} `json:"view"`
		Settings []json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(raw, &s) != nil || s.View.Observer != observerConnection {
		return nil, false
	}
	out := []marotte.ModelFallback{}
	for _, row := range s.Settings {
		if f, ok := DecodeModelFallbackSetting(row); ok {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b marotte.ModelFallback) int { return cmp.Compare(a.Model, b.Model) })
	return out, true
}

// DecodeModelFallbackSetting reduces one composed setting, as _kiro/configuration/set answers it; ok is false for
// any other setting or a malformed chain.
func DecodeModelFallbackSetting(raw json.RawMessage) (marotte.ModelFallback, bool) {
	var st fallbackSetting
	if json.Unmarshal(raw, &st) != nil {
		return marotte.ModelFallback{}, false
	}
	model, ok := strings.CutPrefix(st.KRN, modelFallbackKRN)
	if !ok || model == "" || st.hasEmptyID() {
		return marotte.ModelFallback{}, false
	}
	usedLayer, vendorDefault := st.layers()
	f := marotte.ModelFallback{Model: displayText(model), VendorDefault: vendorDefault}
	configured := usedLayer != "" && !slices.Contains(vendorFallbackLayers, usedLayer) && len(st.Value) > 0
	if configured {
		f.Configured = displayText(st.Value[0])
	}
	switch {
	case st.Compose == composeLocked:
		f.ReadOnlyReason = "locked"
	case configured && len(st.Value) > 1:
		f.ReadOnlyReason = "multiple-targets"
	}
	return f, true
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
