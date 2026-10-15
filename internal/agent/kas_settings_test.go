package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
)

func withKiroTelemetry(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	// No resolver is installed under test, so KiroHome reads $HOME live.
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".kiro", "settings", "cli.json")
	if body != "" {
		writeKiroCLISettings(t, path, body)
	}
	return path
}

func writeKiroCLISettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", path, err)
	}
}

func orgTelemetry(on bool) marotte.GovernanceStatePayload {
	p := *enterpriseProfile()
	p.Features.UsageAnalytics = on
	return p
}

func TestSpawnSites_CarryTelemetryOff(t *testing.T) {
	for _, site := range spawnSites() {
		t.Run(site.name, func(t *testing.T) {
			withKiroTelemetry(t, `{"telemetry.enabled":false}`)
			br := newFakeBridge()
			h, cs := newContentCollectionHub(t, func() ACPBridge { return br })
			site.spawn(t, h, cs, br)

			if opts := br.lastStartOpts(); opts == nil || !opts.DisableTelemetry {
				t.Errorf("%s spawn with telemetry.enabled false: DisableTelemetry = false, want true", site.name)
			}
		})
	}
}

func TestChatSpawn_ResolvesTelemetryUnderTheOrganizationLock(t *testing.T) {
	for _, tc := range []struct {
		org     *marotte.GovernanceStatePayload
		name    string
		cliJSON string
		want    bool
	}{
		{name: "unset reads on, kiro-cli's own default"},
		{name: "a cli.json without the key reads on", cliJSON: `{"chat.enableKnowledge":true}`},
		{name: "switch on", cliJSON: `{"telemetry.enabled":true}`},
		{name: "a stored off is kept", cliJSON: `{"telemetry.enabled":false}`, want: true},
		{name: "organization off overrides the switch", cliJSON: `{"telemetry.enabled":true}`, org: new(orgTelemetry(false)), want: true},
		{name: "organization on overrides the switch", cliJSON: `{"telemetry.enabled":false}`, org: new(orgTelemetry(true))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withKiroTelemetry(t, tc.cliJSON)
			h, _ := reopenFixture(t)
			if tc.org != nil {
				h.config.SetGovernance(t.Context(), *tc.org)
			}
			sb, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			if got := fakeOf(sb).lastStartOpts().DisableTelemetry; got != tc.want {
				t.Errorf("chat spawn under cli.json %q, org lock set %v: DisableTelemetry = %v, want %v", tc.cliJSON, tc.org != nil, got, tc.want)
			}
		})
	}
}

// From the switch and the organization lock to what the real kiro-cli process receives: the toolkit variable in
// its environment and telemetryEnabled on the initialize it reads. Not parallel: it sets environment variables.
func TestChatSpawn_TelemetryReachesTheEngineProcess(t *testing.T) {
	const toolkitVar = "KIRO_DISABLE_TOOLKIT_TELEMETRY"
	for _, tc := range []struct {
		org     *marotte.GovernanceStatePayload
		name    string
		cliJSON string
		wantEnv []string
		wantOn  bool
	}{
		{name: "unset reads on", wantOn: true},
		{name: "switch on", cliJSON: `{"telemetry.enabled":true}`, wantOn: true},
		{name: "switch off", cliJSON: `{"telemetry.enabled":false}`, wantEnv: []string{toolkitVar + "=1"}},
		{name: "organization off with the switch on", cliJSON: `{"telemetry.enabled":true}`, org: new(orgTelemetry(false)), wantEnv: []string{toolkitVar + "=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withKiroTelemetry(t, tc.cliJSON)
			t.Setenv(toolkitVar, "ambient")
			engine := newEngineProcessFake(t)
			h, cs := newContentCollectionHub(t, engine.factory)
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			if tc.org != nil {
				h.config.SetGovernance(t.Context(), *tc.org)
			}
			sb, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			env, initialize := engine.received(t, sb)

			var carried []string
			for _, kv := range env {
				if strings.HasPrefix(kv, toolkitVar+"=") {
					carried = append(carried, kv)
				}
			}
			if !slices.Equal(carried, tc.wantEnv) {
				t.Errorf("engine environment carries %q, want %q", carried, tc.wantEnv)
			}
			switch got := initialize.Params.ClientCapabilities.Meta.Kiro.TelemetryEnabled; {
			case got == nil:
				t.Errorf("initialize carries no telemetryEnabled, want %v", tc.wantOn)
			case *got != tc.wantOn:
				t.Errorf("initialize telemetryEnabled = %v, want %v", *got, tc.wantOn)
			}
		})
	}
}

// engineProcessFake spawns real bridges over a shell script that records its environment and stdin, one directory
// per process, since a chat open may also start the utility session.
type engineProcessFake struct {
	dirs map[*bridge.Bridge]string
	t    *testing.T
	mu   sync.Mutex
}

func newEngineProcessFake(t *testing.T) *engineProcessFake {
	return &engineProcessFake{t: t, dirs: map[*bridge.Bridge]string{}}
}

func (f *engineProcessFake) factory() ACPBridge {
	dir := f.t.TempDir()
	script := filepath.Join(dir, "kiro-cli")
	body := `#!/bin/sh
env > "` + dir + `/env"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + dir + `/stdin"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id" ;;
    session/new) printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-env"}}\n' "$id" ;;
    *) if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi ;;
  esac
done
`
	// ForkLock: a concurrent fork inheriting the write fd fails the exec with ETXTBSY (golang/go#22315).
	syscall.ForkLock.RLock()
	err := os.WriteFile(script, []byte(body), 0o700)
	syscall.ForkLock.RUnlock()
	if err != nil {
		f.t.Errorf("write the engine fake: %v", err)
	}
	b := bridge.New(script, dir)
	f.mu.Lock()
	f.dirs[b] = dir
	f.mu.Unlock()
	return b
}

type initializeFrame struct {
	Params struct {
		ClientCapabilities struct {
			Meta struct {
				Kiro struct {
					TelemetryEnabled *bool `json:"telemetryEnabled"`
				} `json:"kiro"`
			} `json:"_meta"`
		} `json:"clientCapabilities"`
	} `json:"params"`
	Method string `json:"method"`
}

// received is the environment and the initialize request of the process sb holds.
func (f *engineProcessFake) received(t *testing.T, sb *sharedBridge) (env []string, initialize initializeFrame) {
	t.Helper()
	sb.mu.Lock()
	b, _ := sb.bridge.(*bridge.Bridge)
	sb.mu.Unlock()
	f.mu.Lock()
	dir, ok := f.dirs[b]
	f.mu.Unlock()
	if !ok {
		t.Fatalf("chat bridge %T was not spawned by the engine fake", sb.bridge)
	}
	rawEnv, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatalf("read the engine environment: %v", err)
	}
	rawIn, err := os.ReadFile(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatalf("read the engine's stdin: %v", err)
	}
	for line := range strings.SplitSeq(string(rawIn), "\n") {
		var frame initializeFrame
		if json.Unmarshal([]byte(line), &frame) == nil && frame.Method == "initialize" {
			return strings.Split(string(rawEnv), "\n"), frame
		}
	}
	t.Fatalf("the engine received no initialize request")
	return nil, initialize
}
