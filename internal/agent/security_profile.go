package agent

import (
	"log/slog"

	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// securityProfileID is the stored security_profile; empty when absent or invalid.
func securityProfileID(d settings.Document) string {
	var id string
	d.Into(settings.KeySecurityProfile, &id)
	return id
}

// securityPresets resolves the security profile id into the KAS preset ids a session opens with
// (SessionOpts.Presets), one resolver for every session. Empty is the default profile. A change reaches a chat at its
// next session start or load.
func securityPresets(id string) []string {
	if id == "" {
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
