package testsupport

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCannedRoutes_EveryNullRouteIsHeld fails a route tests/perf/canned-routes.json maps to null, which the client
// test decodes with nothing, unless a Go test holds its answers to what the server writes.
func TestCannedRoutes_EveryNullRouteIsHeld(t *testing.T) {
	root := RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "tests", "perf", "canned-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var routes map[string]json.RawMessage
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	held := heldRoutes(t, filepath.Join(root, "internal"))
	for route, decoder := range routes {
		if string(decoder) != "null" {
			continue
		}
		if !held[route] {
			t.Errorf("canned-routes.json maps %s to null and no Go test holds its answers: hold them with "+
				"testsupport.HoldCannedShape in the handler's package, or name a generated decoder; %s", route, CannedRerecord)
		}
	}
	for route := range held {
		if _, ok := routes[route]; !ok {
			t.Errorf("a Go test holds %s, which canned-routes.json does not name", route)
		}
	}
}

// heldRoutes is every route literal passed to HoldCanned or HoldCannedShape in a _test.go file under dir.
func heldRoutes(t *testing.T, dir string) map[string]bool {
	t.Helper()
	held := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 || !holdsCanned(call.Fun) {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: a canned route is held by a string literal, so this test can find it", fset.Position(call.Pos()))
				return true
			}
			route, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: %v", fset.Position(lit.Pos()), err)
				return true
			}
			held[route] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func holdsCanned(fun ast.Expr) bool {
	if ix, ok := fun.(*ast.IndexExpr); ok {
		fun = ix.X
	}
	name := ""
	switch f := fun.(type) {
	case *ast.Ident:
		name = f.Name
	case *ast.SelectorExpr:
		name = f.Sel.Name
	}
	return name == "HoldCanned" || name == "HoldCannedShape"
}

// The page's own reports answer 204 with no body: their handlers write nothing to hold a shape against.
func TestCanned_ReportsAnswerNoContent(t *testing.T) {
	noContent := func(a *CannedAnswer) error {
		if a.Status != 204 || (len(a.Body) != 0 && string(a.Body) != "null") {
			return fmt.Errorf("answered %d with %s, and the route answers 204 with no body", a.Status, clip(a.Body))
		}
		return nil
	}
	HoldCanned(t, "POST /api/events/view", noContent)
	HoldCanned(t, "POST /api/events/notice", noContent)
	HoldCanned(t, "POST /api/forges/inventory/watch", noContent)
}

func TestReadsBackAs(t *testing.T) {
	type shape struct {
		Name  string `json:"name"`
		Count int    `json:"count,omitempty"`
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "same", body: `{"name":"a","count":2}`},
		{name: "omitted empty", body: `{"name":"a"}`},
		{name: "renamed field", body: `{"title":"a"}`, want: "unknown field"},
		{name: "dropped field", body: `{"count":2}`, want: "is not what"},
		{name: "no body", body: ``, want: "no body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v shape
			err := ReadsBackAs(json.RawMessage(c.body), &v)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("ReadsBackAs(%s) = %v, want nil", c.body, err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("ReadsBackAs(%s) = %v, want an error containing %q", c.body, err, c.want)
			}
		})
	}
}
