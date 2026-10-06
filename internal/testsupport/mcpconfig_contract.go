package testsupport

import (
	"context"
	"testing"
)

// MCPNameSets is the UNION of what the MCP name-census consumers declare; a consumer growing a
// set must add it here, or the suite goes silent on it.
type MCPNameSets interface {
	EnabledNames(ctx context.Context) map[string]struct{}
	ConfiguredNames(ctx context.Context) map[string]struct{}
}

// MCPConfigContractTest exercises the behavioral expectations any MCP
// name-census implementation must meet. Run against both fakes and the real
// store to catch drift.
func MCPConfigContractTest(t *testing.T, newConfig func(t *testing.T) MCPNameSets) {
	t.Helper()

	t.Run("every_name_set_empty_when_no_servers", func(t *testing.T) {
		cfg := newConfig(t)
		ctx := context.Background()
		for _, tc := range []struct {
			names map[string]struct{}
			name  string
		}{
			{cfg.EnabledNames(ctx), "EnabledNames"},
			{cfg.ConfiguredNames(ctx), "ConfiguredNames"},
		} {
			if len(tc.names) != 0 {
				t.Errorf("%s() = %v, want empty", tc.name, tc.names)
			}
		}
	})

	// An enabled name with no config entry would look editable.
	t.Run("enabled_names_are_configured", func(t *testing.T) {
		cfg := newConfig(t)
		ctx := context.Background()
		configured := cfg.ConfiguredNames(ctx)
		for name := range cfg.EnabledNames(ctx) {
			if _, ok := configured[name]; !ok {
				t.Errorf("%q is enabled but not in ConfiguredNames", name)
			}
		}
	})
}
