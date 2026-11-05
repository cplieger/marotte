package policyfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/workspace"
	"go.yaml.in/yaml/v3"
)

func TestPathFor(t *testing.T) {
	home := "/home/u/.kiro"
	wd := "/work/space"
	up, err := PathFor(ScopeUser, Roots{KiroHome: home, WorkDir: wd})
	if err != nil || up != filepath.Join(home, "settings", "permissions.yaml") {
		t.Errorf("user path = %q, err = %v", up, err)
	}
	wp, err := PathFor(ScopeWorkspace, Roots{KiroHome: home, WorkDir: wd})
	wantWP := filepath.Join(home, "workspace-roots", workspace.SessionsBucket(wd), "permissions.yaml")
	if err != nil || wp != wantWP {
		t.Errorf("workspace path = %q, want %q, err = %v", wp, wantWP, err)
	}
	if _, err := PathFor("agent", Roots{KiroHome: home, WorkDir: wd}); err != errInvalidScope {
		t.Errorf("agent scope err = %v, want ErrInvalidScope", err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if f == nil || len(f.Rules) != 0 {
		t.Errorf("missing file should yield empty File, got %+v", f)
	}
}

func TestLoadBlockYAML(t *testing.T) {
	// The format a hand-editing user (or KAS) writes.
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	body := "rules:\n  - capability: fs_write\n    effect: allow\n    match:\n      - \"src/**\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "fs_write" || f.Rules[0].Effect != "allow" ||
		len(f.Rules[0].Match) != 1 || f.Rules[0].Match[0] != "src/**" {
		t.Errorf("parsed rules = %+v", f.Rules)
	}
}

func TestLoadJSONInYAML(t *testing.T) {
	// JSON is valid YAML 1.2 — Load must accept marotte's own writes even if
	// they were emitted as JSON.
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte(`{"rules":[{"capability":"shell","effect":"ask"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "shell" || f.Rules[0].Effect != "ask" {
		t.Errorf("parsed rules = %+v", f.Rules)
	}
}

func TestLoadEmptyIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil || len(f.Rules) != 0 {
		t.Errorf("empty file: rules=%+v err=%v", f, err)
	}
}

func TestLoadMalformedErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte("rules: [ this is : not : valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Errorf("malformed file should error (never silently clobber)")
	}
}

func TestSaveRoundtripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "permissions.yaml") // sub dir auto-created
	in := &File{Rules: []Rule{
		{Capability: "fs_write", Effect: "ask", Match: []string{"src/**"}},
		{Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}},
	}}
	if err := Save(t.Context(), path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600 (policy is sensitive)", fi.Mode().Perm())
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(out.Rules) != 2 || out.Rules[0].Capability != "fs_write" || out.Rules[1].Effect != "deny" {
		t.Errorf("roundtrip rules = %+v", out.Rules)
	}
}

func TestSanitizeRule(t *testing.T) {
	if _, err := SanitizeRule(&Rule{Capability: "shell", Effect: "yolo"}); err == nil {
		t.Error("invalid effect should error")
	}
	if _, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: []string{strings.Repeat("a", maxPatternLen+1)}}); err == nil {
		t.Error("over-long pattern should error")
	}
	if _, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: []string{"a\x00b"}}); err == nil {
		t.Error("control char should error")
	}
	// Trims + de-dups + drops empties.
	got, err := SanitizeRule(&Rule{Capability: "fs_read", Effect: "allow", Match: []string{" a ", "a", "", "b"}})
	if err != nil {
		t.Fatalf("SanitizeRule: %v", err)
	}
	if len(got.Match) != 2 || got.Match[0] != "a" || got.Match[1] != "b" {
		t.Errorf("sanitized match = %v, want [a b]", got.Match)
	}
}

// TestSanitizeRule_AcceptsEvaluate admits the effect KAS accepts at user and workspace scope, so a
// Custom user keeps an evaluate rule Customize copied and can edit or remove it.
func TestSanitizeRule_AcceptsEvaluate(t *testing.T) {
	got, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectEvaluate})
	if err != nil || got.Effect != EffectEvaluate {
		t.Fatalf("SanitizeRule(all evaluate) = %+v, %v; want the rule kept", got, err)
	}
	var f File
	if added, err := f.Upsert(&got); !added || err != nil {
		t.Fatalf("Upsert(all evaluate) = %v, %v on an empty file", added, err)
	}
	ask := Rule{Capability: "all", Effect: EffectAsk}
	if !f.ReplaceEffect(&got, EffectAsk) || len(f.Rules) != 1 || f.Rules[0].Effect != EffectAsk {
		t.Fatalf("ReplaceEffect(evaluate -> ask) left %+v", f.Rules)
	}
	if !f.ReplaceEffect(&ask, EffectEvaluate) || f.Rules[0].Effect != EffectEvaluate {
		t.Fatalf("ReplaceEffect(ask -> evaluate) left %+v", f.Rules)
	}
	if !f.Remove(&got) || len(f.Rules) != 0 {
		t.Errorf("Remove(all evaluate) left %+v", f.Rules)
	}
}

// TestSanitizeRule_RefusesAnAllEmptyPatternList closes the widening side of the
// shape check. A nil Match serialises with no `match` key (yaml:"match,omitempty"),
// which KAS reads as `**`, so accepting `match:[""]` ("matches nothing") would
// write the broadest grant the file can express.
func TestSanitizeRule_RefusesAnAllEmptyPatternList(t *testing.T) {
	cases := map[string][]string{
		"one empty string":       {""},
		"whitespace only":        {"   "},
		"several, all blank":     {"", " ", "\t"},
		"a comma-split leftover": {"", ""},
	}
	for name, patterns := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectAllow, Match: patterns}); !errors.Is(err, errPatternEmpty) {
				t.Errorf("match %q: err = %v, want ErrPatternEmpty", patterns, err)
			}
			if _, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectAllow, Exclude: patterns}); !errors.Is(err, errPatternEmpty) {
				t.Errorf("exclude %q: err = %v, want ErrPatternEmpty", patterns, err)
			}
		})
	}
}

// TestSanitizeRule_BareRuleStaysWritable is the other half of the condition, and
// it is the assertion that fails if the refusal is ever keyed on the OUTPUT being
// empty instead of on the caller having supplied something. An ABSENT match list
// is legitimate: it is the broadest grant a rule can state.
func TestSanitizeRule_BareRuleStaysWritable(t *testing.T) {
	got, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectAllow})
	if err != nil {
		t.Fatalf("SanitizeRule(bare rule) = %v, want it written", err)
	}
	if got.Match != nil || got.Exclude != nil {
		t.Errorf("sanitized = %+v, want nil Match and nil Exclude", got)
	}
}

// TestSanitizeRule_ForwardsUnrecognisedCapability — the capability vocabulary is
// KAS's (SanitizeRule's doc comment owns why), so refusing here would stop marotte
// writing a rule for any capability newer than a hand-copied list.
func TestSanitizeRule_ForwardsUnrecognisedCapability(t *testing.T) {
	// "hooks" is a real capability newer than any hand-copied list.
	for _, capability := range []string{"hooks", "some_future_capability", "nope"} {
		got, err := SanitizeRule(&Rule{Capability: capability, Effect: "deny"})
		if err != nil {
			t.Errorf("SanitizeRule(%q) = %v, want it forwarded to KAS", capability, err)
			continue
		}
		if got.Capability != capability {
			t.Errorf("capability = %q, want %q verbatim", got.Capability, capability)
		}
	}
}

// TestSanitizeRule_RejectsMalformedCapability — a vocabulary check is KAS's; a
// SHAPE check is marotte's, same class
// as the pattern checks. None of these is a capability KAS could ever have, so
// writing one only puts a rule in a security policy file that the user has to
// hand-edit out.
func TestSanitizeRule_RejectsMalformedCapability(t *testing.T) {
	cases := map[string]string{
		"empty":                  "",
		"whitespace only":        "   ",
		"a control character":    "fs_\x00write",
		"a newline (YAML break)": "fs_write\nshell",
		"over the length cap":    strings.Repeat("a", maxCapabilityLen+1),
	}
	for name, capability := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SanitizeRule(&Rule{Capability: capability, Effect: "ask"}); !errors.Is(err, errCapabilityShape) {
				t.Errorf("err = %v, want ErrCapabilityShape", err)
			}
		})
	}
}

// TestSanitizeRule_TrimsCapability: the trim has to happen before the rule is
// written, because signature is a byte comparison — " shell" and "shell" would
// otherwise be two distinct rules the user cannot tell apart in the editor.
func TestSanitizeRule_TrimsCapability(t *testing.T) {
	got, err := SanitizeRule(&Rule{Capability: "  shell  ", Effect: "ask"})
	if err != nil {
		t.Fatalf("SanitizeRule: %v", err)
	}
	if got.Capability != "shell" {
		t.Errorf("capability = %q, want %q", got.Capability, "shell")
	}
}

// TestCapabilities_IsASuggestionNotAnAllowlist: the two are now decoupled, and a
// test that only checked membership would not notice them being re-coupled.
func TestCapabilities_IsASuggestionNotAnAllowlist(t *testing.T) {
	suggested := Capabilities()
	if !slices.IsSorted(suggested) {
		t.Errorf("Capabilities() not sorted: %v", suggested)
	}
	if slices.Contains(suggested, "hooks") {
		t.Fatal("test assumes 'hooks' is NOT in the suggested set; pick another absent capability")
	}
	if _, err := SanitizeRule(&Rule{Capability: "hooks", Effect: "deny"}); err != nil {
		t.Errorf("a capability outside the suggested set must still be writable: %v", err)
	}
}

// TestSanitizeRule_AcceptsEveryBoundAtItsEdge states the near side of each shape
// bound the cases above only state the far side of.
//
// All three caps are inclusive, and the difference matters in one direction only:
// a rule refused here is a rule the editor cannot write, so a bound that is one
// short silently caps what the user is allowed to express in their own security
// policy, with an error naming a limit their input did not actually exceed.
func TestSanitizeRule_AcceptsEveryBoundAtItsEdge(t *testing.T) {
	t.Run("a capability exactly at the length cap", func(t *testing.T) {
		capability := strings.Repeat("a", maxCapabilityLen)
		got, err := SanitizeRule(&Rule{Capability: capability, Effect: "ask"})
		if err != nil {
			t.Fatalf("SanitizeRule(capability of %d bytes) = %v, want nil", maxCapabilityLen, err)
		}
		if got.Capability != capability {
			t.Errorf("capability = %q, want it verbatim", got.Capability)
		}
	})

	t.Run("a pattern exactly at the length cap", func(t *testing.T) {
		pattern := strings.Repeat("a", maxPatternLen)
		got, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: []string{pattern}})
		if err != nil {
			t.Fatalf("SanitizeRule(pattern of %d bytes) = %v, want nil", maxPatternLen, err)
		}
		if len(got.Match) != 1 || got.Match[0] != pattern {
			t.Errorf("match = %v, want the one %d-byte pattern verbatim", got.Match, maxPatternLen)
		}
	})

	t.Run("exactly the maximum number of match entries", func(t *testing.T) {
		match := make([]string, 0, maxMatchEntries)
		for i := range maxMatchEntries {
			match = append(match, "p"+strconv.Itoa(i))
		}
		got, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: match})
		if err != nil {
			t.Fatalf("SanitizeRule(%d match entries) = %v, want nil", maxMatchEntries, err)
		}
		if len(got.Match) != maxMatchEntries {
			t.Errorf("match entries = %d, want %d", len(got.Match), maxMatchEntries)
		}
	})
}

func TestUpsertDedupAndRemove(t *testing.T) {
	f := &File{Rules: []Rule{}}
	r := Rule{Capability: "web_fetch", Effect: "allow"}
	changed, err := f.Upsert(&r)
	if err != nil || !changed || len(f.Rules) != 1 {
		t.Fatalf("first upsert: changed=%v err=%v rules=%d", changed, err, len(f.Rules))
	}
	// Identical rule (by signature) → no-op.
	changed, _ = f.Upsert(&Rule{Capability: "web_fetch", Effect: "allow"})
	if changed || len(f.Rules) != 1 {
		t.Errorf("dup upsert changed=%v rules=%d, want no-op", changed, len(f.Rules))
	}
	// Different effect → distinct rule.
	changed, _ = f.Upsert(&Rule{Capability: "web_fetch", Effect: "deny"})
	if !changed || len(f.Rules) != 2 {
		t.Errorf("distinct upsert changed=%v rules=%d", changed, len(f.Rules))
	}
	if !f.Remove(&Rule{Capability: "web_fetch", Effect: "allow"}) || len(f.Rules) != 1 {
		t.Errorf("remove failed; rules=%d", len(f.Rules))
	}
	if f.Remove(&Rule{Capability: "web_fetch", Effect: "allow"}) {
		t.Error("removing an absent rule should return false")
	}
}

// TestUpsert_RefusesAFullFile pins the per-file rule ceiling at the count it
// names.
//
// The cap is on the file as it STANDS, so a file already holding the maximum is
// full and the next append is refused — off by one here and the ceiling is a
// number the file can always exceed by exactly one rule, which is not a ceiling
// anyone can reason about when the file is a security policy KAS loads.
func TestUpsert_RefusesAFullFile(t *testing.T) {
	full := &File{Rules: make([]Rule, 0, maxRulesPerFile)}
	for i := range maxRulesPerFile {
		full.Rules = append(full.Rules, Rule{Capability: "shell", Effect: "ask", Match: []string{"c" + strconv.Itoa(i)}})
	}

	changed, err := full.Upsert(&Rule{Capability: "shell", Effect: "deny", Match: []string{"one-too-many"}})
	if !errors.Is(err, errTooManyRules) {
		t.Errorf("Upsert() into a file of %d rules error = %v, want ErrTooManyRules", maxRulesPerFile, err)
	}
	if changed || len(full.Rules) != maxRulesPerFile {
		t.Errorf("Upsert() into a full file changed=%v rules=%d, want false and %d",
			changed, len(full.Rules), maxRulesPerFile)
	}

	// And the last slot below the cap is still usable, so the refusal above is
	// the ceiling rather than an off-by-one below it.
	nearlyFull := &File{Rules: full.Rules[:maxRulesPerFile-1]}
	changed, err = nearlyFull.Upsert(&Rule{Capability: "shell", Effect: "deny", Match: []string{"last-slot"}})
	if err != nil || !changed || len(nearlyFull.Rules) != maxRulesPerFile {
		t.Errorf("Upsert() into a file of %d rules changed=%v err=%v rules=%d, want true, nil and %d",
			maxRulesPerFile-1, changed, err, len(nearlyFull.Rules), maxRulesPerFile)
	}
}

func TestSignatureOrderIndependent(t *testing.T) {
	a := Rule{Capability: "fs_read", Effect: "ask", Match: []string{"x", "y"}}
	b := Rule{Capability: "fs_read", Effect: "ask", Match: []string{"y", "x"}}
	if signature(&a) != signature(&b) {
		t.Error("signature must be order-independent for match globs")
	}
}

// KAS keys a rule by a JSON string of its fields, so a pattern holding a comma is its own rule.
func TestSignatureTellsACommaPatternFromTwoPatterns(t *testing.T) {
	pairs := map[string][2]Rule{
		"match": {
			{Capability: "shell", Effect: "deny", Match: []string{"a,b"}},
			{Capability: "shell", Effect: "deny", Match: []string{"a", "b"}},
		},
		"exclude": {
			{Capability: "shell", Effect: "deny", Exclude: []string{"a,b"}},
			{Capability: "shell", Effect: "deny", Exclude: []string{"a", "b"}},
		},
		"match into exclude": {
			{Capability: "shell", Effect: "deny", Match: []string{"a|x:b"}},
			{Capability: "shell", Effect: "deny", Match: []string{"a"}, Exclude: []string{"b"}},
		},
	}
	for name, p := range pairs {
		t.Run(name, func(t *testing.T) {
			if signature(&p[0]) == signature(&p[1]) {
				t.Errorf("%+v and %+v share a signature", p[0], p[1])
			}
		})
	}
}

// --- ReplaceEffect (in-place effect editing) ---

func TestReplaceEffect(t *testing.T) {
	base := func() *File {
		return &File{Rules: []Rule{
			{Capability: "fs_write", Effect: "deny", Match: []string{"**/.git/**"}},
			{Capability: "shell", Effect: "ask", Match: []string{"rm *"}},
			{Capability: "web_fetch", Effect: "allow"},
		}}
	}

	t.Run("changes_effect_in_place_preserving_position", func(t *testing.T) {
		f := base()
		old := Rule{Capability: "shell", Effect: "ask", Match: []string{"rm *"}}
		if !f.ReplaceEffect(&old, "deny") {
			t.Fatal("ReplaceEffect = false, want true")
		}
		if len(f.Rules) != 3 {
			t.Fatalf("rules = %d, want 3 (in-place, not remove+append)", len(f.Rules))
		}
		if f.Rules[1].Effect != "deny" || f.Rules[1].Match[0] != "rm *" {
			t.Errorf("rules[1] = %+v, want the shell rule updated at index 1", f.Rules[1])
		}
	})

	t.Run("absent_rule_returns_false", func(t *testing.T) {
		f := base()
		old := Rule{Capability: "shell", Effect: "allow", Match: []string{"rm *"}}
		if f.ReplaceEffect(&old, "deny") {
			t.Error("ReplaceEffect on absent rule = true, want false")
		}
	})

	t.Run("same_effect_is_noop", func(t *testing.T) {
		f := base()
		old := Rule{Capability: "web_fetch", Effect: "allow"}
		if f.ReplaceEffect(&old, "allow") {
			t.Error("ReplaceEffect to same effect = true, want false")
		}
	})

	t.Run("collapses_into_existing_duplicate", func(t *testing.T) {
		f := &File{Rules: []Rule{
			{Capability: "shell", Effect: "ask", Match: []string{"rm *"}},
			{Capability: "shell", Effect: "deny", Match: []string{"rm *"}},
		}}
		old := Rule{Capability: "shell", Effect: "ask", Match: []string{"rm *"}}
		if !f.ReplaceEffect(&old, "deny") {
			t.Fatal("ReplaceEffect = false, want true (old removed, duplicate kept)")
		}
		if len(f.Rules) != 1 || f.Rules[0].Effect != "deny" {
			t.Errorf("rules = %+v, want single deny rule (no duplicate)", f.Rules)
		}
	})
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readRaw decodes the saved bytes generically, the way KAS's parser sees them.
func readRaw(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("saved file does not parse: %v\n%s", err, data)
	}
	return doc
}

func rulesIn(t *testing.T, doc map[string]any) int {
	t.Helper()
	rules, ok := doc["rules"].([]any)
	if !ok {
		t.Fatalf("rules = %#v, want a list", doc["rules"])
	}
	return len(rules)
}

func TestLoadSave_KeepsTheKeysFileDoesNotEdit(t *testing.T) {
	// policies is KAS's preset list; workspaceTrusted and workspaceTrustCase are the TUI's
	// trust record in the workspace file.
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	writeFixture(t, path, "policies:\n  - dev-shell\n"+
		"rules:\n  - capability: shell\n    effect: ask\n"+
		"workspaceTrusted: true\nworkspaceTrustCase: \"/Work/Space\"\n")
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := f.Upsert(&Rule{Capability: "web_fetch", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(t.Context(), path, f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	doc := readRaw(t, path)
	if got, ok := doc["policies"].([]any); !ok || !slices.Equal(got, []any{"dev-shell"}) {
		t.Errorf("policies = %#v, want [dev-shell]", doc["policies"])
	}
	if doc["workspaceTrusted"] != true {
		t.Errorf("workspaceTrusted = %#v, want true", doc["workspaceTrusted"])
	}
	if doc["workspaceTrustCase"] != "/Work/Space" {
		t.Errorf("workspaceTrustCase = %#v, want /Work/Space", doc["workspaceTrustCase"])
	}
	if n := rulesIn(t, doc); n != 2 {
		t.Errorf("rules = %d, want 2", n)
	}
}

func TestLoad_RefusesADocumentItCannotRoundTrip(t *testing.T) {
	for name, body := range map[string]string{
		"a list at the top":   "- capability: shell\n  effect: ask\n",
		"a duplicated key":    "policies: [a]\nrules: []\npolicies: [b]\n",
		"a duplicated rules":  "rules: []\nrules:\n  - capability: shell\n    effect: ask\n",
		"a scalar at the top": "allow everything\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "permissions.yaml")
			writeFixture(t, path, body)
			if _, err := Load(path); err == nil {
				t.Error("Load succeeded, want an error so Save never rewrites the file")
			}
		})
	}
}

func TestLoad_ReadsTheJSONSiblingKASWouldRead(t *testing.T) {
	const jsonBody = `{"policies":["read-all"],"rules":[{"capability":"shell","effect":"deny","match":["rm *"]}]}`
	t.Run("yaml absent", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, filepath.Join(dir, jsonFilename), jsonBody)
		yamlPath := filepath.Join(dir, filename)
		f, err := Load(yamlPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(f.Rules) != 1 || f.Rules[0].Effect != "deny" {
			t.Fatalf("rules = %+v, want the .json's deny rule", f.Rules)
		}
		if _, err := f.Upsert(&Rule{Capability: "web_fetch", Effect: "allow"}); err != nil {
			t.Fatal(err)
		}
		if err := Save(t.Context(), yamlPath, f); err != nil {
			t.Fatalf("Save: %v", err)
		}
		doc := readRaw(t, yamlPath)
		if n := rulesIn(t, doc); n != 2 {
			t.Errorf("saved .yaml rules = %d, want the .json's rule plus the new one", n)
		}
		if got, ok := doc["policies"].([]any); !ok || !slices.Equal(got, []any{"read-all"}) {
			t.Errorf("saved .yaml policies = %#v, want the .json's [read-all]", doc["policies"])
		}
	})
	t.Run("yaml blank", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, filepath.Join(dir, filename), "  \n")
		writeFixture(t, filepath.Join(dir, jsonFilename), jsonBody)
		f, err := Load(filepath.Join(dir, filename))
		if err != nil || len(f.Rules) != 1 {
			t.Errorf("Load = %+v, %v, want the .json's one rule", f, err)
		}
	})
	t.Run("yaml present", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, filepath.Join(dir, filename), "rules: []\n")
		writeFixture(t, filepath.Join(dir, jsonFilename), jsonBody)
		f, err := Load(filepath.Join(dir, filename))
		if err != nil || len(f.Rules) != 0 {
			t.Errorf("Load = %+v, %v, want the .yaml's zero rules (it wins over the .json)", f, err)
		}
	})
	t.Run("unparseable json", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, filepath.Join(dir, jsonFilename), `{"rules": [`)
		if _, err := Load(filepath.Join(dir, filename)); err == nil {
			t.Error("Load succeeded, want an error so Save never shadows the .json with an empty .yaml")
		}
	})
}

func TestEditable(t *testing.T) {
	const userFile = "/home/u/.kiro/settings/permissions.yaml"
	for _, tc := range []struct {
		name, scope, source, effect string
		want                        bool
	}{
		{name: "a user file rule", scope: "user", source: userFile, effect: "allow", want: true},
		{name: "a workspace file deny", scope: "workspace", source: "/k/workspace-roots/h/permissions.yaml", effect: "deny", want: true},
		{name: "a user ask", scope: "user", source: userFile, effect: "ask", want: true},
		{name: "a preset the user file references", scope: "user", source: "preset:dev-shell in " + userFile, effect: "allow"},
		{name: "an evaluate rule in the user file", scope: "user", source: userFile, effect: "evaluate", want: true},
		{name: "an effect the writer does not know", scope: "user", source: userFile, effect: "prompt"},
		{name: "a session preset", scope: "session", source: "preset:allow-all", effect: "allow"},
		{name: "an agent profile rule", scope: "agent", source: "agent-profile", effect: "allow"},
		{name: "the kiro baseline", scope: "kiro", source: "kiro-scope", effect: "deny"},
		{name: "an organization rule", scope: "organization", source: "org", effect: "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Editable(tc.scope, tc.source, tc.effect); got != tc.want {
				t.Errorf("Editable(%q, %q, %q) = %v, want %v", tc.scope, tc.source, tc.effect, got, tc.want)
			}
		})
	}
}
