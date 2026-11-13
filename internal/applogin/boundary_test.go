package applogin

import (
	"go/ast"
	"go/types"
	"slices"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

const accessPkg = "github.com/cplieger/access"

// moduleSource is every non-test package of the module, type-checked once per test binary.
var moduleSource = sync.OnceValues(func() ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  "../..",
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, p.Errors[0]
		}
	}
	return pkgs, nil
})

type callSite struct {
	call *ast.CallExpr
	pkg  *packages.Package
	fn   string // the enclosing function, "pkg.Func" or "pkg.(*T).Method"
}

// calls lists every call in the module's non-test files whose callee match accepts.
func calls(t *testing.T, match func(*types.Func) bool) []callSite {
	t.Helper()
	pkgs, err := moduleSource()
	if err != nil {
		t.Fatalf("load the module: %v", err)
	}
	var out []callSite
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				name := p.Name + "." + fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) == 1 {
					name = p.Name + ".(" + types.ExprString(fd.Recv.List[0].Type) + ")." + fd.Name.Name
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					c, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if fn := calleeOf(p, c); fn != nil && match(fn) {
						out = append(out, callSite{call: c, pkg: p, fn: name})
					}
					return true
				})
			}
		}
	}
	return out
}

func calleeOf(p *packages.Package, c *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch fun := c.Fun.(type) {
	case *ast.SelectorExpr:
		id = fun.Sel
	case *ast.Ident:
		id = fun
	default:
		return nil
	}
	fn, _ := p.TypesInfo.Uses[id].(*types.Func)
	return fn
}

func isMaskedMethod(fn *types.Func, method string) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || fn.Name() != method {
		return false
	}
	n, ok := types.Unalias(sig.Recv().Type()).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != accessPkg {
		return false
	}
	return n.Obj().Name() == "Secret" || n.Obj().Name() == "Hash"
}

// TestBoundary_RevealSites: every Reveal in the module's non-test code is
// at a listed site, and every listed site reveals.
func TestBoundary_RevealSites(t *testing.T) {
	want := []string{
		"applogin.(*Origin).RecoveryLink",
		"applogin.(*Origin).SetupLinks",
		"applogin.KeyBytes",
		"applogin.NewAgentKeyVerifier",
		"applogin.encode",
	}
	var got []string
	for _, c := range calls(t, func(fn *types.Func) bool { return isMaskedMethod(fn, "Reveal") }) {
		if !slices.Contains(got, c.fn) {
			got = append(got, c.fn)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Reveal sites = %v, want exactly %v", got, want)
	}
}

// TestBoundary_CompareSites: Hash.Equal at exactly its two sites with their
// operands, and no generic equality over a type holding a Hash or a Secret.
func TestBoundary_CompareSites(t *testing.T) {
	var got []string
	for _, c := range calls(t, func(fn *types.Func) bool { return isMaskedMethod(fn, "Equal") }) {
		sel := c.call.Fun.(*ast.SelectorExpr)
		recv, arg := types.ExprString(sel.X), types.ExprString(c.call.Args[0])
		got = append(got, c.fn+": "+recv+".Equal("+arg+")")
	}
	slices.Sort(got)
	want := []string{
		"applogin.(*agentKeyVerifier).Verify: access.APIKeyHash(tok).Equal(v.hash)",
		"applogin.(*txView).RotateSession: cur.TokenHash.Equal(rot.ExpectHash)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Hash.Equal sites = %v, want exactly %v", got, want)
	}
	generic := func(fn *types.Func) bool {
		if fn.Pkg() == nil {
			return false
		}
		switch fn.Pkg().Path() + "." + fn.Name() {
		case "reflect.DeepEqual", "bytes.Equal", "crypto/subtle.ConstantTimeCompare":
			return true
		}
		return false
	}
	for _, c := range calls(t, generic) {
		for _, a := range c.call.Args {
			if holdsMasked(c.pkg.TypesInfo.TypeOf(a), map[types.Type]bool{}) {
				t.Errorf("%s compares %s, whose type holds a Hash or a Secret, with %s", c.fn, types.ExprString(a), types.ExprString(c.call.Fun))
			}
		}
	}
}

// holdsMasked reports whether t is or holds, through fields, pointers, slices, arrays or maps, an
// access.Hash or access.Secret.
func holdsMasked(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == accessPkg &&
		(n.Obj().Name() == "Hash" || n.Obj().Name() == "Secret") {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return holdsMasked(u.Elem(), seen)
	case *types.Slice:
		return holdsMasked(u.Elem(), seen)
	case *types.Array:
		return holdsMasked(u.Elem(), seen)
	case *types.Map:
		return holdsMasked(u.Key(), seen) || holdsMasked(u.Elem(), seen)
	case *types.Struct:
		for f := range u.Fields() {
			if holdsMasked(f.Type(), seen) {
				return true
			}
		}
	case *types.Interface:
		return false
	}
	return false
}
