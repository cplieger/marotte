package apitable_test

import (
	"go/ast"
	"go/constant"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/apitable"
	"golang.org/x/tools/go/packages"
)

// declaredQueryKeys is, per package, every key a Query type that package defines declares.
func declaredQueryKeys() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, e := range apitable.Entries() {
		if e.Query == nil {
			continue
		}
		pkg := e.Query.PkgPath()
		if out[pkg] == nil {
			out[pkg] = map[string]bool{}
		}
		for f := range e.Query.Fields() {
			key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			out[pkg][key] = true
		}
	}
	return out
}

func TestEndpoints_QueryReadsAreDeclared(t *testing.T) {
	s := loaded(t)
	declared := declaredQueryKeys()
	reads := 0
	s.eachFunc(func(p *packages.Package, decl *ast.FuncDecl) {
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			key, ok := queryRead(p.TypesInfo, decl, n)
			if !ok {
				return true
			}
			reads++
			tv := p.TypesInfo.Types[key]
			if tv.Value == nil || tv.Value.Kind() != constant.String {
				t.Errorf("%s: a query key that is not a constant expression", s.pos(key.Pos()))
				return true
			}
			if k := constant.StringVal(tv.Value); !declared[p.PkgPath][k] {
				t.Errorf("%s: reads query key %q, which no Query type of %s declares", s.pos(key.Pos()), k, p.PkgPath)
			}
			return true
		})
	})
	if reads < 20 {
		t.Errorf("found %d query reads, want the handlers' full set", reads)
	}
}
