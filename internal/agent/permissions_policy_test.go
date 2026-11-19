package agent

// list and explain ride an observer pass and name its session; presets/stores, like the workspace-global
// knowledge calls, names none.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func seedPolicy(br *fakeBridge, list, explain string) {
	br.callResults = map[string]json.RawMessage{
		methodV3PermissionsList:    json.RawMessage(list),
		methodV3PermissionsExplain: json.RawMessage(explain),
	}
}

func TestPolicyList(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br := oneObserver(t, h)
	// Shape captured from a live v3 permissions/list reply.
	seedPolicy(br, `{"rules":[`+
		`{"capability":"fs_write","effect":"allow","match":["/x/**"],"scope":"user","source":"/u/permissions.yaml"},`+
		`{"capability":"shell","effect":"deny","match":["sudo *"],"scope":"user","source":"/u/permissions.yaml"}]}`, `{}`)

	rules, err := h.config.PolicyList(t.Context(), "user")
	if err != nil {
		t.Fatalf("PolicyList: %v", err)
	}
	if len(rules) != 2 || rules[0].Capability != "fs_write" || rules[0].Effect != "allow" ||
		rules[1].Effect != "deny" || rules[1].Scope != "user" {
		t.Errorf("rules = %+v", rules)
	}
	// list is session-scoped and forwards the scope filter.
	p := br.paramsFor(methodV3PermissionsList)
	if p[marotte.KeySessionID] == nil || p[marotte.KeySessionID] == "" {
		t.Errorf("permissions/list params missing sessionId: %+v", p)
	}
	if p["scope"] != "user" {
		t.Errorf("scope filter not forwarded: %+v", p)
	}
}

// _kiro/permissions/list returns rules only; the skipped rules come from the same pass's door.
func TestPolicyListingReturnsTheDoorsErrors(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br := oneObserver(t, h)
	seedPolicy(br, `{"rules":[{"capability":"shell","effect":"ask","scope":"kiro","source":"kiro-scope"}]}`, `{}`)
	br.policyErrors = []marotte.PolicyErrorItem{{Scope: "user", Message: "unknown capability 'teleport'"}}

	rules, loadErrs, err := h.config.PolicyListing(t.Context(), "")
	if err != nil {
		t.Fatalf("PolicyListing: %v", err)
	}
	if len(rules) != 1 || rules[0].Capability != "shell" {
		t.Errorf("rules = %+v, want the one listed rule", rules)
	}
	if len(loadErrs) != 1 || loadErrs[0].Message != "unknown capability 'teleport'" || loadErrs[0].Scope != "user" {
		t.Errorf("load errors = %+v, want the door's one skipped rule", loadErrs)
	}
}

func TestPolicyListOmitsEmptyScope(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br := oneObserver(t, h)
	seedPolicy(br, `{"rules":[]}`, `{}`)
	if _, err := h.config.PolicyList(t.Context(), ""); err != nil {
		t.Fatalf("PolicyList: %v", err)
	}
	if _, hasScope := br.paramsFor(methodV3PermissionsList)["scope"]; hasScope {
		t.Error("empty scope should be omitted from params, not sent as \"\"")
	}
}

func TestPolicyExplainMapsCamelCase(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br := oneObserver(t, h)
	seedPolicy(br, `{"rules":[]}`, `{"capability":"fs_write","resource":"/x/y","effect":"ask",`+
		`"isExplicitAsk":true,"matchedRule":{"capability":"fs_write","match":["/x/**"],"effect":"ask"},`+
		`"scope":"workspace","source":"/w/permissions.yaml"}`)

	res, err := h.config.PolicyExplain(t.Context(), marotte.PolicyExplainRequest{Capability: "fs_write", Resource: "/x/y"})
	if err != nil {
		t.Fatalf("PolicyExplain: %v", err)
	}
	if res.Effect != "ask" || !res.IsExplicitAsk || res.Scope != "workspace" {
		t.Errorf("explain = %+v", res)
	}
	if res.MatchedRule == nil || res.MatchedRule.Capability != "fs_write" || len(res.MatchedRule.Match) != 1 {
		t.Errorf("matchedRule = %+v", res.MatchedRule)
	}
	// explain forwards capability, resource and sessionId.
	p := br.paramsFor(methodV3PermissionsExplain)
	if p["capability"] != "fs_write" || p["resource"] != "/x/y" || p[marotte.KeySessionID] == nil {
		t.Errorf("explain params = %+v", p)
	}
}

func TestPolicyExplainRequiresTarget(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br := oneObserver(t, h)
	seedPolicy(br, `{}`, `{}`)
	if _, err := h.config.PolicyExplain(t.Context(), marotte.PolicyExplainRequest{}); err == nil {
		t.Error("PolicyExplain with no capability/toolId should error")
	}
}

// stores is sessionless (KAS answers a sessionId -32602) and asks about exactly the roots a chat door opens over.
func TestPresetStores_SendsSessionRootsWithoutSession(t *testing.T) {
	f := newAgentRootsFixture(t)
	h, _, br := newTestHubIn(f.work, WithConfigDir(f.config))
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroPermissionsPresetStores: json.RawMessage(`{"stores":[` +
		`{"scope":"user","file":"/u/settings/permissions.yaml","read":{"ok":true,"presets":[]}},` +
		`{"scope":"workspace","workspaceRoot":"` + f.work + `","file":"` + f.work + `/.kiro/settings/permissions.yaml",` +
		`"read":{"ok":true,"presets":[{"id":"dev-shell","status":"applies"}]}},` +
		`{"scope":"workspace","workspaceRoot":"` + f.extra + `","file":"` + f.extra + `/.kiro/settings/permissions.json",` +
		`"read":{"ok":false,"error":"bad file"}}]}`)}

	stores, err := h.config.PresetStores(t.Context())
	if err != nil {
		t.Fatalf("PresetStores: %v", err)
	}
	p := br.paramsFor(methodKiroPermissionsPresetStores)
	if _, ok := p[marotte.KeySessionID]; ok {
		t.Errorf("presets/stores params carry a sessionId, which KAS refuses: %+v", p)
	}
	if len(p) != 1 {
		t.Errorf("presets/stores params = %+v, want only workspaceRoots", p)
	}
	roots, _ := json.Marshal(p["workspaceRoots"])
	if want, _ := json.Marshal([]string{f.work, f.extra}); string(roots) != string(want) {
		t.Errorf("workspaceRoots = %s, want the work dir then the additional directory %s", roots, want)
	}
	if len(stores) != 3 || stores[0].Scope != "user" || stores[1].File != f.work+"/.kiro/settings/permissions.yaml" ||
		len(stores[1].Read.Presets) != 1 || stores[1].Read.Presets[0].Status != "applies" ||
		stores[2].Read.OK {
		t.Errorf("stores = %+v, want the three rows as KAS sent them", stores)
	}
}

func TestPresetStores_AnEmptyReplyIsNoRowsAndABadOneAnError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroPermissionsPresetStores: json.RawMessage(`{}`)}
	stores, err := h.config.PresetStores(t.Context())
	if err != nil || stores == nil || len(stores) != 0 {
		t.Errorf("PresetStores = %+v, %v; want no rows and no error", stores, err)
	}
	br.callResults = map[string]json.RawMessage{methodKiroPermissionsPresetStores: json.RawMessage(`{"stores":{}}`)}
	if _, err := h.config.PresetStores(t.Context()); err == nil {
		t.Error("PresetStores decoded a malformed reply as no rows; the pill would hide a saved grant silently")
	}
}
