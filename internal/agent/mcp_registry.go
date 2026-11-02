// MCP runtime registry: which servers kiro-cli reported initialised or failed, from v3
// `_kiro/mcp/status`, cleared on bridge exit. It feeds the MCP page's status column and the
// generated steering's "Connected integrations". Not persisted: bridges re-announce on start.

package agent

import (
	"cmp"
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/webhttp/v4"
)

type mcpServerState = marotte.MCPServerState

const (
	mcpStateConnected mcpServerState = "connected"
	mcpStateOAuth     mcpServerState = "needs_auth"
	mcpStateSigningIn mcpServerState = "signing_in"
	mcpStateFailed    mcpServerState = "failed"
	mcpStateDisabled  mcpServerState = "disabled"
)

// signInSlotTTL bounds a sign-in slot nobody released: KAS's 600 s connect ceiling plus margin.
const signInSlotTTL = 630 * time.Second

// lazySignInTTL is KAS's LAZY_AUTH_TIMEOUT_MS: its callback listener for a mid-session re-auth waits this long.
const lazySignInTTL = 300 * time.Second

// maxLazySignIns bounds the anchors: KAS runs one re-auth per server, and the oldest is evicted past it.
const maxLazySignIns = 8

// lazySignIn is a consent URL no Sign in claimed, a mid-session re-auth: the URL names no server, so a pasted
// callback's state picks it. relayed is its single-use latch, as mcpServerRuntime.Relayed is a row's.
type lazySignIn struct {
	deadline time.Time
	authURL  string
	state    string
	relayed  bool
}

// signInSlot is the one sign-in the engine runs at a time. _kiro/openExternalUrl carries only the URL, so a
// second concurrent sign-in would make its consent page unattributable.
type signInSlot struct {
	deadline time.Time
	server   string
}

// Origin "user" attaches it to marotte's own row; anything else gives it a read-only row. Shadows
// marks one occupying a configured name.
type mcpServerRuntime struct {
	Name        string
	State       mcpServerState
	Origin      marotte.Origin
	OriginRoot  string
	OriginPower string
	// OAuthURL is the consent URL KAS opened for a Sign in, set only while signing_in: the relay's anchor.
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

type mcpRegistry struct {
	// config is the name sets marotte configured. Optional: without it every server is unconfigured.
	config mcpNameSets `wiring:"optional"`
	// engine is the one engine whose MCP pool every session shares; live control is an engine call.
	engine *engineOwner
	// bus publishes the MCP lifecycle events.
	bus *bus
	// lifetime supplies the done channel the debounce loop exits on.
	lifetime *lifetime
	servers  map[string]*mcpServerRuntime
	// onChange is installed later by SetMCPOnChange.
	onChange func() `wiring:"optional"`
	// notifyCh coalesces onChange signals within the debounce window (capacity 1).
	notifyCh chan struct{}
	// pool is the engine's last status snapshot: KAS sends its one pool's _kiro/mcp/status to every session, so any
	// copy is the engine's. Cleared at the engine's end.
	pool []poolServerJSON
	// now reads the clock for the sign-in slot's and the lazy anchors' deadlines.
	now         func() time.Time
	signIn      signInSlot
	lazySignIns []*lazySignIn
	mu          sync.RWMutex
}

var _ signInURLClaimer = (*mcpRegistry)(nil)

func newMCPRegistry(engine *engineOwner, b *bus, lt *lifetime, cfg mcpNameSets) *mcpRegistry {
	r := &mcpRegistry{
		engine:   engine,
		bus:      b,
		lifetime: lt,
		config:   cfg,
		servers:  make(map[string]*mcpServerRuntime),
		notifyCh: make(chan struct{}, 1),
		now:      time.Now,
	}
	return r
}

// setOnChange wires an invalidation callback fired outside the lock on every mutation, starting the debounce goroutine on first call.
func (reg *mcpRegistry) setOnChange(fn func()) {
	reg.mu.Lock()
	first := reg.onChange == nil && fn != nil
	reg.onChange = fn
	reg.mu.Unlock()
	if first {
		reg.startNotifier()
	}
}

// snapshot returns a deep copy of the registry, sorted by server name.
func (reg *mcpRegistry) snapshot() []mcpServerRuntime {
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
	mux.HandleFunc("/api/mcp/sign-in", reg.handleSignIn)
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

func (reg *mcpRegistry) setPool(servers []translate.MCPPoolServer) {
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
	changed := !slices.EqualFunc(reg.pool, pool, poolServerEqual)
	reg.pool = pool
	reg.mu.Unlock()
	if changed {
		reg.poolChanged()
	}
}

func poolServerEqual(a, b poolServerJSON) bool {
	return a.Name == b.Name && slices.Equal(a.Resources, b.Resources) && slices.Equal(a.ResourceTemplates, b.ResourceTemplates)
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

// handlePool serves GET /api/mcp/pool: the engine's MCP resources and templates, sorted by server; empty before any
// session reported them.
func (reg *mcpRegistry) handlePool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	reg.mu.RLock()
	out := slices.Clone(reg.pool)
	reg.mu.RUnlock()
	if out == nil {
		out = []poolServerJSON{}
	}
	webhttp.WriteJSON(w, map[string][]poolServerJSON{"servers": out})
}

// RecordOAuth marks a server as waiting for sign-in. It keeps no URL: the one a failed status entry carries
// belongs to an attempt whose listener KAS already closed, so only a Sign in can complete.
func (reg *mcpRegistry) RecordOAuth(ctx context.Context, name string, src marotte.MCPSource) {
	reg.install(reg.originFor(ctx, name, src).record(name, mcpStateOAuth))

	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPOAuthNeeded, "", marotte.MCPOAuthPayload{Server: name}))
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
		reg.releaseSignIn(name)
		return
	}
	reg.install(prov.record(name, mcpStateDisabled))
	reg.signalChange()
}

// Every transition installs a fresh one, the pointer identity the relay reservation keys on. Every status KAS
// reports for the server the sign-in slot names ends that sign-in, so it frees the slot.
func (reg *mcpRegistry) install(rec *mcpServerRuntime) {
	reg.mu.Lock()
	reg.servers[rec.Name] = rec
	if reg.signIn.server == rec.Name {
		reg.signIn = signInSlot{}
	}
	reg.mu.Unlock()
}

// anchorSignInURL records a consent URL no Sign in claimed and returns when KAS stops waiting for its callback. A
// URL with no state could never match a paste, so it is not kept.
func (reg *mcpRegistry) anchorSignInURL(authURL string) time.Time {
	deadline := reg.now().Add(lazySignInTTL)
	u, err := url.Parse(authURL)
	if err != nil {
		return deadline
	}
	state := u.Query().Get("state")
	if state == "" {
		return deadline
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.pruneLazySignInsLocked()
	if len(reg.lazySignIns) >= maxLazySignIns {
		reg.lazySignIns = slices.Delete(reg.lazySignIns, 0, len(reg.lazySignIns)-maxLazySignIns+1)
	}
	reg.lazySignIns = append(reg.lazySignIns, &lazySignIn{deadline: deadline, authURL: authURL, state: state})
	return deadline
}

func (reg *mcpRegistry) pruneLazySignInsLocked() {
	now := reg.now()
	reg.lazySignIns = slices.DeleteFunc(reg.lazySignIns, func(l *lazySignIn) bool { return !now.Before(l.deadline) })
}

// oauthAttempt is a granted reservation to relay one authorization attempt's callback, the only
// handle that can give it back. The record pointer (rec, or lazy for a sign-in no Sign in started) is the
// attempt's identity: every transition installs a fresh record, so a stale token's release is a no-op. authURL is
// copied in so the handler never reads a later attempt's anchor; rec and lazy are only dereferenced under the lock.
type oauthAttempt struct {
	rec     *mcpServerRuntime
	lazy    *lazySignIn
	server  string
	authURL string
}

// beginOAuthRelay reserves the named server's Sign in, or else the unexpired lazy anchor whose state is the
// pasted callback's. All under one lock: a concurrent paste gets errRelayAlreadyDone, a state no anchor holds
// errRelayStateDrift, nothing in flight errRelayNoFlow.
func (reg *mcpRegistry) beginOAuthRelay(name, pastedState string) (oauthAttempt, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if s, exists := reg.servers[name]; exists && s.State == mcpStateSigningIn && s.OAuthURL != "" {
		if s.Relayed {
			return oauthAttempt{}, errRelayAlreadyDone
		}
		s.Relayed = true
		return oauthAttempt{rec: s, server: name, authURL: s.OAuthURL}, nil
	}
	reg.pruneLazySignInsLocked()
	if len(reg.lazySignIns) == 0 {
		return oauthAttempt{}, errRelayNoFlow
	}
	for _, l := range reg.lazySignIns {
		// Constant-time, as matchAdvertisedCallback's: the state is secret-equivalent.
		if subtle.ConstantTimeCompare([]byte(l.state), []byte(pastedState)) != 1 {
			continue
		}
		if l.relayed {
			return oauthAttempt{}, errRelayAlreadyDone
		}
		l.relayed = true
		return oauthAttempt{lazy: l, server: name, authURL: l.authURL}, nil
	}
	return oauthAttempt{}, errRelayStateDrift
}

// releaseOAuthRelay gives back an undelivered reservation (refused paste, transport failure,
// listener refusal); a delivery keeps it as the latch. A no-op unless the attempt is current.
func (reg *mcpRegistry) releaseOAuthRelay(a oauthAttempt) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	switch {
	case a.rec != nil && reg.servers[a.server] == a.rec:
		a.rec.Relayed = false
	case a.lazy != nil && slices.Contains(reg.lazySignIns, a.lazy):
		a.lazy.relayed = false
	}
}

// clearAll wipes the registry, the pool and the lazy anchors and broadcasts mcp_disconnected per server when the engine ends: MCP
// subprocesses live inside kiro-cli.
func (reg *mcpRegistry) clearAll(ctx context.Context) {
	reg.mu.Lock()
	prev := reg.servers
	reg.servers = make(map[string]*mcpServerRuntime)
	reg.signIn = signInSlot{}
	reg.lazySignIns = nil
	hadPool := len(reg.pool) > 0
	reg.pool = nil
	reg.mu.Unlock()
	if hadPool {
		reg.poolChanged()
	}

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

type mcpProvenance struct {
	origin  marotte.Origin
	root    string
	power   string
	shadows bool
}

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

// Field order must match that struct: handleStatus converts directly, so a reorder is a compile
// error, not a silent swap.
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

type mcpStatusResponse struct {
	Servers []statusServer `json:"servers"`
}

func (reg *mcpRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := reg.snapshot()
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
