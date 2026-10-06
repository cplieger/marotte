package bridge

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// envNames returns the names in a composed environment.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			name = kv
		}
		out = append(out, name)
	}
	return out
}

// A credential-shaped inherited name does not reach the spawn. One case per family: a family the rules miss is the
// mistake that matters.
func TestScreenBridgeEnv_DropsCredentialShapedNames(t *testing.T) {
	cases := map[string]struct{ name, value string }{
		"forge token":            {"GITHUB_TOKEN", "ghp_live"},
		"cloud key id":           {"AWS_ACCESS_KEY_ID", "AKIAEXAMPLE"},
		"cloud secret":           {"AWS_SECRET_ACCESS_KEY", "wJalr"},
		"package registry":       {"NPM_TOKEN", "npm_live"},
		"oauth client secret":    {"OIDC_CLIENT_SECRET", "s3cr3t"},
		"an unforeseen spelling": {"SOME_VENDOR_API_TOKEN", "tok"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env, dropped := screenBridgeEnv([]string{c.name + "=" + c.value}, nil, nil)
			if len(env) != 0 {
				t.Errorf("env = %v, want the credential dropped", env)
			}
			if !slices.Equal(dropped, []string{c.name}) {
				t.Errorf("dropped = %v, want [%s]", dropped, c.name)
			}
			// Assert on the value, so a mask-the-value shortcut fails.
			for _, kv := range env {
				if strings.Contains(kv, c.value) {
					t.Errorf("value survived in %q", kv)
				}
			}
		})
	}
}

// Each named credential spelling, one at a time, so narrowing a suffix rule to an exact list fails here.
func TestScreenBridgeEnv_DropsEveryNameTheDecisionNames(t *testing.T) {
	named := []string{
		"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "GITEA_SERVER_TOKEN", "NPM_TOKEN",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN",
		"KIRO_API_KEY",
	}
	for _, name := range named {
		t.Run(name, func(t *testing.T) {
			env, dropped := screenBridgeEnv([]string{name + "=x"}, nil, nil)
			if len(env) != 0 || len(dropped) != 1 {
				t.Errorf("%s: env = %v, dropped = %v, want dropped", name, env, dropped)
			}
		})
	}
}

// The agent's children are compilers, package managers and git; a screen taking an ordinary build variable gets
// switched off.
func TestScreenBridgeEnv_KeepsTheAmbientBuildEnvironment(t *testing.T) {
	inherited := []string{
		"PATH=/usr/bin", "HOME=/config/home", "TERM=xterm", "LANG=C.UTF-8", "TZ=UTC",
		"CGO_ENABLED=1", "GOFLAGS=-mod=mod", "GOMODCACHE=/config/go/pkg/mod",
		"CARGO_HOME=/config/cargo", "npm_config_registry=https://registry.npmjs.org",
		"AWS_REGION=eu-west-1", "AWS_PROFILE=default",
		// Names that only contain a credential word are kept.
		"TOKEN_BUCKET_SIZE=64", "SECRET_DIR=/run/secrets", "TOKENIZER=bpe",
		"AWS_DEFAULT_REGION=eu-west-1", "SSH_AUTH_SOCK=/run/ssh-agent",
	}
	env, dropped := screenBridgeEnv(inherited, nil, nil)
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want nothing", dropped)
	}
	if !slices.Equal(env, inherited) {
		t.Errorf("env = %v, want the inherited environment unchanged", env)
	}
}

// A name that is only the suffix reaches nobody.
func TestScreenBridgeEnv_BareSuffixIsNotACredential(t *testing.T) {
	for _, name := range []string{"_TOKEN", "_SECRET"} {
		env, dropped := screenBridgeEnv([]string{name + "=x"}, nil, nil)
		if len(dropped) != 0 || len(env) != 1 {
			t.Errorf("%s: env = %v, dropped = %v, want kept", name, env, dropped)
		}
	}
}

// The overlay is marotte's own and os/exec keeps a key's last value, so the screen never touches it, even for a
// name the inherited half would lose.
func TestScreenBridgeEnv_OverlayIsExemptAndStaysLast(t *testing.T) {
	env, dropped := screenBridgeEnv(
		[]string{"PATH=/usr/bin", "GITHUB_TOKEN=leak"},
		[]string{"PATH=/config/tools/kiro-cli-versions/2.18.1:/usr/bin", "GH_TOKEN=deliberate"},
		nil,
	)
	if !slices.Equal(dropped, []string{"GITHUB_TOKEN"}) {
		t.Errorf("dropped = %v, want only the inherited credential", dropped)
	}
	want := []string{
		"PATH=/usr/bin",
		"PATH=/config/tools/kiro-cli-versions/2.18.1:/usr/bin",
		"GH_TOKEN=deliberate",
	}
	if !slices.Equal(env, want) {
		t.Errorf("env = %v, want %v (overlay unfiltered and last)", env, want)
	}
}

// The overlay is appended with nothing inherited too.
func TestScreenBridgeEnv_OverlayLandsWithNothingInherited(t *testing.T) {
	overlay := []string{"PATH=/config/tools/kiro-cli-versions/2.18.1", "MAROTTE_HOME=/config"}
	env, dropped := screenBridgeEnv(nil, overlay, nil)
	if !slices.Equal(env, overlay) {
		t.Errorf("screenBridgeEnv(nil, %v, nil) env = %v, want %v", overlay, env, overlay)
	}
	if len(dropped) != 0 {
		t.Errorf("screenBridgeEnv(nil, %v, nil) dropped = %v, want nothing", overlay, dropped)
	}
}

// The override keeps a false positive from being a reason to disable the screen.
func TestScreenBridgeEnv_OperatorOverridePassesTheNameThrough(t *testing.T) {
	inherited := []string{"BUILDKITE_AGENT_TOKEN=needed", "GITHUB_TOKEN=leak"}
	env, dropped := screenBridgeEnv(inherited, nil, ParseEnvAllowlist("BUILDKITE_AGENT_TOKEN"))
	if !slices.Equal(envNames(env), []string{"BUILDKITE_AGENT_TOKEN"}) {
		t.Errorf("env = %v, want only the allowed name", env)
	}
	if !slices.Equal(dropped, []string{"GITHUB_TOKEN"}) {
		t.Errorf("dropped = %v, want the unallowed credential", dropped)
	}
}

// An exact-name credential yields to the allowlist: a headless API-key deployment opts in by naming it.
func TestScreenBridgeEnv_AllowlistedExactNamePassesThrough(t *testing.T) {
	env, dropped := screenBridgeEnv([]string{KiroAPIKeyVar + "=k"}, nil, ParseEnvAllowlist(KiroAPIKeyVar))
	if !slices.Equal(env, []string{KiroAPIKeyVar + "=k"}) || len(dropped) != 0 {
		t.Errorf("screenBridgeEnv(allowlisted %s) env = %v, dropped = %v, want it kept", KiroAPIKeyVar, env, dropped)
	}
}

func TestParseEnvAllowlist(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"empty":              {"", nil},
		"blank":              {"   ", nil},
		"all separators":     {" , , ", nil},
		"single":             {"GH_TOKEN", []string{"GH_TOKEN"}},
		"several with space": {"GH_TOKEN, NPM_TOKEN ,X_SECRET", []string{"GH_TOKEN", "NPM_TOKEN", "X_SECRET"}},
		"trailing separator": {"GH_TOKEN,", []string{"GH_TOKEN"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := ParseEnvAllowlist(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("ParseEnvAllowlist(%q) has %d entries, want %d", c.raw, len(got), len(c.want))
			}
			for _, w := range c.want {
				if _, ok := got[w]; !ok {
					t.Errorf("ParseEnvAllowlist(%q) missing %q", c.raw, w)
				}
			}
		})
	}
}

// An entry with no `=` must not make the screen index past the string.
func TestScreenBridgeEnv_MalformedEntryIsKeptWhole(t *testing.T) {
	env, dropped := screenBridgeEnv([]string{"NOTANASSIGNMENT", "ALSO_A_TOKEN"}, nil, nil)
	if !slices.Equal(env, []string{"NOTANASSIGNMENT"}) {
		t.Errorf("env = %v, want the non-assignment kept", env)
	}
	if !slices.Equal(dropped, []string{"ALSO_A_TOKEN"}) {
		t.Errorf("dropped = %v, want the credential-shaped bare name", dropped)
	}
}

// FuzzScreenBridgeEnv pins on arbitrary input that no credential-shaped name survives and nothing else is lost.
func FuzzScreenBridgeEnv(f *testing.F) {
	f.Add("PATH=/usr/bin\nGITHUB_TOKEN=ghp\nHOME=/config/home")
	f.Add("AWS_SECRET_ACCESS_KEY=x\nAWS_REGION=eu-west-1")
	f.Add("=leading\n_TOKEN=bare\nA_SECRET=y")
	f.Add("no-equals\nGOFLAGS=-mod=mod")
	f.Fuzz(func(t *testing.T, raw string) {
		inherited := strings.Split(raw, "\n")
		env, dropped := screenBridgeEnv(inherited, nil, nil)
		if len(env)+len(dropped) != len(inherited) {
			t.Fatalf("accounting: %d kept + %d dropped != %d inherited", len(env), len(dropped), len(inherited))
		}
		for _, kv := range env {
			name, _, ok := strings.Cut(kv, "=")
			if !ok {
				name = kv
			}
			if isCredentialEnv(name, nil) {
				t.Errorf("credential-shaped name %q survived", name)
			}
		}
		for _, name := range dropped {
			if !isCredentialEnv(name, nil) {
				t.Errorf("dropped %q, which is not credential-shaped", name)
			}
		}
	})
}

// envDumpFake writes a fake kiro-cli that records its spawn environment before the handshake.
func envDumpFake(t *testing.T, dir, dumpPath string) string {
	t.Helper()
	script := `#!/bin/sh
env > "` + dumpPath + `"
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-env"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "env-dump-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return scriptPath
}

// The screen holds through a real spawn and the dropped names are logged, the operator's only notice. Not parallel:
// it sets an environment variable and swaps the slog default.
func TestStart_ScreensCredentialsOutOfTheSpawnAndNamesThem(t *testing.T) {
	const probe = "MAROTTE_SPAWN_PROBE_TOKEN"
	t.Setenv(probe, "shh")

	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "child.env")
	scriptPath := envDumpFake(t, dir, dumpPath)

	logs := captureLogs(t)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read the child environment dump: %v", err)
	}
	if strings.Contains(string(dump), probe) {
		t.Errorf("%s reached the kiro-cli environment; the spawn does not apply the credential screen", probe)
	}
	if !strings.Contains(logs.String(), probe) {
		t.Errorf("the spawn dropped %s without naming it in the log:\n%s", probe, logs.String())
	}
}

// The session id joins a reap line to its chat. Not parallel: it swaps the slog default.
func TestStop_ReapLineNamesTheSession(t *testing.T) {
	dir := t.TempDir()
	scriptPath := envDumpFake(t, dir, filepath.Join(dir, "child.env"))

	logs := captureLogs(t)

	b := New(scriptPath, dir)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()

	var reap string
	for line := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(line, `msg="kiro-cli reaped"`) || strings.Contains(line, `msg="kiro-cli ended on its own"`) {
			reap = line
		}
	}
	if reap == "" {
		t.Fatalf("Stop logged no reap line:\n%s", logs.String())
	}
	if !strings.Contains(reap, "session_id=sess-env ") {
		t.Errorf("reap line = %q, want session_id=sess-env (the session the handshake returned)", reap)
	}
}

// spawnEnvDump starts a bridge against the env-dumping fake and returns the child environment's lines.
func spawnEnvDump(t *testing.T, opts *marotte.StartOpts) []string {
	t.Helper()
	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "child.env")
	scriptPath := envDumpFake(t, dir, dumpPath)
	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	opts.Lifetime = context.Background()
	if err := b.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read the child environment dump: %v", err)
	}
	return strings.Split(string(dump), "\n")
}

// The session door's memory mode is the one memory switch, so a spawn leaves the memory arm to AWS's ramp.
func TestStart_ChildEnvironmentPinsNoMemoryArm(t *testing.T) {
	for _, line := range spawnEnvDump(t, &marotte.StartOpts{Memory: marotte.MemoryPreference{Mode: "disabled"}}) {
		if strings.HasPrefix(line, "KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED=") {
			t.Errorf("child environment carries %q; the memory arm must follow the ramp", line)
		}
	}
}

// A run bridge turns KAS's LLM session title off; a chat bridge leaves it alone.
func TestStart_SessionTitleSwitchOnlyWhenAsked(t *testing.T) {
	const want = "KIRO_DISABLE_SESSION_TITLE_LLM=true"
	if lines := spawnEnvDump(t, &marotte.StartOpts{DisableSessionTitles: true}); !slices.Contains(lines, want) {
		t.Errorf("run-bridge child environment is missing %q", want)
	}
	if lines := spawnEnvDump(t, &marotte.StartOpts{}); slices.Contains(lines, want) {
		t.Errorf("chat-bridge child environment carries %q; titles name History rows there", want)
	}
}

func TestStart_ChildEnvironmentCarriesTheToolLoadLever(t *testing.T) {
	const toolLoadEnvVar = "KIRO_FEATURE_TOOL_LOAD_ENABLED"
	t.Setenv(toolLoadEnvVar, "ambient-should-lose")

	for _, on := range []bool{false, true} {
		name := "tool load off"
		if on {
			name = "tool load on"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dumpPath := filepath.Join(dir, "child.env")
			scriptPath := envDumpFake(t, dir, dumpPath)

			b := New(scriptPath, dir)
			t.Cleanup(b.Stop)
			if err := b.Start(context.Background(), &marotte.StartOpts{
				Lifetime:   context.Background(),
				ToolSearch: on,
			}); err != nil {
				t.Fatalf("Start: %v", err)
			}

			dump, err := os.ReadFile(dumpPath)
			if err != nil {
				t.Fatalf("read the child environment dump: %v", err)
			}
			lines := strings.Split(string(dump), "\n")
			want := toolLoadEnvVar + "=" + strconv.FormatBool(on)
			if !slices.Contains(lines, want) {
				t.Errorf("child environment is missing %q with ToolSearch=%t:\n%s", want, on, dump)
			}
			if slices.Contains(lines, toolLoadEnvVar+"=ambient-should-lose") {
				t.Errorf("the inherited %s survived with ToolSearch=%t; the lever must be appended after the screen",
					toolLoadEnvVar, on)
			}
		})
	}
}

// The parent pins the child locale over the inherited value. The screen passes LANG through, and os/exec keeps the
// last assignment, so a locale appended before it is a no-op; the ambient value is set for that reason.
func TestStart_ChildEnvironmentPinsTheLocale(t *testing.T) {
	t.Setenv(localeEnvVar, "ambient-should-lose")

	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "child.env")
	scriptPath := envDumpFake(t, dir, dumpPath)

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)
	if err := b.Start(context.Background(), &marotte.StartOpts{Lifetime: context.Background()}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read the child environment dump: %v", err)
	}
	lines := strings.Split(string(dump), "\n")
	// Composed through the production function so the expected value cannot go stale.
	if want := localeEnv()[0]; !slices.Contains(lines, want) {
		t.Errorf("child environment is missing %q; every spawned tool then chooses its own output encoding:\n%s",
			want, dump)
	}
	if slices.Contains(lines, localeEnvVar+"=ambient-should-lose") {
		t.Errorf("the inherited %s survived; the pin must be appended AFTER the screen so it wins",
			localeEnvVar)
	}
}
