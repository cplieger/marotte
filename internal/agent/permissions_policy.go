// The read-only native Cedar policy view (_kiro/permissions/list + explain) at GET /api/permissions,
// each on an observer pass. Editing is a file write KAS hot-reloads (internal/policyfile).

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// policyCallTimeout bounds one list or explain, its observer pass's door included.
const policyCallTimeout = 45 * time.Second

// policyRaw issues a read-only, session-scoped _kiro/permissions/* request on an observer pass: KAS answers only for
// a resident session, and the pass carries the profile's current presets.
func (st *Settings) policyRaw(ctx context.Context, method string, extra map[string]any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := st.observe(ctx, func(ctx context.Context, s engineSession) error {
		var err error
		raw, err = observerCall(ctx, s, fmt.Sprintf("policy call %s", method), method, extra)
		return err
	})
	return raw, err
}

// PolicyList returns the native policy rules, optionally for one scope, via _kiro/permissions/list.
func (st *Settings) PolicyList(ctx context.Context, scope string) ([]marotte.PolicyRule, error) {
	extra := map[string]any{}
	if scope != "" {
		extra["scope"] = scope
	}
	cctx, cancel := context.WithTimeout(ctx, policyCallTimeout)
	defer cancel()
	raw, err := st.policyRaw(cctx, methodV3PermissionsList, extra)
	if err != nil {
		return nil, err
	}
	var out struct {
		Rules []marotte.PolicyRule `json:"rules"`
	}
	if len(raw) == 0 {
		return []marotte.PolicyRule{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse permissions/list: %w", err)
	}
	if out.Rules == nil {
		out.Rules = []marotte.PolicyRule{}
	}
	return out.Rules, nil
}

// explainWire mirrors KAS's _kiro/permissions/explain reply.
type explainWire struct {
	Capability  string `json:"capability"`
	Resource    string `json:"resource"`
	Effect      string `json:"effect"`
	MatchedRule *struct {
		Capability string   `json:"capability"`
		Effect     string   `json:"effect"`
		Match      []string `json:"match"`
		Exclude    []string `json:"exclude"`
	} `json:"matchedRule"`
	Scope         string `json:"scope"`
	Source        string `json:"source"`
	IsExplicitAsk bool   `json:"isExplicitAsk"`
}

// PolicyExplain simulates a policy decision without executing or prompting (KAS
// evaluateSingleResource). Exactly one of Capability and ToolID; shell also needs a resource.
func (st *Settings) PolicyExplain(ctx context.Context, req marotte.PolicyExplainRequest) (*marotte.PolicyExplainResult, error) {
	extra := map[string]any{}
	switch {
	case req.Capability != "":
		extra["capability"] = req.Capability
	case req.ToolID != "":
		extra["toolId"] = req.ToolID
	default:
		return nil, errors.New("capability or tool_id required")
	}
	if req.Resource != "" {
		extra["resource"] = req.Resource
	}
	cctx, cancel := context.WithTimeout(ctx, policyCallTimeout)
	defer cancel()
	raw, err := st.policyRaw(cctx, methodV3PermissionsExplain, extra)
	if err != nil {
		return nil, err
	}
	var w explainWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse permissions/explain: %w", err)
	}
	res := &marotte.PolicyExplainResult{
		Capability:    w.Capability,
		Resource:      w.Resource,
		Effect:        w.Effect,
		IsExplicitAsk: w.IsExplicitAsk,
		Scope:         w.Scope,
		Source:        w.Source,
	}
	if w.MatchedRule != nil {
		res.MatchedRule = &marotte.PolicyRuleCore{
			Capability: w.MatchedRule.Capability,
			Match:      w.MatchedRule.Match,
			Exclude:    w.MatchedRule.Exclude,
			Effect:     w.MatchedRule.Effect,
		}
	}
	return res, nil
}
