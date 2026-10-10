// Operator-supplied kiro-cli launch flags (MAROTTE_KIRO_ACP_ARGS). marotte emits --agent-engine and --auth-method,
// refuses every flag that conflicts with them or that kiro-cli rejects on v3, and passes -v and future flags.

package bridge

import (
	"log/slog"
	"strings"
)

const (
	flagAgentEngine     = "--agent-engine"
	flagAuthMethod      = "--auth-method"
	flagAuthMethodAlias = "--authMethod"
	flagAgent           = "--agent"
	flagTrustAll        = "--trust-all-tools"
	flagTrustAllShort   = "-a"
	flagTrustTools      = "--trust-tools"
	flagModel           = "--model"
	flagEffort          = "--effort"
)

var valueBearing = map[string]bool{
	flagAgentEngine:     true,
	flagAuthMethod:      true,
	flagAuthMethodAlias: true,
	flagAgent:           true,
	flagTrustTools:      true,
	flagModel:           true,
	flagEffort:          true,
}

// ParseACPArgs splits an operator-supplied flag string and filters refused flags.
func ParseACPArgs(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	kept := filterACPArgs(fields)
	slog.Info("appending extra kiro-cli acp flags",
		"acp_args_count", len(kept), "refused_count", len(fields)-len(kept))
	return kept
}

// filterACPArgs drops flags owned by marotte's wire and session configuration.
func filterACPArgs(fields []string) []string {
	kept := make([]string, 0, len(fields))
	skipValue := false
	for _, f := range fields {
		if skipValue {
			skipValue = false
			continue
		}
		name, _, hasInlineValue := strings.Cut(f, "=")
		if reason, refused := refuseReason(name); refused {
			slog.Warn("refusing kiro-cli acp flag", "flag", name, "reason", reason)
			skipValue = valueBearing[name] && !hasInlineValue
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

// v3Refused opens every reason for a flag kiro-cli rejects alongside --agent-engine=v3.
const v3Refused = "kiro-cli refuses this alongside --agent-engine=v3 and exits before initialize, so it would kill every chat bridge; "

func refuseReason(name string) (reason string, refused bool) {
	switch name {
	case flagAgentEngine:
		return "marotte is v3-only on the wire; the v2 handlers were removed, so v1/v2 would stall session/new", true
	case flagAuthMethod, flagAuthMethodAlias:
		return "kiro-cli rejects an invalid auth method and exits before initialize, so it would kill every chat bridge; marotte fixes relay authentication to cli", true
	case flagModel, flagEffort:
		return v3Refused + "pick the model and reasoning effort per chat in the composer instead", true
	case flagAgent:
		return v3Refused + "pick the role per chat with the mode pill instead", true
	case flagTrustAll, flagTrustAllShort, flagTrustTools:
		return v3Refused + "tool authorization is kiro-cli's Cedar policy; edit permissions.yaml (Settings → Permissions) instead", true
	default:
		return "", false
	}
}
