package marotte

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// specVersionWrites names each function in the module's non-test Go code that assigns, increments, takes the address
// of or sets in a literal a field called SpecVersion, as "<path>:<func>".
func specVersionWrites(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			owner := "<package level>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if writesSpecVersion(n) {
					out = append(out, filepath.ToSlash(rel)+":"+owner)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	return out
}

func writesSpecVersion(n ast.Node) bool {
	isField := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "SpecVersion"
	}
	switch n := n.(type) {
	case *ast.AssignStmt:
		return slices.ContainsFunc(n.Lhs, isField)
	case *ast.IncDecStmt:
		return isField(n.X)
	case *ast.UnaryExpr:
		return n.Op == token.AND && isField(n.X)
	case *ast.KeyValueExpr:
		key, ok := n.Key.(*ast.Ident)
		return ok && key.Name == "SpecVersion"
	}
	return false
}

// TestBotSpecVersion_OnlyBotWriterWritesIt pins the save's base check: a write of a Bot's view that moved
// SpecVersion any other way could set a version an earlier write already published.
func TestBotSpecVersion_OnlyBotWriterWritesIt(t *testing.T) {
	writes := specVersionWrites(t, filepath.Join("..", ".."))

	if len(writes) == 0 {
		t.Fatal("found no write of SpecVersion, want BotWriter's: the scan reads nothing")
	}
	for _, w := range writes {
		if w != "internal/marotte/domain_bot.go:BotWriter" {
			t.Errorf("%s writes SpecVersion, want only BotWriter to", w)
		}
	}
}
