package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
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

func TestEngineSpawn_ResolvesTelemetryUnderTheOrganizationLock(t *testing.T) {
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
				reportAccount(t, h, *tc.org)
			}

			if got := h.resolveEngine(t.Context(), "/fake/kiro-cli").opts.DisableTelemetry; got != tc.want {
				t.Errorf("engine spawn under cli.json %q, org lock set %v: DisableTelemetry = %v, want %v", tc.cliJSON, tc.org != nil, got, tc.want)
			}
		})
	}
}

// From the switch and the organization lock to what the real kiro-cli process receives: the toolkit variable in
// its environment and telemetryEnabled on the initialize it reads. Not parallel: it sets environment variables.
func TestEngineSpawn_TelemetryReachesTheEngineProcess(t *testing.T) {
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
			cs := newTestChatStore()
			h := New(context.Background(), t.TempDir(), engine, cs, WithConfigDir(t.TempDir()))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			if tc.org != nil {
				h.config.SetGovernance(t.Context(), *tc.org)
			}
			close(engine.installed)
			if !awaitEngine(h) {
				t.Fatal("the engine over the fake process never became ready")
			}
			env, initialize := engine.received(t)

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

// engineProcessFake is an EngineSource over a shell script that records its environment and stdin; the engine
// starts once installed closes, so a test sets the lock first.
type engineProcessFake struct {
	installed chan struct{}
	dir       string
	script    string
}

var _ EngineSource = (*engineProcessFake)(nil)

func newEngineProcessFake(t *testing.T) *engineProcessFake {
	t.Helper()
	dir := t.TempDir()
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
		t.Fatalf("write the engine fake: %v", err)
	}
	return &engineProcessFake{installed: make(chan struct{}), dir: dir, script: script}
}

func (f *engineProcessFake) CLIPath() string             { return f.script }
func (f *engineProcessFake) Installed() <-chan struct{}  { return f.installed }
func (*engineProcessFake) Env() []string                 { return nil }
func (*engineProcessFake) EnvAllow() map[string]struct{} { return nil }

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

// received is the environment and the initialize request of the engine process.
func (f *engineProcessFake) received(t *testing.T) (env []string, initialize initializeFrame) {
	t.Helper()
	rawEnv, err := os.ReadFile(filepath.Join(f.dir, "env"))
	if err != nil {
		t.Fatalf("read the engine environment: %v", err)
	}
	rawIn, err := os.ReadFile(filepath.Join(f.dir, "stdin"))
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

func writeCompactionSetting(t *testing.T, dir string, enabled bool, pct int) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		settings.KeyAutoCompactionEnabled: enabled,
		settings.KeyAutoCompactPct:        pct,
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func TestAutoCompactionPolicy_ReadsTheSettingAndFailsToTheDefault(t *testing.T) {
	policy := func(dir string) (bool, int) {
		t.Helper()
		doc, _, err := settings.Read(t.Context(), dir)
		if err != nil {
			t.Fatalf("settings.Read(%s) = %v", dir, err)
		}
		return autoCompactionPolicy(doc)
	}
	if enabled, pct := policy(t.TempDir()); !enabled || pct != 80 {
		t.Errorf("autoCompactionPolicy with no config.json = (%v, %d), want (true, 80)", enabled, pct)
	}
	dir := t.TempDir()
	writeCompactionSetting(t, dir, false, 60)
	if enabled, pct := policy(dir); enabled || pct != 60 {
		t.Errorf("autoCompactionPolicy(stored false, 60) = (%v, %d), want (false, 60)", enabled, pct)
	}
	// KAS refuses a threshold above 80, so a stored 85 or 90 reads as the default.
	for _, stored := range []int{83, 85, 90} {
		writeCompactionSetting(t, dir, true, stored)
		if _, pct := policy(dir); pct != 80 {
			t.Errorf("autoCompactionPolicy(stored %d) pct = %d, want 80", stored, pct)
		}
	}
}

// TestChatDoor_SendsTheCompactionPolicyAndAnObserverDoorDoesNot pins that a chat's door carries the policy's threshold
// and an observer pass keeps KAS's defaults.
func TestChatDoor_SendsTheCompactionPolicyAndAnObserverDoorDoesNot(t *testing.T) {
	for _, tc := range []struct {
		enabled       bool
		wantThreshold int
	}{
		{enabled: true, wantThreshold: 60},
		{enabled: false, wantThreshold: 0},
	} {
		t.Run(fmt.Sprintf("auto_compaction_enabled=%v", tc.enabled), func(t *testing.T) {
			dir := t.TempDir()
			writeCompactionSetting(t, dir, tc.enabled, 60)
			var mu sync.Mutex
			var doors []*recordingStartBridge
			record := func() engineSession {
				rb := newRecordingStartBridge()
				mu.Lock()
				doors = append(doors, rb)
				mu.Unlock()
				return rb
			}
			src := fakeEngineOf(record, nil)
			src.observer = record
			cs := newTestChatStore()
			h := newRuntimeOn(t.Context(), t.TempDir(), src, cs, WithConfigDir(dir))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
				t.Fatalf("seed the chat: %v", err)
			}
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			if err := h.engine.observe(t.Context(), nil, nil, func(context.Context, engineSession) error { return nil }); err != nil {
				t.Fatalf("observer pass: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			var chat, observer int
			for _, rb := range doors {
				switch o := rb.startOpts(); o.Role {
				case marotte.SessionRoleChat:
					if o.AutoCompactionThreshold != tc.wantThreshold || o.DisableAutoCompaction == tc.enabled {
						t.Errorf("the chat's door sent threshold %d, disableAutoCompaction %v; want %d, %v",
							o.AutoCompactionThreshold, o.DisableAutoCompaction, tc.wantThreshold, !tc.enabled)
					}
					chat++
				case marotte.SessionRoleObserver:
					if o.AutoCompactionThreshold != 0 || o.DisableAutoCompaction {
						t.Errorf("an observer door sent threshold %d, disableAutoCompaction %v; want 0, false",
							o.AutoCompactionThreshold, o.DisableAutoCompaction)
					}
					observer++
				}
			}
			if chat != 1 || observer < 1 {
				t.Errorf("chat doors = %d, observer doors = %d; want one chat door and at least one observer door", chat, observer)
			}
		})
	}
}
