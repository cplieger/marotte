package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/powers"
	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/webhttp/v3"
)

const (
	methodKiroPowersList    = "_kiro/powers/list"    // C→A request, no params → {powers[], errors[]}
	methodKiroPowersRefresh = "_kiro/powers/refresh" // C→A notification: rescan installed Powers
	// powersCallTimeout bounds the list call; the first call may start the bridge.
	powersCallTimeout = 45 * time.Second
	// powersVerbTimeout bounds an install or removal, the kiro-cli run included.
	powersVerbTimeout = 4 * time.Minute
	// powerRenderFailedCode marks a 502 whose install or removal did happen.
	powerRenderFailedCode = "render_failed"
	// powerPolicyPendingText answers an install refused before the administrator rules were read.
	powerPolicyPendingText = "Your organization's settings have not loaded yet, so Powers cannot be installed. Try again in a moment."
	// powerUnconfiguredText is the load error for a Power whose MCP servers marotte could not read.
	powerUnconfiguredText = "its MCP servers could not be configured"
)

type powersBackend interface {
	Catalog(ctx context.Context) ([]powers.Entry, error)
	Servers(ctx context.Context, name string) ([]string, bool, error)
	Install(ctx context.Context, name string, mode powers.ModeFunc) error
	Uninstall(ctx context.Context, name string, mode powers.ModeFunc) error
	Sync(ctx context.Context, mode powers.ModeFunc) error
}

// WithPowers wires the Powers backend. Unset, the Powers routes answer 503.
func WithPowers(b powersBackend) Option {
	return func(h *Runtime) { h.powersBackend = b }
}

type powersSurface struct {
	backend   powersBackend `wiring:"optional"`
	utility   func() *utilityRuntime
	bridges   *bridgeManager
	broadcast func(context.Context, marotte.ServerEvent)
	locks     func() map[string]marotte.GovernanceLock
	// adminKnown is false until the administrator rules resolve; servers stay off until then.
	adminKnown func() bool
	// refreshAdmin asks for the administrator rules; the boot render calls it.
	refreshAdmin func()
	// detach outlives the request and ends at shutdown (lifetime.TurnContext).
	detach func(context.Context) (context.Context, context.CancelFunc)
	// wake asks syncLoop for one legacy-block sync; capacity 1 coalesces a burst.
	wake chan struct{}
}

// issues are decoded leniently and only counted.
type kasPower struct {
	Meta struct {
		Kiro struct {
			Resource struct {
				Source struct {
					Origin string `json:"origin"`
				} `json:"source"`
			} `json:"resource"`
		} `json:"kiro"`
	} `json:"_meta"`
	Name             string            `json:"name"`
	DisplayName      string            `json:"displayName"`
	Description      string            `json:"description"`
	MCPServerNames   []string          `json:"mcpServerNames"`
	SkillNames       []string          `json:"skillNames"`
	Issues           []json.RawMessage `json:"issues"`
	HasSteeringFiles bool              `json:"hasSteeringFiles"`
	IsAgentPlugin    bool              `json:"isAgentPlugin"`
}

// powerRow is one GET /api/powers row: a catalogue entry, an installed Power, or both.
type powerRow struct {
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name,omitempty"`
	Description   string   `json:"description,omitempty"`
	Author        string   `json:"author,omitempty"`
	Category      string   `json:"category,omitempty"`
	PublisherTier string   `json:"publisher_tier,omitempty"`
	AuthType      string   `json:"auth_type,omitempty"`
	Origin        string   `json:"origin,omitempty"`
	MCPServers    []string `json:"mcp_servers,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	Issues        int      `json:"issues,omitempty"`
	Installed     bool     `json:"installed"`
	InCatalog     bool     `json:"in_catalog"`
	AgentPlugin   bool     `json:"agent_plugin,omitempty"`
	Steering      bool     `json:"steering,omitempty"`
}

const (
	powersReady       = "ready"
	powersUnavailable = "unavailable"
	// powersPartial: KAS listed the Powers it could load and reported others it could not.
	powersPartial = "partial"
)

// maxPowerLoadErrors bounds one list: KAS reports one entry per failed Power directory.
const maxPowerLoadErrors = 32

// powerLoadError is one `_kiro/powers/list` errors[] entry. Its `source`, a home-tree path, is never decoded or forwarded.
type powerLoadError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

type powersListResponse struct {
	Catalog    string           `json:"catalog"`
	Installed  string           `json:"installed"`
	Blocked    string           `json:"blocked,omitempty"`
	Powers     []powerRow       `json:"powers"`
	LoadErrors []powerLoadError `json:"load_errors,omitempty"`
	// PolicyPending is true until the administrator rules are read; installs are refused until then.
	PolicyPending bool `json:"policy_pending,omitempty"`
}

func (ps *powersSurface) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/powers", ps.handleList)
	mux.HandleFunc("GET /api/powers/{name}/servers", ps.handleServers)
	mux.HandleFunc("POST /api/powers/{name}/install", ps.handleInstall)
	mux.HandleFunc("DELETE /api/powers/{name}", ps.handleUninstall)
}

func (ps *powersSurface) unavailable(w http.ResponseWriter) bool {
	if ps.backend != nil {
		return false
	}
	webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("Powers are not available"))
	return true
}

func (ps *powersSurface) handleList(w http.ResponseWriter, r *http.Request) {
	if ps.unavailable(w) {
		return
	}
	ctx := r.Context()
	// A Power installed from a shell has no legacy block until something renders it.
	var unconfigured []string
	if err := ps.backend.Sync(ctx, ps.serversMode); err != nil {
		if partial, ok := errors.AsType[*powers.PartialError](err); ok {
			unconfigured = partial.Failed
		} else {
			slog.Warn("powers: legacy block sync failed", "error", err)
		}
	}
	// The two reads are independent and either may be slow, so they overlap.
	var (
		catalog             []powers.Entry
		catalogErr, listErr error
		installed           []kasPower
		loadErrors          []powerLoadError
		wg                  sync.WaitGroup
	)
	wg.Go(func() { catalog, catalogErr = ps.backend.Catalog(ctx) })
	wg.Go(func() { installed, loadErrors, listErr = ps.installed(ctx) })
	wg.Wait()
	resp := powersListResponse{Catalog: powersReady, Installed: powersReady, PolicyPending: !ps.adminKnown()}
	resp.Blocked, _ = ps.blocked()
	if catalogErr != nil {
		slog.Warn("powers: catalogue unavailable", "error", catalogErr)
		resp.Catalog = powersUnavailable
	}
	loadErrors = withUnconfigured(loadErrors, unconfigured)
	if listErr != nil {
		slog.Warn("powers: installed list unavailable", "error", listErr)
		resp.Installed = powersUnavailable
	} else if len(loadErrors) > 0 {
		resp.Installed = powersPartial
		resp.LoadErrors = loadErrors
	}
	resp.Powers = mergePowers(catalog, installed)
	webhttp.WriteJSON(w, resp)
}

func (ps *powersSurface) installed(ctx context.Context) ([]kasPower, []powerLoadError, error) {
	cctx, cancel := context.WithTimeout(ctx, powersCallTimeout)
	defer cancel()
	raw, err := ps.utility().session.rawCall(cctx, "powers list", methodKiroPowersList, callerParams(nil))
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Powers []kasPower       `json:"powers"`
		Errors []powerLoadError `json:"errors"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, nil, err
		}
	}
	return res.Powers, loadErrorsFrom(res.Errors), nil
}

func loadErrorsFrom(in []powerLoadError) []powerLoadError {
	var out []powerLoadError
	for _, e := range in {
		if len(out) == maxPowerLoadErrors {
			break
		}
		name, msg := powerText(e.Name), powerText(e.Message)
		if name == "" && msg == "" {
			continue
		}
		out = append(out, powerLoadError{Name: name, Message: msg})
	}
	return out
}

// withUnconfigured adds a load error for each Power whose servers marotte skipped
// and KAS did not already report, within maxPowerLoadErrors.
func withUnconfigured(errs []powerLoadError, names []string) []powerLoadError {
	for _, name := range names {
		if len(errs) == maxPowerLoadErrors {
			break
		}
		name = powerText(name)
		if slices.ContainsFunc(errs, func(e powerLoadError) bool { return e.Name == name }) {
			continue
		}
		errs = append(errs, powerLoadError{Name: name, Message: powerUnconfiguredText})
	}
	return errs
}

// mergePowers lists every catalogue entry in catalogue order, marked installed
// when KAS reports it, then the installed Powers the catalogue does not carry.
func mergePowers(catalog []powers.Entry, installed []kasPower) []powerRow {
	byName := make(map[string]*kasPower, len(installed))
	for i := range installed {
		if powers.ValidName(installed[i].Name) {
			byName[installed[i].Name] = &installed[i]
		}
	}
	out := make([]powerRow, 0, len(catalog)+len(byName))
	seen := make(map[string]bool, len(catalog))
	for i := range catalog {
		e := &catalog[i]
		row := powerRow{
			Name: e.Name, DisplayName: e.DisplayName, Description: e.Description,
			Author: e.Author, Category: e.Category, PublisherTier: e.PublisherTier,
			AuthType: e.AuthType, InCatalog: true,
		}
		if k := byName[e.Name]; k != nil {
			applyInstalled(&row, k)
		}
		seen[e.Name] = true
		out = append(out, row)
	}
	for i := range installed {
		k := &installed[i]
		if byName[k.Name] != k || seen[k.Name] {
			continue
		}
		row := powerRow{Name: k.Name, DisplayName: powerText(k.DisplayName), Description: powerText(k.Description)}
		applyInstalled(&row, k)
		out = append(out, row)
	}
	return out
}

func applyInstalled(row *powerRow, k *kasPower) {
	row.Installed = true
	row.Origin = powerText(k.Meta.Kiro.Resource.Source.Origin)
	row.MCPServers = powerTexts(k.MCPServerNames)
	row.Skills = powerTexts(k.SkillNames)
	row.Issues = len(k.Issues)
	row.AgentPlugin = k.IsAgentPlugin
	row.Steering = k.HasSteeringFiles
}

func powerText(s string) string { return runesafe.SanitizeSingleLineBounded(s, 512) }

func powerTexts(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = powerText(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type powerServersResponse struct {
	Servers []string `json:"servers"`
	Known   bool     `json:"known"`
}

func (ps *powersSurface) handleServers(w http.ResponseWriter, r *http.Request) {
	if ps.unavailable(w) {
		return
	}
	name, ok := powerName(w, r)
	if !ok {
		return
	}
	servers, known, err := ps.backend.Servers(r.Context(), name)
	if err != nil {
		writePowerErr(w, err)
		return
	}
	if servers == nil {
		servers = []string{}
	}
	webhttp.WriteJSON(w, powerServersResponse{Servers: servers, Known: known})
}

// handleInstall refuses under an administrator powers lock; removal stays open.
func (ps *powersSurface) handleInstall(w http.ResponseWriter, r *http.Request) {
	if reason, locked := ps.blocked(); locked {
		httpreply.Forbidden(w, reason)
		return
	}
	ps.runVerb(w, r, "installed", powersBackend.Install)
}

func (ps *powersSurface) handleUninstall(w http.ResponseWriter, r *http.Request) {
	ps.runVerb(w, r, "removed", powersBackend.Uninstall)
}

func (ps *powersSurface) blocked() (string, bool) {
	if ps.locks == nil {
		return "", false
	}
	l, ok := ps.locks()[marotte.LockPowers]
	return l.Reason, ok
}

// serversMode is handed over unevaluated, so the lock is read inside the Manager's write lock.
// It fails closed until the administrator rules are known.
func (ps *powersSurface) serversMode() powers.ServersMode {
	if !ps.adminKnown() {
		return powers.ServersUnresolved
	}
	if _, locked := ps.blocked(); locked {
		return powers.ServersSuppressed
	}
	return powers.ServersActive
}

// runVerb takes a method expression so no method value is read off a nil backend
// before unavailable answers 503.
func (ps *powersSurface) runVerb(w http.ResponseWriter, r *http.Request, done string, verb func(powersBackend, context.Context, string, powers.ModeFunc) error) {
	if ps.unavailable(w) {
		return
	}
	name, ok := powerName(w, r)
	if !ok {
		return
	}
	// A closed tab must not kill kiro-cli mid-install; a shutdown must.
	dctx, release := ps.detach(r.Context())
	defer release()
	ctx, cancel := context.WithTimeout(dctx, powersVerbTimeout)
	defer cancel()
	err := verb(ps.backend, ctx, name, ps.serversMode)
	renderErr, rendered := errors.AsType[*powers.RenderError](err)
	if errors.Is(err, powers.ErrPowersLocked) {
		reason, _ := ps.blocked()
		httpreply.Forbidden(w, cmp.Or(reason, lockReasonPowers))
		return
	}
	if err != nil && !rendered {
		writePowerErr(w, err)
		return
	}
	// A render failure follows a change kiro-cli already made, so everyone must still see it.
	ps.refresh(ctx)
	ps.broadcastChanged()
	if rendered {
		slog.Warn("powers: change made but its MCP servers were not configured", "power", name, "error", renderErr)
		msg := fmt.Sprintf("%s was %s, but its MCP servers could not be configured", name, done)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSONWithCode(msg, powerRenderFailedCode))
		return
	}
	webhttp.Ok(w)
}

// refresh tells every live process to rescan installed Powers: each chat and run bridge, and a running utility session.
func (ps *powersSurface) refresh(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, ignorePushTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for chatID, sb := range ps.bridges.all() {
		wg.Go(func() {
			if err := sb.Notify(cctx, methodKiroPowersRefresh, map[string]any{}); err != nil {
				slog.Warn("powers: refresh not sent", "chat_id", chatID, "error", err)
			}
		})
	}
	wg.Go(func() {
		if err := ps.utility().session.notifyIfLive(cctx, methodKiroPowersRefresh, map[string]any{}); err != nil {
			slog.Warn("powers: refresh not sent to the utility session", "error", err)
		}
	})
	wg.Wait()
}

// itemsChanged handles `_kiro/powers/items_changed`: the legacy block re-renders and clients
// refetch. It only wakes syncLoop: it runs on a forward loop and Sync can wait minutes.
func (ps *powersSurface) itemsChanged(_ context.Context, params json.RawMessage) {
	var p struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Status == "failed" {
		slog.Debug("powers: kiro-cli reported a failed powers scan")
		return
	}
	ps.requestSync()
}

func (ps *powersSurface) requestSync() {
	select {
	case ps.wake <- struct{}{}:
	default:
	}
}

func (ps *powersSurface) syncLoop(ctx context.Context, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ps.wake:
		}
		if ps.backend != nil {
			sctx, cancel := context.WithTimeout(ctx, powersVerbTimeout)
			if err := ps.backend.Sync(sctx, ps.serversMode); err != nil {
				slog.Warn("powers: legacy block sync failed", "error", err)
			}
			cancel()
		}
		ps.broadcastChanged()
	}
}

// SyncPowers renders the legacy block under the locks known now (servers suppressed at boot) and
// requests the administrator rules, re-rendering on their resolution and every lock change.
func (rt *Runtime) SyncPowers(ctx context.Context) {
	if rt.powers.backend == nil {
		return
	}
	if err := rt.powers.backend.Sync(ctx, rt.powers.serversMode); err != nil {
		slog.Warn("powers: legacy block not rendered at boot", "error", err)
	}
	rt.powers.refreshAdmin()
}

func (ps *powersSurface) broadcastChanged() {
	ps.broadcast(context.Background(), marotte.NewEvent(marotte.EventPowersChanged, "", marotte.PowersChangedPayload{}))
}

func powerName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !powers.ValidName(name) {
		httpreply.BadRequest(w, "invalid power name")
		return "", false
	}
	return name, true
}

// writePowerErr maps an unknown name to 404, a kiro-cli refusal to its own sentence, anything else to a generic 502.
func writePowerErr(w http.ResponseWriter, err error) {
	if errors.Is(err, powers.ErrUnknownPower) {
		httpreply.NotFound(w, "no such power in the catalogue")
		return
	}
	if errors.Is(err, powers.ErrPolicyUnknown) {
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON(powerPolicyPendingText))
		return
	}
	if cliErr, ok := errors.AsType[*powers.CLIError](err); ok {
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(cliErr.Message))
		return
	}
	slog.Warn("powers op failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("powers request failed"))
}
