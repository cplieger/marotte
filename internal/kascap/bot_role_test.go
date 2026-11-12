package kascap

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/marotte"
)

// botRoleMatrix is every shape of the Bot role's entry; its order is bot_role.golden's line order. KiroHome is a test
// path, never the instance's.
var botRoleMatrix = []struct {
	name     string
	memory   bool
	steering string
}{
	{"memory off, no steering", false, ""},
	{"memory off, steering set", false, "Always answer in French."},
	{"memory on, no steering", true, ""},
	{"memory on, steering set", true, "Always answer in French."},
}

var botRolePaths = bot.Paths{KiroHome: "/kiro-home-test"}

func botRoleRecord(memory bool, steering string) *marotte.Chat {
	return &marotte.Chat{
		ID:            "c-bot-golden",
		Name:          "Weekly answers",
		CurrentModeID: marotte.BotRoleID,
		Bot: &marotte.Bot{
			Goal:      "Answer animap's weekly questions.\nKeep the backlog under ten.",
			Steering:  steering,
			StartedAt: 1_700_000_000_000,
			Memory:    memory,
		},
	}
}

// TestBotRoleDeclaresExactly pins the bytes of the client custom agent a Bot's session doors carry.
func TestBotRoleDeclaresExactly(t *testing.T) {
	names := make([]string, len(botRoleMatrix))
	for i := range botRoleMatrix {
		names[i] = botRoleMatrix[i].name
	}
	got := renderLines(t, names, func(i int) marotte.ClientCustomAgent {
		return bot.Role(botRoleRecord(botRoleMatrix[i].memory, botRoleMatrix[i].steering), botRolePaths)
	})
	checkGolden(t, botRoleGoldenPath, got, names)
}

// defaultPolicyName is the surviving name of KAS's buildDefaultAgentPolicy, `<setName>(<fn>,"buildDefaultAgentPolicy")`.
const defaultPolicyName = `,"buildDefaultAgentPolicy")`

// defaultPolicyReturnRe reads the policy the function returns with no sub-agents: `{if(t.length===0)return X;`.
var defaultPolicyReturnRe = regexp.MustCompile(`^\(` + jsIdent + `\)\{if\(` + jsIdent + `\.length===0\)return (` + jsIdent + `);`)

// jsKeyRe is an unquoted object key in a minified literal, quoted to read the literal as JSON.
var jsKeyRe = regexp.MustCompile(`([{,])(` + jsIdent + `):`)

// defaultAgentRules returns the rules of the bundle's default agent policy, every spread and binding resolved.
func defaultAgentRules(t *testing.T, src string) []marotte.PolicyRuleCore {
	t.Helper()
	var fns []string
	for _, at := range indexesOf(src, defaultPolicyName) {
		if name, _ := identBefore(src, at); name != "" {
			fns = append(fns, name)
		}
	}
	if len(fns) != 1 {
		t.Fatalf("found %d `<fn>%s` sites %v, want exactly 1", len(fns), defaultPolicyName, fns)
	}
	lit := "function " + fns[0]
	sites := literalSites(src, lit+"(", false)
	if len(sites) != 1 {
		t.Fatalf("found %d `%s(` declarations, want exactly 1", len(sites), lit)
	}
	body := src[sites[0]+len(lit):]
	m := defaultPolicyReturnRe.FindStringSubmatch(body[:min(len(body), 200)])
	if m == nil {
		t.Fatalf("%s no longer opens with the no-sub-agent return: %.200s", fns[0], body)
	}
	obj := jsBracketed(src, jsBinding(t, src, m[1], '{'))
	open := topLevelKey(obj, "rules:[")
	if open < 0 {
		t.Fatalf("default policy %s has no rules array: %.200s", m[1], obj)
	}
	return fullRuleElements(t, src, jsBracketed(obj, open), 0)
}

// fullRuleElements reads one rules array literal with each rule's match and exclude lists, as ruleElements reads it.
func fullRuleElements(t *testing.T, src, arr string, depth int) []marotte.PolicyRuleCore {
	t.Helper()
	if depth > presetSpreadDepth {
		t.Fatalf("rule spread chain deeper than %d at %.200s", presetSpreadDepth, arr)
	}
	var out []marotte.PolicyRuleCore
	for _, el := range topLevelElements(arr) {
		switch m := ruleSpreadRe.FindStringSubmatch(el); {
		case strings.HasPrefix(el, "{"):
			out = append(out, parseFullRule(t, el))
		case m != nil && m[2] != "":
			obj := jsBracketed(src, jsBinding(t, src, m[1], '{'))
			open := topLevelKey(obj, "rules:[")
			if open < 0 {
				t.Fatalf("policy binding %s has no rules array: %.200s", m[1], obj)
			}
			out = append(out, fullRuleElements(t, src, jsBracketed(obj, open), depth+1)...)
		case m != nil:
			out = append(out, fullRuleElements(t, src, jsBracketed(src, jsBinding(t, src, m[1], '[')), depth+1)...)
		case ruleIdentRe.MatchString(el):
			out = append(out, parseFullRule(t, jsBracketed(src, jsBinding(t, src, el, '{'))))
		default:
			t.Fatalf("rules element %.200q has a shape fullRuleElements does not read", el)
		}
	}
	return out
}

// parseFullRule reads one minified rule literal; a key other than the four a rule carries fails.
func parseFullRule(t *testing.T, obj string) marotte.PolicyRuleCore {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(jsKeyRe.ReplaceAllString(obj, `$1"$2":`)))
	dec.DisallowUnknownFields()
	var r marotte.PolicyRuleCore
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("rule %.200q does not read as {capability, match, exclude, effect}: %v", obj, err)
	}
	return r
}

// The fixture is 3.1.0's shape: a bare rule binding, a spread rule array and an inline rule.
func TestDefaultAgentRules_ResolvesTheBindings(t *testing.T) {
	const src = `var Cmr={capability:"fs_read",match:["./**"],effect:"allow"},` +
		`AJe=[{capability:"shell",match:["pwd"],effect:"allow"},{capability:"shell",match:["npm audit *"],exclude:["npm audit fix*"],effect:"allow"}];` +
		`var $Ms={rules:[Cmr,...AJe,{capability:"web_fetch",match:["kiro.dev"],effect:"allow"}]};` +
		`function BMs(t){if(t.length===0)return $Ms;let e=[...new Set(t)].sort();return{rules:[...$Ms.rules]}}a(BMs,"buildDefaultAgentPolicy");`
	got := defaultAgentRules(t, src)
	want := []marotte.PolicyRuleCore{
		{Capability: "fs_read", Effect: "allow", Match: []string{"./**"}},
		{Capability: "shell", Effect: "allow", Match: []string{"pwd"}},
		{Capability: "shell", Effect: "allow", Match: []string{"npm audit *"}, Exclude: []string{"npm audit fix*"}},
		{Capability: "web_fetch", Effect: "allow", Match: []string{"kiro.dev"}},
	}
	if !slices.EqualFunc(got, want, policyRuleEqual) {
		t.Errorf("defaultAgentRules = %+v, want %+v", got, want)
	}
}

func policyRuleEqual(a, b marotte.PolicyRuleCore) bool {
	return a.Capability == b.Capability && a.Effect == b.Effect && slices.Equal(a.Match, b.Match) && slices.Equal(a.Exclude, b.Exclude)
}

// TestBotRoleCarriesTheDefaultAgentPolicy pins the role's rules to the bundle's default agent policy: KAS gives a
// client agent only its own rules, so a bump that changes the default changes what a Bot may do unasked.
func TestBotRoleCarriesTheDefaultAgentPolicy(t *testing.T) {
	role := bot.Role(botRoleRecord(false, ""), botRolePaths).Permissions.Rules
	forEachBundle(t, func(t *testing.T, b bundle) {
		want := defaultAgentRules(t, b.src)
		if len(want) == 0 {
			t.Fatal("the default agent policy resolved to no rules; defaultAgentRules no longer reads it")
		}
		if !slices.EqualFunc(role, want, policyRuleEqual) {
			raw, _ := json.Marshal(want)
			t.Errorf("the Bot role's rules differ from the bundle's default agent policy; copy these into internal/bot:\n%s", raw)
		}
	})
}

// aliasTablePrefix is the opening of KAS's tool alias table, whose keys survive mangling.
const aliasTablePrefix = `={fs_read:"fs_read",read:"fs_read",read_file:"fs_read"`

// subagentClass is the class every sub-agent tool alias maps to in the alias table.
const subagentClass = `"use_subagent"`

// subagentAliases returns the alias table's keys whose class is the sub-agent class, in table order.
func subagentAliases(t *testing.T, src string) []string {
	t.Helper()
	sites := indexesOf(src, aliasTablePrefix)
	if len(sites) != 1 {
		t.Fatalf("found %d tool alias tables `<x>%s`, want exactly 1", len(sites), aliasTablePrefix)
	}
	obj := jsBracketed(src, sites[0]+1)
	var out []string
	for _, el := range topLevelElements(obj) {
		if key, class, ok := strings.Cut(el, ":"); ok && class == subagentClass {
			out = append(out, key)
		}
	}
	return out
}

func TestSubagentAliases_ReadsTheClass(t *testing.T) {
	const src = `var ros={fs_read:"fs_read",read:"fs_read",read_file:"fs_read",use_subagent:"use_subagent",subagent:"use_subagent",` +
		`session:"session",delegate:"use_subagent",switch_to_execution:"switch_to_execution"};`
	if got, want := subagentAliases(t, src), []string{"use_subagent", "subagent", "delegate"}; !slices.Equal(got, want) {
		t.Errorf("subagentAliases = %v, want %v", got, want)
	}
}

// TestBotRoleExcludesTheSubagentClass pins the role's exclusions to every id the bundle maps to the sub-agent class:
// a Bot has no sub-agent tool and hands work to workflows.
func TestBotRoleExcludesTheSubagentClass(t *testing.T) {
	excluded := bot.Role(botRoleRecord(false, ""), botRolePaths).ExcludedTools
	forEachBundle(t, func(t *testing.T, b bundle) {
		class := subagentAliases(t, b.src)
		if len(class) == 0 {
			t.Fatal("the alias table maps no id to the sub-agent class; subagentAliases no longer reads it")
		}
		got := slices.DeleteFunc(slices.Clone(excluded), func(id string) bool { return id == "switch_to_execution" })
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(class))) {
			t.Errorf("the Bot role excludes %v beside switch_to_execution; the bundle's sub-agent class is %v", got, class)
		}
	})
}
