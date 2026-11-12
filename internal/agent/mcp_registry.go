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

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
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

// maxConsentAnchors bounds the anchors no Sign in took: KAS runs one re-auth per server.
const maxConsentAnchors = 8

// signInAnchor is a consent URL KAS opened, identified by its state. Only relayed changes after it is created, so
// authURL is read without the lock. attempt is the Sign in it was attributed to, 0 when none; flowID is KAS's id
// for a mid-session re-auth, "" otherwise.
type signInAnchor struct {
	expires time.Time
	state   string
	authURL string
	flowID  string
	attempt uint64
	// relayed is the anchor's single-use relay latch (mcp_oauth_relay.go), set on reservation and kept on
	// delivery, so a resubmit gets "already relayed". A failed relay gives it back.
	relayed bool
}

// consentShowing is how anchorConsentURL decided and published the showing of a consent URL KAS opened.
type consentShowing int

const (
	consentRefused consentShowing = iota // a new unattributed URL with no page connected: nothing is kept
	consentOnRow                         // attributed: the row is published signing_in with it
	consentSeen                          // its state is already anchored: nothing new is shown
	consentBanner                        // unattributed with a page connected: shown on every page
)

type consentShown struct {
	server string // consentOnRow only
	how    consentShowing
}

// viewSignIn is a Sign in the user started in the view's session.
type viewSignIn struct {
	deadline time.Time
	server   string
	// attempt is the claim that filed it: a sign-in that outlives it (a status ended it and a second Sign in filed
	// another) must not end the second one, nor take its consent URL.
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
	// OAuthURL is the standing Sign in's anchored consent URL, set only while signing_in.
	OAuthURL          string
	Error             string
	Tools             []string
	Prompts           []marotte.MCPPromptInfo
	Resources         []marotte.MCPResourceInfo
	ResourceTemplates []marotte.MCPResourceTemplateInfo
	// Relayed is the row's anchor's relay latch, filled by projectLocked; a stored record never holds it.
	Relayed bool
	Shadows bool
}

// mcpView is one session's last _kiro/mcp/status frame and the Sign ins standing in that session.
type mcpView struct {
	registry *marotte.GovernanceMCPRegistry
	// signIns is the Sign ins standing for the session's servers. At most one stands per server name across the
	// views: the consent URL KAS opens names its server but not its session.
	signIns []viewSignIn
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
	// versions mints the `mcp_status` version commitLocked stamps rows with.
	versions *subject.Versions
	// epoch is the hub epoch GET /api/mcp/status stamps.
	epoch func() string
	// lifetime supplies the done channel the debounce loop exits on.
	lifetime *lifetime
	// sessionOf names the session a chat's live bridge serves.
	sessionOf func(marotte.ChatID) (marotte.SessionID, bool)
	// setGovernance receives the union of the views' MCP registry blocks.
	setGovernance func(context.Context, *marotte.GovernanceMCPRegistry)
	// servers is derived from views (deriveLocked), never written on its own.
	servers map[string]*mcpServerRuntime
	// rows is the last committed projection, the one GET /api/mcp/status serves and mcp_status announced.
	rows map[string]*marotte.MCPServerStatus
	// onChange is installed later by SetMCPOnChange.
	onChange func() `wiring:"optional"`
	// notifyCh coalesces onChange signals within the debounce window (capacity 1).
	notifyCh chan struct{}
	// launchResets counts each server's automatic resets since it last connected (resetFailedForLaunch).
	launchResets map[string]int
	// now reads the clock for the sign-ins' and the anchors' deadlines.
	now func() time.Time
	// views is in recording order; resolveServers picks the view that represents each name.
	views []mcpView
	// anchors are engine-wide, oldest first, each added by anchorConsentURL: a consent URL names no session to
	// file it under.
	anchors []*signInAnchor
	// signIns counts the sign-ins claimed, minting viewSignIn.attempt.
	signIns uint64
	mu      sync.RWMutex
}

var _ consentAnchorer = (*mcpRegistry)(nil)

func newMCPRegistry(engine *engineOwner, b *bus, lt *lifetime, cfg mcpNameSets,
	stamps mcpStamps,
	sessionOf func(marotte.ChatID) (marotte.SessionID, bool),
	setGovernance func(context.Context, *marotte.GovernanceMCPRegistry),
) *mcpRegistry {
	return &mcpRegistry{
		engine:        engine,
		bus:           b,
		versions:      stamps.versions,
		epoch:         stamps.epoch,
		lifetime:      lt,
		config:        cfg,
		sessionOf:     sessionOf,
		setGovernance: setGovernance,
		servers:       make(map[string]*mcpServerRuntime),
		rows:          make(map[string]*marotte.MCPServerStatus),
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

// mcpStamps is what the registry stamps its status rows with: the shared subject registry and the hub epoch.
type mcpStamps struct {
	versions *subject.Versions
	epoch    func() string
}

// projectLocked is the status inventory the records give now: each record with its attributed anchor's relay latch.
// The read lock suffices.
func (reg *mcpRegistry) projectLocked() map[string]*marotte.MCPServerStatus {
	anchors := reg.consentAnchorsLocked()
	out := make(map[string]*marotte.MCPServerStatus, len(reg.servers))
	for name, v := range reg.servers {
		row := marotte.MCPServerStatus(*v)
		if a := anchors[row.Name]; a != nil && row.State == mcpStateSigningIn && row.OAuthURL == a.authURL {
			row.Relayed = a.relayed
		}
		out[name] = &row
	}
	return out
}

// commitLocked makes the projection the served rows. When any row changed it mints `mcp_status` once and broadcasts
// one stamped mcp_status frame per changed or removed server before it returns, so frames go out in version order
// and the GET never serves a version whose frames are not sent. Every write that can move the projection calls it
// before releasing the write lock; a call that finds no change sends nothing. A committed row is never written again,
// so a frame shares it.
func (reg *mcpRegistry) commitLocked() {
	next := reg.projectLocked()
	var changed []string
	for name, row := range next {
		if old, ok := reg.rows[name]; !ok || !sameStatus(old, row) {
			changed = append(changed, name)
		}
	}
	for name := range reg.rows {
		if _, ok := next[name]; !ok {
			changed = append(changed, name)
		}
	}
	reg.rows = next
	if len(changed) == 0 {
		return
	}
	slices.Sort(changed)
	stamp := marotte.NewSubjectStamp(string(subject.KindMCPStatus), "", reg.versions.BumpCounter(subject.KindMCPStatus, ""))
	for _, name := range changed {
		ev := marotte.NewEvent(marotte.EventMCPStatus, "", marotte.MCPStatusPayload{Server: name, Status: next[name]})
		ev.Subject = stamp
		reg.bus.Broadcast(context.Background(), ev)
	}
}

func sameStatus(a, b *marotte.MCPServerStatus) bool {
	ra, rb := mcpServerRuntime(*a), mcpServerRuntime(*b)
	return sameRecord(&ra, &rb)
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
	var signIns []viewSignIn
	var failed viewSignInFailure
	if i >= 0 {
		prev, failed = reg.views[i].servers, reg.views[i].failedSignIn
		signIns = slices.DeleteFunc(slices.Clone(reg.views[i].signIns), func(s viewSignIn) bool {
			return !reportsState(status.Servers, s.server, translate.MCPReportConnecting)
		})
	}
	if failed.server != "" && !reportsState(status.Servers, failed.server, translate.MCPReportNeedsAuth) {
		failed = viewSignInFailure{}
	}
	v := mcpView{
		session: sessionID, registry: status.Registry, signIns: signIns, failedSignIn: failed,
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
	changed   bool
	poolMoved bool
	anyView   bool
}

// deriveLocked rebuilds servers from views and commits the projection.
func (reg *mcpRegistry) deriveLocked(configured map[string]struct{}) mcpDerived {
	d := mcpDerived{anyView: len(reg.views) > 0, registry: unionRegistry(reg.views)}
	next := make(map[string]*mcpServerRuntime, len(reg.servers))
	anchors := reg.consentAnchorsLocked()
	for _, r := range resolveServers(reg.views) {
		var consentURL string
		if a := anchors[r.report.Name]; a != nil {
			consentURL = a.authURL
		}
		reg.deriveServerLocked(&d, next, &r, configured, consentURL)
	}
	for name := range reg.servers {
		if _, ok := next[name]; !ok {
			d.changed = true
		}
	}
	reg.servers = next
	reg.commitLocked()
	return d
}

// consentAnchorsLocked is, per server, the unexpired anchor attributed to its standing unexpired Sign in. It never
// writes reg.anchors: projectLocked runs under the read lock.
func (reg *mcpRegistry) consentAnchorsLocked() map[string]*signInAnchor {
	now := reg.now()
	var out map[string]*signInAnchor
	for _, v := range reg.views {
		for _, s := range v.signIns {
			if !now.Before(s.deadline) {
				continue
			}
			if a := reg.attemptAnchorLocked(s.attempt); a != nil {
				if out == nil {
					out = map[string]*signInAnchor{}
				}
				out[s.server] = a
			}
		}
	}
	return out
}

// attemptAnchorLocked is the anchor attributed to attempt, nil when it has none. An attributed anchor expires with
// its Sign in, so a caller that checked the Sign in needs no expiry check.
func (reg *mcpRegistry) attemptAnchorLocked(attempt uint64) *signInAnchor {
	for _, a := range reg.anchors {
		if a.attempt == attempt {
			return a
		}
	}
	return nil
}

// deriveServerLocked files r's record into next; a server recordFor rejects is left out. A non-empty consentURL
// makes the record signing_in with it.
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
	next[s.Name] = rec
	if old := reg.servers[s.Name]; old == nil || !sameRecord(old, rec) {
		d.changed = true
	}
}

// then folds a later derive's outcome into d, so one publish carries both.
func (d *mcpDerived) then(n mcpDerived) {
	d.changed = d.changed || n.changed
	d.poolMoved = d.poolMoved || n.poolMoved
	d.anyView, d.registry = n.anyView, n.registry
}

func sameRecord(a, b *mcpServerRuntime) bool {
	return a.Name == b.Name && a.State == b.State && a.Origin == b.Origin && a.OriginRoot == b.OriginRoot &&
		a.OriginPower == b.OriginPower && a.OAuthURL == b.OAuthURL && a.Error == b.Error && a.Shadows == b.Shadows &&
		a.Relayed == b.Relayed && slices.Equal(a.Tools, b.Tools) && slices.Equal(a.Prompts, b.Prompts) &&
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

// publish signals a derive's pool move and record change and pushes the views' registry union; with no view left the
// last union stands, since closing chats does not change the organization's registry. The rows went out at commit.
func (reg *mcpRegistry) publish(ctx context.Context, d mcpDerived) {
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
	httpserver.WriteJSON(w, map[string][]poolServerJSON{"servers": out})
}

// anchorConsentURL is the one writer of new anchors, and decides how a consent URL KAS opened is shown. A state an
// unexpired anchor holds is consentSeen whatever pageConnected says. A new anchor takes the standing Sign in for the
// server KAS named only while that Sign in has none, so a later URL never rewrites its row. A URL with no state is
// never an anchor: no paste could match it.
func (reg *mcpRegistry) anchorConsentURL(ctx context.Context, req consentRequest, pageConnected bool) consentShown {
	authURL := req.URL
	state := consentState(authURL)
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	now := reg.now()
	reg.pruneAnchorsLocked(now)
	if state != "" && slices.ContainsFunc(reg.anchors, func(a *signInAnchor) bool { return a.state == state }) {
		reg.mu.Unlock()
		return consentShown{how: consentSeen}
	}
	standing := reg.standingSignInLocked(req.Server, now)
	if state != "" && standing.attempt != 0 && reg.attemptAnchorLocked(standing.attempt) == nil {
		reg.addAnchorLocked(&signInAnchor{
			expires: standing.deadline, state: state, authURL: authURL, flowID: req.FlowID, attempt: standing.attempt,
		})
		d := reg.deriveLocked(configured)
		reg.mu.Unlock()
		reg.publish(ctx, d)
		return consentShown{how: consentOnRow, server: standing.server}
	}
	if !pageConnected {
		reg.mu.Unlock()
		return consentShown{how: consentRefused}
	}
	expires := now.Add(lazySignInTTL)
	if state != "" {
		reg.addAnchorLocked(&signInAnchor{expires: expires, state: state, authURL: authURL, flowID: req.FlowID})
	}
	reg.mu.Unlock()
	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventOpenExternalURL, "", marotte.OpenExternalURLPayload{
		URL: authURL, ExpiresAt: expires, Server: req.Server, FlowID: req.FlowID,
	}))
	return consentShown{how: consentBanner}
}

// standingSignInLocked is the unexpired Sign in standing for server, the zero value when none does.
func (reg *mcpRegistry) standingSignInLocked(server string, now time.Time) viewSignIn {
	if server == "" {
		return viewSignIn{}
	}
	for _, v := range reg.views {
		for _, s := range v.signIns {
			if s.server == server && now.Before(s.deadline) {
				return s
			}
		}
	}
	return viewSignIn{}
}

// endLazySignIn drops the anchors of the mid-session re-auth flowID names, which KAS ended with
// _kiro/mcp/oauthCompleted, and tells every page whether it succeeded. A flow no anchor carries publishes nothing.
func (reg *mcpRegistry) endLazySignIn(ctx context.Context, flowID, server string, success bool) {
	if flowID == "" {
		return
	}
	reg.mu.Lock()
	n := len(reg.anchors)
	reg.anchors = slices.DeleteFunc(reg.anchors, func(a *signInAnchor) bool { return a.flowID == flowID })
	ended := len(reg.anchors) < n
	if ended {
		// An attributed anchor carries the flow id it was opened with: dropping it ends the row's relay overlay.
		reg.commitLocked()
	}
	reg.mu.Unlock()
	if !ended {
		return
	}
	reg.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPSignInClosed, "", marotte.MCPSignInClosedPayload{
		Server: server, FlowID: flowID, Success: success,
	}))
}

// consentState is the state a consent URL carries, "" when it carries none or does not parse.
func consentState(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("state")
}

// addAnchorLocked appends a, first evicting the oldest unattributed anchors past maxConsentAnchors of them. An
// attributed anchor is never evicted: there is at most one per standing Sign in, and its row shows it.
func (reg *mcpRegistry) addAnchorLocked(a *signInAnchor) {
	unattributed := func(o *signInAnchor) bool { return o.attempt == 0 }
	if unattributed(a) {
		for countFunc(reg.anchors, unattributed) >= maxConsentAnchors {
			j := slices.IndexFunc(reg.anchors, unattributed)
			reg.anchors = slices.Delete(reg.anchors, j, j+1)
		}
	}
	reg.anchors = append(reg.anchors, a)
}

func countFunc[T any](s []T, f func(T) bool) int {
	n := 0
	for _, v := range s {
		if f(v) {
			n++
		}
	}
	return n
}

// pruneAnchorsLocked drops the expired anchors. Write lock only: a reader under the read lock filters by expiry.
func (reg *mcpRegistry) pruneAnchorsLocked(now time.Time) {
	reg.anchors = slices.DeleteFunc(reg.anchors, func(a *signInAnchor) bool { return !now.Before(a.expires) })
}

// oauthAttempt is a granted reservation to relay one anchor's callback, the only handle that can give it back.
type oauthAttempt struct {
	anchor *signInAnchor
}

// beginOAuthRelay reserves the unexpired anchor whose state is the pasted callback's, all under one lock: a
// concurrent paste gets errRelayAlreadyDone, a state no anchor holds errRelayStateDrift, no anchor at all
// errRelayNoFlow.
func (reg *mcpRegistry) beginOAuthRelay(ctx context.Context, pastedState string) (oauthAttempt, error) {
	configured := reg.configuredNames(ctx)
	reg.mu.Lock()
	d := reg.expireSignInsLocked(configured)
	a, err := reg.reserveRelayLocked(pastedState)
	reg.mu.Unlock()
	reg.publish(ctx, d)
	return a, err
}

func (reg *mcpRegistry) reserveRelayLocked(pastedState string) (oauthAttempt, error) {
	reg.pruneAnchorsLocked(reg.now())
	if len(reg.anchors) == 0 {
		return oauthAttempt{}, errRelayNoFlow
	}
	for _, a := range reg.anchors {
		// Constant-time: the state is secret-equivalent, and an early exit would leak it to a retrying caller.
		if subtle.ConstantTimeCompare([]byte(a.state), []byte(pastedState)) != 1 {
			continue
		}
		if a.relayed {
			return oauthAttempt{}, errRelayAlreadyDone
		}
		a.relayed = true
		reg.commitLocked()
		return oauthAttempt{anchor: a}, nil
	}
	return oauthAttempt{}, errRelayStateDrift
}

// releaseOAuthRelay gives back an undelivered reservation (refused paste, transport failure,
// listener refusal); a delivery keeps it as the latch.
func (reg *mcpRegistry) releaseOAuthRelay(a oauthAttempt) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	a.anchor.relayed = false
	reg.commitLocked()
}

// clearAll wipes every view (and so every sign-in), record and anchor when the engine ends, MCP subprocesses living
// inside kiro-cli; its commit announces each wiped server gone.
func (reg *mcpRegistry) clearAll() {
	reg.mu.Lock()
	had := len(reg.servers) > 0
	reg.servers = make(map[string]*mcpServerRuntime)
	reg.launchResets = make(map[string]int)
	hadPool := slices.ContainsFunc(reg.views, func(v mcpView) bool { return len(viewPool(v.servers)) > 0 })
	reg.views = nil
	reg.anchors = nil
	reg.commitLocked()
	reg.mu.Unlock()
	if hadPool {
		reg.poolChanged()
	}
	if had {
		reg.signalChange()
	}
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

// handleStatus serves GET /api/mcp/status: the committed rows and the `mcp_status` version they were committed at.
func (reg *mcpRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rows, version := reg.statusRowsStamped()
	stamp := marotte.NewSubjectStamp(string(subject.KindMCPStatus), "", version)
	stamp.Epoch = reg.epoch()
	httpserver.WriteJSON(w, marotte.MCPStatusResponse{Servers: rows, Subject: stamp})
}

// statusRowsStamped is the committed rows, sorted by name, and their version, read under one lock: a commit mints
// under the write lock, so the pair is never torn.
func (reg *mcpRegistry) statusRowsStamped() (rows []marotte.MCPServerStatus, version string) {
	reg.mu.RLock()
	rows = make([]marotte.MCPServerStatus, 0, len(reg.rows))
	for _, row := range reg.rows {
		rows = append(rows, *row)
	}
	version, _ = reg.versions.Current(subject.KindMCPStatus, "")
	reg.mu.RUnlock()
	slices.SortFunc(rows, func(a, b marotte.MCPServerStatus) int { return cmp.Compare(a.Name, b.Name) })
	return rows, version
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
