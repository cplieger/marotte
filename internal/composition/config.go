package composition

import (
	"cmp"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notify"
	"github.com/cplieger/marotte/internal/workspace"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/toolbelt/v3/pin"
)

// Config holds all environment/flag values needed to build the app.
type Config struct {
	WorkDir   string
	ConfigDir string
	VapidSub  string
	// KiroCLIVersion and the two digests are the Renovate-pinned literals
	// entrypoint.sh declares and exports. They are the manager's whole input:
	// no version means no managed install (bare `go run`), and a malformed
	// digest fails manager construction rather than a 528 MB download.
	KiroCLIVersion     string
	KiroCLISHA256      string
	KiroCLISHA256ARM64 string
	// KASLookup is the environment kiro-cli finds its KAS runtime from; a reader resolves it per
	// use (KASLookup.Find), because the runtime is unpacked at the engine's first start.
	KASLookup workspace.KASLookup
	// Tools is the tools engine's and the kiro-cli install's root (bin/, opt/, npm/, python/,
	// kiro-cli-versions/) on the persistent volume, with the server's verdict on it.
	Tools toolsRoot
	// ToolCatalogPath is the compiled tool catalog baked into the
	// image (missing = degraded catalog search). With the runtime
	// refresh below it is only the first-boot/offline fallback.
	ToolCatalogPath string
	// ToolCatalogURL is the published catalog the engine refreshes
	// from at boot and on the ToolCatalogRefresh schedule.
	ToolCatalogURL string
	// ToolCatalogOverlays are the bundled-tools files the engine re-applies to every loaded
	// catalog: the tools marotte needs or recommends, plus their UI copy, which the published
	// catalog lacks. A missing file warns and is dropped (see bundledToolsFiles).
	ToolCatalogOverlays []string
	// ToolCatalogRequire lists the tool names a fetched catalog must
	// resolve before it replaces the current one — the embedded
	// required-tools.txt, injected by main.
	ToolCatalogRequire []string
	// TrustedProxies are the reverse-proxy prefixes whose X-Forwarded-For httpserver may trust, from
	// TRUSTED_PROXIES. Empty trusts nothing and logs the unspoofable socket peer.
	TrustedProxies []netip.Prefix
	// TrustedInstallUIDs names identities whose write access to the tools tree does not fail the
	// kiro-cli install's custody check or the tools engine's integrity check, from
	// TRUSTED_INSTALL_UIDS. Empty (the default) refuses any tree another identity can write.
	// Never compiled in: only the deployment knows which account is already as privileged as
	// this process.
	TrustedInstallUIDs []int
	// HostPolicy is the exact-match Host allowlist from ALLOWED_HOSTS, the anti-DNS-rebinding gate
	// applied before the CSRF check. Unset = inactive, any Host accepted (the server warns at
	// listen time).
	HostPolicy *httpserver.HostPolicy
	// BridgeEnvAllow re-permits names the bridge's credential screen would drop, from
	// bridge.EnvAllowVar. Nil is the shipped and right configuration.
	BridgeEnvAllow map[string]struct{}
	// BrowseRoots is the file browser's allow-list: WorkDir, ConfigDir, marotte.DefaultUploadDir,
	// plus MAROTTE_BROWSE_ROOTS grants (colon-separated absolute paths). Everything else is denied.
	BrowseRoots []string
	// notifyErr is a configured but unusable notify token, which validateConfig makes fatal.
	notifyErr error
	// ACPArgs are operator kiro-cli launch flags from MAROTTE_KIRO_ACP_ARGS, filtered by
	// bridge.ParseACPArgs and appended to the agent engine's argv. An escape hatch for a flag
	// upstream adds, not a capability switch.
	ACPArgs []string
	// ToolCatalogRefresh is the engine refresh cadence (toolbelt's 24h default), with no env override.
	ToolCatalogRefresh time.Duration
	// NotifyGate admits the notify route's bearer token from notify.TokenEnv; the zero Gate is
	// the door off. It holds the token's digest only.
	NotifyGate notify.Gate
	// KiroAPIKeySet records whether bridge.KiroAPIKeyVar is present. The value
	// is never read into Config.
	KiroAPIKeySet bool
}

// ConfigFromEnv reads configuration from environment variables with
// sensible defaults.
func ConfigFromEnv() Config {
	paths := resolveBootPaths(envx.Source{})
	configDir := paths.configDir
	workDir := absDir("KIRO_WORK_DIR", cmp.Or(envx.String("KIRO_WORK_DIR"), "/workspace"))
	notifyGate, notifyErr := notifyGateFromEnv()
	return Config{
		NotifyGate:          notifyGate,
		notifyErr:           notifyErr,
		WorkDir:             workDir,
		ConfigDir:           configDir,
		KiroCLIVersion:      envx.String("KIRO_CLI_VERSION"),
		KiroCLISHA256:       envx.String("KIRO_CLI_SHA256"),
		KiroCLISHA256ARM64:  envx.String("KIRO_CLI_SHA256_ARM64"),
		VapidSub:            cmp.Or(envx.String("VAPID_SUBJECT"), "mailto:marotte@noreply.invalid"),
		Tools:               paths.tools,
		KASLookup:           workspace.KASLookupFromEnv(),
		ToolCatalogPath:     cmp.Or(envx.String("MAROTTE_TOOL_CATALOG"), "/opt/marotte/tool-catalog.json"),
		ToolCatalogURL:      cmp.Or(envx.String("MAROTTE_TOOL_CATALOG_URL"), toolbelt.DefaultCatalogURL),
		ToolCatalogRefresh:  toolbelt.DefaultCatalogRefresh,
		ToolCatalogOverlays: bundledToolsFiles(os.Getenv("MAROTTE_BUNDLED_TOOLS")),
		TrustedProxies:      parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		TrustedInstallUIDs:  parseTrustedInstallUIDs(os.Getenv("TRUSTED_INSTALL_UIDS")),
		HostPolicy:          parseAllowedHosts(os.Getenv("ALLOWED_HOSTS")),
		BrowseRoots:         browseRoots(workDir, configDir, os.Getenv("MAROTTE_BROWSE_ROOTS")),
		ACPArgs:             bridge.ParseACPArgs(os.Getenv("MAROTTE_KIRO_ACP_ARGS")),
		BridgeEnvAllow:      bridge.ParseEnvAllowlist(os.Getenv(bridge.EnvAllowVar)),
		KiroAPIKeySet:       os.Getenv(bridge.KiroAPIKeyVar) != "",
	}
}

// absDir makes a configured directory absolute against the startup cwd: the browse roots, the file
// API and the config-file doors treat the config dir as an absolute identity, and a relative tools
// dir would give toolbelt's bin/ symlinks targets resolved against bin/ itself. A failed Getwd
// leaves the value cleaned and relative, with a warning.
func absDir(envVar, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		slog.Warn("config: cannot make the directory absolute", "env", envVar, "dir", dir, "error", err)
		return filepath.Clean(dir)
	}
	return abs
}

// apiKeyReachesEngine reports whether bridge.KiroAPIKeyVar is set AND allowlisted, so the agent
// engine authenticates with the key rather than the vault's sign-in.
func apiKeyReachesEngine(cfg *Config) bool {
	if !cfg.KiroAPIKeySet {
		return false
	}
	_, allowed := cfg.BridgeEnvAllow[bridge.KiroAPIKeyVar]
	return allowed
}

// logBridgeEnvPosture warns once at boot when an allowlisted API key reaches
// kiro-cli. Not allowlisted needs no line: the per-spawn drop warning names it.
func logBridgeEnvPosture(cfg *Config) {
	if !apiKeyReachesEngine(cfg) {
		return
	}
	slog.Warn(bridge.KiroAPIKeyVar + " reaches kiro-cli because " + bridge.EnvAllowVar +
		" allows it: the agent engine authenticates with it ahead of marotte's sign-in, " +
		"so the agent runs as the key's identity, not the signed-in account marotte shows")
}

// defaultBundledTools is the image path of marotte's bundled-tools file (shipped by the Dockerfile
// beside the binary). A var, not a const, so a test can point the default at an absent path; never
// reassigned in production.
var defaultBundledTools = "/opt/marotte/bundled-tools.json"

// A missing file warns: it is the only place gopls, typescript, typescript-language-server and
// pyright exist, so every DefaultSeed template would fail at enable time. Non-fatal, so a bare `go
// run` still works.
func bundledToolsFiles(explicit string) []string {
	path := filepath.Clean(cmp.Or(explicit, defaultBundledTools))
	if _, err := os.Stat(path); err != nil { // #nosec G703 -- operator-supplied env var, cleaned above; an existence probe that reads no content
		slog.Warn("config: bundled tools file does not resolve; the seeded language servers "+
			"will not resolve at enable time",
			"path", path, "explicit", explicit != "",
			"env", "MAROTTE_BUNDLED_TOOLS", "error", err)
		return nil
	}
	return []string{path}
}

// browseRoots assembles the file browser's allow-list: the three standard mounts plus
// MAROTTE_BROWSE_ROOTS grants, malformed entries logged and skipped so a typo cannot take the UI
// down. The uploads directory is a standard mount because an upload with no "dir" targets it, and
// an ungranted target is a 403.
func browseRoots(workDir, configDir, raw string) []string {
	extra, invalid := filebrowse.ParseBrowseRoots(raw)
	if len(invalid) > 0 {
		slog.Warn("config: ignoring malformed MAROTTE_BROWSE_ROOTS entries (want absolute paths, colon-separated)",
			"entries", invalid)
	}
	roots := make([]string, 0, 3+len(extra))
	roots = append(roots, workDir, configDir, marotte.DefaultUploadDir)
	return append(roots, extra...)
}

// Unset yields nil. LENIENT and fail-SAFE: a malformed entry is logged and skipped, falling back to
// the socket peer, never trusting a forwarded header.
func parseTrustedProxies(raw string) []netip.Prefix {
	prefixes, invalid := httpserver.ParseCIDRs(strings.Split(raw, ","))
	if len(invalid) > 0 {
		slog.Warn("config: ignoring malformed TRUSTED_PROXIES entries (want CIDR or IP)",
			"entries", invalid)
	}
	return prefixes
}

// Unset yields nil, fully enforcing. Each entry ASSERTS the uid is already as privileged as this
// process. Malformed entries drop with one warning that reports a count, never the values.
func parseTrustedInstallUIDs(raw string) []int {
	uids, rejected := pin.ParseIdentities(raw)
	if rejected > 0 {
		slog.Warn("config: ignoring unusable TRUSTED_INSTALL_UIDS entries (want whole numbers above 0)",
			"invalid_count", rejected,
			"hint", "list only numeric uids, comma-separated, each an account already at least as privileged as this server")
	}
	return uids
}

// parseAllowedHosts parses ALLOWED_HOSTS into a httpserver.HostPolicy, the exact-Host gate that closes
// the DNS-rebinding hole CSRF alone leaves open (CWE-346). The loopback carve-out is enabled and
// the 403 names ALLOWED_HOSTS; malformed entries are dropped. Unset or blank is an INACTIVE policy;
// an all-invalid list is an active EMPTY one that fails closed, warned by name.
func parseAllowedHosts(raw string) *httpserver.HostPolicy {
	policy, invalid := httpserver.ParseHostList(strings.Split(raw, ","),
		httpserver.WithLoopbackExempt(true),
		httpserver.WithHostAllowlistError("",
			"host not allowed. Add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		slog.Warn("config: dropping malformed ALLOWED_HOSTS entries; they cannot match any browser-sent Host",
			"entries", invalid,
			"hint", "use bare hostnames or IPs only (no scheme, path, or CIDR), e.g. localhost,192.0.2.5,marotte.example.com")
	}
	if policy.Active() && policy.Size() == 0 {
		slog.Warn("config: ALLOWED_HOSTS has no usable entries; rejecting every non-loopback request (fail closed)",
			"hint", "fix the entries listed in the preceding warning to restore browser access")
	}
	return policy
}

// notifyGateFromEnv reads notify.TokenEnv through envx's file convention. Unset is the door off
// with no error; anything configured but unusable is an error naming the key and the failure
// class only, because envx's own errors carry the file path.
func notifyGateFromEnv() (notify.Gate, error) {
	const key = notify.TokenEnv
	if envx.IsBlankSecretFilePath(key) {
		return notify.Gate{}, errors.New(key + "_FILE is set but blank: unset it, or point it at the token file")
	}
	value, _, err := envx.SecretWithSource(key)
	var missing *envx.MissingError
	switch {
	case errors.As(err, &missing):
		return notify.Gate{}, nil
	case err != nil:
		return notify.Gate{}, errors.New(key + "_FILE cannot be used: " + secretFileFailure(err))
	}
	return notify.ParseToken(value)
}

// secretFileFailure names an envx secret-file failure's class without its path.
func secretFileFailure(err error) string {
	switch {
	case errors.Is(err, envx.ErrBlankSecretFile):
		return "the file is blank"
	case errors.Is(err, envx.ErrSecretFilePathRejected):
		return "the path must be clean, with no .. component"
	case errors.Is(err, envx.ErrSecretFileTooLarge), errors.Is(err, envx.ErrSecretFileGrew):
		return "the file is too large"
	default:
		return "the file cannot be read"
	}
}

// logNotifyPosture writes the boot's one line on whether the notify route admits a token.
func logNotifyPosture(cfg *Config) {
	if !cfg.NotifyGate.On() {
		slog.Info("notify route is off: " + notify.TokenEnv + " is unset, so POST /api/sessions/{session}/notify answers 401")
		return
	}
	slog.Info("notify route is on: " + notify.TokenEnv + " admits POST /api/sessions/{session}/notify and nothing else")
}
