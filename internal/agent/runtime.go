// Package agent coordinates the server's per-chat runtime: SSE fan-out, ACP bridge lifecycle, POST
// /api/command dispatch, the utility-bridge services, agent terminals, the browser shell shim, and the MCP
// runtime registry.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/buffer"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/secretstore"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/sse"
	"github.com/cplieger/webhttp/v3"
)

// keepaliveInterval is the per-connection keepalive cadence; the client watchdog is three of them and
// liveness.AliveWindow two. A var only for tests.
var keepaliveInterval = liveness.Keepalive

// keepaliveEventName is pinned by every bundle's SSE_HEARTBEAT_EVENT listener; renaming it silences their watchdog.
const keepaliveEventName = "heartbeat"

// specChangedWindow is the spec_changed coalescing window: marks inside it
// collapse into one broadcast when it closes.
const specChangedWindow = 500 * time.Millisecond

const (
	replayBufSize = 1024
	// replayTTL bounds the ring in time: an older frame is not replayed, and a longer absence gets a fresh hello plus a digest.
	replayTTL = 10 * time.Minute
	// replyMaxEvents caps one resume's replay; past it the hello says gap_budget and the client reconciles via the digest.
	replyMaxEvents = 256

	// outputBufferLimit is the subprocess output ring budget: 64 KB covers a 200x50 screen with ANSI.
	outputBufferLimit = buffer.DefaultOutputCap
)

// lifetime groups process lifecycle, shutdown and workspace paths.
type lifetime struct {
	// shutdownCtx is the runtime's own child of New's lifetime context, so Shutdown ends the runtime without ending the app.
	shutdownCtx    context.Context
	done           chan struct{}
	shutdownCancel context.CancelFunc
	// workRoot is the kernel-confined handle on workDir (confineInWorkDir), never closed. nil when workDir could
	// not be opened; the fs handlers then refuse.
	workRoot      *os.Root
	kiroTelemetry *cachedBoolField
	workDir       string
	configDir     string
	inflight      sync.WaitGroup
	// loops covers background goroutines exiting on done, separate from inflight so a timed-out shutdown names which wedged.
	loops sync.WaitGroup
	mu    sync.Mutex
	// drainGate orders goUnlessDraining's Add before Shutdown's draining flip, so every admitted Add precedes inflight.Wait.
	drainGate sync.RWMutex
	draining  atomic.Bool
}

// goUnlessDraining runs fn on inflight unless Shutdown has begun, reporting whether it did.
func (lt *lifetime) goUnlessDraining(fn func()) bool {
	lt.drainGate.RLock()
	defer lt.drainGate.RUnlock()
	if lt.draining.Load() {
		return false
	}
	lt.inflight.Go(fn)
	return true
}

// derivedContext returns a cancellable child of the process lifetime, for
// work that must outlive the request that started it.
func (lt *lifetime) derivedContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(lt.shutdownCtx)
}

// TurnContext returns a turn's context and the teardown to defer. Detached from reqCtx's cancellation (the
// POST context dies on return) but keeping its values; shutdown re-attaches via AfterFunc.
func (lt *lifetime) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
	stop := context.AfterFunc(lt.shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// InflightAdd increments the inflight counter Shutdown waits on.
func (lt *lifetime) InflightAdd(delta int) {
	lt.inflight.Add(delta)
}

// InflightDone decrements the inflight counter.
func (lt *lifetime) InflightDone() {
	lt.inflight.Done()
}

// Draining reports that Shutdown has begun.
func (lt *lifetime) Draining() bool {
	return lt.draining.Load()
}

// bridges groups Runtime fields related to ACP bridge management.
type bridges struct {
	factory ACPBridgeFactory
	mgr     *bridgeManager
}

// bus groups SSE transport, replay and pending permissions: cplieger/sse plus chat-topic filtering and pending replay.
type bus struct {
	fanout       *sse.Hub
	pendingPerms *pendingPermsTracker
	// presence receives the hub's connect feed and client acks (alive_route.go); nil drops both.
	presence presenceTable `wiring:"optional"`
	// steers is the record of mid-turn steers and KAS's buffer the connect replay serves; separate wire objects from pendingPerms.
	steers *steerRecords
	// chatStatus holds each chat's last self-declared status (chat_status.go).
	chatStatus *chatStatusCache
	// stageStatusDesc records a declared description on the open turn, for the agent_finished push.
	stageStatusDesc func(marotte.ChatID, string)
	// retractPush drops a held push whose ask was settled (BridgeCoordinator.RetractPush).
	retractPush func(marotte.PushSubject)
	// legacyConnects and v3Connects count connects by wire generation; a legacy one (no SSE-Wire header) is an
	// old bundle still running. Observability only.
	legacyConnects atomic.Uint64
	v3Connects     atomic.Uint64
	// closeAfter cuts the next connection after that many frames (sse_probe.go). Test-only.
	closeAfter atomic.Int64
}

// Runtime is the central coordinator.
type Runtime struct {
	// steerQueue is the steer record as the steer commands and their jobs drive it.
	steerQueue steerQueue

	push      pushService
	chatStore chatRecords
	mcpConfig mcpNameSets
	lifecycle *lifetime
	bridge    *bridges
	bus       *bus
	coord     *BridgeCoordinator
	// versions is the digest registry: workspace stores and the chat store mint into it, the resolver and REST envelopes read it.
	versions *subject.Versions
	// digestSlots bounds concurrent digest resolutions (digestConcurrency).
	digestSlots digestSemaphore
	// digestHook runs inside a held slot; nil in production.
	digestHook func()

	// catalog is the workspace's one mode and model vocabulary (Catalog).
	catalog *Catalog
	// slash is the workspace slash menu, steeringIssues KAS's steering issues; both re-sent by KAS every session.
	slash          *slashCatalog
	steeringIssues *steeringIssues
	mcpRegistry    *mcpRegistry
	shellMgr       *ShellManager
	// authReadiness carries the command layer's account of a failed sign-in, which readiness reports.
	authReadiness      *command.AuthReadiness
	chatHandlers       map[string]chatHandler
	sessUpdateHandlers map[marotte.ACPUpdateKind]sessionUpdateHandler
	runStepHandlers    map[marotte.ACPUpdateKind]sessionUpdateHandler
	noopMethods        map[string]struct{}
	dispatcher         *command.Dispatcher
	translator         *translate.Translator
	// config owns the KAS configuration surface over the utility bridge.
	config *Settings
	// runs owns the workflow-run surface.
	runs      *Runs
	runRoutes *runRoutes
	inbound   *inbound
	// specs coalesces spec-directory marks into spec_changed broadcasts.
	specs         *spec.Notifier
	replay        *replay
	utility       *utilityLease
	sessionReaper *kirosession.Reaper
	sessionRefs   func(context.Context) (map[string]struct{}, bool)
	// sweepGate closes once this process serves its config dir, which the session sweep waits for. Nil: no gate.
	sweepGate  <-chan struct{}
	lines      *buffer.LineTracker
	agentTerms *agentTerminals
	hookStatus *hookStatusCache

	// secrets holds the credentials KAS asks marotte to persist (bridge_v3_secret.go), one store since KAS's key
	// namespace is global. Nil reports "absent".
	secrets *secretstore.Store

	// tabs is the open-tab set and the membership coordinator; nil without a store (unavailable). Held only for retention.
	tabs       *tabs.Store
	membership *command.Membership

	// specApprovals is the spec-phase approval record; nil means unavailable.
	specApprovals *specapproval.Store

	// steerLedger records the steers this server sent: the only way to tell the user's words from a workflow's in the same KAS buffer.
	steerLedger *command.SteerLedger

	// Code-intelligence activation inputs + in-flight guard (code_intel.go).
	ciGate func() bool
	// locksHook runs when the governance lock map moves, after the runtime's own
	// follow-ups; nil when the composition root wires none.
	locksHook func(context.Context)
	ciPath    string
	// powersBackend is WithPowers' value, read once into powers.
	powersBackend powersBackend
	powers        *powersSurface
	// acpArgs are the filtered operator launch flags (WithACPArgs), chat bridges only.
	acpArgs []string
	// sessionSeen is last among the pointer-bearing fields for fieldalignment.
	sessionSeen sessionBaseline
	// sessionExportMu serializes session exports: KAS writes each to a fixed file name per process.
	sessionExportMu sync.Mutex
	ciBusy          atomic.Bool
}

// Option configures optional Runtime parameters.
type Option func(*Runtime)

// WithConfigDir sets the configuration directory for permissions,
// checkpoints, and ignore rules.
func WithConfigDir(dir string) Option {
	return func(h *Runtime) { h.lifecycle.configDir = dir }
}

// WithACPArgs sets the operator kiro-cli flags appended to every chat bridge's argv, pre-filtered by bridge.ParseACPArgs.
func WithACPArgs(args []string) Option {
	return func(h *Runtime) { h.acpArgs = args }
}

// WithGovernanceLocksHook wires a callback for when the governance lock map moves, for locks living outside
// the runtime; it reads the locks itself (Settings.GovernanceLocks).
func WithGovernanceLocksHook(fn func(context.Context)) Option {
	return func(h *Runtime) { h.locksHook = fn }
}

// WithSchedules wires the schedule store; absent, the schedule routes are not registered.
func WithSchedules(st *schedule.Store) Option {
	return func(h *Runtime) { h.runs.schedules = st }
}

// WithRunLeases wires the durable run-lease store. Absent falls back to memory (a lease carries the run's clock);
// this adds restart survival.
func WithRunLeases(st *runlease.Store) Option {
	return func(h *Runtime) { h.runs.leases = st }
}

// WithTabs wires the open-tab set, enabling the tab commands, tabs_changed and retention's predicate.
// Absent, tab commands answer unavailable.
func WithTabs(st *tabs.Store) Option {
	return func(h *Runtime) { h.tabs = st }
}

// WithSpecApprovals wires the spec-phase approval record; absent, approve_spec_phase is unavailable.
func WithSpecApprovals(st *specapproval.Store) Option {
	return func(h *Runtime) { h.specApprovals = st }
}

// WithVersions wires the shared subject registry; absent, the runtime mints into a private one.
func WithVersions(v *subject.Versions) Option {
	return func(h *Runtime) { h.versions = v }
}

// WithPush wires the push notification service at construction time.
func WithPush(p pushService) Option {
	return func(h *Runtime) { h.push = p }
}

// WithPresence wires the push presence table; absent, every push is sent (fail-open).
func WithPresence(p presenceTable) Option {
	return func(h *Runtime) { h.bus.presence = p }
}

// WithMCPConfig wires the MCP config store, whose name sets attribute statuses and gate live control.
func WithMCPConfig(c mcpNameSets) Option {
	return func(h *Runtime) { h.mcpConfig = c }
}

// WithAuthReadiness wires command-layer sign-in outcomes to readiness.
func WithAuthReadiness(readiness *command.AuthReadiness) Option {
	return func(h *Runtime) { h.authReadiness = readiness }
}

// WithSessionReaper wires the KAS session reaper and the referenced-session thunk. refs returns (set,
// complete); an incomplete set skips the sweep (sweepSessionsOnce).
func WithSessionReaper(r *kirosession.Reaper, refs func(context.Context) (map[string]struct{}, bool)) Option {
	return func(h *Runtime) {
		h.sessionReaper = r
		h.sessionRefs = refs
	}
}

// WithSessionSweepGate holds the periodic orphan-session sweep until gate closes, once the listener bound:
// the cheapest ownership evidence, since the reaper's root is $KIRO_HOME.
func WithSessionSweepGate(gate <-chan struct{}) Option {
	return func(h *Runtime) { h.sweepGate = gate }
}

// New constructs a Runtime; tool authorization is Cedar's. ctx is the required lifetime (nil panics in
// context.WithCancel); Shutdown cancels the runtime's own child. chatStore is required too.
func New(ctx context.Context, workDir string, factory ACPBridgeFactory, chatStore chatRecords, opts ...Option) *Runtime {
	// The bus exists before its hub so the presence hook can close over it.
	sseP := &bus{
		pendingPerms: newPendingPermsTracker(),
		steers:       newSteerRecords(),
		chatStatus:   newChatStatusCache(),
	}
	// MustNew: every option is constant, so a bad set is a build defect the first test catches. The write timeout
	// is explicit so the listener's TCP_USER_TIMEOUT and the presence window agree.
	sseP.fanout = sse.MustNew(
		sse.WithReplay(replayBufSize),
		sse.WithReplayTTL(replayTTL),
		sse.WithReplyMaxEvents(replyMaxEvents),
		sse.WithKeepalive(keepaliveInterval),
		sse.WithKeepaliveEvent(keepaliveEventName),
		sse.WithWriteTimeout(liveness.AliveWindow),
		sse.WithReconnectDelay(liveness.ReconnectDelay),
		sse.WithPresence(func(ev sse.PresenceEvent) { sseP.forwardPresence(&ev) }),
	)
	lc := &lifetime{
		workDir:       workDir,
		done:          make(chan struct{}),
		kiroTelemetry: newCachedBoolField(kiroSettingsPath(), kiroTelemetryKey, true),
	}
	lc.shutdownCtx, lc.shutdownCancel = context.WithCancel(ctx)
	// Best-effort: an unopenable workDir fails closed at the handlers, not at construction.
	if root, err := os.OpenRoot(workDir); err != nil {
		slog.Error("workspace root: open failed; agent filesystem requests will be refused",
			"work_dir", workDir, "error", err)
	} else {
		lc.workRoot = root
	}

	// Locals first, so the run surface names its two collaborators rather than a *Runtime.
	bridgeP := &bridges{
		factory: factory,
		mgr:     newBridgeManager(factory),
	}
	configP := newSettings(lc, nil) // broadcast assigned below, with the rest
	runs := &Runs{
		bridges:   bridgeP.mgr,
		lifecycle: lc,
		workDir:   workDir,
		chats:     chatStore,
		perms:     sseP,
		bus:       sseP,

		cancelRetryBase: defaultCancelRetryBase,
	}

	h := &Runtime{
		lifecycle:      lc,
		bridge:         bridgeP,
		bus:            sseP,
		runs:           runs,
		config:         configP,
		chatStore:      chatStore,
		catalog:        &Catalog{},
		slash:          &slashCatalog{},
		steeringIssues: &steeringIssues{},
		versions:       &subject.Versions{},
		digestSlots:    newDigestSemaphore(digestConcurrency),
		hookStatus:     newHookStatusCache(kiroSettingsPath()),
		chatHandlers:   make(map[string]chatHandler),
		noopMethods:    make(map[string]struct{}),
	}
	// Options may write the run surface's two stores, so they run after it exists.
	for _, o := range opts {
		o(h)
	}
	// After the options and before anything can mint.
	sseP.pendingPerms.versions = h.versions
	sseP.steers.versions = h.versions
	sseP.chatStatus.versions = h.versions
	runs.asks.versions = h.versions
	h.catalog.versions = h.versions
	// Construction, then wiring, never interleaved: roles bind by value, so a nil at the literal stays nil.
	// TestNew_EveryTranslateRoleIsWired pins it.
	h.utility = &utilityLease{build: h.buildUtility, reconcile: func() { h.ReconcileSessionSettings(lc.shutdownCtx) }}
	h.runRoutes = &runRoutes{runs: runs, epoch: h.Epoch}
	h.mcpRegistry = newMCPRegistry(bridgeP.mgr, sseP, lc, h.mcpConfig)
	h.powers = &powersSurface{
		backend: h.powersBackend, utility: h.utility.get,
		bridges: bridgeP.mgr, broadcast: sseP.Broadcast,
		locks: configP.GovernanceLocks, adminKnown: configP.AdminPolicyKnown,
		refreshAdmin: configP.refreshAdminPolicy,
		detach:       lc.TurnContext, wake: make(chan struct{}, 1),
	}
	h.replay = &replay{
		chats: chatStore, lifetime: lc, workDir: workDir,
		projections: map[marotte.ChatID]*loadProjection{},
		broadcast:   sseP.Broadcast,
	}
	if lc.configDir != "" {
		runs.log = newRunLog(lc.configDir)
		bridgeP.mgr.hostsLiveRun = runs.hostsLiveRun
	}
	h.coord = newBridgeCoordinator(h)
	h.coord.reconcileSessions = h.ReconcileSessionSettings
	h.coord.autoCompact = newAutoCompactor(h.coord)
	sseP.stageStatusDesc = h.coord.turns.stageStatusDescription
	sseP.retractPush = h.coord.RetractPush
	// Built here because coord and the ignore matcher do not exist at the literal. Workspace-global: the
	// orchestrator and its subagents write one spec from several sessions.
	h.specs = spec.NewNotifier(specChangedWindow, func(dir string) {
		sseP.Broadcast(context.Background(), marotte.NewEvent(marotte.EventSpecChanged, "", marotte.SpecChangedPayload{Dir: dir}))
	})
	h.inbound = &inbound{
		lifetime: lc, coord: h.coord, chats: chatStore,
		bus: sseP, specs: h.specs,
	}
	h.shellMgr = NewShellManager(lc.shutdownCtx, workDir)
	h.lines = buffer.NewLineTracker()
	h.agentTerms = newAgentTerminals(bridgeP.mgr, lc, sseP.Broadcast, h.coord.turns.currentTurn)
	runs.terminals = h.agentTerms
	// A method value on the built Runtime (load_projection.go).
	h.replay.onProjection = h.replay.swapProjectedTranscript
	h.replay.underLifecycle = func(ctx context.Context, chatID marotte.ChatID, fn func() error) error {
		return h.coord.turns.withLifecycle(ctx, chatID, func(*chatLifecycle) error { return fn() })
	}
	// The lease's own accessor, so neither collaborator references the Runtime.
	runs.utility = h.utility.get
	runs.coord = h.coord
	configP.utility = h.utility.get
	configP.broadcast = sseP.Broadcast
	configP.onLocksChanged = h.onGovernanceLocksChanged
	configP.onAdminResolved = h.powers.requestSync
	runs.locks = configP.GovernanceLocks

	// Before both consumers.
	h.steerLedger = command.NewSteerLedger()
	h.steerQueue = steerQueue{recs: sseP.steers, coord: h.coord, locks: newChatLocks()}
	h.wireSteerRecords()
	h.translator = translate.New(h.translateRoles())
	runs.translate = h.translator
	h.dispatcher = command.New()
	h.registerCommandHandlers()
	runs.runEnded = h.membership.WakeRetention
	h.initDispatch()
	if lc.configDir != "" {
		// Best-effort: without a store, bridges do not declare `_meta.kiro.secretStorage` and MCP OAuth re-registers.
		secrets, err := secretstore.New(lc.configDir)
		if err != nil {
			slog.Error("secretstore: open failed; MCP credentials will not persist", "error", err)
		} else {
			h.secrets = secrets
		}
	}
	requireCollaborators(h)
	h.sessionSeen.seen, _ = h.sessionFingerprint(lc.shutdownCtx)
	lc.loops.Go(h.cullIdleUtilityBridge)
	lc.loops.Go(h.sweepSessionsLoop)
	lc.loops.Go(func() { h.powers.syncLoop(lc.shutdownCtx, lc.done) })
	return h
}

// UtilityPrompt delegates to the utility text-gen agent, built lazily. effort "" keeps the session's level.
func (rt *Runtime) UtilityPrompt(ctx context.Context, prompt string, effort marotte.EffortLevel) (string, error) {
	return rt.utility.get().textgen.UtilityPrompt(ctx, prompt, effort)
}

// MCPRegistry returns the MCP runtime registry as a RouteRegistrar.
func (rt *Runtime) MCPRegistry() RouteRegistrar { return rt.mcpRegistry }

// MCPSnapshot returns a stable-ordered snapshot of connected MCP servers only.
func (rt *Runtime) MCPSnapshot() []marotte.MCPSnapshotServer {
	snap := rt.mcpRegistry.Snapshot()
	out := make([]marotte.MCPSnapshotServer, 0, len(snap))
	for i := range snap {
		if snap[i].State != mcpStateConnected {
			continue
		}
		out = append(out, marotte.MCPSnapshotServer{Name: snap[i].Name})
	}
	return out
}

// SetMCPOnChange wires a callback fired on every MCP registry change (main.go regenerates environment.md).
func (rt *Runtime) SetMCPOnChange(fn func()) { rt.mcpRegistry.SetOnChange(fn) }

// SetPreBridgeSpawn wires a callback run synchronously before any bridge starts (refreshing `environment.md`),
// so it must be fast. It lives on the coordinator, which would otherwise capture a nil at construction.
func (rt *Runtime) SetPreBridgeSpawn(fn func(context.Context)) { rt.coord.preBridgeSpawn = fn }

// SetChatSteering installs the chat-only steering renderer, on the coordinator for SetPreBridgeSpawn's reason.
func (rt *Runtime) SetChatSteering(fn func(context.Context) []marotte.ClientSteeringDoc) {
	rt.coord.chatSteering = fn
}

// SetIdentityCheck wires the auth registrar's TTL-gated probe onto every chat bridge open; call before serving commands.
func (rt *Runtime) SetIdentityCheck(check func(context.Context)) {
	if check == nil {
		panic("agent: identity check is nil")
	}
	rt.coord.ensureIdentity = check
}

// RetireBridges applies an observed identity change to chat and utility sessions without interrupting runs.
func (rt *Runtime) RetireBridges(reason string) { rt.coord.RetireBridges(reason) }

// RegisterRoutes wires /api/events (SSE), /api/command (POST) and /api/shell/ws (WebSocket PTY).
func (rt *Runtime) RegisterRoutes(mux *http.ServeMux) {
	// Only these two refuse once Shutdown flips draining (refuseWhenDraining).
	mux.Handle("/api/events", rt.refuseWhenDraining(http.HandlerFunc(rt.handleSSE)))
	mux.Handle("/api/command", rt.refuseWhenDraining(rt.dispatcher))
	// Behind the same drain gate, under a RouteTimeout so a resolution parked on a lock frees its slot.
	mux.Handle("POST /api/sync", rt.refuseWhenDraining(
		webhttp.RouteTimeout(rt.bus.fanout.DigestHandler(rt.resolveDigest), digestTimeout, "digest timed out"),
	))
	// Not drain-gated: a late receipt changes nothing.
	mux.HandleFunc("POST /api/events/alive", rt.handleAlive)
	mux.HandleFunc("/api/shell/ws", rt.shellMgr.handleWS)
	mux.HandleFunc("POST /api/shell/restart", rt.shellMgr.handleRestart)
	mux.HandleFunc("/api/file-changes", rt.handleFileChanges)
	rt.config.registerKnowledgeRoutes(mux)
	rt.config.registerMemoryRoutes(mux)
	rt.config.registerHooksRoutes(mux)
	rt.config.registerGovernanceRoutes(mux)
	rt.powers.register(mux)
	rt.runRoutes.register(mux)
	// The pre-session catalog (kiro-cli 2.14 _kiro/config/template).
	mux.HandleFunc("GET /api/config-template", rt.handleConfigTemplate)
	mux.HandleFunc("GET /api/sessions", rt.handleSessionList)
	mux.HandleFunc("GET /api/chats/{id}/kiro-session", rt.handleKiroSessionExport)
	mux.HandleFunc("GET /api/slash-commands", rt.handleSlashCommands)
	mux.HandleFunc("GET /api/steering/issues", rt.handleSteeringIssues)
}

// Shutdown drains in-flight prompts and closes every bridge, bounded by ctx. Bridges stop before the
// inflight wait, since a stuck Call returns only through its bridge's teardown. ctx is the only bound:
// webhttp.Run's pre-drain hook is synchronous. The error names the first wait to exceed it.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	slog.Info("agent runtime draining")
	rt.lifecycle.drainGate.Lock()
	rt.lifecycle.draining.Store(true)
	rt.lifecycle.drainGate.Unlock()

	// 0. Stop the tickers first, so no late cull or sweep races bridge teardown.
	select {
	case <-rt.lifecycle.done:
		// Already closed (re-entrant shutdown in tests).
	default:
		close(rt.lifecycle.done)
	}

	// 1. Stop every bridge so in-flight Calls unblock, the cause claimed first; concurrently, since each Stop waits
	// out kiro-cli's SIGTERM teardown.
	bridges := rt.bridge.mgr.drain()
	for chatID := range bridges {
		rt.coord.claimShutdownCause(chatID)
	}
	var stops sync.WaitGroup
	for _, sb := range bridges {
		stops.Go(sb.bridge.Stop)
	}
	teardownErr := awaitBounded(ctx, "bridge teardown", stops.Wait)

	// 1a. Cancelled after the drain: drain() empties the map first, so no bridge reaches the death closer with a dead context.
	rt.lifecycle.shutdownCancel()

	// 1c. Unblock pending browser pushes rather than draining their 10s timeouts.
	if rt.push != nil {
		rt.push.Close()
	}

	// Each wait below is bounded by ctx; an expired one is abandoned, not cancelled.

	// 2. Wait for in-flight prompt handlers to clean up.
	if handlersErr := awaitBounded(ctx, "in-flight handlers", rt.lifecycle.inflight.Wait); teardownErr == nil {
		teardownErr = handlersErr
	}

	// 2a. The unread steers the skipped death closer would have recorded, durably. The gone flag refuses a late fold.
	for chatID := range bridges {
		rt.holdUnreadSteers(ctx, chatID)
	}

	// 2b. Join the background loops; step 0 signalled them.
	if teardownErr == nil {
		teardownErr = awaitBounded(ctx, "background loops", rt.lifecycle.loops.Wait)
	}

	// 3. The utility bridge, after the handler wait so no lease holder is mid-Call.
	if utilityErr := awaitBounded(ctx, "utility bridge teardown", rt.stopUtilityBridge); teardownErr == nil {
		teardownErr = utilityErr
	}

	// 4b. Kill agent terminals; their exit waiters decrement inflight, so this has its own bound.
	if teardownErr == nil {
		teardownErr = awaitBounded(ctx, "agent terminals", rt.agentTerms.drainAll)
	}

	// 5. Shell and SSE teardown run whatever the budget did: signals first, then waits.
	rt.shellMgr.kill(ctx)
	if err := rt.bus.fanout.Shutdown(ctx); err != nil {
		slog.Warn("sse hub shutdown", "error", err)
	}
	if teardownErr != nil {
		return teardownErr
	}
	slog.Info("runtime shutdown complete")
	return nil
}

// holdUnreadSteers hands off one drained chat's steers. The lifecycle is re-fenced first; a resend already
// opened gets its boundary note; the forward exit is awaited within ctx. Every other live row gets one
// dropped/restart entry (textless when KAS may hold it), and record-only rows join the Held row.
func (rt *Runtime) holdUnreadSteers(ctx context.Context, chatID marotte.ChatID) {
	rt.coord.turns.refence(chatID)
	if exit := rt.coord.turns.forwardExit(chatID); exit != nil {
		select {
		case <-exit:
		case <-ctx.Done():
		}
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), command.ResendBridgeWait)
	unlock, err := rt.steerQueue.LockSteerOps(lockCtx, chatID)
	cancel()
	if err != nil {
		slog.Warn("shutdown: a steer operation still held the chat; taking its steers anyway", "chat_id", chatID)
	} else {
		defer unlock()
	}
	durableCtx := durable.Context(ctx)
	rows := rt.bus.steers.ShutdownTake(chatID)
	if len(rows) == 0 {
		return
	}
	resent := rt.resentKeys(durableCtx, chatID)
	var held []shutdownRow
	for _, r := range rows {
		steer := &marotte.EntrySteer{
			Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped, Reason: marotte.SteerReasonRestart,
		}
		switch {
		case r.Agent:
			steer.Origin = marotte.SteerOriginAgent
		case resent[r.Key]:
			steer.Text, steer.Reason = r.Text, marotte.SteerReasonBoundary
		case !r.KASHeld:
			steer.Text = r.Text
			held = append(held, r)
		}
		rt.coord.recordSteer(durableCtx, chatID, r.Key, steer)
	}
	rt.coord.HoldUnread(durableCtx, chatID, held)
}

// resentKeys answers the steer keys a turn_open in the log carries; an unreadable log answers none.
func (rt *Runtime) resentKeys(ctx context.Context, chatID marotte.ChatID) map[string]bool {
	entries, err := rt.coord.chatStore.All(ctx, chatID)
	if err != nil {
		slog.Warn("shutdown: the chat's log could not be read; its steers are held", "chat_id", chatID, "error", err)
		return nil
	}
	keys := make(map[string]bool)
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindTurnOpen {
			continue
		}
		var open marotte.EntryTurnOpen
		if json.Unmarshal(entries[i].Payload, &open) != nil || open.Prompt == nil {
			continue
		}
		for _, k := range open.Prompt.Resends {
			keys[k] = true
		}
	}
	return keys
}

// awaitBounded runs wait until it completes or ctx expires, naming what still ran. The goroutine makes a
// WaitGroup or channel range cancellable.
func awaitBounded(ctx context.Context, what string, wait func()) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait()
	}()
	// AwaitDone: a bare select would sometimes report a just-finished wait as hung.
	if webhttp.AwaitDone(ctx, done) {
		return nil
	}
	return fmt.Errorf("%s still running: %w", what, ctx.Err())
}

// bridgeIdleTimeout bounds the utility session's idle time; chat bridges belong to their tabs.
const bridgeIdleTimeout = 30 * time.Minute

// Broadcast sends a ServerEvent to every SSE client: the app-facing door; in-package code uses h.bus.Broadcast.
func (rt *Runtime) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	rt.bus.emit(evt)
}

// Epoch is the hub's current epoch, stamped on every REST envelope so a client refuses a previous epoch's response.
func (rt *Runtime) Epoch() string {
	return rt.bus.fanout.Position().Epoch
}

// refuseWhenDraining answers 503 once Shutdown flips draining, for commands and the event stream only. A route
// wrapper, so health, version and static stay up; webhttp's own gate flips later, at srv.Shutdown.
func (rt *Runtime) refuseWhenDraining(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.lifecycle.draining.Load() {
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
				httpreply.ErrorJSON("shutting down"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sweepSessionsInterval is how often the orphan-session sweep runs after
// its initial boot pass.
const sweepSessionsInterval = 1 * time.Hour

// sweepSessionsLoop sweeps orphan KAS session state once this process owns its config dir, then every
// sweepSessionsInterval until shutdown.
func (rt *Runtime) sweepSessionsLoop() {
	if rt.sessionReaper == nil || rt.sessionRefs == nil {
		return
	}
	if !rt.awaitSweepGate() {
		return
	}
	rt.sweepSessionsOnce()
	ticker := time.NewTicker(sweepSessionsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rt.lifecycle.done:
			return
		case <-ticker.C:
			rt.sweepSessionsOnce()
		}
	}
}

// awaitSweepGate blocks until this process may reap, false when shutdown came first; nil proceeds. Waiting,
// not skipping: the gate closes milliseconds after Build.
func (rt *Runtime) awaitSweepGate() bool {
	if rt.sweepGate == nil {
		return true
	}
	select {
	case <-rt.sweepGate:
		return true
	case <-rt.lifecycle.done:
		return false
	}
}

// sweepSessionsOnce runs one orphan-session sweep. The keep-list is every chat's chain plus every live
// session (age is no evidence), or a live subprocess loses its state past the 10-minute guard. An
// incomplete keep-list skips the sweep.
func (rt *Runtime) sweepSessionsOnce() {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()
	refs, complete := rt.sessionRefs(ctx)
	if !complete {
		slog.Warn("kirosession: skipping orphan sweep, keep-list incomplete (a chat file could not be read)")
		return
	}
	if refs == nil {
		refs = map[string]struct{}{}
	}
	for _, id := range rt.liveSessionIDs() {
		refs[id] = struct{}{}
	}
	rt.sessionReaper.Sweep(refs)
}

// liveSessionIDs returns every held bridge's ACP session id, chat bridges and the utility session.
func (rt *Runtime) liveSessionIDs() []string {
	var ids []string
	for _, sb := range rt.bridge.mgr.all() {
		if id := string(sb.bridge.SessionID()); id != "" {
			ids = append(ids, id)
		}
	}
	if id := rt.utilityLiveSessionID(); id != "" {
		ids = append(ids, id)
	}
	return ids
}

// utilityLiveSessionID returns the utility session's id, or "". peek: get would create the session it inspects.
func (rt *Runtime) utilityLiveSessionID() string {
	u := rt.utility.peek()
	if u == nil {
		return ""
	}
	return u.session.liveID()
}

// stopUtilityBridge stops the utility session if built; take clears and stops it in one step.
func (rt *Runtime) stopUtilityBridge() {
	if u := rt.utility.take(); u != nil {
		u.session.Stop()
	}
}

// cullIdleUtilityBridge stops the utility session once idle past bridgeIdleTimeout, every 60 seconds.
// Chat bridges are owned by their tabs.
func (rt *Runtime) cullIdleUtilityBridge() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rt.lifecycle.done:
			return
		case <-ticker.C:
			rt.cullIdleUtilityBridgeOnce()
		}
	}
}

// cullIdleUtilityBridgeOnce runs one sweep; peek, since building a bridge to check idleness creates work.
func (rt *Runtime) cullIdleUtilityBridgeOnce() {
	u := rt.utility.peek()
	if u != nil && u.session.stopIfIdle(time.Now().Add(-bridgeIdleTimeout)) {
		slog.Info("culled idle utility bridge")
	}
}
