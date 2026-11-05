package composition

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/kirorelease"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/marotte/internal/signin"
	kirosignin "github.com/cplieger/marotte/internal/signin/kiro"
	"github.com/cplieger/toolbelt/v3/pin"
)

// The layout facts marotte brings to the install: where the convenience symlinks go, and
// what its own SHELL-era installer left on the volume.
const (
	// kiroLinkDir holds the non-authoritative convenience symlink a shell resolves kiro-cli
	// through. Co-owned by the toolbelt engine, which is why the legacy sweep
	// names its targets.
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
	// version is the active kiro-cli version, "" when none is active or the install is unmanaged.
	version func() string
	// env is the environment overlay for a spawned kiro-cli, nil when there is none.
	env func() []string
	// ready is the /api/health verdict plus the library's TYPED reason, or nil when this
	// app does not own the install and readiness stays pure-listener. The wording an
	// operator reads is applied at the HTTP boundary.
	ready func() (bool, pin.Reason)
	// rescan re-derives the active version from disk without downloading, or nil when there
	// is no manager. It backs the loopback repair hook.
	rescan func(context.Context) (bool, error)
	// installed is closed once a version is ACTIVE, by the install or by a rescan repairing one
	// that gave up; nil when no install can complete, and the agent engine starts at once or never.
	// A channel because nothing can wake on the manager's poll.
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
		cliPath: func() string { return kirorelease.Name },
		version: func() string { return "" },
		// Non-nil on purpose: cliPath, env and stop are called unconditionally, so only
		// ready and rescan may be nil, and their nil-ness is what MEANS "no manager".
		env:  func() []string { return nil },
		stop: func() {},
	}
}

// unavailableKiroRuntime is the runtime for a container where no version can ever activate
// (unusable pins or a rejected tools dir). It reports unready rather than letting every chat
// fail one at a time — degraded, never fatal, so the repair paths stay alive (invariant 6).
func unavailableKiroRuntime() kiroRuntime {
	return kiroRuntime{
		cliPath: func() string { return "" },
		version: func() string { return "" },
		env:     func() []string { return nil },
		ready:   func() (bool, pin.Reason) { return false, pin.ReasonUnavailable },
		stop:    func() {},
	}
}

// startKiroCLI builds the install manager and starts the install in the background, bind-first:
// only readiness waits, so a first-boot download is a /api/health reason. Three shapes: a
// rejected tools dir or unusable pins (unready), no pins or no tools dir (unmanaged), and the
// managed install. The rejection is checked first, so no pins never makes a bare-name search
// that could reach the rejected folder.
func startKiroCLI(ctx context.Context, cfg *Config) kiroRuntime {
	toolsDir, rejected := cfg.Tools.use()
	if rejected != nil {
		slog.Error("kiro-cli is not installed because marotte rejects its tools folder, so chats stay unavailable",
			"error", logsafe.Field(rejected.Error()))
		return unavailableKiroRuntime()
	}
	if cfg.KiroCLIVersion == "" || toolsDir == "" {
		slog.Warn("no kiro-cli pins or tools folder in the environment: resolving kiro-cli by bare name and installing nothing",
			"hint", "expected outside the container (bare `go run`); in the image entrypoint.sh exports KIRO_CLI_VERSION and both digests")
		return unmanagedKiroRuntime()
	}
	mgr, err := pin.New(kiroInstallConfig(cfg, toolsDir))
	if err != nil {
		slog.Error("kiro-cli install manager could not be built from the exported pins; no version can be installed, so chats stay unavailable",
			"error", err,
			"hint", "this is an image defect: check the KIRO_CLI_VERSION / KIRO_CLI_SHA256 / KIRO_CLI_SHA256_ARM64 literals in entrypoint.sh")
		return unavailableKiroRuntime()
	}
	return managedKiroRuntime(ctx, mgr)
}

func managedKiroRuntime(ctx context.Context, mgr *pin.Manager) kiroRuntime {
	ensureCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	installed := make(chan struct{})
	// Once: the install and every successful rescan each report activation.
	activated := sync.OnceFunc(func() { close(installed) })
	go func() {
		defer close(done)
		// Not acted on, but read: the one signal separating "a version is active" from "the
		// installer gave up".
		if err := mgr.EnsureWithRetry(ensureCtx); err == nil {
			activated()
		}
	}()
	return kiroRuntime{
		cliPath: mgr.Path,
		version: mgr.Version,
		env:     mgr.PathEnv,
		ready:   mgr.Ready,
		rescan: func(rctx context.Context) (bool, error) {
			ok, err := mgr.Rescan(rctx)
			if ok {
				activated()
			}
			return ok, err
		},
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

// kiroInstallConfig is marotte's whole install of the kiro-cli release: kirorelease's profile, the
// pins, the tools tree and local policy. A function so the namespace test builds a manager from the
// exact production configuration.
func kiroInstallConfig(cfg *Config, root string) *pin.Config {
	return &pin.Config{
		Name:        kirorelease.Name,
		URLTemplate: kirorelease.URLTemplate,
		ArchTokens:  kirorelease.ArchTokens(),
		ArtifactDir: kirorelease.ArtifactDir,
		ProbeArgs:   kirorelease.ProbeArgs(),
		Notice:      kirorelease.Notice,
		Version:     cfg.KiroCLIVersion,
		Digests: map[string]string{
			"amd64": cfg.KiroCLISHA256,
			"arm64": cfg.KiroCLISHA256ARM64,
		},
		Root:       root,
		LinkDir:    kiroLinkDir,
		Assertions: kiroSettings(),
		Purge:      kiroLegacyPurge(),
		// Untrusted stays unset: healBootModes has removed group and other write from this root before
		// this runs, and pin's custody check judges the writers left, so Untrusted would only add
		// a digest-verified reinstall at every start. TrustedUIDs is a fact about the volume's ACL,
		// empty by default.
		TrustedUIDs: cfg.TrustedInstallUIDs,
	}
}

// kiroLegacyPurge describes the layout marotte's shell installer left on the tools volume: the
// promoted dispatchers and the orphan staging trees. Three named targets rather than a `kiro-cli*`
// prefix, because the toolbelt engine co-owns the directory and a prefix sweep took its live
// symlink.
func kiroLegacyPurge() *pin.Purge {
	return &pin.Purge{
		Names:       kirorelease.ShellEraDispatchers(),
		StagePrefix: legacyStagePrefix,
		Marker:      legacyPurgeMarker,
	}
}

// kiroSettings is the kiro-cli settings marotte owns, re-asserted on every start. The auto-update
// gate is the one Required entry; the others are best-effort. A key the user can change must never
// be here, or every restart overwrites the choice: those are seeded where unset
// (server.SeedKiroSettings).
func kiroSettings() []pin.Assertion {
	return []pin.Assertion{
		kirorelease.AutoUpdateGate(),
		// marotte owns chat retention end to end, so kiro-cli's competing purge is pinned
		// off: 0 = never.
		kirorelease.Setting("cleanup.periodDays", "0"),
	}
}

// startKiroSettingsSeed never seeds without a managed install (installed nil), so a developer's
// own kiro-cli settings stay untouched.
func startKiroSettingsSeed(ctx context.Context, installed <-chan struct{}, seed func(context.Context)) (stop func()) {
	return runBackground(ctx, "kiro-cli settings seed", func(bctx context.Context) {
		select {
		case <-installed:
			seed(bctx)
		case <-bctx.Done():
		}
	})
}

// engineSource is the agent engine's view of the kiro-cli runtime.
type engineSource struct {
	kiro  kiroRuntime
	allow map[string]struct{}
}

var (
	_ agent.EngineSource  = engineSource{}
	_ agent.ContentSharer = engineSource{}
)

func (s engineSource) CLIPath() string               { return s.kiro.cliPath() }
func (s engineSource) Installed() <-chan struct{}    { return s.kiro.installed }
func (s engineSource) Env() []string                 { return s.kiro.env() }
func (s engineSource) EnvAllow() map[string]struct{} { return s.allow }

// shareContentSetting is the kiro-cli switch the relay derives its engine's KIRO_CONTENT_COLLECTION_ENABLED from at
// spawn, overwriting any inherited value.
const shareContentSetting = "codeWhisperer.shareCodeWhispererContentWithAWS"

// shareContentTimeout bounds one `kiro-cli settings` write, which kiro-cli serializes under its own file lock.
const shareContentTimeout = 5 * time.Second

// ShareContent writes kiro-cli's content-collection switch. It writes nothing without a managed install, so a
// developer's own kiro-cli settings stay untouched, or while no version is active: the spawn that follows writes it.
func (s engineSource) ShareContent(ctx context.Context, enabled bool) error {
	if s.kiro.installed == nil {
		return nil
	}
	path := s.kiro.cliPath()
	if path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, shareContentTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "settings", shareContentSetting, strconv.FormatBool(enabled))
	procgroup.OwnGroup(cmd, time.Second)
	cmd.Env = append(os.Environ(), s.kiro.env()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kiro-cli settings %s: %w (output %s)", shareContentSetting, err,
			logsafe.Field(strings.TrimSpace(string(out))))
	}
	return nil
}

// engineBackend is the sign-in backend the agent engine runs.
const engineBackend = kirosignin.ID

// signinBackends is every sign-in backend marotte is built with; their ids and host callbacks are unique.
func signinBackends() []signin.Backend {
	return []signin.Backend{kirosignin.New()}
}
