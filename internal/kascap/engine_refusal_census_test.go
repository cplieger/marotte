package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const engineRefusalSource = "../translate/engine_refusal.go"

// blockedByTemplateRe is the head of KAS's rule-less refusal sentence, "Blocked by ${rule?`a
// permission rule (…)`:…}", whose "Blocked by " the translate recognizer strips.
var blockedByTemplateRe = regexp.MustCompile("Blocked by \\$\\{" + jsIdent + "\\?`a permission rule")

// toolCallDeniedLiteralRe is the start of every string literal opening "Tool call denied: ".
var toolCallDeniedLiteralRe = regexp.MustCompile(`"Tool call denied: [^"]*`)

// engineRefusalLeads returns the `lead` of every engineRefusals row in src, the translate
// package's source, which kascap cannot import.
func engineRefusalLeads(t *testing.T, src []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), engineRefusalSource, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", engineRefusalSource, err)
	}
	var leads []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "engineRefusals" || len(spec.Values) != 1 {
			return true
		}
		list, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatalf("%s: engineRefusals is not a slice literal", engineRefusalSource)
		}
		for _, row := range list.Elts {
			leads = append(leads, rowLead(t, row))
		}
		return false
	})
	if len(leads) == 0 {
		t.Fatalf("%s: found no engineRefusals lead; the declarations moved", engineRefusalSource)
	}
	return leads
}

func rowLead(t *testing.T, row ast.Expr) string {
	t.Helper()
	lit, ok := row.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("%s: an engineRefusals row is not a struct literal", engineRefusalSource)
	}
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if key, isIdent := kv.Key.(*ast.Ident); !ok || !isIdent || key.Name != "lead" {
			continue
		}
		val, ok := kv.Value.(*ast.BasicLit)
		if !ok || val.Kind != token.STRING {
			t.Fatalf("%s: a lead is not a string literal", engineRefusalSource)
		}
		s, err := strconv.Unquote(val.Value)
		if err != nil {
			t.Fatalf("%s: %v", engineRefusalSource, err)
		}
		return s
	}
	t.Fatalf("%s: an engineRefusals row has no lead", engineRefusalSource)
	return ""
}

func TestEngineRefusalLeadExtractor(t *testing.T) {
	const src = `package translate
var engineRefusals = []struct{ lead string; basis string }{
	{lead: "Tool call denied: one", basis: "a"},
	{basis: "b", lead: "the \"two\""},
}`
	got := engineRefusalLeads(t, []byte(src))
	if want := []string{"Tool call denied: one", `the "two"`}; !slices.Equal(got, want) {
		t.Errorf("engineRefusalLeads = %q, want %q", got, want)
	}
}

// TestEngineRefusalLeadsMatchBundle fails when KAS rewords a refusal the translate package
// recognises by text, renames the "Blocked by" template the recognizer strips, or adds a
// "Tool call denied: " refusal no lead opens, which would otherwise read as a tool failure.
func TestEngineRefusalLeadsMatchBundle(t *testing.T) {
	src, err := os.ReadFile(filepath.Clean(engineRefusalSource))
	if err != nil {
		t.Fatalf("read %s: %v", engineRefusalSource, err)
	}
	leads := engineRefusalLeads(t, src)
	forEachBundle(t, func(t *testing.T, b bundle) {
		for _, lead := range leads {
			if !strings.Contains(b.src, lead) {
				t.Errorf("the bundle no longer carries the refusal lead %q verbatim; that refusal reads as a tool failure", lead)
			}
		}
		if n := len(blockedByTemplateRe.FindAllStringIndex(b.src, -1)); n != 1 {
			t.Errorf("found %d \"Blocked by ${…?`a permission rule\" templates, want exactly 1; re-anchor the recognizer's prefix", n)
		}
		for _, lit := range toolCallDeniedLiteralRe.FindAllString(b.src, -1) {
			body := strings.TrimPrefix(lit, `"`)
			if !slices.ContainsFunc(leads, func(lead string) bool { return strings.HasPrefix(body, lead) }) {
				t.Errorf("the bundle has a refusal %q no engineRefusals lead opens; add its row", body)
			}
		}
	})
}
