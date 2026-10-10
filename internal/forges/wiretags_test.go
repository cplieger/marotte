package forges_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/wirespec"
	"github.com/cplieger/wiregen/v3"
)

// Both paths are read off a type rather than written as literals, so a moved
// package cannot leave the walk scanning nothing.
var (
	forgesPkgPath   = wiregen.TypeRef[forges.PR]().PkgPath
	forgeapiPkgPath = wiregen.TypeRef[forgeapi.RepoRef]().PkgPath
)

type typeDecl struct {
	spec     *ast.TypeSpec
	forgeapi map[string]bool
}

// forgeTypeDecls parses every non-test file of this package. Source rather than
// reflection, because the registry holds names and only the declaration says
// what a field's type is written as.
func forgeTypeDecls(t *testing.T) map[string]typeDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/forges: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]typeDecl{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		imported := forgeapiImportNames(file)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					out[ts.Name.Name] = typeDecl{spec: ts, forgeapi: imported}
				}
			}
		}
	}
	return out
}

func forgeapiImportNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || (path != forgeapiPkgPath && !strings.HasPrefix(path, forgeapiPkgPath+"/")) {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		names[local] = true
	}
	return names
}

func forgeapiTypeIn(decls map[string]typeDecl, expr ast.Expr, imported, seen map[string]bool) string {
	found := ""
	ast.Inspect(expr, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := n.X.(*ast.Ident); ok && imported[pkg.Name] {
				found = pkg.Name + "." + n.Sel.Name
			}
			return false
		case *ast.Ident:
			d, local := decls[n.Name]
			if local && !seen[n.Name] {
				seen[n.Name] = true
				found = forgeapiTypeIn(decls, d.spec.Type, d.forgeapi, seen)
			}
		}
		return true
	})
	return found
}

// TestWireTags_EveryRegisteredForgeStruct holds every struct the wire registry
// takes from this package to two rules: each field that reaches the wire names
// its JSON key, and none holds a forgeapi type. The first is what keeps the
// generated TypeScript's keys the ones the routes write; the second keeps a
// library change from changing the client's wire.
func TestWireTags_EveryRegisteredForgeStruct(t *testing.T) {
	decls := forgeTypeDecls(t)
	var registered []string
	for _, wt := range wirespec.Registry().Types {
		if wt.PkgPath == forgesPkgPath {
			registered = append(registered, wt.Name)
		}
	}
	if len(registered) == 0 {
		t.Fatalf("the wire registry holds no type from %s; the walk is broken, not the package", forgesPkgPath)
	}
	for _, name := range registered {
		t.Run(name, func(t *testing.T) {
			d, ok := decls[name]
			if !ok {
				t.Fatalf("forges.%s is registered but declared in no non-test file of the package", name)
			}
			st, ok := d.spec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("forges.%s is registered as a wire type but is not a struct", name)
			}
			for _, field := range st.Fields.List {
				for _, fieldName := range wireFieldNames(field) {
					if key := jsonKey(field); key == "" {
						t.Errorf("forges.%s.%s reaches the wire with no json key in its tag", name, fieldName)
					}
					if via := forgeapiTypeIn(decls, field.Type, d.forgeapi, map[string]bool{}); via != "" {
						t.Errorf("forges.%s.%s holds %s; a wire struct carries plain values", name, fieldName, via)
					}
				}
			}
		})
	}
}

// wireFieldNames answers the exported names field declares, the only ones
// encoding/json writes. An embedded field is named for its type.
func wireFieldNames(field *ast.Field) []string {
	var out []string
	if len(field.Names) == 0 {
		typ := field.Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		switch typ := typ.(type) {
		case *ast.Ident:
			out = append(out, typ.Name)
		case *ast.SelectorExpr:
			out = append(out, typ.Sel.Name)
		}
	}
	for _, n := range field.Names {
		out = append(out, n.Name)
	}
	exported := out[:0]
	for _, n := range out {
		if ast.IsExported(n) {
			exported = append(exported, n)
		}
	}
	return exported
}

func jsonKey(field *ast.Field) string {
	if field.Tag == nil {
		return ""
	}
	raw, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return ""
	}
	key, _, _ := strings.Cut(reflect.StructTag(raw).Get("json"), ",")
	return key
}
