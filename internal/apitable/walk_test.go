package apitable_test

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/cplieger/marotte"

// source is every package of the module, parsed and type-checked once per test binary.
type source struct {
	err   error
	fset  *token.FileSet
	pkgs  []*packages.Package
	funcs map[string]*funcSite // keyed by types.Func.FullName, so a call from another package finds it
}

type funcSite struct {
	pkg  *packages.Package
	decl *ast.FuncDecl
	obj  *types.Func
}

var loadSource = sync.OnceValue(func() *source {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports,
		Dir: "../..",
	}
	pkgs, err := packages.Load(cfg, "./...")
	s := &source{err: err, pkgs: pkgs, funcs: map[string]*funcSite{}}
	if err != nil {
		return s
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			s.err = p.Errors[0]
			return s
		}
		s.fset = p.Fset
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				if obj, ok := p.TypesInfo.Defs[fd.Name].(*types.Func); ok {
					s.funcs[obj.FullName()] = &funcSite{pkg: p, decl: fd, obj: obj}
				}
			}
		}
	}
	return s
})

func loaded(t *testing.T) *source {
	t.Helper()
	s := loadSource()
	if s.err != nil {
		t.Fatalf("load the module's packages: %v", s.err)
	}
	return s
}

func (s *source) pos(p token.Pos) string {
	at := s.fset.Position(p)
	rel, err := filepath.Rel(filepath.Join("..", ".."), at.Filename)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = at.Filename
	}
	return rel + ":" + strconv.Itoa(at.Line)
}

// calleeName is the full name of the function or method call invokes, or "".
func calleeName(info *types.Info, call *ast.CallExpr) string {
	var id *ast.Ident
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fn
	case *ast.SelectorExpr:
		id = fn.Sel
	case *ast.IndexExpr:
		if sel, ok := fn.X.(*ast.SelectorExpr); ok {
			id = sel.Sel
		} else if i, ok := fn.X.(*ast.Ident); ok {
			id = i
		}
	default:
		return ""
	}
	if f, ok := info.Uses[id].(*types.Func); ok {
		if o := f.Origin(); o != nil {
			f = o
		}
		return f.FullName()
	}
	return ""
}

// eachFunc calls fn for every function body of every non-test file, the enclosing declaration
// with it (function literals belong to their declaration).
func (s *source) eachFunc(fn func(p *packages.Package, decl *ast.FuncDecl)) {
	for _, p := range s.pkgs {
		for _, f := range p.Syntax {
			if strings.HasSuffix(p.Fset.Position(f.Pos()).Filename, "_test.go") {
				continue
			}
			var inits []ast.Stmt
			for _, d := range f.Decls {
				switch x := d.(type) {
				case *ast.FuncDecl:
					if x.Body != nil {
						fn(p, x)
					}
				case *ast.GenDecl:
					if x.Tok != token.VAR {
						continue
					}
					for _, spec := range x.Specs {
						for _, v := range spec.(*ast.ValueSpec).Values {
							inits = append(inits, &ast.ExprStmt{X: v})
						}
					}
				}
			}
			// A package-level initializer can hold a sink or a read too; it runs as a parameterless body.
			if len(inits) > 0 {
				fn(p, &ast.FuncDecl{Name: ast.NewIdent("init"), Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{List: inits}})
			}
		}
	}
}

func TestEndpoints_NoDirectMuxRegistration(t *testing.T) {
	s := loaded(t)
	for _, p := range s.pkgs {
		for _, f := range p.Syntax {
			name := p.Fset.Position(f.Pos()).Filename
			if strings.HasSuffix(name, "_test.go") || p.PkgPath == modulePath+"/internal/route/mount" {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch calleeName(p.TypesInfo, call) {
				case "(*net/http.ServeMux).Handle", "(*net/http.ServeMux).HandleFunc":
					t.Errorf("%s: a direct ServeMux registration; bind through the registrar", s.pos(call.Pos()))
				}
				return true
			})
		}
	}
}

// queryRead reports the key expression of a read of a request's query or form: Request.FormValue,
// and url.Values Get, Has or an index on values from the request (fromRequest).
func queryRead(info *types.Info, decl *ast.FuncDecl, n ast.Node) (ast.Expr, bool) {
	switch x := n.(type) {
	case *ast.CallExpr:
		switch calleeName(info, x) {
		case "(*net/http.Request).FormValue":
			if len(x.Args) == 1 {
				return x.Args[0], true
			}
		case "(net/url.Values).Get", "(net/url.Values).Has":
			if sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok && len(x.Args) == 1 && fromRequest(info, decl, sel.X) {
				return x.Args[0], true
			}
		}
	case *ast.IndexExpr:
		if tv, ok := info.Types[x.X]; ok && types.TypeString(tv.Type, nil) == "net/url.Values" && fromRequest(info, decl, x.X) {
			return x.Index, true
		}
	}
	return nil, false
}

// fromRequest reports whether values e may be a request's query: a call of an *http.Request's
// URL.Query(), a local assigned one, or a parameter, which a caller can pass one through. Values
// parsed from any other URL (an outbound callback, a pasted link) are not the request's.
func fromRequest(info *types.Info, decl *ast.FuncDecl, e ast.Expr) bool {
	e = ast.Unparen(e)
	if requestQuery(info, e) {
		return true
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	v, ok := info.Uses[id].(*types.Var)
	if !ok {
		return false
	}
	for _, f := range decl.Type.Params.List {
		for _, n := range f.Names {
			if info.Defs[n] == v {
				return true
			}
		}
	}
	found := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, l := range as.Lhs {
			if li, ok := l.(*ast.Ident); ok && (info.Defs[li] == v || info.Uses[li] == v) && requestQuery(info, as.Rhs[i]) {
				found = true
			}
		}
		return !found
	})
	return found
}

// requestQuery reports whether e is X.URL.Query() for an X of type *http.Request.
func requestQuery(info *types.Info, e ast.Expr) bool {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Query" {
		return false
	}
	u, ok := sel.X.(*ast.SelectorExpr)
	if !ok || u.Sel.Name != "URL" {
		return false
	}
	tv, ok := info.Types[u.X]
	return ok && types.TypeString(tv.Type, nil) == "*net/http.Request"
}
