package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os/signal"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notify"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/toolbelt/v3/pin"
)

// Server holds shared state and registers all HTTP handlers.
type Server struct {
	forges       routeHandler
	mcpConfig    routeHandler
	chats        routeHandler
	git          routeHandler
	files        routeHandler
	auth         routeHandler
	push         routeHandler
	mcpStatus    routeHandler
	accountUsage AccountUsageProvider
	policy       policyProvider
	policyReload policyReloader
	// governance answers the administrator's lock map; nil means nothing is locked.
	governance governanceLocks
	// kiroDefaults fills GET /api/settings' kiro_defaults; nil leaves it empty.
	kiroDefaults kiroDefaultsReader
	agent        chatEngine
	steering     SteeringGenerator
	mcpRegistry  routeHandler
	staticFS     fs.FS
	// tabs is the open-tab set; nil (no config dir) answers an empty collection at version 0.
	tabs tabReader
	// preview serves /preview/ and its grant and stamp endpoints; nil leaves them unmounted.
	preview routeHandler
	// metrics serves GET /metrics; nil leaves the route unmounted.
	metrics http.Handler
	// specApprovals is the spec-phase approval record; nil (no config dir) means
	// the spec GET carries no approvals.
	specApprovals specApprovalReader
	cliRunner     cliRunner
	// kiroDocs memoizes the .kiro inventory; a pointer so the zero Server needs no init.
	kiroDocs *docsCache
	tools    toolsSource
	// kiroReady is the install manager's readiness verdict, re-read per /healthz.
	kiroReady func() (bool, pin.Reason)
	// kiroRescan re-derives the active version from disk; nil leaves the repair route unmounted.
	kiroRescan func(context.Context) (bool, error)
	// authUnavailable reads a latch, never a probe (see WithAuthUnavailable).
	authUnavailable func() bool
	// hostPolicy is the ALLOWED_HOSTS allowlist; nil or inactive accepts any Host.
	hostPolicy *httpserver.HostPolicy
	// notifyDeliverer is the chats the notify route delivers into; nil leaves the route unmounted.
	notifyDeliverer notify.Deliverer
	// onListen fires once per successful bind, before serving: the evidence that this process
	// serves its config dir.
	onListen   func()
	listenAddr string
	configDir  string
	workDir    string
	// kasNode resolves the node binary KAS runs on; nil or "" means it cannot be located.
	kasNode func() string
	// sensitive is the file browser's deny list; the zero value is the one rooted at /config.
	sensitive filebrowse.Sensitive
	// heldKiroPrefs keeps the user's value of each kiro-cli setting a lock pins.
	heldKiroPrefs heldKiroPrefs
	// entries is the endpoint table every route is bound against.
	entries []route.Entry
	// openAPI is the document GET /api/openapi.json serves.
	openAPI []byte
	// trustedProxies feeds clientIP; nil logs the unspoofable socket peer.
	trustedProxies []netip.Prefix
	acctUsage      acctUsageCache
	cliTimeouts    cliTimeouts
	// ready is true between listener bind and the shutdown signal.
	ready atomic.Bool
	// ignoredPublicWarned latches clientIP's one warning per process.
	ignoredPublicWarned atomic.Bool
	// notifyGate is the notify route's credential; the zero Gate is the door off.
	notifyGate notify.Gate
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithEndpoints sets the endpoint table every route is bound against and the OpenAPI document
// generated from it. Without it, Routes refuses every binding.
func WithEndpoints(entries []route.Entry, openAPI []byte) Option {
	return func(s *Server) { s.entries, s.openAPI = entries, openAPI }
}

// WithSteering sets the steering generator used to produce environment.md for kiro-cli.
func WithSteering(g SteeringGenerator) Option { return func(s *Server) { s.steering = g } }

// WithAgent sets the agent runtime that owns the agent engine, its sessions and SSE broadcasts.
func WithAgent(a chatEngine) Option { return func(s *Server) { s.agent = a } }

// WithChats sets the chat store, whose own router owns the chat HTTP surface.
func WithChats(c routeHandler) Option { return func(s *Server) { s.chats = c } }

// WithGit sets the git handler for the /api/git endpoints.
func WithGit(g routeHandler) Option { return func(s *Server) { s.git = g } }

// WithFiles sets the file handler for workspace file read/write endpoints.
func WithFiles(f routeHandler) Option { return func(s *Server) { s.files = f } }

// WithAuth sets the auth handler for login, logout, and whoami endpoints.
func WithAuth(a routeHandler) Option { return func(s *Server) { s.auth = a } }

// WithPush sets the push service used for Web Push notification delivery.
func WithPush(p routeHandler) Option { return func(s *Server) { s.push = p } }

// WithMCPConfig sets the route handler for MCP server configuration endpoints.
func WithMCPConfig(r routeHandler) Option { return func(s *Server) { s.mcpConfig = r } }

// WithMCPStatus sets the route handler for the MCP runtime status endpoint.
func WithMCPStatus(r routeHandler) Option { return func(s *Server) { s.mcpStatus = r } }

// WithMCPRegistry sets the route handler for the MCP registry proxy endpoint.
func WithMCPRegistry(r routeHandler) Option { return func(s *Server) { s.mcpRegistry = r } }

// WithPreview sets the sandboxed web-preview routes.
func WithPreview(r routeHandler) Option { return func(s *Server) { s.preview = r } }

// WithMetrics sets the Prometheus exposition GET /metrics serves. It sits behind the same
// ALLOWED_HOSTS gate as every route and has no login of its own.
func WithMetrics(h http.Handler) Option { return func(s *Server) { s.metrics = h } }

// WithForges sets the route handler for forge (GitHub/GitLab/Gitea) HTTP endpoints.
func WithForges(r routeHandler) Option { return func(s *Server) { s.forges = r } }

// WithTools sets the source of the tools engine backing the /api/tools surface.
func WithTools(src toolsSource) Option { return func(s *Server) { s.tools = src } }

// WithAccountUsage sets the provider backing GET /api/account/usage.
func WithAccountUsage(p AccountUsageProvider) Option {
	return func(s *Server) { s.accountUsage = p }
}

// WithPolicy sets the Cedar policy provider backing GET /api/permissions and
// POST /api/permissions/explain. The rule writer needs no provider.
func WithPolicy(p policyProvider) Option {
	return func(s *Server) { s.policy = p }
}

// WithGovernanceLocks sets the lock map a write to a locked kiro-cli setting is
// checked against.
func WithGovernanceLocks(g governanceLocks) Option {
	return func(s *Server) { s.governance = g }
}

// WithKiroDefaults sets what GET /api/settings reports an unset three-state setting resolves to.
func WithKiroDefaults(k kiroDefaultsReader) Option {
	return func(s *Server) { s.kiroDefaults = k }
}

// WithPolicyReload wires what a security-profile change needs after it persisted. Optional:
// unwired, live sessions keep the old presets and no client is told.
func WithPolicyReload(p policyReloader) Option {
	return func(s *Server) { s.policyReload = p }
}

// WithStaticFS sets the embedded filesystem serving the compiled web UI.
func WithStaticFS(staticFS fs.FS) Option {
	return func(s *Server) { s.staticFS = staticFS }
}

// WithKiroCLI sets the resolvers for the kiro-cli binary and its environment: resolvers
// because the install manager selects (and can switch) the active version after the bind.
func WithKiroCLI(resolvePath func() string, resolveEnv func() []string) Option {
	return func(s *Server) {
		s.cliRunner = &execCLIRunner{cliPath: resolvePath, env: resolveEnv}
	}
}

// WithKiroReady sets the kiro-cli readiness verdict /healthz reports. Unset
// leaves the probe reflecting only that the listener is up.
func WithKiroReady(ready func() (bool, pin.Reason)) Option {
	return func(s *Server) { s.kiroReady = ready }
}

// WithAuthUnavailable sets the sign-in leg /healthz reports. It must be a LATCH read,
// not a probe: the handler runs per request. Unset leaves readiness with no auth leg.
func WithAuthUnavailable(unavailable func() bool) Option {
	return func(s *Server) { s.authUnavailable = unavailable }
}

// WithKiroRescan sets the disk-rescan hook the loopback repair route exposes.
// Unset leaves the route unmounted.
func WithKiroRescan(rescan func(context.Context) (bool, error)) Option {
	return func(s *Server) { s.kiroRescan = rescan }
}

// WithListenAddr sets the net.Listen address ListenAndServe binds. ListenAndServe refuses an empty one.
func WithListenAddr(addr string) Option { return func(s *Server) { s.listenAddr = addr } }

// WithConfigDir sets the configuration directory path used for chat files and settings.
func WithConfigDir(d string) Option { return func(s *Server) { s.configDir = d } }

// WithSensitive sets the deny list the `.kiro` docs scan refuses, the same value
// the file browser holds.
func WithSensitive(sensitive filebrowse.Sensitive) Option {
	return func(s *Server) { s.sensitive = sensitive }
}

// WithTabs wires the open-tab set GET /api/tabs reads. A nil store stays a nil INTERFACE
// (not a typed nil), so the handler's unwired branch is taken.
func WithTabs(st *tabs.Store) Option {
	return func(s *Server) {
		if st == nil {
			return
		}
		s.tabs = st
	}
}

// WithSpecApprovals wires the spec-phase approval record the spec GET reads. A nil store
// stays a nil INTERFACE (not a typed nil), so the handler's unwired branch is taken.
func WithSpecApprovals(st *specapproval.Store) Option {
	return func(s *Server) {
		if st == nil {
			return
		}
		s.specApprovals = st
	}
}

// WithKASNode sets how the node binary KAS runs on is found, which compiles hook matchers exactly
// as KAS will. It is called per request, because kiro-cli names its runtime entry only at its first
// start. Unset, a hook with a matcher cannot be created.
func WithKASNode(node func() string) Option { return func(s *Server) { s.kasNode = node } }

// WithWorkDir sets the workspace directory served by the file handler and git endpoints.
func WithWorkDir(d string) Option { return func(s *Server) { s.workDir = d } }

// WithTrustedProxies sets the reverse-proxy networks trusted when resolving the
// access-log client_ip. Empty trusts nothing, so the socket peer is logged.
func WithTrustedProxies(trusted []netip.Prefix) Option {
	return func(s *Server) { s.trustedProxies = trusted }
}

// WithHostPolicy sets the exact-match Host allowlist the security middleware
// applies before the CSRF check — the anti-DNS-rebinding gate. A nil or inactive
// policy accepts any Host.
func WithHostPolicy(p *httpserver.HostPolicy) Option {
	return func(s *Server) { s.hostPolicy = p }
}

// WithNotifyGate sets the notify route's credential, which the server refuses on every other route.
func WithNotifyGate(g notify.Gate) Option {
	return func(s *Server) { s.notifyGate = g }
}

// WithNotifyDeliverer mounts the notify route over d, the chats it delivers into.
func WithNotifyDeliverer(d notify.Deliverer) Option {
	return func(s *Server) { s.notifyDeliverer = d }
}

// WithOnListen registers a callback fired once the listener has SUCCESSFULLY bound (a bind
// failure never fires it). It runs on the bind goroutine ahead of serving, so it must not block.
func WithOnListen(fn func()) Option {
	return func(s *Server) { s.onListen = fn }
}

// New constructs a Server with the given options applied.
func New(opts ...Option) *Server {
	s := &Server{
		cliTimeouts: defaultCLITimeouts(),
		kiroDocs:    &docsCache{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register binds every route the server serves onto m, the composed handler packages' included.
func (s *Server) Register(m routeMux) {
	m.Bind(route.AnyMethod("/", spaHandler(s.staticFS)))
	registerAPIFallback(m)
	s.agent.RegisterRoutes(m)
	if s.notifyDeliverer != nil {
		m.Bind(notify.Binding(notify.NewHandler(s.notifyGate, s.notifyDeliverer)))
	}
	m.Bind(route.Methods("/api/version", http.HandlerFunc(s.handleVersion), route.Typed[struct{}, VersionInfo](http.MethodGet)))
	m.Bind(route.Methods("/api/diagnostics", http.HandlerFunc(s.handleDiagnostics), route.Typed[struct{}, DiagnosticsReport](http.MethodPost)))
	m.Bind(route.Methods("/api/kiro-settings", http.HandlerFunc(s.handleKiroSettings),
		route.Typed[struct{}, KiroSettings](http.MethodGet).WithQuery(reflect.TypeFor[KiroSettingsQuery]()),
		route.Typed[KiroSettingWrite, httpreply.OKBody](http.MethodPut)))
	s.chats.RegisterRoutes(m)
	m.Bind(route.Raw(healthzPath, http.HandlerFunc(s.handleHealth)))
	// Without it the "/" single-page app answers a stray trailing slash 200, which a prober reads as healthy.
	m.Bind(route.AnyMethod(healthzPath+"/", httpreply.UnknownEndpoint))
	// Bound explicitly, or the "/" single-page app answers a scrape with its HTML.
	if s.metrics != nil {
		m.Bind(route.Raw(metricsPath, s.metrics))
	}
	// Method-qualified, so a LAN caller's wrong method meets the /api/ fallback, never an Allow list.
	rescan := http.Handler(httpreply.UnknownEndpoint)
	if s.kiroRescan != nil {
		rescan = http.HandlerFunc(s.handleKiroRescan)
	}
	m.Bind(route.JSON[struct{}, RescanResult](http.MethodPost+" "+kiroRescanPath, loopbackOnly(kiroRescanSurface, rescan)))
	m.Bind(route.AnyMethod(pprofPath, pprofHandler()))
	s.auth.RegisterRoutes(m)
	m.Bind(route.Methods("/api/steering", http.HandlerFunc(s.handleSteering),
		route.Typed[struct{}, SteeringDoc](http.MethodGet),
		route.Typed[SteeringWrite, SteeringSave](http.MethodPut).WithHeaders(ifMatchHeader)))
	if s.tools != nil {
		(&toolsAPI{src: s.tools}).register(m)
	}
	// Method-qualified: a plain path would conflict in ServeMux with toolbelt's PATCH and DELETE /api/tools/{name}.
	m.Bind(route.JSON[struct{}, ToolPresence](http.MethodGet+" "+toolsAPIPrefix+"/status", http.HandlerFunc(handleToolStatus)))
	s.git.RegisterRoutes(m)
	s.files.RegisterRoutes(m)
	m.Bind(route.Methods("/api/settings", http.HandlerFunc(s.handleSettings),
		route.Typed[struct{}, marotte.EffectiveSettings](http.MethodGet),
		route.Typed[SettingsPatch, marotte.EffectiveSettings](http.MethodPatch)))
	m.Bind(route.Methods("/api/tabs", http.HandlerFunc(s.handleTabs), route.Typed[struct{}, marotte.TabList](http.MethodGet)))
	m.Bind(route.Methods("/api/workspace/kiro-docs", http.HandlerFunc(s.handleKiroDocs),
		route.Typed[struct{}, KiroDocsResponse](http.MethodGet)))
	m.Bind(route.Methods("/api/workspace/kiro-docs/new", http.HandlerFunc(s.handleKiroDocNew),
		route.Typed[KiroDocNewRequest, KiroDocNewResult](http.MethodPost).WithStatus(http.StatusCreated)))
	m.Bind(route.Methods("/api/specs/{dir}", http.HandlerFunc(s.handleSpec),
		route.Typed[struct{}, marotte.Spec](http.MethodGet).WithParams(reflect.TypeFor[marotte.DirPath]()).WithHeaders(ifNoneMatchHeader)))
	s.mcpConfig.RegisterRoutes(m)
	s.mcpStatus.RegisterRoutes(m)
	s.mcpRegistry.RegisterRoutes(m)
	if s.forges != nil {
		s.forges.RegisterRoutes(m)
	}
	m.Bind(route.Methods("/api/permissions", http.HandlerFunc(s.handlePolicyView),
		route.Typed[struct{}, marotte.PolicyView](http.MethodGet).WithQuery(reflect.TypeFor[PolicyViewQuery]())))
	m.Bind(route.Methods("/api/permissions/explain", http.HandlerFunc(s.handlePolicyExplain),
		route.Typed[marotte.PolicyExplainRequest, marotte.PolicyExplainResult](http.MethodPost)))
	m.Bind(route.Methods("/api/permissions/rules", http.HandlerFunc(s.handlePolicyRules),
		route.Typed[PolicyRuleWrite, httpreply.OKBody](http.MethodPost)))
	m.Bind(route.Methods("/api/permissions/profile", http.HandlerFunc(s.handlePolicyProfile),
		route.Typed[PolicyProfileWrite, httpreply.OKBody](http.MethodPost)))
	m.Bind(route.Methods("/api/account/usage", http.HandlerFunc(s.handleAccountUsage),
		route.Typed[struct{}, marotte.AccountUsage](http.MethodGet)))
	m.Bind(route.Raw(openAPIPath, openAPIHandler(s.openAPI)))
	s.push.RegisterRoutes(m)
	if s.preview != nil {
		s.preview.RegisterRoutes(m)
	}
	s.registerTestHooks(m)
}

// Routes is the sealed route mux: every route bound against the endpoint table WithEndpoints set.
// Its error is a *SealError listing every disagreement between the two.
func (s *Server) Routes() (*http.ServeMux, error) {
	mux, g := s.routes()
	if err := g.seal(); err != nil {
		return nil, err
	}
	return mux, nil
}

// routes binds every route and leaves the registrar unsealed, for a test that serves a partial server.
func (s *Server) routes() (*http.ServeMux, *Registrar) {
	mux := http.NewServeMux()
	g := NewRegistrar(mux, s.entries)
	s.Register(g)
	return mux, g
}

// ListenAndServe binds and seals every route and starts the HTTP server, blocking until
// SIGTERM/SIGINT. A refused seal returns its *SealError before any listener opens.
func (s *Server) ListenAndServe() error {
	if s.listenAddr == "" {
		return errors.New("no listen address")
	}
	mux, err := s.Routes()
	if err != nil {
		return err
	}
	cspPolicy, err := buildCSPPolicy(s.staticFS)
	if err != nil {
		return fmt.Errorf("build CSP: %w", err)
	}

	// Inside securityMiddleware (only CSRF-checked requests are deduped) and inside the access
	// logger (a replay is still logged).
	idem := newIdempotencyCache(idempotencyTTL)
	defer idem.stop()

	// MaxHeaderValueCount is left at its default: a 431 is answered BELOW the middleware chain,
	// with no access-log line, request id or client_ip.
	handler := httpserver.Chain(mux, s.middlewareStack(cspPolicy, idem)...)
	srv := httpserver.New(handler)
	srv.Addr = s.listenAddr

	// Bound up front so port-in-use surfaces synchronously.
	lc := listenConfig()
	ln, err := lc.Listen(context.Background(), "tcp", srv.Addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	s.ready.Store(true)
	// AFTER the bind, so nothing can read a failed bind as ownership.
	if s.onListen != nil {
		s.onListen()
	}
	// The bound ADDRESS, not the configured one: a misdirected boot's own output then says
	// which listener it got.
	slog.Info("Kiro Web UI listening", "addr", ln.Addr().String())
	// DNS rebinding rides the victim's BROWSER, so it reaches even a loopback bind.
	if !s.hostPolicy.Active() {
		slog.Warn("ALLOWED_HOSTS is unset or blank; any Host header is accepted, leaving DNS rebinding open even on loopback/private binds",
			"hint", "set ALLOWED_HOSTS to the exact hostnames/IPs you browse to (e.g. localhost,192.0.2.5,marotte.example.com)")
	}

	// The pre-drain hook keeps the agent-before-server order: readiness flips, the runtime stops
	// bridges and streams, and only then does the HTTP drain run.
	runErr := httpserver.Run(ctx, srv, ln, httpserver.WithPreDrain(func(drainCtx context.Context) {
		slog.Info("received signal, shutting down", "cause", context.Cause(ctx))
		s.ready.Store(false)
		// Run calls this hook SYNCHRONOUSLY before srv.Shutdown: drainCtx is passed on, never discarded.
		if err := s.agent.Shutdown(drainCtx); err != nil {
			slog.Error("agent runtime shutdown did not finish within the grace period", "error", err)
		}
	}))
	s.ready.Store(false) // no-op on the signal path; covers a serve failure
	return runErr
}

// clientIP is the access log's client_ip. The first request whose ignored forwarding header names a
// public address warns once, because a reverse proxy missing from TRUSTED_PROXIES otherwise makes
// every line log the proxy's address silently.
func (s *Server) clientIP(r *http.Request) string {
	res := httpserver.ResolveClient(r, s.trustedProxies...)
	if res.IgnoredPublic && s.ignoredPublicWarned.CompareAndSwap(false, true) {
		slog.Warn("a peer outside TRUSTED_PROXIES forwarded a public client address; client_ip logs the peer instead",
			"peer", res.Peer.String(),
			"hint", "if a reverse proxy sits in front of marotte, add its address to TRUSTED_PROXIES; a client sending its own X-Forwarded-For also triggers this")
	}
	return res.IP.String()
}

const metricsPath = "/metrics"

// registerAPIFallback puts a 404 under the /api/ subtree, so an unmatched API path is not
// answered 200 with index.html by "/". Both spellings, or GET /api draws a 301.
func registerAPIFallback(m routeMux) {
	m.Bind(route.AnyMethod(strings.TrimSuffix(apiPathPrefix, "/"), httpreply.UnknownEndpoint))
	m.Bind(route.AnyMethod(apiPathPrefix, httpreply.UnknownEndpoint))
}

// middlewareStack returns the middleware wrapping the route mux, OUTERMOST FIRST: access log,
// recovery, security (CSP + ALLOWED_HOSTS + CSRF), notify-token confinement, Fetch Metadata, canonical path, compression,
// dedup. A method so the ORDER, a security property, is assertable without a port.
func (s *Server) middlewareStack(cspPolicy string, idem *idempotencyCache) []httpserver.Middleware {
	return []httpserver.Middleware{
		httpserver.Logging(
			// Skipped by path, or the long-lived stream logs one open-forever line.
			httpserver.WithSkipPaths("/api/events"),
			// The shell PTY is silenced by RESPONSE, not path, so every handshake refusal keeps its line.
			httpserver.WithSkipUpgrades(true),
			// A prober and a scraper call these every 15 to 60s, so their access lines log at Debug
			// whatever the status; a refused one surfaces through its gate's own Warn.
			httpserver.WithProbeLogLevel(healthzPath, metricsPath),
			httpserver.WithClientIPFunc(s.clientIP),
			// A preview path carries a 12h bearer token, so it logs as the route template.
			httpserver.WithTemplatePathsUnder(preview.PathPrefix),
		),
		httpserver.Recoverer(),
		func(next http.Handler) http.Handler { return securityMiddleware(cspPolicy, s.hostPolicy, next) },
		confineNotifyToken(s.notifyGate),
		fetchMetadataGate,
		// INSIDE the host and CSRF gates (their 403 is not shadowed), OUTSIDE the mux (ServeMux
		// canonicalizes before routing) and the dedup cache (a refusal mints no replay entry).
		canonicalAPIPath,
		// OUTSIDE the dedup cache: the cache stores identity bytes, and a replay is coded for the
		// retry's own Accept-Encoding.
		compressJSON(),
		idem.middleware,
	}
}

// decodeBody applies LimitBody, decodes JSON into v, and returns true on success. On
// failure it writes a 400 and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpreply.DecodeBody(w, r, v, "bad request")
}

// healthzPath is the health route: no login, GET and HEAD.
const healthzPath = "/healthz"

// handleHealth answers 200 {"status":"ok"}, or 503 {"status":"unhealthy","reasons":[...]}
// listing every standing fault: not serving (startup or drain), the kiro-cli install verdict,
// then sign-in. runtime-health.ts reads reasons[0], so that order is a client contract.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// One read of each leg per request, so a verdict moving mid-request cannot drop every reason.
	kiroFault := ""
	if s.kiroReady != nil {
		if ok, why := s.kiroReady(); !ok {
			kiroFault = cmp.Or(kiroReasonText(why), reasonUnavailable)
		}
	}
	authOK := s.authUnavailable == nil || !s.authUnavailable()
	serving := s.ready.Load()
	checks := []health.Check{{Signal: health.SignalFunc(func() bool { return serving }), Reason: "starting up or shutting down"}}
	for _, reason := range []string{reasonInstalling, reasonRetrying, reasonUnavailable, reasonSettings} {
		checks = append(checks, health.Check{Signal: health.SignalFunc(func() bool { return kiroFault != reason }), Reason: reason})
	}
	checks = append(checks, health.Check{Signal: health.SignalFunc(func() bool { return authOK }), Reason: reasonSignIn})
	health.CheckHandler(checks...).ServeHTTP(w, r)
}

// confineNotifyToken refuses the notify token on every path but the notify route, so a leaked
// token opens no other door.
func confineNotifyToken(gate notify.Gate) httpserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !notify.IsRoute(r.URL.Path) && gate.Carries(r) {
				httpreply.Forbidden(w, "this token admits only POST /api/sessions/{session}/notify")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
