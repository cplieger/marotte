package marotte

// PermissionOutcome is the typed wire shape for ACP permission-outcome
// responses. Replaces anonymous map[string]any for compile-time safety.
type PermissionOutcome struct {
	Meta    *PermissionOutcomeMeta `json:"_meta,omitempty"`
	Outcome PermissionOutcomeInner `json:"outcome"`
}

// PermissionOutcomeInner is the nested outcome payload.
type PermissionOutcomeInner struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// PermissionOutcomeSelected builds the ACP permission-outcome response
// for a selected option. Single source of truth for the wire shape.
func PermissionOutcomeSelected(optionID string) *PermissionOutcome {
	return &PermissionOutcome{
		Outcome: PermissionOutcomeInner{Outcome: "selected", OptionID: optionID},
	}
}

// PermissionOutcomeWithFileDecisions builds a permission response carrying per-file decisions for a
// TURN APPROVAL as `_meta.kiro.fileDecisions` (pending-action id to accept/reject). KAS restores
// every id the map omits, so an omitted id is a REJECT: send a decision for every file offered.
func PermissionOutcomeWithFileDecisions(optionID string, decisions map[string]bool) *PermissionOutcome {
	out := PermissionOutcomeSelected(optionID)
	if len(decisions) > 0 {
		out.Meta = &PermissionOutcomeMeta{Kiro: PermissionOutcomeKiro{FileDecisions: decisions}}
	}
	return out
}

// PermissionOutcomeWithRejectionReason builds a permission response carrying the
// user's note as `_meta.kiro.rejectionReason`, which KAS hands the model as
// "The user rejected this tool call: <reason>". An empty reason sends no `_meta`:
// KAS forwards "" verbatim as a dangling sentence. KAS reads the note only on a
// reject_once answer, so the caller need not gate on the option's kind.
func PermissionOutcomeWithRejectionReason(optionID, reason string) *PermissionOutcome {
	out := PermissionOutcomeSelected(optionID)
	if reason != "" {
		out.Meta = &PermissionOutcomeMeta{Kiro: PermissionOutcomeKiro{RejectionReason: reason}}
	}
	return out
}

// PermissionOutcomeAlways builds an allow_always or reject_always answer that saves consent
// as a durable rule. KAS writes the rule itself and reads only scope and resource here (the
// capability is the ask's own), the shape the kiro-cli TUI sends.
func PermissionOutcomeAlways(optionID string, consent PermissionConsentAnswer) *PermissionOutcome {
	out := PermissionOutcomeSelected(optionID)
	out.Meta = &PermissionOutcomeMeta{Kiro: PermissionOutcomeKiro{Consent: &consent}}
	return out
}

// PermissionConsentAnswer is `_meta.kiro.consent` on an always answer.
type PermissionConsentAnswer struct {
	Scope      string `json:"scope"`
	Capability string `json:"capability,omitempty"`
	Resource   string `json:"resource"`
}

// ConsentScopeUser saves a rule to the user permissions file, which every session reads.
const ConsentScopeUser = "user"

// PermissionOutcomeMeta is the `_meta` envelope on a permission reply.
type PermissionOutcomeMeta struct {
	Kiro PermissionOutcomeKiro `json:"kiro"`
}

// PermissionOutcomeKiro is the vendor block inside that envelope.
type PermissionOutcomeKiro struct {
	FileDecisions   map[string]bool          `json:"fileDecisions,omitempty"`
	Consent         *PermissionConsentAnswer `json:"consent,omitempty"`
	RejectionReason string                   `json:"rejectionReason,omitempty"`
}

// PermissionOutcomeCancelled builds the ACP permission-outcome response
// for a cancelled/denied permission. Single source of truth for the wire shape.
func PermissionOutcomeCancelled() *PermissionOutcome {
	return &PermissionOutcome{
		Outcome: PermissionOutcomeInner{Outcome: string(StopReasonCancelled)},
	}
}
