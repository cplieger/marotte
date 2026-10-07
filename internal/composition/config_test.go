package composition

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/webhttp/v3"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("KIRO_CLI_VERSION", "")
	cfg := ConfigFromEnv()

	if cfg.WorkDir == "" {
		t.Error("WorkDir is empty, want default")
	}
	if cfg.ConfigDir == "" {
		t.Error("ConfigDir is empty, want default")
	}
	if cfg.KiroCLIVersion != "" {
		t.Errorf("KiroCLIVersion = %q, want empty outside the container", cfg.KiroCLIVersion)
	}
	if cfg.AuthConfig.LoginURLTimeout <= 0 {
		t.Error("LoginURLTimeout is zero/negative, want positive default")
	}
	if cfg.AuthConfig.LoginTimeout <= 0 {
		t.Error("LoginTimeout is zero/negative, want positive default")
	}
	if cfg.AuthConfig.LogoutTimeout <= 0 {
		t.Error("LogoutTimeout is zero/negative, want positive default")
	}
	if cfg.AuthConfig.WhoamiTimeout <= 0 {
		t.Error("WhoamiTimeout is zero/negative, want positive default")
	}
}

func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("KIRO_WORK_DIR", "/custom/work")
	t.Setenv("KIRO_CONFIG_DIR", "/custom/config")
	t.Setenv("KIRO_CLI_VERSION", "9.9.9")
	t.Setenv("KIRO_CLI_SHA256", "abc")
	t.Setenv("KIRO_CLI_SHA256_ARM64", "def")

	cfg := ConfigFromEnv()

	if cfg.WorkDir != "/custom/work" {
		t.Errorf("WorkDir = %q, want /custom/work", cfg.WorkDir)
	}
	if cfg.ConfigDir != "/custom/config" {
		t.Errorf("ConfigDir = %q, want /custom/config", cfg.ConfigDir)
	}
	if cfg.KiroCLIVersion != "9.9.9" || cfg.KiroCLISHA256 != "abc" || cfg.KiroCLISHA256ARM64 != "def" {
		t.Errorf("pins = %q/%q/%q, want the three exported literals verbatim",
			cfg.KiroCLIVersion, cfg.KiroCLISHA256, cfg.KiroCLISHA256ARM64)
	}
}

func TestConfigFromEnv_RelativeDirsBecomeAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Setup: Getwd: %v", err)
	}
	t.Setenv("KIRO_CONFIG_DIR", "data/")
	t.Setenv("KIRO_WORK_DIR", "./work")

	cfg := ConfigFromEnv()

	wantConfig, wantWork := filepath.Join(cwd, "data"), filepath.Join(cwd, "work")
	if cfg.ConfigDir != wantConfig {
		t.Errorf("ConfigFromEnv() ConfigDir = %q, want %q", cfg.ConfigDir, wantConfig)
	}
	if cfg.WorkDir != wantWork {
		t.Errorf("ConfigFromEnv() WorkDir = %q, want %q", cfg.WorkDir, wantWork)
	}
	if want := filepath.Join(wantConfig, "tools"); cfg.ToolsDir != want {
		t.Errorf("ConfigFromEnv() ToolsDir = %q, want %q", cfg.ToolsDir, want)
	}
	if !slices.Contains(cfg.BrowseRoots, wantConfig) || !slices.Contains(cfg.BrowseRoots, wantWork) {
		t.Errorf("ConfigFromEnv() BrowseRoots = %q, want both %q and %q", cfg.BrowseRoots, wantConfig, wantWork)
	}
}

// TestConfigFromEnv_RemovedOverridesAreIgnored pins that the catalog refresh and
// sign-in timeout variables are inert: set, they change nothing.
func TestConfigFromEnv_RemovedOverridesAreIgnored(t *testing.T) {
	t.Setenv("MAROTTE_TOOL_CATALOG_REFRESH", "off")
	for _, name := range []string{
		"MAROTTE_AUTH_LOGIN_URL_TIMEOUT", "MAROTTE_AUTH_LOGIN_TIMEOUT",
		"MAROTTE_AUTH_LOGOUT_TIMEOUT", "MAROTTE_AUTH_WHOAMI_TIMEOUT",
	} {
		t.Setenv(name, "1s")
	}

	cfg := ConfigFromEnv()

	if cfg.ToolCatalogRefresh != toolbelt.DefaultCatalogRefresh {
		t.Errorf("ToolCatalogRefresh = %v, want toolbelt's default %v", cfg.ToolCatalogRefresh, toolbelt.DefaultCatalogRefresh)
	}
	if cfg.AuthConfig != auth.DefaultConfig {
		t.Errorf("AuthConfig = %+v, want auth.DefaultConfig %+v", cfg.AuthConfig, auth.DefaultConfig)
	}
}

// containsIP reports whether any network in nets contains ipStr.
func containsIP(nets []*net.IPNet, ipStr string) bool {
	ip := net.ParseIP(ipStr)
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// TestParseAllowedHosts pins the ALLOWED_HOSTS parser: unset or blank is an INACTIVE policy, valid
// entries engage the gate.
func TestParseAllowedHosts(t *testing.T) {
	allows := func(t *testing.T, policy *webhttp.HostPolicy, host, remoteAddr string) bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/x", nil)
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		return policy.Allows(req)
	}

	t.Run("empty yields an inactive policy (any Host accepted)", func(t *testing.T) {
		policy := parseAllowedHosts("")
		if policy.Active() {
			t.Error("parseAllowedHosts(\"\") is active; want the permissive backward-compatible default")
		}
		if !allows(t, policy, "anything.example:8080", "") {
			t.Error("inactive policy rejected a request")
		}
	})

	t.Run("valid list gates exactly, with the loopback carve-out", func(t *testing.T) {
		policy := parseAllowedHosts("localhost, Marotte.Example.COM.")
		if !policy.Active() || policy.Size() != 2 {
			t.Fatalf("policy active=%v size=%d, want active with 2 entries", policy.Active(), policy.Size())
		}
		for _, c := range []struct {
			host, peer string
			want       bool
		}{
			{"MAROTTE.example.com:8080", "192.168.1.50:44444", true},
			{"attacker.evil:8080", "192.168.1.50:44444", false},
			{"127.0.0.1:8080", "127.0.0.1:54321", true},
			{"127.0.0.1:8080", "192.168.1.50:44444", false},
		} {
			if got := allows(t, policy, c.host, c.peer); got != c.want {
				t.Errorf("Allows(Host %q, peer %s) = %v, want %v", c.host, c.peer, got, c.want)
			}
		}
	})

	t.Run("malformed entry dropped, valid subset kept", func(t *testing.T) {
		policy := parseAllowedHosts("http://marotte.example.com, localhost")
		if got := policy.Size(); got != 1 {
			t.Fatalf("policy size = %d, want 1 (the URL-shaped entry dropped, the valid one kept)", got)
		}
		if !allows(t, policy, "localhost:8080", "192.168.1.50:44444") {
			t.Error("valid entry localhost missing from the allowlist")
		}
	})

	t.Run("all-invalid fails closed (active empty)", func(t *testing.T) {
		policy := parseAllowedHosts(":8080")
		if !policy.Active() || policy.Size() != 0 {
			t.Fatalf("policy active=%v size=%d, want an active empty policy (fail closed, never fall open)", policy.Active(), policy.Size())
		}
		if allows(t, policy, "marotte.example.com:8080", "192.168.1.50:44444") {
			t.Error("non-loopback request admitted by an active empty policy; all-invalid configuration must deny-all")
		}
	})
}

// TestParseTrustedProxies pins the CIDR/IP list parser: an empty or
// whitespace-only value yields nil (the unconfigured, spoof-safe
// socket-peer default), valid CIDRs and bare IPs parse to matching
// networks, whitespace and empty entries are skipped, and a malformed
// entry is dropped (fail-safe) rather than aborting the parse.
func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty yields nil (unconfigured default)", func(t *testing.T) {
		if got := parseTrustedProxies(""); got != nil {
			t.Errorf("parseTrustedProxies(%q) = %v, want nil", "", got)
		}
	})
	t.Run("whitespace-only yields nil", func(t *testing.T) {
		if got := parseTrustedProxies("   "); got != nil {
			t.Errorf("parseTrustedProxies(spaces) = %v, want nil", got)
		}
	})

	tests := []struct {
		name        string
		raw         string
		wantLen     int
		contains    []string
		notContains []string
	}{
		{
			name:        "single CIDR",
			raw:         "10.0.0.0/8",
			wantLen:     1,
			contains:    []string{"10.1.2.3"},
			notContains: []string{"192.168.1.1"},
		},
		{
			name:        "multiple CIDRs",
			raw:         "10.0.0.0/8,192.168.0.0/16",
			wantLen:     2,
			contains:    []string{"10.1.2.3", "192.168.1.1"},
			notContains: []string{"172.16.0.1"},
		},
		{
			name:        "bare IPv4 becomes a single host",
			raw:         "203.0.113.7",
			wantLen:     1,
			contains:    []string{"203.0.113.7"},
			notContains: []string{"203.0.113.8"},
		},
		{
			name:        "bare IPv6 becomes a single host",
			raw:         "2001:db8::1",
			wantLen:     1,
			contains:    []string{"2001:db8::1"},
			notContains: []string{"2001:db8::2"},
		},
		{
			name:     "whitespace around entries is trimmed",
			raw:      "  10.0.0.0/8 , 192.168.0.0/16 ",
			wantLen:  2,
			contains: []string{"10.9.9.9", "192.168.9.9"},
		},
		{
			name:     "malformed entries are skipped, valid ones kept",
			raw:      "10.0.0.0/8, not-an-ip, 192.168.0.0/16",
			wantLen:  2,
			contains: []string{"10.1.1.1", "192.168.1.1"},
		},
		{
			name:     "empty entries are skipped",
			raw:      "10.0.0.0/8,,,",
			wantLen:  1,
			contains: []string{"10.0.0.1"},
		},
		{
			name:    "all-malformed yields empty",
			raw:     "nonsense, also-bad",
			wantLen: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTrustedProxies(tc.raw)
			if len(got) != tc.wantLen {
				t.Fatalf("parseTrustedProxies(%q) len = %d, want %d (got %v)",
					tc.raw, len(got), tc.wantLen, got)
			}
			for _, ip := range tc.contains {
				if !containsIP(got, ip) {
					t.Errorf("parseTrustedProxies(%q) should contain %s", tc.raw, ip)
				}
			}
			for _, ip := range tc.notContains {
				if containsIP(got, ip) {
					t.Errorf("parseTrustedProxies(%q) should NOT contain %s", tc.raw, ip)
				}
			}
		})
	}
}

// TestConfigFromEnv_TrustedProxies pins that TRUSTED_PROXIES flows from
// the environment into Config: unset leaves the spoof-safe nil default,
// and a comma-separated value parses to the network set.
func TestConfigFromEnv_TrustedProxies(t *testing.T) {
	t.Run("unset yields nil (socket-peer default)", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXIES", "")
		if cfg := ConfigFromEnv(); cfg.TrustedProxies != nil {
			t.Errorf("TrustedProxies = %v, want nil when unset", cfg.TrustedProxies)
		}
	})
	t.Run("set parses to network set", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 192.168.0.0/16")
		cfg := ConfigFromEnv()
		if len(cfg.TrustedProxies) != 2 {
			t.Fatalf("len(TrustedProxies) = %d, want 2", len(cfg.TrustedProxies))
		}
		if !containsIP(cfg.TrustedProxies, "10.1.2.3") {
			t.Error("TrustedProxies should contain 10.1.2.3")
		}
	})
}

// TestTrustedProxies_ClientIPResolution asserts that the parsed set, fed to webhttp.ClientIP as server.go does,
// resolves the real client only from a trusted peer.
func TestTrustedProxies_ClientIPResolution(t *testing.T) {
	const (
		peer      = "198.51.100.4"
		xffClient = "203.0.113.9"
	)
	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
		r.RemoteAddr = peer + ":5000"
		r.Header.Set("X-Forwarded-For", xffClient)
		return r
	}

	t.Run("unconfigured: XFF ignored, socket peer wins", func(t *testing.T) {
		trusted := parseTrustedProxies("")
		if got := webhttp.ClientIP(newReq(), trusted...); got != peer {
			t.Errorf("ClientIP unconfigured = %q, want socket peer %q", got, peer)
		}
	})
	t.Run("configured, peer trusted: XFF client wins", func(t *testing.T) {
		trusted := parseTrustedProxies("198.51.100.0/24")
		if got := webhttp.ClientIP(newReq(), trusted...); got != xffClient {
			t.Errorf("ClientIP trusted peer = %q, want XFF client %q", got, xffClient)
		}
	})
	t.Run("configured, peer NOT trusted: XFF ignored", func(t *testing.T) {
		trusted := parseTrustedProxies("10.0.0.0/8")
		if got := webhttp.ClientIP(newReq(), trusted...); got != peer {
			t.Errorf("ClientIP untrusted peer = %q, want socket peer %q", got, peer)
		}
	})
}

// TestBundledToolsFiles pins how a catalog-overlay path that fails to resolve is reported, default
// and explicit alike.
func TestBundledToolsFiles(t *testing.T) {
	t.Run("a resolvable explicit path is used", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bundled-tools.json")
		if err := os.WriteFile(path, []byte(`{"entries":{}}`), 0o600); err != nil {
			t.Fatalf("Setup: write bundled tools: %v", err)
		}
		logs := captureDefaultLogger(t)

		got := bundledToolsFiles(path)
		if len(got) != 1 || got[0] != path {
			t.Errorf("bundledToolsFiles(%q) = %v, want [%s]", path, got, path)
		}
		if strings.Contains(logs.String(), "bundled tools file does not resolve") {
			t.Errorf("logs = %q, must not warn about a path that resolved", logs.String())
		}
	})

	t.Run("an explicit path that does not resolve is dropped and named", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.json")
		logs := captureDefaultLogger(t)

		if got := bundledToolsFiles(path); got != nil {
			t.Errorf("bundledToolsFiles(%q) = %v, want nil", path, got)
		}
		if !strings.Contains(logs.String(), "MAROTTE_BUNDLED_TOOLS") {
			t.Errorf("logs = %q, want a warning naming the variable the operator set", logs.String())
		}
		if !strings.Contains(logs.String(), `"explicit":true`) {
			t.Errorf("logs = %q, want \"explicit\":true so a typo is distinguishable from the "+
				"out-of-container case", logs.String())
		}
	})

	t.Run("a missing default warns too, marked not explicit", func(t *testing.T) {
		absent := filepath.Join(t.TempDir(), "bundled-tools.json")
		restore := defaultBundledTools
		defaultBundledTools = absent
		t.Cleanup(func() { defaultBundledTools = restore })

		logs := captureDefaultLogger(t)

		if got := bundledToolsFiles(""); got != nil {
			t.Errorf("bundledToolsFiles(\"\") = %v, want nil outside the image", got)
		}
		if !strings.Contains(logs.String(), "bundled tools file does not resolve") {
			t.Errorf("logs = %q, want a warning: the seeded language servers live only in "+
				"that file, so its absence is not a silent condition", logs.String())
		}
		if !strings.Contains(logs.String(), `"explicit":false`) {
			t.Errorf("logs = %q, want \"explicit\":false so nobody reads it as an operator mistake",
				logs.String())
		}
	})
}

// TestBrowseRoots pins the lenient parser: the standard mounts always survive, and only a real
// malformed entry is reported.
func TestBrowseRoots(t *testing.T) {
	t.Run("several valid extras all survive alongside the standard mounts", func(t *testing.T) {
		logs := captureDefaultLogger(t)

		got := browseRoots("/work", "/config", "/srv/a:/srv/b:/srv/c")
		want := []string{"/work", "/config", marotte.DefaultUploadDir, "/srv/a", "/srv/b", "/srv/c"}
		if !slices.Equal(got, want) {
			t.Errorf("browseRoots() = %v, want %v", got, want)
		}
		if strings.Contains(logs.String(), "MAROTTE_BROWSE_ROOTS") {
			t.Errorf("logs = %q, must stay silent for a list with nothing malformed in it", logs.String())
		}
	})

	t.Run("a malformed entry is dropped, named, and does not take the mounts down", func(t *testing.T) {
		logs := captureDefaultLogger(t)

		got := browseRoots("/work", "/config", "relative/path:/srv/ok")
		want := []string{"/work", "/config", marotte.DefaultUploadDir, "/srv/ok"}
		if !slices.Equal(got, want) {
			t.Errorf("browseRoots() = %v, want %v", got, want)
		}
		if !strings.Contains(logs.String(), "MAROTTE_BROWSE_ROOTS") {
			t.Errorf("logs = %q, want a warning naming the dropped entry", logs.String())
		}
	})
}

// TestParseTrustedProxies_ReportsOnlyRealRejections pins the report half of the lenient parser.
func TestParseTrustedProxies_ReportsOnlyRealRejections(t *testing.T) {
	t.Run("a well-formed list is parsed silently", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		if got := parseTrustedProxies("10.0.0.0/8, 192.0.2.10"); len(got) != 2 {
			t.Errorf("parseTrustedProxies() = %v, want 2 networks", got)
		}
		if strings.Contains(logs.String(), "TRUSTED_PROXIES") {
			t.Errorf("logs = %q, must stay silent for a list with nothing malformed in it", logs.String())
		}
	})

	t.Run("a malformed entry is named and the valid subset survives", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		if got := parseTrustedProxies("10.0.0.0/8, not-an-ip"); len(got) != 1 {
			t.Errorf("parseTrustedProxies() = %v, want the one valid network", got)
		}
		if !strings.Contains(logs.String(), "TRUSTED_PROXIES") {
			t.Errorf("logs = %q, want a warning naming the dropped entry", logs.String())
		}
	})
}

// TestParseTrustedInstallUIDs pins the one report marotte emits about a list whose entries assert
// privilege.
func TestParseTrustedInstallUIDs(t *testing.T) {
	t.Run("a well-formed list is parsed silently", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		if got := parseTrustedInstallUIDs("1000,1001"); !slices.Equal(got, []int{1000, 1001}) {
			t.Errorf("parseTrustedInstallUIDs() = %v, want [1000 1001]", got)
		}
		if strings.Contains(logs.String(), "TRUSTED_INSTALL_UIDS") {
			t.Errorf("logs = %q, must stay silent for a list with nothing unusable in it", logs.String())
		}
	})

	t.Run("an unusable entry is reported by count, never by value", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		if got := parseTrustedInstallUIDs("1000,hunter2"); !slices.Equal(got, []int{1000}) {
			t.Errorf("parseTrustedInstallUIDs() = %v, want [1000]", got)
		}
		out := logs.String()
		if !strings.Contains(out, "TRUSTED_INSTALL_UIDS") {
			t.Errorf("logs = %q, want a warning naming the variable", out)
		}
		if strings.Contains(out, "hunter2") {
			t.Errorf("logs = %q, must not echo the refused text: any key can carry a secret", out)
		}
	})
}

// TestParseAllowedHosts_WarnsOnlyWhenBrowserAccessIsAtRisk pins both warnings this parser owns, and
// their silence.
func TestParseAllowedHosts_WarnsOnlyWhenBrowserAccessIsAtRisk(t *testing.T) {
	const failClosed = "rejecting every non-loopback request"

	t.Run("a usable list is parsed silently", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		policy := parseAllowedHosts("localhost, marotte.example.com")
		if !policy.Active() || policy.Size() != 2 {
			t.Fatalf("policy active = %v, size = %d, want active with 2 entries", policy.Active(), policy.Size())
		}
		if out := logs.String(); strings.Contains(out, "ALLOWED_HOSTS") {
			t.Errorf("logs = %q, must stay silent for a list that works", out)
		}
	})

	t.Run("a dropped entry is named while the valid subset still serves", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		policy := parseAllowedHosts("http://marotte.example.com, localhost")
		if !policy.Active() || policy.Size() != 1 {
			t.Fatalf("policy active = %v, size = %d, want active with 1 entry", policy.Active(), policy.Size())
		}
		out := logs.String()
		if !strings.Contains(out, "ALLOWED_HOSTS") {
			t.Errorf("logs = %q, want a warning naming the dropped entry", out)
		}
		if strings.Contains(out, failClosed) {
			t.Errorf("logs = %q, must not claim a fail-closed policy while an entry still matches", out)
		}
	})

	t.Run("an all-invalid list says every request is now refused", func(t *testing.T) {
		logs := captureDefaultLogger(t)
		policy := parseAllowedHosts(":8080")
		if !policy.Active() || policy.Size() != 0 {
			t.Fatalf("policy active = %v, size = %d, want active and empty", policy.Active(), policy.Size())
		}
		if out := logs.String(); !strings.Contains(out, failClosed) {
			t.Errorf("logs = %q, want the fail-closed warning: every browser request now 403s", out)
		}
	})
}

// The key's presence is the only fact the boot report needs, so the value never
// enters Config: a Config is logged, diffed and handed around freely.
func TestConfigFromEnv_RecordsKiroAPIKeyPresenceNotValue(t *testing.T) {
	const secret = "kiro-api-key-test-value"
	t.Setenv("KIRO_API_KEY", secret)
	cfg := ConfigFromEnv()
	if !cfg.KiroAPIKeySet {
		t.Error("KiroAPIKeySet = false with KIRO_API_KEY set, want true")
	}
	if dump := fmt.Sprintf("%+v", cfg); strings.Contains(dump, secret) {
		t.Errorf("Config carries the API key value: %s", dump)
	}

	t.Setenv("KIRO_API_KEY", "")
	if ConfigFromEnv().KiroAPIKeySet {
		t.Error("KiroAPIKeySet = true with KIRO_API_KEY empty, want false")
	}
}

// An allowlisted API key replaces the kiro-cli login as the agent's identity with
// nothing on screen saying so, which is the one case that earns a boot line. Not
// allowlisted is already named by the per-spawn drop warning, so it stays silent.
//
// Not parallel: it swaps the slog default.
func TestLogBridgeEnvPosture(t *testing.T) {
	const identityMsg = "KIRO_API_KEY reaches kiro-cli"
	cases := []struct {
		name     string
		keySet   bool
		allow    string
		wantWarn bool
	}{
		{name: "key unset", allow: "KIRO_API_KEY"},
		{name: "key set, not allowlisted", keySet: true, allow: "GH_TOKEN"},
		{name: "key set and allowlisted", keySet: true, allow: "GH_TOKEN,KIRO_API_KEY", wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureDefaultLogger(t)
			logBridgeEnvPosture(&Config{
				KiroAPIKeySet:  tc.keySet,
				BridgeEnvAllow: bridge.ParseEnvAllowlist(tc.allow),
			})
			if got := strings.Contains(logs.String(), identityMsg); got != tc.wantWarn {
				t.Errorf("logBridgeEnvPosture(keySet=%t, allow=%q) warned = %t, want %t; logs:\n%s",
					tc.keySet, tc.allow, got, tc.wantWarn, logs.String())
			}
		})
	}
}
