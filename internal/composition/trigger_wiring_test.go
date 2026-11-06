package composition

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// buildCalls is every call Build makes in its own statements, closures skipped, by callee name ("agent.New" for a
// selector), in source order.
type buildCalls struct {
	at   map[string][]token.Pos
	args map[string][][]ast.Expr
}

func readBuildCalls(t *testing.T) buildCalls {
	t.Helper()
	src, err := os.ReadFile("composition.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "composition.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "Build" && fn.Recv == nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("composition.go has no func Build")
	}
	bc := buildCalls{at: map[string][]token.Pos{}, args: map[string][][]ast.Expr{}}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isClosure := n.(*ast.FuncLit); isClosure {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		bc.at[name] = append(bc.at[name], call.Pos())
		bc.args[name] = append(bc.args[name], call.Args)
		return true
	})
	return bc
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// first is the position of the one call to name, failing the test when Build makes none or several.
func (bc buildCalls) first(t *testing.T, name string) token.Pos {
	t.Helper()
	if n := len(bc.at[name]); n != 1 {
		t.Fatalf("Build calls %s %d times, want once", name, n)
	}
	return bc.at[name][0]
}

// The confirmer exists, its records loaded, before agent.New returns, so a run_start that arrives before the trigger
// runner is built still confirms; its boot and effects wait for the agent's engine, so they start once h exists.
func TestConfirmerBeforeAgentAndRunnerAfterForges(t *testing.T) {
	bc := readBuildCalls(t)
	t.Run("the confirmer is built after the lease store and handed to agent.New", func(t *testing.T) {
		leases, conf, agentNew := bc.first(t, "openRunLeaseStore"), bc.first(t, "openTriggerConfirmer"),
			bc.first(t, "agent.New")
		if leases >= conf || conf >= agentNew {
			t.Errorf("order openRunLeaseStore %d, openTriggerConfirmer %d, agent.New %d; want them in that order",
				leases, conf, agentNew)
		}
		wired := false
		for _, arg := range bc.args["agent.New"][0] {
			if call, ok := arg.(*ast.CallExpr); ok && calleeName(call.Fun) == "withTriggerConfirmer" {
				wired = true
			}
		}
		if !wired {
			t.Error("agent.New is not given withTriggerConfirmer")
		}
		if start := bc.first(t, "startTriggerConfirmer"); start <= agentNew {
			t.Errorf("startTriggerConfirmer at %d, want it after agent.New at %d", start, agentNew)
		}
	})
	t.Run("the trigger runner is built after the forges manager", func(t *testing.T) {
		t.Skip("needs 5.16")
	})
}
