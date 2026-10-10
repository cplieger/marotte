package agent

// swapMerged is the one production caller of EntryLog.Rewrite: a second would lay down an order
// mergeEntries never produced, past a revert gate it never read. A census, because the next
// caller is one nobody has written yet.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Directories the walk skips: the bundled front end, its dependencies, the vitest cache and test fixtures.
var callerSkipDirs = map[string]bool{
	".git":         true,
	".vitest":      true,
	"node_modules": true,
	"static":       true,
	"static-src":   true,
	"testdata":     true,
}

// A walk that stops finding files must fail. 406 measured; the floor catches a wrongly rooted walker.
const callerFileFloor = 300

type rewriteSite struct {
	file string
	line int
	fn   string
}

func (s rewriteSite) String() string { return fmt.Sprintf("%s:%d in %s", s.file, s.line, s.fn) }

func TestSwapMerged_RewriteHasOneProductionCaller(t *testing.T) {
	root := moduleRoot(t)
	var sites []rewriteSite
	files := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if callerSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Rewrite" {
					return true
				}
				sites = append(sites, rewriteSite{
					file: filepath.ToSlash(rel),
					line: fset.Position(call.Pos()).Line,
					fn:   fn.Name.Name,
				})
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if files < callerFileFloor {
		t.Fatalf("the census read %d production Go files under %s, want at least %d: a walk "+
			"that finds nothing reports a clean census whatever the tree holds",
			files, root, callerFileFloor)
	}

	const wantFile, wantFn = "internal/agent/entry_merge.go", "swapMerged"
	if len(sites) != 1 {
		t.Fatalf("Rewrite is called from %d production sites %v, want exactly 1 (%s in %s): a "+
			"second caller lays down a turn order MergeEntries did not produce, and under R2's "+
			"ordinal reuse the log's order is first appearance, so nothing downstream can tell "+
			"which writer decided it",
			len(sites), sites, wantFn, wantFile)
	}
	if got := sites[0]; got.file != wantFile || got.fn != wantFn {
		t.Errorf("Rewrite's one caller is %s, want %s in %s: the rewritten order is decided "+
			"outside the swap that read the revert gate", got, wantFn, wantFile)
	}
}
