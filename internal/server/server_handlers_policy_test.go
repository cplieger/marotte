package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

type fakePolicy struct {
	rules       []marotte.PolicyRule
	listErr     error
	explain     *marotte.PolicyExplainResult
	explainErr  error
	explainReqs []marotte.PolicyExplainRequest
}

func (f *fakePolicy) PolicyList(_ context.Context, _ string) ([]marotte.PolicyRule, error) {
	return f.rules, f.listErr
}

func (f *fakePolicy) PolicyExplain(_ context.Context, req marotte.PolicyExplainRequest) (*marotte.PolicyExplainResult, error) {
	f.explainReqs = append(f.explainReqs, req)
	return f.explain, f.explainErr
}

func postRules(t *testing.T, s *Server, body policyRuleBody) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/rules", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyRules(rec, req)
	return rec
}

func TestPolicyViewLive(t *testing.T) {
	f := &fakePolicy{rules: []marotte.PolicyRule{
		{Capability: "fs_write", Effect: "allow", Scope: "user", Source: "/x/permissions.yaml"},
	}}
	s := &Server{policy: f, workDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", http.NoBody)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got marotte.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Rules) != 1 || got.Rules[0].Capability != "fs_write" {
		t.Errorf("view = %+v", got)
	}
	if len(got.WritableScopes) != 2 || len(got.Capabilities) == 0 {
		t.Errorf("view metadata = %+v", got)
	}
}

func TestPolicyViewFileFallback(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	if err := policyfile.Save(t.Context(), up, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "shell", Effect: "deny", Match: []string{"sudo *"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{policy: &fakePolicy{listErr: context.DeadlineExceeded}, workDir: work}
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", http.NoBody)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)
	var got marotte.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Available {
		t.Error("Available should be false on fallback")
	}
	if len(got.Rules) != 1 || got.Rules[0].Capability != "shell" || got.Rules[0].Scope != "user" {
		t.Errorf("fallback rules = %+v", got.Rules)
	}
}

func TestPolicyRuleAddDefaultsToAsk(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}
	// No effect → defaults to ask, never allow.
	rec := postRules(t, s, policyRuleBody{Op: "add", Scope: "workspace", Capability: "fs_write", Match: []string{"src/**"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	wp, _ := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	f, err := policyfile.Load(wp)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Effect != "ask" {
		t.Errorf("written rule = %+v, want effect=ask", f.Rules)
	}
}

func TestPolicyRuleAddExplicitAllow(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}
	rec := postRules(t, s, policyRuleBody{Op: "add", Scope: "user", Capability: "web_fetch", Effect: "allow"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	f, _ := policyfile.Load(up)
	if len(f.Rules) != 1 || f.Rules[0].Effect != "allow" || f.Rules[0].Capability != "web_fetch" {
		t.Errorf("written rule = %+v", f.Rules)
	}
}

func TestPolicyRuleInvalidScopeRejected(t *testing.T) {
	s := &Server{workDir: t.TempDir()}
	rec := postRules(t, s, policyRuleBody{Op: "add", Scope: "agent", Capability: "shell", Effect: "ask"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (agent scope not writable)", rec.Code)
	}
}

// TestPolicyRuleUnrecognisedCapabilityRoundTrips pins that an unrecognised capability is
// written verbatim: KAS's loader is the authority and skips it non-fatally, reporting it on
// _kiro/policy/changed's errors array.
func TestPolicyRuleUnrecognisedCapabilityRoundTrips(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}

	// "hooks" is the real case: an upstream security report asked for it.
	rec := postRules(t, s, policyRuleBody{
		Op: "add", Scope: "user", Capability: "hooks", Effect: "deny",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (KAS is the authority on the vocabulary)", rec.Code)
	}
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	f, err := policyfile.Load(up)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "hooks" || f.Rules[0].Effect != "deny" {
		t.Errorf("written rule = %+v, want the unrecognised capability persisted verbatim", f.Rules)
	}
}

// TestPolicyRuleMalformedCapabilityRejected pins that an empty or control-character
// capability is still a 400.
func TestPolicyRuleMalformedCapabilityRejected(t *testing.T) {
	for name, capability := range map[string]string{
		"empty":             "",
		"control character": "fs_\x00write",
		"newline":           "fs_write\nshell",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			s := &Server{workDir: t.TempDir()}
			rec := postRules(t, s, policyRuleBody{
				Op: "add", Scope: "user", Capability: capability, Effect: "ask",
			})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (malformed, not merely unrecognised)", rec.Code)
			}
		})
	}
}

// TestPickerCapabilities_UnionsInWhatTheRulesUse pins that a capability any rule uses
// becomes selectable.
func TestPickerCapabilities_UnionsInWhatTheRulesUse(t *testing.T) {
	base := policyfile.Capabilities()
	if slices.Contains(base, "hooks") {
		t.Fatal("test assumes 'hooks' is absent from the suggested set")
	}

	got := pickerCapabilities([]marotte.PolicyRule{
		{Capability: "hooks", Effect: "deny", Scope: "kiro"},
		// Already suggested: must not be duplicated.
		{Capability: "shell", Effect: "ask", Scope: "user"},
		// Repeat of the new one: must not be duplicated either.
		{Capability: "hooks", Effect: "ask", Scope: "user"},
		// An empty capability is not an offer.
		{Capability: "", Effect: "ask", Scope: "user"},
	})

	if !slices.Contains(got, "hooks") {
		t.Errorf("picker = %v, want it to offer 'hooks'", got)
	}
	if len(got) != len(base)+1 {
		t.Errorf("picker has %d entries, want %d (one new capability, no duplicates)", len(got), len(base)+1)
	}
	if !slices.IsSorted(got) {
		t.Errorf("picker not sorted: %v", got)
	}
	for _, c := range base {
		if !slices.Contains(got, c) {
			t.Errorf("picker dropped the suggested capability %q", c)
		}
	}
}

// TestPickerCapabilities_NoRulesIsTheSuggestedSet pins a non-EMPTY result for no rules:
// slices.Equal(nil, []string{}) is true, and an empty set would marshal as null.
func TestPickerCapabilities_NoRulesIsTheSuggestedSet(t *testing.T) {
	got := pickerCapabilities(nil)
	if len(got) == 0 {
		t.Fatal("picker is empty; the capabilities wire field would marshal as null, not []")
	}
	if !slices.Equal(got, policyfile.Capabilities()) {
		t.Errorf("picker = %v, want the suggested set", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("picker = %v, want one alphabetical list", got)
	}
}

func TestPolicyRuleRemoveDenyRequiresConfirm(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	wp, _ := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	if err := policyfile.Save(t.Context(), wp, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{workDir: work}

	// Without confirm → 409, rule stays.
	rec := postRules(t, s, policyRuleBody{Op: "remove", Scope: "workspace", Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (deny removal needs confirm)", rec.Code)
	}
	if f, _ := policyfile.Load(wp); len(f.Rules) != 1 {
		t.Errorf("deny rule removed without confirm; rules=%d", len(f.Rules))
	}

	// With confirm → 200, rule gone.
	rec = postRules(t, s, policyRuleBody{Op: "remove", Scope: "workspace", Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}, Confirm: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed remove status = %d, want 200", rec.Code)
	}
	if f, _ := policyfile.Load(wp); len(f.Rules) != 0 {
		t.Errorf("deny rule not removed with confirm; rules=%d", len(f.Rules))
	}
}

func TestPolicyRuleUnknownOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: t.TempDir()}
	rec := postRules(t, s, policyRuleBody{Op: "frobnicate", Scope: "user", Capability: "shell", Effect: "ask"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown op)", rec.Code)
	}
}

func TestPolicyExplainProxies(t *testing.T) {
	f := &fakePolicy{explain: &marotte.PolicyExplainResult{Capability: "fs_write", Effect: "ask"}}
	s := &Server{policy: f}
	b, _ := json.Marshal(marotte.PolicyExplainRequest{Capability: "fs_write", Resource: "/etc/hosts"})
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/explain", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyExplain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got marotte.PolicyExplainResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Effect != "ask" {
		t.Errorf("explain effect = %q", got.Effect)
	}
	if len(f.explainReqs) != 1 || f.explainReqs[0].Resource != "/etc/hosts" {
		t.Errorf("explain reqs = %+v", f.explainReqs)
	}
}

func TestPolicyExplainRequiresTarget(t *testing.T) {
	s := &Server{policy: &fakePolicy{}}
	b, _ := json.Marshal(marotte.PolicyExplainRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/explain", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyExplain(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no capability/tool_id)", rec.Code)
	}
}

func TestPolicyRuleUpdate(t *testing.T) {
	seed := func(t *testing.T) (s *Server, wp string) {
		t.Helper()
		home := t.TempDir()
		work := t.TempDir()
		t.Setenv("HOME", home)
		wp, _ = policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
		if err := policyfile.Save(t.Context(), wp, &policyfile.File{
			Rules: []policyfile.Rule{
				{Capability: "shell", Effect: "ask", Match: []string{"rm *"}},
				{Capability: "fs_read", Effect: "allow", Match: []string{"src/**"}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		return &Server{workDir: work}, wp
	}

	t.Run("narrowing_update_needs_no_confirm", func(t *testing.T) {
		s, wp := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "deny", Match: []string{"rm *"},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		f, _ := policyfile.Load(wp)
		if len(f.Rules) != 2 || f.Rules[0].Effect != "deny" {
			t.Errorf("rules = %+v, want shell rule updated in place at index 0", f.Rules)
		}
	})

	t.Run("widening_update_requires_confirm", func(t *testing.T) {
		s, wp := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "allow", Match: []string{"rm *"},
		})
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (widening needs confirm)", rec.Code)
		}
		if f, _ := policyfile.Load(wp); f.Rules[0].Effect != "ask" {
			t.Error("rule widened without confirm")
		}

		rec = postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "allow", Match: []string{"rm *"}, Confirm: true,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("confirmed widening status = %d, want 200", rec.Code)
		}
		if f, _ := policyfile.Load(wp); f.Rules[0].Effect != "allow" {
			t.Error("confirmed widening did not apply")
		}
	})

	t.Run("replay_when_target_already_exists_is_ok", func(t *testing.T) {
		s, wp := seed(t)
		body := policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "deny", Match: []string{"rm *"},
		}
		if rec := postRules(t, s, body); rec.Code != http.StatusOK {
			t.Fatalf("first update = %d", rec.Code)
		}
		// Retry with the same body: old rule is gone, target exists → 200.
		if rec := postRules(t, s, body); rec.Code != http.StatusOK {
			t.Fatalf("replayed update = %d, want 200 (idempotent)", rec.Code)
		}
		f, _ := policyfile.Load(wp)
		if len(f.Rules) != 2 {
			t.Errorf("replay duplicated rules: %+v", f.Rules)
		}
	})

	t.Run("missing_rule_is_404", func(t *testing.T) {
		s, _ := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "deny", Match: []string{"never-added *"},
		})
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("invalid_new_effect_is_400", func(t *testing.T) {
		s, _ := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "frobnicate", Match: []string{"rm *"},
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

func TestPolicyExplainShellRequiresResource(t *testing.T) {
	// KAS has no command-independent shell decision, so the handler refuses up front.
	f := &fakePolicy{explain: &marotte.PolicyExplainResult{Capability: "shell", Effect: "ask"}}
	s := &Server{policy: f}
	b, _ := json.Marshal(marotte.PolicyExplainRequest{Capability: "shell", Resource: "   "})
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/explain", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyExplain(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (shell needs a resource)", rec.Code)
	}
	if len(f.explainReqs) != 0 {
		t.Errorf("explain forwarded despite missing resource: %+v", f.explainReqs)
	}
}

// TestPolicyRuleUpdate_UnchangedEffectNeedsNoConfirm pins that a no-op update needs no
// confirm, or the gate would stop meaning anything.
func TestPolicyRuleUpdate_UnchangedEffectNeedsNoConfirm(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	wp, _ := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	if err := policyfile.Save(t.Context(), wp, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "shell", Effect: "ask", Match: []string{"rm *"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{workDir: work}

	rec := postRules(t, s, policyRuleBody{
		Op: "update", Scope: "workspace", Capability: "shell",
		Effect: "ask", NewEffect: "ask", Match: []string{"rm *"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; an unchanged effect widens nothing. body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestPolicyRulesFromFiles_ScopeSelectsItsOwnFile pins that a scoped read stays in its file.
func TestPolicyRulesFromFiles_ScopeSelectsItsOwnFile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	wp, _ := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	if err := policyfile.Save(t.Context(), up, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "web_fetch", Effect: "allow"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := policyfile.Save(t.Context(), wp, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "fs_write", Effect: "deny", Match: []string{"src/**"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{workDir: work}

	tests := []struct {
		scope          string
		wantCapability string
	}{
		{scope: policyfile.ScopeUser, wantCapability: "web_fetch"},
		{scope: policyfile.ScopeWorkspace, wantCapability: "fs_write"},
	}
	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			got := s.policyRulesFromFiles(tt.scope)
			if len(got) != 1 {
				t.Fatalf("policyRulesFromFiles(%q) returned %d rules, want 1: %+v", tt.scope, len(got), got)
			}
			if got[0].Capability != tt.wantCapability {
				t.Errorf("policyRulesFromFiles(%q) capability = %q, want %q",
					tt.scope, got[0].Capability, tt.wantCapability)
			}
			if got[0].Scope != tt.scope {
				t.Errorf("policyRulesFromFiles(%q) scope = %q, want %q", tt.scope, got[0].Scope, tt.scope)
			}
		})
	}
}

// TestPolicyView_DropsIdenticalDuplicatesFromKAS pins the dedupe against the measured reply
// shape (kiro-cli 2.19.0: one rule reported ten times).
func TestPolicyView_DropsIdenticalDuplicatesFromKAS(t *testing.T) {
	// The rule as it actually arrived, ten times.
	dup := marotte.PolicyRule{
		Capability: "all",
		Effect:     "allow",
		Scope:      policyfile.ScopeWorkspace,
		Source:     "/config/home/.kiro/workspace-roots/c52ddf65534b7b46/permissions.yaml",
		Match:      []string{"*"},
	}
	rules := []marotte.PolicyRule{
		{Capability: "fs_write", Effect: "deny", Scope: "kiro", Source: "kiro-scope", Match: []string{".kiro/settings/"}},
	}
	for range 10 {
		rules = append(rules, dup)
	}
	rules = append(rules, marotte.PolicyRule{
		Capability: "fs_read", Effect: "allow", Scope: "agent", Source: "agent-profile", Match: []string{"./**"},
	})

	s := &Server{policy: &fakePolicy{rules: rules}, workDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", http.NoBody)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)

	var got marotte.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 3 {
		t.Fatalf("Rules length = %d, want 3 (one per distinct rule); got %+v", len(got.Rules), got.Rules)
	}
	// First-arrival order, so rows do not reshuffle between reads.
	wantCaps := []string{"fs_write", "all", "fs_read"}
	for i, want := range wantCaps {
		if got.Rules[i].Capability != want {
			t.Errorf("Rules[%d].Capability = %q, want %q", i, got.Rules[i].Capability, want)
		}
	}
}

// TestDedupePolicyRules_KeepsTheSameRuleInTwoScopes pins that one rule in two files stays
// two rows.
func TestDedupePolicyRules_KeepsTheSameRuleInTwoScopes(t *testing.T) {
	rules := []marotte.PolicyRule{
		{Capability: "shell", Effect: "ask", Scope: policyfile.ScopeUser, Source: "/home/u/.kiro/settings/permissions.yaml", Match: []string{"rm *"}},
		{Capability: "shell", Effect: "ask", Scope: policyfile.ScopeWorkspace, Source: "/w/.kiro/settings/permissions.yaml", Match: []string{"rm *"}},
	}
	if got := dedupePolicyRules(rules); len(got) != 2 {
		t.Errorf("dedupePolicyRules kept %d of 2 cross-scope rules; got %+v", len(got), got)
	}
}

func TestDedupePolicyRules_EmptyStaysNonNil(t *testing.T) {
	got := dedupePolicyRules(nil)
	if got == nil {
		t.Error("dedupePolicyRules(nil) = nil, want an empty slice (the wire field must not degrade to null)")
	}
	if len(got) != 0 {
		t.Errorf("dedupePolicyRules(nil) = %+v, want empty", got)
	}
}

// TestPolicyRuleKey_GlobContentCannotForgeACollision pins the length-prefixed key.
func TestPolicyRuleKey_GlobContentCannotForgeACollision(t *testing.T) {
	cases := map[string][2]marotte.PolicyRule{
		"split differs": {
			{Capability: "shell", Effect: "allow", Match: []string{"a", "b"}},
			{Capability: "shell", Effect: "allow", Match: []string{"a:b"}},
		},
		"separator inside one glob": {
			{Capability: "shell", Effect: "allow", Match: []string{"a|b"}},
			{Capability: "shell", Effect: "allow", Match: []string{"a", "b"}},
		},
		"match vs exclude": {
			{Capability: "shell", Effect: "allow", Match: []string{"x"}},
			{Capability: "shell", Effect: "allow", Exclude: []string{"x"}},
		},
		"glob order": {
			{Capability: "shell", Effect: "allow", Match: []string{"a", "b"}},
			{Capability: "shell", Effect: "allow", Match: []string{"b", "a"}},
		},
		"scope differs": {
			{Capability: "all", Effect: "allow", Scope: "user"},
			{Capability: "all", Effect: "allow", Scope: "workspace"},
		},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			if a, b := policyRuleKey(&pair[0]), policyRuleKey(&pair[1]); a == b {
				t.Errorf("policyRuleKey collided on distinct rules: %q", a)
			}
			if got := dedupePolicyRules([]marotte.PolicyRule{pair[0], pair[1]}); len(got) != 2 {
				t.Errorf("dedupePolicyRules merged distinct rules: %+v", got)
			}
		})
	}
}

func TestPolicyRuleKey_IdenticalRulesAgree(t *testing.T) {
	r := marotte.PolicyRule{
		Capability: "all", Effect: "allow", Scope: "workspace", Source: "/w/p.yaml",
		Match: []string{"*"}, Exclude: []string{"secret/**"},
	}
	// A separate value with equal fields, not the same variable.
	same := marotte.PolicyRule{
		Capability: "all", Effect: "allow", Scope: "workspace", Source: "/w/p.yaml",
		Match: []string{"*"}, Exclude: []string{"secret/**"},
	}
	if policyRuleKey(&r) != policyRuleKey(&same) {
		t.Errorf("policyRuleKey disagreed on equal rules:\n %q\n %q", policyRuleKey(&r), policyRuleKey(&same))
	}
}
