package apitable_test

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/route"
	"github.com/cplieger/marotte/internal/server"
	"golang.org/x/tools/go/packages"
)

// owners is every package's Codes; TestCodes_EveryWrittenCodeIsOwned fails on a package that
// declares one and is missing here.
var owners = map[string]route.CodeSet{
	modulePath + "/internal/agent":      agent.Codes,
	modulePath + "/internal/command":    command.Codes,
	modulePath + "/internal/filebrowse": filebrowse.Codes,
	modulePath + "/internal/forges":     forges.Codes,
	modulePath + "/internal/git":        git.Codes,
	modulePath + "/internal/httpreply":  httpreply.Codes,
	modulePath + "/internal/server":     server.Codes,
}

// libraryCodes is each relayed library's code set, keyed by the arm that resolves to it.
var libraryCodes = map[string]route.CodeSet{
	"forgeapi": forges.ForgeAPICodes,
}

// sinkSpec places a sink's code and status: an argument index, or a fixed status.
type sinkSpec struct {
	code, status int // argument indexes; status -1 reads the enclosing WriteJSONStatus
	fixed        int // the status a library writes the code at, when status is -2
}

var sinks = map[string]sinkSpec{
	modulePath + "/internal/httpreply.ErrorJSONWithCode":   {code: 1, status: -1},
	"github.com/cplieger/go/httpserver.WriteError":         {code: 3, status: 2},
	"github.com/cplieger/go/httpserver.WithRateLimitError": {code: 0, status: -2, fixed: http.StatusTooManyRequests},
	modulePath + "/internal/command.StatusErrorReason":     {code: 1, status: 0},
}

// declaredSources are functions whose result is a code owned where it was constructed: the
// reason of command.StatusOf comes from a command.StatusErrorReason sink.
var declaredSources = map[string]int{
	modulePath + "/internal/command.StatusOf": 1,
}

const (
	writeJSONStatus = "github.com/cplieger/go/httpserver.WriteJSONStatus"
	writeJSON       = "github.com/cplieger/go/httpserver.WriteJSON"
)

// resolved is what an expression evaluates to: constants, a declared source, a library's code
// set, or a dependency on the enclosing function's parameters.
type resolved struct {
	consts  []constant.Value
	params  []int
	source  bool
	library string
	fail    string
}

func (r *resolved) merge(o resolved) {
	r.consts = append(r.consts, o.consts...)
	r.params = append(r.params, o.params...)
	r.source = r.source || o.source
	if o.library != "" {
		r.library = o.library
	}
	if r.fail == "" {
		r.fail = o.fail
	}
}

type walker struct {
	s     *source
	depth int
}

func (w *walker) failAt(p token.Pos, why string) resolved {
	return resolved{fail: w.s.pos(p) + ": " + why}
}

// resolve evaluates e inside decl. args, when non-nil, are the call-site values of decl's parameters.
func (w *walker) resolve(p *packages.Package, decl *ast.FuncDecl, e ast.Expr, args []resolved) resolved {
	if w.depth > 8 {
		return w.failAt(e.Pos(), "resolution too deep")
	}
	w.depth++
	defer func() { w.depth-- }()
	info := p.TypesInfo
	if tv, ok := info.Types[e]; ok && tv.Value != nil {
		return resolved{consts: []constant.Value{tv.Value}}
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		v, ok := info.Uses[x].(*types.Var)
		if !ok {
			return w.failAt(x.Pos(), "code "+x.Name+" is not a constant or a variable")
		}
		if i := paramIndex(info, decl, v); i >= 0 {
			if args != nil && i < len(args) {
				return args[i]
			}
			return resolved{params: []int{i}}
		}
		return w.local(p, decl, v, x.Pos(), args)
	case *ast.CallExpr:
		if tv, ok := info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return w.resolve(p, decl, x.Args[0], args)
		}
		return w.callResult(p, decl, x, 0, args)
	case *ast.SelectorExpr:
		if lib := libraryField(info, x); lib != "" {
			return resolved{library: lib}
		}
	}
	return w.failAt(e.Pos(), "a dynamic code: "+types.ExprString(e))
}

// libraryField names the library whose code x reads: the Code field of a forgeapi.Error.
func libraryField(info *types.Info, x *ast.SelectorExpr) string {
	if x.Sel.Name != "Code" {
		return ""
	}
	tv, ok := info.Types[x.X]
	if !ok {
		return ""
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if types.TypeString(t, nil) == "github.com/cplieger/forgeapi/v2.Error" {
		return "forgeapi"
	}
	return ""
}

func paramIndex(info *types.Info, decl *ast.FuncDecl, v *types.Var) int {
	i := 0
	for _, field := range decl.Type.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, name := range field.Names {
			if info.Defs[name] == v {
				return i
			}
			i++
		}
	}
	return -1
}

// local resolves v from every assignment to it in decl's body.
func (w *walker) local(p *packages.Package, decl *ast.FuncDecl, v *types.Var, at token.Pos, args []resolved) resolved {
	info := p.TypesInfo
	var out resolved
	found := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || (info.Defs[id] != v && info.Uses[id] != v) {
					continue
				}
				found = true
				switch {
				case len(s.Rhs) == len(s.Lhs):
					out.merge(w.resolve(p, decl, s.Rhs[i], args))
				case len(s.Rhs) == 1:
					if call, ok := ast.Unparen(s.Rhs[0]).(*ast.CallExpr); ok {
						out.merge(w.callResult(p, decl, call, i, args))
					} else {
						out.merge(w.failAt(s.Pos(), "a code from a tuple that is not a call"))
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if info.Defs[name] != v {
					continue
				}
				found = true
				if i < len(s.Values) {
					out.merge(w.resolve(p, decl, s.Values[i], args))
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{s.Key, s.Value} {
				if id, ok := e.(*ast.Ident); ok && (info.Defs[id] == v || info.Uses[id] == v) {
					found = true
					out.merge(w.failAt(s.Pos(), "a code from a range loop"))
				}
			}
		}
		return true
	})
	if !found {
		return w.failAt(at, "code variable "+v.Name()+" is never assigned in its function")
	}
	return out
}

// callResult resolves result k of call: a declared source, or the k-th result of every return of
// a function whose body the module holds.
func (w *walker) callResult(p *packages.Package, decl *ast.FuncDecl, call *ast.CallExpr, k int, args []resolved) resolved {
	name := calleeName(p.TypesInfo, call)
	if idx, ok := declaredSources[name]; ok {
		if k == idx {
			return resolved{source: true}
		}
		return w.failAt(call.Pos(), "a declared source's other result")
	}
	site, ok := w.s.funcs[name]
	if !ok {
		return w.failAt(call.Pos(), "a code from "+types.ExprString(call.Fun)+", whose body is not in the module")
	}
	callArgs := make([]resolved, len(call.Args))
	for i, a := range call.Args {
		callArgs[i] = w.resolve(p, decl, a, args)
	}
	var out resolved
	returns := 0
	ast.Inspect(site.decl.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		returns++
		switch {
		case len(ret.Results) > k && len(ret.Results) > 1 || len(ret.Results) == 1 && k == 0:
			out.merge(w.resolve(site.pkg, site.decl, ret.Results[k], callArgs))
		case len(ret.Results) == 1:
			if inner, ok := ast.Unparen(ret.Results[0]).(*ast.CallExpr); ok {
				out.merge(w.callResult(site.pkg, site.decl, inner, k, callArgs))
			}
		default:
			out.merge(w.failAt(ret.Pos(), "a bare return of a code"))
		}
		return true
	})
	if returns == 0 {
		return w.failAt(call.Pos(), "a code from a function with no return")
	}
	return out
}

// site is one place a code is written, before its call-site arguments are known.
type site struct {
	pkg        *packages.Package
	decl       *ast.FuncDecl
	code       ast.Expr
	status     ast.Expr // nil: fixed, or unplaced when placed is false
	statusPkg  *packages.Package
	statusDecl *ast.FuncDecl
	fixed      int
	pos        token.Pos
	unplaced   bool
}

// written is one resolved code a package writes.
type written struct {
	pkg, code, library string
	status             int
	pos                string
}

// enclosingStatus finds the status a body n is written with: the WriteJSONStatus call whose body is n,
// a local holding n, or a call of decl when decl returns n. ok200 is a body WriteJSON writes, which
// is no error envelope. A nil status with ok200 false is a body the walk could not place.
func enclosingStatus(s *source, p *packages.Package, decl *ast.FuncDecl, n ast.Node) (status ast.Expr, at *packages.Package, atDecl *ast.FuncDecl, ok200 bool) {
	info := p.TypesInfo
	holds := func(arg ast.Expr) bool { return bodyRef(info, decl, arg, n) }
	ast.Inspect(decl.Body, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		switch calleeName(info, call) {
		case writeJSONStatus:
			if len(call.Args) == 3 && holds(call.Args[2]) {
				status, at, atDecl = call.Args[1], p, decl
			}
		case writeJSON:
			if holds(call.Args[len(call.Args)-1]) {
				ok200 = true
			}
		}
		return true
	})
	if status != nil || ok200 || !returns(info, decl, n) {
		return status, at, atDecl, ok200
	}
	fn, _ := info.Defs[decl.Name].(*types.Func)
	if fn == nil {
		return nil, nil, nil, false
	}
	s.eachFunc(func(q *packages.Package, d *ast.FuncDecl) {
		ast.Inspect(d.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if !callsOf(q.TypesInfo, d, call.Args[len(call.Args)-1], fn.FullName()) {
				return true
			}
			switch calleeName(q.TypesInfo, call) {
			case writeJSONStatus:
				if len(call.Args) == 3 {
					status, at, atDecl = call.Args[1], q, d
				}
			case writeJSON:
				ok200 = true
			}
			return true
		})
	})
	return status, at, atDecl, ok200
}

// callsOf reports whether arg is a call of fn, or a local of decl assigned one.
func callsOf(info *types.Info, decl *ast.FuncDecl, arg ast.Expr, fn string) bool {
	if c, ok := ast.Unparen(arg).(*ast.CallExpr); ok {
		return calleeName(info, c) == fn
	}
	id, ok := ast.Unparen(arg).(*ast.Ident)
	if !ok {
		return false
	}
	v := info.Uses[id]
	found := false
	ast.Inspect(decl.Body, func(m ast.Node) bool {
		if as, ok := m.(*ast.AssignStmt); ok {
			for i, l := range as.Lhs {
				li, ok := l.(*ast.Ident)
				if !ok || i >= len(as.Rhs) || (info.Defs[li] != v && info.Uses[li] != v) {
					continue
				}
				if c, ok := ast.Unparen(as.Rhs[i]).(*ast.CallExpr); ok && calleeName(info, c) == fn {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// bodyRef reports whether arg is n, &n, or a local of decl whose value is n.
func bodyRef(info *types.Info, decl *ast.FuncDecl, arg ast.Expr, n ast.Node) bool {
	arg = ast.Unparen(arg)
	if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
		arg = ast.Unparen(u.X)
	}
	if arg == n {
		return true
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	v := info.Uses[id]
	found := false
	ast.Inspect(decl.Body, func(m ast.Node) bool {
		switch x := m.(type) {
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				if li, ok := l.(*ast.Ident); ok && i < len(x.Rhs) && (info.Defs[li] == v || info.Uses[li] == v) && ast.Unparen(x.Rhs[i]) == n {
					found = true
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if info.Defs[name] == v && i < len(x.Values) && ast.Unparen(x.Values[i]) == n {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// returns reports whether decl returns n, directly or through a local holding it.
func returns(info *types.Info, decl *ast.FuncDecl, n ast.Node) bool {
	found := false
	ast.Inspect(decl.Body, func(m ast.Node) bool {
		if r, ok := m.(*ast.ReturnStmt); ok {
			for _, res := range r.Results {
				if bodyRef(info, decl, res, n) {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// isTypedErrorBody reports a struct with both an "error" and a "code" JSON field.
func isTypedErrorBody(t types.Type) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	var hasError, hasCode bool
	for i := range st.NumFields() {
		switch jsonName(st.Tag(i)) {
		case "error":
			hasError = true
		case "code":
			hasCode = true
		}
	}
	return hasError && hasCode
}

func jsonName(tag string) string {
	name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
	return name
}

// responseWriters are the calls whose last argument is a response body.
var responseWriters = map[string]bool{
	"github.com/cplieger/go/httpserver.WriteJSON":       true,
	"github.com/cplieger/go/httpserver.WriteJSONStatus": true,
}

// codeSites is every code sink, and per typed error body written as a response how many of its
// literals set Code.
func codeSites(s *source) (sites []site, bodies map[string]int) {
	bodies = map[string]int{}
	written := map[string]bool{}
	s.eachFunc(func(p *packages.Package, decl *ast.FuncDecl) {
		info := p.TypesInfo
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if responseWriters[calleeName(info, x)] && len(x.Args) > 0 {
					if tv, ok := info.Types[x.Args[len(x.Args)-1]]; ok && isTypedErrorBody(tv.Type) {
						written[types.TypeString(tv.Type, nil)] = true
					}
				}
				spec, ok := sinks[calleeName(info, x)]
				if !ok || spec.code >= len(x.Args) {
					return true
				}
				st := site{pkg: p, decl: decl, code: x.Args[spec.code], pos: x.Pos(), fixed: spec.fixed}
				switch spec.status {
				case -1:
					var ok200 bool
					st.status, st.statusPkg, st.statusDecl, ok200 = enclosingStatus(s, p, decl, x)
					if ok200 {
						return true
					}
					st.unplaced = st.status == nil
				case -2:
				default:
					st.status = x.Args[spec.status]
				}
				sites = append(sites, st)
			case *ast.CompositeLit:
				tv, ok := info.Types[x]
				if !ok || !isTypedErrorBody(tv.Type) {
					return true
				}
				bodies[types.TypeString(tv.Type, nil)] += 0
				for _, el := range x.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if id, isID := kv.Key.(*ast.Ident); ok && isID && id.Name == "Code" {
						st := site{pkg: p, decl: decl, code: kv.Value, pos: x.Pos()}
						var ok200 bool
						st.status, st.statusPkg, st.statusDecl, ok200 = enclosingStatus(s, p, decl, x)
						if ok200 {
							delete(bodies, types.TypeString(tv.Type, nil))
							return true
						}
						bodies[types.TypeString(tv.Type, nil)]++
						st.unplaced = st.status == nil
						sites = append(sites, st)
					}
				}
			}
			return true
		})
	})
	for body := range bodies {
		if !written[body] {
			delete(bodies, body)
		}
	}
	return sites, bodies
}

// evalSite evaluates st's code and status, with args as its function's parameters when non-nil.
func evalSite(w *walker, st site, args []resolved) (code, status resolved) {
	code = w.resolve(st.pkg, st.decl, st.code, args)
	switch {
	case st.status == nil:
	case st.statusDecl == nil || st.statusDecl == st.decl:
		status = w.resolve(st.pkg, st.decl, st.status, args)
	default:
		status = w.resolve(st.statusPkg, st.statusDecl, st.status, nil)
	}
	return code, status
}

// writtenCodes resolves every code sink, following wrappers (a function whose parameter reaches a
// sink) to their call sites, to a fixed point.
func writtenCodes(t *testing.T, s *source) ([]written, map[string]int) {
	t.Helper()
	sites, bodies := codeSites(s)
	w := &walker{s: s}
	var out []written
	type pending struct {
		st   site
		args []resolved
		from *packages.Package
		at   token.Pos
	}
	queue := make([]pending, 0, len(sites))
	for _, st := range sites {
		queue = append(queue, pending{st: st, from: st.pkg, at: st.pos})
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		pd := queue[0]
		queue = queue[1:]
		code, status := evalSite(w, pd.st, pd.args)
		if len(code.params)+len(status.params) > 0 && pd.args == nil {
			// A wrapper: each call of pd.st.decl is a site of its own.
			wrapped := pd.st.pkg.TypesInfo.Defs[pd.st.decl.Name].(*types.Func).FullName()
			key := wrapped + "@" + s.pos(pd.st.pos)
			if seen[key] {
				continue
			}
			seen[key] = true
			s.eachFunc(func(p *packages.Package, decl *ast.FuncDecl) {
				ast.Inspect(decl.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || calleeName(p.TypesInfo, call) != wrapped {
						return true
					}
					args := make([]resolved, len(call.Args))
					for i, a := range call.Args {
						args[i] = w.resolve(p, decl, a, nil)
					}
					queue = append(queue, pending{st: pd.st, args: args, from: p, at: call.Pos()})
					return true
				})
			})
			continue
		}
		at := s.pos(pd.at)
		switch {
		case code.fail != "":
			t.Errorf("%s: unresolved code: %s", at, code.fail)
			continue
		case len(code.params) > 0:
			t.Errorf("%s: code still depends on a parameter after a call site", at)
			continue
		case code.source:
			continue
		}
		if pd.st.unplaced && code.library == "" {
			t.Errorf("%s: the status this code is answered with is not found", at)
			continue
		}
		statuses := []int{pd.st.fixed}
		if pd.st.status != nil {
			statuses = statuses[:0]
			if status.fail != "" || len(status.params) > 0 || status.source {
				if code.library == "" {
					t.Errorf("%s: unresolved status: %s", at, status.fail)
					continue
				}
			}
			for _, c := range status.consts {
				n, _ := constant.Int64Val(c)
				statuses = append(statuses, int(n))
			}
		}
		if code.library != "" {
			out = append(out, written{pkg: pd.from.PkgPath, library: code.library, pos: at})
			continue
		}
		for _, c := range code.consts {
			if c.Kind() != constant.String {
				t.Errorf("%s: a code that is not a string", at)
				continue
			}
			for _, st := range statuses {
				out = append(out, written{pkg: pd.from.PkgPath, code: constant.StringVal(c), status: st, pos: at})
			}
		}
	}
	return out, bodies
}

func codeIn(set route.CodeSet, code string) (route.Code, bool) {
	i := slices.IndexFunc(set, func(c route.Code) bool { return c.Code == code })
	if i < 0 {
		return route.Code{}, false
	}
	return set[i], true
}

func TestCodes_EveryWrittenCodeIsOwned(t *testing.T) {
	s := loaded(t)
	for _, p := range s.pkgs {
		obj := p.Types.Scope().Lookup("Codes")
		if obj == nil || types.TypeString(obj.Type(), nil) != modulePath+"/internal/route.CodeSet" {
			continue
		}
		if _, ok := owners[p.PkgPath]; !ok {
			t.Errorf("%s declares Codes, which this test's owners map lacks", p.PkgPath)
		}
	}
	writes, bodies := writtenCodes(t, s)
	for body, n := range bodies {
		if n == 0 {
			t.Errorf("typed error body %s is written with no Code the walk resolves", body)
		}
	}
	for _, wr := range writes {
		if wr.library != "" {
			if libraryCodes[wr.library] == nil {
				t.Errorf("%s: library %s has no code set", wr.pos, wr.library)
			}
			continue
		}
		set, ok := owners[wr.pkg]
		if !ok {
			t.Errorf("%s: %s writes %q and declares no Codes", wr.pos, wr.pkg, wr.code)
			continue
		}
		c, ok := codeIn(set, wr.code)
		switch {
		case !ok:
			t.Errorf("%s: %s writes %q at %d, which its Codes lacks", wr.pos, wr.pkg, wr.code, wr.status)
		case c.Status != wr.status:
			t.Errorf("%s: %s writes %q at %d, its Codes lists %d", wr.pos, wr.pkg, wr.code, wr.status, c.Status)
		}
	}
	statusOf := map[string]int{}
	check := func(owner string, set route.CodeSet) {
		for _, c := range set {
			if c.Status < 400 || c.Status > 599 {
				t.Errorf("%s lists %q at %d, outside 400 to 599", owner, c.Code, c.Status)
			}
			if prev, ok := statusOf[c.Code]; ok && prev != c.Status {
				t.Errorf("%q is listed at %d and at %d; a code has one status", c.Code, prev, c.Status)
			}
			statusOf[c.Code] = c.Status
		}
	}
	for owner, set := range owners {
		check(owner, set)
	}
	for lib, set := range libraryCodes {
		check(lib, set)
	}
}

func TestCodes_EveryOwnedCodeIsWritten(t *testing.T) {
	writes, _ := writtenCodes(t, loaded(t))
	for owner, set := range owners {
		for _, c := range set {
			if !slices.ContainsFunc(writes, func(wr written) bool { return wr.pkg == owner && wr.code == c.Code }) {
				t.Errorf("%s lists %q, which no sink in the package writes", owner, c.Code)
			}
		}
	}
}
