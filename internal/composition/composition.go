// Package composition wires all marotte services together and manages application lifecycle.
package composition

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/logctl"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/mcp/prewarm"
	"github.com/cplieger/marotte/internal/powers"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/server"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/workspace"
	"github.com/cplieger/toolbelt/v3"
)

// App holds all wired-up services for the marotte server.
type App struct {
	Runtime        *agent.Runtime
	Server         *server.Server
	purgeScheduler *archive.PurgeScheduler
	mcpPrewarm     *prewarm.Runner
	tools          *toolsSlot
	// stopKiro cancels the background kiro-cli install, so shutdown need not wait it out.
	stopKiro func()
	// stopOrphanSweep stops the boot orphan sweep and WAITS: a sweep in flight issues
	// one `inspect` per lease over the utility bridge the teardown below is about to close.
	stopOrphanSweep func()
	// stopKiroSeed stops the kiro-cli settings seed and waits for its goroutine.
	stopKiroSeed func()
	// stopPRPoller stops the PR-status poller and waits for its goroutine.
	stopPRPoller func()
	// stopForgeKeeper stops the forge credential keeper and waits for its goroutine.
	stopForgeKeeper func()
	// stopApp ends the app's LIFETIME: what every process-bound component is parented on.
	stopApp func()
}

// Build constructs all services and wires them together. staticFS is the embedded
// web UI; cfg must be treated as read-only from here on.
func Build(ctx context.Context, cfg *Config, staticFS fs.FS) (*App, error) {
	// flock, so the lock auto-releases on SIGKILL: two processes on one configDir
	// corrupt chat files.
	if err := acquireInstanceLock(cfg.ConfigDir); err != nil {
		return nil, fmt.Errorf("another marotte instance is running on %s: %w", cfg.ConfigDir, err)
	}

	if err := validateConfig(ctx, cfg); err != nil {
		return nil, fmt.Errorf("config validation failed:\n  %w", err)
	}

	// The app's lifetime: Build's ctx is context.Background() in production, so every
	// component whose work must not outlive the process is parented on appCtx.
	appCtx, stopApp := context.WithCancel(ctx)
	built := false
	defer cancelUnless(&built, stopApp)

	logctl.Install(ctx, cfg.ConfigDir)

	// The three paths a boot's blast radius derives from, on one line. KIRO_HOME decides whose KAS
	// session trees a sweep may delete and does not follow the config dir. After Install, or it
	// bypasses logfmt.
	slog.Info("boot paths resolved",
		"config_dir", cfg.ConfigDir, "work_dir", cfg.WorkDir, "kiro_home", workspace.KiroHome())
	logBridgeEnvPosture(cfg)

	// Backgrounded: the listener binds first, so a first-boot download is an unready verdict rather
	// than a missing server. Below Install because its early returns log synchronously.
	kiro := startKiroCLI(ctx, cfg)

	steer := steering.New(cfg.WorkDir, cfg.ConfigDir)
	steer.Generate(ctx)

	legacyCheckpoints := filepath.Join(cfg.ConfigDir, "checkpoints")
	if err := os.RemoveAll(legacyCheckpoints); err != nil {
		slog.Warn("legacy checkpoint wipe failed",
			"error", err, "path", legacyCheckpoints)
	}

	sweepStaleTemps(ctx, cfg.ConfigDir, cfg.WorkDir)

	versions := &subject.Versions{}
	chatStore, err := chat.NewStore(filepath.Join(cfg.ConfigDir, "chats"), chat.WithVersions(versions))
	if err != nil {
		return nil, err
	}

	// Resolved per SPAWN: resolving once per process would pin every chat to whatever
	// version was installed first.
	bridgeFactory := func() agent.ACPBridge {
		return bridge.New(kiro.cliPath(), cfg.WorkDir,
			bridge.WithEnv(kiro.env()), bridge.WithEnvAllow(cfg.BridgeEnvAllow))
	}

	mcpStore, err := mcp.New(appCtx, cfg.ConfigDir, nil,
		mcp.WithWaitForReady(func(ctx context.Context) (bool, bool) {
			return settings.MCPWaitForReady(ctx, cfg.ConfigDir)
		}))
	if err != nil {
		return nil, err
	}

	powersMgr := powers.NewManager(
		powers.NewCatalog(&http.Client{Timeout: 30 * time.Second}, powers.RegistryURL),
		mcpStore, kiro.cliPath, kiro.env,
		filepath.Join(workspace.KiroHome(), "powers", "installed"),
	)

	scheduleStore := openScheduleStore(cfg.ConfigDir)
	leaseStore := openRunLeaseStore(cfg.ConfigDir)

	presence := push.NewPresence()
	pushSvc := push.New(appCtx, cfg.ConfigDir, cfg.VapidSub, push.WithPresence(presence))

	// The second argument is whom this reaper answers for; only the workspace root is correct.
	sessionReaper := kirosession.New(filepath.Join(workspace.KiroHome(), "sessions"), cfg.WorkDir)
	// Closed by the server once its listener has bound; the destructive session sweep
	// waits on it. Created here because the runtime is built before the server.
	listenerBound := make(chan struct{})
	tabStore := openTabStore(cfg.ConfigDir)
	approvalStore := openSpecApprovalStore(cfg.ConfigDir)
	authReadiness := new(command.AuthReadiness)
	lockedSettings := new(lateLockSink)
	h := agent.New(appCtx, cfg.WorkDir, bridgeFactory, chatStore,
		agent.WithConfigDir(cfg.ConfigDir), agent.WithMCPConfig(mcpStore), agent.WithPush(pushSvc),
		agent.WithKASMCPRenderer(mcpStore),
		agent.WithDebugLogs(logctl.Debug, logctl.SetDebug),
		agent.WithPresence(presence),
		agent.WithACPArgs(cfg.ACPArgs),
		agent.WithPowers(powersMgr),
		agent.WithAuthReadiness(authReadiness),
		agent.WithSessionReaper(sessionReaper, chatStore.ReferencedSessionIDs),
		agent.WithSessionSweepGate(listenerBound),
		agent.WithSchedules(scheduleStore),
		agent.WithRunLeases(leaseStore),
		agent.WithTabs(tabStore),
		agent.WithSpecApprovals(approvalStore),
		agent.WithVersions(versions),
		agent.WithGovernanceLocksHook(lockedSettings.run))
	chat.WithBroadcaster(h)(chatStore)
	chat.WithEpoch(h.Epoch)(chatStore)
	pruneTabs(ctx, tabStore, chatStore)
	// Once at boot, so a Power installed from a shell before this start gets its
	// servers with no tab open; the runtime renders it again on every lock change.
	h.SyncPowers(ctx)

	// BEFORE anything can launch: relying on the scheduler's first tick would make
	// correctness a property of the tick interval. On the APP's lifetime, never Build's,
	// or the sweep outlives App.Shutdown; backgrounded because a boot must not wait.
	stopOrphanSweep := startOrphanSweep(appCtx, h.Runs().SweepOrphaned, kiro.installed)

	startScheduleRunner(appCtx, scheduleStore, h.Runs())

	mcpRegistry := mcp.NewRegistryProxy()
	mcpPrewarm := prewarm.NewRunner(appCtx, mcpStore)
	mcpPrewarm.OnStatus = func(pkg string, state prewarm.State) {
		h.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPPrewarm, "", marotte.MCPPrewarmPayload{
			Package: pkg,
			State:   string(state),
		}))
	}
	mcpStore.SetOnChange(func(ctx context.Context) {
		h.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPConfigChanged, "", marotte.MCPConfigChangedPayload{}))
		mcpPrewarm.Run(ctx)
		// No bridge restart and nothing to forward: the persist renders KAS's own config
		// file, whose watcher reconnects in place, so a change reaches every LIVE session.
	})
	mcpPrewarm.Run(ctx)

	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		return steering.MCPSnapshot{Servers: h.MCPSnapshot()}
	})
	h.SetMCPOnChange(func() { steer.Generate(appCtx) })
	h.SetPreBridgeSpawn(func(ctx context.Context) { steer.Generate(ctx) })
	h.SetChatSteering(steer.ChatDocs)

	forgesManager := forges.NewManager(cfg.ConfigDir)
	if refreshErr := forgesManager.Refresh(ctx); refreshErr != nil {
		_ = refreshErr
	}

	tools, err := wireToolsEngine(appCtx, cfg, h, githubTokenFor(forgesManager))
	if err != nil {
		return nil, err
	}

	gitHandler := git.NewHandler(cfg.WorkDir)
	gitAIHandler := git.NewAIHandler(cfg.WorkDir, h)
	ensureUploadDir()
	sensitive := filebrowse.NewSensitive(cfg.ConfigDir)
	fileHandler, err := filebrowse.New(sensitive, cfg.BrowseRoots,
		wireToolsManifestHook(tools, h))
	if err != nil {
		return nil, err
	}
	fileHandler.AllowToolOutputs(filepath.Join(workspace.KiroHome(), "sessions"))
	identity := auth.NewIdentity(kiro.cliPath, kiro.env, func() {
		h.RetireBridges("account identity changed")
	})
	h.SetIdentityCheck(identity.EnsureCurrent)
	authHandler := auth.NewHandler(kiro.cliPath,
		auth.WithConfig(cfg.AuthConfig),
		auth.WithTrustedProxies(cfg.TrustedProxies),
		auth.WithIdentity(identity))
	// Off the boot path: Run primes and refreshes the identity /api/whoami answers from.
	go authHandler.Run(appCtx)
	forgesHTTP := forges.NewHTTPHandler(forgesManager, h)

	// A cache, because steering.Generate runs synchronously on the pre-bridge-spawn path
	// and forgeSnapshot lists each connection's repositories from its forge.
	forgeCache := newForgeSnapshotCache(appCtx, steer, func(bctx context.Context) steering.ForgeSnapshot {
		return forgeSnapshot(bctx, forgesManager)
	})
	steer.SetForgeSnapshot(forgeCache.snapshot)
	go forgeCache.refresh()
	forgesHTTP.SetOnChange(func() { go forgeCache.refresh() })

	prPoller := newPRStatusPoller(forgesManager, gitHandler, pushSvc, prPollGate(presence, pushSvc),
		forges.WithInventoryPush(versions, h), forges.WithViewers(presence))
	forgesHTTP.SetPoller(prPoller)
	stopPRPoller := runBackground(ctx, "pr status poller", prPoller.Run)
	stopForgeKeeper := runBackground(ctx, "forge credential keeper",
		forges.NewKeeper(forgesManager, forgesHTTP.NotifyChanged).Run)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	retention := func() time.Duration { return chatRetention(ctx, cfg.ConfigDir) }
	purgeScheduler := chat.NewPurgeScheduler(chatStore, retention, func(pctx context.Context) archive.PurgeResult {
		window, purge := runRetention(pctx, cfg.ConfigDir)
		if !purge {
			return archive.PurgeResult{}
		}
		r := h.PurgeParentlessRuns(pctx, window)
		return archive.PurgeResult{NextDeadline: r.NextDeadline, Purged: r.Purged, Kept: r.Kept, Errors: r.Errors}
	})
	// The purge scans the directory live chats live in, so this predicate keeps an old-but-open
	// conversation out of it.
	chat.WithLive(h.HasLiveBridge)(chatStore)
	chat.WithOpenTab(h.Membership().HasOpenTab)(chatStore)
	// Not retention predicates: the transcript GET reads both. Injected post-construction because
	// the store cannot import the agent.
	chat.WithLiveTurn(h.TurnLive)(chatStore)
	chat.WithOpenTurns(h.OpenTurns)(chatStore)
	chat.WithOnPurge(func(id marotte.ChatID, sessionChain []string) {
		// After the per-chat record lock is released, keeping the lock order acyclic.
		// RetentionClose reaps the chain itself, so no second reap here.
		h.Membership().RetentionClose(appCtx, id, sessionChain)
	})(chatStore)
	// An exempt chat contributes no wake-up deadline, so closing its tab must trigger
	// a pass; without this the purge noticed up to an hour later.
	h.Membership().SetRetentionWake(purgeScheduler.Trigger)
	purgeScheduler.Start(appCtx)

	previewSigner, err := preview.NewSigner()
	if err != nil {
		return nil, fmt.Errorf("preview signer: %w", err)
	}

	srv := server.New(
		server.WithSteering(steer),
		server.WithAgent(h),
		server.WithChats(chatStore),
		server.WithGit(gitHandler),
		server.WithGitAI(gitAIHandler),
		server.WithFiles(fileHandler),
		server.WithAuth(authHandler),
		server.WithPush(pushSvc),
		server.WithMCPConfig(mcpStore),
		server.WithMCPStatus(h.MCPRegistry()),
		server.WithMCPRegistry(mcpRegistry),
		server.WithForges(forgesHTTP),
		server.WithPreview(preview.New(cfg.WorkDir, previewSigner, slog.Default())),
		server.WithTools(tools),
		server.WithUtilityPrompt(h),
		server.WithAccountUsage(h),
		server.WithPolicy(h.Config()),
		server.WithGovernanceLocks(h.Config()),
		// The recycle a security-profile change needs, or the policy view describes the
		// profile that was in force before it.
		server.WithPolicyReload(h),
		server.WithStaticFS(static),
		server.WithKiroCLI(kiro.cliPath, kiro.env),
		server.WithKiroReady(kiro.ready),
		server.WithKiroRescan(kiro.rescan),
		server.WithAuthUnavailable(authReadiness.Unavailable),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithSensitive(sensitive),
		server.WithTabs(tabStore),
		server.WithSpecApprovals(approvalStore),
		server.WithWorkDir(cfg.WorkDir),
		server.WithTrustedProxies(cfg.TrustedProxies),
		server.WithHostPolicy(cfg.HostPolicy),
		// ListenAndServe is callable twice and a second close panics.
		server.WithOnListen(sync.OnceFunc(func() { close(listenerBound) })),
	)
	lockedSettings.bind(appCtx, srv.ApplyGovernanceLocks)
	stopKiroSeed := startKiroSettingsSeed(appCtx, kiro.installed, srv.SeedKiroSettings)

	built = true
	return &App{
		Runtime:         h,
		Server:          srv,
		purgeScheduler:  purgeScheduler,
		mcpPrewarm:      mcpPrewarm,
		tools:           tools,
		stopKiro:        kiro.stop,
		stopOrphanSweep: stopOrphanSweep,
		stopKiroSeed:    stopKiroSeed,
		stopPRPoller:    stopPRPoller,
		stopForgeKeeper: stopForgeKeeper,
		stopApp:         stopApp,
	}, nil
}

// Run starts the HTTP server and blocks until shutdown, which the server handles itself.
func (a *App) Run() error {
	err := a.Server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		slog.Info("HTTP server shut down cleanly")
		return nil
	}
	if err != nil {
		slog.Error("HTTP server", "error", err)
		a.shutdownHub()
	}
	return err
}

// Shutdown stops background services in reverse order. Every member is treated as optional: a
// panic on a service never started would abort the teardown of the rest.
func (a *App) Shutdown() {
	callIfSet(a.stopPRPoller)
	callIfSet(a.stopForgeKeeper)
	// Before stopKiro because this stop WAITS: a sweep reaches KAS over the utility bridge
	// the kiro teardown is about to close, so the reverse order leaves one mid-inspect.
	callIfSet(a.stopOrphanSweep)
	callIfSet(a.stopKiroSeed)
	callIfSet(a.stopKiro)
	if a.purgeScheduler != nil {
		a.purgeScheduler.Stop()
	}
	if a.mcpPrewarm != nil {
		a.mcpPrewarm.Stop()
	}
	if a.tools != nil {
		a.tools.Close()
	}
	// Immediately BEFORE the runtime's teardown and no earlier: sooner would signal the
	// push service's Done while the poller that consults it is still running.
	callIfSet(a.stopApp)
	a.shutdownHub()
}

// hubStopGrace bounds the runtime teardown App.Shutdown owns: the signal context is already
// cancelled here, so a derived budget would be zero. 10s is the runtime's PTY teardown ceiling.
const hubStopGrace = 10 * time.Second

// shutdownHub tears the runtime down on that budget and LOGS an expiry: both callers are
// terminal paths with nobody above them to return an error to.
func (a *App) shutdownHub() {
	if a.Runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hubStopGrace)
	defer cancel()
	if err := a.Runtime.Shutdown(ctx); err != nil {
		slog.Error("agent runtime shutdown did not finish within the grace period",
			"grace", hubStopGrace, "error", err)
	}
}

// callIfSet runs fn when it is set; App's function members have no nil-safe receiver.
func callIfSet(fn func()) {
	if fn != nil {
		fn()
	}
}

// cancelUnless calls cancel unless *built is true, read at call time through the pointer.
// Named rather than a closure so it does not count against Build's complexity ceiling.
func cancelUnless(built *bool, cancel context.CancelFunc) {
	if !*built {
		cancel()
	}
}

// chatRetention resolves the purge window on every pass: 0 = off, -1 = forever, N > 0 = purge after
// N days. FieldStrict, so a PRESENT but unreadable config.json answers 0 rather than the default,
// which a malformed file could otherwise use to override a stored -1; an absent key or file keeps
// the default.
func chatRetention(ctx context.Context, configDir string) time.Duration {
	days, ok, err := settings.FieldStrict[int](ctx, configDir, settings.KeyChatRetentionDays)
	if err != nil {
		slog.Error("chat retention: config.json is present but unreadable; purged nothing this pass rather than applying the default window",
			"key", settings.KeyChatRetentionDays,
			"default_days", settings.DefaultChatRetentionDays, "error", err)
		return 0
	}
	if !ok {
		days = settings.DefaultChatRetentionDays
	}
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// runRetention resolves how the purge treats a finished parentless run: purge is
// false for keep-forever and for a config.json that is present and unreadable, a
// zero window purges once no tab shows the run, and N days purges N days after
// its last update. An absent key takes the default, as chatRetention does.
func runRetention(ctx context.Context, configDir string) (window time.Duration, purge bool) {
	days, ok, err := settings.FieldStrict[int](ctx, configDir, settings.KeyChatRetentionDays)
	if err != nil {
		return 0, false
	}
	if !ok {
		days = settings.DefaultChatRetentionDays
	}
	if days < 0 {
		return 0, false
	}
	return time.Duration(days) * 24 * time.Hour, true
}

// sweepStaleTemps removes orphan temps left by SIGKILL between CreateTemp and Rename, sparing
// anything under an hour old. configDir is swept recursively (no hand-kept writer list), workDir
// flat. Failed and Unreadable are reported apart.
func sweepStaleTemps(ctx context.Context, configDir, workDir string) {
	const tempMaxAge = time.Hour
	for _, sweep := range []struct {
		dir  string
		opts []atomicfile.Option
	}{
		{configDir, []atomicfile.Option{atomicfile.WithRecursive(true)}},
		{workDir, nil},
	} {
		res, err := atomicfile.CleanupStaleTemps(ctx, sweep.dir, tempMaxAge, sweep.opts...)
		if err != nil {
			slog.Debug("stale temp cleanup failed", "dir", sweep.dir, "error", err)
		}
		if res.Failed > 0 {
			slog.Warn("stale temp cleanup could not reclaim every orphan; they are accumulating on the volume",
				"dir", sweep.dir, "failed", res.Failed,
				"hint", "check ownership and mode on the paths logged at debug level")
		}
		if res.Unreadable > 0 {
			slog.Warn("stale temp cleanup could not enter every subdirectory; it may be hiding uncounted orphans",
				"dir", sweep.dir, "unreadable", res.Unreadable)
		}
		slog.Debug("stale temp cleanup done", "dir", sweep.dir, "removed", res.Removed)
	}
}

// forgeSnapshotTTL bounds how stale the cached forge snapshot may get before a read kicks a
// background revalidation.
const forgeSnapshotTTL = 5 * time.Minute

// forgeSnapshotCache is a stale-while-revalidate cache around forgeSnapshot: snapshot() never
// blocks on a forge request (zero value before the boot prime), refresh() rebuilds in the caller
// and regenerates the steering file only on a change.
type forgeSnapshotCache struct {
	// appCtx is the app's lifetime, required at construction: both entry points are
	// context-free callbacks, and both reach rebuild, which must not outlive the process.
	appCtx context.Context
	build  func(context.Context) steering.ForgeSnapshot
	steer  *steering.Generator
	at     time.Time
	snap   steering.ForgeSnapshot
	mu     sync.Mutex
	busy   bool
	dirty  bool
}

// newForgeSnapshotCache wires a cache around build that regenerates steer on a change.
// ctx is the app's lifetime and is required; see the appCtx field.
func newForgeSnapshotCache(ctx context.Context, steer *steering.Generator,
	build func(context.Context) steering.ForgeSnapshot,
) *forgeSnapshotCache {
	return &forgeSnapshotCache{appCtx: ctx, build: build, steer: steer}
}

// snapshot returns the cached forge snapshot immediately, kicking a single-flight
// background refresh when stale. Safe on the pre-bridge-spawn critical path.
func (c *forgeSnapshotCache) snapshot() steering.ForgeSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) >= forgeSnapshotTTL && !c.busy {
		c.busy = true
		go c.rebuild()
	}
	return c.snap
}

// refresh rebuilds the snapshot, then regenerates the steering file if the data changed. A request
// arriving mid-rebuild is coalesced, not dropped: that rebuild may predate the change. Callers run
// refresh in their own goroutine.
func (c *forgeSnapshotCache) refresh() {
	c.mu.Lock()
	if c.busy {
		c.dirty = true
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	c.rebuild()
}

// rebuild does the rebuild and conditional regen, looping while coalesced requests are
// pending. Entered only with busy already claimed by the caller; clears it when done.
func (c *forgeSnapshotCache) rebuild() {
	for {
		snap := c.build(c.appCtx)
		c.mu.Lock()
		changed := !reflect.DeepEqual(c.snap, snap)
		c.snap = snap
		c.at = time.Now()
		again := c.dirty
		c.dirty = false
		c.busy = again
		c.mu.Unlock()
		// Generate skips byte-identical writes anyway; skipping the RENDER for the common
		// no-change TTL refresh is what avoids a pointless workspace scan.
		if changed {
			c.steer.Generate(c.appCtx)
		}
		if !again {
			return
		}
	}
}

// forgeSnapshot builds the steering forge snapshot: one provider per configured forge,
// each enriched best-effort with its repo list.
func forgeSnapshot(ctx context.Context, forgesManager *forges.Manager) steering.ForgeSnapshot {
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	configured := forgesManager.List(fctx)
	providers := make([]steering.ForgeProvider, 0, len(configured))
	for i := range configured {
		f := &configured[i]
		providers = append(providers, steering.ForgeProvider{
			Kind:  string(f.Kind),
			Host:  f.Host,
			User:  f.Username,
			Email: f.Email,
			Repos: repoNamesFor(ctx, forgesManager, f.ID),
		})
	}
	return steering.ForgeSnapshot{Providers: providers}
}

// repoNamesFor returns every repo reachable for the given forge, or nil. Best-effort: a
// forge whose repos cannot be listed is still surfaced, just without a repo list.
func repoNamesFor(ctx context.Context, forgesManager *forges.Manager, id string) []string {
	repoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	names, err := forgesManager.RepoNames(repoCtx, id)
	if err != nil {
		return nil
	}
	return names
}

// wireToolsEngine starts the tools engine in its slot. An error is a failure the boot stops on;
// a survivable one leaves the slot down with its reason.
func wireToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime,
	githubToken func(context.Context) (string, error),
) (*toolsSlot, error) {
	tools := newToolsSlot(filepath.Join(cfg.ConfigDir, toolsManifestName), func() (*toolbelt.Engine, error) {
		return buildToolsEngine(appCtx, cfg, h, githubToken)
	})
	if err := tools.start(); err != nil {
		return nil, err
	}
	return tools, nil
}

// buildToolsEngine constructs the shared toolbelt engine with marotte's SSE adapters and enqueues
// the boot jobs, reconcile first; a failed enqueue only logs, since installed tools persist on the
// volume. A toolbelt.New failure is returned as is, for the slot to classify. githubToken is the
// engine's only GitHub credential.
func buildToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime,
	githubToken func(context.Context) (string, error),
) (*toolbelt.Engine, error) {
	catalogRefresh := &toolbelt.CatalogRefresh{
		URL:      cfg.ToolCatalogURL,
		Require:  cfg.ToolCatalogRequire,
		Interval: cfg.ToolCatalogRefresh,
	}
	toolsEngine, err := toolbelt.New(&toolbelt.Config{
		ConfigDir: cfg.ConfigDir,
		ToolsDir:  cfg.ToolsDir,
		// The engine EXECUTES what it finds here and this dir leads PATH, over a volume
		// the operator reshapes by hand. The refusal must NOT be fatal: survivableToolsFailure.
		VerifyRootIntegrity: true,
		CatalogPath:         cfg.ToolCatalogPath,
		Refresh:             catalogRefresh,
		CatalogOverlays:     cfg.ToolCatalogOverlays,
		Seed:                toolbelt.DefaultSeed(),
		System:              []string{"git", "jq", "curl", "unzip", "xz", "ssh", "tar", "bash"},
		GitHubToken:         githubToken,
		OnJobChanged: func(j *toolbelt.Job) {
			h.Broadcast(context.Background(), marotte.NewEvent(marotte.EventToolJobChanged, "",
				marotte.ToolJobChangedPayload{Job: j}))
			warnIfGitHubRateLimited(j)
			// Async because a job callback fires under the queue lock and must not
			// block; the call itself is idempotent.
			if j != nil && j.State == toolbelt.JobDone {
				// The app's lifetime, not Background: this goroutine writes lsp.json,
				// and a Background parent let SIGTERM abandon it mid-write.
				go h.EnsureCodeIntelligence(appCtx)
			}
		},
		OnJobOutput: func(jobID string, lines []string) {
			h.Broadcast(context.Background(), marotte.NewEvent(marotte.EventToolJobOutput, "",
				marotte.ToolJobOutputPayload{JobID: jobID, Lines: lines}))
		},
	})
	if err != nil {
		return nil, err
	}
	// The gate agent/code_intel.go consults; the boot fire below covers a volume that
	// already has servers but no lsp.json, later fires ride the job callback above.
	// Wired before the first job is enqueued: a job that finishes fires that callback
	// on the queue's goroutine, which reads what this sets.
	h.SetCodeIntelligence(filepath.Join(cfg.WorkDir, ".kiro", "settings", "lsp.json"), func() bool {
		inv, ierr := toolsEngine.Inventory()
		if ierr != nil {
			return false
		}
		for i := range inv.Tools {
			if inv.Tools[i].Lsp && !inv.Tools[i].Disabled && inv.Tools[i].Installed {
				return true
			}
		}
		return false
	})
	if _, _, rerr := toolsEngine.Reconcile(toolbelt.ReconcileFull); rerr != nil {
		slog.Warn("tools: boot reconcile not enqueued", "error", rerr)
	}
	if _, rerr := toolsEngine.RefreshCatalog(); rerr != nil {
		slog.Warn("tools: boot catalog refresh not enqueued", "error", rerr)
	}
	// The app's lifetime, not Background — see the OnJobChanged spawn above.
	go h.EnsureCodeIntelligence(appCtx)
	return toolsEngine, nil
}

// githubTokenFor is the tools engine's GitHub credential: the github.com connection's
// token, read per request. No usable connection sends the request anonymously rather
// than failing it, which an error would.
func githubTokenFor(m *forges.Manager) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return m.GitHubToken(ctx), nil }
}

// warnIfGitHubRateLimited names the reset and the fix for a tools job GitHub's API rate
// limit failed, which at boot no Settings screen is open to show.
func warnIfGitHubRateLimited(j *toolbelt.Job) {
	if j == nil || j.State != toolbelt.JobFailed || j.ErrorCode != toolbelt.ErrorCodeGitHubRateLimited || j.RateLimit == nil {
		return
	}
	var hint string
	switch {
	case j.RateLimit.Secondary:
		hint = "GitHub's secondary rate limit was reached; retry after the reset"
	case j.RateLimit.Authenticated:
		hint = "the connected GitHub account's limit was reached; retry after the reset"
	default:
		hint = "connect a GitHub account in Git -> Sources to raise the limit"
	}
	resets := "unknown"
	if j.RateLimit.ResetAt > 0 {
		resets = time.UnixMilli(j.RateLimit.ResetAt).Format(time.RFC3339)
	}
	slog.Warn("tools: GitHub's API rate limit failed a tools job",
		"job", j.ID, "kind", j.Kind, "authenticated", j.RateLimit.Authenticated,
		"secondary", j.RateLimit.Secondary, "resets_at", resets, "hint", hint)
}

// logRootIntegrityRefusal reports a refusal one line per offending path, then the consequence. It
// does not touch /api/health: only the tools subsystem is off, and the Tools tab says why.
func logRootIntegrityRefusal(err error) {
	refusal, ok := errors.AsType[*toolbelt.RootIntegrityError](err)
	if !ok {
		// Sentinel-classified but not carrying the type: report it all rather than nothing.
		slog.Error("tools engine disabled: a managed root failed the integrity check", "error", err)
		return
	}
	for _, f := range refusal.Findings {
		slog.Error("tools: managed root is not fit to execute from",
			"path", f.Path, "reason", f.Reason)
	}
	slog.Warn("tools engine disabled: marotte is running without the tools subsystem; "+
		"Settings -> Tools says why",
		"finding_count", len(refusal.Findings),
		"hint", "the check reports only and never repairs: fix the paths above from inside the container "+
			"(chmod g-w,o-w on a writable dir; replace a symlinked root with a real directory), then save "+
			"tools.json in Settings -> Tools or restart")
}

// warnIfNoLSPEnabled nudges when no language server is enabled: kiro-cli scans PATH at
// session start, so a box without one silently lacks code intelligence.
func warnIfNoLSPEnabled(e *toolbelt.Engine) {
	inv, err := e.Inventory()
	if err != nil {
		return
	}
	for i := range inv.Tools {
		if inv.Tools[i].Lsp && !inv.Tools[i].Disabled {
			return
		}
	}
	slog.Warn("no language servers enabled; kiro code intelligence will be limited",
		"hint", "enable gopls (Go), typescript-language-server (TypeScript), or pyright (Python) in Settings -> Tools")
}

// openScheduleStore opens the workflow-schedule store, or nil to leave scheduling off. A
// malformed schedules.json warns rather than aborting boot (invariant 6: a bad file on the
// volume must still leave a way IN to fix it).
func openScheduleStore(dir string) *schedule.Store {
	st, err := schedule.NewStore(dir)
	if err != nil {
		slog.Warn("workflow scheduling disabled", "error", err)
		return nil
	}
	return st
}

// ensureUploadDir creates the composer's upload target so the file handler can mount it, warning
// rather than failing. Needed because filebrowse's per-upload MkdirAll runs inside the mount's
// os.Root, and openMounts skips a root it cannot open.
func ensureUploadDir() {
	if err := os.MkdirAll(marotte.DefaultUploadDir, 0o755); err != nil {
		slog.Warn("composer uploads will be refused until this directory exists",
			"path", marotte.DefaultUploadDir, "error", err)
	}
}

// openTabStore opens the open-tab set, ALWAYS returning a store: the arrangement is re-derivable,
// and no store would take the four tab commands down.
func openTabStore(dir string) *tabs.Store {
	st, err := tabs.NewStore(dir)
	if err != nil {
		slog.Warn("tab arrangement starting empty", "error", err)
	}
	return st
}

// openSpecApprovalStore opens the spec-phase approval record, ALWAYS returning a store: an approval
// is re-creatable, and no store would take the approve command down.
func openSpecApprovalStore(dir string) *specapproval.Store {
	st, err := specapproval.NewStore(dir)
	if err != nil {
		slog.Warn("spec approvals starting empty", "error", err)
	}
	return st
}

// pruneTabs is the tab set's one-shot LOAD-TIME crash recovery for a crash between the
// coordinator's two writes. Only CHAT tabs are checked against the chat store; a missing file is no
// reason to close an editor, run or singleton tab.
func pruneTabs(ctx context.Context, st *tabs.Store, chats *chat.Store) {
	if st == nil {
		return
	}
	dropped, _, err := st.Prune(ctx, func(t marotte.TabSubject) bool {
		if t.Kind != marotte.TabKindChat {
			return true
		}
		_, ok := chats.Get(ctx, marotte.ChatID(t.Ref))
		return ok
	})
	if err != nil {
		slog.Warn("tab prune failed; the arrangement may name a chat that is gone", "error", err)
		return
	}
	if len(dropped) > 0 {
		slog.Info("tab prune dropped tabs whose subject is gone", "count", len(dropped))
	}
}

// openRunLeaseStore opens the durable run-lease store, ALWAYS returning one: a lease
// carries a run's wall clock and its unattended mark, so refusing to open the store would
// leave every run unbounded — the opposite of what the record is for.
func openRunLeaseStore(dir string) *runlease.Store {
	st, err := runlease.NewStore(dir)
	if err != nil {
		slog.Warn("run leases starting empty; runs from before this boot will not be swept", "error", err)
	}
	return st
}

// startOrphanSweep runs the boot orphan sweep and RETRIES it once the kiro-cli install
// completes. sweep is a method VALUE rather than the run surface, so the retry POLICY is
// testable with no agent runtime behind it.
func startOrphanSweep(ctx context.Context, sweep func(context.Context) bool,
	installed <-chan struct{},
) (stop func()) {
	return runBackground(ctx, "orphan sweep", func(bctx context.Context) {
		if sweep(bctx) {
			return
		}
		select {
		case <-installed:
			sweep(bctx)
		case <-bctx.Done():
		}
	})
}

// startScheduleRunner starts the schedule sweep when scheduling is available; the runner
// reuses Runtime.Launch, so a scheduled run needs no host chat. ctx must be the APP
// lifetime: Runner.Run's only exit is its ctx.Done arm, and Build's context never ends.
func startScheduleRunner(ctx context.Context, st *schedule.Store, l schedule.Launcher) {
	if st == nil {
		return
	}
	go schedule.NewRunner(st, l).Run(ctx)
}

// newPRStatusPoller builds the CI-flip notifier that fills the pull-request inventory. git answers
// which repos are checked out and their origins, forges joins them to connections; the lookup runs
// per sweep, so a later clone or connection is watched without a restart.
func newPRStatusPoller(mgr *forges.Manager,
	gitHandler *git.Handler, notifier forges.PRNotifier, gate func() forges.Gate, opts ...forges.PollerOption,
) *forges.PRStatusPoller {
	origins := func(rctx context.Context) []forges.RepoOrigin {
		remotes := gitHandler.RepoRemotes(rctx)
		out := make([]forges.RepoOrigin, 0, len(remotes))
		for _, r := range remotes {
			out = append(out, forges.RepoOrigin{Dir: r.Name, WebBase: r.WebBase, Slug: r.Slug})
		}
		return out
	}
	return forges.NewPRStatusPoller(forges.NewManagerPRSource(mgr, origins), notifier, gate, opts...)
}

// prPollGate opens the PR-status poller's sweep while a client is connected on the
// stream, or while the pull-request notice is enabled and a subscription exists to
// receive it. A subscription alone keeps nothing open: a browser stays subscribed
// with every tab closed, and the notice defaults off.
func prPollGate(presence *push.Presence, svc *push.Service) func() forges.Gate {
	return func() forges.Gate {
		return forges.Gate{Present: presence.AnyPresent(), Push: svc.Wants(marotte.PushKindPRStatus)}
	}
}

// backgroundStopGrace bounds how long a stop function waits for its goroutine. The wait stops a
// sweep inside a forge subprocess outliving Shutdown; cancellation reaches the subprocess through
// its context, so anything slower is a bug to log.
const backgroundStopGrace = 5 * time.Second

// runBackground starts fn on a cancellable child of ctx and returns the stop function that holds
// the cancel, since Build's context.Background() has no owner.
func runBackground(ctx context.Context, name string, fn func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(backgroundStopGrace):
			slog.Warn("background loop did not stop within the shutdown grace",
				"loop", name, "grace", backgroundStopGrace)
		}
	}
}

// lateLockSink carries governance lock changes to a writer bound after the runtime
// that publishes them is built.
type lateLockSink struct {
	apply func(context.Context)
	mu    sync.Mutex
}

func (l *lateLockSink) run(ctx context.Context) {
	l.mu.Lock()
	apply := l.apply
	l.mu.Unlock()
	if apply != nil {
		apply(ctx)
	}
}

// bind installs the writer and applies the locks in force once, because a change
// published before the bind reached no writer.
func (l *lateLockSink) bind(ctx context.Context, apply func(context.Context)) {
	l.mu.Lock()
	l.apply = apply
	l.mu.Unlock()
	apply(ctx)
}
