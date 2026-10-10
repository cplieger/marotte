package translate

// _kiro/governance/state, pushed at session/new and session/load and on change. The state is
// account-global: the runtime caches it and owns the governance_state SSE, which adds the lock map.

import (
	"context"
	"encoding/json"

	"github.com/cplieger/marotte/internal/marotte"
)

type v3GovernanceState struct {
	SessionID      string               `json:"sessionId"`
	DisabledReason string               `json:"disabledReason"`
	Features       v3GovernanceFeatures `json:"features"`
	IsEnterprise   bool                 `json:"isEnterprise"`
}

// v3GovernanceFeatures mirrors KAS's GovernanceFeatures (getFeatures()).
type v3GovernanceFeatures struct {
	MCPEnabled           bool `json:"mcpEnabled"`
	WebToolsEnabled      bool `json:"webToolsEnabled"`
	UsageAnalytics       bool `json:"usageAnalytics"`
	ContentCollection    bool `json:"contentCollection"`
	PromptLogging        bool `json:"promptLogging"`
	CodeReferenceTracker bool `json:"codeReferenceTracker"`
	AutonomousAgents     bool `json:"autonomousAgents"`
}

// payload converts the wire shape into the domain payload with Known=true; SessionID is
// dropped (governance is account-global).
func (w v3GovernanceState) payload() marotte.GovernanceStatePayload {
	return marotte.GovernanceStatePayload{
		Known:          true,
		IsEnterprise:   w.IsEnterprise,
		DisabledReason: w.DisabledReason,
		Features: marotte.GovernanceFeatures{
			MCPEnabled:           w.Features.MCPEnabled,
			WebToolsEnabled:      w.Features.WebToolsEnabled,
			UsageAnalytics:       w.Features.UsageAnalytics,
			ContentCollection:    w.Features.ContentCollection,
			PromptLogging:        w.Features.PromptLogging,
			CodeReferenceTracker: w.Features.CodeReferenceTracker,
			AutonomousAgents:     w.Features.AutonomousAgents,
		},
	}
}

// DecodeGovernanceState decodes a raw _kiro/governance/state params object into the domain
// payload, false on empty/invalid params. Exported for the utility bridge's copy.
func DecodeGovernanceState(raw json.RawMessage) (marotte.GovernanceStatePayload, bool) {
	if len(raw) == 0 {
		return marotte.GovernanceStatePayload{}, false
	}
	var w v3GovernanceState
	if err := json.Unmarshal(raw, &w); err != nil {
		return marotte.GovernanceStatePayload{}, false
	}
	return w.payload(), true
}

// HandleGovernanceState hands _kiro/governance/state to the runtime's cache. A subagent-session
// copy is skipped: the parent copy carries the identical flags.
func (t *Translator) HandleGovernanceState(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := unmarshalParams[v3GovernanceState](msg, "governance/state")
	if !ok {
		return
	}
	if t.foreignSession(chatID, p.SessionID) {
		return
	}
	t.governance.SetGovernance(ctx, p.payload())
}
