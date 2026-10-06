package agent

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// securityPresets resolves the configured security profile into the KAS preset ids a session opens with
// (StartOpts.Presets), one resolver for every session. Empty is the Custom profile. A change reaches a
// chat at its next session start or load.
func securityPresets(ctx context.Context, configDir string) []string {
	var id string
	if !settings.FieldInto(ctx, configDir, settings.KeySecurityProfile, &id) || id == "" {
		p, _ := policyfile.ProfileFor(policyfile.DefaultProfile)
		return p.Presets
	}
	p, ok := policyfile.ProfileFor(id)
	if !ok {
		// Fall back rather than send nothing: nothing means Custom, which would drop the fs_read floor on a typo.
		slog.Warn("unknown security profile in settings; falling back",
			"key", settings.KeySecurityProfile, "value", id, "fallback", policyfile.DefaultProfile)
		p, _ = policyfile.ProfileFor(policyfile.DefaultProfile)
		return p.Presets
	}
	return p.Presets
}
