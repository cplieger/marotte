package composition

import (
	"cmp"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/envx/v2"
	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/webhttp/v3"
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
	// KASNodePath is the node binary kiro-cli runs KAS on, resolved as kiro-cli resolves it:
	// KIRO_KAS_NODE_PATH, else the embedded copy in kiro-cli's data dir. It may not exist yet.
	KASNodePath string
	// ToolsDir is the tools engine's install tree root (bin/, opt/,
	// npm/, python/) on the persistent volume.
	ToolsDir string
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
	// TrustedProxies are the reverse-proxy networks whose X-Forwarded-For webhttp.ClientIP may
	// trust, from TRUSTED_PROXIES. Empty trusts nothing and logs the unspoofable socket peer.
	TrustedProxies []*net.IPNet
	// TrustedInstallUIDs names identities whose write access to the kiro-cli install tree does not
	// invalidate pinstall's custody, from TRUSTED_INSTALL_UIDS. Empty (the default) refuses any
	// tree another identity can write. Never compiled in: only the deployment knows which account
	// is already as privileged as this process.
	TrustedInstallUIDs []int
	// HostPolicy is the exact-match Host allowlist from ALLOWED_HOSTS, the anti-DNS-rebinding gate
	// applied before the CSRF check. Unset = inactive, any Host accepted (the server warns at
	// listen time).
	HostPolicy *webhttp.HostPolicy
	// BridgeEnvAllow re-permits names the bridge's credential screen would drop, from
	// bridge.EnvAllowVar. Nil is the shipped and right configuration.
	BridgeEnvAllow map[string]struct{}
	// BrowseRoots is the file browser's allow-list: WorkDir, ConfigDir, marotte.DefaultUploadDir,
	// plus MAROTTE_BROWSE_ROOTS grants (colon-separated absolute paths). Everything else is denied.
	BrowseRoots []string
	// ACPArgs are operator kiro-cli launch flags from MAROTTE_KIRO_ACP_ARGS, filtered by
	// bridge.ParseACPArgs and appended to every CHAT bridge's argv, never the utility bridge's. An
	// escape hatch for a flag upstream adds, not a capability switch.
	ACPArgs []string
	// ToolCatalogRefresh is the engine refresh cadence (toolbelt's 24h default); AuthConfig is
	// auth.DefaultConfig. Neither has an env override.
	ToolCatalogRefresh time.Duration
	AuthConfig         auth.Config
	// KiroAPIKeySet records whether bridge.KiroAPIKeyVar is present. The value
	// is never read into Config.
	KiroAPIKeySet bool
}

// ConfigFromEnv reads configuration from environment variables with
// sensible defaults.
func ConfigFromEnv() Config {
	configDir := absDir("KIRO_CONFIG_DIR", cmp.Or(envx.String("KIRO_CONFIG_DIR"), "/config"))
	workDir := absDir("KIRO_WORK_DIR", cmp.Or(envx.String("KIRO_WORK_DIR"), "/workspace"))
	return Config{
		WorkDir:             workDir,
		ConfigDir:           configDir,
		KiroCLIVersion:      envx.String("KIRO_CLI_VERSION"),
		KiroCLISHA256:       envx.String("KIRO_CLI_SHA256"),
		KiroCLISHA256ARM64:  envx.String("KIRO_CLI_SHA256_ARM64"),
		VapidSub:            cmp.Or(envx.String("VAPID_SUBJECT"), "mailto:marotte@noreply.invalid"),
		ToolsDir:            cmp.Or(envx.String("MAROTTE_TOOLS_DIR"), filepath.Join(configDir, "tools")),
		KASNodePath:         cmp.Or(envx.String("KIRO_KAS_NODE_PATH"), embeddedKASNode(os.Getenv("XDG_DATA_HOME"), os.Getenv("HOME"))),
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
		AuthConfig:          auth.DefaultConfig,
		KiroAPIKeySet:       os.Getenv(bridge.KiroAPIKeyVar) != "",
	}
}

// embeddedKASNode is where kiro-cli extracts the node it runs KAS on: its data dir is
// $XDG_DATA_HOME/kiro-cli, else $HOME/.local/share/kiro-cli. "" when neither is set.
func embeddedKASNode(dataHome, home string) string {
	if dataHome == "" {
		if home == "" {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "kiro-cli", "node")
}

// absDir makes a configured directory absolute against the startup cwd. The browse roots, the file
// API's root namespace and the client's config-file doors all treat it as an absolute identity, so a
// relative value would name one directory to the os calls and another to them. A failed Getwd
// leaves the value cleaned and relative, and validateConfig reports it.
func absDir(envVar, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		slog.Warn("config: cannot make the directory absolute", "env", envVar, "dir", dir, "error", err)
		return filepath.Clean(dir)
	}
	return abs
}

// logBridgeEnvPosture warns once at boot when an allowlisted API key reaches
// kiro-cli. Not allowlisted needs no line: the per-spawn drop warning names it.
func logBridgeEnvPosture(cfg *Config) {
	if !cfg.KiroAPIKeySet {
		return
	}
	if _, allowed := cfg.BridgeEnvAllow[bridge.KiroAPIKeyVar]; !allowed {
		return
	}
	slog.Warn(bridge.KiroAPIKeyVar + " reaches kiro-cli because " + bridge.EnvAllowVar +
		" allows it: kiro-cli authenticates with it ahead of the kiro-cli login, " +
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
func parseTrustedProxies(raw string) []*net.IPNet {
	nets, invalid := webhttp.ParseCIDRs(strings.Split(raw, ","))
	if len(invalid) > 0 {
		slog.Warn("config: ignoring malformed TRUSTED_PROXIES entries (want CIDR or IP)",
			"entries", invalid)
	}
	return nets
}

// Unset yields nil, fully enforcing. Each entry ASSERTS the uid is already as privileged as this
// process. Malformed entries drop with one warning that reports a count, never the values.
func parseTrustedInstallUIDs(raw string) []int {
	uids, rejected := pinstall.ParseIdentities(raw)
	if rejected > 0 {
		slog.Warn("config: ignoring unusable TRUSTED_INSTALL_UIDS entries (want whole numbers above 0)",
			"invalid_count", rejected,
			"hint", "list only numeric uids, comma-separated, each an account already at least as privileged as this server")
	}
	return uids
}

// parseAllowedHosts parses ALLOWED_HOSTS into a webhttp.HostPolicy, the exact-Host gate that closes
// the DNS-rebinding hole CSRF alone leaves open (CWE-346). The loopback carve-out is enabled and
// the 403 names ALLOWED_HOSTS; malformed entries are dropped. Unset or blank is an INACTIVE policy;
// an all-invalid list is an active EMPTY one that fails closed, warned by name.
func parseAllowedHosts(raw string) *webhttp.HostPolicy {
	policy, invalid := webhttp.ParseHostList(strings.Split(raw, ","),
		webhttp.WithLoopbackExempt(true),
		webhttp.WithHostAllowlistError("",
			"host not allowed. Add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		slog.Warn("config: dropping malformed ALLOWED_HOSTS entries; they cannot match any browser-sent Host",
			"entries", invalid,
			"hint", "use bare hostnames or IPs only (no scheme, path, or CIDR), e.g. localhost,192.168.1.5,marotte.example.com")
	}
	if policy.Active() && policy.Size() == 0 {
		slog.Warn("config: ALLOWED_HOSTS has no usable entries; rejecting every non-loopback request (fail closed)",
			"hint", "fix the entries listed in the preceding warning to restore browser access")
	}
	return policy
}
