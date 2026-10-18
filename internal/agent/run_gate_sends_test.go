package agent

// Run work written through Call, CallAt or Notify on an engine or an engine-opened session passes guardRunWork,
// which refuses it unless an admission is bound to its context. The behaviour test pins the guard on all six; the
// census pins what the guard leans on and the source can prove: only admitRunWork mints an admission (a literal
// with fields, or a conversion), only runAdmission.bind and the guard read the context key, only engineOwner.spawn
// builds an engine, and every workflow verb is classified. Owners are matched by receiver and name.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// stepSteerParams is a struct, so the guard reads a session id through JSON as well as from a map.
type stepSteerParams struct {
	SessionID marotte.SessionID `json:"sessionId"`
	Message   string            `json:"message"`
}

// Each request is sent through a method held in a local, the shape a source scan cannot follow.
func TestRunWork_AnUnadmittedSendIsRefusedAtTheEngine(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	eng, _ := h.engine.current()
	if _, ok := eng.(guardedEngine); !ok {
		t.Fatalf("the runtime's engine is %T, want it guarded", eng)
	}
	sess, err := h.engine.openSession(t.Context(), &marotte.SessionOpts{}, nil)
	if err != nil {
		t.Fatalf("Setup: openSession: %v", err)
	}
	if _, ok := sess.(guardedSession); !ok {
		t.Fatalf("an engine-opened session is %T, want it guarded", sess)
	}
	own := sess.SessionID()
	type caller func(ctx context.Context, method string, params any) error
	callEngine := func(ctx context.Context, m string, p any) error { _, err := eng.Call(ctx, m, p); return err }
	callAtEngine := func(ctx context.Context, m string, p any) error { _, _, err := eng.CallAt(ctx, m, p); return err }
	callSession := func(ctx context.Context, m string, p any) error { _, err := sess.Call(ctx, m, p); return err }
	callAtSession := func(ctx context.Context, m string, p any) error { _, _, err := sess.CallAt(ctx, m, p); return err }
	notifyEngine := func(ctx context.Context, m string, p any) error { return eng.Notify(ctx, m, p) }
	notifySession := func(ctx context.Context, m string, p any) error { return sess.Notify(ctx, m, p) }
	wf := map[string]any{keyWorkflowID: "wf_1"}
	step := map[string]any{marotte.KeySessionID: "sess_step"}
	runWork := []struct {
		send   caller
		params any
		name   string
		method string
	}{
		{name: "invoke on the engine lane", send: callEngine, method: methodKiroWorkflowInvoke, params: wf},
		{name: "resume on a session", send: callSession, method: methodKiroWorkflowResume, params: wf},
		{name: "retry on a session, at its lane position", send: callAtSession, method: methodKiroWorkflowRetry, params: wf},
		{name: "update on the engine lane", send: callAtEngine, method: methodKiroWorkflowUpdate, params: wf},
		{name: "a resume notification on the engine lane", send: notifyEngine, method: methodKiroWorkflowResume, params: wf},
		{name: "a resume notification on a session", send: notifySession, method: methodKiroWorkflowResume, params: wf},
		{name: "a prompt to a step's session", send: callSession, method: marotte.MethodPrompt, params: step},
		{
			name: "a steer to a step's session, in a struct", send: callAtEngine, method: marotte.MethodSessionSteer,
			params: stepSteerParams{SessionID: "sess_step", Message: "x"},
		},
		{
			name: "a steer naming no session", send: callSession, method: marotte.MethodSessionSteer,
			params: map[string]any{"message": "x"},
		},
	}
	for _, tc := range runWork {
		t.Run(tc.name, func(t *testing.T) {
			br.forgetCalls()
			method := tc.method
			notifiedBefore := len(br.notified(method))
			if err := tc.send(t.Context(), method, tc.params); !errors.Is(err, errRunWorkUnadmitted) {
				t.Errorf("unadmitted send = %v, want errRunWorkUnadmitted", err)
			}
			if calls := br.callLog(); slices.Contains(calls, method) {
				t.Errorf("the engine saw %s unadmitted: %v", method, calls)
			}
			if n := len(br.notified(method)); n != notifiedBefore {
				t.Errorf("the engine was notified of %s unadmitted (%d notifications, want %d)", method, n, notifiedBefore)
			}
			a, aErr := h.runs.admitRunWork(refuseClosed, "wf_1", "")
			if aErr != nil {
				t.Fatalf("Setup: admitRunWork on an open gate = %v", aErr)
			}
			if err := tc.send(a.bind(t.Context()), method, tc.params); errors.Is(err, errRunWorkUnadmitted) {
				t.Errorf("admitted send = %v, want it written", err)
			}
		})
	}
	notRunWork := []struct {
		params any
		name   string
		method string
	}{
		{
			name: "a chat's prompt to its own session", method: marotte.MethodPrompt,
			params: map[string]any{marotte.KeySessionID: own},
		},
		{
			name: "a chat's steer to its own session", method: marotte.MethodSessionSteer,
			params: map[string]any{marotte.KeySessionID: string(own), "message": "x"},
		},
		{name: "a pause", method: methodKiroWorkflowPause, params: wf},
	}
	for _, tc := range notRunWork {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sess.Call(t.Context(), tc.method, tc.params); errors.Is(err, errRunWorkUnadmitted) {
				t.Errorf("send = %v, want it written unadmitted", err)
			}
		})
	}
}

// knownNotRunWork are the `_kiro/workflow/*` verbs that make no step do model work; every other one must be in
// runWorkVerbs, so a new verb is classified before it is sent.
var knownNotRunWork = []string{
	"methodKiroWorkflowList", "methodKiroWorkflowInspect", "methodKiroWorkflowListRecipes", "methodKiroWorkflowNew",
	"methodKiroWorkflowCancel", "methodKiroWorkflowDelete", "methodKiroWorkflowPause", "methodKiroWorkflowRecipesChanged",
}

// A parse that finds no source must fail. 123 measured; the floor catches a wrongly rooted parse.
const gateSendFileFloor = 100

// guardOwners are the only functions, receiver-qualified, that may name each token the guard's proof rests on.
var guardOwners = map[string][]string{
	"runAdmission{...}": {"Runs.admitRunWork"},
	"runAdmission(...)": {"Runs.admitRunWork"},
	"admissionKey":      {"runAdmission.bind", "guardRunWork"},
	"guardedEngine{":    {"engineOwner.spawn"},
	"bridge.NewEngine":  {"engineOwner.spawn"},
	"spawnEngine":       {"engineOwner.spawn"},
}

func TestRunWork_TheGuardsProofIsMintedInOnePlace(t *testing.T) {
	fset := token.NewFileSet()
	files := parseAgentSources(t, fset)
	runWorkVerbConsts := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.GenDecl); ok {
				collectRunWorkVerbs(d, runWorkVerbConsts)
			}
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				checkVerbsClassified(t, d, runWorkVerbConsts)
			case *ast.FuncDecl:
				checkGuardTokens(t, fset, d)
			}
		}
	}
	if !runWorkVerbConsts["methodKiroWorkflowResume"] {
		t.Errorf("runWorkVerbs lists %v, want the run-work verbs: the census is reading the wrong map", runWorkVerbConsts)
	}
}

// collectRunWorkVerbs records the constants keying runWorkVerbs' literal.
func collectRunWorkVerbs(d *ast.GenDecl, into map[string]bool) {
	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "runWorkVerbs" || len(vs.Values) != 1 {
			continue
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					into[id.Name] = true
				}
			}
		}
	}
}

func checkVerbsClassified(t *testing.T, d *ast.GenDecl, runWork map[string]bool) {
	t.Helper()
	if d.Tok != token.CONST {
		return
	}
	for _, spec := range d.Specs {
		for _, name := range spec.(*ast.ValueSpec).Names {
			n := name.Name
			if strings.HasPrefix(n, "methodKiroWorkflow") && !runWork[n] && !slices.Contains(knownNotRunWork, n) {
				t.Errorf("%s is not classified: add it to runWorkVerbs, or to knownNotRunWork if it starts no model work", n)
			}
		}
	}
}

// checkGuardTokens fails each use, in fn, of a token guardOwners does not give fn: a runAdmission literal with
// fields or a conversion to runAdmission, a write to its fields, the admission context key, or an engine's
// construction.
func checkGuardTokens(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) {
	t.Helper()
	if fn.Body == nil {
		return
	}
	name := qualifiedName(fn)
	use := func(tok string, pos token.Pos) {
		if !slices.Contains(guardOwners[tok], name) {
			t.Errorf("%s: %s names %s, which only %v may", fset.Position(pos), name, tok, guardOwners[tok])
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if isIdent(ast.Unparen(x.Fun), "runAdmission") {
				use("runAdmission(...)", x.Pos())
			}
		case *ast.CompositeLit:
			if isIdent(x.Type, "runAdmission") && len(x.Elts) > 0 {
				use("runAdmission{...}", x.Pos())
			}
			if isIdent(x.Type, "guardedEngine") {
				use("guardedEngine{", x.Pos())
			}
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				if isAdmissionField(lhs) {
					t.Errorf("%s: %s writes a runAdmission field; only admitRunWork mints one", fset.Position(lhs.Pos()), name)
				}
			}
		case *ast.UnaryExpr:
			if x.Op == token.AND && isAdmissionField(x.X) {
				t.Errorf("%s: %s takes a runAdmission field's address", fset.Position(x.Pos()), name)
			}
		case *ast.Ident:
			if x.Name == "admissionKey" {
				use("admissionKey", x.Pos())
			}
		case *ast.SelectorExpr:
			if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "bridge" && x.Sel.Name == "NewEngine" {
				use("bridge.NewEngine", x.Pos())
			}
			if x.Sel.Name == "spawnEngine" {
				use("spawnEngine", x.Pos())
			}
		}
		return true
	})
}

// qualifiedName is fn's name prefixed by its receiver's type, "engineOwner.spawn" for (*engineOwner).spawn.
func qualifiedName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	switch g := typ.(type) {
	case *ast.IndexExpr:
		typ = g.X
	case *ast.IndexListExpr:
		typ = g.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func isAdmissionField(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "admittedBy" || sel.Sel.Name == "admittedIn")
}

func parseAgentSources(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()
	names, err := filepath.Glob("*.go")
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
	if len(files) < gateSendFileFloor {
		t.Fatalf("parsed %d production files, want at least %d", len(files), gateSendFileFloor)
	}
	return files
}
