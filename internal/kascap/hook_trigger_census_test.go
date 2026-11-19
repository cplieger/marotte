package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const hookTriggerSource = "../marotte/hook_triggers.go"

var (
	// triggerSpellingRe is one `spelling:"Canonical"` entry of KAS's frozen trigger-name table.
	triggerSpellingRe = regexp.MustCompile(`(` + jsIdent + `|"[^"]*"):"([A-Za-z]+)"`)
	// triggerTableTailRe is what sits between the table's closing brace and its normalizer's
	// keep-names call: `);a(fn,`.
	triggerTableTailRe = regexp.MustCompile(`^\);?` + jsIdent + `\(` + jsIdent + `,$`)
	// subjectArmRe is one arm of matcherSubjectForTrigger: its case labels and the subject they return.
	subjectArmRe = regexp.MustCompile(`((?:case"[A-Za-z]+":)+)return"([A-Za-z]+)"`)
	// commandFilterTailRe is a runMatchingHooks call ending in a filter that keeps only command hooks.
	commandFilterTailRe = regexp.MustCompile(`,` + jsIdent + `=>` + jsIdent + `\.action\.kind==="command"\)$`)
)

// declaredHookTriggers returns the value of every trigger* const in hook_triggers.go: the canonical
// names marotte's table holds.
func declaredHookTriggers(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), hookTriggerSource, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", hookTriggerSource, err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 || !strings.HasPrefix(vs.Names[0].Name, "trigger") {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatalf("%s: %s is not a string literal", hookTriggerSource, vs.Names[0].Name)
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", hookTriggerSource, err)
			}
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s: found no trigger* const; the declarations moved", hookTriggerSource)
	}
	return names
}

// kasTriggerSpellings returns KAS's normalizeTriggerName table, each spelling it loads mapped to
// its canonical trigger. A trigger KAS dispatches but does not list there is dropped at load.
func kasTriggerSpellings(t *testing.T, src string) map[string]string {
	t.Helper()
	const anchor = `"normalizeTriggerName")`
	sites := indexesOf(src, anchor)
	if len(sites) != 1 {
		t.Fatalf("found %d %s keep-names calls, want exactly 1; re-anchor on KAS's trigger normalizer", len(sites), anchor)
	}
	open := strings.LastIndex(src[:sites[0]], "Object.freeze({")
	if open < 0 {
		t.Fatal("no Object.freeze({ table before normalizeTriggerName; re-anchor on KAS's trigger normalizer")
	}
	open += len("Object.freeze(")
	table := jsBracketed(src, open)
	if table == "" || !triggerTableTailRe.MatchString(src[open+len(table):sites[0]]) {
		t.Fatal("the frozen table before normalizeTriggerName is not the one it reads; re-anchor on KAS's trigger normalizer")
	}
	out := make(map[string]string)
	for _, m := range triggerSpellingRe.FindAllStringSubmatch(table, -1) {
		out[strings.Trim(m[1], `"`)] = m[2]
	}
	if len(out) == 0 {
		t.Fatal("the trigger-name table yielded no spelling; the entry pattern was reshaped upstream")
	}
	return out
}

// kasMatcherSubjects returns the subject KAS's matcherSubjectForTrigger gives each trigger it names.
func kasMatcherSubjects(t *testing.T, src string) map[string]string {
	t.Helper()
	const anchor = `,"matcherSubjectForTrigger")`
	sites := indexesOf(src, anchor)
	if len(sites) != 1 {
		t.Fatalf("found %d %s keep-names calls, want exactly 1; re-anchor on KAS's matcher subjects", len(sites), anchor)
	}
	name, _ := identBefore(src, sites[0])
	decl := "function " + name + "("
	if name == "" || strings.Count(src, decl) != 1 {
		t.Fatalf("matcherSubjectForTrigger is %q, declared %d times; want one declaration", name, strings.Count(src, decl))
	}
	at := strings.Index(src, decl)
	body := jsBracketed(src, at+strings.IndexByte(src[at:], '{'))
	out := make(map[string]string)
	for _, m := range subjectArmRe.FindAllStringSubmatch(body, -1) {
		for label := range strings.SplitSeq(strings.TrimSuffix(m[1], ":"), ":") {
			out[strings.Trim(strings.TrimPrefix(label, "case"), `"`)] = m[2]
		}
	}
	if len(out) == 0 {
		t.Fatal("matcherSubjectForTrigger: no arm matched; the switch was reshaped upstream, so fix the pattern")
	}
	return out
}

// kasCommandOnlyTriggers returns each trigger KAS runs through runMatchingHooks, true when that call
// keeps only its command hooks.
func kasCommandOnlyTriggers(t *testing.T, src string) map[string]bool {
	t.Helper()
	const anchor = `,"runMatchingHooks")`
	sites := indexesOf(src, anchor)
	if len(sites) != 1 {
		t.Fatalf("found %d %s keep-names calls, want exactly 1; re-anchor on KAS's hook runner", len(sites), anchor)
	}
	runner, _ := identBefore(src, sites[0])
	if runner == "" {
		t.Fatal("runMatchingHooks: no function name before its keep-names call")
	}
	out := make(map[string]bool)
	for _, at := range literalSites(src, runner+`("`, false) {
		open := at + len(runner)
		trigger, _, _ := strings.Cut(src[open+2:], `"`)
		call := jsCall(src, open)
		if call == "" {
			t.Fatalf("runMatchingHooks(%q): unbalanced call", trigger)
		}
		only := commandFilterTailRe.MatchString(call)
		if prev, seen := out[trigger]; seen && prev != only {
			t.Errorf("runMatchingHooks runs %s both with and without a command-only filter", trigger)
		}
		out[trigger] = only
	}
	if len(out) == 0 {
		t.Fatal("runMatchingHooks: no call site found; the runner was reshaped upstream")
	}
	return out
}

// jsCall returns the balanced `(...)` run opening at index open; strings are skipped.
func jsCall(src string, open int) string {
	depth := 0
	for i := open; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			i = jsStringEnd(src, i)
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			if depth--; depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return ""
}

func TestKASCommandOnlyTriggersExtractor(t *testing.T) {
	const src = `async function u$(t,e,n,r){}a(u$,"runMatchingHooks");` +
		`function f(t,e){return u$("SessionEnd",{x:"a)"},e,u=>u.action.kind==="command")}` +
		`function g(t,e){return u$("PreToolUse",{y:[1]},e,l=>!t.skip.includes(l.id))}` +
		`function h(t,e){return u$("Stop",{},e)}`
	got := kasCommandOnlyTriggers(t, src)
	want := map[string]bool{"SessionEnd": true, "PreToolUse": false, "Stop": false}
	if !maps.Equal(got, want) {
		t.Errorf("kasCommandOnlyTriggers = %v, want %v", got, want)
	}
}

func TestKASTriggerSpellingsExtractor(t *testing.T) {
	const src = `x;var q,w=E(()=>{q=Object.freeze({Stop:"Stop",agentStop:"Stop","odd-key":"PreToolUse"});a(Z,"normalizeTriggerName")`
	got := kasTriggerSpellings(t, src)
	want := map[string]string{"Stop": "Stop", "agentStop": "Stop", "odd-key": "PreToolUse"}
	if !maps.Equal(got, want) {
		t.Errorf("kasTriggerSpellings = %v, want %v", got, want)
	}
}

func TestKASMatcherSubjectsExtractor(t *testing.T) {
	const src = `function n$1(t){switch(t){case"PreToolUse":case"PostToolUse":return"toolName";case"Stop":return"none"}}` +
		`var y=E(()=>{a(n$1,"matcherSubjectForTrigger")})`
	got := kasMatcherSubjects(t, src)
	want := map[string]string{"PreToolUse": "toolName", "PostToolUse": "toolName", "Stop": "none"}
	if !maps.Equal(got, want) {
		t.Errorf("kasMatcherSubjects = %v, want %v", got, want)
	}
}

// TestHookTriggersMatchBundle fails when KAS loads a trigger marotte's table lacks, or the reverse,
// when marotte resolves a spelling KAS loads to another trigger, or when a trigger's matcher subject
// or command-only filter differs from KAS's.
func TestHookTriggersMatchBundle(t *testing.T) {
	declared := declaredHookTriggers(t)
	forEachBundle(t, func(t *testing.T, b bundle) {
		spellings := kasTriggerSpellings(t, b.src)
		subjects := kasMatcherSubjects(t, b.src)
		commandOnly := kasCommandOnlyTriggers(t, b.src)
		loaded := slices.Sorted(maps.Values(spellings))
		loaded = slices.Compact(loaded)
		for _, name := range loaded {
			if !slices.Contains(declared, name) {
				t.Errorf("KAS loads the trigger %s and internal/marotte/hook_triggers.go lacks it", name)
				continue
			}
			got, _ := marotte.HookTriggerSubject(name)
			if want, ok := subjects[name]; !ok || string(got) != want {
				t.Errorf("trigger %s has the subject %q, KAS's matcherSubjectForTrigger gives %q (named there: %v)", name, got, want, ok)
			}
			if got := marotte.HookTriggerRunsCommandsOnly(name); got != commandOnly[name] {
				t.Errorf("trigger %s runs commands only: marotte says %v, KAS's runMatchingHooks filter says %v", name, got, commandOnly[name])
			}
		}
		for _, name := range declared {
			if !slices.Contains(loaded, name) {
				t.Errorf("internal/marotte/hook_triggers.go holds the trigger %s, which KAS does not load", name)
			}
		}
		for spelling, want := range spellings {
			if got, ok := marotte.CanonicalHookTrigger(spelling); !ok || got != want {
				t.Errorf("KAS loads %q as %s, marotte resolves it to %q (known: %v)", spelling, want, got, ok)
			}
		}
	})
}
