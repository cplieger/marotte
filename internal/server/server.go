package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/webhttp/v3"
)

const port = "9847"

// listenPort is the port the listener binds; a -tags marotte_test binary may move it
// (testhooks_marottetest.go).
var listenPort = port

// Server holds shared state and registers all HTTP handlers.
type Server struct {
	forges        routeHandler
	mcpConfig     routeHandler
	chats         routeHandler
	git           routeHandler
	gitAI         routeHandler
	files         routeHandler
	auth          routeHandler
	push          pushService
	mcpStatus     routeHandler
	utilityPrompt utilityPrompter
	accountUsage  AccountUsageProvider
	policy        policyProvider
	policyReload  policyReloader
	// governance answers the administrator's lock map; nil means nothing is locked.
	governance  governanceLocks
	mcpRender   mcpRenderer
	agent       chatEngine
	steering    SteeringGenerator
	mcpRegistry routeHandler
	staticFS    fs.FS
	// tabs is the open-tab set; nil (no config dir) answers an empty collection at version 0.
	tabs tabReader
	// preview serves /preview/ and its grant and stamp endpoints; nil leaves them unmounted.
	preview routeHandler
	// specApprovals is the spec-phase approval record; nil (no config dir) means
	// the spec GET carries no approvals.
	specApprovals specApprovalReader
	cliRunner     CLIRunner
	// kiroDocs memoizes the .kiro inventory; a pointer so the zero Server needs no init.
	kiroDocs *docsCache
	tools    toolsSource
	// kiroReady is the install manager's readiness verdict, re-read per /api/health.
	kiroReady func() (bool, pinstall.Reason)
	// kiroRescan re-derives the active version from disk; nil leaves the repair route unmounted.
	kiroRescan func(context.Context) (bool, error)
	// authUnavailable reads a latch, never a probe (see WithAuthUnavailable).
	authUnavailable func() bool
	// hostPolicy is the ALLOWED_HOSTS allowlist; nil or inactive accepts any Host.
	hostPolicy *webhttp.HostPolicy
	// onListen fires once per successful bind, before serving: the evidence that this process
	// serves its config dir.
	onListen  func()
	configDir string
	workDir   string
	// sensitive is the file browser's deny list; the zero value is the one rooted at /config.
	sensitive filebrowse.Sensitive
	// heldKiroPrefs keeps the user's value of each kiro-cli setting a lock pins.
	heldKiroPrefs heldKiroPrefs
	// trustedProxies feeds webhttp.WithClientIP; nil logs the unspoofable socket peer.
	trustedProxies []*net.IPNet
	acctUsage      acctUsageCache
	cliTimeouts    cliTimeouts
	// ready is true between listener bind and the shutdown signal.
	ready atomic.Bool
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithSteering sets the steering generator used to produce environment.md for kiro-cli.
func WithSteering(g SteeringGenerator) Option { return func(s *Server) { s.steering = g } }

// WithAgent sets the agent runtime that manages bridge processes and SSE broadcasts.
func WithAgent(a chatEngine) Option { return func(s *Server) { s.agent = a } }

// WithChats sets the chat store, whose own router owns the chat HTTP surface.
func WithChats(c routeHandler) Option { return func(s *Server) { s.chats = c } }

// WithGit sets the git handler for non-AI git HTTP endpoints.
func WithGit(g routeHandler) Option { return func(s *Server) { s.git = g } }

// WithGitAI sets the route handler for AI-assisted git operations.
func WithGitAI(r routeHandler) Option { return func(s *Server) { s.gitAI = r } }

// WithFiles sets the file handler for workspace file read/write endpoints.
func WithFiles(f routeHandler) Option { return func(s *Server) { s.files = f } }

// WithAuth sets the auth handler for login, logout, and whoami endpoints.
func WithAuth(a routeHandler) Option { return func(s *Server) { s.auth = a } }

// WithPush sets the push service used for Web Push notification delivery.
func WithPush(p pushService) Option { return func(s *Server) { s.push = p } }

// WithMCPConfig sets the route handler for MCP server configuration endpoints.
func WithMCPConfig(r routeHandler) Option { return func(s *Server) { s.mcpConfig = r } }

// WithMCPStatus sets the route handler for the MCP runtime status endpoint.
func WithMCPStatus(r routeHandler) Option { return func(s *Server) { s.mcpStatus = r } }

// WithMCPRegistry sets the route handler for the MCP registry proxy endpoint.
func WithMCPRegistry(r routeHandler) Option { return func(s *Server) { s.mcpRegistry = r } }

// WithPreview sets the sandboxed web-preview routes.
func WithPreview(r routeHandler) Option { return func(s *Server) { s.preview = r } }

// WithForges sets the route handler for forge (GitHub/GitLab/Gitea) HTTP endpoints.
func WithForges(r routeHandler) Option { return func(s *Server) { s.forges = r } }

// WithTools sets the source of the tools engine backing the /api/tools surface.
func WithTools(src toolsSource) Option { return func(s *Server) { s.tools = src } }

// WithUtilityPrompt sets the utility prompter used for AI-assisted tasks.
func WithUtilityPrompt(p utilityPrompter) Option {
	return func(s *Server) { s.utilityPrompt = p }
}

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

// WithPolicyReload wires what a security-profile change needs after it persisted. Optional:
// unwired, live sessions keep the old presets and no client is told.
func WithPolicyReload(p policyReloader) Option {
	return func(s *Server) { s.policyReload = p }
}

// WithMCPRenderer wires the KAS-config re-render an MCP wait setting change needs.
// Optional: unwired, the setting applies at the next render.
func WithMCPRenderer(r mcpRenderer) Option {
	return func(s *Server) { s.mcpRender = r }
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

// WithKiroReady sets the kiro-cli readiness verdict /api/health reports. Unset
// leaves the probe reflecting only that the listener is up.
func WithKiroReady(ready func() (bool, pinstall.Reason)) Option {
	return func(s *Server) { s.kiroReady = ready }
}

// WithAuthUnavailable sets the sign-in leg /api/health reports. It must be a LATCH read,
// not a probe: the handler runs per request. Unset leaves readiness with no auth leg.
func WithAuthUnavailable(unavailable func() bool) Option {
	return func(s *Server) { s.authUnavailable = unavailable }
}

// WithKiroRescan sets the disk-rescan hook the loopback repair route exposes.
// Unset leaves the route unmounted.
func WithKiroRescan(rescan func(context.Context) (bool, error)) Option {
	return func(s *Server) { s.kiroRescan = rescan }
}

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

// WithWorkDir sets the workspace directory served by the file handler and git endpoints.
func WithWorkDir(d string) Option { return func(s *Server) { s.workDir = d } }

// WithTrustedProxies sets the reverse-proxy networks trusted when resolving the
// access-log client_ip. Empty trusts nothing, so the socket peer is logged.
func WithTrustedProxies(trusted []*net.IPNet) Option {
	return func(s *Server) { s.trustedProxies = trusted }
}

// WithHostPolicy sets the exact-match Host allowlist the security middleware
// applies before the CSRF check — the anti-DNS-rebinding gate. A nil or inactive
// policy accepts any Host.
func WithHostPolicy(p *webhttp.HostPolicy) Option {
	return func(s *Server) { s.hostPolicy = p }
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

// ListenAndServe registers all routes and starts the HTTP server, blocking until SIGTERM/SIGINT.
//
// Every route is a PLAIN PATH with the method gated in the handler: ServeMux's 405 fires only
// when no pattern matched, and the "/" SPA mount matches everything. Do not put a method
// back on a pattern here.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.Handle("/", spaHandler(s.staticFS))
	registerAPIFallback(mux)
	s.agent.RegisterRoutes(mux)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/kiro-settings", s.handleKiroSettings)
	s.chats.RegisterRoutes(mux)
	mux.HandleFunc("/api/health", s.handleHealth)
	// Only when this server owns the install, so the route is absent rather than a misleading 503.
	if s.kiroRescan != nil {
		mux.Handle(kiroRescanPath, loopbackOnly(kiroRescanSurface, http.HandlerFunc(s.handleKiroRescan)))
	}
	mux.Handle(pprofPath, pprofHandler())
	s.auth.RegisterRoutes(mux)
	mux.HandleFunc("/api/steering", s.handleSteering)
	// The exact app-owned pattern wins over toolbelt's subtree for EVERY method, keeping
	// "status" out of its {name} handlers.
	if s.tools != nil {
		api := &toolsAPI{src: s.tools}
		mux.Handle(toolsAPIPrefix, api)
		mux.Handle(toolsAPIPrefix+"/", api)
	}
	mux.HandleFunc("/api/tools/status", handleToolStatus)
	s.git.RegisterRoutes(mux)
	if s.gitAI != nil {
		s.gitAI.RegisterRoutes(mux)
	}
	s.files.RegisterRoutes(mux)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/tabs", s.handleTabs)
	mux.HandleFunc("/api/workspace/kiro-config", s.handleKiroConfig)
	mux.HandleFunc("/api/workspace/kiro-docs", s.handleKiroDocs)
	mux.HandleFunc("/api/specs/{dir}", s.handleSpec)
	s.mcpConfig.RegisterRoutes(mux)
	s.mcpStatus.RegisterRoutes(mux)
	s.mcpRegistry.RegisterRoutes(mux)
	if s.forges != nil {
		s.forges.RegisterRoutes(mux)
	}
	mux.HandleFunc("/api/permissions", s.handlePolicyView)
	mux.HandleFunc("/api/permissions/explain", s.handlePolicyExplain)
	mux.HandleFunc("/api/permissions/rules", s.handlePolicyRules)
	mux.HandleFunc("/api/permissions/profile", s.handlePolicyProfile)
	mux.HandleFunc("/api/utility/explain-error", s.handleUtilityExplainError)
	mux.HandleFunc("/api/utility/resolve-conflict", s.handleUtilityResolveConflict)
	mux.HandleFunc("/api/account/usage", s.handleAccountUsage)
	s.push.RegisterRoutes(mux)
	if s.preview != nil {
		s.preview.RegisterRoutes(mux)
	}
	s.registerTestHooks(mux)

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
	handler := webhttp.Chain(mux, s.middlewareStack(cspPolicy, idem)...)
	srv := webhttp.NewServer(handler)
	srv.Addr = ":" + listenPort

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
	// The bound ADDRESS, not the port constant: a misdirected boot's own output then says
	// which listener it got.
	slog.Info("Kiro Web UI listening", "addr", ln.Addr().String())
	// DNS rebinding rides the victim's BROWSER, so it reaches even a loopback bind.
	if !s.hostPolicy.Active() {
		slog.Warn("ALLOWED_HOSTS is unset or blank; any Host header is accepted, leaving DNS rebinding open even on loopback/private binds",
			"hint", "set ALLOWED_HOSTS to the exact hostnames/IPs you browse to (e.g. localhost,192.168.1.5,marotte.example.com)")
	}

	// The pre-drain hook keeps the agent-before-server order: readiness flips, the runtime stops
	// bridges and streams, and only then does the HTTP drain run.
	runErr := webhttp.Run(ctx, srv, ln, nil, webhttp.WithPreDrain(func(drainCtx context.Context) {
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

// msgUnknownAPIEndpoint is what an /api/ path no route claims answers with.
const msgUnknownAPIEndpoint = "unknown endpoint"

// registerAPIFallback puts a 404 under the /api/ subtree, so an unmatched API path is not
// answered 200 with index.html by "/". Both spellings, or GET /api draws a 301.
func registerAPIFallback(mux *http.ServeMux) {
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpreply.NotFound(w, msgUnknownAPIEndpoint)
	})
	mux.Handle(strings.TrimSuffix(apiPathPrefix, "/"), fallback)
	mux.Handle(apiPathPrefix, fallback)
}

// middlewareStack returns the middleware wrapping the route mux, OUTERMOST FIRST: access log,
// recovery, security (CSP + ALLOWED_HOSTS + CSRF), Fetch Metadata, canonical path, dedup,
// compression. A method so the ORDER, a security property, is assertable without a port.
func (s *Server) middlewareStack(cspPolicy string, idem *idempotencyCache) []webhttp.Middleware {
	return []webhttp.Middleware{
		webhttp.Logging(
			// Skipped by path, or the long-lived stream logs one open-forever line.
			webhttp.WithSkipPaths("/api/events"),
			// The shell PTY is silenced by RESPONSE, not path, so every handshake refusal keeps its line.
			webhttp.WithSkipUpgrades(true),
			// /api/health is probed every 30s, so a healthy probe logs at Debug and only
			// a failing one is surfaced.
			webhttp.ProbeLogLevel("/api/health"),
			webhttp.WithClientIP(s.trustedProxies...),
			// A preview path carries a 12h bearer token, so it logs as the route template.
			webhttp.WithTemplatePathsUnder(preview.PathPrefix),
		),
		webhttp.Recoverer(),
		func(next http.Handler) http.Handler { return securityMiddleware(cspPolicy, s.hostPolicy, next) },
		fetchMetadataGate,
		// INSIDE the host and CSRF gates (their 403 is not shadowed), OUTSIDE the mux (ServeMux
		// canonicalizes before routing) and the dedup cache (a refusal mints no replay entry).
		canonicalAPIPath,
		idem.middleware,
		// INNERMOST: the cache stores identity bytes and a replay re-negotiates encoding.
		compressJSON,
	}
}

// requirePOST returns true if r.Method is POST, and otherwise writes 405 and returns false.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return httpreply.RequireMethod(w, r, http.MethodPost)
}

// decodeBody applies LimitBody, decodes JSON into v, and returns true on success. On
// failure it writes a 400 and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpreply.DecodeBody(w, r, v, "bad request")
}

// healthBody is the readiness envelope handleHealth and handleKiroRescan both answer with. A
// struct, not a map, so the key order matches webhttp.ReadinessHandler's byte for byte.
type healthBody struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// handleHealth returns 200 {"status":"ok"} when the listener is bound and serving, or 503
// {"status":"unready",...} during startup, drain, or an unavailable kiro-cli. The kiro-cli
// verdict is version-aware and read per request without a spawn.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// Never cached: a 200 with no freshness is heuristically cacheable (RFC 9111), and a cached
	// "ok" would outlive a drain.
	w.Header().Set("Cache-Control", "no-store")
	unready := func(reason string) {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
			Status: "unready",
			Reason: reason,
		})
	}
	if !s.ready.Load() {
		unready("starting up or shutting down")
		return
	}
	if s.kiroReady != nil {
		if ok, why := s.kiroReady(); !ok {
			unready(kiroReasonText(why))
			return
		}
	}
	// The kiro-cli leg stays FIRST: it is the superset failure.
	if s.authUnavailable != nil && s.authUnavailable() {
		unready(reasonSignIn)
		return
	}
	webhttp.WriteJSON(w, healthBody{Status: "ok"})
}
