// MCP runtime registry: one view per session from v3 `_kiro/mcp/status` (each frame replaces its session's view
// whole), each view's own sign-in, and the by-name records derived from the views for the MCP page, the OAuth relay
// and the generated steering's "Connected integrations". Not persisted: a view ends with its session, and every view
// with the engine.

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

// signInTTL bounds a sign-in nobody ended: KAS's 600 s connect ceiling plus margin.
const signInTTL = 630 * time.Second

// lazySignInTTL is KAS's LAZY_AUTH_TIMEOUT_MS: its callback listener for a mid-session re-auth waits this long.
const lazySignInTTL = 300 * time.Second

// maxLazySignIns bounds the anchors: KAS runs one re-auth per server, and the oldest is evicted past it.
const maxLazySignIns = 8

// lazySignIn is a consent URL no Sign in claimed, a mid-session re-auth: the URL names no server or session, so a
// pasted callback's state picks it. relayed is its single-use latch, as mcpServerRuntime.Relayed is a record's.
type lazySignIn struct {
	deadline time.Time
	authURL  string
	state    string
	relayed  bool
}

// viewSignIn is a Sign in the user started in the view's session. authURL is the consent URL KAS opened for it, ""
// until it opens one.
type viewSignIn struct {
	deadline time.Time
	server   string
	authURL  string
	// attempt is the claim that filed it: a sign-in that outlives it (a status ended it and a second Sign in filed
	// another) must not end the second one.
	attempt uint64
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

// mcpView is one session's last _kiro/mcp/status frame and the Sign in standing in that session.
type mcpView struct {
	registry *marotte.GovernanceMCPRegistry
	// signIn is nil unless a Sign in for one of the session's servers stands. At most one unexpired sign-in stands
	// across the views: the consent URL KAS opens names neither its session nor its server.
	signIn *viewSignIn
	// failedSignIn is the last Sign in the engine refused in this session, kept while its server still waits so the
	// row says why and offers Sign in again; a new claim clears it.
	failedSignIn viewSignInFailure
	session      marotte.SessionID
	servers      []translate.MCPServerReport
}

// viewSignInFailure is a refused Sign in's server and the engine's reason; the zero value is none.
type viewSignInFailure struct {
	server string
	reason string
}

type mcpRegistry struct {
	// config is the name sets marotte configured. Optional: without it every server is unconfigured.
	config mcpNameSets `wiring:"optional"`
	// engine is the one engine every session's MCP instances live in; live control is an engine call.
	engine *engineOwner
	// bus publishes the MCP lifecycle events.
	bus *bus
	// lifetime supplies the done channel the debounce loop exits on.
	lifetime *lifetime
	// sessionOf names the session a chat's live bridge serves.
	sessionOf func(marotte.ChatID) (marotte.SessionID, bool)
	// setGovernance receives the union of the views' MCP registry blocks.
	setGovernance func(context.Context, *marotte.GovernanceMCPRegistry)
	// servers is derived from views (deriveLocked), never written on its own.
	servers map[string]*mcpServerRuntime
	// onChange is installed later by SetMCPOnChange.
	onChange func() `wiring:"optional"`
	// notifyCh coalesces onChange signals within the debounce window (capacity 1).
	notifyCh chan struct{}
	// launchResets counts each server's automatic resets since it last connected (resetFailedForLaunch).
	launchResets map[string]int
	// now reads the clock for the sign-ins' and the lazy anchors' deadlines.
	now func() time.Time
	// views is in recording order; resolveServers picks the view that represents each name.
	views []mcpView
	// lazySignIns are engine-wide: a mid-session re-auth's consent URL names no session to file it under.
	lazySignIns []*lazySignIn
	// signIns counts the sign-ins claimed, minting viewSignIn.attempt.
	signIns uint64
	mu      sync.RWMutex
}

var _ signInURLClaimer = (*mcpRegistry)(nil)

func newMCPRegistry(engine *engineOwner, b *bus, lt *lifetime, cfg mcpNameSets,
	sessionOf func(marotte.ChatID) (marotte.SessionID, bool),
	setGovernance func(context.Context, *marotte.GovernanceMCPRegistry),
) *mcpRegistry {
	return &mcpRegistry{
		engine:        engine,
		bus:           b,
		lifetime:      lt,
		config:        cfg,
		sessionOf:     sessionOf,
		setGovernance: setGovernance,
		servers:       make(map[string]*mcpServerRuntime),
		launchResets:  make(map[string]int),
		notifyCh:      make(chan struct{}, 1),
		now:           time.Now,
	}
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

// RecordSessionStatus replaces sessionID's view with its frame and re-derives the records. A server the frame
// reports connecting keeps the view's previous entry for it, and its standing Sign in; any other report for the
// signing-in server ends that sign-in.
func (reg *mcpRegistry) RecordSessionStatus(ctx context.Context, sessionID marotte.SessionID, status translate.MCPSessionStatus) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	i := reg.viewIndexLocked(sessionID)
	var prev []translate.MCPServerReport
	var signIn *viewSignIn
	var failed viewSignInFailure
	if i >= 0 {
		prev, signIn, failed = reg.views[i].servers, reg.views[i].signIn, reg.views[i].failedSignIn
	}
	if signIn != nil && !reportsState(status.Servers, signIn.server, translate.MCPReportConnecting) {
		signIn = nil
	}
	if failed.server != "" && !reportsState(status.Servers, failed.server, translate.MCPReportNeedsAuth) {
		failed = viewSignInFailure{}
	}
	v := mcpView{
		session: sessionID, registry: status.Registry, signIn: signIn, failedSignIn: failed,
		servers: settleConnecting(status.Servers, prev),
	}
	poolMoved := i < 0 || !slices.EqualFunc(viewPool(prev), viewPool(v.servers), poolServerEqual)
	if i >= 0 {
		reg.views[i] = v
	} else {
		reg.views = append(reg.views, v)
	}
	d := reg.deriveLocked(configured)
	d.poolMoved = poolMoved
	reg.mu.Unlock()
	reg.publish(ctx, d)
}

// dropSessions ends the views of sessions marotte no longer holds: KAS sends nothing at a session-scoped close.
func (reg *mcpRegistry) dropSessions(ctx context.Context, ids []marotte.SessionID) {
	if len(ids) == 0 {
		return
	}
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	n := len(reg.views)
	reg.views = slices.DeleteFunc(reg.views, func(v mcpView) bool { return slices.Contains(ids, v.session) })
	if len(reg.views) == n {
		reg.mu.Unlock()
		return
	}
	d := reg.deriveLocked(configured)
	d.poolMoved = true
	reg.mu.Unlock()
	reg.publish(ctx, d)
}

func (reg *mcpRegistry) dropSession(ctx context.Context, id marotte.SessionID) {
	reg.dropSessions(ctx, []marotte.SessionID{id})
}

func (reg *mcpRegistry) viewIndexLocked(id marotte.SessionID) int {
	return slices.IndexFunc(reg.views, func(v mcpView) bool { return v.session == id })
}

// mcpResolution is what the views say about one server name. Every reader of the views asks resolveServers which
// session represents a name, so the row, its Reconnect and Sign in, and "Connected integrations" never disagree.
type mcpResolution struct {
	// report is the entry the name's record derives from, in views[view]: the oldest view of the best-ranked state
	// (reportRank). A session where the server is not working outranks a connected one because KAS keeps one instance
	// per declaration, and a session holding another declaration of the name stays waiting or failed through the
	// other's sign-in, a reset and a reopen (kiro-cli 3.0.0), so only an action on that session repairs it.
	report *translate.MCPServerReport
	// session is report's session, the one the row's Reconnect and Sign in address.
	session marotte.SessionID
	// signInError is why the engine refused the last Sign in in session, "" when it did not.
	signInError string
	view        int
	// connected is whether any view reports the name connected, the agent able to use it.
	connected bool
}

// resolveServers resolves every name the views list, in the order they first list it.
func resolveServers(views []mcpView) []mcpResolution {
	var out []mcpResolution
	for vi := range views {
		v := &views[vi]
		for i := range v.servers {
			s := &v.servers[i]
			j := slices.IndexFunc(out, func(r mcpResolution) bool { return r.report.Name == s.Name })
			if j < 0 {
				out = append(out, mcpResolution{report: s, session: v.session, view: vi})
				j = len(out) - 1
			} else if reportRank(s.State) < reportRank(out[j].report.State) {
				out[j].report, out[j].session, out[j].view = s, v.session, vi
			}
			out[j].connected = out[j].connected || s.State == translate.MCPReportConnected
		}
	}
	for j := range out {
		r := &out[j]
		if f := views[r.view].failedSignIn; f.server == r.report.Name && r.report.State == translate.MCPReportNeedsAuth {
			r.signInError = f.reason
		}
	}
	return out
}

// reportRank orders the states a name's row derives from, lowest first: a wait for sign-in, then a failure, then a
// connection, then the rest.
func reportRank(state translate.MCPReportState) int {
	switch state {
	case translate.MCPReportNeedsAuth:
		return 0
	case translate.MCPReportFailed:
		return 1
	case translate.MCPReportConnected:
		return 2
	default:
		return 3
	}
}

// resolveLocked is name's resolution; false when no view lists it.
func (reg *mcpRegistry) resolveLocked(name string) (mcpResolution, bool) {
	for _, r := range resolveServers(reg.views) {
		if r.report.Name == name {
			return r, true
		}
	}
	return mcpResolution{}, false
}

// actionSession is the session name's row represents, the one its Reconnect resets.
func (reg *mcpRegistry) actionSession(name string) (marotte.SessionID, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	r, ok := reg.resolveLocked(name)
	return r.session, ok
}

// connectedNames is every server some view reports connected, sorted.
func (reg *mcpRegistry) connectedNames() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	var out []string
	for _, r := range resolveServers(reg.views) {
		if r.connected {
			out = append(out, r.report.Name)
		}
	}
	slices.Sort(out)
	return out
}

func reportsState(servers []translate.MCPServerReport, name string, state translate.MCPReportState) bool {
	return slices.ContainsFunc(servers, func(s translate.MCPServerReport) bool { return s.Name == name && s.State == state })
}

func settleConnecting(next, prev []translate.MCPServerReport) []translate.MCPServerReport {
	out := make([]translate.MCPServerReport, 0, len(next))
	for i := range next {
		s := &next[i]
		if s.State != translate.MCPReportConnecting {
			out = append(out, *s)
			continue
		}
		if j := slices.IndexFunc(prev, func(p translate.MCPServerReport) bool { return p.Name == s.Name }); j >= 0 {
			out = append(out, prev[j])
		}
	}
	return out
}

// mcpDerived is what a re-derive changed, published outside the lock.
type mcpDerived struct {
	registry  *marotte.GovernanceMCPRegistry
	events    []marotte.ServerEvent
	changed   bool
	poolMoved bool
	anyView   bool
}

// deriveLocked rebuilds servers from views. A record keeps its pointer, and so its relay latch, while its state and
// authorization URL stand; any other change updates it in place. Only a changed record raises an event.
func (reg *mcpRegistry) deriveLocked(configured map[string]struct{}) mcpDerived {
	d := mcpDerived{anyView: len(reg.views) > 0, registry: unionRegistry(reg.views)}
	next := make(map[string]*mcpServerRuntime, len(reg.servers))
	signingIn := reg.consentURLsLocked()
	for _, r := range resolveServers(reg.views) {
		reg.deriveServerLocked(&d, next, &r, configured, signingIn[r.report.Name])
	}
	for name := range reg.servers {
		if _, ok := next[name]; !ok {
			ev := marotte.NewEvent(marotte.EventMCPDisconnected, "", marotte.MCPDisconnectedPayload{Server: name})
			d.change(&ev)
		}
	}
	reg.servers = next
	return d
}

// consentURLsLocked is each server's consent URL from the unexpired sign-in KAS opened one for.
func (reg *mcpRegistry) consentURLsLocked() map[string]string {
	now := reg.now()
	var out map[string]string
	for _, v := range reg.views {
		if s := v.signIn; s != nil && s.authURL != "" && now.Before(s.deadline) {
			if out == nil {
				out = map[string]string{}
			}
			out[s.server] = s.authURL
		}
	}
	return out
}

// deriveServerLocked files r's record into next, reusing the current record while its state and authorization
// URL stand; a server recordFor rejects is left out. A non-empty consentURL makes the record signing_in with it.
func (reg *mcpRegistry) deriveServerLocked(d *mcpDerived, next map[string]*mcpServerRuntime,
	r *mcpResolution, configured map[string]struct{}, consentURL string,
) {
	s := r.report
	rec, ok := reg.recordFor(s, configured)
	if !ok {
		return
	}
	if rec.State == mcpStateOAuth {
		rec.Error = r.signInError
	}
	if consentURL != "" {
		rec.State, rec.OAuthURL, rec.Error = mcpStateSigningIn, consentURL, ""
	}
	if rec.State == mcpStateConnected {
		delete(reg.launchResets, s.Name)
	}
	old := reg.servers[s.Name]
	if old != nil && old.State == rec.State && old.OAuthURL == rec.OAuthURL {
		next[s.Name] = old
		if !sameRecord(old, rec) {
			ev := inPlaceEvent(old, rec)
			rec.Relayed = old.Relayed
			*old = *rec
			d.change(ev)
		}
		return
	}
	next[s.Name] = rec
	d.change(stateEvent(rec))
}

// then folds a later derive's outcome into d, so one publish carries both.
func (d *mcpDerived) then(n mcpDerived) {
	d.events = append(d.events, n.events...)
	d.changed = d.changed || n.changed
	d.poolMoved = d.poolMoved || n.poolMoved
	d.anyView, d.registry = n.anyView, n.registry
}

func (d *mcpDerived) change(ev *marotte.ServerEvent) {
	d.changed = true
	if ev != nil {
		d.events = append(d.events, *ev)
	}
}

// stateEvent is the SSE event announcing rec; a disabled server has none, and claimSignInURL's mcp_sign_in_open
// announces a signing_in one.
func stateEvent(rec *mcpServerRuntime) *marotte.ServerEvent {
	var ev marotte.ServerEvent
	switch rec.State {
	case mcpStateConnected:
		ev = marotte.NewEvent(marotte.EventMCPConnected, "", marotte.MCPConnectedPayload{Server: rec.Name})
	case mcpStateOAuth:
		ev = marotte.NewEvent(marotte.EventMCPOAuthNeeded, "", marotte.MCPOAuthPayload{Server: rec.Name, Error: rec.Error})
	case mcpStateFailed:
		ev = marotte.NewEvent(marotte.EventMCPFailed, "", marotte.MCPFailedPayload{Server: rec.Name, Error: rec.Error})
	default:
		return nil
	}
	return &ev
}

// inPlaceEvent announces old updated to next; a needs_auth record only when its sign-in error moved, the one field
// its client handler takes.
func inPlaceEvent(old, next *mcpServerRuntime) *marotte.ServerEvent {
	if next.State == mcpStateOAuth && old.Error == next.Error {
		return nil
	}
	return stateEvent(next)
}

func sameRecord(a, b *mcpServerRuntime) bool {
	return a.Name == b.Name && a.State == b.State && a.Origin == b.Origin && a.OriginRoot == b.OriginRoot &&
		a.OriginPower == b.OriginPower && a.OAuthURL == b.OAuthURL && a.Error == b.Error && a.Shadows == b.Shadows &&
		slices.Equal(a.Tools, b.Tools) && slices.Equal(a.Prompts, b.Prompts) &&
		slices.Equal(a.Resources, b.Resources) && slices.Equal(a.ResourceTemplates, b.ResourceTemplates)
}

// recordFor is the record a report derives; ok is false for marotte's own server reported disabled, whose config
// row already shows it off, so the name leaves the records.
func (reg *mcpRegistry) recordFor(s *translate.MCPServerReport, configured map[string]struct{}) (*mcpServerRuntime, bool) {
	prov := reg.originFor(s.Name, s.Source, configured)
	var state mcpServerState
	switch s.State {
	case translate.MCPReportConnected:
		state = mcpStateConnected
	case translate.MCPReportNeedsAuth:
		state = mcpStateOAuth
	case translate.MCPReportFailed:
		state = mcpStateFailed
	default:
		if prov.origin == marotte.OriginUser {
			return nil, false
		}
		state = mcpStateDisabled
	}
	rec := prov.record(s.Name, state)
	// No AuthorizationURL: a failed entry's URL belongs to an attempt whose listener KAS already closed, so only a
	// Sign in can complete.
	rec.Error = s.Error
	rec.Tools, rec.Prompts, rec.Resources, rec.ResourceTemplates = s.Tools, s.Prompts, s.Resources, s.ResourceTemplates
	return rec, true
}

// publish broadcasts a derive's events and pushes the views' registry union; with no view left the last union
// stands, since closing chats does not change the organization's registry.
func (reg *mcpRegistry) publish(ctx context.Context, d mcpDerived) {
	for _, ev := range d.events {
		reg.bus.Broadcast(ctx, ev)
	}
	if d.poolMoved {
		reg.poolChanged()
	}
	if d.anyView {
		reg.setGovernance(ctx, d.registry)
	}
	if d.changed {
		reg.signalChange()
	}
}

// unionRegistry merges the views' registry blocks, nil when none is in registry mode.
func unionRegistry(views []mcpView) *marotte.GovernanceMCPRegistry {
	var out *marotte.GovernanceMCPRegistry
	for _, v := range views {
		r := v.registry
		if r == nil {
			continue
		}
		if out == nil {
			out = &marotte.GovernanceMCPRegistry{
				Servers: []marotte.GovernanceRegistryServer{}, Filtered: []string{}, Unresolved: []string{},
			}
		}
		for _, s := range r.Servers {
			if !slices.ContainsFunc(out.Servers, func(o marotte.GovernanceRegistryServer) bool { return o.Name == s.Name }) {
				out.Servers = append(out.Servers, s)
			}
		}
		out.Filtered = appendNew(out.Filtered, r.Filtered)
		out.Unresolved = appendNew(out.Unresolved, r.Unresolved)
	}
	return out
}

func appendNew(dst, src []string) []string {
	for _, s := range src {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}

// viewPool is what a view's connected servers offer the `#` menu, sorted by server.
func viewPool(servers []translate.MCPServerReport) []poolServerJSON {
	var pool []poolServerJSON
	for i := range servers {
		s := &servers[i]
		if s.State != translate.MCPReportConnected || len(s.Resources)+len(s.ResourceTemplates) == 0 {
			continue
		}
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
	return pool
}

func poolServerEqual(a, b poolServerJSON) bool {
	return a.Name == b.Name && slices.Equal(a.Resources, b.Resources) && slices.Equal(a.ResourceTemplates, b.ResourceTemplates)
}

// poolFor is chatID's own view's pool when its session is live and has reported, else the union of every view's
// (a chat's session opens only at its first prompt, rewind or fork), the oldest view's entry for each name.
func (reg *mcpRegistry) poolFor(chatID marotte.ChatID) []poolServerJSON {
	var sid marotte.SessionID
	live := false
	if chatID != "" {
		sid, live = reg.sessionOf(chatID)
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if live {
		if i := reg.viewIndexLocked(sid); i >= 0 {
			return viewPool(reg.views[i].servers)
		}
	}
	var out []poolServerJSON
	for _, v := range reg.views {
		for _, row := range viewPool(v.servers) {
			if !slices.ContainsFunc(out, func(o poolServerJSON) bool { return o.Name == row.Name }) {
				out = append(out, row)
			}
		}
	}
	slices.SortFunc(out, func(a, b poolServerJSON) int { return cmp.Compare(a.Name, b.Name) })
	return out
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

// handlePool serves GET /api/mcp/pool?chat_id=: poolFor's resources and templates, sorted by server; empty before
// any session reported them.
func (reg *mcpRegistry) handlePool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	out := reg.poolFor(marotte.ChatID(r.URL.Query().Get("chat_id")))
	if out == nil {
		out = []poolServerJSON{}
	}
	webhttp.WriteJSON(w, map[string][]poolServerJSON{"servers": out})
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
// attempt's identity: a state or URL change installs a fresh record, so a stale token's release is a no-op. authURL
// is copied in so the handler never reads a later attempt's anchor; rec and lazy are only dereferenced under the lock.
type oauthAttempt struct {
	rec     *mcpServerRuntime
	lazy    *lazySignIn
	server  string
	authURL string
}

// beginOAuthRelay reserves the named server's unexpired Sign in, or else the unexpired lazy anchor whose state is
// the pasted callback's. All under one lock: a concurrent paste gets errRelayAlreadyDone, a state no anchor holds
// errRelayStateDrift, nothing in flight errRelayNoFlow.
func (reg *mcpRegistry) beginOAuthRelay(ctx context.Context, name, pastedState string) (oauthAttempt, error) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	d := reg.expireSignInsLocked(configured)
	a, err := reg.reserveRelayLocked(name, pastedState)
	reg.mu.Unlock()
	reg.publish(ctx, d)
	return a, err
}

func (reg *mcpRegistry) reserveRelayLocked(name, pastedState string) (oauthAttempt, error) {
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

// clearAll wipes every view (and so every sign-in), record and lazy anchor and broadcasts mcp_disconnected per
// server when the engine ends: MCP subprocesses live inside kiro-cli.
func (reg *mcpRegistry) clearAll(ctx context.Context) {
	reg.mu.Lock()
	prev := reg.servers
	reg.servers = make(map[string]*mcpServerRuntime)
	reg.launchResets = make(map[string]int)
	hadPool := slices.ContainsFunc(reg.views, func(v mcpView) bool { return len(viewPool(v.servers)) > 0 })
	reg.views = nil
	reg.lazySignIns = nil
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

// configuredNames is read before the lock: the store behind it may do I/O. Nil without a config.
func (reg *mcpRegistry) configuredNames(ctx context.Context) map[string]struct{} {
	if reg.config == nil {
		return nil
	}
	return reg.config.ConfiguredNames(ctx)
}

// originFor reads the provenance KAS stamped on its entry for a name. "user" or no stamp attributes to
// marotte's row only when marotte's store holds the name, so a spoofed stamp stays foreign; without a
// config every such entry is marotte's.
func (reg *mcpRegistry) originFor(name string, src marotte.MCPSource, configured map[string]struct{}) mcpProvenance {
	var p mcpProvenance
	_, held := configured[name]
	switch marotte.Origin(src.Origin) {
	case "", marotte.OriginUser:
		p.origin = marotte.OriginUnknown
		if reg.config == nil || held {
			p.origin = marotte.OriginUser
		}
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
	p.shadows = held
	return p
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
