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
	"runtime"
	"sync"
	"time"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/logctl"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/mcp/prewarm"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/powers"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/server"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/trigger"
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
	// one `inspect` per lease over the agent engine the teardown below is about to stop.
	stopOrphanSweep func()
	// stopKiroSeed stops the kiro-cli settings seed and waits for its goroutine.
	stopKiroSeed func()
	// stopConfigWatch stops the config.json watch and waits for a pass or effect in flight.
	stopConfigWatch func()
	// stopToolsWatch stops the tools.json watch between ticks.
	stopToolsWatch func()
	// stopPRPoller stops the PR-status poller and waits for its goroutine.
	stopPRPoller func()
	// stopForgeKeeper stops the forge credential keeper and waits for its goroutine.
	stopForgeKeeper func()
	// stopForges ends the forge reads that run behind their callers, and waits.
	stopForges func()
	// stopGitScans ends the git status scans that run behind their answers, and waits.
	stopGitScans func()
	// stopMCPCallbacks ends the MCP store's change callbacks, and waits.
	stopMCPCallbacks func()
	// stopForgeSteering ends the forge steering syncs and waits for them.
	stopForgeSteering func()
	// stopScheduleRunner stops the schedule runner and waits for its goroutine.
	stopScheduleRunner func()
	// stopTriggerConfirmer stops the trigger confirmer's cancels and releases and waits for them.
	stopTriggerConfirmer func()
	// stopTriggerLauncher stops the trigger launcher and waits for its launch in flight.
	stopTriggerLauncher func()
	// stopTriggerRunner stops the trigger runner's polls and waits for those in flight.
	stopTriggerRunner func()
	// stopKASHeal stops the KAS MCP file heal loop and waits for its goroutine.
	stopKASHeal func()
	// stopRecentExpiry stops the Recently closed expiry runner and waits for its goroutine.
	stopRecentExpiry func()
	// stopToolsSteering waits for the boot's environment.md regeneration, which reads the tools engine.
	stopToolsSteering func()
	// stopKeepState stops the keep-state reconcile and waits: a round in flight reads KAS over the agent engine.
	stopKeepState func()
	// stopSessionSweep stops the orphan session sweep and waits: a round in flight reads the runtime's resident sessions.
	stopSessionSweep func()
	// stopApp ends the app's LIFETIME: what every process-bound component is parented on.
	stopApp func()
	// waitSignIn waits for the sign-in vault's goroutines, which end with the app's lifetime.
	waitSignIn func()
	// roots is never closed: the claims last until the process exits (heldRoots).
	roots *heldRoots
}

// Build constructs all services and wires them together. staticFS is the embedded
// web UI; cfg must be treated as read-only from here on.
func Build(ctx context.Context, cfg *Config, staticFS fs.FS) (*App, error) {
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
	logNotifyPosture(cfg)

	// Before the first write into any root: every writer below would otherwise run in a second
	// instance too, rendering its own state over the live one's.
	roots, claimErr := claimAndHealRoots(cfg)
	if claimErr != nil {
		return nil, fmt.Errorf("refusing to start: %w", claimErr)
	}
	// Before the first engine start, whose own leases carry the same stamp.
	sweepOwnSessionLeases(roots)

	// Backgrounded: the listener binds first, so a first-boot download is an unready verdict rather
	// than a missing server. Below Install because its early returns log synchronously.
	kiro := startKiroCLI(ctx, cfg)

	steer := newSteering(cfg)
	regenerateSteering(ctx, steer.Generate)

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
	// Before agent.New, so no Bot door makes a directory while the sweep judges it.
	sweepBotDirs(ctx, chatStore, bot.Paths{KiroHome: workspace.KiroHome()})

	// The sign-in the agent engine is answered from, on the app's lifetime: its poll and renewals outlive a request.
	vault := signin.Open(appCtx, cfg.ConfigDir, signinBackends()...)
	apiKey := apiKeyReachesEngine(cfg)

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

	launchRules, ruleStates := openLaunchRuleStore(cfg.ConfigDir)
	leaseStore := openRunLeaseStore(cfg.ConfigDir)
	confirmer := openTriggerConfirmer(launchRules, ruleStates, leaseStore)

	presence := push.NewPresence()
	pushSvc := push.New(appCtx, cfg.ConfigDir, cfg.VapidSub, push.WithPresence(presence))

	// The second argument is whom this reaper answers for; only the workspace root is correct.
	sessionReaper := kirosession.New(filepath.Join(workspace.KiroHome(), "sessions"), cfg.WorkDir)
	// Closed by the server once its listener has bound; the destructive session sweep
	// waits on it. Created here because the sweep starts before the server is built.
	listenerBound := make(chan struct{})
	tabStore := openTabStore(cfg.ConfigDir)
	approvalStore := openSpecApprovalStore(cfg.ConfigDir)
	authReadiness := new(command.AuthReadiness)
	lockedSettings := new(lateLockSink)
	h := agent.New(appCtx, cfg.WorkDir, engineSource{kiro: kiro, allow: cfg.BridgeEnvAllow}, chatStore,
		agent.WithSignIn(vault, engineBackend), agent.WithAPIKey(apiKey),
		agent.WithConfigDir(cfg.ConfigDir), agent.WithMCPConfig(mcpStore), agent.WithPush(pushSvc),
		agent.WithKASMCPRenderer(mcpStore),
		agent.WithDebugLogs(logctl.Debug, logctl.SetDebug),
		agent.WithPresence(presence),
		agent.WithACPArgs(cfg.ACPArgs),
		agent.WithPowers(powersMgr),
		agent.WithAuthReadiness(authReadiness),
		agent.WithSessionReaper(sessionReaper),
		agent.WithLaunchRules(launchRules),
		withTriggerConfirmer(confirmer),
		agent.WithRunLeases(leaseStore),
		agent.WithTabs(tabStore),
		agent.WithSpecApprovals(approvalStore),
		agent.WithVersions(versions),
		agent.WithGovernanceLocksHook(lockedSettings.run))
	chat.WithBroadcaster(h)(chatStore)
	chat.WithEpoch(h.Epoch)(chatStore)
	pruneTabs(ctx, tabStore, chatStore)
	stopRecentExpiry := runBackground(appCtx, "recently closed expiry", h.Membership().RunRecentExpiry)
	// Once at boot, so a Power installed from a shell before this start gets its
	// servers with no tab open; the runtime renders it again on every lock change.
	h.SyncPowers(ctx)

	// BEFORE anything can launch: relying on the scheduler's first tick would make
	// correctness a property of the tick interval. On the APP's lifetime, never Build's,
	// or the sweep outlives App.Shutdown; backgrounded because a boot must not wait.
	stopOrphanSweep := startOrphanSweep(appCtx, h.Runs().BootSweep, h.EngineReady())
	stopSessionSweep := startSessionSweep(appCtx, sessionReaper, sessionHolders(chatStore, h), listenerBound)

	stopScheduleRunner := startScheduleRunner(appCtx, launchRules, h.Runs())
	stopTriggerConfirmer := startTriggerConfirmer(appCtx, confirmer, h.EngineReady(), h.Runs())
	triggerLauncher := newTriggerLauncher(confirmer, h.Runs())

	mcpRegistry := mcp.NewRegistryProxy()
	mcpPrewarm := prewarm.NewRunner(appCtx, mcpStore)
	mcpPrewarm.OnStatus = func(pkg string, serverIDs []string, state marotte.MCPPrewarmState) {
		h.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPPrewarm, "", marotte.MCPPrewarmPayload{
			Package:   pkg,
			ServerIDs: serverIDs,
			State:     state,
		}))
	}
	mcpStore.SetOnChange(func(ctx context.Context) {
		h.Broadcast(ctx, marotte.NewEvent(marotte.EventMCPConfigChanged, "", marotte.MCPConfigChangedPayload{}))
		mcpPrewarm.Run(ctx)
		// No engine restart and nothing to forward: the persist renders KAS's own config
		// file, whose watcher reconnects in place, so a change reaches every LIVE session.
	})
	mcpPrewarm.Run(ctx)

	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		return steering.MCPSnapshot{Servers: h.MCPSnapshot()}
	})
	h.SetMCPOnChange(func() { regenerateSteering(appCtx, steer.Generate) })
	healer := &kasHealer{heal: mcpStore.HealKASConfig, afterHeal: h.SyncPowers, every: kasHealInterval}
	h.SetPreSessionOpen(func(ctx context.Context) {
		regenerateSteering(ctx, steer.Generate)
		healer.healOnce(ctx, "session_open")
	})
	stopKASHeal := runBackground(appCtx, "kas mcp config heal", healer.run)
	h.SetChatSteering(steer.ChatDocs)

	// The manager notifies nothing before the routes serve, the keeper runs or a forge snapshot
	// is read, all after forgeNotify is wired below; forgeNotifier.notify logs a call that comes earlier.
	forgeNotify := &forgeNotifier{hub: h}
	forgesManager := forges.NewManager(cfg.ConfigDir, forgeNotify.notify)
	triggerBudget := trigger.NewBudget()
	forgesManager.ObserveAttempts(triggerBudget.Observe)
	forgesManager.Load(ctx)
	h.SetEventRuleChecker(forgesManager)
	h.SetTriggerBudget(triggerBudget)
	triggerRunner := newTriggerRunner(confirmer, triggerLauncher, forges.NewFeedSource(forgesManager), triggerBudget,
		cfg.ConfigDir)
	// After the runner installed the fleet caps, so no launch passes uncapped.
	stopTriggerLauncher := startTriggerLauncher(appCtx, triggerLauncher, h.EngineReady())
	stopTriggerRunner := startTriggerRunner(appCtx, triggerRunner)

	tools, err := wireToolsEngine(cfg, h, githubTokenFor(forgesManager))
	if err != nil {
		return nil, err
	}
	stopToolsSteering := wireToolsSteering(appCtx, tools, steer)

	gitHandler := git.NewHandler(cfg.WorkDir, git.WithShowMax(filebrowse.WholeFileMax))
	ensureUploadDir()
	sensitive := filebrowse.NewSensitive(cfg.ConfigDir)
	agentDirs := settings.AgentDirs{Workspace: cfg.WorkDir, Config: cfg.ConfigDir}
	configWatch := newConfigWatch(cfg.ConfigDir, h)
	fileOpts := append(ownedFileOptions(ownedFileDeps{Config: configWatch, Tools: tools, Dirs: agentDirs}),
		filebrowse.WithLiveRoots(func(ctx context.Context) []string {
			granted, _, _ := agentDirs.Granted(ctx)
			return granted
		}))
	fileHandler, err := filebrowse.New(sensitive, cfg.BrowseRoots, fileOpts...)
	if err != nil {
		return nil, err
	}
	fileHandler.AllowToolOutputs(filepath.Join(workspace.KiroHome(), "sessions"))
	authHandler := auth.NewHandler(vault, engineBackend,
		auth.WithTrustedProxies(cfg.TrustedProxies),
		auth.WithAPIKey(apiKey))
	forgesHTTP := forges.NewHTTPHandler(forgesManager)

	prPoller := newPRStatusPoller(forgesManager, gitHandler, h, prPollGate(presence, pushSvc),
		forges.WithInventoryPush(versions, h), forges.WithViewers(presence))
	forgesHTTP.SetPoller(prPoller)
	forgeSteer := forgeNotify.wire(appCtx, prPoller, forgesManager, steer)
	forgeSteer.kick()
	stopPRPoller := runBackground(ctx, "pr status poller", prPoller.Run)
	stopForgeKeeper := runBackground(ctx, "forge credential keeper", forges.NewKeeper(forgesManager).Run)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	// The transcript GET reads both. Injected post-construction because the store cannot import
	// the agent.
	chat.WithLiveTurn(h.TurnLive)(chatStore)
	chat.WithOpenTurns(h.OpenTurns)(chatStore)
	chat.WithHookStatusShown(h.Config().HookStatusShown)(chatStore)
	purgeScheduler, startKeepState, err := wireRetention(appCtx, cfg, chatStore, h,
		retentionStores{tabs: tabStore, leases: leaseStore, reaper: sessionReaper})
	if err != nil {
		return nil, err
	}
	purgeScheduler.Start(appCtx)
	stopKeepState := startKeepState()

	previewSigner, err := preview.NewSigner()
	if err != nil {
		return nil, fmt.Errorf("preview signer: %w", err)
	}

	srv := server.New(
		server.WithSteering(steer),
		server.WithAgent(h),
		server.WithChats(chatStore),
		server.WithGit(gitHandler),
		server.WithFiles(fileHandler),
		server.WithAuth(authHandler),
		server.WithPush(pushSvc),
		server.WithMCPConfig(mcpStore),
		server.WithMCPStatus(h.MCPRegistry()),
		server.WithMCPRegistry(mcpRegistry),
		server.WithForges(forgesHTTP),
		server.WithPreview(preview.New(cfg.WorkDir, sensitive, previewSigner, slog.Default())),
		server.WithMetrics(obs.Handler()),
		server.WithTools(tools),
		server.WithAccountUsage(h),
		server.WithPolicy(h.Config()),
		server.WithGovernanceLocks(h.Config()),
		server.WithKiroDefaults(h.Config()),
		// The recycle a security-profile change needs, or the policy view describes the
		// profile that was in force before it.
		server.WithPolicyReload(h),
		server.WithStaticFS(static),
		server.WithKiroCLI(kiro.cliPath, kiro.env),
		server.WithKiroReady(kiro.ready),
		server.WithKiroRescan(kiro.rescan),
		server.WithAuthUnavailable(authReadiness.Unavailable),
		server.WithListenAddr(cfg.ListenAddr),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithSensitive(sensitive),
		server.WithKASNode(func() string { return cfg.KASLookup.Find(kiro.version()).Node }),
		server.WithTabs(tabStore),
		server.WithSpecApprovals(approvalStore),
		server.WithWorkDir(cfg.WorkDir),
		server.WithTrustedProxies(cfg.TrustedProxies),
		server.WithHostPolicy(cfg.HostPolicy),
		server.WithNotifyGate(cfg.NotifyGate),
		server.WithNotifyDeliverer(h.Notifier()),
		// ListenAndServe is callable twice and a second close panics.
		server.WithOnListen(sync.OnceFunc(func() { close(listenerBound) })),
	)
	lockedSettings.bind(appCtx, srv.ApplyGovernanceLocks)
	stopKiroSeed := startKiroSettingsSeed(appCtx, kiro.installed, srv.SeedKiroSettings)
	stopConfigWatch := startConfigWatch(appCtx, configWatch)
	stopToolsWatch := startToolsWatch(appCtx, tools)

	built = true
	return &App{
		Runtime:              h,
		Server:               srv,
		purgeScheduler:       purgeScheduler,
		mcpPrewarm:           mcpPrewarm,
		tools:                tools,
		stopKiro:             kiro.stop,
		stopOrphanSweep:      stopOrphanSweep,
		stopKiroSeed:         stopKiroSeed,
		stopConfigWatch:      stopConfigWatch,
		stopToolsWatch:       stopToolsWatch,
		stopPRPoller:         stopPRPoller,
		stopForgeKeeper:      stopForgeKeeper,
		stopForges:           func() { shutdownWithin("forge reads", forgesManager.Shutdown) },
		stopGitScans:         func() { shutdownWithin("git status scans", gitHandler.Shutdown) },
		stopMCPCallbacks:     func() { shutdownWithin("mcp change callbacks", mcpStore.Shutdown) },
		stopForgeSteering:    forgeSteer.close,
		stopScheduleRunner:   stopScheduleRunner,
		stopTriggerConfirmer: stopTriggerConfirmer,
		stopTriggerLauncher:  stopTriggerLauncher,
		stopTriggerRunner:    stopTriggerRunner,
		stopKASHeal:          stopKASHeal,
		stopRecentExpiry:     stopRecentExpiry,
		stopToolsSteering:    stopToolsSteering,
		stopKeepState:        stopKeepState,
		stopSessionSweep:     stopSessionSweep,
		stopApp:              stopApp,
		waitSignIn:           vault.Wait,
		roots:                roots,
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

// Shutdown stops background services, mostly in reverse order; a comment marks each step whose
// position is load-bearing. Every member is treated as optional: a panic on a service never
// started would abort the teardown of the rest.
func (a *App) Shutdown() {
	// First: the config watch's stop waits for an effect in flight, which pushes to the runtime;
	// the tools watch's tick reaches the engine Close ends.
	callIfSet(a.stopConfigWatch)
	callIfSet(a.stopToolsWatch)
	// Before stopForges: a poll in flight reads through the forge connections.
	callIfSet(a.stopTriggerRunner)
	callIfSet(a.stopPRPoller)
	callIfSet(a.stopForgeKeeper)
	// Before the runtime's teardown: a fill that stores a repository page notifies the hub.
	callIfSet(a.stopForges)
	callIfSet(a.stopGitScans)
	callIfSet(a.stopKASHeal)
	// Before the runtime's teardown: an expiry in flight emits through the runtime's hub.
	callIfSet(a.stopRecentExpiry)
	// Before the runtime's teardown: a sweep in flight launches its run through the runtime.
	callIfSet(a.stopScheduleRunner)
	// Before the runtime's teardown: a launch in flight runs through the runtime, and its cancels go to the
	// confirmer, so the launcher stops first.
	callIfSet(a.stopTriggerLauncher)
	// Before the runtime's teardown: a cancel in flight reaches the engine through the runtime.
	callIfSet(a.stopTriggerConfirmer)
	// Before stopKiro because this stop WAITS: a sweep reaches KAS over the agent engine the
	// runtime teardown is about to stop, so the reverse order leaves one mid-inspect.
	callIfSet(a.stopOrphanSweep)
	callIfSet(a.stopKeepState)
	callIfSet(a.stopSessionSweep)
	callIfSet(a.stopKiroSeed)
	callIfSet(a.stopKiro)
	if a.purgeScheduler != nil {
		a.purgeScheduler.Stop()
	}
	// Before the prewarm stop: a callback in flight may still queue an install, which that stop
	// then cancels and waits for.
	callIfSet(a.stopMCPCallbacks)
	if a.mcpPrewarm != nil {
		a.mcpPrewarm.Stop()
	}
	// Immediately BEFORE the runtime's teardown and no earlier: sooner would signal the
	// push service's Done while the poller that consults it is still running.
	callIfSet(a.stopApp)
	a.shutdownHub()
	// After the runtime's teardown, which may still call the vault: a renewal's save lands before exit.
	callIfSet(a.waitSignIn)
	// With stopToolsSteering and for its reason: a sync's Generate queues behind the boot
	// regeneration and reads the engine Close ends.
	callIfSet(a.stopForgeSteering)
	// Last: the boot regeneration may wait in Inventory, which takes no ctx (each probe keeps
	// toolbelt's own 10s timeout), so the wait can outlast a stop and must not hold back the two
	// above. A job event after the hub's teardown is dropped.
	callIfSet(a.stopToolsSteering)
	if a.tools != nil {
		a.tools.Close()
	}
	runtime.KeepAlive(a.roots)
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

// App's function members have no nil-safe receiver.
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
// N days. FieldStrict, so a config.json that cannot be read answers 0 rather than the default,
// which a malformed file could otherwise use to override a stored -1; an absent key, or a file
// the config watch settled as absent, keeps the default.
func chatRetention(ctx context.Context, configDir string) time.Duration {
	days, ok, err := settings.FieldStrict[int](ctx, configDir, settings.KeyChatRetentionDays)
	if err != nil {
		if !isCtxErr(err) {
			slog.Error("chat retention: config.json cannot be read; purged nothing this pass rather than applying the default window",
				"key", settings.KeyChatRetentionDays,
				"default_days", settings.DefaultChatRetentionDays, "error", err)
		}
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

// runRetention resolves how the run purge and the run-log sweep treat finished runs: disabled for
// keep-forever and for a config.json that cannot be read; a zero window purges an
// unclaimed run once no tab shows it and leaves a chat's runs to the chat; N days ages out every
// finished run N days after its last update. An absent key takes the default, as chatRetention does.
func runRetention(ctx context.Context, configDir string) agent.RunRetention {
	days, ok, err := settings.FieldStrict[int](ctx, configDir, settings.KeyChatRetentionDays)
	if err != nil {
		return agent.RunRetention{}
	}
	if !ok {
		days = settings.DefaultChatRetentionDays
	}
	if days < 0 {
		return agent.RunRetention{}
	}
	return agent.RunRetention{Window: time.Duration(days) * 24 * time.Hour, Enabled: true, ChatRuns: days > 0}
}

// sweepStaleTemps removes orphan temps left by SIGKILL between CreateTemp and Rename, sparing
// anything under an hour old. configDir is swept recursively (no hand-kept writer list), workDir
// flat. Failed and Unreadable are reported apart.
func sweepStaleTemps(ctx context.Context, configDir, workDir string) {
	const tempMaxAge = time.Hour
	for _, sweep := range []struct {
		dir  string
		opts []filesafe.SweepOption
	}{
		{configDir, []filesafe.SweepOption{filesafe.WithRecursive(true)}},
		{workDir, nil},
	} {
		res, err := filesafe.CleanupStaleTemps(ctx, sweep.dir, tempMaxAge, sweep.opts...)
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

// eventHub is where the forge notifier broadcasts; *agent.Runtime is one.
type eventHub interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// forgeNotifier is the forge manager's notify. Its poller and steering are
// set by wire after the manager is built, so a call before that logs and broadcasts
// nothing rather than dereferencing nil or silently skipping the drop.
type forgeNotifier struct {
	hub    eventHub
	poller *forges.PRStatusPoller
	steer  *forgeSteering
}

// notify drops a replaced account's inventory before any tab is told, so no
// tab re-reads the old account's entry, then syncs the steering file behind
// the caller. A stored repository page changes no row, so no tab is told.
func (n *forgeNotifier) notify(ctx context.Context, c forges.Change) {
	if n.poller == nil || n.steer == nil {
		slog.Error("forges: a change was notified before the forge poller was wired", "connection", c.ID)
		return
	}
	if c.ReposListed {
		n.steer.kick()
		return
	}
	if c.AccountChanged {
		n.poller.DropInventory(c.ID)
	}
	n.hub.Broadcast(ctx, marotte.NewEvent(marotte.EventForgesChanged, "", marotte.ForgesChangedPayload{}))
	n.steer.kick()
}

// wire makes n notify poller and a steering sync over m that renders into steer,
// and returns that sync for the caller's boot run. n is wired before steer reads
// a snapshot, because a read can start a repository fill whose landing notifies;
// the sync exists only once steer renders the forge section.
func (n *forgeNotifier) wire(appCtx context.Context, poller *forges.PRStatusPoller, m *forges.Manager,
	steer *steering.Generator,
) *forgeSteering {
	s := newForgeSteering(appCtx, m, steer.Generate)
	n.poller, n.steer = poller, s
	steer.SetForgeSnapshot(func() steering.ForgeSnapshot { return forgeSnapshot(appCtx, m) })
	return s
}

// forgeSteering regenerates the steering file when its forge section moved.
type forgeSteering struct {
	// lifetime is a child of the app's, which close also ends: sync runs behind
	// context-free callers and must not outlive the process. gate orders each
	// kick's syncs.Add before close's end, so none is added once Wait may run.
	lifetime context.Context
	end      context.CancelFunc
	m        *forges.Manager
	generate func(context.Context) error
	last     steering.ForgeSnapshot
	syncs    sync.WaitGroup
	gate     sync.Mutex
	mu       sync.Mutex
}

func newForgeSteering(appCtx context.Context, m *forges.Manager, generate func(context.Context) error) *forgeSteering {
	lifetime, end := context.WithCancel(appCtx)
	return &forgeSteering{lifetime: lifetime, end: end, m: m, generate: generate}
}

// kick runs sync behind the caller, unless close has begun.
func (s *forgeSteering) kick() {
	s.gate.Lock()
	defer s.gate.Unlock()
	if s.lifetime.Err() != nil {
		return
	}
	s.syncs.Go(s.sync)
}

// close cancels the syncs kick started, refuses any later kick, and waits for
// those in flight with no bound: a sync waits out a Generate that holds its lock
// past the cancel.
func (s *forgeSteering) close() {
	s.gate.Lock()
	s.end()
	s.gate.Unlock()
	s.syncs.Wait()
}

// shutdownWithin runs a component's Shutdown bounded by backgroundStopGrace, and logs when the
// grace runs out; what names the work it ends.
func shutdownWithin(what string, shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), backgroundStopGrace)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		slog.Warn("background work did not stop within the shutdown grace", "work", what, "grace", backgroundStopGrace, "error", err)
	}
}

// sync regenerates the steering file when the snapshot it renders moved. A
// repository page the snapshot found missing or stale is read behind it, and
// its arrival syncs again (forgeNotifier.notify).
func (s *forgeSteering) sync() {
	// Taken under mu, so last is never replaced by a snapshot older than it.
	s.mu.Lock()
	snap := forgeSnapshot(s.lifetime, s.m)
	changed := !reflect.DeepEqual(s.last, snap)
	s.last = snap
	s.mu.Unlock()
	// Generate scans the workspace, so an unmoved snapshot skips it.
	if changed {
		regenerateSteering(s.lifetime, s.generate)
	}
}

// forgeSnapshot builds the steering forge snapshot from memory: one provider per connection, read
// live so a sign-in or a sign-out reaches the next chat at once, with the repository names the
// forge cache holds for its current account.
func forgeSnapshot(ctx context.Context, m *forges.Manager) steering.ForgeSnapshot {
	rows := m.List()
	providers := make([]steering.ForgeProvider, 0, len(rows))
	for i := range rows {
		f := &rows[i]
		providers = append(providers, steering.ForgeProvider{
			Kind:  string(f.Kind),
			Host:  f.Host,
			User:  f.Username,
			Email: f.Email,
			Repos: m.HeldRepoNames(ctx, f.ID, f.Account),
		})
	}
	return steering.ForgeSnapshot{Providers: providers}
}

// wireToolsEngine starts the tools engine in its slot. An error is a failure the boot stops on;
// a survivable one leaves the slot down with its reason.
func wireToolsEngine(cfg *Config, h *agent.Runtime,
	githubToken func(context.Context) (string, error),
) (*toolsSlot, error) {
	tools := newToolsSlot(filepath.Join(cfg.ConfigDir, toolsManifestName), func() (*toolbelt.Engine, error) {
		return buildToolsEngine(cfg, h, githubToken)
	})
	if err := tools.start(); err != nil {
		return nil, err
	}
	return tools, nil
}

// bakedCommands are commands the Dockerfile's runtime stage installs; the engine reports which of
// them a host has.
var bakedCommands = []string{
	"git", "jq", "curl", "rg", "unzip", "xz", "ssh", "scp", "sftp", "ssh-keygen", "ssh-add", "ssh-keyscan",
	"openssl", "tar", "bash",
}

// A failed enqueue only logs, since installed tools persist on the volume. A toolbelt.New failure
// is returned as is, for the slot to classify. githubToken is the engine's only GitHub credential.
func buildToolsEngine(cfg *Config, h *agent.Runtime,
	githubToken func(context.Context) (string, error),
) (*toolbelt.Engine, error) {
	toolsDir, err := cfg.Tools.use()
	if err != nil {
		return nil, err
	}
	catalogRefresh := &toolbelt.CatalogRefresh{
		URL:      cfg.ToolCatalogURL,
		Require:  cfg.ToolCatalogRequire,
		Interval: cfg.ToolCatalogRefresh,
	}
	toolsEngine, err := toolbelt.New(&toolbelt.Config{
		ConfigDir: cfg.ConfigDir,
		ToolsDir:  toolsDir,
		// The engine EXECUTES what it finds here and this dir is on PATH, over a volume
		// the operator reshapes by hand. The refusal must NOT be fatal: survivableToolsFailure.
		VerifyRootIntegrity: true,
		CatalogPath:         cfg.ToolCatalogPath,
		Refresh:             catalogRefresh,
		CatalogOverlays:     cfg.ToolCatalogOverlays,
		Seed:                toolbelt.DefaultSeed(),
		System:              bakedCommands,
		GitHubToken:         githubToken,
		TrustedUIDs:         cfg.TrustedInstallUIDs,
		OnJobChanged: func(j *toolbelt.Job) {
			h.Broadcast(context.Background(), marotte.NewEvent(marotte.EventToolJobChanged, "",
				marotte.ToolJobChangedPayload{Job: j}))
			warnIfGitHubRateLimited(j)
			// Async because a job callback fires under the queue lock and must not
			// block; the call itself is idempotent.
			if j != nil && j.State == toolbelt.JobDone {
				h.KickCodeIntelligence()
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
	h.KickCodeIntelligence()
	return toolsEngine, nil
}

func newSteering(cfg *Config) *steering.Generator {
	toolsDir, _ := cfg.Tools.use()
	return steering.New(steering.Config{
		WorkDir: cfg.WorkDir, ConfigDir: cfg.ConfigDir, ToolsDir: toolsDir, ServerAddr: cfg.LocalAddr,
	})
}

// regenerateSteering runs generate and logs its failure at ERROR, except the ctx's own error, which
// is a quiet stop. It judges the error alone, so a write failing while a cancel is pending logs.
func regenerateSteering(ctx context.Context, generate func(context.Context) error) {
	err := generate(ctx)
	if err == nil || isCtxErr(err) {
		return
	}
	slog.Error("steering: environment.md not written", "error", err)
}

// isCtxErr reports a cancellation or deadline at any depth.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// wireToolsSteering lists the engine's installed tools in environment.md and regenerates it off the
// boot path, since a first inventory runs every tool's probe; without that regeneration the file
// lists no tools until the next session opens. stop cancels that regeneration and waits for it with
// no grace, unlike runBackground: the engine's Inventory ignores the context, and Shutdown closes
// the engine right after stop.
func wireToolsSteering(appCtx context.Context, tools *toolsSlot, steer *steering.Generator) (stop func()) {
	steer.SetToolsSnapshot(tools.installedTools)
	ctx, cancel := context.WithCancel(appCtx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		regenerateSteering(ctx, steer.Generate)
	}()
	return func() {
		cancel()
		<-done
	}
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
// does not touch /healthz: only the tools subsystem is off, and the Tools tab says why.
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
		"hint", "the check reports only and never repairs. Fix each path above, then save tools.json in "+
			"Settings -> Tools or restart: inside the container, chown a dir another account owns, chmod g-w,o-w "+
			"a group- or world-writable dir, and replace a symlink or a non-directory with a real directory; on the "+
			"host, strip an ACL grant (setfacl -b for a POSIX list, the host's NFSv4 ACL tool on ZFS or NFS; the "+
			"image ships neither). A user you trust that owns or can write a dir can go in TRUSTED_INSTALL_UIDS "+
			"instead, which only a restart reads; it never clears a group writer, or a managed dir that is itself "+
			"a symlink or a non-directory")
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

// openLaunchRuleStore opens the launch-rule store and its state writer, or nils to leave scheduling off. A
// malformed launch-rules.json warns rather than aborting boot (invariant 6: a bad file on the
// volume must still leave a way IN to fix it).
func openLaunchRuleStore(dir string) (*launchrule.Store, launchrule.StateWriter) {
	st, w, err := launchrule.NewStore(dir)
	if err != nil {
		slog.Warn("launch rules disabled", "error", err)
		return nil, nil
	}
	return st, w
}

// openTriggerConfirmer builds the event rules' confirmer over the launch-rule store, nil when there is none, its
// boot load read against leases. The confirmer is the only holder of w, so every event rule's state write is its
// own.
func openTriggerConfirmer(st *launchrule.Store, w launchrule.StateWriter, leases *runlease.Store) *trigger.Confirmer {
	if st == nil {
		return nil
	}
	return trigger.NewConfirmer(st, w, leases)
}

// withTriggerConfirmer hands c to the runtime, or nothing for a nil c: a nil *trigger.Confirmer inside the
// interface would read as wired.
func withTriggerConfirmer(c *trigger.Confirmer) agent.Option {
	if c == nil {
		return func(*agent.Runtime) {}
	}
	return agent.WithTriggerConfirmer(c)
}

// startTriggerConfirmer runs the confirmer's cancels and releases through fx once the engine is ready. A nil c
// starts nothing and returns a nil stop.
func startTriggerConfirmer(ctx context.Context, c *trigger.Confirmer, engineReady <-chan struct{},
	fx trigger.Effects,
) (stop func()) {
	if c == nil {
		return nil
	}
	return runBackground(ctx, "trigger confirmer", func(bctx context.Context) { c.Run(bctx, engineReady, fx) })
}

// newTriggerLauncher builds the one launcher of c's rules over l, nil when there is no confirmer.
func newTriggerLauncher(c *trigger.Confirmer, l trigger.Launcher) *trigger.LaunchLoop {
	if c == nil {
		return nil
	}
	return trigger.NewLauncher(c, l)
}

// startTriggerLauncher runs ll until ctx ends; its first launch waits for engineReady. A nil ll starts nothing and
// returns a nil stop.
func startTriggerLauncher(ctx context.Context, ll *trigger.LaunchLoop, engineReady <-chan struct{}) (stop func()) {
	if ll == nil {
		return nil
	}
	return runBackground(ctx, "trigger launcher", func(bctx context.Context) {
		if err := ll.Run(bctx, engineReady); err != nil {
			slog.Error("trigger launcher", "error", err)
		}
	})
}

// newTriggerRunner builds the poll runner of c's rules over src, installing on ll the fleet caps configDir's
// config.json sets; nil when there is no confirmer.
func newTriggerRunner(c *trigger.Confirmer, ll *trigger.LaunchLoop, src trigger.FeedSource, b *trigger.Budget,
	configDir string,
) *trigger.Runner {
	if c == nil {
		return nil
	}
	return trigger.NewRunner(c, ll, src, b, configDir)
}

// startTriggerRunner runs r's polls until ctx ends, with no presence gate: triggers exist for the hours nobody is
// present. A nil r starts nothing and returns a nil stop.
func startTriggerRunner(ctx context.Context, r *trigger.Runner) (stop func()) {
	if r == nil {
		return nil
	}
	return runBackground(ctx, "trigger runner", r.Run)
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
	if aside := st.Unread(); aside != "" {
		slog.Warn("tab arrangement unread: the run purge deletes no run until the aside is removed or restored", "aside", aside)
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

// heldUnlessDraining wraps the purge's OnPurge so closeChain runs held on the runtime's in-flight wait
// (hold): the purge scheduler outlives the runtime's Shutdown. Skipped once Shutdown began; the next boot's
// orphan-session sweep, finished-run purge and tab prune reap what the chain left.
func heldUnlessDraining(hold func() (release func(), ok bool),
	closeChain func(marotte.ChatID, []string),
) func(marotte.ChatID, []string) {
	return func(id marotte.ChatID, sessionChain []string) {
		release, ok := hold()
		if !ok {
			return
		}
		defer release()
		closeChain(id, sessionChain)
	}
}

// pruneTabs is the tab set's one-shot LOAD-TIME crash recovery for a crash between the
// coordinator's two writes. Only CHAT tabs are checked against the chat store, and only a record
// that is plainly absent drops its tab: an unreadable one may still exist. A missing file is no
// reason to close an editor, run or singleton tab. It then opens each remaining chat tab's log, so
// its read position is projected from the first request.
func pruneTabs(ctx context.Context, st *tabs.Store, chats *chat.Store) {
	if st == nil {
		return
	}
	dropped, _, err := st.Prune(ctx, func(t marotte.TabSubject) bool {
		if t.Kind != marotte.TabKindChat {
			return true
		}
		_, found, err := chats.Lookup(ctx, marotte.ChatID(t.Ref))
		if err != nil {
			slog.Warn("tab prune kept a chat whose record could not be read", "chat_id", logsafe.Field(t.Ref), "error", err)
			return true
		}
		return found
	})
	switch {
	case err != nil:
		slog.Warn("tab prune failed; the arrangement may name a chat that is gone", "error", err)
	case len(dropped) > 0:
		slog.Info("tab prune dropped tabs whose subject is gone", "count", len(dropped))
	}
	open, _ := st.List()
	for _, t := range open {
		if t.Kind != marotte.TabKindChat {
			continue
		}
		if err := chats.Warm(ctx, marotte.ChatID(t.Ref)); err != nil {
			slog.Warn("chat log not opened at boot, its unread state waits for the next read",
				"chat_id", logsafe.Field(t.Ref), "error", err)
		}
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
	if aside := st.Unread(); aside != "" {
		slog.Warn("run leases unread: no chat from before this boot is purged until the aside is removed or restored", "aside", aside)
	}
	return st
}

// startOrphanSweep runs the boot orphan sweep and RETRIES it once the first agent engine
// is ready. sweep is a method VALUE rather than the run surface, so the retry POLICY is
// testable with no agent runtime behind it.
func startOrphanSweep(ctx context.Context, sweep func(context.Context) bool,
	engineReady <-chan struct{},
) (stop func()) {
	return runBackground(ctx, "orphan sweep", func(bctx context.Context) {
		if sweep(bctx) {
			return
		}
		select {
		case <-engineReady:
			sweep(bctx)
		case <-bctx.Done():
		}
	})
}

var (
	_ kirosession.Holder = (*chat.Store)(nil)
	_ kirosession.Holder = (*agent.Runtime)(nil)
)

// sessionHolders is every record outside KAS's own state that names a KAS session marotte will load again. A new
// one joins here; TestSessionHolders_HoldEveryPersistedSessionField fails on a stored session field none holds.
func sessionHolders(chats *chat.Store, residents *agent.Runtime) kirosession.Holders {
	return kirosession.Holders{
		Records: []kirosession.Holder{chats},
		Live:    []kirosession.Holder{residents},
	}
}

// startSessionSweep runs the orphan session sweep once gate closes, then hourly; stop cancels it and waits.
func startSessionSweep(ctx context.Context, r *kirosession.Reaper, h kirosession.Holders,
	gate <-chan struct{},
) (stop func()) {
	sweep := kirosession.NewOrphanSweep(r, h)
	return runBackground(ctx, "orphan session sweep", func(bctx context.Context) { sweep.Run(bctx, gate) })
}

// The runner reuses Runtime.Launch, so a scheduled run needs no host chat. A nil store
// starts nothing and returns a nil stop.
func startScheduleRunner(ctx context.Context, st *launchrule.Store, l schedule.Launcher) (stop func()) {
	if st == nil {
		return nil
	}
	return runBackground(ctx, "schedule runner", schedule.NewRunner(st, l).Run)
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
