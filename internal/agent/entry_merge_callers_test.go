package agent

// The rewritten order is ONE function's answer, and this is what says so.
//
// Under R2 a turn's ordinal is reused after a rewind (the surviving high-water plus
// one), so two turns in one log can carry the same n and the log's order is FIRST
// APPEARANCE — nothing sorts it. EntryLog.Rewrite is what lays that order down on
// disk, and SwapMerged is the one function that decides it. A second production
// caller would be a second decider: a writer laying down an order MergeEntries never
// produced, against a revert gate the swap already read and it did not.
//
// A census rather than a comment, because the property is an ABSENCE — the next
// caller is the one nobody wrote yet, so no list can see it coming.

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

// Directories with no Go the walk needs, or none at all: the bundled front end, its
// dependency tree, the vitest cache, and the fixtures a test reads as data.
var callerSkipDirs = map[string]bool{
	".git":         true,
	".vitest":      true,
	"node_modules": true,
	"static":       true,
	"static-src":   true,
	"testdata":     true,
}

// A walk that stops finding files must fail rather than report a clean census. 406
// non-test files measured; the floor is low enough that a package moving costs
// nothing and high enough that a walker rooted at the wrong directory is caught.
const callerFileFloor = 300

// rewriteSite is one production call of a method named Rewrite.
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

	const wantFile, wantFn = "internal/agent/entry_merge.go", "SwapMerged"
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
