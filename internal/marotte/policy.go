package marotte

// Native Cedar policy domain types: KAS's policy is what is ENFORCED, read via
// _kiro/permissions/list (→ {rules:[{capability, match?, exclude?, effect, scope, source}]}) and
// explain (a pure simulation) on an observer session, and edited through the permissions.yaml files KAS
// hot-reloads (internal/policyfile). policy/check is NOT used: it raises a real permission request.
// effect is allow | ask | deny | evaluate (deny > ask > evaluate > allow); scope is kiro |
// administration | organization | user | workspace | agent | session, of which only user and
// workspace are file-editable.

// PolicyRule is one native policy rule as reported by _kiro/permissions/list.
// Capability + effect are always present; match/exclude are optional glob
// lists; scope + source carry provenance (source is a file path for
// user/workspace rules, or a synthetic tag like "kiro-scope"/"agent-profile").
type PolicyRule struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Scope      string   `json:"scope"`
	Source     string   `json:"source"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
	// Editable is stamped by GET /api/permissions from policyfile.Editable: the rule table's
	// controls can write this row. KAS never sends it.
	Editable bool `json:"editable"`
}

// PolicyRuleCore is the rule shape without provenance, used inside an
// explain result's matched_rule.
type PolicyRuleCore struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// PolicyView is the GET /api/permissions response: the native policy rule
// set plus the metadata the editor needs. Available is false when the engine
// could not answer (the view falls back to reading the editable files directly).
// Capabilities is what the rule-adder's dropdown OFFERS: the suggested set
// unioned with every capability the live rules already use, so it can learn a
// name marotte shipped without.
type PolicyView struct {
	// Profile is the posture in force; Profiles below is the ladder it comes from.
	// Split apart here only because the two have different alignments — read them
	// together.
	Profile        string       `json:"profile"`
	Rules          []PolicyRule `json:"rules"`
	WritableScopes []string     `json:"writable_scopes"`
	Capabilities   []string     `json:"capabilities"`
	// Profiles is the security-posture ladder in picker order, loosest last, and
	// Profile above is the one in force. Both travel here rather than being derived
	// client-side: the ladder decides what one click grants, so policyfile owns it
	// and the client renders it.
	//
	// Order is part of the payload. A client that sorted these would put the
	// loosest option somewhere in the middle of a list a reader scans from
	// cautious to permissive.
	Profiles []SecurityProfile `json:"profiles"`
	// LoadErrors are the rules KAS skipped or failed to load at the reading pass's door
	// (session/new _meta.policyErrors); empty, never null, when it reported none or the view
	// fell back to the files.
	LoadErrors []PolicyErrorItem `json:"load_errors"`
	Available  bool              `json:"available"`
}

// SecurityProfile is one entry in the picker: the persisted id and the KAS policy
// preset ids it sends. The presets travel so the UI can say what a profile grants
// without a second round trip, and so a reader can tell two profiles apart by
// something more than their names.
type SecurityProfile struct {
	ID      string   `json:"id"`
	Presets []string `json:"presets"`
}

// PolicyExplainRequest is the POST /api/permissions/explain body. Exactly
// one of Capability / ToolID is required; Resource is the path (fs caps) or
// command (shell) being simulated. KAS requires a resource for the shell
// capability.
type PolicyExplainRequest struct {
	Capability string `json:"capability,omitempty"`
	ToolID     string `json:"tool_id,omitempty"`
	Resource   string `json:"resource,omitempty"`
}

// PolicyExplainResult mirrors _kiro/permissions/explain (a pure simulation).
type PolicyExplainResult struct {
	Capability    string          `json:"capability"`
	Resource      string          `json:"resource,omitempty"`
	Effect        string          `json:"effect"`
	MatchedRule   *PolicyRuleCore `json:"matched_rule,omitempty"`
	Scope         string          `json:"scope,omitempty"`
	Source        string          `json:"source,omitempty"`
	IsExplicitAsk bool            `json:"is_explicit_ask"`
}

// PolicyErrorItem is one entry in a policy/changed or policy/error
// notification's errors array (from KAS).
type PolicyErrorItem struct {
	Scope   string `json:"scope,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal,omitempty"`
}

// PermissionsChangedPayload is the SSE payload for type="permissions_changed": a nudge with no
// fields. A client showing the policy refetches GET /api/permissions, which carries the rules
// and the load errors.
type PermissionsChangedPayload struct{}

// PolicyErrorPayload is the SSE payload for type="policy_error" (translated
// from _kiro/policy/error). Surfaced as a banner so a bad hand-edit or a
// rejected rule is visible without hunting the logs.
type PolicyErrorPayload struct {
	Errors []PolicyErrorItem `json:"errors,omitempty"`
}
