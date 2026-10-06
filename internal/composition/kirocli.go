package composition

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/pinstall/v3/kirocli"
)

// The layout facts marotte brings to the install: where the convenience symlink goes, and
// what its own SHELL-era installer left on the volume.
const (
	// kiroLinkDir holds the non-authoritative `docker exec … kiro-cli` symlink. Co-owned
	// by the toolbelt engine, which is why the legacy sweep names its targets.
	kiroLinkDir = "bin"
	// legacyStagePrefix prefixed the shell installer's staging trees, so a match is an
	// orphan its EXIT trap missed. Ends in a dot so it cannot match the install root.
	legacyStagePrefix = ".kiro-cli-stage."
	// legacyPurgeMarker records that the one-time migration sweep ran, so it does not walk
	// the co-owned bin directory every boot. Where the toolbelt engine never looks.
	legacyPurgeMarker = ".kiro-cli-legacy-purged"
)

// kiroRuntime is the running kiro-cli subsystem the rest of the wiring consumes. Every
// field is a FUNCTION because the install completes after the listener binds, so a path or
// an environment captured at construction would freeze the first empty answer forever.
type kiroRuntime struct {
	// cliPath resolves the active kiro-cli's absolute path, or "" when no version is
	// active. Called per use, never captured.
	cliPath func() string
	// env is the environment overlay for a spawned kiro-cli, nil when there is none.
	env func() []string
	// ready is the /api/health verdict plus the library's TYPED reason, or nil when this
	// app does not own the install and readiness stays pure-listener. The wording an
	// operator reads is applied at the HTTP boundary.
	ready func() (bool, pinstall.Reason)
	// rescan re-derives the active version from disk without downloading, or nil when there
	// is no manager. It backs the loopback repair hook.
	rescan func(context.Context) (bool, error)
	// installed is closed once a version is ACTIVE (success, not "gave up"), nil when no install
	// can complete: a utility bridge cannot start before it. A channel because nothing can wake on
	// the manager's poll.
	installed <-chan struct{}
	// stop cancels the background install AND waits, so a caller reshaping the tools tree
	// afterwards cannot race a cancelled attempt's final writes.
	stop func()
}

// unmanagedKiroRuntime is the runtime for a process with no pins: a bare `go run` outside
// the container. kiro-cli resolves by bare name through the developer's own PATH and there
// is no readiness gate, so /api/health reflects only that the listener is up.
func unmanagedKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return kirocli.Name },
		// Non-nil on purpose: cliPath, env and stop are called unconditionally, so only
		// ready and rescan may be nil, and their nil-ness is what MEANS "no manager".
		env:  func() []string { return nil },
		stop: func() {},
	}
}

// unavailableKiroRuntime is the runtime for a container whose pins are unusable, so no
// version can ever activate. It reports unready rather than letting every chat fail one at a
// time — degraded, never fatal, so the repair paths stay alive (invariant 6).
func unavailableKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return "" },
		env:     func() []string { return nil },
		ready:   func() (bool, pinstall.Reason) { return false, pinstall.ReasonUnavailable },
		stop:    func() {},
	}
}

// startKiroCLI builds the install manager and starts the install in the background, bind-first:
// only readiness waits, so a first-boot download is a /api/health reason. Three shapes: no pins,
// unusable pins (unready), and the managed install.
func startKiroCLI(ctx context.Context, cfg *Config) kiroRuntime {
	if cfg.KiroCLIVersion == "" || cfg.ToolsDir == "" {
		slog.Warn("no kiro-cli pins in the environment: resolving kiro-cli by bare name and installing nothing",
			"hint", "expected outside the container (bare `go run`); in the image entrypoint.sh exports KIRO_CLI_VERSION and both digests")
		return unmanagedKiroRuntime()
	}
	mgr, err := pinstall.New(kiroInstallConfig(cfg))
	if err != nil {
		slog.Error("kiro-cli install manager could not be built from the exported pins; no version can be installed, so chats stay unavailable",
			"error", err,
			"hint", "this is an image defect: check the KIRO_CLI_VERSION / KIRO_CLI_SHA256 / KIRO_CLI_SHA256_ARM64 literals in entrypoint.sh")
		return unavailableKiroRuntime()
	}
	ensureCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	installed := make(chan struct{})
	go func() {
		defer close(done)
		// Not acted on, but read: the one signal separating "a version is active" from "the
		// installer gave up".
		if err := mgr.EnsureWithRetry(ensureCtx); err == nil {
			close(installed)
		}
	}()
	return kiroRuntime{
		cliPath:   mgr.Path,
		env:       mgr.PathEnv,
		ready:     mgr.Ready,
		rescan:    mgr.Rescan,
		installed: installed,
		stop: func() {
			cancel()
			// Bounded because stop runs on the shutdown path: cancellation is honoured in
			// milliseconds, and the timeout only stops a library that ever stopped
			// honouring it from wedging shutdown.
			select {
			case <-done:
			case <-time.After(kiroStopGrace):
				slog.Warn("the kiro-cli installer did not stop within the shutdown grace; continuing",
					"grace", kiroStopGrace)
			}
		},
	}
}

// kiroStopGrace bounds how long shutdown waits for the background install to notice
// cancellation. Generous, because expiring it means giving up on a guarantee.
const kiroStopGrace = 5 * time.Second

// kiroInstallConfig is marotte's whole deployment of the kiro-cli release (pins, tools tree, local
// policy) over kirocli.Release()'s profile. A function so the namespace test builds a manager from
// the exact production configuration.
func kiroInstallConfig(cfg *Config) *pinstall.Config {
	return &pinstall.Config{
		Release: kirocli.Release(),
		Version: cfg.KiroCLIVersion,
		Digests: map[string]string{
			"amd64": cfg.KiroCLISHA256,
			"arm64": cfg.KiroCLISHA256ARM64,
		},
		Root:    cfg.ToolsDir,
		LinkDir: kiroLinkDir,
		// Require names the chat sidecar because `kiro-cli acp` re-execs it by a plain PATH search,
		// while `--version` is answered by the main binary: without this a sidecar-less directory
		// reported READY and failed every chat spawn.
		Require:  []string{kirocli.Name + "-chat"},
		Optional: []string{kirocli.Name + "-term"},
		Assert:   kiroSettings(),
		Purge:    kiroLegacyPurge(),
		// Untrusted stays unset: marotte makes no observation of the install root being writable by
		// others. TrustedUIDs is a fact about the volume's ACL, empty by default.
		TrustedUIDs: cfg.TrustedInstallUIDs,
	}
}

// kiroLegacyPurge describes the layout marotte's shell installer left on the tools volume: the
// promoted dispatchers and the orphan staging trees. Three named targets rather than a `kiro-cli*`
// prefix, because the toolbelt engine co-owns the directory and a prefix sweep took its live
// symlink.
func kiroLegacyPurge() *pinstall.Purge {
	return &pinstall.Purge{
		Names:       kirocli.ShellEraDispatchers(),
		StagePrefix: legacyStagePrefix,
		Marker:      legacyPurgeMarker,
	}
}

// kiroSettings is marotte's kiro-cli settings set, re-asserted on every boot, best-effort. A key
// belongs here only with a kiro-cli-SIDE role: KAS's ACP path reads no kiro-cli setting.
// app.disableAutoupdates is absent because kirocli.Release() declares it Mandatory.
func kiroSettings() []pinstall.Assertion {
	return []pinstall.Assertion{
		kirocli.Setting("chat.enableKnowledge", true),
		kirocli.Setting("chat.enableSubagent", true),
		kirocli.Setting("chat.enablePromptHints", true),
		kirocli.Setting("hooks.showStatus", true),
		// Off: telemetry, and the resource-inheritance switch seeded so the Settings UI
		// reflects reality rather than an unset-means-on fallback.
		kirocli.Setting("telemetry.enabled", false),
		kirocli.Setting("chat.disableInheritingDefaultResources", false),
		// marotte owns chat retention end to end, so kiro-cli's competing purge is pinned
		// off: 0 = never. Raw because the value is not a boolean.
		kirocli.SettingRaw("cleanup.periodDays", "0"),
	}
}
