package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const toolIdentitySource = "../translate/tool_identity.go"

// declaredToolMap reads the string-to-string map literal named name from the translator's source, so
// the translator exports nothing for these tests. A key or value may be a string const declared in
// the same file.
func declaredToolMap(t *testing.T, name string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), toolIdentitySource, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", toolIdentitySource, err)
	}
	consts := map[string]string{}
	var literal *ast.CompositeLit
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		if lit, ok := spec.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				consts[spec.Names[0].Name] = s
			}
		}
		if spec.Names[0].Name == name {
			literal, _ = spec.Values[0].(*ast.CompositeLit)
		}
		return true
	})
	if literal == nil {
		t.Fatalf("%s: no map literal named %s; the declaration moved", toolIdentitySource, name)
	}
	str := func(e ast.Expr) string {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				if s, err := strconv.Unquote(e.Value); err == nil {
					return s
				}
			}
		case *ast.Ident:
			if s, ok := consts[e.Name]; ok {
				return s
			}
		}
		t.Fatalf("%s: %s holds an entry that is neither a string literal nor a string const", toolIdentitySource, name)
		return ""
	}
	out := map[string]string{}
	for _, elt := range literal.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("%s: %s is not a keyed map literal", toolIdentitySource, name)
		}
		out[str(kv.Key)] = str(kv.Value)
	}
	if len(out) == 0 {
		t.Fatalf("%s: %s is empty", toolIdentitySource, name)
	}
	return out
}

// toolIDMetaSetRe matches the set of action types KAS stamps `_meta.kiro.toolId` for, declared
// beside the disclose_context reader. From 3.1.0 a keep-names call (`a(f,"isToolIdRoutedActionType");`)
// follows the membership function.
var toolIDMetaSetRe = regexp.MustCompile(`new Set\(\[((?:"[a-z_]+",?)+)\]\);function [A-Za-z0-9_$]+\(t\)\{return [A-Za-z0-9_$]+\.has\(t\)\}` +
	`(?:[A-Za-z0-9_$]+\([A-Za-z0-9_$]+,"[A-Za-z]+"\);)?function [A-Za-z0-9_$]+\(t,e\)\{if\(t!=="disclose_context"`)

// The pinned agent server titles a built-in from its tool class (`static id="x";static title="Y"`)
// or definition object (`{id:"x",title:"Y"`), else falls back to the bare id when it carries no
// `remote_` or `subagent_` prefix. Each row of the translator's table must be one of those.
func TestBuiltinToolTitlesMatchTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		for title, id := range declaredToolMap(t, "builtinToolByTitle") {
			class := `static id="` + id + `";static title="` + title + `"`
			object := `{id:"` + id + `",title:"` + title + `"`
			bare := title == id && !strings.HasPrefix(id, "remote_") && !strings.HasPrefix(id, "subagent_") &&
				strings.Contains(b.src, `"`+id+`"`) && !strings.Contains(b.src, `static id="`+id+`"`)
			if !strings.Contains(b.src, class) && !strings.Contains(b.src, object) && !bare {
				t.Errorf("%s: no tool titled %q with id %q; fix builtinToolByTitle in internal/translate/tool_identity.go", b.name, title, id)
			}
		}
	})
}

// The pinned agent server folds each aliased id into the tool it names in its own tool-family
// tables (`remote_web_search:"web_search"`).
func TestBuiltinToolAliasesMatchTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		for from, to := range declaredToolMap(t, "builtinToolAliases") {
			if !strings.Contains(b.src, from+`:"`+to+`"`) {
				t.Errorf("%s: no alias %s -> %s; fix builtinToolAliases in internal/translate/tool_identity.go", b.name, from, to)
			}
		}
	})
}

// builtinToolName prefers `_meta.kiro.toolId`, and keeps a title table only because KAS stamps it on
// so few tools. When the set grows, key on it and shrink builtinToolByTitle.
func TestToolIDMetaIsStampedOnTheKnownTools(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		m := toolIDMetaSetRe.FindStringSubmatch(b.src)
		if m == nil {
			t.Fatalf("%s: no toolId action-type set beside the disclose_context reader; re-anchor this test", b.name)
		}
		got := strings.Split(strings.ReplaceAll(m[1], `"`, ""), ",")
		want := []string{"disclose_context", "create_diagram"}
		if !slices.Equal(got, want) {
			t.Errorf("%s: KAS stamps toolId on %v, want %v", b.name, got, want)
		}
	})
}
