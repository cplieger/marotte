package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

func withKiroTelemetry(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	workspace.SetKiroHomeForTest(t, home)
	path := filepath.Join(home, "settings", "cli.json")
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
		{name: "unset reads off, as the boot seed and the Settings switch do", want: true},
		{name: "switch on", cliJSON: `{"telemetry.enabled":true}`},
		{name: "switch off", cliJSON: `{"telemetry.enabled":false}`, want: true},
		{name: "organization off overrides the switch", cliJSON: `{"telemetry.enabled":true}`, org: new(orgTelemetry(false)), want: true},
		{name: "organization on overrides the switch", cliJSON: `{"telemetry.enabled":false}`, org: new(orgTelemetry(true))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withKiroTelemetry(t, tc.cliJSON)
			h, _ := reopenFixture(t)
			if tc.org != nil {
				h.config.SetGovernance(t.Context(), *tc.org)
			}
			sb, err := h.coord.OpenBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}

			if got := fakeOf(sb).lastStartOpts().DisableTelemetry; got != tc.want {
				t.Errorf("chat spawn under cli.json %q, org lock set %v: DisableTelemetry = %v, want %v", tc.cliJSON, tc.org != nil, got, tc.want)
			}
		})
	}
}
