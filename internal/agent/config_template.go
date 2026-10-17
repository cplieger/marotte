package agent

// GET /api/config-template serves the mode and model catalog from kiro-cli's session-less
// _kiro/config/template, with an engine call.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/modeltext"
	"github.com/cplieger/marotte/internal/translate"
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
	// Meta is the shared model-choice block, which carries each model's own effort tiers.
	Meta marotte.ModelChoiceMeta `json:"_meta"`
}

type configTemplateAssembly struct {
	response           marotte.ConfigTemplateResponse
	modelOptionPresent bool
}

// handleConfigTemplate serves GET /api/config-template: always 200 with non-null lists, and
// ConfigTemplateResponse.catalog says which outcome produced them. A live session's lists win
// over the template, and the custom agents KAS would load for the workspace are merged over
// either, so the picker lists them before any session has run.
func (rt *Runtime) handleConfigTemplate(w http.ResponseWriter, r *http.Request) {
	u := rt.engine
	cctx, cancel := context.WithTimeout(r.Context(), configTemplateTimeout)
	defer cancel()
	// No early return: a template outage still serves the live catalog.
	out := readTemplate(cctx, u)
	modes, models, stamp := rt.catalog.modesModelsStamped()
	if len(modes) > 0 {
		out.response.Modes = modes
	}
	out.response.Modes = mergeCustomAgents(out.response.Modes, workspaceAgents(cctx, u, rt.lifecycle.workDir))
	if len(models) > 0 {
		out.response.Models = models
	}
	// One read for both lists and their version.
	stamp.Epoch = rt.Epoch()
	out.response.Subject = stamp
	webhttp.WriteJSON(w, withCatalogVerdict(out))
}

// readTemplate reads and decodes `_kiro/config/template`; a failed read or decode is an
// unavailable catalog with its reason.
func readTemplate(ctx context.Context, o *engineOwner) *configTemplateAssembly {
	raw, err := o.configTemplateRaw(ctx)
	if err != nil {
		slog.Warn("config template failed", "error", err)
		return unavailableTemplate(marotte.CatalogReasonRPC)
	}
	var tpl kasConfigTemplate
	if uErr := json.Unmarshal(raw, &tpl); uErr != nil {
		slog.Warn("config template decode failed", "error", uErr)
		return unavailableTemplate(marotte.CatalogReasonDecode)
	}
	return templateToResponse(&tpl)
}

// servedModels is the model list GET /api/config-template serves, and false when that read
// has no catalog to serve (CatalogReady unmet). The template is read only when no session has
// reported a live list, since the live one wins.
func (rt *Runtime) servedModels(ctx context.Context) ([]marotte.SessionModel, bool) {
	if _, models, _ := rt.catalog.modesModelsStamped(); len(models) > 0 {
		return models, true
	}
	cctx, cancel := context.WithTimeout(ctx, configTemplateTimeout)
	defer cancel()
	out := withCatalogVerdict(readTemplate(cctx, rt.engine))
	return out.Models, out.Catalog == marotte.CatalogReady
}

// workspaceAgents is KAS's custom-agent list for workDir, or nil when getState fails: the catalog
// then serves its base modes alone, under the same verdict.
func workspaceAgents(ctx context.Context, o *engineOwner, workDir string) []marotte.SessionMode {
	raw, err := o.workspaceConfigurationRaw(ctx, []string{workDir})
	if err != nil {
		slog.Warn("workspace configuration failed", "error", err)
		return nil
	}
	agents, ok := translate.DecodeCustomAgents(raw)
	if !ok {
		slog.Warn("workspace configuration decode failed")
	}
	return agents
}

// mergeCustomAgents lays agents over base: an agent replaces the base mode of its id in place and
// names that mode's source in Shadows when it differs from its own, and a new id is appended.
// A base mode with no source is bundled, as the template reports the built-in ones.
func mergeCustomAgents(base, agents []marotte.SessionMode) []marotte.SessionMode {
	out := slices.Clone(base)
	for _, a := range agents {
		i := slices.IndexFunc(out, func(m marotte.SessionMode) bool { return m.ID == a.ID })
		if i < 0 {
			out = append(out, a)
			continue
		}
		replaced := cmp.Or(out[i].Source, "bundled")
		if replaced != a.Source {
			a.Shadows = replaced
		}
		out[i] = a
	}
	return out
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
		Modes:  modes,
		Models: []marotte.SessionModel{},
	}}
	for i := range tpl.ConfigOptions {
		opt := &tpl.ConfigOptions[i]
		if opt.ID == marotte.ConfigOptionModel {
			out.modelOptionPresent = true
			_ = json.Unmarshal(opt.CurrentValue, &out.response.DefaultModel) // string; ignore non-string
			out.response.Models = flattenTemplateModels(opt.Options)
		}
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
		out = append(out, c.Meta.SessionModel(c.Value, c.Name, c.Description))
	}
	return out
}
