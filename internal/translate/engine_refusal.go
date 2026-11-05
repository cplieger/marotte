package translate

import (
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// engineRefusals are KAS's permission refusals that name no rule. KAS builds policyDenial
// only from a matched rule, so these are recognisable only by their text;
// kascap's TestEngineRefusalLeadsMatchBundle pins each lead against the bundle.
var engineRefusals = []struct {
	lead       string
	basis      marotte.DenialBasis
	capability string
}{
	{lead: "Tool call denied: Kiro could not parse this shell command", basis: marotte.DenialUnparseable, capability: "shell"},
	{lead: "Tool call denied: the user's permission file(s) failed to parse.", basis: marotte.DenialPermissionsInvalid},
	{lead: "Tool call denied: Kiro's permission engine (cedar-wasm) could not be loaded", basis: marotte.DenialCedarUnavailable},
	{lead: "the permission policy (no policy loaded, so every call is denied)", basis: marotte.DenialNotLoaded},
}

// engineRefusal is the denial a settled call's output states, nil unless the wire status
// is failed and the call never reached a terminal: a command that ran has one, so a
// program printing the same sentence is never misread. status is the WIRE status, since
// gradeStoppedCall may already have rewritten the call's own to aborted.
func engineRefusal(status marotte.ToolStatus, terminalID, output string) *marotte.ToolDenial {
	if status != marotte.ToolFailed || terminalID != "" {
		return nil
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(output, "Output:\n"), "\n")
	first = strings.TrimPrefix(first, "Blocked by ")
	for _, r := range engineRefusals {
		if strings.HasPrefix(first, r.lead) {
			return &marotte.ToolDenial{Capability: r.capability, Basis: r.basis}
		}
	}
	return nil
}
