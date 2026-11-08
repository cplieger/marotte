package agent

// A chat's session is reachable only through a lease on its life (session_lease.go). These censuses parse every
// production file of the package and fail on any access the lease does not own: a read of the field outside the
// lease file, an unclassified lease method, a second constructor, a stored session, a widening assertion, a session
// that escapes its lease's callback, an unlisted session-taking function, an unlisted writer of a record's session
// id, and a command seam that hands out a session. Each check has a fixture row holding one violation.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/command"
)

// leaseField is sharedBridge's session field; only functions in leaseFile may name it.
const (
	leaseField = "leased"
	leaseFile  = "session_lease.go"
)

// leaseKinds classifies every *sharedBridge method declared in the lease file.
var leaseKinds = map[string]string{
	"use": "use", "lease": "use", "Call": "use", "CallAt": "use", "PromptAt": "use", "Notify": "use",
	"useRun":    "useRun",
	"responder": "responder", "Respond": "responder",
	"closeOwned": "closer",
	"attach":     "attach",
	"holds":      "fact", "hasSession": "fact", "open": "fact", "endedOnItsOwn": "fact", "sessionDeadLocked": "fact",
	"SessionID": "fact", "awaitAbandonedPrompt": "fact", "observeEffort": "fact", "observeThinking": "fact",
}

// sessionTypes are the package's interfaces whose method set holds an engine call on a session; the census derives
// the set from the source and fails when it differs from this list.
var sessionTypes = []string{"acpCaller", "acpSessionCaller", "engineSession", "newSessionFacts", "renamableSession"}

// engineLaneTypes match sessionTypes' signatures but are the engine, whose calls go to the engine lane; a session is
// reached from one only through its door methods, which sessionTakers lists.
var engineLaneTypes = []string{"engineHandle"}

// engineCalls are the methods that make an interface a session type when their signature equals engineSession's.
var engineCalls = []string{"Call", "CallAt", "PromptAt", "Notify", "SetModel", "EnsureEffort", "EnsureThinking", "Close", "Release"}

// sessionStores are the only struct fields that may hold a session: the entry's lease and the decorators that are
// the session they wrap. Keyed "struct.field", an embedded field by its type name.
var sessionStores = []string{"sharedBridge.leased", "guardedSession.engineSession", "hostRelay.engineSession"}

// sessionTakers lists every function with a session-typed parameter, by receiver-qualified name, with its role. A
// "lease helper" is called from inside a lease on the session it is handed, so its body follows the lease's rules.
var sessionTakers = map[string]string{
	"sharedBridge.attach":                         "the lease file",
	"sharedBridge.endedOnItsOwn":                  "the lease file",
	"applyModelSwitchOn":                          "lease helper",
	"bridgeManager.removeIfBridge":                "lease helper",
	"Runs.workflowNew":                            "lease helper",
	"bridgeCoordinator.markReplayLoaded":          "door step",
	"bridgeCoordinator.persistNewSessionMetadata": "door step",
	"reassertUserName":                            "door step",
	"Runtime.answerEarly":                         "door step",
	"bridgeCoordinator.goForward":                 "the forward",
	"bridgeCoordinator.goRunForward":              "the forward",
	"bridgeCoordinator.forwardAt":                 "the forward",
	"bridgeCoordinator.drainLane":                 "the forward",
	"closeBlankLoad":                              "door step",
	"guardSession":                                "decorator",
	"newHostRelay":                                "decorator",
	"Runtime.closeSession":                        "the closer",
	"engineOwner.closePass":                       "observer pass",
	"drainSession":                                "observer pass",
	"observerCall":                                "observer pass",
	"Runtime.observerForward":                     "observer pass",
}

// recordSessionWriters are the only functions that write a chat record's session id: the door's record and detach,
// the empty-turn refresh's lease, and the two record creations, which run before the chat has a life.
var recordSessionWriters = map[string][]string{
	"agent":   {"bridgeCoordinator.detachRetried", "bridgeCoordinator.persistNewSessionMetadata", "bridgeCoordinator.retireSession"},
	"command": {"cmdResumeSession", "forkCreate"},
}

// leaseCallers are the lease methods whose function-literal callback receives the session.
var leaseCallers = []string{"use", "useClosing", "useRun", "closeOwned", "useSession"}

// A parse of internal/command that finds fewer files is wrongly rooted. 39 measured.
const commandFileFloor = 30

type leaseCensus struct {
	fset  *token.FileSet
	files []*ast.File
	// ifaces are the package's interface declarations by name.
	ifaces map[string]*ast.InterfaceType
}

func newLeaseCensus(t *testing.T, fixtures map[string]string) *leaseCensus {
	t.Helper()
	fset := token.NewFileSet()
	files := parseAgentSources(t, fset)
	for _, name := range slices.Sorted(mapsKeys(fixtures)) {
		f, err := parser.ParseFile(fset, name, fixtures[name], parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse fixture %s: %v", name, err)
		}
		files = append(files, f)
	}
	c := &leaseCensus{fset: fset, files: files, ifaces: map[string]*ast.InterfaceType{}}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if it, ok := ts.Type.(*ast.InterfaceType); ok {
					c.ifaces[ts.Name.Name] = it
				}
			}
			return true
		})
	}
	return c
}

func mapsKeys(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (c *leaseCensus) fileName(f *ast.File) string {
	return filepath.Base(c.fset.Position(f.Package).Filename)
}

func (c *leaseCensus) pos(p token.Pos) string {
	position := c.fset.Position(p)
	return filepath.Base(position.Filename) + ":" + itoa(position.Line)
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

// methodSet resolves an interface's methods, package-local embedding included, to their parameter-free signatures.
func (c *leaseCensus) methodSet(name string, seen map[string]bool) map[string]string {
	out := map[string]string{}
	it, ok := c.ifaces[name]
	if !ok || seen[name] {
		return out
	}
	seen[name] = true
	for _, field := range it.Methods.List {
		if ft, ok := field.Type.(*ast.FuncType); ok {
			for _, n := range field.Names {
				out[n.Name] = signature(ft)
			}
			continue
		}
		if id, ok := field.Type.(*ast.Ident); ok {
			maps.Copy(out, c.methodSet(id.Name, seen))
		}
	}
	return out
}

func signature(ft *ast.FuncType) string {
	list := func(fl *ast.FieldList) string {
		if fl == nil {
			return ""
		}
		var parts []string
		for _, f := range fl.List {
			for range max(1, len(f.Names)) {
				parts = append(parts, types.ExprString(f.Type))
			}
		}
		return strings.Join(parts, ", ")
	}
	return "func(" + list(ft.Params) + ") (" + list(ft.Results) + ")"
}

// derivedSessionTypes are the interfaces holding at least one engine call with engineSession's signature.
func (c *leaseCensus) derivedSessionTypes() []string {
	ref := c.methodSet("engineSession", map[string]bool{})
	var out []string
	for name := range c.ifaces {
		set := c.methodSet(name, map[string]bool{})
		for _, m := range engineCalls {
			if sig, ok := set[m]; ok && ref[m] != "" && sig == ref[m] {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// isSession reports whether e names a session type: *bridge.Session or a listed session interface.
func isSession(e ast.Expr) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return slices.Contains(sessionTypes, x.Name)
	case *ast.StarExpr:
		if sel, ok := x.X.(*ast.SelectorExpr); ok {
			pkg, ok := sel.X.(*ast.Ident)
			return ok && pkg.Name == "bridge" && sel.Sel.Name == "Session"
		}
	}
	return false
}

// holdsSession reports whether a value of type e stores a session: e is one, or a pointer, array, slice, map or
// channel of one. A function type only passes one through.
func holdsSession(e ast.Expr) bool {
	if isSession(e) {
		return true
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.StarExpr:
		return holdsSession(x.X)
	case *ast.ArrayType:
		return holdsSession(x.Elt)
	case *ast.MapType:
		return holdsSession(x.Key) || holdsSession(x.Value)
	case *ast.ChanType:
		return holdsSession(x.Value)
	}
	return false
}

func (c *leaseCensus) fieldOwner() []string {
	var bad []string
	declared := 0
	for _, f := range c.files {
		inLeaseFile := c.fileName(f) == leaseFile
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Field:
				for _, name := range x.Names {
					if name.Name == leaseField {
						declared++
					}
				}
			case *ast.SelectorExpr:
				if x.Sel.Name == leaseField && !inLeaseFile {
					bad = append(bad, c.pos(x.Pos())+": reads sharedBridge."+leaseField+" outside "+leaseFile)
				}
			}
			return true
		})
	}
	if declared != 1 {
		bad = append(bad, "the field name "+leaseField+" is declared "+itoa(declared)+" times, want once (sharedBridge's)")
	}
	return bad
}

func (c *leaseCensus) classified() []string {
	var bad []string
	found := map[string]bool{}
	for _, f := range c.files {
		inLeaseFile := c.fileName(f) == leaseFile
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			recv, method, isMethod := strings.Cut(qualifiedName(fn), ".")
			if isMethod && recv == "sharedBridge" && inLeaseFile {
				found[method] = true
				if _, ok := leaseKinds[method]; !ok {
					bad = append(bad, c.pos(fn.Pos())+": sharedBridge."+method+" is not classified in leaseKinds")
				}
			}
		}
	}
	for m := range leaseKinds {
		if !found[m] {
			bad = append(bad, "leaseKinds lists sharedBridge."+m+", which "+leaseFile+" does not declare")
		}
	}
	return bad
}

func (c *leaseCensus) oneConstructor() []string {
	var bad []string
	literals := 0
	for _, f := range c.files {
		inLeaseFile := c.fileName(f) == leaseFile
		for _, decl := range f.Decls {
			name := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = qualifiedName(fn)
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isIdent(lit.Type, "sharedBridge") {
					return true
				}
				if !inLeaseFile || name != "newSharedBridge" {
					bad = append(bad, c.pos(lit.Pos())+": "+name+" builds a sharedBridge literal; only newSharedBridge may")
				} else {
					literals++
				}
				return true
			})
		}
	}
	if literals != 1 {
		bad = append(bad, "newSharedBridge holds "+itoa(literals)+" sharedBridge literals, want 1")
	}
	return bad
}

func (c *leaseCensus) noStore() []string {
	var bad []string
	for _, f := range c.files {
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					if vs.Type != nil && holdsSession(vs.Type) && (len(vs.Names) != 1 || vs.Names[0].Name != "_") {
						bad = append(bad, c.pos(vs.Pos())+": a package variable holds a session")
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				if st, ok := x.Type.(*ast.StructType); ok {
					bad = append(bad, c.structStores(x.Name.Name, st)...)
					return true
				}
				if !isSession(x.Type) && holdsSession(x.Type) {
					bad = append(bad, c.pos(x.Pos())+": type "+x.Name.Name+" holds a session")
				}
			case *ast.CompositeLit:
				if lt, ok := x.Type.(*ast.StructType); ok {
					bad = append(bad, c.structStores("", lt)...)
				}
			case *ast.CallExpr:
				if isIdent(x.Fun, "make") || isIdent(x.Fun, "new") {
					if len(x.Args) > 0 && holdsSession(x.Args[0]) {
						bad = append(bad, c.pos(x.Pos())+": "+types.ExprString(x)+" builds a container of sessions")
					}
				}
			case *ast.ArrayType, *ast.MapType, *ast.ChanType:
				if holdsSession(x.(ast.Expr)) {
					bad = append(bad, c.pos(x.Pos())+": "+types.ExprString(x.(ast.Expr))+" is a container of sessions")
				}
				return false
			}
			return true
		})
	}
	return bad
}

func (c *leaseCensus) structStores(owner string, st *ast.StructType) []string {
	var bad []string
	for _, field := range st.Fields.List {
		if !holdsSession(field.Type) {
			continue
		}
		names := make([]string, 0, len(field.Names))
		for _, n := range field.Names {
			names = append(names, n.Name)
		}
		if len(names) == 0 {
			names = append(names, types.ExprString(field.Type))
		}
		for _, n := range names {
			if key := owner + "." + n; !slices.Contains(sessionStores, key) {
				bad = append(bad, c.pos(field.Pos())+": "+key+" stores a session")
			}
		}
	}
	return bad
}

func (c *leaseCensus) noWidening() []string {
	var bad []string
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeAssertExpr:
				if x.Type != nil && isSession(x.Type) {
					bad = append(bad, c.pos(x.Pos())+": asserts to "+types.ExprString(x.Type))
				}
			case *ast.TypeSwitchStmt:
				for _, s := range x.Body.List {
					for _, e := range s.(*ast.CaseClause).List {
						if isSession(e) {
							bad = append(bad, c.pos(e.Pos())+": a type switch case names "+types.ExprString(e))
						}
					}
				}
			case *ast.CallExpr:
				if len(x.Args) == 1 && isSession(x.Fun) && !isIdent(x.Args[0], "nil") {
					bad = append(bad, c.pos(x.Pos())+": converts to "+types.ExprString(x.Fun))
				}
			}
			return true
		})
	}
	return bad
}

// sessionParam is the name of fn's first session-typed parameter, "" when it has none.
func sessionParam(ft *ast.FuncType) string {
	for _, p := range ft.Params.List {
		if isSession(p.Type) {
			for _, n := range p.Names {
				return n.Name
			}
		}
	}
	return ""
}

// escapes lists every use of the session parameter name in body other than as a call's receiver or a call's
// argument: an assignment, a return, a send, a composite literal, a method value, a capture by a nested function
// literal, a go or defer statement, or an append.
func (c *leaseCensus) escapes(where, name string, body *ast.BlockStmt) []string {
	var bad []string
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			if why := escapeOf(stack, id); why != "" {
				bad = append(bad, c.pos(id.Pos())+": "+where+" lets the leased session "+name+" escape: "+why)
			}
		}
		stack = append(stack, n)
		return true
	})
	return bad
}

func escapeOf(stack []ast.Node, id *ast.Ident) string {
	for _, n := range stack {
		switch n.(type) {
		case *ast.FuncLit:
			return "captured by a nested function"
		case *ast.GoStmt:
			return "handed to a go statement"
		case *ast.DeferStmt:
			return "handed to a defer statement"
		}
	}
	parent := stack[len(stack)-1]
	switch p := parent.(type) {
	case *ast.SelectorExpr:
		if len(stack) >= 2 {
			if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == p {
				return ""
			}
		}
		return "a method value"
	case *ast.CallExpr:
		if isIdent(p.Fun, "append") {
			return "appended"
		}
		if slices.Contains(p.Args, ast.Expr(id)) {
			return ""
		}
		return "called as a function"
	case *ast.AssignStmt:
		return "assigned"
	case *ast.ReturnStmt:
		return "returned"
	case *ast.SendStmt:
		return "sent on a channel"
	case *ast.CompositeLit, *ast.KeyValueExpr:
		return "stored in a composite literal"
	case *ast.ValueSpec:
		return "assigned"
	}
	return "used as a value"
}

func (c *leaseCensus) noEscape() (bad []string, callbacks int) {
	for _, f := range c.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			where := qualifiedName(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !slices.Contains(leaseCallers, sel.Sel.Name) {
					return true
				}
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.FuncLit)
					if !ok {
						continue
					}
					if name := sessionParam(lit.Type); name != "" && name != "_" {
						callbacks++
						bad = append(bad, c.escapes(where, name, lit.Body)...)
					}
				}
				return true
			})
		}
	}
	return bad, callbacks
}

func (c *leaseCensus) sessionTaking() []string {
	var bad []string
	seen := map[string]bool{}
	for _, f := range c.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := sessionParam(fn.Type)
			if name == "" {
				continue
			}
			key := qualifiedName(fn)
			seen[key] = true
			role, listed := sessionTakers[key]
			if !listed {
				bad = append(bad, c.pos(fn.Pos())+": "+key+" takes a session and is not listed in sessionTakers")
				continue
			}
			if role == "lease helper" && fn.Body != nil && name != "_" {
				bad = append(bad, c.escapes(key, name, fn.Body)...)
			}
		}
	}
	for key := range sessionTakers {
		if !seen[key] {
			bad = append(bad, "sessionTakers lists "+key+", which takes no session")
		}
	}
	return bad
}

func recordSessionCallers(fset *token.FileSet, files []*ast.File) map[string]int {
	out := map[string]int{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RecordSession" {
						out[qualifiedName(fn)]++
					}
				}
				return true
			})
		}
	}
	_ = fset
	return out
}

func parseCommandSources(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()
	names, err := filepath.Glob(filepath.Join("..", "command", "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, rErr := os.ReadFile(name)
		if rErr != nil {
			t.Fatalf("read %s: %v", name, rErr)
		}
		f, pErr := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if pErr != nil {
			t.Fatalf("parse %s: %v", name, pErr)
		}
		files = append(files, f)
	}
	if len(files) < commandFileFloor {
		t.Fatalf("parsed %d command files, want at least %d", len(files), commandFileFloor)
	}
	return files
}

func recordWriterViolations(pkg string, callers map[string]int) []string {
	var bad []string
	for fn := range callers {
		if !slices.Contains(recordSessionWriters[pkg], fn) {
			bad = append(bad, pkg+": "+fn+" writes a chat record's session id")
		}
	}
	for _, fn := range recordSessionWriters[pkg] {
		if callers[fn] == 0 {
			bad = append(bad, pkg+": recordSessionWriters lists "+fn+", which writes no session id")
		}
	}
	return bad
}

// seamSessions lists every method of seam returning a type, other than command.Bridge, with Call, PromptAt or Close.
func seamSessions(seam reflect.Type) []string {
	entry := reflect.TypeFor[command.Bridge]()
	var bad []string
	for m := range seam.Methods() {
		for out := range m.Type.Outs() {
			if out == entry {
				continue
			}
			for _, verb := range []string{"Call", "PromptAt", "Close"} {
				if _, ok := out.MethodByName(verb); ok {
					bad = append(bad, seam.String()+"."+m.Name+" returns "+out.String()+", which has "+verb)
					break
				}
			}
		}
	}
	return bad
}

func reportAll(t *testing.T, bad []string) {
	t.Helper()
	for _, b := range bad {
		t.Error(b)
	}
}

func wantCaught(t *testing.T, bad []string, want string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(b, want) {
			return
		}
	}
	t.Errorf("the census missed the fixture's violation %q; it reported %v", want, bad)
}

func TestSessionLease_TheSessionFieldIsReadOnlyInTheLeaseFile(t *testing.T) {
	reportAll(t, newLeaseCensus(t, nil).fieldOwner())
	t.Run("a read in another file fails", func(t *testing.T) {
		c := newLeaseCensus(t, map[string]string{
			"bridge_state_fixture.go": "package agent\nfunc (sb *sharedBridge) peek() any { return sb." + leaseField + " }\n",
		})
		wantCaught(t, c.fieldOwner(), "bridge_state_fixture.go:2")
	})
}

func TestSessionLease_EveryLeaseMethodIsClassified(t *testing.T) {
	reportAll(t, newLeaseCensus(t, nil).classified())
	t.Run("an unclassified method fails", func(t *testing.T) {
		c := newLeaseCensus(t, map[string]string{
			leaseFile: "package agent\nfunc (sb *sharedBridge) borrow() {}\n",
		})
		wantCaught(t, c.classified(), "sharedBridge.borrow is not classified")
	})
}

func TestSessionLease_OneConstructorBuildsEveryEntry(t *testing.T) {
	reportAll(t, newLeaseCensus(t, nil).oneConstructor())
	t.Run("a literal in the bridge manager fails", func(t *testing.T) {
		c := newLeaseCensus(t, map[string]string{
			"bridge_manager.go": "package agent\nfunc (bm *bridgeManager) mint() *sharedBridge { return &sharedBridge{state: bridgeStarting} }\n",
		})
		wantCaught(t, c.oneConstructor(), "bridgeManager.mint builds a sharedBridge literal")
	})
}

func TestSessionLease_TheSessionTypesAreTheListedInterfaces(t *testing.T) {
	c := newLeaseCensus(t, nil)
	got := c.derivedSessionTypes()
	want := slices.Sorted(slices.Values(append(slices.Clone(sessionTypes), engineLaneTypes...)))
	if !slices.Equal(got, want) {
		t.Errorf("interfaces with an engine call = %v, want sessionTypes plus engineLaneTypes %v", got, want)
	}
	if slices.Contains(got, "pushService") {
		t.Errorf("pushService's Close() was matched by name, want it told apart by signature")
	}
	t.Run("a new interface with a session call fails", func(t *testing.T) {
		c := newLeaseCensus(t, map[string]string{
			"deps_fixture.go": "package agent\nimport \"context\"\ntype modelSwapper interface{ SetModel(ctx context.Context, modelID string) error }\n",
		})
		if got := c.derivedSessionTypes(); !slices.Contains(got, "modelSwapper") {
			t.Errorf("derived %v, want modelSwapper found by its SetModel", got)
		}
	})
}

func TestSessionLease_NoSessionIsStoredOutsideTheDecorators(t *testing.T) {
	reportAll(t, newLeaseCensus(t, nil).noStore())
	for name, src := range map[string]string{
		"a struct field":  "type parked struct{ s engineSession }",
		"a map value":     "var parkedByChat map[string]acpCaller",
		"a slice element": "func f() { _ = make([]*bridge.Session, 0) }",
		"a channel":       "type handoff struct{ ch chan renamableSession }",
	} {
		t.Run(name+" fails", func(t *testing.T) {
			c := newLeaseCensus(t, map[string]string{"store_fixture.go": "package agent\n" + src + "\n"})
			wantCaught(t, c.noStore(), "store_fixture.go:2")
		})
	}
}

func TestSessionLease_NoAssertionWidensAResponder(t *testing.T) {
	reportAll(t, newLeaseCensus(t, nil).noWidening())
	for name, src := range map[string]string{
		"an assertion":  "func f(r acpResponder) { _ = r.(acpCaller) }",
		"a type switch": "func f(r acpResponder) { switch r.(type) { case engineSession: } }",
		"a conversion":  "func f(r newSessionFacts) { _ = acpCaller(r) }",
	} {
		t.Run(name+" fails", func(t *testing.T) {
			c := newLeaseCensus(t, map[string]string{"widen_fixture.go": "package agent\n" + src + "\n"})
			wantCaught(t, c.noWidening(), "widen_fixture.go:2")
		})
	}
}

func TestSessionLease_ALeasedSessionDoesNotEscapeItsCall(t *testing.T) {
	bad, callbacks := newLeaseCensus(t, nil).noEscape()
	reportAll(t, bad)
	if callbacks < leaseCallbackFloor {
		t.Errorf("found %d lease callbacks, want at least %d: the census is reading the wrong calls", callbacks, leaseCallbackFloor)
	}
	for name, body := range map[string]string{
		"an assignment":  "kept = s; return nil",
		"a return":       "return s.(error)",
		"a send":         "ch <- s; return nil",
		"a capture":      "go func() { _ = s.SessionID() }(); return nil",
		"a composite":    "_ = []any{s}; return nil",
		"a method value": "f := s.SessionID; _ = f; return nil",
		"a go statement": "go keep(s); return nil",
	} {
		t.Run(name+" fails", func(t *testing.T) {
			src := "package agent\nimport \"context\"\nvar kept any\nvar ch chan any\nfunc keep(any) {}\n" +
				"func (sb *sharedBridge) leak(ctx context.Context) error {\n" +
				"return sb.use(ctx, func(ctx context.Context, s engineSession) error {\n" + body + "\n})\n}\n"
			c := newLeaseCensus(t, map[string]string{"escape_fixture.go": src})
			bad, _ := c.noEscape()
			wantCaught(t, bad, "escape_fixture.go:8")
		})
	}
}

// A parse that finds fewer lease callbacks is reading the wrong calls. 13 measured.
const leaseCallbackFloor = 10

func TestSessionLease_EverySessionTakingFunctionIsListed(t *testing.T) {
	reportAll(t, newLeaseCensus(t, nil).sessionTaking())
	for name, param := range map[string]string{
		"an engineSession":    "engineSession",
		"an acpCaller":        "acpCaller",
		"an acpSessionCaller": "acpSessionCaller",
		"a renamableSession":  "renamableSession",
		"a newSessionFacts":   "newSessionFacts",
		"a bridge.Session":    "*bridge.Session",
	} {
		t.Run("a new function taking "+name+" fails", func(t *testing.T) {
			c := newLeaseCensus(t, map[string]string{
				"taker_fixture.go": "package agent\nfunc stash(s " + param + ") {}\n",
			})
			wantCaught(t, c.sessionTaking(), "stash takes a session and is not listed")
		})
	}
	t.Run("a lease helper that keeps its session fails", func(t *testing.T) {
		sessionTakers["stashHelper"] = "lease helper"
		t.Cleanup(func() { delete(sessionTakers, "stashHelper") })
		c := newLeaseCensus(t, map[string]string{
			"taker_fixture.go": "package agent\nvar kept any\nfunc stashHelper(s engineSession) {\nkept = s\n}\n",
		})
		wantCaught(t, c.sessionTaking(), "taker_fixture.go:4")
	})
}

func TestSessionLease_OnlyTheListedFunctionsWriteTheSessionID(t *testing.T) {
	fset := token.NewFileSet()
	reportAll(t, recordWriterViolations("agent", recordSessionCallers(fset, parseAgentSources(t, fset))))
	reportAll(t, recordWriterViolations("command", recordSessionCallers(fset, parseCommandSources(t, fset))))
	t.Run("another writer fails", func(t *testing.T) {
		f, err := parser.ParseFile(fset, "prompt.go",
			"package command\nfunc refresh(c interface{ RecordSession(string) }) { c.RecordSession(\"\") }\n", 0)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		wantCaught(t, recordWriterViolations("command", recordSessionCallers(fset, []*ast.File{f})), "refresh writes")
	})
}

func TestSessionLease_TheCommandSeamReturnsNoSession(t *testing.T) {
	reportAll(t, seamSessions(reflect.TypeFor[command.Bridge]()))
	reportAll(t, seamSessions(reflect.TypeFor[bridgeRole]()))
	t.Run("a seam returning a session fails", func(t *testing.T) {
		type leakySeam interface{ Session() engineSession }
		if bad := seamSessions(reflect.TypeFor[leakySeam]()); len(bad) == 0 {
			t.Errorf("a seam method returning engineSession passed the census")
		}
	})
}
