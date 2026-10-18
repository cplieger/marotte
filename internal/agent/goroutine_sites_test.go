package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// unheldSites is every time.AfterFunc and go statement in this package's non-test code, by enclosing function, with
// why its body may run off inflight and past the drain gate. Anything else goes through the lifetime's gate
// (afterUnlessDraining, goUnlessDraining, goClaimed) or inflight.Go from a caller already held.
var unheldSites = map[string]struct {
	why   string
	count int
}{
	"runtime.go (lifetime).afterUnlessDraining":     {"the gate itself, and the bare arm of a fixture with no lifetime", 2},
	"runtime.go awaitBounded":                       {"it only waits", 1},
	"run_asks.go (Runs).armTimersLocked":            {"kickCell marks memory and starts its work through goUnlessDraining", 1},
	"run_asks.go (Runs).armRequeryLocked":           {"kickCell, as armTimersLocked", 1},
	"bridge_state.go (sharedBridge).ArmCancelGrace": {"it cancels a prompt context; the held prompt goroutine closes", 1},
	"turn_reap.go (bridgeCoordinator).armCompactionReapLocked": {
		"it claims the turn's cause and cancels that turn's prompt context; the held prompt goroutine closes", 1,
	},
	"agent_terminal.go (agentTerminals).awaitExit":      {"a process-group observation its caller joins", 1},
	"turn_finalize.go (bridgeCoordinator).publishClose": {"a fixture with no lifetime", 1},
	"bridge_coord.go (bridgeCoordinator).Notify":        {"a push once draining, which push.Close in Shutdown owns", 1},
	"engine_calls.go (engineOwner).observe":             {"drainSession only answers its pass's requests", 1},
	"engine_owner.go (engineOwner).backoff":             {"it only waits", 1},
	"engine_lane.go (Runtime).observerForward": {
		"a frame pump ending with its pass's session; a request's work goes through answerUnlessDraining", 1,
	},
	"run_hosts.go newHostRelay": {"a frame pump ending with its session; early answers through answerUnlessDraining", 1},
}

func TestUnheldSites_AreOnlyTheListedOnes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package dir: %v", err)
	}
	got := map[string]int{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := name + " " + funcKey(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.GoStmt:
					got[key]++
				case *ast.CallExpr:
					if isTimeAfterFunc(n) {
						got[key]++
					}
				}
				return true
			})
		}
	}
	if len(got) < 5 {
		t.Fatalf("the scan found %d sites; it reads the package, so a set this small means the scan broke", len(got))
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		want, listed := unheldSites[k]
		switch {
		case !listed:
			t.Errorf("%s starts %d unheld timer or goroutine bodies: run them through the lifetime's gate "+
				"(afterUnlessDraining, goUnlessDraining) so Shutdown waits for them and refuses them once draining, "+
				"or list the site with why it may run unheld", k, got[k])
		case got[k] != want.count:
			t.Errorf("%s starts %d unheld bodies, want %d; a new one needs the gate or its own reason", k, got[k], want.count)
		}
	}
	for k := range unheldSites {
		if got[k] == 0 {
			t.Errorf("listed site %s starts no unheld body any more; delete its entry", k)
		}
	}
}

func funcKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return "(" + id.Name + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

func isTimeAfterFunc(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "AfterFunc" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}
