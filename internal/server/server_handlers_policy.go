package server

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/webhttp/v3"
)

// Native Cedar policy endpoints: GET /api/permissions (the enforced view), POST
// /api/permissions/explain (a simulation, no consent prompt), POST /api/permissions/rules (a
// permissions.yaml write KAS hot-reloads). The writer is conservative: a new rule defaults to
// `ask`, only user and workspace scopes are writable, and widening needs an explicit confirm.

// handlePolicyView serves GET /api/permissions via _kiro/permissions/list on the utility
// bridge, degrading to the editable files (Available=false) when no bridge answers.
func (s *Server) handlePolicyView(w http.ResponseWriter, r *http.Request) {
	// Gated here, not on the pattern (see ListenAndServe).
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	scope := r.URL.Query().Get("scope")
	view := marotte.PolicyView{
		WritableScopes: []string{policyfile.ScopeUser, policyfile.ScopeWorkspace},
		Profiles:       securityProfileCatalog(),
		Profile:        s.activeProfile(r.Context()),
		Available:      true,
	}
	if s.policy != nil {
		rules, err := s.policy.PolicyList(r.Context(), scope)
		if err == nil {
			view.Rules = dedupePolicyRules(rules)
			view.Capabilities = pickerCapabilities(view.Rules)
			webhttp.WriteJSON(w, view)
			return
		}
		slog.Warn("policy view: live list failed, falling back to file read", "error", logsafe.Field(err.Error()))
	}
	// Fallback: read the editable files directly so the editor works even
	// when no bridge can answer (e.g. not signed in).
	view.Available = false
	view.Rules = dedupePolicyRules(s.policyRulesFromFiles(scope))
	view.Capabilities = pickerCapabilities(view.Rules)
	webhttp.WriteJSON(w, view)
}

// dedupePolicyRules drops rules identical in EVERY field, keeping first-arrival order:
// _kiro/permissions/list reports one rule several times. The key is the whole value, because
// scope and source legitimately separate two rows (one rule in two files is two files to edit).
// Done here because both the live and the file-fallback projections pass through.
func dedupePolicyRules(rules []marotte.PolicyRule) []marotte.PolicyRule {
	if len(rules) == 0 {
		// Preserve the empty-not-null contract the wire field carries.
		return []marotte.PolicyRule{}
	}
	out := make([]marotte.PolicyRule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, r := range rules {
		key := policyRuleKey(&r)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	if dropped := len(rules) - len(out); dropped > 0 {
		slog.Debug("policy view: dropped identical duplicate rules", "dropped", dropped, "kept", len(out))
	}
	return out
}

// policyRuleKey is a total, collision-free key over a rule's six fields: every element is
// length-prefixed, because a glob can contain any separator. Glob order is significant.
func policyRuleKey(r *marotte.PolicyRule) string {
	var b strings.Builder
	for _, s := range []string{r.Capability, r.Effect, r.Scope, r.Source} {
		fmt.Fprintf(&b, "%d:%s", len(s), s)
	}
	for _, list := range [][]string{r.Match, r.Exclude} {
		fmt.Fprintf(&b, "|%d|", len(list))
		for _, s := range list {
			fmt.Fprintf(&b, "%d:%s", len(s), s)
		}
	}
	return b.String()
}

// pickerCapabilities is what the capability dropdowns offer: marotte's suggested set UNION
// every capability the returned rules use, so a capability KAS gains becomes selectable with
// no release. Not a write filter. Sorted as one list; never empty, so the wire field is not null.
func pickerCapabilities(rules []marotte.PolicyRule) []string {
	suggested := policyfile.Capabilities()
	seen := make(map[string]struct{}, len(suggested)+len(rules))
	for _, c := range suggested {
		seen[c] = struct{}{}
	}
	for i := range rules {
		if c := rules[i].Capability; c != "" {
			seen[c] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// Used only as the no-bridge fallback for the view.
func (s *Server) policyRulesFromFiles(scope string) []marotte.PolicyRule {
	home, err := os.UserHomeDir()
	if err != nil {
		return []marotte.PolicyRule{}
	}
	out := []marotte.PolicyRule{}
	for _, sc := range []string{policyfile.ScopeUser, policyfile.ScopeWorkspace} {
		if scope != "" && scope != sc {
			continue
		}
		path, perr := policyfile.PathFor(sc, policyfile.Roots{Home: home, WorkDir: s.workDir})
		if perr != nil {
			continue
		}
		f, lerr := policyfile.Load(path)
		if lerr != nil || f == nil {
			continue
		}
		for _, ru := range f.Rules {
			out = append(out, marotte.PolicyRule{
				Capability: ru.Capability, Effect: ru.Effect,
				Match: ru.Match, Exclude: ru.Exclude,
				Scope: sc, Source: path,
			})
		}
	}
	return out
}

// handlePolicyExplain serves POST /api/permissions/explain: a pure simulation (KAS's
// evaluateSingleResource raises no consent prompt).
func (s *Server) handlePolicyExplain(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	if s.policy == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("policy explain unavailable"))
		return
	}
	var req marotte.PolicyExplainRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Capability == "" && req.ToolID == "" {
		httpreply.BadRequest(w, "capability or tool_id required")
		return
	}
	// KAS requires a resource for the shell capability, so refuse here with a clear reason.
	if req.Capability == capShell && strings.TrimSpace(req.Resource) == "" {
		httpreply.BadRequest(w, "the shell capability needs a resource, the command, to evaluate")
		return
	}
	res, err := s.policy.PolicyExplain(r.Context(), req)
	if err != nil {
		slog.Warn("policy explain failed", "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("policy explain failed"))
		return
	}
	webhttp.WriteJSON(w, res)
}

// op is "add", "remove" or "update". An add's empty effect defaults to "ask". For remove and update
// the rule fields identify the EXISTING rule; a widening change needs confirm=true.
type policyRuleBody struct {
	Op         string   `json:"op"`
	Scope      string   `json:"scope"`
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	NewEffect  string   `json:"new_effect"`
	Match      []string `json:"match"`
	Exclude    []string `json:"exclude"`
	Confirm    bool     `json:"confirm"`
}

// capShell is the capability whose decisions are always resource-scoped.
const capShell = "shell"

// effectRank orders effects by strictness for the widening gate: moving a
// rule to a lower rank grants the agent more than it had.
var effectRank = map[string]int{
	policyfile.EffectDeny:  2,
	policyfile.EffectAsk:   1,
	policyfile.EffectAllow: 0,
}

// Writes the scope's permissions.yaml; KAS hot-reloads and emits _kiro/policy/changed → the
// permissions_changed SSE.
func (s *Server) handlePolicyRules(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body policyRuleBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !policyfile.ValidScope(body.Scope) {
		httpreply.BadRequest(w, "scope must be user or workspace")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		httpreply.InternalError(w, err)
		return
	}
	path, err := policyfile.PathFor(body.Scope, policyfile.Roots{Home: home, WorkDir: s.workDir})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	switch body.Op {
	case "add":
		s.policyRuleAdd(w, r, &body, path)
	case "remove":
		policyRuleRemove(w, r, &body, path)
	case "update":
		policyRuleUpdate(w, r, &body, path)
	default:
		httpreply.BadRequest(w, "op must be add, remove, or update")
	}
}

func (*Server) policyRuleAdd(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	// Conservative default: a new rule with no explicit effect is `ask`, never `allow`.
	effect := cmp.Or(body.Effect, policyfile.EffectAsk)
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	// marotte does not own the capability vocabulary, so an unrecognised name is written through
	// for KAS to judge; log it, since KAS skips such a rule with no server-side trace.
	if !slices.Contains(policyfile.Capabilities(), rule.Capability) {
		slog.Warn("writing a policy rule naming a capability marotte does not recognise; "+
			"kiro-cli decides whether it loads",
			"capability", logsafe.Field(rule.Capability), "effect", rule.Effect, "scope", body.Scope)
	}
	f, err := policyfile.Load(path)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("existing policy file could not be parsed. Edit it manually"))
		return
	}
	changed, err := f.Upsert(&rule)
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	if !changed {
		webhttp.Ok(w) // idempotent: identical rule already present
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("policy rule added", "scope", body.Scope, "capability", logsafe.Field(rule.Capability), "effect", rule.Effect)
	webhttp.Ok(w)
}

func policyRuleRemove(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	if !policyfile.ValidEffect(body.Effect) {
		httpreply.BadRequest(w, "effect required to remove a rule")
		return
	}
	// Removing a deny widens access: require an explicit confirm.
	if body.Effect == policyfile.EffectDeny && !body.Confirm {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("removing a deny rule widens access. Resend with confirm=true"))
		return
	}
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: body.Effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("existing policy file could not be parsed. Edit it manually"))
		return
	}
	if !f.Remove(&rule) {
		webhttp.Ok(w) // idempotent: rule already absent
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("policy rule removed", "scope", body.Scope, "capability", logsafe.Field(rule.Capability), "effect", rule.Effect)
	webhttp.Ok(w)
}

// A widening change requires confirm=true.
func policyRuleUpdate(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	if !policyfile.ValidEffect(body.Effect) {
		httpreply.BadRequest(w, "effect required to identify the rule")
		return
	}
	if !policyfile.ValidEffect(body.NewEffect) {
		httpreply.BadRequest(w, "new_effect must be allow, deny, or ask")
		return
	}
	if effectRank[body.NewEffect] < effectRank[body.Effect] && !body.Confirm {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("changing "+body.Effect+" to "+body.NewEffect+" widens access. Resend with confirm=true"))
		return
	}
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: body.Effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("existing policy file could not be parsed. Edit it manually"))
		return
	}
	if !f.ReplaceEffect(&rule, body.NewEffect) {
		// Idempotent replay: the target state may already be on disk.
		target := rule
		target.Effect = body.NewEffect
		if f.Has(&target) {
			webhttp.Ok(w)
			return
		}
		httpreply.NotFound(w, "rule not found. Refresh the policy view")
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("policy rule updated", "scope", body.Scope,
		"capability", logsafe.Field(rule.Capability), "effect", body.Effect, "new_effect", body.NewEffect)
	webhttp.Ok(w)
}
