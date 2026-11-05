package kascap

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/policyfile"
)

// presetRule is one rule of a KAS policy preset as its registry literal spells it.
type presetRule struct{ capability, effect string }

// presetSpreadDepth bounds the spread chain so a self-referencing binding fails instead of looping.
const presetSpreadDepth = 8

var (
	ruleCapabilityRe = regexp.MustCompile(`\bcapability:"([a-z_]+)"`)
	ruleEffectRe     = regexp.MustCompile(`\beffect:"([a-z_]+)"`)
	// ruleSpreadRe is a registry element reusing another binding: `...X` (an array of rules) or
	// `...X.rules` (another preset's rules).
	ruleSpreadRe = regexp.MustCompile(`^\.\.\.(` + jsIdent + `)(\.rules)?$`)
	ruleIdentRe  = regexp.MustCompile(`^` + jsIdent + `$`)
)

// readOnlyAllows is every capability the Read-only profile may auto-approve.
var readOnlyAllows = []string{"fs_read", "web_fetch", "web_search"}

// TestReadOnlyPresetsAllowNoShellOrWrite resolves the Read-only profile's presets against the pinned
// registry. A shell, write, evaluate or all-capability grant in one of them would auto-approve
// `git status > <path>` or `git log --output=<path>`, which policyfile's own preset list cannot see.
func TestReadOnlyPresetsAllowNoShellOrWrite(t *testing.T) {
	p, ok := policyfile.ProfileFor(policyfile.ProfileReadOnly)
	if !ok {
		t.Fatal("policyfile.ProfileFor(read-only) missing")
	}
	forEachBundle(t, func(t *testing.T, b bundle) {
		// The witness: read-only-shell is all shell allows, so resolving none means the
		// extractor lost the spreads, and the Read-only check below would pass vacuously.
		if !slices.ContainsFunc(presetRules(t, b.src, "read-only-shell"), func(r presetRule) bool {
			return r.capability == "shell" && r.effect == "allow"
		}) {
			t.Fatal("read-only-shell resolved with no shell allow rule; presetRules no longer reads the registry")
		}
		for _, id := range p.Presets {
			rules := presetRules(t, b.src, id)
			if len(rules) == 0 {
				t.Fatalf("preset %q resolved to no rules; the registry literal was reshaped", id)
			}
			for _, r := range rules {
				if r.effect == "ask" || r.effect == "deny" ||
					(r.effect == "allow" && slices.Contains(readOnlyAllows, r.capability)) {
					continue
				}
				t.Errorf("Read-only preset %q has rule {capability:%q, effect:%q}; Read-only may only allow %v, "+
					"so drop the preset from policyfile's Read-only profile", id, r.capability, r.effect, readOnlyAllows)
			}
		}
	})
}

// The fixture is the 2.37 registry's shape: a shared rule array, a spread of another preset's rules
// and a bare rule binding.
func TestPresetRules_ResolvesSpreads(t *testing.T) {
	const src = `var Tsr={capability:"fs_read",match:["./**"],effect:"allow"},` +
		`MGe=[{capability:"shell",match:["git status","pwd"],effect:"allow"}],` +
		`Esr={id:"edit-workspace",description:"Allow rules:[ in prose",rules:[` +
		`{capability:"fs_write",match:["./**"],exclude:["a}b"],effect:"allow"}]},` +
		`Djo={id:"approve-for-me",requiresClassifier:!0,rules:[{capability:"all",effect:"evaluate"},...Esr.rules,...MGe,Tsr]};`
	got := presetRules(t, src, "approve-for-me")
	want := []presetRule{{"all", "evaluate"}, {"fs_write", "allow"}, {"shell", "allow"}, {"fs_read", "allow"}}
	if !slices.Equal(got, want) {
		t.Errorf("presetRules(approve-for-me) = %v, want %v", got, want)
	}
}

// presetRules returns the rules of the registry preset id, with every spread resolved.
func presetRules(t *testing.T, src, id string) []presetRule {
	t.Helper()
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$.])(` + jsIdent + `)=\{id:"` + regexp.QuoteMeta(id) + `",`)
	return presetObjectRules(t, src, discoverOne(t, "preset "+id, re, src), 0)
}

func presetObjectRules(t *testing.T, src, name string, depth int) []presetRule {
	t.Helper()
	obj := jsBracketed(src, jsBinding(t, src, name, '{'))
	open := topLevelKey(obj, "rules:[")
	if open < 0 {
		t.Fatalf("preset binding %s has no rules array: %.200s", name, obj)
	}
	return ruleElements(t, src, jsBracketed(obj, open), depth)
}

// ruleElements reads one rules array literal, `[` to `]`.
func ruleElements(t *testing.T, src, arr string, depth int) []presetRule {
	t.Helper()
	if depth > presetSpreadDepth {
		t.Fatalf("preset spread chain deeper than %d at %.200s", presetSpreadDepth, arr)
	}
	var out []presetRule
	for _, el := range topLevelElements(arr) {
		switch m := ruleSpreadRe.FindStringSubmatch(el); {
		case strings.HasPrefix(el, "{"):
			out = append(out, parseRule(t, el))
		case m != nil && m[2] != "":
			out = append(out, presetObjectRules(t, src, m[1], depth+1)...)
		case m != nil:
			out = append(out, ruleElements(t, src, jsBracketed(src, jsBinding(t, src, m[1], '[')), depth+1)...)
		case ruleIdentRe.MatchString(el):
			out = append(out, parseRule(t, jsBracketed(src, jsBinding(t, src, el, '{'))))
		default:
			t.Fatalf("preset rules element %.200q has a shape presetRules does not read", el)
		}
	}
	return out
}

func parseRule(t *testing.T, obj string) presetRule {
	t.Helper()
	c, e := ruleCapabilityRe.FindAllStringSubmatch(obj, -1), ruleEffectRe.FindAllStringSubmatch(obj, -1)
	if len(c) != 1 || len(e) != 1 {
		t.Fatalf("preset rule %.200q: want one capability and one effect, got %d and %d", obj, len(c), len(e))
	}
	return presetRule{capability: c[0][1], effect: e[0][1]}
}

// jsBinding returns the index of the bracket opening name's single `name=<open>` binding.
func jsBinding(t *testing.T, src, name string, open byte) int {
	t.Helper()
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$.])` + regexp.QuoteMeta(name) + `=` + regexp.QuoteMeta(string(open)))
	locs := re.FindAllStringIndex(src, -1)
	if len(locs) != 1 {
		t.Fatalf("binding %s=%c: found %d in the bundle, want exactly 1", name, open, len(locs))
	}
	return locs[0][1] - 1
}

// topLevelKey returns the index of the bracket ending key (`rules:[`) at depth 1 of obj, or -1.
func topLevelKey(obj, key string) int {
	depth := 0
	for i := 0; i < len(obj); i++ {
		switch c := obj[i]; c {
		case '"', '\'', '`':
			i = jsStringEnd(obj, i)
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		default:
			if depth == 1 && (obj[i-1] == ',' || obj[i-1] == '{') && strings.HasPrefix(obj[i:], key) {
				return i + len(key) - 1
			}
		}
	}
	return -1
}

// topLevelElements splits an array literal into its comma-separated elements.
func topLevelElements(arr string) []string {
	var out []string
	depth, start := 0, 1
	for i := 0; i < len(arr); i++ {
		switch c := arr[i]; c {
		case '"', '\'', '`':
			i = jsStringEnd(arr, i)
		case '{', '[':
			depth++
		case '}', ']':
			if depth--; depth == 0 {
				if el := strings.TrimSpace(arr[start:i]); el != "" {
					out = append(out, el)
				}
			}
		case ',':
			if depth == 1 {
				out = append(out, strings.TrimSpace(arr[start:i]))
				start = i + 1
			}
		}
	}
	return out
}
