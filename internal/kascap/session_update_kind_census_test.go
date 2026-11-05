package kascap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"testing"
)

// sessionUpdateMemberRe matches one member of KAS's session/update schema union, whose
// discriminant is a literal schema call on a mangled callee.
var sessionUpdateMemberRe = regexp.MustCompile(`\{sessionUpdate:` + jsIdent + `\("([a-z_]+)"\)\}`)

func sessionUpdateKinds(src string) []string {
	var kinds []string
	for _, m := range sessionUpdateMemberRe.FindAllStringSubmatch(src, -1) {
		kinds = append(kinds, m[1])
	}
	return kinds
}

// declaredUpdateKinds is every ACPUpdateKind string constant in marotte's two declaring files:
// the domain kinds and the replay-only user_message_chunk.
func declaredUpdateKinds(t *testing.T) map[string]bool {
	t.Helper()
	declared := map[string]bool{}
	for _, path := range []string{"../marotte/domain_chat.go", "../translate/replay_wire.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok || len(spec.Values) != 1 {
				return true
			}
			var typeName string
			switch typ := spec.Type.(type) {
			case *ast.Ident:
				typeName = typ.Name
			case *ast.SelectorExpr:
				typeName = typ.Sel.Name
			}
			lit, isLit := spec.Values[0].(*ast.BasicLit)
			if typeName != "ACPUpdateKind" || !isLit || lit.Kind != token.STRING {
				return true
			}
			kind, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				t.Fatalf("%s: decode ACPUpdateKind %s: %v", path, lit.Value, unquoteErr)
			}
			declared[kind] = true
			return true
		})
	}
	if !declared["agent_message_chunk"] || !declared["user_message_chunk"] {
		t.Fatalf("declared kinds %v miss a known one; an ACPUpdateKind declaration moved", slices.Sorted(maps.Keys(declared)))
	}
	return declared
}

// undeclaredUpdateKinds are the schema members marotte does not name, each with why it is admitted. A kind KAS
// emits must be named instead, or the runtime census reports it and every door drops it.
var undeclaredUpdateKinds = map[string]string{
	"plan_update":  "one literal occurrence, the schema itself: KAS has no emit site; re-check at every bump",
	"plan_removed": "one literal occurrence, the schema itself: KAS has no emit site; re-check at every bump",
}

func TestSessionUpdateKindExtractor(t *testing.T) {
	const src = `.and(Me({sessionUpdate:sn("a")})),x.and(Me({sessionUpdate:zz("b")})),{sessionUpdate:"c"}`
	if got := sessionUpdateKinds(src); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("sessionUpdateKinds = %v, want [a b] (any callee, literal kinds outside a schema call skipped)", got)
	}
}

func TestSessionUpdateKindCensus(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		members := requireNonEmpty(t, "session/update schema union", sessionUpdateKinds(b.src))
		declared := declaredUpdateKinds(t)
		for _, kind := range members {
			if _, admitted := undeclaredUpdateKinds[kind]; !declared[kind] && !admitted {
				t.Errorf("the bundle's session/update union names %q, which marotte does not: declare an "+
					"ACPUpdateKind and route it, or admit it in undeclaredUpdateKinds with the reason", kind)
			}
		}
		for kind := range undeclaredUpdateKinds {
			switch {
			case declared[kind]:
				t.Errorf("undeclaredUpdateKinds admits %q, which marotte now declares: drop the row", kind)
			case !slices.Contains(members, kind):
				t.Errorf("undeclaredUpdateKinds admits %q, which the bundle's union no longer names: drop the row", kind)
			}
		}
	})
}
