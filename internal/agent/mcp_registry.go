// MCP runtime registry: which servers kiro-cli reported initialised or failed, from v3
// `_kiro/mcp/status`, cleared on bridge exit. It feeds the MCP page's status column and the
// generated steering's "Connected integrations". Not persisted: bridges re-announce on start.

package agent

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/webhttp/v3"
)

// mcpServerState aliases marotte.MCPServerState.
type mcpServerState = marotte.MCPServerState

const (
	mcpStateIdle      mcpServerState = "idle"
	mcpStateConnected mcpServerState = "connected"
	mcpStateOAuth     mcpServerState = "needs_auth"
	mcpStateFailed    mcpServerState = "failed"
	mcpStateDisabled  mcpServerState = "disabled"
)

// mcpServerRuntime is one server's record. Origin "user" attaches it to marotte's own row;
// anything else gives it a read-only row. Shadows marks one occupying a configured name.
type mcpServerRuntime struct {
	Name              string
	State             mcpServerState
	Origin            marotte.Origin
	OriginRoot        string
	OriginPower       string
	OAuthURL          string
	Error             string
	Tools             []string
	Prompts           []marotte.MCPPromptInfo
	Resources         []marotte.MCPResourceInfo
	ResourceTemplates []marotte.MCPResourceTemplateInfo
	// Relayed is this attempt's single-use relay latch (mcp_oauth_relay.go), set on reservation and
	// kept on delivery, so a resubmit gets "already relayed". A failed relay gives it back.
	Relayed bool
	Shadows bool
}

// mcpRegistry is the in-memory view of connected MCP servers plus its routes and live-chat-bridge calls.
type mcpRegistry struct {
	// bridges looks up a chat's live bridge; live control needs a chat bridge, not the utility one.
	bridges *bridgeManager
	// bus publishes the four MCP lifecycle events.
	bus *bus
	// lifetime supplies the done channel the debounce loop exits on.
	lifetime *lifetime
	// config is the name sets marotte configured. Optional: without it every server is unconfigured.
	config  mcpNameSets `wiring:"optional"`
	servers map[string]*mcpServerRuntime
	// pools is each live bridge's last status snapshot, keyed by bridge; only that bridge's forward
	// loop writes or drops it.
	pools map[ACPBridge][]poolServerJSON
	// onChange is installed later by SetMCPOnChange.
	onChange func() `wiring:"optional"`
	// notifyCh coalesces onChange signals within the debounce window (capacity 1).
	notifyCh chan struct{}
	mu       sync.RWMutex
}

func newMCPRegistry(bridges *bridgeManager, b *bus, lt *lifetime, cfg mcpNameSets) *mcpRegistry {
	r := &mcpRegistry{
		bridges:  bridges,
		bus:      b,
		lifetime: lt,
		config:   cfg,
		servers:  make(map[string]*mcpServerRuntime),
		pools:    make(map[ACPBridge][]poolServerJSON),
		notifyCh: make(chan struct{}, 1),
	}
	return r
}

// SetOnChange wires an invalidation callback fired outside the lock on every mutation, starting the debounce goroutine on first call.
func (reg *mcpRegistry) SetOnChange(fn func()) {
	reg.mu.Lock()
	first := reg.onChange == nil && fn != nil
	reg.onChange = fn
	reg.mu.Unlock()
	if first {
		reg.startNotifier()
	}
}

// Snapshot returns a deep copy of the registry, sorted by server name.
func (reg *mcpRegistry) Snapshot() []mcpServerRuntime {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]mcpServerRuntime, 0, len(reg.servers))
	for _, v := range reg.servers {
		out = append(out, *v)
	}
	slices.SortFunc(out, func(a, b mcpServerRuntime) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// RegisterRoutes wires the status view and the live-control routes (mcp_control.go); these exact
// paths outrank the mcp store's "/api/mcp/" subtree.
func (reg *mcpRegistry) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp/status", reg.handleStatus)
	mux.HandleFunc("/api/mcp/pool", reg.handlePool)
	mux.HandleFunc("/api/mcp/reconnect", reg.handleReconnect)
	mux.HandleFunc("/api/mcp/prompt", reg.handlePrompt)
	mux.HandleFunc("/api/mcp/resource", reg.handleResource)
	// Registered here, or the "/api/mcp/" subtree reads "oauth-relay" as a server id and 404s.
	mux.HandleFunc("/api/mcp/oauth-relay", reg.handleOAuthRelay)
}

// RecordConnected marks a server connected, replaces its four arrays, broadcasts mcp_connected and fires onChange.
func (reg *mcpRegistry) RecordConnected(ctx context.Context, name string, src marotte.MCPSource, tools []string, prompts []marotte.MCPPromptInfo, resources []marotte.MCPResourceInfo, templates []marotte.MCPResourceTemplateInfo) {
	rec := reg.originFor(ctx, name, src).record(name, mcpStateConnected)
	rec.Tools, rec.Prompts, rec.Resources, rec.ResourceTemplates = tools, prompts, resources, templates
	reg.install(rec)

	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPConnected, "", marotte.MCPConnectedPayload{Server: name}))
	reg.signalChange()
}

// setPool replaces bridge's pool with one status frame's snapshot.
func (reg *mcpRegistry) setPool(bridge ACPBridge, servers []translate.MCPPoolServer) {
	pool := make([]poolServerJSON, 0, len(servers))
	for _, s := range servers {
		row := poolServerJSON{Name: s.Name, Resources: s.Resources, ResourceTemplates: s.ResourceTemplates}
		if row.Resources == nil {
			row.Resources = []marotte.MCPResourceInfo{}
		}
		if row.ResourceTemplates == nil {
			row.ResourceTemplates = []marotte.MCPResourceTemplateInfo{}
		}
		pool = append(pool, row)
	}
	slices.SortFunc(pool, func(a, b poolServerJSON) int { return cmp.Compare(a.Name, b.Name) })
	reg.mu.Lock()
	reg.pools[bridge] = pool
	reg.mu.Unlock()
	reg.poolChanged()
}

// dropPool forgets bridge's pool once its forward loop has exited.
func (reg *mcpRegistry) dropPool(bridge ACPBridge) {
	reg.mu.Lock()
	delete(reg.pools, bridge)
	reg.mu.Unlock()
	reg.poolChanged()
}

// poolChanged tells clients a pool moved, so a `#` menu re-reads it.
func (reg *mcpRegistry) poolChanged() {
	reg.bus.Broadcast(reg.lifetime.shutdownCtx, marotte.NewEvent(marotte.EventMCPPoolChanged, "", marotte.MCPPoolChangedPayload{}))
}

type poolServerJSON struct {
	Name              string                            `json:"name"`
	Resources         []marotte.MCPResourceInfo         `json:"resources"`
	ResourceTemplates []marotte.MCPResourceTemplateInfo `json:"resource_templates"`
}

// handlePool serves GET /api/mcp/pool?chat_id=<id>: the serving bridge's resources and
// templates, sorted by server; empty with no bridge.
func (reg *mcpRegistry) handlePool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	chatID := marotte.ChatID(r.URL.Query().Get("chat_id"))
	if chatID == "" {
		httpreply.BadRequest(w, "chat_id required")
		return
	}
	out := reg.servingPool(chatID)
	if out == nil {
		out = []poolServerJSON{}
	}
	webhttp.WriteJSON(w, map[string][]poolServerJSON{"servers": out})
}

var poolSelected = func() {}

// servingPoolAttempts bounds the re-reads a bridge replacement can force.
const servingPoolAttempts = 3

// servingPool is the pool of the bridge serving chatID at read time. A failed session/load can
// swap the bridge mid-read while the old pool is still held, so the selection is re-checked;
// one that keeps moving answers no pool.
func (reg *mcpRegistry) servingPool(chatID marotte.ChatID) []poolServerJSON {
	if reg.bridges == nil {
		return nil
	}
	for range servingPoolAttempts {
		sb := reg.bridges.get(chatID)
		if sb == nil {
			return nil
		}
		b := sb.current()
		poolSelected()
		reg.mu.RLock()
		pool := reg.pools[b]
		reg.mu.RUnlock()
		if reg.bridges.get(chatID) == sb && sb.current() == b {
			return pool
		}
	}
	return nil
}

// RecordOAuth marks a server as waiting for OAuth and broadcasts the URL.
func (reg *mcpRegistry) RecordOAuth(ctx context.Context, name string, src marotte.MCPSource, url string) {
	rec := reg.originFor(ctx, name, src).record(name, mcpStateOAuth)
	rec.OAuthURL = url
	reg.install(rec)

	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPOAuthNeeded, "", marotte.MCPOAuthPayload{Server: name, URL: url}))
	reg.signalChange()
}

// RecordInitFailure marks a server as having failed initialisation and
// broadcasts mcp_failed.
func (reg *mcpRegistry) RecordInitFailure(ctx context.Context, name string, src marotte.MCPSource, errMsg string) {
	rec := reg.originFor(ctx, name, src).record(name, mcpStateFailed)
	rec.Error = errMsg
	reg.install(rec)

	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPFailed, "", marotte.MCPFailedPayload{Server: name, Error: errMsg}))
	reg.signalChange()
}

// RecordDisabled records a server KAS reports "disabled". marotte's own entry is dropped (its config
// row shows off); any other origin becomes a read-only row. No SSE event exists, but onChange fires.
func (reg *mcpRegistry) RecordDisabled(ctx context.Context, name string, src marotte.MCPSource) {
	prov := reg.originFor(ctx, name, src)
	if prov.origin == marotte.OriginUser {
		return
	}
	reg.install(prov.record(name, mcpStateDisabled))
	reg.signalChange()
}

// install replaces a server's record; every transition installs a fresh one, the pointer identity the relay reservation keys on.
func (reg *mcpRegistry) install(rec *mcpServerRuntime) {
	reg.mu.Lock()
	reg.servers[rec.Name] = rec
	reg.mu.Unlock()
}

// oauthAttempt is a granted reservation to relay one authorization attempt's callback, the only
// handle that can give it back. The record pointer is the attempt's identity: every transition
// installs a fresh record, so a stale token's release is a no-op. authURL is copied in so the
// handler never reads a later attempt's anchor; rec is only dereferenced under the lock.
type oauthAttempt struct {
	rec     *mcpServerRuntime
	server  string
	authURL string
}

// beginOAuthRelay reserves the relay for a server's pending authorization, returning the attempt
// and the URL to check against. All under one lock: a concurrent paste gets errRelayAlreadyDone,
// any other state errRelayNoFlow.
func (reg *mcpRegistry) beginOAuthRelay(name string) (oauthAttempt, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	s, exists := reg.servers[name]
	if !exists || s.State != mcpStateOAuth || s.OAuthURL == "" {
		return oauthAttempt{}, errRelayNoFlow
	}
	if s.Relayed {
		return oauthAttempt{}, errRelayAlreadyDone
	}
	s.Relayed = true
	return oauthAttempt{rec: s, server: name, authURL: s.OAuthURL}, nil
}

// releaseOAuthRelay gives back an undelivered reservation (refused paste, transport failure,
// listener refusal); a delivery keeps it as the latch. A no-op unless the attempt is current.
func (reg *mcpRegistry) releaseOAuthRelay(a oauthAttempt) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if a.rec != nil && reg.servers[a.server] == a.rec {
		a.rec.Relayed = false
	}
}

// clearAll wipes the registry and broadcasts mcp_disconnected per server, when the last bridge
// exits: MCP subprocesses live inside kiro-cli.
func (reg *mcpRegistry) clearAll(ctx context.Context) {
	reg.mu.Lock()
	prev := reg.servers
	reg.servers = make(map[string]*mcpServerRuntime)
	reg.mu.Unlock()

	if len(prev) == 0 {
		return
	}
	for name := range prev {
		if ctx.Err() != nil {
			break
		}
		reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPDisconnected, "", marotte.MCPDisconnectedPayload{Server: name}))
	}
	reg.signalChange()
}

// mcpProvenance is where a reported server came from, resolved by originFor.
type mcpProvenance struct {
	origin  marotte.Origin
	root    string
	power   string
	shadows bool
}

// record builds a fresh runtime record carrying this provenance.
func (p mcpProvenance) record(name string, state mcpServerState) *mcpServerRuntime {
	return &mcpServerRuntime{
		Name:        name,
		State:       state,
		Origin:      p.origin,
		OriginRoot:  p.root,
		OriginPower: p.power,
		Shadows:     p.shadows,
	}
}

// originFor reads the provenance KAS stamped on its entry for a name. "user" attributes to
// marotte's row only when marotte's store holds the name, so a spoofed stamp stays foreign.
func (reg *mcpRegistry) originFor(ctx context.Context, name string, src marotte.MCPSource) mcpProvenance {
	var p mcpProvenance
	switch marotte.Origin(src.Origin) {
	case "", marotte.OriginUser:
		p.origin = reg.userOrigin(ctx, name)
		return p
	case marotte.OriginWorkspace:
		p.origin, p.root = marotte.OriginWorkspace, src.Root
	case marotte.OriginPower:
		p.origin, p.power = marotte.OriginPower, src.Power
	case marotte.OriginBundled:
		p.origin = marotte.OriginBundled
	default:
		p.origin = marotte.OriginUnknown
	}
	if reg.config != nil {
		_, p.shadows = reg.config.ConfiguredNames(ctx)[name]
	}
	return p
}

// userOrigin attributes a "user" or unstamped entry by ownership: a name marotte's store holds is marotte's row.
func (reg *mcpRegistry) userOrigin(ctx context.Context, name string) marotte.Origin {
	if reg.config == nil {
		return marotte.OriginUser
	}
	if _, ok := reg.config.ConfiguredNames(ctx)[name]; ok {
		return marotte.OriginUser
	}
	return marotte.OriginUnknown
}

// statusServer is the JSON projection of mcpServerRuntime. Field order must match that struct:
// handleStatus converts directly, so a reorder is a compile error, not a silent swap.
type statusServer struct {
	Name  string                 `json:"name"`
	State marotte.MCPServerState `json:"state"`
	// Origin is never omitted: the client attributes a status to marotte's row only on "user".
	Origin      marotte.Origin `json:"origin"`
	OriginRoot  string         `json:"origin_root,omitempty"`
	OriginPower string         `json:"origin_power,omitempty"`
	OAuthURL    string         `json:"oauth_url,omitempty"`
	Error       string         `json:"error,omitempty"`
	// Tools is the connected server's tool names, for the per-tool deny editor's suggestions.
	Tools             []string                          `json:"tools,omitempty"`
	Prompts           []marotte.MCPPromptInfo           `json:"prompts,omitempty"`
	Resources         []marotte.MCPResourceInfo         `json:"resources,omitempty"`
	ResourceTemplates []marotte.MCPResourceTemplateInfo `json:"resource_templates,omitempty"`
	// Relayed stops a reload or second device offering the paste box for a spent code.
	Relayed bool `json:"relayed,omitempty"`
	// Shadows keeps the client from putting this status on marotte's config row.
	Shadows bool `json:"shadows,omitempty"`
}

// mcpStatusResponse is the typed response for the MCP status endpoint.
type mcpStatusResponse struct {
	Servers []statusServer `json:"servers"`
}

func (reg *mcpRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := reg.Snapshot()
	out := make([]statusServer, len(snap))
	for i := range snap {
		out[i] = statusServer(snap[i])
	}
	webhttp.WriteJSON(w, mcpStatusResponse{Servers: out})
}

// startNotifier runs the one goroutine that drains notifyCh and calls onChange after a short
// debounce, exiting on lifetime.done. Call after SetOnChange. It joins lifetime.loops so Shutdown's
// background-loop wait covers the onChange writer.
func (reg *mcpRegistry) startNotifier() {
	reg.lifetime.loops.Go(func() {
		const debounce = 100 * time.Millisecond
		for {
			select {
			case <-reg.lifetime.done:
				return
			case <-reg.notifyCh:
			}
			t := time.NewTimer(debounce)
			select {
			case <-reg.lifetime.done:
				t.Stop()
				return
			case <-t.C:
			}
			// Drain signals that arrived during the window.
			select {
			case <-reg.notifyCh:
			default:
			}
			reg.mu.RLock()
			cb := reg.onChange
			reg.mu.RUnlock()
			if cb != nil {
				cb()
			}
		}
	})
}

// signalChange signals the notifier without blocking; calls within the window collapse into one.
func (reg *mcpRegistry) signalChange() {
	select {
	case reg.notifyCh <- struct{}{}:
	default:
		// Already signalled.
	}
}
