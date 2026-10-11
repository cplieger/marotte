package agent

// GET /api/config-template serves the mode and model catalog from kiro-cli's session-less
// _kiro/config/template, over the utility bridge.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
	"github.com/cplieger/webhttp/v3"
)

// The client's CATALOG_REQUEST_TIMEOUT_MS (static-src/model-catalog.ts) must stay longer: move them
// together.
const configTemplateTimeout = 45 * time.Second

// The model catalog is the "model" entry.
type kasConfigTemplate struct {
	Modes struct {
		AvailableModes []kasModeInfo `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions []kasConfigOption `json:"configOptions"`
}

type kasModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Meta        struct {
		Kiro struct {
			Source string `json:"source"`
		} `json:"kiro"`
	} `json:"_meta"`
}

type kasConfigOption struct {
	ID           string            `json:"id"`
	CurrentValue json.RawMessage   `json:"currentValue"`
	Options      []kasConfigChoice `json:"options"`
}

type kasConfigChoice struct {
	Value       string            `json:"value"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Options     []kasConfigChoice `json:"options"` // grouped selects nest
	// Meta is the shared model-choice block; tiers are the `effortLevel` option's options[] (marotte.SessionModel).
	Meta marotte.ModelChoiceMeta `json:"_meta"`
}

type configTemplateAssembly struct {
	response           marotte.ConfigTemplateResponse
	modelOptionPresent bool
}

// handleConfigTemplate serves GET /api/config-template: always 200 with non-null lists, and
// ConfigTemplateResponse.catalog says which outcome produced them. A live session's lists
// win: only KAS has resolved workspace agents.
func (rt *Runtime) handleConfigTemplate(w http.ResponseWriter, r *http.Request) {
	u := rt.utility.get()
	cctx, cancel := context.WithTimeout(r.Context(), configTemplateTimeout)
	defer cancel()
	// No early return: a template outage still serves the live catalog.
	var out *configTemplateAssembly
	raw, err := u.session.configTemplateRaw(cctx)
	switch {
	case err != nil:
		slog.Warn("config template failed", "error", err)
		out = unavailableTemplate(marotte.CatalogReasonRPC)
	default:
		var tpl kasConfigTemplate
		if uErr := json.Unmarshal(raw, &tpl); uErr != nil {
			slog.Warn("config template decode failed", "error", uErr)
			out = unavailableTemplate(marotte.CatalogReasonDecode)
		} else {
			out = templateToResponse(&tpl)
		}
	}
	modes, models, stamp := rt.catalog.modesModelsStamped()
	if len(modes) > 0 {
		out.response.Modes = modes
	}
	if len(models) > 0 {
		out.response.Models = models
	}
	// One read for both lists and their version.
	stamp.Epoch = rt.Epoch()
	out.response.Subject = stamp
	webhttp.WriteJSON(w, withCatalogVerdict(out))
}

// withCatalogVerdict is the only conversion from an assembled catalog to its response. A live
// model list is ready without template defaults; when the list is empty, the model option's
// presence separates filtered entries from an omitted catalog.
func withCatalogVerdict(out *configTemplateAssembly) marotte.ConfigTemplateResponse {
	response := out.response
	switch {
	case len(response.Models) > 0 || out.modelOptionPresent:
		response.Catalog = marotte.CatalogReady
	case response.CatalogReason != "":
		response.Catalog = marotte.CatalogUnavailable
	default:
		response.Catalog = marotte.CatalogEmpty
	}
	return response
}

// unavailableTemplate is the one body for a read that produced no catalog, arrays non-null.
func unavailableTemplate(reason marotte.CatalogReason) *configTemplateAssembly {
	return &configTemplateAssembly{response: marotte.ConfigTemplateResponse{
		CatalogReason: reason,
		Modes:         []marotte.SessionMode{},
		Models:        []marotte.SessionModel{},
		EffortLevels:  []marotte.SessionEffortLevel{},
	}}
}

// templateToResponse flattens the template into the client catalog: modes with their source tag
// and models with the per-session [Deprecated]/[Legacy] filtering.
func templateToResponse(tpl *kasConfigTemplate) *configTemplateAssembly {
	modes := make([]marotte.SessionMode, 0, len(tpl.Modes.AvailableModes))
	for i := range tpl.Modes.AvailableModes {
		m := &tpl.Modes.AvailableModes[i]
		if m.ID == "" {
			continue
		}
		modes = append(modes, marotte.SessionMode{
			ID:          m.ID,
			Name:        m.Name,
			Description: m.Description,
			Source:      m.Meta.Kiro.Source,
		})
	}
	out := &configTemplateAssembly{response: marotte.ConfigTemplateResponse{
		Modes:        modes,
		Models:       []marotte.SessionModel{},
		EffortLevels: []marotte.SessionEffortLevel{},
	}}
	for i := range tpl.ConfigOptions {
		opt := &tpl.ConfigOptions[i]
		switch opt.ID {
		case marotte.ConfigOptionModel:
			out.modelOptionPresent = true
			_ = json.Unmarshal(opt.CurrentValue, &out.response.DefaultModel) // string; ignore non-string
			out.response.Models = flattenTemplateModels(opt.Options)
		case marotte.ConfigOptionEffort:
			_ = json.Unmarshal(opt.CurrentValue, &out.response.EffortActive) // string; ignore non-string
			out.response.EffortLevels = flattenTemplateEfforts(opt.Options)
		}
	}
	return out
}

func flattenTemplateEfforts(choices []kasConfigChoice) []marotte.SessionEffortLevel {
	out := make([]marotte.SessionEffortLevel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenTemplateEfforts(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		out = append(out, marotte.SessionEffortLevel{ID: c.Value, Name: c.Name})
	}
	return out
}

// flattenTemplateModels converts the model select's choices (flat or grouped)
// into the domain catalog, dropping hidden-tagged entries.
func flattenTemplateModels(choices []kasConfigChoice) []marotte.SessionModel {
	out := make([]marotte.SessionModel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenTemplateModels(c.Options)...)
			continue
		}
		if c.Value == "" || modeltext.Hidden(c.Description) {
			continue
		}
		out = append(out, marotte.SessionModel{
			ID:                 c.Value,
			Name:               c.Name,
			Description:        c.Description,
			RateMultiplier:     c.Meta.Kiro.RateMultiplier,
			HasEffort:          c.Meta.Kiro.HasEffort,
			DefaultEffortLevel: c.Meta.Kiro.DefaultEffortLevel,
			ThinkingToggleable: c.Meta.Kiro.ThinkingToggleable,
			ThinkingDefaultOff: c.Meta.ThinkingDefaultOff(),
		})
	}
	return out
}
